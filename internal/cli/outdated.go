package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/MohammadMD1383/modharbor/internal/app"
	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/migrate"
	"github.com/MohammadMD1383/modharbor/internal/resolver"
	"github.com/MohammadMD1383/modharbor/internal/ui"
	"github.com/spf13/cobra"
)

// outcmdOptions are the shared flags for outdated / update.
type outcmdOptions struct {
	dryRun         bool
	force          bool
	allowDowngrade bool
	include        []string
	exclude        []string
	yes            bool
	backup         bool
}

func newOutdatedCmd() *cobra.Command {
	var opts outcmdOptions

	cmd := &cobra.Command{
		Use:     "outdated [instance]",
		Aliases: []string{"out-of-date", "check"},
		Short:   "Check which mods have a newer compatible version",
		Long: strings.TrimSpace(`
Compare every installed mod against the newest version published for this
instance's Minecraft version and loader.

Only versions compatible with your exact Minecraft release are considered, so a
"newer" build that does not yet support 26.3 will not be suggested.
`),
		Example: strings.TrimSpace(`
  modharbor outdated
  modharbor outdated 26.3-fabric-mod --channel beta
  modharbor outdated --include sodium --include zoomify
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
			plan, err := planUpdates(cmd.Context(), a, inst, rows, opts)
			if err != nil {
				return fail("%v", err)
			}

			if flagJSON {
				return printJSON(outdatedJSON{
					Instance:  inst.ID,
					MCVersion: inst.MCVersion,
					Loader:    loaderLabel(inst.Type),
					Updates:   toUpdateEntries(plan.updates),
					Current:   len(plan.current),
					Total:     plan.total,
				})
			}
			renderOutdated(inst, plan)
			return nil
		},
	}

	cmd.Flags().StringArrayVar(&opts.include, "include", nil, "only check these mods (repeatable)")
	cmd.Flags().StringArrayVar(&opts.exclude, "exclude", nil, "skip these mods (repeatable)")
	return cmd
}

// updatePlan is the outcome of comparing installed mods against upstream.
type updatePlan struct {
	updates   []migrate.Result
	current   []migrate.Result
	total     int
	skipped   int
	scanned   int
	unmatched int
	// skipReasons explains, per mod, why no compatible build was offered.
	skipReasons []finding
}

type updateEntry struct {
	Mod        string  `json:"mod"`
	Current    string  `json:"current"`
	Available  string  `json:"available"`
	File       string  `json:"file,omitempty"`
	Target     string  `json:"target,omitempty"`
	Action     string  `json:"action"`
	Reason     string  `json:"reason,omitempty"`
	Published  string  `json:"published,omitempty"`
	Size       int64   `json:"size,omitempty"`
	Confidence float64 `json:"confidence"`
}

type outdatedJSON struct {
	Instance  string        `json:"instance"`
	MCVersion string        `json:"mcVersion"`
	Loader    string        `json:"loader"`
	Total     int           `json:"total"`
	Current   int           `json:"current"`
	Updates   []updateEntry `json:"updates"`
}

// planUpdates asks the provider for the newest compatible version of each mod.
func planUpdates(ctx context.Context, a *app.App, inst *instance.Info, rows []ScannedMod, opts outcmdOptions) (*updatePlan, error) {
	files, err := instance.ModFiles(inst.ModsDirOrDefault())
	if err != nil {
		return nil, err
	}
	// Map file name -> sha1 so the plan carries hashes for install later.
	shaByFile := make(map[string]string, len(rows))
	for _, r := range rows {
		shaByFile[r.FileName] = r.SHA1
	}
	_ = shaByFile
	_ = files

	st, err := a.State()
	if err != nil {
		return nil, err
	}

	plan := &updatePlan{total: len(rows)}

	// Run one migration pass with everything disabled, purely to compute the
	// plan. This keeps a single code path responsible for version selection.
	mig := migrate.New(a.MR(), resolver.New(resolver.Options{Modrinth: a.MR(), Cache: st}), st)
	rep, err := mig.Run(ctx, inst, inst, migrate.Options{
		Channel:        a.Channel(),
		DryRun:         true,
		Force:          opts.force,
		AllowDowngrade: opts.allowDowngrade,
		Include:        opts.include,
		Exclude:        opts.exclude,
	})
	if err != nil {
		return nil, err
	}

	plan.unmatched = rep.Unmatched
	plan.skipped = rep.Skipped
	for _, r := range rep.Results {
		plan.scanned++
		switch r.Action {
		case migrate.ActionInstall, migrate.ActionReplace, migrate.ActionReinstall:
			plan.updates = append(plan.updates, r)
		case migrate.ActionKeep:
			plan.current = append(plan.current, r)
		case migrate.ActionSkip:
			// A mod that cannot be upgraded for this game version is the
			// single most useful thing doctor can report.
			plan.skipReasons = append(plan.skipReasons, finding{
				severity: sevWarn,
				title:    r.Title + " has no build for MC " + inst.MCVersion,
				detail:   orDash(r.Reason),
				fix:      "remove it, or wait for the author to publish support",
			})
		}
	}

	sort.SliceStable(plan.updates, func(i, j int) bool {
		return strings.ToLower(plan.updates[i].Title) < strings.ToLower(plan.updates[j].Title)
	})
	return plan, nil
}

func renderOutdated(inst *instance.Info, plan *updatePlan) {
	ui.Heading("Update check", inst.Label())

	if len(plan.updates) == 0 {
		ui.Blank()
		ui.Success("everything is up to date")
		if plan.unmatched > 0 {
			ui.Note("%d mod(s) have no upstream match and were not checked", plan.unmatched)
		}
		ui.Blank()
		return
	}

	ui.Blank()
	tab := ui.NewTable("", "MOD", "INSTALLED", "AVAILABLE", "SIZE")
	tab.Align(0, ui.AlignRight)
	tab.Align(4, ui.AlignRight)

	for _, r := range plan.updates {
		icon := ui.OK(ui.SymArrow)
		switch r.Action {
		case migrate.ActionReplace:
			icon = ui.Accent(ui.SymMove)
		case migrate.ActionReinstall:
			icon = ui.Muted(ui.SymMove)
		}
		tab.Row(
			icon,
			truncateName(r.Title, 32),
			ui.Bad(r.SourceVersion),
			ui.Bold(ui.OK(r.TargetVersion)),
			ui.Faint(ui.HumanBytes(r.Size)),
		)
	}
	tab.Footer("", fmt.Sprint(len(plan.updates)), "to update", "", "")
	tab.Render()

	if plan.unmatched > 0 {
		ui.Note("%d mod(s) were skipped because they have no upstream match", plan.unmatched)
		ui.Blank()
	}
	ui.Hint("run `modharbor update` to install these")
	ui.Blank()
}

func toUpdateEntries(results []migrate.Result) []updateEntry {
	out := make([]updateEntry, 0, len(results))
	for _, r := range results {
		out = append(out, updateEntry{
			Mod: r.Title, Current: r.SourceVersion, Available: r.TargetVersion,
			File: r.SourceFile, Target: r.TargetFile, Action: string(r.Action),
			Reason: r.Reason, Size: r.Size, Confidence: r.Confidence,
		})
	}
	return out
}
