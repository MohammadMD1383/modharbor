package ui

import "os"

// DefaultWidth is used when the real terminal width cannot be determined.
const DefaultWidth = 100

// MaxWidth caps table width so output stays readable on ultrawide monitors.
const MaxWidth = 140

// Width returns the usable terminal width, clamped to a sane range.
func Width() int {
	w := terminalWidth(os.Stdout)
	if w < 40 {
		w = terminalWidth(os.Stderr)
	}
	if w <= 0 {
		w = DefaultWidth
	}
	if w > MaxWidth {
		w = MaxWidth
	}
	return w
}

// Height returns the terminal row count, or 0 when unknown.
func Height() int {
	return terminalHeight(os.Stdout)
}
