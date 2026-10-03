package mrpack

import (
	"archive/zip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MohammadMD1383/modharbor/internal/hashutil"
	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
)

// downloadClient fetches pack files. Modrinth's CDN benefits from keep-alive
// connections when a pack pulls dozens of jars.
var downloadClient = &http.Client{
	Timeout: 10 * time.Minute,
	Transport: &http.Transport{
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
	},
}

// Install materialises a pack into an instance.
//
// modsDir receives everything under mods/. overridesDir receives the pack's
// overrides/ tree — pass the instance root, since that is where a launcher
// expects config, resourcepacks and shaderpacks to land — and includeOverrides
// decides whether that happens at all.
//
// A mod whose sha1 already exists in modsDir is skipped, which makes re-running
// an import cheap and makes `--yes` safe. Downloads land in a temporary file in
// the destination directory and are renamed into place only after the digest
// checks out, so an interrupted import never leaves a half-written jar for
// Minecraft to choke on.
func Install(ctx context.Context, mr *modrinth.Client, pack *Modpack, modsDir string, overridesDir string, includeOverrides bool) (installed, skipped int, err error) {
	if pack == nil {
		return 0, 0, fmt.Errorf("no modpack to install")
	}
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		return 0, 0, fmt.Errorf("%s: %w", modsDir, err)
	}

	present, err := installedDigests(modsDir)
	if err != nil {
		return 0, 0, err
	}

	mods := pack.Mods()
	for _, f := range mods {
		want := f.SHA1()
		if want != "" {
			if _, ok := present[want]; ok {
				skipped++
				continue
			}
		}

		url, err := resolveURL(ctx, mr, pack, f)
		if err != nil {
			return installed, skipped, fmt.Errorf("%s: %w", f.Name(), err)
		}
		dest := filepath.Join(modsDir, f.Name())
		if err := downloadInto(ctx, url, dest, want); err != nil {
			return installed, skipped, fmt.Errorf("%s: %w", f.Name(), err)
		}
		if want != "" {
			// A pack can list the same file twice; recording it now stops the
			// duplicate from being downloaded a second time.
			present[want] = f.Name()
		}
		installed++
	}

	if !includeOverrides {
		return installed, skipped, nil
	}
	overridden, err := extractOverrides(pack, overridesDir)
	if err != nil {
		return installed, skipped, err
	}
	return installed + overridden, skipped, nil
}

// resolveURL finds where a manifest entry can be fetched from.
//
// The pack's own `downloads` list comes first: it is the only source that
// needs no network at all, and it is what makes an export portable even if the
// upstream project is later deleted. Only when it is empty do we spend a
// request, and even then the version id is read from the dependency map so a
// whole pack costs one batch call rather than one call per mod.
func resolveURL(ctx context.Context, mr *modrinth.Client, pack *Modpack, f File) (string, error) {
	for _, u := range f.Downloads {
		if u != "" {
			return u, nil
		}
	}
	if mr == nil {
		return "", fmt.Errorf("no download URL and no Modrinth client available")
	}

	dep, ok := pack.DependencyFor(f.Path)
	if !ok || dep.VersionID == "" {
		return "", fmt.Errorf("no download URL%s and no version id recorded", overrideHint(f.Path))
	}

	versions, err := mr.VersionsBatch(ctx, []string{dep.VersionID})
	if err != nil {
		return "", fmt.Errorf("looking up version %s: %w", dep.VersionID, err)
	}
	for _, v := range versions {
		file, ok := v.PrimaryFile()
		if !ok {
			continue
		}
		if url, err := mr.CDNURL(ctx, file); err == nil && url != "" {
			return url, nil
		}
		return file.URL, nil
	}
	return "", fmt.Errorf("version %s has no downloadable file", dep.VersionID)
}

// overrideHint names the situation in the error message, because an override
// entry legitimately has no URL and the user needs to know that is expected.
func overrideHint(path string) string {
	if isOverridePath(path) {
		return " (the pack carries this file as an override, so the archive itself is incomplete)"
	}
	return ""
}

// installedDigests maps sha1 to file name for everything already in modsDir.
func installedDigests(modsDir string) (map[string]string, error) {
	files, err := instance.ModFiles(modsDir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", modsDir, err)
	}
	out := make(map[string]string, len(files))
	for _, f := range files {
		sha1, err := hashutil.SHA1File(f)
		if err != nil {
			// An unreadable jar is not a reason to refuse the whole import.
			continue
		}
		out[sha1] = filepath.Base(f)
	}
	return out, nil
}

// downloadInto fetches url into dest and verifies wantSHA1 before the file
// becomes visible to the loader.
func downloadInto(ctx context.Context, url, dest, wantSHA1 string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", modrinth.UserAgent)

	resp, err := downloadClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s returned %s", url, resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".modharbor-dl-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	h := sha1.New()
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

	if want := hashutil.Normalize(wantSHA1); want != "" {
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
			return fmt.Errorf("checksum mismatch: expected sha1 %s, got %s", want, got)
		}
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}

// extractOverrides writes the pack's overrides/ tree into dest, verifying each
// file against the digest recorded when the pack was loaded.
//
// Files already present with the right digest are left alone, so importing the
// same pack twice does not rewrite a user's hand-edited config.
func extractOverrides(pack *Modpack, dest string) (int, error) {
	overrides := pack.Overrides()
	if len(overrides) == 0 {
		return 0, nil
	}
	if dest == "" {
		return 0, fmt.Errorf("the pack has %d override file(s) but no destination directory was given", len(overrides))
	}
	if pack.source == "" {
		return 0, fmt.Errorf("this pack was not loaded from an archive, so its %d override file(s) are unavailable", len(overrides))
	}

	zr, err := zip.OpenReader(pack.source)
	if err != nil {
		return 0, fmt.Errorf("reopening %s: %w", pack.source, err)
	}
	defer zr.Close()

	wanted := make(map[string]File, len(overrides))
	for _, f := range overrides {
		wanted[normalisePath(f.Path)] = f
	}

	written := 0
	for _, zf := range zr.File {
		name := normalisePath(zf.Name)
		f, ok := wanted[name]
		if !ok || zf.FileInfo().IsDir() {
			continue
		}
		rel := strings.TrimPrefix(name, OverridesPrefix)
		if rel == "" {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		ok, err := writeOverride(zf, target, f)
		if err != nil {
			return written, fmt.Errorf("%s: %w", f.Path, err)
		}
		if ok {
			written++
		}
	}
	return written, nil
}

// writeOverride copies one archive entry to target. It reports whether the
// file was written; an existing file with the expected digest is a no-op.
func writeOverride(zf *zip.File, target string, want File) (bool, error) {
	rc, err := zf.Open()
	if err != nil {
		return false, err
	}
	defer rc.Close()

	if digest := hashutil.Normalize(want.SHA1()); digest != "" {
		if existing, err := hashutil.SHA1File(target); err == nil && existing == digest {
			return false, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".modharbor-ov-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	h := sha1.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), rc); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}

	if expected := hashutil.Normalize(want.SHA1()); expected != "" {
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, expected) {
			return false, fmt.Errorf("checksum mismatch: expected sha1 %s, got %s", expected, got)
		}
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return false, err
	}
	return true, os.Rename(tmpName, target)
}
