package cli

import (
	"strings"
	"testing"
)

// scan already holds every jar's identity, so it should surface the same
// duplicate doctor reports rather than making the user run a second command.
func TestScanWarnsAboutDuplicateMods(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, _ := newTestInstance(t, instID, map[string][]byte{
		"old-copy.jar": fabricJar(t, sodiumModID, sodiumModName, "0.9.0", ""),
		"new-copy.jar": fabricJar(t, sodiumModID, sodiumModName, "0.10.0", ""),
	})
	writeTestConfig(t, mcDir, fx.API())

	out, err := captureCLI(t, "scan", instID)
	if err != nil {
		t.Fatalf("scan: %v\n%s", err, out)
	}
	if !strings.Contains(out, sodiumModID) || !strings.Contains(out, "new-copy.jar") {
		t.Errorf("scan does not name the duplicated mod and its jars:\n%s", out)
	}
	if !strings.Contains(out, "doctor") {
		t.Errorf("scan does not point at doctor for the fix:\n%s", out)
	}
}

// The machine-readable scan report carries the same warning so scripts do not
// have to parse human output.
func TestScanJSONCarriesDuplicates(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, _ := newTestInstance(t, instID, map[string][]byte{
		"old-copy.jar": fabricJar(t, sodiumModID, sodiumModName, "0.9.0", ""),
		"new-copy.jar": fabricJar(t, sodiumModID, sodiumModName, "0.10.0", ""),
	})
	writeTestConfig(t, mcDir, fx.API())

	out, err := captureCLI(t, "scan", instID, "--json")
	if err != nil {
		t.Fatalf("scan --json: %v\n%s", err, out)
	}
	var rep struct {
		Duplicates []struct {
			ModID string   `json:"modId"`
			Files []string `json:"files"`
		} `json:"duplicates"`
	}
	decodeJSON(t, out, &rep)
	if len(rep.Duplicates) != 1 {
		t.Fatalf("duplicates = %d groups, want 1:\n%s", len(rep.Duplicates), out)
	}
	if rep.Duplicates[0].ModID != sodiumModID {
		t.Errorf("modId = %q, want %q", rep.Duplicates[0].ModID, sodiumModID)
	}
	if len(rep.Duplicates[0].Files) != 2 {
		t.Errorf("files = %v, want both jars", rep.Duplicates[0].Files)
	}
}

// A clean instance stays quiet: no warning, no duplicates key.
func TestScanWithoutDuplicatesStaysQuiet(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, _ := newTestInstance(t, instID, map[string][]byte{
		"sodium.jar": fabricJar(t, sodiumModID, sodiumModName, "0.9.0", ""),
	})
	writeTestConfig(t, mcDir, fx.API())

	out, err := captureCLI(t, "scan", instID)
	if err != nil {
		t.Fatalf("scan: %v\n%s", err, out)
	}
	if strings.Contains(out, "provide the mod") {
		t.Errorf("scan warns about duplicates on a clean instance:\n%s", out)
	}

	jsonOut, err := captureCLI(t, "scan", instID, "--json")
	if err != nil {
		t.Fatalf("scan --json: %v\n%s", err, jsonOut)
	}
	if strings.Contains(jsonOut, `"duplicates"`) {
		t.Errorf("scan --json emits duplicates on a clean instance:\n%s", jsonOut)
	}
}
