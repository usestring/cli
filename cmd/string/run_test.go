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
		client: http.DefaultClient,
		stdin:  strings.NewReader(""),
		stdout: &stdout,
		stderr: &stderr,
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
		if got.status != 1 || !strings.Contains(got.errors, "STRING_API_BASE_URL must be an HTTP(S) URL") {
			t.Errorf("suffix %q: %+v", suffix, got)
		}
	}
}

func TestMissingKeyAndAPIErrorsFailWithoutPrintingTheKey(t *testing.T) {
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"bad key"}`)
	})

	missing := invoke(t, []string{"fetch", "https://example.com"}, baseURL, "")
	if missing.status != 1 || !strings.Contains(missing.errors, "Set STRING_API_KEY") {
		t.Errorf("missing %+v", missing)
	}

	rejected := invoke(t, []string{"fetch", "https://example.com"}, baseURL, "private-key")
	if rejected.status != 1 || !strings.Contains(rejected.errors, "HTTP 401") || strings.Contains(rejected.errors, "private-key") {
		t.Errorf("rejected %+v", rejected)
	}

	malformed := invoke(t, []string{"fetch", "https://example.com"}, baseURL, "private\nkey")
	if malformed.status != 1 || !strings.Contains(malformed.errors, "STRING_API_KEY contains characters") || strings.Contains(malformed.errors, "private") {
		t.Errorf("malformed %+v", malformed)
	}
}
