package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/usestring/cli/skills"
)

// String's hosted MCP server. Claude Code and Codex sign in to it with
// String, so init can add it to them without putting a key in their settings.
const (
	mcpName = "string-web-access"
	mcpHost = "mcp.usestring.ai"
	mcpURL  = "https://" + mcpHost + "/v3/mcp"
	mcpDocs = "https://portal.usestring.ai/docs/mcp/remote"
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
	parsed, err := parse(list, spec{options: []string{"agent"}, switches: []string{"no-mcp"}})
	if err != nil {
		return usage(err)
	}
	if parsed.help {
		return write(rt.stdout, []byte(help))
	}
	if err := expectPositionals(parsed.positionals, 0, "string init [--agent NAME[,NAME...]] [--no-mcp]"); err != nil {
		return usage(err)
	}
	if _, ok := parsed.value("input"); ok {
		return usage(errors.New("--input does not apply to init"))
	}
	home, err := homeDir(rt.env)
	if err != nil {
		return err
	}
	names, _ := parsed.value("agent")
	chosen, err := targets(names, home, rt.env)
	if err != nil {
		return usage(err)
	}
	var report strings.Builder
	for _, a := range chosen {
		dir := filepath.Join(a.config(home, rt.env), "skills")
		if err := installSkills(dir); err != nil {
			return err
		}
		fmt.Fprintf(&report, "Installed the string-cli skill for %s in %s\n", a.label, filepath.Join(dir, "string-cli"))
	}
	if !parsed.switches["no-mcp"] {
		addMCP(&report, chosen, home, rt)
	}
	if _, err := rt.lookPath("string"); err != nil {
		report.WriteString("\nAgents run the skill's commands as `string`, which is not on your PATH. Install it with:\n  npm install -g @usestring/cli\n")
	}
	if key, err := apiKey(rt.env); err == nil && key != "" {
		return write(rt.stdout, []byte(report.String()))
	}
	if rt.terminal && oauthClientID != "" {
		report.WriteString("\nSigning in.\n")
		if err := write(rt.stdout, []byte(report.String())); err != nil {
			return err
		}
		return signIn(rt)
	}
	report.WriteString("\nstring fetch and string search work now, without a key, within a daily allowance. For more, run string login, or set STRING_API_KEY to an API key from https://portal.usestring.ai/settings.\n")
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

// addMCP adds String's MCP server to Claude Code and Codex when they are among
// the chosen agents and lack it. The other agents get the setup page instead:
// most connect with an API key, which init never copies into another
// program's settings, and OpenCode signs in only from version 2.
func addMCP(report *strings.Builder, chosen []agent, home string, rt runtime) {
	var lines, others []string
	for _, a := range chosen {
		switch a.name {
		case "claude-code":
			lines = append(lines, addClaudeMCP(home, rt))
		case sharedAgent.name:
			dir := filepath.Join(home, ".codex")
			if value, ok := rt.env("CODEX_HOME"); ok && value != "" {
				dir = value
			}
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				lines = append(lines, addCodexMCP(dir))
			}
		default:
			others = append(others, a.label)
		}
	}
	if len(others) > 0 {
		lines = append(lines, fmt.Sprintf("To add String's MCP server to %s, see %s", strings.Join(others, ", "), mcpDocs))
	}
	if len(lines) > 0 {
		fmt.Fprintf(report, "\n%s\n", strings.Join(lines, "\n"))
	}
}

// addClaudeMCP adds the server through Claude Code's own command, which keeps
// its settings file consistent while Claude Code may be running.
func addClaudeMCP(home string, rt runtime) string {
	settings := filepath.Join(home, ".claude.json")
	if dir, ok := rt.env("CLAUDE_CONFIG_DIR"); ok && dir != "" {
		settings = filepath.Join(dir, ".claude.json")
	}
	if claudeHasMCP(settings) {
		return "Claude Code already has String's MCP server."
	}
	add := []string{"mcp", "add", "--transport", "http", "--scope", "user", mcpName, mcpURL}
	claude, err := rt.lookPath("claude")
	if err != nil {
		return "Add String's MCP server to Claude Code with: claude " + strings.Join(add, " ")
	}
	if output, err := rt.program(claude, add...); err != nil {
		reason, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
		if reason == "" {
			reason = err.Error()
		}
		return "Could not add String's MCP server to Claude Code: " + reason
	}
	return "Added String's MCP server to Claude Code. Sign in to it with: claude mcp login " + mcpName
}

// claudeHasMCP reports whether Claude Code's user settings already have a
// server by this name or at String's MCP host.
func claudeHasMCP(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var settings struct {
		MCPServers map[string]struct {
			URL string `json:"url"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(data, &settings) != nil {
		return false
	}
	for name, server := range settings.MCPServers {
		if name == mcpName || strings.Contains(server.URL, mcpHost) {
			return true
		}
	}
	return false
}

// codexInlineServers matches mcp_servers written as an inline table, which
// TOML forbids extending with an [mcp_servers.NAME] table: appending one would
// leave Codex unable to load its config.
var codexInlineServers = regexp.MustCompile(`(?m)^\s*["']?mcp_servers["']?\s*=`)

// addCodexMCP appends the server to Codex's config.toml. codex mcp add would
// also start a sign-in and wait for it, holding init until someone finishes it.
func addCodexMCP(dir string) string {
	path := filepath.Join(dir, "config.toml")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "Could not add String's MCP server to Codex: " + err.Error()
	}
	config := string(data)
	switch {
	case strings.Contains(config, mcpName) || strings.Contains(config, mcpHost):
		return "Codex already has String's MCP server."
	case codexInlineServers.MatchString(config):
		return "Add String's MCP server to Codex with: codex mcp add " + mcpName + " --url " + mcpURL
	}
	entry := "[mcp_servers." + mcpName + "]\nurl = \"" + mcpURL + "\"\n"
	if config != "" {
		entry = "\n" + entry
		if !strings.HasSuffix(config, "\n") {
			entry = "\n" + entry
		}
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err == nil {
		_, err = file.WriteString(entry)
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		return "Could not add String's MCP server to Codex: " + err.Error()
	}
	return fmt.Sprintf("Added String's MCP server to Codex in %s. Sign in to it with: codex mcp login %s", path, mcpName)
}
