//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows)

package main

import "os"

// isTerminal takes any character device for a terminal, /dev/null included:
// on this system the standard library has no way to ask which of them it is.
func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
