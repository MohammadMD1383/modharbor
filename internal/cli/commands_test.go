package cli

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MohammadMD1383/modharbor/internal/ui"
)

// B1, and the most expensive defect in the project's history: `fail` wrapped
// its message in a silentError, and Execute returned that error's exit code
// without ever printing it. Every failing command therefore exited 1 having
// written nothing at all — no diagnostic on stdout, none on stderr, and in a
// script nothing to grep. The exit code alone made it look like the tool had
// reported the problem.
//
// Asserting only the exit code would pass against the broken build, so this
// checks the bytes: a non-zero exit must come with a message on stderr naming
// what could not be resolved.
func TestMissingInstanceExitsNonZeroWithAMessageOnStderr(t *testing.T) {
	const missing = "no-such-instance"

	fx := newFakeModrinth(t)
	mcDir, _ := newTestInstance(t, "26.3-fabric-mod", nil)
	writeTestConfig(t, mcDir, fx.API())

	code, stdout, stderr := executeCLI(t, "list", missing)

	if code == 0 {
		t.Errorf("exit code = 0 for a nonexistent instance, want non-zero\nstderr: %s", stderr)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Fatal("stderr is empty: the command failed silently, which is bug B1")
	}
	// The message has to say which reference failed, or the user has nothing
	// to act on beyond the fact that something happened.
	mustContain(t, stderr, missing, ui.SymFail)

	// Nothing was rendered, so there is no partial report on stdout to be
	// mistaken for a successful empty result.
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout should be empty for a command that could not start, got:\n%s", stdout)
	}
}

// The message a failing command emits is produced by Execute, not by the
// command. Running the same command through root.Execute() alone must therefore
// yield no output whatsoever — which is exactly what B1 looked like from the
// inside, and what makes captureCLI the wrong tool for testing error paths.
func TestFailureOutputIsProducedByExecuteNotByTheCommand(t *testing.T) {
	fx := newFakeModrinth(t)
	mcDir, _ := newTestInstance(t, "26.3-fabric-mod", nil)
	writeTestConfig(t, mcDir, fx.API())

	out, runErr := captureCLI(t, "list", "no-such-instance")
	if runErr == nil {
		t.Fatal("expected the command to return an error")
	}
	if !strings.Contains(runErr.Error(), "no-such-instance") {
		t.Errorf("the returned error does not name the missing instance: %v", runErr)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("the command itself printed something while failing:\n%s", out)
	}

	// And the same failure through the real entry point does speak. If this
	// ever goes quiet, Execute has stopped rendering errors again.
	code, _, stderr := executeCLI(t, "list", "no-such-instance")
	if code == 0 {
		t.Error("exit code = 0 through Execute, want non-zero")
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("Execute printed nothing for a failing command")
	}
}

// With no instance named and none configured, the user gets one chance to work
// out what to do. Whichever message ResolveInstance produces, the requirement
// is the same: something on stderr, and something that names the missing
// setting rather than the failing command.
func TestNoInstanceAtAllStillExplainsItself(t *testing.T) {
	fx := newFakeModrinth(t)
	mcDir, _ := newTestInstance(t, "26.3-fabric-mod", nil)
	writeTestConfig(t, mcDir, fx.API())

	code, _, stderr := executeCLI(t, "list")

	if code == 0 {
		t.Errorf("exit code = 0 with no instance at all, want non-zero\nstderr: %s", stderr)
	}
	mustContain(t, stderr, "no instance")
	// It has to name the setting that would fix it, not just report absence.
	mustContain(t, stderr, "defaultInstance")
}

// `list --json` is what scripts read to decide whether an instance is healthy,
// so its counts have to add up and agree with the rows they summarise. A
// mismatch here is not cosmetic: it is a script acting on mods it thinks are
// unidentified.
//
// The payload is decoded into the production scanJSON type, so a renamed field
// or a changed tag breaks this test rather than quietly reshaping the wire
// format for every consumer.
func TestListJsonCountsAgreeWithTheRowsTheySummarise(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, _ := newTestInstance(t, instID, map[string][]byte{
		// Resolvable by hash against the fake API.
		sodiumOldFile: []byte(sodiumOldJar),
		// A real jar the API knows nothing about, so it must land in the
		// unresolved bucket rather than being dropped.
		"unknownmod-1.0.jar": fabricJar(t, "unknownmod", "Unknown Mod", "1.0", ""),
	})
	writeTestConfig(t, mcDir, fx.API())

	out, err := captureCLI(t, "list", instID, "--json")
	if err != nil {
		t.Fatalf("list --json: %v\n%s", err, out)
	}

	var got scanJSON
	decodeJSON(t, out, &got)

	if got.Instance != instID {
		t.Errorf("instance = %q, want %q", got.Instance, instID)
	}
	if got.MCVersion != sodiumGameVsn {
		t.Errorf("mcVersion = %q, want %q", got.MCVersion, sodiumGameVsn)
	}
	// The loader has to be reported as the loader, not as the raw enum name.
	if got.Loader != "fabric" {
		t.Errorf("loader = %q, want %q", got.Loader, "fabric")
	}

	if got.Total != 2 {
		t.Errorf("total = %d, want 2", got.Total)
	}
	if got.Identified != 1 {
		t.Errorf("identified = %d, want 1", got.Identified)
	}
	if got.Unresolved != 1 {
		t.Errorf("unresolved = %d, want 1", got.Unresolved)
	}

	// The counts are derived from the rows, so verify they are rather than
	// trusting that the same loop filled both.
	if got.Total != len(got.Mods) {
		t.Errorf("total = %d but %d mods were listed", got.Total, len(got.Mods))
	}
	var identified, unresolved int
	for _, m := range got.Mods {
		if m.Method == "unmatched" {
			unresolved++
			continue
		}
		identified++
	}
	if identified != got.Identified || unresolved != got.Unresolved {
		t.Errorf("rows give identified=%d unresolved=%d, header says %d and %d",
			identified, unresolved, got.Identified, got.Unresolved)
	}

	// An unidentified jar is still installed and still occupies disk, so it
	// has to appear in the listing with a size.
	var sawUnknown bool
	for _, m := range got.Mods {
		switch m.FileName {
		case "unknownmod-1.0.jar":
			sawUnknown = true
			if m.Method != "unmatched" {
				t.Errorf("%s method = %q, want unmatched", m.FileName, m.Method)
			}
			if m.Size == 0 {
				t.Errorf("%s reported a size of 0; sizes drive the total shown to the user", m.FileName)
			}
		case sodiumOldFile:
			if m.ProjectID != sodiumProjectID {
				t.Errorf("%s projectId = %q, want %q", m.FileName, m.ProjectID, sodiumProjectID)
			}
			if m.Method != "hash" {
				t.Errorf("%s method = %q, want hash", m.FileName, m.Method)
			}
			if m.Version != sodiumOldNo {
				t.Errorf("%s version = %q, want %q", m.FileName, m.Version, sodiumOldNo)
			}
		}
	}
	if !sawUnknown {
		t.Error("the unidentified jar is missing from the listing")
	}
}

// The action is the field a script acts on: replace swaps a jar for a newer
// build, reinstall re-fetches the same version. Treating one as the other
// either downgrades a mod or claims an upgrade that never happened. They are
// distinguished here by an exactly equal version pair, which is the case a
// naive "is the version newer?" check gets wrong.
//
// Decoded into the production outdatedJSON type so a renamed field breaks this
// test rather than reshaping the wire format unnoticed.
func TestOutdatedJsonSeparatesReinstallFromReplace(t *testing.T) {
	const instID = "26.3-fabric-mod"

	cases := []struct {
		name       string
		file       string
		body       []byte
		wantAction string
		wantFrom   string
		wantTo     string
	}{
		{
			// An older build is installed and a newer one is published:
			// the ordinary upgrade.
			name:       "older installed jar is a replace",
			file:       sodiumOldFile,
			body:       []byte(sodiumOldJar),
			wantAction: "replace",
			wantFrom:   sodiumOldNo,
			wantTo:     sodiumNewNo,
		},
		{
			// Same version number, different bytes — a mirror or repackage.
			// Re-fetching it is the fix; calling it an upgrade would imply
			// the user was missing something.
			name:       "same version with different bytes is a reinstall",
			file:       sodiumMirrorFile,
			body:       []byte(sodiumMirrorJar),
			wantAction: "reinstall",
			wantFrom:   sodiumMirrorNo,
			wantTo:     sodiumNewNo,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFakeModrinth(t)
			mcDir, instPath := newTestInstance(t, instID, map[string][]byte{tc.file: tc.body})
			writeTestConfig(t, mcDir, fx.API())
			modsDir := modsDirOf(instPath)
			before := snapshotDir(t, modsDir)

			out, err := captureCLI(t, "outdated", instID, "--json")
			if err != nil {
				t.Fatalf("outdated --json: %v\n%s", err, out)
			}

			var got outdatedJSON
			decodeJSON(t, out, &got)

			if got.Total != 1 {
				t.Errorf("total = %d, want 1", got.Total)
			}
			if got.Current != 0 {
				t.Errorf("current = %d, want 0: nothing was up to date", got.Current)
			}
			if len(got.Updates) != 1 {
				t.Fatalf("got %d update(s), want 1: %s", len(got.Updates), out)
			}

			u := got.Updates[0]
			if u.Action != tc.wantAction {
				t.Errorf("action = %q, want %q", u.Action, tc.wantAction)
			}
			if u.Current != tc.wantFrom {
				t.Errorf("current = %q, want %q", u.Current, tc.wantFrom)
			}
			if u.Available != tc.wantTo {
				t.Errorf("available = %q, want %q", u.Available, tc.wantTo)
			}
			if u.Mod != sodiumTitle {
				t.Errorf("mod = %q, want %q", u.Mod, sodiumTitle)
			}
			// The whole point of the reinstall action is that the versions
			// match; if they did not, this row would be a replace.
			if tc.wantAction == "reinstall" && u.Current != u.Available {
				t.Errorf("reinstall row has current %q and available %q; the versions must be equal",
					u.Current, u.Available)
			}

			// Planning is read-only. A command that wrote to mods/ while
			// reporting would destroy the before/after evidence every other
			// test in this package relies on.
			assertDirUnchanged(t, before, snapshotDir(t, modsDir))
			if n := fx.downloads(); n != 0 {
				t.Errorf("outdated --json downloaded %d jar(s); it must only plan", n)
			}
		})
	}
}

// A mod with no build for this game version is neither an update nor a no-op.
// Folding it into "current" would make `outdated` certify an instance whose mod
// cannot run on that Minecraft version, so it must be counted as seen and
// offered as nothing.
func TestOutdatedJsonCountsAModWithNoCompatibleBuildWithoutOfferingIt(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	// The project exists but publishes no versions at all.
	mcDir, _ := newTestInstance(t, instID, map[string][]byte{
		lithiumFile: []byte(lithiumJar),
	})
	writeTestConfig(t, mcDir, fx.API())

	out, err := captureCLI(t, "outdated", instID, "--json")
	if err != nil {
		t.Fatalf("outdated --json: %v\n%s", err, out)
	}

	var got outdatedJSON
	decodeJSON(t, out, &got)

	if got.Total != 1 {
		t.Errorf("total = %d, want 1: the jar was seen", got.Total)
	}
	if len(got.Updates) != 0 {
		t.Errorf("got %d update(s) for a mod with no compatible build, want 0: %s",
			len(got.Updates), out)
	}
	// It must not be counted as current either.
	if got.Current != 0 {
		t.Errorf("current = %d, want 0", got.Current)
	}
	// This payload has no field carrying the reason it was dropped, so a
	// script asking "why is this mod not in updates?" has no answer here. The
	// next test covers the one place the reason does reach the user.
}

// The skip reason has to reach the user somewhere, and doctor is that place:
// the mod resolves cleanly by hash, so nothing else in a run would mention it.
func TestDoctorExplainsAModWithNoBuildForThisMinecraftVersion(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	// Fixture bytes rather than a built jar: the fake API recognises this
	// exact content by hash, and a mod that does not resolve at all would
	// never reach the version query that produces the explanation.
	mcDir, _ := newTestInstance(t, instID, map[string][]byte{
		lithiumFile: []byte(lithiumJar),
	})
	writeTestConfig(t, mcDir, fx.API())

	var sawSkip bool
	for _, f := range diagnoseInstance(t, instID) {
		if !strings.Contains(f.title, lithiumTitle) || !strings.Contains(f.title, "no build") {
			continue
		}
		sawSkip = true
		// A missing build is a warning, not an error: the instance still
		// starts, and calling it an error would make `doctor --fix` and the
		// exit status cry wolf.
		if f.severity != sevWarn {
			t.Errorf("severity = %q, want %q", f.severity, sevWarn)
		}
		if f.fix == "" {
			t.Error("the finding offers no way to resolve it")
		}
	}
	if !sawSkip {
		t.Error("no finding explaining that " + lithiumTitle + " has no build")
	}
}

// Two jars providing the same mod id is the failure that stops Minecraft
// starting with no error message at all, so it is an error rather than a
// warning, and the fix has to name both jars — "remove a duplicate" gives the
// user no way to tell which one to delete, and deleting the wrong one leaves
// the instance just as broken.
func TestDoctorDiagnosesDuplicateJarsAsAnError(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, instPath := newTestInstance(t, instID, map[string][]byte{
		"old-copy.jar": fabricJar(t, sodiumModID, sodiumModName, "0.9.0", ""),
		"new-copy.jar": fabricJar(t, sodiumModID, sodiumModName, "0.10.0", ""),
	})
	// doctor keeps the most recently modified jar, so the timestamps decide
	// which one the finding names. Left equal, the winner is arbitrary.
	setModTime(t, filepath.Join(modsDirOf(instPath), "old-copy.jar"), timeAgo(2*time.Hour))
	setModTime(t, filepath.Join(modsDirOf(instPath), "new-copy.jar"), timeAgo(1*time.Hour))
	writeTestConfig(t, mcDir, fx.API())

	findings := diagnoseInstance(t, instID)
	var dup *finding
	for i := range findings {
		if strings.Contains(findings[i].title, sodiumModID) &&
			strings.Contains(findings[i].title, "2 jars") {
			dup = &findings[i]
		}
	}
	if dup == nil {
		t.Fatal("no duplicate finding for two jars providing the same mod id")
	}

	if dup.severity != sevError {
		t.Errorf("severity = %q, want %q", dup.severity, sevError)
	}
	if dup.detail == "" {
		t.Error("the finding does not say what the duplicate does to the loader")
	}
	if !strings.Contains(dup.fix, "new-copy.jar") || !strings.Contains(dup.fix, "old-copy.jar") {
		t.Errorf("fix %q does not name both the kept and the removed jar", dup.fix)
	}
	// The newer jar is the one to keep. Getting this backwards makes the
	// user delete the copy that is actually the newer build.
	if !strings.Contains(dup.fix, "keep") || !strings.Contains(dup.fix, "new-copy.jar") {
		t.Errorf("fix %q should keep the newer jar", dup.fix)
	}
}

// The counts in a doctor report are what a script reads first, so this pins
// them. It used to assert the opposite of the truth about the exit status: an
// error finding made `doctor --json` report success, which is defect B20. The
// status itself, and the finding bodies that now reach the wire, are asserted
// in json_test.go.
func TestDoctorJsonReportsAnErrorCountForDuplicateJars(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, _ := newTestInstance(t, instID, map[string][]byte{
		"old-copy.jar": fabricJar(t, sodiumModID, sodiumModName, "0.9.0", ""),
		"new-copy.jar": fabricJar(t, sodiumModID, sodiumModName, "0.10.0", ""),
	})
	writeTestConfig(t, mcDir, fx.API())

	// The failure is the expected verdict here: an error finding fails the run
	// whether or not the caller asked for JSON. The payload still has to be
	// whole on stdout for the counts below to be readable at all.
	out, err := captureCLI(t, "doctor", instID, "--json")
	var silent *silentError
	if !errors.As(err, &silent) {
		t.Fatalf("doctor --json: %v\n%s", err, out)
	}

	rep := decodeDoctorReport(t, out)
	if rep.Instance != instID {
		t.Errorf("instance = %q, want %q", rep.Instance, instID)
	}
	if rep.Errors != 1 {
		t.Errorf("errors = %d, want 1 for a duplicate mod", rep.Errors)
	}
	if rep.Warnings != 0 {
		t.Errorf("warnings = %d, want 0", rep.Warnings)
	}
}

// doctorReportJSON mirrors the summary half of the wire form of a doctor
// report. Findings stay untyped because this test is about the counts;
// json_test.go decodes the finding bodies, which is where the shape of a
// finding on the wire is pinned.
type doctorReportJSON struct {
	Instance string
	Errors   int
	Warnings int
	Findings []struct{}
}

func decodeDoctorReport(t *testing.T, out string) doctorReportJSON {
	t.Helper()
	var rep doctorReportJSON
	decodeJSON(t, out, &rep)
	return rep
}

// diagnoseInstance runs the exact discovery `doctor` runs and returns the
// findings with their severities intact.
func diagnoseInstance(t *testing.T, instID string) []finding {
	t.Helper()

	// doctor skips its compatibility pass under --offline; pin the flag
	// because the package-level globals outlive any single command.
	origOffline := flagOffline
	flagOffline = false
	t.Cleanup(func() { flagOffline = origOffline })

	a, err := bootstrap()
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	inst, err := a.ResolveInstance(instID)
	if err != nil {
		t.Fatalf("resolve %s: %v", instID, err)
	}
	ctx := context.Background()
	rows, err := scanInstance(ctx, a, inst, false)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	findings, err := diagnose(ctx, a, inst, rows)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	return findings
}

// `--fix` moves the shadowed jar aside rather than deleting it, and the report
// still has to name which one survived so the two agree.
func TestDoctorFixPreservesTheDuplicateItMovedAside(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, instPath := newTestInstance(t, instID, map[string][]byte{
		"old-copy.jar": fabricJar(t, sodiumModID, sodiumModName, "0.9.0", ""),
		"new-copy.jar": fabricJar(t, sodiumModID, sodiumModName, "0.10.0", ""),
	})
	setModTime(t, filepath.Join(modsDirOf(instPath), "old-copy.jar"), timeAgo(2*time.Hour))
	setModTime(t, filepath.Join(modsDirOf(instPath), "new-copy.jar"), timeAgo(1*time.Hour))
	writeTestConfig(t, mcDir, fx.API())

	// doctor exits non-zero when it found errors, and deliberately says
	// nothing further: the findings were already printed. Only the sentinel
	// "exit 1, no message" error is acceptable here, which is the one case
	// where a silent failure is correct.
	_, err := captureCLI(t, "doctor", instID, "--fix")
	if err == nil {
		t.Error("doctor --fix exited 0 after reporting an error-severity duplicate")
	} else if strings.TrimSpace(err.Error()) != "" {
		t.Fatalf("doctor --fix printed an error on top of its findings: %q", err)
	}

	modsDir := modsDirOf(instPath)
	if _, err := os.Stat(filepath.Join(modsDir, "new-copy.jar")); err != nil {
		t.Errorf("the newer jar was removed instead of kept: %v", err)
	}
	// Nothing is deleted: the jar the heuristic dropped has to be recoverable,
	// because the heuristic is a heuristic.
	//
	// The archive is read through archive/zip rather than grepped. A jar's
	// entries are deflated, so the literal version string appears in the file
	// bytes only when the compressor happened to choose to store it — which
	// varies with the Go version building the fixture. Asserting on the raw
	// bytes passed on Go 1.27 and failed in CI, which is the test lying rather
	// than the code being wrong.
	preserved, err := zip.OpenReader(filepath.Join(modsDir, ".modharbor-backup", "duplicates", "old-copy.jar"))
	if err != nil {
		t.Fatalf("the shadowed jar was not preserved: %v", err)
	}
	defer preserved.Close()

	var got string
	for _, f := range preserved.File {
		if f.Name != "fabric.mod.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("opening %s: %v", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("reading %s: %v", f.Name, err)
		}
		got = string(b)
	}
	if !strings.Contains(got, `"version":"0.9.0"`) {
		t.Errorf("the preserved jar is not the one that was moved; fabric.mod.json = %s", got)
	}
}

func timeAgo(d time.Duration) time.Time { return time.Now().Add(-d) }
