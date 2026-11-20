package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// browserCheck is what string login --browser sends the portal each time it
// asks whether the person has authorized the sign-in.
type browserCheck struct {
	SessionID    string `json:"session_id"`
	CodeVerifier string `json:"code_verifier"`
}

// checks is every status check that reached the stand-in.
func (s *signInService) checks() []browserCheck {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.asked)
}

// browserWaiting starts the wait for a fresh link on stderr, where no person
// watches it at a terminal.
const browserWaiting = "sign-in · waiting for authorization · the link expires in 15m · resume: string login --poll\n"

var (
	browserPending  = signInAnswer{http.StatusOK, `{"status":"pending"}`, ""}
	browserComplete = signInAnswer{http.StatusOK, `{"status":"complete","apiKey":"authorized-key","teamName":"Acme"}`, ""}
)

// browserLink is the sign-in link in output. Every check the CLI made must
// carry its session id and a verifier whose S256 challenge it carries.
func browserLink(t *testing.T, output string, checks []browserCheck) string {
	t.Helper()
	link := regexp.MustCompile(regexp.QuoteMeta(portalURL) + `/agent-auth\?code_challenge=([A-Za-z0-9_-]{43})&source=cli#session_id=([0-9a-f]{64})`).FindStringSubmatch(output)
	if link == nil {
		t.Fatalf("no sign-in link in %q", output)
	}
	verifier := regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
	for _, check := range checks {
		if check.SessionID != link[2] || !verifier.MatchString(check.CodeVerifier) || codeChallenge(check.CodeVerifier) != link[1] {
			t.Errorf("a status check does not carry the link's session id and a verifier for its challenge")
		}
	}
	return link[0]
}

func TestCodeChallengeIsTheS256OfRFC7636(t *testing.T) {
	// RFC 7636, appendix B.
	if got := codeChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Errorf("the challenge is %q", got)
	}
}

// Once the person clicks Authorize, the key the portal hands over once is
// saved where string login saves it and in --env-file, and printed nowhere.
func TestLoginBrowserSavesTheKeyOnceThePersonAuthorizes(t *testing.T) {
	// The first check is made before the link is shown.
	service := &signInService{status: []signInAnswer{browserPending, browserPending, browserComplete}}
	service.start(t)
	session := newSignInSession(t)
	session.terminal = true
	envFile := filepath.Join(t.TempDir(), ".env")

	got := session.run("login", "--browser", "--env-file", envFile)

	link := browserLink(t, got.output, service.checks())
	want := "Open this link, sign in to String or create an account, and click Authorize:\n" + link + "\nLogged in to Acme. The key is saved in " + session.file("credentials.json") + ".\nWrote it to " + envFile + " as STRING_API_KEY.\n"
	if got.status != 0 || got.output != want || got.errors != browserWaiting {
		t.Errorf("login --browser: %+v, want %q", got, want)
	}
	if want := []time.Duration{3 * time.Second, 3 * time.Second}; !reflect.DeepEqual(session.waits, want) || len(service.checks()) != 3 {
		t.Errorf("waits %v and %d checks, want %v and 3", session.waits, len(service.checks()), want)
	}
	saved, err := loadCredentials(session.file("credentials.json"))
	data, _ := os.ReadFile(envFile)
	if err != nil || saved.APIKey != "authorized-key" || string(data) != "STRING_API_KEY=authorized-key\n" {
		t.Errorf("saved %+v, %v, and the env file holds %q", saved, err, data)
	}
	if _, err := os.Stat(session.file("pending.json")); !os.IsNotExist(err) || service.requests("/oauth/device_authorization") != 0 {
		t.Errorf("pending.json is there afterwards (%v), or a device code was asked for", err)
	}

	// An agent gets the link at once as JSON, and --poll finishes. Only
	// pending.json, readable by its owner alone, keeps the verifier.
	service = &signInService{status: []signInAnswer{browserPending, browserComplete}}
	service.start(t)
	session = newSignInSession(t)
	started := session.run("login", "--browser", "--json")
	shown := decode(t, started.output).(map[string]any)
	link = browserLink(t, started.output, service.checks())
	if want := map[string]any{"verification_uri_complete": link, "expires_in": 900.0, "interval": 3.0, "next": "string login --poll"}; started.status != 0 || !reflect.DeepEqual(shown, want) {
		t.Errorf("login --browser --json: %+v, want %v", started, want)
	}
	var pending pendingLogin
	data, err = os.ReadFile(session.file("pending.json"))
	info, statErr := os.Stat(session.file("pending.json"))
	if err != nil || statErr != nil || info.Mode().Perm() != 0o600 || json.Unmarshal(data, &pending) != nil || !slices.Equal(service.checks(), []browserCheck{{pending.SessionID, pending.CodeVerifier}}) {
		t.Errorf("pending.json is not a 0600 file with the session id and verifier that were sent: %v, %v", err, statErr)
	}
	done := session.run("login", "--poll", "--json")
	if want := map[string]any{"status": "logged_in", "team": "Acme", "credentials": session.file("credentials.json")}; done.status != 0 || done.errors != browserWaiting || !reflect.DeepEqual(decode(t, done.output), want) {
		t.Errorf("login --poll --json: %+v, want %v", done, want)
	}
	if _, err := os.Stat(session.file("pending.json")); !os.IsNotExist(err) || strings.Contains(started.output+started.errors+done.output+done.errors, "authorized-key") {
		t.Errorf("pending.json is there afterwards (%v), or the key was printed", err)
	}

	// init --browser under an agent hands back the same kind of link.
	service = &signInService{status: []signInAnswer{browserPending, browserComplete}}
	service.start(t)
	session = newSignInSession(t)
	got = session.run("init", "--browser")
	link = browserLink(t, got.output, service.checks())
	if !strings.HasSuffix(got.output, "For more:\nOpen this link, sign in to String or create an account, and click Authorize:\n"+link+"\nThen run string login --poll to save the key. The link expires in 15 minutes.\n") {
		t.Errorf("init --browser: %+v", got)
	}
	if done := session.run("login", "--poll"); done.status != 0 || !strings.HasPrefix(done.output, "Logged in to Acme.") {
		t.Errorf("login --poll after init --browser: %+v", done)
	}
}

// Every final answer ends the sign-in and removes pending.json.
func TestLoginBrowserEndsOnAFinalAnswer(t *testing.T) {
	for _, c := range []struct {
		answer  signInAnswer
		code    string
		message string
	}{
		{signInAnswer{http.StatusOK, `{"status":"denied"}`, ""}, "login_denied", "Sign-in was denied"},
		{signInAnswer{http.StatusOK, `{"status":"expired"}`, ""}, "login_expired", "The sign-in link expired; run string login --browser again"},
		{signInAnswer{http.StatusBadRequest, `{"status":"error","error":"invalid_verifier"}`, ""}, "invalid_verifier", "String did not accept this sign-in (invalid_verifier)"},
		{signInAnswer{http.StatusConflict, `{"status":"error","error":"key_limit"}`, ""}, "key_limit", "Your organization already has as many API keys as it may have"},
		// An answer this CLI doesn't know ends it with what the portal said.
		{signInAnswer{http.StatusOK, `{"status":"withdrawn","message":"The request was withdrawn."}`, ""}, "login_failed", "Sign-in failed: The request was withdrawn."},
	} {
		service := &signInService{status: []signInAnswer{browserPending, c.answer}}
		service.start(t)
		session := newSignInSession(t)
		if started := session.run("login", "--browser", "--no-wait"); started.status != 0 {
			t.Fatalf("login --browser --no-wait: %+v", started)
		}

		got := session.run("login", "--poll", "--json")

		envelope := decode(t, lastLine(got.errors)).(map[string]any)["error"].(map[string]any)
		if got.status != exitAuth || got.output != "" || envelope["code"] != c.code || envelope["message"] != c.message {
			t.Errorf("%s: %+v, want exit %d, %s and %q", c.answer.body, got, exitAuth, c.code, c.message)
		}
		for _, name := range []string{"credentials.json", "pending.json"} {
			if _, err := os.Stat(session.file(name)); !os.IsNotExist(err) {
				t.Errorf("%s: %s is there afterwards: %v", c.answer.body, name, err)
			}
		}
	}

	// A portal that answers 404 doesn't have this sign-in, so nobody gets a link.
	service := &signInService{status: []signInAnswer{{http.StatusNotFound, "<html>Not Found</html>", ""}}}
	service.start(t)
	session := newSignInSession(t)
	session.terminal = true
	got := session.run("login", "--browser")
	if got.status != exitAuth || got.output != "" || got.errors != "error: This String server does not offer string login --browser yet. Use string login.\n" {
		t.Errorf("a portal without the page: %+v", got)
	}
	if _, err := os.Stat(session.file("pending.json")); !os.IsNotExist(err) {
		t.Errorf("a portal without the page: pending.json is there: %v", err)
	}
}

// A busy or unreachable portal is asked again, no sooner than its Retry-After
// says, and never after the 15 minutes the sign-in lasts.
func TestLoginBrowserWaitsOutABusyPortalUntilItsDeadline(t *testing.T) {
	limited := func(retryAfter string) signInAnswer {
		return signInAnswer{http.StatusTooManyRequests, `{"status":"error","error":"rate_limited"}`, retryAfter}
	}
	unavailable := signInAnswer{http.StatusServiceUnavailable, `{"status":"error","error":"unavailable"}`, "5"}
	dropped := signInAnswer{}
	for _, c := range []struct {
		answers []signInAnswer
		exit    int
		waits   []time.Duration
	}{
		{[]signInAnswer{limited("7"), browserComplete}, 0, []time.Duration{3 * time.Second, 7 * time.Second}},
		{[]signInAnswer{limited("1"), browserComplete}, 0, []time.Duration{3 * time.Second, 3 * time.Second}},
		{[]signInAnswer{unavailable, dropped, browserComplete}, 0, []time.Duration{3 * time.Second, 5 * time.Second, 3 * time.Second}},
		// A wait that would pass the deadline ends the sign-in instead.
		{[]signInAnswer{limited("9223372036854775807")}, exitAuth, []time.Duration{3 * time.Second}},
	} {
		service := &signInService{status: append([]signInAnswer{browserPending}, c.answers...)}
		service.start(t)
		session := newSignInSession(t)
		if started := session.run("login", "--browser", "--no-wait"); started.status != 0 {
			t.Fatalf("login --browser --no-wait: %+v", started)
		}

		got := session.run("login", "--poll")

		if got.status != c.exit || !reflect.DeepEqual(session.waits, c.waits) {
			t.Errorf("answers %v: exit %d after waits %v, want exit %d after %v: %+v", c.answers, got.status, session.waits, c.exit, c.waits, got)
		}
	}

	// Nobody acts on the link: the CLI asks every 3 seconds for 15 minutes.
	// stderr, which is not a terminal here, gets a line every 15 seconds.
	service := &signInService{status: []signInAnswer{browserPending}}
	service.start(t)
	session := newSignInSession(t)
	session.terminal = true
	got := session.run("login", "--browser")
	var waited time.Duration
	for _, wait := range session.waits {
		waited += wait
	}
	beats := strings.Count(got.errors, "\nwaiting: not authorized yet · ")
	if got.status != exitAuth || !strings.HasPrefix(got.errors, browserWaiting) || !strings.HasSuffix(got.errors, "\nwaiting: not authorized yet · 15m\nerror: The sign-in link expired; run string login --browser again\n") || beats != 60 || waited != 15*time.Minute || len(service.checks()) != 301 {
		t.Errorf("nobody acts: %+v after %v, %d checks and %d lines, want the expiry after 15m, 301 checks and 60 lines", got, waited, len(service.checks()), beats)
	}
	if _, err := os.Stat(session.file("pending.json")); !os.IsNotExist(err) {
		t.Errorf("nobody acts: pending.json is there afterwards: %v", err)
	}
}

// string opens the sign-in link in the browser only for a person at a
// terminal, and never with --no-open, wherever it is on the command line.
// Under an agent, in CI or in a pipe, the link it prints is the whole
// hand-off.
func TestSignInOpensABrowserOnlyForAPersonAtATerminal(t *testing.T) {
	person := execCtx{stdinTTY: true, stdoutTTY: true, stderrTTY: true}
	for _, c := range []struct {
		about         string
		exec          execCtx
		before, after []string
		opens         bool
	}{
		{about: "a person at a terminal", exec: person, opens: true},
		{about: "a person at a terminal with --no-open", exec: person, after: []string{"--no-open"}},
		{about: "a person at a terminal with --no-open first", exec: person, before: []string{"--no-open"}},
		{about: "Claude Code at a terminal", exec: execCtx{stdinTTY: true, stdoutTTY: true, stderrTTY: true, agent: "claude-code"}},
		{about: "CI at a terminal", exec: execCtx{stdinTTY: true, stdoutTTY: true, stderrTTY: true, ci: true}},
		{about: "a person piping stdout", exec: execCtx{stdinTTY: true, stderrTTY: true}},
		{about: "a script", exec: execCtx{}},
	} {
		for _, flow := range [][]string{{"login", "--no-wait"}, {"login", "--browser", "--no-wait"}} {
			service := &signInService{status: []signInAnswer{browserPending}}
			service.start(t)
			var stdout, stderr bytes.Buffer
			var opened []string
			status := run(slices.Concat(c.before, flow, c.after), runtime{
				env:        envOf(map[string]string{"HOME": t.TempDir()}),
				client:     http.DefaultClient,
				stdin:      strings.NewReader(""),
				stdout:     &stdout,
				stderr:     &stderr,
				openURL:    func(link string) error { opened = append(opened, link); return nil },
				sleep:      func(time.Duration) {},
				exec:       c.exec,
				osHostname: func() (string, error) { return "build-7.corp.example.test", nil },
			})
			if status != 0 {
				t.Fatalf("%s, %v: exit %d: %s", c.about, flow, status, stderr.String())
			}
			if (len(opened) > 0) != c.opens {
				t.Errorf("%s, %v: opened %v", c.about, flow, opened)
			}
			for _, link := range opened {
				if !strings.Contains(stdout.String(), link) {
					t.Errorf("%s, %v: opened %s without printing it: %q", c.about, flow, link, stdout.String())
				}
			}
			if !strings.Contains(stdout.String(), "string login --poll") {
				t.Errorf("%s, %v: printed %q", c.about, flow, stdout.String())
			}
		}
	}
}
