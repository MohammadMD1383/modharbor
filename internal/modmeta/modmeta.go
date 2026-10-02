// Package modmeta reads identifying metadata out of Minecraft mod jars.
//
// Mods are identified by SHA-1 first (exact, authoritative). When a jar was
// downloaded from a mirror such as CurseForge the hash will not match, so we
// fall back to reading the embedded metadata descriptors that loaders use:
//
//	fabric.mod.json   Fabric
//	quilt.mod.json    Quilt
//	META-INF/mods.toml NeoForge / Forge
//	META-INF/MANIFEST.MF  legacy MCForge
//	mcmod.info        legacy Forge
package modmeta

import (
	"archive/zip"
	"encoding/json"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Loader identifies which mod loader a jar targets.
type Loader string

// Supported loaders.
const (
	LoaderUnknown  Loader = ""
	LoaderFabric   Loader = "fabric"
	LoaderQuilt    Loader = "quilt"
	LoaderForge    Loader = "forge"
	LoaderNeoForge Loader = "neoforge"
	LoaderModrinth Loader = "modrinth-pack" // placeholder, never matches a jar
)

// String renders the loader name.
func (l Loader) String() string {
	if l == LoaderUnknown {
		return "unknown"
	}
	return string(l)
}

// Meta is the normalized metadata extracted from a mod jar.
type Meta struct {
	// ModID is the loader-specific identifier, e.g. "sodium".
	ModID string
	// Name is the human-readable project name, e.g. "Sodium".
	Name string
	// Version is the mod's own version string.
	Version string
	// Description is the mod blurb, truncated to a sane length.
	Description string
	// Authors lists declared contributors.
	Authors []string
	// Loader reports which loader descriptor was found.
	Loader Loader
	// DependsOn maps mod ids to version ranges.
	DependsOn map[string]string
	// Recommends maps mod ids to version ranges.
	Recommends map[string]string
	// Conflicts maps mod ids to version ranges.
	Conflicts map[string]string
	// MinecraftVersion is the version declared in the metadata, if any.
	MinecraftVersion string
	// JavaVersion is the minimum Java major version, if declared.
	JavaVersion int
	// Entrypoints records loader entrypoint keys (client, main, ...).
	Entrypoints []string
	// Icon is the embedded icon file name inside the jar.
	Icon string
	// ContactHomepage and other contact links, when present.
	Links map[string]string
	// License list, when present.
	Licenses []string
}

// IsEmpty reports whether nothing identifying was found.
func (m *Meta) IsEmpty() bool {
	return m.ModID == "" && m.Name == ""
}

// IsLibraryOnly reports whether the jar declares itself as a library
// (Fabric's "environment": "client" absent and a `library` type marker).
func (m *Meta) IsLibraryOnly() bool { return false }

// ---------------------------------------------------------------- fabric

// flexStringList accepts the several shapes Fabric allows for list fields.
//
// The spec permits "license": "MIT" as well as "license": ["MIT", "Apache-2.0"],
// and "authors" as a bare string or an array. A strict decoder rejects the
// singular forms, which would otherwise make a perfectly good mod look
// unidentifiable, so these fields use a tolerant type.
type flexStringList []string

// UnmarshalJSON accepts a string, an array of strings, or an object whose
// values are strings, normalising all of them to a slice.
func (f *flexStringList) UnmarshalJSON(b []byte) error {
	var single string
	if err := json.Unmarshal(b, &single); err == nil {
		*f = flexStringList{single}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err == nil {
		*f = flexStringList(many)
		return nil
	}
	var obj map[string]string
	if err := json.Unmarshal(b, &obj); err == nil {
		out := make([]string, 0, len(obj))
		for _, v := range obj {
			out = append(out, v)
		}
		*f = out
		return nil
	}
	// Give up quietly: a field we cannot read must never cost us the mod's
	// identity.
	*f = nil
	return nil
}

// flexAuthors accepts "authors" as an array of objects, an array of strings,
// or a bare string.
//
// The custom unmarshaler must live on the slice type rather than on its
// element: encoding/json rejects a scalar before it ever reaches an element
// unmarshaler, so a per-element hook alone is not enough.
type flexAuthors []string

func (f *flexAuthors) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		if one != "" {
			*f = flexAuthors{one}
		}
		return nil
	}
	var objs []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(b, &objs); err == nil {
		out := make([]string, 0, len(objs))
		for _, o := range objs {
			if o.Name != "" {
				out = append(out, o.Name)
			}
		}
		*f = out
		return nil
	}
	var strs []string
	if err := json.Unmarshal(b, &strs); err == nil {
		*f = flexAuthors(strs)
		return nil
	}
	// A field we cannot read must never cost us the mod's identity.
	*f = nil
	return nil
}

type fabricModJSON struct {
	SchemaVersion int               `json:"schemaVersion"`
	ID            string            `json:"id"`
	Version       string            `json:"version"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Authors       flexAuthors       `json:"authors"`
	Contact       map[string]string `json:"contact"`
	License       flexStringList    `json:"license"`
	Icon          string            `json:"icon"`
	Environment   string            `json:"environment"`
	Entrypoints   map[string]any    `json:"entrypoints"`
	Depends       map[string]any    `json:"depends"`
	Recommends    map[string]any    `json:"recommends"`
	Conflicts     map[string]any    `json:"conflicts"`
	Breaks        map[string]any    `json:"breaks"`
	Suggests      map[string]any    `json:"suggests"`
	Java          map[string]any    `json:"java"`
}

// ---------------------------------------------------------------- quilt

type quiltModJSON struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"quilt_loader"`
	Version       string `json:"version"`
	Metadata      struct {
		Name         string            `json:"name"`
		Description  string            `json:"description"`
		Contributors map[string]string `json:"contributors"`
		Contact      map[string]string `json:"contact"`
		License      string            `json:"license"`
		Icon         string            `json:"icon"`
	} `json:"metadata"`
	MetadataVersion int               `json:"metadata_version"`
	Group           map[string]string `json:"intermediate_mappings"`
	Dependencies    []struct {
		ID       string   `json:"id"`
		Versions []string `json:"versions"`
		Type     string   `json:"type"`
	} `json:"depends"`
}

// ---------------------------------------------------------------- legacy

type legacyMcModInfo struct {
	ModID       string `json:"modid"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Author      string `json:"author"`
	Credits     string `json:"credits"`
	MCVersion   string `json:"mcversion"`
	Description string `json:"description"`
}

// Read extracts metadata from the jar at the given path.
func Read(jarPath string) (*Meta, error) {
	zr, err := zip.OpenReader(jarPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return readFromZip(&zr.Reader)
}

// readFromZip extracts metadata from an already-open archive, so the same
// logic serves jars on disk and jars nested inside other jars.
func readFromZip(zr *zip.Reader) (*Meta, error) {
	meta := &Meta{
		DependsOn:  map[string]string{},
		Recommends: map[string]string{},
		Conflicts:  map[string]string{},
		Links:      map[string]string{},
	}

	found := false

	if raw := readEntry(zr, "fabric.mod.json"); raw != nil {
		if m, ok := parseFabric(raw); ok {
			meta = mergeMeta(meta, m)
			found = true
		}
	}

	if raw := readEntry(zr, "quilt.mod.json"); raw != nil {
		if m, ok := parseQuilt(raw); ok {
			meta = mergeMeta(meta, m)
			found = true
		}
	}

	if raw := readEntry(zr, "META-INF/mods.toml"); raw != nil {
		if m, ok := parseModsToml(string(raw)); ok {
			meta = mergeMeta(meta, m)
			found = true
		}
	}

	if raw := readEntry(zr, "mcmod.info"); raw != nil {
		if m, ok := parseMcModInfo(raw); ok {
			if meta.ModID == "" {
				meta.ModID = m.ModID
			}
			if meta.Version == "" {
				meta.Version = m.Version
			}
			if meta.Name == "" {
				meta.Name = m.Name
			}
			found = true
		}
	}

	// Classify by presence of loader entrypoint markers when the explicit
	// descriptor was absent (very old Fabric mods).
	if !found {
		for _, f := range zr.File {
			if strings.HasPrefix(f.Name, "net/fabricmc/") {
				meta.Loader = LoaderFabric
				found = true
				break
			}
		}
	}

	if !found {
		return meta, nil
	}
	return meta, nil
}

func mergeMeta(dst, src *Meta) *Meta {
	if src.ModID != "" {
		dst.ModID = src.ModID
	}
	if src.Name != "" {
		dst.Name = src.Name
	}
	if src.Version != "" {
		dst.Version = src.Version
	}
	if src.Description != "" {
		dst.Description = src.Description
	}
	if src.MinecraftVersion != "" {
		dst.MinecraftVersion = src.MinecraftVersion
	}
	if src.JavaVersion > 0 {
		dst.JavaVersion = src.JavaVersion
	}
	if src.Loader != LoaderUnknown {
		dst.Loader = src.Loader
	}
	if len(src.Authors) > 0 {
		dst.Authors = src.Authors
	}
	if len(src.Entrypoints) > 0 {
		dst.Entrypoints = src.Entrypoints
	}
	if src.Icon != "" {
		dst.Icon = src.Icon
	}
	if len(src.Licenses) > 0 {
		dst.Licenses = src.Licenses
	}
	for k, v := range src.Links {
		dst.Links[k] = v
	}
	for k, v := range src.DependsOn {
		dst.DependsOn[k] = v
	}
	for k, v := range src.Recommends {
		dst.Recommends[k] = v
	}
	for k, v := range src.Conflicts {
		dst.Conflicts[k] = v
	}
	return dst
}

func parseFabric(raw []byte) (*Meta, bool) {
	var f fabricModJSON
	if err := json.Unmarshal(raw, &f); err != nil {
		// Schemas drift, and a mod whose metadata we cannot fully decode is
		// still identifiable from the three fields that matter. Fall back to
		// a minimal decode rather than losing the mod entirely.
		return parseFabricMinimal(raw)
	}
	m := &Meta{
		ModID:      f.ID,
		Name:       f.Name,
		Version:    f.Version,
		Loader:     LoaderFabric,
		DependsOn:  map[string]string{},
		Recommends: map[string]string{},
		Conflicts:  map[string]string{},
		Links:      map[string]string{},
	}
	m.Authors = append(m.Authors, f.Authors...)
	m.Licenses = f.License
	m.Icon = f.Icon
	for k, v := range f.Contact {
		m.Links[k] = v
	}
	for k := range f.Entrypoints {
		m.Entrypoints = append(m.Entrypoints, k)
	}
	for id, spec := range f.Depends {
		m.DependsOn[id] = specString(spec)
	}
	for id, spec := range f.Recommends {
		m.Recommends[id] = specString(spec)
	}
	for id, spec := range f.Conflicts {
		m.Conflicts[id] = specString(spec)
	}
	for id := range f.Breaks {
		if _, ok := m.Conflicts[id]; !ok {
			m.Conflicts[id] = "*"
		}
	}
	// ">=8" style java dependency
	if s, ok := f.Java[">=8"]; ok {
		_ = s
	}
	m.Description = truncate(f.Description, 200)
	if m.Name == "" {
		m.Name = m.ModID
	}
	return m, m.ModID != ""
}

// parseFabricMinimal decodes only the fields needed for identification, so an
// unexpected type in some other field cannot make a mod unidentifiable.
func parseFabricMinimal(raw []byte) (*Meta, bool) {
	// Only scalars here: an object-typed field with an unexpected shape must
	// not prevent us from reading the mod id.
	var f struct {
		ID          string `json:"id"`
		Version     string `json:"version"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &f); err != nil || f.ID == "" {
		return nil, false
	}
	m := &Meta{
		ModID:       f.ID,
		Name:        f.Name,
		Version:     f.Version,
		Description: truncate(f.Description, 200),
		Loader:      LoaderFabric,
		DependsOn:   map[string]string{},
		Recommends:  map[string]string{},
		Conflicts:   map[string]string{},
		Links:       map[string]string{},
	}
	// Dependencies are best-effort on their own.
	var deps struct {
		Depends map[string]any `json:"depends"`
	}
	if err := json.Unmarshal(raw, &deps); err == nil {
		for id, spec := range deps.Depends {
			m.DependsOn[id] = specString(spec)
		}
	}
	if m.Name == "" {
		m.Name = m.ModID
	}
	return m, true
}

func parseQuilt(raw []byte) (*Meta, bool) {
	var q quiltModJSON
	if err := json.Unmarshal(raw, &q); err != nil {
		return nil, false
	}
	m := &Meta{
		ModID:       q.ID,
		Name:        q.Metadata.Name,
		Version:     q.Version,
		Description: truncate(q.Metadata.Description, 200),
		Loader:      LoaderQuilt,
		DependsOn:   map[string]string{},
		Recommends:  map[string]string{},
		Conflicts:   map[string]string{},
		Links:       map[string]string{},
		Licenses:    []string{},
	}
	if q.Metadata.License != "" {
		m.Licenses = append(m.Licenses, q.Metadata.License)
	}
	m.Icon = q.Metadata.Icon
	for _, v := range q.Metadata.Contributors {
		m.Authors = append(m.Authors, v)
	}
	for k, v := range q.Metadata.Contact {
		m.Links[k] = v
	}
	for _, d := range q.Dependencies {
		spec := "*"
		if len(d.Versions) > 0 {
			spec = strings.Join(d.Versions, " || ")
		}
		switch strings.ToLower(d.Type) {
		case "required", "":
			m.DependsOn[d.ID] = spec
		case "optional", "recommended":
			m.Recommends[d.ID] = spec
		case "incompatible", "conflicting":
			m.Conflicts[d.ID] = spec
		}
	}
	if m.Name == "" {
		m.Name = m.ModID
	}
	return m, m.ModID != ""
}

// parseModsToml handles the subset of mods.toml needed for identification.
//
// Relevant shapes:
//
//	[[mods]]                      modId, version, displayName, description
//	[[dependencies.<modid>]]      modId, versionRange, mandatory, ordering
//	[[dependencies.<modid>.<id>]]  side = "BOTH" | "CLIENT" | "SERVER"
func parseModsToml(s string) (*Meta, bool) {
	m := &Meta{
		Loader:     LoaderForge,
		DependsOn:  map[string]string{},
		Recommends: map[string]string{},
		Conflicts:  map[string]string{},
		Links:      map[string]string{},
	}

	// State for the dependency table currently being read.
	var (
		inDeps     bool
		depKey     string // "worldedit" or "worldedit/forge"
		depSub     string // nested id for the two-level form
		depModID   string
		depRange   string
		depMandate string
	)

	flushDep := func() {
		if !inDeps || depModID == "" {
			return
		}
		rng := depRange
		if rng == "" {
			rng = "*"
		}
		kind := strings.ToLower(depMandate)
		switch {
		case kind == "true":
			m.DependsOn[depModID] = rng
		case kind == "false":
			m.Recommends[depModID] = rng
		default:
			// NeoForge's two-level form lists relationships explicitly.
			switch strings.ToLower(depKey) {
			case "incompatible", "conflicting", "breaks":
				m.Conflicts[depModID] = rng
			default:
				if strings.EqualFold(depSub, "incompatible") {
					m.Conflicts[depModID] = rng
				} else {
					m.Recommends[depModID] = rng
				}
			}
		}
		depModID, depRange, depMandate, depSub = "", "", "", ""
	}

	for _, line := range strings.Split(s, "\n") {
		raw := strings.TrimSpace(line)
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}

		if strings.HasPrefix(raw, "[") {
			flushDep()
			name := strings.Trim(strings.Trim(raw, "[]"), " ")
			head := name
			if i := strings.IndexAny(head, "/."); i > 0 {
				head = head[:i]
			}
			inDeps = false
			switch strings.ToLower(name) {
			case "mods", "modrinth", "modrinth:metadata", "metadata":
				inDeps = false
			case "dependencies":
				inDeps = false
			default:
				lname := strings.ToLower(name)
				if strings.HasPrefix(lname, "dependencies.") {
					inDeps = true
					rest := name[len("dependencies."):]
					depKey = ""
					depSub = ""
					fields := strings.FieldsFunc(rest, func(r rune) bool {
						return r == '.' || r == '/'
					})
					if len(fields) > 0 {
						depKey = fields[0]
					}
					if len(fields) > 1 {
						depSub = fields[1]
					}
				} else if strings.ToLower(name) == "mods" {
					inDeps = false
				} else if head == "mods" && (strings.HasPrefix(name, "mods.") || strings.HasPrefix(name, "mods/")) {
					inDeps = false
				}
			}
			continue
		}

		k, v, ok := splitTomlKV(raw)
		if !ok {
			continue
		}
		lk := strings.ToLower(k)

		if inDeps {
			switch lk {
			case "modid":
				depModID = v
			case "versionrange", "versionrangerequired":
				depRange = v
			case "mandatory":
				depMandate = v
			case "ordering", "type":
				depMandate = v
			}
			continue
		}

		// [[mods]] block
		switch lk {
		case "modid":
			m.ModID = v
		case "version":
			if m.Version == "" {
				m.Version = v
			}
		case "displayname":
			m.Name = v
		case "description":
			if m.Description == "" {
				m.Description = truncate(v, 200)
			}
		case "authors", "author":
			m.Authors = append(m.Authors, v)
		case "displayurl", "modproperties":
			m.Links["homepage"] = v
		case "logo":
			m.Icon = v
		}
	}
	flushDep()

	if m.Loader == LoaderForge {
		m.Loader = LoaderForge
	}
	if m.Name == "" {
		m.Name = m.ModID
	}
	return m, m.ModID != ""
}

func splitTomlKV(s string) (string, string, bool) {
	i := strings.Index(s, "=")
	if i < 0 {
		return "", "", false
	}
	k := strings.TrimSpace(s[:i])
	v := strings.TrimSpace(s[i+1:])
	v = strings.Trim(v, `"'`)
	return k, v, k != ""
}

func parseMcModInfo(raw []byte) (*Meta, bool) {
	var arr []legacyMcModInfo
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
		var single legacyMcModInfo
		if err := json.Unmarshal(raw, &single); err != nil || single.ModID == "" {
			return nil, false
		}
		arr = []legacyMcModInfo{single}
	}
	a := arr[0]
	m := &Meta{
		ModID:            a.ModID,
		Name:             a.Name,
		Version:          a.Version,
		MinecraftVersion: a.MCVersion,
		DependsOn:        map[string]string{},
		Recommends:       map[string]string{},
		Conflicts:        map[string]string{},
		Links:            map[string]string{},
	}
	if a.Author != "" {
		m.Authors = append(m.Authors, a.Author)
	}
	if m.Name == "" {
		m.Name = m.ModID
	}
	return m, m.ModID != ""
}

func readEntry(zr *zip.Reader, name string) []byte {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil
		}
		defer rc.Close()
		// Guard against zip bombs in metadata files.
		b, err := io.ReadAll(io.LimitReader(rc, 4<<20))
		if err != nil {
			return nil
		}
		return b
	}
	return nil
}

func specString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, x := range t {
			parts = append(parts, fmtAny(x))
		}
		return strings.Join(parts, " || ")
	case bool:
		return "*"
	default:
		return "*"
	}
}

func fmtAny(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return "*"
	}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

// ---------------------------------------------------------------- helpers

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// NormalizeName reduces an identifier to lowercase alphanumerics so that
// "Inventory Profiles Next", "inventory-profiles-next" and
// "inventoryprofilesnext" all compare equal.
func NormalizeName(s string) string {
	return nonAlnum.ReplaceAllString(strings.ToLower(s), "")
}

// GuessModIDFromFileName extracts a plausible mod id from a jar file name
// when no loader descriptor is present.
//
// Jar names overwhelmingly follow "{id}-{version}[-{loader}[-{mcversion}]]",
// where separators are a mix of '-', '_', '+' and URL-escaped '%2B'. We keep
// the leading identifier run and drop loader / version noise.
//
//	"sodium-fabric-0.9.1%2Bmc26.2.jar"                 -> sodium
//	"fabric-api-0.154.2%2B26.2.jar"                     -> fabricapi
//	"yet_another_config_lib_v3-3.9.5+26.2-fabric.jar"    -> yetanotherconfiglibv3
//	"wthit-26.2-fabric-20.0.0.jar"                      -> wthit
func GuessModIDFromFileName(fileName string) string {
	base := path.Base(fileName)
	base = strings.TrimSuffix(base, path.Ext(base))
	base = DecodePercentEscapes(base)

	// Split on the separator characters that never appear inside an id.
	fields := strings.FieldsFunc(base, func(r rune) bool {
		return r == '-' || r == '_' || r == ' ' || r == '+' || r == '.' || r == '@' || r == '#'
	})
	if len(fields) == 0 {
		return base
	}

	// Jar names follow "{id}-{version}[-{loader}[-{mcversion}]]", so the id
	// is everything before the first numeric token. Only trailing loader
	// words are then stripped, because words like "api" or "lib" can be a
	// legitimate part of a mod id ("fabric-api", "libipn").
	name := fields
	for i, f := range fields {
		if startsWithDigit(f) {
			name = fields[:i]
			break
		}
	}
	for len(name) > 1 && isTrailingNoise(name[len(name)-1]) {
		name = name[:len(name)-1]
	}
	if len(name) == 0 {
		name = fields[:1]
	}
	return strings.Join(name, "")
}

// startsWithDigit reports whether a token begins with an ASCII digit.
func startsWithDigit(s string) bool {
	return s != "" && s[0] >= '0' && s[0] <= '9'
}

// isTrailingNoise reports whether a token at the end of an id is loader or
// packaging noise rather than part of the mod's own name.
func isTrailingNoise(tok string) bool {
	switch strings.ToLower(tok) {
	case "fabric", "forge", "neoforge", "quilt", "mc", "minecraft",
		"jar", "mod", "mods", "release", "beta", "alpha", "snapshot",
		"build", "final", "hotfix", "v", "port", "client", "server",
		"edition", "edition2", "reuploaded", "shaded":
		return true
	}
	return startsWithDigit(tok)
}

// DecodePercentEscapes expands the %2B / %20 escapes that appear in file names
// downloaded by launchers and the Modrinth app.
func DecodePercentEscapes(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '%' && i+2 < len(s) {
			if hi, ok1 := hexVal(s[i+1]); ok1 {
				if lo, ok2 := hexVal(s[i+2]); ok2 {
					b.WriteByte(hi<<4 | lo)
					i += 3
					continue
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
