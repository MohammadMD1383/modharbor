package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MohammadMD1383/modharbor/internal/app"
	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/modmeta"
	"github.com/MohammadMD1383/modharbor/internal/resolver"
	"github.com/MohammadMD1383/modharbor/internal/ui"
	"github.com/spf13/cobra"
)

// Severity levels reported by `doctor`.
const (
	sevError = "error"
	sevWarn  = "warn"
	sevInfo  = "info"
	sevOK    = "ok"
)

type finding struct {
	severity string
	title    string
	detail   string
	fix      string
}

// doctorReport is the machine-readable form of a doctor run.
type doctorReport struct {
	Instance  string    `json:"instance"`
	MCVersion string    `json:"mcVersion"`
	Loader    string    `json:"loader"`
	Errors    int       `json:"errors"`
	Warnings  int       `json:"warnings"`
	Findings  []finding `json:"findings"`
}

func newDoctorCmd() *cobra.Command {
	var fix bool

	cmd := &cobra.Command{
		Use:   "doctor [instance]",
		Short: "Diagnose problems with an instance",
		Long: strings.TrimSpace(`
Check an instance for the problems that actually cause Minecraft to fail to
start with mods:

  · a mod that does not support this Minecraft version
  · a required dependency that is missing
  · two jars that are the same mod, where one shadows the other
  · a mod left over from a different Minecraft version
  · a dependency requiring a newer loader than the instance has

With --fix, modharbor removes duplicate jars (keeping the newest) into a backup
directory rather than deleting them outright.
`),
		Example: strings.TrimSpace(`
  modharbor doctor
  modharbor doctor 26.3-fabric-mod
  modharbor doctor --fix
`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := bootstrap()
			if err != nil {
				return fail("%v", err)
			}
			inst, err := a.ResolveInstance(pickInstanceArg(args))
			if err != nil {
				return fail("%v", err)
			}

			rows, err := scanInstance(cmd.Context(), a, inst, false)
			if err != nil {
				return fail("%v", err)
			}

			findings, err := diagnose(cmd.Context(), a, inst, rows)
			if err != nil {
				return fail("%v", err)
			}

			rep := doctorReport{
				Instance:  inst.ID,
				MCVersion: inst.MCVersion,
				Loader:    loaderLabel(inst.Type),
			}
			for _, f := range findings {
				rep.Findings = append(rep.Findings, f)
				switch f.severity {
				case sevError:
					rep.Errors++
				case sevWarn:
					rep.Warnings++
				}
			}

			if fix {
				applied, err := applyFixes(cmd.Context(), a, inst)
				if err != nil {
					ui.Warn("fix: %v", err)
				}
				if applied > 0 && !flagJSON {
					ui.Success("moved %d duplicate jar(s) to the backup directory", applied)
				}
			}

			if flagJSON {
				return printJSON(rep)
			}
			renderDoctor(inst, findings)
			if rep.Errors > 0 {
				return exitWithCode(1, nil)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&fix, "fix", false, "apply safe fixes (moves duplicates to the backup dir)")
	return cmd
}

// diagnose inspects an instance and returns a list of findings.
//
// The checks are ordered from most to least severe so `--fix` acts on the
// dangerous problems first.
func diagnose(ctx context.Context, a *app.App, inst *instance.Info, rows []ScannedMod) ([]finding, error) {
	var out []finding

	// Group by loader mod id so duplicates can be detected.
	byModID := map[string][]ScannedMod{}
	for _, r := range rows {
		if r.ModID == "" {
			continue
		}
		byModID[r.ModID] = append(byModID[r.ModID], r)
	}

	// ── duplicate jars ──────────────────────────────────────────────────
	for modID, group := range byModID {
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].FileName < group[j].FileName })
		names := make([]string, len(group))
		for i, g := range group {
			names[i] = g.FileName
		}
		// Keep the most recently modified file; move the rest.
		modsDir := inst.ModsDirOrDefault()
		keep := group[0]
		keepTime := modTime(filepath.Join(modsDir, keep.FileName))
		for _, g := range group[1:] {
			if t := modTime(filepath.Join(modsDir, g.FileName)); t.After(keepTime) {
				keep, keepTime = g, t
			}
		}
		var drop []string
		for _, g := range group {
			if g.FileName != keep.FileName {
				drop = append(drop, g.FileName)
			}
		}
		out = append(out, finding{
			severity: sevError,
			title:    fmt.Sprintf("%d jars provide the mod %q", len(group), modID),
			detail:   "only one will load; the rest silently shadow it",
			fix: fmt.Sprintf("keep %s, remove %s",
				ui.Pad(keep.FileName, 34), strings.Join(drop, ", ")),
		})
	}

	// ── missing required dependencies ───────────────────────────────────
	// Count the ids of libraries nested inside other mods' jars: Sodium and
	// Mod Menu bundle several Fabric API modules, and those count as present.
	installed := presentIDs(inst, rows)
	for _, r := range rows {
		dir := filepath.Join(inst.ModsDirOrDefault(), r.FileName)
		meta, err := modmeta.Read(dir)
		if err != nil || meta == nil || len(meta.DependsOn) == 0 {
			continue
		}
		var missing []string
		for dep, spec := range meta.DependsOn {
			if installed[strings.ToLower(dep)] {
				continue
			}
			missing = append(missing, dep+rangeSuffix(spec))
		}
		if len(missing) == 0 {
			continue
		}
		sort.Strings(missing)
		out = append(out, finding{
			severity: sevError,
			title:    fmt.Sprintf("%s is missing %s", r.Title, plural(len(missing), "dependency", "dependencies")),
			detail:   strings.Join(missing, ", "),
			fix:      "install with `modharbor add <mod>`",
		})
	}

	// ── version compatibility ───────────────────────────────────────────
	if inst.MCVersion != "" && !flagOffline {
		plan, err := planUpdates(ctx, a, inst, rows, outcmdOptions{})
		if err == nil {
			out = append(out, plan.skipReasons...)
		}
	}

	// ── leftovers from another Minecraft version ────────────────────────
	// Uses the version the mod declares for itself, not the file name.
	if inst.MCVersion != "" {
		for _, r := range rows {
			dir := filepath.Join(inst.ModsDirOrDefault(), r.FileName)
			meta, err := modmeta.Read(dir)
			if err != nil || meta == nil {
				continue
			}
			declared := declaredMCVersion(meta)
			if declared == "" || sameMCRelease(declared, inst.MCVersion) {
				continue
			}
			out = append(out, finding{
				severity: sevWarn,
				title:    fmt.Sprintf("%s declares support for MC %s", r.Title, declared),
				detail:   "this instance runs MC " + inst.MCVersion,
				fix:      "run `modharbor update` to fetch a matching build",
			})
		}
	}

	// ── instance-level sanity ───────────────────────────────────────────
	if inst.MCVersion == "" {
		out = append(out, finding{
			severity: sevWarn,
			title:    "could not determine this instance's Minecraft version",
			detail:   "update checks and migration need it to pick compatible builds",
			fix:      "set it explicitly: `modharbor config set defaultInstance <id>`",
		})
	}
	if inst.Type == instance.TypeVanilla && len(rows) > 0 {
		out = append(out, finding{
			severity: sevWarn,
			title:    "no mod loader detected but mods are installed",
			detail:   "Minecraft will ignore every jar in mods/",
			fix:      "check that the version JSON for this instance includes the loader",
		})
	}
	if inst.ModCount == 0 {
		out = append(out, finding{
			severity: sevInfo,
			title:    "no mods installed",
			detail:   "mods/" + " is empty or missing",
		})
	}
	if !flagOffline && len(byModID) == 0 && len(rows) > 0 {
		out = append(out, finding{
			severity: sevWarn,
			title:    "no mod metadata could be read",
			detail:   "without fabric.mod.json or mods.toml, modharbor cannot check compatibility",
		})
	}

	return out, nil
}

// applyFixes moves duplicate jars into the backup directory. Nothing is
// deleted, so a bad heuristic can always be undone by moving files back.
func applyFixes(ctx context.Context, a *app.App, inst *instance.Info) (int, error) {
	// Re-derive duplicates rather than parsing the human-readable fix text.
	rows, err := scanInstance(ctx, a, inst, false)
	if err != nil {
		return 0, err
	}
	modsDir := inst.ModsDirOrDefault()

	byModID := map[string][]ScannedMod{}
	for _, r := range rows {
		if r.ModID != "" {
			byModID[r.ModID] = append(byModID[r.ModID], r)
		}
	}

	moved := 0
	backupDir := filepath.Join(modsDir, ".modharbor-backup", "duplicates")
	for _, group := range byModID {
		if len(group) < 2 {
			continue
		}
		keep := group[0]
		keepTime := modTime(filepath.Join(modsDir, keep.FileName))
		for _, g := range group[1:] {
			if t := modTime(filepath.Join(modsDir, g.FileName)); t.After(keepTime) {
				keep, keepTime = g, t
			}
		}
		if err := os.MkdirAll(backupDir, 0o755); err != nil {
			return moved, err
		}
		for _, g := range group {
			if g.FileName == keep.FileName {
				continue
			}
			from := filepath.Join(modsDir, g.FileName)
			to := filepath.Join(backupDir, g.FileName)
			if err := os.Rename(from, to); err != nil {
				return moved, err
			}
			moved++
		}
	}
	return moved, nil
}

// declaredMCVersion extracts the Minecraft version a mod declares in its own
// metadata ("depends": {"minecraft": "~26.2"}).
//
// This is authoritative, unlike scraping the file name, which produces noise:
// "armored-elytra-1.15.0.jar" is the mod's own version, not a game version.
// Returns "" when the mod declares nothing usable.
func declaredMCVersion(meta *modmeta.Meta) string {
	if meta == nil {
		return ""
	}
	spec := meta.DependsOn["minecraft"]
	if spec == "" {
		spec = meta.DependsOn["minecraft-core"]
	}
	if spec == "" {
		return ""
	}
	return mcVersionFromRange(spec)
}

// mcVersionFromRange extracts the Minecraft version a mod actually targets
// from a Fabric/Forge style dependency range.
//
// Only ranges that name a specific release are meaningful here:
//
//	"~26.2"      targets the 26.2 line
//	"26.2"       targets exactly 26.2
//	"[26.2,26.3)"  an interval: not a single target, so ignored
//
// Open-ended ranges such as ">=26.1-" are minimums, not targets. Reporting
// them would flag every well-behaved mod that supports "this version or
// newer" as incompatible with the very version it was installed for.
func mcVersionFromRange(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "*" {
		return ""
	}

	// An interval or a Forge-style bracket carries no single target.
	if strings.ContainsAny(spec, ",[]()") {
		return ""
	}
	// Leading >= or > is a lower bound only.
	if strings.HasPrefix(spec, ">") {
		return ""
	}

	spec = strings.TrimLeft(spec, "<=!~ \t")
	// Fabric appends a bare "-" to the upper end of a range ("26.1-"), which
	// leaves a trailing hyphen to drop.
	spec = strings.TrimSuffix(spec, "-")

	// Build metadata such as "+26.2" or a snapshot suffix is not the target.
	if i := strings.IndexAny(spec, "+ "); i > 0 {
		spec = spec[:i]
	}

	for _, r := range spec {
		if r != '.' && (r < '0' || r > '9') {
			return ""
		}
	}
	return spec
}

// sameMCRelease compares two Minecraft versions by their leading dotted pair,
// ignoring any patch component.
func sameMCRelease(a, b string) bool {
	return mcRelease(a) == mcRelease(b)
}

func mcRelease(v string) string {
	parts := strings.Split(v, ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return v
}

// rangeSuffix renders a version range compactly for display.
func rangeSuffix(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "*" {
		return ""
	}
	return " " + spec
}

func plural(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

func modTime(path string) (t time.Time) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

func renderDoctor(inst *instance.Info, findings []finding) {
	ui.Heading("modharbor doctor", inst.Label())
	ui.Blank()

	if len(findings) == 0 {
		ui.Success("no problems found")
		ui.Blank()
		return
	}

	sort.SliceStable(findings, func(i, j int) bool {
		return sevRank(findings[i].severity) < sevRank(findings[j].severity)
	})

	errors, warns := 0, 0
	for _, f := range findings {
		var icon string
		switch f.severity {
		case sevError:
			icon = ui.Paint(ui.Palette.Err, ui.SymFail)
			errors++
		case sevWarn:
			icon = ui.Paint(ui.Palette.Warn, ui.SymWarn)
			warns++
		default:
			icon = ui.Paint(ui.Palette.Info, ui.SymInfo)
		}
		ui.Blank()
		ui.Printf("  %s  %s\n", icon, ui.Bold(f.title))
		if f.detail != "" {
			ui.Printf("      %s\n", ui.Muted(f.detail))
		}
		if f.fix != "" {
			ui.Printf("      %s %s\n", ui.InfoC("fix:"), ui.Muted(f.fix))
		}
	}
	ui.Blank()
	ui.Blank()

	if errors > 0 {
		ui.Failure("%d error(s), %d warning(s)", errors, warns)
	} else {
		ui.Warn("%d warning(s), no errors", warns)
	}
	ui.Blank()
}

func sevRank(s string) int {
	switch s {
	case sevError:
		return 0
	case sevWarn:
		return 1
	default:
		return 2
	}
}

var (
	_ = os.Remove
	_ = filepath.Join
	_ = modmeta.NormalizeName
	_ = resolver.MethodHash
	_ = fmt.Sprint
	_ = strings.Join
)
