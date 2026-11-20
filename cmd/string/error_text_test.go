package main

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

// An error's text starts with error:, keeps its details under it, and puts
// the step out after a blank line. It never carries an HTML page, a JSON body
// or Go's own words for a network failure.
func TestErrorTextIsOneSentenceAndItsSteps(t *testing.T) {
	for _, c := range []struct {
		about       string
		status      int
		header      string
		body        string
		want        string
		mustNotHave string
	}{
		{"an HTML page", 502, "X-Request-Id: req-7", "<html><body>Bad Gateway</body></html>", "error: HTTP 502 from HOST\n  x-request-id: req-7\n", "<html>"},
		{"the API's message", 400, "", `{"error":"bad query","code":"invalid_request"}`, "error: bad query (HTTP 400)\n", `{"error"`},
		{"a short plain answer", 503, "", "upstream timeout", "error: HTTP 503 from HOST: upstream timeout\n", ""},
		{"a long plain answer", 503, "", strings.Repeat("word ", 60), "error: HTTP 503 from HOST\n", "word"},
		{"a rate limit", 429, "Retry-After: 30", `{"error":"Too many requests"}`, "error: Too many requests (HTTP 429)\n  Retry-After: 30\n", ""},
	} {
		base := api(t, func(w http.ResponseWriter, r *http.Request) {
			if name, value, ok := strings.Cut(c.header, ": "); ok {
				w.Header().Set(name, value)
			}
			w.WriteHeader(c.status)
			io.WriteString(w, c.body)
		})
		host := strings.TrimSuffix(strings.TrimPrefix(base, "http://"), "/v1")
		got := invoke(t, []string{"search", "coffee"}, base, "test-key")
		if want := strings.ReplaceAll(c.want, "HOST", host); got.errors != want || (c.mustNotHave != "" && strings.Contains(got.errors, c.mustNotHave)) {
			t.Errorf("%s: stderr %q, want %q", c.about, got.errors, want)
		}
	}

	// A request that got no answer says what happened in a sentence.
	for _, c := range []struct {
		err  error
		want string
	}{
		{&net.DNSError{Err: "no such host", Name: "api.example.test", IsNotFound: true}, "error: string couldn't find api.example.test: the name doesn't resolve.\n"},
		{&net.OpError{Op: "dial", Net: "tcp", Err: &net.AddrError{Err: "connection refused", Addr: "api.example.test:443"}}, "error: string couldn't connect to api.example.test.\n"},
		{io.ErrUnexpectedEOF, "error: string lost the connection to api.example.test before an answer came.\n"},
	} {
		got, _ := runWith(t, &http.Client{Transport: failingTransport{c.err}}, map[string]string{"STRING_API_KEY": "test-key", "STRING_API_BASE_URL": "https://api.example.test/v1"}, "search", "coffee")
		if got.errors != c.want || strings.Contains(got.errors, "dial tcp") {
			t.Errorf("%v: stderr %q, want %q", c.err, got.errors, c.want)
		}
	}

	// A message of more than one line keeps the rest under its first line.
	if got := runOffline(t, "fetch", "https://example.com", "--headers", "-x"); !strings.HasPrefix(got.errors, "error: Option '--headers' argument is ambiguous.\n  Did you forget") {
		t.Errorf("an ambiguous option: %q", got.errors)
	}
}
