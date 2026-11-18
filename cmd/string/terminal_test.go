//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

func TestTheNullDeviceAPipeAndAFileAreNotTerminals(t *testing.T) {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	regular, err := os.Create(filepath.Join(t.TempDir(), "file"))
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	closed, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()

	for name, file := range map[string]*os.File{
		"the null device, " + os.DevNull: null,
		"the reading end of a pipe":      reader,
		"the writing end of a pipe":      writer,
		"a regular file":                 regular,
		"a closed file":                  closed,
		// What os.NewFile returns for a descriptor that is not valid.
		"no file": nil,
	} {
		if isTerminal(file) {
			t.Errorf("%s counts as a terminal", name)
		}
	}
}

// The pseudo-terminal test covers Linux wherever the tests run. This one also
// covers macOS, the BSDs and Windows, whenever the tests run from a terminal.
func TestTheTerminalTheTestsRunFromIsATerminal(t *testing.T) {
	name := "/dev/tty"
	if goruntime.GOOS == "windows" {
		name = "CONIN$"
	}
	terminal, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		t.Skipf("the tests are not running from a terminal: %v", err)
	}
	defer terminal.Close()
	if !isTerminal(terminal) {
		t.Errorf("%s does not count as a terminal", name)
	}
}

// signInEnv, when set, makes this test binary stand in for the command: see
// TestCommandProcess. Its value is where that command's sign-in goes.
const signInEnv = "STRING_TEST_SIGN_IN"

// TestCommandProcess is the command, not a test of it. Only main looks at the
// real stdin, and main ends the process, so a test that needs it runs this
// test binary again with signInEnv set. This then runs main with the
// arguments that follow "--".
func TestCommandProcess(t *testing.T) {
	service := os.Getenv(signInEnv)
	if service == "" {
		return
	}
	authURL, portalURL, oauthClientID = service, service, "test-client"
	os.Args = append([]string{"string"}, flag.Args()...)
	main()
}

// runCommand runs the command in a process of its own and returns how it
// ended. stdin is its standard input, or the null device when nil. Its home is
// a throwaway one, and its PATH has no program in it, so that string login
// finds no browser to open.
func runCommand(t *testing.T, service string, stdin *os.File, list ...string) result {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A command that takes its stdin for a terminal can wait for as long as a
	// sign-in code lives, so it gets a deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, append([]string{"-test.run=^TestCommandProcess$", "--"}, list...)...)
	command.Env = []string{signInEnv + "=" + service, "HOME=" + t.TempDir(), "PATH=" + t.TempDir(), "STRING_API_BASE_URL=http://127.0.0.1:1/v1"}
	if stdin != nil {
		command.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	var exited *exec.ExitError
	if err := command.Run(); err != nil && !errors.As(err, &exited) {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatalf("string %s was still running after 20 seconds, having printed %q", strings.Join(list, " "), stdout.String())
	}
	return result{command.ProcessState.ExitCode(), stdout.String(), stderr.String()}
}

// A command often has the null device as its stdin when nobody is at the
// keyboard, as under a coding agent or in CI. string took that for a
// terminal, and waited ten minutes for a person.
func TestGivenTheNullDeviceAsStdinTheCommandWaitsForNobody(t *testing.T) {
	// A sign-in that waits asks for the token, and is refused here, so the
	// command ends either way.
	service := &signInService{token: []signInAnswer{{http.StatusBadRequest, `{"error":"access_denied"}`, ""}}}
	service.start(t)

	login := runCommand(t, authURL, nil, "login")
	if login.status != 0 || !strings.Contains(login.output, "Open https://example.test/device?code=ABCD-EFGH and confirm the code ABCD-EFGH.\nThen run string login --poll") {
		t.Errorf("login: %+v", login)
	}
	initialized := runCommand(t, authURL, nil, "init")
	if initialized.status != 0 || strings.Contains(initialized.output, "press Ctrl-C") || !strings.Contains(initialized.output, "Then run string login --poll") {
		t.Errorf("init: %+v", initialized)
	}
	if started, asked := service.requests("/oauth/device_authorization"), service.requests("/oauth/token"); started != 2 || asked != 0 {
		t.Errorf("%d sign-ins were started and the token was asked for %d times; login and init start one each, and neither waits", started, asked)
	}
	token := runCommand(t, authURL, nil, "login", "--with-token")
	if token.status != 2 || !strings.Contains(token.errors, "No key on stdin") {
		t.Errorf("login --with-token: %+v", token)
	}
}
