package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/modmeta"
	"github.com/MohammadMD1383/modharbor/internal/ui"
	"github.com/spf13/cobra"
)

// ─── add ────────────────────────────────────────────────────────────────────

func newAddCmd() *cobra.Command {
	var (
		channel  string
		dryRun   bool
		withDeps bool
		yes      bool
	)

	cmd := &cobra.Command{
		Use:   "add <project...> [instance]",
		Short: "Install mods from Modrinth",
		Long: strings.TrimSpace(`
Install one or more Modrinth projects into an instance.

Each project may be a slug ("sodium"), a project id ("AANobbMI") or a full
URL. Dependencies are resolved recursively unless --no-deps is passed, which is
what you want for things like Sodium that require the Sodium core library.

Only builds matching the instance's Minecraft version and loader are
considered.
`),
		Example: strings.TrimSpace(`
  modharbor add sodium
  modharbor add sodium zoomify --all
  modharbor add sodium --instance 26.3-fabric-mod
  modharbor add https://modrinth.com/mod/lithium
  modharbor add sodium --channel beta --dry-run
`),
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := bootstrap()
			if err != nil {
				return fail("%v", err)
			}
			if channel != "" {
				a.Config.Update.Channel = channel
			}
			instRef, projects := splitArgs(args)
			if len(projects) == 0 {
				return fail("no project given")
			}
			inst, err := resolveInstance(a, []string{instRef})
			if err != nil {
				return err
			}

			verbosef("instance %s  mods=%s  loader=%s  channel=%s",
				inst.ID, inst.ModsDirOrDefault(), loaderName(inst.Type), a.Channel())

			ctx := cmd.Context()
			installed, skipped, err := installProjects(ctx, a, inst, projects, withDeps, dryRun, a.Channel())
			if err != nil {
				return fail("%v", err)
			}

			if flagJSON {
				return printJSON(map[string]any{
					"instance":  inst.ID,
					"installed": len(installed),
					"skipped":   skipped,
					"dryRun":    dryRun,
				})
			}

			ui.Heading("Install", inst.Label())
			ui.Blank()
			for _, f := range installed {
				state := "add"
				if dryRun {
					state = "busy"
				}
				ui.Task(state, truncateName(f.name, 30), ui.OK(f.version)+ui.Faint("  "+f.filename))
			}
			for _, s := range skipped {
				ui.Task("skip", truncateName(s, 30), ui.Warnc("already installed"))
			}
			ui.Blank()
			if len(installed) == 0 && len(skipped) == 0 {
				ui.Warn("nothing to do")
			} else if dryRun {
				// The wording has to match what happened: nothing was written,
				// so claiming "installed" would be a lie the user acts on.
				ui.Success("would install %d mod(s)", len(installed))
			} else {
				ui.Success("installed %d mod(s)", len(installed))
			}
			if dryRun {
				ui.Note("dry run: nothing was downloaded and mods/ is unchanged")
				ui.Hint("re-run without --dry-run to install for real")
			}
			ui.Blank()
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&channel, "channel", "", "release channel: release, beta, alpha")
	f.BoolVar(&dryRun, "dry-run", false, "resolve and report without downloading")
	f.BoolVar(&withDeps, "no-deps", false, "do not install dependencies")
	f.BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	return cmd
}

// installedMod records what was fetched.
type installedMod struct {
	name     string
	version  string
	filename string
	sha1     string
	url      string
	sha512   string
	size     int64
	// dryRun marks a mod that was planned but deliberately not downloaded,
	// so the summary can say "would install" instead of claiming it happened.
	dryRun bool
}

// splitArgs separates a trailing instance reference from project arguments.
//
// The instance may be positional — `add sodium 26.3-fabric-mod` — or supplied
// with -i/--instance, and the two must resolve the same way. When no argument
// names an instance we fall back to the flag rather than to nothing; leaving it
// empty is not the same thing, because ResolveInstance would then quietly use a
// different instance than the user asked for on the command line.
func splitArgs(args []string) (instRef string, projects []string) {
	if ref, rest := positionalInstance(args); ref != "" {
		return ref, rest
	}
	return flagInstance, args
}

// positionalInstance picks an instance out of the positional arguments.
//
// An argument is taken as the instance when it contains a path separator or
// otherwise looks like an instance name. Guessing this way is deliberate: a
// Modrinth slug never contains a path separator, so the only way to misread it
// is to name a mod after an instance, which is not worth a prompt.
func positionalInstance(args []string) (instRef string, projects []string) {
	for i, arg := range args {
		if strings.ContainsAny(arg, "/\\") {
			return arg, args[:i]
		}
	}
	if len(args) > 1 {
		last := args[len(args)-1]
		if looksLikeInstanceRef(last) {
			return last, args[:len(args)-1]
		}
	}
	return "", args
}

func looksLikeInstanceRef(s string) bool {
	if strings.Contains(s, "/") || strings.Contains(s, "\\") {
		return true
	}
	if _, err := os.Stat(s); err == nil {
		return true
	}
	// Names that embed a Minecraft version are almost always instances.
	return strings.Contains(s, "-fabric") ||
		strings.Contains(s, "-forge") ||
		strings.Contains(s, "-quilt") ||
		strings.Contains(s, "-neoforge") ||
		strings.Contains(s, "-mod")
}

// ─── remove ─────────────────────────────────────────────────────────────────

func newRemoveCmd() *cobra.Command {
	var (
		dryRun  bool
		yes     bool
		keepJar bool
	)

	cmd := &cobra.Command{
		Use:     "remove <mod...> [instance]",
		Aliases: []string{"rm", "uninstall"},
		Short:   "Remove mods from an instance",
		Long: strings.TrimSpace(`
Remove mods by name, mod id or project id.

Matching is forgiving: "ipn", "libIPN" and "inventoryprofilesnext" all resolve
to the same jar. Use --dry-run to confirm what would go before it goes.
`),
		Example: strings.TrimSpace(`
  modharbor remove sodium
  modharbor remove sodium zoomify --dry-run
`),
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := bootstrap()
			if err != nil {
				return fail("%v", err)
			}
			instRef, wanted := splitArgs(args)
			inst, err := resolveInstance(a, []string{instRef})
			if err != nil {
				return err
			}

			files, err := instance.ModFiles(inst.ModsDirOrDefault())
			if err != nil {
				return fail("%v", err)
			}
			if len(files) == 0 {
				ui.Warn("no mods installed in %s", inst.ID)
				return nil
			}

			rows, err := scanInstance(cmd.Context(), a, inst, false)
			if err != nil {
				return fail("%v", err)
			}

			matched := matchMods(rows, wanted)
			if len(matched) == 0 {
				ui.Warn("nothing matched %s", strings.Join(wanted, ", "))
				ui.Hint("run `modharbor list` to see what is installed")
				return exitWithCode(1, nil)
			}

			ui.Heading("Remove", inst.Label())
			ui.Blank()
			for _, m := range matched {
				ui.Task("del", ui.Pad(truncateName(m.Title, 30), 30), ui.Faint(m.FileName))
			}
			ui.Blank()

			if !dryRun {
				if !yes && ui.IsInteractive() {
					p := ui.NewPrompter(false)
					if !p.Confirm(fmt.Sprintf("  Remove %d mod(s)?", len(matched)), false) {
						ui.Blank()
						ui.Info("nothing was removed")
						return nil
					}
				}
				if keepJar {
					// Move rather than delete, so the user can put it back.
					backupDir := filepath.Join(inst.ModsDirOrDefault(), ".modharbor-backup", "removed")
					if err := os.MkdirAll(backupDir, 0o755); err != nil {
						return fail("%v", err)
					}
					for _, m := range matched {
						from := filepath.Join(inst.ModsDirOrDefault(), m.FileName)
						to := filepath.Join(backupDir, m.FileName)
						if err := os.Rename(from, to); err != nil {
							ui.Warn("could not move %s: %v", m.FileName, err)
						}
					}
				} else {
					for _, m := range matched {
						if err := os.Remove(filepath.Join(inst.ModsDirOrDefault(), m.FileName)); err != nil {
							ui.Warn("could not remove %s: %v", m.FileName, err)
						}
					}
				}
			}

			if flagJSON {
				return printJSON(map[string]any{"removed": len(matched), "dryRun": dryRun})
			}
			verb := "Removed"
			if dryRun {
				verb = "Would remove"
			}
			ui.Success("%s %d mod(s)", verb, len(matched))
			if keepJar && !dryRun {
				ui.Hint("jars were moved to .modharbor-backup/removed/ — restore them by moving them back")
			}
			ui.Blank()
			return nil
		},
	}

	f := cmd.Flags()
	f.BoolVar(&dryRun, "dry-run", false, "show what would be removed")
	f.BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	f.BoolVar(&keepJar, "keep-files", false, "move the jars to a backup instead of deleting")
	return cmd
}

// matchMods resolves user-supplied names against scanned mods.
func matchMods(rows []ScannedMod, wanted []string) []ScannedMod {
	var out []ScannedMod
	for _, w := range wanted {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		needle := modmeta.NormalizeName(w)
		for _, r := range rows {
			if modmeta.NormalizeName(r.Title) == needle ||
				modmeta.NormalizeName(r.ModID) == needle ||
				modmeta.NormalizeName(r.ProjectID) == needle ||
				strings.EqualFold(r.FileName, w) ||
				strings.HasPrefix(strings.ToLower(r.FileName), strings.ToLower(w)) {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// ─── info ───────────────────────────────────────────────────────────────────

func newInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info <project>",
		Short: "Show a project's details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := bootstrap()
			if err != nil {
				return fail("%v", err)
			}
			p, err := a.MR().Project(cmd.Context(), projectKey(args[0]))
			if err != nil {
				return fail("%v", err)
			}
			if flagJSON {
				return printJSON(p)
			}

			ui.Heading(p.Title, p.Description)
			ui.Blank()

			l := ui.NewList()
			l.Add("Project", ui.Brand(p.Slug))
			l.Add("Modrinth id", ui.Muted(p.ID))
			if len(p.Categories) > 0 {
				l.Add("Categories", ui.Muted(strings.Join(p.Categories, ", ")))
			}
			if len(p.Loaders) > 0 {
				l.Add("Loaders", ui.Muted(strings.Join(p.Loaders, ", ")))
			}
			l.Add("Downloads", ui.Bold(ui.HumanCount(int(p.DownloadCount))))
			l.Add("License", ui.Muted(orDash(p.License.ID)))
			l.Add("Updated", ui.Muted(p.Updated.Format("2 Jan 2006")))
			l.Add("URL", ui.InfoC("https://modrinth.com/mod/"+p.Slug))
			l.Render()
			ui.Blank()

			gv := recentGameVersions(p.GameVersions)
			if len(gv) > 0 {
				ui.Note("supports: %s", ui.Muted(strings.Join(gv, ", ")))
				ui.Blank()
			}
			ui.Hint("install with `modharbor add %s`", p.Slug)
			ui.Blank()
			return nil
		},
	}
}

// recentGameVersions returns the newest few Minecraft versions from the tail
// of Modrinth's chronological list.
func recentGameVersions(all []string) []string {
	if len(all) == 0 {
		return nil
	}
	n := 6
	if len(all) < n {
		n = len(all)
	}
	out := append([]string(nil), all[len(all)-n:]...)
	sort.Strings(out)
	return out
}

// projectKey extracts a project id or slug from a user-supplied argument,
// accepting full Modrinth URLs.
func projectKey(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://modrinth.com/mod/")
	s = strings.TrimPrefix(s, "http://modrinth.com/mod/")
	s = strings.TrimPrefix(s, "https://modrinth.com/project/")
	// The fourth of the four forms modrinth.com serves over both schemes was
	// missing, so an http project link was looked up as a whole URL and
	// reported as a project that does not exist.
	s = strings.TrimPrefix(s, "http://modrinth.com/project/")
	// The query string goes before the trailing slash, never after: a query
	// ends the string, so trimming the slash first leaves it glued to the
	// query and "…/lithium/?tab=versions" resolves to the non-existent project
	// "lithium/". A URL pasted out of a browser has both.
	if i := strings.Index(s, "?"); i > 0 {
		s = s[:i]
	}
	return strings.TrimSuffix(s, "/")
}
