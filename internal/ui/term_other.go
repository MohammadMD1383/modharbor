//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package ui

import "os"

// terminalWidth is unavailable on this platform; fall back to a default.
func terminalWidth(_ *os.File) int { return DefaultWidth }

// terminalHeight is unavailable on this platform.
func terminalHeight(_ *os.File) int { return 0 }
