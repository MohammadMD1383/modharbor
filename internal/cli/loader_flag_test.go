package cli

import (
	"testing"

	"github.com/MohammadMD1383/modharbor/internal/instance"
)

// A vanilla instance declares no loader, so without --loader every query
// falls back to fabric. An explicit flag has to win, or a modded folder with
// no version JSON can never ask about forge builds.
func TestLoaderFlagOverridesVanillaInstance(t *testing.T) {
	orig := flagLoader
	t.Cleanup(func() { flagLoader = orig })

	flagLoader = "forge"
	inst := &instance.Info{ID: "vanilla-mods", Type: instance.TypeVanilla}
	if err := applyLoaderOverride(inst); err != nil {
		t.Fatalf("applyLoaderOverride: %v", err)
	}
	if inst.Type != instance.TypeForge {
		t.Errorf("Type = %q, want forge", inst.Type)
	}
	if got := loaderName(inst.Type); got != "forge" {
		t.Errorf("loaderName = %q, want forge", got)
	}
}

// An explicit flag is intent: it wins even when the instance already
// declares a loader.
func TestLoaderFlagOverridesDetectedLoader(t *testing.T) {
	orig := flagLoader
	t.Cleanup(func() { flagLoader = orig })

	flagLoader = "neoforge"
	inst := &instance.Info{ID: "x", Type: instance.TypeFabric}
	if err := applyLoaderOverride(inst); err != nil {
		t.Fatalf("applyLoaderOverride: %v", err)
	}
	if inst.Type != instance.TypeNeoForge {
		t.Errorf("Type = %q, want neoforge", inst.Type)
	}
}

func TestLoaderFlagAbsentLeavesInstanceAlone(t *testing.T) {
	orig := flagLoader
	t.Cleanup(func() { flagLoader = orig })

	flagLoader = ""
	inst := &instance.Info{ID: "x", Type: instance.TypeVanilla}
	if err := applyLoaderOverride(inst); err != nil {
		t.Fatalf("applyLoaderOverride: %v", err)
	}
	if inst.Type != instance.TypeVanilla {
		t.Errorf("Type = %q, want vanilla", inst.Type)
	}
}

func TestLoaderFlagRejectsUnknownLoaders(t *testing.T) {
	for _, bad := range []string{"rift", "vanilla", "", "fabric-mod"} {
		if bad == "" {
			continue
		}
		if _, err := parseLoaderFlag(bad); err == nil {
			t.Errorf("parseLoaderFlag(%q) = nil, want an error", bad)
		}
	}
	for _, good := range []string{"fabric", "Forge", " NEOFORGE ", "quilt"} {
		if _, err := parseLoaderFlag(good); err != nil {
			t.Errorf("parseLoaderFlag(%q) = %v, want nil", good, err)
		}
	}
}

func TestValidateLoaderFlagNormalisesInPlace(t *testing.T) {
	orig := flagLoader
	t.Cleanup(func() { flagLoader = orig })

	flagLoader = "Forge"
	if err := validateLoaderFlag(); err != nil {
		t.Fatalf("validateLoaderFlag: %v", err)
	}
	if flagLoader != "forge" {
		t.Errorf("flagLoader = %q, want normalised forge", flagLoader)
	}
}
