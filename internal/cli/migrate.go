package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/migrate"
	"github.com/MohammadMD1383/modharbor/internal/resolver"
	"github.com/MohammadMD1383/modharbor/internal/ui"
	"github.com/spf13/cobra"
)

func newMigrateCmd() *cobra.Command {
	var (
		dryRun         bool
		copyUnknown    bool
		allowDowngrade bool
		channel        string
		include        []string
		exclude        []string
		yes            bool
		quietSummary   bool
		yesToAll       bool
	)

	cmd := &cobra.Command{
		Use:   "migrate <source> <target>",
		Short: "Copy mods from one instance to another, resolving versions",
		Long: strings.TrimSpace(`
Migrate mods between two Minecraft instances.

This is the command for the usual upgrade: you have a working instance with a
pile of mods, install a fresh one for a new Minecraft release, and want the
same set of mods working there.

For each mod found in the source instance, modharbor:

  1. identifies it (exact hash lookup where possible)
  2. finds the newest build for the target's Minecraft version and loader
  3. downloads it and verifies the checksum
  4. installs it, backing up anything it replaces

Mods with no compatible upstream build are reported with the reason. Mods that
cannot be identified at all are left alone unless --copy-unknown is passed.

Nothing is written unless you confirm, so it is safe to explore with --dry-run.
`),
		Example: strings.TrimSpace(`
  # See exactly what would happen
  modharbor migrate 26.2-fabric-mod 26.3-fabric-mod --dry-run

  # Do it
  modharbor migrate 26.2-fabric-mod 26.3-fabric-mod

  # Include a private jar you built yourself
  modharbor migrate 26.2-fabric-mod 26.3-fabric-mod --copy-unknown

  # Carry configs as well as mods
  modharbor migrate 26.2-fabric-mod 26.3-fabric-mod --with-configs
`),
		Args: cobra.RangeArgs(2, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := bootstrap()
			if err != nil {
				return fail("%v", err)
			}
			if channel != "" {
				a.Config.Update.Channel = channel
			}

			src, err := a.ResolveInstance(args[0])
			if err != nil {
				return fail("source instance: %v", err)
			}
			dst, err := a.ResolveInstance(args[1])
			if err != nil {
				return fail("target instance: %v", err)
			}
			// --loader applies to the target: version selection reads the
			// destination's loader, the source side is only identified.
			if err := applyLoaderOverride(dst); err != nil {
				return err
			}
			if src.Path == dst.Path {
				return fail("source and target are the same instance (%s)", src.ID)
			}
			if flagJSON {
				// JSON mode is non-interactive and never prompts.
				yesToAll = true
				quietSummary = true
			}

			st, err := a.State()
			if err != nil {
				return fail("%v", err)
			}

			eng := migrate.New(a.MR(), resolver.New(resolver.Options{
				Modrinth: a.MR(), Cache: st,
			}), st)

			opts := migrate.Options{
				Channel:        a.Channel(),
				CopyUnknown:    copyUnknown,
				DryRun:         dryRun,
				AllowDowngrade: allowDowngrade,
				Include:        include,
				Exclude:        exclude,
				OnProgress:     migrateProgress(dryRun),
			}

			if !dryRun && !yes && !yesToAll && ui.IsInteractive() {
				// Show the plan before touching anything.
				preview, err := eng.Run(cmd.Context(), src, dst, opts)
				if err != nil {
					return fail("%v", err)
				}
				renderMigration(src, dst, preview, true)
				p := ui.NewPrompter(false)
				if !p.Confirm("\n  Apply these changes?", false) {
					ui.Blank()
					ui.Info("nothing was changed")
					return nil
				}
				opts.OnProgress = migrateProgress(false)
			}

			rep, err := eng.Run(cmd.Context(), src, dst, opts)
			if err != nil {
				return fail("%v", err)
			}

			if withConfigs {
				if err := migrateConfigs(src, dst, dryRun); err != nil {
					ui.Warn("configs: %v", err)
				}
			}

			if flagJSON {
				return printJSON(migrationJSON{
					Source: src.ID, Target: dst.ID, DryRun: rep.DryRun,
					Summary: tallyOf(rep), Changes: toUpdateEntries(rep.Results),
				})
			}
			if quietSummary {
				ui.Blank()
				ui.Success("migration complete")
				return nil
			}
			renderMigration(src, dst, rep, false)
			return nil
		},
	}

	f := cmd.Flags()
	f.BoolVar(&dryRun, "dry-run", false, "show the plan without changing anything")
	f.BoolVar(&copyUnknown, "copy-unknown", false, "copy mods that have no upstream match verbatim")
	f.BoolVar(&allowDowngrade, "allow-downgrade", false, "install older builds when the target has newer ones")
	f.StringVar(&channel, "version-channel", "", "release channel: release, beta, alpha")
	f.StringArrayVar(&include, "include", nil, "only migrate these mods (repeatable)")
	f.StringArrayVar(&exclude, "exclude", nil, "skip these mods (repeatable)")
	f.BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	f.BoolVar(&quietSummary, "quiet-summary", false, "print only the summary")
	f.BoolVar(&withConfigs, "with-configs", false, "also copy config, defaultconfigs, resourcepacks and shaderpacks")
	return cmd
}

// withConfigs enables the optional config-directory carry-over.
var withConfigs bool

// migrateProgress renders a live task list while the plan is computed.
func migrateProgress(dryRun bool) func(int, int, migrate.Result) {
	if flagQuiet || flagJSON {
		return nil
	}
	sp := ui.NewSpinner("planning").Start()
	return func(done, total int, r migrate.Result) {
		if done == total {
			sp.Done("")
		}
	}
}

// tallyJSON is the machine-readable form of a migration's outcome counts.
type tallyJSON struct {
	Installed   int `json:"installed"`
	Replaced    int `json:"replaced"`
	Reinstalled int `json:"reinstalled"`
	Kept        int `json:"kept"`
	Skipped     int `json:"skipped"`
	Unmatched   int `json:"unmatched"`
	Copied      int `json:"copied"`
	Duplicates  int `json:"duplicates"`
	Failed      int `json:"failed"`
}

type migrationJSON struct {
	Source  string        `json:"source"`
	Target  string        `json:"target"`
	DryRun  bool          `json:"dryRun"`
	Summary tallyJSON     `json:"summary"`
	Changes []updateEntry `json:"changes"`
}

// tallyOf projects a report's counters into the JSON shape.
func tallyOf(r *migrate.Report) tallyJSON {
	return tallyJSON{
		Installed: r.Installed, Replaced: r.Replaced, Kept: r.Kept,
		Reinstalled: r.Reinstalled,
		Skipped:     r.Skipped, Unmatched: r.Unmatched, Copied: r.Copied,
		Duplicates: r.Duplicates, Failed: r.Failed,
	}
}

// renderMigration prints the plan or the result.
func renderMigration(src, dst *instance.Info, rep *migrate.Report, isPreview bool) {
	title := "Migration plan"
	if !rep.DryRun {
		title = "Migration complete"
	}
	_ = isPreview

	ui.Heading(title, src.Label()+"  "+ui.Arrow()+"  "+dst.Label())

	rows := make([]migrate.Result, 0, len(rep.Results))
	rows = append(rows, rep.Results...)
	sort.SliceStable(rows, func(i, j int) bool {
		return actionRank(rows[i].Action) < actionRank(rows[j].Action)
	})

	shown := 0
	for _, r := range rows {
		if r.Action == migrate.ActionKeep {
			continue
		}
		ui.Task(actionState(r.Action), truncateName(r.Title, 30), actionDetail(r))
		shown++
	}
	if shown > 0 {
		ui.Blank()
	}

	kept := 0
	for _, r := range rows {
		if r.Action == migrate.ActionKeep {
			kept++
		}
	}
	if kept > 0 {
		ui.Note("%d mod(s) already up to date", kept)
		ui.Blank()
	}

	p := ui.NewPanel("Summary")
	if rep.Installed > 0 {
		p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(rep.Installed), 4), ui.OK("to install"))
	}
	if rep.Replaced > 0 {
		p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(rep.Replaced), 4), ui.Accent("to replace"))
	}
	if rep.Copied > 0 {
		p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(rep.Copied), 4), ui.Muted("copied verbatim"))
	}
	if rep.Skipped > 0 {
		p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(rep.Skipped), 4), ui.Warnc("no compatible build"))
	}
	if rep.Reinstalled > 0 {
		p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(rep.Reinstalled), 4), ui.Muted("re-fetched from upstream"))
	}
	if rep.Duplicates > 0 {
		p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(rep.Duplicates), 4), ui.Warnc("duplicate jars"))
	}
	if rep.Unmatched > 0 {
		p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(rep.Unmatched), 4), ui.Bad("unmatched upstream"))
	}
	if kept > 0 {
		p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(kept), 4), ui.Faint("already current"))
	}
	if len(rep.Results) == 0 {
		p.Line("nothing to do")
	}
	p.Render()
	ui.Blank()

	if rep.DryRun {
		ui.Info("dry run: nothing was written")
		ui.Hint("re-run without --dry-run to apply")
		ui.Blank()
		return
	}

	if rep.Duplicates > 0 {
		ui.Warn("%d duplicate jar(s) — only one copy of each mod can load", rep.Duplicates)
		for _, r := range rep.Results {
			if r.Action == migrate.ActionDuplicate {
				ui.Task("warn", truncateName(r.Title, 30), ui.Faint(r.Reason))
			}
		}
		ui.Blank()
	}
	if rep.Unmatched > 0 || rep.Skipped > 0 {
		ui.Warn("%d mod(s) need a manual decision", rep.Unmatched+rep.Skipped)
		for _, r := range rep.Results {
			if r.Action == migrate.ActionSkip {
				ui.Task("skip", truncateName(r.Title, 30), ui.Faint(r.Reason))
			}
			if r.Action == migrate.ActionUnmatched {
				ui.Task("skip", truncateName(r.Title, 30),
					ui.Faint("no upstream match — re-run with --copy-unknown to carry it over"))
			}
		}
		ui.Blank()
	}
}

// actionState maps an action to the glyph used by ui.Task.
func actionState(a migrate.Action) string {
	switch a {
	case migrate.ActionInstall:
		return "add"
	case migrate.ActionReplace:
		return "move"
	case migrate.ActionCopy:
		return "add"
	case migrate.ActionReinstall:
		return "move"
	case migrate.ActionSkip:
		return "skip"
	case migrate.ActionDuplicate:
		return "warn"
	case migrate.ActionUnmatched:
		return "warn"
	default:
		return "ok"
	}
}

func actionDetail(r migrate.Result) string {
	switch r.Action {
	case migrate.ActionInstall:
		return ui.OK(r.TargetVersion) + ui.Faint("  "+r.TargetFile)
	case migrate.ActionReplace:
		return ui.Diff(orDash(r.SourceVersion), r.TargetVersion)
	case migrate.ActionCopy:
		return ui.Muted("copied verbatim")
	case migrate.ActionReinstall:
		return ui.Muted("re-fetched ") + ui.Muted(r.TargetVersion) + ui.Faint("  "+r.Reason)
	case migrate.ActionSkip:
		return ui.Warnc(r.Reason)
	case migrate.ActionDuplicate:
		return ui.Warnc(r.Reason)
	default:
		return ""
	}
}

// actionRank orders rows so changes appear before no-ops.
func actionRank(a migrate.Action) int {
	switch a {
	case migrate.ActionInstall:
		return 0
	case migrate.ActionReplace:
		return 1
	case migrate.ActionCopy:
		return 2
	case migrate.ActionReinstall:
		return 3
	case migrate.ActionSkip:
		return 4
	case migrate.ActionDuplicate:
		return 5
	case migrate.ActionUnmatched:
		return 6
	default:
		return 7
	}
}

// migrateConfigs copies mod configuration directories between instances.
func migrateConfigs(src, dst *instance.Info, dryRun bool) error {
	dirs := []string{"config", "defaultconfigs", "resourcepacks", "shaderpacks"}
	copied := 0
	for _, d := range dirs {
		from := filepath.Join(src.Path, d)
		to := filepath.Join(dst.Path, d)
		if _, err := os.Stat(from); err != nil {
			continue
		}
		if _, err := os.Stat(to); err == nil {
			// Never clobber existing configuration.
			continue
		}
		if dryRun {
			copied++
			continue
		}
		if err := copyTree(from, to); err != nil {
			return fmt.Errorf("%s: %w", d, err)
		}
		copied++
	}
	if copied > 0 {
		verb := "copied"
		if dryRun {
			verb = "would copy"
		}
		ui.Success("%s %d config directory(ies)", verb, copied)
	}
	return nil
}

// copyTree recursively copies a directory tree.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFileTo(path, target)
	})
}

func copyFileTo(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	buf := make([]byte, 32*1024)
	if _, err := io.CopyBuffer(out, in, buf); err != nil {
		return err
	}
	return out.Sync()
}
