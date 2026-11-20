package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// crawlAPI stands in for String's sitemap endpoints: a quote, an approval,
// then one status per poll from statuses, repeating the last.
type crawlAPI struct {
	statuses []string
	polls    int
}

// crawlPollsAtMost ends a crawl that a wait polls this often, so a wait that
// never stops by itself fails its test instead of hanging it.
const crawlPollsAtMost = 200

func (a *crawlAPI) start(t *testing.T) string {
	return api(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/sitemap":
			io.WriteString(w, `{"jobId":"job-1","status":"awaiting_approval","estimatedCostUsd":"1.05","estimatedPages":1050}`)
		case "POST /v1/sitemap/job-1/approve":
			io.WriteString(w, `{"jobId":"job-1","status":"running"}`)
		case "GET /v1/sitemap/job-1":
			status := a.statuses[min(a.polls, len(a.statuses)-1)]
			if a.polls >= crawlPollsAtMost {
				status = `{"jobId":"job-1","status":"completed"}`
			}
			io.WriteString(w, status)
			a.polls++
		default:
			http.NotFound(w, r)
		}
	})
}

const crawlRunning = `{"jobId":"job-1","status":"running","pending":3400,"processed":1200}`

// runCrawl runs string against base with a clock that moves only when string
// pauses, so a wait of minutes takes no time.
func runCrawl(base string, exec execCtx, list ...string) result {
	clock := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var stdout, stderr bytes.Buffer
	status := run(list, runtime{
		env:    envOf(map[string]string{"STRING_API_KEY": "test-key", "STRING_API_BASE_URL": base}),
		client: http.DefaultClient,
		stdin:  strings.NewReader(""),
		stdout: &stdout,
		stderr: &stderr,
		sleep:  func(d time.Duration) { clock = clock.Add(d) },
		now:    func() time.Time { return clock },
		exec:   exec,
	})
	return result{status, stdout.String(), stderr.String()}
}

// Under an agent, --wait first says how to resume, prints a plain line every
// 15s, and stops after 90s with exit 7, leaving the crawl running.
func TestSitemapWaitUnderAnAgentStopsAfter90s(t *testing.T) {
	crawl := &crawlAPI{statuses: []string{crawlRunning}}
	base := crawl.start(t)
	got := runCrawl(base, execCtx{agent: "claude-code"}, "sitemap", "submit", "https://example.com", "--approve", "--wait")

	want := "job job-1 · running · up to $1.05 · resume: string sitemap status job-1 --wait\n"
	for _, at := range []string{"15s", "30s", "45s", "1m", "1m15s"} {
		want += "waiting: running · 1,200 pages done, 3,400 queued · " + at + "\n"
	}
	want += "Stopped waiting after 90s; the crawl keeps running.\nNext: run `string sitemap status job-1 --wait`\n"
	if got.status != exitTimeout || got.errors != want {
		t.Errorf("exit %d, stderr:\n%s\nwant exit 7 and:\n%s", got.status, got.errors, want)
	}
	// It polled every 3s from 0s to 87s, and the last status is the output.
	if crawl.polls != 30 {
		t.Errorf("%d polls, want 30", crawl.polls)
	}
	expectJSON(t, got.output, crawlRunning)

	crawl.polls = 0
	got = runCrawl(base, execCtx{agent: "claude-code"}, "sitemap", "status", "job-1", "--wait", "--json")
	lines := strings.Split(strings.TrimSpace(got.errors), "\n")
	expectJSON(t, lines[len(lines)-1], "{\"error\":{\"code\":\"wait_timeout\",\"message\":\"Stopped waiting after 90s; the crawl keeps running.\",\"next_step\":\"run `string sitemap status job-1 --wait`\",\"retry_after_seconds\":null}}")
	if got.status != exitTimeout || lines[0] != "job job-1 · running · resume: string sitemap status job-1 --wait" {
		t.Errorf("status --wait --json: %+v", got)
	}
}

// --timeout sets the wait's limit for anyone, 0 lifts it, and a person or a
// script waits until the crawl ends.
func TestSitemapWaitLastsAsLongAsItsLimit(t *testing.T) {
	// 40 polls take 2 minutes, past an agent's 90s.
	statuses := make([]string, 0, 41)
	for range 40 {
		statuses = append(statuses, crawlRunning)
	}
	statuses = append(statuses, `{"jobId":"job-1","status":"completed","pagesProcessed":4600}`)
	for _, c := range []struct {
		about   string
		exec    execCtx
		flags   []string
		stopped string
	}{
		{about: "a script", exec: execCtx{}},
		{about: "a person at a terminal", exec: execCtx{stdinTTY: true, stdoutTTY: true, stderrTTY: true}},
		{about: "an agent with --timeout 0", exec: execCtx{agent: "codex"}, flags: []string{"--timeout", "0"}},
		{about: "an agent with --timeout 5m", exec: execCtx{agent: "codex"}, flags: []string{"--timeout", "5m"}},
		{about: "a script with --timeout 30s", exec: execCtx{}, flags: []string{"--timeout", "30s"}, stopped: "Stopped waiting after 30s; the crawl keeps running."},
		{about: "a person with --timeout=1m", exec: execCtx{stdinTTY: true, stdoutTTY: true, stderrTTY: true}, flags: []string{"--timeout=1m"}, stopped: "Stopped waiting after 60s; the crawl keeps running."},
	} {
		crawl := &crawlAPI{statuses: statuses}
		got := runCrawl(crawl.start(t), c.exec, append([]string{"sitemap", "status", "job-1", "--wait"}, c.flags...)...)
		switch {
		case c.stopped == "" && (got.status != 0 || crawl.polls != 41):
			t.Errorf("%s: exit %d after %d polls, want 0 after 41: %s", c.about, got.status, crawl.polls, got.errors)
		case c.stopped != "" && (got.status != exitTimeout || !strings.Contains(got.errors, c.stopped+"\nNext: run `string sitemap status job-1 --wait`\n")):
			t.Errorf("%s: exit %d: %s", c.about, got.status, got.errors)
		}
	}
}

// A person who sees stderr at a terminal gets one line redrawn in place; an
// agent's terminal gets the plain lines, with no carriage returns to pile up.
func TestSitemapWaitRedrawsOnlyForAPerson(t *testing.T) {
	// The command's own request takes the first status, and the wait polls
	// the rest every 3s, so the seventh running one comes at 15s.
	statuses := []string{crawlRunning, crawlRunning, crawlRunning, crawlRunning, crawlRunning, crawlRunning, crawlRunning, `{"jobId":"job-1","status":"completed"}`}
	person := runCrawl((&crawlAPI{statuses: statuses}).start(t), execCtx{stderrTTY: true}, "sitemap", "status", "job-1", "--wait")
	if person.status != 0 || !strings.Contains(person.errors, "\r\x1b[KCrawling: 1200 pages done, 3400 queued") || strings.Contains(person.errors, "waiting:") || !strings.HasSuffix(person.errors, "queued\n") {
		t.Errorf("a person: %+v", person)
	}
	agent := runCrawl((&crawlAPI{statuses: statuses}).start(t), execCtx{stdinTTY: true, stdoutTTY: true, stderrTTY: true, agent: "codex"}, "sitemap", "status", "job-1", "--wait")
	if want := "job job-1 · running · resume: string sitemap status job-1 --wait\nwaiting: running · 1,200 pages done, 3,400 queued · 15s\n"; agent.status != 0 || agent.errors != want {
		t.Errorf("an agent at a terminal: %+v, want stderr %q", agent, want)
	}
}

// A status string doesn't know ends the wait with exit 7 and names the
// status, where the wait used to go on forever.
func TestSitemapWaitStopsOnAStatusItDoesNotKnow(t *testing.T) {
	for status, message := range map[string]string{
		`{"jobId":"job-1","status":"paused","pending":1,"processed":2}`: `Stopped waiting: the crawl's status is "paused", which string doesn't know.`,
		`{"jobId":"job-1"}`: "Stopped waiting: the answer gave the crawl no status.",
		`not json`:          "Stopped waiting: the answer gave the crawl no status.",
	} {
		crawl := &crawlAPI{statuses: []string{crawlRunning, status}}
		got := runCrawl(crawl.start(t), execCtx{}, "sitemap", "status", "job-1", "--wait")
		if want := message + "\nNext: run `string sitemap status job-1 --wait`\n"; got.status != exitTimeout || !strings.HasSuffix(got.errors, want) || crawl.polls != 2 {
			t.Errorf("%s: exit %d after %d polls: %s", status, got.status, crawl.polls, got.errors)
		}
	}
}

// A poll that is still going when the wait's limit passes ends with it, as a
// stopped wait rather than a request that timed out.
func TestSitemapWaitEndsAPollAtItsLimit(t *testing.T) {
	release := make(chan struct{})
	gets := 0
	base := api(t, func(w http.ResponseWriter, r *http.Request) {
		if gets++; gets == 1 {
			io.WriteString(w, crawlRunning)
			return
		}
		select {
		case <-r.Context().Done():
		case <-release:
		case <-time.After(10 * time.Second):
			io.WriteString(w, `{"jobId":"job-1","status":"completed"}`)
		}
	})
	t.Cleanup(func() { close(release) })
	var stdout, stderr bytes.Buffer
	status := run([]string{"sitemap", "status", "job-1", "--wait", "--timeout", "200ms"}, runtime{
		env:    envOf(map[string]string{"STRING_API_KEY": "test-key", "STRING_API_BASE_URL": base}),
		client: http.DefaultClient,
		stdout: &stdout,
		stderr: &stderr,
		sleep:  func(time.Duration) {},
	})
	if want := "Stopped waiting after 200ms; the crawl keeps running.\nNext: run `string sitemap status job-1 --wait`\n"; status != exitTimeout || !strings.HasSuffix(stderr.String(), want) {
		t.Errorf("exit %d: %s", status, stderr.String())
	}
}

// pollLog stands in for a crawl that keeps running, and keeps how long each
// request had left before its deadline. first runs as the first request is
// answered.
type pollLog struct {
	left  []time.Duration
	first func()
}

func (p *pollLog) RoundTrip(r *http.Request) (*http.Response, error) {
	deadline, _ := r.Context().Deadline()
	p.left = append(p.left, time.Until(deadline))
	if len(p.left) == 1 {
		p.first()
	}
	answer := crawlRunning
	if len(p.left) > crawlPollsAtMost {
		answer = `{"jobId":"job-1","status":"completed"}`
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(answer)), Request: r}, nil
}

// The wait's limit counts from the command's first request, and each poll
// may take only what is left, so a slow answer can't carry an agent's
// command past its 90s.
func TestSitemapWaitGivesEachPollOnlyWhatIsLeft(t *testing.T) {
	clock := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	// The command's own request takes 30s.
	polls := &pollLog{first: func() { clock = clock.Add(30 * time.Second) }}
	var stdout, stderr bytes.Buffer
	status := run([]string{"sitemap", "status", "job-1", "--wait"}, runtime{
		env:    envOf(map[string]string{"STRING_API_KEY": "test-key", "STRING_API_BASE_URL": "https://api.example.test/v1"}),
		client: &http.Client{Transport: polls},
		stdout: &stdout,
		stderr: &stderr,
		sleep:  func(d time.Duration) { clock = clock.Add(d) },
		now:    func() time.Time { return clock },
		exec:   execCtx{agent: "claude-code"},
	})
	// The command's own request, then a poll every 3s from 30s to 87s.
	if status != exitTimeout || len(polls.left) != 21 {
		t.Fatalf("exit %d after %d requests: %s", status, len(polls.left), stderr.String())
	}
	for i, left := range polls.left[1:] {
		at := 30*time.Second + time.Duration(3*i)*time.Second
		if want := agentWaitLimit - at; left > want || left < want-2*time.Second {
			t.Errorf("the poll at %v had %v left, want %v", at, left, want)
		}
	}
}
