// Package hashutil provides hashing primitives used for mod identification
// and download verification.
//
// Modrinth publishes both SHA-1 and SHA-512 for every file; CurseForge uses
// SHA-1 (and MD5 for legacy files). We compute SHA-1 and SHA-512 in a single
// pass over the file.
package hashutil

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
)

// Hashes holds the digests computed for one file.
type Hashes struct {
	Size   int64  `json:"size"`
	SHA1   string `json:"sha1,omitempty"`
	SHA512 string `json:"sha512,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// Multi accumulates several hashes in a single read.
type Multi struct {
	sha1   hash.Hash
	sha512 hash.Hash
	sha256 hash.Hash
	size   int64
}

// NewMulti creates an empty accumulator.
func NewMulti() *Multi {
	return &Multi{
		sha1:   sha1.New(),
		sha512: sha512.New(),
		sha256: sha256.New(),
	}
}

// Write feeds bytes into every accumulator.
func (m *Multi) Write(p []byte) (int, error) {
	m.sha1.Write(p)
	m.sha512.Write(p)
	m.sha256.Write(p)
	m.size += int64(len(p))
	return len(p), nil
}

// Sum returns the computed digests.
func (m *Multi) Sum() Hashes {
	return Hashes{
		Size:   m.size,
		SHA1:   hex.EncodeToString(m.sha1.Sum(nil)),
		SHA512: hex.EncodeToString(m.sha512.Sum(nil)),
		SHA256: hex.EncodeToString(m.sha256.Sum(nil)),
	}
}

// File computes all supported hashes for the file at path.
func File(path string) (Hashes, error) {
	f, err := os.Open(path)
	if err != nil {
		return Hashes{}, err
	}
	defer f.Close()

	m := NewMulti()
	if _, err := io.Copy(m, f); err != nil {
		return Hashes{}, err
	}
	h := m.Sum()

	fi, err := f.Stat()
	if err == nil {
		h.Size = fi.Size()
	}
	return h, nil
}

// SHA1File returns just the SHA-1 of a file, the identifier used by both
// Modrinth and CurseForge hash lookup endpoints.
func SHA1File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SHA1Bytes hashes an in-memory buffer.
func SHA1Bytes(b []byte) string {
	s := sha1.Sum(b)
	return hex.EncodeToString(s[:])
}

// SHA512Bytes hashes an in-memory buffer with SHA-512.
func SHA512Bytes(b []byte) string {
	s := sha512.Sum512(b)
	return hex.EncodeToString(s[:])
}

// Verify checks that data hashes to the expected digest. It accepts SHA-1,
// SHA-512 or SHA-256 (auto-detected by digest length) and treats an empty
// expectation as "skip".
func Verify(data []byte, expected string) error {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if expected == "" {
		return nil
	}
	var got string
	switch len(expected) {
	case 40:
		got = SHA1Bytes(data)
	case 64:
		got = SHA256Bytes(data)
	case 128:
		got = SHA512Bytes(data)
	default:
		return fmt.Errorf("unsupported digest length %d for %q", len(expected), expected)
	}
	if !strings.EqualFold(got, expected) {
		return fmt.Errorf("hash mismatch: expected %s, got %s", expected, got)
	}
	return nil
}

// SHA256Bytes hashes an in-memory buffer with SHA-256.
func SHA256Bytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Normalize lowercases a digest and strips common prefixes.
func Normalize(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimPrefix(h, "sha1:")
	h = strings.TrimPrefix(h, "sha512:")
	h = strings.TrimPrefix(h, "sha256:")
	return h
}
