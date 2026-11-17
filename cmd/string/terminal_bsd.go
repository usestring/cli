//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package main

import (
	"syscall"
	"unsafe"
)

// isTerminalDescriptor asks the kernel for fd's terminal attributes, which
// only a terminal has. macOS and the BSDs name the request TIOCGETA.
func isTerminalDescriptor(fd uintptr) bool {
	var attributes syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGETA, uintptr(unsafe.Pointer(&attributes)))
	return errno == 0
}
