package cli

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fetchFile is the only route by which a jar reaches an instance, and its
// staging-then-rename discipline is what stops Minecraft ever loading a
// half-written file. These tests pin that contract down; the progress work is
// layered on top of it and must not weaken any of it.

// digestOf is the sha512 fetchFile verifies a download against.
func digestOf(b []byte) string {
	sum := sha512.Sum512(b)
	return hex.EncodeToString(sum[:])
}

// assertNothingAt fails if anything exists at path. That is what "the download
// never became visible" means in practice.
func assertNothingAt(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no file at %s, but stat returned %v", path, err)
	}
}

// assertNoStagingLeftovers fails if the destination directory still holds a
// staging file, which would mean a failed download left debris behind.
func assertNoStagingLeftovers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".modharbor-") {
			t.Errorf("leftover staging file %s in %s", e.Name(), dir)
		}
	}
}

// serveBytes starts a test server returning body, standing in for a CDN.
func serveBytes(t *testing.T, body []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestFetchFileWritesVerifiedContent(t *testing.T) {
	body := bytes.Repeat([]byte("modjar"), 4096)
	url := serveBytes(t, body)

	dir := t.TempDir()
	dest := filepath.Join(dir, "mod.jar")

	if err := fetchFile(context.Background(), url, dest, digestOf(body), nil); err != nil {
		t.Fatalf("fetchFile: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("content mismatch: got %d bytes, want %d", len(got), len(body))
	}
	assertNoStagingLeftovers(t, dir)
}

// A checksum mismatch must leave nothing at dest. Verifying before the rename
// is precisely so that Minecraft never sees a jar we could not vouch for.
func TestFetchFileChecksumMismatchLeavesNoFile(t *testing.T) {
	body := []byte("this is not the jar you are looking for")
	url := serveBytes(t, body)

	dir := t.TempDir()
	dest := filepath.Join(dir, "mod.jar")
	wrong := digestOf([]byte("the expected content"))

	err := fetchFile(context.Background(), url, dest, wrong, nil)
	if err == nil {
		t.Fatal("expected a checksum error, got nil")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error should name the checksum, got: %v", err)
	}
	assertNothingAt(t, dest)
	assertNoStagingLeftovers(t, dir)
}

// An empty expectation means "no digest recorded", not "reject everything".
func TestFetchFileWithoutDigestSkipsVerification(t *testing.T) {
	body := []byte("unverified but expected")
	url := serveBytes(t, body)

	dest := filepath.Join(t.TempDir(), "mod.jar")
	if err := fetchFile(context.Background(), url, dest, "", nil); err != nil {
		t.Fatalf("fetchFile with no digest: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Error("content mismatch")
	}
}

// Real API payloads prefix the algorithm ("sha512:"), so the prefix must be
// stripped before the digest is compared rather than treated as part of it.
func TestFetchFileStripsDigestPrefix(t *testing.T) {
	body := []byte("prefixed digest")
	url := serveBytes(t, body)

	dest := filepath.Join(t.TempDir(), "mod.jar")
	err := fetchFile(context.Background(), url, dest, "sha512:0000", nil)
	if err == nil {
		t.Fatal("expected a checksum error when the digest does not match")
	}
	assertNothingAt(t, dest)
}

// A non-2xx response must fail before anything is written. An error page is
// not a jar, and renaming it into place would be worse than failing outright.
func TestFetchFileNonSuccessStatusLeavesNoFile(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte("<html>not a jar</html>"))
			}))
			defer srv.Close()

			dir := t.TempDir()
			dest := filepath.Join(dir, "mod.jar")

			err := fetchFile(context.Background(), srv.URL, dest, "", nil)
			if err == nil {
				t.Fatalf("expected an error for HTTP %d", status)
			}
			if !strings.Contains(err.Error(), srv.URL) {
				t.Errorf("error should name the URL, got: %v", err)
			}
			assertNothingAt(t, dest)
			assertNoStagingLeftovers(t, dir)
		})
	}
}

// The bar must be cleared on success and on every failure, so a rejected
// download cannot leave progress painted over the user's terminal. stderr is
// not a terminal under `go test`, so ui.Progress is inert here; what this
// proves is that each path reaches Done without panicking and without leaving
// a staging file behind.
func TestFetchFileProgressIsSafeOnEveryPath(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 2048)
	ok := serveBytes(t, body)
	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer missing.Close()

	cases := []struct {
		name    string
		url     string
		digest  string
		wantErr bool
	}{
		{"success", ok, digestOf(body), false},
		{"checksum mismatch", ok, digestOf([]byte("other")), true},
		{"http error", missing.URL, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, "mod.jar")

			err := fetchFile(context.Background(), tc.url, dest, tc.digest,
				newTransfer("Some Mod", int64(len(body))))

			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %t", err, tc.wantErr)
			}
			assertNoStagingLeftovers(t, dir)
		})
	}
}

// Progress reporting is an observer, not a participant: it must not alter the
// bytes that reach disk.
func TestFetchFileProgressPreservesContent(t *testing.T) {
	body := bytes.Repeat([]byte("abcdefgh"), 1024)
	url := serveBytes(t, body)

	dest := filepath.Join(t.TempDir(), "mod.jar")
	tr := newTransfer("Some Mod", int64(len(body)))
	if err := fetchFile(context.Background(), url, dest, digestOf(body), tr); err != nil {
		t.Fatalf("fetchFile: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Error("progress reporting must not corrupt the payload")
	}
}

func TestProgressEnabledHonoursOutputFlags(t *testing.T) {
	saveJSON, saveQuiet := flagJSON, flagQuiet
	t.Cleanup(func() { flagJSON, flagQuiet = saveJSON, saveQuiet })

	cases := []struct {
		name  string
		json  bool
		quiet bool
		want  bool
	}{
		{"default draws", false, false, true},
		{"json suppresses", true, false, false},
		{"quiet suppresses", false, true, false},
		{"both suppress", true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flagJSON, flagQuiet = tc.json, tc.quiet
			if got := progressEnabled(); got != tc.want {
				t.Errorf("progressEnabled() = %t, want %t", got, tc.want)
			}
			if got := newTransfer("Mod", 10) != nil; got != tc.want {
				t.Errorf("newTransfer non-nil = %t, want %t", got, tc.want)
			}
		})
	}
}

// Content-Length is authoritative: it is what the server is actually sending.
// A stale size from the catalogue would stop the bar short of 100%.
func TestTransferTotalPrefersContentLength(t *testing.T) {
	cases := []struct {
		name string
		tr   *transfer
		resp *http.Response
		want int64
	}{
		{"content-length wins", &transfer{Label: "m", Total: 10},
			&http.Response{ContentLength: 99}, 99},
		{"falls back to hint", &transfer{Label: "m", Total: 10},
			&http.Response{ContentLength: -1}, 10},
		{"no response at all", &transfer{Label: "m", Total: 10}, nil, 10},
		{"nothing known", nil, nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := transferTotal(tc.tr, tc.resp); got != tc.want {
				t.Errorf("transferTotal() = %d, want %d", got, tc.want)
			}
		})
	}
}

// shortDigest keeps --verbose lines readable while still giving enough of a
// hash to eyeball against a published one.
func TestShortDigest(t *testing.T) {
	cases := map[string]string{
		"":                         "none",
		"abc":                      "abc",
		"0123456789abcdef0123":     "0123456789ab",
		"0123456789abcdef01234567": "0123456789ab",
	}
	for in, want := range cases {
		if got := shortDigest(in); got != want {
			t.Errorf("shortDigest(%q) = %q, want %q", in, got, want)
		}
	}
}
