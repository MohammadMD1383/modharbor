package mrpack

import (
	"context"
	"net/http"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MohammadMD1383/modharbor/internal/hashutil"
)

// recordingProgress is a Progress that remembers what it was told, so tests
// can assert a download was tracked without drawing anything.
type recordingProgress struct {
	mu    sync.Mutex
	added int64
	done  bool
}

func (p *recordingProgress) Add(n int64) {
	p.mu.Lock()
	p.added += n
	p.mu.Unlock()
}

func (p *recordingProgress) Done() {
	p.mu.Lock()
	p.done = true
	p.mu.Unlock()
}

type progressCall struct {
	name  string
	total int64
	bar   *recordingProgress
}

// recordProgress returns a ProgressFunc logging every tracker it opens.
func recordProgress(calls *[]progressCall, mu *sync.Mutex) ProgressFunc {
	return func(name string, total int64) Progress {
		bar := &recordingProgress{}
		mu.Lock()
		*calls = append(*calls, progressCall{name: name, total: total, bar: bar})
		mu.Unlock()
		return bar
	}
}

// TestInstallWithProgressTracksEachDownload covers the reason the hook exists:
// an import that pulls several jars must show each one. A tracker that never
// advances, or one left open on failure, is a bar stuck on screen.
func TestInstallWithProgressTracksEachDownload(t *testing.T) {
	isolateEnv(t)
	const otherBody = installJarBody + "lithium differs\n"
	s := newInstallStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/lithium.jar" {
			_, _ = w.Write([]byte(otherBody))
			return
		}
		_, _ = w.Write([]byte(installJarBody))
	})
	dir := t.TempDir()
	modsDir := filepath.Join(dir, "mods")

	sodiumDigest := hashutil.SHA1Bytes([]byte(installJarBody))
	lithiumDigest := hashutil.SHA1Bytes([]byte(otherBody))
	pack := &Modpack{
		FormatVersion: FormatVersion,
		Game:          GameMinecraft,
		VersionID:     "1.0.0",
		Name:          "progress test",
		Files: []File{
			{
				Path:      ModsPrefix + "sodium.jar",
				Hashes:    map[string]string{"sha1": sodiumDigest},
				Downloads: []string{s.URL + "/sodium.jar"},
				FileSize:  len(installJarBody),
			},
			{
				Path:      ModsPrefix + "lithium.jar",
				Hashes:    map[string]string{"sha1": lithiumDigest},
				Downloads: []string{s.URL + "/lithium.jar"},
				FileSize:  len(otherBody),
			},
		},
	}

	var mu sync.Mutex
	var calls []progressCall
	installed, skipped, err := InstallWithProgress(context.Background(), nil, pack, modsDir, dir, false,
		recordProgress(&calls, &mu))
	if err != nil {
		t.Fatalf("InstallWithProgress: %v", err)
	}
	if installed != 2 || skipped != 0 {
		t.Fatalf("installed=%d skipped=%d, want 2/0", installed, skipped)
	}
	if len(calls) != 2 {
		t.Fatalf("hook opened %d tracker(s), want 1 per downloaded jar", len(calls))
	}
	names := map[string]bool{}
	wantAdded := map[string]int64{"sodium.jar": int64(len(installJarBody)), "lithium.jar": int64(len(otherBody))}
	for _, c := range calls {
		names[c.name] = true
		if c.total != wantAdded[c.name] {
			t.Errorf("%s total = %d, want the manifest size %d", c.name, c.total, wantAdded[c.name])
		}
		c.bar.mu.Lock()
		added, done := c.bar.added, c.bar.done
		c.bar.mu.Unlock()
		if added != wantAdded[c.name] {
			t.Errorf("%s advanced %d byte(s), want %d", c.name, added, wantAdded[c.name])
		}
		if !done {
			t.Errorf("%s was never Done", c.name)
		}
	}
	if !names["sodium.jar"] || !names["lithium.jar"] {
		t.Errorf("trackers = %v, want one per jar by file name", names)
	}
	assertContent(t, filepath.Join(modsDir, "sodium.jar"), installJarBody)
	assertContent(t, filepath.Join(modsDir, "lithium.jar"), otherBody)
}

// TestInstallWithProgressSkipsSilently covers the no-op half: a jar whose
// digest is already on disk is not downloaded, so it must not open a tracker
// either — a bar for a transfer that never happens is noise.
func TestInstallWithProgressSkipsSilently(t *testing.T) {
	isolateEnv(t)
	const otherBody = installJarBody + "lithium differs\n"
	s := newInstallStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/lithium.jar" {
			_, _ = w.Write([]byte(otherBody))
			return
		}
		_, _ = w.Write([]byte(installJarBody))
	})
	dir := t.TempDir()
	modsDir := filepath.Join(dir, "mods")

	files := []File{
		{
			Path:      ModsPrefix + "sodium.jar",
			Hashes:    map[string]string{"sha1": hashutil.SHA1Bytes([]byte(installJarBody))},
			Downloads: []string{s.URL + "/sodium.jar"},
			FileSize:  len(installJarBody),
		},
		{
			Path:      ModsPrefix + "lithium.jar",
			Hashes:    map[string]string{"sha1": hashutil.SHA1Bytes([]byte(otherBody))},
			Downloads: []string{s.URL + "/lithium.jar"},
			FileSize:  len(otherBody),
		},
	}
	pack := &Modpack{FormatVersion: FormatVersion, Game: GameMinecraft, VersionID: "1.0.0", Name: "skip test", Files: files}

	var mu sync.Mutex
	var calls []progressCall
	hook := recordProgress(&calls, &mu)
	if _, _, err := InstallWithProgress(context.Background(), nil, pack, modsDir, dir, false, hook); err != nil {
		t.Fatalf("first InstallWithProgress: %v", err)
	}

	calls = nil
	if _, _, err := InstallWithProgress(context.Background(), nil, pack, modsDir, dir, false, hook); err != nil {
		t.Fatalf("second InstallWithProgress: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("a fully cached import opened %d tracker(s), want none", len(calls))
	}
	if got := s.hits(); got != 2 {
		t.Fatalf("%d request(s), want 2 (one per jar on the first run only)", got)
	}
}

// TestInstallWithProgressNilHookStaysSilent pins the default: without a hook
// the install behaves exactly like Install, so every existing caller keeps
// working by passing nothing.
func TestInstallWithProgressNilHookStaysSilent(t *testing.T) {
	isolateEnv(t)
	s := newInstallStub(t, bodyStub(installJarBody))
	dir := t.TempDir()
	modsDir := filepath.Join(dir, "mods")

	pack := oneModPack("sodium.jar", s.URL+"/sodium.jar", hashutil.SHA1Bytes([]byte(installJarBody)))

	installed, skipped, err := InstallWithProgress(context.Background(), nil, pack, modsDir, dir, false, nil)
	if err != nil {
		t.Fatalf("InstallWithProgress: %v", err)
	}
	if installed != 1 || skipped != 0 {
		t.Fatalf("installed=%d skipped=%d, want 1/0", installed, skipped)
	}
	assertContent(t, filepath.Join(modsDir, "sodium.jar"), installJarBody)
}

// TestInstallWithProgressDoneOnChecksumMismatch covers the failure half of the
// tracker contract: a corrupt download still releases its bar, otherwise the
// terminal keeps a stuck line above the error.
func TestInstallWithProgressDoneOnChecksumMismatch(t *testing.T) {
	isolateEnv(t)
	s := newInstallStub(t, bodyStub(installOtherBody))
	dir := t.TempDir()
	modsDir := filepath.Join(dir, "mods")

	pack := oneModPack("sodium.jar", s.URL+"/sodium.jar", hashutil.SHA1Bytes([]byte(installJarBody)))

	var mu sync.Mutex
	var calls []progressCall
	if _, _, err := InstallWithProgress(context.Background(), nil, pack, modsDir, dir, false,
		recordProgress(&calls, &mu)); err == nil {
		t.Fatal("a digest mismatch was reported as a success")
	}
	if len(calls) != 1 {
		t.Fatalf("hook opened %d tracker(s), want 1", len(calls))
	}
	calls[0].bar.mu.Lock()
	done := calls[0].bar.done
	calls[0].bar.mu.Unlock()
	if !done {
		t.Fatal("a failed download never Done its tracker")
	}
	assertNothingLeft(t, modsDir)
}
