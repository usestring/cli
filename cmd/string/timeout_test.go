package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTimeoutTakesADurationOrSeconds(t *testing.T) {
	for value, want := range map[string]time.Duration{"90s": 90 * time.Second, "5m": 5 * time.Minute, "1m30s": 90 * time.Second, "90": 90 * time.Second, "0": 0, "0s": 0, "250ms": 250 * time.Millisecond} {
		if got, err := parseTimeout(value); err != nil || got != want {
			t.Errorf("%q: %v, %v; want %v", value, got, err, want)
		}
	}
	for _, value := range []string{"", "-5s", "-1", "1.5", "5 m", "soon", "--json"} {
		if got, err := parseTimeout(value); err == nil {
			t.Errorf("%q: %v, want an error", value, got)
		}
	}
	// A bad value, or none, exits 2 before anything is sent.
	for _, list := range [][]string{{"fetch", "https://example.com", "--timeout", "soon"}, {"search", "golang", "--timeout"}} {
		got := invoke(t, list, "http://127.0.0.1:1/v1", "test-key")
		if got.status != exitUsage || !strings.Contains(got.errors, "--timeout takes a duration such as 90s or 5m, a number of seconds, or 0 for no limit") {
			t.Errorf("%q: %+v", list, got)
		}
	}
}

// limitLog stands in for String, and keeps how long each request had left
// before its deadline, by path: -1 for no deadline.
type limitLog struct {
	mu   sync.Mutex
	left map[string]time.Duration
}

func (l *limitLog) RoundTrip(r *http.Request) (*http.Response, error) {
	left := time.Duration(-1)
	if deadline, ok := r.Context().Deadline(); ok {
		left = time.Until(deadline)
	}
	l.mu.Lock()
	l.left[r.URL.Path] = left
	l.mu.Unlock()
	answer := `{}`
	switch r.URL.Path {
	case "/v1/fetch":
		answer = "# Example Domain\n"
	case "/v1/integrations/reddit":
		answer = `{"sites":[{"name":"reddit","actions":[{"name":"post","input":{"properties":{"post":{"type":"string"}}}}]}]}`
	case "/oauth/device_authorization":
		answer = `{"device_code":"device-1","user_code":"ABCD-EFGH","verification_uri":"https://example.test/device","verification_uri_complete":"https://example.test/device?code=ABCD-EFGH","expires_in":600,"interval":5}`
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(answer)), Request: r}, nil
}

// Each request to String, to the API or to sign in, has a time limit: 120s,
// 300s for an integration's action, 90s for any request under an agent, or
// what --timeout says, where 0 means none.
func TestEachRequestHasATimeLimit(t *testing.T) {
	auth, client := authURL, oauthClientID
	t.Cleanup(func() { authURL, oauthClientID = auth, client })
	authURL, oauthClientID = "https://auth.example.test", "test-client"
	const none = -1
	for _, c := range []struct {
		about                           string
		agent                           string
		flags                           []string
		fetch, manifest, action, signIn time.Duration
	}{
		{"a person", "", nil, 120 * time.Second, 120 * time.Second, 300 * time.Second, 120 * time.Second},
		{"an agent", "claude-code", nil, 90 * time.Second, 90 * time.Second, 90 * time.Second, 90 * time.Second},
		{"--timeout 5m", "", []string{"--timeout", "5m"}, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute},
		{"an agent with --timeout=45", "claude-code", []string{"--timeout=45"}, 45 * time.Second, 45 * time.Second, 45 * time.Second, 45 * time.Second},
		{"--timeout 0", "", []string{"--timeout", "0"}, none, none, none, none},
	} {
		log := &limitLog{left: map[string]time.Duration{}}
		env := map[string]string{"HOME": t.TempDir(), "STRING_API_KEY": "test-key", "STRING_API_BASE_URL": "https://api.example.test/v1"}
		for _, list := range [][]string{{"fetch", "https://example.com"}, {"integrations", "run", "reddit", "post", "--post", "1"}, {"login", "--no-wait"}} {
			var stdout, stderr bytes.Buffer
			status := run(append(list, c.flags...), runtime{
				env:    envOf(env),
				client: &http.Client{Transport: log},
				stdin:  strings.NewReader(""),
				stdout: &stdout,
				stderr: &stderr,
				sleep:  func(time.Duration) {},
				exec:   execCtx{agent: c.agent},
			})
			if status != 0 {
				t.Fatalf("%s: %q: exit %d: %s%s", c.about, list, status, stdout.String(), stderr.String())
			}
		}
		for path, want := range map[string]time.Duration{"/v1/fetch": c.fetch, "/v1/integrations/reddit": c.manifest, "/v1/integrations/reddit/post": c.action, "/oauth/device_authorization": c.signIn} {
			left, sent := log.left[path]
			switch {
			case !sent:
				t.Errorf("%s: nothing reached %s", c.about, path)
			case want == none && left != none:
				t.Errorf("%s: %s had %v left, want no limit", c.about, path, left)
			case want != none && (left > want || left < want-10*time.Second):
				t.Errorf("%s: %s had %v left, want %v", c.about, path, left, want)
			}
		}
	}
}

// A request that gets no answer within its limit exits 7 and says how to
// allow longer. So does one whose answer stops arriving, whether after its
// headers or partway through its body.
func TestARequestThatRunsOutOfTimeExits7(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sitemap":
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
		case "/v1/search":
			io.WriteString(w, `{"results":[`)
			w.(http.Flusher).Flush()
		}
		// Waiting for string to give up, or long enough to fail the test
		// rather than hang it if string never does.
		select {
		case <-r.Context().Done():
		case <-release:
		case <-time.After(10 * time.Second):
			io.WriteString(w, "{}")
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	defer func(limit time.Duration) { bodyIdleLimit = limit }(bodyIdleLimit)
	bodyIdleLimit = 50 * time.Millisecond
	base, host := server.URL+"/v1", strings.TrimPrefix(server.URL, "http://")

	got := invoke(t, []string{"fetch", "https://example.com", "--timeout", "50ms"}, base, "test-key")
	if want := "error: " + host + " took longer than 50ms to answer, so string stopped waiting.\n\nnext: run it again with --timeout 100ms\n"; got.status != exitTimeout || got.errors != want {
		t.Errorf("fetch: %+v, want exit 7 and %q", got, want)
	}
	got = invoke(t, []string{"--json", "--timeout=50ms", "fetch", "https://example.com"}, base, "test-key")
	expectJSON(t, got.errors, `{"error":{"code":"timeout","message":"`+host+` took longer than 50ms to answer, so string stopped waiting.","next_step":"run it again with --timeout 100ms","retry_after_seconds":null,"exit_code":7,"retryable":true,"user_action_required":false,"next_commands":[{"type":"retry","why":"run it again with --timeout 100ms"}]}}`)
	if got.status != exitTimeout {
		t.Errorf("fetch --json: %+v", got)
	}

	quiet := "error: " + host + " stopped sending its answer: nothing arrived for 50ms.\n\nnext: run it again\n"
	for _, list := range [][]string{{"sitemap", "list"}, {"search", "golang"}} {
		if got := invoke(t, list, base, "test-key"); got.status != exitTimeout || got.errors != quiet {
			t.Errorf("%q: %+v, want exit 7 and %q", list, got, quiet)
		}
	}
}

// The way out of a limit is a longer one, which an agent whose shell stops
// commands at 2 minutes has to run in the background.
func TestARequestOverItsLimitOffersALongerOne(t *testing.T) {
	for _, c := range []struct {
		limit time.Duration
		agent bool
		next  string
	}{
		{120 * time.Second, false, "run it again with --timeout 4m"},
		{300 * time.Second, false, "run it again with --timeout 10m"},
		{90 * time.Second, true, "run it again with --timeout 3m, in the background if your shell stops commands after 2 minutes"},
		{30 * time.Second, true, "run it again with --timeout 60s"},
		{75 * time.Second, false, "run it again with --timeout 2m30s"},
	} {
		got := tookTooLong("request.usestring.ai", c.limit, c.agent)
		if got.exit != exitTimeout || got.code != "timeout" || got.nextStep != c.next {
			t.Errorf("%v, agent %t: %+v, want next %q", c.limit, c.agent, got, c.next)
		}
	}
}
