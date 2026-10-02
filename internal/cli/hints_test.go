package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// This file covers what modharbor says when it cannot do what was asked: the
// two ways of naming an instance, and the reference a user pastes out of a
// browser. Both used to produce a technically correct but useless answer.

// setDefaultInstance adds a defaultInstance to the config writeTestConfig
// wrote. The harness helper is shared and read-only by convention, and this is
// the one thing a test needs beyond what it already sets up.
func setDefaultInstance(t *testing.T, id string) {
	t.Helper()

	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "modharbor", "config.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	cfg["defaultInstance"] = id
	patched, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	if err := os.WriteFile(path, patched, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// A project URL pasted from a browser usually carries both a trailing slash
// and a query string, because that is what the address bar contains.
//
// Both used to be stripped, but in the wrong order: the query string was cut
// last, so the slash survived inside it and "…/lithium/?tab=versions" asked
// Modrinth for a project called "lithium/" — which does not exist. The failure
// reached the user as "project not found" about a project that plainly does
// exist.
func TestProjectKeyStripsTheQueryBeforeTheTrailingSlash(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"trailing slash and query on a mod URL",
			"https://modrinth.com/mod/lithium/?tab=versions", "lithium"},
		{"trailing slash and query on a project URL",
			"https://modrinth.com/project/lithium/?tab=versions", "lithium"},
		{"trailing slash and query on an http project URL",
			"http://modrinth.com/project/lithium/?tab=versions", "lithium"},
		{"empty query after the slash",
			"https://modrinth.com/mod/lithium/?", "lithium"},
		{"several parameters",
			"https://modrinth.com/mod/lithium/?tab=versions&version=1.2.3", "lithium"},
		{"query and slash with surrounding whitespace",
			"  https://modrinth.com/mod/lithium/?tab=versions\n", "lithium"},
		// The forms that already worked, kept here because reordering the two
		// strips is exactly the kind of change that breaks the easy cases.
		{"trailing slash alone", "https://modrinth.com/mod/lithium/", "lithium"},
		{"query alone", "https://modrinth.com/mod/lithium?tab=versions", "lithium"},
		{"http mod URL", "http://modrinth.com/mod/lithium", "lithium"},
		{"http project URL", "http://modrinth.com/project/lithium", "lithium"},
		{"bare slug", "lithium", "lithium"},
		{"bare slug with a slash", "lithium/", "lithium"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectKey(tc.in); got != tc.want {
				t.Errorf("projectKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The unit table above pins the parsing. This one pins the consequence, because
// the defect was only ever visible as a lookup that failed: the project behind
// "lithium/" does not exist, and the user is told so.
func TestInfoResolvesAPastedProjectURLWithTrailingSlashAndQuery(t *testing.T) {
	fx := newFakeModrinth(t)
	mcDir, _ := newTestInstance(t, "26.3-fabric-mod", nil)
	writeTestConfig(t, mcDir, fx.API())

	// `info` is the command that takes a project reference and nothing else,
	// so it exercises the pasted URL end to end: past projectKey, into the
	// API, and back out as the project's own title.
	out, err := captureCLI(t, "info", "https://modrinth.com/mod/lithium/?tab=versions", "--json")
	if err != nil {
		t.Fatalf("info with a pasted URL: %v\n%s", err, out)
	}
	var proj struct {
		Slug  string `json:"slug"`
		Title string `json:"title"`
	}
	decodeJSON(t, out, &proj)
	if proj.Slug != lithiumSlug {
		t.Errorf("slug = %q, want %q", proj.Slug, lithiumSlug)
	}
	if proj.Title != lithiumTitle {
		t.Errorf("title = %q, want %q", proj.Title, lithiumTitle)
	}
}

// With nothing to go on, the user gets one sentence and it has to name both
// ways out: an argument, and the setting. The hint beneath it names the command
// that lists what is there.
//
// Before this was wired up the message was ResolveInstance's own, which says
// neither, and no command carried the error type Execute needs to add the
// hint — so a bare `modharbor list` on a fresh install pointed at nothing.
func TestMissingInstanceErrorNamesBothWaysOut(t *testing.T) {
	fx := newFakeModrinth(t)
	mcDir, _ := newTestInstance(t, "26.3-fabric-mod", nil)
	writeTestConfig(t, mcDir, fx.API())

	code, _, stderr := executeCLI(t, "list")

	if code == 0 {
		t.Errorf("exit code = 0 with no instance at all, want non-zero\nstderr: %s", stderr)
	}
	mustContain(t, stderr,
		"no instance specified",
		"pass one as an argument",
		"config set defaultInstance",
		"try: modharbor instances",
	)
}

// Two commands describing the same situation differently is worse than one
// bad description, because the user cannot tell whether the two failures mean
// different things. Every command that resolves an instance goes through one
// path, so the wording cannot drift.
func TestEveryInstanceCommandReportsAMissingInstanceIdentically(t *testing.T) {
	fx := newFakeModrinth(t)
	mcDir, _ := newTestInstance(t, "26.3-fabric-mod", nil)
	writeTestConfig(t, mcDir, fx.API())

	// `watch` is absent on purpose: it fails the same way, but a test that
	// ever let an instance resolve would sit in its polling loop forever.
	commands := [][]string{
		{"list"},
		{"scan"},
		{"outdated"},
		{"update"},
		{"deps"},
		{"doctor"},
		{"rollback"},
		// Commands that parse the instance out of a mixed argument list reach
		// the same path, so they have to answer the same way.
		{"add", sodiumSlug},
		{"remove", sodiumSlug},
		{"link", sodiumSlug, sodiumProjectID},
		{"unlink", sodiumSlug},
	}

	for _, args := range commands {
		t.Run(args[0], func(t *testing.T) {
			code, _, stderr := executeCLI(t, args...)
			if code == 0 {
				t.Errorf("%v: exit code = 0 with no instance at all, want non-zero\nstderr: %s",
					args, stderr)
			}
			mustContain(t, stderr, "no instance specified", "config set defaultInstance",
				"try: modharbor instances")
		})
	}
}

// The check has to distinguish "no reference was given" from "no default is
// configured". A user who set defaultInstance has already done the thing the
// error suggests, so refusing to run would be a new bug: the command must
// quietly use the default, exactly as it did before.
func TestConfiguredDefaultInstanceStillResolvesWithNoFlagGiven(t *testing.T) {
	const instID = "26.3-fabric-mod"

	fx := newFakeModrinth(t)
	mcDir, _ := newTestInstance(t, instID, map[string][]byte{
		sodiumOldFile: []byte(sodiumOldJar),
	})
	writeTestConfig(t, mcDir, fx.API())
	setDefaultInstance(t, instID)

	code, stdout, stderr := executeCLI(t, "list", "--json")

	if code != 0 {
		t.Fatalf("list --json with a configured default: exit %d\nstderr: %s", code, stderr)
	}
	var rep struct {
		Instance string `json:"instance"`
	}
	decodeJSON(t, stdout, &rep)
	if rep.Instance != instID {
		t.Errorf("resolved %q, want the configured default %q", rep.Instance, instID)
	}
}
