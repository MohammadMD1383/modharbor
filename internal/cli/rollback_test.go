package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MohammadMD1383/modharbor/internal/instance"
)

// The snapshot layer under mods/.modharbor-backup/ is the whole reason
// `rollback` is safe to run. It had no tests at all, and one half of it
// produced directory names the other half refuses to parse — see
// TestNextStampProducesANameLoadSnapshotsCanRestore.

// ─── fixtures ───────────────────────────────────────────────────────────────

// snapDir creates a restore point under root holding the named jars, each with
// recognisable content, and returns its path.
func snapDir(t *testing.T, root, name string, jars ...string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, j := range jars {
		if err := os.WriteFile(filepath.Join(dir, j), []byte("snapshot:"+j), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// putJar writes a jar into a mods directory.
func putJar(t *testing.T, modsDir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modsDir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readJar returns a file's contents, failing the test if it is missing.
func readJar(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// names lists the direct jar children of dir.
func jarNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// ─── the round trip that matters ────────────────────────────────────────────

// A rollback archives the current mods/ before restoring, and the user is told
// the restore is undoable. That is only true if the directory applyRollback
// writes is one loadSnapshots will list and pickSnapshot can select. nextStamp
// is the only thing that invents a name, so it is the only place the two halves
// can disagree — and they did: it appended "-001" to disambiguate a
// same-second collision, and the reader parsed the name with a bare
// time.Parse, which rejects the suffix. The archive a rollback wrote was the one
// directory it could not see.
func TestNextStampProducesANameLoadSnapshotsCanRestore(t *testing.T) {
	modsDir := t.TempDir()
	root := filepath.Join(modsDir, backupDirName)

	// One collision is enough to reach the suffix.
	first := nextStamp(modsDir, time.Now())
	if err := os.MkdirAll(filepath.Join(root, first), 0o755); err != nil {
		t.Fatal(err)
	}
	second := nextStamp(modsDir, time.Now())

	if second == first {
		t.Fatalf("nextStamp returned %q twice: the collision was not avoided", second)
	}

	// A real restore point, not an empty directory: loadSnapshots skips those.
	snapDir(t, root, second, "sodium.jar")

	snaps, err := loadSnapshots(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 {
		t.Fatalf("loadSnapshots found %d snapshots, want 1: %q is not restorable", len(snaps), second)
	}
	if snaps[0].Name != second {
		t.Errorf("loadSnapshots returned %q, want %q", snaps[0].Name, second)
	}

	// And it must be selectable by name, which is what --to does.
	got, err := pickSnapshot(snaps, second)
	if err != nil {
		t.Fatalf("pickSnapshot(%q): %v", second, err)
	}
	if got.Name != second {
		t.Errorf("pickSnapshot returned %q, want %q", got.Name, second)
	}
}

func TestNextStampAvoidsCollisionsInOrder(t *testing.T) {
	modsDir := t.TempDir()
	root := filepath.Join(modsDir, backupDirName)
	now := time.Now()

	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		name := nextStamp(modsDir, now)
		if seen[name] {
			t.Fatalf("nextStamp reused %q on iteration %d", name, i)
		}
		seen[name] = true
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 5 {
		t.Errorf("got %d distinct names, want 5", len(seen))
	}
}

// The suffix is a disambiguator, not a licence to overwrite. When the search
// space is exhausted nextStamp returns the colliding name rather than looping
// forever, and applyRollback then merges into the existing snapshot. That is
// the documented fallback, so it is pinned rather than left to chance.
func TestNextStampGivesUpRatherThanLoopingForever(t *testing.T) {
	if testing.Short() {
		t.Skip("creates a thousand directories")
	}
	modsDir := t.TempDir()
	root := filepath.Join(modsDir, backupDirName)
	now := time.Now()
	stamp := now.UTC().Format(rollbackLayout)

	// Occupy the base stamp and every suffix the loop will try.
	for i := 0; i < 1000; i++ {
		name := stamp
		if i > 0 {
			name = fmt.Sprintf("%s-%03d", stamp, i)
		}
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got := nextStamp(modsDir, now)
	if !strings.HasPrefix(got, stamp) {
		t.Fatalf("nextStamp = %q, want a name under %q", got, stamp)
	}
	if got != stamp {
		t.Errorf("nextStamp = %q, want the exhausted fallback %q", got, stamp)
	}
}

// suffixes returns the -NNN disambiguators nextStamp walks through.
func suffixes(from, to int) []string {
	var out []string
	for i := from; i <= to; i++ {
		out = append(out, fmt.Sprintf("-%03d", i))
	}
	return out
}

// ─── loadSnapshots ──────────────────────────────────────────────────────────

func TestLoadSnapshotsListsNewestFirst(t *testing.T) {
	root := t.TempDir()
	snapDir(t, root, "20260101T120000Z", "sodium.jar")
	snapDir(t, root, "20260301T120000Z", "sodium.jar", "lithium.jar")
	snapDir(t, root, "20260201T120000Z", "lithium.jar")

	snaps, err := loadSnapshots(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"20260301T120000Z", "20260201T120000Z", "20260101T120000Z"}
	if len(snaps) != len(want) {
		t.Fatalf("got %d snapshots, want %d", len(snaps), len(want))
	}
	for i, w := range want {
		if snaps[i].Name != w {
			t.Errorf("snaps[%d].Name = %q, want %q", i, snaps[i].Name, w)
		}
	}

	// File names are sorted inside a snapshot, and Path is absolute.
	if got := snaps[0].Files; len(got) != 2 || got[0] != "lithium.jar" || got[1] != "sodium.jar" {
		t.Errorf("Files = %v, want [lithium.jar sodium.jar]", got)
	}
	if !filepath.IsAbs(snaps[0].Path) {
		t.Errorf("Path = %q, want absolute", snaps[0].Path)
	}
	if snaps[0].Modified.IsZero() {
		t.Error("Modified is zero: the directory mtime was not recorded")
	}
}

// The comment on the sort says the name is a UTC timestamp so a reverse string
// compare is chronological. That only holds while the disambiguating suffix
// stays after the trailing Z, where it is a greater name than the bare stamp —
// so a later snapshot still sorts first.
func TestLoadSnapshotsOrdersDisambiguatedSnapshotsNewestFirst(t *testing.T) {
	root := t.TempDir()
	snapDir(t, root, "20260101T120000Z", "a.jar")
	snapDir(t, root, "20260101T120000Z-001", "b.jar")
	snapDir(t, root, "20260101T120000Z-002", "c.jar")

	snaps, err := loadSnapshots(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"20260101T120000Z-002", "20260101T120000Z-001", "20260101T120000Z"}
	if len(snaps) != len(want) {
		t.Fatalf("got %d snapshots, want %d: %v", len(snaps), len(want), snaps)
	}
	for i, w := range want {
		if snaps[i].Name != w {
			t.Errorf("snaps[%d].Name = %q, want %q", i, snaps[i].Name, w)
		}
	}
}

// Everything under the backup root that is not a stamped directory is a bucket
// or junk, and a bucket must never be offered as a restore point: `removed`
// holds jars the user asked to delete.
func TestLoadSnapshotsSkipsEverythingThatIsNotARestorePoint(t *testing.T) {
	root := t.TempDir()
	snapDir(t, root, "20260101T120000Z", "sodium.jar")
	snapDir(t, root, removedBucket, "deleted.jar")
	snapDir(t, root, "duplicates", "dup.jar")
	snapDir(t, root, "not-a-timestamp", "x.jar")
	snapDir(t, root, "20260101T120000Z-", "y.jar")    // empty suffix
	snapDir(t, root, "20260101T120000Z-abc", "z.jar") // suffix must be digits
	snapDir(t, root, "20260101T120000Z", nil...)      // empty: nothing to restore
	// A plain file, not a directory.
	if err := os.WriteFile(filepath.Join(root, "20260102T120000Z"), []byte("jar"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A jar sitting loose in the backup root.
	if err := os.WriteFile(filepath.Join(root, "stray.jar"), []byte("jar"), 0o644); err != nil {
		t.Fatal(err)
	}

	snaps, err := loadSnapshots(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].Name != "20260101T120000Z" {
		var names []string
		for _, s := range snaps {
			names = append(names, s.Name)
		}
		t.Fatalf("got %v, want only [20260101T120000Z]", names)
	}
}

// A healthy instance has no backup directory at all. That is the normal state,
// not a failure, and the command turns it into a friendly message — so it must
// not come back as an error.
func TestLoadSnapshotsTreatsAMissingBackupRootAsEmpty(t *testing.T) {
	snaps, err := loadSnapshots(filepath.Join(t.TempDir(), "never-created"))
	if err != nil {
		t.Fatalf("loadSnapshots on a missing root: %v", err)
	}
	if len(snaps) != 0 {
		t.Errorf("got %d snapshots, want 0", len(snaps))
	}
}

// A different I/O failure must still surface. Returning nil here would make the
// command claim the instance has no snapshots when it simply could not look.
func TestLoadSnapshotsReportsARealReadFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not gate directory reads on Windows")
	}
	// root ignores the permission bits entirely, so the directory stays readable
	// and the test would pass for the wrong reason. CI containers and the
	// `unshare -rm` isolation check both run as uid 0.
	if os.Geteuid() == 0 {
		t.Skip("running as root, which bypasses directory permissions")
	}
	root := filepath.Join(t.TempDir(), "locked")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	snapDir(t, root, "20260101T120000Z", "sodium.jar")
	if err := os.Chmod(root, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	if _, err := loadSnapshots(root); err == nil {
		t.Error("loadSnapshots on an unreadable root returned no error")
	}
}

// Modified comes from the directory, not from the newest jar inside it, and it
// is the only field the age column can be derived from — the name is a UTC
// timestamp but carries no reference point.
func TestLoadSnapshotsRecordsTheDirectoryModificationTime(t *testing.T) {
	root := t.TempDir()
	dir := snapDir(t, root, "20260101T120000Z", "a.jar", "b.jar")

	// Set the mtime on the directory itself, after its contents exist.
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(dir, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	snaps, err := loadSnapshots(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 {
		t.Fatalf("got %d snapshots, want 1", len(snaps))
	}
	if !snaps[0].Modified.Equal(stamp) {
		t.Errorf("Modified = %v, want %v", snaps[0].Modified, stamp)
	}
}

func TestIsNonRestorableBucket(t *testing.T) {
	cases := map[string]bool{
		removedBucket: true,
		"duplicates":  true,
		// A stamped directory is a restore point even though it is a
		// directory too; the timestamp parse is what separates them.
		"20260101T120000Z":     false,
		"20260101T120000Z-001": false,
		"removed-old":          false,
		"":                     false,
	}
	for name, want := range cases {
		if got := isNonRestorableBucket(name); got != want {
			t.Errorf("isNonRestorableBucket(%q) = %v, want %v", name, got, want)
		}
	}
}

// ─── pickSnapshot ───────────────────────────────────────────────────────────

func TestPickSnapshot(t *testing.T) {
	snaps := []snapshot{
		{Name: "20260301T120000Z"},
		{Name: "20260201T120000Z"},
		{Name: "20260101T120000Z"},
	}

	// No --to means the newest, which is what the sort already arranged.
	got, err := pickSnapshot(snaps, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "20260301T120000Z" {
		t.Errorf("pickSnapshot(\"\") = %q, want the newest", got.Name)
	}

	// --to selects exactly, including a disambiguated name, and does not
	// prefix-match: a user who typed a name should get that name or an error.
	got, err = pickSnapshot(snaps, "20260201T120000Z")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "20260201T120000Z" {
		t.Errorf("pickSnapshot picked %q", got.Name)
	}

	// An unknown name must list what is available, or the user cannot recover
	// without leaving the terminal.
	_, err = pickSnapshot(snaps, "202602")
	if err == nil {
		t.Fatal("pickSnapshot accepted a prefix")
	}
	for _, want := range []string{"20260301T120000Z", "no snapshot named", "202602"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// The disambiguated name is a real name, not a prefix of another.
	dis := []snapshot{{Name: "20260101T120000Z"}, {Name: "20260101T120000Z-001"}}
	if got, err = pickSnapshot(dis, "20260101T120000Z-001"); err != nil {
		t.Fatalf("pickSnapshot on a disambiguated name: %v", err)
	} else if got.Name != "20260101T120000Z-001" {
		t.Errorf("pickSnapshot = %q, want the -001 name", got.Name)
	}
}

// ─── planRollback ───────────────────────────────────────────────────────────

func TestPlanRollbackSeparatesRestoreOverwriteAndArchive(t *testing.T) {
	modsDir := t.TempDir()
	snap := snapshot{Name: "20260101T120000Z", Files: []string{"sodium.jar", "lithium.jar"}}
	// sodium exists now and will be replaced; lithium is only in the snapshot.
	putJar(t, modsDir, "sodium.jar", "new sodium")
	putJar(t, modsDir, "fabric-api.jar", "untouched")

	plan := planRollback(modsDir, snap)
	if got := plan.Restore; len(got) != 2 || got[0] != "sodium.jar" || got[1] != "lithium.jar" {
		t.Errorf("Restore = %v, want [sodium.jar lithium.jar]", got)
	}
	if got := plan.Overwrite; len(got) != 1 || got[0] != "sodium.jar" {
		t.Errorf("Overwrite = %v, want [sodium.jar]", got)
	}
	// Everything currently installed is archived first, not just the clashes,
	// so an interrupted rollback still leaves the old jars findable.
	want := []string{"fabric-api.jar", "sodium.jar"}
	if got := plan.Archive; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Archive = %v, want %v", got, want)
	}
}

// A mods directory that does not exist yet is a first install, not a failure.
func TestPlanRollbackWithNoModsDirectory(t *testing.T) {
	plan := planRollback(filepath.Join(t.TempDir(), "absent"), snapshot{
		Name: "20260101T120000Z", Files: []string{"sodium.jar"},
	})
	if len(plan.Restore) != 1 || len(plan.Overwrite) != 0 || len(plan.Archive) != 0 {
		t.Errorf("plan = %+v, want one restore and nothing else", plan)
	}
}

// A stray non-jar file in mods/ is not swept into the archive: only jars are
// restore points, and quietly moving a config would be surprising.
func TestPlanRollbackIgnoresNonJarFiles(t *testing.T) {
	modsDir := t.TempDir()
	putJar(t, modsDir, "sodium.jar", "x")
	if err := os.WriteFile(filepath.Join(modsDir, "README.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := planRollback(modsDir, snapshot{Name: "x", Files: []string{"sodium.jar"}})
	if len(plan.Archive) != 1 || plan.Archive[0] != "sodium.jar" {
		t.Errorf("Archive = %v, want [sodium.jar]", plan.Archive)
	}
}

// ─── applyRollback ──────────────────────────────────────────────────────────

func TestApplyRollbackRestoresAndArchives(t *testing.T) {
	modsDir := filepath.Join(t.TempDir(), "mods")
	root := filepath.Join(modsDir, backupDirName)
	// The snapshot to restore from, plus one stamped with the current second.
	// applyRollback archives into a directory of its own, and because the
	// update that created the second snapshot ran this same second, the archive
	// is forced to disambiguate its name.
	older := "20260101T120000Z"
	newest := time.Now().UTC().Format(rollbackLayout)
	snapDir(t, root, older, "sodium.jar", "lithium.jar")
	snapDir(t, root, newest, "sodium.jar")
	putJar(t, modsDir, "sodium.jar", "newer sodium")
	putJar(t, modsDir, "fabric-api.jar", "untouched")

	snaps, err := loadSnapshots(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("fixture has %d snapshots, want 2", len(snaps))
	}
	target, err := pickSnapshot(snaps, older)
	if err != nil {
		t.Fatal(err)
	}
	plan := planRollback(modsDir, target)
	archived, err := applyRollback(modsDir, target, plan)
	if err != nil {
		t.Fatalf("applyRollback: %v", err)
	}
	if archived != 2 {
		t.Errorf("archived = %d, want 2: every jar in mods/ is swept, not only the clashes", archived)
	}

	// The snapshot's copies won.
	if got := readJar(t, filepath.Join(modsDir, "sodium.jar")); got != "snapshot:sodium.jar" {
		t.Errorf("sodium.jar = %q, want the snapshot's copy", got)
	}
	if got := readJar(t, filepath.Join(modsDir, "lithium.jar")); got != "snapshot:lithium.jar" {
		t.Errorf("lithium.jar = %q, want the snapshot's copy", got)
	}

	// A restore *moves* the snapshot's jars out, so that snapshot is spent and
	// stops being offered as a restore target. What is left is the update's own
	// snapshot and the archive this rollback just wrote — and the archive is
	// what makes the restore undoable, so loadSnapshots has to see it.
	snaps, err = loadSnapshots(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		var names []string
		for _, s := range snaps {
			names = append(names, s.Name)
		}
		t.Fatalf("got %v, want the update's snapshot plus the archive", names)
	}
	// The archive is the most recently written directory, so it sorts first:
	// "-001" is a greater name than the bare stamp it disambiguates.
	if snaps[0].Name == older {
		t.Errorf("the spent snapshot is still offered as a restore point: %q", snaps[0].Name)
	}
	fresh := snaps[0]
	if snaps[1].Name != newest {
		t.Errorf("snaps[1] = %q, want the update's own snapshot %q", snaps[1].Name, newest)
	}
	// mods/ now holds exactly the snapshot, and nothing else: the jar that was
	// never part of the snapshot was swept into the archive rather than left
	// alone. That is the documented contract, and it is the reason the archive
	// has to be discoverable.
	if got := jarNames(t, modsDir); len(got) != 2 {
		t.Errorf("mods/ holds %v, want just the snapshot's two jars", got)
	}
	if got := jarNames(t, fresh.Path); len(got) != 2 {
		t.Errorf("archive holds %v, want the two jars that were in mods/", got)
	}
	if got := readJar(t, filepath.Join(fresh.Path, "sodium.jar")); got != "newer sodium" {
		t.Errorf("archived sodium.jar = %q, want the copy that was replaced", got)
	}
	if got := readJar(t, filepath.Join(fresh.Path, "fabric-api.jar")); got != "untouched" {
		t.Errorf("archived fabric-api.jar = %q, want it preserved", got)
	}
}

// A rollback into an instance that has no mods directory yet has to create it.
func TestApplyRollbackCreatesTheModsDirectory(t *testing.T) {
	modsDir := filepath.Join(t.TempDir(), "brand", "new", "mods")
	root := filepath.Join(modsDir, backupDirName)
	snapDir(t, root, "20260101T120000Z", "sodium.jar")

	snaps, err := loadSnapshots(root)
	if err != nil {
		t.Fatal(err)
	}
	plan := planRollback(modsDir, snaps[0])
	if _, err := applyRollback(modsDir, snaps[0], plan); err != nil {
		t.Fatalf("applyRollback: %v", err)
	}
	if got := readJar(t, filepath.Join(modsDir, "sodium.jar")); got != "snapshot:sodium.jar" {
		t.Errorf("sodium.jar = %q", got)
	}
}

// A missing source must be reported against the file it failed on. Thirty jars
// with no name is the B29 failure mode.
func TestApplyRollbackNamesTheFileItFailedOn(t *testing.T) {
	modsDir := t.TempDir()
	root := filepath.Join(modsDir, backupDirName)
	// sodium.jar is present, ghost.jar is not, so the second restore fails.
	dir := snapDir(t, root, "20260101T120000Z", "sodium.jar")
	putJar(t, modsDir, "fabric-api.jar", "untouched")

	snaps, err := loadSnapshots(root)
	if err != nil {
		t.Fatal(err)
	}
	snap := snaps[0]
	snap.Files = []string{"sodium.jar", "ghost.jar"} // as if the directory listed it
	snap.Path = dir

	plan := rollbackPlan{Restore: snap.Files, Archive: []string{"fabric-api.jar"}}
	archived, err := applyRollback(modsDir, snap, plan)
	if err == nil {
		t.Fatal("applyRollback succeeded with a missing source file")
	}
	if !strings.Contains(err.Error(), "ghost.jar") {
		t.Errorf("error %q does not name the file that failed", err)
	}
	// The jars that did move must not be reported as lost: the count comes back
	// alongside the error precisely so the caller can say where they went.
	if archived != 1 {
		t.Errorf("archived = %d, want 1 alongside the error", archived)
	}
}

// ─── moveFile ───────────────────────────────────────────────────────────────

func TestMoveFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.jar")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The destination's parent does not exist yet.
	dst := filepath.Join(dir, "deep", "nested", "dst.jar")
	if err := moveFile(src, dst); err != nil {
		t.Fatalf("moveFile: %v", err)
	}
	if got := readJar(t, dst); got != "payload" {
		t.Errorf("dst = %q, want payload", got)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("the source still exists after moveFile")
	}
}

// A move that cannot happen must not leave half a file behind. `os.Remove` on
// the source only runs after the copy succeeds, so the failure has to leave
// both ends alone — otherwise mods/ gains a truncated jar, which is exactly
// what crashes Minecraft on launch.
func TestMoveFileLeavesNothingBehindWhenTheSourceIsMissing(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "absent.jar")
	dst := filepath.Join(dir, "dst.jar")
	err := moveFile(src, dst)
	if err == nil {
		t.Fatal("moveFile succeeded with a missing source")
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("a destination was created for a move that failed: %v", statErr)
	}
}

// ─── the command ────────────────────────────────────────────────────────────

// rollbackInst builds an instance holding one snapshot per name given, plus a
// current mods/ directory, and points the CLI at it.
func rollbackInst(t *testing.T, mods map[string]string, snaps map[string][]string) (instID string, modsDir string) {
	t.Helper()

	fake := newFakeModrinth(t)
	mcDir, instPath := newTestInstance(t, "testinst", nil)
	writeTestConfig(t, mcDir, fake.API())

	modsDir = modsDirOf(instPath)
	for name, body := range mods {
		putJar(t, modsDir, name, body)
	}
	for name, jars := range snaps {
		snapDir(t, filepath.Join(modsDir, backupDirName), name, jars...)
	}
	return "testinst", modsDir
}

// --list is the only way a user discovers what they can roll back to, so a
// snapshot the command wrote itself has to appear in it. That is the whole
// point of the disambiguating suffix: the archive a rollback produces is the
// newest entry in this very list.
func TestRollbackListReportsEveryRestorePointIncludingADisambiguatedOne(t *testing.T) {
	instID, _ := rollbackInst(t, map[string]string{"sodium.jar": "current"},
		map[string][]string{
			"20260101T120000Z":   {"sodium.jar"},
			"20260101T120000Z-1": {"sodium.jar", "lithium.jar"},
		})

	out, err := captureCLI(t, "rollback", instID, "--list", "--json")
	if err != nil {
		t.Fatalf("rollback --list: %v\n%s", err, out)
	}

	var payload struct {
		Snapshots []struct {
			Timestamp string   `json:"timestamp"`
			Files     int      `json:"files"`
			Names     []string `json:"names"`
		} `json:"snapshots"`
	}
	decodeJSON(t, out, &payload)

	if len(payload.Snapshots) != 2 {
		t.Fatalf("got %d snapshots, want 2: %s", len(payload.Snapshots), out)
	}
	// Newest first, and the disambiguated name is the newer of the two.
	if payload.Snapshots[0].Timestamp != "20260101T120000Z-1" {
		t.Errorf("snapshots[0].timestamp = %q, want the -1 name first", payload.Snapshots[0].Timestamp)
	}
	if payload.Snapshots[0].Files != 2 {
		t.Errorf("snapshots[0].files = %d, want 2", payload.Snapshots[0].Files)
	}

	// The human table must name it too, or the JSON is the only place it exists.
	out, err = captureCLI(t, "rollback", instID, "--list")
	if err != nil {
		t.Fatalf("rollback --list: %v\n%s", err, out)
	}
	// The summary carries the count as well as the total size; plural() hands
	// back the word, so the number has to be interpolated or the line reads
	// "snapshots, 58 B".
	mustContain(t, out, "20260101T120000Z-1", "2 snapshots")
	mustNotContain(t, out, removedBucket)
}

func TestRollbackListOnACleanInstanceSaysWhereToLook(t *testing.T) {
	instID, modsDir := rollbackInst(t, map[string]string{"sodium.jar": "current"}, nil)

	out, err := captureCLI(t, "rollback", instID, "--list")
	if err != nil {
		t.Fatalf("rollback --list: %v\n%s", err, out)
	}
	mustContain(t, out, "no snapshots found", filepath.Join(modsDir, backupDirName))

	// A jar set aside by `remove --keep-files` is not a restore point, and the
	// user has to be told where it went rather than left looking for it.
	snapDir(t, filepath.Join(modsDir, backupDirName), removedBucket, "gone.jar")
	out, err = captureCLI(t, "rollback", instID, "--list")
	if err != nil {
		t.Fatalf("rollback --list: %v\n%s", err, out)
	}
	mustContain(t, out, "1 jar(s)", "not a rollback target")

	// The JSON form has to agree, and it has to keep its shape: --list --json
	// emits an array of snapshots, so with nothing to show it must still emit
	// an array. Emitting a count here instead is what makes a consumer's
	// `.snapshots[]` fail on exactly the instances that have nothing to restore.
	out, err = captureCLI(t, "rollback", instID, "--list", "--json")
	if err != nil {
		t.Fatalf("rollback --list --json: %v\n%s", err, out)
	}
	var listed struct {
		Instance  string `json:"instance"`
		Snapshots []struct {
			Timestamp string `json:"timestamp"`
		} `json:"snapshots"`
	}
	decodeJSON(t, out, &listed)
	if listed.Snapshots == nil {
		t.Errorf("snapshots is null or absent, want an empty array: %s", out)
	}
	if len(listed.Snapshots) != 0 {
		t.Errorf("snapshots = %v, want none", listed.Snapshots)
	}

	// `rollback --json` without --list is a different document and may report
	// the bucket count; it must not be mistaken for the list form.
	out, err = captureCLI(t, "rollback", instID, "--json")
	if err != nil {
		t.Fatalf("rollback --json: %v\n%s", err, out)
	}
	mustContain(t, out, `"removedBucketFiles": 1`, `"dryRun": false`)
	mustNotContain(t, out, `"names"`)
}

func TestRollbackDryRunChangesNothingAndSaysSo(t *testing.T) {
	instID, modsDir := rollbackInst(t,
		map[string]string{"sodium.jar": "current", "fabric-api.jar": "untouched"},
		map[string][]string{"20260101T120000Z": {"sodium.jar"}})
	before := snapshotDir(t, modsDir)

	out, err := captureCLI(t, "rollback", instID, "--dry-run", "--json")
	if err != nil {
		t.Fatalf("rollback --dry-run: %v\n%s", err, out)
	}

	var payload struct {
		Snapshot    string   `json:"snapshot"`
		DryRun      bool     `json:"dryRun"`
		Restored    []string `json:"restored"`
		Overwritten []string `json:"overwritten"`
		Archived    int      `json:"archived"`
		Snapshots   int      `json:"snapshots"`
	}
	decodeJSON(t, out, &payload)
	if payload.Snapshot != "20260101T120000Z" || !payload.DryRun {
		t.Errorf("snapshot/dryRun = %q/%v", payload.Snapshot, payload.DryRun)
	}
	if len(payload.Restored) != 1 || payload.Restored[0] != "sodium.jar" {
		t.Errorf("restored = %v", payload.Restored)
	}
	if len(payload.Overwritten) != 1 {
		t.Errorf("overwritten = %v, want the jar that is about to be replaced", payload.Overwritten)
	}
	// A dry run reports what would happen; it must not claim it did.
	if payload.Archived != 0 {
		t.Errorf("archived = %d on a dry run, want 0", payload.Archived)
	}

	assertDirUnchanged(t, before, snapshotDir(t, modsDir))

	out, err = captureCLI(t, "rollback", instID, "--dry-run")
	if err != nil {
		t.Fatalf("rollback --dry-run: %v\n%s", err, out)
	}
	mustContain(t, out, "will overwrite a current jar", "dry run: nothing was changed")
}

func TestRollbackRestoresAndLeavesAnUndoableArchive(t *testing.T) {
	instID, modsDir := rollbackInst(t,
		map[string]string{"sodium.jar": "current"},
		map[string][]string{"20260101T120000Z": {"sodium.jar", "lithium.jar"}})

	out, err := captureCLI(t, "rollback", instID, "--yes", "--json")
	if err != nil {
		t.Fatalf("rollback: %v\n%s", err, out)
	}
	var payload struct {
		Snapshot  string   `json:"snapshot"`
		Restored  []string `json:"restored"`
		Archived  int      `json:"archived"`
		Snapshots int      `json:"snapshots"`
	}
	decodeJSON(t, out, &payload)
	if len(payload.Restored) != 2 || payload.Archived != 1 {
		t.Errorf("restored/archived = %v/%d, want two files and one archived", payload.Restored, payload.Archived)
	}

	if got := readJar(t, filepath.Join(modsDir, "sodium.jar")); got != "snapshot:sodium.jar" {
		t.Errorf("sodium.jar = %q, want the snapshot's copy", got)
	}

	// The archive the run just wrote must be listed, or "the restore is
	// undoable" is a claim the tool cannot back up.
	out, err = captureCLI(t, "rollback", instID, "--list", "--json")
	if err != nil {
		t.Fatalf("rollback --list: %v\n%s", err, out)
	}
	mustContain(t, out, `"files": 1`)

	snaps, err := loadSnapshots(filepath.Join(modsDir, backupDirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 {
		t.Fatalf("got %d restore points, want just the archive", len(snaps))
	}
	if got := readJar(t, filepath.Join(snaps[0].Path, "sodium.jar")); got != "current" {
		t.Errorf("archived sodium.jar = %q, want the jar that was replaced", got)
	}
}

// The human summary makes three claims about where the replaced jars went and
// that the restore is undoable. Each one has to name the real bucket, because
// the archive directory is the only place a user can put a jar back from.
func TestRollbackHumanSummaryAccountsForEveryMovedJar(t *testing.T) {
	instID, modsDir := rollbackInst(t,
		map[string]string{"sodium.jar": "current", "fabric-api.jar": "untouched"},
		map[string][]string{"20260101T120000Z": {"sodium.jar", "lithium.jar"}})
	snapDir(t, filepath.Join(modsDir, backupDirName), removedBucket, "gone.jar")

	out, err := captureCLI(t, "rollback", instID, "--to", "20260101T120000Z", "--yes")
	if err != nil {
		t.Fatalf("rollback: %v\n%s", err, out)
	}
	mustContain(t, out,
		"2 file(s) restored",
		"2 current jar(s) archived",
		"the restore is undoable",
		"the snapshot's copy replaced the current jar",
		"1 jar(s) in", removedBucket,
		"came from `remove --keep-files` and were not touched")

	// The --quiet form has to name the snapshot it restored from, or a script
	// reading only that line cannot tell which of several it undid. A second
	// instance, because the run above spent its snapshot by restoring it.
	instID, _ = rollbackInst(t,
		map[string]string{"sodium.jar": "current"},
		map[string][]string{"20260101T120000Z": {"sodium.jar", "lithium.jar"}})
	out, err = captureCLI(t, "rollback", instID, "--yes", "--quiet")
	if err != nil {
		t.Fatalf("rollback --quiet: %v\n%s", err, out)
	}
	mustContain(t, out, "restored 2 file(s) from 20260101T120000Z")
}

func TestRollbackRejectsAnUnknownSnapshotAndNamesTheOnesThatExist(t *testing.T) {
	instID, _ := rollbackInst(t, nil, map[string][]string{
		"20260101T120000Z":   {"sodium.jar"},
		"20260101T120000Z-1": {"sodium.jar"},
	})

	// executeCLI, not captureCLI: a failing command writes its message in
	// Execute, and testing root.Execute() would skip exactly that (B1).
	code, _, stderr := executeCLI(t, "rollback", instID, "--to", "20260102T000000Z")
	if code == 0 {
		t.Errorf("exit code = 0, want non-zero for an unknown snapshot")
	}
	mustContain(t, stderr, `no snapshot named "20260102T000000Z"`,
		"20260101T120000Z-1", "20260101T120000Z")

	// A prefix must not be accepted either: the name the user typed has to be
	// the name they get.
	if code, _, _ := executeCLI(t, "rollback", instID, "--to", "20260101"); code == 0 {
		t.Error("rollback accepted a timestamp prefix")
	}
}

func TestRollbackToAnExactDisambiguatedName(t *testing.T) {
	instID, modsDir := rollbackInst(t, nil, map[string][]string{
		"20260101T120000Z":   {"sodium.jar"},
		"20260101T120000Z-1": {"lithium.jar"},
	})

	out, err := captureCLI(t, "rollback", instID, "--to", "20260101T120000Z-1", "--yes", "--json")
	if err != nil {
		t.Fatalf("rollback: %v\n%s", err, out)
	}
	mustContain(t, out, `"snapshot": "20260101T120000Z-1"`)
	if got := readJar(t, filepath.Join(modsDir, "lithium.jar")); got != "snapshot:lithium.jar" {
		t.Errorf("lithium.jar = %q, want the -1 snapshot's copy", got)
	}
	if _, err := os.Stat(filepath.Join(modsDir, "sodium.jar")); !os.IsNotExist(err) {
		t.Error("the other snapshot was restored too")
	}
}

// ─── the reporting helpers ──────────────────────────────────────────────────

func TestCountBucket(t *testing.T) {
	root := t.TempDir()
	snapDir(t, root, removedBucket, "a.jar", "b.jar")
	if got := countBucket(root, removedBucket); got != 2 {
		t.Errorf("countBucket = %d, want 2", got)
	}
	// A bucket that was never created is zero, not an error.
	if got := countBucket(root, "duplicates"); got != 0 {
		t.Errorf("countBucket(missing bucket) = %d, want 0", got)
	}
	// A stray file in the bucket is not a jar, so it is not counted.
	if err := os.WriteFile(filepath.Join(root, removedBucket, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := countBucket(root, removedBucket); got != 2 {
		t.Errorf("countBucket with a stray file = %d, want 2", got)
	}
	// A missing backup root is zero too, so the message does not claim a count.
	if got := countBucket(filepath.Join(t.TempDir(), "absent"), removedBucket); got != 0 {
		t.Errorf("countBucket(missing root) = %d, want 0", got)
	}
}

// The JSON shape is a compatibility surface, and an empty list must encode as
// [] rather than null — the same trap as `add --json`.
func TestSnapshotRows(t *testing.T) {
	rows := snapshotRows([]snapshot{
		{Name: "20260101T120000Z", Files: []string{"a.jar", "b.jar"}},
		{Name: "20260101T120001Z", Files: []string{"c.jar"}},
	})
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0]["timestamp"] != "20260101T120000Z" || rows[0]["files"] != 2 {
		t.Errorf("rows[0] = %v", rows[0])
	}
	names, ok := rows[0]["names"].([]string)
	if !ok || len(names) != 2 {
		t.Errorf("names = %v, want the file list", rows[0]["names"])
	}

	empty := snapshotRows(nil)
	if empty == nil {
		t.Error("snapshotRows(nil) = nil, want an empty slice so it encodes as []")
	}
	if len(empty) != 0 {
		t.Errorf("snapshotRows(nil) has %d rows", len(empty))
	}
}

func TestSnapshotBytesSumsEverySnapshot(t *testing.T) {
	root := t.TempDir()
	snapDir(t, root, "20260101T120000Z", "aaaa.jar")
	snapDir(t, root, "20260102T120000Z", "bb.jar", "c.jar")
	snaps, err := loadSnapshots(root)
	if err != nil {
		t.Fatal(err)
	}
	// snapDir writes "snapshot:"+name, so the total is that prefix per file.
	want := int64(len("snapshot:aaaa.jar") + len("snapshot:bb.jar") + len("snapshot:c.jar"))
	if got := snapshotBytes(snaps); got != want {
		t.Errorf("snapshotBytes = %d, want %d", got, want)
	}
	// A file that has vanished is skipped rather than failing the whole total.
	if err := os.Remove(filepath.Join(root, "20260101T120000Z", "aaaa.jar")); err != nil {
		t.Fatal(err)
	}
	if got, want := snapshotBytes(snaps), want-int64(len("snapshot:aaaa.jar")); got != want {
		t.Errorf("snapshotBytes after a removal = %d, want %d", got, want)
	}
	if got := snapshotBytes(nil); got != 0 {
		t.Errorf("snapshotBytes(nil) = %d, want 0", got)
	}
}

func TestSnapshotAge(t *testing.T) {
	if got := snapshotAge(time.Time{}); got != "—" {
		t.Errorf("snapshotAge(zero) = %q, want an em dash", got)
	}
	now := time.Now()
	if got := snapshotAge(now.Add(-3 * time.Hour)); !strings.Contains(got, "ago") || got == "—" {
		t.Errorf("snapshotAge(3h ago) = %q", got)
	}
}

// ─── the machine-readable result ────────────────────────────────────────────

func TestRollbackResultProjectsEveryField(t *testing.T) {
	inst := &instance.Info{ID: "26.3-fabric-mod"}
	plan := rollbackPlan{
		Restore:   []string{"sodium.jar"},
		Overwrite: []string{"sodium.jar"},
		Archive:   []string{"sodium.jar", "lithium.jar"},
	}
	got := rollbackResult(inst, "/mods", "/backups", snapshot{Name: "20260101T120000Z"},
		plan, true, 2, 5, 1)

	if got.Instance != "26.3-fabric-mod" || got.ModsDir != "/mods" || got.BackupDir != "/backups" {
		t.Errorf("paths = %+v", got)
	}
	if got.Snapshot != "20260101T120000Z" || !got.DryRun {
		t.Errorf("snapshot/dryRun = %q/%v", got.Snapshot, got.DryRun)
	}
	if len(got.Restored) != 1 || len(got.Overwritten) != 1 {
		t.Errorf("restored/overwritten = %v/%v", got.Restored, got.Overwritten)
	}
	if got.Archived != 2 || got.Snapshots != 5 || got.RemovedFiles != 1 {
		t.Errorf("counts = %d/%d/%d", got.Archived, got.Snapshots, got.RemovedFiles)
	}
	// Archive is a plan field, not a result field: it is what happens, not what
	// was reported as done.
	if len(got.Restored) != 1 {
		t.Error("Restored must come from the plan, not from Archive")
	}
}

func TestContains(t *testing.T) {
	list := []string{"a.jar", "b.jar"}
	for _, c := range []struct {
		in   string
		want bool
	}{{"a.jar", true}, {"b.jar", true}, {"c.jar", false}, {"", false}, {"a", false}} {
		if got := contains(list, c.in); got != c.want {
			t.Errorf("contains(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	if contains(nil, "a.jar") {
		t.Error("contains(nil, ...) = true")
	}
}
