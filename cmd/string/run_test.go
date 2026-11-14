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
		sleep:    func(time.Duration) {},
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
	expectJSON(t, body, `{"mainContentOnly":true,"url":"https://example.com/","method":"GET","format":"markdown"}`)
}

func TestFieldFlagsAreTheAPIFieldsOfTheSameName(t *testing.T) {
	var bodies []string
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		bodies = append(bodies, r.URL.Path+" "+requestBody(t, r))
		io.WriteString(w, "# Example\n")
	})
	schema := inputFile(t, `{"type":"object","properties":{"title":{"type":"string"}}}`)

	for _, list := range [][]string{
		{"https://example.com", "--mainContentOnly", "--countryCode", "DE", "--maxRedirects", "3", "--headers", `{"Accept-Language":"de"}`, "--solveCaptcha=false"},
		{"fetch", "https://example.com", "--jsonSchema", "@" + schema},
		{"search", "coffee", "--searchType", "images", "--dateRange", "week", "--count", "5", "--safeSearch"},
		{"search", "coffee", "--dateRange", `{"from":"2024-01-01"}`},
	} {
		if got := invoke(t, list, baseURL, "test-key"); got.status != 0 {
			t.Fatalf("%v: %+v", list, got)
		}
	}
	want := []string{
		`/v1/fetch {"url":"https://example.com/","method":"GET","format":"markdown","mainContentOnly":true,"countryCode":"DE","maxRedirects":3,"headers":{"Accept-Language":"de"},"solveCaptcha":false}`,
		// A schema comes back only in JSON, so it makes JSON the default.
		`/v1/fetch {"url":"https://example.com/","method":"GET","format":"json","jsonSchema":{"type":"object","properties":{"title":{"type":"string"}}}}`,
		`/v1/search {"query":"coffee","searchType":"images","dateRange":"week","searchCount":5,"safeSearch":true}`,
		`/v1/search {"query":"coffee","dateRange":{"from":"2024-01-01"}}`,
	}
	for i := range want {
		gotPath, gotBody, _ := strings.Cut(bodies[i], " ")
		wantPath, wantBody, _ := strings.Cut(want[i], " ")
		if gotPath != wantPath {
			t.Errorf("call %d went to %s, want %s", i, gotPath, wantPath)
		}
		expectJSON(t, gotBody, wantBody)
	}

	for list, message := range map[string]string{
		"sitemap submit https://example.com --max-pages 5": "Did you mean '--maxPages'?",
		"fetch https://example.com --maxRedirects many":    "--maxRedirects must be an integer",
		"fetch https://example.com --executeJS=yes":        "--executeJS takes no value, or =true or =false",
		"search coffee --coordinates {":                    "--coordinates must be JSON",
	} {
		if got := invoke(t, strings.Fields(list), baseURL, "test-key"); got.status != 2 || !strings.Contains(got.errors, message) {
			t.Errorf("%s: %+v", list, got)
		}
	}
	if len(bodies) != len(want) {
		t.Errorf("bad flags reached the API: %v", bodies[len(want):])
	}
}

func TestOutputWritesTheResponseToAFile(t *testing.T) {
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "# Example") })
	path := filepath.Join(t.TempDir(), "page.md")

	got := invoke(t, []string{"https://example.com", "-o", path}, baseURL, "test-key")

	saved, err := os.ReadFile(path)
	if got.status != 0 || got.output != "" || err != nil || string(saved) != "# Example\n" || !strings.Contains(got.errors, "Saved 10 bytes to "+path) {
		t.Errorf("%+v, saved %q, %v", got, saved, err)
	}
}

func TestSitemapApproveAndWaitFollowTheCrawlToItsEnd(t *testing.T) {
	var calls []string
	statuses := []string{
		`{"jobId":"job-1","status":"running","pending":2,"processed":1}`,
		`{"jobId":"job-1","status":"completed","pagesProcessed":3}`,
	}
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/sitemap":
			io.WriteString(w, `{"jobId":"job-1","status":"awaiting_approval","estimatedCostUsd":0.01}`)
		case r.Method == "POST":
			io.WriteString(w, `{"jobId":"job-1","status":"running"}`)
		default:
			io.WriteString(w, statuses[0])
			statuses = statuses[1:]
		}
	})
	var stdout, stderr bytes.Buffer
	status := run([]string{"sitemap", "submit", "https://example.com", "--maxPages", "3", "--approve", "--wait"}, runtime{
		env: func(name string) (string, bool) {
			value, ok := map[string]string{"STRING_API_KEY": "test-key", "STRING_API_BASE_URL": baseURL}[name]
			return value, ok
		},
		client:   http.DefaultClient,
		stdin:    strings.NewReader(""),
		stdout:   &stdout,
		stderr:   &stderr,
		sleep:    func(time.Duration) {},
		progress: true,
	})

	if status != 0 {
		t.Fatalf("status %d: %s", status, stderr.String())
	}
	if want := []string{"POST /v1/sitemap", "POST /v1/sitemap/job-1/approve", "GET /v1/sitemap/job-1", "GET /v1/sitemap/job-1"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("calls %v, want %v", calls, want)
	}
	expectJSON(t, stdout.String(), `{"jobId":"job-1","status":"completed","pagesProcessed":3}`)
	if !strings.Contains(stderr.String(), "Crawling: 1 pages done, 2 queued") {
		t.Errorf("progress %q", stderr.String())
	}

	if got := invoke(t, []string{"sitemap", "submit", "https://example.com", "--wait"}, baseURL, "test-key"); got.status != 2 || !strings.Contains(got.errors, "--wait needs --approve") {
		t.Errorf("wait without approve: %+v", got)
	}
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

func TestInitAddsTheMCPServerToClaudeCodeAndCodexOnceWithoutAKey(t *testing.T) {
	home := t.TempDir()
	for _, dir := range []string{".claude", ".codex"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	codexConfig := filepath.Join(home, ".codex", "config.toml")
	existing := "model = \"gpt-5\"\n\n[mcp_servers.other]\ncommand = \"npx\""
	if err := os.WriteFile(codexConfig, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HOME": home, "STRING_API_KEY": "secret-key"}
	var ran [][]string
	var stdout, stderr bytes.Buffer
	rt := runtime{
		env: func(name string) (string, bool) {
			value, ok := env[name]
			return value, ok
		},
		lookPath: func(name string) (string, error) {
			if name == "claude" {
				return "/opt/bin/claude", nil
			}
			return "", os.ErrNotExist
		},
		// Like claude mcp add, record the server in Claude Code's user settings.
		program: func(name string, args ...string) ([]byte, error) {
			ran = append(ran, append([]string{name}, args...))
			settings := `{"mcpServers":{"string-web-access":{"type":"http","url":"https://mcp.usestring.ai/v3/mcp"}}}`
			return nil, os.WriteFile(filepath.Join(home, ".claude.json"), []byte(settings), 0o600)
		},
		stdout: &stdout,
		stderr: &stderr,
	}

	if status := run([]string{"init", "--no-mcp"}, rt); status != 0 {
		t.Fatalf("status %d: %s", status, stderr.String())
	}
	if got, _ := os.ReadFile(codexConfig); len(ran) > 0 || string(got) != existing {
		t.Fatalf("--no-mcp ran %q and left Codex's config as %q", ran, got)
	}
	for range 2 {
		if status := run([]string{"init"}, rt); status != 0 {
			t.Fatalf("status %d: %s", status, stderr.String())
		}
	}
	add := []string{"/opt/bin/claude", "mcp", "add", "--transport", "http", "--scope", "user", "string-web-access", "https://mcp.usestring.ai/v3/mcp"}
	if !reflect.DeepEqual(ran, [][]string{add}) {
		t.Errorf("ran %q, want %q once", ran, add)
	}
	want := existing + "\n\n[mcp_servers.string-web-access]\nurl = \"https://mcp.usestring.ai/v3/mcp\"\n"
	if got, _ := os.ReadFile(codexConfig); string(got) != want {
		t.Errorf("Codex's config is\n%s\nwant\n%s", got, want)
	}
}

func TestInitLeavesACodexConfigItCannotExtendUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	// TOML forbids adding [mcp_servers.NAME] to an inline table, and Codex
	// will not start with a config that does.
	existing := "mcp_servers = { other = { command = \"npx\" } }\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	addCodexMCP(dir)
	if got, _ := os.ReadFile(path); string(got) != existing {
		t.Errorf("config became %q", got)
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

func TestIntegrationsListAndRunTakeTheInputFieldsAsFlags(t *testing.T) {
	const manifest = `{"manifestVersion":2,"sites":[{"name":"reddit","title":"Reddit","version":"1.0.1",
		"description":"Reddit posts with their comment trees. And more.",
		"actions":[{"name":"post","description":"Read one Reddit post and its comment tree.",
			"input":{"type":"object","required":["post"],"properties":{
				"post":{"type":"string","description":"The post's URL or id."},
				"sort":{"type":"string","enum":["best","top"]},
				"maxPages":{"type":["null","integer"]},
				"flairs":{"type":"array","items":{"type":"string"}}}},
			"examples":[{"input":{"post":"https://www.reddit.com/r/golang/comments/1xapaw7/","maxPages":3}}]}]}]}`
	var posts []string
	var authorized []bool
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		_, sent := r.Header["Authorization"]
		authorized = append(authorized, sent)
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/integrations", "GET /v1/integrations/reddit":
			io.WriteString(w, manifest)
		case "POST /v1/integrations/reddit/post":
			posts = append(posts, requestBody(t, r))
			io.WriteString(w, `{"envelopeVersion":1,"data":[{"id":"1xapaw7"}],"failures":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	listed := invoke(t, []string{"integrations"}, baseURL, "")
	if listed.status != 0 || !strings.Contains(listed.output, "reddit         Reddit posts with their comment trees.\n  post         Read one Reddit post") || authorized[0] {
		t.Errorf("list %+v", listed)
	}
	site := invoke(t, []string{"integrations", "list", "reddit"}, baseURL, "")
	for _, want := range []string{"string integrations run reddit post\n", "--post TEXT\n      (required) The post's URL or id.", "--sort best|top", "--flairs TEXT (repeatable)", "Example: string integrations run reddit post --post https://www.reddit.com/r/golang/comments/1xapaw7/ --maxPages 3"} {
		if !strings.Contains(site.output, want) {
			t.Errorf("list reddit lacks %q:\n%s", want, site.output)
		}
	}

	ran := invoke(t, []string{"integrations", "run", "reddit", "post", "--post", "1xapaw7", "--maxPages", "2", "--flairs", "News", "--flairs", "Help"}, baseURL, "test-key")
	if ran.status != 0 || len(posts) != 1 {
		t.Fatalf("run %+v, posts %v", ran, posts)
	}
	expectJSON(t, posts[0], `{"post":"1xapaw7","maxPages":2,"flairs":["News","Help"]}`)
	expectJSON(t, ran.output, `{"envelopeVersion":1,"data":[{"id":"1xapaw7"}],"failures":[]}`)

	if got := invoke(t, []string{"integrations", "run", "reddit", "post", "--max-pages", "2"}, baseURL, "test-key"); got.status != 2 || !strings.Contains(got.errors, "Did you mean '--maxPages'?") {
		t.Errorf("misspelled flag %+v", got)
	}
	if got := invoke(t, []string{"integrations", "run", "reddit", "post", "--post", "1xapaw7"}, baseURL, ""); got.status != 3 || !strings.Contains(got.errors, "needs a key") {
		t.Errorf("keyless run %+v", got)
	}
	if len(posts) != 1 {
		t.Errorf("refused runs reached the API: %v", posts)
	}
}

// runAt runs the CLI with env as its whole environment and a fixed clock.
func runAt(t *testing.T, list []string, env map[string]string, now time.Time) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	status := run(list, runtime{
		env: func(name string) (string, bool) {
			value, ok := env[name]
			return value, ok
		},
		client: http.DefaultClient,
		stdin:  strings.NewReader(""),
		stdout: &stdout,
		stderr: &stderr,
		now:    func() time.Time { return now },
	})
	return result{status, stdout.String(), stderr.String()}
}

func TestStatusShowsTheBalanceWhatIsHeldThePlanAndTopUp(t *testing.T) {
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("%s %s without the key", r.Method, r.URL.Path)
		}
		switch r.URL.Path {
		case "/v1/account/balance":
			io.WriteString(w, `{"currency":"USD","balance":25,"spent":10.5,"held":2.25,"remaining":12.25,"exhausted":false,"asOf":"2026-11-05T09:30:00Z",
				"plan":{"tier":"pro","name":"Pro","priceUsdMonth":49},
				"autoTopUp":{"enabled":true,"amountUsd":20,"maxPerPeriodUsd":100,"spentThisPeriodUsd":20,"remainingThisPeriodUsd":80}}`)
		case "/v1/pricing":
			io.WriteString(w, `{"plans":[{"id":"pro","requestsPerSecond":20}],"free":{"requestsPerSecond":2}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	env := map[string]string{"STRING_API_KEY": "test-key", "STRING_API_BASE_URL": baseURL}
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

	got := runAt(t, []string{"status"}, env, now)
	want := "string " + version + `
Key       STRING_API_KEY from the environment
Balance   $12.25 left, with usage counted to 09:30 UTC
Held      $2.25 for crawls still running
Plan      Pro, 20 requests a second
Top-up    on, $20.00 at a time, $80.00 left this period
`
	if got.status != 0 || got.output != want {
		t.Errorf("got %+v, want output\n%s", got, want)
	}

	got = runAt(t, []string{"status", "--json"}, env, now)
	report := decode(t, got.output).(map[string]any)
	if got.status != 0 || report["version"] != version || report["key"].(map[string]any)["source"] != "environment" ||
		report["balance"].(map[string]any)["remaining"] != 12.25 || report["requestsPerSecond"] != 20.0 || report["keyless"] != nil {
		t.Errorf("--json %+v", got)
	}
}

func TestStatusWithoutAKeyShowsWhatTheLastKeylessCallLeft(t *testing.T) {
	var calls []string
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		w.Header().Set("X-String-Keyless", "true")
		w.Header().Set("X-Keyless-Requests-Remaining", "49")
		w.Header().Set("X-Keyless-Credits-Remaining", "95")
		io.WriteString(w, "# Example Domain\n")
	})
	env := map[string]string{"HOME": t.TempDir(), "STRING_API_BASE_URL": baseURL}
	morning := time.Date(2026, 11, 5, 9, 15, 0, 0, time.UTC)

	if got := runAt(t, []string{"status"}, env, morning); got.status != 0 || !strings.Contains(got.output, "Keyless   no keyless call from this machine today") {
		t.Errorf("before any call %+v", got)
	}
	if got := runAt(t, []string{"fetch", "https://example.com"}, env, morning); got.status != 0 {
		t.Fatalf("fetch %+v", got)
	}
	got := runAt(t, []string{"status"}, env, morning.Add(2*time.Hour))
	want := "string " + version + `
Key       none, so fetch and search run keyless
Keyless   49 requests and 95 credits left today, as of this machine's last keyless call at 09:15 UTC
`
	if got.status != 0 || got.output != want {
		t.Errorf("got %+v, want output\n%s", got, want)
	}
	if got := runAt(t, []string{"status"}, env, morning.Add(24*time.Hour)); !strings.Contains(got.output, "no keyless call from this machine today; the allowance resets at 00:00 UTC") {
		t.Errorf("next day %+v", got)
	}
	if len(calls) != 1 {
		t.Errorf("status reached the API without a key: %v", calls)
	}
}

func TestStatusSaysWhenTheKeyIsRefusedOrTheBalanceIsUnavailable(t *testing.T) {
	code := http.StatusUnauthorized
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		io.WriteString(w, `{"error":"Invalid API key"}`)
	})
	env := map[string]string{"STRING_API_KEY": "test-key", "STRING_API_BASE_URL": baseURL}
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

	refused := runAt(t, []string{"status"}, env, now)
	if refused.status != 3 || !strings.Contains(refused.output, "refused by the API (HTTP 401)") || strings.Contains(refused.output+refused.errors, "test-key") {
		t.Errorf("refused %+v", refused)
	}
	code = http.StatusNotFound
	missing := runAt(t, []string{"status"}, env, now)
	if missing.status != 0 || !strings.Contains(missing.output, "Balance   unavailable (HTTP 404); see https://portal.usestring.ai/usage") {
		t.Errorf("missing %+v", missing)
	}
}
