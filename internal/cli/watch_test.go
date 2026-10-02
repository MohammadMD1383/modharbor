package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MohammadMD1383/modharbor/internal/app"
	"github.com/MohammadMD1383/modharbor/internal/config"
	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/migrate"
	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
	"github.com/MohammadMD1383/modharbor/internal/ui"
)

const (
	watchFixtureID      = "26.3-fabric-mod"
	watchFixtureMC      = "26.3"
	watchFixtureMod     = "sodium"
	watchFixtureVersion = "0.9.1"
	watchFixtureBase    = "0.9.3"
	watchFixtureNext    = "0.9.4"
)

// ─── stub Modrinth ───────────────────────────────────────────────────────────

// watchStub stands in for Modrinth with a single project whose newest version
// the test can rewrite mid-run, which is how a release "dropping" is simulated.
type watchStub struct {
	srv *httptest.Server

	mu sync.Mutex
	// newest is the version number currently published for the project.
	newest string
	// cycles counts version-list lookups, i.e. planning passes that got far
	// enough to ask upstream what exists.
	cycles int
}

func newWatchStub(t *testing.T, newest string) *watchStub {
	t.Helper()
	s := &watchStub{newest: newest}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *watchStub) publish(version string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.newest = version
}

func (s *watchStub) cycleCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cycles
}

func (s *watchStub) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if strings.HasSuffix(r.URL.Path, "/version") {
		s.cycles++
	}
	newest := s.newest
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	p := r.URL.Path

	switch {
	case strings.HasPrefix(p, "/version_file/"):
		// The fixture jar is built by the test, so it corresponds to no
		// published file and the resolver must fall back to metadata.
		w.WriteHeader(http.StatusNotFound)

	case strings.HasPrefix(p, "/project/") && strings.HasSuffix(p, "/version"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/project/"), "/version")
		if id != watchFixtureMod {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_ = json.NewEncoder(w).Encode(s.versions(r, newest))

	case p == "/project/"+watchFixtureMod:
		_ = json.NewEncoder(w).Encode(modrinth.Project{
			ID:            watchFixtureMod,
			Slug:          watchFixtureMod,
			Title:         "Sodium",
			Loaders:       []string{"fabric"},
			GameVersions:  []string{watchFixtureMC},
			ClientSide:    "required",
			ServerSide:    "unsupported",
			DownloadCount: 5_000_000,
		})

	case p == "/search":
		_ = json.NewEncoder(w).Encode(modrinth.SearchResponse{
			Hits: []modrinth.Hit{{
				ProjectID: watchFixtureMod,
				Slug:      watchFixtureMod,
				Title:     "Sodium",
			}},
			TotalHits: 1,
		})

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// versions applies the game-version and loader filters the real API applies, so
// the tests exercise filtering rather than assuming it away.
func (s *watchStub) versions(r *http.Request, newest string) []modrinth.Version {
	v := modrinth.Version{
		ID:            "ver-" + newest,
		ProjectID:     watchFixtureMod,
		Name:          newest,
		VersionNumber: newest,
		VersionType:   "release",
		Loaders:       []string{"fabric"},
		GameVersions:  []string{watchFixtureMC},
		DatePublished: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		Files: []modrinth.File{{
			Filename: watchFixtureMod + "-fabric-" + newest + ".jar",
			Primary:  true,
			Size:     4096,
			Hashes:   map[string]string{"sha1": strings.Repeat("a", 40), "sha512": strings.Repeat("b", 128)},
			URL:      s.srv.URL + "/data/" + newest + "/" + watchFixtureMod + ".jar",
		}},
	}

	gameVersions := jsonList(r.URL.Query().Get("game_versions"))
	loaders := jsonList(r.URL.Query().Get("loaders"))

	// An unfiltered query must return the version, or callers that ask "does
	// this project publish anything at all?" would hear no.
	if len(gameVersions) == 0 && len(loaders) == 0 {
		return []modrinth.Version{v}
	}
	for _, want := range gameVersions {
		if !v.Supports(want, "") {
			return []modrinth.Version{}
		}
	}
	for _, want := range loaders {
		if !v.Supports("", want) {
			return []modrinth.Version{}
		}
	}
	return []modrinth.Version{v}
}

func jsonList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// ─── instance fixture ────────────────────────────────────────────────────────

// watchEnv is one fully isolated watch world: a temp Minecraft directory, temp
// XDG state, and a config pointed at the stub.
type watchEnv struct {
	app  *app.App
	inst *instance.Info
	stub *watchStub
}

func newWatchEnv(t *testing.T, newest string) *watchEnv {
	t.Helper()

	stub := newWatchStub(t, newest)
	root := t.TempDir()
	dir := filepath.Join(root, "versions", watchFixtureID)

	writeTestJSON(t, filepath.Join(dir, watchFixtureID+".json"), map[string]any{
		"id":        watchFixtureID,
		"type":      "release",
		"mainClass": "net.fabricmc.loader.impl.launch.knot.KnotClient",
		"libraries": []map[string]string{
			{"name": "net.fabricmc:fabric-loader:0.16.10"},
		},
	})
	// The sidecar is how a launcher records the game version separately from the
	// version id.
	writeTestJSON(t, filepath.Join(dir, "TLauncherAdditional.json"), map[string]any{
		"jar":     watchFixtureMC,
		"modpack": map[string]any{"name": watchFixtureID},
	})

	mods := filepath.Join(dir, "mods")
	if err := os.MkdirAll(mods, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFabricModJar(t, filepath.Join(mods, watchFixtureMod+"-fabric-"+watchFixtureVersion+".jar"),
		watchFixtureMod, watchFixtureVersion)

	// Nothing in this test may reach the real user: config, data, cache and the
	// Minecraft directory are all redirected into temp dirs.
	isolateUserEnv(t)
	t.Setenv("MODHARBOR_MINECRAFT_DIR", root)

	confDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "modharbor")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestJSON(t, filepath.Join(confDir, "config.json"), map[string]any{
		"minecraftDir": root,
		"modrinth":     map[string]any{"enabled": true, "baseUrl": stub.srv.URL},
		"curseForge":   map[string]any{"enabled": false},
		"update":       map[string]any{"channel": "release"},
	})

	cfg, paths, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, paths)
	inst, err := a.ResolveInstance(watchFixtureID)
	if err != nil {
		t.Fatalf("resolving fixture instance: %v", err)
	}
	if inst.MCVersion != watchFixtureMC {
		t.Fatalf("fixture MCVersion = %q, want %q", inst.MCVersion, watchFixtureMC)
	}

	return &watchEnv{app: a, inst: inst, stub: stub}
}

// isolateUserEnv points every filesystem location modharbor reads or writes at
// a temp directory. Tests that stop short of building a full fixture still need
// it: a bare bootstrap otherwise loads the real user's config.
func isolateUserEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("MODHARBOR_MINECRAFT_DIR", t.TempDir())
}

func writeTestJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeFabricModJar writes a jar carrying the metadata the resolver reads to
// identify a mod without consulting the network.
func writeFabricModJar(t *testing.T, path, id, version string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"schemaVersion": 1,
		"id":            id,
		"version":       version,
		"name":          "Sodium",
	})
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("fabric.mod.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ─── output capture ──────────────────────────────────────────────────────────

// safeBuffer collects ui output while the watch loop runs on another goroutine,
// so the test can inspect it without racing.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *safeBuffer) count(needle string) int {
	return strings.Count(b.String(), needle)
}

// captureUI redirects the ui package and restores it when the test ends.
func captureUI(t *testing.T) *safeBuffer {
	t.Helper()
	buf := &safeBuffer{}
	ui.SetWriters(buf, io.Discard)
	ui.SetColor(false)
	t.Cleanup(func() {
		ui.SetWriters(os.Stdout, os.Stderr)
		ui.SetColor(false)
	})
	return buf
}

// waitFor polls until cond holds. Waiting on an observable effect (a printed
// cycle, a request reaching the stub) keeps the tests free of sleeps that only
// happen to be long enough.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ─── tests ───────────────────────────────────────────────────────────────────

func TestWatchLoopStaysSilentWhileUpstreamIsUnchanged(t *testing.T) {
	env := newWatchEnv(t, watchFixtureBase)
	out := captureUI(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- watchLoop(ctx, env.app, env.inst, 5*time.Millisecond) }()

	// The first cycle must print the state watch starts from.
	waitFor(t, "the baseline cycle", func() bool {
		return strings.Contains(out.String(), "baseline")
	})

	// Then let several more cycles run against an upstream that never changes.
	const extraCycles = 3
	waitFor(t, "further polling cycles", func() bool {
		return env.stub.cycleCount() >= 1+extraCycles
	})
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("watchLoop returned %v, want nil", err)
	}

	got := out.String()
	if n := out.count("baseline"); n != 1 {
		t.Errorf("baseline printed %d times, want exactly 1:\n%s", n, got)
	}
	// renderOutdated's table footer is the marker for a printed table.
	if n := out.count("to update"); n != 1 {
		t.Errorf("table printed %d times, want exactly 1 (unchanged cycles must be silent):\n%s", n, got)
	}
	if !strings.Contains(got, watchFixtureBase) {
		t.Errorf("output does not mention the available version %s:\n%s", watchFixtureBase, got)
	}
	if n := out.count("stopped watching"); n != 1 {
		t.Errorf("farewell printed %d times, want 1:\n%s", n, got)
	}
}

func TestWatchLoopReportsANewlyPublishedVersion(t *testing.T) {
	env := newWatchEnv(t, watchFixtureBase)
	out := captureUI(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- watchLoop(ctx, env.app, env.inst, 5*time.Millisecond) }()

	waitFor(t, "the baseline cycle", func() bool {
		return strings.Contains(out.String(), "baseline")
	})
	cyclesBefore := env.stub.cycleCount()

	// A release drops upstream while watch is asleep between cycles.
	env.stub.publish(watchFixtureNext)

	waitFor(t, "the changed cycle", func() bool {
		return strings.Contains(out.String(), watchFixtureNext)
	})
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("watchLoop returned %v, want nil", err)
	}
	if env.stub.cycleCount() <= cyclesBefore {
		t.Error("the change was reported without another polling cycle")
	}

	got := out.String()
	if n := out.count("baseline"); n != 1 {
		t.Errorf("baseline printed %d times, want 1:\n%s", n, got)
	}
	if n := out.count("to update"); n != 2 {
		t.Errorf("table printed %d times, want 2 (baseline then the change):\n%s", n, got)
	}
	if !strings.Contains(got, "1 update(s) available") {
		t.Errorf("change was not announced as an update:\n%s", got)
	}
}

func TestWatchLoopStopsPromptlyOnContextCancel(t *testing.T) {
	env := newWatchEnv(t, watchFixtureBase)
	out := captureUI(t)

	// An interval no test would ever wait out: only cancellation can end this.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- watchLoop(ctx, env.app, env.inst, time.Hour) }()

	waitFor(t, "the first cycle", func() bool { return env.stub.cycleCount() >= 1 })
	cancel()

	select {
	case err := <-done:
		// A user pressing Ctrl-C is not an error, and must not be a non-zero exit.
		if err != nil {
			t.Fatalf("watchLoop returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watchLoop did not return within 5s of cancellation")
	}

	if !strings.Contains(out.String(), "stopped watching "+watchFixtureID) {
		t.Errorf("missing farewell:\n%s", out.String())
	}
}

func TestWatchLoopSurvivesAFailedCycle(t *testing.T) {
	env := newWatchEnv(t, watchFixtureBase)
	out := captureUI(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- watchLoop(ctx, env.app, env.inst, 5*time.Millisecond) }()

	waitFor(t, "the baseline cycle", func() bool {
		return strings.Contains(out.String(), "baseline")
	})

	// The mods vanish mid-watch, the way a launcher swap or a manual tidy does it.
	// The jar is moved rather than deleted so the same fixture can come back and
	// prove the loop recovered instead of merely surviving.
	jar := filepath.Join(env.inst.ModsDirOrDefault(), watchFixtureMod+"-fabric-"+watchFixtureVersion+".jar")
	hidden := filepath.Join(t.TempDir(), "sodium.jar")
	if err := os.Rename(jar, hidden); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failure to be reported", func() bool {
		return strings.Contains(out.String(), "check failed")
	})

	// The loop must still be running: a failed cycle is not a reason to exit, so
	// restoring the jar must produce another successful cycle.
	cyclesDuringOutage := env.stub.cycleCount()
	if err := os.Rename(hidden, jar); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "polling to resume", func() bool { return env.stub.cycleCount() > cyclesDuringOutage })

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("watchLoop returned %v after a failed cycle, want nil", err)
	}
}

func TestWatchJSONEmitsOneObjectPerCycle(t *testing.T) {
	env := newWatchEnv(t, watchFixtureBase)
	out := captureUI(t)

	flagJSON = true
	t.Cleanup(func() { flagJSON = false })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- watchLoop(ctx, env.app, env.inst, 5*time.Millisecond) }()

	waitFor(t, "two JSON cycles", func() bool { return jsonCycleCount(out) >= 2 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("watchLoop returned %v, want nil", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	// The farewell line is plain text, not JSON.
	cycles, changed := 0, 0
	for _, line := range lines {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var doc watchCycleJSON
		if err := json.Unmarshal([]byte(line), &doc); err != nil {
			t.Fatalf("cycle line is not a single JSON object: %v\n%s", err, line)
		}
		if doc.Instance != watchFixtureID || doc.MCVersion != watchFixtureMC {
			t.Errorf("cycle %d identifies %q/%q, want %q/%q", doc.Cycle, doc.Instance, doc.MCVersion, watchFixtureID, watchFixtureMC)
		}
		if len(doc.Updates) == 0 {
			t.Errorf("cycle %d reported no updates", doc.Cycle)
		}
		if doc.Changed {
			changed++
		}
		cycles++
	}
	if cycles < 2 {
		t.Fatalf("got %d JSON cycles, want at least 2:\n%s", cycles, out.String())
	}
	// Every cycle is emitted so a consumer can see the stream is alive; only the
	// first should claim a change.
	if changed != 1 {
		t.Errorf("%d cycles reported changed=true, want exactly 1:\n%s", changed, out.String())
	}
}

func TestWatchSignatureIgnoresIrrelevantChange(t *testing.T) {
	base := &updatePlan{
		updates: []migrate.Result{{
			ProjectID: "sodium", Action: migrate.ActionReplace,
			SourceVersion: "0.9.1", TargetVersion: watchFixtureBase,
			TargetFile: "sodium-fabric-" + watchFixtureBase + ".jar", Size: 4096,
		}},
	}

	// A mirror re-uploading the same version must not look like news.
	repacked := *base
	repacked.updates = append([]migrate.Result(nil), base.updates...)
	repacked.updates[0].Size = 8192
	if watchSignature(base) != watchSignature(&repacked) {
		t.Error("a file size change must not count as a change")
	}

	// A different version on offer is the change users care about.
	newer := *base
	newer.updates = append([]migrate.Result(nil), base.updates...)
	newer.updates[0].TargetVersion = watchFixtureNext
	if watchSignature(base) == watchSignature(&newer) {
		t.Error("a new available version must change the signature")
	}

	// A mod dropping out of upstream is reported in the table, so it counts.
	lost := *base
	lost.unmatched = 1
	if watchSignature(base) == watchSignature(&lost) {
		t.Error("a change in the unmatched tally must change the signature")
	}

	// Empty plans must be distinguishable from "nothing seen yet" only by the
	// caller's baseline flag, so an empty plan fingerprints consistently.
	if a, b := watchSignature(&updatePlan{}), watchSignature(&updatePlan{}); a != b {
		t.Error("two empty plans must fingerprint identically")
	}
}

func TestWatchRejectsIntervalBelowMinimum(t *testing.T) {
	cases := []string{"1s", "59s", "500ms"}
	for _, every := range cases {
		t.Run(every, func(t *testing.T) {
			out := captureUI(t)

			root := newRootCmd()
			root.SetArgs([]string{"watch", "--every", every})
			err := root.Execute()
			if err == nil {
				t.Fatalf("--every %s was accepted, want a rejection", every)
			}
			if !strings.Contains(err.Error(), watchMinInterval.String()) {
				t.Errorf("error %q does not name the %s minimum", err, watchMinInterval)
			}
			// The check happens before anything is read from disk or network.
			if out.String() != "" {
				t.Errorf("rejection printed %q, want nothing on stdout", out.String())
			}
		})
	}
}

func TestWatchRejectsOffline(t *testing.T) {
	out := captureUI(t)

	// The flag is a package global shared with every other command.
	flagOffline = true
	t.Cleanup(func() { flagOffline = false })

	root := newRootCmd()
	root.SetArgs([]string{"watch", "--offline"})
	err := root.Execute()
	if err == nil {
		t.Fatal("watch ran with --offline, want a refusal")
	}
	if !strings.Contains(err.Error(), "--offline") {
		t.Errorf("error %q does not explain that --offline is the problem", err)
	}
	if out.String() != "" {
		t.Errorf("refusal printed %q, want nothing on stdout", out.String())
	}
}

func TestWatchAcceptsMinimumInterval(t *testing.T) {
	// The boundary itself must be accepted, so the failure has to come from
	// somewhere else. Pointing every path at a temp dir means the run stops at
	// "no instance given" instead of reading the real user's config.
	isolateUserEnv(t)
	out := captureUI(t)

	root := newRootCmd()
	root.SetArgs([]string{"watch", "--every", watchMinInterval.String()})
	err := root.Execute()
	if err == nil {
		t.Fatal("watch ran to completion without an instance")
	}
	if strings.Contains(err.Error(), "minimum") {
		t.Errorf("--every %s was rejected as too short: %v", watchMinInterval, err)
	}
	if out.String() != "" {
		t.Errorf("the loop started despite failing early: %q", out.String())
	}
}

// jsonCycleCount counts the emitted per-cycle objects, ignoring the farewell.
func jsonCycleCount(out *safeBuffer) int {
	n := 0
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "{") {
			n++
		}
	}
	return n
}
