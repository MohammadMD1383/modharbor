package cli

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
)

// fetchClient downloads mod files. Modrinth's CDN benefits from keep-alive
// connections when pulling dozens of jars.
var fetchClient = &http.Client{
	Timeout: 10 * time.Minute,
	Transport: &http.Transport{
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
	},
}

// fetchFile downloads url into dest, verifying sha512 when provided.
//
// The download lands in a temporary file and is renamed into place only after
// the digest matches, so a failed or truncated download never leaves a broken
// jar where Minecraft would try to load it.
func fetchFile(ctx context.Context, url, dest, wantSHA512 string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", modrinth.UserAgent)

	resp, err := fetchClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s returned %s", url, resp.Status)
	}

	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".modharbor-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	h := sha512.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if want := normaliseDigest(wantSHA512); want != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(got, want) {
			return fmt.Errorf("checksum mismatch for %s (expected %s, got %s)",
				filepath.Base(dest), want, got)
		}
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}

// sortVersionsByPreference orders versions newest-first while preferring
// releases over betas over alphas within the allowed channel.
func sortVersionsByPreference(vers []modrinth.Version, channel modrinth.Channel) {
	rank := func(t string) int {
		switch channel {
		case modrinth.ChannelAlpha:
			switch t {
			case "release":
				return 0
			case "beta":
				return 1
			default:
				return 2
			}
		case modrinth.ChannelBeta:
			if t == "release" {
				return 0
			}
			return 1
		default:
			return 0
		}
	}
	sort.SliceStable(vers, func(i, j int) bool {
		ri, rj := rank(vers[i].VersionType), rank(vers[j].VersionType)
		if ri != rj {
			return ri < rj
		}
		return vers[i].DatePublished.After(vers[j].DatePublished)
	})
}

func normaliseDigest(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimPrefix(h, "sha512:")
	return h
}
