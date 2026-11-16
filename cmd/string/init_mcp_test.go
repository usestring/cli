package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const mcpSettingsFile = ".claude.json"

var mcpConfigFile = filepath.Join(".codex", "config.toml")

// mcpHome makes a home directory with Claude Code and Codex in it. settings
// and config are the contents of Claude Code's user settings and of Codex's
// config.toml; "" leaves the file out.
func mcpHome(t *testing.T, settings, config string) string {
	t.Helper()
	home := t.TempDir()
	for _, dir := range []string{".claude", ".codex"} {
		if err := os.Mkdir(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, contents := range map[string]string{mcpSettingsFile: settings, mcpConfigFile: config} {
		if contents == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(home, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// mcpInit runs string init in home and returns the lines it printed and the
// programs it ran. claude is on PATH only when onPath is true, and running it
// changes nothing, so the files afterwards show what init itself wrote.
func mcpInit(t *testing.T, home string, onPath bool) (lines []string, ran [][]string) {
	t.Helper()
	// With a key, init ends after the MCP step.
	env := map[string]string{"HOME": home, "STRING_API_KEY": "test-key"}
	var stdout, stderr bytes.Buffer
	status := run([]string{"init"}, runtime{
		env: func(name string) (string, bool) {
			value, ok := env[name]
			return value, ok
		},
		lookPath: func(name string) (string, error) {
			if name == "claude" && !onPath {
				return "", os.ErrNotExist
			}
			return "/opt/bin/" + name, nil
		},
		program: func(name string, args ...string) ([]byte, error) {
			ran = append(ran, append([]string{name}, args...))
			return nil, nil
		},
		stdout: &stdout,
		stderr: &stderr,
	})
	if status != 0 {
		t.Fatalf("status %d: %s", status, stderr.String())
	}
	return strings.Split(stdout.String(), "\n"), ran
}

// mcpExpect fails the test unless init printed every one of want as a line.
func mcpExpect(t *testing.T, lines []string, want ...string) {
	t.Helper()
	for _, line := range want {
		if !slices.Contains(lines, line) {
			t.Errorf("init did not print\n%s\nIt printed\n%s", line, strings.Join(lines, "\n"))
		}
	}
}

// mcpFile fails the test unless the file under home holds exactly contents.
func mcpFile(t *testing.T, home, name, contents string) {
	t.Helper()
	if got, err := os.ReadFile(filepath.Join(home, name)); err != nil || string(got) != contents {
		t.Errorf("%s holds %q (%v), want %q", name, got, err, contents)
	}
}

func TestInitPrintsCommandsThatPasteIntoAnyShellWhenItCannotAddTheMCPServer(t *testing.T) {
	// claude is not on PATH, and TOML forbids adding a table to an inline
	// mcp_servers.
	config := "mcp_servers = { other = { command = \"npx\" } }\n"
	home := mcpHome(t, "", config)

	lines, ran := mcpInit(t, home, false)

	// zsh refuses the ? unquoted, and cmd.exe passes single quotes on as part
	// of the URL, so the quotes are double.
	mcpExpect(t, lines,
		`Add String's MCP server to Claude Code with: claude mcp add --transport http --scope user string "https://mcp.usestring.ai?login"`,
		`Add String's MCP server to Codex with: codex mcp add string --url "https://mcp.usestring.ai?login"`,
	)
	if len(ran) > 0 {
		t.Errorf("ran %q", ran)
	}
	mcpFile(t, home, mcpConfigFile, config)
}

func TestInitNamesTheMCPServerAnAgentAlreadyHasAndLeavesItAlone(t *testing.T) {
	for _, server := range []struct{ name, url string }{
		// What init wrote before it used the main address and the docs' name.
		{"string-web-access", "https://mcp.usestring.ai/v3/mcp"},
		{"web", "https://mcp.usestring.ai?login"},
		// init's own entry, on a second run.
		{"string", "https://mcp.usestring.ai?login"},
	} {
		settings := `{"numStartups":3,"mcpServers":{"other":{"type":"stdio","command":"npx"},"` + server.name + `":{"type":"http","url":"` + server.url + `"}}}` + "\n"
		config := "model = \"gpt-5\"\n\n[mcp_servers.other]\ncommand = \"npx\"\n\n[mcp_servers." + server.name + "]\nurl = \"" + server.url + "\"\n"
		home := mcpHome(t, settings, config)

		lines, ran := mcpInit(t, home, true)

		mcpExpect(t, lines,
			"Claude Code already has String's MCP server, as "+server.name+".",
			"Codex already has String's MCP server, as "+server.name+".",
		)
		if len(ran) > 0 {
			t.Errorf("%s: ran %q", server.name, ran)
		}
		mcpFile(t, home, mcpSettingsFile, settings)
		mcpFile(t, home, mcpConfigFile, config)
	}
}

func TestInitAddsTheMCPServerWhenOnlyTheWordStringIsThere(t *testing.T) {
	// Servers whose names contain init's, and the word wherever else it can
	// be. None of it is a server named string or at String's host.
	settings := `{"string":{"mcpServers":{"string":{"type":"stdio","command":"string"}}},
		"mcpServers":{"strings":{"type":"stdio","command":"string","args":["string"]},
			"my-string":{"type":"http","url":"https://example.com/string"},
			"String":{"type":"http","url":"https://example.com/mcp"}}}`
	config := `# string settings
model = "gpt-5"
string = "string"
notify = [
  "string",
]

[mcp_servers.strings]
command = "string"
args = ["--type", "string"]

[mcp_servers.my-string]
command = "npx"

[mcp_servers.my-string.env]
string = "string"

[mcp_servers.String]
command = "npx"

[projects."/home/me/mcp_servers.string"]
trust_level = "trusted"

[profiles.mcp_servers]
model = "string"

[shell_environment_policy.set]
string = "string"
`
	home := mcpHome(t, settings, config)

	lines, ran := mcpInit(t, home, true)

	add := []string{"/opt/bin/claude", "mcp", "add", "--transport", "http", "--scope", "user", "string", "https://mcp.usestring.ai?login"}
	if !reflect.DeepEqual(ran, [][]string{add}) {
		t.Errorf("ran %q, want %q", ran, add)
	}
	mcpFile(t, home, mcpConfigFile, config+"\n[mcp_servers.string]\nurl = \"https://mcp.usestring.ai?login\"\n")
	mcpExpect(t, lines,
		"Added String's MCP server to Claude Code. Sign in to it with: claude mcp login string",
		"Added String's MCP server to Codex in "+filepath.Join(home, mcpConfigFile)+". Sign in to it with: codex mcp login string",
	)
}

func TestInitLeavesAnotherServerNamedLikeItsOwnAlone(t *testing.T) {
	// String's self-hosted server, under the name its docs give it. claude mcp
	// add refuses a name that is taken, and a second [mcp_servers.string]
	// would stop Codex loading its config.
	settings := `{"mcpServers":{"string":{"type":"stdio","command":"npx","args":["-y","@usestring/mcp"]}}}` + "\n"
	config := "[mcp_servers.string]\ncommand = \"npx\"\nargs = [\"-y\", \"@usestring/mcp\"]\n"
	home := mcpHome(t, settings, config)

	lines, ran := mcpInit(t, home, true)

	mcpExpect(t, lines,
		"Claude Code already has an MCP server named string, so String's hosted server was not added.",
		"Codex already has an MCP server named string, so String's hosted server was not added.",
	)
	if len(ran) > 0 {
		t.Errorf("ran %q", ran)
	}
	mcpFile(t, home, mcpSettingsFile, settings)
	mcpFile(t, home, mcpConfigFile, config)
}

func TestInitReadsEveryWayCodexConfigHasToDefineAServer(t *testing.T) {
	const (
		named   = "Codex already has an MCP server named string, so String's hosted server was not added."
		unnamed = "Codex already has String's MCP server."
	)
	for _, test := range []struct{ about, config, want string }{
		// A server named string that is not String's. Appending a second
		// definition of it, in any of these forms, would stop Codex loading
		// the file.
		{"a table", "[mcp_servers.string]\ncommand = \"npx\"\n", named},
		{"a quoted name", "[mcp_servers.\"string\"]\ncommand = \"npx\"\n", named},
		{"spaces and single quotes", "[ 'mcp_servers' . 'string' ]\ncommand = \"npx\"\n", named},
		{"a comment after the header", "[mcp_servers.string] # local\ncommand = \"npx\"\n", named},
		{"a byte order mark", "\xef\xbb\xbf[mcp_servers.string]\ncommand = \"npx\"\n", named},
		{"Windows line endings", "model = \"gpt-5\"\r\n\r\n[mcp_servers.string]\r\ncommand = \"npx\"\r\n", named},
		{"a key under [mcp_servers]", "[mcp_servers]\nother = { command = \"npx\" }\nstring = { command = \"npx\" }\n", named},
		{"a dotted key under [mcp_servers]", "[mcp_servers]\nstring.command = \"npx\"\n", named},
		{"a dotted key at the top", "mcp_servers.string.command = \"npx\"\n", named},
		{"a dotted key below text that starts with a bracket", "developer_instructions = \"\"\"\n[Sources] Cite every page.\n\"\"\"\nmcp_servers.string.command = \"npx\"\n", named},
		{"a key of an inline table", "mcp_servers = { other = { command = \"npx\" }, string = { command = \"npx\" } }\n", named},

		// String's server under another name, where the name can be read.
		{"what codex mcp add makes of an inline table", "[mcp_servers ]\nother = { command = \"npx\" }\nweb = { url = \"https://mcp.usestring.ai?login\" }\n", "Codex already has String's MCP server, as web."},
		{"a dotted key at the top", "mcp_servers.web.url = \"https://mcp.usestring.ai/v3/mcp\"\n", "Codex already has String's MCP server, as web."},
		{"a URL on a later line of the entry", "[mcp_servers.bridge]\ncommand = \"npx\"\nargs = [\n  \"mcp-remote\",\n  \"https://mcp.usestring.ai/v3/mcp\",\n]\n\n[mcp_servers.other]\ncommand = \"npx\"\n", "Codex already has String's MCP server, as bridge."},
		{"two of them", "[mcp_servers.web]\nurl = \"https://mcp.usestring.ai?login\"\n\n[mcp_servers.old]\nurl = \"https://mcp.usestring.ai/v3/mcp\"\n", "Codex already has String's MCP server, as old."},

		// String's host where no name is read. The file is still left alone.
		{"an inline table", "mcp_servers = { web = { url = \"https://mcp.usestring.ai?login\" } }\n", unnamed},
		{"a comment in another server's table", "[mcp_servers.other]\ncommand = \"npx\"\n# url = \"https://mcp.usestring.ai/v3/mcp\"\n", unnamed},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(path, []byte(test.config), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := addCodexMCP(dir); got != test.want {
			t.Errorf("%s: init says\n%s\nwant\n%s", test.about, got, test.want)
		}
		if got, _ := os.ReadFile(path); string(got) != test.config {
			t.Errorf("%s: config became %q", test.about, got)
		}
	}
}
