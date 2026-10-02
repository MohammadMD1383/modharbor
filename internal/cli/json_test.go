package cli

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file covers the two halves of `doctor --json` that automation depends
// on: that every finding says something, and that the exit status tells the
// truth. Both were false and neither showed up in the human path, which is why
// the report is worth nothing to a script today.

// doctorJSONReport is the wire contract a consumer of `doctor --json` relies
// on. Findings are decoded into a struct of their own on purpose: the
// production report holds them behind an unexported struct, so a test that
// asserted only the counts would pass happily against a payload of `{}`.
type doctorJSONReport struct {
	Instance  string `json:"instance"`
	MCVersion string `json:"mcVersion"`
	Loader    string `json:"loader"`
	Errors    int    `json:"errors"`
	Warnings  int    `json:"warnings"`
	Findings  []struct {
		Severity string `json:"severity"`
		Title    string `json:"title"`
		Detail   string `json:"detail"`
		Fix      string `json:"fix"`
	} `json:"findings"`
}

// finding returns the first finding whose title contains needle.
func (r doctorJSONReport) finding(needle string) (int, bool) {
	for i, f := range r.Findings {
		if strings.Contains(f.Title, needle) {
			return i, true
		}
	}
	return 0, false
}

// duplicateJars builds the smallest instance with an error finding: two jars
// claiming the same mod id, which is a problem the loader cannot recover from.
//
// The timestamps decide which jar the finding tells the user to keep, so they
// are pinned; left equal, the assertion would be about an arbitrary winner.
func duplicateJars(t *testing.T) (mcDir, instPath string) {
	t.Helper()
	mcDir, instPath = newTestInstance(t, "26.3-fabric-mod", map[string][]byte{
		"old-copy.jar": fabricJar(t, sodiumModID, sodiumModName, "0.9.0", ""),
		"new-copy.jar": fabricJar(t, sodiumModID, sodiumModName, "0.10.0", ""),
	})
	setModTime(t, filepath.Join(modsDirOf(instPath), "old-copy.jar"), timeAgo(2*time.Hour))
	setModTime(t, filepath.Join(modsDirOf(instPath), "new-copy.jar"), timeAgo(1*time.Hour))
	return mcDir, instPath
}

// A finding that reaches the wire as `{}` leaves a script with the counts and
// nothing else: it cannot tell which jar to remove, or why the instance will
// not start. Severity, title, detail and fix all have to survive the trip.
func TestDoctorJSONCarriesTheWholeFinding(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, _ := duplicateJars(t)
	writeTestConfig(t, mcDir, fx.API())

	out, err := captureCLI(t, "doctor", instID, "--json")
	// The run is expected to fail: one error finding is a failure in both
	// paths. The payload still has to be complete for the decoding below.
	var silent *silentError
	if !errors.As(err, &silent) {
		t.Fatalf("doctor --json: %v\n%s", err, out)
	}

	var rep doctorJSONReport
	decodeJSON(t, out, &rep)
	if rep.Errors != 1 {
		t.Errorf("errors = %d, want 1 for a duplicate mod", rep.Errors)
	}
	i, ok := rep.finding("2 jars provide the mod")
	if !ok {
		t.Fatalf("no finding for the duplicate mod in %s", out)
	}
	got := rep.Findings[i]
	if got.Severity != sevError {
		t.Errorf("severity = %q, want %q", got.Severity, sevError)
	}
	if !strings.Contains(got.Title, sodiumModID) {
		t.Errorf("title %q does not name the mod", got.Title)
	}
	if !strings.Contains(got.Detail, "only one will load") {
		t.Errorf("detail %q does not explain the consequence", got.Detail)
	}
	if !strings.Contains(got.Fix, "new-copy.jar") || !strings.Contains(got.Fix, "old-copy.jar") {
		t.Errorf("fix %q does not name both jars, so it cannot be acted on", got.Fix)
	}
}

// Severity is the field a script filters and branches on, and an error report
// is not much use if every entry reads "warn". A mod left over from another
// Minecraft version has to arrive as a warning with its own detail and fix.
func TestDoctorJSONCarriesTheSeverityOfEveryFinding(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, _ := newTestInstance(t, instID, map[string][]byte{
		// Declares support for MC 26.1 while the instance runs 26.3.
		"legacy-1.0.0.jar": fabricJar(t, "legacymod", "Legacy Mod", "1.0.0",
			`{"minecraft":"~26.1"}`),
	})
	writeTestConfig(t, mcDir, fx.API())

	out, err := captureCLI(t, "doctor", instID, "--json")
	if err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, out)
	}

	var rep doctorJSONReport
	decodeJSON(t, out, &rep)
	if rep.Errors != 0 {
		t.Errorf("errors = %d, want 0: a version mismatch is a warning", rep.Errors)
	}
	i, ok := rep.finding("declares support for MC 26.1")
	if !ok {
		t.Fatalf("no finding for the leftover mod in %s", out)
	}
	got := rep.Findings[i]
	if got.Severity != sevWarn {
		t.Errorf("severity = %q, want %q", got.Severity, sevWarn)
	}
	if !strings.Contains(got.Detail, "26.3") {
		t.Errorf("detail %q does not say what the instance runs", got.Detail)
	}
	if got.Fix == "" {
		t.Error("the finding has no fix on the wire")
	}
}

// A finding with nothing to fix must not invent one. Emitting an empty string
// under a key named "fix" reads as advice, and a script that renders it would
// tell the user to do something undefined.
func TestDoctorJSONLeavesAnAbsentFixAbsent(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	// A jar that is not an archive is the one finding doctor raises without a
	// fix: nothing modharbor offers will make the metadata appear.
	mcDir, _ := newTestInstance(t, instID, map[string][]byte{
		"mystery-1.0.jar": []byte("not a jar at all\n"),
	})
	writeTestConfig(t, mcDir, fx.API())

	out, err := captureCLI(t, "doctor", instID, "--json")
	if err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, out)
	}

	var rep doctorJSONReport
	decodeJSON(t, out, &rep)
	i, ok := rep.finding("no mod metadata could be read")
	if !ok {
		t.Fatalf("no finding for the unreadable jar in %s", out)
	}
	if rep.Findings[i].Severity != sevWarn {
		t.Errorf("severity = %q, want %q", rep.Findings[i].Severity, sevWarn)
	}
	if rep.Findings[i].Fix != "" {
		t.Errorf("fix = %q, want nothing", rep.Findings[i].Fix)
	}
	if strings.Contains(out, `"fix": ""`) {
		t.Errorf("an empty fix was written to the payload:\n%s", out)
	}
}

// The verdict has to be the same however the report is rendered. A script that
// adds --json to get parseable output currently gets exit 0 on an instance
// that the human path calls broken, which is the worst failure mode a CLI has:
// the flag meant to make a run scriptable changes what the run says.
func TestDoctorJSONExitStatusMatchesTheHumanPath(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, _ := duplicateJars(t)
	writeTestConfig(t, mcDir, fx.API())

	humanCode, _, _ := executeCLI(t, "doctor", instID)
	jsonCode, stdout, stderr := executeCLI(t, "doctor", instID, "--json")

	if jsonCode != humanCode {
		t.Errorf("exit status = %d with --json and %d without it: "+
			"a formatting flag must not change the verdict", jsonCode, humanCode)
	}
	if jsonCode != 1 {
		t.Errorf("exit status = %d for an instance with an error finding, want 1", jsonCode)
	}
	// A silentError is what carries the status, so stderr has to stay empty:
	// a complaint interleaved with the document makes it unparseable.
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("stderr is not empty in --json mode: %s", stderr)
	}
	decodeJSON(t, stdout, &doctorJSONReport{})
}

// The other half of the parity: a healthy instance must not be made to look
// broken by the fix above. Warnings are advice, not failure.
func TestDoctorJSONExitStatusIsZeroWithoutErrorFindings(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, _ := newTestInstance(t, instID, map[string][]byte{
		"legacy-1.0.0.jar": fabricJar(t, "legacymod", "Legacy Mod", "1.0.0",
			`{"minecraft":"~26.1"}`),
	})
	writeTestConfig(t, mcDir, fx.API())

	code, stdout, stderr := executeCLI(t, "doctor", instID, "--json")
	if code != 0 {
		t.Errorf("exit status = %d for a warning-only instance, want 0\nstderr: %s", code, stderr)
	}

	var rep doctorJSONReport
	decodeJSON(t, stdout, &rep)
	if rep.Errors != 0 {
		t.Errorf("errors = %d, want 0", rep.Errors)
	}
	if rep.Warnings == 0 {
		t.Errorf("warnings = 0 for a mod built for another Minecraft version:\n%s", stdout)
	}
}
