// Command string is String Web Access from a terminal: the Go build of
// @usestring/cli, with the same commands, flags, output and exit codes.
package main

import (
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// version is stamped by release builds with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// A closed pipe (string search ... | head) ends the command quietly with
	// its status, as the Node CLI does, instead of dying on SIGPIPE.
	signal.Ignore(syscall.SIGPIPE)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Node's fetch gives up on a response that sends no headers for five
	// minutes; without this a stalled request would hang forever.
	transport.ResponseHeaderTimeout = 5 * time.Minute
	os.Exit(run(os.Args[1:], runtime{
		env:      os.LookupEnv,
		lookPath: exec.LookPath,
		client:   &http.Client{Transport: transport},
		stdin:    os.Stdin,
		stdout:   os.Stdout,
		stderr:   os.Stderr,
	}))
}
