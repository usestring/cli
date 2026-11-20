//go:build unix

package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// interruptHelperEnv, when set, makes this test binary run string with its
// words as arguments: see TestInterruptHelperProcess.
const interruptHelperEnv = "STRING_TEST_INTERRUPT_HELPER"

// TestInterruptHelperProcess is a string that a test interrupts, not a test.
// It handles Ctrl-C as main does.
func TestInterruptHelperProcess(t *testing.T) {
	words := os.Getenv(interruptHelperEnv)
	if words == "" {
		return
	}
	rt := runtime{
		env:       os.LookupEnv,
		client:    &http.Client{},
		stdin:     os.Stdin,
		stdout:    os.Stdout,
		stderr:    os.Stderr,
		sleep:     time.Sleep,
		exec:      newExecCtx(os.LookupEnv, os.Stdin, os.Stdout, os.Stderr),
		interrupt: &interruption{},
	}
	stopOnInterrupt(rt.interrupt, os.Stderr)
	os.Exit(run(strings.Fields(words), rt))
}

// Ctrl-C ends string the way SIGINT ends a program, so the shell sees that it
// was interrupted, once string has said what keeps running.
func TestCtrlCEndsStringAsAnInterruptedProgram(t *testing.T) {
	test, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		about, words, at, said string
	}{
		{"a crawl's wait", "sitemap status job-1 --wait", "GET /v1/sitemap/job-1 2", "job job-1 · running · resume: string sitemap status job-1 --wait\n" + crawlGoesOn},
		// A fetch leaves nothing behind, so nothing is said.
		{"a fetch", "fetch https://example.com", "POST /v1/fetch 1", ""},
	} {
		reached := make(chan struct{})
		var once sync.Once
		var mu sync.Mutex
		seen := map[string]int{}
		base := api(t, func(w http.ResponseWriter, r *http.Request) {
			// With the body read, the server sees string go away.
			io.Copy(io.Discard, r.Body)
			mu.Lock()
			kind := r.Method + " " + r.URL.Path
			seen[kind]++
			at := kind+" "+strconv.Itoa(seen[kind]) == c.at
			mu.Unlock()
			if at {
				once.Do(func() { close(reached) })
			}
			if r.Method == "GET" {
				io.WriteString(w, crawlRunning)
				return
			}
			// The fetch never answers, until string is gone.
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
		})
		helper := exec.Command(test, "-test.run=^TestInterruptHelperProcess$")
		helper.Env = append(os.Environ(), interruptHelperEnv+"="+c.words, "HOME="+t.TempDir(), "STRING_API_KEY=test-key", "STRING_API_BASE_URL="+base, "STRING_AGENT=human")
		var stderr bytes.Buffer
		helper.Stderr = &stderr
		if err := helper.Start(); err != nil {
			t.Fatal(err)
		}
		ended := make(chan error, 1)
		go func() { ended <- helper.Wait() }()
		select {
		case <-reached:
		case err := <-ended:
			t.Fatalf("%s: string ended before the Ctrl-C: %v: %s", c.about, err, stderr.String())
		case <-time.After(10 * time.Second):
			helper.Process.Kill()
			t.Fatalf("%s: string never reached %s", c.about, c.at)
		}

		helper.Process.Signal(os.Interrupt)

		var err error
		select {
		case err = <-ended:
		case <-time.After(10 * time.Second):
			helper.Process.Kill()
			t.Fatalf("%s: string kept running after the Ctrl-C", c.about)
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("%s: string ended with %v, not by a signal", c.about, err)
		}
		if status, ok := exit.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGINT {
			t.Errorf("%s: string ended with %v, want SIGINT", c.about, exit)
		}
		if stderr.String() != c.said {
			t.Errorf("%s: stderr %q, want %q", c.about, stderr.String(), c.said)
		}
	}
}
