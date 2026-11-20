package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// attributionHeaders are the headers that say how string runs.
var attributionHeaders = []string{"User-Agent", "X-String-CLI-Mode", "X-String-Agent", "X-String-Agent-Raw"}

// headerLog stands in for the API and the sign-in service, and keeps the
// attribution headers each request carried, by its path.
type headerLog struct {
	mu   sync.Mutex
	seen map[string]map[string]string
}

func (l *headerLog) start(t *testing.T) string {
	t.Helper()
	l.seen = map[string]map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := map[string]string{}
		for _, name := range attributionHeaders {
			if values := r.Header.Values(name); len(values) > 0 {
				got[name] = strings.Join(values, ",")
			}
		}
		l.mu.Lock()
		l.seen[r.URL.Path] = got
		l.mu.Unlock()
		switch r.URL.Path {
		case "/v1/fetch":
			io.WriteString(w, "# Example Domain\n")
		case "/v1/sitemap":
			io.WriteString(w, `{"jobs":[]}`)
		case "/oauth/device_authorization":
			io.WriteString(w, `{"device_code":"device-1","user_code":"ABCD-EFGH","verification_uri":"https://example.test/device","verification_uri_complete":"https://example.test/device?code=ABCD-EFGH","expires_in":600,"interval":5}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	auth, portal, client := authURL, portalURL, oauthClientID
	t.Cleanup(func() { authURL, portalURL, oauthClientID = auth, portal, client })
	authURL, portalURL, oauthClientID = server.URL, server.URL, "test-client"
	return server.URL
}

// Every request string sends to String, whether to the API or to sign in,
// says that string sent it and how it runs: a person, a script, a CI job, or
// which coding agent. DO_NOT_TRACK leaves only the version.
func TestRequestsSayHowStringRuns(t *testing.T) {
	product := "string-cli/" + version
	platform := product + " (" + goruntime.GOOS + "/" + goruntime.GOARCH
	for _, c := range []struct {
		about string
		exec  execCtx
		want  map[string]string
	}{
		{"a person at a terminal", execCtx{stdinTTY: true, stdoutTTY: true, stderrTTY: true},
			map[string]string{"User-Agent": platform + ")", "X-String-CLI-Mode": "interactive"}},
		{"a script", execCtx{},
			map[string]string{"User-Agent": platform + ")", "X-String-CLI-Mode": "non-interactive"}},
		{"a CI job", execCtx{ci: true},
			map[string]string{"User-Agent": platform + ")", "X-String-CLI-Mode": "ci"}},
		{"Claude Code", execCtx{agent: "claude-code", agentFrom: "AI_AGENT", agentRaw: "claude-code_2-1-322_agent"},
			map[string]string{"User-Agent": platform + "; agent=claude-code)", "X-String-CLI-Mode": "agent", "X-String-Agent": "claude-code", "X-String-Agent-Raw": "claude-code_2-1-322_agent"}},
		{"Codex in a terminal", execCtx{stdinTTY: true, stdoutTTY: true, stderrTTY: true, agent: "codex", agentFrom: "CODEX_CI"},
			map[string]string{"User-Agent": platform + "; agent=codex)", "X-String-CLI-Mode": "agent", "X-String-Agent": "codex"}},
		{"an agent under DO_NOT_TRACK", execCtx{agent: "claude-code", agentFrom: "AI_AGENT", agentRaw: "claude-code_2-1-322_agent", doNotTrack: true},
			map[string]string{"User-Agent": product}},
	} {
		var log headerLog
		base := log.start(t)
		home := t.TempDir()
		env := map[string]string{"HOME": home, "STRING_API_BASE_URL": base + "/v1"}
		command := func(stdin string, list ...string) {
			t.Helper()
			var stdout, stderr bytes.Buffer
			rt := runtime{
				env:      envOf(env),
				lookPath: func(string) (string, error) { return "/usr/local/bin/string", nil },
				client:   http.DefaultClient,
				stdin:    strings.NewReader(stdin),
				stdout:   &stdout,
				stderr:   &stderr,
				sleep:    func(time.Duration) {},
				exec:     c.exec,
			}
			// login --with-token reads the key from stdin, which a terminal
			// can't give it.
			if list[0] == "login" && stdin != "" {
				rt.exec.stdinTTY = false
			}
			if status := run(list, rt); status != 0 {
				t.Fatalf("%s: string %s: exit %d: %s%s", c.about, strings.Join(list, " "), status, stdout.String(), stderr.String())
			}
		}
		command("", "fetch", "https://example.com")
		command("", "login", "--no-wait")
		command("test-key\n", "login", "--with-token")

		for _, path := range []string{"/v1/fetch", "/oauth/device_authorization", "/v1/sitemap"} {
			got, sent := log.seen[path]
			if !sent {
				t.Errorf("%s: nothing reached %s", c.about, path)
				continue
			}
			want := c.want
			// The mode is how this one command ran.
			if path == "/v1/sitemap" && c.exec.stdinTTY && want["X-String-CLI-Mode"] == "interactive" {
				want = map[string]string{"User-Agent": want["User-Agent"], "X-String-CLI-Mode": "non-interactive"}
			}
			for _, name := range attributionHeaders {
				if got[name] != want[name] {
					t.Errorf("%s: %s carried %s %q, want %q", c.about, path, name, got[name], want[name])
				}
			}
		}
	}
}
