//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package ui

import (
	"bufio"
	"os"
	"strings"
)

// readPassword falls back to a plain read on platforms without termios.
func readPassword() (string, error) {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line), err
}
