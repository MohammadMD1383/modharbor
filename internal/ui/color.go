package ui

import (
	"os"
	"strings"
	"sync/atomic"
)

// ANSI control sequences.
const (
	Reset     = "\x1b[0m"
	BoldSeq   = "\x1b[1m"
	DimSeq    = "\x1b[2m"
	ItalicSeq = "\x1b[3m"
	UnderSeq  = "\x1b[4m"
)

// Bold renders s in bold.
func Bold(s string) string { return wrapSeq(BoldSeq, s) }

// Dim renders s dimmed.
func Dim(s string) string { return wrapSeq(DimSeq, s) }

// Italic renders s in italics.
func Italic(s string) string { return wrapSeq(ItalicSeq, s) }

// Underline renders s underlined.
func Underline(s string) string { return wrapSeq(UnderSeq, s) }

func wrapSeq(seq, s string) string {
	if !ColorEnabled() {
		return s
	}
	return seq + s + Reset
}

// colorEnabled is the resolved flag; nil means "auto-detect on first use".
// It is an atomic pointer because Spinner/Progress read it from background
// goroutines while tests (and InitColor) may write it concurrently.
var colorEnabled atomic.Pointer[bool]

// SetColorEnabled forces colour output on or off.
func SetColorEnabled(on bool) {
	v := on
	colorEnabled.Store(&v)
}

// ColorEnabled reports whether ANSI colour should be emitted.
func ColorEnabled() bool {
	if v := colorEnabled.Load(); v != nil {
		return *v
	}
	return autoDetectColor()
}

// InitColor resolves colour support once, at startup.
//
// quiet suppresses decorative output; explicit reflects --color/--no-color so
// the user's explicit choice always wins over auto-detection.
func InitColor(quiet, explicit bool) {
	// An explicit --color outranks both auto-detection and --quiet: --quiet is
	// about noise, and answering someone who asked for colour with grey text is
	// worse than ignoring --quiet.
	if explicit {
		SetColorEnabled(true)
		return
	}
	SetColorEnabled(autoDetectColor() && !quiet)
}

func autoDetectColor() bool {
	// Honour the community NO_COLOR convention and TERM=dumb.
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	if t := os.Getenv("TERM"); t == "" || t == "dumb" {
		return false
	}
	// Respect CLICOLOR_FORCE for CI environments that want colour.
	if os.Getenv("CLICOLOR_FORCE") == "1" {
		return true
	}
	return IsTerminal(os.Stdout)
}

// IsTerminal reports whether f is attached to a character device.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// IsInteractive reports whether we can safely prompt the user.
func IsInteractive() bool {
	return IsTerminal(os.Stdin) && !isEnvSet("MODHARBOR_NONINTERACTIVE")
}

func isEnvSet(name string) bool {
	v, ok := os.LookupEnv(name)
	if !ok {
		return false
	}
	switch strings.ToLower(v) {
	case "", "0", "false", "no":
		return false
	}
	return true
}

// ─── Text measurement ───────────────────────────────────────────────────────

// VisibleWidth returns the display width of s, ignoring ANSI escape sequences.
func VisibleWidth(s string) int {
	n := 0
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			if r == 'm' || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
		case r == 0x1b:
			inEsc = true
		default:
			n++
		}
	}
	return n
}

// Pad right-pads s with spaces to reach n visible cells.
func Pad(s string, n int) string {
	d := n - VisibleWidth(s)
	if d <= 0 {
		return s
	}
	return s + strings.Repeat(" ", d)
}

// PadLeft left-pads s with spaces to reach n visible cells.
func PadLeft(s string, n int) string {
	d := n - VisibleWidth(s)
	if d <= 0 {
		return s
	}
	return strings.Repeat(" ", d) + s
}

// Truncate shortens s to at most n visible cells, appending an ellipsis.
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if VisibleWidth(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	r := []rune(s)
	if len(r) > n-1 {
		r = r[:n-1]
	}
	return string(r) + "…"
}
