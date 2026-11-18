package main

import (
	"crypto/ed25519"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/usestring/cli/skills"
)

// skillMachine is a machine where an older release's string init installed
// the skill for Codex's shared directory and for Claude Code, and where Cursor
// is installed without it.
type skillMachine struct {
	c                      *testCopy
	shared, claude, cursor string
}

const oldSkill = "the skill of 0.3.0\n"

func newSkillMachine(t *testing.T, v string) skillMachine {
	t.Helper()
	setVersion(t, v)
	_, public := testKey(t)
	c := newTestCopy(t, newUpdateHost(t), []ed25519.PublicKey{public}, ".local/bin")
	// No check is due, so the check does not muddle what the refresh did.
	c.record(updateState{CheckedAt: c.now, SeenVersion: "0.3.0"})
	m := skillMachine{
		c:      c,
		shared: filepath.Join(c.home, ".agents", "skills", "string-cli", "SKILL.md"),
		claude: filepath.Join(c.home, ".claude", "skills", "string-cli", "SKILL.md"),
		cursor: filepath.Join(c.home, ".cursor"),
	}
	for _, path := range []string{m.shared, m.claude} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(oldSkill), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(m.cursor, 0o755); err != nil {
		t.Fatal(err)
	}
	return m
}

func skillAt(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

func TestUpdateRefreshesTheSkillWhereInitInstalledIt(t *testing.T) {
	embedded, err := fs.ReadFile(skills.FS, "string-cli/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("on the first command of a newer release", func(t *testing.T) {
		m := newSkillMachine(t, "0.3.1")
		if got := m.c.run("status"); got != m.c.runAlone("status") {
			t.Errorf("the refresh changed how the command ended: %+v", got)
		}
		if skillAt(m.shared) != string(embedded) || skillAt(m.claude) != string(embedded) || m.c.state().SeenVersion != "0.3.1" {
			t.Errorf("the skill is %.30q for Codex and %.30q for Claude Code, and update.json %+v", skillAt(m.shared), skillAt(m.claude), m.c.state())
		}
		if _, err := os.Stat(filepath.Join(m.cursor, "skills")); !os.IsNotExist(err) {
			t.Errorf("the refresh installed the skill for Cursor: %v", err)
		}
		// Once: a skill changed after that stays as it is.
		if err := os.WriteFile(m.claude, []byte("edited\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		m.c.run("status")
		if skillAt(m.claude) != "edited\n" {
			t.Errorf("the skill was rewritten again: %.30q", skillAt(m.claude))
		}
	})

	t.Run("with no record of the release that last rewrote it", func(t *testing.T) {
		m := newSkillMachine(t, "0.3.1")
		m.c.record(updateState{CheckedAt: m.c.now})
		m.c.run("status")
		if skillAt(m.shared) != string(embedded) || m.c.state().SeenVersion != "0.3.1" {
			t.Errorf("the skill is %.30q, and update.json %+v", skillAt(m.shared), m.c.state())
		}
	})

	for _, check := range []struct {
		name, version string
		list          []string
		env           map[string]string
	}{
		// Otherwise two copies of string would write their skills back and forth.
		{name: "from an older release", version: "0.2.9", list: []string{"status"}},
		{name: "from a probe", version: "0.3.1", list: []string{"--version"}},
		{name: "from a development build", version: "dev", list: []string{"status"}},
		{name: "with STRING_AUTO_UPDATE=0", version: "0.3.1", list: []string{"status"}, env: map[string]string{"STRING_AUTO_UPDATE": "0"}},
	} {
		t.Run("never "+check.name, func(t *testing.T) {
			m := newSkillMachine(t, check.version)
			maps.Copy(m.c.env, check.env)
			m.c.run(check.list...)
			if skillAt(m.shared) != oldSkill || skillAt(m.claude) != oldSkill || m.c.state().SeenVersion != "0.3.0" {
				t.Errorf("the skill is %.30q, and update.json %+v", skillAt(m.shared), m.c.state())
			}
		})
	}

	t.Run("never where the release cannot be recorded", func(t *testing.T) {
		m := newSkillMachine(t, "0.3.1")
		if err := os.Remove(m.c.file("update.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(m.c.file("update.json"), 0o700); err != nil {
			t.Fatal(err)
		}
		m.c.run("status")
		if skillAt(m.shared) != oldSkill {
			t.Errorf("the skill is %.30q", skillAt(m.shared))
		}
	})
}
