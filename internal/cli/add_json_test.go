package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// `add --json` used to carry only counts, so a script could see that one mod
// was installed but not which one. The payload must name every planned mod
// with enough detail to verify the file on disk.
func TestAddJSONCarriesTheInstalledMods(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, _ := newTestInstance(t, instID, map[string][]byte{
		"unrelated.jar": []byte("some other mod"),
	})
	writeTestConfig(t, mcDir, fx.API())

	out, err := captureCLI(t, "add", sodiumSlug, instID, "--dry-run", "--json")
	if err != nil {
		t.Fatalf("add --json: %v\n%s", err, out)
	}

	var rep struct {
		Instance  string `json:"instance"`
		Installed int    `json:"installed"`
		Mods      []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			File    string `json:"file"`
		} `json:"mods"`
		Skipped []string `json:"skipped"`
		DryRun  bool     `json:"dryRun"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("add --json is not valid JSON: %v\n%s", err, out)
	}
	if rep.Instance != instID {
		t.Errorf("instance = %q, want %q", rep.Instance, instID)
	}
	if rep.Installed != 1 {
		t.Errorf("installed = %d, want 1", rep.Installed)
	}
	if len(rep.Mods) != 1 {
		t.Fatalf("mods has %d entries, want 1:\n%s", len(rep.Mods), out)
	}
	got := rep.Mods[0]
	if got.Name == "" {
		t.Error("mods[0].name is empty: a script cannot tell what was installed")
	}
	if got.Version == "" {
		t.Error("mods[0].version is empty")
	}
	if !strings.HasSuffix(got.File, ".jar") {
		t.Errorf("mods[0].file = %q, want the jar filename", got.File)
	}
	if !rep.DryRun {
		t.Error("dryRun = false for a --dry-run")
	}
}

// An empty plan must encode as `[]`, not `null`: a script iterating the array
// without a nil check should not have to care that nothing was installed.
func TestAddJSONEmptyPlanIsAnArray(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, _ := newTestInstance(t, instID, map[string][]byte{
		"unrelated.jar": []byte("some other mod"),
	})
	writeTestConfig(t, mcDir, fx.API())

	// Install for real first, so the second run has nothing left to do.
	if _, err := captureCLI(t, "add", sodiumSlug, instID); err != nil {
		t.Fatalf("add: %v", err)
	}
	out, err := captureCLI(t, "add", sodiumSlug, instID, "--json")
	if err != nil {
		t.Fatalf("add --json: %v\n%s", err, out)
	}
	if strings.Contains(out, `"mods": null`) || strings.Contains(out, `"mods":null`) {
		t.Errorf("empty mods encoded as null:\n%s", out)
	}
}
