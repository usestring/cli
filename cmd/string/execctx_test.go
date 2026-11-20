package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func envOf(vars map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := vars[name]
		return value, ok
	}
}

// Each agent is named by what its own command runner sets, in the order the
// rule gives. A person's terminal sets none of these, except the two Claude
// Code variables its IDE extensions also set, which count only when stdin is
// not a terminal.
func TestDetectAgentNamesTheAgentFromItsOwnVariables(t *testing.T) {
	for _, c := range []struct {
		about     string
		env       map[string]string
		stdinTTY  bool
		agent     string
		from, raw string
	}{
		{about: "nothing set", agent: ""},
		{about: "nothing set at a terminal", stdinTTY: true, agent: ""},
		{about: "Claude Code's AI_AGENT", env: map[string]string{"AI_AGENT": "claude-code_2-1-322_agent"}, agent: "claude-code", from: "AI_AGENT", raw: "claude-code_2-1-322_agent"},
		{about: "AI_AGENT with a version after @", env: map[string]string{"AI_AGENT": "opencode@1.4.0"}, agent: "opencode", from: "AI_AGENT", raw: "opencode@1.4.0"},
		{about: "GitHub Copilot's AI_AGENT", env: map[string]string{"AI_AGENT": "github_copilot_cli_1.2"}, agent: "github-copilot", from: "AI_AGENT", raw: "github_copilot_cli_1.2"},
		{about: "AI_AGENT in capitals", env: map[string]string{"AI_AGENT": "Cursor"}, agent: "cursor", from: "AI_AGENT", raw: "Cursor"},
		{about: "AI_AGENT at a terminal", env: map[string]string{"AI_AGENT": "gemini-cli"}, stdinTTY: true, agent: "gemini-cli", from: "AI_AGENT", raw: "gemini-cli"},
		{about: "Claude Code's command runner", env: map[string]string{"CLAUDE_CODE_CHILD_SESSION": "1"}, agent: "claude-code", from: "CLAUDE_CODE_CHILD_SESSION"},
		{about: "Codex", env: map[string]string{"CODEX_CI": "1", "NO_COLOR": "1", "TERM": "dumb"}, agent: "codex", from: "CODEX_CI"},
		{about: "a Codex thread", env: map[string]string{"CODEX_THREAD_ID": "019a"}, agent: "codex", from: "CODEX_THREAD_ID"},
		{about: "Codex's sandbox", env: map[string]string{"CODEX_SANDBOX": "seatbelt"}, agent: "codex", from: "CODEX_SANDBOX"},
		{about: "Gemini CLI", env: map[string]string{"GEMINI_CLI": "1"}, agent: "gemini-cli", from: "GEMINI_CLI"},
		{about: "Cursor's agent", env: map[string]string{"CURSOR_AGENT": "1"}, agent: "cursor", from: "CURSOR_AGENT"},
		{about: "opencode, which also sets AGENT=1", env: map[string]string{"OPENCODE": "1", "AGENT": "1"}, agent: "opencode", from: "OPENCODE"},
		{about: "Goose, which also sets AGENT=goose", env: map[string]string{"GOOSE_TERMINAL": "1", "AGENT": "goose"}, agent: "goose", from: "GOOSE_TERMINAL"},
		{about: "Amp", env: map[string]string{"AMP_CURRENT_THREAD_ID": "T-1"}, agent: "amp", from: "AMP_CURRENT_THREAD_ID"},
		{about: "Cline", env: map[string]string{"CLINE_ACTIVE": "true"}, agent: "cline", from: "CLINE_ACTIVE"},
		{about: "the Copilot CLI", env: map[string]string{"COPILOT_CLI": "1"}, agent: "github-copilot", from: "COPILOT_CLI"},
		{about: "Warp's agent", env: map[string]string{"WARP_RUN_ID": "r1"}, agent: "warp", from: "WARP_RUN_ID"},
		{about: "AI_AGENT before a runner's own variable", env: map[string]string{"AI_AGENT": "claude-code_2-1-322_agent", "CODEX_CI": "1"}, agent: "claude-code", from: "AI_AGENT", raw: "claude-code_2-1-322_agent"},
		{about: "CLAUDECODE without a terminal", env: map[string]string{"CLAUDECODE": "1"}, agent: "claude-code", from: "CLAUDECODE"},
		{about: "CLAUDECODE in an IDE terminal", env: map[string]string{"CLAUDECODE": "1"}, stdinTTY: true, agent: ""},
		{about: "CLAUDE_CODE_ENTRYPOINT without a terminal", env: map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"}, agent: "claude-code", from: "CLAUDE_CODE_ENTRYPOINT"},
		{about: "CLAUDE_CODE_ENTRYPOINT in an IDE terminal", env: map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"}, stdinTTY: true, agent: ""},
		{about: "AGENT=1", env: map[string]string{"AGENT": "1"}, agent: "unknown", from: "AGENT"},
		{about: "AGENT=amp", env: map[string]string{"AGENT": "amp"}, agent: "amp", from: "AGENT"},
		{about: "AGENT=0", env: map[string]string{"AGENT": "0"}, agent: ""},
		{about: "AGENT=false", env: map[string]string{"AGENT": "false"}, agent: ""},
		{about: "STRING_AGENT names the agent", env: map[string]string{"STRING_AGENT": "claude-code"}, stdinTTY: true, agent: "claude-code", from: "STRING_AGENT"},
		{about: "STRING_AGENT=1", env: map[string]string{"STRING_AGENT": "1"}, agent: "unknown", from: "STRING_AGENT"},
		{about: "STRING_AGENT over AI_AGENT", env: map[string]string{"STRING_AGENT": "my-bot", "AI_AGENT": "claude-code_2-1-322_agent"}, agent: "my-bot", from: "STRING_AGENT"},
		{about: "STRING_AGENT=human under an agent", env: map[string]string{"STRING_AGENT": "human", "AI_AGENT": "claude-code_2-1-322_agent", "CLAUDE_CODE_CHILD_SESSION": "1"}, agent: "", from: "STRING_AGENT"},
		{about: "STRING_AGENT=0 under an agent", env: map[string]string{"STRING_AGENT": "0", "CODEX_CI": "1"}, agent: "", from: "STRING_AGENT"},
		{about: "a name that can't go in a header", env: map[string]string{"STRING_AGENT": "my bot\r\nX-Evil: 1"}, agent: "my-bot--x-evil--1", from: "STRING_AGENT"},
		{about: "a name of nothing but symbols", env: map[string]string{"AGENT": "!!!"}, agent: "unknown", from: "AGENT"},
		{about: "AI_AGENT that can't go in a header", env: map[string]string{"AI_AGENT": "codex\r\nX-Evil: 1"}, agent: "codex--x-evil--1", from: "AI_AGENT", raw: "codexX-Evil: 1"},
		// Every person's Cursor terminal sets it, and a terminal type is the
		// same for a person.
		{about: "CURSOR_TRACE_ID and TERM_PROGRAM", env: map[string]string{"CURSOR_TRACE_ID": "abc", "TERM_PROGRAM": "vscode", "TERM": "dumb"}, agent: ""},
		{about: "CI alone", env: map[string]string{"CI": "true"}, agent: ""},
	} {
		agent, from, raw := detectAgent(envOf(c.env), c.stdinTTY)
		if agent != c.agent || from != c.from || raw != c.raw {
			t.Errorf("%s: got agent %q from %q (raw %q), want %q from %q (raw %q)", c.about, agent, from, raw, c.agent, c.from, c.raw)
		}
	}
}

// A long name is cut, so that a header stays short.
func TestAgentNamesAreShort(t *testing.T) {
	agent, _, raw := detectAgent(envOf(map[string]string{"AI_AGENT": strings.Repeat("a", 200)}), false)
	if len(agent) > 32 || len(raw) > 64 {
		t.Errorf("agent %q (%d) and raw %q (%d) are not cut to 32 and 64", agent, len(agent), raw, len(raw))
	}
}

// The decisions execCtx makes, as a truth table over the three streams, an
// agent and CI. A person at a terminal is the only one who is interactive or
// sees an update notice; an agent never is, whatever its streams are.
func TestExecCtxDecisions(t *testing.T) {
	for _, c := range []struct {
		about                        string
		in, out, err, noOpen         bool
		agent                        string
		ci                           bool
		interactive, notice, browser bool
	}{
		{about: "a person at a terminal", in: true, out: true, err: true, interactive: true, notice: true, browser: true},
		{about: "a person at a terminal with --no-open", in: true, out: true, err: true, noOpen: true, interactive: true, notice: true},
		{about: "a person saving stdout to a file", in: true, err: true},
		{about: "a person piping stdin in", out: true, err: true, notice: true},
		{about: "stderr redirected at a terminal", in: true, out: true, interactive: true, browser: true},
		{about: "a script"},
		{about: "CI", ci: true},
		{about: "CI with a terminal", in: true, out: true, err: true, ci: true},
		{about: "an agent with pipes", agent: "claude-code"},
		{about: "an agent with a terminal", in: true, out: true, err: true, agent: "codex"},
		{about: "an agent in CI", agent: "claude-code", ci: true},
	} {
		ctx := execCtx{stdinTTY: c.in, stdoutTTY: c.out, stderrTTY: c.err, agent: c.agent, ci: c.ci, noOpen: c.noOpen}
		if got := ctx.interactive(); got != c.interactive {
			t.Errorf("%s: interactive %t, want %t", c.about, got, c.interactive)
		}
		if got := ctx.attended(); got != c.notice {
			t.Errorf("%s: attended %t, want %t", c.about, got, c.notice)
		}
		if got := ctx.opensBrowser(); got != c.browser {
			t.Errorf("%s: opensBrowser %t, want %t", c.about, got, c.browser)
		}
	}

	// Every combination, for what holds whatever the streams are.
	for bits := range 1 << 6 {
		ctx := execCtx{stdinTTY: bits&1 != 0, stdoutTTY: bits&2 != 0, stderrTTY: bits&4 != 0, ci: bits&16 != 0, noOpen: bits&32 != 0}
		if bits&8 != 0 {
			ctx.agent = "codex"
		}
		about := fmt.Sprintf("%+v", ctx)
		if (ctx.agent != "" || ctx.ci) && (ctx.interactive() || ctx.attended() || ctx.opensBrowser()) {
			t.Errorf("%s: an agent or CI counts as a person", about)
		}
		if ctx.interactive() && !(ctx.stdinTTY && ctx.stdoutTTY) {
			t.Errorf("%s: interactive without a terminal on stdin and stdout", about)
		}
		if ctx.attended() && !(ctx.stdoutTTY && ctx.stderrTTY) {
			t.Errorf("%s: an update notice without a terminal on stdout and stderr", about)
		}
		if ctx.opensBrowser() != (ctx.interactive() && !ctx.noOpen) {
			t.Errorf("%s: opensBrowser %t", about, ctx.opensBrowser())
		}
	}
}

// main reads the streams it is given, and the variables, once.
func TestNewExecCtxReadsTheStreamsAndVariables(t *testing.T) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	got := newExecCtx(envOf(map[string]string{"CI": "1", "DO_NOT_TRACK": "1", "CODEX_CI": "1"}), null, null, null)
	want := execCtx{agent: "codex", agentFrom: "CODEX_CI", ci: true, doNotTrack: true}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	for value, on := range map[string]bool{"": false, "0": false, "false": false, "FALSE": false, "1": true, "true": true, "yes": true} {
		if got := newExecCtx(envOf(map[string]string{"DO_NOT_TRACK": value}), null, null, null).doNotTrack; got != on {
			t.Errorf("DO_NOT_TRACK=%q: %t, want %t", value, got, on)
		}
	}
}

// The mode a request names follows the same rule: an agent wins over CI,
// and CI over a terminal.
func TestExecCtxMode(t *testing.T) {
	for _, c := range []struct {
		ctx  execCtx
		mode string
	}{
		{execCtx{stdinTTY: true, stdoutTTY: true, stderrTTY: true}, "interactive"},
		{execCtx{stdinTTY: true, stderrTTY: true}, "non-interactive"},
		{execCtx{}, "non-interactive"},
		{execCtx{ci: true}, "ci"},
		{execCtx{stdinTTY: true, stdoutTTY: true, stderrTTY: true, ci: true}, "ci"},
		{execCtx{agent: "claude-code"}, "agent"},
		{execCtx{stdinTTY: true, stdoutTTY: true, stderrTTY: true, agent: "codex"}, "agent"},
		{execCtx{agent: "claude-code", ci: true}, "agent"},
	} {
		if got := c.ctx.mode(); got != c.mode {
			t.Errorf("%+v: mode %q, want %q", c.ctx, got, c.mode)
		}
	}
}
