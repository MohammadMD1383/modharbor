package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B2: `add --dry-run` used to resolve and then install anyway, then print a
// hint claiming nothing had been written. The plan must be reported without any
// download and without a single byte changing in mods/.
func TestAddDryRunDownloadsNothing(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	// Sodium is not installed here, so the command has something to plan and
	// the write it used to perform would be visible.
	mcDir, instPath := newTestInstance(t, instID, map[string][]byte{
		"unrelated.jar": []byte("some other mod"),
	})
	writeTestConfig(t, mcDir, fx.API())
	modsDir := filepath.Join(instPath, "mods")
	before := snapshotDir(t, modsDir)

	out, err := captureCLI(t, "add", sodiumSlug, instID, "--dry-run")
	if err != nil {
		t.Fatalf("add --dry-run: %v\n%s", err, out)
	}

	if n := fx.downloads(); n != 0 {
		t.Errorf("dry run downloaded %d jar(s); it must not download at all", n)
	}
	assertDirUnchanged(t, before, snapshotDir(t, modsDir))

	// The summary has to say what actually happened. "installed" here would
	// be the original bug in its printed form.
	mustContain(t, out, "would install 1 mod(s)")
	mustNotContain(t, out, "installed 1 mod(s)")
}

// A dry run must be reproducible as a real one: the same request afterwards
// installs exactly what was planned, and only then.
func TestAddAfterDryRunInstalls(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, instPath := newTestInstance(t, instID, map[string][]byte{
		"unrelated.jar": []byte("some other mod"),
	})
	writeTestConfig(t, mcDir, fx.API())
	modsDir := filepath.Join(instPath, "mods")

	out, err := captureCLI(t, "add", sodiumSlug, instID)
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if fx.downloads() == 0 {
		t.Errorf("a real run downloaded nothing\n%s", out)
	}
	mustContain(t, out, "installed 1 mod(s)")
	mustNotContain(t, out, "would install")

	body, err := os.ReadFile(filepath.Join(modsDir, sodiumNewFile))
	if err != nil {
		t.Fatalf("%s was not installed: %v", sodiumNewFile, err)
	}
	if string(body) != sodiumNewJar {
		t.Errorf("%s = %q, want %q", sodiumNewFile, body, sodiumNewJar)
	}
}

// B3: `-i/--instance` was silently dropped, because splitArgs returned an empty
// reference whenever the last argument did not look like an instance and nothing
// then fell back to the global flag.
func TestSplitArgsHonoursInstanceFlag(t *testing.T) {
	orig := flagInstance
	t.Cleanup(func() { flagInstance = orig })

	tests := []struct {
		name         string
		args         []string
		flagInstance string
		wantInst     string
		wantProjects []string
	}{
		{
			name:         "positional instance still wins",
			args:         []string{"sodium", "26.3-fabric-mod"},
			flagInstance: "other-instance",
			wantInst:     "26.3-fabric-mod",
			wantProjects: []string{"sodium"},
		},
		{
			name:         "falls back to the flag",
			args:         []string{"sodium"},
			flagInstance: "26.3-fabric-mod",
			wantInst:     "26.3-fabric-mod",
			wantProjects: []string{"sodium"},
		},
		{
			name:         "several projects still fall back",
			args:         []string{"sodium", "lithium"},
			flagInstance: "26.3-fabric-mod",
			wantInst:     "26.3-fabric-mod",
			wantProjects: []string{"sodium", "lithium"},
		},
		{
			name:         "path form keeps working",
			args:         []string{"sodium", "./versions/26.3-fabric-mod"},
			flagInstance: "other-instance",
			wantInst:     "./versions/26.3-fabric-mod",
			wantProjects: []string{"sodium"},
		},
		{
			// Empty here is correct: ResolveInstance turns it into the config
			// default, which is the third link in the chain.
			name:         "empty without a flag defers to the config",
			args:         []string{"sodium"},
			flagInstance: "",
			wantInst:     "",
			wantProjects: []string{"sodium"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			flagInstance = tc.flagInstance
			gotInst, gotProjects := splitArgs(tc.args)
			if gotInst != tc.wantInst {
				t.Errorf("instance = %q, want %q", gotInst, tc.wantInst)
			}
			if strings.Join(gotProjects, ",") != strings.Join(tc.wantProjects, ",") {
				t.Errorf("projects = %v, want %v", gotProjects, tc.wantProjects)
			}
		})
	}
}

// The two spellings of the same request must resolve to the same instance, not
// merely to a plausible one.
//
// Before the fix the -i form failed outright: splitArgs returned an empty
// reference, ResolveInstance fell through to an unset default, and the command
// aborted with "no instance given".
func TestAddFlagAndPositionalResolveSameInstance(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, _ := newTestInstance(t, instID, nil)
	writeTestConfig(t, mcDir, fx.API())

	// The instance label is what both renderings echo back, so asserting on it
	// covers both spellings without depending on the JSON shape.
	label := instID + " (MC 26.3, Fabric)"

	byFlag, err := captureCLI(t, "add", "-i", instID, sodiumSlug, "--dry-run")
	if err != nil {
		t.Fatalf("add -i %s %s: %v\n%s", instID, sodiumSlug, err, byFlag)
	}
	byPositional, err := captureCLI(t, "add", sodiumSlug, instID, "--dry-run")
	if err != nil {
		t.Fatalf("add %s %s: %v\n%s", sodiumSlug, instID, err, byPositional)
	}

	mustContain(t, byFlag, label)
	mustContain(t, byPositional, label)

	// Both spellings must also agree that nothing was written.
	mustContain(t, byFlag, "would install 1 mod(s)")
	mustContain(t, byPositional, "would install 1 mod(s)")
}

// B4: `update` planned by calling the engine without DryRun, so the first pass
// applied everything and the second found nothing left to do — reporting
// "nothing changed" after a successful update.
func TestUpdateAllAppliesAndReports(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, instPath := newTestInstance(t, instID, map[string][]byte{
		sodiumOldFile: []byte(sodiumOldJar),
	})
	writeTestConfig(t, mcDir, fx.API())
	modsDir := filepath.Join(instPath, "mods")

	out, err := captureCLI(t, "update", instID, "--all")
	if err != nil {
		t.Fatalf("update --all: %v\n%s", err, out)
	}

	// The summary must come from the pass that wrote, so it has to count the
	// replacement rather than reporting an empty run.
	mustContain(t, out, "1 mod(s) updated")
	mustNotContain(t, out, "nothing changed")

	// And the disk must agree with the report.
	newPath := filepath.Join(modsDir, sodiumNewFile)
	body, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("%s was not installed: %v", sodiumNewFile, err)
	}
	if string(body) != sodiumNewJar {
		t.Errorf("%s = %q, want %q", sodiumNewFile, body, sodiumNewJar)
	}
	if _, err := os.Stat(filepath.Join(modsDir, sodiumOldFile)); !os.IsNotExist(err) {
		t.Errorf("the superseded %s is still in mods/", sodiumOldFile)
	}
}

// The replaced jar must be recoverable, which is the whole reason update keeps
// a backup directory in the first place.
func TestUpdateAllBacksUpReplacedJar(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, instPath := newTestInstance(t, instID, map[string][]byte{
		sodiumOldFile: []byte(sodiumOldJar),
	})
	writeTestConfig(t, mcDir, fx.API())

	if _, err := captureCLI(t, "update", instID, "--all"); err != nil {
		t.Fatalf("update --all: %v", err)
	}

	backupRoot := filepath.Join(instPath, "mods", ".modharbor-backup")
	entries, err := os.ReadDir(backupRoot)
	if err != nil {
		t.Fatalf("no backup directory at %s: %v", backupRoot, err)
	}
	var found bool
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(backupRoot, e.Name(), sodiumOldFile))
		if err == nil && string(body) == sodiumOldJar {
			found = true
		}
	}
	if !found {
		t.Errorf("%s was not preserved in %s", sodiumOldFile, backupRoot)
	}
}

// The plan pass must stay read-only for `update --dry-run` too, otherwise the
// flag would be as good as decorative here.
func TestUpdateDryRunWritesNothing(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, instPath := newTestInstance(t, instID, map[string][]byte{
		sodiumOldFile: []byte(sodiumOldJar),
	})
	writeTestConfig(t, mcDir, fx.API())
	modsDir := filepath.Join(instPath, "mods")
	before := snapshotDir(t, modsDir)

	out, err := captureCLI(t, "update", instID, "--dry-run")
	if err != nil {
		t.Fatalf("update --dry-run: %v\n%s", err, out)
	}

	if n := fx.downloads(); n != 0 {
		t.Errorf("update --dry-run downloaded %d jar(s)", n)
	}
	assertDirUnchanged(t, before, snapshotDir(t, modsDir))
	mustContain(t, out, "mods/ is unchanged")
}
