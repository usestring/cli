package main

import (
	"os"
	"strings"
)

// execCtx is where this run of string happens: which of its streams are
// terminals, whether a coding agent runs it, and whether it is a CI job. main
// works it out once, before the command runs, and every prompt, browser,
// notice and wait asks it instead of looking at the streams again. What it
// decides never changes the bytes on stdout or the exit status.
type execCtx struct {
	stdinTTY, stdoutTTY, stderrTTY bool
	// agent names the coding agent that runs string, such as claude-code, or
	// is empty for a person or a script.
	agent string
	// agentFrom is the variable the agent was named by, so that the reason
	// for agent mode can be shown.
	agentFrom string
	// agentRaw is AI_AGENT as the agent set it, version and all, when that
	// is where the name came from.
	agentRaw string
	// ci is a CI job: CI is set, and not to false or 0.
	ci bool
	// noOpen is --no-open: string opens no browser, even for a person.
	noOpen bool
	// noInput is --no-input or STRING_NO_INPUT: nobody will answer, so
	// string waits for no person, even at a terminal.
	noInput bool
	// doNotTrack is DO_NOT_TRACK: requests then name only string's version.
	doNotTrack bool
}

func newExecCtx(env func(string) (string, bool), stdin, stdout, stderr *os.File) execCtx {
	return execCtxFor(env, isTerminal(stdin), isTerminal(stdout), isTerminal(stderr))
}

// execCtxFor is newExecCtx for streams already known to be terminals or not.
func execCtxFor(env func(string) (string, bool), stdinTTY, stdoutTTY, stderrTTY bool) execCtx {
	c := execCtx{
		stdinTTY:   stdinTTY,
		stdoutTTY:  stdoutTTY,
		stderrTTY:  stderrTTY,
		ci:         inCI(env),
		doNotTrack: switchedOn(env, "DO_NOT_TRACK"),
		noInput:    switchedOn(env, "STRING_NO_INPUT"),
	}
	c.agent, c.agentFrom, c.agentRaw = detectAgent(env, c.stdinTTY)
	return c
}

// interactive reports a person at the keyboard who sees what string prints
// and can answer it there. Only then does a command wait for a person
// without being told to with --poll.
func (c execCtx) interactive() bool {
	return c.stdinTTY && c.stdoutTTY && c.agent == "" && !c.ci && !c.noInput
}

// opensBrowser reports whether string may open a link in the browser itself:
// only for a person at a terminal, and not with --no-open. Anywhere else the
// link it prints is the whole hand-off.
func (c execCtx) opensBrowser() bool {
	return c.interactive() && !c.noOpen
}

// attended reports a person watching both output streams at a terminal, the
// only reader an update notice is for.
func (c execCtx) attended() bool {
	return c.stdoutTTY && c.stderrTTY && c.agent == "" && !c.ci
}

// mode is how string runs, as the X-String-CLI-Mode header names it.
func (c execCtx) mode() string {
	switch {
	case c.agent != "":
		return "agent"
	case c.ci:
		return "ci"
	case c.interactive():
		return "interactive"
	}
	return "non-interactive"
}

// agentVars are set only by a coding agent's own command runner, never in a
// terminal a person uses, so any of them being set names the agent.
var agentVars = []struct{ name, agent string }{
	{"CLAUDE_CODE_CHILD_SESSION", "claude-code"},
	{"CODEX_CI", "codex"},
	{"CODEX_THREAD_ID", "codex"},
	{"CODEX_SANDBOX", "codex"},
	{"CODEX_SANDBOX_NETWORK_DISABLED", "codex"},
	{"GEMINI_CLI", "gemini-cli"},
	{"CURSOR_AGENT", "cursor"},
	{"OPENCODE", "opencode"},
	{"GOOSE_TERMINAL", "goose"},
	{"AMP_CURRENT_THREAD_ID", "amp"},
	{"CLINE_ACTIVE", "cline"},
	{"COPILOT_CLI", "github-copilot"},
	{"OZ_RUN_ID", "warp"},
	{"WARP_RUN_ID", "warp"},
}

// detectAgent names the coding agent that runs string, the variable that
// named it, and AI_AGENT as it was set when that variable is AI_AGENT. It
// returns empty strings for a person or a script. The variables are checked
// in a fixed order. STRING_AGENT comes first, and it can also force the
// person's view. Terminal type, terminal program and the process tree are
// never read: each is the same for a person.
func detectAgent(env func(string) (string, bool), stdinTTY bool) (agent, from, raw string) {
	value := func(name string) string {
		v, _ := env(name)
		return strings.TrimSpace(v)
	}
	switch v := strings.ToLower(value("STRING_AGENT")); v {
	case "":
	case "0", "false", "no", "none", "human":
		return "", "STRING_AGENT", ""
	case "1", "true", "yes":
		return "unknown", "STRING_AGENT", ""
	default:
		return agentName(v), "STRING_AGENT", ""
	}
	if v := value("AI_AGENT"); v != "" {
		// Claude Code sets claude-code_2-1-322_agent: the name, then the
		// version, so the name is what comes before the version.
		name, _, _ := strings.Cut(strings.ToLower(v), "@")
		switch {
		case strings.HasPrefix(name, "claude-code"):
			name = "claude-code"
		case strings.HasPrefix(name, "github_copilot"), strings.HasPrefix(name, "github-copilot"):
			name = "github-copilot"
		default:
			name, _, _ = strings.Cut(name, "_")
		}
		return agentName(name), "AI_AGENT", headerText(v)
	}
	for _, known := range agentVars {
		if value(known.name) != "" {
			return known.agent, known.name, ""
		}
	}
	// Claude Code's IDE extensions set these in a person's own terminal too,
	// where stdin is that terminal. Its command runner gives stdin no
	// terminal.
	if !stdinTTY {
		for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT"} {
			if value(name) != "" {
				return "claude-code", name, ""
			}
		}
	}
	switch v := strings.ToLower(value("AGENT")); v {
	case "", "0", "false":
		return "", "", ""
	case "1", "true":
		return "unknown", "AGENT", ""
	default:
		return agentName(v), "AGENT", ""
	}
}

// agentName makes a name an agent gave itself safe for a header and a log
// line: lower-case letters, digits, dots, hyphens and underscores, at most 32
// of them.
func agentName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if b.Len() == 32 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if name := strings.Trim(b.String(), "-._"); name != "" {
		return name
	}
	return "unknown"
}

// headerText keeps the printable ASCII of text, at most 64 bytes of it, so
// that it can be sent as a header value.
func headerText(text string) string {
	var b strings.Builder
	for i := 0; i < len(text) && b.Len() < 64; i++ {
		if c := text[i]; c >= 0x20 && c < 0x7f {
			b.WriteByte(c)
		}
	}
	return strings.TrimSpace(b.String())
}

// switchedOn reports whether the variable is set to something other than
// false or 0.
func switchedOn(env func(string) (string, bool), name string) bool {
	value, _ := env(name)
	value = strings.ToLower(strings.TrimSpace(value))
	return value != "" && value != "0" && value != "false"
}
