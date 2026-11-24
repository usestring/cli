package main

import (
	"slices"
	"strings"
	"testing"
	"unicode"
)

// completed is what string __complete answers shell for words, the last of
// them the word being typed: the value of each answer, without what it does,
// or :files.
func completed(t *testing.T, shell string, words ...string) []string {
	t.Helper()
	got := runOffline(t, append([]string{"__complete", shell}, words...)...)
	if got.status != 0 || got.errors != "" {
		t.Fatalf("string __complete %s %q: exit %d, stderr %q", shell, words, got.status, got.errors)
	}
	var values []string
	for line := range strings.Lines(got.output) {
		line = strings.TrimSuffix(line, "\n")
		if shell == "zsh" && line != ":files" {
			// name:description, with any colon in the name escaped.
			escaped := strings.ReplaceAll(line, `\:`, "\x00")
			name, _, _ := strings.Cut(escaped, ":")
			line = strings.ReplaceAll(name, "\x00", ":")
		}
		line, _, _ = strings.Cut(line, "\t")
		values = append(values, line)
	}
	return values
}

// Tab completes what string takes next on the line: a command, a
// subcommand, the command's own flags, the values a flag names, a file name,
// or nothing after --, in the form each shell's script reads. A script that
// one string printed asks whichever string is installed later, so this is a
// contract between versions.
func TestCompletionAnswersWhatTheLineTakesNext(t *testing.T) {
	choices := func(flags []flagDoc, name string) []string {
		i := slices.IndexFunc(flags, func(f flagDoc) bool { return f.name == name })
		return strings.Split(flags[i].value, "|")
	}
	formats := choices(commandDocs["fetch"].fields, "format")
	var sitemapCommands []string
	for _, l := range commandDocs["sitemap"].leaves {
		sitemapCommands = append(sitemapCommands, l.words()[1])
	}
	helpWords := append(slices.DeleteFunc(slices.Clone(commands), func(c string) bool { return c == "help" }), helpTopicNames...)
	for _, c := range []struct {
		shell string
		words []string
		want  []string
	}{
		{"zsh", []string{""}, commands},
		{"zsh", []string{"sitemap", ""}, sitemapCommands},
		{"zsh", []string{"help", ""}, helpWords},
		{"zsh", []string{"help", "sitemap", ""}, sitemapCommands},
		{"zsh", []string{"completion", ""}, completionShells},
		{"zsh", []string{"request", ""}, requestMethods},
		{"fish", []string{"fetch", "--format", ""}, formats},
		{"zsh", []string{"--errors", ""}, choices(globalFlagDocs, "errors")},
		{"zsh", []string{"fetch", "--format=m"}, []string{"--format=markdown"}},
		// A value is offered only when the flag names its choices.
		{"zsh", []string{"--timeout", ""}, nil},
		// A flag completes in the spelling it was started in, whatever its
		// case, and a switch that is on unless turned off completes as =false.
		{"zsh", []string{"fetch", "--main-c"}, []string{"--main-content-only"}},
		{"zsh", []string{"fetch", "--main_c"}, []string{"--main_content_only"}},
		{"zsh", []string{"fetch", "--execute-"}, []string{"--execute-js"}},
		{"bash", []string{"fetch", "--maincontent"}, []string{"--mainContentOnly"}},
		{"zsh", []string{"fetch", "--solve"}, []string{"--solveCaptcha=false"}},
		{"zsh", []string{"fetch", "--solveCaptcha="}, []string{"--solveCaptcha=false"}},
		// A value its page names FILE is a file name, and sitemap's --pathPrefix
		// PATH is the path of a URL.
		{"zsh", []string{"fetch", "-o", ""}, []string{":files"}},
		{"zsh", []string{"login", "--env-file", ""}, []string{":files"}},
		{"zsh", []string{"sitemap", "submit", "https://example.com", "--pathPrefix", ""}, nil},
		{"zsh", []string{"init", "--agent", "claude-code,cu"}, []string{"claude-code,cursor"}},
		// After --, every word is an argument.
		{"zsh", []string{"fetch", "--", "-"}, nil},
		// Windows PowerShell drops an empty argument, so its script sends "".
		{"powershell", []string{`""`}, commands},
		// bash splits a word at = and :, and replaces only what follows the
		// last of them. The word after an = starts once a space follows it.
		{"bash", []string{"fetch", "--format", "=", "m"}, []string{"markdown"}},
		{"bash", []string{"fetch", "--format", "="}, formats},
		{"bash", []string{"fetch", "--format", "=", ""}, nil},
	} {
		if got := completed(t, c.shell, c.words...); !slices.Equal(got, c.want) {
			t.Errorf("%s, string %s: %q, want %q", c.shell, strings.Join(c.words, " "), got, c.want)
		}
	}
	// A subcommand's flags are its own, and before the subcommand a command
	// takes those of each, but string integrations alone is integrations
	// list. A flag given already isn't offered again, even when it was given
	// under an older name, nor is a name already in a list.
	for _, c := range []struct {
		shell        string
		words        []string
		offered, not []string
	}{
		{"zsh", []string{"sitemap", "status", "--"}, []string{"--wait", "--output", "--json"}, []string{"--approve", "--maxPages"}},
		{"zsh", []string{"sitemap", "--"}, []string{"--approve", "--wait"}, nil},
		{"zsh", []string{"integrations", "--"}, []string{"--json"}, []string{"--output"}},
		// FIELD stands for the fields of an action, which only the API knows.
		{"zsh", []string{"integrations", "run", "--"}, []string{"--output"}, []string{"--FIELD"}},
		{"zsh", []string{"trial", "--"}, []string{"--json"}, []string{"--email"}},
		{"zsh", []string{"trial", "claim", "--"}, []string{"--email"}, nil},
		{"zsh", []string{"help", "--"}, []string{"--all"}, nil},
		{"zsh", []string{"--"}, []string{"--version"}, nil},
		{"zsh", []string{"fetch", "-"}, []string{"-o"}, nil},
		{"zsh", []string{"search", "--count", "5", "--search"}, []string{"--searchType"}, []string{"--searchCount"}},
		{"zsh", []string{"init", "--agent", "claude-code,"}, []string{"claude-code,cursor"}, []string{"claude-code,claude-code"}},
		// --captureXHR takes its value only after an =.
		{"zsh", []string{"fetch", "--captureXHR", "--fo"}, []string{"--format"}, nil},
		// A URL or a domain is fetched, and bash splits a URL at its colons.
		{"zsh", []string{"example.com", "--max"}, []string{"--max-tokens"}, nil},
		{"bash", []string{"http", ":", "//localhost", ":", "8080", "--max"}, []string{"--max-tokens"}, nil},
		{"bash", []string{"--timeout", "=", "5s", "fetch", "--fo"}, []string{"--format"}, nil},
	} {
		got := completed(t, c.shell, c.words...)
		for _, want := range c.offered {
			if !slices.Contains(got, want) {
				t.Errorf("%s, string %s: %q, which lacks %s", c.shell, strings.Join(c.words, " "), got, want)
			}
		}
		for _, unwanted := range c.not {
			if slices.Contains(got, unwanted) {
				t.Errorf("%s, string %s: %q, which has %s", c.shell, strings.Join(c.words, " "), got, unwanted)
			}
		}
	}
	// A command's own flags come before those every command takes.
	if got := completed(t, "zsh", "fetch", "--"); slices.Index(got, "--format") > slices.Index(got, "--json") {
		t.Errorf("zsh, string fetch --: %q, with --json before --format", got)
	}
	// Windows PowerShell shows what a flag does in the console's code page,
	// which shows UTF-8 as other characters.
	got := runOffline(t, "__complete", "powershell", "login", "--device")
	if !strings.Contains(got.output, "--device-name\t") || strings.ContainsFunc(got.output, func(r rune) bool { return r > unicode.MaxASCII }) {
		t.Errorf("powershell, string login --device: %q", got.output)
	}
}

// string completion prints the script for the shell named, in any case, takes
// pwsh for PowerShell, and names the shell meant when the name is a near miss.
func TestCompletionPrintsTheScriptForTheShellNamed(t *testing.T) {
	for _, shell := range completionShells {
		if got := runOffline(t, "completion", shell); got.status != 0 || got.output != completionScripts[shell] || got.errors != "" {
			t.Errorf("string completion %s: exit %d, stderr %q", shell, got.status, got.errors)
		}
	}
	for _, name := range []string{"pwsh", "PowerShell"} {
		if got := runOffline(t, "completion", name); got.output != completionScripts["powershell"] {
			t.Errorf("string completion %s: exit %d, stderr %q", name, got.status, got.errors)
		}
	}
	got, report := runWith(t, nil, map[string]string{}, "completion", "zhs", "--json")
	next, _ := report["next_commands"].([]any)
	if got.status != exitUsage || got.output != "" || len(next) != 1 || next[0].(map[string]any)["command"] != "string completion zsh" {
		t.Errorf("string completion zhs: %+v", got)
	}
	if got := runOffline(t, "completion"); got.status != exitUsage || got.output != "" {
		t.Errorf("string completion: %+v", got)
	}
}
