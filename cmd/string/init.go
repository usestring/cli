package main

import (
	"bytes"
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
// ?login is what asks for that sign-in: with no credential, the bare address
// serves keyless fetch and search and never asks.
const (
	// mcpName is the name String's docs give the server, so the commands they
	// show work on an entry init wrote. init never renames an entry, so a
	// later release cannot change the name for anyone who already has one.
	mcpName = "string"
	mcpHost = "mcp.usestring.ai"
	mcpURL  = "https://" + mcpHost + "?login"
	// mcpURLArg is mcpURL in a command printed for a person to paste, quoted
	// because zsh reads its ? as a glob and refuses the command. The quotes
	// are double because cmd.exe passes single ones on as part of the URL,
	// while PowerShell, bash, zsh and fish take double quotes too.
	mcpURLArg = `"` + mcpURL + `"`
	mcpDocs   = "https://docs.usestring.ai/mcp/remote"
	// mcpNameTaken is init's line for an agent whose server named mcpName is
	// not at String's host, as when String's self-hosted server was set up
	// under the name its docs give. Neither agent keeps two servers under one
	// name, and init never replaces an entry.
	mcpNameTaken = "%s already has an MCP server named " + mcpName + ", so String's hosted server was not added."
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
	parsed, err := parse(list, spec{options: []string{"agent"}, switches: []string{"no-mcp", "no-login"}})
	if err != nil {
		return usage(err)
	}
	if parsed.help {
		return write(rt.stdout, []byte(help))
	}
	if err := expectPositionals(parsed.positionals, 0, "string init [--agent NAME[,NAME...]] [--no-mcp] [--no-login]"); err != nil {
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
		report.WriteString("\nAgents run the skill's commands as `string`, which is not on your PATH. " + pathHint(rt) + "\n")
	}
	if key, err := apiKey(rt.env); err == nil && key != "" {
		return write(rt.stdout, []byte(report.String()))
	}
	// A person who pipes install.sh into sh reads stderr at a terminal, and starts keyless.
	captured := !rt.progress && !inCI(rt.env)
	if oauthClientID != "" && !parsed.switches["no-login"] && (rt.terminal || captured) {
		var err error
		if rt.terminal {
			report.WriteString("\nSigning in. string fetch and string search already work without a key, within a daily allowance, so you can press Ctrl-C to skip this and run string login later.\n")
			if err := write(rt.stdout, []byte(report.String())); err != nil {
				return err
			}
			report.Reset()
			err = signIn(rt, "")
		} else {
			// Nobody approves while init waits under an agent, so init hands
			// back the link and code, as string login does there, and
			// string login --poll saves the key. init prints text only.
			var started bytes.Buffer
			text := rt
			text.json, text.stdout = false, &started
			if _, err = startSignIn(text, false, ""); err == nil {
				fmt.Fprintf(&report, "\nSigning in. string fetch and string search already work without a key, within a daily allowance. For more:\n%s", started.Bytes())
				return write(rt.stdout, []byte(report.String()))
			}
			report.WriteString("\n")
		}
		if err == nil {
			return nil
		}
		// The skill and MCP steps are done, so init reports a sign-in that
		// ended without a key and still succeeds.
		refused := asFailure(err)
		why, _, _ := strings.Cut(refused.message, "\n")
		switch refused.code {
		case "login_expired":
			why = "the code expired"
		case "login_denied":
			why = "it was denied"
		}
		fmt.Fprintf(&report, "Sign-in did not finish: %s. Run string login to sign in later.\n", strings.TrimSuffix(why, "."))
	}
	report.WriteString("\nstring fetch and string search work now, without a key, within a daily allowance. For more, run string login, or set STRING_API_KEY to an API key from https://portal.usestring.ai/api-keys.\n")
	return write(rt.stdout, []byte(report.String()))
}

// inCI reports a CI job, where nobody is there to approve a sign-in that init
// would start.
func inCI(env func(string) (string, bool)) bool {
	value, _ := env("CI")
	return value != "" && value != "false" && value != "0"
}

// pathHint is how to put the running copy of string on PATH. Only a copy that
// npm installed is sent to npm. npm may not have this release, since its
// publish step can run later or not at all, and the package there is then an
// older CLI that would take the name the skill calls.
func pathHint(rt runtime) string {
	path, err := rt.binary()
	if err != nil {
		return "Add this copy's directory to your PATH, or move the binary into a directory that is already on it."
	}
	// A path element, whichever separator the system uses.
	if strings.Contains(strings.ReplaceAll(path, `\`, "/"), "/node_modules/") {
		return "Install it with:\n  npm install -g @usestring/cli"
	}
	return "This copy is at:\n  " + path + "\nAdd its directory to your PATH, or move the binary into a directory that is already on it."
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
	if line := alreadyThere("Claude Code", claudeServers(settings)); line != "" {
		return line
	}
	add := []string{"mcp", "add", "--transport", "http", "--scope", "user", mcpName, mcpURL}
	claude, err := rt.lookPath("claude")
	if err != nil {
		return "Add String's MCP server to Claude Code with: claude " + strings.Join(add[:len(add)-1], " ") + " " + mcpURLArg
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

// alreadyThere is init's line for an agent it adds nothing to, or "" when it
// can add the server. servers maps the name of each MCP server the agent has
// to the text of its entry. A server at String's host is String's under
// whatever name it has, and init says that name: it is the one the agent's
// sign-in command takes.
func alreadyThere(label string, servers map[string]string) string {
	var hosted string
	for name, entry := range servers {
		// The first by name, so two such servers give the same line on every run.
		if strings.Contains(entry, mcpHost) && (hosted == "" || name < hosted) {
			hosted = name
		}
	}
	if hosted != "" {
		return fmt.Sprintf("%s already has String's MCP server, as %s.", label, hosted)
	}
	if _, taken := servers[mcpName]; taken {
		return fmt.Sprintf(mcpNameTaken, label)
	}
	return ""
}

// claudeServers reads the MCP servers in Claude Code's user settings: each
// server's name and its URL, which a server run as a local program lacks.
func claudeServers(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var settings struct {
		MCPServers map[string]struct {
			URL string `json:"url"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(data, &settings) != nil {
		return nil
	}
	servers := map[string]string{}
	for name, server := range settings.MCPServers {
		servers[name] = server.URL
	}
	return servers
}

// codexInlineServers matches mcp_servers written as an inline table, which
// TOML forbids extending with an [mcp_servers.NAME] table: appending one would
// leave Codex unable to load its config.
var codexInlineServers = regexp.MustCompile(`(?m)^\s*["']?mcp_servers["']?\s*=`)

// codexInlineName matches mcpName where an inline table has its keys. The
// servers of an inline mcp_servers are not read one by one, so any such key
// counts as the name being taken.
var codexInlineName = regexp.MustCompile(`[{,]\s*["']?` + regexp.QuoteMeta(mcpName) + `["']?\s*[.=]`)

// tomlKey matches one part of a TOML key, bare or quoted, and the spaces TOML
// allows around it.
var tomlKey = regexp.MustCompile(`^[ \t]*(?:"([^"]*)"|'([^']*)'|([A-Za-z0-9_-]+))[ \t]*`)

// tomlPath reads the dotted key that starts s, as in a."b".c, and returns its
// parts and what follows them.
func tomlPath(s string) (path []string, rest string) {
	for {
		part := tomlKey.FindStringSubmatch(s)
		if part == nil {
			return nil, s
		}
		path, s = append(path, part[1]+part[2]+part[3]), s[len(part[0]):]
		if !strings.HasPrefix(s, ".") {
			return path, s
		}
		s = s[1:]
	}
}

// codexServers reads the MCP servers in Codex's config.toml: each server's
// name and the lines that define it. It follows every way TOML has to write
// mcp_servers.NAME outside an inline table: a [mcp_servers.NAME] table, a NAME
// key under [mcp_servers], and a key that starts with mcp_servers.NAME. A
// second definition of a server stops Codex loading its config, so init must
// see the name it would append in any of them. Searching the file for the
// name would not do: nearly every config has the word "string" in it.
func codexServers(config string) map[string]string {
	servers := map[string]string{}
	var table []string
	var name string
	// A byte order mark, which Codex accepts, would hide a table on line one.
	for line := range strings.SplitSeq(strings.TrimPrefix(config, "\ufeff"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		var path []string
		if header := strings.TrimLeft(line, "["); header != line {
			keys, rest := tomlPath(header)
			if end := strings.TrimLeft(rest, "] \t"); strings.HasPrefix(rest, "]") && (end == "" || end[0] == '#') {
				table, path = keys, keys
			}
		} else if keys, rest := tomlPath(line); strings.HasPrefix(rest, "=") {
			path = slices.Concat(table, keys)
		}
		// A line that is neither a table header nor a key carries on the
		// value above it, such as a long args list, and stays with that
		// value's server.
		if path != nil {
			name = ""
			if len(path) > 1 && path[0] == "mcp_servers" {
				name = path[1]
			}
		}
		if name != "" {
			servers[name] += line + "\n"
		}
	}
	return servers
}

// addCodexMCP appends the server to Codex's config.toml. codex mcp add would
// also start a sign-in and wait for it, holding init until someone finishes it.
func addCodexMCP(dir string) string {
	path := filepath.Join(dir, "config.toml")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "Could not add String's MCP server to Codex: " + err.Error()
	}
	config := string(data)
	if line := alreadyThere("Codex", codexServers(config)); line != "" {
		return line
	}
	switch {
	case strings.Contains(config, mcpHost):
		// The host is where no server's name was read, such as an inline
		// table or a comment. Any mention of it leaves the file alone.
		return "Codex already has String's MCP server."
	case codexInlineServers.MatchString(config):
		// codex mcp add replaces a server that has the name it is given, so
		// the command is printed only when the name is free.
		if codexInlineName.MatchString(config) {
			return fmt.Sprintf(mcpNameTaken, "Codex")
		}
		return "Add String's MCP server to Codex with: codex mcp add " + mcpName + " --url " + mcpURLArg
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
