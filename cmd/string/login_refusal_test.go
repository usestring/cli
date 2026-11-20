package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain points sign-in at an address that refuses connections. The defaults
// are String's production servers, so a test that forgets to set authURL and
// portalURL fails instead of asking them for a device code.
func TestMain(m *testing.M) {
	authURL, portalURL = "http://127.0.0.1:1", "http://127.0.0.1:1"
	os.Exit(m.Run())
}

func TestSignInInTestsGoesNowhereUnlessATestSetsItsOwnServer(t *testing.T) {
	for name, value := range map[string]string{"authURL": authURL, "portalURL": portalURL} {
		address, err := url.Parse(value)
		if err != nil || address.Hostname() != "127.0.0.1" {
			t.Fatalf("%s is %q in tests, which is not this machine", name, value)
		}
		if connection, err := net.DialTimeout("tcp", address.Host, time.Second); err == nil {
			connection.Close()
			t.Errorf("%s is %q in tests, and something there accepts connections", name, value)
		}
	}
}

// signInAnswer is one answer from the stand-in below: a status, a body and,
// from a busy portal, a Retry-After header.
type signInAnswer struct {
	status     int
	body       string
	retryAfter string
}

var (
	signInApproved = signInAnswer{http.StatusOK, `{"access_token":"access-1","token_type":"Bearer"}`, ""}
	signInKey      = signInAnswer{http.StatusOK, `{"apiKey":"released-key"}`, ""}
)

// signInService stands in for the sign-in service and the portal. The token
// endpoint, the key request and the status check of string login --browser
// each give their answers in order, then repeat the last one. An answer with
// no status closes the connection instead.
type signInService struct {
	token  []signInAnswer
	key    []signInAnswer
	status []signInAnswer

	mu     sync.Mutex
	calls  map[string]int
	polled []string
	asked  []browserCheck
	// named has a line for each key request, and for any other request that
	// carried a device name: its path, a space, and the name as it was sent.
	named []string
}

// start serves the stand-in and points the CLI's sign-in at it for this test.
func (s *signInService) start(t *testing.T) {
	t.Helper()
	s.calls = map[string]int{}
	next := func(answers *[]signInAnswer, w http.ResponseWriter) {
		answer := (*answers)[0]
		if len(*answers) > 1 {
			*answers = (*answers)[1:]
		}
		if answer.status == 0 {
			if connection, _, err := w.(http.Hijacker).Hijack(); err == nil {
				connection.Close()
			}
			return
		}
		if answer.retryAfter != "" {
			w.Header().Set("Retry-After", answer.retryAfter)
		}
		w.WriteHeader(answer.status)
		io.WriteString(w, answer.body)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls[r.URL.Path]++
		if name, sent := r.Header[deviceNameHeader]; sent || r.URL.Path == "/api/oauth/api-key" {
			s.named = append(s.named, r.URL.Path+" "+strings.Join(name, ","))
		}
		switch r.URL.Path {
		case "/oauth/device_authorization":
			io.WriteString(w, `{"device_code":"device-1","user_code":"ABCD-EFGH","verification_uri":"https://example.test/device","verification_uri_complete":"https://example.test/device?code=ABCD-EFGH","expires_in":600,"interval":5}`)
		case "/oauth/token":
			s.polled = append(s.polled, r.FormValue("device_code"))
			next(&s.token, w)
		case "/api/oauth/api-key":
			if r.Header.Get("Authorization") != "Bearer access-1" {
				t.Errorf("the key request carried Authorization %q", r.Header.Get("Authorization"))
			}
			next(&s.key, w)
		case "/oauth/userinfo":
			io.WriteString(w, `{"email":"dev@localhost"}`)
		case "/api/agent-auth/status":
			var check browserCheck
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Origin") != "" || decoder.Decode(&check) != nil {
				t.Errorf("a status check is not a POST of JSON with only session_id and code_verifier, without Origin")
			}
			s.asked = append(s.asked, check)
			next(&s.status, w)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	auth, portal, client := authURL, portalURL, oauthClientID
	t.Cleanup(func() { authURL, portalURL, oauthClientID = auth, portal, client })
	authURL, portalURL, oauthClientID = server.URL, server.URL, "test-client"
}

// requests is how many requests reached path, or every path when it is empty.
func (s *signInService) requests(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for called, count := range s.calls {
		if path == "" || called == path {
			total += count
		}
	}
	return total
}

// deviceNames is a copy of named.
func (s *signInService) deviceNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.named)
}

// codes is the device code that each request to the token endpoint carried.
func (s *signInService) codes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.polled)
}

// signInSession is a throwaway home that the commands of one test share. It
// records the waits the CLI asked for instead of sleeping through them, and
// its clock moves on by each one.
type signInSession struct {
	env      map[string]string
	terminal bool
	progress bool
	waits    []time.Duration
	now      time.Time
}

func newSignInSession(t *testing.T) *signInSession {
	return &signInSession{env: map[string]string{"HOME": t.TempDir(), "STRING_API_BASE_URL": "http://127.0.0.1:1/v1"}, now: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
}

func (s *signInSession) run(list ...string) result {
	var stdout, stderr bytes.Buffer
	env := func(name string) (string, bool) {
		value, ok := s.env[name]
		return value, ok
	}
	status := run(list, runtime{
		env:      env,
		lookPath: func(string) (string, error) { return "/usr/local/bin/string", nil },
		client:   http.DefaultClient,
		stdin:    strings.NewReader(""),
		stdout:   &stdout,
		stderr:   &stderr,
		sleep: func(d time.Duration) {
			s.waits = append(s.waits, d)
			s.now = s.now.Add(d)
		},
		now: func() time.Time { return s.now },
		// CI, an agent and STRING_NO_INPUT come from the session's variables.
		exec: execCtxFor(env, s.terminal, s.terminal, s.progress),
		// A fixed hostname, so no test sends the name of the machine it runs on.
		osHostname: func() (string, error) { return "build-7.corp.example.test", nil },
	})
	return result{status, stdout.String(), stderr.String()}
}

// file is the path of one of the files the CLI keeps under the home.
func (s *signInSession) file(name string) string {
	return filepath.Join(s.env["HOME"], ".config", "string", name)
}

// startThenPoll starts a sign-in the way an agent does and runs the command
// that finishes it.
func (s *signInSession) startThenPoll(t *testing.T, poll ...string) result {
	t.Helper()
	if got := s.run("login", "--no-wait"); got.status != 0 {
		t.Fatalf("login --no-wait: %+v", got)
	}
	s.waits = nil
	return s.run(append([]string{"login", "--poll"}, poll...)...)
}

const (
	signInByHand = "set STRING_API_KEY to an API key from https://portal.usestring.ai/api-keys, or pipe one to `string login --with-token`"
	signInAgain  = "run `string login` again later, or " + signInByHand
	signInPoll   = "run `string login --poll` to finish this sign-in with the same code"
	signInHint   = "string fetch and string search work now, without a key, within a daily allowance. For more, run string login, or set STRING_API_KEY to an API key from https://portal.usestring.ai/api-keys.\n"
)

func TestAPortalRefusalAfterApprovalEndsLoginWithItsExitCodeAndANextStep(t *testing.T) {
	refusals := []struct {
		answer   signInAnswer
		exit     int
		message  string
		next     string
		requests int
	}{
		{signInAnswer{http.StatusForbidden, `{"error":"This app cannot receive String API keys"}`, ""}, exitAuth, "HTTP 403: This app cannot receive String API keys", signInByHand, 1},
		{signInAnswer{http.StatusForbidden, `{"error":"Your organization already has 100 active API keys. Revoke one at https://portal.usestring.ai/api-keys, then sign in again."}`, ""}, exitAuth, "HTTP 403: Your organization already has 100 active API keys.", signInByHand, 1},
		{signInAnswer{http.StatusUnauthorized, `{"error":"Invalid or expired access token"}`, ""}, exitAuth, "HTTP 401: Invalid or expired access token", signInByHand, 1},
		// A 429 turns the request away before it is handled, and the portal
		// checks the token before it creates a key, so these two are asked again.
		{signInAnswer{http.StatusTooManyRequests, `{"error":"Too many requests. Retry shortly."}`, ""}, exitRateLimit, "HTTP 429: Too many requests. Retry shortly.", signInAgain, 4},
		{signInAnswer{http.StatusServiceUnavailable, `{"error":"Could not verify the access token"}`, ""}, exitUpstream, "HTTP 503: Could not verify the access token", signInAgain, 4},
		// These may follow a key that was created, and a second request would
		// create another, so they are asked once.
		{signInAnswer{http.StatusServiceUnavailable, `{"error":"Could not create a key for this sign-in"}`, ""}, exitUpstream, "HTTP 503: Could not create a key for this sign-in", signInAgain, 1},
		{signInAnswer{http.StatusInternalServerError, `{"error":"Internal Server Error"}`, ""}, exitUpstream, "HTTP 500: Internal Server Error", signInAgain, 1},
		{signInAnswer{http.StatusBadGateway, "<html>Bad Gateway</html>", ""}, exitUpstream, "HTTP 502 from ", signInAgain, 1},
	}
	for _, refusal := range refusals {
		for _, asJSON := range []bool{false, true} {
			service := &signInService{token: []signInAnswer{signInApproved}, key: []signInAnswer{refusal.answer}}
			service.start(t)
			session := newSignInSession(t)
			poll := []string{}
			if asJSON {
				poll = append(poll, "--json")
			}

			got := session.startThenPoll(t, poll...)

			said := fmt.Sprintf("%d %s, --json %t: %+v", refusal.answer.status, refusal.answer.body, asJSON, got)
			if got.status != refusal.exit || got.output != "" {
				t.Errorf("%s: want exit %d and nothing on stdout", said, refusal.exit)
			}
			if asJSON {
				envelope := decode(t, got.errors).(map[string]any)["error"].(map[string]any)
				message, _ := envelope["message"].(string)
				if envelope["code"] != "key_release_failed" || envelope["status"] != float64(refusal.answer.status) || !strings.HasPrefix(message, refusal.message) || envelope["next_step"] != refusal.next {
					t.Errorf("%s: want the message %q and the next step %q", said, refusal.message, refusal.next)
				}
			} else if !strings.HasPrefix(got.errors, refusal.message) || !strings.HasSuffix(got.errors, "\nNext: "+refusal.next+"\n") {
				t.Errorf("%s: want the message %q and the next step %q", said, refusal.message, refusal.next)
			}
			if requests := service.requests("/api/oauth/api-key"); requests != refusal.requests {
				t.Errorf("%s: asked for the key %d times, want %d", said, requests, refusal.requests)
			}
			for _, name := range []string{"credentials.json", "pending.json"} {
				if _, err := os.Stat(session.file(name)); !os.IsNotExist(err) {
					t.Errorf("%s: %s is there afterwards: %v", said, name, err)
				}
			}
		}
	}
}

func TestLoginAsksAgainForTheKeyWhileThePortalIsBusy(t *testing.T) {
	busyThenReady := func() *signInService {
		return &signInService{token: []signInAnswer{signInApproved}, key: []signInAnswer{
			{http.StatusTooManyRequests, `{"error":"Too many requests. Retry shortly."}`, ""},
			{http.StatusServiceUnavailable, `{"error":"Could not verify the access token"}`, ""},
			signInKey,
		}}
	}

	// A person at a terminal is told once why the command is still going.
	service := busyThenReady()
	service.start(t)
	session := newSignInSession(t)
	session.terminal, session.progress = true, true
	got := session.run("login")
	if got.status != 0 || !strings.Contains(got.output, "Logged in as dev@localhost.") || got.errors != "Approved. Waiting for String to hand over the key...\n" {
		t.Errorf("at a terminal: %+v", got)
	}
	// The first wait is the poll for approval; the others space out the key requests.
	if want := []time.Duration{5 * time.Second, 2 * time.Second, 4 * time.Second}; !reflect.DeepEqual(session.waits, want) {
		t.Errorf("waits %v, want %v", session.waits, want)
	}
	if requests := service.requests("/api/oauth/api-key"); requests != 3 {
		t.Errorf("asked for the key %d times, want 3", requests)
	}
	if saved, err := loadCredentials(session.file("credentials.json")); err != nil || saved.APIKey != "released-key" || saved.Email != "dev@localhost" {
		t.Errorf("saved %+v, %v", saved, err)
	}

	// An agent's --json run gets one JSON object on stdout and nothing else.
	service = busyThenReady()
	service.start(t)
	session = newSignInSession(t)
	got = session.startThenPoll(t, "--json")
	if got.status != 0 || got.errors != "" || decode(t, got.output).(map[string]any)["status"] != "logged_in" {
		t.Errorf("with --json: %+v", got)
	}
	if requests := service.requests("/api/oauth/api-key"); requests != 3 {
		t.Errorf("with --json: asked for the key %d times, want 3", requests)
	}
}

func TestLoginAsksAgainNoSoonerThanThePortalSaysAndNotForLong(t *testing.T) {
	limited := func(retryAfter string) signInAnswer {
		return signInAnswer{http.StatusTooManyRequests, `{"error":"Too many requests. Retry shortly."}`, retryAfter}
	}
	for _, c := range []struct {
		key      []signInAnswer
		exit     int
		waits    []time.Duration
		requests int
	}{
		// Longer than the CLI's own pause, so the portal's wait is used.
		{[]signInAnswer{limited("7"), signInKey}, 0, []time.Duration{7 * time.Second}, 2},
		// Shorter, so the CLI keeps its own.
		{[]signInAnswer{limited("1"), signInKey}, 0, []time.Duration{2 * time.Second}, 2},
		// The waits never add up to more than half a minute.
		{[]signInAnswer{limited("10")}, exitRateLimit, []time.Duration{10 * time.Second, 10 * time.Second, 10 * time.Second}, 4},
		{[]signInAnswer{limited("20")}, exitRateLimit, []time.Duration{20 * time.Second}, 2},
		{[]signInAnswer{limited("120")}, exitRateLimit, nil, 1},
		{[]signInAnswer{limited("9223372036854775807")}, exitRateLimit, nil, 1},
	} {
		service := &signInService{token: []signInAnswer{signInApproved}, key: c.key}
		service.start(t)
		session := newSignInSession(t)
		asked := c.key[0].retryAfter

		got := session.startThenPoll(t)

		// The poll for approval waits five seconds before the first key request.
		if want := append([]time.Duration{5 * time.Second}, c.waits...); got.status != c.exit || !reflect.DeepEqual(session.waits, want) {
			t.Errorf("Retry-After %s: exit %d after waits %v, want exit %d after %v: %s", asked, got.status, session.waits, c.exit, want, got.errors)
		}
		if requests := service.requests("/api/oauth/api-key"); requests != c.requests {
			t.Errorf("Retry-After %s: asked for the key %d times, want %d", asked, requests, c.requests)
		}
		if c.exit != 0 && !strings.Contains(got.errors, "\nRetry-After: "+asked+"\n") {
			t.Errorf("Retry-After %s: the failure doesn't pass it on: %s", asked, got.errors)
		}
	}

	service := &signInService{token: []signInAnswer{signInApproved}, key: []signInAnswer{limited("120")}}
	service.start(t)
	got := newSignInSession(t).startThenPoll(t, "--json")
	envelope := decode(t, got.errors).(map[string]any)["error"].(map[string]any)
	if got.status != exitRateLimit || envelope["retry_after_seconds"] != 120.0 {
		t.Errorf("with --json: %+v", got)
	}
}

func TestSignInServerTroubleWhileWaitingForApprovalHasItsOwnExitCode(t *testing.T) {
	for _, c := range []struct {
		answer signInAnswer
		exit   int
		next   string
	}{
		// Only a busy server leaves a code to go on with.
		{signInAnswer{http.StatusTooManyRequests, `{"message":"Too many requests"}`, ""}, exitRateLimit, "Next: " + signInPoll + "\n"},
		{signInAnswer{http.StatusServiceUnavailable, `{"message":"Service unavailable"}`, ""}, exitUpstream, "Next: " + signInPoll + "\n"},
		{signInAnswer{http.StatusBadRequest, `{"message":"Bad request"}`, ""}, exitAuth, ""},
	} {
		service := &signInService{token: []signInAnswer{c.answer}, key: []signInAnswer{signInKey}}
		service.start(t)
		session := newSignInSession(t)

		got := session.startThenPoll(t)

		if want := fmt.Sprintf("Sign-in failed: HTTP %d\n%s", c.answer.status, c.next); got.status != c.exit || got.errors != want {
			t.Errorf("the token endpoint answered %d: %+v, want exit %d and %q", c.answer.status, got, c.exit, want)
		}
		if _, err := os.Stat(session.file("credentials.json")); !os.IsNotExist(err) {
			t.Errorf("the token endpoint answered %d, and a key was saved: %v", c.answer.status, err)
		}
	}
}

func TestABusySignInServerLeavesTheCodeForLoginPollToFinish(t *testing.T) {
	for _, c := range []struct {
		answer signInAnswer
		exit   int
	}{
		{signInAnswer{http.StatusTooManyRequests, `{"message":"Too many requests"}`, ""}, exitRateLimit},
		{signInAnswer{http.StatusServiceUnavailable, `{"message":"Service unavailable"}`, ""}, exitUpstream},
	} {
		// A person waits at a terminal; an agent starts the sign-in and polls,
		// with --json or without.
		for _, way := range []struct{ atTerminal, asJSON bool }{{true, false}, {false, false}, {false, true}} {
			service := &signInService{token: []signInAnswer{c.answer, signInApproved}, key: []signInAnswer{signInKey}}
			service.start(t)
			session := newSignInSession(t)
			session.terminal = way.atTerminal
			said := fmt.Sprintf("the token endpoint answered %d, at a terminal %t, --json %t", c.answer.status, way.atTerminal, way.asJSON)

			var busy result
			switch {
			case way.atTerminal:
				busy = session.run("login")
			case way.asJSON:
				busy = session.startThenPoll(t, "--json")
			default:
				busy = session.startThenPoll(t)
			}
			// The failure says that the code is still good and how to go on with it.
			message := fmt.Sprintf("Sign-in failed: HTTP %d", c.answer.status)
			if way.asJSON {
				envelope := decode(t, busy.errors).(map[string]any)["error"].(map[string]any)
				if busy.status != c.exit || envelope["code"] != "login_failed" || envelope["message"] != message || envelope["next_step"] != signInPoll {
					t.Errorf("%s: %+v, want exit %d, the message %q and the next step %q", said, busy, c.exit, message, signInPoll)
				}
			} else if want := message + "\nNext: " + signInPoll + "\n"; busy.status != c.exit || busy.errors != want {
				t.Errorf("%s: %+v, want exit %d and %q", said, busy, c.exit, want)
			}

			// The code is not spent, so the next poll gets the token for it.
			done := session.run("login", "--poll")
			if want := "Logged in as dev@localhost. The key is saved in " + session.file("credentials.json") + ".\n"; done.status != 0 || done.output != want {
				t.Errorf("%s, then login --poll: %+v, want exit 0 and %q", said, done, want)
			}
			if codes, issued := service.codes(), service.requests("/oauth/device_authorization"); !reflect.DeepEqual(codes, []string{"device-1", "device-1"}) || issued != 1 {
				t.Errorf("%s: the token endpoint was asked about %v and %d codes were issued, want the one code asked about twice", said, codes, issued)
			}
			if _, err := os.Stat(session.file("pending.json")); !os.IsNotExist(err) {
				t.Errorf("%s: pending.json is there after the sign-in finished: %v", said, err)
			}
		}
	}

	// A refusal spends the code, and nothing is left to finish.
	for _, refusal := range []signInAnswer{
		{http.StatusBadRequest, `{"error":"access_denied"}`, ""},
		{http.StatusBadRequest, `{"message":"Bad request"}`, ""},
	} {
		service := &signInService{token: []signInAnswer{refusal, signInApproved}, key: []signInAnswer{signInKey}}
		service.start(t)
		session := newSignInSession(t)
		if got := session.startThenPoll(t); got.status != exitAuth || strings.Contains(got.errors, "login --poll") {
			t.Errorf("the token endpoint answered %s: %+v, want exit %d and no word of login --poll", refusal.body, got, exitAuth)
		}
		if got := session.run("login", "--poll"); got.status != exitUsage || !strings.HasPrefix(got.errors, "No sign-in is waiting.") {
			t.Errorf("the token endpoint answered %s, then login --poll: %+v, want exit %d and no sign-in waiting", refusal.body, got, exitUsage)
		}
	}
}

func TestABuildWithNoClientRefusesLoginAndInitSignsNobodyIn(t *testing.T) {
	service := &signInService{token: []signInAnswer{signInApproved}, key: []signInAnswer{signInKey}, status: []signInAnswer{browserComplete}}
	service.start(t)
	oauthClientID = ""
	session := newSignInSession(t)
	session.terminal = true

	for _, command := range [][]string{{"login"}, {"login", "--browser"}} {
		got := session.run(command...)
		if got.status != exitAuth || got.output != "" || !strings.Contains(got.errors, "This build of string cannot sign in through the browser.") || !strings.Contains(got.errors, "Keys are at https://portal.usestring.ai/api-keys.") {
			t.Errorf("%s: %+v", strings.Join(command, " "), got)
		}
	}
	got := session.run("login", "--json")
	if envelope := decode(t, got.errors).(map[string]any)["error"].(map[string]any); got.status != exitAuth || got.output != "" || envelope["code"] != "login_unavailable" {
		t.Errorf("login --json: %+v", got)
	}

	for _, command := range [][]string{{"init"}, {"init", "--browser"}} {
		got = session.run(command...)
		if got.status != 0 || got.errors != "" || !strings.Contains(got.output, "Installed the string-cli skill") || !strings.HasSuffix(got.output, "\n\n"+signInHint) || strings.Contains(got.output, "Signing in") {
			t.Errorf("%s from a terminal: %+v", strings.Join(command, " "), got)
		}
	}

	if requests := service.requests(""); requests != 0 {
		t.Errorf("a build with no client sent %d requests", requests)
	}
	for _, name := range []string{"credentials.json", "pending.json"} {
		if _, err := os.Stat(session.file(name)); !os.IsNotExist(err) {
			t.Errorf("%s is there afterwards: %v", name, err)
		}
	}
}

func TestInitStillSucceedsWhenItsSignInEndsWithoutAKey(t *testing.T) {
	const before = "\n\nSigning in. string fetch and string search already work without a key, within a daily allowance, so you can press Ctrl-C to skip this and run string login later.\nOpen https://example.test/device?code=ABCD-EFGH and confirm the code ABCD-EFGH.\n"
	for _, ending := range []struct {
		token signInAnswer
		key   signInAnswer
		why   string
	}{
		{signInAnswer{http.StatusBadRequest, `{"error":"access_denied"}`, ""}, signInKey, "it was denied"},
		{signInAnswer{http.StatusBadRequest, `{"error":"expired_token"}`, ""}, signInKey, "the code expired"},
		// Nobody approves, and the code's ten minutes run out.
		{signInAnswer{http.StatusBadRequest, `{"error":"authorization_pending"}`, ""}, signInKey, "the code expired"},
		{signInApproved, signInAnswer{http.StatusForbidden, `{"error":"This app cannot receive String API keys"}`, ""}, "HTTP 403: This app cannot receive String API keys"},
		{signInApproved, signInAnswer{http.StatusTooManyRequests, `{"error":"Too many requests. Retry shortly."}`, ""}, "HTTP 429: Too many requests. Retry shortly"},
	} {
		service := &signInService{token: []signInAnswer{ending.token}, key: []signInAnswer{ending.key}}
		service.start(t)
		session := newSignInSession(t)
		session.terminal = true

		got := session.run("init")

		after := "Sign-in did not finish: " + ending.why + ". Run string login to sign in later.\n\n" + signInHint
		if got.status != 0 || got.errors != "" || !strings.HasPrefix(got.output, "Installed the string-cli skill") || !strings.HasSuffix(got.output, before+after) {
			t.Errorf("sign-in ended with %q: %+v\nwant exit 0 and output ending\n%s", ending.why, got, before+after)
		}
		if _, err := os.Stat(filepath.Join(session.env["HOME"], ".agents", "skills", "string-cli", "SKILL.md")); err != nil {
			t.Errorf("sign-in ended with %q, and the skill is not installed: %v", ending.why, err)
		}
		if _, err := os.Stat(session.file("credentials.json")); !os.IsNotExist(err) {
			t.Errorf("sign-in ended with %q, and a key was saved: %v", ending.why, err)
		}
	}
}

func TestInitStillSucceedsWhenTheSignInServiceCannotBeReached(t *testing.T) {
	// No server is started, so sign-in goes to TestMain's address, which refuses.
	defer func(client string) { oauthClientID = client }(oauthClientID)
	oauthClientID = "test-client"
	session := newSignInSession(t)
	session.terminal = true

	got := session.run("init")

	if got.status != 0 || got.errors != "" || !strings.Contains(got.output, "\nSign-in did not finish: ") || !strings.HasSuffix(got.output, ". Run string login to sign in later.\n\n"+signInHint) {
		t.Errorf("init from a terminal: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(session.env["HOME"], ".agents", "skills", "string-cli", "SKILL.md")); err != nil {
		t.Errorf("the skill is not installed: %v", err)
	}
}

func TestInitUnderAnAgentStartsTheSignInThatLoginPollFinishes(t *testing.T) {
	service := &signInService{token: []signInAnswer{signInApproved}, key: []signInAnswer{signInKey}}
	service.start(t)
	session := newSignInSession(t)

	// As under a coding agent: init hands back the link and code at once.
	got := session.run("init")
	const started = "\n\nSigning in. string fetch and string search already work without a key, within a daily allowance. For more:\nOpen https://example.test/device?code=ABCD-EFGH and confirm the code ABCD-EFGH.\nThen run string login --poll to save the key. The code expires in 10 minutes.\n"
	if got.status != 0 || got.errors != "" || !strings.HasSuffix(got.output, started) || service.requests("/oauth/token") != 0 {
		t.Errorf("init without a terminal: %+v, and the token was asked for %d times; want exit 0, output ending\n%s\nand no wait", got, service.requests("/oauth/token"), started)
	}
	if done := session.run("login", "--poll"); done.status != 0 || !strings.HasPrefix(done.output, "Logged in as dev@localhost.") {
		t.Errorf("login --poll after init: %+v", done)
	}

	// In CI nobody is there to approve. A person who pipes install.sh into sh
	// reads stderr at a terminal, and the first run stays keyless. Neither run
	// sends a request.
	ci := newSignInSession(t)
	ci.env["CI"] = "true"
	piped := newSignInSession(t)
	piped.progress = true
	for name, session := range map[string]*signInSession{"in CI": ci, "piped, with stderr at a terminal": piped} {
		before := service.requests("")
		if got := session.run("init"); got.status != 0 || !strings.HasSuffix(got.output, "\n\n"+signInHint) || service.requests("") != before {
			t.Errorf("init %s: %+v, and %d requests sent, want the keyless note and none", name, got, service.requests("")-before)
		}
	}
}

func TestInitNoLoginSkipsSignIn(t *testing.T) {
	service := &signInService{token: []signInAnswer{signInApproved}, key: []signInAnswer{signInKey}}
	service.start(t)
	session := newSignInSession(t)
	session.terminal = true

	got := session.run("init", "--no-login")

	if got.status != 0 || got.errors != "" || !strings.HasSuffix(got.output, "\n\n"+signInHint) || strings.Contains(got.output, "Signing in") {
		t.Errorf("init --no-login: %+v", got)
	}
	if requests := service.requests(""); requests != 0 {
		t.Errorf("init --no-login sent %d requests", requests)
	}
	if _, err := os.Stat(filepath.Join(session.env["HOME"], ".agents", "skills", "string-cli", "SKILL.md")); err != nil {
		t.Errorf("init --no-login did not install the skill: %v", err)
	}
}

func TestInitSaysHowToPutTheCopyThatIsRunningOnPATH(t *testing.T) {
	const (
		missing = "\nAgents run the skill's commands as `string`, which is not on your PATH. "
		byNPM   = missing + "Install it with:\n  npm install -g @usestring/cli\n"
		orMove  = "or move the binary into a directory that is already on it.\n"
	)
	at := func(path string) string {
		return missing + "This copy is at:\n  " + path + "\nAdd its directory to your PATH, " + orMove
	}
	for _, c := range []struct {
		about string
		path  string
		err   error
		want  string
	}{
		// npm installed these copies, so npm has this version to install.
		{"npm install -g", "/usr/local/lib/node_modules/@usestring/cli/node_modules/@usestring/cli-linux-x64/bin/string", nil, byNPM},
		{"npx on Windows", `C:\Users\me\AppData\Local\npm-cache\_npx\1a2b3c\node_modules\@usestring\cli-win32-x64\bin\string.exe`, nil, byNPM},
		// These came from somewhere else, and the package on npm may be older.
		{"an unpacked download", "/home/me/Downloads/string_linux_amd64/string", nil, at("/home/me/Downloads/string_linux_amd64/string")},
		{"the Windows zip", `C:\Users\me\Downloads\string\string.exe`, nil, at(`C:\Users\me\Downloads\string\string.exe`)},
		{"a directory named like npm's", "/home/me/node_modules.bak/string", nil, at("/home/me/node_modules.bak/string")},
		{"a system that does not say where the binary is", "", os.ErrNotExist, missing + "Add this copy's directory to your PATH, " + orMove},
	} {
		env := map[string]string{"HOME": t.TempDir(), "STRING_API_KEY": "test-key"}
		var stdout, stderr bytes.Buffer
		status := run([]string{"init", "--no-mcp"}, runtime{
			env: func(name string) (string, bool) {
				value, ok := env[name]
				return value, ok
			},
			lookPath:   func(string) (string, error) { return "", os.ErrNotExist },
			executable: func() (string, error) { return c.path, c.err },
			stdout:     &stdout,
			stderr:     &stderr,
		})
		// With a key set, the hint is the last thing init prints.
		if got := stdout.String(); status != 0 || !strings.HasSuffix(got, c.want) || (c.want != byNPM && strings.Contains(got, "npm install")) {
			t.Errorf("%s: exit %d, want the output to end with %q, and npm named only for a copy npm installed:\n%s%s", c.about, status, c.want, got, stderr.String())
		}
	}
}

func TestHelpDescribesSignInAsItWorks(t *testing.T) {
	got := newSignInSession(t).run("--help")
	// The help is wrapped by hand, so compare it with the line breaks taken out.
	text := strings.Join(strings.Fields(got.output), " ")
	for _, want := range []string{
		"login signs in through your browser and saves a new API key that String creates for that sign-in.",
		"set STRING_API_KEY to an API key from https://portal.usestring.ai/api-keys.",
		"string init [--agent NAME[,NAME...]] [--no-mcp] [--no-login]",
		"--no-login skips the sign-in, and init exits 0 whether or not the sign-in finishes.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the help lacks %q", want)
		}
	}
	for _, gone := range []string{"the API key you choose", "portal.usestring.ai/settings"} {
		if strings.Contains(text, gone) {
			t.Errorf("the help still says %q", gone)
		}
	}
}

// The portal only redirects its old docs addresses now, and a released binary
// keeps the links it was built with.
func TestHelpLinksToTheDocsOnTheHostThatServesThem(t *testing.T) {
	for command, link := range map[string]string{
		"fetch":        "https://docs.usestring.ai/api-reference/fetch",
		"search":       "https://docs.usestring.ai/api-reference/search",
		"sitemap":      "https://docs.usestring.ai/api-reference/sitemap",
		"integrations": "https://docs.usestring.ai/sites",
	} {
		got := newSignInSession(t).run(command, "--help")
		if got.status != 0 || !strings.Contains(got.output, " "+link+"\n") || strings.Contains(got.output, "portal.usestring.ai/docs") {
			t.Errorf("string %s --help: exit %d, want the link %s and none to portal.usestring.ai/docs:\n%s", command, got.status, link, got.output)
		}
	}
}

func TestLoginSaysWhenSTRINGAPIKEYStillWinsOverTheKeyItSaved(t *testing.T) {
	const stillWins = "STRING_API_KEY is set, and commands keep using it instead until you unset it.\n"
	service := &signInService{token: []signInAnswer{signInApproved}, key: []signInAnswer{signInKey}}
	service.start(t)
	session := newSignInSession(t)
	loggedIn := "Logged in as dev@localhost. The key is saved in " + session.file("credentials.json") + ".\n"

	if got := session.startThenPoll(t); got.status != 0 || got.output != loggedIn || got.errors != "" {
		t.Errorf("without STRING_API_KEY: %+v", got)
	}
	session.env["STRING_API_KEY"] = "a-key-that-stopped-working"
	if got := session.startThenPoll(t); got.status != 0 || got.output != loggedIn+stillWins || got.errors != "" {
		t.Errorf("with STRING_API_KEY: %+v", got)
	}
	// With --json, stdout stays one JSON object and the note goes to stderr.
	got := session.startThenPoll(t, "--json")
	if got.status != 0 || got.errors != stillWins {
		t.Errorf("with STRING_API_KEY and --json: %+v", got)
	}
	expectJSON(t, got.output, `{"status":"logged_in","email":"dev@localhost","credentials":"`+session.file("credentials.json")+`"}`)

	got = session.run("logout")
	if got.status != 0 || !strings.Contains(got.output, "The key itself still works until you delete it in https://portal.usestring.ai/api-keys.\n") {
		t.Errorf("logout: %+v", got)
	}
}

func TestLoginShowsWhereToApproveAndHowToFinish(t *testing.T) {
	const open = "Open https://example.test/device?code=ABCD-EFGH and confirm the code ABCD-EFGH.\n"
	service := &signInService{token: []signInAnswer{signInApproved}, key: []signInAnswer{signInKey}}
	service.start(t)
	session := newSignInSession(t)

	// Without a terminal, or with --no-wait, the command returns at once.
	for _, list := range [][]string{{"login"}, {"login", "--no-wait"}} {
		if got := session.run(list...); got.status != 0 || got.output != open+"Then run string login --poll to save the key. The code expires in 10 minutes.\n" {
			t.Errorf("%v: %+v", list, got)
		}
	}
	session.terminal = true
	want := open + "Logged in as dev@localhost. The key is saved in " + session.file("credentials.json") + ".\n"
	if got := session.run("login"); got.status != 0 || got.output != want {
		t.Errorf("login at a terminal: %+v, want output %q", got, want)
	}
}
