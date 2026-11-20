// Command string is String Web Access from a terminal: the Go build of
// @usestring/cli, which works without a key for fetch and search.
package main

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	goruntime "runtime"
	"syscall"
	"time"
)

// version is stamped by release builds with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// A closed pipe (string search ... | head) ends the command quietly with
	// its status, as the Node CLI does, instead of dying on SIGPIPE.
	signal.Ignore(syscall.SIGPIPE)
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	go func() {
		<-interrupts
		os.Exit(exitInterrupted)
	}()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Node's fetch gives up on a response that sends no headers for five
	// minutes; without this a stalled request would hang forever.
	transport.ResponseHeaderTimeout = 5 * time.Minute
	rt := runtime{
		env:      os.LookupEnv,
		lookPath: exec.LookPath,
		client:   &http.Client{Transport: transport},
		stdin:    os.Stdin,
		stdout:   os.Stdout,
		stderr:   os.Stderr,
		openURL:  openBrowser,
		sleep:    time.Sleep,
		program:  runProgram,
		exec:     newExecCtx(os.LookupEnv, os.Stdin, os.Stdout, os.Stderr),
		spawn:    spawnDetached,
	}
	status := run(os.Args[1:], rt)
	afterCommand(os.Args[1:], rt)
	os.Exit(status)
}

// runProgram runs name with args and returns what it printed. The deadline
// stops a program that hangs from holding the command that ran it.
func runProgram(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// openBrowser shows link in the default browser. string login prints the link
// too, so a machine without a browser only loses the convenience.
func openBrowser(link string) error {
	var command *exec.Cmd
	switch goruntime.GOOS {
	case "darwin":
		command = exec.Command("open", link)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", link)
	default:
		command = exec.Command("xdg-open", link)
	}
	if err := command.Start(); err != nil {
		return err
	}
	go command.Wait()
	return nil
}
