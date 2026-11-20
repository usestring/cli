package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	crawlGoesOn = "error: Interrupted; the crawl job-1 keeps running.\n\nnext: run `string sitemap status job-1 --wait`\n"
	mayBeDone   = "error: Interrupted; the request may already have been processed.\n"
)

// A Ctrl-C says what keeps running and the command that picks it up, or that
// a request may already have done what it asked. A read needs no word, and
// once the command is done nothing is left to say.
func TestCtrlCSaysWhatKeepsRunningOrMayHaveHappened(t *testing.T) {
	person := execCtx{stdinTTY: true, stdoutTTY: true, stderrTTY: true}
	for _, c := range []struct {
		about string
		exec  execCtx
		list  []string
		// at is the request a Ctrl-C comes during: its method, path and how
		// many requests of that kind came before it and it.
		at   string
		said string
	}{
		{"a crawl's wait", execCtx{}, []string{"sitemap", "status", "job-1", "--wait"}, "GET /v1/sitemap/job-1 2", crawlGoesOn},
		{"a crawl's wait for a person", person, []string{"sitemap", "status", "job-1", "--wait"}, "GET /v1/sitemap/job-1 2", "\r\x1b[K" + crawlGoesOn},
		{"a crawl's wait under --json", execCtx{agent: "codex"}, []string{"sitemap", "status", "job-1", "--wait", "--json"}, "GET /v1/sitemap/job-1 2", `{"error":{"code":"interrupted","exit_code":130,"message":"Interrupted; the crawl job-1 keeps running.","next_commands":[{"type":"wait","command":"string sitemap status job-1 --wait","argv":["string","sitemap","status","job-1","--wait"]}],"next_step":"run ` + "`string sitemap status job-1 --wait`" + `","retry_after_seconds":null,"retryable":false,"user_action_required":false}}` + "\n"},
		{"a submit", execCtx{}, []string{"sitemap", "submit", "https://example.com"}, "POST /v1/sitemap 1", mayBeDone},
		{"an approval", execCtx{}, []string{"sitemap", "submit", "https://example.com", "--approve", "--wait"}, "POST /v1/sitemap/job-1/approve 1", mayBeDone},
		{"a cancel", execCtx{}, []string{"sitemap", "cancel", "job-1"}, "DELETE /v1/sitemap/job-1 1", mayBeDone},
		{"a request", execCtx{}, []string{"request", "POST", "https://api.example.com/orders", "--body", "{}"}, "POST /v1/fetch 1", mayBeDone},
		{"an integration's action", execCtx{}, []string{"integrations", "run", "reddit", "post", "--post", "1"}, "POST /v1/integrations/reddit/post 1", mayBeDone},
		{"a fetch", execCtx{}, []string{"fetch", "https://example.com"}, "POST /v1/fetch 1", ""},
		{"a crawl's status", execCtx{}, []string{"sitemap", "status", "job-1"}, "GET /v1/sitemap/job-1 1", ""},
	} {
		interrupted := &interruption{}
		var said bytes.Buffer
		var mu sync.Mutex
		seen := map[string]int{}
		base := api(t, func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			kind := r.Method + " " + r.URL.Path
			seen[kind]++
			if fmt.Sprintf("%s %d", kind, seen[kind]) == c.at {
				interrupted.report(&said)
			}
			switch kind {
			case "POST /v1/sitemap":
				io.WriteString(w, `{"jobId":"job-1","status":"awaiting_approval","estimatedCostUsd":"1.05"}`)
			case "POST /v1/sitemap/job-1/approve":
				io.WriteString(w, `{"jobId":"job-1","status":"running"}`)
			case "GET /v1/sitemap/job-1":
				if seen[kind] == 1 {
					io.WriteString(w, crawlRunning)
					return
				}
				io.WriteString(w, `{"jobId":"job-1","status":"completed"}`)
			case "DELETE /v1/sitemap/job-1":
				io.WriteString(w, `{"jobId":"job-1","status":"canceled"}`)
			case "POST /v1/fetch":
				io.WriteString(w, "# Example Domain\n")
			case "GET /v1/integrations/reddit":
				io.WriteString(w, `{"sites":[{"name":"reddit","actions":[{"name":"post","input":{"properties":{"post":{"type":"string"}}}}]}]}`)
			case "POST /v1/integrations/reddit/post":
				io.WriteString(w, `{"envelopeVersion":1,"data":[],"failures":[]}`)
			default:
				http.NotFound(w, r)
			}
		})

		got := runInterruptible(base, c.exec, interrupted, c.list...)

		mu.Lock()
		reached := seen[c.at[:strings.LastIndexByte(c.at, ' ')]]
		mu.Unlock()
		if got.status != 0 || reached == 0 {
			t.Fatalf("%s: exit %d after %v: %s", c.about, got.status, seen, got.errors)
		}
		if said.String() != c.said {
			t.Errorf("%s: a Ctrl-C would say %q, want %q", c.about, said.String(), c.said)
		}
		var after bytes.Buffer
		if interrupted.report(&after); after.Len() != 0 {
			t.Errorf("%s: after the command, a Ctrl-C would still say %q", c.about, after.String())
		}
	}
}

// A Ctrl-C during a sign-in's or a claim's wait says that it stays open, and
// how to finish it.
func TestCtrlCSaysASignInOrClaimStaysOpen(t *testing.T) {
	for _, c := range []struct {
		about, at, said string
		browser         bool
	}{
		{"a device code", "/oauth/token", "error: Interrupted; the sign-in stays open until its code expires.\n\nnext: run `string login --poll`\n", false},
		{"a browser sign-in", "/api/agent-auth/status", "error: Interrupted; the sign-in stays open until its link expires.\n\nnext: run `string login --poll`\n", true},
	} {
		// A browser sign-in is checked once before its link is shown.
		service := &signInService{token: []signInAnswer{signInApproved}, key: []signInAnswer{signInKey}, status: []signInAnswer{browserPending, browserComplete}}
		service.start(t)
		session := newSignInSession(t)
		session.interrupt = &interruption{}
		start := []string{"login", "--no-wait"}
		if c.browser {
			start = append(start, "--browser")
		}
		if got := session.run(start...); got.status != 0 {
			t.Fatalf("%s: %v: %+v", c.about, start, got)
		}
		var said bytes.Buffer
		service.mu.Lock()
		service.during = func(path string) {
			if path == c.at && said.Len() == 0 {
				session.interrupt.report(&said)
			}
		}
		service.mu.Unlock()

		if got := session.run("login", "--poll"); got.status != 0 || said.String() != c.said {
			t.Errorf("%s: %+v, and a Ctrl-C would say %q, want %q", c.about, got, said.String(), c.said)
		}
	}

	var said bytes.Buffer
	interrupted := &interruption{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		interrupted.report(&said)
		io.WriteString(w, `{"access_token":"claimed-key","token_type":"Bearer","organization_id":9}`)
	}))
	t.Cleanup(server.Close)
	defer func(p string) { portalURL = p }(portalURL)
	portalURL = server.URL
	home, dir := claimHome(t)
	registration := trialRegistration{RegistrationID: "reg_7", ClaimToken: "clm_7_secret", ClaimEmail: "dev@localhost", ClaimCodeExpires: time.Now().Add(10 * time.Minute), ClaimInterval: 5}
	if err := writePrivate(filepath.Join(dir, "trial.json"), registration); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	status := run([]string{"trial", "claim", "--poll"}, runtime{env: envOf(map[string]string{"HOME": home}), client: http.DefaultClient, stdout: &stdout, stderr: &stderr, interrupt: interrupted})
	if want := "error: Interrupted; the claim stays open until its code expires.\n\nnext: run `string trial claim --poll`\n"; status != 0 || said.String() != want {
		t.Errorf("a claim: exit %d (%s), and a Ctrl-C would say %q, want %q", status, stderr.String(), said.String(), want)
	}
}

// runInterruptible runs string against base, with interrupted as what a
// Ctrl-C would leave behind, on a clock that moves only when string pauses.
func runInterruptible(base string, exec execCtx, interrupted *interruption, list ...string) result {
	clock := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var stdout, stderr bytes.Buffer
	status := run(list, runtime{
		env:       envOf(map[string]string{"STRING_API_KEY": "test-key", "STRING_API_BASE_URL": base}),
		client:    http.DefaultClient,
		stdin:     strings.NewReader(""),
		stdout:    &stdout,
		stderr:    &stderr,
		sleep:     func(d time.Duration) { clock = clock.Add(d) },
		now:       func() time.Time { return clock },
		exec:      exec,
		interrupt: interrupted,
	})
	return result{status, stdout.String(), stderr.String()}
}
