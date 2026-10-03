package instance

import "testing"

func TestLabelTitleCasesLoader(t *testing.T) {
	cases := map[Type]string{
		TypeVanilla:  "Vanilla",
		TypeFabric:   "Fabric",
		TypeQuilt:    "Quilt",
		TypeForge:    "Forge",
		TypeNeoForge: "Neoforge",
		TypeUnknown:  "Vanilla",
	}
	for typ, want := range cases {
		got := (&Info{Name: "test", MCVersion: "26.3", Type: typ}).Label()
		wantLabel := "test (MC 26.3, " + want + ")"
		if got != wantLabel {
			t.Errorf("Label() for type %q = %q, want %q", typ, got, wantLabel)
		}
	}
}

func TestLabelFallsBackOnMissingMCVersion(t *testing.T) {
	got := (&Info{Name: "test", Type: TypeFabric}).Label()
	if got != "test (MC unknown, Fabric)" {
		t.Errorf("Label() = %q, want %q", got, "test (MC unknown, Fabric)")
	}
}
