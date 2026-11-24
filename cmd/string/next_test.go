package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// crawlAnswers is an API that quotes a submitted crawl as job-6, answers each
// job's status, gives job-1's five urls and the 25 jobs a page at a time, as
// many as --limit asks or the API's own page holds, and notes each request
// in sent. job-7 counts urls it sends none of, and job-8's quote has no cost
// yet.
func crawlAnswers(t *testing.T, sent *[]string) string {
	t.Helper()
	statuses := map[string]string{"job-1": "running", "job-2": "completed", "job-3": "token_cap_exceeded", "job-4": "canceled", "job-5": "failed", "job-6": "awaiting_approval"}
	return api(t, func(w http.ResponseWriter, r *http.Request) {
		*sent = append(*sent, r.Method+" "+r.URL.RequestURI())
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		var records []string
		switch path := strings.TrimPrefix(r.URL.Path, "/v1/sitemap"); {
		case r.Method == "POST" && path == "":
			io.WriteString(w, `{"jobId":"job-6","status":"awaiting_approval","estimatedCostUsd":"1.05","estimatedPages":1050}`)
		case path == "/job-7/urls":
			io.WriteString(w, `{"jobId":"job-7","total":9,"urls":[]}`)
		case path == "/job-8":
			io.WriteString(w, `{"jobId":"job-8","status":"awaiting_approval","estimatedCostUsd":null}`)
		case path == "/job-1/urls":
			for i := offset; i < min(offset+cmp.Or(limit, 1000), 5); i++ {
				records = append(records, fmt.Sprintf(`{"url":"https://example.com/%d"}`, i))
			}
			fmt.Fprintf(w, `{"jobId":"job-1","total":5,"urls":[%s]}`, strings.Join(records, ","))
		case path == "":
			for i := offset; i < min(offset+cmp.Or(limit, 20), 25); i++ {
				records = append(records, fmt.Sprintf(`{"jobId":"job-%d"}`, i))
			}
			fmt.Fprintf(w, `{"jobs":[%s]}`, strings.Join(records, ","))
		case statuses[strings.TrimPrefix(path, "/")] != "":
			job := strings.TrimPrefix(path, "/")
			fmt.Fprintf(w, `{"jobId":%q,"status":%q}`, job, statuses[job])
		default:
			http.NotFound(w, r)
		}
	})
}

// A crawl's JSON ends with next_commands, the steps that carry it on, and
// stderr names the first: a quote's approve, a spend that needs the user's
// go; a running crawl's wait; a finished one's results; and while there are
// more, the next page. Each step repeats the global switches given, but only
// the approve, the wait and the next page, which print the same JSON, keep
// --fields: a job's results are other JSON. -q leaves out the line on stderr.
func TestACrawlsJSONEndsWithTheStepsThatCarryItOn(t *testing.T) {
	var sent []string
	baseURL := crawlAnswers(t, &sent)
	for _, c := range []struct {
		list           []string
		stdout, stderr string
	}{
		{[]string{"sitemap", "submit", "https://example.com", "--json"},
			`{"jobId":"job-6","status":"awaiting_approval","estimatedCostUsd":"1.05","estimatedPages":1050,"next_commands":[{"type":"spend","command":"string sitemap approve job-6 --wait --json","argv":["string","sitemap","approve","job-6","--wait","--json"],"why":"starts the crawl at its quoted cost, up to $1.05, which needs the user's go"}]}`,
			"next, with the user's go: string sitemap approve job-6 --wait --json\n"},
		{[]string{"sitemap", "status", "job-1"},
			`{"jobId":"job-1","status":"running","next_commands":[{"type":"wait","command":"string sitemap status job-1 --wait","argv":["string","sitemap","status","job-1","--wait"],"why":"follows the crawl until it ends"}]}`,
			"next: string sitemap status job-1 --wait\n"},
		{[]string{"-q", "sitemap", "status", "job-1"},
			`{"jobId":"job-1","status":"running","next_commands":[{"type":"wait","command":"string sitemap status job-1 --wait -q","argv":["string","sitemap","status","job-1","--wait","-q"],"why":"follows the crawl until it ends"}]}`,
			""},
		{[]string{"sitemap", "status", "job-1", "--fields", "status,next_commands.command"},
			`{"status":"running","next_commands":[{"command":"string sitemap status job-1 --wait --fields status,next_commands.command"}]}`,
			"next: string sitemap status job-1 --wait --fields status,next_commands.command\n"},
		{[]string{"sitemap", "status", "job-2", "--fields", "status,next_commands.command"},
			`{"status":"completed","next_commands":[{"command":"string sitemap results job-2 --limit 100"}]}`,
			"next: string sitemap results job-2 --limit 100\n"},
		{[]string{"sitemap", "status", "job-3", "--fields=next_commands"},
			`{"next_commands":[{"type":"read","command":"string sitemap results job-3 --limit 100","argv":["string","sitemap","results","job-3","--limit","100"],"why":"lists the first 100 pages it crawled"}]}`,
			"next: string sitemap results job-3 --limit 100\n"},
		{[]string{"sitemap", "status", "job-4", "--fields", "next_commands.command"},
			`{"next_commands":[{"command":"string sitemap results job-4 --limit 100"}]}`,
			"next: string sitemap results job-4 --limit 100\n"},
		{[]string{"sitemap", "status", "job-5"}, `{"jobId":"job-5","status":"failed"}`, ""},
		{[]string{"sitemap", "status", "job-8", "--fields", "next_commands.why"},
			`{"next_commands":[{"why":"starts the crawl at its quoted cost, which needs the user's go"}]}`,
			"next, with the user's go: string sitemap approve job-8 --wait --fields next_commands.why\n"},
		// A page with none of the records its total counts has no next page.
		{[]string{"sitemap", "results", "job-7", "--offset", "3"}, `{"jobId":"job-7","total":9,"urls":[]}`, ""},
		{[]string{"sitemap", "results", "job-1", "--limit", "2"},
			`{"jobId":"job-1","total":5,"urls":[{"url":"https://example.com/0"},{"url":"https://example.com/1"}],"next_commands":[{"type":"read","command":"string sitemap results job-1 --limit 2 --offset 2","argv":["string","sitemap","results","job-1","--limit","2","--offset","2"],"why":"prints the next page"}]}`,
			"next: string sitemap results job-1 --limit 2 --offset 2\n"},
		{[]string{"sitemap", "results", "job-1", "--limit", "2", "--offset", "2", "--fields", "url"},
			`{"jobId":"job-1","total":5,"urls":[{"url":"https://example.com/2"},{"url":"https://example.com/3"}],"next_commands":[{"type":"read","command":"string sitemap results job-1 --limit 2 --fields url --offset 4","argv":["string","sitemap","results","job-1","--limit","2","--fields","url","--offset","4"],"why":"prints the next page"}]}`,
			"next: string sitemap results job-1 --limit 2 --fields url --offset 4\n"},
		{[]string{"sitemap", "results", "job-1", "--limit=2", "--offset=4"}, `{"jobId":"job-1","total":5,"urls":[{"url":"https://example.com/4"}]}`, ""},
		{[]string{"sitemap", "results", "job-1"}, `{"jobId":"job-1","total":5,"urls":[{"url":"https://example.com/0"},{"url":"https://example.com/1"},{"url":"https://example.com/2"},{"url":"https://example.com/3"},{"url":"https://example.com/4"}]}`, ""},
		{[]string{"sitemap", "list", "--limit", "2", "--offset", "23"},
			`{"jobs":[{"jobId":"job-23"},{"jobId":"job-24"}],"next_commands":[{"type":"read","command":"string sitemap list --limit 2 --offset 25","argv":["string","sitemap","list","--limit","2","--offset","25"],"why":"prints the next page"}]}`,
			"next: string sitemap list --limit 2 --offset 25\n"},
		{[]string{"sitemap", "list", "--limit", "5", "--offset", "23"}, `{"jobs":[{"jobId":"job-23"},{"jobId":"job-24"}]}`, ""},
	} {
		got := invoke(t, c.list, baseURL, "test-key")
		if got.status != 0 || got.output != c.stdout+"\n" || got.errors != c.stderr {
			t.Errorf("string %s: exit %d\nstdout %s\nwant   %s\nstderr %q\nwant   %q", strings.Join(c.list, " "), got.status, got.output, c.stdout, got.errors, c.stderr)
		}
	}

	// A page of jobs as full as the API's own has the next page after it.
	// -o - is stdout, so the next page goes there too.
	for _, c := range []struct{ list, next string }{
		{"sitemap list", "string sitemap list --offset 20"},
		{"sitemap list --offset 20", ""},
		{"sitemap results job-1 --limit 2 -o -", "string sitemap results job-1 --limit 2 -o - --offset 2"},
	} {
		got := invoke(t, strings.Fields(c.list), baseURL, "test-key")
		var answer struct {
			Next []nextCommand `json:"next_commands"`
		}
		switch err := json.Unmarshal([]byte(got.output), &answer); {
		case err != nil:
			t.Errorf("string %s printed %q: %v", c.list, got.output, err)
		case c.next == "" && (answer.Next != nil || got.errors != ""):
			t.Errorf("string %s: next_commands %+v, stderr %q, want none", c.list, answer.Next, got.errors)
		case c.next != "" && (len(answer.Next) != 1 || answer.Next[0].Type != stepRead || answer.Next[0].Command != c.next || got.errors != "next: "+c.next+"\n"):
			t.Errorf("string %s: next_commands %+v, stderr %q, want %s", c.list, answer.Next, got.errors, c.next)
		}
	}
}

// A page saved with -o FILE was kept out of stdout, so the rest is too: its
// step is --all -o FILE from the page's --offset, which writes FILE again
// with every record from that page on, one JSON object a line.
func TestAPageSavedToAFileLeadsToEveryRecordInIt(t *testing.T) {
	var sent []string
	baseURL := crawlAnswers(t, &sent)
	dir := t.TempDir()
	for _, c := range []struct {
		list  []string
		saved string
		step  string
		all   string
	}{
		{[]string{"sitemap", "results", "job-1", "--limit", "2", "--offset", "1", "-o"}, "urls.json", "sitemap results job-1 --offset 1 -o %s --all", lines(1, 5, `{"url":"https://example.com/%d"}`)},
		{[]string{"sitemap", "list", "--limit=5", "--output"}, "jobs.json", "sitemap list --output %s --all", lines(0, 25, `{"jobId":"job-%d"}`)},
	} {
		saved := filepath.Join(dir, c.saved)
		got := invoke(t, append(c.list, saved), baseURL, "test-key")
		step := "string " + fmt.Sprintf(c.step, saved)
		if got.status != 0 || got.output != "" || !strings.HasSuffix(got.errors, "\nnext: "+step+"\n") {
			t.Errorf("string %s %s: exit %d, stdout %q, stderr %s", strings.Join(c.list, " "), saved, got.status, got.output, got.errors)
			continue
		}
		data, _ := os.ReadFile(saved)
		var page struct {
			Next []nextCommand `json:"next_commands"`
		}
		if err := json.Unmarshal(data, &page); err != nil || len(page.Next) != 1 {
			t.Errorf("%s holds %s", c.saved, data)
			continue
		}
		why := "writes " + saved + " again with every record from this page on, so it then holds NDJSON: one JSON object a line"
		if next := page.Next[0]; next.Type != stepRead || next.Command != step || next.Why != why || !reflect.DeepEqual(next.Argv, strings.Fields(step)) {
			t.Errorf("%s: the step is %+v, want %s, for %s", c.saved, next, step, why)
		}
		if got := invoke(t, page.Next[0].Argv[1:], baseURL, "test-key"); got.status != 0 || got.output != "" {
			t.Errorf("%s: exit %d, stdout %q, stderr %s", step, got.status, got.output, got.errors)
		}
		if data, err := os.ReadFile(saved); err != nil || string(data) != c.all {
			t.Errorf("after %s, %s holds %q, %v\nwant %q", step, c.saved, data, err, c.all)
		}
	}
}

// A wait that ends in a failure prints the last status as it came: the
// steps out of it are its error's.
func TestAWaitThatFailsLeavesItsStepsToItsError(t *testing.T) {
	var sent []string
	baseURL := crawlAnswers(t, &sent)
	got := invoke(t, []string{"sitemap", "status", "job-4", "--wait", "--json"}, baseURL, "test-key")
	said := strings.Split(strings.TrimSpace(got.errors), "\n")
	if got.status != exitFailure || got.output != `{"jobId":"job-4","status":"canceled"}`+"\n" || strings.Contains(got.errors, "next: ") || !strings.Contains(said[len(said)-1], `"code":"crawl_canceled"`) {
		t.Errorf("exit %d\nstdout %s\nstderr %s", got.status, got.output, got.errors)
	}
}

// withNext ends a JSON object with the steps, as the encoder writes them,
// and leaves anything else as it came.
func TestWithNextEndsAJSONObject(t *testing.T) {
	steps := []nextCommand{{Type: stepRead, Command: "string x --a '<b>&c'", Argv: []string{"string", "x", "--a", "<b>&c"}}}
	encoded := `[{"type":"read","command":"string x --a '<b>&c'","argv":["string","x","--a","<b>&c"]}]`
	for data, want := range map[string]string{
		`{"a":1}` + "\n":         `{"a":1,"next_commands":` + encoded + "}\n",
		"{\n  \"a\": [1]\n}\n\n": "{\n  \"a\": [1],\"next_commands\":" + encoded + "}\n\n",
		`{}`:                     `{"next_commands":` + encoded + `}`,
		`{"next_commands":[]}`:   `{"next_commands":[]}`,
		`[{"a":1}]`:              `[{"a":1}]`,
		`{"a":1`:                 `{"a":1`,
		`{"a":1}{"b":2}`:         `{"a":1}{"b":2}`,
	} {
		if got := string(withNext([]byte(data), steps)); got != want {
			t.Errorf("withNext(%q) = %q, want %q", data, got, want)
		}
	}
	if got := string(withNext([]byte(`{"a":1}`), nil)); got != `{"a":1}` {
		t.Errorf("with no steps, withNext = %q", got)
	}
}

// A step to every record from a saved page on leaves out --limit, in any of
// its spellings, and its value.
func TestWithoutLeavesOutTheOptionNamed(t *testing.T) {
	line := []string{"sitemap", "list", "--limit", "5", "--offset", "2", "--Limit=7", "-o", "jobs.json"}
	if got, want := without(line, "--limit"), []string{"sitemap", "list", "--offset", "2", "-o", "jobs.json"}; !reflect.DeepEqual(got, want) {
		t.Errorf("without(%q, --limit) = %q, want %q", line, got, want)
	}
}
