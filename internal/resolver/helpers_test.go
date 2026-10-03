package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MohammadMD1383/modharbor/internal/hashutil"
	"github.com/MohammadMD1383/modharbor/internal/modmeta"
	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
	"github.com/MohammadMD1383/modharbor/internal/store"
)

// ─── ResolveAll ─────────────────────────────────────────────────────────────
//
// ResolveAll starts one goroutine per candidate and relies on a semaphore to
// stay inside API rate limits. That makes three properties load bearing: the
// results must stay aligned with their inputs (goroutines write by index), the
// semaphore must actually bound concurrency, and progress must be reported once
// per candidate rather than once per completed batch.

func TestResolveAllKeepsResultsInInputOrder(t *testing.T) {
	stub, projects, versions := stubModrinth(t)
	stub.add(versions, projects)
	r := New(Options{Modrinth: stub.client()})

	cands := []Candidate{
		// 0: exact hash hit.
		{FileName: "sodium-fabric-0.9.1.jar", SHA1: "sodiumhash", Size: 1_913_459,
			Meta: &modmeta.Meta{ModID: "sodium", Name: "Sodium", Version: "0.9.1", Loader: modmeta.LoaderFabric}},
		// 1: nothing upstream knows this mod.
		{FileName: "privatemod-1.0.0.jar", SHA1: "privatehash",
			Meta: &modmeta.Meta{ModID: "privatemod", Name: "Private Mod", Version: "1.0.0", Loader: modmeta.LoaderFabric}},
		// 2: slug probe hit, the CurseForge-mirror case the hash lookup cannot
		//    reach.
		{FileName: "common-networking-fabric-26.2-1.1.0.jar", SHA1: "mirrorhash", Size: 23_937,
			Meta: &modmeta.Meta{ModID: "commonnetworking", Name: "Common Network", Version: "26.2-1.1.0", Loader: modmeta.LoaderFabric}},
	}

	got := r.ResolveAll(context.Background(), cands)
	if len(got) != len(cands) {
		t.Fatalf("got %d results, want %d", len(got), len(cands))
	}
	wantMethods := []string{MethodHash, MethodUnmatched, MethodSlug}
	for i, c := range cands {
		if got[i].Candidate.FileName != c.FileName {
			t.Errorf("result %d carries %q, want %q: results are not aligned with their inputs",
				i, got[i].Candidate.FileName, c.FileName)
		}
		if got[i].Resolution.Method != wantMethods[i] {
			t.Errorf("result %d (%s) method = %q, want %q",
				i, c.FileName, got[i].Resolution.Method, wantMethods[i])
		}
	}
}

func TestResolveAllReportsProgressOncePerCandidate(t *testing.T) {
	stub, projects, versions := stubModrinth(t)
	stub.add(versions, projects)

	const n = 12
	var mu sync.Mutex
	seen := map[int]int{}
	totals := map[int]bool{}

	r := New(Options{
		Modrinth: stub.client(),
		Progress: func(done, total int) {
			mu.Lock()
			defer mu.Unlock()
			seen[done]++
			totals[total] = true
		},
	})
	r.ResolveAll(context.Background(), stubCandidates(n))

	mu.Lock()
	defer mu.Unlock()
	if len(totals) != 1 || !totals[n] {
		t.Errorf("progress reported totals %v, want just %d", totals, n)
	}
	if len(seen) != n {
		t.Errorf("progress reported %d distinct done values, want %d (%v)", len(seen), n, seen)
	}
	for done, count := range seen {
		if done < 1 || done > n {
			t.Errorf("progress reported done=%d, want 1..%d", done, n)
		}
		if count != 1 {
			t.Errorf("progress reported done=%d %d times, want once", done, count)
		}
	}
}

func TestResolveAllBoundsConcurrentRequests(t *testing.T) {
	// The worker count is the rate-limit guard. If it stops bounding, the tool
	// hammers the API and gets everyone else rate limited with it.
	const workers = 6

	stub, projects, versions := stubModrinth(t)
	stub.add(versions, projects)
	stub.delay = 15 * time.Millisecond

	r := New(Options{Modrinth: stub.client()})
	r.ResolveAll(context.Background(), stubCandidates(workers*3))

	peak := stub.peakRequests()
	if peak > workers {
		t.Errorf("%d requests were in flight at once, want at most %d", peak, workers)
	}
	if peak < 2 {
		t.Errorf("only ever %d request in flight: ResolveAll is not running in parallel at all", peak)
	}
}

// A cancelled context must never turn into a confident wrong answer: every
// candidate comes back either cancelled or unmatched.
func TestResolveAllSurvivesCancelledContext(t *testing.T) {
	// No providers, so every lookup fails instantly and the goroutines spend
	// their time queued on the semaphore — which is exactly where the
	// ctx.Done branch lives.
	r := New(Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cands := stubCandidates(200)
	got := r.ResolveAll(ctx, cands)
	if len(got) != len(cands) {
		t.Fatalf("got %d results, want %d", len(got), len(cands))
	}
	var cancelled int
	for i, res := range got {
		if res.Candidate.FileName != cands[i].FileName {
			t.Fatalf("result %d carries %q, want %q", i, res.Candidate.FileName, cands[i].FileName)
		}
		if res.Resolution.ProjectID != "" || res.Resolution.Confidence != 0 {
			t.Errorf("result %d resolved to %+v under a cancelled context", i, res.Resolution)
		}
		if errors.Is(res.Err, context.Canceled) {
			cancelled++
			continue
		}
		if !errors.Is(res.Err, ErrUnmatched) {
			t.Errorf("result %d err = %v, want context.Canceled or ErrUnmatched", i, res.Err)
		}
	}
	// With far more candidates than workers, some goroutine is parked on the
	// semaphore when the context dies and must report the cancellation.
	if cancelled == 0 {
		t.Error("no candidate reported context.Canceled: the cancellation branch never ran")
	}
}

func TestResolveAllHandlesNoCandidates(t *testing.T) {
	got := New(Options{}).ResolveAll(context.Background(), nil)
	if len(got) != 0 {
		t.Errorf("got %d results for no candidates, want 0", len(got))
	}
}

// ─── ForInstance ────────────────────────────────────────────────────────────

func TestForInstanceScopesVersionSelection(t *testing.T) {
	stub, projects, versions := stubModrinth(t)
	stub.add(versions, projects)
	r := New(Options{Modrinth: stub.client()})
	ctx := context.Background()

	// Unscoped there is nothing to select for.
	if v := r.latestFor(ctx, "AANobbMI"); v != nil {
		t.Errorf("latestFor on an unscoped resolver = %q, want nil", v.ID)
	}

	scoped := r.ForInstance("26.3", "fabric")
	v := scoped.latestFor(ctx, "AANobbMI")
	if v == nil {
		t.Fatal("scoped latestFor found nothing for 26.3/fabric")
	}
	if v.ID != "sod-263" {
		t.Errorf("scoped latestFor chose %q, want sod-263 (the 26.3 build)", v.ID)
	}

	// Scoping must not leak back into the parent: migrate resolves one
	// instance at a time and the parent still has to stay unconstrained for
	// the next one.
	if r.mcVersion != "" || r.loader != "" {
		t.Errorf("ForInstance mutated the parent: mcVersion=%q loader=%q", r.mcVersion, r.loader)
	}
	if v := r.latestFor(ctx, "AANobbMI"); v != nil {
		t.Errorf("parent latestFor = %q after scoping, want nil", v.ID)
	}
}

func TestForInstanceSharesTheVersionCache(t *testing.T) {
	stub, projects, versions := stubModrinth(t)
	stub.add(versions, projects)
	r := New(Options{Modrinth: stub.client()})
	scoped := r.ForInstance("26.3", "fabric")

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := scoped.versionsCached(ctx, "AANobbMI"); err != nil {
			t.Fatalf("versionsCached: %v", err)
		}
	}
	if got := stub.count("/version"); got != 1 {
		t.Errorf("served %d version listings, want 1: the cache is not shared", got)
	}
}

// A failed listing must not be memoised, or one transient 404 would poison the
// resolver for the rest of the run.
func TestVersionsCacheDoesNotMemoiseFailures(t *testing.T) {
	stub, projects, versions := stubModrinth(t)
	stub.add(versions, projects)
	stub.failVersionList("flaky", true)
	r := New(Options{Modrinth: stub.client()}).ForInstance("26.3", "fabric")
	ctx := context.Background()

	if _, err := r.versionsCached(ctx, "flaky"); err == nil {
		t.Fatal("expected an error while the project 404s")
	}
	stub.failVersionList("flaky", false)
	vs, err := r.versionsCached(ctx, "flaky")
	if err != nil {
		t.Fatalf("the retry after a recovered listing failed: %v", err)
	}
	if len(vs) != 0 {
		t.Errorf("got %d versions, want 0", len(vs))
	}
}

// ─── pure helpers ───────────────────────────────────────────────────────────

func TestNonEmpty(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nothing to drop", []string{"26.2", "fabric"}, []string{"26.2", "fabric"}},
		{"drops blanks", []string{"", "26.2", ""}, []string{"26.2"}},
		{"all blank", []string{"", ""}, []string{}},
		{"nil", nil, nil},
		{"empty", []string{}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := nonEmpty(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("nonEmpty(%v) = %v, want %v", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("nonEmpty(%v) = %v, want %v", c.in, got, c.want)
				}
			}
		})
	}
}

func TestNonEmptyLeavesTheCallersSliceIntact(t *testing.T) {
	// The obvious wrong implementation appends straight into the caller's
	// backing array, which would silently rewrite a slice the caller still
	// holds — here, the game version and loader pair sent upstream.
	in := []string{"26.2", "", "fabric", ""}
	want := append([]string(nil), in...)
	_ = nonEmpty(in)
	for i := range in {
		if in[i] != want[i] {
			t.Fatalf("nonEmpty mutated its input: got %v, want %v", in, want)
		}
	}
}

func TestSortVersionsNewestFirst(t *testing.T) {
	old := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	newest := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	v := []modrinth.Version{
		{ID: "b-old", DatePublished: old},
		{ID: "a-new", DatePublished: newest},
		{ID: "z-mid", DatePublished: mid},
	}
	sortVersionsNewestFirst(v)
	for i, want := range []string{"a-new", "z-mid", "b-old"} {
		if v[i].ID != want {
			t.Fatalf("order = %s, want %s", versionIDs(v), "a-new,z-mid,b-old")
		}
	}

	// Same instant: the id breaks the tie, so the choice does not depend on
	// whatever order the API happened to return.
	tied := []modrinth.Version{
		{ID: "c", DatePublished: newest},
		{ID: "a", DatePublished: newest},
		{ID: "b", DatePublished: newest},
	}
	sortVersionsNewestFirst(tied)
	for i, want := range []string{"a", "b", "c"} {
		if tied[i].ID != want {
			t.Fatalf("tie-break order = %s, want a,b,c", versionIDs(tied))
		}
	}
}

func TestItoa(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{7, "7"},
		{42, "42"},
		{238937, "238937"},
		{-1, "-1"},
		{-238937, "-238937"},
		// CurseForge project ids are decimal and the store round trips them as
		// strings, so the large ones have to be exact too.
		{4611686018427387904, "4611686018427387904"}, // 1<<62
		{-4611686018427387904, "-4611686018427387904"},
	}
	for _, c := range cases {
		if got := itoa(c.in); got != c.want {
			t.Errorf("itoa(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestModrinthLoader(t *testing.T) {
	cases := []struct {
		in   modmeta.Loader
		want string
	}{
		{modmeta.LoaderFabric, "fabric"},
		{modmeta.LoaderQuilt, "quilt"},
		{modmeta.LoaderForge, "forge"},
		{modmeta.LoaderNeoForge, "neoforge"},
		{modmeta.LoaderUnknown, ""},
		{modmeta.Loader("something-else"), ""},
	}
	for _, c := range cases {
		if got := modrinthLoader(c.in); got != c.want {
			t.Errorf("modrinthLoader(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHashOfMatchesHashutil(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mod.jar")
	if err := os.WriteFile(path, []byte("not really a jar"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := HashOf(path)
	if err != nil {
		t.Fatalf("HashOf: %v", err)
	}
	if want := hashutil.SHA1Bytes([]byte("not really a jar")); got != want {
		t.Errorf("HashOf = %q, want %q", got, want)
	}
	if _, err := HashOf(filepath.Join(t.TempDir(), "absent.jar")); err == nil {
		t.Error("expected an error for a missing file")
	}
}

func TestErrUnmatchedIsIdentifiable(t *testing.T) {
	// Callers report unmatched mods with errors.Is, so the sentinel has to be a
	// comparable value rather than a fresh one each time.
	if got := ErrUnmatched.Error(); got != "no upstream match found" {
		t.Errorf("ErrUnmatched.Error() = %q", got)
	}
	if !errors.Is(fmt.Errorf("resolving sodium.jar: %w", ErrUnmatched), ErrUnmatched) {
		t.Error("errors.Is does not see ErrUnmatched through a wrap")
	}
	if errors.Is(errors.New("some other failure"), ErrUnmatched) {
		t.Error("an unrelated error must not match ErrUnmatched")
	}
}

// ─── helpers that need a server ─────────────────────────────────────────────

func TestVersionPlausible(t *testing.T) {
	stub, projects, versions := stubModrinth(t)
	stub.add(versions, projects)
	r := New(Options{Modrinth: stub.client()})
	ctx := context.Background()

	cases := []struct {
		name    string
		project string
		local   string
		want    bool
	}{
		// Modrinth decorates its version numbers on either side of the number
		// itself, while a jar in the wild usually declares the bare number, so
		// neither direction can be tested with equality.
		{"bare number, bare upstream", "AANobbMI", "0.9.1", true},
		{"game version first upstream", "yacl", "3.7.1", true},
		{"game version trailing locally", "AANobbMI", "mc26.2-0.9.1-fabric", true},
		{"a version the project never shipped", "AANobbMI", "3.1.4", false},
		{"no version at all", "AANobbMI", "   ", false},
		{"unknown project", "nope", "0.9.1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := r.versionPlausible(ctx, c.project, c.local); got != c.want {
				t.Errorf("versionPlausible(%s, %q) = %v, want %v", c.project, c.local, got, c.want)
			}
		})
	}
}

func TestFindProjectByName(t *testing.T) {
	stub, projects, versions := stubModrinth(t)
	stub.add(versions, projects)
	r := New(Options{Modrinth: stub.client()})
	ctx := context.Background()

	if p := r.findProjectByName(ctx, "  "); p != nil {
		t.Errorf("an empty name matched %q, want nil", p.Slug)
	}
	if p := r.findProjectByName(ctx, "nothing-like-this-exists"); p != nil {
		t.Errorf("an unknown name matched %q, want nil", p.Slug)
	}
	if p := r.findProjectByName(ctx, "Common Network"); p == nil || p.ID != "cnet" {
		t.Errorf("Common Network resolved to %+v, want cnet", p)
	}
	// Containment is how a launcher's display name gets matched onto a
	// differently titled Modrinth project.
	if p := r.findProjectByName(ctx, "YetAnotherConfigLib"); p == nil || p.ID != "yacl" {
		t.Errorf("YetAnotherConfigLib resolved to %+v, want yacl", p)
	}
}

// ─── version decorations ────────────────────────────────────────────────────

// Regression. A CurseForge-mirror jar declares the bare mod version ("3.7.1")
// while Modrinth publishes the same build decorated with the game version in
// front ("mc26.2-3.7.1"). "YetAnotherConfigLib" against "YetAnotherConfigLib
// (YACL)" is a containment match, so it lands below confSlug and versionPlausible
// is the gate. That gate read the leading dotted run of the published number —
// which for an mc-first number is the Minecraft release — and rejected the mod
// outright, so exactly the mirror jars the fuzzy chain exists for never got
// identified.
func TestNameMatchSurvivesAnMCFirstVersionNumber(t *testing.T) {
	stub := newModrinthStub(t)
	stub.add(mcFirstYACL(), []modrinth.Project{mcFirstYACLProject()})
	stub.hits = []modrinth.Hit{
		{ProjectID: "yacl", Slug: "yacl", Title: "YetAnotherConfigLib (YACL)", Downloads: 30_000_000},
	}
	r := New(Options{Modrinth: stub.client()})

	c := Candidate{
		FileName: "YetAnotherConfigLib-fabric-3.7.1.jar", SHA1: "yaclmirror", Size: 402_000,
		Meta: &modmeta.Meta{
			ModID: "yet_another_config_lib_v3", Name: "YetAnotherConfigLib",
			Version: "3.7.1", Loader: modmeta.LoaderFabric,
		},
	}
	hit := modrinth.Hit{ProjectID: "yacl", Slug: "yacl", Title: "YetAnotherConfigLib (YACL)"}
	if score := similarity(c, hit); score >= confSlug {
		t.Fatalf("precondition gone: the match now scores %.2f, so versionPlausible is not the gate any more", score)
	}

	res := r.Resolve(context.Background(), c)
	if res.Err != nil {
		t.Fatalf("a legitimate mirror jar went unmatched: %v (resolution %+v)", res.Err, res.Resolution)
	}
	if res.Resolution.ProjectID != "yacl" {
		t.Errorf("ProjectID = %q, want yacl", res.Resolution.ProjectID)
	}
	if res.Resolution.Method != MethodName {
		t.Errorf("Method = %q, want %q", res.Resolution.Method, MethodName)
	}
}

// The decoration must not become a licence to match anything: a name match is
// still only accepted when the project also survives corroboration.
func TestNameMatchStillNeedsCorroboration(t *testing.T) {
	stub := newModrinthStub(t)
	stub.add(mcFirstYACL(), []modrinth.Project{mcFirstYACLProject()})
	stub.hits = []modrinth.Hit{
		{ProjectID: "yacl", Slug: "yacl", Title: "YetAnotherConfigLib (YACL)", Downloads: 30_000_000},
	}
	r := New(Options{Modrinth: stub.client()})

	// A Forge jar cannot be the Fabric YACL, whatever the version says.
	c := Candidate{
		FileName: "YetAnotherConfigLib-forge-3.7.1.jar", SHA1: "yaclforge", Size: 402_000,
		Meta: &modmeta.Meta{
			ModID: "yet_another_config_lib_v3", Name: "YetAnotherConfigLib",
			Version: "3.7.1", Loader: modmeta.LoaderForge,
		},
	}
	res := r.Resolve(context.Background(), c)
	if res.Resolution.Method != MethodUnmatched {
		t.Errorf("a Forge jar matched a Fabric project: %+v", res.Resolution)
	}
}

func mcFirstYACLProject() modrinth.Project {
	return modrinth.Project{ID: "yacl", Slug: "yacl", Title: "YetAnotherConfigLib (YACL)", Loaders: []string{"fabric"}}
}

func mcFirstYACL() map[string][]modrinth.Version {
	return map[string][]modrinth.Version{
		"yacl": {{ID: "yacl-1", ProjectID: "yacl", VersionNumber: "mc26.2-3.7.1", VersionType: "release",
			Loaders: []string{"fabric"}, GameVersions: []string{"26.2"},
			DatePublished: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			Files:         []modrinth.File{{Filename: "yacl-3.7.1.jar", Primary: true, Size: 400_000}}}},
	}
}

func TestLatestForPicksTheNewestMatch(t *testing.T) {
	stub, projects, versions := stubModrinth(t)
	stub.add(versions, projects)
	r := New(Options{Modrinth: stub.client()})
	ctx := context.Background()

	if v := r.latestFor(ctx, "AANobbMI"); v != nil {
		t.Errorf("unscoped latestFor = %q, want nil", v.ID)
	}

	scoped := r.ForInstance("26.2", "fabric")
	v := scoped.latestFor(ctx, "AANobbMI")
	if v == nil {
		t.Fatal("no version for 26.2/fabric")
	}
	if v.ID != "sod-262b" {
		t.Errorf("latestFor chose %q, want sod-262b (the newer of two 26.2 builds)", v.ID)
	}

	// A game version the project never shipped for has no answer, and the caller
	// must be told so rather than handed another release's build.
	if v := scoped.latestFor(ctx, "nope"); v != nil {
		t.Errorf("latestFor for an unknown project = %q, want nil", v.ID)
	}
	if v := r.ForInstance("1.7.10", "fabric").latestFor(ctx, "AANobbMI"); v != nil {
		t.Errorf("latestFor for an unshipped game version = %q, want nil", v.ID)
	}
}

// A weak name match must not be pinned in the cache; a strong one should be, or
// every migration redoes the whole fuzzy search.
func TestPersistOnlyCachesConfidentMatches(t *testing.T) {
	cases := []struct {
		name       string
		res        store.Resolution
		wantCached bool
	}{
		{"exact hash", store.Resolution{SHA1: "aaa", Method: MethodHash, Confidence: confHash}, true},
		{"slug", store.Resolution{SHA1: "bbb", Method: MethodSlug, Confidence: confSlug}, true},
		{"strong name", store.Resolution{SHA1: "ccc", Method: MethodName, Confidence: confNameHigh}, true},
		{"weak name", store.Resolution{SHA1: "ddd", Method: MethodName, Confidence: confNameLow}, false},
		{"just below the floor", store.Resolution{SHA1: "eee", Method: MethodName, Confidence: confNameHigh - 0.01}, false},
		{"nothing to key on", store.Resolution{Method: MethodHash, Confidence: confHash}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			New(Options{Cache: st}).persist(c.res)
			if _, ok := st.Lookup(c.res.SHA1); ok != c.wantCached {
				t.Errorf("cached = %v, want %v", ok, c.wantCached)
			}
		})
	}
}

func TestPersistWithoutACacheIsANoOp(t *testing.T) {
	New(Options{}).persist(store.Resolution{SHA1: "aaa", Method: MethodHash, Confidence: confHash})
}

// ─── fixtures ───────────────────────────────────────────────────────────────

func stubCandidates(n int) []Candidate {
	cands := make([]Candidate, n)
	for i := range cands {
		cands[i] = Candidate{
			FileName: fmt.Sprintf("mod%d-1.0.0.jar", i),
			SHA1:     fmt.Sprintf("sha%d", i),
			Meta: &modmeta.Meta{
				ModID: fmt.Sprintf("mod%d", i), Name: fmt.Sprintf("Mod %d", i), Version: "1.0.0",
			},
		}
	}
	return cands
}

func versionIDs(vs []modrinth.Version) string {
	ids := make([]string, len(vs))
	for i, v := range vs {
		ids[i] = v.ID
	}
	return strings.Join(ids, ",")
}

// stubModrinth builds the fixture these tests share: two Fabric projects, one
// with several versions spanning two Minecraft releases (including two builds for
// the same release, so "newest" has something to choose between), and search
// hits for both.
func stubModrinth(t *testing.T) (*modrinthStub, []modrinth.Project, map[string][]modrinth.Version) {
	t.Helper()
	sodium := modrinth.Project{ID: "AANobbMI", Slug: "sodium", Title: "Sodium", Loaders: []string{"fabric"}}
	cnet := modrinth.Project{ID: "cnet", Slug: "common-network", Title: "Common Network", Loaders: []string{"fabric"}}
	yacl := modrinth.Project{ID: "yacl", Slug: "yacl", Title: "YetAnotherConfigLib (YACL)", Loaders: []string{"fabric"}}

	sodiumVersions := []modrinth.Version{
		{ID: "sod-261", ProjectID: "AANobbMI", VersionNumber: "0.9.1", VersionType: "release",
			Loaders: []string{"fabric"}, GameVersions: []string{"26.1"},
			DatePublished: time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC),
			Files:         []modrinth.File{{Filename: "sodium-fabric-0.9.1.jar", Primary: true, Size: 1_913_459}}},
		{ID: "sod-262a", ProjectID: "AANobbMI", VersionNumber: "0.9.3", VersionType: "release",
			Loaders: []string{"fabric"}, GameVersions: []string{"26.2"},
			DatePublished: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			Files:         []modrinth.File{{Filename: "sodium-fabric-0.9.3.jar", Primary: true, Size: 1_950_000}}},
		{ID: "sod-262b", ProjectID: "AANobbMI", VersionNumber: "0.9.3-alpha.2", VersionType: "alpha",
			Loaders: []string{"fabric"}, GameVersions: []string{"26.2"},
			DatePublished: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
			Files:         []modrinth.File{{Filename: "sodium-fabric-0.9.3-alpha.2.jar", Primary: true, Size: 1_960_000}}},
		{ID: "sod-263", ProjectID: "AANobbMI", VersionNumber: "0.9.4", VersionType: "release",
			Loaders: []string{"fabric"}, GameVersions: []string{"26.3"},
			DatePublished: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			Files:         []modrinth.File{{Filename: "sodium-fabric-0.9.4.jar", Primary: true, Size: 1_980_000}}},
	}
	cnetVersions := []modrinth.Version{
		{ID: "cn-1", ProjectID: "cnet", VersionNumber: "26.2-1.1.0", VersionType: "release",
			Loaders: []string{"fabric"}, GameVersions: []string{"26.2"},
			DatePublished: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
			Files:         []modrinth.File{{Filename: "common-networking-fabric-26.2-1.1.0.jar", Primary: true, Size: 23_937}}},
	}
	yaclVersions := []modrinth.Version{
		{ID: "yacl-1", ProjectID: "yacl", VersionNumber: "mc26.2-3.7.1", VersionType: "release",
			Loaders: []string{"fabric"}, GameVersions: []string{"26.2"},
			DatePublished: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			Files:         []modrinth.File{{Filename: "yacl-3.7.1.jar", Primary: true, Size: 400_000}}},
	}

	versions := map[string][]modrinth.Version{
		"sodium":         sodiumVersions,
		"common-network": cnetVersions,
		"yacl":           yaclVersions,
		// The hash endpoint is keyed by digest rather than project.
		"sha1:sodiumhash": {sodiumVersions[0]},
	}

	stub := newModrinthStub(t)
	stub.hits = []modrinth.Hit{
		{ProjectID: "AANobbMI", Slug: "sodium", Title: "Sodium", Downloads: 50_000_000},
		{ProjectID: "cnet", Slug: "common-network", Title: "Common Network", Downloads: 900_000},
		{ProjectID: "yacl", Slug: "yacl", Title: "YetAnotherConfigLib (YACL)", Downloads: 30_000_000},
	}
	return stub, []modrinth.Project{sodium, cnet, yacl}, versions
}

// ─── fake Modrinth ──────────────────────────────────────────────────────────

// modrinthStub serves canned Modrinth payloads and records what it was asked
// for, so tests can assert on caching, on the filters a scoped resolver sends,
// and on how many requests were in flight at once.
type modrinthStub struct {
	*httptest.Server

	mu       sync.Mutex
	paths    []string
	inFlight int
	peak     int

	// delay slows every handler so overlapping work is observable.
	delay time.Duration
	// failVersionLists 404s a project's version listing when set.
	failVersionLists map[string]bool

	projects map[string]modrinth.Project   // by slug and by id
	versions map[string][]modrinth.Version // by slug and by id, plus "sha1:<hash>"
	hits     []modrinth.Hit
}

func newModrinthStub(t *testing.T) *modrinthStub {
	t.Helper()
	s := &modrinthStub{
		projects:         map[string]modrinth.Project{},
		versions:         map[string][]modrinth.Version{},
		failVersionLists: map[string]bool{},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *modrinthStub) add(versions map[string][]modrinth.Version, projects []modrinth.Project) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range versions {
		s.versions[k] = v
	}
	for _, p := range projects {
		s.projects[p.Slug] = p
		s.projects[p.ID] = p
		// The real API accepts either a slug or an id in the path.
		if vs, ok := s.versions[p.Slug]; ok {
			s.versions[p.ID] = vs
		}
	}
}

// failVersionList makes /project/<key>/version answer 404 while on is true.
func (s *modrinthStub) failVersionList(key string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failVersionLists[key] = on
}

func (s *modrinthStub) client() *modrinth.Client {
	return modrinth.New(modrinth.Options{BaseURL: s.URL, HTTP: s.Client()})
}

// count reports how many requests touched a path fragment.
func (s *modrinthStub) count(fragment string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	for _, p := range s.paths {
		if strings.Contains(p, fragment) {
			n++
		}
	}
	return n
}

func (s *modrinthStub) peakRequests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak
}

func (s *modrinthStub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.paths = append(s.paths, r.URL.RequestURI())
	s.inFlight++
	if s.inFlight > s.peak {
		s.peak = s.inFlight
	}
	delay := s.delay
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inFlight--
		s.mu.Unlock()
	}()

	if delay > 0 {
		time.Sleep(delay)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case strings.HasPrefix(path, "version_file/"):
		vs, ok := s.versions["sha1:"+strings.TrimPrefix(path, "version_file/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, vs[0])
	case strings.HasPrefix(path, "project/"):
		key := strings.TrimPrefix(path, "project/")
		if vkey, ok := strings.CutSuffix(key, "/version"); ok {
			if s.failVersionLists[vkey] {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(w, s.filter(vkey, r.URL.Query()))
			return
		}
		p, ok := s.projects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, p)
	case path == "search":
		writeJSON(w, modrinth.SearchResponse{Hits: s.hits, TotalHits: len(s.hits)})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// filter applies the game_versions and loaders filters the way Modrinth does, so
// a scoped resolver's request can be checked end to end.
func (s *modrinthStub) filter(key string, q url.Values) []modrinth.Version {
	all := s.versions[key]
	if all == nil {
		return nil
	}
	games, loaders := stubList(q.Get("game_versions")), stubList(q.Get("loaders"))
	out := make([]modrinth.Version, 0, len(all))
	for _, v := range all {
		if len(games) > 0 && !stubAny(v.GameVersions, games) {
			continue
		}
		if len(loaders) > 0 && !stubAny(v.Loaders, loaders) {
			continue
		}
		out = append(out, v)
	}
	return out
}

func stubList(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func stubAny(have, want []string) bool {
	for _, h := range have {
		for _, w := range want {
			if strings.EqualFold(h, w) {
				return true
			}
		}
	}
	return false
}
