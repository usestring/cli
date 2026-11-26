package main

import (
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// trialChallenge asks for a proof of work small enough to solve at once.
	trialChallenge = `{"error":"proof_of_work_required","proof_of_work":{"challenge":"v1.challenge","difficulty":4,"algorithm":"sha256"}}`
	// trialRegistered is a key that doesn't expire, with a week to claim it.
	trialRegistered = `{"registration_id":"reg_7","access_token":"trial-key","token_type":"Bearer","access_token_expires":"9999-12-31T00:00:00.000Z","balance_usd":"0.50","claim_url":"https://portal.example/api/agent-signup/identity/claim","claim_token":"clm_7_secret","claim_token_expires":"2026-11-25T12:00:00.000Z"}`
	// initHandsBack is the sign-in init hands back under an agent.
	initHandsBack = "\nSigning in. string fetch and string search already work without a key, within a daily allowance. For more:\nOpen https://example.test/device?code=ABCD-EFGH and confirm the code ABCD-EFGH.\nThen run string login --poll to save the key. The code expires in 10 minutes.\n"
)

func TestInitUnderAnAgentRegistersATrialKeyAndStartsNoSignIn(t *testing.T) {
	service := &signInService{
		trial: []signInAnswer{{http.StatusUnauthorized, trialChallenge, ""}, {http.StatusOK, trialRegistered, ""}},
		token: []signInAnswer{signInApproved},
		key:   []signInAnswer{signInKey},
	}
	service.start(t)
	session := newSignInSession(t)
	session.env["CLAUDE_CODE_CHILD_SESSION"] = "1"

	got := session.run("init")

	registered := fmt.Sprintf("\n\nRegistered a trial key with $0.50 of credit. It doesn't expire and is saved in %s.\nA person can claim this agent onto their String account until Wed, 25 Nov 2026 12:00:00 UTC. The claim token is saved in %s.\n", session.file("credentials.json"), session.file("trial.json"))
	if got.status != 0 || got.errors != "" || !strings.HasPrefix(got.output, "Installed the string-cli skill") || !strings.HasSuffix(got.output, registered) {
		t.Errorf("init under an agent: %+v\nwant exit 0 and output ending\n%s", got, registered)
	}
	if strings.Contains(got.output, "trial-key") || strings.Contains(got.output, "clm_7_secret") {
		t.Errorf("init printed a secret: %s", got.output)
	}
	if trials, signIns := service.requests("/api/agent-signup/identity"), service.requests("/oauth/device_authorization"); trials != 2 || signIns != 0 {
		t.Errorf("init sent %d trial registrations and started %d sign-ins, want 2 (the challenge, then the proof) and none", trials, signIns)
	}
	saved, err := loadCredentials(session.file("credentials.json"))
	if err != nil || saved.APIKey != "trial-key" {
		t.Errorf("saved credentials %+v, %v", saved, err)
	}
	for _, name := range []string{"credentials.json", "trial.json"} {
		if info, err := os.Stat(session.file(name)); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, %v", name, info, err)
		}
	}
	if data, _ := os.ReadFile(session.file("trial.json")); !strings.Contains(string(data), `"claimToken": "clm_7_secret"`) {
		t.Errorf("trial.json: %s", data)
	}
	if _, err := os.Stat(filepath.Join(session.env["HOME"], ".agents", "skills", "string-cli", "SKILL.md")); err != nil {
		t.Errorf("the skill is not installed: %v", err)
	}

	// The key is there now, so a second init registers nothing and signs
	// nobody in.
	before := service.requests("")
	again := session.run("init")
	if again.status != 0 || strings.Contains(again.output, "Registered") || strings.Contains(again.output, "Signing in") || service.requests("") != before {
		t.Errorf("init with the trial key saved: %+v, and %d requests sent", again, service.requests("")-before)
	}
}

func TestInitHandsBackTheSignInWhenThePortalGivesNoTrialKey(t *testing.T) {
	for _, c := range []struct {
		about  string
		answer signInAnswer
		// note is the line init prints before the sign-in, if any.
		note string
	}{
		{"a portal without trial keys", signInAnswer{http.StatusNotFound, `{"error":"not_found"}`, ""}, ""},
		{"trial keys switched off", signInAnswer{http.StatusForbidden, `{"error":"anonymous_not_enabled","error_description":"Anonymous agent registration is not enabled."}`, ""}, ""},
		{"a rate limit", signInAnswer{http.StatusTooManyRequests, `{"error":"rate_limited","message":"Too many trial keys from this network today."}`, ""}, "\nNo trial key: Too many trial keys from this network today.\n"},
		{"a portal error", signInAnswer{http.StatusBadGateway, `{"error":"server_error"}`, ""}, "\nNo trial key: Trial registration failed: HTTP 502 server_error.\n"},
	} {
		service := &signInService{trial: []signInAnswer{c.answer}, token: []signInAnswer{signInApproved}, key: []signInAnswer{signInKey}}
		service.start(t)
		session := newSignInSession(t)

		got := session.run("init")

		want := "\n" + c.note + initHandsBack
		// A portal that offers no trial keys is not worth a line.
		if got.status != 0 || got.errors != "" || !strings.HasSuffix(got.output, want) || strings.Count(got.output, "trial key") != strings.Count(c.note, "trial key") {
			t.Errorf("%s: %+v\nwant exit 0 and output ending\n%s", c.about, got, want)
		}
		if trials, signIns := service.requests("/api/agent-signup/identity"), service.requests("/oauth/device_authorization"); trials != 1 || signIns != 1 {
			t.Errorf("%s: %d trial registrations and %d sign-ins, want one of each", c.about, trials, signIns)
		}
		for _, name := range []string{"credentials.json", "trial.json"} {
			if _, err := os.Stat(session.file(name)); !os.IsNotExist(err) {
				t.Errorf("%s: %s is there: %v", c.about, name, err)
			}
		}
		// login --poll finishes the sign-in init handed back.
		if done := session.run("login", "--poll"); done.status != 0 || !strings.HasPrefix(done.output, "Logged in as dev@localhost.") {
			t.Errorf("%s: login --poll after init: %+v", c.about, done)
		}
	}
}

func TestInitRegistersATrialKeyOnlyWhenNobodyCanApproveASignIn(t *testing.T) {
	for _, c := range []struct {
		about    string
		env      map[string]string
		args     []string
		terminal bool
	}{
		{about: "a person at a terminal", terminal: true},
		{about: "CI", env: map[string]string{"CI": "true"}},
		{about: "--no-login under an agent", env: map[string]string{"STRING_AGENT": "my-bot"}, args: []string{"--no-login"}},
		{about: "--browser under an agent", env: map[string]string{"STRING_AGENT": "my-bot"}, args: []string{"--browser"}},
		{about: "--device-name under an agent", env: map[string]string{"STRING_AGENT": "my-bot"}, args: []string{"--device-name", "build box"}},
		{about: "a key set under an agent", env: map[string]string{"STRING_AGENT": "my-bot", "STRING_API_KEY": "set-key"}},
		{about: "a key setting init cannot use", env: map[string]string{"STRING_AGENT": "my-bot", "STRING_API_KEY": "not a key"}},
	} {
		service := &signInService{trial: []signInAnswer{{http.StatusOK, trialRegistered, ""}}, token: []signInAnswer{signInApproved}, key: []signInAnswer{signInKey}, status: []signInAnswer{browserComplete}}
		service.start(t)
		session := newSignInSession(t)
		session.terminal, session.progress = c.terminal, c.terminal
		maps.Copy(session.env, c.env)

		got := session.run(append([]string{"init"}, c.args...)...)

		if got.status != 0 || strings.Contains(got.output, "trial key") {
			t.Errorf("%s: %+v", c.about, got)
		}
		if trials := service.requests("/api/agent-signup/identity"); trials != 0 {
			t.Errorf("%s: init sent %d trial registrations, want none", c.about, trials)
		}
		if _, err := os.Stat(session.file("trial.json")); !os.IsNotExist(err) {
			t.Errorf("%s: trial.json is there: %v", c.about, err)
		}
	}
}

func TestABuildWithNoClientRegistersNoTrialKeyUnderAnAgent(t *testing.T) {
	service := &signInService{trial: []signInAnswer{{http.StatusOK, trialRegistered, ""}}}
	service.start(t)
	oauthClientID = ""
	session := newSignInSession(t)
	session.env["STRING_AGENT"] = "my-bot"

	got := session.run("init")

	if got.status != 0 || got.errors != "" || !strings.HasSuffix(got.output, "\n\n"+signInHint) || service.requests("") != 0 {
		t.Errorf("init from a build with no client: %+v, and %d requests sent", got, service.requests(""))
	}
}
