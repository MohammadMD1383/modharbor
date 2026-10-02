package mrpack

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/MohammadMD1383/modharbor/internal/hashutil"
	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
)

// RequiredMod is one mod an instance needs, plus whatever modharbor could
// learn about it upstream.
//
// A zero ProjectID means the jar is *not* on Modrinth — a private build, a
// CurseForge mirror, or something hand-assembled. Those travel as overrides
// rather than as a download URL, which is the only way the format can carry a
// file nobody can fetch.
type RequiredMod struct {
	// ProjectID is the Modrinth project, empty when unresolved.
	ProjectID string
	// VersionID is the exact Modrinth version, empty when unresolved.
	VersionID string
	// FileName is the jar's name inside the instance's mods directory.
	FileName string
	// SHA1 is the local file's digest, always present.
	SHA1 string
	// Title is the project's display name, empty when unresolved.
	Title string

	// The fields below are export plumbing. All of them were already needed
	// to identify the mod, so they are captured here once instead of being
	// re-requested while the manifest is assembled.

	// path is the jar's absolute location on disk.
	path string
	// file carries the upstream URL, hashes and size.
	file modrinth.File
	// clientSide and serverSide are Modrinth's client_side / server_side
	// declarations, using the same vocabulary as the manifest's `env` map.
	clientSide string
	serverSide string
}

// SourcePath is the mod's location on disk.
func (r RequiredMod) SourcePath() string { return r.path }

// Resolved reports whether Modrinth recognises this mod.
func (r RequiredMod) Resolved() bool { return r.ProjectID != "" && r.VersionID != "" }

// RequiredMods hashes every jar in an instance and identifies it against
// Modrinth.
//
// Identification is per-file and best-effort: a jar that cannot be resolved is
// still returned, with only its local digest filled in, because the caller
// needs to know it exists in order to carry it across as an override. A
// failure to identify is therefore never fatal — an unreachable API just
// produces a pack with more overrides.
func RequiredMods(ctx context.Context, mr *modrinth.Client, instancePath string) ([]RequiredMod, error) {
	modsDir := filepath.Join(instancePath, "mods")
	files, err := instance.ModFiles(modsDir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", modsDir, err)
	}
	if len(files) == 0 {
		return nil, nil
	}

	out := make([]RequiredMod, len(files))
	errs := make([]error, len(files))

	// Hashing is the expensive half of this pass, so the network lookups run
	// alongside it rather than queueing behind it.
	var wg sync.WaitGroup
	for i, path := range files {
		out[i].path = path
		out[i].FileName = filepath.Base(path)

		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			sha1, err := hashutil.SHA1File(path)
			if err != nil {
				errs[i] = fmt.Errorf("hashing %s: %w", filepath.Base(path), err)
				return
			}
			out[i].SHA1 = sha1
			identify(ctx, mr, &out[i])
		}(i, path)
	}
	wg.Wait()

	// A jar we could not read at all is worth reporting; one we merely failed
	// to identify is not, because the export will carry it as an override.
	var firstErr error
	for i := range out {
		if errs[i] != nil && out[i].SHA1 == "" && firstErr == nil {
			firstErr = errs[i]
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}

	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].FileName) < strings.ToLower(out[j].FileName)
	})
	return out, nil
}

// identify fills in the upstream fields of a mod from its digest.
//
// A hash lookup is the only exact method Modrinth offers, so it is the only
// one used here: guessing from a file name would quietly attach the wrong
// project to somebody else's jar.
func identify(ctx context.Context, mr *modrinth.Client, rm *RequiredMod) {
	if mr == nil || rm.SHA1 == "" {
		return
	}
	ver, err := mr.VersionByHash(ctx, rm.SHA1)
	if err != nil {
		// Unknown hash, or the API is unreachable. Either way this mod
		// becomes an override, which is always a correct outcome.
		return
	}
	file, ok := ver.PrimaryFile()
	if !ok {
		return
	}
	rm.ProjectID = ver.ProjectID
	rm.VersionID = ver.ID
	rm.file = file

	proj, err := mr.Project(ctx, ver.ProjectID)
	if err != nil {
		// The manifest is valid without the side metadata; failing the whole
		// export over a missing display name would be a poor trade.
		return
	}
	rm.Title = proj.Title
	rm.clientSide = proj.ClientSide
	rm.serverSide = proj.ServerSide
}

// env renders Modrinth's client_side / server_side declarations into the
// manifest's `env` map, which speaks the same vocabulary.
//
// An empty side means "both": Modrinth documents `required` as applying to
// both environments. Emitting this wrongly makes a launcher skip a
// server-side-only mod, so it is only written when we actually know it.
func (r RequiredMod) env() map[string]string {
	if r.clientSide == "" && r.serverSide == "" {
		return nil
	}
	client, server := r.clientSide, r.serverSide
	if client == "" {
		client = EnvRequired
	}
	if server == "" {
		server = EnvRequired
	}
	return map[string]string{EnvClient: client, EnvServer: server}
}
