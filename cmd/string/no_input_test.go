package main

import (
	"bytes"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// The global switches come out wherever they are before "--", and nothing
// after it is touched.
func TestGlobalSwitchesComeOutAnywhereBeforeTheDoubleDash(t *testing.T) {
	for _, c := range []struct {
		list, kept []string
		want       globalSwitches
	}{
		{[]string{"login", "--no-input"}, []string{"login"}, globalSwitches{noInput: true}},
		{[]string{"--json", "--no-open", "login", "--no-wait"}, []string{"login", "--no-wait"}, globalSwitches{json: true, noOpen: true}},
		{[]string{"-y", "fetch", "https://example.com", "--yes"}, []string{"fetch", "https://example.com"}, globalSwitches{}},
		{[]string{"--timeout", "90s", "fetch", "https://example.com"}, []string{"fetch", "https://example.com"}, globalSwitches{hasTimeout: true, timeout: "90s"}},
		{[]string{"search", "golang", "--timeout=5m", "--json"}, []string{"search", "golang"}, globalSwitches{json: true, hasTimeout: true, timeout: "5m"}},
		{[]string{"login", "--poll", "--timeout"}, []string{"login", "--poll"}, globalSwitches{hasTimeout: true}},
		{[]string{"search", "--", "--no-input", "-y", "--json", "--timeout", "5"}, []string{"search", "--", "--no-input", "-y", "--json", "--timeout", "5"}, globalSwitches{}},
	} {
		kept, got := takeGlobalSwitches(c.list)
		if !slices.Equal(kept, c.kept) || got != c.want {
			t.Errorf("%q: kept %q and %+v, want %q and %+v", c.list, kept, got, c.kept, c.want)
		}
	}
	// No command asks anything yet, so -y and --yes change nothing.
	for _, list := range [][]string{{"-y", "--version"}, {"--version", "--yes"}} {
		var stdout, stderr bytes.Buffer
		if status := run(list, runtime{stdout: &stdout, stderr: &stderr}); status != 0 || stdout.String() != "string "+version+"\n" {
			t.Errorf("%q: exit %d: %q %q", list, status, stdout.String(), stderr.String())
		}
	}
}

// login and init wait for an approval only for a person at a terminal. Under
// an agent, in CI, or with --no-input or STRING_NO_INPUT, login prints the link
// and code and returns at once, and init hands them back the same way, except
// in CI, where nobody could approve and it stays keyless.
func TestSignInWaitsOnlyForAPersonAtATerminal(t *testing.T) {
	const handedBack = "Then run string login --poll to save the key. The code expires in 10 minutes.\n"
	for _, c := range []struct {
		about string
		env   map[string]string
		flags []string
		// init is what init does: "wait", "hand back" or "keyless".
		init  string
		waits bool
	}{
		{about: "a person at a terminal", init: "wait", waits: true},
		{about: "Claude Code at a terminal", env: map[string]string{"AI_AGENT": "claude-code_2-1-322_agent"}, init: "hand back"},
		{about: "an agent named by STRING_AGENT at a terminal", env: map[string]string{"STRING_AGENT": "my-bot"}, init: "hand back"},
		{about: "CI at a terminal", env: map[string]string{"CI": "true"}, init: "keyless"},
		{about: "--no-input at a terminal", flags: []string{"--no-input"}, init: "hand back"},
		{about: "STRING_NO_INPUT=1 at a terminal", env: map[string]string{"STRING_NO_INPUT": "1"}, init: "hand back"},
	} {
		for _, command := range []string{"login", "init"} {
			service := &signInService{token: []signInAnswer{signInApproved}, key: []signInAnswer{signInKey}}
			service.start(t)
			session := newSignInSession(t)
			session.terminal, session.progress = true, true
			maps.Copy(session.env, c.env)
			got := session.run(append([]string{command}, c.flags...)...)
			waited := service.requests("/oauth/token") > 0
			if got.status != 0 {
				t.Errorf("%s: %s: %+v", c.about, command, got)
				continue
			}
			switch want := c.init; {
			case command == "login" && waited != c.waits:
				t.Errorf("%s: login waited for the approval: %t, want %t: %+v", c.about, waited, c.waits, got)
			case command == "login" && !c.waits && !strings.HasSuffix(got.output, handedBack):
				t.Errorf("%s: login printed %q", c.about, got.output)
			case command == "init" && waited != (want == "wait"):
				t.Errorf("%s: init waited for the approval: %t, want %s: %+v", c.about, waited, want, got)
			case command == "init" && want == "hand back" && !strings.HasSuffix(got.output, handedBack):
				t.Errorf("%s: init printed %q", c.about, got.output)
			case command == "init" && want == "keyless" && (service.requests("") > 0 || !strings.HasSuffix(got.output, "\n\n"+signInHint)):
				t.Errorf("%s: init sent %d requests and printed %q", c.about, service.requests(""), got.output)
			}
		}
	}
}

// trial claim waits for the person to claim the agent only when one is at the
// terminal, and names --email when it is missing.
func TestTrialClaimWaitsOnlyForAPersonAtATerminal(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agent-signup/identity/claim":
			io.WriteString(w, claimAttemptAnswer)
		case "/api/agent-signup/token":
			polls++
			io.WriteString(w, `{"access_token":"claimed-key","token_type":"Bearer","organization_id":9}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	defer func(p string) { portalURL = p }(portalURL)
	portalURL = server.URL

	person := execCtx{stdinTTY: true, stdoutTTY: true, stderrTTY: true}
	for _, c := range []struct {
		about string
		exec  execCtx
		flags []string
		waits bool
	}{
		{"a person at a terminal", person, nil, true},
		{"an agent at a terminal", execCtx{stdinTTY: true, stdoutTTY: true, stderrTTY: true, agent: "claude-code"}, nil, false},
		{"--no-input at a terminal", person, []string{"--no-input"}, false},
	} {
		home, _ := claimHome(t)
		polls = 0
		var stdout, stderr bytes.Buffer
		status := run(append([]string{"trial", "claim", "--email", "dev@localhost"}, c.flags...), runtime{
			env:    envOf(map[string]string{"HOME": home}),
			client: http.DefaultClient,
			stdout: &stdout,
			stderr: &stderr,
			sleep:  func(time.Duration) {},
			exec:   c.exec,
		})
		if status != 0 || (polls > 0) != c.waits {
			t.Errorf("%s: exit %d after %d polls, want a wait: %t: %s%s", c.about, status, polls, c.waits, stdout.String(), stderr.String())
		}
		if !c.waits && !strings.Contains(stdout.String(), "string trial claim --poll") {
			t.Errorf("%s: printed %q", c.about, stdout.String())
		}
	}

	var stdout, stderr bytes.Buffer
	rt := runtime{env: envOf(map[string]string{"HOME": t.TempDir()}), stdout: &stdout, stderr: &stderr}
	const want = "trial claim needs --email ADDRESS (or --poll to finish a claim already started)"
	if status := run([]string{"trial", "claim"}, rt); status != exitUsage || !strings.Contains(stderr.String(), want) {
		t.Errorf("trial claim without --email: exit %d: %s", status, stderr.String())
	}
	stderr.Reset()
	if status := run([]string{"trial", "claim", "--json"}, rt); status != exitUsage || !strings.Contains(stderr.String(), `"code":"missing_input"`) {
		t.Errorf("trial claim --json without --email: exit %d: %s", status, stderr.String())
	}
}
