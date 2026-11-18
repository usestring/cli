package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// string login --env-file puts the key in an env file for an app or agent to
// load, and never prints it.
func TestLoginEnvFileSetsTheKeyLineAndLeavesTheRestOfTheFile(t *testing.T) {
	service := &signInService{token: []signInAnswer{signInApproved}, key: []signInAnswer{signInKey}}
	service.start(t)
	dir := t.TempDir()
	expect := func(about string, got result, path, content string, mode os.FileMode) {
		t.Helper()
		if got.status != 0 || strings.Contains(got.output+got.errors, "released-key") {
			t.Errorf("%s: want exit 0 and the key printed nowhere: %+v", about, got)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != content {
			t.Errorf("%s: the file holds %q, %v, want %q", about, data, err, content)
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != mode {
			t.Errorf("%s: the file's mode is not %v: %v, %v", about, mode, info, err)
		}
	}

	// A person waits at a terminal, and the file is new.
	session := newSignInSession(t)
	session.terminal = true
	created := filepath.Join(dir, ".env")
	got := session.run("login", "--env-file", created)
	expect("a new file", got, created, "STRING_API_KEY=released-key\n", 0o600)
	if !strings.HasSuffix(got.output, "Wrote it to "+created+" as STRING_API_KEY.\n") {
		t.Errorf("a new file: the output doesn't say where the key went: %q", got.output)
	}

	// An agent names the file when it starts, and a bare --poll finishes. Every
	// line that set the key gets the new one, whichever way it was written.
	existing := filepath.Join(dir, "app.env")
	before := "# app\r\nexport STRING_API_KEY=old-key\r\nAPP_NAME=demo\r\nSTRING_API_KEY = older-key\r\nDEBUG=1"
	if err := os.WriteFile(existing, []byte(before), 0o640); err != nil {
		t.Fatal(err)
	}
	session = newSignInSession(t)
	if started := session.run("login", "--json", "--env-file", existing); started.status != 0 {
		t.Fatalf("login --json --env-file: %+v", started)
	}
	got = session.run("login", "--poll", "--json")
	expect("a file that set the key", got, existing, "# app\r\nexport STRING_API_KEY=released-key\r\nAPP_NAME=demo\r\nSTRING_API_KEY=released-key\r\nDEBUG=1", 0o640)
	if got.status == 0 && decode(t, got.output).(map[string]any)["env_file"] != existing {
		t.Errorf("a file that set the key: the JSON doesn't name the file: %s", got.output)
	}

	// --env-file on --poll works too, and a file without the key gains a line.
	other := filepath.Join(dir, "other.env")
	if err := os.WriteFile(other, []byte("DEBUG=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = newSignInSession(t).startThenPoll(t, "--env-file", other)
	expect("a file without the key", got, other, "DEBUG=1\nSTRING_API_KEY=released-key\n", 0o600)

	// A file that can't be written is refused before a sign-in starts, so
	// nobody approves a sign-in whose key would not arrive there.
	started := service.requests("/oauth/device_authorization")
	got = newSignInSession(t).run("login", "--env-file", filepath.Join(dir, "missing", ".env"))
	if got.status != exitUsage || service.requests("/oauth/device_authorization") != started {
		t.Errorf("a file in a missing directory: %+v, and %d sign-ins started, want exit %d and none", got, service.requests("/oauth/device_authorization")-started, exitUsage)
	}
}
