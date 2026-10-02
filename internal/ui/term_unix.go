//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package ui

import (
	"os"
	"syscall"
	"unsafe"
)

type winsize struct {
	rows, cols, xpixel, ypixel uint16
}

const tiocgwinsz = syscall.TIOCGWINSZ

func windowSize(f *os.File) (cols, rows int) {
	var ws winsize
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		f.Fd(),
		uintptr(tiocgwinsz),
		uintptr(unsafe.Pointer(&ws)),
	)
	if errno != 0 {
		return 0, 0
	}
	return int(ws.cols), int(ws.rows)
}

// terminalWidth returns the column count of the terminal attached to f,
// or 0 when it cannot be determined.
func terminalWidth(f *os.File) int {
	c, _ := windowSize(f)
	return c
}

// terminalHeight returns the row count of the terminal attached to f.
func terminalHeight(f *os.File) int {
	_, r := windowSize(f)
	return r
}
