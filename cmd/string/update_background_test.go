package main

import (
	"bytes"
	"cmp"
	"crypto/ed25519"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Where a package manager would have put the binary, under the home.
const (
	homebrewDir = "Cellar/string/0.3.0/bin"
	npxDir      = ".npm/_npx/4f3a/node_modules/@usestring/cli/bin"
	byHandDir   = "Downloads"
)

// Commands the update check follows that need no network: status without a
// key prints a report and succeeds, and balance without one fails.
var offlineCommands = [][]string{{"status"}, {"balance"}}

func TestUpdateTriggerStartsACheckOnlyWhenOneIsDueAndAllowed(t *testing.T) {
	_, public := testKey(t)
	now := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	for _, check := range []struct {
		name string
		// dir is where the binary is. Empty means .local/bin, registered by
		// the install script.
		dir      string
		pinned   bool
		version  string
		env      map[string]string
		noKeys   bool
		terminal bool
		agent    string
		piped    bool
		// checked is when the last check was; zero is never.
		checked time.Time
		due     bool
	}{
		{name: "never checked", due: true},
		{name: "checked an hour ago", checked: now.Add(-time.Hour)},
		{name: "checked a day ago", checked: now.Add(-checkEvery), due: true},
		{name: "checked tomorrow by a clock that was wrong", checked: now.Add(checkEvery), due: true},
		{name: "in CI", env: map[string]string{"CI": "true"}},
		{name: "with CI=false", env: map[string]string{"CI": "false"}, due: true},
		{name: "with CI=0", env: map[string]string{"CI": "0"}, due: true},
		{name: "with STRING_AUTO_UPDATE=0", env: map[string]string{"STRING_AUTO_UPDATE": "0"}},
		{name: "with STRING_AUTO_UPDATE=OFF", env: map[string]string{"STRING_AUTO_UPDATE": "OFF"}},
		{name: "with STRING_AUTO_UPDATE=1", env: map[string]string{"STRING_AUTO_UPDATE": "1"}, due: true},
		{name: "a development build", version: "dev"},
		{name: "a pinned copy", pinned: true},
		{name: "a build with no signing key", noKeys: true},
		{name: "from Homebrew on a terminal", dir: homebrewDir, terminal: true, due: true},
		{name: "from Homebrew with nobody to tell", dir: homebrewDir},
		{name: "from Homebrew on a terminal in CI", dir: homebrewDir, terminal: true, env: map[string]string{"CI": "1"}},
		{name: "from Homebrew under an agent at a terminal", dir: homebrewDir, terminal: true, agent: "codex"},
		{name: "from Homebrew with stdout piped", dir: homebrewDir, terminal: true, piped: true},
		{name: "from npx on a terminal", dir: npxDir, terminal: true},
		{name: "installed by hand on a terminal", dir: byHandDir, terminal: true, due: true},
		{name: "installed by hand with nobody to tell", dir: byHandDir},
	} {
		for _, list := range offlineCommands {
			t.Run(check.name+", "+list[0], func(t *testing.T) {
				setVersion(t, cmp.Or(check.version, "0.3.0"))
				host := newUpdateHost(t)
				c := newTestCopy(t, host, []ed25519.PublicKey{public}, cmp.Or(check.dir, ".local/bin"))
				if check.dir == "" {
					register := []string{"update", "--register"}
					if check.pinned {
						register = append(register, "--pin")
					}
					c.run(register...)
				}
				if check.noKeys {
					c.keys = nil
				}
				maps.Copy(c.env, check.env)
				c.terminal, c.agent, c.piped = check.terminal, check.agent, check.piped
				c.now = now
				if !check.checked.IsZero() {
					c.record(updateState{CheckedAt: check.checked})
				}

				alone := c.runAlone(list...)
				if got := c.run(list...); got != alone {
					t.Errorf("the update check changed how the command ended:\n%+v\nwithout it:\n%+v", got, alone)
				}
				after, _ := os.ReadFile(c.file("update.json"))
				if check.due && (!slices.Equal(c.spawned, []string{c.binary}) || !c.state().CheckedAt.Equal(now)) {
					t.Errorf("no check started: spawned %v, update.json %s", c.spawned, after)
				}
				if !check.due && (len(c.spawned) > 0 || !c.state().CheckedAt.Equal(check.checked)) {
					t.Errorf("a check started: spawned %v, update.json %s", c.spawned, after)
				}
				// The command only starts the check; the check asks the host.
				if len(host.requested()) > 0 {
					t.Errorf("the command asked the download host for %v", host.requested())
				}
			})
		}
	}
}

// A machine where the check cannot be recorded, such as one with a read-only
// config directory or in a sandbox, would otherwise start one on every
// command.
func TestUpdateTriggerStartsNoCheckItCannotRecord(t *testing.T) {
	setVersion(t, "0.3.0")
	_, public := testKey(t)
	host := newUpdateHost(t)

	// update.json cannot be replaced while a directory is in its place.
	script := newTestCopy(t, host, []ed25519.PublicKey{public}, ".local/bin")
	script.run("update", "--register")
	if err := os.Mkdir(script.file("update.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Nothing can be written under a config directory that is a file.
	byHand := newTestCopy(t, host, []ed25519.PublicKey{public}, byHandDir)
	byHand.terminal = true
	config := byHand.file("")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]*testCopy{"from the install script": script, "installed by hand": byHand} {
		alone := c.runAlone("status")
		if got := c.run("status"); got != alone || len(c.spawned) > 0 {
			t.Errorf("%s: %+v, spawned %v", name, got, c.spawned)
		}
	}
}

// Install scripts, package managers' tests and the updater itself run
// --version on a binary that may not be installed yet, and nothing may follow
// that or any other look at the binary.
func TestUpdateTriggerLeavesProbesAlone(t *testing.T) {
	setVersion(t, "0.3.0")
	_, public := testKey(t)
	host := newUpdateHost(t)
	host.point("0.3.0")
	newCopy := func() *testCopy {
		c := newTestCopy(t, host, []ed25519.PublicKey{public}, ".local/bin")
		c.run("update", "--register")
		c.terminal = true
		// A check is due, and an update and a release are waiting to be told.
		c.record(updateState{Latest: "0.3.1", UpdatedFrom: "0.2.9", UpdatedTo: "0.3.0"})
		return c
	}
	for _, list := range [][]string{{}, {"--version"}, {"--json", "--version"}, {"--help"}, {"-h"}, {"help"}, {"search", "--help"}, {"sitemap"}, {"integrations", "help"}, {"update"}, {"update", "--check"}} {
		c := newCopy()
		before, _ := os.ReadFile(c.file("update.json"))
		got := c.run(list...)
		after, _ := os.ReadFile(c.file("update.json"))
		if len(c.spawned) > 0 || !bytes.Equal(before, after) || strings.Contains(got.errors, "0.2.9") || strings.Contains(got.errors, "0.3.1") {
			t.Errorf("%v: %+v, spawned %v, update.json %s", list, got, c.spawned, after)
		}
	}
	// The same copy does start a check and tell of the update after a command.
	c := newCopy()
	if notice := c.notice("status"); len(c.spawned) != 1 || !strings.Contains(notice, "0.2.9") {
		t.Errorf("status: notice %q, spawned %v", notice, c.spawned)
	}
}

// A notice is for a person who sees both output streams at a terminal. A
// coding agent can run string in a terminal too, and a notice there would
// land in what it reads.
func TestUpdateNoticesShowOnATerminalOnly(t *testing.T) {
	_, public := testKey(t)
	host := newUpdateHost(t)
	keys := []ed25519.PublicKey{public}

	t.Run("after the background updated this copy", func(t *testing.T) {
		setVersion(t, "0.3.1")
		c := newTestCopy(t, host, keys, ".local/bin")
		c.run("update", "--register")
		c.record(updateState{CheckedAt: c.now.Add(-time.Hour), Latest: "0.3.1", UpdatedFrom: "0.3.0", UpdatedTo: "0.3.1", UpdatedAt: c.now.Add(-time.Hour)})
		if notice := c.notice("status"); notice != "" {
			t.Errorf("with nobody at a terminal: %q", notice)
		}
		c.terminal = true
		if notice := c.notice("status", "--json"); notice != "" {
			t.Errorf("with --json: %q", notice)
		}
		c.agent = "claude-code"
		if notice := c.notice("status"); notice != "" {
			t.Errorf("under an agent: %q", notice)
		}
		c.agent, c.piped = "", true
		if notice := c.notice("status"); notice != "" {
			t.Errorf("with stdout piped: %q", notice)
		}
		c.piped = false
		if notice := c.notice("balance"); !strings.Contains(notice, "0.3.0") || !strings.Contains(notice, "0.3.1") {
			t.Errorf("on a terminal: %q", notice)
		}
		if notice := c.notice("status"); notice != "" {
			t.Errorf("a second time: %q", notice)
		}
	})

	t.Run("of a release for a copy Homebrew updates", func(t *testing.T) {
		setVersion(t, "0.3.0")
		c := newTestCopy(t, host, keys, homebrewDir)
		c.record(updateState{CheckedAt: c.now.Add(-time.Hour), Latest: "0.3.1"})
		if notice := c.notice("status"); notice != "" {
			t.Errorf("with nobody at a terminal: %q", notice)
		}
		c.terminal = true
		if notice := c.notice("status", "--json"); notice != "" {
			t.Errorf("with --json: %q", notice)
		}
		if notice := c.notice("balance"); !strings.Contains(notice, "0.3.1") || !strings.Contains(notice, managers[installHomebrew].command) {
			t.Errorf("on a terminal: %q", notice)
		}
		if notice := c.notice("status"); notice != "" {
			t.Errorf("again the same day: %q", notice)
		}
		c.now = c.now.Add(checkEvery)
		if notice := c.notice("status"); !strings.Contains(notice, "0.3.1") {
			t.Errorf("the next day: %q", notice)
		}
		if err := changeUpdateState(c.file("update.json"), func(s *updateState) { s.Latest = "0.3.2" }); err != nil {
			t.Fatal(err)
		}
		if notice := c.notice("status"); !strings.Contains(notice, "0.3.2") {
			t.Errorf("of a newer release the same day: %q", notice)
		}
	})

	for _, check := range []struct {
		name, dir, latest string
		env               map[string]string
		agent             string
		piped             bool
	}{
		// The next npx run fetches the new release anyway.
		{name: "from npx", dir: npxDir, latest: "0.3.1"},
		{name: "of a pre-release, to a stable build", dir: homebrewDir, latest: "0.4.0-rc.1"},
		{name: "in CI", dir: homebrewDir, latest: "0.3.1", env: map[string]string{"CI": "true"}},
		{name: "with STRING_AUTO_UPDATE=0", dir: homebrewDir, latest: "0.3.1", env: map[string]string{"STRING_AUTO_UPDATE": "0"}},
		{name: "under an agent at a terminal", dir: homebrewDir, latest: "0.3.1", agent: "claude-code"},
		{name: "with stdout piped", dir: homebrewDir, latest: "0.3.1", piped: true},
	} {
		t.Run("none "+check.name, func(t *testing.T) {
			setVersion(t, "0.3.0")
			c := newTestCopy(t, host, keys, check.dir)
			maps.Copy(c.env, check.env)
			c.terminal, c.agent, c.piped = true, check.agent, check.piped
			c.record(updateState{CheckedAt: c.now.Add(-time.Hour), Latest: check.latest})
			if notice := c.notice("status"); notice != "" {
				t.Errorf("%q", notice)
			}
		})
	}
	t.Run("none of an update with STRING_AUTO_UPDATE=0", func(t *testing.T) {
		setVersion(t, "0.3.1")
		c := newTestCopy(t, host, keys, ".local/bin")
		c.run("update", "--register")
		c.env["STRING_AUTO_UPDATE"] = "0"
		c.terminal = true
		c.record(updateState{CheckedAt: c.now.Add(-time.Hour), UpdatedFrom: "0.3.0", UpdatedTo: "0.3.1"})
		if notice := c.notice("status"); notice != "" {
			t.Errorf("%q", notice)
		}
	})
}

func TestUpdateBackgroundRecordsWhatItFoundAndDid(t *testing.T) {
	seed, public := testKey(t)
	otherSeed, _ := testKey(t)
	keys := []ed25519.PublicKey{public}

	t.Run("an update", func(t *testing.T) {
		setVersion(t, "0.3.0")
		host := newUpdateHost(t)
		host.point("0.3.1")
		host.add(t, goodRelease(t, "0.3.1", seed))
		c := newTestCopy(t, host, keys, ".local/bin")
		c.run("update", "--register")
		// What went wrong last time is cleared by a check that works.
		c.record(updateState{CheckedAt: c.now, Error: "GET /stable: HTTP 503"})
		if got := c.run("update", "--background"); got != (result{}) {
			t.Errorf("the background check printed %+v", got)
		}
		want := updateState{CheckedAt: c.now, Latest: "0.3.1", UpdatedFrom: "0.3.0", UpdatedTo: "0.3.1", UpdatedAt: c.now}
		if c.installed() != "string 0.3.1\n" || c.state() != want {
			t.Errorf("the binary prints %q, update.json is %+v", c.installed(), c.state())
		}
		if _, err := os.Stat(c.file("update.lock")); !os.IsNotExist(err) {
			t.Errorf("the lock is still there: %v", err)
		}
		if left := c.leftovers(); len(left) > 0 {
			t.Errorf("the update left %v behind", left)
		}
	})

	t.Run("a release it would not install", func(t *testing.T) {
		setVersion(t, "0.3.0")
		host := newUpdateHost(t)
		host.point("0.3.1")
		bad := goodRelease(t, "0.3.1", seed)
		bad.seed = otherSeed
		host.add(t, bad)
		c := newTestCopy(t, host, keys, ".local/bin")
		c.run("update", "--register")
		c.run("update", "--background")
		if state := c.state(); c.installed() != "string 0.3.0\n" || state.Latest != "0.3.1" || !strings.Contains(state.Error, "manifest.json") || state.UpdatedTo != "" {
			t.Errorf("the binary prints %q, update.json is %+v", c.installed(), state)
		}
		check := decode(t, c.run("update", "--check", "--json").output).(map[string]any)
		if check["error"] != c.state().Error {
			t.Errorf("string update --check does not show the error: %v", check)
		}
	})

	t.Run("a download host that fails", func(t *testing.T) {
		setVersion(t, "0.3.0")
		host := newUpdateHost(t)
		c := newTestCopy(t, host, keys, ".local/bin")
		c.run("update", "--register")
		c.run("update", "--background")
		if state := c.state(); !strings.Contains(state.Error, "404") || state.Latest != "" {
			t.Errorf("update.json is %+v", state)
		}
	})

	t.Run("a copy Homebrew updates", func(t *testing.T) {
		setVersion(t, "0.3.0")
		host := newUpdateHost(t)
		host.point("0.3.1")
		host.add(t, goodRelease(t, "0.3.1", seed))
		c := newTestCopy(t, host, keys, homebrewDir)
		c.run("update", "--background")
		if state := c.state(); c.installed() != "string 0.3.0\n" || state != (updateState{Latest: "0.3.1"}) || !slices.Equal(host.requested(), []string{"/stable"}) {
			t.Errorf("the binary prints %q, update.json is %+v, requests %v", c.installed(), state, host.requested())
		}
	})

	t.Run("a pre-release on /stable, to a stable build", func(t *testing.T) {
		setVersion(t, "0.3.0")
		host := newUpdateHost(t)
		host.point("0.4.0-rc.1")
		host.add(t, goodRelease(t, "0.4.0-rc.1", seed))
		c := newTestCopy(t, host, keys, ".local/bin")
		c.run("update", "--register")
		c.run("update", "--background")
		if state := c.state(); c.installed() != "string 0.3.0\n" || state.Error != "" || state.UpdatedTo != "" || !slices.Equal(host.requested(), []string{"/stable"}) {
			t.Errorf("the binary prints %q, update.json is %+v, requests %v", c.installed(), state, host.requested())
		}
	})

	t.Run("switched off", func(t *testing.T) {
		setVersion(t, "0.3.0")
		host := newUpdateHost(t)
		host.point("0.3.1")
		host.add(t, goodRelease(t, "0.3.1", seed))
		c := newTestCopy(t, host, keys, ".local/bin")
		c.run("update", "--register")
		c.env["CI"] = "true"
		c.run("update", "--background")
		if _, err := os.Stat(c.file("update.json")); c.installed() != "string 0.3.0\n" || len(host.requested()) > 0 || !os.IsNotExist(err) {
			t.Errorf("the binary prints %q, requests %v, update.json: %v", c.installed(), host.requested(), err)
		}
	})
}
