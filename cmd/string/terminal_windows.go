package main

import "syscall"

// isTerminalDescriptor asks for the console mode of the handle fd, which only
// a console has.
func isTerminalDescriptor(fd uintptr) bool {
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(fd), &mode) == nil
}
