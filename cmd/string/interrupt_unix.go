//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

// endInterrupted ends string the way SIGINT ends a program that doesn't catch
// it, so the shell sees 128+2 and a script that ran string stops too. The exit
// is for a signal that somehow doesn't end it.
func endInterrupted() {
	signal.Reset(os.Interrupt)
	syscall.Kill(os.Getpid(), syscall.SIGINT)
	time.Sleep(time.Second)
	os.Exit(exitInterrupted)
}
