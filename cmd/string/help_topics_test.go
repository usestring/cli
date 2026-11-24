package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// string help TOPIC prints the topic, with no request, and a near miss of a
// topic names the one meant.
func TestHelpTopics(t *testing.T) {
	for topic, page := range helpTopics {
		got := runOffline(t, "help", topic)
		if got.status != 0 || got.output != page || !strings.HasPrefix(page, "string help "+topic+": ") || !strings.Contains(page, "\nLearn more: https://") {
			t.Errorf("string help %s: exit %d:\n%s", topic, got.status, got.output)
		}
	}
	if got := runOffline(t, "help", "exit-code"); got.status != exitUsage || got.errors != "error: No help on exit-code. Did you mean exit-codes?\n\nnext: run `string help exit-codes`\n" {
		t.Errorf("string help exit-code: %+v", got)
	}
	// The topic it names keeps the switches given.
	if got := runOffline(t, "help", "exit-code", "-q"); got.status != exitUsage || got.errors != "error: No help on exit-code. Did you mean exit-codes?\n\nnext: run `string help exit-codes -q`\n" {
		t.Errorf("string help exit-code -q: %+v", got)
	}
	// A command's near miss still names the command.
	if got := runOffline(t, "help", "fecth"); got.status != exitUsage || !strings.HasPrefix(got.errors, "error: Unknown command: fecth. Did you mean fetch?") {
		t.Errorf("string help fecth: %+v", got)
	}

	// Every exit code string uses has a line.
	for _, code := range []int{0, exitFailure, exitUsage, exitAuth, exitPayment, exitRateLimit, exitUpstream, exitTimeout, exitInterrupted} {
		if !regexp.MustCompile(`\n  ` + strconv.Itoa(code) + ` +\S`).MatchString(helpTopics["exit-codes"]) {
			t.Errorf("string help exit-codes has no line for %d", code)
		}
	}
	// Every STRING_ variable the code reads is in string help environment.
	files, _ := filepath.Glob("*.go")
	named := regexp.MustCompile(`"(STRING_[A-Z_]+)"`)
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range named.FindAllStringSubmatch(string(data), -1) {
			if !regexp.MustCompile(`\n  ` + match[1] + `[ \n]`).MatchString(helpTopics["environment"]) {
				t.Errorf("%s reads %s, which string help environment doesn't list", file, match[1])
			}
		}
	}
}

// Under an agent, the help for all of string starts with a few lines for it.
// A person sees the help alone, and a command's own page has no such lines.
func TestTheHelpForAllOfStringGreetsAnAgent(t *testing.T) {
	helpFor := func(exec execCtx, list ...string) string {
		var stdout bytes.Buffer
		if status := run(list, runtime{env: envOf(map[string]string{"HOME": t.TempDir()}), client: &http.Client{Transport: noNetwork{t}}, stdout: &stdout, stderr: &bytes.Buffer{}, exec: exec}); status != 0 {
			t.Errorf("%v: exit %d", list, status)
		}
		return stdout.String()
	}
	for _, list := range [][]string{{}, {"help"}, {"--help"}, {"-h"}} {
		if got := helpFor(execCtx{agent: "claude-code"}, list...); got != agentPreamble+help {
			t.Errorf("%v under an agent:\n%s", list, got[:min(len(got), 300)])
		}
		if got := helpFor(execCtx{stdinTTY: true, stdoutTTY: true, stderrTTY: true}, list...); got != help {
			t.Errorf("%v for a person:\n%s", list, got[:min(len(got), 300)])
		}
	}
	if got := helpFor(execCtx{agent: "codex"}, "fetch", "--help"); strings.Contains(got, "For agents:") {
		t.Errorf("fetch --help under an agent has the lines for agents")
	}
	if lines := strings.Count(agentPreamble, "\n"); lines != 4 || slices.ContainsFunc(strings.Split(agentPreamble, "\n"), func(line string) bool { return len(line) > 80 }) {
		t.Errorf("the lines for agents are %d lines, some over 80 columns:\n%s", lines, agentPreamble)
	}
}

// The help for all of string fits on one screen, so an agent reads it whole
// for the price of a few lines: a line for each command's page, and last where
// every command is. A page left out of it is a command nobody finds there.
func TestTheHelpForAllOfStringFitsOnOneScreen(t *testing.T) {
	lines := strings.Split(strings.TrimSuffix(help, "\n"), "\n")
	if last := lines[len(lines)-1]; len(lines) > 25 || !strings.Contains(last, "string help --all") {
		t.Errorf("the help for all of string is %d lines and ends %q; want 25 at most, ending with string help --all", len(lines), last)
	}
	for _, line := range lines {
		if utf8.RuneCountInString(line) > helpWidth {
			t.Errorf("the help for all of string has a line of %d columns: %q", utf8.RuneCountInString(line), line)
		}
	}
	flat := strings.Join(strings.Fields(help), " ")
	for name, page := range commandDocs {
		if !regexp.MustCompile(`(?m)^  `+regexp.QuoteMeta(name)+`( |$)`).MatchString(help) || !strings.Contains(flat, " "+page.summary+" ") {
			t.Errorf("the help for all of string has no line for %s, %q", name, page.summary)
		}
	}
}
