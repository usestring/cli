package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// pagedAPI answers a crawl's urls two at a time, whatever the page size
// asked, out of total, and the jobs a page at a time out of jobs. Each page
// asked for is noted in asked, and failAt fails the page at that offset.
func pagedAPI(t *testing.T, total, jobs int, failAt int, asked *[]string) string {
	t.Helper()
	return api(t, func(w http.ResponseWriter, r *http.Request) {
		*asked = append(*asked, r.URL.RequestURI())
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if offset == failAt {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"error":"Try again"}`)
			return
		}
		var records []string
		switch r.URL.Path {
		case "/v1/sitemap/job-1/urls":
			for i := offset; i < min(offset+2, total); i++ {
				records = append(records, fmt.Sprintf(`{"url":"https://example.com/%d","statusCode":200,"depth":1}`, i))
			}
			fmt.Fprintf(w, `{"jobId":"job-1","total":%d,"urls":[%s]}`, total, strings.Join(records, ","))
		case "/v1/sitemap":
			for i := offset; i < min(offset+limit, jobs); i++ {
				records = append(records, fmt.Sprintf(`{"jobId":"job-%d","status":"completed"}`, i))
			}
			fmt.Fprintf(w, `{"jobs":[%s]}`, strings.Join(records, ","))
		default:
			http.NotFound(w, r)
		}
	})
}

// lines are the lines of the records numbered from to to, as format writes
// each.
func lines(from, to int, format string) string {
	var b strings.Builder
	for i := from; i < to; i++ {
		fmt.Fprintf(&b, format+"\n", i)
	}
	return b.String()
}

// --all prints every record of every page, one JSON object a line, from
// --offset on: a crawl's urls until its total, and the jobs until a page
// holds fewer than a page can. --fields chooses from each record, and -o
// FILE saves them all.
func TestAllPrintsEveryRecordOfEveryPage(t *testing.T) {
	url := `{"url":"https://example.com/%d","statusCode":200,"depth":1}`
	var asked []string
	baseURL := pagedAPI(t, 5, 130, -1, &asked)
	saved := filepath.Join(t.TempDir(), "urls.ndjson")
	for _, c := range []struct {
		list  []string
		want  string
		pages []string
	}{
		{[]string{"sitemap", "results", "job-1", "--all"}, lines(0, 5, url), []string{"/v1/sitemap/job-1/urls?limit=5000&offset=0", "/v1/sitemap/job-1/urls?limit=5000&offset=2", "/v1/sitemap/job-1/urls?limit=5000&offset=4"}},
		{[]string{"sitemap", "results", "job-1", "--offset", "3", "--all"}, lines(3, 5, url), []string{"/v1/sitemap/job-1/urls?limit=5000&offset=3"}},
		{[]string{"sitemap", "results", "job-1", "--all", "--fields", "url"}, lines(0, 5, `{"url":"https://example.com/%d"}`), nil},
		{[]string{"sitemap", "list", "--all", "--fields=jobId"}, lines(0, 130, `{"jobId":"job-%d"}`), []string{"/v1/sitemap?limit=100&offset=0", "/v1/sitemap?limit=100&offset=100"}},
		{[]string{"sitemap", "results", "job-1", "--all", "-o", saved}, "", nil},
	} {
		asked = nil
		got := invoke(t, c.list, baseURL, "test-key")
		if got.status != 0 || got.output != c.want {
			t.Errorf("string %s: exit %d\nstdout %q\nwant   %q\nstderr %s", strings.Join(c.list, " "), got.status, got.output, c.want, got.errors)
		}
		if c.pages != nil && strings.Join(asked, " ") != strings.Join(c.pages, " ") {
			t.Errorf("string %s asked for %v, want %v", strings.Join(c.list, " "), asked, c.pages)
		}
	}
	if data, err := os.ReadFile(saved); err != nil || string(data) != lines(0, 5, url) {
		t.Errorf("-o saved %q, %v", data, err)
	}
	// stderr says what -o saved, but under --json it holds only errors.
	for _, said := range []string{"saved " + saved + " · ", ""} {
		list := []string{"sitemap", "results", "job-1", "--all", "-o", saved}
		if said == "" {
			list = append(list, "--json")
		}
		// Without the file from before, only this run can have written it.
		if err := os.Remove(saved); err != nil {
			t.Fatal(err)
		}
		got := invoke(t, list, baseURL, "test-key")
		data, err := os.ReadFile(saved)
		if got.status != 0 || got.output != "" || !strings.HasPrefix(got.errors, said) || said == "" && got.errors != "" || err != nil || string(data) != lines(0, 5, url) {
			t.Errorf("string %s: %+v, saved %q, %v", strings.Join(list, " "), got, data, err)
		}
	}

	empty := pagedAPI(t, 0, 0, -1, &asked)
	for _, list := range [][]string{{"sitemap", "results", "job-1", "--all", "--fields", "nothing"}, {"sitemap", "list", "--all"}} {
		if got := invoke(t, list, empty, "test-key"); got.status != 0 || got.output != "" || got.errors != "" {
			t.Errorf("string %s with no records: %+v", strings.Join(list, " "), got)
		}
	}
}

// A page after the first that fails leaves the records before it printed,
// and the error gives the command that reads on from there. A first page
// that fails, or -o FILE, which saves nothing until every page is read, have
// nothing to go on from.
func TestAllNamesWhereToGoOnFrom(t *testing.T) {
	url := `{"url":"https://example.com/%d","statusCode":200,"depth":1}`
	var asked []string
	baseURL := pagedAPI(t, 5, 0, 2, &asked)
	got := invoke(t, []string{"sitemap", "results", "job-1", "--all", "--fields", "url"}, baseURL, "test-key")
	resume := "string sitemap results job-1 --all --fields url --offset 2"
	if got.status != exitUpstream || got.output != lines(0, 2, `{"url":"https://example.com/%d"}`) || !strings.Contains(got.errors, "The records before offset 2 are printed, and `"+resume+"` prints the rest.") || !strings.Contains(got.errors, "next: run `"+resume+"`") {
		t.Errorf("a failed second page: exit %d\nstdout %q\nstderr %s", got.status, got.output, got.errors)
	}
	got = invoke(t, []string{"sitemap", "results", "job-1", "--all", "--json"}, baseURL, "test-key")
	envelope, _ := decode(t, got.errors).(map[string]any)["error"].(map[string]any)
	steps, _ := envelope["next_commands"].([]any)
	if want := "string sitemap results job-1 --all --json --offset 2"; got.output != lines(0, 2, url) || len(steps) != 1 || steps[0].(map[string]any)["command"] != want {
		t.Errorf("--json: stdout %q, next_commands %v, want %s", got.output, steps, want)
	}

	saved := filepath.Join(t.TempDir(), "urls.ndjson")
	got = invoke(t, []string{"sitemap", "results", "job-1", "--all", "-o", saved}, baseURL, "test-key")
	if _, err := os.Stat(saved); got.status != exitUpstream || strings.Contains(got.errors, "--offset") || !os.IsNotExist(err) {
		t.Errorf("-o: exit %d, stderr %s, file %v", got.status, got.errors, err)
	}
	first := pagedAPI(t, 5, 0, 0, &asked)
	if got := invoke(t, []string{"sitemap", "results", "job-1", "--all"}, first, "test-key"); got.status != exitUpstream || got.output != "" || strings.Contains(got.errors, "--offset") {
		t.Errorf("a failed first page: %+v", got)
	}
}

// When no record of the first page has any of the names --fields gives,
// --all prints nothing and reads no further page.
func TestAllStopsWhenTheFieldsNameNothing(t *testing.T) {
	var asked []string
	baseURL := pagedAPI(t, 5, 0, -1, &asked)
	got := invoke(t, []string{"sitemap", "results", "job-1", "--all", "--fields", "ulr"}, baseURL, "test-key")
	if got.status != exitUsage || got.output != "" || len(asked) != 1 || !strings.Contains(got.errors, "No record here has ulr, which --fields names.") || !strings.Contains(got.errors, "next: run `string sitemap results job-1 --all --fields url`") {
		t.Errorf("exit %d, stdout %q, asked %v, stderr %s", got.status, got.output, asked, got.errors)
	}
}

// --all reads the pages of results and list, so it refuses --limit, which
// asks for one page, and the commands that have no pages, before anything is
// sent.
func TestAllRefusesWhatHasNoPages(t *testing.T) {
	for list, message := range map[string]string{
		"sitemap results job-1 --all --limit 5":    "--all reads every page, and --limit one page of N",
		"sitemap status job-1 --all":               "--all applies to sitemap results or list",
		"sitemap submit https://example.com --all": "--all applies to sitemap results or list",
		"sitemap results job-1 --all --format raw": "Unknown option '--format'",
		"sitemap list --all=true":                  "Option '--all' does not take an argument",
	} {
		got := runOffline(t, strings.Fields(list)...)
		if got.status != exitUsage || got.output != "" || !strings.Contains(got.errors, message) {
			t.Errorf("string %s: exit %d, stderr %q, want %q", list, got.status, got.errors, message)
		}
	}
}
