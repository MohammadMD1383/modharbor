package migrate

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// httpClient is used for all downloads. Modrinth's CDN is happy with a
// keep-alive connection pool, which matters when pulling dozens of jars.
var httpClient = &http.Client{
	Timeout: 10 * time.Minute,
	Transport: &http.Transport{
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  false,
	},
}

// downloadTo fetches url into destPath, streaming to a temporary file and
// renaming only after the digest checks out. A partially written jar is never
// left behind where the loader could try to load it.
func downloadTo(ctx context.Context, url, destPath, expectSHA512, _ string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "modharbor/1.0")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", filepath.Base(destPath), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("downloading %s: %s", filepath.Base(destPath), resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(destPath), ".modharbor-dl-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	h512 := sha512.New()
	multi := io.MultiWriter(tmp, h512)
	if _, err := io.Copy(multi, resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("downloading %s: %w", filepath.Base(destPath), err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if expected := normalise(expectSHA512); expected != "" {
		got := hex.EncodeToString(h512.Sum(nil))
		if !strings.EqualFold(got, expected) {
			return fmt.Errorf("checksum mismatch for %s: expected sha512 %s, got %s",
				filepath.Base(destPath), expected, got)
		}
	}

	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpName, destPath); err != nil {
		return err
	}
	return nil
}

// copyFile copies src to dst atomically via a temp file.
func copyFile(src, dst string) error {
	if src == dst {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".modharbor-cp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := io.Copy(tmp, in); err != nil {
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
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

// backupDirName is the subdirectory replaced files are moved into.
const backupDirName = ".modharbor-backup"

// backupReplacements moves files that will be replaced into a timestamped
// backup directory, so an interrupted migration is always recoverable.
func backupReplacements(modsDir string, results []Result) error {
	var toBackup []string
	for _, r := range results {
		if r.Action != ActionReplace || r.SourceFile == "" {
			continue
		}
		if r.TargetFile == r.SourceFile {
			continue
		}
		path := filepath.Join(modsDir, r.SourceFile)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		toBackup = append(toBackup, r.SourceFile)
	}
	if len(toBackup) == 0 {
		return nil
	}

	dir := filepath.Join(modsDir, backupDirName, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range toBackup {
		from := filepath.Join(modsDir, name)
		to := filepath.Join(dir, name)
		if err := os.Rename(from, to); err != nil {
			// Fall back to a copy when the rename crosses filesystems.
			if cerr := copyFile(from, to); cerr != nil {
				return fmt.Errorf("backing up %s: %w", name, err)
			}
			_ = os.Remove(from)
		}
	}
	return nil
}

func normalise(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimPrefix(h, "sha512:")
	h = strings.TrimPrefix(h, "sha1:")
	return h
}
