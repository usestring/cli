package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fetchAPI stands in for /fetch: it answers page with X-Status-Code 200, and
// counts the requests.
func fetchAPI(t *testing.T, page string) (string, *atomic.Int32) {
	var calls atomic.Int32
	base := api(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("X-Status-Code", "200")
		io.WriteString(w, page)
	})
	return base, &calls
}

// runAs runs string against base with a key, as exec says it runs.
func runAs(base string, exec execCtx, list ...string) result {
	var stdout, stderr bytes.Buffer
	status := run(list, runtime{env: envOf(map[string]string{"STRING_API_KEY": "test-key", "STRING_API_BASE_URL": base}), client: http.DefaultClient, stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr, exec: exec})
	return result{status, stdout.String(), stderr.String()}
}

// A large page says its size first on stderr where no person reads stdout at
// a terminal, with what the agent keeps of it and the command that saves it.
func TestALargePageSaysItsSizeFirst(t *testing.T) {
	page := strings.Repeat("A line of the page that is long enough.\n", 512)
	base, _ := fetchAPI(t, page)
	note := func(cut, command string) string {
		return "note: example.com returned 20,480 bytes (~5,120 tokens). " + cut + "\nSave it and read parts: " + command + " -o page.md\nOr page it: " + command + " --max-tokens 6000\n"
	}
	for _, c := range []struct {
		about string
		exec  execCtx
		list  []string
		said  string
	}{
		{"Claude Code", execCtx{agent: "claude-code"}, []string{"fetch", "https://example.com"}, note("Claude Code keeps only the first 2 KB of output this long.", "string fetch https://example.com")},
		{"an agent string doesn't know", execCtx{agent: "acme-agent"}, []string{"https://example.com", "--mainContentOnly"}, note("Most agents cut output this long.", "string fetch https://example.com --mainContentOnly")},
		{"a pipe", execCtx{stdinTTY: true, stderrTTY: true}, []string{"fetch", "https://example.com"}, note("Most agents cut output this long.", "string fetch https://example.com")},
		{"a person at a terminal", execCtx{stdinTTY: true, stdoutTTY: true, stderrTTY: true}, []string{"fetch", "https://example.com"}, ""},
		{"-q", execCtx{agent: "claude-code"}, []string{"fetch", "https://example.com", "-q"}, ""},
	} {
		if got := runAs(base, c.exec, c.list...); got.status != 0 || got.output != page || got.errors != c.said {
			t.Errorf("%s: exit %d, stderr %q, want %q", c.about, got.status, got.errors, c.said)
		}
	}
	small, _ := fetchAPI(t, strings.Repeat("x", largeBody-1))
	if got := runAs(small, execCtx{agent: "codex"}, "fetch", "https://example.com"); got.errors != "" {
		t.Errorf("a page of 16 KB: %q", got.errors)
	}
}

// -o checks its path before the request, prints a receipt with the outline of
// a Markdown page, and -o - is stdout.
func TestSavingAPageSaysWhatWasSaved(t *testing.T) {
	page := "# Title\n\nAn intro.\n\n## Install\n\n```\n# a comment, not a heading\n```\n### Deep\n#### Too deep\n"
	base, calls := fetchAPI(t, page)
	dir := t.TempDir()
	path := filepath.Join(dir, "page.md")
	got := runAs(base, execCtx{}, "fetch", "https://example.com", "-o", path)
	saved, _ := os.ReadFile(path)
	if want := "saved " + path + " · 90 B · ~23 tokens · 200 · example.com\n  outline: 1 # Title · 5 ## Install · 10 ### Deep\n"; got.status != 0 || got.output != "" || got.errors != want || string(saved) != page {
		t.Errorf("-o: %+v, want stderr %q", got, want)
	}
	// Twenty headings at most, then how many more.
	many, _ := fetchAPI(t, strings.Repeat("## Part\n", 25))
	if got := runAs(many, execCtx{}, "fetch", "https://example.com", "-o", path); !strings.HasSuffix(got.errors, " · 20 ## Part · 5 more\n") {
		t.Errorf("25 headings: %q", got.errors)
	}
	// JSON has no outline, and -q drops the receipt.
	if got := runAs(base, execCtx{}, "fetch", "https://example.com", "--format", "json", "-o", path); strings.Contains(got.errors, "outline") || !strings.HasPrefix(got.errors, "saved "+path+" · 90 B") {
		t.Errorf("--format json -o: %q", got.errors)
	}
	if got := runAs(base, execCtx{}, "fetch", "https://example.com", "-o", path, "-q"); got.status != 0 || got.errors != "" {
		t.Errorf("-o -q: %+v", got)
	}
	// -o - is stdout.
	if got := runAs(base, execCtx{}, "fetch", "https://example.com", "-o", "-"); got.status != 0 || got.output != page || got.errors != "" {
		t.Errorf("-o -: %+v", got)
	}
	// A path that can't take a file fails before the request.
	before := calls.Load()
	for bad, want := range map[string]string{
		filepath.Join(dir, "missing", "page.md"): "error: Can't save to " + filepath.Join(dir, "missing", "page.md") + ": there is no directory " + filepath.Join(dir, "missing") + "\n",
		dir:                                      "error: -o " + dir + " is a directory; name a file in it, as " + filepath.Join(dir, "page.md") + "\n",
	} {
		if got := runAs(base, execCtx{}, "fetch", "https://example.com", "-o", bad); got.status != exitUsage || got.errors != want {
			t.Errorf("-o %s: %+v, want %q", bad, got, want)
		}
	}
	if calls.Load() != before {
		t.Errorf("a bad -o path still sent %d requests", calls.Load()-before)
	}
}

// -q leaves a wait's first line and its heartbeat out, and only the error is
// said.
func TestQuietWaitsSayOnlyHowTheyEnded(t *testing.T) {
	crawl := &crawlAPI{statuses: []string{crawlRunning}}
	got := runCrawl(crawl.start(t), execCtx{agent: "claude-code"}, "sitemap", "status", "job-1", "--wait", "-q")
	if want := "error: Stopped waiting after 90s; the crawl keeps running.\n\nnext: run `string sitemap status job-1 --wait`\n"; got.status != exitTimeout || got.errors != want {
		t.Errorf("--wait -q: exit %d, stderr %q, want %q", got.status, got.errors, want)
	}
}
