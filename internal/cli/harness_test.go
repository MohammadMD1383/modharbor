package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/MohammadMD1383/modharbor/internal/hashutil"
	"github.com/MohammadMD1383/modharbor/internal/ui"
)

// The helpers in this file run real commands against a throwaway Minecraft
// directory and a fake Modrinth. Every path they touch is derived from
// t.TempDir or XDG_*, so a test can never reach the developer's own instances,
// config or state — the CLI offers no injection seam for those, which is why
// the environment has to be redirected instead.

// ─── output capture ──────────────────────────────────────────────────────────

// captureCLI runs the CLI with args and returns everything it printed.
//
// Both streams are captured because the CLI writes through two different
// paths: internal/ui holds its own writer, while printJSON writes to os.Stdout
// directly. Everything is restored afterwards so tests stay independent.
func captureCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origOut := os.Stdout
	os.Stdout = w
	// Colour is off so assertions can match plain text; ANSI escapes would
	// otherwise sit between a word and the space that follows it.
	ui.SetWriters(w, w)
	ui.SetColorEnabled(false)
	t.Cleanup(func() {
		os.Stdout = origOut
		ui.SetWriters(origOut, os.Stderr)
		ui.SetColorEnabled(false)
	})

	root := newRootCmd()
	root.SetArgs(args)
	runErr := root.Execute()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	return string(body), runErr
}

// ─── the fake Modrinth ───────────────────────────────────────────────────────

// One project, "sodium", with two published versions. Holding the 0.10.0 jar in
// a mods directory and offering 0.11.0 upstream is the smallest setup that
// exercises a real upgrade: the engine resolves the jar by hash, picks the
// newer build, and replaces the file.
const (
	sodiumProjectID = "AANobbMI"
	sodiumSlug      = "sodium"
	sodiumTitle     = "Sodium"

	sodiumOldJar  = "sodium 0.10.0\n"
	sodiumNewJar  = "sodium 0.11.0\n"
	sodiumOldFile = "sodium-0.10.0.jar"
	sodiumNewFile = "sodium-0.11.0.jar"
	sodiumOldID   = "verOld0000000000"
	sodiumNewID   = "verNew0000000000"
	sodiumOldNo   = "mc26.3-0.10.0-fabric"
	sodiumNewNo   = "mc26.3-0.11.0-fabric"
	sodiumOldDate = "2026-01-01T00:00:00Z"
	sodiumNewDate = "2026-02-01T00:00:00Z"
	sodiumGameVsn = "26.3"
)

// fakeModrinth serves the endpoints these tests touch and records what was
// asked for, so a test can assert a dry run fetched no jar rather than merely
// that the mods directory looks unchanged afterwards.
type fakeModrinth struct {
	*httptest.Server

	// searchHits replaces the default empty search result when a test needs
	// the search endpoint, as the `search` command itself does.
	searchHits []map[string]any

	mu       sync.Mutex
	requests []string
}

// newFakeModrinth starts the fake API. close is wired to t.Cleanup.
func newFakeModrinth(t *testing.T) *fakeModrinth {
	t.Helper()

	f := &fakeModrinth{}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

// API is the endpoint a test config file should point at.
func (f *fakeModrinth) API() string { return f.URL + "/v2" }

// downloads counts requests for jar payloads, i.e. the network writes a dry run
// must never make.
func (f *fakeModrinth) downloads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.requests {
		if strings.HasPrefix(p, "/files/") {
			n++
		}
	}
	return n
}

func (f *fakeModrinth) record(path string) {
	f.mu.Lock()
	f.requests = append(f.requests, path)
	f.mu.Unlock()
}

func (f *fakeModrinth) serve(w http.ResponseWriter, r *http.Request) {
	f.record(r.URL.Path)

	// Jar payloads, served outside /v2 so they are trivially distinguishable
	// from API calls in the request log.
	if body, ok := f.jar(r.URL.Path); ok {
		_, _ = w.Write(body)
		return
	}

	switch {
	// Hash lookups: only the two fixture jars are "published".
	case r.URL.Path == "/v2/version_file/"+hashutil.SHA1Bytes([]byte(sodiumOldJar)):
		writeJSON(w, versionJSON(sodiumOldID, sodiumOldNo, sodiumOldFile, sodiumOldJar, sodiumOldDate))
	case r.URL.Path == "/v2/version_file/"+hashutil.SHA1Bytes([]byte(sodiumNewJar)):
		writeJSON(w, versionJSON(sodiumNewID, sodiumNewNo, sodiumNewFile, sodiumNewJar, sodiumNewDate))

	// The newer file's mirror URL, which the CDN lookup resolves into a
	// direct download URL.
	case r.URL.Path == "/v2/data/"+hashutil.SHA1Bytes([]byte(sodiumNewJar)):
		writeJSON(w, map[string]any{"url": f.URL + "/files/" + sodiumNewFile})

	case r.URL.Path == "/v2/project/"+sodiumSlug, r.URL.Path == "/v2/project/"+sodiumProjectID:
		writeJSON(w, map[string]any{
			"id": sodiumProjectID, "slug": sodiumSlug, "title": sodiumTitle,
			"description": "A modern rendering engine",
			"categories":  []string{"optimization"},
			"loaders":     []string{"fabric"},
			"client_side": "required", "server_side": "required",
		})

	// The version listing. Served oldest-first on purpose: the client is
	// responsible for ordering, and a fixture that only works when the
	// server happens to agree would hide a regression there.
	case r.URL.Path == "/v2/project/"+sodiumProjectID+"/version":
		writeJSON(w, []any{
			versionJSON(sodiumOldID, sodiumOldNo, sodiumOldFile, sodiumOldJar, sodiumOldDate),
			versionJSON(sodiumNewID, sodiumNewNo, sodiumNewFile, sodiumNewJar, sodiumNewDate),
		})

	// No fuzzy hits by default: a name match would let the resolver attach
	// these jars to an unrelated project, which is exactly the failure this
	// fixture should not paper over.
	case r.URL.Path == "/v2/search":
		hits := f.searchHits
		if hits == nil {
			hits = []map[string]any{}
		}
		writeJSON(w, map[string]any{
			"hits": hits, "offset": 0, "limit": 20, "total_hits": len(hits),
		})

	default:
		http.NotFound(w, r)
	}
}

// jar returns the fixture payload for a download path.
func (f *fakeModrinth) jar(path string) ([]byte, bool) {
	switch path {
	case "/files/" + sodiumOldFile:
		return []byte(sodiumOldJar), true
	case "/files/" + sodiumNewFile:
		return []byte(sodiumNewJar), true
	default:
		return nil, false
	}
}

// versionJSON builds one Modrinth version payload.
func versionJSON(id, number, filename, body, published string) map[string]any {
	return map[string]any{
		"id": id, "project_id": sodiumProjectID, "name": number, "version_number": number,
		"version_type": "release", "game_versions": []string{sodiumGameVsn},
		"loaders": []string{"fabric"}, "date_published": published,
		"files": []any{map[string]any{
			"hashes": map[string]string{
				"sha1":   hashutil.SHA1Bytes([]byte(body)),
				"sha512": hashutil.SHA512Bytes([]byte(body)),
			},
			"url":      "/v2/data/" + hashutil.SHA1Bytes([]byte(body)),
			"filename": filename, "primary": true, "size": len(body),
		}},
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic(err)
	}
}

// ─── throwaway environment ───────────────────────────────────────────────────

// newTestInstance creates <tmp>/mc/versions/<id>/ holding a Fabric version JSON
// and the given jars, and returns the Minecraft root and the instance path.
func newTestInstance(t *testing.T, id string, jars map[string][]byte) (mcDir, instPath string) {
	t.Helper()

	mcDir = filepath.Join(t.TempDir(), "mc")
	instPath = filepath.Join(mcDir, "versions", id)
	if err := os.MkdirAll(filepath.Join(instPath, "mods"), 0o755); err != nil {
		t.Fatalf("mkdir instance: %v", err)
	}
	doc := fmt.Sprintf(
		`{"id":%q,"mainClass":"net.fabricmc.loader.impl.launch.knot.KnotClient",`+
			`"libraries":[{"name":"net.fabricmc:fabric-loader:0.16.0"}]}`, id)
	if err := os.WriteFile(filepath.Join(instPath, id+".json"), []byte(doc), 0o644); err != nil {
		t.Fatalf("write version json: %v", err)
	}
	for name, body := range jars {
		if err := os.WriteFile(filepath.Join(instPath, "mods", name), body, 0o644); err != nil {
			t.Fatalf("write jar %s: %v", name, err)
		}
	}
	return mcDir, instPath
}

// writeTestConfig redirects config, state and cache away from the developer's
// home directory and points the Modrinth client at the fake server.
//
// Every test that runs a command must go through here: without it the CLI would
// read the real ~/.config, resolve the real ~/.minecraft and rewrite the real
// state file.
func writeTestConfig(t *testing.T, mcDir, apiBase string) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("MODHARBOR_MINECRAFT_DIR", mcDir)

	cfgDir := filepath.Join(home, "config", "modharbor")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	cfg := fmt.Sprintf(`{"minecraftDir":%q,"modrinth":{"enabled":true,"baseUrl":%q}}`,
		mcDir, apiBase)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// ─── assertions ──────────────────────────────────────────────────────────────

// snapshotDir records every file under dir with its contents, so a test can
// prove a directory is byte-identical rather than merely the same size.
func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()

	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out
		}
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			// A subdirectory appearing where there was none is itself a
			// change worth failing on, so record it rather than skipping it.
			out[e.Name()+"/"] = "<dir>"
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = string(body)
	}
	return out
}

// assertDirUnchanged compares two snapshots, naming what moved.
func assertDirUnchanged(t *testing.T, want, got map[string]string) {
	t.Helper()

	var added, removed, changed []string
	for name, body := range got {
		before, ok := want[name]
		if !ok {
			added = append(added, name)
			continue
		}
		if before != body {
			changed = append(changed, name)
		}
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			removed = append(removed, name)
		}
	}
	if len(added) == 0 && len(removed) == 0 && len(changed) == 0 {
		return
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	t.Errorf("directory changed:\n  added:    %v\n  removed:  %v\n  modified: %v",
		added, removed, changed)
}

// mustContain fails unless out contains every fragment.
func mustContain(t *testing.T, out string, fragments ...string) {
	t.Helper()
	for _, f := range fragments {
		if !strings.Contains(out, f) {
			t.Errorf("output does not contain %q\n--- output ---\n%s---", f, out)
		}
	}
}

// mustNotContain fails if out contains any fragment.
func mustNotContain(t *testing.T, out string, fragments ...string) {
	t.Helper()
	for _, f := range fragments {
		if strings.Contains(out, f) {
			t.Errorf("output unexpectedly contains %q\n--- output ---\n%s---", f, out)
		}
	}
}

// lineWith returns the first line of s containing needle, or "".
func lineWith(s, needle string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}
