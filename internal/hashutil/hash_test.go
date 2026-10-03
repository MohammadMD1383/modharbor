package hashutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hashutil underpins mod identification (SHA-1 lookups) and download
// verification, so the digest helpers and the Verify dispatcher are pinned
// against known vectors here.
func TestBytesHelpersMatchKnownVectors(t *testing.T) {
	if got, want := SHA1Bytes([]byte("abc")), "a9993e364706816aba3e25717850c26c9cd0d89d"; got != want {
		t.Errorf("SHA1Bytes(abc) = %q, want %q", got, want)
	}
	if got, want := SHA256Bytes([]byte("abc")), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"; got != want {
		t.Errorf("SHA256Bytes(abc) = %q, want %q", got, want)
	}
	if got, want := SHA512Bytes([]byte("abc")), "ddaf35a193617abacc417349ae20413112e6fa4e89a97ea20a9eeee64b55d39a2192992a274fc1a836ba3c23a3feebbd454d4423643ce80e2a9ac94fa54ca49f"; got != want {
		t.Errorf("SHA512Bytes(abc) = %q, want %q", got, want)
	}
}

func TestMultiAndFileAgree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mod.jar")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewMulti()
	if _, err := m.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Write([]byte("bc")); err != nil {
		t.Fatal(err)
	}
	mem := m.Sum()
	if mem.Size != 3 {
		t.Errorf("Multi size = %d, want 3", mem.Size)
	}

	got, err := File(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != mem {
		t.Errorf("File() = %+v, Multi sum = %+v, want equal", got, mem)
	}
	if got.Size != 3 {
		t.Errorf("File size = %d, want 3", got.Size)
	}
	if got.SHA1 != SHA1Bytes([]byte("abc")) || got.SHA256 != SHA256Bytes([]byte("abc")) || got.SHA512 != SHA512Bytes([]byte("abc")) {
		t.Errorf("File digests = %+v, want the abc vectors", got)
	}
}

func TestFileAndSHA1FileRejectMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.jar")
	if _, err := File(missing); err == nil {
		t.Error("File(missing) = nil error, want open failure")
	}
	if _, err := SHA1File(missing); err == nil {
		t.Error("SHA1File(missing) = nil error, want open failure")
	}
}

func TestSHA1FileMatchesBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mod.jar")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := SHA1File(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := SHA1Bytes([]byte("abc")); got != want {
		t.Errorf("SHA1File() = %q, want %q", got, want)
	}
}

func TestVerifyDispatchesByLength(t *testing.T) {
	data := []byte("abc")
	cases := map[string]string{
		SHA1Bytes(data):   "sha1",
		SHA256Bytes(data): "sha256",
		SHA512Bytes(data): "sha512",
	}
	for digest := range cases {
		if err := Verify(data, digest); err != nil {
			t.Errorf("Verify(%s) = %v, want nil", cases[digest], err)
		}
		// Uppercase and prefixed forms are accepted too.
		if err := Verify(data, "  "+strings.ToUpper(digest)+" "); err != nil {
			t.Errorf("Verify(upper %s) = %v, want nil", cases[digest], err)
		}
	}
	if err := Verify(nil, ""); err != nil {
		t.Errorf("Verify(empty expectation) = %v, want nil (skip)", err)
	}
	if err := Verify(data, SHA1Bytes([]byte("other"))); err == nil {
		t.Error("Verify(wrong digest) = nil, want mismatch")
	}
	if err := Verify(data, "abc"); err == nil || !strings.Contains(err.Error(), "unsupported digest length") {
		t.Errorf("Verify(bad length) = %v, want unsupported-length error", err)
	}
}

func TestNormalizeStripsPrefixes(t *testing.T) {
	for in, want := range map[string]string{
		"  ABCDEF ": "abcdef",
		"SHA1:a9993e364706816aba3e25717850c26c9cd0d89d": "a9993e364706816aba3e25717850c26c9cd0d89d",
		"sha256:DEADBEEF": "deadbeef",
		"sha512:DEADBEEF": "deadbeef",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
