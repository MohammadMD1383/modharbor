// Package modrinth implements a client for the Modrinth API v2.
//
// Modrinth is the primary provider: it exposes exact hash lookups, which lets
// us identify a jar with certainty rather than guessing from its file name.
//
// Reference: https://docs.modrinth.com/api
package modrinth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MohammadMD1383/modharbor/internal/hashutil"
)

// DefaultBaseURL is the public Modrinth v2 API root.
const DefaultBaseURL = "https://api.modrinth.com/v2"

// UserAgent identifies modharbor to the API. Modrinth requires a
// descriptive User-Agent and blocks generic ones.
const UserAgent = "modharbor/1.0 (https://github.com/MohammadMD1383/modharbor)"

// Channel mirrors Modrinth's version_type values.
type Channel string

// Release channels, ordered from most to least stable.
const (
	ChannelRelease Channel = "release"
	ChannelBeta    Channel = "beta"
	ChannelAlpha   Channel = "alpha"
)

// File is a downloadable artifact belonging to a version.
type File struct {
	Hashes   map[string]string `json:"hashes"`
	URL      string            `json:"url"`
	Filename string            `json:"filename"`
	Primary  bool              `json:"primary"`
	Size     int64             `json:"size"`
	FileType string            `json:"file_type"`
}

// SHA1 returns the file's SHA-1 digest.
func (f File) SHA1() string { return f.Hashes["sha1"] }

// SHA512 returns the file's SHA-512 digest.
func (f File) SHA512() string { return f.Hashes["sha512"] }

// Dependency links a version to another project or version.
type Dependency struct {
	VersionID      string `json:"version_id"`
	ProjectID      string `json:"project_id"`
	FileName       string `json:"file_name"`
	DependencyType string `json:"dependency_type"`
}

// Version is one published release of a project.
type Version struct {
	ID            string       `json:"id"`
	ProjectID     string       `json:"project_id"`
	Name          string       `json:"name"`
	VersionNumber string       `json:"version_number"`
	Changelog     string       `json:"changelog"`
	GameVersions  []string     `json:"game_versions"`
	VersionType   string       `json:"version_type"`
	Loaders       []string     `json:"loaders"`
	Features      []string     `json:"features"`
	Dependencies  []Dependency `json:"dependencies"`
	DatePublished time.Time    `json:"date_published"`
	Files         []File       `json:"files"`
}

// PrimaryFile returns the primary file, falling back to the first jar.
func (v Version) PrimaryFile() (File, bool) {
	for _, f := range v.Files {
		if f.Primary && strings.HasSuffix(f.Filename, ".jar") {
			return f, true
		}
	}
	for _, f := range v.Files {
		if strings.HasSuffix(f.Filename, ".jar") {
			return f, true
		}
	}
	return File{}, false
}

// Supports reports whether the version targets the given Minecraft version
// and loader.
func (v Version) Supports(mcVersion, loader string) bool {
	if loader != "" {
		ok := false
		for _, l := range v.Loaders {
			if strings.EqualFold(l, loader) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if mcVersion != "" {
		return containsFold(v.GameVersions, mcVersion)
	}
	return true
}

// Project is a mod as published on Modrinth.
type Project struct {
	ID           string   `json:"id"`
	Slug         string   `json:"slug"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Categories   []string `json:"categories"`
	ClientSide   string   `json:"client_side"`
	ServerSide   string   `json:"server_side"`
	GameVersions []string `json:"game_versions"`
	Loaders      []string `json:"loaders"`
	License      struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"license"`
	Published     time.Time `json:"published"`
	Updated       time.Time `json:"updated"`
	DownloadCount int64     `json:"downloads"`
	Followers     int64     `json:"followers"`
	IconURL       string    `json:"icon_url"`
	URL           string    `json:"url"`
}

// PrimaryColor returns the project's main category for display.
func (p Project) PrimaryCategory() string {
	if len(p.Categories) == 0 {
		return ""
	}
	return p.Categories[0]
}

// Hit is one search result.
type Hit struct {
	ProjectID    string    `json:"project_id"`
	Slug         string    `json:"slug"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Categories   []string  `json:"categories"`
	Versions     []string  `json:"versions"`
	Downloads    int64     `json:"downloads"`
	Follows      int64     `json:"follows"`
	DateModified time.Time `json:"date_modified"`
	IconURL      string    `json:"icon_url"`
}

// SearchResponse is the payload of /search.
type SearchResponse struct {
	Hits      []Hit `json:"hits"`
	Offset    int   `json:"offset"`
	Limit     int   `json:"limit"`
	TotalHits int   `json:"total_hits"`
}

// APIError is a Modrinth API error response.
type APIError struct {
	StatusCode  int
	Code        string
	Description string
	URL         string
}

func (e *APIError) Error() string {
	msg := e.Description
	if msg == "" {
		msg = e.Code
	}
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("modrinth: %d %s", e.StatusCode, msg)
}

// ErrNotFound is returned when a project or version does not exist.
var ErrNotFound = errors.New("not found on modrinth")

// Client talks to the Modrinth API with rate limiting and retries.
type Client struct {
	baseURL   string
	http      *http.Client
	userAgent string
	mu        sync.Mutex
	lastCall  time.Time
	minGap    time.Duration
	cache     map[string]cacheEntry
	cacheMu   sync.RWMutex
	cacheTTL  time.Duration
}

type cacheEntry struct {
	value   any
	expires time.Time
}

// Options configures a Client.
type Options struct {
	BaseURL   string
	HTTP      *http.Client
	UserAgent string
	// CacheTTL enables an in-memory response cache when non-zero.
	CacheTTL time.Duration
}

// New builds a Modrinth client.
func New(opts Options) *Client {
	base := opts.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	hc := opts.HTTP
	if hc == nil {
		hc = &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        32,
				MaxIdleConnsPerHost: 8,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = UserAgent
	}
	return &Client{
		baseURL:   strings.TrimRight(base, "/"),
		http:      hc,
		userAgent: ua,
		minGap:    0, // Modrinth allows generous rates for well-behaved clients
		cache:     map[string]cacheEntry{},
		cacheTTL:  opts.CacheTTL,
	}
}

// throttle paces requests to stay within the API's rate limit.
func (c *Client) throttle() {
	if c.minGap <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := c.minGap - time.Since(c.lastCall); d > 0 {
		time.Sleep(d)
	}
	c.lastCall = time.Now()
}

func (c *Client) cacheGet(key string) (any, bool) {
	if c.cacheTTL <= 0 {
		return nil, false
	}
	c.cacheMu.RLock()
	e, ok := c.cache[key]
	c.cacheMu.RUnlock()
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.value, true
}

func (c *Client) cacheSet(key string, v any) {
	if c.cacheTTL <= 0 {
		return
	}
	c.cacheMu.Lock()
	c.cache[key] = cacheEntry{value: v, expires: time.Now().Add(c.cacheTTL)}
	c.cacheMu.Unlock()
}

// do performs a request with retries and honours 429 Retry-After.
func (c *Client) do(ctx context.Context, method, path string, body any, query url.Values, out any) error {
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = b
	}

	const maxAttempts = 4
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * 500 * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, reader)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		c.throttle()
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		switch {
		case resp.StatusCode == http.StatusNotFound:
			resp.Body.Close()
			return fmt.Errorf("%w: %s", ErrNotFound, path)
		case resp.StatusCode == http.StatusTooManyRequests:
			wait := retryAfter(resp)
			resp.Body.Close()
			lastErr = &APIError{StatusCode: resp.StatusCode, Description: "rate limited"}
			if wait > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(wait):
				}
			}
			continue
		case resp.StatusCode >= 500:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			resp.Body.Close()
			lastErr = &APIError{StatusCode: resp.StatusCode, Description: strings.TrimSpace(string(body)), URL: target}
			continue
		case resp.StatusCode >= 400:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			var apiErr APIError
			if json.Unmarshal(body, &apiErr) == nil {
				apiErr.StatusCode = resp.StatusCode
				apiErr.URL = target
				return &apiErr
			}
			return &APIError{StatusCode: resp.StatusCode, Description: strings.TrimSpace(string(body)), URL: target}
		}

		defer resp.Body.Close()
		if out == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			return nil
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, out)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("modrinth: request failed after %d attempts", maxAttempts)
	}
	return lastErr
}

func retryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// ─── API methods ────────────────────────────────────────────────────────────

// VersionByHash performs an exact lookup of a file digest. This is the
// highest-confidence identification method available.
func (c *Client) VersionByHash(ctx context.Context, sha1 string) (*Version, error) {
	sha1 = hashutil.Normalize(sha1)
	if sha1 == "" {
		return nil, fmt.Errorf("empty hash")
	}
	key := "vh:" + sha1
	if v, ok := c.cacheGet(key); ok {
		if ver, ok := v.(*Version); ok {
			return ver, nil
		}
	}
	var v Version
	if err := c.do(ctx, http.MethodGet, "/version_file/"+sha1, nil, nil, &v); err != nil {
		return nil, err
	}
	c.cacheSet(key, &v)
	return &v, nil
}

// VersionByID fetches one version by its Modrinth id.
func (c *Client) VersionByID(ctx context.Context, id string) (*Version, error) {
	key := "vid:" + id
	if v, ok := c.cacheGet(key); ok {
		if ver, ok := v.(*Version); ok {
			return ver, nil
		}
	}
	var v Version
	if err := c.do(ctx, http.MethodGet, "/version/"+id, nil, nil, &v); err != nil {
		return nil, err
	}
	c.cacheSet(key, &v)
	return &v, nil
}

// VersionsBatch fetches up to 100 versions by id in one request.
func (c *Client) VersionsBatch(ctx context.Context, ids []string) ([]Version, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var out []Version
	for start := 0; start < len(ids); start += 100 {
		end := start + 100
		if end > len(ids) {
			end = len(ids)
		}
		var chunk []Version
		body := map[string][]string{"ids": ids[start:end]}
		if err := c.do(ctx, http.MethodPost, "/versions", body, nil, &chunk); err != nil {
			// Fall back to individual requests on batch failure.
			for _, id := range ids[start:end] {
				v, err := c.VersionByID(ctx, id)
				if err != nil {
					continue
				}
				out = append(out, *v)
			}
			continue
		}
		out = append(out, chunk...)
	}
	return out, nil
}

// Project fetches project metadata by id or slug.
func (c *Client) Project(ctx context.Context, idOrSlug string) (*Project, error) {
	key := "proj:" + idOrSlug
	if v, ok := c.cacheGet(key); ok {
		if p, ok := v.(*Project); ok {
			return p, nil
		}
	}
	var p Project
	if err := c.do(ctx, http.MethodGet, "/project/"+url.PathEscape(idOrSlug), nil, nil, &p); err != nil {
		return nil, err
	}
	c.cacheSet(key, &p)
	return &p, nil
}

// VersionListOptions filters a version listing.
type VersionListOptions struct {
	GameVersions []string
	Loaders      []string
	Channel      Channel
	// Feature filters, e.g. "client" or "server".
	Features []string
}

// Versions lists every version of a project, newest first.
func (c *Client) Versions(ctx context.Context, projectID string, opts VersionListOptions) ([]Version, error) {
	q := url.Values{}
	if len(opts.GameVersions) > 0 {
		b, _ := json.Marshal(opts.GameVersions)
		q.Set("game_versions", string(b))
	}
	if len(opts.Loaders) > 0 {
		b, _ := json.Marshal(opts.Loaders)
		q.Set("loaders", string(b))
	}
	// version_type is deliberately NOT sent upstream. Modrinth accepts only a
	// single value there, and filtering server-side would hide pre-release
	// builds that do support the target game version. Callers filter and rank
	// locally with FilterByChannel, which also lets us tell the user "a beta
	// exists" when the release channel came up empty.

	var out []Version
	if err := c.do(ctx, http.MethodGet, "/project/"+url.PathEscape(projectID)+"/version", nil, q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AllowsChannel reports whether a version type is permitted by the channel.
func AllowsChannel(versionType string, ch Channel) bool {
	for _, allowed := range allowedChannels(ch) {
		if strings.EqualFold(versionType, allowed) {
			return true
		}
	}
	return false
}

// TypeRank orders version types by preference within a channel. Lower is
// better: releases beat betas beat alphas when the channel allows them.
func TypeRank(versionType string, ch Channel) int {
	vs := allowedChannels(ch)
	for i, allowed := range vs {
		if strings.EqualFold(versionType, allowed) {
			return i
		}
	}
	return len(vs)
}

// FilterByChannel drops versions outside the channel and orders the rest
// newest first with the channel's preference respected.
//
// This is required because Modrinth's version_type query parameter only
// accepts a single value, so a beta channel cannot be expressed server-side.
func FilterByChannel(versions []Version, ch Channel) []Version {
	out := make([]Version, 0, len(versions))
	for _, v := range versions {
		if AllowsChannel(v.VersionType, ch) {
			out = append(out, v)
		}
	}
	sortVersions(out, ch)
	return out
}

// sortVersions orders versions newest first, breaking ties by channel
// preference so a release wins over a beta published the same day.
func sortVersions(v []Version, ch Channel) {
	sort.SliceStable(v, func(i, j int) bool {
		if !v[i].DatePublished.Equal(v[j].DatePublished) {
			return v[i].DatePublished.After(v[j].DatePublished)
		}
		ri, rj := TypeRank(v[i].VersionType, ch), TypeRank(v[j].VersionType, ch)
		if ri != rj {
			return ri < rj
		}
		return v[i].ID < v[j].ID
	})
}

// allowedChannels expands a channel into the set of version types to accept.
func allowedChannels(ch Channel) []string {
	switch ch {
	case ChannelAlpha:
		return []string{"release", "beta", "alpha"}
	case ChannelBeta:
		return []string{"release", "beta"}
	default:
		return []string{"release"}
	}
}

// Search queries the project index.
func (c *Client) Search(ctx context.Context, query string, opts SearchOptions) (*SearchResponse, error) {
	q := url.Values{}
	if query != "" {
		q.Set("query", query)
	}
	q.Set("limit", strconv.Itoa(opts.Limit))
	if opts.Offset > 0 {
		q.Set("offset", strconv.Itoa(opts.Offset))
	}
	facets := buildFacets(opts)
	if len(facets) > 0 {
		b, _ := json.Marshal(facets)
		q.Set("facets", string(b))
	}
	if len(opts.Sorts) > 0 {
		b, _ := json.Marshal(opts.Sorts)
		q.Set("index", string(b))
	}

	var out SearchResponse
	if err := c.do(ctx, http.MethodGet, "/search", nil, q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SearchOptions tunes a project search.
type SearchOptions struct {
	Limit       int
	Offset      int
	GameVersion string
	Loader      string
	Categories  []string
	Sorts       []string
	// ProjectIDs restricts results to a known set.
	ProjectIDs []string
}

func buildFacets(opts SearchOptions) []string {
	var facets []string
	add := func(f string) { facets = append(facets, "[["+f+"]]") }
	if opts.GameVersion != "" {
		add(fmt.Sprintf(`{"versions":%q}`, opts.GameVersion))
	}
	if opts.Loader != "" {
		add(fmt.Sprintf(`{"categories":%q}`, opts.Loader))
	}
	for _, c := range opts.Categories {
		add(fmt.Sprintf(`{"categories":%q}`, c))
	}
	if len(opts.ProjectIDs) > 0 {
		ids, _ := json.Marshal(opts.ProjectIDs)
		facets = append(facets, `{"project_ids":`+string(ids)+`}`)
	}
	return facets
}

// CDNURL asks Modrinth for the CDN-hosted URL of a file, which is faster and
// more reliable than the mirror URL stored in the version payload.
func (c *Client) CDNURL(ctx context.Context, f File) (string, error) {
	if f.URL == "" {
		return "", fmt.Errorf("file has no download URL")
	}
	u, err := url.Parse(f.URL)
	if err != nil {
		return f.URL, nil
	}
	// Already a direct CDN link: nothing to resolve.
	if strings.Contains(u.Host, "cdn.modrinth.com") {
		return f.URL, nil
	}
	// Mirrored files are served from "<api-host>/data/<hash>" and the bare
	// hash is what identifies them for CDN resolution.
	pathPart := strings.TrimPrefix(u.Path, "/v2/")
	pathPart = strings.TrimPrefix(pathPart, "/")
	if !strings.HasPrefix(pathPart, "data/") {
		// Not a recognised mirror path (a third-party mirror, say).
		return f.URL, nil
	}

	// pathPart is now "data/<hash>"; the CDN lookup is that same path
	// without the version prefix.
	var resolved struct {
		URL string `json:"url"`
	}
	if err := c.do(ctx, http.MethodGet, "/"+pathPart, nil, nil, &resolved); err != nil || resolved.URL == "" {
		return f.URL, nil
	}
	return resolved.URL, nil
}

// LoaderVersions lists the Modrinth versions of a loader itself
// (fabric-api is treated as an ordinary project, but fabric-loader and
// quilt-loader are queried the same way for doctor checks).
func (c *Client) LoaderVersions(ctx context.Context, projectID, mcVersion string) ([]Version, error) {
	opts := VersionListOptions{Loaders: []string{"fabric"}}
	if mcVersion != "" {
		opts.GameVersions = []string{mcVersion}
	}
	return c.Versions(ctx, projectID, opts)
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}
