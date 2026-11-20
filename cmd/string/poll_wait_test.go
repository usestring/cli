package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// signInPending is the token endpoint's answer while nobody has approved.
var signInPending = signInAnswer{http.StatusBadRequest, `{"error":"authorization_pending"}`, ""}

// pollsAtMost ends a wait that asks this often with a final answer, so a wait
// that never stops by itself fails its test instead of hanging it.
const pollsAtMost = 400

// thenExpired is answer pollsAtMost times, then the portal's word that the
// browser sign-in expired.
func thenExpired(answer signInAnswer) []signInAnswer {
	return append(slices.Repeat([]signInAnswer{answer}, pollsAtMost), signInAnswer{http.StatusOK, `{"status":"expired"}`, ""})
}

// heartbeats are the lines a wait of 90s prints, one every 15s, where no
// person watches stderr at a terminal.
func heartbeats(said string) string {
	lines := ""
	for _, at := range []string{"15s", "30s", "45s", "1m", "1m15s"} {
		lines += "waiting: " + said + " · " + at + "\n"
	}
	return lines
}

// lastLine is the last line of output, where --json puts its error after the
// lines a wait prints.
func lastLine(output string) string {
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	return lines[len(lines)-1]
}

// Under an agent, login --poll first says how to resume, prints a line every
// 15s, and stops after 90s with exit 7. The code is still good, and the next
// login --poll finishes with it.
func TestLoginPollUnderAnAgentStopsAfter90sAndKeepsTheCode(t *testing.T) {
	service := &signInService{token: []signInAnswer{signInPending}, key: []signInAnswer{signInKey}}
	service.start(t)
	session := newSignInSession(t)
	session.env["STRING_AGENT"] = "claude-code"

	got := session.startThenPoll(t)

	want := signInWaiting + heartbeats("not approved yet") + "Stopped waiting after 90s; the code expires in 8m30s.\nNext: " + signInPoll + "\n"
	if got.status != exitTimeout || got.output != "" || got.errors != want {
		t.Errorf("exit %d, stdout %q, stderr:\n%s\nwant exit 7 and:\n%s", got.status, got.output, got.errors, want)
	}
	// It asked every 5s from 5s to 85s, then waited out the last 5s.
	if polls := len(service.codes()); polls != 17 || len(session.waits) != 18 {
		t.Errorf("%d polls after the waits %v, want 17 after 18 waits", polls, session.waits)
	}
	if _, err := os.Stat(session.file("pending.json")); err != nil {
		t.Errorf("pending.json is gone after the wait stopped: %v", err)
	}

	service.mu.Lock()
	service.token = []signInAnswer{signInApproved}
	service.mu.Unlock()
	done := session.run("login", "--poll")
	if want := "Logged in as dev@localhost. The key is saved in " + session.file("credentials.json") + ".\n"; done.status != 0 || done.output != want || done.errors != "sign-in · waiting for approval · the code expires in 8m30s · resume: string login --poll\n" {
		t.Errorf("then login --poll: %+v", done)
	}
	codes, issued := service.codes(), service.requests("/oauth/device_authorization")
	if len(codes) != 18 || slices.ContainsFunc(codes, func(code string) bool { return code != "device-1" }) || issued != 1 {
		t.Errorf("the token endpoint was asked about %v, and %d codes were issued; want the one code 18 times", codes, issued)
	}

	// With --json, the error is the last line on stderr.
	service = &signInService{token: []signInAnswer{signInPending}}
	service.start(t)
	session = newSignInSession(t)
	session.env["STRING_AGENT"] = "codex"
	got = session.startThenPoll(t, "--json")
	if got.status != exitTimeout || got.output != "" || !strings.HasPrefix(got.errors, signInWaiting) {
		t.Errorf("login --poll --json: %+v", got)
	}
	expectJSON(t, lastLine(got.errors), "{\"error\":{\"code\":\"wait_timeout\",\"message\":\"Stopped waiting after 90s; the code expires in 8m30s.\",\"next_step\":\""+signInPoll+"\",\"retry_after_seconds\":null,\"exit_code\":7,\"retryable\":true,\"user_action_required\":false,\"next_commands\":[{\"type\":\"wait\",\"command\":\"string login --poll\",\"argv\":[\"string\",\"login\",\"--poll\"]}]}}")

	// A code that has expired ends login --poll at once, with nothing to resume.
	session = newSignInSession(t)
	session.env["STRING_AGENT"] = "codex"
	if got := session.run("login", "--no-wait"); got.status != 0 {
		t.Fatalf("login --no-wait: %+v", got)
	}
	session.now = session.now.Add(11 * time.Minute)
	if got := session.run("login", "--poll"); got.status != exitAuth || got.errors != "The code expired; run string login again\n" {
		t.Errorf("login --poll after the code expired: %+v", got)
	}

	// A code that runs out as the wait reaches its limit has expired too, and
	// nothing says to go on with it.
	service = &signInService{token: []signInAnswer{signInPending}}
	service.start(t)
	session = newSignInSession(t)
	session.env["STRING_AGENT"] = "codex"
	if got := session.run("login", "--no-wait"); got.status != 0 {
		t.Fatalf("login --no-wait: %+v", got)
	}
	session.now = session.now.Add(8*time.Minute + 30*time.Second)
	if got := session.run("login", "--poll"); got.status != exitAuth || !strings.HasSuffix(got.errors, "\nThe code expired; run string login again\n") {
		t.Errorf("login --poll with 90s left on the code: %+v", got)
	}
	if _, err := os.Stat(session.file("pending.json")); !os.IsNotExist(err) {
		t.Errorf("pending.json is there after the code expired: %v", err)
	}
}

// A sign-in waits as long as its limit: 90s under an agent, --timeout when
// it is given, and otherwise until the code expires, as before. A person who
// watches stderr at a terminal sees no new lines.
func TestSignInWaitLastsAsLongAsItsLimit(t *testing.T) {
	// The person approves after 24 polls, 2 minutes in, past an agent's 90s.
	answers := append(slices.Repeat([]signInAnswer{signInPending}, 24), signInApproved)
	for _, c := range []struct {
		about   string
		person  bool
		agent   string
		flags   []string
		stopped string
	}{
		{about: "a script"},
		{about: "a person at a terminal", person: true},
		{about: "an agent with --timeout 0", agent: "codex", flags: []string{"--timeout", "0"}},
		{about: "an agent with --timeout 5m", agent: "codex", flags: []string{"--timeout", "5m"}},
		{about: "an agent", agent: "codex", stopped: "Stopped waiting after 90s; the code expires in 8m30s."},
		{about: "a script with --timeout 30s", flags: []string{"--timeout", "30s"}, stopped: "Stopped waiting after 30s; the code expires in 9m30s."},
		{about: "a person with --timeout=1m", person: true, flags: []string{"--timeout=1m"}, stopped: "Stopped waiting after 60s; the code expires in 9m."},
	} {
		service := &signInService{token: slices.Clone(answers), key: []signInAnswer{signInKey}}
		service.start(t)
		session := newSignInSession(t)
		if c.agent != "" {
			session.env["STRING_AGENT"] = c.agent
		}
		var got result
		if c.person {
			session.terminal, session.progress = true, true
			got = session.run(append([]string{"login"}, c.flags...)...)
		} else {
			got = session.startThenPoll(t, c.flags...)
		}
		ended := ""
		if c.stopped != "" {
			ended = c.stopped + "\nNext: " + signInPoll + "\n"
		}
		polls := len(service.codes())
		switch {
		case c.stopped == "" && (got.status != 0 || polls != 25):
			t.Errorf("%s: exit %d after %d polls, want 0 after 25: %s", c.about, got.status, polls, got.errors)
		case c.stopped != "" && (got.status != exitTimeout || !strings.HasSuffix(got.errors, ended)):
			t.Errorf("%s: exit %d: %s", c.about, got.status, got.errors)
		case c.person && got.errors != ended:
			t.Errorf("%s: stderr %q, want only %q", c.about, got.errors, ended)
		case !c.person && !strings.HasPrefix(got.errors, signInWaiting):
			t.Errorf("%s: stderr %q, want it to start with %q", c.about, got.errors, signInWaiting)
		}
	}
}

// login --poll on a browser sign-in stops at its limit too, and keeps the
// sign-in for the next login --poll, which finishes it.
func TestLoginPollOnABrowserSignInStopsAt90sAndKeepsIt(t *testing.T) {
	service := &signInService{status: thenExpired(browserPending)}
	service.start(t)
	session := newSignInSession(t)
	session.env["STRING_AGENT"] = "claude-code"
	if started := session.run("login", "--browser", "--json"); started.status != 0 {
		t.Fatalf("login --browser --json: %+v", started)
	}

	got := session.run("login", "--poll")

	want := browserWaiting + heartbeats("not authorized yet") + "Stopped waiting after 90s; the link expires in 13m30s.\nNext: run `string login --poll`\n"
	if got.status != exitTimeout || got.output != "" || got.errors != want {
		t.Errorf("exit %d, stdout %q, stderr:\n%s\nwant exit 7 and:\n%s", got.status, got.output, got.errors, want)
	}
	// One check before the link was shown, then one every 3s from 3s to 87s.
	if checks := len(service.checks()); checks != 30 {
		t.Errorf("%d checks, want 30", checks)
	}
	if _, err := os.Stat(session.file("pending.json")); err != nil {
		t.Errorf("pending.json is gone after the wait stopped: %v", err)
	}

	service.mu.Lock()
	service.status = []signInAnswer{browserComplete}
	service.mu.Unlock()
	if done := session.run("login", "--poll"); done.status != 0 || !strings.HasPrefix(done.output, "Logged in to Acme.") {
		t.Errorf("then login --poll: %+v", done)
	}
	if _, err := os.Stat(session.file("pending.json")); !os.IsNotExist(err) {
		t.Errorf("pending.json is there after the sign-in finished: %v", err)
	}

	// The line every 15s says when the portal is what keeps the sign-in waiting.
	for answer, said := range map[signInAnswer]string{
		{http.StatusServiceUnavailable, `{"status":"error","error":"unavailable"}`, ""}: "the portal answered HTTP 503",
		{}: "no answer from the portal",
	} {
		service := &signInService{status: append([]signInAnswer{browserPending}, thenExpired(answer)...)}
		service.start(t)
		session := newSignInSession(t)
		session.env["STRING_AGENT"] = "claude-code"
		if started := session.run("login", "--browser", "--json"); started.status != 0 {
			t.Fatalf("login --browser --json: %+v", started)
		}
		if got := session.run("login", "--poll"); got.status != exitTimeout || !strings.Contains(got.errors, "\n"+heartbeats(said)) {
			t.Errorf("a portal that answers %+v: %+v, want the lines %q", answer, got, heartbeats(said))
		}
	}
}

// trial claim --poll under an agent stops after 90s too, and the claim stays
// for the next trial claim --poll.
func TestTrialClaimPollUnderAnAgentStopsAfter90sAndKeepsTheClaim(t *testing.T) {
	var mu sync.Mutex
	polls, claimed := 0, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/api/agent-signup/token" {
			http.NotFound(w, r)
			return
		}
		polls++
		if !claimed {
			w.WriteHeader(http.StatusBadRequest)
			if polls > pollsAtMost {
				io.WriteString(w, `{"error":"expired_token","error_description":"Expired."}`)
				return
			}
			io.WriteString(w, `{"error":"authorization_pending","error_description":"Not yet."}`)
			return
		}
		io.WriteString(w, `{"access_token":"claimed-key","token_type":"Bearer","organization_id":9}`)
	}))
	t.Cleanup(server.Close)
	defer func(p string) { portalURL = p }(portalURL)
	portalURL = server.URL

	clock := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	home, dir := claimHome(t)
	registration := trialRegistration{RegistrationID: "reg_7", ClaimToken: "clm_7_secret", ClaimEmail: "dev@localhost", ClaimCodeExpires: clock.Add(10 * time.Minute), ClaimInterval: 5}
	if err := writePrivate(filepath.Join(dir, "trial.json"), registration); err != nil {
		t.Fatal(err)
	}
	poll := func() result {
		var stdout, stderr bytes.Buffer
		env := envOf(map[string]string{"HOME": home, "STRING_AGENT": "claude-code"})
		status := run([]string{"trial", "claim", "--poll"}, runtime{
			env:    env,
			client: http.DefaultClient,
			stdout: &stdout,
			stderr: &stderr,
			sleep:  func(d time.Duration) { clock = clock.Add(d) },
			now:    func() time.Time { return clock },
			exec:   execCtxFor(env, false, false, false),
		})
		return result{status, stdout.String(), stderr.String()}
	}

	got := poll()

	want := "claim · waiting for dev@localhost to enter the code · the code expires in 10m · resume: string trial claim --poll\n" + heartbeats("code not entered yet") + "Stopped waiting after 90s; the code expires in 8m30s.\nNext: run `string trial claim --poll`\n"
	mu.Lock()
	asked := polls
	claimed = true
	mu.Unlock()
	// It asked at once and then every 5s until 85s.
	if got.status != exitTimeout || got.output != "" || got.errors != want || asked != 18 {
		t.Errorf("exit %d after %d polls, stdout %q, stderr:\n%s\nwant exit 7 after 18 and:\n%s", got.status, asked, got.output, got.errors, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "trial.json")); err != nil {
		t.Errorf("trial.json is gone after the wait stopped: %v", err)
	}

	if done := poll(); done.status != 0 || !strings.HasPrefix(done.output, "dev@localhost claimed this agent.") || done.errors != "" {
		t.Errorf("then trial claim --poll: %+v", done)
	}
	if _, err := os.Stat(filepath.Join(dir, "trial.json")); !os.IsNotExist(err) {
		t.Errorf("trial.json is there after the claim finished: %v", err)
	}
}

// A poll still going when the wait's limit passes ends with it: the wait
// stopped, rather than a request that ran out of time, and the code stays.
func TestASignInPollThatRunsIntoTheLimitStopsTheWait(t *testing.T) {
	service := &signInService{token: []signInAnswer{signInSilent, signInApproved}, key: []signInAnswer{signInKey}}
	service.start(t)
	session := newSignInSession(t)
	session.realTime = true

	got := session.startThenPoll(t, "--timeout", "300ms")

	if got.status != exitTimeout || !strings.Contains(got.errors, "\nStopped waiting after 300ms; the code expires in ") || !strings.HasSuffix(got.errors, "\nNext: "+signInPoll+"\n") || service.requests("/oauth/token") != 1 {
		t.Errorf("a poll that ran into the limit: %+v after %d polls, want exit 7, the wait stopped, and 1 poll", got, service.requests("/oauth/token"))
	}
	if done := session.run("login", "--poll"); done.status != 0 {
		t.Errorf("then login --poll: %+v", done)
	}

	// So does a claim's poll, and the claim stays.
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// With the body read, the server sees string hang up.
		r.ParseForm()
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	t.Cleanup(portal.Close)
	defer func(p string) { portalURL = p }(portalURL)
	portalURL = portal.URL
	home, dir := claimHome(t)
	registration := trialRegistration{RegistrationID: "reg_7", ClaimToken: "clm_7_secret", ClaimEmail: "dev@localhost", ClaimCodeExpires: time.Now().Add(10 * time.Minute), ClaimInterval: 5}
	if err := writePrivate(filepath.Join(dir, "trial.json"), registration); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	env := envOf(map[string]string{"HOME": home})
	status := run([]string{"trial", "claim", "--poll", "--timeout", "300ms"}, runtime{
		env:    env,
		client: http.DefaultClient,
		stdout: &stdout,
		stderr: &stderr,
		sleep:  func(time.Duration) {},
		exec:   execCtxFor(env, false, false, false),
	})
	if status != exitTimeout || !strings.HasPrefix(stderr.String(), "Stopped waiting after 300ms; the code expires in ") || !strings.HasSuffix(stderr.String(), "\nNext: run `string trial claim --poll`\n") {
		t.Errorf("a claim poll that ran into the limit: exit %d: %s", status, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "trial.json")); err != nil {
		t.Errorf("trial.json is gone after the wait stopped: %v", err)
	}
}
