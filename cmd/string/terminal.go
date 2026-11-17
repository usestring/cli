//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows

package main

import "os"

// isTerminal reports whether file is a terminal, by asking the system about
// its descriptor. The kind of file does not say: /dev/null and NUL are
// character devices, as a terminal is, and a command often has one as stdin
// when nobody is at the keyboard, as under a coding agent or in CI.
func isTerminal(file *os.File) bool {
	// Not file.Fd, which can change how the file does its I/O.
	conn, err := file.SyscallConn()
	if err != nil {
		return false
	}
	terminal := false
	if err := conn.Control(func(fd uintptr) { terminal = isTerminalDescriptor(fd) }); err != nil {
		return false
	}
	return terminal
}
