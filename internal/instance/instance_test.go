package instance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeInstance builds a fake instance directory on disk.
func writeInstance(t *testing.T, root, id string, libs []string, mainClass string) string {
	t.Helper()
	dir := filepath.Join(root, "versions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	type lib struct {
		Name string `json:"name"`
	}
	var libraryList []lib
	for _, l := range libs {
		libraryList = append(libraryList, lib{Name: l})
	}
	doc := map[string]any{
		"id":        id,
		"type":      "release",
		"mainClass": mainClass,
		"libraries": libraryList,
	}
	writeJSON(t, filepath.Join(dir, id+".json"), doc)
	return dir
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectFabricFromLibraries(t *testing.T) {
	root := t.TempDir()
	dir := writeInstance(t, root, "26.3-fabric-mod",
		[]string{"org.lwjgl:lwjgl:3.4.3", "net.fabricmc:fabric-loader:0.19.5"},
		"net.minecraft.client.main.Main")

	inst, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Type != TypeFabric {
		t.Errorf("Type = %v, want fabric", inst.Type)
	}
	if inst.LoaderVersion != "0.19.5" {
		t.Errorf("LoaderVersion = %q, want 0.19.5", inst.LoaderVersion)
	}
	// The MC version must come from the instance id when nothing better exists.
	if inst.MCVersion != "26.3" {
		t.Errorf("MCVersion = %q, want 26.3", inst.MCVersion)
	}
}

func TestDetectOtherLoaders(t *testing.T) {
	cases := []struct {
		name    string
		libs    []string
		want    Type
		wantVer string
	}{
		{"quilt", []string{"org.quiltmc:quilt-loader:0.27.0"}, TypeQuilt, "0.27.0"},
		{"neoforge", []string{"net.neoforged:neoforge:21.1.0"}, TypeNeoForge, "21.1.0"},
		{"forge", []string{"net.minecraftforge:forge:1.20.1-47.2.0"}, TypeForge, "1.20.1-47.2.0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			dir := writeInstance(t, root, "1.20.1-"+c.name, c.libs, "net.minecraft.client.main.Main")
			inst, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if inst.Type != c.want {
				t.Errorf("Type = %v, want %v", inst.Type, c.want)
			}
			if inst.LoaderVersion != c.wantVer {
				t.Errorf("LoaderVersion = %q, want %q", inst.LoaderVersion, c.wantVer)
			}
		})
	}
}

func TestDetectLoaderFromMainClassWhenLibrariesAreAbsent(t *testing.T) {
	root := t.TempDir()
	dir := writeInstance(t, root, "26.3-fabric", nil, "net.fabricmc.loader.impl.launch.knot.KnotClient")
	inst, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Type != TypeFabric {
		t.Errorf("Type = %v, want fabric from mainClass", inst.Type)
	}
}

// TLauncher hides the Minecraft version behind a sidecar rather than the
// instance id, and the id is often a name like "26.3-fabric-mod". The sidecar
// is the authoritative source when present.
func TestTLauncherSidecarSuppliesMCVersion(t *testing.T) {
	root := t.TempDir()
	dir := writeInstance(t, root, "my-custom-pack", []string{"net.fabricmc:fabric-loader:0.19.5"}, "x.Main")
	writeJSON(t, filepath.Join(dir, "TLauncherAdditional.json"), map[string]any{
		"jar": "26.3",
		"modpack": map[string]any{
			"name": "my-custom-pack",
			"version": map[string]any{
				"minecraftVersionName":  map[string]any{"name": "0.19.5"},
				"gameVersionDTO":        map[string]any{"name": "26.3"},
				"minecraftVersionTypes": []any{map[string]any{"name": "fabric"}},
				"mods":                  []any{},
			},
		},
	})

	inst, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if inst.MCVersion != "26.3" {
		t.Errorf("MCVersion = %q, want 26.3 from the sidecar", inst.MCVersion)
	}
	if inst.Launcher != "TLauncher" {
		t.Errorf("Launcher = %q, want TLauncher", inst.Launcher)
	}
}

// The CurseForge project id recorded per mod is what makes mirror-sourced jars
// identifiable, so parsing it must be exact.
func TestParseTLauncherModsMapsFileNames(t *testing.T) {
	root := t.TempDir()
	dir := writeInstance(t, root, "26.3-fabric-mod", []string{"net.fabricmc:fabric-loader:0.19.5"}, "x.Main")
	writeJSON(t, filepath.Join(dir, "TLauncherAdditional.json"), map[string]any{
		"jar": "26.3",
		"modpack": map[string]any{
			"name": "26.3-fabric-mod",
			"version": map[string]any{
				"mods": []any{
					map[string]any{
						"id": 394468, "name": "Sodium", "author": "jellysquid",
						"linkProject": "https://www.curseforge.com/minecraft/mc-mods/sodium",
						"version": map[string]any{
							"metadata": map[string]any{
								"sha1": "ABCDEF0123456789",
								"path": "mods/sodium-fabric-0.9.1%2Bmc26.3.jar",
							},
						},
					},
					// An entry with no file path must be skipped rather than
					// producing an empty key.
					map[string]any{"id": 1, "name": "Ghost"},
				},
			},
		},
	})

	mods, err := ParseTLauncherMods(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := "sodium-fabric-0.9.1%2Bmc26.3.jar"
	got, ok := mods[want]
	if !ok {
		t.Fatalf("expected a key for %q, got %v", want, mods)
	}
	if got.CurseForgeID != 394468 || got.Name != "Sodium" {
		t.Errorf("unexpected entry %+v", got)
	}
	// Digests arrive in mixed case from the launcher; normalise them.
	if got.SHA1 != "abcdef0123456789" {
		t.Errorf("SHA1 = %q, want lowercased", got.SHA1)
	}
	if len(mods) != 1 {
		t.Errorf("expected 1 usable entry, got %d", len(mods))
	}
}

func TestMCVersionFromID(t *testing.T) {
	cases := map[string]string{
		"26.3-fabric-mod":      "26.3",
		"1.20.1-forge-47.2.0":  "1.20.1",
		"26.2":                 "26.2",
		"v1.21.4":              "1.21.4",
		"26.1.2-fabric-mod-v2": "26.1.2",
		// A loader-first name carries no reliable game version. Guessing one
		// would silently migrate to the wrong release, so we return nothing
		// and let doctor ask the user instead.
		"fabric-loader-0.19.5-26.3": "",
		"no-version-here":           "",
	}
	for in, want := range cases {
		if got := mcVersionFromID(in); got != want {
			t.Errorf("mcVersionFromID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiscoverListsInstancesSorted(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"26.3-fabric-mod", "26.2-fabric-mod", "1.20.1-forge"} {
		dir := writeInstance(t, root, id, []string{"net.fabricmc:fabric-loader:0.19.5"}, "x.Main")
		if err := os.MkdirAll(filepath.Join(dir, "mods"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A directory with no version JSON must be skipped, not fatal.
	if err := os.MkdirAll(filepath.Join(root, "versions", "junk"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 instances, got %d", len(got))
	}
	if got[0].ID != "1.20.1-forge" || got[2].ID != "26.3-fabric-mod" {
		t.Errorf("instances are not sorted by id: %v", []string{got[0].ID, got[1].ID, got[2].ID})
	}
}

func TestDiscoverAcceptsASingleInstanceDirectory(t *testing.T) {
	root := t.TempDir()
	dir := writeInstance(t, root, "26.3-fabric-mod", []string{"net.fabricmc:fabric-loader:0.19.5"}, "x.Main")
	got, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "26.3-fabric-mod" {
		t.Fatalf("expected the single instance, got %+v", got)
	}
}

func TestResolveByIDPathAndPrefix(t *testing.T) {
	root := t.TempDir()
	dir := writeInstance(t, root, "26.3-fabric-mod", []string{"net.fabricmc:fabric-loader:0.19.5"}, "x.Main")
	writeInstance(t, root, "26.2-fabric-mod", []string{"net.fabricmc:fabric-loader:0.19.5"}, "x.Main")

	for _, ref := range []string{"26.3-fabric-mod", "26.3", dir} {
		got, err := Resolve(root, ref)
		if err != nil {
			t.Fatalf("Resolve(%q) failed: %v", ref, err)
		}
		if got.ID != "26.3-fabric-mod" {
			t.Errorf("Resolve(%q) = %q, want 26.3-fabric-mod", ref, got.ID)
		}
	}
	if _, err := Resolve(root, "nonexistent"); err == nil {
		t.Error("expected an error for an unknown reference")
	}
}

func TestModFilesIgnoresNonJarsAndMissingDir(t *testing.T) {
	dir := t.TempDir()
	// A subdirectory inside mods/ must be ignored, not counted.
	if err := os.MkdirAll(filepath.Join(dir, "nested", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a.jar", "b.JAR", "readme.txt", "sodium-fabric.jar"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ModFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("ModFiles = %v, want three jars", got)
	}
	// Results must be sorted so repeated output is stable.
	if got[0] != filepath.Join(dir, "a.jar") {
		t.Errorf("ModFiles is not sorted: %v", got)
	}

	// A missing mods directory is normal for a fresh instance, not an error.
	got, err = ModFiles(filepath.Join(dir, "nope"))
	if err != nil || len(got) != 0 {
		t.Errorf("ModFiles(missing) = %v, %v; want empty and no error", got, err)
	}
}

func TestIsModded(t *testing.T) {
	if i := (&Info{Type: TypeVanilla}); i.IsModded() {
		t.Error("vanilla must not report as modded")
	}
	if i := (&Info{Type: TypeFabric}); !i.IsModded() {
		t.Error("fabric must report as modded")
	}
}
