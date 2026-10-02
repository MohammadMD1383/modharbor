package ui

import (
	"strings"
	"testing"
)

// Width measurement drives every table column. The escapes this package emits
// are multi-byte and zero-width, so counting bytes or runes would push every
// coloured cell out of alignment — which renders as a ragged table rather than
// as an error, and is invisible to any test that only compares strings.
func TestVisibleWidthIgnoresAnsiEscapes(t *testing.T) {
	// Escapes are pasted in literally rather than produced by paint(), so the
	// test does not depend on colour being enabled.
	const (
		red   = "\x1b[31m"
		faint = "\x1b[38;5;240m"
		reset = "\x1b[0m"
		bg    = "\x1b[48;5;236m"
		// An SGR sequence whose final byte never arrived.
		truncated = "\x1b[38;5;24"
	)

	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"plain", "Sodium", 6},
		{"surrounded by colour", red + "Sodium" + reset, 6},
		{"faint foreground", faint + "13.5 MB" + reset, 7},
		{"background colour", bg + "MOD" + reset, 3},
		{"colour inside the text", "So" + red + "di" + reset + "um", 6},
		{"two coloured runs", red + "a" + reset + faint + "b" + reset, 2},
		// An escape that never terminates swallows the rest of the string.
		// 'm' closes an SGR sequence and an uppercase letter closes a cursor
		// move, so anything else is still treated as sequence content.
		{"unterminated numeric escape", truncated + "abc", 0},
		{"only escapes", red + reset, 0},
		// Multi-byte text is counted per rune, not per byte.
		{"multi-byte runes", "café", 4},
		{"box drawing", "╭─╮", 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := VisibleWidth(tc.in); got != tc.want {
				t.Errorf("VisibleWidth(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// Pad is what aligns a table. If it measured the escapes it would over-pad by
// the length of every colour sequence, and columns would drift further apart
// the more colourful the table got.
func TestPadMeasuresVisibleCellsNotEscapes(t *testing.T) {
	const (
		red   = "\x1b[31m"
		reset = "\x1b[0m"
	)

	cases := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{"plain pads to width", "ab", 5, "ab   "},
		{"colour does not change the padding", red + "ab" + reset, 5, red + "ab" + reset + "   "},
		{"already wide enough is untouched", red + "abcdef" + reset, 5, red + "abcdef" + reset},
		{"exact width is untouched", "abcde", 5, "abcde"},
		// Pad only ever grows a cell. Truncation is Truncate's job, so asking Pad for
		// a width narrower than the text is a no-op rather than a cut.
		{"zero width leaves the cell alone", "abc", 0, "abc"},
		{"empty string pads fully", "", 3, "   "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Pad(tc.in, tc.width)
			if got != tc.want {
				t.Errorf("Pad() = %q, want %q", got, tc.want)
			}
			// Whatever the padding, the cell must occupy exactly the
			// requested number of visible cells.
			if w := VisibleWidth(got); w != tc.width && VisibleWidth(tc.in) < tc.width {
				t.Errorf("padded cell is %d visible cells wide, want %d", w, tc.width)
			}
		})
	}
}

// PadLeft right-aligns numeric columns; it has to measure the same way Pad does.
func TestPadLeftMeasuresVisibleCellsNotEscapes(t *testing.T) {
	const (
		faint = "\x1b[38;5;240m"
		reset = "\x1b[0m"
	)

	got := PadLeft(faint+"1,234"+reset, 9)
	want := "    " + faint + "1,234" + reset
	if got != want {
		t.Errorf("PadLeft() = %q, want %q", got, want)
	}
	if w := VisibleWidth(got); w != 9 {
		t.Errorf("padded cell is %d visible cells wide, want 9", w)
	}
}

// Truncate has to leave a string that still fits the cell it was cut for,
// otherwise a long mod name silently widens the column it overflowed.
func TestTruncateNeverExceedsTheRequestedWidth(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
	}{
		{"well over", strings.Repeat("x", 40), 10},
		{"one over", "abcdefghijk", 10},
		{"exactly at the limit", "abcdefghij", 10},
		{"well under", "ab", 10},
		{"empty", "", 10},
		{"multi-byte runes", strings.Repeat("café", 5), 10},
		{"a single cell", strings.Repeat("x", 10), 1},
		{"no room at all", "abcdef", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Truncate(tc.in, tc.width)
			if w := VisibleWidth(got); w > tc.width {
				t.Errorf("Truncate(%q, %d) = %q, which is %d cells wide", tc.in, tc.width, got, w)
			}
			if tc.width > 0 && len(tc.in) <= tc.width && got != tc.in {
				t.Errorf("Truncate(%q, %d) = %q, want the input unchanged", tc.in, tc.width, got)
			}
		})
	}
}

// A truncated cell ends in an ellipsis so the reader knows something was cut,
// and a name that fits is never given one.
func TestTruncateMarksWhatItCutWithAnEllipsis(t *testing.T) {
	got := Truncate("abcdefghijklmno", 8)
	if want := "abcdefg…"; got != want {
		t.Errorf("Truncate() = %q, want %q", got, want)
	}
	if got := Truncate("abc", 8); strings.Contains(got, "…") {
		t.Errorf("Truncate(%q, 8) = %q, want no ellipsis on a name that fits", "abc", got)
	}
}

// Colour is a global, and every table cell in the CLI is built by passing
// painted strings through Pad and Truncate. So the width helpers have to be
// correct whether or not colour happened to be on when they were called.
func TestWidthHelpersMeasureTheSameWithColourOnOrOff(t *testing.T) {
	orig := colorEnabled
	t.Cleanup(func() { colorEnabled = orig })

	const text = "sodium-0.10.0.jar"

	for _, on := range []bool{false, true} {
		SetColorEnabled(on)

		// The decorated and plain strings describe the same text, so they
		// must measure the same even though one is far longer in bytes.
		painted := OK(text)
		if VisibleWidth(painted) != VisibleWidth(text) {
			t.Errorf("colour=%v: VisibleWidth(%q) = %d, want %d",
				on, painted, VisibleWidth(painted), VisibleWidth(text))
		}
		if got, want := len(painted), len(text); on != (got != want) {
			t.Errorf("colour=%v: painted length %d, plain length %d; the escape should only appear when colour is on",
				on, got, want)
		}
		if w := VisibleWidth(Pad(painted, 40)); w != 40 {
			t.Errorf("colour=%v: Pad produced %d visible cells, want 40", on, w)
		}
	}
	SetColorEnabled(false)
}

// Plural backs every "%d mod(s)" the CLI prints, so the singular case has to be
// exactly one — including for zero, where "1 mod" would be a lie.
func TestPluralAgreesWithTheCount(t *testing.T) {
	cases := []struct {
		n          int
		singular   string
		plural     string
		wantPlural bool
	}{
		{0, "mod", "mods", true},
		{1, "mod", "mods", false},
		{2, "mod", "mods", true},
		{-1, "mod", "mods", true},
	}
	for _, tc := range cases {
		got := Plural(tc.n, tc.singular, tc.plural)
		if (got == tc.plural) != tc.wantPlural {
			t.Errorf("Plural(%d, %q, %q) = %q", tc.n, tc.singular, tc.plural, got)
		}
	}
}
