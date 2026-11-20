package main

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// A bare domain is fetched over https. A URL with no scheme gets https://,
// and a note on stderr says so; a scheme in capitals works as it is.
func TestABareDomainIsFetchedOverHTTPS(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	base := api(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, r.URL.Path+" "+string(data))
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/sitemap":
			io.WriteString(w, `{"jobId":"job-1","status":"awaiting_approval"}`)
		case "/v1/fetch":
			io.WriteString(w, `{"statusCode":200}`)
		}
	})
	for _, c := range []struct {
		list            []string
		path, url, note string
	}{
		{[]string{"example.com"}, "/v1/fetch", "https://example.com/", "example.com names no scheme, so string uses https://example.com/\n"},
		{[]string{"example.com/docs?page=2", "--mainContentOnly"}, "/v1/fetch", "https://example.com/docs?page=2", "example.com/docs?page=2 names no scheme, so string uses https://example.com/docs?page=2\n"},
		{[]string{"fetch", "localhost:8080/health"}, "/v1/fetch", "https://localhost:8080/health", "localhost:8080/health names no scheme, so string uses https://localhost:8080/health\n"},
		{[]string{"HTTPS://Example.com/Path"}, "/v1/fetch", "https://example.com/Path", ""},
		{[]string{"request", "POST", "api.example.com/orders", "--body", "{}"}, "/v1/fetch", "https://api.example.com/orders", "api.example.com/orders names no scheme, so string uses https://api.example.com/orders\n"},
		{[]string{"sitemap", "submit", "docs.example.com"}, "/v1/sitemap", "https://docs.example.com/", "docs.example.com names no scheme, so string uses https://docs.example.com/\n"},
	} {
		mu.Lock()
		bodies = nil
		mu.Unlock()
		got := invoke(t, c.list, base, "test-key")
		mu.Lock()
		called := bodies
		mu.Unlock()
		said := strings.Join(c.list, " ")
		if got.status != 0 || got.errors != c.note || len(called) != 1 {
			t.Errorf("string %s: %+v after %v, want exit 0 and stderr %q", said, got, called, c.note)
			continue
		}
		path, body, _ := strings.Cut(called[0], " ")
		if sent, _ := decode(t, body).(map[string]any); path != c.path || sent["url"] != c.url {
			t.Errorf("string %s: sent %s to %s, want url %s to %s", said, body, path, c.url, c.path)
		}
	}
	// A word with no dot is still a command, and another scheme is still refused.
	if got := runOffline(t, "examplecom"); got.status != exitUsage || !strings.HasPrefix(got.errors, "error: Unknown command: examplecom") {
		t.Errorf("string examplecom: %+v", got)
	}
	if got := runOffline(t, "fetch", "ftp://example.com"); got.status != exitUsage || got.errors != "error: URL must use http or https\n" {
		t.Errorf("string fetch ftp://example.com: %+v", got)
	}
}
