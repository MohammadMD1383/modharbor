package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MohammadMD1383/modharbor/internal/app"
	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/modmeta"
	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
)

// installProjects resolves projects and their dependencies, then downloads
// them into the instance.
//
// Resolution order per project:
//  1. exact project id or slug lookup
//  2. search, ranked by similarity against the query
//
// Dependencies declared by the chosen version are followed recursively when
// withDeps is true, which is how things like "Sodium" and its core library
// both land in the folder.
func installProjects(ctx context.Context, a *app.App, inst *instance.Info, projects []string, withDeps bool, channel modrinth.Channel) ([]installedMod, []string, error) {
	cli := a.MR()
	loader := loaderName(inst.Type)
	if loader == "" {
		loader = "fabric"
	}

	modsDir := inst.ModsDirOrDefault()
	existing, err := installedIndex(ctx, a, inst)
	if err != nil {
		return nil, nil, err
	}

	var (
		installed []installedMod
		skipped   []string
		seen      = map[string]bool{}
		queue     = projects
	)

	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true

		proj, err := resolveProject(ctx, cli, ref)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving %s: %w", ref, err)
		}

		verbosef("resolved %q to %s (%s)", ref, proj.Title, proj.ID)

		// Already installed?
		if _, ok := existing[proj.ID]; ok {
			verbosef("skipping %s: already installed", proj.Title)
			skipped = append(skipped, proj.Title)
			continue
		}

		ver, reason := bestCompatible(ctx, cli, proj.ID, inst.MCVersion, loader, channel)
		if ver == nil {
			return nil, nil, fmt.Errorf("%s: %s", proj.Title, reason)
		}
		file, ok := ver.PrimaryFile()
		if !ok {
			return nil, nil, fmt.Errorf("%s: version %s has no jar", proj.Title, ver.VersionNumber)
		}

		url, err := cli.CDNURL(ctx, file)
		if err != nil {
			url = file.URL
		}
		verbosef("selected %s %s for MC %s / %s (%s from %s)",
			proj.Title, ver.VersionNumber, inst.MCVersion, loader,
			file.Filename, url)

		mod := installedMod{
			name:     proj.Title,
			version:  ver.VersionNumber,
			filename: file.Filename,
			sha1:     file.SHA1(),
			sha512:   file.SHA512(),
			url:      url,
			size:     file.Size,
		}
		if err := downloadInto(ctx, url, filepath.Join(modsDir, file.Filename),
			file.SHA512(), newTransfer(proj.Title, file.Size)); err != nil {
			return nil, nil, fmt.Errorf("downloading %s: %w", proj.Title, err)
		}
		installed = append(installed, mod)
		verbosef("verified %s sha1=%s sha512=%s", file.Filename,
			shortDigest(file.SHA1()), shortDigest(file.SHA512()))

		// No ui.Task here on purpose: the caller renders the result list once,
		// from the returned slices. Printing here too duplicated every line and
		// put a stray row on stdout ahead of the JSON under --json.
		if withDeps {
			for _, dep := range ver.Dependencies {
				switch strings.ToLower(dep.DependencyType) {
				case "required":
					if dep.ProjectID != "" && !seen[dep.ProjectID] {
						queue = append(queue, dep.ProjectID)
					}
				}
			}
		}
	}

	return installed, skipped, nil
}

// installedIndex maps project ids already present in an instance to their
// file names.
func installedIndex(ctx context.Context, a *app.App, inst *instance.Info) (map[string]ScannedMod, error) {
	out := map[string]ScannedMod{}
	if flagOffline {
		return out, nil
	}
	rows, err := scanInstance(ctx, a, inst, false)
	if err != nil {
		// An unreadable mods directory is not fatal for `add`.
		return out, nil
	}
	for _, r := range rows {
		// Emitted for every jar, not just the identified ones: "why did
		// modharbor decide this folder was empty?" is answered by the row that
		// came back unmatched.
		verboseResolution(r)
		if r.ProjectID != "" {
			out[r.ProjectID] = r
		}
	}
	return out, nil
}

// verboseResolution reports one jar's identification for --verbose.
//
// This is the diagnostic that answers "why did modharbor think that was X?".
// Every field already exists on ScannedMod, so this costs nothing beyond the
// flag: file name, the strategy that matched, the project, and the confidence
// the strategy earned.
func verboseResolution(r ScannedMod) {
	project := r.ProjectID
	if project == "" {
		project = "none"
	}
	verbosef("%s  method=%s  project=%s  title=%s  confidence=%d%%",
		r.FileName, orDash(r.Method), project, orDash(r.Title),
		int(r.Confidence*100+0.5))
}

// resolveProject looks up a project by id or slug, falling back to search.
func resolveProject(ctx context.Context, cli *modrinth.Client, ref string) (*modrinth.Project, error) {
	key := projectKey(ref)
	if p, err := cli.Project(ctx, key); err == nil {
		return p, nil
	}
	res, err := cli.Search(ctx, key, modrinth.SearchOptions{Limit: 10})
	if err != nil {
		return nil, err
	}
	if res == nil || len(res.Hits) == 0 {
		return nil, fmt.Errorf("no project matching %q", ref)
	}
	// Prefer an exact slug or title hit, otherwise take the top result.
	want := modmeta.NormalizeName(key)
	for _, h := range res.Hits {
		if modmeta.NormalizeName(h.Slug) == want || modmeta.NormalizeName(h.Title) == want {
			return cli.Project(ctx, h.ProjectID)
		}
	}
	return cli.Project(ctx, res.Hits[0].ProjectID)
}

// bestCompatible finds the newest version matching the target environment.
func bestCompatible(ctx context.Context, cli *modrinth.Client, projectID, mcVersion, loader string, channel modrinth.Channel) (*modrinth.Version, string) {
	opts := modrinth.VersionListOptions{
		Channel: channel,
		Loaders: []string{loader},
	}
	if mcVersion != "" {
		opts.GameVersions = []string{mcVersion}
	}

	vers, err := cli.Versions(ctx, projectID, opts)
	if err != nil {
		return nil, "lookup failed: " + err.Error()
	}
	if len(vers) == 0 {
		// Report the reason precisely: missing mod vs no compatible build.
		if _, perr := cli.Project(ctx, projectID); perr != nil {
			return nil, "project not found"
		}
		if mcVersion == "" {
			return nil, "no versions available"
		}
		return nil, fmt.Sprintf("no %s build for MC %s", loader, mcVersion)
	}

	// Modrinth returns versions newest first, but we sort explicitly so
	// releases win over betas when the channel allows both.
	sorted := append([]modrinth.Version(nil), vers...)
	sortVersionsByPreference(sorted, channel)

	for i := range sorted {
		if _, ok := sorted[i].PrimaryFile(); ok {
			return &sorted[i], ""
		}
	}
	return nil, "no version with a downloadable jar"
}

// downloadInto fetches a file into the instance's mods directory, verifying
// its checksum before the file becomes visible to the loader.
//
// tr may be nil, meaning the transfer is silent.
func downloadInto(ctx context.Context, url, dest, sha512 string, tr *transfer) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return fetchFile(ctx, url, dest, sha512, tr)
}

// shortDigest abbreviates a digest for display. Verification errors carry the
// full value; --verbose lines only need enough to compare against a
// published hash by eye.
func shortDigest(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	if d == "" {
		return "none"
	}
	return d
}
