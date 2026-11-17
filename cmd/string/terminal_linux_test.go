package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// openTerminal opens a pseudo-terminal and returns the end that a program
// started at a terminal has as its stdin. It skips the test where the system
// has none to give.
func openTerminal(t *testing.T) *os.File {
	t.Helper()
	// The end a terminal emulator holds. The terminal lasts while it is open.
	emulator, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("cannot open a pseudo-terminal: %v", err)
	}
	t.Cleanup(func() { emulator.Close() })
	request := func(code uintptr, value *uint32) error {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, emulator.Fd(), code, uintptr(unsafe.Pointer(value))); errno != 0 {
			return errno
		}
		return nil
	}
	// A new pseudo-terminal is locked, and its number names the other end.
	var unlocked, number uint32
	if err := request(syscall.TIOCSPTLCK, &unlocked); err != nil {
		t.Skipf("cannot unlock the pseudo-terminal: %v", err)
	}
	if err := request(syscall.TIOCGPTN, &number); err != nil {
		t.Skipf("cannot find the pseudo-terminal's number: %v", err)
	}
	terminal, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("cannot open the pseudo-terminal: %v", err)
	}
	t.Cleanup(func() { terminal.Close() })
	return terminal
}

// Without this, a check that never says yes would pass the other tests, and
// string login would stop waiting for the person at a terminal.
func TestAPseudoTerminalIsATerminal(t *testing.T) {
	if terminal := openTerminal(t); !isTerminal(terminal) {
		t.Error("a pseudo-terminal does not count as a terminal")
	}
}

// At a terminal nothing is piped in, and login --with-token says so. Had the
// command taken the terminal for something else, it would wait to read a key.
func TestAtATerminalLoginWithTokenAsksForAPipe(t *testing.T) {
	got := runCommand(t, "http://127.0.0.1:1", openTerminal(t), "login", "--with-token")
	if got.status != 2 || !strings.Contains(got.errors, "Pipe the key on stdin") {
		t.Errorf("login --with-token at a terminal: %+v", got)
	}
}
