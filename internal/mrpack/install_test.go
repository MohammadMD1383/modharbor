package mrpack

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MohammadMD1383/modharbor/internal/hashutil"
	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
)

// Everything in this file writes into t.TempDir only, and isolateEnv points
// MODHARBOR_MINECRAFT_DIR and the XDG variables at a throwaway home as well.
// Install takes every path as an argument, so it cannot reach the developer's
// real instances on its own — but the day someone adds a fallback that reads
// the config, that fallback must not be a live game.

const (
	installJarBody = "PK\x03\x04 the real jar bytes\n"
	// A second fixture with the same shape but different bytes: every test that
	// needs "something that is not what the pack asked for" uses this, so no
	// case can accidentally pass on a digest collision.
	installOtherBody = "PK\x03\x04 different jar bytes\n"
)

// ─── isolation ───────────────────────────────────────────────────────────────

func isolateEnv(t *testing.T) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("MODHARBOR_MINECRAFT_DIR", filepath.Join(home, "mc"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
}

// ─── the jar server ──────────────────────────────────────────────────────────

// installStub serves pack payloads and counts every request. The count is the
// only way to prove a file was not downloaded: an install that fails still
// looks correct if you only look at the return value.
type installStub struct {
	*httptest.Server

	mu       sync.Mutex
	requests []string
	respond  func(w http.ResponseWriter, r *http.Request)
}

func newInstallStub(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) *installStub {
	t.Helper()

	s := &installStub{respond: respond}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *installStub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.URL.Path)
	respond := s.respond
	s.mu.Unlock()

	if respond == nil {
		http.NotFound(w, r)
		return
	}
	respond(w, r)
}

func (s *installStub) hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *installStub) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// bodyStub always answers with the same bytes.
func bodyStub(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }
}

// ─── assertions ──────────────────────────────────────────────────────────────

// assertNothingLeft fails unless modsDir is empty. A failed install that leaves
// a `.modharbor-dl-*` file behind is not a cosmetic problem: Minecraft scans
// mods/ for jars, and the next run starts by hashing whatever it finds there.
func assertNothingLeft(t *testing.T, modsDir string) {
	t.Helper()

	entries, err := os.ReadDir(modsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("read %s: %v", modsDir, err)
	}
	for _, e := range entries {
		size := 0
		if info, err := e.Info(); err == nil {
			size = int(info.Size())
		}
		t.Errorf("%s survived a failed install (%d bytes)", filepath.Join(modsDir, e.Name()), size)
	}
}

// assertAbsent fails if path exists at all. The size is reported because the
// interesting version of this failure is a zero-byte jar: it loads, and it
// then takes the whole game down with a NoClassDefFoundError.
func assertAbsent(t *testing.T, path string) {
	t.Helper()

	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	t.Fatalf("%s exists after a failed install", path)
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", filepath.Base(path), got, want)
	}
}

// ─── packs ───────────────────────────────────────────────────────────────────

// oneModPack describes a pack whose single jar is fetched from url and is
// expected to hash to sha1.
func oneModPack(name, url, sha1 string) *Modpack {
	return &Modpack{
		FormatVersion: FormatVersion,
		Game:          GameMinecraft,
		VersionID:     "1.0.0",
		Name:          "install test",
		Files: []File{{
			Path:      ModsPrefix + name,
			Hashes:    map[string]string{"sha1": sha1},
			Downloads: []string{url},
			FileSize:  len(installJarBody),
		}},
		Dependencies: map[string]Dependency{"minecraft": {VersionID: "26.3", DependencyType: "minecraft"}},
	}
}

// loadArchivePack writes a real .mrpack and loads it, because Install recovers
// override bytes by reopening the archive it came from — a pack built in memory
// cannot exercise that path at all. An empty manifest means "the archive has no
// files array", which is what the pure-override cases need.
func loadArchivePack(t *testing.T, manifest string, entries map[string]string) *Modpack {
	t.Helper()

	if manifest != "" {
		entries[IndexName] = manifest
	}
	path := filepath.Join(t.TempDir(), "pack.mrpack")
	writeZip(t, path, entries)

	pack, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return pack
}

// ─── checksum failures ───────────────────────────────────────────────────────

// TestInstallChecksumMismatchLeavesNothingBehind covers the case the temp-file
// contract exists for. A modrinth.index.json is a hand-editable file and a
// mirror can serve the wrong bytes, so the digest in the manifest is often the
// only thing standing between a user and a corrupt jar. If the mismatch were
// detected only after the rename, the user would find a broken mod in mods/ and
// a game that will not start.
func TestInstallChecksumMismatchLeavesNothingBehind(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "wrong bytes", body: installOtherBody},
		// A 200 with no body is the quietest corruption there is: no error
		// from the server, and a jar of length zero at the destination if the
		// digest check is skipped or runs on the wrong value.
		{name: "empty body", body: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateEnv(t)
			s := newInstallStub(t, bodyStub(tc.body))
			dir := t.TempDir()
			modsDir := filepath.Join(dir, "mods")

			// The manifest asks for the digest of installJarBody, which is not
			// what the server sends.
			pack := oneModPack("sodium.jar", s.URL+"/sodium.jar", hashutil.SHA1Bytes([]byte(installJarBody)))

			installed, skipped, err := Install(context.Background(), nil, pack, modsDir, dir, false)
			if err == nil {
				t.Fatal("a digest mismatch must not be reported as a success")
			}
			if !strings.Contains(err.Error(), "checksum mismatch") {
				t.Errorf("error %q does not explain the mismatch", err)
			}
			// The message has to name the file: a pack holds dozens, and the
			// user cannot tell which one is now missing from their mods.
			if !strings.Contains(err.Error(), "sodium.jar") {
				t.Errorf("error %q does not name the offending jar", err)
			}
			if installed != 0 || skipped != 0 {
				t.Errorf("installed=%d skipped=%d, want 0/0", installed, skipped)
			}
			assertAbsent(t, filepath.Join(modsDir, "sodium.jar"))
			assertNothingLeft(t, modsDir)
		})
	}
}

// TestInstallReportsWhatItActuallyHashed pins the message content: "expected X,
// got Y" is the difference between a corrupt pack the user must edit and a
// mirror serving bad bytes.
func TestInstallReportsWhatItActuallyHashed(t *testing.T) {
	isolateEnv(t)
	s := newInstallStub(t, bodyStub(installJarBody))
	dir := t.TempDir()

	want := hashutil.SHA1Bytes([]byte(installJarBody))
	pack := oneModPack("sodium.jar", s.URL+"/sodium.jar", hashutil.SHA1Bytes([]byte(installOtherBody)))

	_, _, err := Install(context.Background(), nil, pack, filepath.Join(dir, "mods"), dir, false)
	if err == nil {
		t.Fatal("expected a mismatch")
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not quote the digest that was expected", err)
	}
	if !strings.Contains(err.Error(), hashutil.SHA1Bytes([]byte(installOtherBody))) {
		t.Errorf("error %q does not quote the digest that arrived", err)
	}
}

// TestInstallAcceptsADigestInAnyCase covers the digests real manifests carry.
// Modrinth writes them lower-case, but a hand-edited pack or a mirror of one
// may not, and rejecting an otherwise perfect download over case is a failure
// users report as "modharbor is broken".
func TestInstallAcceptsADigestInAnyCase(t *testing.T) {
	isolateEnv(t)
	s := newInstallStub(t, bodyStub(installJarBody))
	dir := t.TempDir()
	modsDir := filepath.Join(dir, "mods")

	upper := strings.ToUpper(hashutil.SHA1Bytes([]byte(installJarBody)))
	pack := oneModPack("sodium.jar", s.URL+"/sodium.jar", "sha1:"+upper)

	installed, skipped, err := Install(context.Background(), nil, pack, modsDir, dir, false)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed != 1 || skipped != 0 {
		t.Fatalf("installed=%d skipped=%d, want 1/0", installed, skipped)
	}
	assertContent(t, filepath.Join(modsDir, "sodium.jar"), installJarBody)
}

// ─── truncated transfers ─────────────────────────────────────────────────────

// TestInstallTruncatedDownloadLeavesNothingBehind covers the failure that the
// rename contract was written for: the connection dies mid-body. The bytes
// received so far are a valid prefix, and the digest catches it — but only if
// the file is still a temporary one when the digest is checked.
func TestInstallTruncatedDownloadLeavesNothingBehind(t *testing.T) {
	tests := []struct {
		name    string
		respond func(http.ResponseWriter, *http.Request)
	}{
		{
			// Content-Length lies about a body that never arrives in full.
			name: "content-length lies",
			respond: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "4096")
				fmt.Fprint(w, installJarBody)
			},
		},
		{
			// The connection is dropped with no declared length at all, which
			// is what a proxy under load or a mobile network does.
			name: "connection closed mid-body",
			respond: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, installJarBody)
				if hj, ok := w.(http.Hijacker); ok {
					conn, _, err := hj.Hijack()
					if err == nil {
						conn.Close()
					}
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateEnv(t)
			s := newInstallStub(t, tc.respond)
			dir := t.TempDir()
			modsDir := filepath.Join(dir, "mods")

			pack := oneModPack("sodium.jar", s.URL+"/sodium.jar", hashutil.SHA1Bytes([]byte(installJarBody)))

			installed, _, err := Install(context.Background(), nil, pack, modsDir, dir, false)
			if err == nil {
				t.Fatal("a truncated download must not be reported as a success")
			}
			if !strings.Contains(err.Error(), "sodium.jar") {
				t.Errorf("error %q does not name the jar", err)
			}
			if installed != 0 {
				t.Errorf("installed = %d, want 0", installed)
			}
			assertAbsent(t, filepath.Join(modsDir, "sodium.jar"))
			// A half-written jar is the exact outcome this whole design
			// refuses, so the directory has to be empty — no truncated jar and
			// no abandoned temporary file.
			assertNothingLeft(t, modsDir)
		})
	}
}

// TestInstallRefusesAnErrorStatus covers a server that fails the request
// outright. The same rule applies as for a corrupt body: the destination must
// not appear, because a zero-byte jar is worse than a missing one.
func TestInstallRefusesAnErrorStatus(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			isolateEnv(t)
			s := newInstallStub(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			})
			dir := t.TempDir()
			modsDir := filepath.Join(dir, "mods")

			pack := oneModPack("sodium.jar", s.URL+"/sodium.jar", hashutil.SHA1Bytes([]byte(installJarBody)))

			installed, _, err := Install(context.Background(), nil, pack, modsDir, dir, false)
			if err == nil {
				t.Fatalf("status %d was reported as a success", status)
			}
			if !strings.Contains(err.Error(), http.StatusText(status)) {
				t.Errorf("error %q does not name the status", err)
			}
			if !strings.Contains(err.Error(), "sodium.jar") {
				t.Errorf("error %q does not name the jar", err)
			}
			if installed != 0 {
				t.Errorf("installed = %d, want 0", installed)
			}
			assertAbsent(t, filepath.Join(modsDir, "sodium.jar"))
			assertNothingLeft(t, modsDir)
		})
	}
}

// TestInstallFailsWhenTheMirrorIsUnreachable covers the most common real
// failure: the host in a pack's download list is gone or refusing connections.
// It has to be reported as an error against the mod that needed it, and the
// destination must stay empty rather than hold the empty file that a failed
// io.Copy would otherwise leave behind.
func TestInstallFailsWhenTheMirrorIsUnreachable(t *testing.T) {
	isolateEnv(t)
	s := newInstallStub(t, bodyStub(installJarBody))
	dead := s.URL
	s.Close()

	dir := t.TempDir()
	modsDir := filepath.Join(dir, "mods")
	pack := oneModPack("sodium.jar", dead+"/sodium.jar", hashutil.SHA1Bytes([]byte(installJarBody)))

	installed, _, err := Install(context.Background(), nil, pack, modsDir, dir, false)
	if err == nil {
		t.Fatal("an unreachable mirror was reported as a success")
	}
	if !strings.Contains(err.Error(), "sodium.jar") {
		t.Errorf("error %q does not name the jar", err)
	}
	if installed != 0 {
		t.Errorf("installed = %d, want 0", installed)
	}
	assertAbsent(t, filepath.Join(modsDir, "sodium.jar"))
	assertNothingLeft(t, modsDir)
}

// TestInstallRejectsAnUnusableDownloadURL covers a hand-edited pack whose URL
// is not a URL at all. The manifest is a text file, so this is a user error
// waiting to happen and it must read as one.
func TestInstallRejectsAnUnusableDownloadURL(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	modsDir := filepath.Join(dir, "mods")
	pack := oneModPack("sodium.jar", "://cdn.example.invalid/sodium.jar",
		hashutil.SHA1Bytes([]byte(installJarBody)))

	_, _, err := Install(context.Background(), nil, pack, modsDir, dir, false)
	if err == nil {
		t.Fatal("an unparseable download URL was installed")
	}
	if !strings.Contains(err.Error(), "sodium.jar") {
		t.Errorf("error %q does not name the jar", err)
	}
	assertAbsent(t, filepath.Join(modsDir, "sodium.jar"))
	assertNothingLeft(t, modsDir)
}

// TestInstallReportsAVersionItCannotFetch covers the fallback path: a pack with
// no `downloads` at all, which is the normal shape of a pack exported from an
// instance whose mirror has since disappeared. The version id is then the only
// lead, and a version Modrinth no longer publishes leaves nothing to install —
// which the user has to be told, by version id, not by a bare failure.
func TestInstallReportsAVersionItCannotFetch(t *testing.T) {
	isolateEnv(t)
	s := newInstallStub(t, func(w http.ResponseWriter, r *http.Request) {
		// Both the batch call and the per-version retry fail, which is what a
		// deleted version looks like.
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"not_found"}`)
	})
	mr := modrinth.New(modrinth.Options{BaseURL: s.URL, HTTP: s.Client()})
	dir := t.TempDir()
	modsDir := filepath.Join(dir, "mods")

	pack := &Modpack{
		FormatVersion: FormatVersion,
		Game:          GameMinecraft,
		VersionID:     "1.0.0",
		Name:          "missing version",
		Files: []File{{
			Path:   ModsPrefix + "sodium.jar",
			Hashes: map[string]string{"sha1": hashutil.SHA1Bytes([]byte(installJarBody))},
		}},
		Dependencies: map[string]Dependency{
			"minecraft":               {VersionID: "26.3", DependencyType: "minecraft"},
			ModsPrefix + "sodium.jar": {ProjectID: "AANobbMI", VersionID: "vvvv"},
		},
	}

	installed, _, err := Install(context.Background(), mr, pack, modsDir, dir, false)
	if err == nil {
		t.Fatal("a pack naming a version that does not exist was installed")
	}
	// The version id is the only thing in this message. A pack holds dozens of
	// entries, so the user is left to guess which mod this was — reported as a
	// finding rather than pinned here, so that wrapping this with the file name
	// does not have to fight the test.
	if !strings.Contains(err.Error(), "vvvv") {
		t.Errorf("error %q does not name the version that could not be fetched", err)
	}
	if installed != 0 {
		t.Errorf("installed = %d, want 0", installed)
	}
	assertAbsent(t, filepath.Join(modsDir, "sodium.jar"))
	assertNothingLeft(t, modsDir)
}

// TestInstallReportsAVersionWithNoDownloadableFile covers the version that
// exists but publishes nothing usable. Both replies are things Modrinth does
// answer with in practice — a version whose file was withdrawn, and one whose
// file carries no URL at all — and neither may leave a file behind.
func TestInstallReportsAVersionWithNoDownloadableFile(t *testing.T) {
	tests := []struct {
		name  string
		files string
		// wantText is the fragment the error must carry, where there is one to
		// require: only the "no downloadable file" branch names the version.
		wantText string
	}{
		{
			name:     "no files at all",
			files:    `[]`,
			wantText: "vvvv",
		},
		{
			name: "a file with no url",
			files: `[{"hashes": {"sha1": "aa"}, "url": "", "filename": "sodium.jar",
			           "primary": true, "size": 10}]`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateEnv(t)
			s := newInstallStub(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `[{"id": "vvvv", "project_id": "AANobbMI", "files": %s}]`, tc.files)
			})
			mr := modrinth.New(modrinth.Options{BaseURL: s.URL, HTTP: s.Client()})
			dir := t.TempDir()
			modsDir := filepath.Join(dir, "mods")

			pack := &Modpack{
				FormatVersion: FormatVersion,
				Game:          GameMinecraft,
				VersionID:     "1.0.0",
				Name:          "unusable version",
				Files: []File{{
					Path:   ModsPrefix + "sodium.jar",
					Hashes: map[string]string{"sha1": hashutil.SHA1Bytes([]byte(installJarBody))},
				}},
				Dependencies: map[string]Dependency{
					ModsPrefix + "sodium.jar": {ProjectID: "AANobbMI", VersionID: "vvvv"},
				},
			}

			installed, _, err := Install(context.Background(), mr, pack, modsDir, dir, false)
			if err == nil {
				t.Fatalf("an unusable version was installed (%d)", installed)
			}
			if tc.wantText != "" && !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error %q does not name the version", err)
			}
			if installed != 0 {
				t.Errorf("installed = %d, want 0", installed)
			}
			assertAbsent(t, filepath.Join(modsDir, "sodium.jar"))
			assertNothingLeft(t, modsDir)
		})
	}
}

// ─── dedup and skipping ──────────────────────────────────────────────────────

// TestInstallDownloadsADuplicateOnlyOnce pins the `present` map. A pack can
// legitimately list one jar twice, and a 50 MB mod downloaded twice costs the
// user minutes and their bandwidth; worse, the second copy can land under a
// different name and the loader then loads the same mod two times, which is a
// hard crash.
func TestInstallDownloadsADuplicateOnlyOnce(t *testing.T) {
	tests := []struct {
		name string
		// paths are the manifest entries; names is what should end up on disk.
		paths []string
		want  map[string]string
	}{
		{
			name:  "the same entry twice",
			paths: []string{ModsPrefix + "sodium.jar", ModsPrefix + "sodium.jar"},
			want:  map[string]string{"sodium.jar": installJarBody},
		},
		{
			// Two entries, one jar. Deduplication is keyed on the digest, not
			// the path, so only the first name lands on disk — which is the
			// point: two copies of one mod break the loader.
			name:  "one jar under two names",
			paths: []string{ModsPrefix + "sodium.jar", ModsPrefix + "nested/sodium-copy.jar"},
			want:  map[string]string{"sodium.jar": installJarBody},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateEnv(t)
			s := newInstallStub(t, bodyStub(installJarBody))
			dir := t.TempDir()
			modsDir := filepath.Join(dir, "mods")

			digest := hashutil.SHA1Bytes([]byte(installJarBody))
			files := make([]File, 0, len(tc.paths))
			for _, p := range tc.paths {
				files = append(files, File{
					Path:      p,
					Hashes:    map[string]string{"sha1": digest},
					Downloads: []string{s.URL + "/sodium.jar"},
					FileSize:  len(installJarBody),
				})
			}
			pack := &Modpack{
				FormatVersion: FormatVersion,
				Game:          GameMinecraft,
				VersionID:     "1.0.0",
				Name:          "duplicate test",
				Files:         files,
			}

			installed, skipped, err := Install(context.Background(), nil, pack, modsDir, dir, false)
			if err != nil {
				t.Fatalf("Install: %v", err)
			}
			if got := s.hits(); got != 1 {
				t.Fatalf("the jar was downloaded %d time(s), want 1", got)
			}
			if installed != 1 || skipped != 1 {
				t.Fatalf("installed=%d skipped=%d, want 1/1", installed, skipped)
			}
			for name, body := range tc.want {
				assertContent(t, filepath.Join(modsDir, name), body)
			}
			assertNothingLeft(t, filepath.Join(modsDir, "nested"))
		})
	}
}

// TestInstallReplacesAStaleJar is the other half of the skip rule: a file on
// disk whose digest does not match is not "already present". Getting this
// backwards leaves the old mod in place forever and tells the user their
// upgrade succeeded.
func TestInstallReplacesAStaleJar(t *testing.T) {
	isolateEnv(t)
	s := newInstallStub(t, bodyStub(installJarBody))
	dir := t.TempDir()
	modsDir := filepath.Join(dir, "mods")
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale := filepath.Join(modsDir, "sodium.jar")
	if err := os.WriteFile(stale, []byte(installOtherBody), 0o644); err != nil {
		t.Fatalf("write stale jar: %v", err)
	}

	pack := oneModPack("sodium.jar", s.URL+"/sodium.jar", hashutil.SHA1Bytes([]byte(installJarBody)))

	installed, skipped, err := Install(context.Background(), nil, pack, modsDir, dir, false)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed != 1 || skipped != 0 {
		t.Fatalf("installed=%d skipped=%d, want 1/0", installed, skipped)
	}
	if got := s.hits(); got != 1 {
		t.Fatalf("a stale jar was resolved in %d request(s), want 1", got)
	}
	assertContent(t, stale, installJarBody)

	// Immediately afterwards the digest matches, so a second run must not
	// spend another request. This is the cheap re-import that makes --yes safe.
	installed, skipped, err = Install(context.Background(), nil, pack, modsDir, dir, false)
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if installed != 0 || skipped != 1 {
		t.Fatalf("installed=%d skipped=%d, want 0/1", installed, skipped)
	}
	if got := s.hits(); got != 1 {
		t.Fatalf("the second install downloaded the jar again: %d request(s)", got)
	}
}

// TestInstallAbandonsTheRestOfThePackAfterAFailure covers the partial-success
// case, which is where a naive implementation leaves an instance in a state the
// user cannot reason about: some mods updated, one rejected, and no way to tell
// which. It also proves the pre-existing jar is left exactly as it was.
func TestInstallAbandonsTheRestOfThePackAfterAFailure(t *testing.T) {
	isolateEnv(t)
	s := newInstallStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/broken.jar" {
			fmt.Fprint(w, installOtherBody)
			return
		}
		fmt.Fprint(w, installJarBody)
	})
	dir := t.TempDir()
	modsDir := filepath.Join(dir, "mods")
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	untouched := filepath.Join(modsDir, "unrelated.jar")
	if err := os.WriteFile(untouched, []byte(installOtherBody), 0o644); err != nil {
		t.Fatalf("write unrelated jar: %v", err)
	}

	goodDigest := hashutil.SHA1Bytes([]byte(installJarBody))
	pack := &Modpack{
		FormatVersion: FormatVersion,
		Game:          GameMinecraft,
		VersionID:     "1.0.0",
		Name:          "partial failure",
		Files: []File{
			{
				// The pack expects one jar and the server sends another: the
				// mirror is serving the wrong build.
				Path:      ModsPrefix + "broken.jar",
				Hashes:    map[string]string{"sha1": hashutil.SHA1Bytes([]byte("the build the pack asked for"))},
				Downloads: []string{s.URL + "/broken.jar"},
			},
			{
				Path:      ModsPrefix + "sodium.jar",
				Hashes:    map[string]string{"sha1": goodDigest},
				Downloads: []string{s.URL + "/sodium.jar"},
			},
		},
	}

	installed, skipped, err := Install(context.Background(), nil, pack, modsDir, dir, false)
	if err == nil {
		t.Fatal("a bad jar must stop the import")
	}
	if installed != 0 || skipped != 0 {
		t.Fatalf("installed=%d skipped=%d, want 0/0", installed, skipped)
	}
	if got := s.paths(); len(got) != 1 || got[0] != "/broken.jar" {
		t.Fatalf("requests = %v, want the import to stop at the failure", got)
	}
	assertAbsent(t, filepath.Join(modsDir, "broken.jar"))
	assertAbsent(t, filepath.Join(modsDir, "sodium.jar"))
	assertContent(t, untouched, installOtherBody)
}

// ─── install-time refusals ───────────────────────────────────────────────────

// TestInstallRefusesAnUnusablePack covers the arguments a CLI bug or a bad
// invocation can produce. Each of these must fail before anything is written,
// and the "file where a directory belongs" case must not destroy what it finds.
func TestInstallRefusesAnUnusablePack(t *testing.T) {
	t.Run("no pack at all", func(t *testing.T) {
		isolateEnv(t)
		dir := t.TempDir()
		if _, _, err := Install(context.Background(), nil, nil, filepath.Join(dir, "mods"), dir, false); err == nil {
			t.Fatal("a nil pack was installed")
		} else if !strings.Contains(err.Error(), "no modpack") {
			t.Fatalf("error %q does not explain itself", err)
		}
	})

	t.Run("the mods path is a file", func(t *testing.T) {
		isolateEnv(t)
		dir := t.TempDir()
		modsDir := filepath.Join(dir, "mods")
		if err := os.WriteFile(modsDir, []byte("not a directory"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		pack := oneModPack("sodium.jar", "https://example.invalid/sodium.jar",
			hashutil.SHA1Bytes([]byte(installJarBody)))

		_, _, err := Install(context.Background(), nil, pack, modsDir, dir, false)
		if err == nil {
			t.Fatal("installing into a file was allowed")
		}
		if !strings.Contains(err.Error(), modsDir) {
			t.Errorf("error %q does not name the path that failed", err)
		}
		// Whatever was there must survive: install never deletes.
		assertContent(t, modsDir, "not a directory")
	})

	t.Run("an entry with no way to fetch it", func(t *testing.T) {
		// Two shapes of the same dead end, and the user needs to be told which
		// one they hit: modharbor has no Modrinth configured at all, or the
		// pack records no version for this file.
		tests := []struct {
			name    string
			client  *modrinth.Client
			wantErr string
		}{
			{name: "no client", client: nil, wantErr: "no Modrinth client"},
			{name: "no version recorded", client: modrinth.New(modrinth.Options{BaseURL: "https://example.invalid/v2"}), wantErr: "no version id recorded"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				isolateEnv(t)
				s := newInstallStub(t, bodyStub(installJarBody))
				dir := t.TempDir()
				modsDir := filepath.Join(dir, "mods")

				pack := &Modpack{
					FormatVersion: FormatVersion,
					Game:          GameMinecraft,
					VersionID:     "1.0.0",
					Name:          "no url",
					Files: []File{{
						Path:   ModsPrefix + "sodium.jar",
						Hashes: map[string]string{"sha1": hashutil.SHA1Bytes([]byte(installJarBody))},
					}},
					Dependencies: map[string]Dependency{"minecraft": {VersionID: "26.3", DependencyType: "minecraft"}},
				}

				_, _, err := Install(context.Background(), tc.client, pack, modsDir, dir, false)
				if err == nil {
					t.Fatal("an entry with no download URL was installed")
				}
				if !strings.Contains(err.Error(), "no download URL") {
					t.Errorf("error %q does not say what is missing", err)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not explain why: want %q", err, tc.wantErr)
				}
				if n := s.hits(); n != 0 {
					t.Fatalf("%d request(s) were made for an entry that cannot be fetched", n)
				}
				assertNothingLeft(t, modsDir)
			})
		}
	})
}

// TestInstallIgnoresArchiveEntriesThePackDoesNotOwn covers an archive that
// carries more than its overrides. Every real pack has a README and sometimes
// an icon at the root; those belong in the instance root only if the manifest
// asks for them, and copying them into mods/ or config/ would be a surprise.
func TestInstallIgnoresArchiveEntriesThePackDoesNotOwn(t *testing.T) {
	isolateEnv(t)
	const cfgBody = `{"quality":"fast"}`
	pack := loadArchivePack(t, "", map[string]string{
		IndexName: `{
		  "formatVersion": 1,
		  "game": "minecraft",
		  "versionId": "1.0.0",
		  "name": "extra entries",
		  "files": [],
		  "dependencies": {"minecraft": "26.3"}
		}`,
		OverridesPrefix + "config/sodium.json": cfgBody,
		// A directory entry, which every zip writer emits for overrides/.
		OverridesPrefix:          "",
		"README.md":              "install this pack with Fabric",
		OverridesPrefix + "icon": "not a real png",
	})

	instance := t.TempDir()
	if _, _, err := Install(context.Background(), nil, pack,
		filepath.Join(instance, "mods"), instance, true); err != nil {
		t.Fatalf("Install: %v", err)
	}
	// Everything under overrides/ is part of the pack, so it all lands; only
	// what sits outside that tree is nobody's business.
	assertContent(t, filepath.Join(instance, "config", "sodium.json"), cfgBody)
	assertContent(t, filepath.Join(instance, "icon"), "not a real png")
	assertAbsent(t, filepath.Join(instance, "README.md"))
	assertAbsent(t, filepath.Join(instance, "mods", "icon"))
}

// TestInstallReportsAMissingArchive covers the pack that was moved or deleted
// between being loaded and being installed — an import from a USB stick, or a
// cleanup running concurrently. The override bytes live in the archive, so this
// is a failure the user has to be told about rather than an import that quietly
// installs fewer files than the manifest promises.
func TestInstallReportsAMissingArchive(t *testing.T) {
	isolateEnv(t)
	pack := loadArchivePack(t, "", map[string]string{
		IndexName: `{
		  "formatVersion": 1,
		  "game": "minecraft",
		  "versionId": "1.0.0",
		  "name": "vanishing pack",
		  "files": [],
		  "dependencies": {"minecraft": "26.3"}
		}`,
		OverridesPrefix + "config/sodium.json": `{"quality":"fast"}`,
	})
	if err := os.Remove(pack.source); err != nil {
		t.Fatalf("remove archive: %v", err)
	}

	instance := t.TempDir()
	installed, _, err := Install(context.Background(), nil, pack,
		filepath.Join(instance, "mods"), instance, true)
	if err == nil {
		t.Fatal("overrides were reported as installed with no archive to read")
	}
	if !strings.Contains(err.Error(), pack.source) {
		t.Errorf("error %q does not name the archive it could not reopen", err)
	}
	if installed != 0 {
		t.Errorf("installed = %d, want 0", installed)
	}
	assertAbsent(t, filepath.Join(instance, "config", "sodium.json"))
}

// TestInstallKeepsGoingPastAnUnreadableJar covers a mods directory containing a
// file install cannot hash — a jar another process is writing, or one whose
// permissions a backup tool changed. One unreadable file must not abort an
// import that could otherwise complete.
func TestInstallKeepsGoingPastAnUnreadableJar(t *testing.T) {
	isolateEnv(t)
	s := newInstallStub(t, bodyStub(installJarBody))
	dir := t.TempDir()
	modsDir := filepath.Join(dir, "mods")
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	locked := filepath.Join(modsDir, "locked.jar")
	if err := os.WriteFile(locked, []byte(installJarBody), 0o644); err != nil {
		t.Fatalf("write locked jar: %v", err)
	}
	// Read permission is what install actually needs; the file stays on disk.
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })

	// An already-present jar under a different name, so the skip decision has
	// something to work with even though one entry could not be hashed.
	present := filepath.Join(modsDir, "already-here.jar")
	if err := os.WriteFile(present, []byte(installOtherBody), 0o644); err != nil {
		t.Fatalf("write present jar: %v", err)
	}

	// The two entries are chosen so both decisions are visible: sodium is
	// already on disk under another name, and lithium carries the digest of the
	// jar that could not be hashed — which is the case that proves an
	// unreadable file is left out of the map instead of poisoning it.
	pack := &Modpack{
		FormatVersion: FormatVersion,
		Game:          GameMinecraft,
		VersionID:     "1.0.0",
		Name:          "unreadable jar present",
		Files: []File{
			{
				Path:      ModsPrefix + "sodium.jar",
				Hashes:    map[string]string{"sha1": hashutil.SHA1Bytes([]byte(installOtherBody))},
				Downloads: []string{s.URL + "/sodium.jar"},
			},
			{
				Path:      ModsPrefix + "lithium.jar",
				Hashes:    map[string]string{"sha1": hashutil.SHA1Bytes([]byte(installJarBody))},
				Downloads: []string{s.URL + "/lithium.jar"},
			},
		},
	}

	installed, skipped, err := Install(context.Background(), nil, pack, modsDir, dir, false)
	if err != nil {
		t.Fatalf("an unreadable jar stopped the import: %v", err)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the jar whose digest was already on disk)", skipped)
	}
	if installed != 1 {
		t.Errorf("installed = %d, want 1: an unhashable jar must not register its digest", installed)
	}
	if got := s.hits(); got != 1 {
		t.Fatalf("%d request(s), want 1", got)
	}
	assertContent(t, filepath.Join(modsDir, "lithium.jar"), installJarBody)
	assertAbsent(t, filepath.Join(modsDir, "sodium.jar"))
	// The unreadable jar itself is left exactly as it was found.
	info, err := os.Stat(locked)
	if err != nil {
		t.Fatalf("stat the locked jar: %v", err)
	}
	if info.Size() != int64(len(installJarBody)) {
		t.Fatalf("the locked jar changed size: %d", info.Size())
	}
}

// ─── overrides ───────────────────────────────────────────────────────────────

// TestInstallOverrideChecksumMismatchLeavesNothingBehind covers a pack whose
// manifest disagrees with its own archive. Load keeps the manifest's digest
// when both are present, so a mismatch here is how a repacked or tampered pack
// is caught. The override is a config file: writing a bad one silently changes
// how the game behaves, and the user has no way to tell it was rewritten.
func TestInstallOverrideChecksumMismatchLeavesNothingBehind(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	instance := filepath.Join(dir, "instance")
	if err := os.MkdirAll(instance, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	const cfgPath = OverridesPrefix + "config/sodium.json"
	pack := loadArchivePack(t, fmt.Sprintf(`{
	  "formatVersion": 1,
	  "game": "minecraft",
	  "versionId": "1.0.0",
	  "name": "bad override",
	  "files": [{"path": %q, "hashes": {"sha1": %q}, "fileSize": 1}],
	  "dependencies": {"minecraft": "26.3"}
	}`, cfgPath, hashutil.SHA1Bytes([]byte(installOtherBody))),
		map[string]string{cfgPath: `{"quality":"fast"}`})

	installed, _, err := Install(context.Background(), nil, pack,
		filepath.Join(instance, "mods"), instance, true)
	if err == nil {
		t.Fatal("an override whose digest disagrees with the archive was written")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error %q does not explain the mismatch", err)
	}
	if !strings.Contains(err.Error(), cfgPath) {
		t.Errorf("error %q does not name the file", err)
	}
	if installed != 0 {
		t.Errorf("installed = %d, want 0", installed)
	}
	assertAbsent(t, filepath.Join(instance, "config", "sodium.json"))
	assertNothingLeft(t, filepath.Join(instance, "config"))
}

// TestInstallOverrideNeedsSomewhereToGo covers the two refusals the override
// path can make, both of which otherwise end as a silent no-op that looks like
// a successful import.
func TestInstallOverrideNeedsSomewhereToGo(t *testing.T) {
	t.Run("no destination directory", func(t *testing.T) {
		isolateEnv(t)
		pack := loadArchivePack(t, `{
		  "formatVersion": 1,
		  "game": "minecraft",
		  "versionId": "1.0.0",
		  "name": "overrides",
		  "files": [],
		  "dependencies": {"minecraft": "26.3"}
		}`, map[string]string{OverridesPrefix + "config/sodium.json": `{"quality":"fast"}`})

		_, _, err := Install(context.Background(), nil, pack, filepath.Join(t.TempDir(), "mods"), "", true)
		if err == nil {
			t.Fatal("overrides were silently dropped")
		}
		if !strings.Contains(err.Error(), "override") {
			t.Errorf("error %q does not mention overrides", err)
		}
	})

	t.Run("no archive to read them from", func(t *testing.T) {
		isolateEnv(t)
		// A pack built in memory carries the description of its overrides but
		// not their bytes, so there is nothing to install.
		pack := &Modpack{
			FormatVersion: FormatVersion,
			Game:          GameMinecraft,
			VersionID:     "1.0.0",
			Name:          "in memory",
			Files: []File{{
				Path:   OverridesPrefix + "config/sodium.json",
				Hashes: map[string]string{"sha1": hashutil.SHA1Bytes([]byte(`{"quality":"fast"}`))},
			}},
		}
		instance := t.TempDir()

		_, _, err := Install(context.Background(), nil, pack,
			filepath.Join(instance, "mods"), instance, true)
		if err == nil {
			t.Fatal("overrides were silently dropped")
		}
		if !strings.Contains(err.Error(), "archive") {
			t.Errorf("error %q does not explain where the bytes should have come from", err)
		}
		assertAbsent(t, filepath.Join(instance, "config", "sodium.json"))
	})
}

// TestInstallWritesOverridesOnce covers the successful half of the same path,
// which the failure cases above depend on being reachable: an override lands at
// the right place with the right bytes, and a second import leaves it alone.
// Rewriting a user's config on every import would lose their edits.
func TestInstallWritesOverridesOnce(t *testing.T) {
	isolateEnv(t)
	const cfgBody = `{"quality":"fast"}`
	const cfgPath = OverridesPrefix + "config/sodium.json"
	pack := loadArchivePack(t, "", map[string]string{
		IndexName: fmt.Sprintf(`{
		  "formatVersion": 1,
		  "game": "minecraft",
		  "versionId": "1.0.0",
		  "name": "overrides",
		  "files": [],
		  "dependencies": {"minecraft": "26.3"}
		}`),
		cfgPath: cfgBody,
	})

	instance := t.TempDir()
	modsDir := filepath.Join(instance, "mods")

	installed, _, err := Install(context.Background(), nil, pack, modsDir, instance, true)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed != 1 {
		t.Fatalf("installed = %d, want 1 override", installed)
	}
	target := filepath.Join(instance, "config", "sodium.json")
	assertContent(t, target, cfgBody)

	// A hand-edited file whose digest no longer matches is replaced: the pack
	// is the source of truth, and silently keeping the edit would make an
	// import mean different things on different machines.
	if err := os.WriteFile(target, []byte(`{"quality":"fancy"}`), 0o644); err != nil {
		t.Fatalf("edit config: %v", err)
	}
	installed, _, err = Install(context.Background(), nil, pack, modsDir, instance, true)
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if installed != 1 {
		t.Fatalf("installed = %d, want the edited file to be replaced", installed)
	}
	assertContent(t, target, cfgBody)

	// Untouched, the same file is left completely alone.
	installed, _, err = Install(context.Background(), nil, pack, modsDir, instance, true)
	if err != nil {
		t.Fatalf("third Install: %v", err)
	}
	if installed != 0 {
		t.Fatalf("installed = %d, want the unchanged file to be kept", installed)
	}
	assertContent(t, target, cfgBody)
}

// TestInstallDoesNotRewriteOverridesWhenAskedNotTo covers the flag itself: a
// caller that passes includeOverrides=false gets the mods and nothing else,
// even though the archive is sitting right there ready to be unpacked.
func TestInstallDoesNotRewriteOverridesWhenAskedNotTo(t *testing.T) {
	isolateEnv(t)
	pack := loadArchivePack(t, "", map[string]string{
		IndexName: `{
		  "formatVersion": 1,
		  "game": "minecraft",
		  "versionId": "1.0.0",
		  "name": "overrides",
		  "files": [],
		  "dependencies": {"minecraft": "26.3"}
		}`,
		OverridesPrefix + "config/sodium.json": `{"quality":"fast"}`,
	})

	instance := t.TempDir()
	installed, _, err := Install(context.Background(), nil, pack,
		filepath.Join(instance, "mods"), instance, false)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed != 0 {
		t.Fatalf("installed = %d, want nothing", installed)
	}
	if _, err := os.Stat(filepath.Join(instance, "config")); !os.IsNotExist(err) {
		t.Fatal("overrides were written although the caller opted out")
	}
}
