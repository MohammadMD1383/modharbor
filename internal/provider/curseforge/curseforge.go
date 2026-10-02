// Package curseforge implements an optional CurseForge API v1 client.
//
// CurseForge requires an API key for every request, so modharbor treats it as
// an optional provider. When no key is configured the client refuses to make
// requests rather than failing with confusing 403s.
//
// CurseForge remains useful even with a key: some mods are published only
// there, and the launcher metadata that ships with an instance records the
// CurseForge project id for every installed mod.
package curseforge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the CurseForge v1 API root.
const DefaultBaseURL = "https://api.curseforge.com/v1"

// UserAgent identifies modharbor to CurseForge.
const UserAgent = "modharbor/1.0 (https://github.com/MohammadMD1383/modharbor)"

// ErrNoAPIKey is returned when an operation needs a key that is not set.
var ErrNoAPIKey = errors.New("curseforge: no API key configured (run `modharbor config set curseforge.apiKey <key>`)")

// ErrNullResponse reports a successful request whose body is the JSON literal
// null.
//
// It is an error rather than an empty result because the two mean different
// things to a caller: an empty listing says this mod publishes no file for the
// requested version, while null says CurseForge answered with nothing at all.
// Decoding null produces a zero project with no error, and the caller then
// reports "this mod publishes nothing" for a mod that is perfectly healthy.
var ErrNullResponse = errors.New("curseforge: the API answered null instead of the requested resource")

// Loader names as CurseForge spells them.
const (
	LoaderFabric   = "FABRIC"
	LoaderForge    = "FORGE"
	LoaderNeoForge = "NEOFORGE"
	LoaderQuilt    = "QUILT"
)

// File is a downloadable artifact.
type File struct {
	ID                   int64       `json:"id"`
	GameID               int64       `json:"gameId"`
	FileName             string      `json:"fileName"`
	FileType             int         `json:"fileType"`
	FileDate             string      `json:"fileDate"`
	FileSize             int64       `json:"fileSize"`
	FileFingerprints     []int64     `json:"fileFingerprints"`
	DownloadCount        int64       `json:"downloadCount"`
	DownloadURL          *string     `json:"downloadUrl"`
	IsServerPack         bool        `json:"isServerPack"`
	ServerPackFileID     *int64      `json:"serverPackFileId"`
	SortableGameVersions []VersionID `json:"sortableGameVersions"`
	// GameVersions is this endpoint's list of ids. Unlike ModFile.GameVersions
	// it is numeric by nature: the game file endpoint never publishes names.
	GameVersions []int `json:"gameVersions"`
}

// ModFile is a published file version of a mod.
type ModFile struct {
	ID          int64   `json:"id"`
	GameID      int64   `json:"gameId"`
	ModID       int64   `json:"modId"`
	DisplayName string  `json:"displayName"`
	FileName    string  `json:"fileName"`
	FileDate    string  `json:"fileDate"`
	FileLength  int64   `json:"fileLength"`
	DownloadURL *string `json:"downloadUrl"`
	// GameVersions is the readable list CurseForge publishes for a mod file:
	// Minecraft version names ("26.3") with the loader ("Fabric") mixed in.
	// When the payload carries none, it is filled from the ids below, because
	// this is the field a caller compares against a version string.
	GameVersions []string `json:"gameVersions"`
	// SortableGameVersions is the same list as ids, game versions mixed with
	// loader categories. It is kept because it is the only list a loader-agnostic
	// file ever publishes, and because an id is the stable identity of a version
	// across the renames CurseForge applies to snapshots.
	SortableGameVersions []VersionID   `json:"sortableGameVersions"`
	Dependencies         []Dependency  `json:"dependencies"`
	ReleaseType          int           `json:"releaseType"`
	Fingerprints         []Fingerprint `json:"fileFingerprints"`
	// SHA1 mirrors the Modrinth-shaped accessor so callers can treat both
	// providers uniformly.
	SHA1   string `json:"-"`
	SHA512 string `json:"-"`
}

// Fingerprint is one digest of a file.
type Fingerprint struct {
	Algorithm int    `json:"algorithm"`
	Value     string `json:"value"`
}

// Fingerprint algorithm ids.
const (
	FingerprintSHA1   = 1
	FingerprintMD5    = 2
	FingerprintSHA512 = 4
)

// Dependency links a file to another mod.
type Dependency struct {
	ModID        int64  `json:"modId"`
	RelationType int    `json:"relationType"`
	FileID       *int64 `json:"fileId"`
}

// Dependency relation types.
const (
	RelationEmbeddedLibrary = 1
	RelationOptional        = 2
	RelationRequired        = 3
	RelationTool            = 4
	RelationIncompatible    = 5
	RelationInclude         = 6
)

// Release types.
const (
	ReleaseTypeRelease = 1
	ReleaseTypeBeta    = 2
	ReleaseTypeAlpha   = 3
)

// Mod is a project.
type Mod struct {
	ID            int        `json:"id"`
	GameID        int        `json:"gameId"`
	Name          string     `json:"name"`
	Slug          string     `json:"slug"`
	Summary       string     `json:"summary"`
	Status        int        `json:"status"`
	DownloadCount int64      `json:"downloadCount"`
	IsFeatured    bool       `json:"isFeatured"`
	Categories    []Category `json:"categories"`
	GameVersions  []string   `json:"gameVersions"`
	LatestFiles   []ModFile  `json:"latestFiles"`
}

// Category is a project category.
type Category struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// Error is a CurseForge API error.
type Error struct {
	StatusCode int
	Message    string
	URL        string
}

// Error names the request that failed, not just the status.
//
// A user migrating a pack has no way to attach a bare "503" to a file: the
// status alone does not say which of the hundred requests modharbor made
// failed, and the project id that would identify it lives in the path.
func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		// A 5xx with an empty body is the common shape: there is nothing from
		// the server to quote, so the status text is all that can be said.
		msg = strings.TrimSpace(http.StatusText(e.StatusCode))
	}
	return fmt.Sprintf("curseforge: %d %s [%s]", e.StatusCode, msg, e.URL)
}

// ErrNotFound signals a missing project or file.
var ErrNotFound = errors.New("not found on curseforge")

// Client talks to the CurseForge API.
type Client struct {
	baseURL   string
	apiKey    string
	http      *http.Client
	userAgent string

	mu       sync.RWMutex
	fileByID map[int64]*ModFile

	// versionsMu guards versions, which is filled by a request rather than by a
	// decode. It is separate from mu so that waiting for the version list does
	// not block every cache hit in the pack behind it.
	versionsMu sync.Mutex
	versions   *versionTable
}

// gameVersion is one row of the game version list.
type gameVersion struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	GameID   int64  `json:"gameId"`
	Sortable int    `json:"sortableGameVersion"`
}

// versionTable indexes CurseForge's game version list both ways: the resolver
// asks for the id behind a version name, and the file decoder asks for the name
// behind an id.
type versionTable struct {
	list  []gameVersion
	names map[VersionID]string
}

// The game endpoints repeat the id 432 that GameIDMinecraft holds, because a
// const expression cannot call strconv and building these per call would be
// noise at every use site.
const (
	gameVersionsPath   = "/games/432/versions"
	gameCategoriesPath = "/games/432/categories"
)

// Options configures a Client.
type Options struct {
	BaseURL   string
	APIKey    string
	HTTP      *http.Client
	UserAgent string
}

// New builds a CurseForge client.
func New(opts Options) *Client {
	base := opts.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	hc := opts.HTTP
	if hc == nil {
		hc = &http.Client{
			Timeout:   60 * time.Second,
			Transport: &http.Transport{MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second},
		}
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = UserAgent
	}
	c := &Client{
		baseURL:   strings.TrimRight(base, "/"),
		apiKey:    opts.APIKey,
		http:      hc,
		userAgent: ua,
		fileByID:  map[int64]*ModFile{},
	}
	return c
}

// HasKey reports whether an API key is configured.
func (c *Client) HasKey() bool { return strings.TrimSpace(c.apiKey) != "" }

func (c *Client) do(ctx context.Context, path string, query url.Values, out any) error {
	if !c.HasKey() {
		return ErrNoAPIKey
	}
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, path)
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return &Error{StatusCode: resp.StatusCode, Message: "invalid or missing API key", URL: target}
	case resp.StatusCode >= 400:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var e Error
		if json.Unmarshal(b, &e) == nil && e.Message != "" {
			e.StatusCode = resp.StatusCode
			e.URL = target
			return &e
		}
		return &Error{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(b)), URL: target}
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		// A 200 whose body is null decodes into a zero value without an
		// error, which is indistinguishable from a project that genuinely has
		// nothing to publish. Naming the endpoint is what tells the two apart.
		return fmt.Errorf("%w: %s", ErrNullResponse, target)
	}
	return json.Unmarshal(b, out)
}

// Mod fetches a project by id.
func (c *Client) Mod(ctx context.Context, id int64) (*Mod, error) {
	var m Mod
	if err := c.do(ctx, "/mods/"+strconv.FormatInt(id, 10), nil, &m); err != nil {
		return nil, err
	}
	for i := range m.LatestFiles {
		if err := c.hydrate(ctx, &m.LatestFiles[i]); err != nil {
			return nil, err
		}
		c.remember(&m.LatestFiles[i])
	}
	return &m, nil
}

// Files lists a project's published files.
func (c *Client) Files(ctx context.Context, modID int64) ([]ModFile, error) {
	q := url.Values{}
	q.Set("modId", strconv.FormatInt(modID, 10))
	q.Set("index", "0")
	q.Set("pageSize", "100")

	var out []ModFile
	if err := c.do(ctx, "/mods/"+strconv.FormatInt(modID, 10)+"/files", q, &out); err != nil {
		return nil, err
	}
	for i := range out {
		if err := c.hydrate(ctx, &out[i]); err != nil {
			return nil, err
		}
		c.remember(&out[i])
	}
	return out, nil
}

// ModFile fetches one file by id, serving from cache when possible.
func (c *Client) ModFile(ctx context.Context, fileID int64) (*ModFile, error) {
	c.mu.RLock()
	if f, ok := c.fileByID[fileID]; ok {
		c.mu.RUnlock()
		return f, nil
	}
	c.mu.RUnlock()

	var f ModFile
	if err := c.do(ctx, "/files/"+strconv.FormatInt(fileID, 10), nil, &f); err != nil {
		return nil, err
	}
	if err := c.hydrate(ctx, &f); err != nil {
		return nil, err
	}
	c.remember(&f)
	return &f, nil
}

func (c *Client) remember(f *ModFile) {
	c.mu.Lock()
	c.fileByID[f.ID] = f
	c.mu.Unlock()
}

// Search finds projects by name.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]Mod, error) {
	q := url.Values{}
	q.Set("searchFilter", query)
	q.Set("index", "0")
	q.Set("pageSize", strconv.Itoa(limit))
	q.Set("classId", "6")  // mods
	q.Set("gameId", "432") // Minecraft

	var out []Mod
	if err := c.do(ctx, "/mods/search", q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GameVersionID resolves a Minecraft version string such as "26.3" to its
// numeric id, which the CurseForge API uses everywhere.
func (c *Client) GameVersionID(ctx context.Context, mcVersion string) (int64, error) {
	versions, err := c.gameVersionTable(ctx)
	if err != nil {
		return 0, err
	}
	want := strings.TrimSpace(mcVersion)
	for _, v := range versions.list {
		if strings.EqualFold(v.Name, want) {
			return v.ID, nil
		}
	}
	return 0, fmt.Errorf("curseforge: no game version id for %q", mcVersion)
}

// LoaderID resolves a loader name to its numeric category id.
func (c *Client) LoaderID(ctx context.Context, loader string) (int64, error) {
	if id, ok := knownLoaderIDs[strings.ToUpper(loader)]; ok {
		return id, nil
	}
	q := url.Values{}
	q.Set("gameId", strconv.Itoa(GameIDMinecraft))

	var cats []Category
	if err := c.do(ctx, gameCategoriesPath, q, &cats); err != nil {
		return 0, err
	}
	want := strings.ToUpper(loader)
	for _, cat := range cats {
		if strings.EqualFold(cat.Name, want) {
			return int64(cat.ID), nil
		}
	}
	return 0, fmt.Errorf("curseforge: unknown loader %q", loader)
}

// gameVersionTable fetches the game version list at most once per client.
//
// Every file in a listing that publishes ids instead of names needs it, so a
// request per file would multiply the request budget of a migration by the size
// of the pack. Concurrent callers wait on the one fetch rather than issuing
// their own, which is the only behaviour this memoisation changes: a client
// used to ask for the version list again on every lookup.
//
// A failure is not remembered. CurseForge answers 500 while it rebuilds an
// index, and pinning that would turn one hiccup into a permanently blind client.
func (c *Client) gameVersionTable(ctx context.Context) (*versionTable, error) {
	c.versionsMu.Lock()
	defer c.versionsMu.Unlock()
	if c.versions != nil {
		return c.versions, nil
	}

	q := url.Values{}
	q.Set("gameId", strconv.Itoa(GameIDMinecraft))

	var list []gameVersion
	if err := c.do(ctx, gameVersionsPath, q, &list); err != nil {
		return nil, err
	}
	names := make(map[VersionID]string, len(list))
	for _, v := range list {
		names[VersionID(strconv.FormatInt(v.ID, 10))] = v.Name
	}
	c.versions = &versionTable{list: list, names: names}
	return c.versions, nil
}

var knownLoaderIDs = map[string]int64{
	LoaderFabric:   4,
	LoaderForge:    1,
	LoaderNeoForge: 6,
	LoaderQuilt:    5,
}

// knownLoaderNames inverts knownLoaderIDs for recognising a loader among a
// project's categories, where only the id arrives.
var knownLoaderNames = func() map[int]string {
	out := make(map[int]string, len(knownLoaderIDs))
	for name, id := range knownLoaderIDs {
		out[int(id)] = name
	}
	return out
}()

// ClassID is the category id for mods.
const ClassID = 6

// GameIDMinecraft is CurseForge's id for Minecraft.
const GameIDMinecraft = 432

// hydrateFingerprints copies the numeric fingerprint list into the
// digest-named fields so both providers expose the same shape.
func hydrateFingerprints(f *ModFile) {
	for _, fp := range f.Fingerprints {
		switch fp.Algorithm {
		case FingerprintSHA1:
			f.SHA1 = strings.ToLower(fp.Value)
		case FingerprintSHA512:
			f.SHA512 = strings.ToLower(fp.Value)
		}
	}
}

// hydrate completes a decoded file. It is a method because resolving the ids
// a file published may need the version list, and that is a request rather than
// a decode.
func (c *Client) hydrate(ctx context.Context, f *ModFile) error {
	hydrateFingerprints(f)
	return c.resolveGameVersions(ctx, f)
}

// resolveGameVersions spells out the ids a file published instead of names.
//
// sortableGameVersions is a list of ids, so copying it into GameVersions makes
// every entry something that can never equal a Minecraft version string:
// "432" is not "26.3", and the file was then rejected for every release while
// the project still appeared to publish it. The ids are resolved through
// CurseForge's own version table instead, and the raw ids stay on the file.
//
// Loader category ids resolve to nothing here, because the version list only
// knows Minecraft versions. That loses nothing: the loader is a property of the
// project and is checked there, by Mod.SupportsLoader.
func (c *Client) resolveGameVersions(ctx context.Context, f *ModFile) error {
	if len(f.GameVersions) > 0 || len(f.SortableGameVersions) == 0 {
		return nil
	}
	table, err := c.gameVersionTable(ctx)
	if err != nil {
		// Reported rather than swallowed: a file whose target versions could
		// not be read is not a file that supports nothing, and failing the
		// listing tells the user something is wrong where silence would tell
		// them the mod has no compatible build.
		return err
	}
	for _, id := range f.SortableGameVersions {
		if name, ok := table.names[id]; ok {
			f.GameVersions = append(f.GameVersions, name)
		}
	}
	return nil
}

// Supports reports whether the file targets a Minecraft version.
//
// There is deliberately no loader parameter. CurseForge files loader targeting
// on the project, and although a file repeats the loader name inside its own
// version list, a library or a datapack names none at all, so a file cannot
// speak for it. Accepting a loader and ignoring it was the worse arrangement:
// a Forge build passed a Fabric check and the veto in the resolver — the one
// mechanism that stops modharbor installing the wrong loader — quietly stopped
// applying. Use Mod.SupportsLoader for the loader half.
func (f ModFile) Supports(mcVersion string) bool {
	if mcVersion == "" {
		return true
	}
	for _, v := range f.GameVersions {
		if strings.EqualFold(v, mcVersion) {
			return true
		}
	}
	return false
}

// SupportsLoader reports whether the project publishes builds for a loader.
//
// A project that names no loader category is reported as unsupported rather
// than as supported, because this package reads absent evidence as rejection
// everywhere else: a file with no version list is not treated as compatible.
// Loaders exposes the categories for the caller that needs to tell "published
// none" apart from "published, and not yours".
func (m Mod) SupportsLoader(loader string) bool {
	loader = strings.ToUpper(strings.TrimSpace(loader))
	if loader == "" {
		return true
	}
	for _, l := range m.Loaders() {
		if l == loader {
			return true
		}
	}
	return false
}

// Loaders returns the loaders a project publishes builds for, spelled the way
// CurseForge does (LoaderFabric and friends).
func (m Mod) Loaders() []string {
	out := make([]string, 0, len(m.Categories))
	seen := map[string]bool{}
	for _, cat := range m.Categories {
		name := loaderCategoryName(cat)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// loaderCategoryName recognises a loader among a project's categories, which
// also hold class ("Library", "Utility") and platform entries.
//
// The slug and the name are checked before the id because a loader id is only
// stable as long as CurseForge keeps its numbering; a loader added after this
// table was written still answers by name.
func loaderCategoryName(cat Category) string {
	for known := range knownLoaderIDs {
		if strings.EqualFold(cat.Slug, known) || strings.EqualFold(cat.Name, known) {
			return known
		}
	}
	if name, ok := knownLoaderNames[cat.ID]; ok {
		return name
	}
	return ""
}

// Newest sorts files newest-first by their file date.
func Newest(files []ModFile) []ModFile {
	out := append([]ModFile(nil), files...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && fileDate(out[j]).After(fileDate(out[j-1])); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func fileDate(f ModFile) time.Time {
	t, err := time.Parse(time.RFC3339, f.FileDate)
	if err != nil {
		return time.Time{}
	}
	return t
}

// DownloadBody performs a GET returning the raw response body, used by the
// downloader when no direct URL is available.
func (c *Client) DownloadBody(ctx context.Context, downloadURL string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(downloadURL, c.baseURL) && c.HasKey() {
		req.Header.Set("x-api-key", c.apiKey)
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, &Error{StatusCode: resp.StatusCode, Message: http.StatusText(resp.StatusCode), URL: downloadURL}
	}
	return resp.Body, nil
}
