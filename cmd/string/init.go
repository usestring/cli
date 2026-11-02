package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/usestring/cli/skills"
)

// agent is a coding agent that loads skills from a directory under the
// user's home. The skill goes to an agent only when its config directory
// exists, unless --agent names it.
type agent struct {
	name   string
	label  string
	config func(home string, env func(string) (string, bool)) string
}

func under(parts ...string) func(string, func(string) (string, bool)) string {
	return func(home string, _ func(string) (string, bool)) string {
		return filepath.Join(append([]string{home}, parts...)...)
	}
}

func underConfig(name string) func(string, func(string) (string, bool)) string {
	return func(home string, env func(string) (string, bool)) string {
		if dir, ok := env("XDG_CONFIG_HOME"); ok && dir != "" {
			return filepath.Join(dir, name)
		}
		return filepath.Join(home, ".config", name)
	}
}

// sharedAgent is ~/.agents/skills, which Codex and several other agents
// read. It is always installed, so every agent that reads it has the skill.
var sharedAgent = agent{"codex", "Codex and agents that read ~/.agents/skills", under(".agents")}

var agents = []agent{
	{"claude-code", "Claude Code", func(home string, env func(string) (string, bool)) string {
		if dir, ok := env("CLAUDE_CONFIG_DIR"); ok && dir != "" {
			return dir
		}
		return filepath.Join(home, ".claude")
	}},
	{"cursor", "Cursor", under(".cursor")},
	{"gemini-cli", "Gemini CLI", under(".gemini")},
	{"opencode", "OpenCode", underConfig("opencode")},
	{"github-copilot", "GitHub Copilot", under(".copilot")},
	{"windsurf", "Windsurf", under(".codeium", "windsurf")},
	{"goose", "Goose", underConfig("goose")},
	{"droid", "Droid", under(".factory")},
	{"kiro-cli", "Kiro CLI", under(".kiro")},
	{"qwen-code", "Qwen Code", under(".qwen")},
	{"grok", "Grok Build", under(".grok")},
	{"openclaw", "OpenClaw", under(".openclaw")},
	{"hermes-agent", "Hermes Agent", under(".hermes")},
}

func homeDir(env func(string) (string, bool)) (string, error) {
	for _, name := range []string{"HOME", "USERPROFILE"} {
		if dir, ok := env(name); ok && dir != "" {
			return dir, nil
		}
	}
	return "", errors.New("Cannot find your home directory; set HOME")
}

// targets picks where the skills go: the shared directory and every agent
// found on this machine, or exactly the agents --agent names.
func targets(names string, home string, env func(string) (string, bool)) ([]agent, error) {
	all := append([]agent{sharedAgent}, agents...)
	if names == "" {
		chosen := []agent{sharedAgent}
		for _, a := range agents {
			if info, err := os.Stat(a.config(home, env)); err == nil && info.IsDir() {
				chosen = append(chosen, a)
			}
		}
		return chosen, nil
	}
	var chosen []agent
	for name := range strings.SplitSeq(names, ",") {
		i := slices.IndexFunc(all, func(a agent) bool { return a.name == strings.TrimSpace(name) })
		if i < 0 {
			valid := make([]string, len(all))
			for j, a := range all {
				valid[j] = a.name
			}
			return nil, fmt.Errorf("Unknown agent: %s. Use one of: %s", name, strings.Join(valid, ", "))
		}
		if !slices.ContainsFunc(chosen, func(a agent) bool { return a.name == all[i].name }) {
			chosen = append(chosen, all[i])
		}
	}
	return chosen, nil
}

func initCommand(list []string, rt runtime) error {
	parsed, err := parseArgs(list, "agent")
	if err != nil {
		return err
	}
	if parsed.help {
		return write(rt.stdout, []byte(help))
	}
	if err := expectPositionals(parsed.positionals, 0, "string init [--agent NAME[,NAME...]]"); err != nil {
		return err
	}
	if _, ok := parsed.value("input"); ok {
		return errors.New("--input does not apply to init")
	}
	home, err := homeDir(rt.env)
	if err != nil {
		return err
	}
	names, _ := parsed.value("agent")
	chosen, err := targets(names, home, rt.env)
	if err != nil {
		return err
	}
	var report strings.Builder
	for _, a := range chosen {
		dir := filepath.Join(a.config(home, rt.env), "skills")
		if err := installSkills(dir); err != nil {
			return err
		}
		fmt.Fprintf(&report, "Installed the string-cli skill for %s in %s\n", a.label, filepath.Join(dir, "string-cli"))
	}
	if _, err := rt.lookPath("string"); err != nil {
		report.WriteString("\nAgents run the skill's commands as `string`, which is not on your PATH. Install it with:\n  npm install -g @usestring/cli\n")
	}
	if _, err := apiKey(rt.env); err != nil {
		report.WriteString("\nRun string login to sign in, or set STRING_API_KEY to an API key from https://portal.usestring.ai/settings.\n")
	}
	return write(rt.stdout, []byte(report.String()))
}

// installSkills copies every embedded skill into dir, replacing an older copy.
func installSkills(dir string) error {
	return fs.WalkDir(skills.FS, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == "." {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(skills.FS, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
