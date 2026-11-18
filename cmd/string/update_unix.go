//go:build unix

package main

import (
	"os"
	"syscall"
)

// detachedAttributes give the background check a session of its own, so that
// closing the terminal, or Ctrl-C in it, does not stop the check.
func detachedAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// swapBinary renames the new binary over the old one. The old file is never
// written to: a process running it keeps it, and the new file being a new
// inode keeps macOS from checking it against the old one's code signature.
func swapBinary(newPath, path string) error {
	return os.Rename(newPath, path)
}

// removeAsides has nothing to remove: only Windows moves the old binary aside.
func removeAsides(path string) {}
