package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/ui"
	"github.com/spf13/cobra"
)

// backupDirName is the directory migrate/update move replaced jars into. It
// mirrors the constant of the same name in internal/migrate, which is the only
// writer; naming it here keeps the reader honest about the layout on disk.
const backupDirName = ".modharbor-backup"

// removedBucket holds jars `remove --keep-files` set aside. They were never
// replaced, so restoring them is not what rollback means — but a user who only
// sees this bucket needs to be told where their files went.
const removedBucket = "removed"

// rollbackLayout is the timestamp format backupReplacements writes, which is
// what makes the directory names sort chronologically as plain strings.
const rollbackLayout = "20060102T150405Z"

// snapshot is one restorable backup directory.
type snapshot struct {
	// Name is the UTC timestamp directory name.
	Name string
	// Path is the absolute directory holding the replaced jars.
	Path string
	// Files are the jar names inside it.
	Files []string
	// Modified is the directory's modification time, used only to break ties
	// when two runs landed in the same second.
	Modified time.Time
}

// rollbackJSON is the machine-readable form of a rollback run.
type rollbackJSON struct {
	Instance     string   `json:"instance"`
	ModsDir      string   `json:"modsDir"`
	BackupDir    string   `json:"backupDir"`
	Snapshot     string   `json:"snapshot,omitempty"`
	DryRun       bool     `json:"dryRun"`
	Restored     []string `json:"restored,omitempty"`
	Overwritten  []string `json:"overwritten,omitempty"`
	Archived     int      `json:"archived"`
	Snapshots    int      `json:"snapshots"`
	RemovedFiles int      `json:"removedBucketFiles"`
}

func newRollbackCmd() *cobra.Command {
	var (
		list   bool
		to     string
		yes    bool
		dryRun bool
	)

	cmd := &cobra.Command{
		Use:   "rollback [instance]",
		Short: "Restore the mods replaced by the last migrate or update",
		Long: strings.TrimSpace(`
Put back the jars that modharbor replaced during the most recent migrate or
update.

Every change modharbor makes to a mods directory first moves what it replaces
into a timestamped snapshot under mods/.modharbor-backup/, so an update that
breaks the game is always recoverable.

Restoring is itself undoable: the current mods/ contents are moved into a fresh
snapshot before anything is put back. Where a name exists on both sides, the
snapshot wins — that is the older jar you asked for — and the newer one is kept
in the new snapshot rather than deleted.
`),
		Example: strings.TrimSpace(`
  # See what can be restored
  modharbor rollback --list

  # Restore the newest snapshot
  modharbor rollback 26.3-fabric-mod

  # Restore a specific one
  modharbor rollback --to 20260901T101500Z

  # Plan first
  modharbor rollback --dry-run
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
			modsDir := inst.ModsDirOrDefault()
			backupRoot := filepath.Join(modsDir, backupDirName)

			snaps, err := loadSnapshots(backupRoot)
			if err != nil {
				return fail("%v", err)
			}

			if len(snaps) == 0 {
				return renderNoSnapshots(inst, modsDir, backupRoot)
			}

			if list {
				if flagJSON {
					return printJSON(map[string]any{
						"instance":  inst.ID,
						"modsDir":   modsDir,
						"backupDir": backupRoot,
						"snapshots": snapshotRows(snaps),
					})
				}
				renderSnapshotList(inst, backupRoot, snaps)
				return nil
			}

			target, err := pickSnapshot(snaps, to)
			if err != nil {
				return fail("%v", err)
			}

			plan := planRollback(modsDir, target)
			if flagJSON && dryRun {
				return printJSON(rollbackResult(inst, modsDir, backupRoot, target, plan, true, 0, len(snaps), 0))
			}

			if dryRun {
				renderRollbackPlan(inst, modsDir, backupRoot, target, plan)
				return nil
			}

			if !yes && ui.IsInteractive() && !flagQuiet {
				ui.Heading("Rollback", inst.Label()+"  "+ui.Arrow()+"  "+ui.Path(target.Name))
				ui.Blank()
				for _, n := range plan.Restore {
					ui.Task("move", truncateName(n, 30), ui.Muted("will be restored"))
				}
				ui.Blank()
				p := ui.NewPrompter(false)
				if !p.Confirm("\n  Restore these files?", false) {
					ui.Blank()
					ui.Info("nothing was changed")
					ui.Blank()
					return nil
				}
			}

			archived, err := applyRollback(modsDir, target, plan)
			if err != nil {
				return fail("%v", err)
			}

			removedCount := countBucket(backupRoot, removedBucket)
			if flagJSON {
				return printJSON(rollbackResult(inst, modsDir, backupRoot, target, plan, false, archived, len(snaps), removedCount))
			}
			if flagQuiet {
				ui.Blank()
				ui.Success("restored %d file(s) from %s", len(plan.Restore), target.Name)
				ui.Blank()
				return nil
			}
			renderRollback(inst, modsDir, backupRoot, target, plan, archived, removedCount)
			return nil
		},
	}

	f := cmd.Flags()
	f.BoolVar(&list, "list", false, "list available snapshots instead of restoring")
	f.StringVar(&to, "to", "", "restore this snapshot instead of the most recent one")
	f.BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	f.BoolVar(&dryRun, "dry-run", false, "show what would be restored without changing anything")
	return cmd
}

// rollbackPlan is the outcome of comparing a snapshot against the current mods
// directory.
type rollbackPlan struct {
	// Restore are the files the snapshot will put back.
	Restore []string
	// Overwrite names the files that exist in both places; the snapshot wins.
	Overwrite []string
	// Archive are the current jars that will be swept into a new snapshot so
	// the restore itself can be undone.
	Archive []string
}

// loadSnapshots lists the restorable snapshot directories, newest first.
//
// The removed/ and duplicates/ buckets are excluded: they hold jars that were
// deliberately deleted rather than replaced, so restoring them would undo a
// removal the user asked for.
func loadSnapshots(backupRoot string) ([]snapshot, error) {
	entries, err := os.ReadDir(backupRoot)
	if err != nil {
		if os.IsNotExist(err) {
			// No backup directory at all is the normal state of a healthy
			// instance, not an error.
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", backupRoot, err)
	}

	var out []snapshot
	for _, e := range entries {
		if !e.IsDir() || isNonRestorableBucket(e.Name()) {
			continue
		}
		if _, err := time.Parse(rollbackLayout, e.Name()); err != nil {
			// Anything not stamped by backupReplacements is a bucket.
			continue
		}
		files, err := instance.ModFiles(filepath.Join(backupRoot, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", filepath.Join(backupRoot, e.Name()), err)
		}
		if len(files) == 0 {
			continue
		}
		names := make([]string, 0, len(files))
		for _, f := range files {
			names = append(names, filepath.Base(f))
		}
		sort.Strings(names)
		s := snapshot{Name: e.Name(), Path: filepath.Join(backupRoot, e.Name()), Files: names}
		if fi, err := e.Info(); err == nil {
			s.Modified = fi.ModTime()
		}
		out = append(out, s)
	}

	// The directory name is a UTC timestamp, so a plain reverse string sort is
	// chronological; Modified only breaks ties within the same second.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name > out[j].Name
		}
		return out[i].Modified.After(out[j].Modified)
	})
	return out, nil
}

// isNonRestorableBucket reports whether a directory under the backup root is a
// side-bucket rather than a restore point.
func isNonRestorableBucket(name string) bool {
	switch name {
	case removedBucket, "duplicates":
		return true
	default:
		return false
	}
}

// pickSnapshot chooses the snapshot to restore, honouring --to.
//
// An exact timestamp is required rather than a prefix match: a user who typed
// --to should not silently get a different snapshot than the one they can see
// in --list.
func pickSnapshot(snaps []snapshot, to string) (snapshot, error) {
	if to == "" {
		return snaps[0], nil
	}
	for _, s := range snaps {
		if s.Name == to {
			return s, nil
		}
	}
	names := make([]string, 0, len(snaps))
	for _, s := range snaps {
		names = append(names, s.Name)
	}
	return snapshot{}, fmt.Errorf("no snapshot named %q; available: %s", to, strings.Join(names, ", "))
}

// planRollback compares the snapshot against the current mods directory.
func planRollback(modsDir string, s snapshot) rollbackPlan {
	present := map[string]bool{}
	if files, err := instance.ModFiles(modsDir); err == nil {
		for _, f := range files {
			present[filepath.Base(f)] = true
		}
	}

	plan := rollbackPlan{}
	for _, name := range s.Files {
		plan.Restore = append(plan.Restore, name)
		if present[name] {
			plan.Overwrite = append(plan.Overwrite, name)
		}
	}
	for name := range present {
		plan.Archive = append(plan.Archive, name)
	}
	sort.Strings(plan.Archive)
	return plan
}

// applyRollback performs the restore.
//
// Order matters: everything currently in mods/ is swept into a fresh snapshot
// first, so an interruption halfway through still leaves every jar somewhere
// findable. Only then is the chosen snapshot moved back.
func applyRollback(modsDir string, s snapshot, plan rollbackPlan) (int, error) {
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		return 0, fmt.Errorf("%s: %w", modsDir, err)
	}

	archiveDir := filepath.Join(modsDir, backupDirName, nextStamp(modsDir, time.Now()))
	archived := 0
	if len(plan.Archive) > 0 {
		if err := os.MkdirAll(archiveDir, 0o755); err != nil {
			return 0, fmt.Errorf("%s: %w", archiveDir, err)
		}
		for _, name := range plan.Archive {
			from := filepath.Join(modsDir, name)
			to := filepath.Join(archiveDir, name)
			if err := moveFile(from, to); err != nil {
				return archived, fmt.Errorf("archiving %s: %w", name, err)
			}
			archived++
		}
	}

	for _, name := range plan.Restore {
		from := filepath.Join(s.Path, name)
		to := filepath.Join(modsDir, name)
		if err := moveFile(from, to); err != nil {
			return archived, fmt.Errorf("restoring %s: %w", name, err)
		}
	}
	return archived, nil
}

// nextStamp produces a snapshot name that does not collide with an existing
// one, which matters when a rollback follows an update inside the same second.
func nextStamp(modsDir string, now time.Time) string {
	root := filepath.Join(modsDir, backupDirName)
	stamp := now.UTC().Format(rollbackLayout)
	if _, err := os.Stat(filepath.Join(root, stamp)); os.IsNotExist(err) {
		return stamp
	}
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%03d", stamp, i)
		if _, err := os.Stat(filepath.Join(root, candidate)); os.IsNotExist(err) {
			return candidate
		}
	}
	return stamp
}

// moveFile renames src to dst, falling back to copy-and-delete when the two
// live on different filesystems — a backup directory can be a symlink or a
// bind mount, and that must not be a reason to fail.
func moveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyFileTo(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// countBucket reports how many jars sit in a non-restorable bucket, so the user
// knows where a `remove --keep-files` went.
func countBucket(backupRoot, bucket string) int {
	files, err := instance.ModFiles(filepath.Join(backupRoot, bucket))
	if err != nil {
		return 0
	}
	return len(files)
}

// snapshotRows projects snapshots into the machine-readable shape.
func snapshotRows(snaps []snapshot) []map[string]any {
	out := make([]map[string]any, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, map[string]any{
			"timestamp": s.Name,
			"files":     len(s.Files),
			"names":     s.Files,
		})
	}
	return out
}

func rollbackResult(inst *instance.Info, modsDir, backupRoot string, s snapshot, plan rollbackPlan, dryRun bool, archived, snaps, removed int) rollbackJSON {
	return rollbackJSON{
		Instance: inst.ID, ModsDir: modsDir, BackupDir: backupRoot,
		Snapshot: s.Name, DryRun: dryRun, Restored: plan.Restore,
		Overwritten: plan.Overwrite, Archived: archived,
		Snapshots: snaps, RemovedFiles: removed,
	}
}

// renderNoSnapshots explains that there is nothing to restore, and names the
// directory that was searched so the user can check their assumption.
func renderNoSnapshots(inst *instance.Info, modsDir, backupRoot string) error {
	removed := countBucket(backupRoot, removedBucket)
	if flagJSON {
		return printJSON(rollbackJSON{
			Instance: inst.ID, ModsDir: modsDir, BackupDir: backupRoot,
			RemovedFiles: removed,
		})
	}

	ui.Heading("Rollback", inst.Label())
	ui.Blank()
	ui.Warn("no snapshots found in %s", ui.Path(backupRoot))
	ui.Blank()

	if removed > 0 {
		// This is a distinct bucket on purpose: those jars were removed on
		// request, so nothing here is a restore point.
		ui.Info("%d jar(s) in %s were set aside by `modharbor remove --keep-files`",
			removed, ui.Path(filepath.Join(backupRoot, removedBucket)))
		ui.Note("that is a separate bucket and is not a rollback target")
		ui.Hint("move them back by hand if you want them again")
	} else {
		ui.Note("snapshots are created when modharbor replaces a jar during migrate or update")
		ui.Hint("run `modharbor update --dry-run` to see whether anything would change")
	}
	ui.Blank()
	return nil
}

// renderSnapshotList prints every restorable snapshot, newest first.
func renderSnapshotList(inst *instance.Info, backupRoot string, snaps []snapshot) {
	ui.Heading("Snapshots", inst.Label()+"  "+ui.Path(backupRoot))
	ui.Blank()

	tab := ui.NewTable("", "SNAPSHOT (UTC)", "FILES", "TAKEN")
	tab.Align(0, ui.AlignRight)
	tab.Align(2, ui.AlignRight)

	for i, s := range snaps {
		marker := ui.Faint(ui.SymDot)
		if i == 0 {
			marker = ui.OK(ui.SymStar)
		}
		tab.Row(
			marker,
			s.Name,
			fmt.Sprint(len(s.Files)),
			ui.Faint(snapshotAge(s.Modified)),
		)
	}
	tab.Render()

	ui.Success("%s, %s", plural(len(snaps), "snapshot", "snapshots"), ui.HumanBytes(snapshotBytes(snaps)))
	ui.Hint("restore the newest with `modharbor rollback %s`", inst.ID)
	if removed := countBucket(backupRoot, removedBucket); removed > 0 {
		ui.Note("%d jar(s) in %s are set aside by `remove --keep-files` and cannot be rolled back",
			removed, ui.Path(filepath.Join(backupRoot, removedBucket)))
	}
	ui.Blank()
}

// snapshotBytes totals the size of every snapshot's contents.
func snapshotBytes(snaps []snapshot) int64 {
	var n int64
	for _, s := range snaps {
		for _, name := range s.Files {
			if fi, err := os.Stat(filepath.Join(s.Path, name)); err == nil {
				n += fi.Size()
			}
		}
	}
	return n
}

// snapshotAge renders how long ago a snapshot was taken.
func snapshotAge(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return ui.HumanDuration(time.Since(t).Seconds()) + " ago"
}

// renderRollbackPlan shows what a restore would do.
func renderRollbackPlan(inst *instance.Info, modsDir, backupRoot string, s snapshot, plan rollbackPlan) {
	ui.Heading("Rollback plan", inst.Label()+"  "+ui.Arrow()+"  "+ui.Path(s.Name))
	ui.Blank()

	for _, name := range plan.Restore {
		if contains(plan.Overwrite, name) {
			ui.Task("move", truncateName(name, 30), ui.Warnc("exists now — snapshot wins"))
			continue
		}
		ui.Task("move", truncateName(name, 30), ui.OK("will be restored"))
	}
	ui.Blank()

	p := ui.NewPanel("Summary")
	p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(len(plan.Restore)), 4), ui.OK("to restore"))
	if len(plan.Overwrite) > 0 {
		p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(len(plan.Overwrite)), 4), ui.Warnc("will overwrite a current jar"))
	}
	p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(len(plan.Archive)), 4), ui.Muted("current jars archived first"))
	p.Render()
	ui.Blank()

	ui.Info("dry run: nothing was changed")
	ui.Hint("re-run without --dry-run to apply")
	ui.Blank()
}

// renderRollback prints what a restore actually did.
func renderRollback(inst *instance.Info, modsDir, backupRoot string, s snapshot, plan rollbackPlan, archived, removed int) {
	ui.Heading("Rollback complete", inst.Label()+"  "+ui.Path(s.Name))
	ui.Blank()

	ui.Task("move", fmt.Sprintf("%d file(s) restored", len(plan.Restore)), ui.OK(s.Name))
	if archived > 0 {
		ui.Task("move", fmt.Sprintf("%d current jar(s) archived", archived), ui.Muted("the restore is undoable"))
	}
	if len(plan.Overwrite) > 0 {
		for _, name := range plan.Overwrite {
			ui.Task("warn", truncateName(name, 30), ui.Warnc("the snapshot's copy replaced the current jar"))
		}
	}
	ui.Blank()

	p := ui.NewPanel("Summary")
	p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(len(plan.Restore)), 4), ui.OK("restored"))
	if len(plan.Overwrite) > 0 {
		p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(len(plan.Overwrite)), 4), ui.Warnc("overwritten"))
	}
	p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(archived), 4), ui.Muted("archived"))
	p.Render()
	ui.Blank()

	ui.Success("restored %d file(s) from %s", len(plan.Restore), s.Name)
	if archived > 0 {
		ui.Note("the jars that were in mods/ are in %s", ui.Path(filepath.Join(backupRoot, "…")))
	}
	if removed > 0 {
		ui.Note("%d jar(s) in %s came from `remove --keep-files` and were not touched",
			removed, ui.Path(filepath.Join(backupRoot, removedBucket)))
	}
	ui.Hint("undo this with `modharbor rollback --list`")
	ui.Blank()
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
