package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

// runFetching runs string against base with the variables in vars and a key.
func runFetching(base string, vars map[string]string, list ...string) result {
	vars["STRING_API_KEY"], vars["STRING_API_BASE_URL"] = "test-key", base
	var stdout, stderr bytes.Buffer
	status := run(list, runtime{env: envOf(vars), client: http.DefaultClient, stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr})
	return result{status, stdout.String(), stderr.String()}
}

// --errors json gives JSON errors as --json does, and leaves stdout alone:
// fetch's Markdown stays Markdown.
func TestErrorsJSONLeavesStdoutAlone(t *testing.T) {
	failing := false
	base := api(t, func(w http.ResponseWriter, r *http.Request) {
		if failing {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"bad url"}`)
			return
		}
		io.WriteString(w, "# Example Domain\n")
	})
	for _, list := range [][]string{{"fetch", "https://example.com", "--errors", "json"}, {"--errors=json", "https://example.com"}} {
		failing = false
		if got := runFetching(base, map[string]string{}, list...); got.status != 0 || got.output != "# Example Domain\n" || got.errors != "" {
			t.Errorf("%v: %+v", list, got)
		}
		failing = true
		got := runFetching(base, map[string]string{}, list...)
		if envelope := decode(t, got.errors).(map[string]any)["error"].(map[string]any); got.status != exitUsage || envelope["message"] != "bad url" || envelope["exit_code"] != float64(exitUsage) {
			t.Errorf("%v failing: %+v", list, got)
		}
	}
	if got := runFetching(base, map[string]string{}, "fetch", "https://example.com", "--errors", "text"); got.status != exitUsage || !strings.HasPrefix(got.errors, "error: bad url (HTTP 400)") {
		t.Errorf("--errors text: %+v", got)
	}
	if got := runOffline(t, "fetch", "https://example.com", "--errors", "xml"); got.status != exitUsage || got.errors != "error: --errors takes json or text, not \"xml\"\n" {
		t.Errorf("--errors xml: %+v", got)
	}
}

// fetch --json says once that its stdout changes in 0.4, and a fetch whose
// site answered an error says how its exit code changes. Either can be
// silenced.
func TestFetchSaysWhatChangesIn04(t *testing.T) {
	status := "200"
	base := api(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Status-Code", status)
		io.WriteString(w, "# Example Domain\n")
	})
	for _, c := range []struct {
		about  string
		status string
		vars   map[string]string
		list   []string
		said   string
	}{
		{"fetch --json", "200", map[string]string{}, []string{"fetch", "https://example.com", "--json"}, fetchJSONNotice},
		{"a URL with --json", "200", map[string]string{}, []string{"--json", "https://example.com"}, fetchJSONNotice},
		{"fetch --errors json", "200", map[string]string{}, []string{"fetch", "https://example.com", "--errors", "json"}, ""},
		{"search --json", "200", map[string]string{}, []string{"search", "coffee", "--json"}, ""},
		{"a missing page", "404", map[string]string{}, []string{"fetch", "https://example.com/gone"}, "warning: https://example.com/gone answered 404. From 0.4, string fetch exits 8 for that (target_not_found); --no-fail will keep exit 0.\n"},
		{"a page that's gone", "410", map[string]string{}, []string{"fetch", "https://example.com/gone"}, "warning: https://example.com/gone answered 410. From 0.4, string fetch exits 8 for that (target_not_found); --no-fail will keep exit 0.\n"},
		{"a site that fails", "503", map[string]string{}, []string{"fetch", "https://example.com"}, "warning: https://example.com/ answered 503. From 0.4, string fetch exits 6 for that (target_http_error), with the page still printed; --no-fail will keep exit 0.\n"},
		{"a search of a site that fails", "503", map[string]string{}, []string{"search", "coffee"}, ""},
		{"warnings silenced", "404", map[string]string{"STRING_NO_DEPRECATION_WARNINGS": "1"}, []string{"fetch", "https://example.com/gone", "--json"}, ""},
		{"0.2 kept", "404", map[string]string{"STRING_COMPAT": "0.2"}, []string{"fetch", "https://example.com/gone", "--json"}, ""},
	} {
		status = c.status
		if got := runFetching(base, c.vars, c.list...); got.status != 0 || got.errors != c.said {
			t.Errorf("%s: exit %d, stderr %q, want %q", c.about, got.status, got.errors, c.said)
		}
	}
}
