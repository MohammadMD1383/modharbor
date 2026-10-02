package cli

import (
	"fmt"
	"strings"

	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/migrate"
	"github.com/MohammadMD1383/modharbor/internal/resolver"
	"github.com/MohammadMD1383/modharbor/internal/ui"
	"github.com/spf13/cobra"
)

func newUpdateCmd() *cobra.Command {
	var (
		dryRun         bool
		allowDowngrade bool
		include        []string
		exclude        []string
		yes            bool
		all            bool
		quietSummary   bool
	)

	cmd := &cobra.Command{
		Use:     "update [instance]",
		Aliases: []string{"upgrade", "up"},
		Short:   "Update installed mods to their newest compatible version",
		Long: strings.TrimSpace(`
Update every mod that has a newer build for this instance's Minecraft version
and loader.

Only compatible builds are considered: a mod that has not yet published a
26.3 build will not be updated even if a newer 26.2 build exists. Files are
verified by checksum and anything replaced is kept in a backup directory, so a
bad update is always recoverable.

Interactive by default: you choose which mods to apply. Use --all to accept
everything, or --dry-run to see the list first.
`),
		Example: strings.TrimSpace(`
  modharbor update --dry-run
  modharbor update --all
  modharbor update --channel beta
  modharbor update --exclude sodium
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

			st, err := a.State()
			if err != nil {
				return fail("%v", err)
			}
			eng := migrate.New(a.MR(), resolver.New(resolver.Options{
				Modrinth: a.MR(), Cache: st,
			}), st)

			opts := migrate.Options{
				Channel:        a.Channel(),
				AllowDowngrade: allowDowngrade,
				Include:        include,
				Exclude:        exclude,
			}

			// Planning must not write. Engine.Run materialises as it decides
			// unless DryRun is set, so a planning pass that applied its own
			// results would leave the apply pass re-deciding against a folder
			// it had just rewritten: every mod would come back as already
			// current and the command would report an empty run.
			planOpts := opts
			planOpts.DryRun = true
			planOpts.OnProgress = migrateProgress(true)

			plan, err := eng.Run(cmd.Context(), inst, inst, planOpts)
			if err != nil {
				return fail("%v", err)
			}
			updatable := countActions(plan, migrate.ActionInstall, migrate.ActionReplace, migrate.ActionReinstall)
			totalMods := len(plan.Results)

			if flagJSON {
				return printJSON(outdatedJSON{
					Instance: inst.ID, MCVersion: inst.MCVersion,
					Loader: loaderLabel(inst.Type), Total: totalMods,
					Current: len(plan.Results) - updatable,
					Updates: toUpdateEntries(plan.Results),
				})
			}

			if updatable == 0 {
				ui.Heading("Update check", inst.Label())
				ui.Blank()
				ui.Success("everything is up to date")
				if plan.Unmatched > 0 {
					ui.Note("%d mod(s) have no upstream match", plan.Unmatched)
				}
				ui.Blank()
				return nil
			}

			renderOutdated(inst, &updatePlan{
				updates: plan.Results, current: nil, total: totalMods,
				unmatched: plan.Unmatched,
			})

			if dryRun {
				ui.Note("dry run: mods/ is unchanged")
				ui.Hint("re-run without --dry-run to apply")
				ui.Blank()
				return nil
			}

			// Interactive selection.
			if !yes && !all && ui.IsInteractive() && !flagQuiet {
				names := make([]string, 0, updatable)
				for _, r := range plan.Results {
					if r.Action == migrate.ActionInstall || r.Action == migrate.ActionReplace ||
						r.Action == migrate.ActionReinstall {
						names = append(names, fmt.Sprintf("%s  %s %s",
							ui.Pad(truncateName(r.Title, 28), 30),
							ui.Muted(orDash(r.SourceVersion)),
							ui.Diff(orDash(r.SourceVersion), r.TargetVersion)))
					}
				}
				p := ui.NewPrompter(false)
				def := make([]bool, len(names))
				for i := range def {
					def[i] = true
				}
				chosen, _ := p.MultiSelect("Select mods to update", names, def)
				if len(chosen) == 0 {
					ui.Blank()
					ui.Info("nothing selected")
					ui.Blank()
					return nil
				}
				opts.Include = selectedTitles(plan.Results, chosen)
				opts.Exclude = nil
				// Re-plan with the narrowed selection. This stays dry for the
				// same reason the first plan did; the selection changes what
				// would happen, not whether anything has happened yet.
				planOpts = opts
				planOpts.DryRun = true
				planOpts.OnProgress = migrateProgress(true)
				plan, err = eng.Run(cmd.Context(), inst, inst, planOpts)
				if err != nil {
					return fail("%v", err)
				}
			}

			// One write pass, carrying whatever the planning above settled on.
			// The summary is built from this report because it is the only one
			// describing a filesystem that actually changed.
			applyOpts := opts
			applyOpts.OnProgress = migrateProgress(false)
			rep, err := eng.Run(cmd.Context(), inst, inst, applyOpts)
			if err != nil {
				return fail("%v", err)
			}
			if quietSummary {
				ui.Blank()
				ui.Success("updated %d mod(s)", rep.Installed+rep.Replaced+rep.Reinstalled)
				ui.Blank()
				return nil
			}
			renderUpdateResult(inst, rep)
			return nil
		},
	}

	f := cmd.Flags()
	f.BoolVar(&dryRun, "dry-run", false, "list available updates without installing")
	f.BoolVar(&allowDowngrade, "allow-downgrade", false, "allow installing older builds")
	f.StringArrayVar(&include, "include", nil, "only update these mods (repeatable)")
	f.StringArrayVar(&exclude, "exclude", nil, "skip these mods (repeatable)")
	f.BoolVarP(&yes, "yes", "y", false, "accept the default selection")
	f.BoolVar(&all, "all", false, "update everything without prompting")
	f.BoolVar(&quietSummary, "quiet-summary", false, "print only the summary")
	return cmd
}

func countActions(rep *migrate.Report, actions ...migrate.Action) int {
	want := map[migrate.Action]bool{}
	for _, a := range actions {
		want[a] = true
	}
	n := 0
	for _, r := range rep.Results {
		if want[r.Action] {
			n++
		}
	}
	return n
}

// selectedTitles maps chosen menu indexes back to mod titles so they can be
// fed to a second, narrowed planning pass.
func selectedTitles(results []migrate.Result, chosen []int) []string {
	var updatable []string
	for _, r := range results {
		if r.Action == migrate.ActionInstall || r.Action == migrate.ActionReplace ||
			r.Action == migrate.ActionReinstall {
			updatable = append(updatable, titleOf(r))
		}
	}
	var out []string
	for _, i := range chosen {
		if i >= 0 && i < len(updatable) {
			out = append(out, updatable[i])
		}
	}
	return out
}

// titleOf prefers the loader-specific mod id over the display name, because
// the include/exclude filters match on identifiers.
func titleOf(r migrate.Result) string {
	if r.ProjectID != "" {
		return r.ProjectID
	}
	return r.Title
}

// renderUpdateResult prints what changed on disk.
func renderUpdateResult(inst *instance.Info, rep *migrate.Report) {
	ui.Heading("Update complete", inst.Label())
	ui.Blank()

	for _, r := range rep.Results {
		switch r.Action {
		case migrate.ActionInstall:
			ui.Task("add", truncateName(r.Title, 30),
				ui.OK(r.TargetVersion)+ui.Faint("  "+r.TargetFile))
		case migrate.ActionReplace:
			ui.Task("move", truncateName(r.Title, 30), ui.Diff(orDash(r.SourceVersion), r.TargetVersion))
		case migrate.ActionCopy:
			ui.Task("add", truncateName(r.Title, 30), ui.Muted("copied verbatim"))
		case migrate.ActionReinstall:
			ui.Task("move", truncateName(r.Title, 30),
				ui.Muted("re-fetched "+orDash(r.TargetVersion))+ui.Faint("  "+r.Reason))
		}
	}
	ui.Blank()

	n := rep.Installed + rep.Replaced + rep.Reinstalled
	if n > 0 {
		ui.Success("%d mod(s) updated", n)
		ui.Hint("rollback with `modharbor rollback`")
	} else {
		ui.Info("nothing changed")
	}
	ui.Blank()
}
