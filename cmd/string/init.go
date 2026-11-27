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
// user's home. An agent counts as found on this machine when its config
// directory exists.
type agent struct {
	name   string
	label  string
	config func(home string, env func(string) (string, bool)) string
	// shared is true for an agent that also reads ~/.agents/skills. string
	// init puts the skill there for all of them, and in such an agent's own
	// directory only when --agent names it, since an agent that reads both
	// may see two copies: Gemini CLI then warns of a skill conflict.
	shared bool
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

// sharedAgent is ~/.agents/skills, which Codex and most other agents read.
// It is always installed, so every agent that reads it has the skill.
var sharedAgent = agent{"codex", "Codex and agents that read ~/.agents/skills", under(".agents"), true}

// agents are the agents init knows besides Codex. Claude Code reads only
// ~/.claude/skills. For each other agent that isn't shared, nothing shows it
// reads ~/.agents/skills, so it gets a copy of its own.
var agents = []agent{
	{"claude-code", "Claude Code", func(home string, env func(string) (string, bool)) string {
		if dir, ok := env("CLAUDE_CONFIG_DIR"); ok && dir != "" {
			return dir
		}
		return filepath.Join(home, ".claude")
	}, false},
	{"cursor", "Cursor", under(".cursor"), true},
	{"gemini-cli", "Gemini CLI", under(".gemini"), true},
	{"opencode", "OpenCode", underConfig("opencode"), true},
	{"github-copilot", "GitHub Copilot", under(".copilot"), true},
	{"windsurf", "Windsurf", under(".codeium", "windsurf"), false},
	{"goose", "Goose", underConfig("goose"), true},
	{"droid", "Droid", under(".factory"), true},
	{"kiro-cli", "Kiro CLI", under(".kiro"), false},
	{"qwen-code", "Qwen Code", under(".qwen"), false},
	{"grok", "Grok Build", under(".grok"), false},
	{"openclaw", "OpenClaw", under(".openclaw"), false},
	{"hermes-agent", "Hermes Agent", under(".hermes"), false},
}

// skillName is the skill's directory in skills.FS and in each agent's skills
// directory.
const skillName = "string-cli"

// skillsDir is where agent a reads skills from.
func skillsDir(a agent, home string, env func(string) (string, bool)) string {
	return filepath.Join(a.config(home, env), "skills")
}

// skillCopies lists the directories under the agents' skills directories that
// hold the skill, the shared one first. Two agents that read one directory
// give it once.
func skillCopies(home string, env func(string) (string, bool)) []string {
	var dirs []string
	for _, a := range append([]agent{sharedAgent}, agents...) {
		dir := filepath.Join(skillsDir(a, home, env), skillName)
		if info, err := os.Stat(dir); err == nil && info.IsDir() && !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	return dirs
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
	parsed, err := parse(list, spec{options: []string{"agent", "device-name"}, switches: []string{"no-mcp", "no-login", "browser"}})
	if err != nil {
		return usage(err)
	}
	if parsed.help {
		return write(rt.stdout, []byte(helpFor("init")))
	}
	if err := expectPositionals(parsed.positionals, 0, "string init [--agent NAME[,NAME...]] [--no-mcp] [--no-login] [--browser] [--device-name NAME]"); err != nil {
		return usage(err)
	}
	if _, ok := parsed.value("input"); ok {
		return usage(errors.New("--input does not apply to init"))
	}
	deviceName, err := deviceNameOption(parsed)
	if err != nil {
		return usage(err)
	}
	if deviceName != "" && parsed.switches["browser"] {
		return usage(errors.New("--device-name does not apply to --browser"))
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
	// Without --agent, an agent found here that reads ~/.agents/skills takes
	// the skill from there.
	var sharing []string
	for _, a := range chosen {
		if names == "" && a.shared && a.name != sharedAgent.name {
			sharing = append(sharing, a.label)
		}
	}
	var report strings.Builder
	for _, a := range chosen {
		if slices.Contains(sharing, a.label) {
			continue
		}
		dir := skillsDir(a, home, rt.env)
		if err := installSkills(dir); err != nil {
			return err
		}
		fmt.Fprintf(&report, "Installed the string-cli skill for %s in %s\n", a.label, filepath.Join(dir, skillName))
		if a.name == sharedAgent.name && len(sharing) > 0 {
			verb := "reads"
			if len(sharing) > 1 {
				verb = "read"
			}
			fmt.Fprintf(&report, "%s %s it there too.\n", joinAnd(sharing), verb)
		}
	}
	if names == "" {
		removeGeminiCopy(&report, home, rt.env)
	}
	if !parsed.switches["no-mcp"] {
		addMCP(&report, chosen, home, rt)
	}
	if _, err := rt.lookPath("string"); err != nil {
		report.WriteString("\nAgents run the skill's commands as `string`, which is not on your PATH. " + pathHint(rt) + "\n")
	}
	key, keyErr := apiKey(rt.env)
	if keyErr == nil && key != "" {
		return write(rt.stdout, []byte(report.String()))
	}
	// Nobody approves a sign-in in CI. A person who pipes install.sh into sh
	// reads stderr at a terminal, and starts keyless.
	handBack := !rt.exec.ci && (rt.exec.agent != "" || rt.exec.noInput || !rt.exec.stderrTTY)
	if oauthClientID != "" && !parsed.switches["no-login"] && (rt.exec.interactive() || handBack) {
		browser := parsed.switches["browser"]
		var err error
		if rt.exec.interactive() {
			report.WriteString("\nSigning in. string fetch and string search already work without a key, within a daily allowance, so you can press Ctrl-C to skip this and run string login later.\n")
			if err := write(rt.stdout, []byte(report.String())); err != nil {
				return err
			}
			report.Reset()
			err = signIn(rt, browser, "", deviceName)
		} else {
			// Nobody here approves a sign-in, so init first registers a trial
			// key, which needs nobody: the agent can use it at once, and a
			// person can claim the agent onto their account later. --browser
			// only picks the sign-in init falls back to, while --device-name
			// names a sign-in's key, so it asks for the sign-in instead. A key
			// setting init could not read, a portal without trial keys, or one
			// that refuses this one leaves the sign-in below as it was.
			if path, ok := credentialsPath(rt.env); ok && keyErr == nil && deviceName == "" {
				trial, err := registerTrial(rt, path)
				if err == nil {
					fmt.Fprintf(&report, "\n%s", trial.text(path))
					return write(rt.stdout, []byte(report.String()))
				}
				if refused := asFailure(err); refused.code != "trial_unavailable" {
					why, _, _ := strings.Cut(refused.message, "\n")
					fmt.Fprintf(&report, "\nNo trial key: %s.\n", strings.TrimSuffix(why, "."))
				}
			}
			// Nobody approves while init waits under an agent or with
			// --no-input, so init hands back the link and code, as string
			// login does there, and string login --poll saves the key. init
			// prints text only.
			var started bytes.Buffer
			text := rt
			text.json, text.stdout = false, &started
			if _, err = startSignIn(text, browser, false, "", deviceName); err == nil {
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
			if browser {
				why = "the link expired"
			}
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

// joinAnd lists words as a sentence does: a, b and c.
func joinAnd(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// removeGeminiCopy removes the skill from Gemini CLI's own skills directory,
// where releases before 0.3 put it. Gemini CLI reads ~/.agents/skills too,
// and warns of a skill conflict at every start while both copies are there.
// Only a copy as string init writes it goes. Anything else there stays, and
// init says why.
func removeGeminiCopy(report *strings.Builder, home string, env func(string) (string, bool)) {
	gemini := agents[slices.IndexFunc(agents, func(a agent) bool { return a.name == "gemini-cli" })]
	dir := filepath.Join(skillsDir(gemini, home, env), skillName)
	info, err := os.Lstat(dir)
	if err != nil {
		return
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		// No skill, so nothing for Gemini CLI to warn about.
		return
	}
	if !initWrote(dir, info) {
		fmt.Fprintf(report, "Left %s as it is: it isn't a plain copy from string init, so it may be yours. Gemini CLI warns of a skill conflict while it and ~/.agents/skills/%s are both there.\n", dir, skillName)
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(report, "Could not remove the older copy in %s, which makes Gemini CLI warn of a skill conflict: %v\n", dir, err)
		return
	}
	fmt.Fprintf(report, "Removed the older copy in %s, since Gemini CLI reads ~/.agents/skills and warns of a skill conflict when it finds two. For a Gemini CLI too old to read ~/.agents/skills, string init --agent gemini-cli puts it back.\n", dir)
}

// initWrote reports whether dir, which info describes without following a
// link, holds a copy of the skill as string init writes one: a directory,
// not a link to one, with no file but the skill's own, and a SKILL.md that
// names the skill.
func initWrote(dir string, info fs.FileInfo) bool {
	if !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		embedded, err := fs.Stat(skills.FS, skillName+"/"+entry.Name())
		if !entry.Type().IsRegular() || err != nil || embedded.IsDir() {
			return false
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return false
	}
	front, ok := strings.CutPrefix(strings.ReplaceAll(string(data), "\r\n", "\n"), "---\n")
	front, _, closed := strings.Cut(front, "\n---\n")
	return ok && closed && slices.Contains(strings.Split(front, "\n"), "name: "+skillName)
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
