package modmeta

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// These tests cover nested.jar resolution, which the handoff calls out as load
// bearing: without it Sodium reports seven missing Fabric API modules and Mod
// Menu four, all of which are bundled inside the very jar that needs them.

// jarBytes builds a jar in memory, so it can be embedded as an entry of an
// outer jar. writeZip only writes to a path, which is not enough for this.
func jarBytes(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := newZipWriter(&buf)
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
	return buf.Bytes()
}

// writeZipBytes builds an archive whose entries are raw bytes.
func writeZipBytes(t *testing.T, path string, entries map[string][]byte) {
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
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// nestedByFileName indexes a result by FileName, because the archive order of
// the entries is not deterministic and the tests do not care about it.
func nestedByFileName(t *testing.T, got []Nested) map[string]Nested {
	t.Helper()
	out := make(map[string]Nested, len(got))
	for _, n := range got {
		if _, dup := out[n.FileName]; dup {
			t.Fatalf("duplicate entry %q in ReadNested result", n.FileName)
		}
		out[n.FileName] = n
	}
	return out
}

// Fabric puts bundled libraries in "jars/"; some jars use "META-INF/jars/".
// Both have to be read, and each entry's identity comes from the nested jar's
// own descriptor rather than the outer one.
func TestReadNestedReadsBothConventionalLocations(t *testing.T) {
	dir := t.TempDir()
	outer := filepath.Join(dir, "sodium.jar")

	rendering := jarBytes(t, map[string]string{
		"fabric.mod.json": `{"schemaVersion":1,"id":"fabric-rendering-v1","version":"2.0.0","name":"Fabric Rendering API"}`,
	})
	screen := jarBytes(t, map[string]string{
		"fabric.mod.json": `{"schemaVersion":1,"id":"fabric-screen-api-v1","version":"4.2.0","name":"Fabric Screen API"}`,
	})

	writeZipBytes(t, outer, map[string][]byte{
		"fabric.mod.json":              []byte(`{"schemaVersion":1,"id":"sodium","version":"0.9.1","name":"Sodium"}`),
		"jars/fabric-rendering.jar":    rendering,
		"META-INF/jars/screen-api.jar": screen,
	})

	nested, err := ReadNested(outer)
	if err != nil {
		t.Fatal(err)
	}
	if len(nested) != 2 {
		t.Fatalf("ReadNested returned %d entries, want 2: %+v", len(nested), nested)
	}

	byName := nestedByFileName(t, nested)
	got, ok := byName["jars/fabric-rendering.jar"]
	if !ok {
		t.Fatalf("missing jars/ entry, got %+v", byName)
	}
	if got.ModID != "fabric-rendering-v1" || got.Version != "2.0.0" {
		t.Errorf("jars/ entry = %+v, want id fabric-rendering-v1 version 2.0.0", got)
	}
	if got.Name != "Fabric Rendering API" {
		t.Errorf("Name = %q, want Fabric Rendering API", got.Name)
	}
	if got.Loader != LoaderFabric {
		t.Errorf("Loader = %q, want fabric", got.Loader)
	}

	got, ok = byName["META-INF/jars/screen-api.jar"]
	if !ok {
		t.Fatalf("missing META-INF/jars/ entry, got %+v", byName)
	}
	if got.ModID != "fabric-screen-api-v1" || got.Version != "4.2.0" {
		t.Errorf("META-INF/jars/ entry = %+v, want id fabric-screen-api-v1 version 4.2.0", got)
	}
}

// Only the conventional directories are inspected. Decoding an arbitrary blob
// inside a mod that merely happens to end in ".jar" would be both slow and
// wrong, so everything else is ignored — including a jar at the archive root.
func TestReadNestedIgnoresJarsOutsideConventionalLocations(t *testing.T) {
	dir := t.TempDir()
	outer := filepath.Join(dir, "asset-heavy.jar")

	lib := jarBytes(t, map[string]string{
		"fabric.mod.json": `{"schemaVersion":1,"id":"real-nested","version":"1.0.0"}`,
	})

	writeZipBytes(t, outer, map[string][]byte{
		"jars/real-nested.jar": lib,
		"assets/top.jar":       lib,
		"data/nested.jar":      lib,
		"top.jar":              lib,
		"jars/readme.txt":      []byte("not a jar"),
		"jars/":                nil,
	})

	nested, err := ReadNested(outer)
	if err != nil {
		t.Fatal(err)
	}
	if len(nested) != 1 {
		t.Fatalf("ReadNested returned %d entries, want 1: %+v", len(nested), nested)
	}
	if nested[0].FileName != "jars/real-nested.jar" {
		t.Errorf("FileName = %q, want jars/real-nested.jar", nested[0].FileName)
	}
}

func TestReadNestedNoNestedJars(t *testing.T) {
	dir := t.TempDir()
	outer := filepath.Join(dir, "sodium.jar")
	writeZip(t, outer, map[string]string{
		"fabric.mod.json": `{"schemaVersion":1,"id":"sodium","version":"0.9.1"}`,
	})

	nested, err := ReadNested(outer)
	if err != nil {
		t.Fatal(err)
	}
	if len(nested) != 0 {
		t.Fatalf("expected no nested jars, got %+v", nested)
	}
}

func TestReadNestedMissingFile(t *testing.T) {
	if _, err := ReadNested(filepath.Join(t.TempDir(), "nope.jar")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadNestedNonZip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.jar")
	if err := os.WriteFile(p, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadNested(p); err == nil {
		t.Fatal("expected zip error")
	}
}

// A nested jar we cannot decode is still a file that is really there, so it is
// reported with an empty id rather than dropped. Dropping it would silently
// under-count what the outer mod provides; an empty id simply contributes
// nothing to the id set.
func TestReadNestedKeepsUndecodableNestedJar(t *testing.T) {
	dir := t.TempDir()
	outer := filepath.Join(dir, "wrapped.jar")
	writeZipBytes(t, outer, map[string][]byte{
		"jars/garbage.jar": []byte("this is not a zip archive at all"),
	})

	nested, err := ReadNested(outer)
	if err != nil {
		t.Fatalf("an undecodable nested jar must not fail the whole read: %v", err)
	}
	if len(nested) != 1 {
		t.Fatalf("ReadNested returned %d entries, want 1: %+v", len(nested), nested)
	}
	if nested[0].FileName != "jars/garbage.jar" {
		t.Errorf("FileName = %q, want jars/garbage.jar", nested[0].FileName)
	}
	if nested[0].ModID != "" {
		t.Errorf("ModID = %q, want empty for an undecodable jar", nested[0].ModID)
	}
}

// A nested jar with no loader descriptor is a plain library. It is still
// listed, but it contributes no mod id.
func TestReadNestedKeepsNestedJarWithoutDescriptor(t *testing.T) {
	dir := t.TempDir()
	outer := filepath.Join(dir, "modmenu.jar")
	plain := jarBytes(t, map[string]string{"com/example/Lib.class": "\xca\xfe\xba\xbe"})

	writeZipBytes(t, outer, map[string][]byte{
		"jars/plain-lib.jar": plain,
	})

	nested, err := ReadNested(outer)
	if err != nil {
		t.Fatal(err)
	}
	if len(nested) != 1 {
		t.Fatalf("ReadNested returned %d entries, want 1: %+v", len(nested), nested)
	}
	if nested[0].ModID != "" || nested[0].Version != "" || nested[0].Loader != LoaderUnknown {
		t.Errorf("descriptor-less nested jar should be blank, got %+v", nested[0])
	}
}

func TestReadNestedSkipsEmptyNestedJar(t *testing.T) {
	dir := t.TempDir()
	outer := filepath.Join(dir, "odd.jar")
	writeZipBytes(t, outer, map[string][]byte{
		"jars/empty.jar": nil,
	})

	nested, err := ReadNested(outer)
	if err != nil {
		t.Fatal(err)
	}
	if len(nested) != 0 {
		t.Fatalf("a zero-length entry is not a jar, got %+v", nested)
	}
}

// The size cap exists so a hostile archive cannot exhaust memory. The entry
// below is written honestly, so its declared size is accurate: without the cap
// it really would be read, which is what makes this test able to fail.
func TestReadNestedSkipsOversizedEntry(t *testing.T) {
	dir := t.TempDir()
	outer := filepath.Join(dir, "huge.jar")

	f, err := os.Create(outer)
	if err != nil {
		t.Fatal(err)
	}
	zw := newZipWriter(f)
	w, err := zw.Create("jars/enormous.jar")
	if err != nil {
		t.Fatal(err)
	}
	// Zeros deflate to almost nothing, so the archive stays small on disk.
	chunk := make([]byte, 1<<20)
	for written := uint64(0); written <= maxNestedSize; written += uint64(len(chunk)) {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	nested, err := ReadNested(outer)
	if err != nil {
		t.Fatal(err)
	}
	if len(nested) != 0 {
		t.Fatalf("an entry over the size cap was not skipped: %+v", nested)
	}
}

// NestedModIDs is what a dependency check compares against, so it must be
// case-insensitive and must not invent ids for jars that declare none.
func TestNestedModIDs(t *testing.T) {
	dir := t.TempDir()
	outer := filepath.Join(dir, "sodium.jar")

	writeZipBytes(t, outer, map[string][]byte{
		"fabric.mod.json": []byte(`{"schemaVersion":1,"id":"sodium","version":"0.9.1"}`),
		"jars/fabric-rendering-v1.jar": jarBytes(t, map[string]string{
			"fabric.mod.json": `{"schemaVersion":1,"id":"Fabric-Rendering-V1","version":"2.0.0"}`,
		}),
		"jars/plain-lib.jar": jarBytes(t, map[string]string{"com/example/Lib.class": "x"}),
	})

	got := NestedModIDs(outer)
	if !got["fabric-rendering-v1"] {
		t.Errorf("NestedModIDs = %v, want fabric-rendering-v1 lowercased", got)
	}
	if got["sodium"] {
		t.Errorf("NestedModIDs = %v, must not include the outer jar's own id", got)
	}
	if len(got) != 1 {
		t.Errorf("NestedModIDs = %v, want exactly the one declared nested id", got)
	}
}

func TestNestedModIDsUnreadableJar(t *testing.T) {
	got := NestedModIDs(filepath.Join(t.TempDir(), "nope.jar"))
	if got == nil {
		t.Fatal("NestedModIDs must return an initialised map, got nil")
	}
	if len(got) != 0 {
		t.Errorf("NestedModIDs = %v, want empty for an unreadable jar", got)
	}
}

// ProvidedIDs is the union the deps command uses: the outer jar's own id plus
// everything nested inside it.
func TestProvidedIDs(t *testing.T) {
	dir := t.TempDir()
	outer := filepath.Join(dir, "modmenu.jar")

	writeZipBytes(t, outer, map[string][]byte{
		"fabric.mod.json": []byte(`{"schemaVersion":1,"id":"ModMenu","version":"11.0.0"}`),
		"jars/screen-api.jar": jarBytes(t, map[string]string{
			"fabric.mod.json": `{"schemaVersion":1,"id":"fabric-screen-api-v1","version":"4.2.0"}`,
		}),
		"jars/key-binding.jar": jarBytes(t, map[string]string{
			"fabric.mod.json": `{"schemaVersion":1,"id":"fabric-key-binding-api-v1","version":"1.0.4"}`,
		}),
	})

	got := ProvidedIDs(outer)
	for _, want := range []string{"modmenu", "fabric-screen-api-v1", "fabric-key-binding-api-v1"} {
		if !got[want] {
			t.Errorf("ProvidedIDs = %v, missing %q", got, want)
		}
	}
	if len(got) != 3 {
		t.Errorf("ProvidedIDs = %v, want exactly 3 ids", got)
	}
}

// An outer jar that declares nothing of its own still provides its nested ids.
func TestProvidedIDsOuterJarWithoutDescriptor(t *testing.T) {
	dir := t.TempDir()
	outer := filepath.Join(dir, "wrapper.jar")

	writeZipBytes(t, outer, map[string][]byte{
		"jars/inner.jar": jarBytes(t, map[string]string{
			"fabric.mod.json": `{"schemaVersion":1,"id":"inner-lib","version":"0.1.0"}`,
		}),
	})

	got := ProvidedIDs(outer)
	if !got["inner-lib"] {
		t.Errorf("ProvidedIDs = %v, want inner-lib", got)
	}
	if len(got) != 1 {
		t.Errorf("ProvidedIDs = %v, want exactly 1 id", got)
	}
}
