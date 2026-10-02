package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MohammadMD1383/modharbor/internal/hashutil"
	"github.com/MohammadMD1383/modharbor/internal/mrpack"
)

// The export tests share one fixture: a single jar that the stub recognises, so
// the only interesting question is which server export asked about it. Nothing
// here reads or writes anything outside t.TempDir.

const (
	exportInstanceID = "26.3-fabric-export"
	exportMCVersion  = "26.3"

	exportProjectID = "AANobbMI"
	exportVersionID = "verExport00000000"
	exportVersionNo = "0.10.0"
	exportFileName  = "sodium-0.10.0.jar"
	exportJarBody   = "sodium 0.10.0 export fixture\n"
)

// ─── the fake Modrinth ───────────────────────────────────────────────────────

// exportStub records every path it is asked for. The log is what makes these
// tests regression tests rather than smoke tests: an export that quietly talked
// to a different server still produces a valid pack, so only the log can tell.
type exportStub struct {
	*httptest.Server

	mu    sync.Mutex
	paths []string
}

func newExportStub(t *testing.T) *exportStub {
	t.Helper()

	s := &exportStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// API is the base URL a config document should carry.
func (s *exportStub) API() string { return s.URL + "/v2" }

// asked counts recorded paths carrying the given prefix.
func (s *exportStub) asked(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := 0
	for _, p := range s.paths {
		if strings.HasPrefix(p, prefix) {
			n++
		}
	}
	return n
}

func (s *exportStub) log() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.paths...)
}

func (s *exportStub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.paths = append(s.paths, r.URL.Path)
	s.mu.Unlock()

	sha1 := hashutil.SHA1Bytes([]byte(exportJarBody))
	switch r.URL.Path {
	case "/v2/version_file/" + sha1:
		writeJSON(w, map[string]any{
			"id": exportVersionID, "project_id": exportProjectID,
			"name": exportVersionNo, "version_number": exportVersionNo,
			"version_type": "release", "game_versions": []string{exportMCVersion},
			"loaders": []string{"fabric"}, "date_published": "2026-01-01T00:00:00Z",
			"files": []any{map[string]any{
				"hashes": map[string]string{
					"sha1":   sha1,
					"sha512": hashutil.SHA512Bytes([]byte(exportJarBody)),
				},
				"url":      "/v2/data/" + sha1,
				"filename": exportFileName, "primary": true, "size": len(exportJarBody),
			}},
		})

	case "/v2/project/" + exportProjectID:
		writeJSON(w, map[string]any{
			"id": exportProjectID, "slug": "sodium", "title": "Sodium",
			"client_side": "required", "server_side": "required",
		})

	// The CDN lookup entryFor makes in preference to the mirror URL.
	case "/v2/data/" + sha1:
		writeJSON(w, map[string]any{"url": s.URL + "/files/" + exportFileName})

	default:
		http.NotFound(w, r)
	}
}

// newUnknownModrinth stands in for a Modrinth that recognises nothing, so a
// jar sent to it silently becomes an override rather than an error. It exists to
// catch traffic aimed at the wrong endpoint: it counts, and never serves.
func newUnknownModrinth(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// ─── throwaway environment ───────────────────────────────────────────────────

// exportHome points config, state and cache at temp directories. The Minecraft
// directory is redirected separately by newExportInstance; both are needed
// because a bare bootstrap would otherwise resolve the developer's own
// ~/.minecraft and read their real config.
func exportHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	return home
}

// newExportInstance creates a one-mod Fabric instance and returns the Minecraft
// root containing it.
func newExportInstance(t *testing.T) string {
	t.Helper()

	mcDir := filepath.Join(t.TempDir(), "mc")
	instPath := filepath.Join(mcDir, "versions", exportInstanceID)
	if err := os.MkdirAll(filepath.Join(instPath, "mods"), 0o755); err != nil {
		t.Fatalf("mkdir instance: %v", err)
	}
	doc := fmt.Sprintf(
		`{"id":%q,"mainClass":"net.fabricmc.loader.impl.launch.knot.KnotClient",`+
			`"libraries":[{"name":"net.fabricmc:fabric-loader:0.16.10"}]}`, exportInstanceID)
	if err := os.WriteFile(filepath.Join(instPath, exportInstanceID+".json"), []byte(doc), 0o644); err != nil {
		t.Fatalf("write version json: %v", err)
	}
	jar := filepath.Join(instPath, "mods", exportFileName)
	if err := os.WriteFile(jar, []byte(exportJarBody), 0o644); err != nil {
		t.Fatalf("write jar: %v", err)
	}

	t.Setenv("MODHARBOR_MINECRAFT_DIR", mcDir)
	return mcDir
}

// writeExportConfig writes a config document and returns its path. An empty
// name lands in the XDG location that config.Load("") reads by default.
func writeExportConfig(t *testing.T, home, name, mcDir, baseURL string) string {
	t.Helper()

	dir := filepath.Join(home, "config", "modharbor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	body := fmt.Sprintf(`{"minecraftDir":%q,"modrinth":{"enabled":true,"baseUrl":%q}}`, mcDir, baseURL)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config %s: %v", path, err)
	}
	return path
}

// ─── tests ───────────────────────────────────────────────────────────────────

// TestExportUsesTheAppsModrinthClient is the regression test for B7.
//
// Export used to build its own client from config.Load(""), which meant the
// endpoint the application had already resolved was thrown away. The two
// configs here make that observable: the XDG config — the one export re-read —
// points at a Modrinth that recognises nothing, while --config points at the
// stub. With the old client the stub's log stayed empty and the jar was carried
// as an override; with the app's client the stub sees the lookup and the jar is
// referenced by URL.
func TestExportUsesTheAppsModrinthClient(t *testing.T) {
	stub := newExportStub(t)
	unknown, unknownHits := newUnknownModrinth(t)

	home := exportHome(t)
	mcDir := newExportInstance(t)
	writeExportConfig(t, home, "config.json", mcDir, unknown.URL+"/v2")
	appConfig := writeExportConfig(t, home, "export.json", mcDir, stub.API())

	out := t.TempDir()
	stdout, err := captureCLI(t, "export", "--config", appConfig, "--json", out, exportInstanceID)
	if err != nil {
		t.Fatalf("export: %v\n--- output ---\n%s", err, stdout)
	}

	if n := stub.asked("/v2/version_file/"); n == 0 {
		t.Errorf("the configured Modrinth server was never asked to identify the jar; "+
			"export must use the application's client\nstub saw: %v\n%s", stub.log(), stdout)
	}
	if n := unknownHits.Load(); n != 0 {
		t.Errorf("export sent %d request(s) to the endpoint in the user config it "+
			"should not have consulted", n)
	}

	var res exportJSON
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("decoding export result: %v\n--- output ---\n%s", err, stdout)
	}
	if res.Resolved != 1 || res.Overrides != 0 || res.Total != 1 {
		t.Errorf("export result = %+v, want one resolved mod and no overrides", res)
	}
}

// TestExportPinsInstalledVersions exports a temp instance and reads the pack
// back, because a manifest that lists a different file than the one on disk
// would reinstall something the user never asked for.
func TestExportPinsInstalledVersions(t *testing.T) {
	stub := newExportStub(t)

	home := exportHome(t)
	mcDir := newExportInstance(t)
	writeExportConfig(t, home, "config.json", mcDir, stub.API())

	out := t.TempDir()
	stdout, err := captureCLI(t, "export", "--json", out, exportInstanceID)
	if err != nil {
		t.Fatalf("export: %v\n--- output ---\n%s", err, stdout)
	}

	var res exportJSON
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("decoding export result: %v\n--- output ---\n%s", err, stdout)
	}

	pack, err := mrpack.Load(res.Path)
	if err != nil {
		t.Fatalf("reading back %s: %v", res.Path, err)
	}

	mods := pack.Mods()
	if len(mods) != 1 {
		t.Fatalf("manifest carries %d mod file(s), want 1: %+v", len(mods), mods)
	}
	if got, want := mods[0].Path, mrpack.ModsPrefix+exportFileName; got != want {
		t.Errorf("manifest path = %q, want %q", got, want)
	}
	if got, want := mods[0].SHA1(), hashutil.SHA1Bytes([]byte(exportJarBody)); got != want {
		t.Errorf("manifest sha1 = %q, want the digest of the installed jar %q", got, want)
	}
	if len(mods[0].Downloads) != 1 || !strings.HasSuffix(mods[0].Downloads[0], "/files/"+exportFileName) {
		t.Errorf("manifest downloads = %v, want the stub's URL for %s", mods[0].Downloads, exportFileName)
	}
	if got := packMCVersion(pack); got != exportMCVersion {
		t.Errorf("manifest minecraft dependency = %q, want %q", got, exportMCVersion)
	}
	if got, want := pack.VersionID, exportMCVersion; got != want {
		t.Errorf("manifest versionId = %q, want %q", got, want)
	}
	if len(pack.Overrides()) != 0 {
		t.Errorf("a jar Modrinth recognises must not be shipped as an override: %+v", pack.Overrides())
	}
}
