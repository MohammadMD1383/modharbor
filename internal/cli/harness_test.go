package cli

import (
	"archive/zip"
	"bytes"
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
	"time"

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
// Both streams are returned because the CLI writes through two different
// paths: internal/ui holds its own writers, while printJSON writes to
// os.Stdout directly. They are concatenated rather than kept apart, which is
// what a test asserting on "the output" wants. Use executeCLI when the two
// streams have to be told apart. Everything is restored afterwards so tests
// stay independent.
func captureCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var runErr error
	stdout, stderr := captureStreams(t, func() {
		root := newRootCmd()
		root.SetArgs(args)
		runErr = root.Execute()
	})
	return stdout + stderr, runErr
}

// executeCLI runs the real process entry point and reports its exit code along
// with each stream kept separate.
//
// It exists because the two entry points are not interchangeable. A command
// returns an error to cobra and writes nothing; it is Execute that turns that
// error into an exit code *and* a message on stderr. Testing root.Execute()
// therefore skips exactly the code that once swallowed every message (B1):
// the tool exited non-zero while both streams stayed empty. Only calling
// Execute() itself can catch that coming back.
func executeCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()

	origArgs := os.Args
	// cobra reads os.Args[1:] when no SetArgs call happened, so this is how the
	// binary's argv reaches it. Naming the program "modharbor" keeps cobra's
	// own "cobra.test" workaround from swallowing the arguments.
	os.Args = append([]string{"modharbor"}, args...)
	t.Cleanup(func() { os.Args = origArgs })

	stdout, stderr = captureStreams(t, func() { code = Execute() })
	return code, stdout, stderr
}

// captureStreams points os.Stdout, os.Stderr and the internal/ui writers at
// pipes, runs fn, and returns what each stream received.
func captureStreams(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()

	origOut, origErr := os.Stdout, os.Stderr
	outR, outW := newPipe(t)
	errR, errW := newPipe(t)
	os.Stdout, os.Stderr = outW, errW
	// Colour is off so assertions can match plain text; ANSI escapes would
	// otherwise sit between a word and the space that follows it.
	ui.SetWriters(outW, errW)
	ui.SetColorEnabled(false)
	t.Cleanup(func() {
		os.Stdout, os.Stderr = origOut, origErr
		ui.SetWriters(origOut, origErr)
		ui.SetColorEnabled(false)
	})

	// Each pipe is drained by its own goroutine while fn runs. Reading only
	// afterwards would deadlock on any output larger than the pipe buffer,
	// turning a printing test into a hung suite.
	var wg sync.WaitGroup
	var outBody, errBody []byte
	var outErr, readErr error
	for _, p := range []struct {
		r    *os.File
		w    *os.File
		body *[]byte
		fail *error
	}{
		{r: outR, w: outW, body: &outBody, fail: &outErr},
		{r: errR, w: errW, body: &errBody, fail: &readErr},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := io.ReadAll(p.r)
			*p.body, *p.fail = b, err
		}()
	}

	fn()

	for _, w := range []*os.File{outW, errW} {
		if err := w.Close(); err != nil {
			t.Fatalf("close pipe: %v", err)
		}
	}
	wg.Wait()
	for _, r := range []*os.File{outR, errR} {
		if err := r.Close(); err != nil {
			t.Fatalf("close reader: %v", err)
		}
	}
	if outErr != nil {
		t.Fatalf("read stdout: %v", outErr)
	}
	if readErr != nil {
		t.Fatalf("read stderr: %v", readErr)
	}
	return string(outBody), string(errBody)
}

func newPipe(t *testing.T) (r, w *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	return r, w
}

// ─── the fake Modrinth ───────────────────────────────────────────────────────

// One project, "sodium", with two published versions. Holding the 0.10.0 jar in
// a mods directory and offering 0.11.0 upstream is the smallest setup that
// exercises a real upgrade: the engine resolves the jar by hash, picks the
// newer build, and replaces the file.
//
// A third fixture, sodiumMirrorJar, is known to Modrinth by hash but is absent
// from the project's version listing. It stands in for a jar republished
// elsewhere: the same version number with different bytes, which the engine
// must label a reinstall rather than an upgrade.
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

	sodiumMirrorJar  = "sodium 0.11.0 (repacked)\n"
	sodiumMirrorFile = "sodium-0.11.0-mirror.jar"
	sodiumMirrorID   = "verMirror00000000"
	sodiumMirrorNo   = sodiumNewNo
	sodiumMirrorDate = "2025-12-01T00:00:00Z"
	sodiumModID      = "sodium"
	sodiumModName    = "Sodium"
)

// A second project that exists but publishes nothing compatible. It is the
// cheapest way to reach the "skip" decision: the local jar resolves cleanly by
// hash, then the version query comes back empty, so the engine has to explain
// why rather than silently treating the mod as current.
const (
	lithiumProjectID = "gvQqBUqZ"
	lithiumSlug      = "lithium"
	lithiumTitle     = "Lithium"
	lithiumJar       = "lithium 0.2.0\n"
	lithiumFile      = "lithium-0.2.0.jar"
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
	// Hash lookups: every fixture jar is "published", so each resolves to a
	// project without any fuzzy matching.
	case r.URL.Path == "/v2/version_file/"+hashutil.SHA1Bytes([]byte(sodiumOldJar)):
		writeJSON(w, versionJSON(sodiumProjectID, sodiumOldID, sodiumOldNo,
			sodiumOldFile, sodiumOldJar, sodiumOldDate, sodiumGameVsn))
	case r.URL.Path == "/v2/version_file/"+hashutil.SHA1Bytes([]byte(sodiumNewJar)):
		writeJSON(w, versionJSON(sodiumProjectID, sodiumNewID, sodiumNewNo,
			sodiumNewFile, sodiumNewJar, sodiumNewDate, sodiumGameVsn))

	// The mirror is known by hash but deliberately missing from the listing
	// below. A jar on disk that resolves to the same version number as the
	// newest build, with different bytes, is the reinstall case.
	case r.URL.Path == "/v2/version_file/"+hashutil.SHA1Bytes([]byte(sodiumMirrorJar)):
		writeJSON(w, versionJSON(sodiumProjectID, sodiumMirrorID, sodiumMirrorNo,
			sodiumMirrorFile, sodiumMirrorJar, sodiumMirrorDate, sodiumGameVsn))

	case r.URL.Path == "/v2/version_file/"+hashutil.SHA1Bytes([]byte(lithiumJar)):
		writeJSON(w, versionJSON(lithiumProjectID, "verLithium0000000", "mc26.3-0.2.0-fabric",
			lithiumFile, lithiumJar, "2026-01-15T00:00:00Z", sodiumGameVsn))

	// The newer file's mirror URL, which the CDN lookup resolves into a
	// direct download URL.
	case r.URL.Path == "/v2/data/"+hashutil.SHA1Bytes([]byte(sodiumNewJar)):
		writeJSON(w, map[string]any{"url": f.URL + "/files/" + sodiumNewFile})
	case r.URL.Path == "/v2/data/"+hashutil.SHA1Bytes([]byte(sodiumMirrorJar)):
		writeJSON(w, map[string]any{"url": f.URL + "/files/" + sodiumMirrorFile})

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
	// server happens to agree would hide a regression there. The mirror is
	// left out on purpose, as explained above.
	case r.URL.Path == "/v2/project/"+sodiumProjectID+"/version":
		writeJSON(w, []any{
			versionJSON(sodiumProjectID, sodiumOldID, sodiumOldNo,
				sodiumOldFile, sodiumOldJar, sodiumOldDate, sodiumGameVsn),
			versionJSON(sodiumProjectID, sodiumNewID, sodiumNewNo,
				sodiumNewFile, sodiumNewJar, sodiumNewDate, sodiumGameVsn),
		})

	case r.URL.Path == "/v2/project/"+lithiumSlug, r.URL.Path == "/v2/project/"+lithiumProjectID:
		writeJSON(w, map[string]any{
			"id": lithiumProjectID, "slug": lithiumSlug, "title": lithiumTitle,
			"description": "Optimises physics and tick behaviour",
			"categories":  []string{"optimization"},
			"loaders":     []string{"fabric"},
			"client_side": "required", "server_side": "required",
		})

	// The project exists but has published nothing. bestVersion has to fall
	// through to explainEmpty, which is what distinguishes a skip from a
	// silent "already up to date".
	case r.URL.Path == "/v2/project/"+lithiumProjectID+"/version":
		writeJSON(w, []any{})

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
	case "/files/" + sodiumMirrorFile:
		return []byte(sodiumMirrorJar), true
	default:
		return nil, false
	}
}

// versionJSON builds one Modrinth version payload. projectID and gameVersion
// are explicit because the fixtures span two projects; a hard-coded one made
// every added project a chance to attach itself to sodium instead.
func versionJSON(projectID, id, number, filename, body, published, gameVersion string) map[string]any {
	return map[string]any{
		"id": id, "project_id": projectID, "name": number, "version_number": number,
		"version_type": "release", "game_versions": []string{gameVersion},
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

// fabricJar builds a real jar containing a fabric.mod.json descriptor.
//
// A plain []byte fixture will not do. Everything that identifies a mod without
// a Modrinth hit — the duplicate check in doctor, the mod-id column in list,
// the dependency check — works by opening the archive and reading the
// descriptor, so a fixture that is not a zip would silently exercise none of it
// and those checks would look "covered" while never running.
func fabricJar(t *testing.T, modID, name, version, dependsOn string) []byte {
	t.Helper()

	if dependsOn == "" {
		dependsOn = "{}"
	}
	descriptor := fmt.Sprintf(
		`{"schemaVersion":1,"id":%q,"version":%q,"name":%q,"depends":%s}`,
		modID, version, name, dependsOn)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("fabric.mod.json")
	if err != nil {
		t.Fatalf("create fabric.mod.json: %v", err)
	}
	if _, err := io.WriteString(w, descriptor); err != nil {
		t.Fatalf("write fabric.mod.json: %v", err)
	}
	// One real class file so the archive is not suspiciously small.
	w, err = zw.Create("net/example/Dummy.class")
	if err != nil {
		t.Fatalf("create class entry: %v", err)
	}
	if _, err := io.WriteString(w, "// fixture\n"); err != nil {
		t.Fatalf("write class entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close jar: %v", err)
	}
	return buf.Bytes()
}

// modsDirOf is the mods directory of an instance built by newTestInstance.
func modsDirOf(instPath string) string { return filepath.Join(instPath, "mods") }

// setModTime pins a file's timestamp. doctor decides which of a set of
// duplicate jars to keep by comparing modification times, so a test that leaves
// them all equal is asserting on an arbitrary winner.
func setModTime(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
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

// decodeJSON parses the stdout of a --json command into v.
//
// The failure message quotes the payload so a malformed document is diagnosable
// from the test output alone.
func decodeJSON(t *testing.T, out string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("output is not valid JSON: %v\n--- output ---\n%s---", err, out)
	}
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
