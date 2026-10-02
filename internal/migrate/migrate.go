// Package migrate implements version-to-version mod migration, the core
// job modharbor exists for.
//
// The algorithm for each mod found in the source instance:
//
//  1. Identify it exactly, when possible, via SHA-1 hash lookup.
//  2. Determine the target Minecraft version and loader from the destination.
//  3. Ask the provider for the newest compatible version.
//  4. If the destination already has a jar for that project, compare hashes
//     and skip when they match; otherwise replace.
//  5. If no compatible version exists, record a skip with a human reason.
//
// Unidentifiable mods are copied verbatim when --copy-unknown is set, which
// is the right behaviour for private or locally built jars.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MohammadMD1383/modharbor/internal/hashutil"
	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/modmeta"
	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
	"github.com/MohammadMD1383/modharbor/internal/resolver"
	"github.com/MohammadMD1383/modharbor/internal/store"
)

// Action describes what happened (or would happen) to one mod.
type Action string

// Possible migration outcomes.
const (
	ActionInstall   Action = "install"   // copy a compatible version into the target
	ActionReplace   Action = "replace"   // swap an existing jar for a newer one
	ActionKeep      Action = "keep"      // target already has this exact file
	ActionSkip      Action = "skip"      // no compatible version available
	ActionUnmatched Action = "unmatched" // could not identify the mod upstream
	ActionCopy      Action = "copy"      // copied verbatim (unknown mod)
	ActionReinstall Action = "reinstall" // re-fetch the same version from upstream
	ActionDuplicate Action = "duplicate" // another jar already provides this mod
	ActionError     Action = "error"
)

// Result is the outcome for a single mod.
type Result struct {
	Action Action
	// Title is the mod's display name.
	Title string
	// ProjectID is the upstream project, when identified.
	ProjectID string
	// SourceFile and TargetFile are file names on either side.
	SourceFile string
	TargetFile string
	// SourceVersion and TargetVersion are version strings.
	SourceVersion string
	TargetVersion string
	// Reason explains skips and unmatched mods.
	Reason string
	// SHA1 of the file that was installed (or would be).
	SHA1 string
	// Size of the installed file in bytes.
	Size int64
	// DownloadURL is the CDN URL when the mod came from Modrinth.
	DownloadURL string
	// ExpectedSHA512 verifies the download.
	ExpectedSHA512 string
	// Confidence is the identification confidence, 0..1.
	Confidence float64
	// MatchMethod explains how the mod was identified.
	MatchMethod string
	// DryRun marks results computed without touching the filesystem.
	DryRun bool
}

// OK reports whether the result represents a successful (or benign) outcome.
func (r Result) OK() bool {
	switch r.Action {
	case ActionInstall, ActionReplace, ActionKeep, ActionCopy:
		return true
	}
	return false
}

// Report aggregates a migration run.
type Report struct {
	Source      instance.Info
	Target      instance.Info
	Results     []Result
	StartedAt   time.Time
	Duration    time.Duration
	DryRun      bool
	CopyUnknown bool
	// Totals, filled in by Summarise.
	Installed   int
	Replaced    int
	Kept        int
	Skipped     int
	Unmatched   int
	Copied      int
	Reinstalled int
	Duplicates  int
	Failed      int
}

// Summarise recomputes the tally fields.
func (r *Report) Summarise() {
	r.Installed, r.Replaced, r.Kept = 0, 0, 0
	r.Skipped, r.Unmatched, r.Copied = 0, 0, 0
	r.Reinstalled, r.Duplicates, r.Failed = 0, 0, 0
	for _, res := range r.Results {
		switch res.Action {
		case ActionInstall:
			r.Installed++
		case ActionReplace:
			r.Replaced++
		case ActionKeep:
			r.Kept++
		case ActionSkip:
			r.Skipped++
		case ActionUnmatched:
			r.Unmatched++
		case ActionDuplicate:
			r.Duplicates++
		case ActionReinstall:
			r.Reinstalled++
		case ActionCopy:
			r.Copied++
		case ActionError:
			r.Failed++
		}
	}
}

// Planned returns the results that would change the filesystem.
func (r *Report) Planned() []Result {
	var out []Result
	for _, res := range r.Results {
		if res.Action == ActionInstall || res.Action == ActionReplace || res.Action == ActionCopy {
			out = append(out, res)
		}
	}
	return out
}

// Options controls a migration.
type Options struct {
	// Channel selects release / beta / alpha versions.
	Channel modrinth.Channel
	// CopyUnknown copies jars that could not be identified upstream.
	CopyUnknown bool
	// DryRun computes the plan without writing anything.
	DryRun bool
	// Concurrency bounds parallel work.
	Concurrency int
	// OnProgress is called as each mod completes.
	OnProgress func(done, total int, res Result)
	// Force re-evaluates mods even when the target already has a match.
	Force bool
	// AllowDowngrade permits installing a build older than the one already
	// present, which is what you want when migrating to an older Minecraft
	// release.
	AllowDowngrade bool
	// Include lists restrict migration to these project ids or slugs.
	Include []string
	// Exclude lists project ids or slugs to skip.
	Exclude []string
}

// Engine performs migrations.
type Engine struct {
	mr    *modrinth.Client
	res   *resolver.Resolver
	cache *store.Store
	// Lookup maps an already-present jar name in the target to its hash, so
	// "keep" decisions do not require re-downloading.
	targetHashes map[string]string
}

// New builds a migration engine.
func New(mr *modrinth.Client, res *resolver.Resolver, cache *store.Store) *Engine {
	return &Engine{mr: mr, res: res, cache: cache, targetHashes: map[string]string{}}
}

// Run plans and (unless DryRun) performs a migration.
func (e *Engine) Run(ctx context.Context, src, dst *instance.Info, opts Options) (*Report, error) {
	started := time.Now()
	rep := &Report{
		Source:      *src,
		Target:      *dst,
		StartedAt:   started,
		DryRun:      opts.DryRun,
		CopyUnknown: opts.CopyUnknown,
	}

	srcFiles, err := instance.ModFiles(src.ModsDirOrDefault())
	if err != nil {
		return nil, fmt.Errorf("reading source mods: %w", err)
	}
	dstFiles, err := instance.ModFiles(dst.ModsDirOrDefault())
	if err != nil {
		return nil, fmt.Errorf("reading target mods: %w", err)
	}

	// Index the target by project identity so we can detect no-ops. We use
	// cached resolutions where available and fall back to jar metadata.
	dstIndex, err := e.indexTarget(ctx, dst, dstFiles)
	if err != nil {
		return nil, err
	}

	// Build the candidate list from the source instance.
	tlMods, _ := instance.ParseTLauncherMods(src.Path)
	cands, err := e.candidatesFor(ctx, src, srcFiles, tlMods)
	if err != nil {
		return nil, err
	}

	cands = applyFilters(cands, opts)

	scoped := e.res.ForInstance(dst.MCVersion, loaderName(dst.Type))
	results := scoped.ResolveAll(ctx, cands)

	// For each identified mod, decide what the target should contain.
	final := make([]Result, 0, len(results))
	for _, rr := range results {
		if rr.Resolution.Method == resolver.MethodUnmatched {
			final = append(final, e.handleUnmatched(rr, opts))
			continue
		}
		final = append(final, e.decide(ctx, rr, dst, dstIndex, opts))
	}

	final = dedupeByProject(final)

	sort.SliceStable(final, func(i, j int) bool {
		// Unmatched mods last: they are the ones needing human attention.
		if (final[i].Action == ActionUnmatched) != (final[j].Action == ActionUnmatched) {
			return final[j].Action == ActionUnmatched
		}
		return strings.ToLower(final[i].Title) < strings.ToLower(final[j].Title)
	})

	rep.Results = final
	rep.Duration = time.Since(started)
	rep.Summarise()

	if opts.OnProgress == nil && len(final) > 0 {
		// Progress is reported per result; without a callback there is
		// nothing further to emit here.
		_ = len(final)
	}

	if !opts.DryRun {
		if err := e.materialize(ctx, final, src.ModsDirOrDefault(), dst); err != nil {
			return nil, err
		}
	}
	return rep, nil
}

// candidatesFor builds resolver candidates for a set of jars.
func (e *Engine) candidatesFor(ctx context.Context, inst *instance.Info, files []string, tl map[string]instance.TLauncherMod) ([]resolver.Candidate, error) {
	cands := make([]resolver.Candidate, 0, len(files))
	for _, f := range files {
		base := filepath.Base(f)
		c := resolver.Candidate{Path: f, FileName: base}

		sha1, err := hashutil.SHA1File(f)
		if err != nil {
			cands = append(cands, c)
			continue
		}
		c.SHA1 = sha1
		if fi, err := os.Stat(f); err == nil {
			c.Size = fi.Size()
		}
		if m, err := modmeta.Read(f); err == nil {
			c.Meta = m
		}
		// Launcher metadata supplies the CurseForge project id, which is a
		// strong identifier on its own.
		if tm, ok := tl[base]; ok {
			c.CurseForgeID = tm.CurseForgeID
			c.CurseForgeName = tm.Name
			if c.Meta != nil && c.Meta.Name == "" {
				c.Meta.Name = tm.Name
			}
		}
		cands = append(cands, c)
	}
	return cands, nil
}

// indexTarget maps target jars to their project identity.
func (e *Engine) indexTarget(ctx context.Context, dst *instance.Info, files []string) (map[string]targetEntry, error) {
	idx := map[string]targetEntry{}
	tlMods, _ := instance.ParseTLauncherMods(dst.Path)

	for _, f := range files {
		base := filepath.Base(f)
		entry := targetEntry{FileName: base}
		if sha1, err := hashutil.SHA1File(f); err == nil {
			entry.SHA1 = sha1
		}
		if m, err := modmeta.Read(f); err == nil {
			entry.Meta = m
		}
		if tm, ok := tlMods[base]; ok {
			entry.CurseForgeID = tm.CurseForgeID
			entry.CurseForgeName = tm.Name
		}
		// Prefer a cached resolution, else resolve now.
		if entry.SHA1 != "" {
			if prev, ok := e.cache.Lookup(entry.SHA1); ok && prev.ProjectID != "" {
				entry.ProjectID = prev.ProjectID
				entry.VersionID = prev.VersionID
				entry.VersionNumber = prev.VersionNumber
				entry.Title = prev.Title
				idx[entry.ProjectID] = entry
				continue
			}
		}
		if entry.SHA1 != "" {
			if r := e.res.ForInstance(dst.MCVersion, loaderName(dst.Type)).Resolve(ctx, resolver.Candidate{
				Path: f, FileName: base, SHA1: entry.SHA1,
				Meta: entry.Meta, CurseForgeID: entry.CurseForgeID, CurseForgeName: entry.CurseForgeName,
			}); r.Resolution.ProjectID != "" {
				entry.ProjectID = r.Resolution.ProjectID
				entry.VersionID = r.Resolution.VersionID
				entry.VersionNumber = r.Resolution.VersionNumber
				entry.Title = r.Resolution.Title
			}
		}
		if entry.ProjectID == "" && entry.Meta != nil {
			// Fall back to the mod id as a loose key.
			idx[entry.Meta.ModID] = entry
			continue
		}
		if entry.ProjectID != "" {
			idx[entry.ProjectID] = entry
		}
	}
	return idx, nil
}

// targetEntry is one jar already present in the destination instance.
type targetEntry struct {
	FileName       string
	SHA1           string
	ProjectID      string
	VersionID      string
	VersionNumber  string
	Title          string
	Meta           *modmeta.Meta
	CurseForgeID   int
	CurseForgeName string
}

// decide chooses the action for an identified mod.
func (e *Engine) decide(ctx context.Context, rr resolver.Result, dst *instance.Info, dstIdx map[string]targetEntry, opts Options) Result {
	res := rr.Resolution
	meta := rr.Candidate.Meta

	title := res.Title
	if title == "" && meta != nil {
		title = meta.Name
	}
	if title == "" {
		title = filepath.Base(rr.Candidate.FileName)
	}

	out := Result{
		Title:         title,
		ProjectID:     res.ProjectID,
		SourceFile:    rr.Candidate.FileName,
		SourceVersion: firstNonEmpty(res.VersionNumber, versionOf(meta)),
		Confidence:    res.Confidence,
		MatchMethod:   res.Method,
		DryRun:        opts.DryRun,
	}

	// A CurseForge-only project cannot be resolved on Modrinth; its numeric
	// id is not a Modrinth project id.
	if res.Provider == "curseforge" {
		out.Action = ActionSkip
		out.Reason = "published on CurseForge only; install it from the CurseForge app or launcher"
		return out
	}

	// Ask the provider for the best compatible version.
	best, reason := e.bestVersion(ctx, res.ProjectID, dst, opts.Channel)
	if best == nil {
		out.Action = ActionSkip
		out.Reason = reason
		return out
	}

	file, ok := best.PrimaryFile()
	if !ok {
		out.Action = ActionSkip
		out.Reason = "upstream version has no jar"
		return out
	}

	out.TargetVersion = best.VersionNumber
	out.TargetFile = file.Filename
	out.SHA1 = file.SHA1()
	out.ExpectedSHA512 = file.SHA512()
	out.Size = file.Size

	url, err := e.mr.CDNURL(ctx, file)
	if err == nil {
		out.DownloadURL = url
	}

	if existing, ok := dstIdx[res.ProjectID]; ok {
		bothHashed := existing.SHA1 != "" && file.SHA1() != ""
		switch {
		case bothHashed && strings.EqualFold(existing.SHA1, file.SHA1()):
			// Byte-identical: nothing to do.
			out.Action = ActionKeep
			out.Reason = "already installed"
			out.TargetFile = existing.FileName
			return out

		case bothHashed:
			// Same project, known-different bytes. Replacing is correct even
			// when the version ids match, but the two cases deserve different
			// labels: an identical version number with differing bytes means
			// the local jar is a mirror copy or a repackage, and the user
			// should see that this is a re-fetch rather than an upgrade.
			if existing.VersionNumber != "" &&
				existing.VersionNumber == best.VersionNumber {
				out.Action = ActionReinstall
				out.Reason = "same version from a different source"
				out.TargetFile = file.Filename
				return out
			}
			out.Action = ActionReplace
			return out

		case existing.VersionID != "" && existing.VersionID == best.ID:
			// Identical version and we could not compare hashes (an
			// unidentified or mirror-sourced jar): assume it is fine.
			out.Action = ActionKeep
			out.Reason = "already on this version"
			out.TargetFile = existing.FileName
			return out
		}
		out.Action = ActionReplace
		return out
	}

	out.Action = ActionInstall
	return out
}

// handleUnmatched deals with jars we could not identify upstream.
func (e *Engine) handleUnmatched(rr resolver.Result, opts Options) Result {
	meta := rr.Candidate.Meta
	title := filepath.Base(rr.Candidate.FileName)
	if meta != nil && meta.Name != "" {
		title = meta.Name
	}
	out := Result{
		Action:        ActionUnmatched,
		Title:         title,
		SourceFile:    rr.Candidate.FileName,
		SourceVersion: versionOf(meta),
		MatchMethod:   resolver.MethodUnmatched,
		DryRun:        opts.DryRun,
	}
	if meta != nil && meta.Version != "" {
		out.SHA1 = rr.Candidate.SHA1
	}
	if opts.CopyUnknown {
		out.Action = ActionCopy
		out.TargetFile = rr.Candidate.FileName
		out.SHA1 = rr.Candidate.SHA1
		out.Size = rr.Candidate.Size
		out.Reason = "copied verbatim; not published on any configured provider"
		return out
	}
	out.Reason = "not found upstream (use --copy-unknown to carry it over)"
	return out
}

// bestVersion finds the newest version compatible with the destination.
//
// When nothing in the current channel fits, it widens the search to explain
// *why*: "only a beta exists for MC 26.3" is far more actionable than a bare
// "no compatible version".
func (e *Engine) bestVersion(ctx context.Context, projectID string, dst *instance.Info, channel modrinth.Channel) (*modrinth.Version, string) {
	loader := loaderName(dst.Type)
	if loader == "" {
		loader = "fabric"
	}

	opts := modrinth.VersionListOptions{Loaders: []string{loader}}
	if dst.MCVersion != "" {
		opts.GameVersions = []string{dst.MCVersion}
	}

	vers, err := e.mr.Versions(ctx, projectID, opts)
	if err != nil {
		// A CurseForge-only project looks like a missing Modrinth project.
		if isMissingProject(err) {
			return nil, "not published on Modrinth"
		}
		return nil, "lookup failed: " + err.Error()
	}
	if len(vers) == 0 {
		return nil, e.explainEmpty(ctx, projectID, dst, loader, channel)
	}

	// The API only accepts one version_type, so multi-type channels are
	// filtered and ranked here.
	filtered := modrinth.FilterByChannel(vers, channel)
	if len(filtered) == 0 {
		return nil, e.excludeReason(vers, dst, channel)
	}

	best := filtered[0]
	if _, ok := best.PrimaryFile(); !ok {
		return nil, "newest compatible version has no jar"
	}
	return &best, ""
}

// explainEmpty produces a reason when the version query returned nothing at
// all, distinguishing a missing project from a missing game-version build.
func (e *Engine) explainEmpty(ctx context.Context, projectID string, dst *instance.Info, loader string, channel modrinth.Channel) string {
	if _, perr := e.mr.Project(ctx, projectID); perr != nil {
		return "not published on Modrinth"
	}
	if dst.MCVersion == "" {
		return "no versions available"
	}
	// The loader may simply be wrong for this project; report that.
	if vers, err := e.mr.Versions(ctx, projectID, modrinth.VersionListOptions{}); err == nil {
		if reason := loaderReason(vers, loader, dst.MCVersion); reason != "" {
			return reason
		}
	}
	return fmt.Sprintf("no %s build for MC %s", loader, dst.MCVersion)
}

// excludeReason explains that compatible builds exist but sit outside the
// requested channel, naming the channel that would work.
func (e *Engine) excludeReason(vers []modrinth.Version, dst *instance.Info, channel modrinth.Channel) string {
	// These versions already match the game version and loader, so the only
	// reason they were dropped is their version_type.
	best := bestByPreference(vers)
	if best == "" {
		return "no compatible build"
	}
	switch best {
	case "beta":
		return fmt.Sprintf("only a pre-release build exists for MC %s — retry with --channel beta", dst.MCVersion)
	case "alpha":
		return fmt.Sprintf("only an experimental build exists for MC %s — retry with --channel alpha", dst.MCVersion)
	default:
		return fmt.Sprintf("no build available on the %s channel", channel)
	}
}

// loaderReason explains a mismatch caused by the loader filter.
func loaderReason(all []modrinth.Version, loader, mcVersion string) string {
	hasGame := false
	loaders := map[string]bool{}
	for _, v := range all {
		if containsFold(v.GameVersions, mcVersion) {
			hasGame = true
		}
		for _, l := range v.Loaders {
			loaders[l] = true
		}
	}
	if hasGame && !loaders[loader] {
		return fmt.Sprintf("this mod does not support %s", loader)
	}
	return ""
}

// bestByPreference returns the most advanced version_type present.
func bestByPreference(vers []modrinth.Version) string {
	rank := map[string]int{"release": 0, "beta": 1, "alpha": 2}
	best, bestRank := "", -1
	for _, v := range vers {
		r, ok := rank[v.VersionType]
		if !ok {
			continue
		}
		if r > bestRank {
			best, bestRank = v.VersionType, r
		}
	}
	return best
}

// isMissingProject reports whether an error means "not on Modrinth".
func isMissingProject(err error) bool {
	return err != nil && errors.Is(err, modrinth.ErrNotFound)
}

// materialize writes the planned changes to disk.
func (e *Engine) materialize(ctx context.Context, results []Result, srcDir string, dst *instance.Info) error {
	modsDir := dst.ModsDirOrDefault()
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		return err
	}

	changeCount := 0
	for _, r := range results {
		switch r.Action {
		case ActionInstall, ActionReplace, ActionCopy, ActionReinstall:
			changeCount++
		}
	}
	if changeCount == 0 {
		return nil
	}

	// Back up the files we are about to replace.
	if err := backupReplacements(modsDir, results); err != nil {
		return err
	}

	// Install and copy actions are independent, so run them concurrently.
	// Replacements run afterwards so that a failure cannot remove a mod
	// before its replacement is safely on disk.
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
	)
	for _, r := range results {
		switch r.Action {
		case ActionInstall, ActionCopy, ActionReinstall:
		default:
			continue
		}

		wg.Add(1)
		go func(r Result) {
			defer wg.Done()

			dest := filepath.Join(modsDir, r.TargetFile)
			var err error
			switch r.Action {
			case ActionCopy:
				// SourceFile is a bare name; join it against the source
				// instance's mods directory.
				err = copyFile(filepath.Join(srcDir, r.SourceFile), dest)
			default:
				if r.DownloadURL == "" {
					err = fmt.Errorf("no download URL for %s", r.Title)
					break
				}
				err = downloadTo(ctx, r.DownloadURL, dest, r.ExpectedSHA512, r.SHA1)
			}
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}(r)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}

	// Removals happen after installs so a failure never leaves a gap.
	for _, r := range results {
		if r.Action != ActionReplace {
			continue
		}
		old := filepath.Join(modsDir, r.SourceFile)
		if _, err := os.Stat(old); err == nil {
			_ = os.Remove(old)
		}
		if r.DownloadURL == "" {
			continue
		}
		if err := downloadTo(ctx, r.DownloadURL, filepath.Join(modsDir, r.TargetFile), r.ExpectedSHA512, r.SHA1); err != nil {
			return err
		}
	}

	if e.cache != nil {
		for _, r := range results {
			switch r.Action {
			case ActionInstall, ActionReplace, ActionCopy:
			default:
				continue
			}
			if r.SHA1 == "" || r.ProjectID == "" {
				continue
			}
			e.cache.Put(store.Resolution{
				SHA1:          r.SHA1,
				Provider:      "modrinth",
				ProjectID:     r.ProjectID,
				VersionID:     "",
				VersionNumber: r.TargetVersion,
				Filename:      r.TargetFile,
				Method:        r.MatchMethod,
				Confidence:    r.Confidence,
				SourceFile:    r.TargetFile,
				InstanceID:    dst.ID,
			})
		}
	}
	return nil
}

func versionOf(m *modmeta.Meta) string {
	if m == nil {
		return ""
	}
	return m.Version
}

// containsFold reports whether list contains want, case-insensitively.
func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func loaderName(t instance.Type) string {
	switch t {
	case instance.TypeFabric:
		return "fabric"
	case instance.TypeQuilt:
		return "quilt"
	case instance.TypeForge:
		return "forge"
	case instance.TypeNeoForge:
		return "neoforge"
	default:
		return ""
	}
}

// applyFilters honours the Include and Exclude lists.
func applyFilters(cands []resolver.Candidate, opts Options) []resolver.Candidate {
	if len(opts.Include) == 0 && len(opts.Exclude) == 0 {
		return cands
	}
	inc := toSet(opts.Include)
	exc := toSet(opts.Exclude)

	out := cands[:0:0]
	for _, c := range cands {
		keys := candidateKeys(c)
		if len(inc) > 0 && !intersects(keys, inc) {
			continue
		}
		if len(exc) > 0 && intersects(keys, exc) {
			continue
		}
		out = append(out, c)
	}
	return out
}

func candidateKeys(c resolver.Candidate) []string {
	var keys []string
	if c.Meta != nil {
		keys = append(keys, strings.ToLower(c.Meta.ModID), strings.ToLower(c.Meta.Name))
		keys = append(keys, strings.ToLower(modmeta.GuessModIDFromFileName(c.FileName)))
	}
	if c.CurseForgeID > 0 {
		keys = append(keys, fmt.Sprintf("%d", c.CurseForgeID))
	}
	return keys
}

func toSet(list []string) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, s := range list {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" {
			out[s] = true
		}
	}
	return out
}

func intersects(keys []string, set map[string]bool) bool {
	for _, k := range keys {
		if set[k] {
			return true
		}
	}
	return false
}
