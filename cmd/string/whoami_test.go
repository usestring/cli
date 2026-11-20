package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// whoami checks the key with one request that spends no credit: it says
// whether String takes it, exits 3 when String refuses it, and 6 when it
// couldn't ask. --offline says where the key comes from without asking.
func TestWhoamiChecksTheKeyWithString(t *testing.T) {
	const savedKey, envKey = "saved-key-0123456789abcdef", "env-key-fedcba9876543210"
	answer, sent := http.StatusOK, ""
	base := api(t, func(w http.ResponseWriter, r *http.Request) {
		sent = r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization")
		w.WriteHeader(answer)
		io.WriteString(w, `{"error":"Invalid API key"}`)
	})
	whoami := func(client *http.Client, env string, saved bool, list ...string) result {
		home := t.TempDir()
		vars := map[string]string{"HOME": home, "STRING_API_BASE_URL": base}
		if env != "" {
			vars["STRING_API_KEY"] = env
		}
		if saved {
			if err := saveCredentials(filepath.Join(home, ".config", "string", "credentials.json"), credentials{APIKey: savedKey, Email: "dev@localhost"}); err != nil {
				t.Fatal(err)
			}
		}
		var stdout, stderr bytes.Buffer
		status := run(append([]string{"whoami"}, list...), runtime{env: envOf(vars), client: client, stdout: &stdout, stderr: &stderr})
		return result{status, stdout.String(), strings.ReplaceAll(stderr.String(), home, "HOME")}
	}
	path := filepath.Join("HOME", ".config", "string", "credentials.json")

	got := whoami(http.DefaultClient, "", true)
	if !strings.HasSuffix(got.output, "/.config/string/credentials.json (saved-ke…). String accepts it.\n") || !strings.HasPrefix(got.output, "Logged in as dev@localhost, with the key saved in ") || got.status != 0 || sent != "GET /v1/account/balance Bearer "+savedKey {
		t.Errorf("a saved key: %+v, sent %q", got, sent)
	}
	if got := whoami(http.DefaultClient, envKey, false); got.status != 0 || got.output != "Using STRING_API_KEY from the environment (env-key-…). String accepts it.\n" || sent != "GET /v1/account/balance Bearer "+envKey {
		t.Errorf("STRING_API_KEY: %+v, sent %q", got, sent)
	}
	if got := whoami(http.DefaultClient, envKey, true); got.status != 0 || !strings.HasPrefix(got.output, "Using STRING_API_KEY from the environment. It wins over the key saved in ") || !strings.HasSuffix(got.output, " (env-key-…). String accepts it.\n") {
		t.Errorf("both: %+v", got)
	}
	if got := whoami(http.DefaultClient, "", true, "--json"); got.status != 0 || !strings.Contains(got.output, `"accepted":true`) || !strings.Contains(got.output, `"key_prefix":"saved-ke…"`) || !strings.Contains(got.output, `"source":"saved"`) || !strings.Contains(got.output, `"email":"dev@localhost"`) {
		t.Errorf("--json: %+v", got)
	}

	answer = http.StatusUnauthorized
	if got := whoami(http.DefaultClient, "", true); got.status != exitAuth || got.errors != "error: String refused the key saved in "+path+" (saved-ke…).\n\nnext: run `string login` to save a new key\n" {
		t.Errorf("a refused saved key: %+v", got)
	}
	if got := whoami(http.DefaultClient, envKey, false); got.status != exitAuth || got.errors != "error: String refused the key in STRING_API_KEY (env-key-…).\n\nnext: check STRING_API_KEY, or unset it and run `string login`\n" {
		t.Errorf("a refused STRING_API_KEY: %+v", got)
	}
	answer = http.StatusServiceUnavailable
	if got := whoami(http.DefaultClient, envKey, false); got.status != exitUpstream || !strings.HasPrefix(got.errors, "error: Using STRING_API_KEY from the environment (env-key-…), but string couldn't check it with String.\n  ") {
		t.Errorf("String failing: %+v", got)
	}
	unreachable := &http.Client{Transport: failingTransport{errors.New("no route")}}
	if got := whoami(unreachable, envKey, false); got.status != exitUpstream || !strings.Contains(got.errors, "but string couldn't check it with String.\n  string lost the connection to ") {
		t.Errorf("no network: %+v", got)
	}

	offline := &http.Client{Transport: noNetwork{t}}
	if got := whoami(offline, "", true, "--offline"); got.status != 0 || !strings.HasPrefix(got.output, "Logged in as dev@localhost, with the key saved in ") || !strings.HasSuffix(got.output, "credentials.json.\n") {
		t.Errorf("--offline: %+v", got)
	}
	// With --json, --offline gives the object too, without accepted, since
	// nothing asked String.
	got = whoami(offline, envKey, false, "--offline", "--json")
	if shown, _ := decode(t, got.output).(map[string]any); got.status != 0 || shown["source"] != "STRING_API_KEY" || shown["key_prefix"] != "env-key-…" || shown["accepted"] != nil {
		t.Errorf("--offline --json: %+v", got)
	}
	if got := whoami(offline, "", false); got.status != exitAuth || !strings.HasPrefix(got.errors, "error: Not logged in.") {
		t.Errorf("no key: %+v", got)
	}
	if keyPrefix("abcdef") != "ab…" || keyPrefix("0123456789abcdefghijklmnop") != "01234567…" {
		t.Errorf("keyPrefix: %q, %q", keyPrefix("abcdef"), keyPrefix("0123456789abcdefghijklmnop"))
	}
}
