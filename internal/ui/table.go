package ui

import (
	"fmt"
	"strings"
)

// Align controls column alignment in a Table.
type Align int

// Supported column alignments.
const (
	AlignLeft Align = iota
	AlignRight
	AlignCenter
)

// Table renders a rounded, colourised grid.
//
//	type t := ui.NewTable("MOD", "INSTALLED", "LATEST")
//	t.Row("sodium", ui.Muted("0.9.1"), ui.OK("0.9.3"))
//	t.Render()
type Table struct {
	headers []string
	rows    [][]string
	aligns  []Align
	// Footer holds an optional summary row rendered in bold below a rule.
	footers [][]string
}

// NewTable creates a table with the given column headers.
func NewTable(headers ...string) *Table {
	return &Table{headers: headers, aligns: make([]Align, len(headers))}
}

// Row appends a row. Missing cells are padded automatically.
func (t *Table) Row(cells ...string) {
	t.rows = append(t.rows, cells)
	return
}

// RowF appends a row built with fmt.Sprintf semantics.
func (t *Table) RowF(format string, a ...any) {
	t.rows = append(t.rows, []string{fmt.Sprintf(format, a...)})
	return
}

// Footer appends a bold summary row rendered after a separator rule.
func (t *Table) Footer(cells ...string) {
	t.footers = append(t.footers, cells)
	return
}

// Align sets the alignment for column i.
func (t *Table) Align(i int, a Align) *Table {
	if i >= 0 && i < len(t.aligns) {
		t.aligns[i] = a
	}
	return t
}

// AlignAllRight right-aligns every column, useful for pure numeric tables.
func (t *Table) AlignAllRight() *Table {
	for i := range t.aligns {
		t.aligns[i] = AlignRight
	}
	return t
}

// Len reports the number of data rows.
func (t *Table) Len() int { return len(t.rows) }

// Render writes the table to stdout.
func (t *Table) Render() {
	if len(t.rows) == 0 {
		return
	}
	widths := t.measure()
	total := t.totalWidth(widths)
	fit := Width()
	shrink := total > fit
	if shrink {
		// The rules have to be drawn from the widths the rows actually get.
		// Measuring them against the natural width left a rule a hundred
		// cells wide over a body that had been carefully squeezed to fit, and
		// the rule was the part that wrapped.
		widths = shrinkColumns(widths, fit-2)
		total = t.totalWidth(widths)
	}

	rule := paint(Palette.Rule, strings.Repeat(boxH, total))

	// Header
	Line("  " + t.headerRow(widths, shrink, fit))
	Line("  " + rule)

	// Body
	for _, r := range t.rows {
		Line("  " + t.rowCells(r, widths, shrink, fit))
	}

	// Footers
	for _, f := range t.footers {
		Line("  " + rule)
		Line("  " + Bold(t.rowCells(f, widths, shrink, fit)))
	}
	Blank()
}

func (t *Table) measure() []int {
	w := make([]int, len(t.headers))
	for i, h := range t.headers {
		w[i] = VisibleWidth(h)
	}
	for _, r := range t.rows {
		for i, c := range r {
			if i < len(w) && VisibleWidth(c) > w[i] {
				w[i] = VisibleWidth(c)
			}
		}
	}
	for _, f := range t.footers {
		for i, c := range f {
			if i < len(w) && VisibleWidth(c) > w[i] {
				w[i] = VisibleWidth(c)
			}
		}
	}
	return w
}

// totalWidth is the visible width of one rendered row: every cell plus one
// two-space gutter *between* each pair of cells. The rules are drawn to this
// width, so it has to be the same sum the rows use — one cell wide and the
// table reads as a line of dashes hanging over the right edge.
func (t *Table) totalWidth(w []int) int {
	total := 2 * (len(w) - 1)
	for _, x := range w {
		total += x
	}
	if total < 12 {
		total = 12
	}
	return total
}

func (t *Table) headerRow(w []int, shrink bool, fit int) string {
	limit := fit - 2
	cells := make([]string, len(t.headers))
	// Headers get the leftover space preferentially; body truncates.
	widths := append([]int(nil), w...)
	if shrink {
		widths = shrinkColumns(widths, limit)
	}
	for i, h := range t.headers {
		cells[i] = Bold(paint(Palette.Brand, PadLeft(h, widths[i])))
		if t.aligns[i] == AlignLeft {
			cells[i] = Bold(paint(Palette.Brand, Pad(h, widths[i])))
		}
	}
	// Trimmed like a body row, so no line of a table ends in padding. The rule
	// below it is the frame and stays full width; nothing here needs the header
	// to reserve the last column's padding to get that.
	return strings.TrimRight(strings.Join(cells, "  "), " ")
}

func (t *Table) rowCells(row []string, w []int, shrink bool, fit int) string {
	widths := append([]int(nil), w...)
	if shrink {
		widths = shrinkColumns(widths, fit-2)
	}
	cells := make([]string, len(w))
	for i := range w {
		cell := ""
		if i < len(row) {
			cell = row[i]
		}
		// Re-apply alignment on the padded form so colours stay balanced.
		if VisibleWidth(cell) > widths[i] {
			cell = Truncate(cell, widths[i])
		}
		switch t.aligns[i] {
		case AlignRight:
			cells[i] = PadLeft(cell, widths[i])
		case AlignCenter:
			pad := widths[i] - VisibleWidth(cell)
			if pad > 0 {
				left := pad / 2
				cells[i] = strings.Repeat(" ", left) + cell + strings.Repeat(" ", pad-left)
			} else {
				cells[i] = cell
			}
		default:
			cells[i] = Pad(cell, widths[i])
		}
	}
	return strings.TrimRight(strings.Join(cells, "  "), " ")
}

// shrinkColumns iteratively reduces the widest columns until the total fits.
func shrinkColumns(w []int, budget int) []int {
	out := append([]int(nil), w...)
	if budget < 8 {
		budget = 8
	}
	total := 0
	for _, x := range out {
		total += x + 2
	}
	if total <= budget {
		return out
	}
	excess := total - budget
	for excess > 0 {
		widest, wi := -1, -1
		for i, x := range out {
			if x > 6 && x > widest {
				widest, wi = x, i
			}
		}
		if wi < 0 {
			break
		}
		// Only columns wider than six are cut, so widest/2 is at least three.
		cut := widest / 2
		out[wi] -= cut
		excess -= cut
	}
	return out
}

// ─── Simple key/value list ──────────────────────────────────────────────────

// List renders a compact aligned "key  value" block.
type List struct{ pairs [][2]string }

// NewList creates an empty list.
func NewList() *List { return &List{} }

// Add appends a key/value pair.
func (l *List) Add(k, v string) *List {
	l.pairs = append(l.pairs, [2]string{k, v})
	return l
}

// Render prints the list.
func (l *List) Render() {
	if len(l.pairs) == 0 {
		return
	}
	w := 0
	for _, p := range l.pairs {
		if len(p[0]) > w {
			w = len(p[0])
		}
	}
	for _, p := range l.pairs {
		Line("  " + paint(Palette.Muted, Pad(p[0], w)) + "  " + p[1])
	}
}

// ─── Panel ──────────────────────────────────────────────────────────────────

// Panel renders content inside a rounded box, used for summaries.
type Panel struct {
	title string
	lines []string
	icon  string
}

// NewPanel creates a titled panel.
func NewPanel(title string) *Panel { return &Panel{title: title} }

// WithIcon prefixes the title with a glyph.
func (p *Panel) WithIcon(icon string) *Panel { p.icon = icon; return p }

// Line appends a pre-rendered line.
func (p *Panel) Line(s string) *Panel { p.lines = append(p.lines, s); return p }

// Linef appends a formatted line.
func (p *Panel) Linef(format string, a ...any) *Panel {
	p.lines = append(p.lines, fmt.Sprintf(format, a...))
	return p
}

// Render prints the panel.
func (p *Panel) Render() {
	if len(p.lines) == 0 {
		return
	}
	inner := 0
	for _, l := range p.lines {
		if VisibleWidth(l) > inner {
			inner = VisibleWidth(l)
		}
	}
	if p.title != "" {
		// inner+4 is the width of the frame: two borders, one space each side.
		if VisibleWidth(p.labelText())+4 > inner {
			inner = VisibleWidth(p.labelText()) + 4
		}
	}
	if inner+4 > Width()-2 {
		inner = Width() - 6
	}

	top := paint(Palette.Brand, boxTL)
	if p.title != "" {
		// The label has to fit the frame, which the width clamp above may have
		// narrowed under it. A title longer than the terminal would otherwise
		// push the top edge past the right of the body and wrap it.
		//
		// The label's own width is what the rule is measured from, not the
		// title's: the two used to disagree by the label's padding spaces,
		// which left the top border two cells wider than the body under it.
		label := Bold(paint(Palette.Brand, Truncate(" "+p.labelText()+" ", inner+2)))
		// The remainder is never negative: the label was truncated to fit above.
		rest := inner + 2 - VisibleWidth(label)
		top += label + paint(Palette.Rule, strings.Repeat(boxH, rest)) + paint(Palette.Brand, boxTR)
	} else {
		top += paint(Palette.Rule, strings.Repeat(boxH, inner+2)) + paint(Palette.Brand, boxTR)
	}
	Line("  " + top)
	v := paint(Palette.BrandDim, boxV)
	for _, l := range p.lines {
		Line("  " + v + " " + Pad(Truncate(l, inner), inner) + " " + v)
	}
	Line("  " + paint(Palette.Brand, boxBL) + paint(Palette.Rule, strings.Repeat(boxH, inner+2)) + paint(Palette.Brand, boxBR))
}

func (p *Panel) labelText() string {
	if p.icon == "" {
		return p.title
	}
	return p.icon + " " + p.title
}
