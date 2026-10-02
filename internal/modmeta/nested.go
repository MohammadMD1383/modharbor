package modmeta

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
)

// Nested describes a mod bundled inside another mod's jar.
//
// Fabric mods routinely ship their libraries under "jars/": Sodium contains
// its rendering APIs, Mod Menu contains the Fabric screen APIs, and so on.
// Those count as installed, so a dependency check that ignores them reports a
// long list of false problems.
type Nested struct {
	// FileName is the entry path inside the outer jar, e.g. "jars/sodium-x.jar".
	FileName string
	// ModID and Version come from the nested jar's own descriptor.
	ModID   string
	Name    string
	Version string
	Loader  Loader
}

// ReadNested lists the mods bundled inside an outer mod jar.
//
// Nested jars are read through an in-memory reader rather than being written
// to disk, and each is size-capped so a hostile archive cannot exhaust memory.
func ReadNested(jarPath string) ([]Nested, error) {
	zr, err := zip.OpenReader(jarPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	var out []Nested
	for _, f := range zr.File {
		if !isJarEntry(f.Name) {
			continue
		}
		// Skip absurdly large entries; a legitimate nested library is small.
		if f.UncompressedSize64 > maxNestedSize {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxNestedSize))
		rc.Close()
		if err != nil || len(data) == 0 {
			continue
		}
		n := Nested{FileName: f.Name}
		if meta, err := readJarBytes(data); err == nil && meta != nil {
			n.ModID = meta.ModID
			n.Name = meta.Name
			n.Version = meta.Version
			n.Loader = meta.Loader
		}
		out = append(out, n)
	}
	return out, nil
}

// maxNestedSize caps how much of a nested jar we will read.
const maxNestedSize = 64 << 20 // 64 MiB

// isJarEntry reports whether a zip entry is a nested jar worth inspecting.
//
// Only the conventional locations are considered, which keeps us from
// decoding arbitrary blobs inside a mod.
func isJarEntry(name string) bool {
	if !strings.HasSuffix(name, ".jar") {
		return false
	}
	lower := "META-INF/jars/"
	switch {
	case strings.HasPrefix(name, "jars/"):
		return true
	case strings.HasPrefix(name, lower):
		return true
	}
	return false
}

// readJarBytes parses metadata from an in-memory jar.
func readJarBytes(data []byte) (*Meta, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	return readFromZip(zr)
}

// NestedModIDs returns the set of mod ids provided by an outer jar's nested
// jars, which is what a dependency check should treat as installed.
func NestedModIDs(jarPath string) map[string]bool {
	out := map[string]bool{}
	nested, err := ReadNested(jarPath)
	if err != nil {
		return out
	}
	for _, n := range nested {
		if n.ModID != "" {
			out[strings.ToLower(n.ModID)] = true
		}
	}
	return out
}

// ProvidedIDs returns every mod id an outer jar provides: its own, plus the
// ids of everything nested inside it.
func ProvidedIDs(jarPath string) map[string]bool {
	out := NestedModIDs(jarPath)
	if meta, err := Read(jarPath); err == nil && meta != nil && meta.ModID != "" {
		out[strings.ToLower(meta.ModID)] = true
	}
	return out
}
