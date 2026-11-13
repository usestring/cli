package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTrialSolvesTheProofOfWorkAndSavesTheKeyAndClaimToken(t *testing.T) {
	refusalStatus, refusal := 0, ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent-signup/identity" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Type  string
			Proof struct{ Challenge, Nonce string }
		}
		json.NewDecoder(r.Body).Decode(&body)
		if refusalStatus != 0 {
			w.WriteHeader(refusalStatus)
			io.WriteString(w, refusal)
			return
		}
		digest := sha256.Sum256([]byte(body.Proof.Challenge + ":" + body.Proof.Nonce))
		// 12 zero bits: the first three hex digits.
		if body.Type != "anonymous" || body.Proof.Challenge != "v1.challenge" || !strings.HasPrefix(hex.EncodeToString(digest[:]), "000") {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"proof_of_work_required","proof_of_work":{"challenge":"v1.challenge","difficulty":12,"algorithm":"sha256"}}`)
			return
		}
		io.WriteString(w, `{"registration_id":"reg_7","access_token":"trial-key","token_type":"Bearer","expires_in":86400,"access_token_expires":"2026-11-06T12:00:00.000Z","balance_usd":"0.50","claim_url":"https://portal.example/api/agent-signup/identity/claim","claim_token":"clm_7_secret","claim_token_expires":"2026-11-12T12:00:00.000Z"}`)
	}))
	t.Cleanup(server.Close)
	defer func(p string) { portalURL = p }(portalURL)
	portalURL = server.URL

	home := t.TempDir()
	env := map[string]string{"HOME": home}
	var stdout, stderr bytes.Buffer
	rt := runtime{
		env:    func(name string) (string, bool) { value, ok := env[name]; return value, ok },
		client: http.DefaultClient,
		stdout: &stdout,
		stderr: &stderr,
	}

	if status := run([]string{"trial", "--json"}, rt); status != 0 {
		t.Fatalf("trial status %d: %s", status, stderr.String())
	}
	if strings.Contains(stdout.String(), "trial-key") || strings.Contains(stdout.String(), "clm_7_secret") {
		t.Errorf("printed a secret: %s", stdout.String())
	}
	dir := filepath.Join(home, ".config", "string")
	saved, err := loadCredentials(filepath.Join(dir, "credentials.json"))
	if err != nil || saved.APIKey != "trial-key" {
		t.Errorf("saved credentials %+v, %v", saved, err)
	}
	for _, name := range []string{"credentials.json", "trial.json"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, %v", name, info, err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, "trial.json"))
	if !strings.Contains(string(data), `"claimToken": "clm_7_secret"`) {
		t.Errorf("trial.json: %s", data)
	}

	stderr.Reset()
	if status := run([]string{"trial"}, rt); status != exitUsage || !strings.Contains(stderr.String(), "string logout") {
		t.Errorf("second trial status %d: %s", status, stderr.String())
	}

	os.Remove(filepath.Join(dir, "credentials.json"))
	refusalStatus, refusal = http.StatusForbidden, `{"error":"anonymous_not_enabled","error_description":"Anonymous registration is switched off."}`
	stderr.Reset()
	if status := run([]string{"trial"}, rt); status != exitAuth || !strings.Contains(stderr.String(), "switched off") {
		t.Errorf("switched-off trial status %d: %s", status, stderr.String())
	}

	// A portal without the endpoint: agent sign-up switched off, or a release that predates it.
	for _, unserved := range []struct {
		status int
		body   string
	}{
		{http.StatusNotFound, `{"error":"not_found"}`},
		{http.StatusForbidden, `{"error":"Forbidden - Cross-origin request rejected"}`},
	} {
		refusalStatus, refusal = unserved.status, unserved.body
		stderr.Reset()
		if status := run([]string{"trial"}, rt); status != exitAuth || !strings.Contains(stderr.String(), "does not offer trial keys") {
			t.Errorf("trial against %d %s: status %d: %s", unserved.status, unserved.body, status, stderr.String())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials.json")); !os.IsNotExist(err) {
		t.Errorf("a refused trial saved a key: %v", err)
	}
}

const claimAttemptAnswer = `{"registration_id":"reg_7","status":"initiated","claim_attempt":{"user_code":"123456","expires_in":600,"verification_uri":"https://portal.example/agent/claim?attempt=cla_7_page","interval":5}}`

// claimHome is a home directory holding what string trial saved: a trial key
// and the registration with its claim token.
func claimHome(t *testing.T) (home, dir string) {
	home = t.TempDir()
	dir = filepath.Join(home, ".config", "string")
	if err := saveCredentials(filepath.Join(dir, "credentials.json"), credentials{APIKey: "trial-key"}); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(filepath.Join(dir, "trial.json"), trialRegistration{RegistrationID: "reg_7", ClaimToken: "clm_7_secret"}); err != nil {
		t.Fatal(err)
	}
	return home, dir
}

func TestTrialClaimShowsTheCodeThenSavesTheKeyOnTheirAccount(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agent-signup/identity/claim":
			var body struct {
				ClaimToken string `json:"claim_token"`
				Email      string `json:"email"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.ClaimToken != "clm_7_secret" || body.Email != "dev@localhost" {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":"invalid_request","error_description":"Unexpected claim."}`)
				return
			}
			io.WriteString(w, claimAttemptAnswer)
		case "/api/agent-signup/token":
			r.ParseForm()
			if r.PostForm.Get("grant_type") != claimGrantType || r.PostForm.Get("claim_token") != "clm_7_secret" {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":"invalid_grant","error_description":"Unexpected poll."}`)
				return
			}
			if polls++; polls < 3 {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":"authorization_pending","error_description":"Not yet."}`)
				return
			}
			io.WriteString(w, `{"access_token":"claimed-key","token_type":"Bearer","organization_id":9}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	defer func(p string) { portalURL = p }(portalURL)
	portalURL = server.URL

	env := map[string]string{"HOME": t.TempDir()}
	var stdout, stderr bytes.Buffer
	var slept []time.Duration
	rt := runtime{
		env:    func(name string) (string, bool) { value, ok := env[name]; return value, ok },
		client: http.DefaultClient,
		stdout: &stdout,
		stderr: &stderr,
		sleep:  func(d time.Duration) { slept = append(slept, d) },
		now:    func() time.Time { return time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC) },
	}

	if status := run([]string{"trial", "claim", "--email", "dev@localhost"}, rt); status != exitUsage || !strings.Contains(stderr.String(), "Run string trial first") {
		t.Errorf("claim with no registration: status %d: %s", status, stderr.String())
	}
	home, dir := claimHome(t)
	env["HOME"] = home
	stderr.Reset()
	if status := run([]string{"trial", "claim", "--poll"}, rt); status != exitUsage || !strings.Contains(stderr.String(), "No claim is waiting") {
		t.Errorf("poll before a claim: status %d: %s", status, stderr.String())
	}

	stderr.Reset()
	if status := run([]string{"trial", "claim", "--email", "dev@localhost"}, rt); status != 0 {
		t.Fatalf("claim status %d: %s", status, stderr.String())
	}
	for _, want := range []string{"https://portal.example/agent/claim?attempt=cla_7_page", "123456", "string trial claim --poll"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("claim output lacks %q: %s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "clm_7_secret") {
		t.Errorf("printed the claim token: %s", stdout.String())
	}
	stdout.Reset()
	if status := run([]string{"trial", "claim", "--poll", "--json"}, rt); status != 0 {
		t.Fatalf("poll status %d: %s", status, stderr.String())
	}
	if polls != 3 || !slices.Equal(slept, []time.Duration{5 * time.Second, 5 * time.Second}) {
		t.Errorf("polled %d times, sleeping %v", polls, slept)
	}
	if strings.Contains(stdout.String(), "claimed-key") || !strings.Contains(stdout.String(), `"status":"claimed"`) {
		t.Errorf("poll output: %s", stdout.String())
	}
	saved, err := loadCredentials(filepath.Join(dir, "credentials.json"))
	if err != nil || saved.APIKey != "claimed-key" || saved.Email != "dev@localhost" {
		t.Errorf("saved credentials %+v, %v", saved, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "trial.json")); !os.IsNotExist(err) {
		t.Errorf("trial.json kept the claim token: %v", err)
	}
}

func TestTrialClaimStopsWhenTheClaimCannotGoOn(t *testing.T) {
	type answer struct {
		status int
		body   string
	}
	var start, token answer
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := start
		if r.URL.Path == "/api/agent-signup/token" {
			polls++
			reply = token
		}
		w.WriteHeader(reply.status)
		io.WriteString(w, reply.body)
	}))
	t.Cleanup(server.Close)
	defer func(p string) { portalURL = p }(portalURL)
	portalURL = server.URL

	home, dir := claimHome(t)
	env := map[string]string{"HOME": home}
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	var stdout, stderr bytes.Buffer
	rt := runtime{
		env:    func(name string) (string, bool) { value, ok := env[name]; return value, ok },
		client: http.DefaultClient,
		stdout: &stdout,
		stderr: &stderr,
		sleep:  func(d time.Duration) { now = now.Add(d) },
		now:    func() time.Time { return now },
	}
	claimThenPoll := func() int {
		stderr.Reset()
		if status := run([]string{"trial", "claim", "--email", "dev@localhost"}, rt); status != 0 {
			return status
		}
		return run([]string{"trial", "claim", "--poll"}, rt)
	}

	start = answer{http.StatusOK, claimAttemptAnswer}
	token = answer{http.StatusBadRequest, `{"error":"expired_token","error_description":"No claim code is live."}`}
	if status := claimThenPoll(); status != exitAuth || !strings.Contains(stderr.String(), "string trial claim --email dev@localhost") {
		t.Errorf("expired code: status %d: %s", status, stderr.String())
	}

	// A portal that answers pending forever is polled only while the code lives.
	token = answer{http.StatusBadRequest, `{"error":"authorization_pending","error_description":"Not yet."}`}
	polls = 0
	if status := claimThenPoll(); status != exitAuth || polls > 600/5+2 {
		t.Errorf("endless pending: status %d after %d polls: %s", status, polls, stderr.String())
	}

	refusals := []struct {
		start  answer
		status int
		says   string
	}{
		{answer{http.StatusBadRequest, `{"error":"claimed_or_in_flight","error_description":"This registration has already been claimed."}`}, exitFailure, "string trial claim --poll"},
		{answer{http.StatusTooManyRequests, `{"error":"rate_limited","message":"Too many sign-up attempts. Wait a few minutes and try again."}`}, exitRateLimit, "Too many sign-up attempts"},
		{answer{http.StatusForbidden, `{"error":"Forbidden - Cross-origin request rejected"}`}, exitAuth, "does not offer trial keys"},
	}
	for _, refusal := range refusals {
		start = refusal.start
		if status := claimThenPoll(); status != refusal.status || !strings.Contains(stderr.String(), refusal.says) {
			t.Errorf("claim answered %d %s: status %d: %s", refusal.start.status, refusal.start.body, status, stderr.String())
		}
	}
	saved, err := loadCredentials(filepath.Join(dir, "credentials.json"))
	if err != nil || saved.APIKey != "trial-key" {
		t.Errorf("a claim that didn't finish replaced the key: %+v, %v", saved, err)
	}
}
