package ui

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Prompter asks the user questions, degrading to safe defaults when stdin
// is not a TTY (CI, pipes, --yes).
type Prompter struct {
	r      *bufio.Reader
	w      *os.File
	assume bool
}

// NewPrompter builds a prompter. When assume is true every question returns
// the default, which is what --non-interactive does.
func NewPrompter(assume bool) *Prompter {
	return &Prompter{r: bufio.NewReader(os.Stdin), w: os.Stdout, assume: assume}
}

func (p *Prompter) readLine() (string, bool) {
	line, err := p.r.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	return strings.TrimSpace(line), true
}

func (p *Prompter) ask(prompt, def string) string {
	if p.assume || !IsInteractive() {
		return def
	}
	s := paint(Palette.Brand, "?") + " " + prompt
	if def != "" {
		s += " " + paint(Palette.Faint, "["+def+"]")
	}
	s += paint(Palette.Faint, " › ")
	_, _ = fmt.Fprint(p.w, s)
	line, ok := p.readLine()
	if !ok || line == "" {
		_, _ = fmt.Fprintln(p.w)
		return def
	}
	return line
}

// Confirm asks a yes/no question. Default is used on empty input.
func (p *Prompter) Confirm(prompt string, def bool) bool {
	suffix := "y/N"
	if def {
		suffix = "Y/n"
	}
	ans := strings.ToLower(p.ask(prompt+" ("+suffix+")", ""))
	switch ans {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	}
	return def
}

// Input asks for a free-form value.
func (p *Prompter) Input(prompt, def string) string {
	return p.ask(prompt, def)
}

// Int asks for a whole number, re-prompting until valid.
func (p *Prompter) Int(prompt string, def int) int {
	for {
		s := p.ask(prompt, strconv.Itoa(def))
		if s == "" {
			return def
		}
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
		Warn("  %q is not a number", s)
	}
}

// Select shows a numbered menu and returns the chosen index (0-based).
// When non-interactive it returns def.
func (p *Prompter) Select(prompt string, options []string, def int) int {
	if p.assume || !IsInteractive() {
		return def
	}
	if len(options) == 0 {
		return -1
	}
	Blank()
	Line("  " + Bold(prompt))
	for i, o := range options {
		Line(fmt.Sprintf("    %s %s", paint(Palette.Brand, PadLeft(strconv.Itoa(i+1), 2)), o))
	}
	idx := p.Int("  choice", def+1) - 1
	if idx < 0 || idx >= len(options) {
		return def
	}
	return idx
}

// MultiSelect lets the user toggle options by number. Returns chosen indices.
// def is the default selection when input is unavailable.
func (p *Prompter) MultiSelect(prompt string, options []string, def []bool) ([]int, bool) {
	if p.assume || !IsInteractive() {
		var out []int
		for i, d := range def {
			if d {
				out = append(out, i)
			}
		}
		return out, true
	}
	sel := make([]bool, len(options))
	copy(sel, def)

	Blank()
	Line("  " + Bold(prompt))
	for i, o := range options {
		Line(fmt.Sprintf("    %s %s", paint(Palette.Brand, PadLeft(strconv.Itoa(i+1), 2)), o))
	}
	_, _ = fmt.Fprintf(p.w, "  %s numbers to toggle, %s accept › ",
		paint(Palette.Brand, "space/comma"), paint(Palette.Brand, "enter"))
	raw, ok := p.readLine()
	if !ok || strings.TrimSpace(raw) == "" {
		var out []int
		for i, s := range sel {
			if s {
				out = append(out, i)
			}
		}
		return out, true
	}
	for _, tok := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ' ' || r == ',' || r == '+'
	}) {
		if n, err := strconv.Atoi(tok); err == nil && n >= 1 && n <= len(sel) {
			sel[n-1] = !sel[n-1]
		}
	}
	var out []int
	for i, s := range sel {
		if s {
			out = append(out, i)
		}
	}
	return out, true
}

// Secret reads a value without echoing (used for API keys).
func (p *Prompter) Secret(prompt string) string {
	if p.assume || !IsInteractive() {
		return ""
	}
	_, _ = fmt.Fprintf(p.w, "  %s %s › ", paint(Palette.Brand, "?"), prompt)
	v, err := readPassword()
	if err != nil {
		_, _ = fmt.Fprintln(p.w)
		return ""
	}
	_, _ = fmt.Fprintln(p.w)
	return v
}
