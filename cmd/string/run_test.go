package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func api(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server.URL + "/v1"
}

type result struct {
	status int
	output string
	errors string
}

func invoke(t *testing.T, list []string, baseURL, key string) result {
	t.Helper()
	env := map[string]string{"STRING_API_KEY": key, "STRING_API_BASE_URL": baseURL}
	var stdout, stderr bytes.Buffer
	status := run(list, runtime{
		env: func(name string) (string, bool) {
			value, ok := env[name]
			return value, ok
		},
		lookPath: func(string) (string, error) { return "/usr/local/bin/string", nil },
		client:   http.DefaultClient,
		stdin:    strings.NewReader(""),
		stdout:   &stdout,
		stderr:   &stderr,
	})
	return result{status, stdout.String(), stderr.String()}
}

func inputFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "options.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func decode(t *testing.T, data string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(data), &value); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	return value
}

func expectJSON(t *testing.T, got string, want string) {
	t.Helper()
	if g, w := decode(t, got), decode(t, want); !reflect.DeepEqual(g, w) {
		t.Errorf("got %s, want %s", got, want)
	}
}

func requestBody(t *testing.T, r *http.Request) string {
	t.Helper()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		t.Error(err)
	}
	return string(data)
}

func TestFetchSendsAuthenticatedJSONAndPreservesAdvancedFields(t *testing.T) {
	var path, authorization, body string
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		path, authorization, body = r.URL.Path, r.Header.Get("Authorization"), requestBody(t, r)
		io.WriteString(w, `{"statusCode":200,"data":{"title":"Example"}}`)
	})
	input := inputFile(t, `{"mainContentOnly":true}`)

	got := invoke(t, []string{"fetch", "https://example.com", "--input", input}, baseURL, "test-key")

	if got.status != 0 {
		t.Fatalf("status %d: %s", got.status, got.errors)
	}
	expectJSON(t, got.output, `{"statusCode":200,"data":{"title":"Example"}}`)
	if path != "/v1/fetch" || authorization != "Bearer test-key" {
		t.Errorf("path %q, authorization %q", path, authorization)
	}
	expectJSON(t, body, `{"mainContentOnly":true,"url":"https://example.com/","method":"GET","format":"json"}`)
}

func TestSearchCarriesTargetingAndResultCount(t *testing.T) {
	var path, body string
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		path, body = r.URL.Path, requestBody(t, r)
		io.WriteString(w, `{"results":[],"zeroResults":true}`)
	})

	got := invoke(t, []string{"search", "coffee near me", "--engine", "google", "--location", "Austin", "--count", "20"}, baseURL, "test-key")

	if got.status != 0 || path != "/v1/search" {
		t.Fatalf("status %d, path %q: %s", got.status, path, got.errors)
	}
	expectJSON(t, body, `{"query":"coffee near me","engine":"google","location":"Austin","searchCount":20}`)
	expectJSON(t, got.output, `{"results":[],"zeroResults":true}`)
}

func TestSitemapSubmissionOnlyQuotes(t *testing.T) {
	var calls []string
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		status := "running"
		if len(calls) == 1 {
			status = "awaiting_approval"
		}
		json.NewEncoder(w).Encode(map[string]string{"jobId": "job-1", "status": status})
	})

	quote := invoke(t, []string{"sitemap", "submit", "https://example.com"}, baseURL, "test-key")
	if quote.status != 0 || decode(t, quote.output).(map[string]any)["status"] != "awaiting_approval" {
		t.Fatalf("quote %+v", quote)
	}
	if want := []string{"POST /v1/sitemap"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls %v, want %v", calls, want)
	}

	approval := invoke(t, []string{"sitemap", "approve", "job-1"}, baseURL, "test-key")
	if approval.status != 0 {
		t.Fatalf("approval %+v", approval)
	}
	if want := []string{"POST /v1/sitemap", "POST /v1/sitemap/job-1/approve"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("calls %v, want %v", calls, want)
	}
}

func TestRequestSendsTheDestinationMethodAndBody(t *testing.T) {
	var path, body string
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		path, body = r.URL.Path, requestBody(t, r)
		io.WriteString(w, `{"statusCode":201,"data":{"saved":true}}`)
	})
	input := inputFile(t, `{"body":{"name":"example"},"headers":{"Content-Type":"application/json"}}`)

	got := invoke(t, []string{"request", "POST", "https://example.com/api", "--input", input}, baseURL, "test-key")

	if got.status != 0 || path != "/v1/fetch" {
		t.Fatalf("status %d, path %q: %s", got.status, path, got.errors)
	}
	expectJSON(t, body, `{"url":"https://example.com/api","method":"POST","format":"json","body":{"name":"example"},"headers":{"Content-Type":"application/json"}}`)
	expectJSON(t, got.output, `{"statusCode":201,"data":{"saved":true}}`)
}

func TestSitemapResultsSendsPaginationAsQueryParameters(t *testing.T) {
	var path, limit, offset string
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		path, limit, offset = r.URL.Path, r.URL.Query().Get("limit"), r.URL.Query().Get("offset")
		io.WriteString(w, `{"jobId":"job-1","total":0,"urls":[]}`)
	})

	got := invoke(t, []string{"sitemap", "results", "job-1", "--limit", "100", "--offset", "5"}, baseURL, "test-key")

	if got.status != 0 || path != "/v1/sitemap/job-1/urls" || limit != "100" || offset != "5" {
		t.Errorf("status %d, path %q, limit %q, offset %q: %s", got.status, path, limit, offset, got.errors)
	}
}

func TestFetchFormatOverridesAnInputFormat(t *testing.T) {
	var body string
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		body = requestBody(t, r)
		io.WriteString(w, `{"ok":true}`)
	})
	input := inputFile(t, `{"format":"raw"}`)

	got := invoke(t, []string{"fetch", "https://example.com", "--input", input, "--format", "json"}, baseURL, "test-key")

	if got.status != 0 || decode(t, body).(map[string]any)["format"] != "json" {
		t.Fatalf("status %d, body %s: %s", got.status, body, got.errors)
	}
	if got.output != "{\"ok\":true}\n" {
		t.Errorf("output %q", got.output)
	}
}

func TestAPIBaseURLsWithEmptyQueryOrFragmentAreRejected(t *testing.T) {
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "{}") })
	for _, suffix := range []string{"?", "#"} {
		got := invoke(t, []string{"fetch", "https://example.com"}, baseURL+suffix, "test-key")
		if got.status != 2 || !strings.Contains(got.errors, "STRING_API_BASE_URL must be an HTTP(S) URL") {
			t.Errorf("suffix %q: %+v", suffix, got)
		}
	}
}

func TestAPIErrorsFailWithoutPrintingTheKey(t *testing.T) {
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"bad key","code":"invalid_api_key","next_actions":[{"type":"login","command":"string login"}]}`)
	})

	rejected := invoke(t, []string{"fetch", "https://example.com"}, baseURL, "private-key")
	if rejected.status != 3 || !strings.Contains(rejected.errors, "HTTP 401") || !strings.Contains(rejected.errors, "Next: check STRING_API_KEY") || strings.Contains(rejected.errors, "private-key") {
		t.Errorf("rejected %+v", rejected)
	}

	malformed := invoke(t, []string{"fetch", "https://example.com"}, baseURL, "private\nkey")
	if malformed.status != 3 || !strings.Contains(malformed.errors, "STRING_API_KEY contains characters") || strings.Contains(malformed.errors, "private") {
		t.Errorf("malformed %+v", malformed)
	}
}

func TestWithoutAKeyFetchAndSearchGoKeylessWithOnlyAllowedFields(t *testing.T) {
	var calls []string
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		if _, sent := r.Header["Authorization"]; sent {
			t.Errorf("keyless request sent Authorization")
		}
		calls = append(calls, r.URL.Path+" "+requestBody(t, r))
		w.Header().Set("X-String-Keyless", "true")
		w.Header().Set("X-Keyless-Requests-Remaining", "49")
		w.Header().Set("X-Keyless-Credits-Remaining", "99")
		io.WriteString(w, `{"ok":true}`)
	})

	fetched := invoke(t, []string{"fetch", "https://example.com", "--format", "markdown"}, baseURL, "")
	if fetched.status != 0 || !strings.Contains(fetched.errors, "X-Keyless-Requests-Remaining: 49") {
		t.Fatalf("fetch %+v", fetched)
	}
	searched := invoke(t, []string{"search", "coffee", "--language", "en"}, baseURL, "")
	if searched.status != 0 || len(calls) != 2 {
		t.Fatalf("search %+v, calls %v", searched, calls)
	}
	expectJSON(t, strings.TrimPrefix(calls[0], "/v1/fetch "), `{"url":"https://example.com/","format":"markdown"}`)
	expectJSON(t, strings.TrimPrefix(calls[1], "/v1/search "), `{"query":"coffee","language":"en"}`)

	for _, list := range [][]string{
		{"search", "coffee", "--count", "20"},
		{"request", "POST", "https://example.com"},
		{"sitemap", "list"},
	} {
		if got := invoke(t, list, baseURL, ""); got.status != 3 || !strings.Contains(got.errors, "string login") {
			t.Errorf("%v: %+v", list, got)
		}
	}
	if len(calls) != 2 {
		t.Errorf("key-only commands reached the API: %v", calls)
	}
}

func TestKeylessRefusalsCarryRetryAfterAndTheNextStepAsJSON(t *testing.T) {
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.Header().Set("X-Request-Id", "req-1")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":"Keyless limit reached","message":"It resets at 00:00 UTC.","signupUrl":"https://portal.usestring.ai/sign-up"}`)
	})

	got := invoke(t, []string{"search", "coffee", "--json"}, baseURL, "")
	if got.status != 3 {
		t.Fatalf("%+v", got)
	}
	envelope := decode(t, got.errors).(map[string]any)["error"].(map[string]any)
	if envelope["status"] != 429.0 || envelope["retry_after_seconds"] != 3600.0 || envelope["request_id"] != "req-1" || !strings.Contains(envelope["next_step"].(string), "string login") {
		t.Errorf("envelope %v", envelope)
	}
}

func TestLoginWithTokenChecksAndSavesAKeyFromStdin(t *testing.T) {
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sitemap" || r.Header.Get("Authorization") != "Bearer good-key" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"Invalid API key"}`)
			return
		}
		io.WriteString(w, `{"jobs":[]}`)
	})
	home := t.TempDir()
	env := map[string]string{"HOME": home, "STRING_API_BASE_URL": baseURL}
	login := func(stdin string) result {
		var stdout, stderr bytes.Buffer
		status := run([]string{"login", "--with-token"}, runtime{
			env: func(name string) (string, bool) {
				value, ok := env[name]
				return value, ok
			},
			client: http.DefaultClient,
			stdin:  strings.NewReader(stdin),
			stdout: &stdout,
			stderr: &stderr,
		})
		return result{status, stdout.String(), stderr.String()}
	}
	path := filepath.Join(home, ".config", "string", "credentials.json")

	if got := login("bad-key\n"); got.status != 3 || strings.Contains(got.output+got.errors, "bad-key") {
		t.Errorf("bad key %+v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("saved a rejected key: %v", err)
	}
	if got := login("good-key\n"); got.status != 0 || strings.Contains(got.output+got.errors, "good-key") {
		t.Fatalf("good key %+v", got)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file %v, %v", info, err)
	}
	if saved, _ := loadCredentials(path); saved.APIKey != "good-key" {
		t.Errorf("saved %+v", saved)
	}
}

func TestInitInstallsTheSkillForDetectedAgentsOnly(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HOME": home}
	var stdout, stderr bytes.Buffer
	rt := runtime{
		env: func(name string) (string, bool) {
			value, ok := env[name]
			return value, ok
		},
		lookPath: func(string) (string, error) { return "", os.ErrNotExist },
		stdout:   &stdout,
		stderr:   &stderr,
	}

	if status := run([]string{"init"}, rt); status != 0 {
		t.Fatalf("status %d: %s", status, stderr.String())
	}
	for _, dir := range []string{".agents/skills", ".claude/skills"} {
		if _, err := os.Stat(filepath.Join(home, dir, "string-cli", "SKILL.md")); err != nil {
			t.Errorf("%s: %v", dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor")); !os.IsNotExist(err) {
		t.Errorf("created a skills directory for an agent that is not installed: %v", err)
	}
	for _, hint := range []string{"npm install -g @usestring/cli", "string login"} {
		if !strings.Contains(stdout.String(), hint) {
			t.Errorf("output lacks %q:\n%s", hint, stdout.String())
		}
	}

	stderr.Reset()
	if status := run([]string{"init", "--agent", "cursor,nope"}, rt); status != 2 || !strings.Contains(stderr.String(), "Unknown agent: nope") {
		t.Errorf("status %d: %s", status, stderr.String())
	}
}

func TestLoginSavesTheReleasedKeyForLaterCommands(t *testing.T) {
	var tokenAnswers []string
	var fetchAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.URL.Path {
		case "/oauth/device_authorization":
			if r.Form.Get("client_id") != "test-client" || !strings.Contains(r.Form.Get("scope"), "web_access") {
				t.Errorf("device authorization form %v", r.Form)
			}
			io.WriteString(w, `{"device_code":"device-1","user_code":"ABCD-EFGH","verification_uri":"https://example.test/device","verification_uri_complete":"https://example.test/device?code=ABCD-EFGH","expires_in":600,"interval":5}`)
		case "/oauth/token":
			if r.Form.Get("device_code") != "device-1" {
				t.Errorf("token form %v", r.Form)
			}
			answer := tokenAnswers[0]
			tokenAnswers = tokenAnswers[1:]
			if strings.Contains(answer, "error") {
				w.WriteHeader(http.StatusBadRequest)
			}
			io.WriteString(w, answer)
		case "/api/oauth/api-key":
			if r.Header.Get("Authorization") != "Bearer access-1" {
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `{"error":"Invalid or expired access token"}`)
				return
			}
			io.WriteString(w, `{"apiKey":"released-key"}`)
		case "/v1/fetch":
			fetchAuthorization = r.Header.Get("Authorization")
			io.WriteString(w, `{"ok":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	defer func(a, p, c string) { authURL, portalURL, oauthClientID = a, p, c }(authURL, portalURL, oauthClientID)
	authURL, portalURL, oauthClientID = server.URL, server.URL, "test-client"

	home := t.TempDir()
	env := map[string]string{"HOME": home, "STRING_API_BASE_URL": server.URL + "/v1"}
	var stdout, stderr bytes.Buffer
	var opened []string
	var waits []time.Duration
	rt := runtime{
		env: func(name string) (string, bool) {
			value, ok := env[name]
			return value, ok
		},
		client:  http.DefaultClient,
		stdout:  &stdout,
		stderr:  &stderr,
		openURL: func(link string) error { opened = append(opened, link); return nil },
		sleep:   func(d time.Duration) { waits = append(waits, d) },
	}

	// Without a terminal, login hands back the link at once and --poll finishes.
	if status := run([]string{"login", "--json"}, rt); status != 0 {
		t.Fatalf("login status %d: %s", status, stderr.String())
	}
	started := decode(t, stdout.String()).(map[string]any)
	if started["user_code"] != "ABCD-EFGH" || started["next"] != "string login --poll" || !reflect.DeepEqual(opened, []string{"https://example.test/device?code=ABCD-EFGH"}) {
		t.Errorf("output %q, opened %v", stdout.String(), opened)
	}
	pendingFile := filepath.Join(home, ".config", "string", "pending.json")
	if info, err := os.Stat(pendingFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("pending file %v, %v", info, err)
	}
	tokenAnswers = []string{`{"error":"authorization_pending"}`, `{"error":"slow_down"}`, `{"access_token":"access-1","token_type":"Bearer"}`}
	if status := run([]string{"login", "--poll"}, rt); status != 0 {
		t.Fatalf("login --poll status %d: %s", status, stderr.String())
	}
	if _, err := os.Stat(pendingFile); !os.IsNotExist(err) {
		t.Errorf("pending file left after sign-in: %v", err)
	}
	if want := []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second}; !reflect.DeepEqual(waits, want) {
		t.Errorf("waits %v, want %v", waits, want)
	}
	path := filepath.Join(home, ".config", "string", "credentials.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file %v, %v", info, err)
	}

	if status := run([]string{"fetch", "https://example.com"}, rt); status != 0 || fetchAuthorization != "Bearer released-key" {
		t.Errorf("fetch status %d, authorization %q: %s", status, fetchAuthorization, stderr.String())
	}
	stdout.Reset()
	if status := run([]string{"whoami"}, rt); status != 0 || !strings.Contains(stdout.String(), path) {
		t.Errorf("whoami status %d: %s", status, stdout.String())
	}
	if status := run([]string{"logout"}, rt); status != 0 {
		t.Errorf("logout status %d: %s", status, stderr.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("credentials file still there: %v", err)
	}
	stderr.Reset()
	if status := run([]string{"whoami"}, rt); status != 3 || !strings.Contains(stderr.String(), "Not logged in") {
		t.Errorf("whoami after logout status %d: %s", status, stderr.String())
	}

	stderr.Reset()
	tokenAnswers = []string{`{"error":"access_denied"}`}
	rt.terminal = true
	if status := run([]string{"login"}, rt); status != 3 || !strings.Contains(stderr.String(), "Sign-in was denied") {
		t.Errorf("denied login status %d: %s", status, stderr.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a denied login saved a key: %v", err)
	}

	stdout.Reset()
	rt.lookPath = func(string) (string, error) { return "/usr/local/bin/string", nil }
	tokenAnswers = []string{`{"access_token":"access-1","token_type":"Bearer"}`}
	if status := run([]string{"init"}, rt); status != 0 || !strings.Contains(stdout.String(), "Logged in") {
		t.Errorf("init from a terminal status %d: %s%s", status, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("init from a terminal saved no key: %v", err)
	}
}
