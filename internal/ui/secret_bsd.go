//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package ui

import (
	"bufio"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// BSD/macOS ioctl request numbers for terminal attribute access.
const (
	tcgets = syscall.TIOCGETA
	tcsets = syscall.TIOCSETA
)

// readPassword disables terminal echo while reading a line from stdin.
func readPassword() (string, error) {
	fd := int(os.Stdin.Fd())

	var old syscall.Termios
	if _, _, errno := syscall.Syscall6(
		syscall.SYS_IOCTL, uintptr(fd), tcgets,
		uintptr(unsafe.Pointer(&old)), 0, 0, 0,
	); errno != 0 {
		return "", errno
	}

	newState := old
	newState.Lflag &^= syscall.ECHO
	newState.Lflag |= syscall.ICANON | syscall.ISIG
	newState.Iflag |= syscall.ICRNL
	if _, _, errno := syscall.Syscall6(
		syscall.SYS_IOCTL, uintptr(fd), tcsets,
		uintptr(unsafe.Pointer(&newState)), 0, 0, 0,
	); errno != 0 {
		return "", errno
	}
	defer func() {
		_, _, _ = syscall.Syscall6(
			syscall.SYS_IOCTL, uintptr(fd), tcsets,
			uintptr(unsafe.Pointer(&old)), 0, 0, 0,
		)
	}()

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line), err
}
