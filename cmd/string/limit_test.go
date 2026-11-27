package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// search --limit N prints only the first N records of the answer, cut after
// it comes, so the request is the same, with or without a key. The rest of
// the answer stays as it came, and --fields and -o take what is left.
func TestSearchLimitPrintsTheFirstRecords(t *testing.T) {
	var bodies []string
	answer := `{"results":[{"position":1,"title":"One","url":"https://one.example/"},{"position":2,"title":"Two"},{"position":3,"title":"Three"}],"zeroResults":false,"images":[{"title":"A"},{"title":"B"}],"places":[{"name":"P"}]}`
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		bodies = append(bodies, requestBody(t, r))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(answer))
	})
	saved := filepath.Join(t.TempDir(), "results.json")
	for _, c := range []struct {
		list []string
		key  string
		want string
	}{
		{[]string{"search", "postgres", "--limit", "2"}, "test-key", `{"results":[{"position":1,"title":"One","url":"https://one.example/"},{"position":2,"title":"Two"}],"zeroResults":false,"images":[{"title":"A"},{"title":"B"}],"places":[{"name":"P"}]}`},
		{[]string{"search", "postgres", "--limit=1"}, "", `{"results":[{"position":1,"title":"One","url":"https://one.example/"}],"zeroResults":false,"images":[{"title":"A"},{"title":"B"}],"places":[{"name":"P"}]}`},
		{[]string{"search", "postgres", "--limit", "3"}, "test-key", answer},
		{[]string{"search", "postgres", "--limit", "9"}, "test-key", answer},
		{[]string{"search", "postgres", "--limit", "2", "--fields", "title"}, "test-key", `{"results":[{"title":"One"},{"title":"Two"}],"zeroResults":false,"images":[{"title":"A"},{"title":"B"}],"places":[{"name":"P"}]}`},
		{[]string{"search", "cats", "--searchType", "images", "--limit", "1"}, "test-key", `{"results":[{"position":1,"title":"One","url":"https://one.example/"},{"position":2,"title":"Two"},{"position":3,"title":"Three"}],"zeroResults":false,"images":[{"title":"A"}],"places":[{"name":"P"}]}`},
		{[]string{"search", "postgres", "--limit", "1", "-o", saved, "-q"}, "test-key", ""},
	} {
		bodies = nil
		got := invoke(t, c.list, baseURL, c.key)
		if want := c.want + "\n"; got.status != 0 || c.want == "" && got.output != "" || c.want != "" && got.output != want {
			t.Errorf("string %s: exit %d\nstdout %s\nwant   %s\nstderr %s", strings.Join(c.list, " "), got.status, got.output, want, got.errors)
		}
		if len(bodies) != 1 || strings.Contains(bodies[0], "limit") {
			t.Errorf("string %s sent %q", strings.Join(c.list, " "), bodies)
		}
	}
	if data, err := os.ReadFile(saved); err != nil || string(data) != `{"results":[{"position":1,"title":"One","url":"https://one.example/"}],"zeroResults":false,"images":[{"title":"A"},{"title":"B"}],"places":[{"name":"P"}]}`+"\n" {
		t.Errorf("-o saved %q, %v", data, err)
	}
}

// --limit takes a positive integer, and refuses --format raw, whose answer is
// the results page, before the search is sent.
func TestSearchLimitRefusesWhatItCantCut(t *testing.T) {
	for list, message := range map[string]string{
		"search postgres --limit 0":              "--limit must be a positive integer",
		"search postgres --limit -1":             "Option '--limit' argument is ambiguous",
		"search postgres --limit two":            "--limit must be a positive integer",
		"search postgres --limit 1.5":            "--limit must be a positive integer",
		"search postgres --format raw --limit 3": "--limit cuts the results, and --format raw prints the results page as HTML",
		"search postgres --limit":                "Option '--limit <value>' argument missing",
	} {
		got := runOffline(t, strings.Fields(list)...)
		if got.status != exitUsage || got.output != "" || !strings.Contains(got.errors, message) {
			t.Errorf("string %s: exit %d, stderr %q, want %q", list, got.status, got.errors, message)
		}
	}
}
