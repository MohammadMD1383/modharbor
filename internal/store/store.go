// Package store persists modharbor's observation state across runs.
//
// Two things are cached:
//
//  1. Jar identity. Hashing a jar is cheap but resolving it to a Modrinth
//     project is not, so results are keyed by SHA-1.
//  2. Version lookups. Cached briefly so that repeated commands in one
//     session (or a watch loop) do not re-query the API.
//
// The store is a single JSON document. It is written atomically.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MohammadMD1383/modharbor/internal/hashutil"
)

// SchemaVersion guards against reading incompatible future documents.
const SchemaVersion = 1

// Resolution records how a jar was identified.
type Resolution struct {
	SHA1 string `json:"sha1"`

	// Provider that supplied the identity ("modrinth", "curseforge", ...).
	Provider string `json:"provider,omitempty"`

	// ProjectID is the provider-native project identifier.
	ProjectID string `json:"projectId,omitempty"`
	// Slug is the human-readable project handle, when available.
	Slug string `json:"slug,omitempty"`
	// Title is the project's display name.
	Title string `json:"title,omitempty"`

	// VersionID identifies the exact installed version.
	VersionID string `json:"versionId,omitempty"`
	// VersionNumber is the version's display string.
	VersionNumber string `json:"versionNumber,omitempty"`

	// Filename is the provider's canonical file name for this version, which
	// may differ from the on-disk name when the jar came from a mirror.
	Filename string `json:"filename,omitempty"`
	// ModIDs lists loader-specific mod ids found inside the jar.
	ModIDs []string `json:"modIds,omitempty"`

	// Method explains which strategy produced this result:
	// "hash", "slug", "name", "curseforge-id" or "manual".
	Method string `json:"method,omitempty"`
	// Confidence is a 0..1 score used to sort ambiguous matches.
	Confidence float64 `json:"confidence,omitempty"`

	// SourceFile is the on-disk jar name this resolution came from.
	SourceFile string `json:"sourceFile,omitempty"`
	// InstanceID records which instance was being processed.
	InstanceID string `json:"instanceId,omitempty"`

	// ResolvedAt timestamps the lookup.
	ResolvedAt time.Time `json:"resolvedAt,omitempty"`
}

// CacheEntry is a time-limited API response cache entry.
type CacheEntry struct {
	Key       string          `json:"key"`
	ExpiresAt time.Time       `json:"expiresAt"`
	Value     json.RawMessage `json:"value"`
}

// Document is the on-disk state file.
type Document struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Resolutions   map[string]Resolution `json:"resolutions"`
	Cache         map[string]CacheEntry `json:"cache,omitempty"`
	// Instances maps an instance path to its last-seen identity.
	Instances map[string]InstanceRecord `json:"instances,omitempty"`
	UpdatedAt time.Time                 `json:"updatedAt"`
}

// InstanceRecord remembers per-instance observations.
type InstanceRecord struct {
	InstanceID string    `json:"instanceId"`
	Path       string    `json:"path"`
	MCVersion  string    `json:"mcVersion,omitempty"`
	Loader     string    `json:"loader,omitempty"`
	ModCount   int       `json:"modCount"`
	LastScan   time.Time `json:"lastScan"`
	LastUpdate time.Time `json:"lastUpdate,omitempty"`
}

// Store is a concurrency-safe, atomically-persisted state document.
type Store struct {
	mu   sync.RWMutex
	doc  Document
	path string
	// dirty tracks whether a write is needed.
	dirty bool
}

// Open loads the state file, creating an empty document when absent.
func Open(path string) (*Store, error) {
	s := &Store{
		doc: Document{
			SchemaVersion: SchemaVersion,
			Resolutions:   map[string]Resolution{},
			Cache:         map[string]CacheEntry{},
			Instances:     map[string]InstanceRecord{},
		},
		path: path,
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return s, nil
	}
	var doc Document
	if err := json.Unmarshal(b, &doc); err != nil {
		// A corrupt cache must never block the tool: start fresh but keep
		// the bad file around for debugging.
		_ = os.Rename(path, path+".corrupt")
		return s, nil
	}
	if doc.Resolutions == nil {
		doc.Resolutions = map[string]Resolution{}
	}
	if doc.Cache == nil {
		doc.Cache = map[string]CacheEntry{}
	}
	if doc.Instances == nil {
		doc.Instances = map[string]InstanceRecord{}
	}
	s.doc = doc
	return s, nil
}

// Path returns the file backing the store.
func (s *Store) Path() string { return s.path }

// Lookup returns a cached resolution for a SHA-1 digest.
func (s *Store) Lookup(sha1 string) (Resolution, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.doc.Resolutions[hashutil.Normalize(sha1)]
	return r, ok
}

// Put stores a resolution, refreshing its timestamp.
func (s *Store) Put(r Resolution) {
	if r.SHA1 == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r.SHA1 = hashutil.Normalize(r.SHA1)
	r.ResolvedAt = time.Now()
	s.doc.Resolutions[r.SHA1] = r
	s.dirty = true
}

// PutManual records a user-confirmed identification, which is authoritative
// and will never be overwritten by automatic resolution.
func (s *Store) PutManual(r Resolution) {
	r.Method = "manual"
	s.Put(r)
}

// AllResolutions returns every cached resolution.
func (s *Store) AllResolutions() []Resolution {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Resolution, 0, len(s.doc.Resolutions))
	for _, r := range s.doc.Resolutions {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SHA1 < out[j].SHA1 })
	return out
}

// Forget removes a resolution by digest.
func (s *Store) Forget(sha1 string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.doc.Resolutions, hashutil.Normalize(sha1))
	s.dirty = true
}

// ─── generic response cache ─────────────────────────────────────────────────

// GetJSON reads a cached JSON value.
func (s *Store) GetJSON(key string, out any) bool {
	s.mu.RLock()
	e, ok := s.doc.Cache[key]
	s.mu.RUnlock()
	if !ok || time.Now().After(e.ExpiresAt) {
		return false
	}
	return json.Unmarshal(e.Value, out) == nil
}

// PutJSON stores a JSON value with a time to live.
func (s *Store) PutJSON(key string, v any, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doc.Cache[key] = CacheEntry{Key: key, ExpiresAt: time.Now().Add(ttl), Value: b}
	s.dirty = true
}

// PruneExpired drops stale cache entries and returns how many were removed.
func (s *Store) PruneExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	n := 0
	for k, e := range s.doc.Cache {
		if now.After(e.ExpiresAt) {
			delete(s.doc.Cache, k)
			n++
		}
	}
	if n > 0 {
		s.dirty = true
	}
	return n
}

// ─── instance records ───────────────────────────────────────────────────────

// PutInstance records an observation about an instance.
func (s *Store) PutInstance(rec InstanceRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.Path == "" {
		return
	}
	existing := s.doc.Instances[rec.Path]
	// Preserve timestamps that this call does not set.
	if rec.LastScan.IsZero() {
		rec.LastScan = existing.LastScan
	}
	if rec.LastUpdate.IsZero() {
		rec.LastUpdate = existing.LastUpdate
	}
	rec.InstanceID = orExisting(rec.InstanceID, existing.InstanceID)
	rec.MCVersion = orExisting(rec.MCVersion, existing.MCVersion)
	rec.Loader = orExisting(rec.Loader, existing.Loader)
	s.doc.Instances[rec.Path] = rec
	s.dirty = true
}

// Instance looks up a recorded instance by path.
func (s *Store) Instance(path string) (InstanceRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.doc.Instances[path]
	return r, ok
}

// Instances lists all recorded instances.
func (s *Store) Instances() []InstanceRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]InstanceRecord, 0, len(s.doc.Instances))
	for _, r := range s.doc.Instances {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func orExisting(v, existing string) string {
	if v != "" {
		return v
	}
	return existing
}

// ─── persistence ────────────────────────────────────────────────────────────

// Save writes the document atomically (temp file plus rename).
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	s.doc.SchemaVersion = SchemaVersion
	s.doc.UpdatedAt = time.Now()
	b, err := json.MarshalIndent(s.doc, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

// Stats summarises the store for the `cache` command.
type Stats struct {
	Resolutions  int       `json:"resolutions"`
	CacheEntries int       `json:"cacheEntries"`
	Instances    int       `json:"instances"`
	UpdatedAt    time.Time `json:"updatedAt,omitempty"`
	SizeBytes    int64     `json:"sizeBytes"`
}

// Stats computes current store statistics.
func (s *Store) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Stats{
		Resolutions:  len(s.doc.Resolutions),
		CacheEntries: len(s.doc.Cache),
		Instances:    len(s.doc.Instances),
		UpdatedAt:    s.doc.UpdatedAt,
	}
	if fi, err := os.Stat(s.path); err == nil {
		st.SizeBytes = fi.Size()
	}
	return st
}

// ErrNotFound signals a missing entry.
var ErrNotFound = errors.New("not found")

// Key builds a namespaced cache key.
func Key(parts ...string) string { return strings.Join(parts, ":") }
