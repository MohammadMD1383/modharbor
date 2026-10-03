// Package mrpack reads and writes Modrinth's `.mrpack` modpack format.
//
// A `.mrpack` is a plain zip archive holding a single `modrinth.index.json`
// manifest at its root, plus an optional `overrides/` tree. The manifest lists
// every file the pack needs with its hashes and one or more download URLs;
// anything the manifest does not describe — a private jar, a config file —
// travels verbatim under `overrides/`.
//
// Reference:
// https://support.modrinth.com/en/articles/8802351-modrinth-modpack-format-mrpack
package mrpack

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MohammadMD1383/modharbor/internal/hashutil"
)

// IndexName is the manifest entry every `.mrpack` must carry at its root.
const IndexName = "modrinth.index.json"

// OverridesPrefix is the archive directory whose contents are copied verbatim
// into the instance on install.
const OverridesPrefix = "overrides/"

// ModsPrefix is the archive directory holding actual mod jars. Everything in
// here is loaded by the mod loader; nothing outside it is.
const ModsPrefix = "mods/"

// FormatVersion is the manifest schema version modharbor writes and the
// highest version it accepts. The spec has only ever defined `1`.
const FormatVersion = 1

// GameMinecraft is the only game the format currently defines.
const GameMinecraft = "minecraft"

// Environment values allowed in a file's `env` map.
const (
	EnvClient      = "client"
	EnvServer      = "server"
	EnvRequired    = "required"
	EnvOptional    = "optional"
	EnvUnsupported = "unsupported"
)

// maxManifestBytes bounds how much of modrinth.index.json is read, so a
// hostile or corrupt archive cannot exhaust memory before we validate anything.
const maxManifestBytes = 16 << 20

// Modpack is a `.mrpack` manifest.
//
// Dependencies is keyed by name. Reserved environment names (`minecraft`,
// `fabric-loader`, …) describe the instance and carry their version in
// VersionID; a key that matches one of Files describes that file's upstream
// mod instead. The two are told apart by key membership in Files, which is
// also how MarshalJSON decides what the on-disk manifest may carry.
type Modpack struct {
	// FormatVersion is the manifest schema version.
	FormatVersion int
	// Game is always "minecraft" today.
	Game string
	// VersionID identifies this particular release of the pack. Modrinth
	// expects a semver-ish string and rejects empty ones, so modharbor uses
	// the Minecraft version when it has nothing better.
	VersionID string
	// Name is the human-readable pack name.
	Name string
	// Summary is an optional one-line description.
	Summary string
	// Files lists every file the pack provides, with manifest entries and
	// archive overrides merged.
	Files []File
	// Dependencies records the instance environment and, per file, the
	// upstream project the file came from.
	Dependencies map[string]Dependency

	// source is the archive this pack was read from, or empty when it was
	// built in memory. Install needs it to recover the bytes of override
	// entries, which the manifest describes only by digest.
	source string
}

// File is one entry of the manifest's `files` array.
type File struct {
	// Path is the archive-relative, slash-separated destination inside the
	// instance, e.g. "mods/sodium-0.6.jar".
	Path string `json:"path"`
	// Hashes carries at least "sha1"; "sha512" is written when known.
	Hashes map[string]string `json:"hashes"`
	// Env records the client/server requirement as "required", "optional" or
	// "unsupported".
	Env map[string]string `json:"env,omitempty"`
	// Downloads lists mirror URLs, tried in order.
	Downloads []string `json:"downloads,omitempty"`
	// FileSize is the byte count, which lets a launcher show progress.
	FileSize int `json:"fileSize"`
}

// Name returns the file's base name, which is what lands in a mods directory.
func (f File) Name() string {
	if i := strings.LastIndex(f.Path, "/"); i >= 0 {
		return f.Path[i+1:]
	}
	return f.Path
}

// SHA1 returns the file's declared SHA-1 digest, normalised.
func (f File) SHA1() string { return hashutil.Normalize(f.Hashes["sha1"]) }

// Dependency describes one entry of the manifest's `dependencies` map.
//
// It is Modrinth's version of the concept: a project, a specific version, the
// file that version ships, and the relationship type. modharbor uses it both
// for the instance environment (where only VersionID is meaningful) and to
// remember which upstream project each exported mod came from.
type Dependency struct {
	// ProjectID is the upstream project, when known.
	ProjectID string
	// VersionID is the exact version, or the version string for an
	// environment entry.
	VersionID string
	// FileName is the jar this dependency resolves to.
	FileName string
	// DependencyType mirrors Modrinth's relation: "required", "optional",
	// "incompatible" or "embedded".
	DependencyType string
}

// version returns the single version string the manifest form can carry, so a
// round trip through the on-disk shape never silently loses information.
func (d Dependency) version() string {
	for _, v := range []string{d.VersionID, d.FileName, d.ProjectID} {
		if v != "" {
			return v
		}
	}
	return ""
}

// ─── on-disk shape ───────────────────────────────────────────────────────────

// wireModpack is the on-disk manifest.
//
// The only difference from Modpack is `dependencies`: Modrinth's spec defines
// it as a plain name → version map, so the richer Dependency values are
// projected onto it by MarshalJSON and rebuilt by UnmarshalJSON.
type wireModpack struct {
	FormatVersion int               `json:"formatVersion"`
	Game          string            `json:"game"`
	VersionID     string            `json:"versionId"`
	Name          string            `json:"name"`
	Summary       string            `json:"summary,omitempty"`
	Files         []File            `json:"files"`
	Dependencies  map[string]string `json:"dependencies"`
}

// MarshalJSON writes the spec-compliant manifest.
func (mp Modpack) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireModpack{
		FormatVersion: mp.FormatVersion,
		Game:          mp.Game,
		VersionID:     mp.VersionID,
		Name:          mp.Name,
		Summary:       mp.Summary,
		Files:         mp.Files,
		Dependencies:  mp.environmentDependencies(),
	})
}

// UnmarshalJSON reads a spec-compliant manifest.
func (mp *Modpack) UnmarshalJSON(b []byte) error {
	var w wireModpack
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	mp.FormatVersion = w.FormatVersion
	mp.Game = w.Game
	mp.VersionID = w.VersionID
	mp.Name = w.Name
	mp.Summary = w.Summary
	mp.Files = w.Files
	mp.Dependencies = nil
	for name, version := range w.Dependencies {
		if mp.Dependencies == nil {
			mp.Dependencies = make(map[string]Dependency, len(w.Dependencies))
		}
		mp.Dependencies[name] = newDependency(name, version)
	}
	return nil
}

// newDependency rebuilds one Dependency from a manifest name and version.
//
// The spec reserves a small set of names for the environment; anything else
// names a component by id and is recorded as the project id instead, so an
// unfamiliar loader key survives a round trip untouched.
func newDependency(name, version string) Dependency {
	d := Dependency{VersionID: version}
	if reservedDependencyNames[name] {
		d.DependencyType = name
	} else {
		d.ProjectID = name
	}
	return d
}

// reservedDependencyNames are the dependency names Modrinth's spec dedicates to
// the instance environment rather than to a mod.
var reservedDependencyNames = map[string]bool{
	"minecraft":     true,
	"forge":         true,
	"neoforge":      true,
	"fabric-loader": true,
	"quilt-loader":  true,
	"quilt":         true,
	"rift":          true,
	"riskofthunder": true,
}

// environmentDependencies projects the dependency map onto the name → version
// shape the manifest stores.
//
// Entries keyed by a file path describe that file's upstream mod. The format
// has nowhere to put them, so they are intentionally left out: every file
// modharbor writes already carries its download URL, and the per-file mapping
// only exists to give Install a fallback for packs that carry no `downloads`.
func (mp Modpack) environmentDependencies() map[string]string {
	isFile := make(map[string]bool, len(mp.Files))
	for _, f := range mp.Files {
		isFile[f.Path] = true
	}
	out := make(map[string]string, len(mp.Dependencies))
	for name, dep := range mp.Dependencies {
		if isFile[name] {
			continue
		}
		if v := dep.version(); v != "" {
			out[name] = v
		}
	}
	return out
}

// ─── loading ─────────────────────────────────────────────────────────────────

// Load reads and validates a `.mrpack` archive.
//
// Everything the manifest describes is merged with the archive's overrides
// tree so callers see one flat list of files; Mods and Overrides partition it
// again by directory.
func Load(path string) (*Modpack, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("%s is not a readable zip archive: %w", path, err)
	}
	defer zr.Close()

	var index *zip.File
	for _, f := range zr.File {
		if f.Name == IndexName {
			index = f
			break
		}
	}
	if index == nil {
		return nil, fmt.Errorf("%s is not a Modrinth modpack: %s is missing from the archive root", path, IndexName)
	}

	raw, err := readZipEntry(index, maxManifestBytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	mp := &Modpack{source: path}
	if err := json.Unmarshal(raw, mp); err != nil {
		return nil, fmt.Errorf("%s: %s is not valid JSON: %w", path, IndexName, err)
	}
	if err := mp.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	overrides, err := readOverrides(&zr.Reader)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	mp.appendOverrides(overrides)
	return mp, nil
}

// validate rejects a manifest that is structurally valid JSON but not a
// modharbor-installable modpack. The messages name the offending field because
// a wrong pack is usually a pack for another game, not a broken download.
func (mp *Modpack) validate() error {
	switch {
	case mp.FormatVersion <= 0:
		return fmt.Errorf("%s does not declare a formatVersion", IndexName)
	case mp.FormatVersion > FormatVersion:
		return fmt.Errorf("%s declares format version %d; modharbor understands up to %d",
			IndexName, mp.FormatVersion, FormatVersion)
	case mp.Game == "":
		return fmt.Errorf("%s does not declare a game; expected %q", IndexName, GameMinecraft)
	case mp.Game != GameMinecraft:
		return fmt.Errorf("%s is a %s modpack; modharbor only handles %s modpacks", IndexName, mp.Game, GameMinecraft)
	case strings.TrimSpace(mp.Name) == "":
		return fmt.Errorf("%s has no name", IndexName)
	}
	return nil
}

// readOverrides describes every file under overrides/ in the archive, hashing
// its bytes so an install verifies them exactly like a downloaded file.
func readOverrides(zr *zip.Reader) ([]File, error) {
	var out []File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !isOverridePath(f.Name) {
			continue
		}
		h, err := hashZipEntry(f)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f.Name, err)
		}
		out = append(out, File{
			Path:     f.Name,
			Hashes:   map[string]string{"sha1": h.SHA1, "sha512": h.SHA512},
			FileSize: int(h.Size),
		})
	}
	// A stable order keeps --json output and diffs reproducible.
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// appendOverrides merges the archive's override entries into Files.
//
// A pack may describe an override in its manifest *and* carry the bytes in the
// archive; whichever arrives first wins so no file is ever listed twice.
func (mp *Modpack) appendOverrides(overrides []File) {
	seen := make(map[string]int, len(mp.Files))
	for i, f := range mp.Files {
		seen[f.Path] = i
	}
	for _, o := range overrides {
		i, ok := seen[o.Path]
		if !ok {
			seen[o.Path] = len(mp.Files)
			mp.Files = append(mp.Files, o)
			continue
		}
		// The archive knows the real size and digest; the manifest knows the
		// env and download metadata. Neither should erase the other.
		if mp.Files[i].FileSize == 0 {
			mp.Files[i].FileSize = o.FileSize
		}
		if len(mp.Files[i].Hashes) == 0 {
			mp.Files[i].Hashes = o.Hashes
		}
	}
}

// ─── queries ─────────────────────────────────────────────────────────────────

// Mods returns the entries under mods/ — the jars a loader actually reads.
// Overrides and other directories (resourcepacks, shaderpacks, …) are
// deliberately excluded.
func (mp *Modpack) Mods() []File { return mp.inDir(ModsPrefix) }

// Overrides returns the entries under overrides/.
func (mp *Modpack) Overrides() []File { return mp.inDir(OverridesPrefix) }

func (mp *Modpack) inDir(prefix string) []File {
	var out []File
	for _, f := range mp.Files {
		if strings.HasPrefix(normalisePath(f.Path), prefix) {
			out = append(out, f)
		}
	}
	return out
}

// DependencyFor returns the dependency recorded for a file path.
func (mp *Modpack) DependencyFor(path string) (Dependency, bool) {
	d, ok := mp.Dependencies[normalisePath(path)]
	return d, ok
}

// ─── saving ──────────────────────────────────────────────────────────────────

// Save writes a valid `.mrpack` to path: modrinth.index.json at the root plus,
// when overridesDir exists, everything under it copied into overrides/.
//
// The manifest is written before the overrides so a truncated archive still
// parses; the file itself is staged next to its destination and renamed, so a
// failure never leaves a half-written pack where the user expects one.
func Save(path string, mp *Modpack, overridesDir string) error {
	if mp == nil {
		return fmt.Errorf("no modpack to save")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".modharbor-mrpack-*")
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := writeArchive(tmp, mp, overridesDir); err != nil {
		tmp.Close()
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return syncDir(dir)
}

func writeArchive(w io.Writer, mp *Modpack, overridesDir string) error {
	zw := zip.NewWriter(w)

	// Files are emitted in path order so exporting the same instance twice
	// produces identical bytes, which matters when packs live in version
	// control. The caller's slice is never reordered.
	sorted := *mp
	sorted.Files = append([]File(nil), mp.Files...)
	sort.SliceStable(sorted.Files, func(i, j int) bool {
		return sorted.Files[i].Path < sorted.Files[j].Path
	})

	manifest, err := json.MarshalIndent(sorted, "", "  ")
	if err != nil {
		zw.Close()
		return fmt.Errorf("encoding %s: %w", IndexName, err)
	}
	manifest = append(manifest, '\n')

	// The manifest goes first so an archive truncated mid-write still parses
	// far enough to tell the user what it was meant to be.
	entry, err := zw.Create(IndexName)
	if err != nil {
		zw.Close()
		return err
	}
	if _, err := entry.Write(manifest); err != nil {
		zw.Close()
		return err
	}

	if overridesDir != "" {
		if err := writeOverrides(zw, overridesDir); err != nil {
			zw.Close()
			return err
		}
	}
	return zw.Close()
}

// writeOverrides copies an overrides directory into the archive under
// overrides/, creating the directory entry so launchers that expect one are
// satisfied even for a small pack.
func writeOverrides(zw *zip.Writer, dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Nothing to override is a legitimate pack.
			return nil
		}
		return fmt.Errorf("%s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: overrides must be a directory", dir)
	}

	// The empty entry is what a launcher-created pack looks like; matching it
	// costs nothing and keeps the two producers' output comparable.
	if _, err := zw.Create(OverridesPrefix); err != nil {
		return err
	}

	return filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		name := OverridesPrefix + filepath.ToSlash(rel)
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := w.Write(body); err != nil {
			return err
		}
		return nil
	})
}

// ─── shared helpers ──────────────────────────────────────────────────────────

// normalisePath rewrites an archive-relative path to the slash-separated,
// dot-free form the spec mandates, so a hand-written manifest using "./mods/x"
// or backslashes still partitions correctly.
func normalisePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "./")
	return p
}

// isOverridePath reports whether an archive path lives in the overrides tree.
func isOverridePath(p string) bool {
	return strings.HasPrefix(normalisePath(p), OverridesPrefix)
}

func readZipEntry(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", f.Name, err)
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, limit))
}

// hashZipEntry digests an archive entry without buffering it, using the same
// multi-hash pass the rest of modharbor uses.
func hashZipEntry(f *zip.File) (hashutil.Hashes, error) {
	rc, err := f.Open()
	if err != nil {
		return hashutil.Hashes{}, err
	}
	defer rc.Close()
	h := hashutil.NewMulti()
	if _, err := io.Copy(h, rc); err != nil {
		return hashutil.Hashes{}, err
	}
	return h.Sum(), nil
}
