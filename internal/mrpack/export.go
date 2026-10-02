package mrpack

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MohammadMD1383/modharbor/internal/config"
	"github.com/MohammadMD1383/modharbor/internal/hashutil"
	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
)

// cacheTTL keeps one export from re-querying a project shared by several mods.
const cacheTTL = 10 * time.Minute

// ExportResult summarises a completed export so the CLI can report on it
// without re-reading the archive it just wrote.
type ExportResult struct {
	// Path is the written archive.
	Path string
	// Name is the pack name recorded in the manifest.
	Name string
	// MCVersion is the game version recorded as dependencies.minecraft.
	MCVersion string
	// Resolved counts mods referenced by download URL.
	Resolved int
	// Overrides counts mods carried verbatim.
	Overrides int
	// Total is the number of files in the manifest.
	Total int
}

// Export writes an instance's mods as a Modrinth `.mrpack` in dir and returns
// a summary including the path it wrote.
//
// Every jar is identified through Modrinth's hash lookup. A jar Modrinth does
// not know — a private build, a CurseForge mirror — is copied into the
// overrides directory and recorded with its real sha1 and sha512, because the
// only way this format can carry an un-downloadable file is to ship its
// bytes. The pack therefore always round-trips, which is worth more than a
// manifest that is smaller or purer.
//
// The Modrinth endpoint comes from the user's config so a mirror or a test
// server is honoured without threading a client through the CLI.
func Export(ctx context.Context, dir string, instancePath string, mcVersion string, overridesDir string) (ExportResult, error) {
	inst, err := instance.Load(instancePath)
	if err != nil {
		// The caller has already resolved the instance; failing here would
		// only cost us a label, so fall back to the directory name.
		inst = &instance.Info{ID: filepath.Base(filepath.Clean(instancePath)), Path: instancePath}
	}
	if mcVersion == "" {
		mcVersion = inst.MCVersion
	}

	mr, err := newClient()
	if err != nil {
		return ExportResult{}, err
	}

	mods, err := RequiredMods(ctx, mr, instancePath)
	if err != nil {
		return ExportResult{}, err
	}
	if len(mods) == 0 {
		return ExportResult{}, fmt.Errorf("no mods found in %s; nothing to export",
			filepath.Join(instancePath, "mods"))
	}

	if overridesDir == "" {
		overridesDir = filepath.Join(dir, ".modharbor-overrides")
	}
	if err := os.MkdirAll(overridesDir, 0o755); err != nil {
		return ExportResult{}, fmt.Errorf("preparing %s: %w", overridesDir, err)
	}

	mp := &Modpack{
		FormatVersion: FormatVersion,
		Game:          GameMinecraft,
		VersionID:     mcVersion,
		Name:          inst.ID,
		Summary:       "Exported from " + inst.ID + " by modharbor",
		Dependencies:  map[string]Dependency{},
	}
	if mcVersion != "" {
		mp.Dependencies["minecraft"] = Dependency{VersionID: mcVersion, DependencyType: "minecraft"}
	}
	if name, version := loaderDependency(inst); version != "" {
		mp.Dependencies[name] = Dependency{VersionID: version, DependencyType: name}
	}

	res := ExportResult{Name: inst.ID, MCVersion: mcVersion}
	for _, m := range mods {
		f, err := entryFor(ctx, mr, m, overridesDir)
		if err != nil {
			return ExportResult{}, err
		}
		mp.Files = append(mp.Files, f)
		mp.Dependencies[f.Path] = Dependency{
			ProjectID:      m.ProjectID,
			VersionID:      m.VersionID,
			FileName:       m.FileName,
			DependencyType: EnvRequired,
		}
		if isOverridePath(f.Path) {
			res.Overrides++
		} else {
			res.Resolved++
		}
	}

	out := filepath.Join(dir, packFileName(inst.ID, mcVersion))
	if err := Save(out, mp, overridesDir); err != nil {
		return ExportResult{}, err
	}
	res.Path = out
	res.Total = len(mp.Files)
	return res, nil
}

// entryFor turns one required mod into a manifest entry.
//
// A resolved mod is referenced by URL so the pack stays small. An unresolved
// one is copied into the overrides tree first, because an entry with no
// download URL is useless to every consumer of the format.
func entryFor(ctx context.Context, mr *modrinth.Client, m RequiredMod, overridesDir string) (File, error) {
	if !m.Resolved() {
		return privateEntry(m, overridesDir)
	}

	// The CDN URL is preferred over the mirror URL: it is what Modrinth hands
	// out to its own launcher and it survives a project changing mirrors.
	// CDNURL degrades to the mirror URL on its own when it cannot resolve one.
	url, err := mr.CDNURL(ctx, m.file)
	if err != nil || url == "" {
		url = m.file.URL
	}
	if url == "" {
		return File{}, fmt.Errorf("%s: modrinth version %s has no download URL",
			m.FileName, m.VersionID)
	}

	hashes := map[string]string{}
	if s := hashutil.Normalize(m.file.SHA1()); s != "" {
		hashes["sha1"] = s
	}
	if s := hashutil.Normalize(m.file.SHA512()); s != "" {
		hashes["sha512"] = s
	}
	return File{
		Path:      ModsPrefix + m.FileName,
		Hashes:    hashes,
		Env:       m.env(),
		Downloads: []string{url},
		FileSize:  int(m.file.Size),
	}, nil
}

// privateEntry copies an unresolvable jar into the overrides tree and
// describes it by its real digests.
//
// The digest comes from the local bytes rather than from anywhere upstream
// precisely because there is no upstream: this record is what lets an import
// verify the file it is about to unpack.
func privateEntry(m RequiredMod, overridesDir string) (File, error) {
	h, err := hashutil.File(m.SourcePath())
	if err != nil {
		return File{}, fmt.Errorf("hashing %s: %w", m.FileName, err)
	}
	rel := ModsPrefix + m.FileName
	dest := filepath.Join(overridesDir, filepath.FromSlash(rel))
	if err := copyFile(m.SourcePath(), dest); err != nil {
		return File{}, fmt.Errorf("staging %s: %w", m.FileName, err)
	}
	return File{
		Path:     OverridesPrefix + rel,
		Hashes:   map[string]string{"sha1": h.SHA1, "sha512": h.SHA512},
		FileSize: int(h.Size),
	}, nil
}

// loaderDependency names the instance's loader and its version for the
// manifest's dependency map, which is what a launcher reads to install the
// right loader. A vanilla instance has none.
func loaderDependency(inst *instance.Info) (name, version string) {
	switch inst.Type {
	case instance.TypeFabric:
		name, version = "fabric-loader", inst.LoaderVersion
	case instance.TypeQuilt:
		name, version = "quilt-loader", inst.LoaderVersion
	case instance.TypeForge:
		name, version = "forge", inst.LoaderVersion
	case instance.TypeNeoForge:
		name, version = "neoforge", inst.LoaderVersion
	}
	return name, version
}

// packFileName builds "<instance>-<mcversion>.mrpack", dropping the version
// when the instance never declared one.
func packFileName(instanceID, mcVersion string) string {
	instanceID = sanitiseName(instanceID)
	if mcVersion == "" {
		return instanceID + ".mrpack"
	}
	return instanceID + "-" + sanitiseName(mcVersion) + ".mrpack"
}

// sanitiseName reduces a name to characters that are safe in a file name on
// every platform modharbor runs on.
func sanitiseName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "instance"
	}
	return out
}

// newClient builds the Modrinth client Export uses, honouring the user's
// configured endpoint so a mirror or test server works without extra flags.
func newClient() (*modrinth.Client, error) {
	cfg, _, err := config.Load("")
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	return modrinth.New(modrinth.Options{BaseURL: cfg.Modrinth.BaseURL, CacheTTL: cacheTTL}), nil
}

// copyFile copies src to dst through a temporary file, so an interrupted
// export cannot leave a truncated jar in the overrides tree.
func copyFile(src, dst string) error {
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
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}
