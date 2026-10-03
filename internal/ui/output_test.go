package ui

import (
	"strings"
	"testing"
)

// ─── Test helpers ───────────────────────────────────────────────────────────

// captureStdout redirects the package's stdout writer for the duration of one
// call and returns what was printed. The package-level writer is process-wide,
// so it is always restored.
//
// It exists to test the dual contract below: Success and friends both print a
// line *and* return the rendered string. A capture that leaked would send a
// later test's output into a discarded buffer and let it pass while checking
// nothing.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := out
	t.Cleanup(func() { out = orig })

	var buf strings.Builder
	out = &buf
	fn()
	return buf.String()
}

// printedLines splits captured output into lines, dropping the trailing empty
// element so one Line() call reads as one element rather than two.
func printedLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// stripEscapes removes ANSI SGR sequences so a test can assert on the
// characters a user actually sees. Written here rather than reusing
// VisibleWidth's counter because these tests compare text, not measures.
func stripEscapes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			i++
			continue
		}
		for i < len(s) {
			c := s[i]
			i++
			if c == 'm' || (c >= 'A' && c <= 'Z') {
				break
			}
		}
	}
	return b.String()
}

// ─── The print-and-return contract ───────────────────────────────────────────

// statusHelper pairs a helper with the symbol it leads with, so the two facts
// about it — which glyph, and that it prints — live in one place.
type statusHelper struct {
	name string
	sym  string
	fn   func(string, ...any) string
}

func statusHelpers() []statusHelper {
	return []statusHelper{
		{"Success", SymOK, Success},
		{"Failure", SymFail, Failure},
		{"Warn", SymWarn, Warn},
		{"Info", SymInfo, Info},
		{"Note", "", Note},
	}
}

// Success, Failure, Warn, Info and Note are the odd ones out in this package:
// every other helper returns a fragment and prints nothing, these do both.
// That is deliberate — the call sites read as statements — but it is exactly
// the contract that produced a real bug during development, where calling
// ui.Info inline inside a larger expression printed in the wrong place.
//
// So for each one the printed bytes and the returned string have to be the
// same, and printing must happen exactly once. "Once" matters as much as
// "correctly": these helpers sit in loops over mods, and a duplicate print
// turns a 33-mod summary into 66 lines.
func TestStatusHelpersPrintExactlyWhatTheyReturn(t *testing.T) {
	colourAuto(t)

	for _, on := range []bool{false, true} {
		SetColorEnabled(on)
		for _, tc := range statusHelpers() {
			t.Run(tc.name, func(t *testing.T) {
				var got string
				printed := captureStdout(t, func() { got = tc.fn("migrated %d mods", 3) })

				if printed != got+"\n" {
					t.Errorf("colour=%v: printed %q but returned %q; by contract they are the same string",
						on, printed, got)
				}
				if lines := printedLines(printed); len(lines) != 1 {
					t.Errorf("colour=%v: printed %d lines (%q), want exactly 1", on, len(lines), printed)
				}
				if !strings.Contains(printed, "migrated 3 mods") {
					t.Errorf("colour=%v: printed %q, want the formatted message %q",
						on, printed, "migrated 3 mods")
				}
			})
		}
	}
}

// With colour off these lines are plain text: a glyph, a space, the message.
// A stray escape here lands in a --json payload or a redirected log, which is
// the failure the colour tests in color_test.go guard against elsewhere.
func TestStatusHelpersRenderPlainTextWhenColourIsOff(t *testing.T) {
	colourAuto(t)
	SetColorEnabled(false)

	for _, tc := range statusHelpers() {
		t.Run(tc.name, func(t *testing.T) {
			want := "upstream rate limited"
			if tc.sym != "" {
				want = tc.sym + " " + want
			}
			if got := tc.fn("upstream rate limited"); got != want {
				t.Errorf("colour off: %s() = %q, want %q", tc.name, got, want)
			}
		})
	}

	// Note has no glyph — it is the explanatory line under a result, so it
	// starts flush left. A leading space here would offset every note.
	if got, want := Note("checked"), "checked"; got != want {
		t.Errorf("colour off: Note() = %q, want %q (no glyph, no leading space)", got, want)
	}
}

// With colour on, each helper paints its own glyph in its own palette colour
// and leaves the message itself unpainted.
//
// Leaving the message unpainted is the point: callers interpolate these lines
// into tables and summaries, and a colour that bleeds past the glyph would
// colour whatever the caller prints next.
func TestStatusHelpersPaintOnlyTheirGlyph(t *testing.T) {
	colourAuto(t)
	SetColorEnabled(true)

	colors := map[string]string{
		"Success": Palette.OK,
		"Failure": Palette.Err,
		"Warn":    Palette.Warn,
		"Info":    Palette.Info,
		"Note":    Palette.Faint,
	}

	const msg = "3 updated"
	for _, tc := range statusHelpers() {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.fn(msg)

			if !strings.Contains(got, msg) {
				t.Errorf("%s(%q) = %q, does not contain the message verbatim", tc.name, msg, got)
			}

			if tc.sym == "" {
				// Note paints the whole line; there is no glyph to stop at.
				if want := Palette.Faint + msg + Reset; got != want {
					t.Errorf("Note(%q) = %q, want %q", msg, got, want)
				}
				return
			}
			if prefix := colors[tc.name] + tc.sym + Reset + " "; !strings.HasPrefix(got, prefix) {
				t.Errorf("%s(%q) = %q, want it to start with the painted glyph %q",
					tc.name, msg, got, prefix)
			}
			// Whatever the decoration, the line has to measure as glyph+space+
			// message, or an aligned summary built from it drifts.
			if w, want := VisibleWidth(got), VisibleWidth(msg)+2; w != want {
				t.Errorf("%s(%q) measures %d cells, want %d", tc.name, msg, w, want)
			}
		})
	}
}

// The five helpers share one implementation, and it formats the message with
// fmt. A format string reaching them with no arguments is the case nothing
// else exercises: every message the CLI passes today is either a bare literal
// with no verbs or a format with arguments, so the two never interact in
// production and the interaction is untested by accident rather than by
// design.
//
// A literal percent is the realistic source — "95% of shards", "100% done" —
// and Sprintf eats the rest of the line as a verb. This test fails on that,
// which is the point.
func TestStatusHelpersDoNotMangleAnArgumentlessPercent(t *testing.T) {
	colourAuto(t)
	SetColorEnabled(false)

	const msg = "shards 95% done"
	for _, tc := range statusHelpers() {
		t.Run(tc.name, func(t *testing.T) {
			want := msg
			if tc.sym != "" {
				want = tc.sym + " " + msg
			}
			if got := tc.fn(msg); got != want {
				t.Errorf("%s(%q) = %q, want %q — an argumentless message is data, not a format string",
					tc.name, msg, got, want)
			}
		})
	}
}

// ─── Headings ───────────────────────────────────────────────────────────────

// Heading is the banner every long-running command opens with. Two things have
// to hold: an empty subtitle prints no line of its own, and the two rules are
// the same string, which is what makes the banner look closed.
func TestHeadingRendersABannerAroundItsTitle(t *testing.T) {
	colourAuto(t)

	for _, on := range []bool{false, true} {
		SetColorEnabled(on)

		// blank, rule, title, subtitle, rule.
		withSub := printedLines(captureStdout(t, func() { Heading("Migrating", "26.2 → 26.3") }))
		if len(withSub) != 5 {
			t.Fatalf("colour=%v: Heading with a subtitle printed %d lines (%q), want 5",
				on, len(withSub), withSub)
		}
		if withSub[0] != "" {
			t.Errorf("colour=%v: Heading should open with a blank line, got %q", on, withSub[0])
		}
		if !strings.Contains(withSub[2], "Migrating") {
			t.Errorf("colour=%v: title line = %q, want it to contain %q", on, withSub[2], "Migrating")
		}
		if !strings.HasPrefix(withSub[2], "  ") {
			t.Errorf("colour=%v: title line = %q, want it indented two spaces like every heading", on, withSub[2])
		}
		if !strings.Contains(withSub[3], "26.2 → 26.3") {
			t.Errorf("colour=%v: subtitle line = %q, want it to contain %q", on, withSub[3], "26.2 → 26.3")
		}
		if withSub[1] != withSub[4] {
			t.Errorf("colour=%v: the two rules differ: %q vs %q", on, withSub[1], withSub[4])
		}

		// No subtitle means no subtitle line; a dangling blank there reads as
		// a half-rendered banner.
		noSub := printedLines(captureStdout(t, func() { Heading("Scanning", "") }))
		if len(noSub) != 4 {
			t.Fatalf("colour=%v: Heading without a subtitle printed %d lines (%q), want 4",
				on, len(noSub), noSub)
		}
		if !strings.Contains(noSub[2], "Scanning") {
			t.Errorf("colour=%v: title line = %q, want it to contain %q", on, noSub[2], "Scanning")
		}
		if noSub[1] != noSub[3] {
			t.Errorf("colour=%v: the two rules differ: %q vs %q", on, noSub[1], noSub[3])
		}
	}
}

// brandBar is a rule of box-drawing characters capped at 72 columns, so it does
// not stretch across an ultrawide monitor next to a 140-column table. Both the
// cap and the character count are the whole contract: the number has to be
// derived from Width() rather than hard-coded, or the banner is the wrong width
// on every machine.
func TestBrandBarIsARuleCappedAt72Columns(t *testing.T) {
	colourAuto(t)

	for _, on := range []bool{false, true} {
		SetColorEnabled(on)

		got := brandBar()
		if w, want := VisibleWidth(got), min(Width(), 72); w != want {
			t.Errorf("colour=%v: brandBar() measures %d cells, want %d (Width() is %d, capped at 72)",
				on, w, want, Width())
		}
		if strings.ContainsAny(got, "\t\n") {
			t.Errorf("colour=%v: brandBar() = %q, want a single unbroken line", on, got)
		}
		// A horizontal rule made of vertical strokes still measures 72, so
		// the width check alone would not notice.
		if strings.ContainsRune(got, []rune(boxV)[0]) {
			t.Errorf("colour=%v: brandBar() = %q, want only horizontal rule characters", on, got)
		}
		if !strings.ContainsRune(got, []rune(boxH)[0]) {
			t.Errorf("colour=%v: brandBar() = %q, want it drawn with the horizontal rule character", on, got)
		}
		// A coloured bar still ends in a reset, or the brand colour leaks
		// into whatever the caller prints after it.
		if on && !strings.HasSuffix(got, Reset) {
			t.Errorf("colour on: brandBar() = %q, want it to end in a reset", got)
		}
		if !on && strings.Contains(got, "\x1b") {
			t.Errorf("colour off: brandBar() = %q, want no escapes", got)
		}
	}
}

// Section draws a title and a hairline rule on one line. The rule has a floor
// of four characters, so a title long enough to leave no room still renders as
// a recognisable section rather than as a dangling space.
//
// The overlong case is the one that can panic or collapse: the computed length
// is Width()-4-len(title), which goes negative on a narrow terminal or a long
// title, and the floor is the only thing between that and an empty rule.
func TestSectionRendersATitleAndARuleOnOneLine(t *testing.T) {
	colourAuto(t)

	for _, on := range []bool{false, true} {
		SetColorEnabled(on)

		t.Run("short title", func(t *testing.T) {
			got := printedLines(captureStdout(t, func() { Section("Mods") }))
			if len(got) != 2 {
				t.Fatalf("colour=%v: Section printed %d lines (%q), want a blank line and a heading",
					on, len(got), got)
			}
			if !strings.Contains(got[1], "Mods") {
				t.Errorf("colour=%v: section line = %q, want it to contain %q", on, got[1], "Mods")
			}
			if !strings.Contains(got[1], boxH) {
				t.Errorf("colour=%v: section line = %q, want a rule after the title", on, got[1])
			}
		})

		t.Run("overlong title", func(t *testing.T) {
			long := strings.Repeat("verylongsectiontitle", 20)
			got := printedLines(captureStdout(t, func() { Section(long) }))
			if len(got) != 2 {
				t.Fatalf("colour=%v: Section printed %d lines (%q), want 2", on, len(got), got)
			}
			if n := strings.Count(got[1], boxH); n < 4 {
				t.Errorf("colour=%v: section rule = %q (%d cells), want the floor of 4", on, got[1], n)
			}
		})
	}
}

// Hint is the next-step suggestion under a result. It is indented and prefixed
// with the └─ elbow so a reader can tell it apart from the result itself, and
// it formats its arguments like the status helpers.
func TestHintIsIndentedPrefixedAndFormatted(t *testing.T) {
	colourAuto(t)

	SetColorEnabled(false)
	plain := printedLines(captureStdout(t, func() { Hint("try %s", "modharbor doctor") }))
	if len(plain) != 1 {
		t.Fatalf("Hint printed %d lines (%q), want 1", len(plain), plain)
	}
	if want := "  └─ try modharbor doctor"; plain[0] != want {
		t.Errorf("colour off: Hint = %q, want %q", plain[0], want)
	}

	// With colour on the same line is still the same line: the elbow is
	// painted, not replaced by an escape the reader has to decode.
	SetColorEnabled(true)
	painted := printedLines(captureStdout(t, func() { Hint("try %s", "modharbor doctor") }))
	if len(painted) != 1 {
		t.Fatalf("colour on: Hint printed %d lines (%q), want 1", len(painted), painted)
	}
	if got, want := stripEscapes(painted[0]), "  └─ try modharbor doctor"; got != want {
		t.Errorf("colour on: Hint renders as %q, want %q", got, want)
	}
}

// ─── Key/value and inline fragments ─────────────────────────────────────────

// KV is the summary block a command would end with. It does not print, so a
// caller can embed it, and it paints the key while leaving the value alone —
// the value is usually already decorated, or is data that must not carry a
// style out of the line.
//
// The bare percent matters: these are rendered by concatenation, and the test
// guards that they stay that way rather than acquiring a Sprintf, where "95%"
// would become "95%!(NOVERB)".
func TestKVPaintsTheKeyAndLeavesTheValueAlone(t *testing.T) {
	colourAuto(t)

	SetColorEnabled(true)
	if got, want := KV("coverage", "95%"), Palette.Muted+"coverage"+Reset+"  95%"; got != want {
		t.Errorf("colour on: KV() = %q, want %q", got, want)
	}

	SetColorEnabled(false)
	if got, want := KV("coverage", "95%"), "coverage  95%"; got != want {
		t.Errorf("colour off: KV() = %q, want %q", got, want)
	}
}

// Count renders the "3 updated, 1 skipped" tallies. The singular case is
// exactly one — including at zero, where "1 updated" would be a lie — and the
// caller's colour reaches the whole tally, so the halves of a summary line
// match.
func TestCountAgreesWithTheNumberItIsCounting(t *testing.T) {
	colourAuto(t)

	cases := []struct {
		n          int
		wantWords  string
		wantNumber string
	}{
		{0, "mods", "0"},
		{1, "mod", "1"},
		{2, "mods", "2"},
		{7, "mods", "7"},
	}

	for _, tc := range cases {
		t.Run(tc.wantWords, func(t *testing.T) {
			SetColorEnabled(true)
			got := Count(tc.n, "mod", "mods", Palette.OK)
			if visible := stripEscapes(got); visible != tc.wantNumber+" "+tc.wantWords {
				t.Errorf("Count(%d) renders as %q, want %q", tc.n, visible, tc.wantNumber+" "+tc.wantWords)
			}
			if want := Palette.OK + tc.wantNumber + " " + tc.wantWords + Reset; got != want {
				t.Errorf("Count(%d) = %q, want the tally painted once, as %q", tc.n, got, want)
			}

			SetColorEnabled(false)
			if got, want := Count(tc.n, "mod", "mods", Palette.OK), tc.wantNumber+" "+tc.wantWords; got != want {
				t.Errorf("colour off: Count(%d) = %q, want %q", tc.n, got, want)
			}
		})
	}
}

// Diff is the "old → new" fragment in an update table. The new version is both
// coloured and bold and the old one is only coloured, and the visual weight
// difference is what makes an upgrade scannable at a glance. If the two arms
// were ever swapped, every upgrade would read as a downgrade.
func TestDiffEmphasisesTheNewVersion(t *testing.T) {
	colourAuto(t)

	const oldV, newV = "0.9.3", "0.10.0"

	SetColorEnabled(true)
	got := Diff(oldV, newV)
	if want := Palette.Err + oldV + Reset + Palette.Faint + " " + SymArrow + " " + Reset +
		BoldSeq + Palette.OK + newV + Reset + Reset; got != want {
		t.Errorf("colour on: Diff() = %q, want %q", got, want)
	}
	if !strings.Contains(got, BoldSeq+Palette.OK+newV+Reset) {
		t.Errorf("Diff() = %q, want the new version %q bold in the success colour", got, newV)
	}
	if visible, want := stripEscapes(got), oldV+" "+SymArrow+" "+newV; visible != want {
		t.Errorf("Diff() renders as %q, want %q", visible, want)
	}

	SetColorEnabled(false)
	if got, want := Diff(oldV, newV), oldV+" "+SymArrow+" "+newV; got != want {
		t.Errorf("colour off: Diff() = %q, want %q", got, want)
	}
}

// Arrow is the bare separator, used where a Diff would be overkill — an
// instance label pointing at another, for instance.
func TestArrowIsTheSeparatorAlone(t *testing.T) {
	colourAuto(t)

	SetColorEnabled(true)
	if got, want := Arrow(), Palette.Faint+SymArrow+Reset; got != want {
		t.Errorf("colour on: Arrow() = %q, want %q", got, want)
	}

	SetColorEnabled(false)
	if got, want := Arrow(), SymArrow; got != want {
		t.Errorf("colour off: Arrow() = %q, want %q", got, want)
	}
}

// ─── The clamp helpers ──────────────────────────────────────────────────────

// max and min back the rule lengths in brandBar and Section. Both are inclusive
// at the boundary, and both are called with a subtraction that goes negative on
// a narrow terminal, so the negative side is not hypothetical.
func TestClampHelpersAreInclusiveAndHandleNegativeOperands(t *testing.T) {
	for _, tc := range []struct{ a, b, want int }{
		{1, 2, 2}, {2, 1, 2}, {5, 5, 5}, {0, 0, 0},
		{-8, 4, 4}, {4, -8, 4}, {-3, -9, -3},
	} {
		if got := max(tc.a, tc.b); got != tc.want {
			t.Errorf("max(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}

	for _, tc := range []struct{ a, b, want int }{
		{1, 2, 1}, {2, 1, 1}, {5, 5, 5}, {0, 0, 0},
		{-8, 4, -8}, {4, -8, -8}, {-3, -9, -9},
	} {
		if got := min(tc.a, tc.b); got != tc.want {
			t.Errorf("min(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
