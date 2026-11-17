package main

import (
	"syscall"
	"unsafe"
)

// isTerminalDescriptor asks the kernel for fd's terminal attributes, which
// only a terminal has.
func isTerminalDescriptor(fd uintptr) bool {
	var attributes syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&attributes)))
	return errno == 0
}
