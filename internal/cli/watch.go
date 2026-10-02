package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/MohammadMD1383/modharbor/internal/app"
	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/ui"
	"github.com/spf13/cobra"
)

// watchDefaultInterval is how often watch polls when --every is not given.
// Thirty minutes keeps a modpack maintainer informed without spending the
// Modrinth rate limit on a background process nobody is watching.
const watchDefaultInterval = 30 * time.Minute

// watchMinInterval is the shortest interval watch accepts.
//
// It exists because a mistyped `--every 1s` turns a passive watcher into an API
// flood: every cycle resolves each jar against Modrinth. Rejecting the value is
// better than clamping it, since a silently-raised interval would leave the
// user believing they asked for something they did not get.
const watchMinInterval = time.Minute

// watchCycleResult is one planning pass plus the fingerprint used to decide
// whether the next pass found anything new.
type watchCycleResult struct {
	plan *updatePlan
	sig  string
}

// watchCycleJSON is one line of `--json` output.
type watchCycleJSON struct {
	Cycle     int           `json:"cycle"`
	At        string        `json:"at"`
	Instance  string        `json:"instance"`
	MCVersion string        `json:"mcVersion"`
	Loader    string        `json:"loader"`
	Changed   bool          `json:"changed"`
	Total     int           `json:"total"`
	Current   int           `json:"current"`
	Updates   []updateEntry `json:"updates"`
}

func newWatchCmd() *cobra.Command {
	var every time.Duration

	cmd := &cobra.Command{
		Use:   "watch [instance]",
		Short: "Keep checking for mod updates and report only when they change",
		Long: strings.TrimSpace(`
Poll for mod updates on a timer and print something only when the set of
available updates changes.

The first check prints the current state, so you start from a known baseline
rather than a wall of "everything changed". After that, silence means nothing
new has been published. The table is the same one ` + "`modharbor outdated`" + ` prints,
so the output is familiar.

Polling needs the network, so watch refuses to run with --offline. Press Ctrl-C
to stop; a terminal hangup ends it just as cleanly.
`),
		Example: strings.TrimSpace(`
  modharbor watch
  modharbor watch 26.3-fabric-mod --every 15m
  modharbor watch --every 2h --channel beta
  modharbor watch --json
`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Both checks run before bootstrap: neither needs an instance, a
			// config file or a network call, so a mistyped flag fails the same
			// way on any machine.
			if flagOffline {
				return fail("--offline cannot be used with watch: every cycle resolves mods against Modrinth, so an offline watcher would only repeat a stale cache")
			}
			if every < watchMinInterval {
				return fail("--every %s is below the %s minimum; polling faster would hammer the Modrinth API", every, watchMinInterval)
			}

			a, err := bootstrap()
			if err != nil {
				return fail("%v", err)
			}
			inst, err := a.ResolveInstance(pickInstanceArg(args))
			if err != nil {
				return fail("%v", err)
			}

			// SIGINT and SIGTERM unwind through the same path as a cancelled
			// context, so tests exercise the real shutdown path.
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return watchLoop(ctx, a, inst, every)
		},
	}

	cmd.Flags().DurationVar(&every, "every", watchDefaultInterval,
		fmt.Sprintf("how often to check for updates, minimum %s", watchMinInterval))
	return cmd
}

// watchLoop polls until ctx ends. It returns nil on every exit, including a
// cancelled context, because a user pressing Ctrl-C is not an error.
func watchLoop(ctx context.Context, a *app.App, inst *instance.Info, every time.Duration) error {
	// The Modrinth client is built lazily on first use and caches project
	// metadata for CacheTTL. A watcher lives far longer than that default, so
	// the cache is tied to the poll interval: otherwise a release published
	// mid-interval could stay invisible for several cycles.
	a.CacheTTL = every

	var (
		prev     string
		haveBase bool
		cycle    int
	)

	for {
		if ctx.Err() != nil {
			return watchStopped(inst)
		}

		cycle++
		res, err := watchCycle(ctx, a, inst)

		switch {
		case ctx.Err() != nil:
			// Cancelled mid-cycle: a slow API call should still stop promptly.
			return watchStopped(inst)

		case err != nil:
			// A watcher that dies on the first network hiccup is not a watcher.
			// Report it on stderr and try again next interval, deliberately
			// leaving the baseline untouched: comparing against a plan that was
			// never fully read could hide a real change.
			ui.Warn("check failed: %v", err)

		default:
			// The baseline cycle always prints: "nothing has changed" is only
			// meaningful once the reader has seen what it is comparing against.
			changed := !haveBase || res.sig != prev

			switch {
			case flagJSON:
				if err := printWatchJSON(inst, res, cycle, changed); err != nil {
					return fail("%v", err)
				}
			case changed:
				renderWatchCycle(inst, res, !haveBase, every)
			}

			prev, haveBase = res.sig, true
		}

		if !sleepUntilCycle(ctx, every) {
			return watchStopped(inst)
		}
	}
}

// watchCycle runs exactly the planning pass `outdated` runs: resolve the jars,
// then ask the provider for the newest compatible build of each. Nothing here
// touches the mods directory.
func watchCycle(ctx context.Context, a *app.App, inst *instance.Info) (*watchCycleResult, error) {
	rows, err := scanInstance(ctx, a, inst, false)
	if err != nil {
		return nil, err
	}

	// scanInstance animates its own spinner while identifying jars, so this one
	// covers only the planning pass. Done("") clears the line without printing,
	// which keeps a changed cycle from being drawn over.
	sp := ui.NewSpinner(fmt.Sprintf("Checking %d mods for updates", len(rows))).Start()
	plan, err := planUpdates(ctx, a, inst, rows, outcmdOptions{})
	sp.Done("")
	if err != nil {
		return nil, err
	}

	return &watchCycleResult{plan: plan, sig: watchSignature(plan)}, nil
}

// watchSignature fingerprints the part of a plan a reader would call "the set
// of updates": which mod, what is installed, what is offered.
//
// Sizes and download URLs are excluded because they can change when a build is
// merely republished; treating that as news would train people to ignore the
// output. plan.updates is already sorted by title, so the fingerprint is stable
// across cycles without extra sorting.
func watchSignature(plan *updatePlan) string {
	var b strings.Builder
	for _, r := range plan.updates {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\n",
			r.ProjectID, r.Action, r.SourceVersion, r.TargetVersion, r.TargetFile)
	}
	// The unmatched tally is shown in the table, so a mod that stops resolving
	// upstream is a change worth interrupting the user for.
	fmt.Fprintf(&b, "unmatched\t%d\n", plan.unmatched)
	return b.String()
}

// renderWatchCycle prints a cycle that has something to say: a one-line stamp,
// the familiar outdated table, and when the next check happens.
func renderWatchCycle(inst *instance.Info, res *watchCycleResult, first bool, every time.Duration) {
	title := fmt.Sprintf("%d update(s) available", len(res.plan.updates))
	if first {
		title = "baseline: watching for changes"
	}

	ui.Blank()
	ui.Line("  " + ui.Brand(ui.SymBullet) + " " + ui.Bold(title) +
		ui.Faint("  "+time.Now().Format("15:04:05")))
	ui.Blank()

	renderOutdated(inst, res.plan)

	ui.Note("next check in %s, press Ctrl-C to stop", every)
	ui.Blank()
}

// printWatchJSON emits a single line per cycle. A line rather than an indented
// document so a consumer can follow the stream with a read loop, and every cycle
// is emitted with its `changed` flag: in a long-running process, silence would be
// indistinguishable from a hang.
func printWatchJSON(inst *instance.Info, res *watchCycleResult, cycle int, changed bool) error {
	b, err := json.Marshal(watchCycleJSON{
		Cycle:     cycle,
		At:        time.Now().UTC().Format(time.RFC3339),
		Instance:  inst.ID,
		MCVersion: inst.MCVersion,
		Loader:    loaderLabel(inst.Type),
		Changed:   changed,
		Total:     res.plan.total,
		Current:   len(res.plan.current),
		Updates:   toUpdateEntries(res.plan.updates),
	})
	if err != nil {
		return err
	}
	ui.Fprintf("%s\n", b)
	return nil
}

// watchStopped ends every loop exit through one place so the farewell is
// identical whether the user interrupted a sleep or a slow API call.
func watchStopped(inst *instance.Info) error {
	ui.Blank()
	ui.Info("stopped watching %s", inst.ID)
	ui.Blank()
	return nil
}

// sleepUntilCycle waits out the interval and reports whether the loop should
// continue. The wait starts once a cycle has finished rather than on a fixed
// ticker, so a slow cycle delays the next one instead of firing immediately
// afterwards and overlapping two rounds of API calls.
func sleepUntilCycle(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
