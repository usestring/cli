package main

import (
	"bytes"
	"errors"
	"net/http"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
)

// noNetwork fails the test on any request, since help and version send none.
type noNetwork struct{ t *testing.T }

func (n noNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	n.t.Errorf("a request went to %s", r.URL)
	return nil, errors.New("no network in this test")
}

// runOffline runs string with no network and no key.
func runOffline(t *testing.T, list ...string) result {
	var stdout, stderr bytes.Buffer
	status := run(list, runtime{
		env:    envOf(map[string]string{"HOME": t.TempDir()}),
		client: &http.Client{Transport: noNetwork{t}},
		stdout: &stdout,
		stderr: &stderr,
	})
	return result{status, stdout.String(), stderr.String()}
}

// Every command has its own help page, the same whether asked for with
// --help, with -h anywhere before --, or with string help COMMAND, and none
// of them sends a request.
func TestEveryCommandHasItsOwnHelp(t *testing.T) {
	asked := map[string][][]string{
		"fetch":        {{"fetch", "https://example.com", "-h"}, {"https://example.com", "--mainContentOnly", "-h"}},
		"search":       {{"search", "coffee", "-h"}},
		"request":      {{"request", "POST", "https://example.com", "-h"}},
		"sitemap":      {{"sitemap", "submit", "https://example.com", "-h"}, {"sitemap", "status", "job-1", "--help"}, {"help", "sitemap", "status"}},
		"integrations": {{"integrations", "list", "-h"}},
		"init":         {{"init", "--no-login", "-h"}},
		"login":        {{"login", "--poll", "-h"}},
		"trial":        {{"trial", "claim", "--poll", "-h"}, {"help", "trial", "claim"}},
		"update":       {{"update", "--check", "-h"}},
	}
	for command, doc := range commandDocs {
		want := doc.page(command)
		ways := append([][]string{{command, "--help"}, {command, "-h"}, {"-h", command}, {"help", command}}, asked[command]...)
		for _, list := range ways {
			if got := runOffline(t, list...); got.status != 0 || got.output != want || got.errors != "" {
				t.Errorf("string %s: exit %d, stderr %q, and stdout:\n%s\nwant the %s page", strings.Join(list, " "), got.status, got.errors, got.output, command)
			}
		}
		for _, section := range []string{"\n\nUsage\n  string ", "\n\nExamples\n  string ", "\n\nReturns\n  stdout   ", "\n  exit     ", "\n\nCost\n  ", "\nLearn more: https://"} {
			if !strings.Contains(want, section) {
				t.Errorf("the %s page has no %q", command, strings.TrimSpace(section))
			}
		}
		if !strings.HasPrefix(want, "string "+command+": ") || len(doc.examples) < 1 || len(doc.examples) > 4 {
			t.Errorf("the %s page starts %q and has %d examples, want a summary line and 1 to 4", command, want[:min(len(want), 40)], len(doc.examples))
		}
	}
	// -h after -- is an argument like any other.
	if got := helpAnywhere([]string{"search", "--", "-h"}); !slices.Equal(got, []string{"search", "--", "-h"}) {
		t.Errorf("helpAnywhere took -h after --: %v", got)
	}
	// Bare string, --help, -h and help still print the help for all of string.
	for _, list := range [][]string{{}, {"--help"}, {"-h"}, {"help"}, {"help", "--help"}} {
		if got := runOffline(t, list...); got.status != 0 || got.output != help {
			t.Errorf("string %s: exit %d, want the help for all of string", strings.Join(list, " "), got.status)
		}
	}
	// A command string doesn't have is an error, as it is without help.
	if got := runOffline(t, "help", "bogus"); got.status != exitUsage || got.output != "" || got.errors != "error: Unknown command: bogus\n\nnext: run `string --help` for the commands\n" {
		t.Errorf("string help bogus: %+v", got)
	}
}

// -v and --version print the version alone; string version adds the build.
func TestVersionSaysWhichBuildThisIs(t *testing.T) {
	for _, list := range [][]string{{"-v"}, {"--version"}} {
		if got := runOffline(t, list...); got.status != 0 || got.output != "string "+version+"\n" {
			t.Errorf("string %s: %+v", list[0], got)
		}
	}
	got := runOffline(t, "version")
	lines := strings.Split(got.output, "\n")
	if got.status != 0 || len(lines) != 4 || lines[0] != "string "+version || !strings.HasPrefix(lines[1], "commit ") || lines[2] != goruntime.GOOS+"/"+goruntime.GOARCH+", "+goruntime.Version() {
		t.Errorf("string version: %+v", got)
	}
	got = runOffline(t, "version", "--json")
	shown, _ := decode(t, got.output).(map[string]any)
	if got.status != 0 || shown["version"] != version || shown["os"] != goruntime.GOOS || shown["arch"] != goruntime.GOARCH || shown["go"] != goruntime.Version() {
		t.Errorf("string version --json: %+v", got)
	}
	if got := runOffline(t, "version", "now"); got.status != exitUsage {
		t.Errorf("string version now: %+v", got)
	}
}
