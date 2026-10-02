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

// Loader names as CurseForge spells them.
const (
	LoaderFabric   = "FABRIC"
	LoaderForge    = "FORGE"
	LoaderNeoForge = "NEOFORGE"
	LoaderQuilt    = "QUILT"
)

// File is a downloadable artifact.
type File struct {
	ID                   int64    `json:"id"`
	GameID               int64    `json:"gameId"`
	FileName             string   `json:"fileName"`
	FileType             int      `json:"fileType"`
	FileDate             string   `json:"fileDate"`
	FileSize             int64    `json:"fileSize"`
	FileFingerprints     []int64  `json:"fileFingerprints"`
	DownloadCount        int64    `json:"downloadCount"`
	DownloadURL          *string  `json:"downloadUrl"`
	IsServerPack         bool     `json:"isServerPack"`
	ServerPackFileID     *int64   `json:"serverPackFileId"`
	SortableGameVersions []string `json:"sortableGameVersions"`
	GameVersions         []int    `json:"gameVersions"`
}

// ModFile is a published file version of a mod.
type ModFile struct {
	ID                   int64         `json:"id"`
	GameID               int64         `json:"gameId"`
	ModID                int64         `json:"modId"`
	DisplayName          string        `json:"displayName"`
	FileName             string        `json:"fileName"`
	FileDate             string        `json:"fileDate"`
	FileLength           int64         `json:"fileLength"`
	DownloadURL          *string       `json:"downloadUrl"`
	GameVersions         []string      `json:"gameVersions"`
	SortableGameVersions []string      `json:"sortableGameVersions"`
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

func (e *Error) Error() string {
	return fmt.Sprintf("curseforge: %d %s", e.StatusCode, e.Message)
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
}

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
	if out == nil {
		return nil
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
		hydrateFingerprints(&m.LatestFiles[i])
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
		hydrateFingerprints(&out[i])
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
	hydrateFingerprints(&f)
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
	q := url.Values{}
	q.Set("gameId", "432")

	var versions []struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Slug     string `json:"slug"`
		GameID   int64  `json:"gameId"`
		Sortable int    `json:"sortableGameVersion"`
	}
	if err := c.do(ctx, "/games/432/versions", q, &versions); err != nil {
		return 0, err
	}
	want := strings.TrimSpace(mcVersion)
	for _, v := range versions {
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
	q.Set("gameId", "432")

	var cats []Category
	if err := c.do(ctx, "/games/432/categories", q, &cats); err != nil {
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

var knownLoaderIDs = map[string]int64{
	LoaderFabric:   4,
	LoaderForge:    1,
	LoaderNeoForge: 6,
	LoaderQuilt:    5,
}

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
	// SortableGameVersions is the more reliable MC version list.
	if len(f.GameVersions) == 0 && len(f.SortableGameVersions) > 0 {
		f.GameVersions = f.SortableGameVersions
	}
}

// Supports reports whether the file targets a Minecraft version and loader.
func (f ModFile) Supports(mcVersion, loader string) bool {
	if mcVersion != "" {
		ok := false
		for _, v := range f.GameVersions {
			if strings.EqualFold(v, mcVersion) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if loader != "" {
		// Loader targeting lives on the project, not the file, so this is
		// only a hint for the caller.
		_ = loader
	}
	return true
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
