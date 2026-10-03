package ui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// ─── Test helpers ───────────────────────────────────────────────────────────

// plainColour turns colour off and restores whatever the next test expects.
// Layout is the subject of this file and escapes are invisible on screen but
// not to a string comparison, so a layout assertion made with colour on would
// be measuring the wrong thing.
func plainColour(t *testing.T) {
	t.Helper()
	colourAuto(t)
	SetColorEnabled(false)
}

// rawLines renders fn and returns the lines it printed, colour left in place.
func rawLines(t *testing.T, fn func()) []string {
	t.Helper()
	lines := printedLines(captureStdout(t, fn))
	// A table ends with a blank line to separate itself from whatever comes
	// next. That blank is not part of the grid, and leaving it in would put
	// every index in these tests one further along than it looks.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// layout renders fn and returns its output as plain lines, one per Line call.
//
// Colour is stripped rather than switched off, because one of the things worth
// asserting is that the *painted* form of a table has the same shape as the
// plain one — see TestTableLayoutSurvivesColour.
func layout(t *testing.T, fn func()) []string {
	t.Helper()
	out := rawLines(t, fn)
	for i, l := range out {
		out[i] = stripEscapes(l)
	}
	return out
}

// columnOf reports the visible offset at which needle starts in a rendered
// line.
//
// The line is stripped of colour first and, because every escape this package
// emits is zero-width, the rune index in the stripped text *is* the visible
// offset. That equivalence is what lets these tests talk about columns at all:
// without it a coloured cell's "position" depends on how many bytes its colour
// codes take, and a glyph like ✔ shifts every column after it.
//
// A needle that appears twice is rejected rather than guessed at, since a short
// value like "-" matches the wrong column as often as the right one.
func columnOf(t *testing.T, line, needle string) int {
	t.Helper()
	plain := stripEscapes(line)
	i := strings.Index(plain, needle)
	if i < 0 {
		t.Fatalf("%q is missing from the rendered line %q", needle, plain)
	}
	if strings.Contains(plain[i+len(needle):], needle) {
		t.Fatalf("%q appears twice in %q, so its offset is ambiguous", needle, plain)
	}
	return utf8.RuneCountInString(plain[:i])
}

// grid measures a rendered line without the two-space indent every table line
// carries, so a comparison against a computed column width is not two cells
// out.
func grid(line string) int { return VisibleWidth(strings.TrimPrefix(line, "  ")) }

// buildModTable is the table `modharbor list` renders: a glyph column, a name,
// a version, a size, and a summary footer. Almost every table in the CLI takes
// this shape, so the tests below assert against it rather than against a
// convenient synthetic one.
func buildModTable() *Table {
	tab := NewTable("", "MOD", "VERSION", "SIZE")
	tab.Align(0, AlignRight)
	tab.Align(3, AlignRight)
	tab.Row(OK(SymOK), "sodium", Muted("0.9.1"), Faint("1.2 MB"))
	tab.Row(OK(SymOK), "mod menu", Muted("4.2.2"), Faint("13.5 MB"))
	tab.Row(Faint(SymDot), "no upstream match", Muted("unknown"), Faint("-"))
	tab.Footer("", "2", "identified", HumanBytes(1))
	return tab
}

// ─── The grid ───────────────────────────────────────────────────────────────

// Every command that lists anything renders one of these, and the whole point
// is that the columns line up. A ragged table is not an error — the build stays
// green, the command exits 0 and every row is perfectly readable on its own —
// so nothing else in the suite would ever notice. Hence the invariant is
// checked directly: the value of a column starts at the same visible offset in
// the header, in every body row and in the footer.
func TestTableColumnsLineUpAcrossHeaderBodyAndFooter(t *testing.T) {
	plainColour(t)

	tab := buildModTable()
	if got := tab.Len(); got != 3 {
		t.Fatalf("Len() = %d, want 3", got)
	}
	lines := layout(t, tab.Render)

	// header, rule, three rows, rule, footer
	if len(lines) != 7 {
		t.Fatalf("rendered %d lines %q, want 7 (header, rule, 3 rows, rule, footer)", len(lines), lines)
	}
	header, rows, footer := lines[0], lines[2:5], lines[6]

	// Columns 1 and 2 are left-aligned, so the value starts where the header
	// starts. This is the check that catches a cell measured in bytes instead of
	// cells: every escape before it would shift it.
	for _, col := range []struct {
		anchor string
		values []string
	}{
		{"MOD", []string{"sodium", "mod menu", "no upstream match"}},
		{"VERSION", []string{"0.9.1", "4.2.2", "unknown"}},
	} {
		want := columnOf(t, header, col.anchor)
		for i, v := range col.values {
			if got := columnOf(t, rows[i], v); got != want {
				t.Errorf("%q starts at offset %d on its row, want %d (the header's %s column)",
					v, got, want, col.anchor)
			}
		}
	}

	// Column 3 is right-aligned, so its *right* edge is the aligned one. This
	// is a different assertion from the one above: a column that was merely
	// padded on the right would pass the start check and fail this one.
	{
		anchor := "SIZE"
		right := columnOf(t, header, anchor) + VisibleWidth(anchor)
		for i, v := range []string{"1.2 MB", "13.5 MB", "-"} {
			if got := columnOf(t, rows[i], v) + VisibleWidth(v); got != right {
				t.Errorf("%q ends at offset %d, want %d (the right edge of the %s column)",
					v, got, right, anchor)
			}
		}
	}

	// The footer is a row like any other, so its cells land in the same columns.
	if got, want := columnOf(t, footer, "identified"), columnOf(t, header, "VERSION"); got != want {
		t.Errorf("the footer's VERSION cell starts at %d, want %d", got, want)
	}
	if got, want := columnOf(t, footer, "1 B")+len("1 B"), columnOf(t, header, "SIZE")+len("SIZE"); got != want {
		t.Errorf("the footer's SIZE cell ends at %d, want %d", got, want)
	}
}

// A table with no rows has nothing to draw, so it prints nothing at all: not a
// lone header hanging over an empty rule. `modharbor list` on an empty instance
// warns separately, and the two would otherwise stack.
func TestTableWithoutRowsPrintsNothing(t *testing.T) {
	plainColour(t)

	tab := NewTable("MOD", "VERSION")
	tab.Footer("0", "mods")
	if out := captureStdout(t, tab.Render); out != "" {
		t.Errorf("a table with no rows printed %q, want nothing", out)
	}
	if got := tab.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
}

// A rendered table stays inside its frame: nothing wider than the columns it
// was measured from, and nothing padded out past its own content. A row that
// outruns the frame is how a table starts wrapping mid-word, and one that ends
// in spaces looks ragged once the spaces are stripped by whatever reads it
// next. The rules are the frame, so they are the one line allowed to be full
// width.
func TestTableRowsStayInsideTheFrame(t *testing.T) {
	plainColour(t)

	for _, tc := range []struct {
		name  string
		build func() *Table
	}{
		{"mod list", buildModTable},
		{"short row", func() *Table {
			tab := NewTable("MOD", "VERSION", "SIZE")
			tab.Row("only one cell")
			return tab
		}},
		{"empty trailing cells", func() *Table {
			tab := NewTable("MOD", "VERSION", "SIZE")
			tab.Row("sodium", "0.9.1", "")
			return tab
		}},
		{"extra cells beyond the headers", func() *Table {
			tab := NewTable("MOD", "VERSION")
			tab.Row("sodium", "0.9.1", "13.5 MB", "surplus")
			return tab
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tab := tc.build()
			lines := layout(t, tab.Render)
			if len(lines) < 2 {
				t.Fatalf("rendered %d lines, want at least a header and a rule", len(lines))
			}

			// The frame: every column plus the gutter between each pair. No line
			// may outrun it, and the rules are exactly it.
			widths := tab.measure()
			frame := 2 * (len(widths) - 1)
			for _, w := range widths {
				frame += w
			}

			for i, l := range lines {
				isRule := strings.Contains(l, boxH)
				w := grid(l)
				if isRule {
					if w != frame {
						t.Errorf("the rule on line %d is %d cells wide, want %d", i, w, frame)
					}
					continue
				}
				if w > frame {
					t.Errorf("line %d is %d cells wide, past the %d-cell frame: %q",
						i, w, frame, l)
				}
				// Trailing padding is trimmed: a row that ends in spaces looks
				// ragged in a paste, a diff, or anything that strips them.
				if l != strings.TrimRight(l, " ") {
					t.Errorf("line %d (%q) ends in padding", i, l)
				}
			}
		})
	}
}

// A row with fewer cells than the table has columns still has to put its cells
// in the right place; the missing ones render as blanks and are trimmed off the
// end. Building rows from a variable set of values is normal, and losing the
// alignment would be a silent misreport rather than a crash.
func TestTableShortRowsKeepLaterColumnsInPlace(t *testing.T) {
	plainColour(t)

	tab := NewTable("MOD", "VERSION", "SIZE")
	tab.Row("sodium", "0.9.1", "1.2 MB")
	tab.Row("mod menu", "4.2.2")
	lines := layout(t, tab.Render)

	// The padding that fills the missing cells is trimmed off the end rather
	// than printed: a table that ends in a run of spaces looks like it has a
	// ragged right edge in a diff, a paste, or anything that strips them.
	for i, l := range lines {
		if l != strings.TrimRight(l, " ") {
			t.Errorf("line %d (%q) ends in padding", i, l)
		}
	}

	short := lines[3]
	if got, want := columnOf(t, short, "4.2.2"), columnOf(t, lines[2], "0.9.1"); got != want {
		t.Errorf("the second cell of the short row starts at %d, want %d", got, want)
	}
	if strings.Contains(short, "1.2 MB") {
		t.Errorf("a row with two cells rendered a third: %q", short)
	}
}

// Align takes a column index built by the caller, and a stale or absent index
// must not corrupt the table. Out of range is ignored rather than panicking: the
// number of columns varies per command and a crash over a cosmetic option is
// not an acceptable failure mode.
func TestTableAlignIgnoresColumnsOutOfRange(t *testing.T) {
	plainColour(t)

	build := func() *Table {
		tab := NewTable("MOD", "VERSION")
		tab.Align(-1, AlignRight)
		tab.Align(2, AlignRight)
		tab.Align(99, AlignCenter)
		tab.Row("sodium", "0.9.1")
		return tab
	}
	got := layout(t, build().Render)

	plain := NewTable("MOD", "VERSION")
	plain.Row("sodium", "0.9.1")
	if want := layout(t, plain.Render); !equalLines(got, want) {
		t.Errorf("an out-of-range Align changed the output:\n got %q\nwant %q", got, want)
	}
}

// AlignAllRight is the shorthand the numeric tables use, and it has to agree
// with setting every column by hand — the two are only interchangeable if
// neither of them misses a column.
func TestTableAlignAllRightMatchesSettingEveryColumn(t *testing.T) {
	plainColour(t)

	rows := func() []string { return []string{"sodium", "0.9.1", "1.2 MB"} }

	byHand := NewTable("MOD", "VERSION", "SIZE")
	byHand.Align(0, AlignRight).Align(1, AlignRight).Align(2, AlignRight)
	byHand.Row(rows()...)
	byHand.Row("mod menu", "4.2.2", "13.5 MB")

	all := NewTable("MOD", "VERSION", "SIZE")
	all.AlignAllRight()
	all.Row(rows()...)
	all.Row("mod menu", "4.2.2", "13.5 MB")

	got := layout(t, all.Render)
	if want := layout(t, byHand.Render); !equalLines(got, want) {
		t.Errorf("AlignAllRight differs from per-column Align:\n got %q\nwant %q", got, want)
	}

	// And it really is right-aligned: every column's values end where the
	// header's ends, not merely start after some padding.
	for _, col := range []struct {
		anchor string
		values []string
	}{
		{"MOD", []string{"sodium", "mod menu"}},
		{"VERSION", []string{"0.9.1", "4.2.2"}},
		{"SIZE", []string{"1.2 MB", "13.5 MB"}},
	} {
		right := columnOf(t, got[0], col.anchor) + VisibleWidth(col.anchor)
		for i, v := range col.values {
			line := got[2+i]
			if e := columnOf(t, line, v) + VisibleWidth(v); e != right {
				t.Errorf("%q ends at %d, want %d (the right edge of %s)", v, e, right, col.anchor)
			}
		}
	}
}

// Centring is only reachable from the exported API, and its arithmetic is easy
// to get wrong in two ways: padding only on one side, and losing the odd cell
// of an odd difference. rowCells is called directly here because the trailing
// trim on a rendered line would swallow the right half of the padding.
func TestTableCenteredColumnsBalanceThePadding(t *testing.T) {
	plainColour(t)

	tab := NewTable("NAME", "STATE", "SIZE")
	tab.Align(1, AlignCenter)

	// Column widths of 8, so each state leaves a known number of cells to
	// share between its two sides.
	cases := []struct{ state, want string }{
		// 6 spare cells, split evenly.
		{"ok", "sodium    " + "   ok   " + "  1.2 MB"},
		// 5 spare cells: the odd one has to go somewhere, and putting it on
		// the left is what the code does.
		{"upx", "sodium    " + "  upx   " + "  1.2 MB"},
		// A value exactly as wide as the column gets no padding at all.
		{"exactly8", "sodium    exactly8  1.2 MB"},
	}

	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			cells := []string{"sodium", tc.state, "1.2 MB"}
			got := stripEscapes(tab.rowCells(cells, []int{8, 8, 8}, false, 0))
			if want := strings.TrimRight(tc.want, " "); got != want {
				t.Errorf("rowCells() = %q, want %q", got, want)
			}
		})
	}
}

// The footer participates in column measurement. `modharbor list` and
// `modharbor search` print their totals in a footer, and if the footer did not
// widen its column the summary would be squeezed to the width of the mod names
// above it and truncated.
func TestTableFooterWidensItsColumn(t *testing.T) {
	plainColour(t)

	tab := NewTable("MOD", "COUNT")
	tab.Row("sodium", "1")
	tab.Footer("identified", "1234")
	if got := tab.Len(); got != 1 {
		t.Errorf("Len() = %d, want 1: a footer is a summary, not a row of the grid", got)
	}
	lines := layout(t, tab.Render)

	// "COUNT" is 5 wide and "identified" is 10, so the footer has to widen the
	// first column to 10. Without that the body's cell is followed by two
	// spaces and the footer's text runs into the column above it.
	body := lines[2]
	rest := body[columnOf(t, body, "sodium")+len("sodium"):]
	if want := strings.Repeat(" ", 10-len("sodium")) + "  1"; !strings.HasPrefix(rest, want) {
		t.Errorf("body row %q does not reserve the footer's column width: want the prefix %q", body, want)
	}
}

// The footer is the one row meant to stand out from the body, and it only does
// so when the bold sequence actually reaches the output.
func TestTableFooterIsRenderedBold(t *testing.T) {
	colourAuto(t)

	tab := NewTable("MOD", "COUNT")
	tab.Row("sodium", "1")
	tab.Footer("identified", "1234")

	SetColorEnabled(true)
	lines := rawLines(t, tab.Render)
	// The two-space indent is written before the cell, so the bold sequence
	// starts the line's content rather than the line itself.
	footer := strings.TrimPrefix(lines[len(lines)-1], "  ")
	if !strings.HasPrefix(footer, BoldSeq) {
		t.Errorf("footer %q does not start with the bold sequence %q", footer, BoldSeq)
	}
	if !strings.HasSuffix(footer, Reset) {
		t.Errorf("footer %q does not end with a reset", footer)
	}

	SetColorEnabled(false)
	if body := rawLines(t, tab.Render)[2]; strings.Contains(body, BoldSeq) {
		t.Errorf("body row %q is bold, but only the footer should be", body)
	}
}

// RowF is the formatted-row convenience, and it is the only way a row's
// content gets built without the caller doing the Sprintf itself.
func TestTableRowFFormatsLikePrintf(t *testing.T) {
	plainColour(t)

	tab := NewTable("SUMMARY")
	tab.RowF("%d of %d mods", 3, 7)
	if got := tab.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}
	if lines := layout(t, tab.Render); !strings.Contains(lines[2], "3 of 7 mods") {
		t.Errorf("the row is %q, want it to contain %q", lines[2], "3 of 7 mods")
	}
}

// ─── Rules ──────────────────────────────────────────────────────────────────

// The rule under the header has to be exactly as wide as the frame the columns
// describe. One cell wider and the table reads as a line of dashes hanging over
// the right edge, which is what it did: the width counted one gutter too many
// for n columns.
//
// The frame is checked against the measured column widths rather than against
// the header line, because no line of a table is padded out past its own
// content — the header and every row are trimmed, so the rule is the widest.
func TestTableRulesAreExactlyAsWideAsTheFrame(t *testing.T) {
	plainColour(t)

	tab := buildModTable()
	lines := layout(t, tab.Render)

	widths := tab.measure()
	frame := 2 * (len(widths) - 1)
	for _, w := range widths {
		frame += w
	}

	// The rules are the second line and the one before the footer.
	for _, i := range []int{1, len(lines) - 2} {
		rule := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(rule, strings.Repeat(boxH, 2)) {
			t.Errorf("line %d (%q) is not a rule", i, lines[i])
			continue
		}
		if got := grid(lines[i]); got != frame {
			t.Errorf("the rule on line %d is %d cells wide, want %d (the measured frame)",
				i, got, frame)
		}
	}
}

// Content wider than the terminal has to be squeezed rather than wrapped, and
// the squeeze has to hold for the rules too: they are the lines that wrap
// worst, being one unbroken run of box-drawing characters with no break
// opportunity in it. The rule used to be drawn at the *unshrunk* width, so it
// was the line that overflowed while the rows beside it fitted.
//
// The table is built wider than MaxWidth so the shrink is guaranteed however
// wide the terminal running the test happens to be.
func TestTableShrinksContentTooWideForTheTerminal(t *testing.T) {
	plainColour(t)

	tab := NewTable("MOD", "DESCRIPTION")
	tab.Align(0, AlignRight)
	// Each description opens with a word that appears nowhere else on its line,
	// so its column can be located by offset.
	tab.Row("sodium", Muted("longest "+strings.Repeat("alpha ", 28)))
	tab.Row("mod menu", Muted("shorter "+strings.Repeat("beta ", 14)))
	tab.Footer("2", "rows")

	lines := layout(t, tab.Render)
	if len(lines) != 6 {
		t.Fatalf("rendered %d lines %q, want 6 (header, rule, 2 rows, rule, footer)", len(lines), lines)
	}
	fit := Width()
	for i, l := range lines {
		if w := VisibleWidth(l); w > fit {
			t.Errorf("line %d is %d cells wide, wider than the %d-cell terminal: %q", i, w, fit, l)
		}
	}

	// The squeezed column is truncated, and says so.
	if !strings.Contains(lines[2], "…") {
		t.Errorf("the over-long cell was not truncated: %q", lines[2])
	}
	// Every body row keeps one width, so the columns still line up.
	if a, b := VisibleWidth(lines[2]), VisibleWidth(lines[3]); a != b {
		t.Errorf("the body rows are %d and %d cells wide, want the same", a, b)
	}
	// The footer is trimmed at the right, so it is compared by column rather
	// than by width: its cells still have to land in the columns above.
	if got, want := columnOf(t, lines[5], "rows"), columnOf(t, lines[2], "longest"); got != want {
		t.Errorf("the footer's second cell starts at %d, want %d", got, want)
	}
}

// ─── shrinkColumns ──────────────────────────────────────────────────────────

// shrinkColumns is the whole of the table's overflow behaviour, so its three
// properties are worth pinning: a table that already fits is left alone, the
// widest column is sacrificed first, and a column already at the floor is
// skipped rather than cut to nothing.
func TestShrinkColumns(t *testing.T) {
	cases := []struct {
		name   string
		in     []int
		budget int
		check  func(t *testing.T, got []int)
	}{
		{
			name:   "a table that already fits is untouched",
			in:     []int{4, 10, 4},
			budget: 100,
			check: func(t *testing.T, got []int) {
				if !equalInts(got, []int{4, 10, 4}) {
					t.Errorf("got %v, want the input unchanged", got)
				}
			},
		},
		{
			// The exact result is pinned, not just the ordering: halving the
			// widest column and cutting one cell at a time both leave "the
			// widest was cut, the narrow ones untouched" true, and only the
			// numbers separate them.
			name:   "the widest column is sacrificed first, and by half",
			in:     []int{4, 100, 4},
			budget: 40,
			check: func(t *testing.T, got []int) {
				if !equalInts(got, []int{4, 25, 4}) {
					t.Errorf("got %v, want [4 25 4]: the 100-cell column halved once, then again", got)
				}
			},
		},
		{
			name:   "a wide table is squeezed to its budget",
			in:     []int{30, 30, 30, 30},
			budget: 60,
			check: func(t *testing.T, got []int) {
				total := 0
				for _, x := range got {
					total += x + 2
				}
				if total > 60 {
					t.Errorf("total %d from %v, want at most the budget of 60", total, got)
				}
				// Columns are never cut below the floor, so an unreachable
				// budget is the only way to exceed it — and here it is not
				// unreachable, because four 30-cell columns can be squeezed
				// into 60 while staying above six.
				for i, x := range got {
					if x < 6 {
						t.Errorf("column %d was cut to %d, below the floor: %v", i, x, got)
					}
				}
			},
		},
		{
			name:   "a column already at the floor is skipped",
			in:     []int{10, 6, 10},
			budget: 4, // below the internal floor of 8
			check: func(t *testing.T, got []int) {
				if got[1] != 6 {
					t.Errorf("the six-cell column was cut to %d; only columns wider than six are", got[1])
				}
			},
		},
		{
			name:   "a budget below the floor is raised to it",
			in:     []int{4, 4, 4},
			budget: 1,
			check: func(t *testing.T, got []int) {
				// 4+4+4 with gutters is 18, well over both 1 and the floor of 8,
				// but no column is wider than six, so the result is the input.
				if !equalInts(got, []int{4, 4, 4}) {
					t.Errorf("got %v, want the columns untouched", got)
				}
			},
		},
		{
			name:   "an unreachable budget stops instead of looping",
			in:     []int{4, 4, 4},
			budget: 8,
			check: func(t *testing.T, got []int) {
				if !equalInts(got, []int{4, 4, 4}) {
					t.Errorf("got %v, want the columns untouched when none can be cut", got)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := append([]int(nil), tc.in...)
			got := shrinkColumns(tc.in, tc.budget)
			tc.check(t, got)
			// The input must not be modified: Render hands the measured widths
			// to the header and to every row, so a mutated slice would shrink
			// the table twice over.
			if !equalInts(tc.in, orig) {
				t.Errorf("shrinkColumns modified its input: %v, was %v", tc.in, orig)
			}
		})
	}
}

// ─── List ───────────────────────────────────────────────────────────────────

// List is the compact key/value block behind `modharbor info` and the doctor
// report. Its only job is that every value starts in the same column.
func TestListAlignsEveryValueInOneColumn(t *testing.T) {
	plainColour(t)

	l := NewList()
	if l.Add("Instance", "26.3-fabric-mod") != l {
		t.Error("Add did not return the list, so it cannot be chained")
	}
	l.Add("Loader", "fabric").Add("Mods", "33")

	lines := layout(t, l.Render)
	if len(lines) != 3 {
		t.Fatalf("rendered %d lines %q, want 3", len(lines), lines)
	}
	want := columnOf(t, lines[0], "26.3-fabric-mod")
	for i, v := range []string{"fabric", "33"} {
		if got := columnOf(t, lines[i+1], v); got != want {
			t.Errorf("%q starts at offset %d, want %d", v, got, want)
		}
	}
}

// The column is sized by the keys and nothing else. A block whose values
// happen to be longer than any key — an instance path, a digest — would then
// carry a gap of its own on every row, which reads as a column of empty cells
// rather than as one aligned list.
func TestListSizesItsKeyColumnByTheKeysAlone(t *testing.T) {
	plainColour(t)

	l := NewList()
	l.Add("Name", "sodium")
	l.Add("Loader", "26.3-fabric-mod")

	lines := layout(t, l.Render)
	// The longest key sets the column, so it carries no padding of its own and
	// the two-space gutter is all that separates it from its value. The values
	// here are longer than any key, which is what makes the two possible
	// widths differ at all.
	longest := columnOf(t, lines[1], "26.3-fabric-mod") - (columnOf(t, lines[1], "Loader") + len("Loader"))
	if longest != 2 {
		t.Errorf("the longest key is followed by %d cells, want the two-space gutter alone", longest)
	}
}

// A list with nothing in it prints nothing rather than an empty line.
func TestListWithoutPairsPrintsNothing(t *testing.T) {
	plainColour(t)
	if out := captureStdout(t, NewList().Render); out != "" {
		t.Errorf("an empty list printed %q, want nothing", out)
	}
}

// ─── Panel ──────────────────────────────────────────────────────────────────

// The panel is a box, and a box is only a box if all four edges are the same
// length. The top edge used to be padded from the *title* rather than from the
// label it actually draws, so it came out two cells wider than the body under
// it: a visible jag on every titled panel the CLI prints.
func TestPanelEdgesAreAllTheSameWidth(t *testing.T) {
	// The second icon is two cells wide, where every icon the CLI uses is one.
	// That is what tells "the frame is sized for the label" apart from "the
	// frame is sized for the title": with a one-cell icon the two agree, and a
	// bug in either passes unnoticed.
	for _, tc := range []struct {
		name  string
		title string
		icon  string
		lines []string
	}{
		{"untitled", "", "", []string{"hello", "3 mods"}},
		{"titled", "Summary", "", []string{"hello", "3 mods"}},
		{"titled with an icon", "Summary", SymStar, []string{"hello", "3 mods"}},
		{"titled with a wide icon", "Summary", "»»", []string{"hello", "3 mods"}},
		// Twenty ellipsis glyphs are twenty cells and sixty bytes. The frame
		// has to be sized for the twenty, or the box is drawn three times too
		// wide for what is in it.
		{"multi-byte body", "Summary", "", []string{strings.Repeat("…", 20)}},
		{"title wider than the body", "A much longer title than any line in it", "", []string{"hi"}},
		{"long line and a long title", "Summary of a migration that touched every jar in the instance", "",
			[]string{strings.Repeat("x", 300)}},
		// The terminal clamp can leave the title wider than the frame it sits
		// in, which used to ask strings.Repeat for a negative count.
		{"title longer than the terminal", strings.Repeat("wide title ", 40), SymStar, []string{"hi"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plainColour(t)
			p := NewPanel(tc.title).WithIcon(tc.icon)
			for _, l := range tc.lines {
				p.Line(l)
			}
			lines := layout(t, p.Render)
			if len(lines) < 3 {
				t.Fatalf("rendered %d lines %q, want a top edge, a body and a bottom edge", len(lines), lines)
			}
			want := VisibleWidth(lines[0])
			for i, l := range lines {
				if got := VisibleWidth(l); got != want {
					t.Errorf("line %d is %d cells wide, want %d (every edge of the box matches): %q",
						i, got, want, l)
				}
			}
			// And the corners are where corners belong. Every line carries the
			// same two-space indent, so it is trimmed before the corners are
			// looked for.
			unindent := func(l string) string { return strings.TrimPrefix(l, "  ") }
			if !strings.HasPrefix(unindent(lines[0]), boxTL) || !strings.HasSuffix(lines[0], boxTR) {
				t.Errorf("the top edge %q does not run %s…%s", lines[0], boxTL, boxTR)
			}
			if last := lines[len(lines)-1]; !strings.HasPrefix(unindent(last), boxBL) || !strings.HasSuffix(last, boxBR) {
				t.Errorf("the bottom edge %q does not run %s…%s", last, boxBL, boxBR)
			}
			for i, l := range lines[1 : len(lines)-1] {
				if !strings.HasPrefix(unindent(l), boxV) || !strings.HasSuffix(l, boxV) {
					t.Errorf("body line %d (%q) is not fenced with %s", i, l, boxV)
				}
			}

			// A box that fits its content is exactly as wide as the widest
			// thing in it plus the frame: two borders and a space either side.
			//
			// Two things can only be told apart from here. The inner width has
			// to be measured in visible cells — a line of twenty ellipsis glyphs
			// is twenty cells and sixty bytes, and sizing the frame by byte
			// length draws a box four times too wide. And it has to be measured
			// from the label, icon included, rather than the bare title, or an
			// icon wider than a cell pushes the right edge out.
			inner := 0
			for _, l := range tc.lines {
				if w := VisibleWidth(l); w > inner {
					inner = w
				}
			}
			if tc.title != "" {
				label := tc.title
				if tc.icon != "" {
					label = tc.icon + " " + label
				}
				if w := VisibleWidth(label) + 4; w > inner {
					inner = w
				}
			}
			if inner+4 <= Width()-2 {
				if got, want := grid(lines[0]), inner+4; got != want {
					t.Errorf("the box is %d cells wide, want %d (its content plus the frame)", got, want)
				}
			}
		})
	}
}

// WithIcon is decoration, but it goes into the label the rule beside it is
// measured from, so the icon has to reach the top edge without widening it.
func TestPanelIconAppearsInTheTopEdge(t *testing.T) {
	// The second icon is two cells wide, where every icon the CLI actually uses
	// is one. That is what separates "the frame is sized for the label" from
	// "the frame is sized for the title": with a one-cell icon the two agree,
	// and a bug in either passes unnoticed.
	for _, icon := range []string{SymStar, "»»"} {
		t.Run(icon, func(t *testing.T) {
			plainColour(t)

			p := NewPanel("Summary").WithIcon(icon)
			p.Line("hello")
			lines := layout(t, p.Render)
			if !strings.Contains(lines[0], icon+" Summary") {
				t.Errorf("the top edge %q does not carry the icon and title in full", lines[0])
			}
			if got, want := VisibleWidth(lines[0]), VisibleWidth(lines[1]); got != want {
				t.Errorf("the icon left the top edge %d cells wide against the body's %d", got, want)
			}
		})
	}
}

// The label is the icon, one space, the title, one space — with or without an
// icon. Interpolating the icon separately put an extra space either side of it
// when there was none, which widened the label without the edge noticing: the
// rule simply got shorter to compensate, so the box stayed square and the title
// sat a cell too far right.
func TestPanelLabelSeparatesIconAndTitleByExactlyOneSpace(t *testing.T) {
	plainColour(t)

	for _, tc := range []struct {
		name  string
		build func() *Panel
		want  string
	}{
		{"no icon", func() *Panel { return NewPanel("Summary") }, " Summary "},
		{"with an icon", func() *Panel { return NewPanel("Summary").WithIcon(SymStar) }, " " + SymStar + " Summary "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.build()
			if got := p.labelText(); got != strings.TrimSpace(tc.want) {
				t.Errorf("labelText() = %q, want %q", got, strings.TrimSpace(tc.want))
			}
			// And the drawn edge contains that label verbatim, which is what
			// rules out the label and the edge disagreeing about spacing. The
			// edge has to *start* with it: an extra space either side of a
			// missing icon would still be a substring of the wider label.
			p.Line("x")
			edge := layout(t, p.Render)[0]
			if !strings.HasPrefix(edge, "  "+boxTL+tc.want) {
				t.Errorf("the top edge %q does not start with the indent, corner and label %q", edge, tc.want)
			}
		})
	}
}

// Linef is the formatted-line convenience on Panel. It matters that the
// formatting happens before the line is measured: a panel that sized its frame
// from "%d of %d mods installed" rather than from the text it produces would
// draw a box three columns too narrow and truncate the line inside it.
func TestPanelLinefFormatsBeforeMeasuring(t *testing.T) {
	plainColour(t)

	const want = "23 of 33 mods installed"
	p := NewPanel("Summary").WithIcon(SymStar)
	p.Linef("%d of %d mods installed", 23, 33)
	lines := layout(t, p.Render)

	if !strings.Contains(lines[1], want) {
		t.Errorf("the line is %q, want it to contain %q", lines[1], want)
	}
	if got, expect := grid(lines[0]), VisibleWidth(want)+4; got != expect {
		t.Errorf("the box is %d cells wide, want %d (the formatted line plus the frame)", got, expect)
	}
}

// A panel with no lines draws nothing; there is no box to draw around.
func TestPanelWithoutLinesPrintsNothing(t *testing.T) {
	plainColour(t)
	p := NewPanel("Summary").WithIcon(SymStar)
	if out := captureStdout(t, p.Render); out != "" {
		t.Errorf("a panel with no lines printed %q, want nothing", out)
	}
}

// Content wider than the terminal is truncated rather than allowed to push the
// right edge of the box past it, which would wrap the border onto a line of its
// own. The measurement has to be in cells, not bytes: a line of multi-byte
// glyphs is longer in bytes than it ever is on screen, so sizing the frame by
// byte length would push a panel of twenty ellipses out past a hundred columns.
func TestPanelTruncatesContentWiderThanTheTerminal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func() *Panel
	}{
		{"ascii", func() *Panel {
			p := NewPanel("Summary")
			p.Line(strings.Repeat("x", 400))
			return p
		}},
		{"multi-byte", func() *Panel {
			p := NewPanel("Summary")
			p.Line(strings.Repeat("…", 400))
			return p
		}},
		{"many short multi-byte runes", func() *Panel {
			p := NewPanel("Summary")
			p.Line(strings.Repeat("café ", 200))
			return p
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plainColour(t)
			lines := layout(t, tc.build().Render)
			fit := Width()
			for i, l := range lines {
				if w := VisibleWidth(l); w > fit {
					t.Errorf("line %d is %d cells wide, wider than the %d-cell terminal: %q", i, w, fit, l)
				}
			}
			if !strings.Contains(lines[1], "…") {
				t.Errorf("the over-long line was not truncated: %q", lines[1])
			}
			// And every edge still matches, which is the check a byte-measured
			// frame fails.
			for i, l := range lines[1:] {
				if w, want := VisibleWidth(l), VisibleWidth(lines[0]); w != want {
					t.Errorf("line %d is %d cells wide, want %d", i+1, w, want)
				}
			}
		})
	}

	// The hard case for a byte measurement is a line that is *short* on screen
	// and long in bytes: twenty ellipsis glyphs are twenty cells and sixty
	// bytes. Measured in bytes, a panel holding one would be sized for sixty
	// cells and print wider than the terminal it was sized against.
	t.Run("short on screen but long in bytes", func(t *testing.T) {
		plainColour(t)
		line := strings.Repeat("…", 20)
		if len(line) <= 20 {
			t.Fatalf("%q is %d bytes for 20 cells; the fixture cannot test a byte measurement", line, len(line))
		}

		p := NewPanel("Summary")
		p.Line(line)
		lines := layout(t, p.Render)
		fit := Width()
		for i, l := range lines {
			if w := VisibleWidth(l); w > fit {
				t.Errorf("line %d is %d cells wide, wider than the %d-cell terminal: %q", i, w, fit, l)
			}
		}
		if !strings.Contains(lines[1], line) {
			t.Errorf("a line that fits was truncated anyway: %q", lines[1])
		}
	})
}

// ─── Colour interaction ─────────────────────────────────────────────────────

// Colour is a global, and every cell the CLI puts in a table is a painted
// string — see `modharbor list`, which passes ui.OK, ui.Muted and ui.Faint
// straight into Row. If padding or measurement counted the escape bytes the
// table would drift apart further the more colourful it got, and only when
// colour was on, which is to say only for the users who can see it.
func TestTableLayoutSurvivesColour(t *testing.T) {
	colourAuto(t)

	shapes := make([]string, 2)
	for i, on := range []bool{false, true} {
		SetColorEnabled(on)
		lines := rawLines(t, buildModTable().Render)
		if len(lines) == 0 {
			t.Fatal("nothing was rendered")
		}
		for j, l := range lines {
			lines[j] = strings.TrimRight(stripEscapes(l), " ")
		}
		shapes[i] = strings.Join(lines, "\n")
	}
	SetColorEnabled(false)

	if shapes[0] != shapes[1] {
		t.Errorf("the painted table has a different shape:\ncolour off:\n%s\ncolour on:\n%s",
			shapes[0], shapes[1])
	}
}

// ─── Small helpers ──────────────────────────────────────────────────────────

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
