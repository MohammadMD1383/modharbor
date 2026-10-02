package mrpack

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MohammadMD1383/modharbor/internal/hashutil"
	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
)

// writeZip builds an archive from a name → contents map. Order does not matter
// to any reader, so tests can state only what they care about.
func writeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
}

// writeInstance creates the minimum on-disk shape instance.Load needs: a
// version JSON named after its own directory, declaring a Fabric loader.
func writeInstance(t *testing.T, dir, id string) string {
	t.Helper()
	abs := filepath.Join(dir, id)
	if err := os.MkdirAll(filepath.Join(abs, "mods"), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", abs, err)
	}
	doc := map[string]any{
		"id":        id,
		"mainClass": "net.fabricmc.loader.impl.launch.knot.KnotClient",
		"libraries": []map[string]string{{"name": "net.fabricmc:fabric-loader:0.16.0"}},
	}
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(abs, id+".json"), b, 0o644); err != nil {
		t.Fatalf("write version json: %v", err)
	}
	return abs
}

func TestLoad(t *testing.T) {
	// The manifest below is the exact shape Modrinth publishes: verified
	// against a real .mrpack rather than written from memory.
	const valid = `{
	  "formatVersion": 1,
	  "game": "minecraft",
	  "versionId": "1.0.0",
	  "name": "Test Pack",
	  "summary": "a pack",
	  "files": [
	    {
	      "path": "mods/sodium-0.6.0.jar",
	      "hashes": {"sha1": "0123456789abcdef0123456789abcdef01234567"},
	      "env": {"client": "required", "server": "unsupported"},
	      "downloads": ["https://cdn.modrinth.com/data/x/versions/y/sodium.jar"],
	      "fileSize": 89214
	    }
	  ],
	  "dependencies": {"minecraft": "26.3", "fabric-loader": "0.16.0"}
	}`

	tests := []struct {
		name    string
		entries map[string]string
		body    string // raw bytes, used instead of entries for a non-zip file
		wantErr string
	}{
		{name: "valid", entries: map[string]string{IndexName: valid}},
		{
			name:    "not a zip",
			body:    "this is not a zip archive at all",
			wantErr: "not a readable zip archive",
		},
		{
			name:    "zip without manifest",
			entries: map[string]string{"readme.txt": "hi"},
			wantErr: "is not a Modrinth modpack",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "pack.mrpack")
			if tc.body != "" {
				if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
			} else {
				writeZip(t, path, tc.entries)
			}

			mp, err := Load(path)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error, got pack %+v", mp)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not mention %q", err, tc.wantErr)
				}
				if !strings.Contains(err.Error(), path) {
					t.Fatalf("error %q does not name the path", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if mp.FormatVersion != 1 || mp.Game != GameMinecraft {
				t.Fatalf("header not parsed: %+v", mp)
			}
			if mp.VersionID != "1.0.0" || mp.Name != "Test Pack" || mp.Summary != "a pack" {
				t.Fatalf("versions/name not preserved: %+v", mp)
			}
			if len(mp.Mods()) != 1 {
				t.Fatalf("expected 1 mod, got %d", len(mp.Mods()))
			}
			f := mp.Mods()[0]
			if f.Name() != "sodium-0.6.0.jar" {
				t.Fatalf("name = %q", f.Name())
			}
			if f.SHA1() != "0123456789abcdef0123456789abcdef01234567" {
				t.Fatalf("sha1 = %q", f.SHA1())
			}
			if f.FileSize != 89214 || len(f.Downloads) != 1 {
				t.Fatalf("file metadata wrong: %+v", f)
			}
			if f.Env[EnvServer] != EnvUnsupported {
				t.Fatalf("env not parsed: %+v", f.Env)
			}
			if got := mp.Dependencies["minecraft"].VersionID; got != "26.3" {
				t.Fatalf("minecraft dependency = %q", got)
			}
			if got := mp.Dependencies["fabric-loader"].VersionID; got != "0.16.0" {
				t.Fatalf("loader dependency = %q", got)
			}
		})
	}
}

func TestLoadRejectsBadManifests(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		wantErr  string
	}{
		{
			name:     "invalid json",
			manifest: `{"formatVersion": 1,`,
			wantErr:  "is not valid JSON",
		},
		{
			name:     "no format version",
			manifest: `{"game":"minecraft","name":"x"}`,
			wantErr:  "does not declare a formatVersion",
		},
		{
			name:     "future format version",
			manifest: `{"formatVersion":99,"game":"minecraft","name":"x"}`,
			wantErr:  "modharbor understands up to 1",
		},
		{
			name:     "wrong game",
			manifest: `{"formatVersion":1,"game":"stardew","name":"x"}`,
			wantErr:  "only handles minecraft",
		},
		{
			name:     "no name",
			manifest: `{"formatVersion":1,"game":"minecraft"}`,
			wantErr:  "has no name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pack.mrpack")
			writeZip(t, path, map[string]string{IndexName: tc.manifest})

			_, err := Load(path)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("error %q does not name the archive", err)
			}
		})
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	overrides := filepath.Join(dir, "ovr")
	if err := os.MkdirAll(filepath.Join(overrides, "config"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(overrides, "config", "sodium.json"), []byte(`{"quality":"fast"}`), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}

	want := &Modpack{
		FormatVersion: FormatVersion,
		Game:          GameMinecraft,
		VersionID:     "26.3",
		Name:          "round trip",
		Summary:       "preserves everything",
		Files: []File{
			{
				Path:      "mods/sodium.jar",
				Hashes:    map[string]string{"sha1": "aaa", "sha512": "bbb"},
				Env:       map[string]string{EnvClient: EnvRequired, EnvServer: EnvRequired},
				Downloads: []string{"https://cdn.modrinth.com/data/a/versions/b/sodium.jar"},
				FileSize:  1234,
			},
			{
				Path:     "resourcepacks/hd.zip",
				Hashes:   map[string]string{"sha1": "ccc"},
				FileSize: 99,
			},
		},
		Dependencies: map[string]Dependency{
			"minecraft":     {VersionID: "26.3", DependencyType: "minecraft"},
			"fabric-loader": {VersionID: "0.16.0", DependencyType: "fabric-loader"},
		},
	}

	out := filepath.Join(dir, "out", "pack-26.3.mrpack")
	if err := Save(out, want, overrides); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The manifest on disk must match the spec, not modharbor's richer struct.
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatalf("open written pack: %v", err)
	}
	var raw struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	rc, err := findIndex(t, &zr.Reader)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if err := json.NewDecoder(rc).Decode(&raw); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	rc.Close()
	zr.Close()
	if raw.Dependencies["minecraft"] != "26.3" || raw.Dependencies["fabric-loader"] != "0.16.0" {
		t.Fatalf("dependencies are not a plain name->version map: %+v", raw.Dependencies)
	}

	got, err := Load(out)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.FormatVersion != want.FormatVersion || got.Game != want.Game {
		t.Fatalf("header changed: %+v", got)
	}
	if got.VersionID != want.VersionID || got.Name != want.Name || got.Summary != want.Summary {
		t.Fatalf("versions changed: %+v", got)
	}
	if len(got.Files) != len(want.Files)+1 {
		// Two manifest files plus the one override the archive carries.
		t.Fatalf("expected 3 files, got %d: %+v", len(got.Files), got.Files)
	}
	if len(got.Mods()) != 1 || len(got.Overrides()) != 1 {
		t.Fatalf("Mods/Overrides partition wrong: %d mods, %d overrides",
			len(got.Mods()), len(got.Overrides()))
	}
	byPath := map[string]File{}
	for _, f := range got.Files {
		byPath[f.Path] = f
	}
	for _, wf := range want.Files {
		gf, ok := byPath[wf.Path]
		if !ok {
			t.Fatalf("%s missing after round trip", wf.Path)
		}
		if gf.SHA1() != wf.SHA1() {
			t.Fatalf("%s sha1 = %q want %q", wf.Path, gf.SHA1(), wf.SHA1())
		}
		if gf.Hashes["sha512"] != wf.Hashes["sha512"] {
			t.Fatalf("%s sha512 not preserved", wf.Path)
		}
		if gf.FileSize != wf.FileSize {
			t.Fatalf("%s size = %d want %d", wf.Path, gf.FileSize, wf.FileSize)
		}
		if len(gf.Downloads) != len(wf.Downloads) {
			t.Fatalf("%s downloads = %d want %d", wf.Path, len(gf.Downloads), len(wf.Downloads))
		}
		if len(wf.Downloads) > 0 && gf.Downloads[0] != wf.Downloads[0] {
			t.Fatalf("%s download URL not preserved", wf.Path)
		}
		if gf.Env[EnvClient] != wf.Env[EnvClient] {
			t.Fatalf("%s env not preserved", wf.Path)
		}
	}
	// The override was hashed from the archive, so it carries a real digest.
	ov := byPath["overrides/config/sodium.json"]
	if ov.SHA1() != hashutil.SHA1Bytes([]byte(`{"quality":"fast"}`)) {
		t.Fatalf("override sha1 = %q", ov.SHA1())
	}
}

// findIndex returns a reader for the manifest inside an archive.
func findIndex(t *testing.T, zr *zip.Reader) (io.ReadCloser, error) {
	t.Helper()
	for _, f := range zr.File {
		if f.Name == IndexName {
			return f.Open()
		}
	}
	return nil, fmt.Errorf("%s missing from the archive", IndexName)
}

func TestExportFallsBackToOverrides(t *testing.T) {
	// A jar nobody has published: Modrinth answers 404 for its digest.
	const jarBody = "a locally built jar nobody can download"

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v2/version_file/") {
			http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	instPath := writeInstance(t, t.TempDir(), "pack-test")
	jarName := "my-private-mod.jar"
	if err := os.WriteFile(filepath.Join(instPath, "mods", jarName), []byte(jarBody), 0o644); err != nil {
		t.Fatalf("write jar: %v", err)
	}

	out := filepath.Join(t.TempDir(), "packs")
	res, err := Export(context.Background(), modrinth.New(modrinth.Options{BaseURL: srv.URL + "/v2"}),
		out, instPath, "26.3", "")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if res.Resolved != 0 || res.Overrides != 1 || res.Total != 1 {
		t.Fatalf("unexpected tally: %+v", res)
	}
	if filepath.Base(res.Path) != "pack-test-26.3.mrpack" {
		t.Fatalf("unexpected file name %q", res.Path)
	}

	pack, err := Load(res.Path)
	if err != nil {
		t.Fatalf("Load exported pack: %v", err)
	}
	if len(pack.Mods()) != 0 {
		t.Fatalf("an unresolvable mod must not be listed under mods/: %+v", pack.Mods())
	}
	ov := pack.Overrides()
	if len(ov) != 1 {
		t.Fatalf("expected 1 override, got %d", len(ov))
	}
	wantPath := OverridesPrefix + ModsPrefix + jarName
	if ov[0].Path != wantPath {
		t.Fatalf("override path = %q want %q", ov[0].Path, wantPath)
	}
	// The whole point: real digests, so an import can verify what it unpacks.
	if got, want := ov[0].SHA1(), hashutil.SHA1Bytes([]byte(jarBody)); got != want {
		t.Fatalf("override sha1 = %q want %q", got, want)
	}
	if got, want := ov[0].Hashes["sha512"], hashutil.SHA512Bytes([]byte(jarBody)); got != want {
		t.Fatalf("override sha512 = %q want %q", got, want)
	}
	if ov[0].FileSize != len(jarBody) {
		t.Fatalf("override size = %d", ov[0].FileSize)
	}
	// And the bytes really are in the archive, so the pack is complete.
	assertArchiveHas(t, res.Path, wantPath, jarBody)
}

// assertArchiveHas checks that an archive contains an exact entry.
func assertArchiveHas(t *testing.T, path, name, body string) {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		got, err := readZipEntry(f, 1<<20)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != body {
			t.Fatalf("%s body = %q want %q", name, got, body)
		}
		return
	}
	t.Fatalf("%s not present in %s", name, path)
}

func TestInstallSkipsAlreadyPresentFile(t *testing.T) {
	const jarBody = "the very same bytes"

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprint(w, jarBody)
	}))
	defer srv.Close()

	mr := modrinth.New(modrinth.Options{BaseURL: srv.URL})
	dir := t.TempDir()
	modsDir := filepath.Join(dir, "mods")
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Present under a different name, which is the case a name-based check
	// would get wrong: the digest is what identifies the mod.
	if err := os.WriteFile(filepath.Join(modsDir, "already-here.jar"), []byte(jarBody), 0o644); err != nil {
		t.Fatalf("write jar: %v", err)
	}

	packPath := filepath.Join(dir, "pack.mrpack")
	writeZip(t, packPath, map[string]string{IndexName: fmt.Sprintf(`{
	  "formatVersion": 1,
	  "game": "minecraft",
	  "versionId": "1.0.0",
	  "name": "skip test",
	  "files": [
	    {
	      "path": "mods/sodium.jar",
	      "hashes": {"sha1": %q},
	      "downloads": [%q],
	      "fileSize": %d
	    }
	  ],
	  "dependencies": {"minecraft": "26.3"}
	}`, hashutil.SHA1Bytes([]byte(jarBody)), srv.URL+"/sodium.jar", len(jarBody))})

	pack, err := Load(packPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	installed, skipped, err := Install(context.Background(), mr, pack, modsDir, dir, true)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed != 0 || skipped != 1 {
		t.Fatalf("installed=%d skipped=%d, want 0/1", installed, skipped)
	}
	if hits != 0 {
		t.Fatalf("a skipped mod must not be downloaded, got %d request(s)", hits)
	}
	if _, err := os.Stat(filepath.Join(modsDir, "sodium.jar")); !os.IsNotExist(err) {
		t.Fatalf("sodium.jar should not have been created")
	}
}

func TestInstallResolvesFallbackURL(t *testing.T) {
	const jarBody = "downloaded via the version fallback"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/versions":
			// The batch endpoint is what lets a whole pack resolve in one call.
			fmt.Fprintf(w, `[{
			  "id": "vvvv", "project_id": "AANobbMI", "version_number": "0.6.0",
			  "files": [{"hashes": {"sha1": %q}, "url": %q, "filename": "sodium.jar",
			             "primary": true, "size": %d}]
			}]`, hashutil.SHA1Bytes([]byte(jarBody)), srv.URL+"/files/sodium.jar", len(jarBody))
		case "/files/sodium.jar":
			fmt.Fprint(w, jarBody)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	mr := modrinth.New(modrinth.Options{BaseURL: srv.URL})
	dir := t.TempDir()
	modsDir := filepath.Join(dir, "mods")

	// No `downloads` at all: the only way to find the file is the recorded
	// version id, which is why Modpack keeps per-file dependencies in memory.
	pack := &Modpack{
		FormatVersion: FormatVersion,
		Game:          GameMinecraft,
		VersionID:     "1.0.0",
		Name:          "fallback",
		Files: []File{{
			Path:     "mods/sodium.jar",
			Hashes:   map[string]string{"sha1": hashutil.SHA1Bytes([]byte(jarBody))},
			FileSize: len(jarBody),
		}},
		Dependencies: map[string]Dependency{
			"minecraft":       {VersionID: "26.3", DependencyType: "minecraft"},
			"mods/sodium.jar": {ProjectID: "AANobbMI", VersionID: "vvvv"},
		},
	}

	installed, skipped, err := Install(context.Background(), mr, pack, modsDir, dir, false)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed != 1 || skipped != 0 {
		t.Fatalf("installed=%d skipped=%d, want 1/0", installed, skipped)
	}
	got, err := os.ReadFile(filepath.Join(modsDir, "sodium.jar"))
	if err != nil {
		t.Fatalf("read installed jar: %v", err)
	}
	if string(got) != jarBody {
		t.Fatalf("installed content = %q", got)
	}
}
