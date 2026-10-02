package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MohammadMD1383/modharbor/internal/hashutil"
	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
	"github.com/MohammadMD1383/modharbor/internal/store"
)

// ─── fixtures ───────────────────────────────────────────────────────────────

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeJar(t *testing.T, dir, name, fabricID, version string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"schemaVersion":1,"id":` + jsonStr(fabricID) +
		`,"version":` + jsonStr(version) +
		`,"name":` + jsonStr(fabricID) + `}`
	writeZipFile(t, filepath.Join(dir, name), map[string]string{"fabric.mod.json": body})
	return filepath.Join(dir, name)
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func makeInstance(t *testing.T, root, id, mcVersion string, loader instance.Type) *instance.Info {
	t.Helper()
	dir := filepath.Join(root, "versions", id)
	libraries := []map[string]any{
		{"name": "org.lwjgl:lwjgl:3.4.3"},
	}
	switch loader {
	case instance.TypeFabric:
		libraries = append(libraries, map[string]any{"name": "net.fabricmc:fabric-loader:0.19.5"})
	case instance.TypeQuilt:
		libraries = append(libraries, map[string]any{"name": "org.quiltmc:quilt-loader:0.27.0"})
	}
	writeJSONFile(t, filepath.Join(dir, id+".json"), map[string]any{
		"id":          id,
		"type":        "release",
		"mainClass":   "net.minecraft.client.main.Main",
		"releaseTime": "2026-09-15T11:23:02+03:30",
		"time":        "2026-09-15T18:36:05+03:30",
		"libraries":   libraries,
	})
	writeJSONFile(t, filepath.Join(dir, "TLauncherAdditional.json"), map[string]any{
		"jar": mcVersion,
		"modpack": map[string]any{
			"name": id,
			"version": map[string]any{
				"minecraftVersionName": map[string]any{"name": "0.19.5"},
				"gameVersionDTO":       map[string]any{"name": mcVersion},
				"minecraftVersionTypes": []any{
					map[string]any{"name": strings.ToLower(string(loader))},
				},
				"mods": []any{},
			},
		},
	})
	// A real instance always has a mods directory, even when empty.
	if err := os.MkdirAll(filepath.Join(dir, "mods"), 0o755); err != nil {
		t.Fatal(err)
	}

	inst, err := instance.Load(dir)
	if err != nil {
		t.Fatalf("loading fixture instance: %v", err)
	}
	return inst
}

// ─── fake Modrinth ──────────────────────────────────────────────────────────

type fakeMR struct {
	// projects maps id -> project
	projects map[string]modrinth.Project
	// versions maps project id -> versions
	versions map[string][]modrinth.Version
	// byHash maps sha1 -> version
	byHash map[string]modrinth.Version
	srv    *httptest.Server
	// cdn maps "data/<hash>" -> direct url
	cdn map[string]string
}

func newFakeMR(t *testing.T) *fakeMR {
	f := &fakeMR{
		projects: map[string]modrinth.Project{},
		versions: map[string][]modrinth.Version{},
		byHash:   map[string]modrinth.Version{},
		cdn:      map[string]string{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMR) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	p := r.URL.Path

	switch {
	case strings.HasPrefix(p, "/version_file/"):
		sha := strings.TrimPrefix(p, "/version_file/")
		v, ok := f.byHash[sha]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(v)

	case strings.HasSuffix(p, "/version") && strings.HasPrefix(p, "/project/") &&
		strings.Count(p, "/") == 3:
		id := p[len("/project/") : len(p)-len("/version")]
		vers, ok := f.versions[id]
		if !ok {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		// Apply the loaders/game_versions filters the same way the real API
		// does, so the tests exercise real filtering behaviour.
		wantGV := parseJSONArray(r.URL.Query().Get("game_versions"))
		wantLoaders := parseJSONArray(r.URL.Query().Get("loaders"))
		wantTypes := parseJSONArray(r.URL.Query().Get("version_type"))
		out := []modrinth.Version{}
		for _, v := range vers {
			if len(wantGV) > 0 && !containsAny(v.GameVersions, wantGV) {
				continue
			}
			if len(wantLoaders) > 0 && !containsAny(v.Loaders, wantLoaders) {
				continue
			}
			if len(wantTypes) > 0 && !containsAny([]string{v.VersionType}, wantTypes) {
				continue
			}
			out = append(out, v)
		}
		_ = json.NewEncoder(w).Encode(out)

	case strings.HasPrefix(p, "/project/"):
		id := strings.TrimPrefix(p, "/project/")
		pr, ok := f.projects[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(pr)

	case strings.HasPrefix(p, "/data/"):
		key := strings.TrimPrefix(p, "/data/")
		if u, ok := f.cdn[key]; ok {
			_ = json.NewEncoder(w).Encode(map[string]string{"url": u})
			return
		}
		w.WriteHeader(http.StatusNotFound)

	case p == "/search":
		hits := []modrinth.Hit{}
		for _, pr := range f.projects {
			hits = append(hits, modrinth.Hit{
				ProjectID: pr.ID, Slug: pr.Slug, Title: pr.Title,
				Downloads: pr.DownloadCount,
			})
		}
		_ = json.NewEncoder(w).Encode(modrinth.SearchResponse{Hits: hits, TotalHits: len(hits)})

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func parseJSONArray(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

func containsAny(hay []string, needles []string) bool {
	for _, n := range needles {
		for _, h := range hay {
			if strings.EqualFold(h, n) {
				return true
			}
		}
	}
	return false
}

// addMod registers a project with a single compatible version.
func (f *fakeMR) addMod(id, slug, title, mcVersion, versionNumber, loader string) modrinth.File {
	body := strings.Repeat("x", 64)
	sha1 := hashutil.SHA1Bytes([]byte(body))
	sha512 := hashutil.SHA512Bytes([]byte(body))
	filename := id + "-" + mcVersion + "-" + versionNumber + ".jar"
	file := modrinth.File{
		Filename: filename,
		Primary:  true,
		Size:     int64(len(body)),
		Hashes:   map[string]string{"sha1": sha1, "sha512": sha512},
		URL:      f.srv.URL + "/v2/data/" + sha1,
	}
	v := modrinth.Version{
		ID:            "ver-" + id + "-" + versionNumber,
		ProjectID:     id,
		VersionNumber: versionNumber,
		VersionType:   "release",
		Loaders:       []string{loader},
		GameVersions:  []string{mcVersion},
		Files:         []modrinth.File{file},
	}
	f.projects[id] = modrinth.Project{ID: id, Slug: slug, Title: title, DownloadCount: 1000}
	f.versions[id] = append(f.versions[id], v)
	f.byHash[sha1] = v
	f.cdn[sha1] = f.srv.URL + "/cdn/" + sha1
	return file
}

func newEngine(t *testing.T, f *fakeMR) *Engine {
	t.Helper()
	eng, _ := newEngineWithStore(t, f)
	return eng
}

// newEngineWithStore returns an engine plus its state store, for tests that
// need to seed or inspect the resolution cache.
func newEngineWithStore(t *testing.T, f *fakeMR) (*Engine, *store.Store) {
	t.Helper()
	cli := modrinth.New(modrinth.Options{BaseURL: f.srv.URL, HTTP: f.srv.Client()})
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return New(cli, newTestResolver(cli, st), st), st
}

// ─── tests ──────────────────────────────────────────────────────────────────

func TestDryRunLeavesFilesystemUntouched(t *testing.T) {
	f := newFakeMR(t)
	f.addMod("sodium", "sodium", "Sodium", "26.3", "0.9.3", "fabric")

	root := t.TempDir()
	src := makeInstance(t, root, "26.2-fabric-mod", "26.2", instance.TypeFabric)
	dst := makeInstance(t, root, "26.3-fabric-mod", "26.3", instance.TypeFabric)

	// Source jar whose bytes exactly match the fake provider's file, so hash
	// lookup succeeds.
	writeJar(t, src.ModsDirOrDefault(), "sodium-fabric-0.9.1.jar", "sodium", "0.9.1")

	eng := newEngine(t, f)
	rep, err := eng.Run(context.Background(), src, dst, Options{DryRun: true, CopyUnknown: false})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Installed != 1 {
		t.Fatalf("Installed = %d, want 1 (results: %+v)", rep.Installed, rep.Results)
	}
	res := rep.Results[0]
	if res.Action != ActionInstall {
		t.Errorf("Action = %q, want install", res.Action)
	}
	if res.TargetVersion != "0.9.3" {
		t.Errorf("TargetVersion = %q, want 0.9.3", res.TargetVersion)
	}
	// Nothing should have been written.
	if _, err := os.Stat(filepath.Join(dst.ModsDirOrDefault(), res.TargetFile)); err == nil {
		t.Error("dry run wrote a file to the destination")
	}
}

func TestKeepWhenTargetAlreadyHasExactFile(t *testing.T) {
	f := newFakeMR(t)
	file := f.addMod("sodium", "sodium", "Sodium", "26.3", "0.9.3", "fabric")

	root := t.TempDir()
	src := makeInstance(t, root, "26.2-fabric-mod", "26.2", instance.TypeFabric)
	dst := makeInstance(t, root, "26.3-fabric-mod", "26.3", instance.TypeFabric)

	writeJar(t, src.ModsDirOrDefault(), "sodium-old.jar", "sodium", "0.9.1")
	// Write the provider's exact bytes into the destination.
	if err := os.WriteFile(filepath.Join(dst.ModsDirOrDefault(), file.Filename), []byte(strings.Repeat("x", 64)), 0o644); err != nil {
		t.Fatal(err)
	}

	eng := newEngine(t, f)
	rep, err := eng.Run(context.Background(), src, dst, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Kept != 1 {
		t.Fatalf("Kept = %d, want 1 (results %+v)", rep.Kept, rep.Results)
	}
}

func TestSkipWhenNoCompatibleVersion(t *testing.T) {
	f := newFakeMR(t)
	// The only build targets 26.2, but the destination is 26.3.
	f.addMod("oldmod", "oldmod", "Old Mod", "26.2", "1.0.0", "fabric")

	root := t.TempDir()
	src := makeInstance(t, root, "26.2-fabric-mod", "26.2", instance.TypeFabric)
	dst := makeInstance(t, root, "26.3-fabric-mod", "26.3", instance.TypeFabric)

	writeJar(t, src.ModsDirOrDefault(), "oldmod-26.2.jar", "oldmod", "1.0.0")
	// Seed the resolver cache so identification succeeds without a hash hit,
	// and use that same engine's store so the seed is visible.
	eng, st := newEngineWithStore(t, f)
	body, _ := os.ReadFile(filepath.Join(src.ModsDirOrDefault(), "oldmod-26.2.jar"))
	st.Put(store.Resolution{
		SHA1: hashutil.SHA1Bytes(body), Provider: "modrinth",
		ProjectID: "oldmod", VersionNumber: "1.0.0", Method: "hash", Confidence: 1,
	})

	rep, err := eng.Run(context.Background(), src, dst, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1 (%+v)", rep.Skipped, rep.Results)
	}
	if !strings.Contains(rep.Results[0].Reason, "26.3") {
		t.Errorf("Reason = %q, want it to mention the target MC version", rep.Results[0].Reason)
	}
}

func TestUnmatchedModIsReportedNotInstalled(t *testing.T) {
	f := newFakeMR(t)
	// No projects registered: nothing can match.
	root := t.TempDir()
	src := makeInstance(t, root, "26.2-fabric-mod", "26.2", instance.TypeFabric)
	dst := makeInstance(t, root, "26.3-fabric-mod", "26.3", instance.TypeFabric)

	writeJar(t, src.ModsDirOrDefault(), "moretools-1.0.0.jar", "more-tools", "1.0.0")

	rep, err := newEngine(t, f).Run(context.Background(), src, dst, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Unmatched != 1 {
		t.Fatalf("Unmatched = %d, want 1 (%+v)", rep.Unmatched, rep.Results)
	}
}

func TestCopyUnknownCarriesModOver(t *testing.T) {
	f := newFakeMR(t)
	root := t.TempDir()
	src := makeInstance(t, root, "26.2-fabric-mod", "26.2", instance.TypeFabric)
	dst := makeInstance(t, root, "26.3-fabric-mod", "26.3", instance.TypeFabric)

	writeJar(t, src.ModsDirOrDefault(), "moretools-1.0.0.jar", "more-tools", "1.0.0")

	rep, err := newEngine(t, f).Run(context.Background(), src, dst, Options{DryRun: true, CopyUnknown: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Copied != 1 {
		t.Fatalf("Copied = %d, want 1 (%+v)", rep.Copied, rep.Results)
	}
}

func TestReplaceWhenTargetHasOlderDifferentFile(t *testing.T) {
	f := newFakeMR(t)
	f.addMod("sodium", "sodium", "Sodium", "26.3", "0.9.3", "fabric")

	root := t.TempDir()
	src := makeInstance(t, root, "26.2-fabric-mod", "26.2", instance.TypeFabric)
	dst := makeInstance(t, root, "26.3-fabric-mod", "26.3", instance.TypeFabric)

	writeJar(t, src.ModsDirOrDefault(), "sodium-old.jar", "sodium", "0.9.1")
	// Destination has a different build of the same project.
	writeJar(t, dst.ModsDirOrDefault(), "sodium-stale.jar", "sodium", "0.9.0")

	eng, st := newEngineWithStore(t, f)
	for _, dir := range []string{src.ModsDirOrDefault(), dst.ModsDirOrDefault()} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
			st.Put(store.Resolution{
				SHA1: hashutil.SHA1Bytes(b), Provider: "modrinth", ProjectID: "sodium",
				VersionNumber: "0.9.1", Method: "hash", Confidence: 1.0,
			})
		}
	}

	rep, err := eng.Run(context.Background(), src, dst, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Replaced != 1 {
		t.Fatalf("Replaced = %d, want 1 (%+v)", rep.Replaced, rep.Results)
	}
}

func TestRealMigrationWritesAndVerifiesChecksum(t *testing.T) {
	f := newFakeMR(t)
	file := f.addMod("sodium", "sodium", "Sodium", "26.3", "0.9.3", "fabric")

	// Serve the exact bytes the fake advertised from the same server.
	body := []byte(strings.Repeat("x", 64))
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/cdn/") {
			_, _ = w.Write(body)
			return
		}
		f.handle(w, r)
	})

	root := t.TempDir()
	src := makeInstance(t, root, "26.2-fabric-mod", "26.2", instance.TypeFabric)
	dst := makeInstance(t, root, "26.3-fabric-mod", "26.3", instance.TypeFabric)
	writeJar(t, src.ModsDirOrDefault(), "sodium-old.jar", "sodium", "0.9.1")

	rep, err := newEngine(t, f).Run(context.Background(), src, dst, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Installed != 1 {
		t.Fatalf("Installed = %d, want 1 (%+v)", rep.Installed, rep.Results)
	}
	dest := filepath.Join(dst.ModsDirOrDefault(), file.Filename)
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", dest, err)
	}
	if hashutil.SHA512Bytes(got) != file.SHA512() {
		t.Error("downloaded file does not match the advertised sha512")
	}
}

func TestChecksumMismatchIsRejected(t *testing.T) {
	f := newFakeMR(t)
	f.addMod("sodium", "sodium", "Sodium", "26.3", "0.9.3", "fabric")

	// Serve wrong bytes from a separate origin.
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("corrupted"))
	}))
	defer badSrv.Close()
	for k := range f.cdn {
		f.cdn[k] = badSrv.URL + "/cdn/" + k
	}

	root := t.TempDir()
	src := makeInstance(t, root, "26.2-fabric-mod", "26.2", instance.TypeFabric)
	dst := makeInstance(t, root, "26.3-fabric-mod", "26.3", instance.TypeFabric)
	writeJar(t, src.ModsDirOrDefault(), "sodium-old.jar", "sodium", "0.9.1")

	if _, err := newEngine(t, f).Run(context.Background(), src, dst, Options{}); err == nil {
		t.Fatal("expected a checksum mismatch error")
	} else if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error = %v, want a checksum mismatch", err)
	}
	// The bad file must not be left behind.
	entries, _ := os.ReadDir(dst.ModsDirOrDefault())
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jar") {
			t.Errorf("a jar was left behind after a failed download: %s", e.Name())
		}
	}
}

func TestIncludeExcludeFilters(t *testing.T) {
	f := newFakeMR(t)
	f.addMod("sodium", "sodium", "Sodium", "26.3", "0.9.3", "fabric")
	f.addMod("zoomify", "zoomify", "Zoomify", "26.3", "2.16.1", "fabric")

	root := t.TempDir()
	src := makeInstance(t, root, "26.2-fabric-mod", "26.2", instance.TypeFabric)
	dst := makeInstance(t, root, "26.3-fabric-mod", "26.3", instance.TypeFabric)
	writeJar(t, src.ModsDirOrDefault(), "sodium-old.jar", "sodium", "0.9.1")
	writeJar(t, src.ModsDirOrDefault(), "zoomify-old.jar", "zoomify", "2.16.0")

	rep, err := newEngine(t, f).Run(context.Background(), src, dst, Options{
		DryRun: true, Include: []string{"sodium"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 1 || rep.Results[0].ProjectID != "sodium" {
		t.Fatalf("expected only sodium, got %+v", rep.Results)
	}
}

func TestReportSummariseAndPlanned(t *testing.T) {
	rep := &Report{Results: []Result{
		{Action: ActionInstall},
		{Action: ActionReplace},
		{Action: ActionKeep},
		{Action: ActionSkip},
		{Action: ActionUnmatched},
		{Action: ActionCopy},
	}}
	rep.Summarise()
	if rep.Installed != 1 || rep.Replaced != 1 || rep.Kept != 1 || rep.Skipped != 1 || rep.Unmatched != 1 || rep.Copied != 1 {
		t.Errorf("unexpected tally: %+v", rep)
	}
	if got := len(rep.Planned()); got != 3 {
		t.Errorf("Planned() = %d, want 3", got)
	}
}
