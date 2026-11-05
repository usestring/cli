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
	"strings"
	"testing"
)

func TestTrialSolvesTheProofOfWorkAndSavesTheKeyAndClaimToken(t *testing.T) {
	enabled := true
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
		if !enabled {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"error":"anonymous_not_enabled","error_description":"Anonymous registration is switched off."}`)
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
	enabled = false
	stderr.Reset()
	if status := run([]string{"trial"}, rt); status != exitAuth || !strings.Contains(stderr.String(), "switched off") {
		t.Errorf("switched-off trial status %d: %s", status, stderr.String())
	}
}
