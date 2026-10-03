// Package ui renders the modharbor terminal experience: colour, symbols,
// boxes, tables, progress bars and prompts.
//
// Zero third-party dependencies. Everything degrades to plain ASCII-safe
// text when stdout is not a TTY, when NO_COLOR is set, or when --no-color
// is passed.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// ─── Brand ──────────────────────────────────────────────────────────────────

// Palette holds the semantic colour codes used across the CLI.
var Palette = struct {
	Brand    string
	BrandDim string
	OK       string
	Warn     string
	Err      string
	Info     string
	Accent   string
	Muted    string
	Faint    string
	Rule     string
	HeaderBg string
}{
	Brand:    "\x1b[38;5;39m",  // bright cyan-blue
	BrandDim: "\x1b[38;5;25m",  // deep blue
	OK:       "\x1b[38;5;42m",  // green
	Warn:     "\x1b[38;5;214m", // amber
	Err:      "\x1b[38;5;203m", // soft red
	Info:     "\x1b[38;5;75m",  // steel blue
	Accent:   "\x1b[38;5;141m", // violet
	Muted:    "\x1b[38;5;245m", // grey
	Faint:    "\x1b[38;5;240m", // dark grey
	Rule:     "\x1b[38;5;238m", // hairline
	HeaderBg: "\x1b[48;5;236m", // header background
}

// ─── Symbols ────────────────────────────────────────────────────────────────

// Status glyphs. A `--ascii` mode swaps these for ASCII equivalents.
const (
	SymOK      = "✔"
	SymFail    = "✖"
	SymWarn    = "▲"
	SymInfo    = "•"
	SymPending = "◐"
	SymArrow   = "→"
	SymBullet  = "│"
	SymMove    = "⇢"
	SymPlus    = "+"
	SymMinus   = "−"
	SymStar    = "★"
	SymDot     = "·"
	SymSkip    = "⊘"
	SymFix     = "✚"
)

var asciiSymbols = map[string]string{
	SymOK: "+", SymFail: "x", SymWarn: "!", SymInfo: "*", SymPending: "~",
	SymArrow: "->", SymBullet: "|", SymMove: "=>", SymPlus: "+",
	SymMinus: "-", SymStar: "*", SymDot: ".", SymSkip: "0", SymFix: "+",
}

// Box drawing pieces.
const (
	boxTL = "╭"
	boxTR = "╮"
	boxBL = "╰"
	boxBR = "╯"
	boxH  = "─"
	boxV  = "│"
)

// barFill / barEmpty build gradient-free but crisp progress bars.
const (
	barFull  = "█"
	barPart1 = "▓"
	barPart2 = "▒"
	barPart3 = "░"
	barEmpty = "░"
)

// ─── Low-level printing ─────────────────────────────────────────────────────

var out io.Writer = os.Stdout
var errOut io.Writer = os.Stderr

// SetWriters overrides output streams (used by tests).
func SetWriters(o, e io.Writer) {
	out = o
	if e == nil {
		e = os.Stderr
	}
	errOut = e
}

// SetColor forces colour output on/off.
func SetColor(on bool) { SetColorEnabled(on) }

// Fprintf writes formatted output to stdout.
func Fprintf(format string, a ...any) { _, _ = fmt.Fprintf(out, format, a...) }

// Eprintf writes formatted output to stderr.
func Eprintf(format string, a ...any) { _, _ = fmt.Fprintf(errOut, format, a...) }

// Printf writes formatted output to stdout.
func Printf(format string, a ...any) { Fprintf(format, a...) }

// Line writes a raw line to stdout.
func Line(s string) { _, _ = fmt.Fprintln(out, s) }

// Blank writes an empty line to stdout.
func Blank() { _, _ = fmt.Fprintln(out) }

// ─── Status lines ───────────────────────────────────────────────────────────

func paint(c, s string) string {
	if !ColorEnabled() {
		return s
	}
	return c + s + Reset
}

// Paint applies an arbitrary ANSI colour code from the palette.
func Paint(c, s string) string { return paint(c, s) }

// The status helpers below both print a line to stdout and return the
// rendered string, so `ui.Success("done")` behaves the way call sites expect.
// Use InfoC/Muted/OK when a coloured fragment is needed inside a larger
// expression instead.

// Success prints a green ✔ line.
func Success(format string, a ...any) string {
	s := line(SymOK, Palette.OK, format, a...)
	Line(s)
	return s
}

// Failure prints a red ✖ line.
func Failure(format string, a ...any) string {
	s := line(SymFail, Palette.Err, format, a...)
	Line(s)
	return s
}

// Warn prints an amber ▲ line.
func Warn(format string, a ...any) string {
	s := line(SymWarn, Palette.Warn, format, a...)
	Line(s)
	return s
}

// Info prints a steel-blue • line.
func Info(format string, a ...any) string {
	s := line(SymInfo, Palette.Info, format, a...)
	Line(s)
	return s
}

// Note prints a dim explanatory line.
func Note(format string, a ...any) string {
	s := paint(Palette.Faint, sprint(format, a...))
	Line(s)
	return s
}

// sprint renders a message for the status helpers above.
//
// A caller with nothing to interpolate passes data, not a format string, and
// data can legitimately contain a percent sign — "shards 95% done" is a
// sentence modharbor has reason to print. Handing that to Sprintf anyway eats
// the rest of the line as a verb ("95% done" becomes "95%!d(MISSING)one"), so
// the argumentless case is returned verbatim. With arguments the behaviour is
// unchanged.
func sprint(format string, a ...any) string {
	if len(a) == 0 {
		return format
	}
	return fmt.Sprintf(format, a...)
}

func line(sym, color, format string, a ...any) string {
	return paint(color, sym) + " " + sprint(format, a...)
}

// ─── Headings ───────────────────────────────────────────────────────────────

// Heading prints a branded banner with an optional subtitle. Used by the root
// command and long-running operations.
func Heading(title, subtitle string) {
	Blank()
	Line(brandBar())
	Line("  " + Bold(title))
	if subtitle != "" {
		Line("  " + paint(Palette.Muted, subtitle))
	}
	Line(brandBar())
}

func brandBar() string {
	if !ColorEnabled() {
		return strings.Repeat(boxH, min(Width(), 72))
	}
	w := min(Width(), 72)
	var b strings.Builder
	b.WriteString(Palette.Brand)
	for i := 0; i < w; i++ {
		if i%3 == 0 {
			b.WriteString(Palette.BrandDim)
		}
		b.WriteString(boxH)
	}
	b.WriteString(Reset)
	return b.String()
}

// Section prints a compact section label followed by a hairline rule.
func Section(title string) {
	Blank()
	Line("  " + Bold(paint(Palette.Brand, title)) + " " + paint(Palette.Rule, strings.Repeat(boxH, max(4, min(Width()-4-len(title), 60)))))
}

// Hint prints a dimmed hint line, usually a next-step suggestion.
func Hint(format string, a ...any) {
	Line("  " + paint(Palette.Faint, "└─ "+fmt.Sprintf(format, a...)))
}

// ─── Key/value rendering ────────────────────────────────────────────────────

// KV renders an aligned "key: value" pair.
func KV(key, value string) string {
	return paint(Palette.Muted, key) + "  " + value
}

// Count renders a coloured tally like "3 updated, 1 skipped".
func Count(n int, singular, plural string, color string) string {
	word := singular
	if n != 1 {
		word = plural
	}
	return paint(color, fmt.Sprintf("%d %s", n, word))
}

// ─── Version diff ───────────────────────────────────────────────────────────

// Diff renders `old → new` where old is dimmed red and new is bold green.
func Diff(old, new string) string {
	arrow := paint(Palette.Faint, " "+SymArrow+" ")
	return paint(Palette.Err, old) + arrow + Bold(paint(Palette.OK, new))
}

// Arrow renders a bare separator.
func Arrow() string { return paint(Palette.Faint, SymArrow) }

// ─── Misc text helpers ──────────────────────────────────────────────────────

// Muted wraps s in the muted palette colour.
func Muted(s string) string { return paint(Palette.Muted, s) }

// Faint wraps s in the faint palette colour.
func Faint(s string) string { return paint(Palette.Faint, s) }

// Accent wraps s in the accent palette colour.
func Accent(s string) string { return paint(Palette.Accent, s) }

// Brand wraps s in the brand colour.
func Brand(s string) string { return paint(Palette.Brand, s) }

// OK wraps s in the success colour.
func OK(s string) string { return paint(Palette.OK, s) }

// Bad wraps s in the error colour.
func Bad(s string) string { return paint(Palette.Err, s) }

// Warnc wraps s in the warning colour.
func Warnc(s string) string { return paint(Palette.Warn, s) }

// InfoC wraps s in the info colour.
func InfoC(s string) string { return paint(Palette.Info, s) }

// Path renders a filesystem path in the accent colour for scanability.
func Path(s string) string { return paint(Palette.Accent, s) }

// Highlight renders text with a reverse-video highlight (used for search hits).
func Highlight(s string) string {
	if !ColorEnabled() {
		return s
	}
	return "\x1b[7m" + s + Reset
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
