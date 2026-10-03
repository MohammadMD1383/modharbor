package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ─── Test helpers ───────────────────────────────────────────────────────────

// colourAuto puts the package back into auto-detect mode (the nil state the
// binary starts in) and restores whatever the previous test left behind.
//
// Every test here reaches the same package-level flag, so without this a test
// that resolved colour would silently decide the result of the next one.
func colourAuto(t *testing.T) {
	t.Helper()
	orig := colorEnabled.Load()
	t.Cleanup(func() {
		if orig == nil {
			colorEnabled.Store(nil)
			return
		}
		v := *orig
		colorEnabled.Store(&v)
	})
	colorEnabled.Store(nil)
}

// clearEnv unsets name and restores it afterwards.
//
// t.Setenv cannot express "unset", and NO_COLOR in particular is only
// meaningful when it is *absent* — a NO_COLOR="" is still a set NO_COLOR and
// still disables colour.
func clearEnv(t *testing.T, name string) {
	t.Helper()
	if v, ok := os.LookupEnv(name); ok {
		t.Cleanup(func() { _ = os.Setenv(name, v) })
	} else {
		t.Cleanup(func() { _ = os.Unsetenv(name) })
	}
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unsetting %s: %v", name, err)
	}
}

// forceColorEnv makes auto-detection deterministic: NO_COLOR absent, a
// non-dumb TERM, and CLICOLOR_FORCE=1. The last one short-circuits the TTY
// probe, so the result does not depend on whether the test binary happens to
// have a terminal on stdout.
func forceColorEnv(t *testing.T) {
	t.Helper()
	clearEnv(t, "NO_COLOR")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CLICOLOR_FORCE", "1")
}

// ─── Decoration ─────────────────────────────────────────────────────────────

// Every decorative helper here returns its input untouched when colour is off
// and wraps it exactly once when colour is on. Two failure modes matter, and
// neither shows up as an error:
//
//   - a stray escape when colour is off pollutes --json output and every log
//     line written to a file;
//   - a missing reset leaks the style into whatever the caller prints next.
func TestDecorationWrapsOnlyWhenColourIsOn(t *testing.T) {
	const text = "sodium"

	seqs := map[string]string{
		"Bold":      BoldSeq,
		"Dim":       DimSeq,
		"Italic":    ItalicSeq,
		"Underline": UnderSeq,
		"Muted":     Palette.Muted,
		"Faint":     Palette.Faint,
		"Accent":    Palette.Accent,
		"Brand":     Palette.Brand,
		"OK":        Palette.OK,
		"Bad":       Palette.Err,
		"Warnc":     Palette.Warn,
		"InfoC":     Palette.Info,
		"Path":      Palette.Accent,
		"Highlight": "\x1b[7m",
	}

	fns := map[string]func(string) string{
		"Bold":      Bold,
		"Dim":       Dim,
		"Italic":    Italic,
		"Underline": Underline,
		"Muted":     Muted,
		"Faint":     Faint,
		"Accent":    Accent,
		"Brand":     Brand,
		"OK":        OK,
		"Bad":       Bad,
		"Warnc":     Warnc,
		"InfoC":     InfoC,
		"Path":      Path,
		"Highlight": Highlight,
	}

	colourAuto(t)
	for name, fn := range fns {
		t.Run(name, func(t *testing.T) {
			SetColorEnabled(false)
			if got := fn(text); got != text {
				t.Errorf("colour off: %s(%q) = %q, want the input unchanged", name, text, got)
			}

			SetColorEnabled(true)
			want := seqs[name] + text + Reset
			if got := fn(text); got != want {
				t.Errorf("colour on: %s(%q) = %q, want %q", name, text, got, want)
			}
			// A painted string has to measure the same as the bare one, or
			// every table column built from it drifts.
			if VisibleWidth(fn(text)) != len(text) {
				t.Errorf("colour on: %s(%q) measures %d cells, want %d",
					name, text, VisibleWidth(fn(text)), len(text))
			}
		})
	}

	// Paint is the generic form the error paths use with an arbitrary code.
	SetColorEnabled(true)
	if got, want := Paint(Palette.Err, text), Palette.Err+text+Reset; got != want {
		t.Errorf("Paint() = %q, want %q", got, want)
	}
	SetColorEnabled(false)
	if got := Paint(Palette.Err, text); got != text {
		t.Errorf("colour off: Paint() = %q, want the input unchanged", got)
	}
}

// ─── Explicit override ───────────────────────────────────────────────────────

// --color exists so a user can see colour in a pipe or a log file. That only
// works if setting the flag overrides the environment, which in a pipe or in CI
// is always asking for no colour.
func TestSetColorEnabledOverridesTheEnvironment(t *testing.T) {
	colourAuto(t)
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CLICOLOR_FORCE", "1")

	if ColorEnabled() {
		t.Fatal("precondition: colour should be off with NO_COLOR set")
	}

	// SetColor is the name the CLI uses; it must reach the same state.
	SetColor(true)
	if !ColorEnabled() {
		t.Error("--color did not enable colour with NO_COLOR set")
	}

	SetColor(false)
	if ColorEnabled() {
		t.Error("--no-color did not disable colour with CLICOLOR_FORCE=1 set")
	}

	// Once resolved, the explicit value is what every later read sees; it must
	// not fall back to re-probing the environment on each call.
	clearEnv(t, "NO_COLOR")
	if ColorEnabled() {
		t.Error("colour was re-detected after an explicit --no-color; the flag must stick")
	}
}

// ─── autoDetectColor ─────────────────────────────────────────────────────────

// The detection order is the de-facto contract with other tools: NO_COLOR wins
// over everything, TERM=dumb disables, and CLICOLOR_FORCE enables. The order
// matters as much as the values — a user who exports both NO_COLOR and
// CLICOLOR_FORCE has asked two contradictory things, and NO_COLOR is the one
// with a published spec.
func TestAutoDetectColorFollowsTheEnvironment(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string // unset keys are removed
		want bool
	}{
		{"NO_COLOR wins over CLICOLOR_FORCE", map[string]string{"NO_COLOR": "1", "CLICOLOR_FORCE": "1", "TERM": "xterm-256color"}, false},
		{"NO_COLOR is honoured even when empty", map[string]string{"NO_COLOR": "", "CLICOLOR_FORCE": "1", "TERM": "xterm-256color"}, false},
		// CLICOLOR_FORCE is listed after NO_COLOR and TERM in the documented
		// order, so it cannot rescue a terminal that cannot render. Each of
		// these three cases pairs the variable with CLICOLOR_FORCE=1: without
		// that pairing a broken TERM check would still report "no colour" via
		// the stdout-is-a-tty fallback and the test would pass either way.
		{"dumb terminal is not rescued by CLICOLOR_FORCE", map[string]string{"TERM": "dumb", "CLICOLOR_FORCE": "1"}, false},
		{"no TERM is not rescued by CLICOLOR_FORCE", map[string]string{"CLICOLOR_FORCE": "1"}, false},
		{"CLICOLOR_FORCE forces colour", map[string]string{"TERM": "xterm-256color", "CLICOLOR_FORCE": "1"}, true},
	}

	colourAuto(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"NO_COLOR", "TERM", "CLICOLOR_FORCE"} {
				clearEnv(t, k)
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := autoDetectColor(); got != tc.want {
				t.Errorf("autoDetectColor() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ─── InitColor ───────────────────────────────────────────────────────────────

// InitColor is the startup decision: the user's explicit --color/--no-color
// choice outranks both auto-detection and --quiet, and --quiet only ever
// suppresses colour that was not explicitly asked for.
//
// This is the matrix a regression in the branch order shows up in. Nothing
// else in the package calls InitColor with explicit set, so this is also the
// only place the rule is enforced at all.
func TestInitColorResolvesQuietAgainstAnExplicitChoice(t *testing.T) {
	cases := []struct {
		name     string
		quiet    bool
		explicit bool
		auto     bool // what auto-detection will report
		want     bool
	}{
		{"quiet suppresses auto-detected colour", true, false, true, false},
		{"quiet leaves auto-detected absence alone", true, false, false, false},
		{"auto-detected colour is used when not quiet", false, false, true, true},
		{"auto-detected absence is used when not quiet", false, false, false, false},
		// An explicit choice wins over auto-detection *and* over --quiet.
		// --quiet is about noise volume; someone who typed --color wants to
		// see the colours, and answering them with grey text instead is
		// worse than ignoring --quiet.
		{"explicit colour overrides auto-detection", false, true, false, true},
		{"explicit colour overrides quiet", true, true, false, true},
		{"explicit colour is honoured when colour was available anyway", false, true, true, true},
	}

	colourAuto(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.auto {
				forceColorEnv(t)
			} else {
				clearEnv(t, "NO_COLOR")
				t.Setenv("NO_COLOR", "1")
			}
			if got := autoDetectColor(); got != tc.auto {
				t.Fatalf("precondition: autoDetectColor() = %v, want %v", got, tc.auto)
			}

			InitColor(tc.quiet, tc.explicit)
			if got := ColorEnabled(); got != tc.want {
				t.Errorf("InitColor(quiet=%v, explicit=%v) with auto-detect=%v set colour=%v, want %v",
					tc.quiet, tc.explicit, tc.auto, got, tc.want)
			}
		})
	}
}

// ─── Terminal detection ──────────────────────────────────────────────────────

// IsTerminal decides both whether to emit escapes and whether it is safe to
// prompt. A regular file must never pass: that is a redirect, and it is the
// case that puts escape codes in a log file.
func TestIsTerminalRejectsFilesThatAreNotDevices(t *testing.T) {
	colourAuto(t)

	if IsTerminal(nil) {
		t.Error("IsTerminal(nil) = true, want false")
	}

	regular := filepath.Join(t.TempDir(), "out.log")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing the probe file: %v", err)
	}
	f, err := os.Open(regular)
	if err != nil {
		t.Fatalf("opening the probe file: %v", err)
	}
	defer f.Close()
	if IsTerminal(f) {
		t.Error("IsTerminal(regular file) = true, want false")
	}

	// A closed handle fails Stat, which must be read as "not a terminal"
	// rather than panicking or defaulting to yes.
	closed := filepath.Join(t.TempDir(), "closed")
	if err := os.WriteFile(closed, nil, 0o600); err != nil {
		t.Fatalf("writing the second probe file: %v", err)
	}
	g, err := os.Open(closed)
	if err != nil {
		t.Fatalf("opening the second probe file: %v", err)
	}
	if err := g.Close(); err != nil {
		t.Fatalf("closing the second probe file: %v", err)
	}
	if IsTerminal(g) {
		t.Error("IsTerminal(closed file) = true, want false")
	}

	// /dev/null is a character device on the unix CI legs, so it stands in for
	// a tty without opening a pty.
	if runtime.GOOS != "windows" {
		dev, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatalf("opening %s: %v", os.DevNull, err)
		}
		defer dev.Close()
		if !IsTerminal(dev) {
			t.Errorf("IsTerminal(%s) = false, want true: it is a character device", os.DevNull)
		}
	}
}

// isEnvSet decides whether MODHARBOR_NONINTERACTIVE is in force. The falsy
// spellings matter: a wrapper script that exports the variable empty is far
// more likely than one that means to disable prompting by setting it to "0".
func TestIsEnvSetTreatsFalsySpellingsAsUnset(t *testing.T) {
	cases := []struct {
		value string
		set   bool
		want  bool
	}{
		{"1", true, true},
		{"yes", true, true},
		{"true", true, true},
		{"on", true, true},
		{"noninteractive", true, true},
		{"", true, false},
		{"0", true, false},
		{"false", true, false},
		{"FALSE", true, false},
		{"False", true, false},
		{"no", true, false},
		{"NO", true, false},
		{"", false, false},
	}

	for _, tc := range cases {
		name := "unset"
		if tc.set {
			name = "set to " + tc.value
			if tc.value == "" {
				name = "set to empty"
			}
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("MH_TEST_FLAG", tc.value)
			if !tc.set {
				clearEnv(t, "MH_TEST_FLAG")
			}
			if got := isEnvSet("MH_TEST_FLAG"); got != tc.want {
				t.Errorf("isEnvSet() with the variable %s = %v, want %v", name, got, tc.want)
			}
		})
	}
}

// IsInteractive gates every prompt. The failure it prevents is the serious
// one: a CLI that blocks on a read inside a pipeline or a cron job hangs until
// stdin closes, with no prompt ever visible to explain why.
func TestIsInteractiveRefusesToPromptWhenToldNotTo(t *testing.T) {
	colourAuto(t)
	clearEnv(t, "MODHARBOR_NONINTERACTIVE")

	orig := os.Stdin
	t.Cleanup(func() { os.Stdin = orig })

	// A regular file is a redirect, not a terminal: not promptable.
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatalf("creating the stdin probe: %v", err)
	}
	defer f.Close()
	os.Stdin = f
	if IsInteractive() {
		t.Error("IsInteractive() = true with stdin redirected from a file, want false")
	}

	if runtime.GOOS == "windows" {
		return // /dev/null is not a reliable character-device probe here.
	}

	dev, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}
	defer dev.Close()
	os.Stdin = dev

	// A character device is promptable unless the user opted out, and the
	// opt-out has to dominate: it is how CI and wrappers disable prompting
	// regardless of how stdin is wired.
	t.Setenv("MODHARBOR_NONINTERACTIVE", "1")
	if IsInteractive() {
		t.Error("IsInteractive() = true with MODHARBOR_NONINTERACTIVE=1, want false")
	}
	t.Setenv("MODHARBOR_NONINTERACTIVE", "0")
	if !IsInteractive() {
		t.Error("IsInteractive() = false with MODHARBOR_NONINTERACTIVE=0, want true: stdin is a device")
	}
}

// ─── Writers ─────────────────────────────────────────────────────────────────

// SetWriters is how every test in this repo captures output. A nil error
// writer silently leaving the process's real stderr in place is a trap: a test
// would print to the terminal and still pass, so the capture would appear to
// work while checking nothing.
func TestSetWritersRoutesBothStreamsAndRestoresStderr(t *testing.T) {
	colourAuto(t)

	// The package-level writers are process-wide state; leaving a builder
	// installed would send every later test's output into a dead buffer.
	origOut, origErrOut := out, errOut
	t.Cleanup(func() { out, errOut = origOut, origErrOut })

	var outBuf, errBuf strings.Builder
	SetWriters(&outBuf, &errBuf)

	Fprintf("to stdout %d", 1)
	Eprintf("to stderr %d", 2)
	Printf("aliased %s", "stdout")
	Line("a raw line")
	Blank()

	if got, want := outBuf.String(), "to stdout 1aliased stdouta raw line\n\n"; got != want {
		t.Errorf("stdout capture = %q, want %q", got, want)
	}
	if got, want := errBuf.String(), "to stderr 2"; got != want {
		t.Errorf("stderr capture = %q, want %q", got, want)
	}

	// A nil error writer has to fall back to the process's real stderr rather
	// than to a discarded writer, which would swallow the diagnostics a caller
	// meant to see. (The stdout side has no such fallback: passing nil there is
	// a programming error, and Line on a nil writer panics loudly instead of
	// quietly dropping the output.)
	SetWriters(nil, nil)
	if errOut != os.Stderr {
		t.Errorf("SetWriters(nil, nil) left errOut = %v, want the process stderr", errOut)
	}
	if out != nil {
		t.Errorf("SetWriters(nil, nil) installed a stdout writer (%v); it should install none", out)
	}

	// And the consequence of that asymmetry: printing with no writer panics
	// rather than silently dropping the line. A test that hits this has passed
	// nil by mistake, and it should hear about it immediately.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("Line with no stdout writer did not panic; a nil writer would swallow the output")
			}
		}()
		Line("into the void")
	}()
}
