package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/usestring/cli/skills"
)

// initAt runs string init with args in home and returns what it printed. A
// key is set, so init signs nobody in, and string is on PATH.
func initAt(t *testing.T, home string, env map[string]string, args ...string) result {
	t.Helper()
	vars := map[string]string{"HOME": home, "STRING_API_KEY": "test-key"}
	for name, value := range env {
		vars[name] = value
	}
	var stdout, stderr bytes.Buffer
	status := run(append([]string{"init", "--no-mcp"}, args...), runtime{
		env:      envOf(vars),
		lookPath: func(name string) (string, error) { return "/opt/bin/" + name, nil },
		stdout:   &stdout,
		stderr:   &stderr,
	})
	return result{status, stdout.String(), stderr.String()}
}

// put writes each file under home, making the directories it needs.
func put(t *testing.T, home string, files map[string]string) {
	t.Helper()
	for path, contents := range files {
		full := filepath.Join(home, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// olderSkill is a SKILL.md as a release before 0.3 installed it.
const olderSkill = "---\nname: string-cli\ndescription: |\n  Search the web.\n---\n\n# String CLI\n"

func embeddedSkill(t *testing.T) string {
	t.Helper()
	data, err := fs.ReadFile(skills.FS, "string-cli/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func there(home, path string) bool {
	_, err := os.Lstat(filepath.Join(home, filepath.FromSlash(path)))
	return err == nil
}

// init puts the skill in ~/.agents/skills, which most agents read, and in
// ~/.claude/skills for Claude Code. An agent that reads neither gets its own
// copy, and the copy an older init put in ~/.gemini/skills goes, since Gemini
// CLI warns of a conflict while it finds two.
func TestInitPutsTheSkillWhereEachAgentReadsIt(t *testing.T) {
	home := t.TempDir()
	put(t, home, map[string]string{
		".claude/settings.json":              "{}",
		".cursor/mcp.json":                   "{}",
		".gemini/skills/string-cli/SKILL.md": olderSkill,
		".gemini/skills/mine/SKILL.md":       "---\nname: mine\n---\n",
		".codeium/windsurf/mcp_config.json":  "{}",
	})
	skill := func(dir string) string { return filepath.Join(home, dir, "skills", "string-cli") }
	want := "Installed the string-cli skill for Codex and agents that read ~/.agents/skills in " + skill(".agents") + "\n" +
		"Cursor and Gemini CLI read it there too.\n" +
		"Installed the string-cli skill for Claude Code in " + skill(".claude") + "\n" +
		"Installed the string-cli skill for Windsurf in " + skill(filepath.Join(".codeium", "windsurf")) + "\n" +
		"Removed the older copy in " + skill(".gemini") + ", since Gemini CLI reads ~/.agents/skills and warns of a skill conflict when it finds two. For a Gemini CLI too old to read ~/.agents/skills, string init --agent gemini-cli puts it back.\n"
	if got := initAt(t, home, nil); got.status != 0 || got.output != want || got.errors != "" {
		t.Errorf("exit %d, stderr %q, and stdout:\n%s\nwant:\n%s", got.status, got.errors, got.output, want)
	}
	for _, dir := range []string{".agents", ".claude", filepath.Join(".codeium", "windsurf")} {
		if got := skillAt(filepath.Join(skill(dir), "SKILL.md")); got != embeddedSkill(t) {
			t.Errorf("%s holds %.30q, want this release's skill", dir, got)
		}
	}
	for path, want := range map[string]bool{".cursor/skills": false, ".gemini/skills/string-cli": false, ".gemini/skills/mine/SKILL.md": true} {
		if there(home, path) != want {
			t.Errorf("%s there: %v, want %v", path, !want, want)
		}
	}

	// A second run finds nothing more to remove.
	if got := initAt(t, home, nil); strings.Contains(got.output, "gemini/skills") {
		t.Errorf("the second run printed:\n%s", got.output)
	}
}

// Only a plain copy of the skill as init writes it goes from ~/.gemini/skills,
// and only when init runs for every agent.
func TestInitLeavesAGeminiCopyItDidntWrite(t *testing.T) {
	dir := filepath.Join(".gemini", "skills", "string-cli")
	left := func(home string) string {
		return "Left " + filepath.Join(home, dir) + " as it is: it isn't a plain copy from string init, so it may be yours. Gemini CLI warns of a skill conflict while it and ~/.agents/skills/string-cli are both there.\n"
	}
	t.Run("with a file of its own", func(t *testing.T) {
		home := t.TempDir()
		put(t, home, map[string]string{".gemini/skills/string-cli/SKILL.md": olderSkill, ".gemini/skills/string-cli/notes.md": "mine"})
		want := "Installed the string-cli skill for Codex and agents that read ~/.agents/skills in " + filepath.Join(home, ".agents", "skills", "string-cli") + "\n" +
			"Gemini CLI reads it there too.\n" + left(home)
		if got := initAt(t, home, nil); got.output != want {
			t.Errorf("stdout:\n%s\nwant:\n%s", got.output, want)
		}
		if skillAt(filepath.Join(home, dir, "notes.md")) != "mine" || skillAt(filepath.Join(home, dir, "SKILL.md")) != olderSkill {
			t.Errorf("init changed the copy")
		}
	})
	t.Run("named as another skill", func(t *testing.T) {
		home := t.TempDir()
		put(t, home, map[string]string{".gemini/skills/string-cli/SKILL.md": strings.Replace(olderSkill, "name: string-cli", "name: my-string", 1)})
		if got := initAt(t, home, nil); !strings.HasSuffix(got.output, left(home)) || !there(home, ".gemini/skills/string-cli/SKILL.md") {
			t.Errorf("stdout:\n%s", got.output)
		}
	})
	t.Run("as a link", func(t *testing.T) {
		home := t.TempDir()
		put(t, home, map[string]string{"mine/string-cli/SKILL.md": olderSkill, ".gemini/skills/other/SKILL.md": "x"})
		if err := os.Symlink(filepath.Join(home, "mine", "string-cli"), filepath.Join(home, dir)); err != nil {
			t.Skipf("no links here: %v", err)
		}
		if got := initAt(t, home, nil); !strings.HasSuffix(got.output, left(home)) || !there(home, ".gemini/skills/string-cli/SKILL.md") || !there(home, "mine/string-cli/SKILL.md") {
			t.Errorf("stdout:\n%s", got.output)
		}
	})
	t.Run("with no skill in it", func(t *testing.T) {
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := initAt(t, home, nil); strings.Contains(got.output, "gemini/skills") || !there(home, ".gemini/skills/string-cli") {
			t.Errorf("stdout:\n%s", got.output)
		}
	})
	// --agent names exactly where the skill goes, and init touches nothing else.
	t.Run("with --agent codex", func(t *testing.T) {
		home := t.TempDir()
		put(t, home, map[string]string{".gemini/skills/string-cli/SKILL.md": olderSkill})
		want := "Installed the string-cli skill for Codex and agents that read ~/.agents/skills in " + filepath.Join(home, ".agents", "skills", "string-cli") + "\n"
		if got := initAt(t, home, nil, "--agent", "codex"); got.output != want || skillAt(filepath.Join(home, dir, "SKILL.md")) != olderSkill {
			t.Errorf("stdout:\n%s\nwant:\n%s", got.output, want)
		}
	})
	t.Run("with --agent gemini-cli,cursor", func(t *testing.T) {
		home := t.TempDir()
		put(t, home, map[string]string{".gemini/skills/string-cli/SKILL.md": olderSkill})
		want := "Installed the string-cli skill for Gemini CLI in " + filepath.Join(home, dir) + "\n" +
			"Installed the string-cli skill for Cursor in " + filepath.Join(home, ".cursor", "skills", "string-cli") + "\n"
		got := initAt(t, home, nil, "--agent", "gemini-cli,cursor")
		if got.output != want || skillAt(filepath.Join(home, dir, "SKILL.md")) != embeddedSkill(t) || there(home, ".agents") {
			t.Errorf("stdout:\n%s\nwant:\n%s", got.output, want)
		}
	})
}

// string skill prints the skill this release carries, and --path the copies
// installed in the agents' skills directories.
func TestSkillPrintsTheGuideAndWhereItIsInstalled(t *testing.T) {
	if got := runOffline(t, "skill"); got.status != 0 || got.output != embeddedSkill(t) || got.errors != "" {
		t.Errorf("string skill: exit %d, stderr %q, stdout %.40q", got.status, got.errors, got.output)
	}

	home := t.TempDir()
	skillPath := func(home string) result {
		var stdout, stderr bytes.Buffer
		status := run([]string{"skill", "--path"}, runtime{env: envOf(map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": filepath.Join(home, ".agents")}), stdout: &stdout, stderr: &stderr})
		return result{status, stdout.String(), stderr.String()}
	}
	if got := skillPath(home); got.status != 0 || got.output != "" || got.errors != "The string-cli skill isn't installed for any agent. string init installs it, and string skill prints it.\n" {
		t.Errorf("with no copy: %+v", got)
	}
	if got := runOffline(t, "skill", "--path", "-q"); got.status != 0 || got.output != "" || got.errors != "" {
		t.Errorf("with no copy and -q: %+v", got)
	}
	// Claude Code's settings in ~/.agents, so both read one directory, which
	// is listed once, and a copy an older init left for Cursor.
	put(t, home, map[string]string{
		".agents/skills/string-cli/SKILL.md": olderSkill,
		".cursor/skills/string-cli/SKILL.md": olderSkill,
		".copilot/skills/string-cli/x.md":    "no SKILL.md",
	})
	want := filepath.Join(home, ".agents", "skills", "string-cli", "SKILL.md") + "\n" + filepath.Join(home, ".cursor", "skills", "string-cli", "SKILL.md") + "\n"
	if got := skillPath(home); got.status != 0 || got.output != want || got.errors != "" {
		t.Errorf("stdout:\n%s\nwant:\n%s", got.output, want)
	}

	for _, list := range [][]string{{"skill", "string-cli"}, {"skill", "--paths"}} {
		if got := runOffline(t, list...); got.status != exitUsage || got.output != "" {
			t.Errorf("string %s: %+v", strings.Join(list, " "), got)
		}
	}
	if got := runOffline(t, "skills"); got.status != exitUsage || !strings.Contains(got.errors, "string skill") {
		t.Errorf("string skills: %+v", got)
	}
}

// The skill names only commands and flags that string has, and stays short
// enough to load into every session that uses it.
func TestTheSkillTeachesOnlyWhatStringHas(t *testing.T) {
	skill := embeddedSkill(t)
	if lines := strings.Count(skill, "\n"); lines > 150 {
		t.Errorf("the skill has %d lines, want 150 or fewer", lines)
	}
	front, _, _ := strings.Cut(strings.TrimPrefix(skill, "---\n"), "\n---\n")
	description, _ := strings.CutPrefix(front[strings.Index(front, "description:"):], "description: |\n")
	if !strings.HasPrefix(front, "name: string-cli\n") || len(description) > 1024 {
		t.Errorf("the frontmatter is\n%s", front)
	}

	// `string WORD`, in a code span or at the start of a line in a code block.
	for _, match := range regexp.MustCompile("(?m)(?:`|^)string ([a-z][a-z-]*)").FindAllStringSubmatch(skill, -1) {
		// string dev is what --version prints for a build from source.
		if !slices.Contains(commands, match[1]) && match[1] != "dev" {
			t.Errorf("the skill runs %q, which is no command", match[0])
		}
	}

	documented := help
	for name, doc := range commandDocs {
		documented += doc.page(name)
	}
	for _, topic := range helpTopics {
		documented += topic
	}
	flag := regexp.MustCompile(`--[a-zA-Z][a-zA-Z-]*`)
	known := flag.FindAllString(documented, -1)
	for _, used := range flag.FindAllString(skill, -1) {
		// --no-init is install.sh's, and --post is a field of reddit post.
		if !slices.Contains(known, used) && used != "--no-init" && used != "--post" {
			t.Errorf("the skill uses %s, which no help page has", used)
		}
	}
}

// String's hosted onboarding guide runs string login --browser --no-wait,
// with --env-file, then string login --poll, but only after it finds
// --browser in string login --help.
func TestLoginHelpKeepsTheFlagsTheHostedGuideLooksFor(t *testing.T) {
	page := runOffline(t, "login", "--help").output
	for _, flag := range []string{"--browser ", "--no-wait ", "--env-file ", "--poll "} {
		if !strings.Contains(page, "\n  "+flag) {
			t.Errorf("string login --help has no line for %s:\n%s", flag, page)
		}
	}
}
