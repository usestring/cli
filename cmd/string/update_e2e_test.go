package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildRelease builds string as release v, trusting key as a release build
// trusts the real signing keys: through the linker, the only way a key gets
// in.
func buildRelease(t *testing.T, v string, key ed25519.PublicKey) []byte {
	t.Helper()
	goCommand, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go command to build string with: %v", err)
	}
	out := filepath.Join(t.TempDir(), binaryName())
	flags := "-X main.version=" + v + " -X github.com/usestring/cli/internal/release.trustedKeys=" + base64.StdEncoding.EncodeToString(key)
	if output, err := exec.Command(goCommand, "build", "-o", out, "-ldflags", flags, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// installedRelease is a release build installed where the install script puts
// one, under a home of its own, taking releases from host.
type installedRelease struct {
	home, binary, host string
}

func installRelease(t *testing.T, data []byte, host string) installedRelease {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := installedRelease{home: home, binary: filepath.Join(home, ".local", "bin", binaryName()), host: host}
	if err := os.MkdirAll(filepath.Dir(r.binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.binary, data, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := r.run(t, "update", "--register"); got.status != 0 {
		t.Fatalf("update --register: %+v", got)
	}
	return r
}

// command is the installed string run with list. Its environment is this
// one's without CI, the STRING_ settings and where the home and the config
// directory are, which it gets its own of.
func (r installedRelease) command(ctx context.Context, list ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, r.binary, list...)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(strings.ToUpper(entry), "=")
		switch name {
		case "CI", "HOME", "USERPROFILE", "XDG_CONFIG_HOME":
			continue
		}
		if !strings.HasPrefix(name, "STRING_") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "HOME="+r.home, "USERPROFILE="+r.home, "STRING_DOWNLOAD_URL="+r.host, "STRING_API_BASE_URL=http://127.0.0.1:1/v1")
	return command
}

func (r installedRelease) run(t *testing.T, list ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := r.command(ctx, list...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	var exited *exec.ExitError
	if err := command.Run(); err != nil && !errors.As(err, &exited) {
		t.Fatalf("string %s: %v", strings.Join(list, " "), err)
	}
	return result{command.ProcessState.ExitCode(), stdout.String(), stderr.String()}
}

// version is what the installed binary prints for --version, or "" while
// there is none to run, as for a moment during a swap on Windows.
func (r installedRelease) version() string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := r.command(ctx, "--version").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func (r installedRelease) file(name string) string {
	return filepath.Join(r.home, ".config", "string", name)
}

// unlocked reports whether no update holds update.lock.
func (r installedRelease) unlocked() bool {
	_, err := os.Stat(r.file("update.lock"))
	return errors.Is(err, fs.ErrNotExist)
}

// waitFor polls until done is true. The deadline only stops a hang.
func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Minute); !done(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s", what)
		}
	}
}

// waitUntilStopped waits for the check a command started to end, so that the
// test removes nothing it still uses. On Windows no directory can be removed
// while a program runs from it, so removing the binary's directory succeeds
// only then; elsewhere it succeeds at once.
func (r installedRelease) waitUntilStopped(t *testing.T) {
	t.Helper()
	waitFor(t, "the check to release update.lock", r.unlocked)
	waitFor(t, "the check to end", func() bool { return os.RemoveAll(filepath.Dir(r.binary)) == nil })
}

// Release builds of string that trust a test key, with the older one updating
// itself from a download host serving the newer: with string update, with
// the daily check an ordinary command starts, and with that command's output
// read through pipes as an agent reads it.
func TestUpdateEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds string twice")
	}
	seed, public := testKey(t)
	older, newer := buildRelease(t, "0.3.0", public), buildRelease(t, "0.3.1", public)
	host := newUpdateHost(t)
	host.point("0.3.1")
	host.add(t, releaseWith("0.3.1", seed, archiveOf(t, binaryName(), newer, false)))

	t.Run("string update", func(t *testing.T) {
		r := installRelease(t, older, host.url)
		if got := r.run(t, "update"); got.status != 0 || r.version() != "string 0.3.1\n" {
			t.Errorf("%+v, and the binary prints %q", got, r.version())
		}
	})

	t.Run("the daily check", func(t *testing.T) {
		r := installRelease(t, older, host.url)
		if got := r.run(t, "status"); got.status != 0 {
			t.Fatalf("status: %+v", got)
		}
		waitFor(t, "the copy to update itself", func() bool { return r.version() == "string 0.3.1\n" })
		r.waitUntilStopped(t)
		if state := readUpdateState(r.file("update.json")); state.UpdatedFrom != "0.3.0" || state.UpdatedTo != "0.3.1" || state.Error != "" {
			t.Errorf("update.json is %+v", state)
		}
	})

	// Agents read a command through pipes and wait for both to close, so the
	// check it starts must hold neither.
	t.Run("pipes", func(t *testing.T) {
		stalled := newUpdateHost(t)
		arrived, ended, answer := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var arrival sync.Once
		stalled.handlers["/stable"] = func(w http.ResponseWriter, request *http.Request) {
			arrival.Do(func() { close(arrived) })
			select {
			case <-answer:
				w.Write([]byte("0.3.0\n"))
			case <-request.Context().Done():
				close(ended)
			}
		}
		release := sync.OnceFunc(func() { close(answer) })
		t.Cleanup(release)
		r := installRelease(t, older, stalled.url)
		command := r.command(context.Background(), "status")
		stdout, err := command.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		stderr, err := command.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		var read sync.WaitGroup
		for _, pipe := range []io.Reader{stdout, stderr} {
			read.Go(func() { _, _ = io.Copy(io.Discard, pipe) })
		}
		read.Wait()
		if err := command.Wait(); err != nil {
			t.Fatalf("status: %v", err)
		}
		// The reader has its end of file and the exit status, and the check
		// the command started is still waiting on the stalled host.
		select {
		case <-arrived:
		case <-time.After(2 * time.Minute):
			t.Fatal("the check never asked the download host")
		}
		select {
		case <-ended:
			t.Fatal("the check had given up on the host before the command's output ended")
		default:
		}
		// The host answers only now, so a check that records the answer was
		// still running after the output ended. One that had held a pipe
		// would have ended first, at its own deadline, with an error.
		release()
		r.waitUntilStopped(t)
		if state := readUpdateState(r.file("update.json")); state.Latest != "0.3.0" || state.Error != "" {
			t.Errorf("update.json is %+v", state)
		}
	})
}
