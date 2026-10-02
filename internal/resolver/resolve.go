// Package resolver identifies an installed mod jar against the upstream
// providers.
//
// Identification proceeds through a chain of increasingly fuzzy strategies,
// each tagged with a confidence score. Exact matches are always preferred.
//
//  1. hash        SHA-1 -> Modrinth /version_file/{sha1}   (confidence 1.00)
//  2. curseforge  launcher-recorded project id              (confidence 0.95)
//  3. slug        mod id or title -> candidate slugs          (confidence 0.90)
//  4. name        Modrinth search, ranked by name similarity  (confidence 0.60-0.90)
//
// Mirrors are the reason steps 2-4 exist. A jar downloaded from CurseForge
// has identical contents to the Modrinth copy in spirit but a different
// SHA-1, so hash lookup fails and we must fall back to name matching.
package resolver

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MohammadMD1383/modharbor/internal/hashutil"
	"github.com/MohammadMD1383/modharbor/internal/modmeta"
	"github.com/MohammadMD1383/modharbor/internal/provider/curseforge"
	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
	"github.com/MohammadMD1383/modharbor/internal/store"
)

// Method names the strategy that produced a resolution.
const (
	MethodHash       = "hash"
	MethodCurseForge = "curseforge-id"
	MethodSlug       = "slug"
	MethodName       = "name"
	MethodFilename   = "filename"
	MethodManual     = "manual"
	MethodUnmatched  = "unmatched"
)

// Confidence floors per method.
const (
	confHash       = 1.0
	confCurseForge = 0.95
	confSlug       = 0.90
	confNameHigh   = 0.80
	confNameLow    = 0.60
)

// Candidate is a jar awaiting identification.
type Candidate struct {
	Path     string
	FileName string
	SHA1     string
	Size     int64
	Meta     *modmeta.Meta
	// CurseForgeID is a project id discovered from launcher metadata.
	CurseForgeID int
	// CurseForgeName is the launcher's display name for the mod.
	CurseForgeName string
}

// Result is the outcome of identifying one candidate.
type Result struct {
	Candidate  Candidate
	Resolution store.Resolution
	// Err records why identification failed, for diagnostics.
	Err error
}

// Resolver identifies jars using Modrinth and, optionally, CurseForge.
type Resolver struct {
	mr       *modrinth.Client
	cf       *curseforge.Client
	cache    *store.Store
	progress func(done, total int)
	// mcVersion and loader restrict version selection during slug probing.
	// They are empty for plain identification.
	mcVersion string
	loader    string

	// versions memoises full version listings used by the corroboration
	// checks, keyed by project id. It is a pointer so scoped resolvers can
	// share it without copying a mutex.
	versionsMu *sync.Mutex
	versions   map[string][]modrinth.Version
}

// Options configures a Resolver.
type Options struct {
	Modrinth   *modrinth.Client
	CurseForge *curseforge.Client
	Cache      *store.Store
	// Progress receives (done, total) updates.
	Progress func(done, total int)
}

// New builds a Resolver.
func New(opts Options) *Resolver {
	var mu sync.Mutex
	return &Resolver{
		mr:         opts.Modrinth,
		cf:         opts.CurseForge,
		cache:      opts.Cache,
		progress:   opts.Progress,
		versions:   map[string][]modrinth.Version{},
		versionsMu: &mu,
	}
}

// ForInstance returns a resolver scoped to an instance, used when probing
// candidate projects for the versions it actually ships.
//
// A new struct is built rather than copying r, which would copy its mutex.
// The version cache is shared because it is keyed by project id and safe to
// share across scopes.
func (r *Resolver) ForInstance(mcVersion, loader string) *Resolver {
	return &Resolver{
		mr:         r.mr,
		cf:         r.cf,
		cache:      r.cache,
		progress:   r.progress,
		mcVersion:  mcVersion,
		loader:     loader,
		versions:   r.versions,
		versionsMu: r.versionsMu,
	}
}

// ResolveAll identifies a batch of jars, in parallel but with bounded
// concurrency so we stay well inside API rate limits.
func (r *Resolver) ResolveAll(ctx context.Context, cands []Candidate) []Result {
	const workers = 6
	results := make([]Result, len(cands))
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	done := 0
	var mu sync.Mutex

	for i, c := range cands {
		wg.Add(1)
		go func(i int, c Candidate) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = Result{Candidate: c, Err: ctx.Err()}
				return
			}
			defer func() { <-sem }()

			res := r.Resolve(ctx, c)
			results[i] = res

			mu.Lock()
			done++
			if r.progress != nil {
				r.progress(done, len(cands))
			}
			mu.Unlock()
		}(i, c)
	}
	wg.Wait()
	return results
}

// Resolve identifies a single jar.
func (r *Resolver) Resolve(ctx context.Context, c Candidate) Result {
	res := Result{Candidate: c}

	// A manual override always wins.
	if c.SHA1 != "" && r.cache != nil {
		if prev, ok := r.cache.Lookup(c.SHA1); ok && prev.Method == MethodManual {
			res.Resolution = prev
			return res
		}
	}

	if c.SHA1 != "" && r.cache != nil {
		if prev, ok := r.cache.Lookup(c.SHA1); ok && prev.Method != "" && prev.ProjectID != "" {
			// Reuse a cached resolution, but refresh the source file name.
			prev.SourceFile = c.FileName
			res.Resolution = prev
			return res
		}
	}

	// 1. Exact hash lookup.
	if v := r.byHash(ctx, c); v != nil {
		res.Resolution = *v
		r.persist(res.Resolution)
		return res
	}

	// 2. CurseForge project id recorded by the launcher.
	if v := r.byCurseForgeID(ctx, c); v != nil {
		res.Resolution = *v
		r.persist(res.Resolution)
		return res
	}

	// 3. Direct slug probing derived from the mod id or title.
	if v := r.bySlug(ctx, c); v != nil {
		res.Resolution = *v
		r.persist(res.Resolution)
		return res
	}

	// 4. Name search.
	if v := r.byNameSearch(ctx, c); v != nil {
		res.Resolution = *v
		r.persist(res.Resolution)
		return res
	}

	res.Resolution = store.Resolution{
		SHA1:       c.SHA1,
		SourceFile: c.FileName,
		Method:     MethodUnmatched,
		ModIDs:     modIDsOf(c),
	}
	res.Err = ErrUnmatched
	return res
}

// ErrUnmatched signals that no provider could identify the jar.
var ErrUnmatched = errUnmatched{}

type errUnmatched struct{}

func (errUnmatched) Error() string { return "no upstream match found" }

// byHash performs an exact SHA-1 lookup on both providers.
func (r *Resolver) byHash(ctx context.Context, c Candidate) *store.Resolution {
	if c.SHA1 == "" || r.mr == nil {
		return nil
	}
	v, err := r.mr.VersionByHash(ctx, c.SHA1)
	if err != nil || v == nil {
		return nil
	}
	out := store.Resolution{
		SHA1:          c.SHA1,
		Provider:      "modrinth",
		ProjectID:     v.ProjectID,
		VersionID:     v.ID,
		VersionNumber: v.VersionNumber,
		Method:        MethodHash,
		Confidence:    confHash,
		SourceFile:    c.FileName,
		InstanceID:    instanceOf(c),
		ModIDs:        modIDsOf(c),
	}
	if f, ok := v.PrimaryFile(); ok {
		out.Filename = f.Filename
	}
	// The version payload carries no title, so fetch the project for a human
	// readable name. It is cached, and a failure here is not fatal.
	if p, perr := r.mr.Project(ctx, v.ProjectID); perr == nil && p != nil {
		out.Slug = p.Slug
		out.Title = p.Title
	}
	if r.mcVersion != "" && !v.Supports(r.mcVersion, r.loader) {
		// The hash matched but the version is for another Minecraft
		// release. Still useful for migration, so keep it but flag it by
		// lowering confidence.
		out.Confidence = confHash - 0.05
	}
	return &out
}

// byCurseForgeID resolves via a CurseForge project id discovered in
// launcher metadata, then finds the equivalent Modrinth project.
func (r *Resolver) byCurseForgeID(ctx context.Context, c Candidate) *store.Resolution {
	if c.CurseForgeID <= 0 {
		return nil
	}
	name := c.CurseForgeName
	if name == "" && c.Meta != nil {
		name = c.Meta.Name
	}

	// Prefer a Modrinth project whose name matches the CurseForge one.
	if r.mr != nil && name != "" {
		proj := r.findProjectByName(ctx, name)
		// The launcher's display name is a weak key: "MoreTools+" also
		// matches the unrelated "More Tools (Polymer)" project by
		// containment. Apply the same corroboration every other path gets,
		// or a private mod gets silently mapped to a stranger's project.
		if proj != nil && !r.versionCorroborated(ctx, c, proj) {
			proj = nil
		}
		if proj != nil {
			out := &store.Resolution{
				SHA1:       c.SHA1,
				Provider:   "modrinth",
				ProjectID:  proj.ID,
				Slug:       proj.Slug,
				Title:      proj.Title,
				Method:     MethodCurseForge,
				Confidence: confCurseForge,
				SourceFile: c.FileName,
				InstanceID: instanceOf(c),
				ModIDs:     modIDsOf(c),
			}
			if v := r.latestFor(ctx, proj.ID); v != nil {
				out.VersionID = v.ID
				out.VersionNumber = v.VersionNumber
				if f, ok := v.PrimaryFile(); ok {
					out.Filename = f.Filename
				}
			}
			return out
		}
	}

	// No Modrinth equivalent. Record the CurseForge identity so the mod is
	// still tracked, and mark it as CurseForge-only so callers report it
	// accurately instead of probing Modrinth with a numeric id.
	return &store.Resolution{
		SHA1:       c.SHA1,
		Provider:   "curseforge",
		ProjectID:  itoa(c.CurseForgeID),
		Title:      name,
		Method:     MethodCurseForge,
		Confidence: confCurseForge,
		SourceFile: c.FileName,
		InstanceID: instanceOf(c),
		ModIDs:     modIDsOf(c),
	}
}

// bySlug probes Modrinth project slugs derived from the mod id and title.
func (r *Resolver) bySlug(ctx context.Context, c Candidate) *store.Resolution {
	if r.mr == nil {
		return nil
	}
	for _, slug := range candidateSlugs(c) {
		proj, err := r.mr.Project(ctx, slug)
		if err != nil || proj == nil {
			continue
		}
		// Guard against a slug collision producing an obviously wrong
		// project: require the names to be compatible.
		if !namesCompatible(c, proj) {
			continue
		}
		// A matching slug is not proof. Another mod may already own that
		// slug, and a private or locally built jar will happily resolve to
		// it. The version the jar declares is the tie-breaker.
		if !r.versionCorroborated(ctx, c, proj) {
			continue
		}
		out := &store.Resolution{
			SHA1:       c.SHA1,
			Provider:   "modrinth",
			ProjectID:  proj.ID,
			Slug:       proj.Slug,
			Title:      proj.Title,
			Method:     MethodSlug,
			Confidence: confSlug,
			SourceFile: c.FileName,
			InstanceID: instanceOf(c),
			ModIDs:     modIDsOf(c),
		}
		if v := r.latestFor(ctx, proj.ID); v != nil {
			out.VersionID = v.ID
			out.VersionNumber = v.VersionNumber
			if f, ok := v.PrimaryFile(); ok {
				out.Filename = f.Filename
			}
		}
		return out
	}
	return nil
}

// byNameSearch falls back to a Modrinth search ranked by name similarity.
//
// Search is the weakest strategy, so a match must be corroborated before we
// trust it. Without that check a private mod named "More Tools" happily
// matched an unrelated published project called "More Tools (Polymer)", and
// migration would then install a stranger's mod. We require either an exact
// name match or a version that plausibly belongs to the candidate project.
func (r *Resolver) byNameSearch(ctx context.Context, c Candidate) *store.Resolution {
	if r.mr == nil {
		return nil
	}
	queries := searchQueries(c)
	for _, q := range queries {
		res, err := r.mr.Search(ctx, q, modrinth.SearchOptions{
			Limit: 10,
			// Do not filter by Minecraft version here: a mod may exist
			// only for older releases and we still want to identify it so
			// the user can see "no compatible version".
		})
		if err != nil || res == nil {
			continue
		}
		best := rankHits(c, res.Hits)
		if best == nil {
			continue
		}
		proj, err := r.mr.Project(ctx, best.Hit.ProjectID)
		if err != nil || proj == nil {
			continue
		}

		if !r.trustworthyNameMatch(ctx, c, proj, best.score) {
			continue
		}
		if !r.versionCorroborated(ctx, c, proj) {
			continue
		}

		out := &store.Resolution{
			SHA1:       c.SHA1,
			Provider:   "modrinth",
			ProjectID:  proj.ID,
			Slug:       proj.Slug,
			Title:      proj.Title,
			Method:     MethodName,
			Confidence: best.score,
			SourceFile: c.FileName,
			InstanceID: instanceOf(c),
			ModIDs:     modIDsOf(c),
		}
		if v := r.latestFor(ctx, proj.ID); v != nil {
			out.VersionID = v.ID
			out.VersionNumber = v.VersionNumber
			if f, ok := v.PrimaryFile(); ok {
				out.Filename = f.Filename
			}
		}
		return out
	}
	return nil
}

// trustworthyNameMatch decides whether a fuzzy name match is safe to accept.
func (r *Resolver) trustworthyNameMatch(ctx context.Context, c Candidate, proj *modrinth.Project, score float64) bool {
	// An exact slug or title match needs no further evidence.
	if score >= confSlug {
		return true
	}
	// Otherwise the match rests on containment, which is exactly where a
	// different mod with a similar name slips through. Demand corroboration
	// from the version the jar declares.
	local := ""
	if c.Meta != nil {
		local = c.Meta.Version
	}
	if local == "" {
		return false
	}
	return r.versionPlausible(ctx, proj.ID, local)
}

// versionCorroborated reports whether the candidate may legitimately belong to
// proj.
//
// Name and slug agreement alone is not enough. Two unrelated mods can share a
// name and a version string: a private "More Tools" 1.0.0 jar looks identical
// by name to an unrelated published "More Tools (Polymer)" 1.0.0, and
// migrating on name alone would install a stranger's mod.
//
// So when the project does publish a version with the same numeric core, we
// additionally compare file sizes. Repackaging and mirroring change a jar's
// bytes but not its size by an order of magnitude, whereas two genuinely
// different mods of the same name usually differ substantially. When the
// project has no such version, there is nothing to compare and the match is
// allowed through.
func (r *Resolver) versionCorroborated(ctx context.Context, c Candidate, proj *modrinth.Project) bool {
	// Hard veto first: a jar that declares Fabric cannot be a Forge-only
	// project, whatever the names look like. This alone rejects the
	// "MoreTools+" project, which is Forge/NeoForge only.
	if !loaderPlausible(c, proj) {
		return false
	}

	local := ""
	if c.Meta != nil {
		local = c.Meta.Version
	}
	if local == "" {
		// No version evidence either way; the name match has to carry it.
		return true
	}

	versions, err := r.versionsCached(ctx, proj.ID)
	if err != nil || len(versions) == 0 {
		return false
	}

	want := normaliseVersion(local)
	core := numericCore(local)

	var sameCore []modrinth.Version
	for _, v := range versions {
		if want != "" && normaliseVersion(v.VersionNumber) == want {
			sameCore = append(sameCore, v)
			continue
		}
		if core != "" && numericCore(v.VersionNumber) == core {
			sameCore = append(sameCore, v)
		}
	}
	if len(sameCore) == 0 {
		// The project never published this exact version, which is normal
		// for mirror-sourced jars where upstream renumbered.
		return true
	}

	if c.Size <= 0 {
		return true
	}
	for _, v := range sameCore {
		f, ok := v.PrimaryFile()
		if !ok || f.Size <= 0 {
			continue
		}
		if sizesAgree(c.Size, f.Size) {
			return true
		}
	}
	return false
}

// sizesAgree reports whether two file sizes are plausibly the same artifact.
//
// A factor of two absorbs repackaging (shading, zip alignment, metadata
// differences) while still separating unrelated mods that merely share a
// name and version string.
func sizesAgree(a, b int64) bool {
	if a <= 0 || b <= 0 {
		return true
	}
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	return float64(hi) <= float64(lo)*sizeTolerance
}

// sizeTolerance is the maximum accepted size ratio between two builds that
// are considered the same artifact.
const sizeTolerance = 2.0

// loaderPlausible vetoes a match when the jar's declared loader is not among
// the project's loaders.
//
// This is a hard constraint rather than a hint: a Fabric jar cannot be a
// Forge-only build, and projects frequently exist solely to host builds for
// one loader. It is also free, since the project payload already lists them.
func loaderPlausible(c Candidate, proj *modrinth.Project) bool {
	if c.Meta == nil || len(proj.Loaders) == 0 {
		return true
	}
	want := modrinthLoader(c.Meta.Loader)
	if want == "" {
		return true
	}
	for _, l := range proj.Loaders {
		if strings.EqualFold(l, want) {
			return true
		}
	}
	return false
}

// modrinthLoader maps a jar's declared loader to a Modrinth loader tag.
func modrinthLoader(l modmeta.Loader) string {
	switch l {
	case modmeta.LoaderFabric:
		return "fabric"
	case modmeta.LoaderQuilt:
		return "quilt"
	case modmeta.LoaderForge:
		return "forge"
	case modmeta.LoaderNeoForge:
		return "neoforge"
	default:
		return ""
	}
}

// versionPlausible reports whether a project has ever published a version
// whose number contains the local version, ignoring build metadata.
//
// Modrinth version numbers are decorated ("mc26.2-0.9.1-fabric", "0.154.2+26.2",
// "fabric-26.3-2.3.8"), so containment rather than equality is the right test.
func (r *Resolver) versionPlausible(ctx context.Context, projectID, localVersion string) bool {
	versions, err := r.versionsCached(ctx, projectID)
	if err != nil || len(versions) == 0 {
		return false
	}
	want := normaliseVersion(localVersion)
	if want == "" {
		return false
	}
	core := numericCore(localVersion)
	for _, v := range versions {
		if normaliseVersion(v.VersionNumber) == want {
			return true
		}
		if core != "" && numericCore(v.VersionNumber) == core {
			return true
		}
	}
	return false
}

// versionsCached lists a project's versions, memoised for the lifetime of a
// resolver. Corroboration may ask for the same project's versions several
// times while resolving one mods folder.
func (r *Resolver) versionsCached(ctx context.Context, projectID string) ([]modrinth.Version, error) {
	r.versionsMu.Lock()
	if v, ok := r.versions[projectID]; ok {
		r.versionsMu.Unlock()
		return v, nil
	}
	r.versionsMu.Unlock()

	vs, err := r.mr.Versions(ctx, projectID, modrinth.VersionListOptions{})

	r.versionsMu.Lock()
	if err == nil {
		r.versions[projectID] = vs
	}
	r.versionsMu.Unlock()
	return vs, err
}

// normaliseVersion lowercases a version string for comparison.
func normaliseVersion(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, "_", "-")
	v = strings.ReplaceAll(v, " ", "")
	return v
}

// numericCore extracts the leading dotted-numeric run of a version string.
func numericCore(v string) string {
	v = normaliseVersion(v)
	re := regexp.MustCompile(`\d+(?:\.\d+)+`)
	return re.FindString(v)
}

// findProjectByName searches for a project whose title or slug matches the
// supplied name.
func (r *Resolver) findProjectByName(ctx context.Context, name string) *modrinth.Project {
	want := modmeta.NormalizeName(name)
	if want == "" {
		return nil
	}
	res, err := r.mr.Search(ctx, name, modrinth.SearchOptions{Limit: 8})
	if err != nil || res == nil {
		return nil
	}
	// Direct slug hit first.
	for _, h := range res.Hits {
		if modmeta.NormalizeName(h.Slug) == want {
			if p, err := r.mr.Project(ctx, h.ProjectID); err == nil {
				return p
			}
		}
	}
	// Then exact normalised title, then containment.
	for _, h := range res.Hits {
		if modmeta.NormalizeName(h.Title) == want {
			if p, err := r.mr.Project(ctx, h.ProjectID); err == nil {
				return p
			}
		}
	}
	for _, h := range res.Hits {
		tn, sn := modmeta.NormalizeName(h.Title), modmeta.NormalizeName(h.Slug)
		if strings.Contains(tn, want) || strings.Contains(want, tn) ||
			strings.Contains(sn, want) || strings.Contains(want, sn) {
			if p, err := r.mr.Project(ctx, h.ProjectID); err == nil {
				return p
			}
		}
	}
	return nil
}

// latestFor returns the newest version of a project matching the resolver's
// Minecraft version and loader scope, or nil when unconstrained.
func (r *Resolver) latestFor(ctx context.Context, projectID string) *modrinth.Version {
	if r.mcVersion == "" && r.loader == "" {
		return nil
	}
	vers, err := r.mr.Versions(ctx, projectID, modrinth.VersionListOptions{
		GameVersions: nonEmpty([]string{r.mcVersion}),
		Loaders:      nonEmpty([]string{r.loader}),
	})
	if err != nil || len(vers) == 0 {
		return nil
	}
	sortVersionsNewestFirst(vers)
	return &vers[0]
}

func nonEmpty(s []string) []string {
	out := s[:0:0]
	for _, v := range s {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func sortVersionsNewestFirst(v []modrinth.Version) {
	sort.SliceStable(v, func(i, j int) bool {
		if !v[i].DatePublished.Equal(v[j].DatePublished) {
			return v[i].DatePublished.After(v[j].DatePublished)
		}
		return v[i].ID < v[j].ID
	})
}

// persist writes a resolution to the cache, unless it is a weak match we do
// not want to pin.
func (r *Resolver) persist(res store.Resolution) {
	if r.cache == nil {
		return
	}
	// Do not cache low-confidence name matches: they are cheap to redo and
	// we would rather re-check them once the user provides more context.
	if res.Confidence < confNameHigh {
		return
	}
	r.cache.Put(res)
}

// ─── matching heuristics ────────────────────────────────────────────────────

// rankedHit pairs a search hit with its similarity score.
type rankedHit struct {
	Hit   modrinth.Hit
	score float64
}

// rankHits picks the most plausible search hit for a candidate.
func rankHits(c Candidate, hits []modrinth.Hit) *rankedHit {
	var ranked []rankedHit
	for _, h := range hits {
		s := similarity(c, h)
		if s > 0 {
			ranked = append(ranked, rankedHit{Hit: h, score: s})
		}
	}
	if len(ranked) == 0 {
		return nil
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].Hit.Downloads > ranked[j].Hit.Downloads
	})
	best := ranked[0]
	return &best
}

// similarity scores how well a search hit matches the candidate, 0..1.
func similarity(c Candidate, h modrinth.Hit) float64 {
	var names []string
	if c.Meta != nil {
		if c.Meta.Name != "" {
			names = append(names, c.Meta.Name)
		}
		if c.Meta.ModID != "" {
			names = append(names, c.Meta.ModID)
		}
	}
	if c.CurseForgeName != "" {
		names = append(names, c.CurseForgeName)
	}
	// The file name stem is a weak signal; use it only as a last resort.
	if len(names) == 0 {
		names = append(names, modmeta.GuessModIDFromFileName(c.FileName))
	}

	hs := []string{h.Title, h.Slug}
	best := 0.0
	for _, n := range names {
		nn := modmeta.NormalizeName(n)
		if nn == "" {
			continue
		}
		for _, s := range hs {
			sn := modmeta.NormalizeName(s)
			switch {
			case sn == nn:
				best = maxf(best, confSlug)
			case strings.Contains(sn, nn) || strings.Contains(nn, sn):
				// Containment is a strong signal when the shorter string is
				// substantial, e.g. "yetanotherconfiglib" inside
				// "yetanotherconfiglibyacl".
				best = maxf(best, confNameHigh)
			default:
				if v := jaccard(nn, sn); v >= 0.75 {
					best = maxf(best, confNameLow+v*confNameHigh)
				}
			}
		}
	}
	return best
}

// namesCompatible reports whether a candidate and project plausibly refer to
// the same mod. Used to veto bad slug probes.
func namesCompatible(c Candidate, p *modrinth.Project) bool {
	var names []string
	if c.Meta != nil {
		if c.Meta.Name != "" {
			names = append(names, c.Meta.Name)
		}
		if c.Meta.ModID != "" {
			names = append(names, c.Meta.ModID)
		}
	}
	if c.CurseForgeName != "" {
		names = append(names, c.CurseForgeName)
	}
	if len(names) == 0 {
		// No usable signal: accept the slug hit.
		return true
	}
	targets := []string{p.Title, p.Slug, p.ID}
	for _, n := range names {
		nn := modmeta.NormalizeName(n)
		if nn == "" {
			continue
		}
		for _, t := range targets {
			tn := modmeta.NormalizeName(t)
			if tn == nn || strings.Contains(tn, nn) || strings.Contains(nn, tn) {
				return true
			}
		}
	}
	return false
}

// candidateSlugs derives plausible Modrinth slugs from a jar.
//
// Slugs are lowercase, hyphen-separated and usually equal to the mod's
// display name ("inventory-profiles-next") or its id ("libipn").
func candidateSlugs(c Candidate) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	var sources []string
	if c.Meta != nil {
		sources = append(sources, c.Meta.Name, c.Meta.ModID)
	}
	if c.CurseForgeName != "" {
		sources = append(sources, c.CurseForgeName)
	}

	for _, s := range sources {
		// The name itself, lowercased with spaces to hyphens.
		add(strings.ToLower(strings.TrimSpace(s)))
		// Compact form with no separators ("inventoryprofilesnext").
		add(modmeta.NormalizeName(s))
		// Hyphenated form of a CamelCase name ("yet-another-config-lib").
		add(hyphenate(s))
	}

	return out
}

// hyphenate converts a display name into a Modrinth-style slug: lowercase,
// hyphen-separated at word boundaries.
//
//	"Yet Another Config Lib" -> "yet-another-config-lib"
//	"YetAnotherConfigLib"     -> "yet-another-config-lib"
//	"libIPN"                  -> "libipn"   (acronyms stay intact)
//	"WorldEditCUI"            -> "world-edit-cui"
//
// The rule is the standard camel-case split: insert a hyphen when a
// lower-or-digit run is followed by an upper-case letter, or when an
// upper-case run is followed by a lower-case letter that continues a word
// ("IPN" + "Fabric" -> "ipn-fabric"). Consecutive capitals stay together so
// acronyms such as "IPN", "GUI" or "CUI" are not shredded into single
// letters.
func hyphenate(s string) string {
	runes := []rune(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(runes) + 4)

	for i, r := range runes {
		if r == ' ' || r == '_' {
			// Collapse runs of separators into a single hyphen.
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
				b.WriteByte('-')
			}
			continue
		}
		if i > 0 && b.Len() > 0 {
			prev := runes[i-1]
			switch {
			case isUpper(r) && !isUpper(prev) && prev != '-' && prev != ' ' && prev != '_':
				// camelCase boundary: fooBar -> foo-Bar
				b.WriteByte('-')
			case isUpper(r) && isUpper(prev) && i+1 < len(runes) && isLower(runes[i+1]):
				// acronym followed by a word: IPNFabric -> IPN-Fabric
				b.WriteByte('-')
			}
		}
		b.WriteRune(toLower(r))
	}
	return b.String()
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isLower(r rune) bool { return r >= 'a' && r <= 'z' }

func toLower(r rune) rune {
	if isUpper(r) {
		return r + ('a' - 'A')
	}
	return r
}

// searchQueries returns progressively looser search strings.
func searchQueries(c Candidate) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || len(s) < 2 || seen[strings.ToLower(s)] {
			return
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}

	if c.Meta != nil {
		add(c.Meta.Name)
		add(c.Meta.ModID)
	}
	add(c.CurseForgeName)
	// The file name without version and extension.
	if c.FileName != "" {
		add(modmeta.GuessModIDFromFileName(c.FileName))
	}
	return out
}

// jaccard returns the token-set similarity of two normalised strings.
func jaccard(a, b string) float64 {
	at, bt := tokens(a), tokens(b)
	if len(at) == 0 || len(bt) == 0 {
		return 0
	}
	inter := 0
	for k := range at {
		if bt[k] {
			inter++
		}
	}
	union := len(at) + len(bt) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// tokens splits a normalised identifier into 4-character shingles, which
// makes the comparison robust to insertions like the "(YACL)" suffix.
func tokens(s string) map[string]bool {
	out := map[string]bool{}
	const n = 4
	if len(s) <= n {
		out[s] = true
		return out
	}
	for i := 0; i+n <= len(s); i++ {
		out[s[i:i+n]] = true
	}
	return out
}

func modIDsOf(c Candidate) []string {
	if c.Meta == nil || c.Meta.ModID == "" {
		return nil
	}
	return []string{c.Meta.ModID}
}

func instanceOf(c Candidate) string { return "" }

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// HashOf is a convenience re-export so callers need not import hashutil.
func HashOf(path string) (string, error) { return hashutil.SHA1File(path) }

// cacheTTL documents the resolver's expectation for the shared store.
const cacheTTL = 6 * time.Hour
