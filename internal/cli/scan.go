package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/MohammadMD1383/modharbor/internal/app"
	"github.com/MohammadMD1383/modharbor/internal/hashutil"
	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/modmeta"
	"github.com/MohammadMD1383/modharbor/internal/resolver"
	"github.com/MohammadMD1383/modharbor/internal/ui"
	"github.com/spf13/cobra"
)

// ScannedMod is one row of scan output.
type ScannedMod struct {
	FileName   string   `json:"file"`
	ModID      string   `json:"modId,omitempty"`
	Title      string   `json:"title,omitempty"`
	Version    string   `json:"version,omitempty"`
	ProjectID  string   `json:"projectId,omitempty"`
	Provider   string   `json:"provider,omitempty"`
	Method     string   `json:"method"`
	Confidence float64  `json:"confidence"`
	SHA1       string   `json:"sha1"`
	Size       int64    `json:"size"`
	Loader     string   `json:"loader,omitempty"`
	Deps       []string `json:"dependencies,omitempty"`
	URL        string   `json:"url,omitempty"`
}

type scanJSON struct {
	Instance   string       `json:"instance"`
	MCVersion  string       `json:"mcVersion"`
	Loader     string       `json:"loader"`
	Total      int          `json:"total"`
	Identified int          `json:"identified"`
	Unresolved int          `json:"unresolved"`
	Mods       []ScannedMod `json:"mods"`
}

func newScanCmd() *cobra.Command {
	var (
		force   bool
		showAll bool
	)

	cmd := &cobra.Command{
		Use:   "scan [instance]",
		Short: "Identify every mod in an instance",
		Long: strings.TrimSpace(`
Identify each jar in an instance's mods directory and record what it is.

Identification is exact where possible: the file's SHA-1 is looked up against
Modrinth, which returns the precise project and version. Jars that came from a
mirror such as CurseForge have a different hash, so they are matched on their
mod id and display name instead.

Results are cached, so later commands are fast. Use --force to re-resolve.
`),
		Example: strings.TrimSpace(`
  modharbor scan 26.2-fabric-mod
  modharbor scan --all
  modharbor scan --json | jq '.mods[] | select(.confidence < 0.8)'
`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := bootstrap()
			if err != nil {
				return fail("%v", err)
			}
			inst, err := resolveInstance(a, args)
			if err != nil {
				return err
			}

			rows, err := scanInstance(cmd.Context(), a, inst, force)
			if err != nil {
				return fail("%v", err)
			}

			if flagJSON {
				return printJSON(newScanJSON(inst, rows))
			}
			renderScan(inst, rows, showAll)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "re-resolve mods even when cached")
	cmd.Flags().BoolVar(&showAll, "all", false, "include unresolved mods in the table")
	return cmd
}

// scanInstance identifies every jar in an instance and caches the results.
func scanInstance(ctx context.Context, a *app.App, inst *instance.Info, force bool) ([]ScannedMod, error) {
	st, err := a.State()
	if err != nil {
		return nil, err
	}

	files, err := instance.ModFiles(inst.ModsDirOrDefault())
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no mods found in %s", inst.ModsDirOrDefault())
	}

	tlMods, _ := instance.ParseTLauncherMods(inst.Path)

	cands := make([]resolver.Candidate, 0, len(files))
	for _, f := range files {
		base := filepathBase(f)
		c := resolver.Candidate{Path: f, FileName: base}

		sha1, err := hashutil.SHA1File(f)
		if err != nil {
			return nil, fmt.Errorf("hashing %s: %w", base, err)
		}
		c.SHA1 = sha1

		if fi, err := statFile(f); err == nil {
			c.Size = fi.Size()
		}
		if m, err := modmeta.Read(f); err == nil {
			c.Meta = m
		}
		if tm, ok := tlMods[base]; ok {
			c.CurseForgeID = tm.CurseForgeID
			c.CurseForgeName = tm.Name
		}
		cands = append(cands, c)
	}

	if force {
		for _, c := range cands {
			st.Forget(c.SHA1)
		}
	}

	scoped := resolver.New(resolver.Options{
		Modrinth: a.MR(),
		Cache:    st,
	}).ForInstance(inst.MCVersion, loaderName(inst.Type))

	if !flagOffline {
		sp := ui.NewSpinner(fmt.Sprintf("Identifying %d mods", len(cands))).Start()
		results := scoped.ResolveAll(ctx, cands)
		sp.Done("")

		rows := make([]ScannedMod, 0, len(results))
		for _, r := range results {
			rows = append(rows, toScannedMod(r))
		}
		if err := st.Save(); err != nil {
			ui.Warn("could not save cache: %v", err)
		}
		sortScanned(rows)
		return rows, nil
	}

	// Offline: report whatever the cache already knows.
	rows := make([]ScannedMod, 0, len(cands))
	for _, c := range cands {
		row := ScannedMod{
			FileName: c.FileName,
			SHA1:     c.SHA1,
			Size:     c.Size,
			Method:   resolver.MethodUnmatched,
		}
		if c.Meta != nil {
			row.ModID = c.Meta.ModID
			row.Title = c.Meta.Name
			row.Version = c.Meta.Version
			row.Loader = string(c.Meta.Loader)
			row.Deps = sortedKeys(c.Meta.DependsOn)
		}
		if prev, ok := st.Lookup(c.SHA1); ok && prev.ProjectID != "" {
			row.ProjectID = prev.ProjectID
			row.Provider = prev.Provider
			row.Method = prev.Method
			row.Confidence = prev.Confidence
			if row.Title == "" {
				row.Title = prev.Title
			}
			if row.Version == "" {
				row.Version = prev.VersionNumber
			}
		}
		rows = append(rows, row)
	}
	sortScanned(rows)
	return rows, nil
}

func toScannedMod(r resolver.Result) ScannedMod {
	res := r.Resolution
	c := r.Candidate

	row := ScannedMod{
		FileName:   c.FileName,
		ProjectID:  res.ProjectID,
		Provider:   res.Provider,
		Method:     res.Method,
		Confidence: res.Confidence,
		SHA1:       c.SHA1,
		Size:       c.Size,
		Title:      res.Title,
		Version:    res.VersionNumber,
	}
	if c.Meta != nil {
		row.ModID = c.Meta.ModID
		row.Loader = string(c.Meta.Loader)
		row.Deps = sortedKeys(c.Meta.DependsOn)
		if row.Title == "" {
			row.Title = c.Meta.Name
		}
		if row.Version == "" {
			row.Version = c.Meta.Version
		}
	}
	if row.Title == "" {
		row.Title = c.FileName
	}
	if res.ProjectID != "" {
		row.URL = "https://modrinth.com/mod/" + res.ProjectID
	}
	return row
}

func newScanJSON(inst *instance.Info, rows []ScannedMod) scanJSON {
	out := scanJSON{
		Instance:  inst.ID,
		MCVersion: inst.MCVersion,
		Loader:    string(inst.Type),
		Total:     len(rows),
		Mods:      rows,
	}
	for _, r := range rows {
		if r.Method == resolver.MethodUnmatched {
			out.Unresolved++
		} else {
			out.Identified++
		}
	}
	return out
}

func sortScanned(rows []ScannedMod) {
	sort.SliceStable(rows, func(i, j int) bool {
		// Unresolved mods float to the bottom: they need attention.
		ui1 := rows[i].Method == resolver.MethodUnmatched
		uj := rows[j].Method == resolver.MethodUnmatched
		if ui1 != uj {
			return uj
		}
		return strings.ToLower(rows[i].Title) < strings.ToLower(rows[j].Title)
	})
}

// renderScan prints a polished table of identified mods.
func renderScan(inst *instance.Info, rows []ScannedMod, showAll bool) {
	identified, unresolved := 0, 0
	for _, r := range rows {
		if r.Method == resolver.MethodUnmatched {
			unresolved++
		} else {
			identified++
		}
	}

	ui.Heading("modharbor scan", inst.Label()+"  "+ui.Path(inst.ModsDirOrDefault()))
	ui.Blank()

	tab := ui.NewTable("MOD", "VERSION", "SOURCE", "MATCH")
	tab.Align(3, ui.AlignRight)

	shown := 0
	for _, r := range rows {
		if r.Method == resolver.MethodUnmatched && !showAll {
			continue
		}
		tab.Row(
			truncateName(r.Title, 30),
			ui.Muted(r.Version),
			methodLabel(r),
			confidenceCell(r.Confidence, r.Method),
		)
		shown++
	}
	if shown > 0 {
		tab.Render()
	}

	if unresolved > 0 {
		ui.Warn("%d mod(s) could not be matched to an upstream project", unresolved)
		for _, r := range rows {
			if r.Method == resolver.MethodUnmatched {
				ui.Task("skip", truncateName(r.Title, 30),
					ui.Faint("left as-is; carried across verbatim on migrate"))
			}
		}
		ui.Blank()
		ui.Hint("pass --all to see them inline, or `modharbor link <sha1> <project>` to map one by hand")
	}

	ui.Success("identified %s of %s mods", fmt.Sprint(identified), fmt.Sprint(len(rows)))
	ui.Blank()
}

// methodLabel renders how a mod was matched.
func methodLabel(r ScannedMod) string {
	switch r.Method {
	case resolver.MethodHash:
		return ui.Muted("hash")
	case resolver.MethodSlug:
		return ui.Muted("mod id")
	case resolver.MethodCurseForge:
		return ui.Muted("curseforge id")
	case resolver.MethodName:
		return ui.Muted("name match")
	case resolver.MethodManual:
		return ui.InfoC("manual")
	default:
		return ui.Bad("unmatched")
	}
}

// confidenceCell renders a confidence score with a colour ramp.
func confidenceCell(conf float64, method string) string {
	switch {
	case method == resolver.MethodUnmatched || conf == 0:
		return ui.Bad("—")
	case conf >= 0.99:
		return ui.OK(fmt.Sprintf("%.0f%%", conf*100))
	case conf >= 0.85:
		return ui.InfoC(fmt.Sprintf("%.0f%%", conf*100))
	default:
		return ui.Warnc(fmt.Sprintf("%.0f%%", conf*100))
	}
}

func truncateName(s string, n int) string {
	if s == "" {
		return "—"
	}
	return ui.Truncate(s, n)
}

func sortedKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
