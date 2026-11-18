package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// createNoWindow gives the background check a console that is never shown, so
// that neither it nor the --version check it runs opens a window.
const createNoWindow = 0x08000000

// detachedAttributes put the background check in a process group of its own,
// out of reach of Ctrl-C in the console it was started from.
func detachedAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow}
}

// errorSharingViolation is Windows refusing a rename while another program,
// such as a virus scanner, has the file open.
const errorSharingViolation syscall.Errno = 32

// swapBinary puts the new binary at path. Windows refuses to replace or delete
// an executable that is running but lets it be renamed, so the old one moves
// aside under a name of its own, and a later update deletes it once nothing
// runs it. An earlier aside file can still be in use by a long command.
func swapBinary(newPath, path string) error {
	aside := path + "." + randomSuffix() + ".old"
	if err := renameWhenFree(path, aside); err != nil {
		return err
	}
	if err := renameWhenFree(newPath, path); err != nil {
		if undo := renameWhenFree(aside, path); undo != nil {
			return fmt.Errorf("%w, and moving the old binary back failed too, so it is at %s: %v", err, aside, undo)
		}
		return err
	}
	return nil
}

// renameWhenFree retries, for up to two seconds, a rename refused while a
// scanner looks at a new executable, which shows as a sharing violation or as
// access denied. The Go toolchain retries the same two errors on Windows.
func renameWhenFree(from, to string) error {
	var err error
	for range 10 {
		err = os.Rename(from, to)
		if !errors.Is(err, errorSharingViolation) && !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return err
}

// removeAsides deletes the binaries earlier updates moved aside from path.
// One that a command still runs from cannot be deleted, and stays for later.
func removeAsides(path string) {
	dir, name := filepath.Split(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), name+".") && strings.HasSuffix(entry.Name(), ".old") {
			os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
