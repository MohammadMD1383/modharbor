package modmeta

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Inventory Profiles Next", "inventoryprofilesnext"},
		{"inventory-profiles-next", "inventoryprofilesnext"},
		{"InventoryProfilesNext", "inventoryprofilesnext"},
		{"libIPN", "libipn"},
		{"YetAnotherConfigLib (YACL)", "yetanotherconfiglibyacl"},
		{"Common Network", "commonnetwork"},
	}
	for _, c := range cases {
		if got := NormalizeName(c.in); got != c.want {
			t.Errorf("NormalizeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGuessModIDFromFileName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"sodium-fabric-0.9.1%2Bmc26.2.jar", "sodium"},
		{"fabric-api-0.154.2%2B26.2.jar", "fabricapi"},
		{"yet_another_config_lib_v3-3.9.5+26.2-fabric.jar", "yetanotherconfiglibv3"},
		{"shulkerboxtooltip-fabric-5.4.0%2B26.2.jar", "shulkerboxtooltip"},
		{"wthit-26.2-fabric-20.0.0.jar", "wthit"},
	}
	for _, c := range cases {
		if got := GuessModIDFromFileName(c.in); got != c.want {
			t.Errorf("GuessModIDFromFileName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestReadFabricModJSON(t *testing.T) {
	// Build a minimal fabric mod jar on the fly.
	dir := t.TempDir()
	jar := filepath.Join(dir, "sodium.jar")
	writeZip(t, jar, map[string]string{
		"fabric.mod.json": `{"schemaVersion":1,"id":"sodium","version":"0.9.1","name":"Sodium","description":"Rendering engine","authors":[{"name":"JellySquid"}],"depends":{"fabricloader":">=0.15.0"},"entrypoints":{"client":["x"]}}`,
	})
	m, err := Read(jar)
	if err != nil {
		t.Fatal(err)
	}
	if m.ModID != "sodium" {
		t.Errorf("ModID = %q, want sodium", m.ModID)
	}
	if m.Name != "Sodium" {
		t.Errorf("Name = %q, want Sodium", m.Name)
	}
	if m.Loader != LoaderFabric {
		t.Errorf("Loader = %q, want fabric", m.Loader)
	}
	if m.DependsOn["fabricloader"] != ">=0.15.0" {
		t.Errorf("DependsOn = %v", m.DependsOn)
	}
	if m.Authors[0] != "JellySquid" {
		t.Errorf("Authors = %v", m.Authors)
	}
	if len(m.Entrypoints) != 1 {
		t.Errorf("Entrypoints = %v", m.Entrypoints)
	}
}

// The Fabric schema allows several fields to be either a scalar or an array.
// A strict decoder rejects the scalar form, which made a mod that is perfectly
// identifiable look unknown: this was a real bug that broke dependency
// checking for most mods.
func TestReadFabricWithScalarFields(t *testing.T) {
	dir := t.TempDir()
	jar := filepath.Join(dir, "wthit.jar")
	writeZip(t, jar, map[string]string{
		// "license" and "authors" are scalars here, and "contact" is absent.
		"fabric.mod.json": `{"schemaVersion":1,"id":"wthit","version":"20.0.0","license":"MIT","authors":"MyName","entrypoints":{"client":["x"]}}`,
	})
	m, err := Read(jar)
	if err != nil {
		t.Fatal(err)
	}
	if m.ModID != "wthit" {
		t.Errorf("ModID = %q, want wthit", m.ModID)
	}
	if len(m.Licenses) != 1 || m.Licenses[0] != "MIT" {
		t.Errorf("Licenses = %v, want [MIT]", m.Licenses)
	}
	if len(m.Authors) != 1 || m.Authors[0] != "MyName" {
		t.Errorf("Authors = %v, want [MyName]", m.Authors)
	}
}

func TestReadFabricArrayFields(t *testing.T) {
	dir := t.TempDir()
	jar := filepath.Join(dir, "sodium.jar")
	writeZip(t, jar, map[string]string{
		"fabric.mod.json": `{"schemaVersion":1,"id":"sodium","version":"1.0","license":["MIT","CC0-1.0"],"authors":[{"name":"A"},{"name":"B"}]}`,
	})
	m, err := Read(jar)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Licenses) != 2 {
		t.Errorf("Licenses = %v, want two entries", m.Licenses)
	}
	if len(m.Authors) != 2 {
		t.Errorf("Authors = %v, want two entries", m.Authors)
	}
}

// An unrecognised field type must not cost us the mod's identity.
func TestReadFabricSurvivesUnexpectedFieldType(t *testing.T) {
	dir := t.TempDir()
	jar := filepath.Join(dir, "weird.jar")
	writeZip(t, jar, map[string]string{
		"fabric.mod.json": `{"id":"weird","version":"1.0","entrypoints":42,"depends":"not-an-object"}`,
	})
	m, err := Read(jar)
	if err != nil {
		t.Fatal(err)
	}
	if m.ModID != "weird" {
		t.Errorf("ModID = %q, want weird", m.ModID)
	}
	if m.Version != "1.0" {
		t.Errorf("Version = %q, want 1.0", m.Version)
	}
}

func TestReadQuiltModJSON(t *testing.T) {
	dir := t.TempDir()
	jar := filepath.Join(dir, "q.jar")
	writeZip(t, jar, map[string]string{
		"quilt.mod.json": `{"schema_version":1,"quilt_loader":"mymod","version":"1.2.3","metadata":{"name":"My Mod","description":"d"},"depends":[{"id":"quilt_loader","versions":["*"]}]}`,
	})
	m, err := Read(jar)
	if err != nil {
		t.Fatal(err)
	}
	if m.ModID != "mymod" || m.Loader != LoaderQuilt {
		t.Errorf("got %+v", m)
	}
}

func TestReadModsToml(t *testing.T) {
	dir := t.TempDir()
	jar := filepath.Join(dir, "f.jar")
	writeZip(t, jar, map[string]string{
		"META-INF/mods.toml": `modLoader="javafml"
[[mods]]
modId="worldedit"
version="7.4.5"
displayName="WorldEdit"
description="API"
[[dependencies.worldedit]]
modId="forge"
mandatory=true
versionRange="[47,)"
`,
	})
	m, err := Read(jar)
	if err != nil {
		t.Fatal(err)
	}
	if m.ModID != "worldedit" {
		t.Errorf("ModID = %q", m.ModID)
	}
	if m.Loader != LoaderForge {
		t.Errorf("Loader = %q", m.Loader)
	}
	if m.DependsOn["forge"] != "[47,)" {
		t.Errorf("DependsOn = %v", m.DependsOn)
	}
}

func TestReadNoDescriptor(t *testing.T) {
	dir := t.TempDir()
	jar := filepath.Join(dir, "empty.jar")
	writeZip(t, jar, map[string]string{"a/b.txt": "hello"})
	m, err := Read(jar)
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsEmpty() {
		t.Errorf("expected empty meta, got %+v", m)
	}
}

func TestReadMissingFile(t *testing.T) {
	if _, err := Read(filepath.Join(t.TempDir(), "nope.jar")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadNonZip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.jar")
	if err := os.WriteFile(p, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(p); err == nil {
		t.Fatal("expected zip error")
	}
}

// writeZip builds a zip archive with the given entries.
func writeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := newZipWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}
