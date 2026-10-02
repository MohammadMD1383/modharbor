// Package instance models a Minecraft installation directory and discovers
// the instances inside it.
//
// The canonical shape is the vanilla launcher layout:
//
//	<root>/versions/<id>/<id>.json     version metadata (loader, libraries)
//	<root>/versions/<id>/mods/*.jar     installed mods
//
// Several third-party launchers add sidecar files that carry useful
// information; modharbor reads them opportunistically:
//
//	TLauncherAdditional.json  MC version, loader, per-mod CurseForge ids
//	mmc-pack.json              MultiMC instance metadata
//	instance.cfg               Prism / ATLauncher metadata
package instance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Type classifies a version directory.
type Type string

// Recognised instance kinds.
const (
	TypeVanilla  Type = "vanilla"
	TypeFabric   Type = "fabric"
	TypeQuilt    Type = "quilt"
	TypeForge    Type = "forge"
	TypeNeoForge Type = "neoforge"
	TypeUnknown  Type = "unknown"
)

// Info is a discovered Minecraft instance.
type Info struct {
	// ID is the version id from the version JSON (also the directory name).
	ID string
	// Name is a human-friendly label.
	Name string
	// Path is the instance directory.
	Path string
	// MCVersion is the Minecraft release this instance runs.
	MCVersion string
	// Type is the mod loader family.
	Type Type
	// LoaderVersion is the loader version, when known.
	LoaderVersion string
	// JavaVersion is the required Java major version, when declared.
	JavaVersion int
	// ModsDir is the mods directory (may not exist yet).
	ModsDir string
	// ModCount is the number of jar files present.
	ModCount int
	// LastModified is used as a fallback sort key.
	LastModified int64
	// Launcher records which launcher owns this instance, if detected.
	Launcher string
	// InheritsFrom is the parent version id for inherited installs.
	InheritsFrom string
	// Warnings collects non-fatal problems found while inspecting.
	Warnings []string
}

// Label renders "name (MC 26.3, Fabric)".
func (i *Info) Label() string {
	t := i.Type
	if t == TypeUnknown {
		t = "vanilla"
	}
	mc := i.MCVersion
	if mc == "" {
		mc = "unknown"
	}
	return i.Name + " (MC " + mc + ", " + strings.Title(string(t)) + ")" //nolint:staticcheck
}

// Dir returns the mods directory, creating nothing.
func (i *Info) Dir(sub string) string { return filepath.Join(i.Path, sub) }

// ModsDirOrDefault returns ModsDir, falling back to <path>/mods.
func (i *Info) ModsDirOrDefault() string {
	if i.ModsDir != "" {
		return i.ModsDir
	}
	return filepath.Join(i.Path, "mods")
}

// IsModded reports whether the instance has any loader.
func (i *Info) IsModded() bool {
	switch i.Type {
	case TypeFabric, TypeQuilt, TypeForge, TypeNeoForge:
		return true
	}
	return false
}

// VersionJSONPath returns the primary version metadata file path.
func (i *Info) VersionJSONPath() string {
	return filepath.Join(i.Path, i.ID+".json")
}

// ─── version JSON schema (subset) ───────────────────────────────────────────

type versionJSON struct {
	ID           string                          `json:"id"`
	InheritsFrom string                          `json:"inheritsFrom"`
	MainClass    string                          `json:"mainClass"`
	Type         string                          `json:"type"`
	JavaVersion  *struct{ MajorVersion float64 } `json:"javaVersion"`
	Libraries    []struct {
		Name  string `json:"name"`
		Rules []struct {
			Action string                 `json:"action"`
			OS     *struct{ Name string } `json:"os"`
		} `json:"rules"`
	} `json:"libraries"`
	Arguments struct {
		Game []json.RawMessage `json:"game"`
	} `json:"arguments"`
}

type libraryName struct {
	Group      string
	Artifact   string
	Version    string
	Classifier string
	Ext        string
}

func parseLibrary(s string) (libraryName, bool) {
	parts := strings.Split(s, ":")
	if len(parts) < 3 {
		return libraryName{}, false
	}
	ln := libraryName{Group: parts[0], Artifact: parts[1]}
	if len(parts) >= 3 {
		ln.Version = parts[2]
	}
	if len(parts) >= 4 {
		ln.Classifier = parts[3]
	}
	if len(parts) >= 5 {
		ln.Ext = parts[4]
	}
	return ln, true
}

// ─── Discovery ──────────────────────────────────────────────────────────────

// Discover lists all instances under the given Minecraft root.
// When root is a single instance directory, only that instance is returned.
func Discover(root string) ([]*Info, error) {
	fi, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, &os.PathError{Op: "discover", Path: root, Err: os.ErrInvalid}
	}

	// A directory that directly contains <id>.json is itself an instance.
	if _, err := findVersionJSON(root); err == nil {
		inst, err := Load(root)
		if err != nil {
			return nil, err
		}
		return []*Info{inst}, nil
	}

	versionsDir := filepath.Join(root, "versions")
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		return nil, &os.PathError{Op: "discover", Path: root, Err: err}
	}

	var out []*Info
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(versionsDir, e.Name())
		inst, err := Load(dir)
		if err != nil {
			// Skip directories that are not real instances.
			continue
		}
		if fi, err := e.Info(); err == nil {
			inst.LastModified = fi.ModTime().Unix()
		}
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// findVersionJSON returns the version metadata file inside an instance dir.
func findVersionJSON(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var candidates []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".json")
		if base == filepath.Base(dir) || base == dirBaseName(dir) {
			candidates = append(candidates, filepath.Join(dir, e.Name()))
		}
	}
	if len(candidates) == 0 {
		// Fall back to any json containing a "mainClass" or "libraries" key.
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if looksLikeVersionJSON(p) {
				candidates = append(candidates, p)
			}
		}
	}
	if len(candidates) == 0 {
		return "", os.ErrNotExist
	}
	sort.Strings(candidates)
	return candidates[0], nil
}

func dirBaseName(dir string) string { return filepath.Base(dir) }

func looksLikeVersionJSON(p string) bool {
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	var probe struct {
		MainClass string `json:"mainClass"`
		Libraries []any  `json:"libraries"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return false
	}
	return probe.MainClass != "" || probe.Libraries != nil
}

// Load inspects a single instance directory.
func Load(dir string) (*Info, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	jsonPath, err := findVersionJSON(abs)
	if err != nil {
		return nil, &os.PathError{Op: "load instance", Path: dir, Err: err}
	}

	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, err
	}
	var vj versionJSON
	if err := json.Unmarshal(raw, &vj); err != nil {
		return nil, &os.PathError{Op: "parse version json", Path: jsonPath, Err: err}
	}

	inst := &Info{
		ID:           vj.ID,
		Path:         abs,
		ModsDir:      filepath.Join(abs, "mods"),
		Type:         TypeVanilla,
		InheritsFrom: vj.InheritsFrom,
	}
	if inst.ID == "" {
		inst.ID = filepath.Base(abs)
	}
	inst.Name = inst.ID
	if vj.JavaVersion != nil {
		inst.JavaVersion = int(vj.JavaVersion.MajorVersion)
	}

	// Loader detection from libraries (most reliable).
	for _, l := range vj.Libraries {
		ln, ok := parseLibrary(l.Name)
		if !ok {
			continue
		}
		switch {
		case ln.Group == "net.fabricmc" && ln.Artifact == "fabric-loader":
			inst.Type = TypeFabric
			inst.LoaderVersion = ln.Version
		case ln.Group == "org.quiltmc" && ln.Artifact == "quilt-loader":
			inst.Type = TypeQuilt
			inst.LoaderVersion = ln.Version
		case strings.Contains(ln.Group, "neoforged") && ln.Artifact == "neoforge":
			inst.Type = TypeNeoForge
			inst.LoaderVersion = ln.Version
		case ln.Group == "net.minecraftforge" || ln.Group == "net.minecraftforge.fml":
			inst.Type = TypeForge
			inst.LoaderVersion = ln.Version
		}
	}

	// Loader detection from mainClass as a fallback.
	if inst.Type == TypeVanilla {
		switch {
		case strings.HasPrefix(vj.MainClass, "net.fabricmc.loader"):
			inst.Type = TypeFabric
		case strings.HasPrefix(vj.MainClass, "org.quiltmc.loader"):
			inst.Type = TypeQuilt
		case strings.Contains(vj.MainClass, "neoforge"):
			inst.Type = TypeNeoForge
		case strings.Contains(vj.MainClass, "forge"):
			inst.Type = TypeForge
		}
	}

	// Minecraft version: from the loader jar in game args, from sidecars, or
	// from the version id / directory name.
	inst.MCVersion = mcVersionFromArgs(&vj)
	if inst.MCVersion == "" {
		inst.MCVersion = mcVersionFromSidecars(abs)
	}
	if inst.MCVersion == "" {
		inst.MCVersion = mcVersionFromID(inst.ID)
	}

	// Launcher metadata (may refine MC version and add mod ids).
	if lm := readLauncherMeta(abs); lm != nil {
		if lm.MCVersion != "" {
			inst.MCVersion = lm.MCVersion
		}
		if lm.Loader != "" && inst.Type == TypeVanilla {
			inst.Type = Type(lm.Loader)
		}
		if lm.LoaderVersion != "" && inst.LoaderVersion == "" {
			inst.LoaderVersion = lm.LoaderVersion
		}
		inst.Launcher = lm.Launcher
	}

	inst.ModCount = countMods(inst.ModsDir)
	return inst, nil
}

// mcVersionFromSidecars looks for a concrete Minecraft version inside any
// launcher metadata that ships with the instance directory. TLauncher is the
// most common source; its "jar" field is exactly the MC version.
func mcVersionFromSidecars(dir string) string {
	if b, err := os.ReadFile(filepath.Join(dir, "TLauncherAdditional.json")); err == nil {
		var doc tLauncherDoc
		if json.Unmarshal(b, &doc) == nil {
			for _, cand := range []string{
				doc.Jar,
				doc.Modpack.Version.GameVersionDTO.Name,
			} {
				if cand != "" && mcVersionFromID(cand) != "" {
					return mcVersionFromID(cand)
				}
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "mmc-pack.json")); err == nil {
		var doc struct {
			MinecraftVersion string `json:"MinecraftVersion"`
		}
		if json.Unmarshal(b, &doc) == nil && doc.MinecraftVersion != "" {
			return doc.MinecraftVersion
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "instance.cfg")); err == nil {
		kv := parseKV(string(b))
		if v, ok := kv["MinecraftVersion"]; ok && v != "" {
			return v
		}
	}
	return ""
}

// mcVersionFromArgs scans --version / --gameDir arguments for a version id.
func mcVersionFromArgs(vj *versionJSON) string {
	// The launcher substitutes ${version_name} at runtime, so args rarely
	// carry a concrete id. TLauncher and MultiMC sometimes inline it though.
	for _, raw := range vj.Arguments.Game {
		var vals struct {
			Values []string `json:"values"`
		}
		if err := json.Unmarshal(raw, &vals); err != nil {
			continue
		}
		for _, v := range vals.Values {
			if v == "${version_name}" {
				continue
			}
			if mv := mcVersionFromID(v); mv != "" {
				return mv
			}
		}
	}
	return ""
}

var mcVersionPrefix = regexp.MustCompile(`^v?(\d+(?:\.\d+)*)(?:[-_ ].*)?$`)

// mcVersionFromID extracts a Minecraft version such as "26.3" or "1.21.1"
// from an instance id like "26.3-fabric-mod" or "1.20.1-forge-47.2.0".
func mcVersionFromID(id string) string {
	// Prefer the leading dotted-numeric run.
	if m := regexp.MustCompile(`^\s*v?(\d+(?:\.\d+)+)`).FindStringSubmatch(id); m != nil {
		return m[1]
	}
	if m := mcVersionPrefix.FindStringSubmatch(id); m != nil {
		return m[1]
	}
	return ""
}

// countMods counts jar files in the mods directory.
func countMods(modsDir string) int {
	entries, err := os.ReadDir(modsDir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if isJarName(e.Name()) {
			n++
		}
	}
	return n
}

func isJarName(name string) bool {
	l := strings.ToLower(name)
	return strings.HasSuffix(l, ".jar")
}

// ModFiles lists the jar files in an instance's mods directory, sorted by
// name. Missing directories yield an empty slice rather than an error.
func ModFiles(modsDir string) ([]string, error) {
	entries, err := os.ReadDir(modsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !isJarName(e.Name()) {
			continue
		}
		files = append(files, filepath.Join(modsDir, e.Name()))
	}
	sort.Strings(files)
	return files, nil
}

// Resolve turns a user-supplied instance reference into a concrete instance.
//
// It accepts, in order of preference:
//
//	an absolute or relative path to an instance directory
//	a path to a versions/<id> directory
//	a bare version id ("26.3-fabric-mod")
//	"current" for the single instance in the directory
func Resolve(root, ref string) (*Info, error) {
	if ref == "" {
		return nil, &os.PathError{Op: "resolve", Path: "", Err: os.ErrNotExist}
	}

	// Direct directory path.
	if strings.ContainsRune(ref, os.PathSeparator) || strings.HasPrefix(ref, ".") {
		if inst, err := Load(ref); err == nil {
			return inst, nil
		}
	}

	instances, err := Discover(root)
	if err != nil {
		return nil, err
	}
	if len(instances) == 0 {
		return nil, os.ErrNotExist
	}

	if strings.EqualFold(ref, "current") || strings.EqualFold(ref, ".") {
		if len(instances) == 1 {
			return instances[0], nil
		}
		return nil, fmtMulti(instances)
	}

	// Exact id match.
	for _, i := range instances {
		if strings.EqualFold(i.ID, ref) {
			return i, nil
		}
	}
	// Prefix match, then contains match.
	for _, i := range instances {
		if strings.HasPrefix(strings.ToLower(i.ID), strings.ToLower(ref)) {
			return i, nil
		}
	}
	for _, i := range instances {
		if strings.Contains(strings.ToLower(i.ID), strings.ToLower(ref)) {
			return i, nil
		}
	}
	return nil, &os.PathError{Op: "resolve instance", Path: ref, Err: os.ErrNotExist}
}

func fmtMulti(instances []*Info) error {
	var b strings.Builder
	b.WriteString("multiple instances found, specify one of:")
	for _, i := range instances {
		b.WriteString("\n  " + i.ID)
	}
	return &os.PathError{Op: "resolve", Path: "current", Err: errMulti{msg: b.String()}}
}

type errMulti struct{ msg string }

func (e errMulti) Error() string { return e.msg }

// ─── launcher sidecars ─────────────────────────────────────────────────────

type launcherMeta struct {
	MCVersion     string
	Loader        string
	LoaderVersion string
	Launcher      string
}

// readLauncherMeta inspects known launcher sidecar files.
func readLauncherMeta(dir string) *launcherMeta {
	// TLauncher / TMultiMC
	if b, err := os.ReadFile(filepath.Join(dir, "TLauncherAdditional.json")); err == nil {
		if m := parseTLauncher(b); m != nil {
			return m
		}
	}
	// MultiMC / Prism
	if b, err := os.ReadFile(filepath.Join(dir, "mmc-pack.json")); err == nil {
		if m := parseMultiMC(b); m != nil {
			return m
		}
	}
	// PrismLauncher instance.cfg
	if b, err := os.ReadFile(filepath.Join(dir, "instance.cfg")); err == nil {
		if m := parsePrismCfg(string(b)); m != nil {
			return m
		}
	}
	// ATLauncher / plain instance.cfg fallback
	if b, err := os.ReadFile(filepath.Join(dir, "ATLauncher.cfg")); err == nil {
		_ = b
		return &launcherMeta{Launcher: "ATLauncher"}
	}
	return nil
}

// TLauncherMod records one mod entry from TLauncher's sidecar.
type TLauncherMod struct {
	CurseForgeID int    `json:"id"`
	Name         string `json:"name"`
	Author       string `json:"author"`
	LinkProject  string `json:"linkProject"`
	FilePath     string `json:"-"`
	SHA1         string `json:"-"`
}

type tLauncherDoc struct {
	Jar         string `json:"jar"`
	SkinVersion any    `json:"skinVersion"`
	Modpack     struct {
		Name    string `json:"name"`
		Version struct {
			MinecraftVersionName struct {
				Name string `json:"name"`
			} `json:"minecraftVersionName"`
			MinecraftVersionTypes []struct {
				Name string `json:"name"`
			} `json:"minecraftVersionTypes"`
			GameVersionDTO struct {
				Name string `json:"name"`
			} `json:"gameVersionDTO"`
			Mods []struct {
				ID      int    `json:"id"`
				Name    string `json:"name"`
				Author  string `json:"author"`
				Link    string `json:"linkProject"`
				Version struct {
					Metadata struct {
						SHA1 string `json:"sha1"`
						Path string `json:"path"`
					} `json:"metadata"`
				} `json:"version"`
			} `json:"mods"`
		} `json:"version"`
	} `json:"modpack"`
}

func parseTLauncher(b []byte) *launcherMeta {
	var doc tLauncherDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil
	}
	m := &launcherMeta{
		Launcher:      "TLauncher",
		MCVersion:     doc.Jar,
		LoaderVersion: doc.Modpack.Version.MinecraftVersionName.Name,
	}
	if m.MCVersion == "" {
		m.MCVersion = doc.Modpack.Version.GameVersionDTO.Name
	}
	if len(doc.Modpack.Version.MinecraftVersionTypes) > 0 {
		m.Loader = strings.ToLower(doc.Modpack.Version.MinecraftVersionTypes[0].Name)
	}
	return m
}

func parseMultiMC(b []byte) *launcherMeta {
	var doc struct {
		InstanceType     string `json:"InstanceType"`
		MinecraftVersion string `json:"MinecraftVersion"`
		LoaderVersion    string `json:"LoaderVersion"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil
	}
	m := &launcherMeta{
		Launcher: "MultiMC/Prism",
		Loader:   strings.ToLower(doc.InstanceType),
	}
	m.MCVersion = doc.MinecraftVersion
	m.LoaderVersion = doc.LoaderVersion
	return m
}

func parsePrismCfg(s string) *launcherMeta {
	kv := parseKV(s)
	m := &launcherMeta{Launcher: "Prism"}
	if v, ok := kv["InstanceType"]; ok {
		m.Loader = strings.ToLower(v)
	}
	if v, ok := kv["MinecraftVersion"]; ok {
		m.MCVersion = v
	}
	if v, ok := kv["ForgeVersion"]; ok {
		m.Loader = "forge"
		m.LoaderVersion = v
	}
	if v, ok := kv["FabricLoaderVersion"]; ok {
		m.Loader = "fabric"
		m.LoaderVersion = v
	}
	return m
}

func parseKV(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		i := strings.Index(line, "=")
		if i < 0 {
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.TrimSpace(line[i+1:])
		if k != "" {
			out[k] = v
		}
	}
	return out
}

// ParseTLauncherMods extracts the per-mod CurseForge mapping from a
// TLauncher sidecar. This is the single most useful signal for launchers
// that installed mods from CurseForge: it gives us project ids we can cross
// reference, plus the exact sha1 the launcher downloaded.
func ParseTLauncherMods(dir string) (map[string]TLauncherMod, error) {
	b, err := os.ReadFile(filepath.Join(dir, "TLauncherAdditional.json"))
	if err != nil {
		return nil, err
	}
	var doc tLauncherDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	out := make(map[string]TLauncherMod, len(doc.Modpack.Version.Mods))
	for _, m := range doc.Modpack.Version.Mods {
		if m.Version.Metadata.Path == "" {
			continue
		}
		base := filepath.Base(m.Version.Metadata.Path)
		out[base] = TLauncherMod{
			CurseForgeID: m.ID,
			Name:         m.Name,
			Author:       m.Author,
			LinkProject:  m.Link,
			FilePath:     m.Version.Metadata.Path,
			SHA1:         strings.ToLower(m.Version.Metadata.SHA1),
		}
	}
	return out, nil
}
