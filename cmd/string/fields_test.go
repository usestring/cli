package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// answering is an API that answers each path with its body in bodies, and
// notes each request it gets in sent.
func answering(t *testing.T, bodies map[string]string, sent *[]string) string {
	t.Helper()
	return api(t, func(w http.ResponseWriter, r *http.Request) {
		*sent = append(*sent, r.Method+" "+r.URL.RequestURI())
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	})
}

var fieldsAnswers = map[string]string{
	"/v1/search": `{"results":[{"position":1,"title":"Vacuum & <autovacuum>","url":"https://a.example/","snippet":"One","source":{"domain":"a.example","name":"A"}},{"position":2,"title":"Tuning","snippet":"Two"}],"zeroResults":false,"entity":{"title":"Postgres"}}`,
	"/v1/sitemap/job-1/urls": `{
  "jobId": "job-1",
  "total": 2,
  "urls": [
    {"url": "https://example.com/", "statusCode": 200, "depth": 0},
    {"url": "https://example.com/a", "statusCode": 404, "depth": 1, "error": "not found"}
  ]
}`,
	"/v1/sitemap":                  `{"jobs":[{"jobId":"job-1","domain":"example.com","status":"completed","pagesProcessed":2},{"jobId":"job-2","domain":"example.org","status":"running","pagesProcessed":40}]}`,
	"/v1/sitemap/job-1":            `{"jobId":"job-1","status":"running","pagesProcessed":40,"pending":7}`,
	"/v1/integrations":             `{"sites":[{"name":"reddit","title":"Reddit","description":"Posts","version":"1.0.0","actions":[{"name":"post"}]}]}`,
	"/v1/integrations/reddit":      `{"sites":[{"name":"reddit","title":"Reddit","version":"1.0.0","actions":[{"name":"post","input":{"properties":{"post":{"type":"string"}},"required":["post"]}}]}]}`,
	"/v1/integrations/reddit/post": `{"envelopeVersion":1,"data":[{"id":"t3_1","title":"Hello","author":{"name":"A","karma":5},"comments":[{"id":"c1","body":"Hi","score":3},{"id":"c2","score":1}]},{"id":"t3_2","title":"Bye","author":{"name":"B"},"comments":[]}],"failures":[],"billing":{"rows":2}}`,
	"/v1/fetch":                    `{"statusCode":200,"headers":{"content-type":"text/html"},"data":"<html>…</html>","finalUrl":"https://example.com/"}`,
	"/v1/account/balance":          `{"remaining":12.5,"held":0,"exhausted":false,"asOf":"2026-11-24T00:00:00Z"}`,
}

// --fields keeps only the fields it names: of each record where the JSON is
// a list of them, with the rest of the answer as it came, and otherwise of
// the object, each in the order --fields names them, and a.b reaches inside
// a, in each of a's records when a is a list.
func TestFieldsKeepOnlyTheFieldsNamedOfEachRecord(t *testing.T) {
	var sent []string
	baseURL := answering(t, fieldsAnswers, &sent)
	for _, c := range []struct {
		list []string
		want string
	}{
		{[]string{"search", "postgres", "--fields", "url,title"}, `{"results":[{"url":"https://a.example/","title":"Vacuum & <autovacuum>"},{"title":"Tuning"}],"zeroResults":false,"entity":{"title":"Postgres"}}`},
		{[]string{"--fields=title,source.domain", "search", "postgres"}, `{"results":[{"title":"Vacuum & <autovacuum>","source":{"domain":"a.example"}},{"title":"Tuning"}],"zeroResults":false,"entity":{"title":"Postgres"}}`},
		{[]string{"search", "postgres", "--fields", "source.domain,source"}, `{"results":[{"source":{"domain":"a.example","name":"A"}},{}],"zeroResults":false,"entity":{"title":"Postgres"}}`},
		{[]string{"sitemap", "results", "job-1", "--Fields", "url,error"}, `{"jobId":"job-1","total":2,"urls":[{"url":"https://example.com/"},{"url":"https://example.com/a","error":"not found"}]}`},
		{[]string{"sitemap", "list", "--fields", "status,jobId"}, `{"jobs":[{"status":"completed","jobId":"job-1"},{"status":"running","jobId":"job-2"}]}`},
		{[]string{"sitemap", "status", "job-1", "--fields", "pagesProcessed,status"}, `{"pagesProcessed":40,"status":"running"}`},
		{[]string{"integrations", "list", "--json", "--fields", "name,version"}, `{"sites":[{"name":"reddit","version":"1.0.0"}]}`},
		{[]string{"integrations", "run", "reddit", "post", "--post", "t3_1", "--fields", "title,comments.score"}, `{"envelopeVersion":1,"data":[{"title":"Hello","comments":[{"score":3},{"score":1}]},{"title":"Bye"}],"failures":[],"billing":{"rows":2}}`},
		{[]string{"integrations", "run", "reddit", "post", "--post", "t3_1", "--fields", "title,author.karma"}, `{"envelopeVersion":1,"data":[{"title":"Hello","author":{"karma":5}},{"title":"Bye"}],"failures":[],"billing":{"rows":2}}`},
		{[]string{"fetch", "https://example.com", "--format", "json", "--fields", "statusCode,finalUrl"}, `{"statusCode":200,"finalUrl":"https://example.com/"}`},
		{[]string{"request", "POST", "https://example.com", "--body", "{}", "--fields", "statusCode"}, `{"statusCode":200}`},
		{[]string{"balance", "--fields", "remaining"}, `{"remaining":12.5}`},
	} {
		// A running crawl's status says on stderr how to follow it, with the
		// same --fields, since the wait prints the same JSON.
		got := invoke(t, c.list, baseURL, "test-key")
		if got.status != 0 || got.output != c.want+"\n" || got.errors != "" && got.errors != "next: string sitemap status job-1 --wait --fields pagesProcessed,status\n" {
			t.Errorf("string %s: exit %d\nstdout %s\nwant   %s\nstderr %s", strings.Join(c.list, " "), got.status, got.output, c.want, got.errors)
		}
	}
}

// A search whose searchType answers in images, places or products has its
// records there, and an answer with no records has nothing to choose from.
func TestFieldsFindTheRecordsOfEachSearchType(t *testing.T) {
	answer := `{"results":[],"zeroResults":false,"images":[{"title":"Cat","imageUrl":"https://img.example/cat.png","width":10}],"places":[{"name":"Cafe","rating":4.5}],"products":[{"title":"Mug","price":"$9"}]}`
	baseURL := answering(t, map[string]string{"/v1/search": answer}, new([]string))
	for searchType, want := range map[string]string{
		"images":   `{"results":[],"zeroResults":false,"images":[{"imageUrl":"https://img.example/cat.png"}],"places":[{"name":"Cafe","rating":4.5}],"products":[{"title":"Mug","price":"$9"}]}`,
		"places":   `{"results":[],"zeroResults":false,"images":[{"title":"Cat","imageUrl":"https://img.example/cat.png","width":10}],"places":[{"rating":4.5}],"products":[{"title":"Mug","price":"$9"}]}`,
		"shopping": `{"results":[],"zeroResults":false,"images":[{"title":"Cat","imageUrl":"https://img.example/cat.png","width":10}],"places":[{"name":"Cafe","rating":4.5}],"products":[{"price":"$9"}]}`,
	} {
		fields := map[string]string{"images": "imageUrl", "places": "rating", "shopping": "price"}[searchType]
		got := invoke(t, []string{"search", "cats", "--searchType", searchType, "--fields", fields}, baseURL, "test-key")
		if got.status != 0 || got.output != want+"\n" {
			t.Errorf("--searchType %s: exit %d, %s, stderr %s", searchType, got.status, got.output, got.errors)
		}
	}
	empty := answering(t, map[string]string{"/v1/search": `{"results":[],"zeroResults":true}`}, new([]string))
	for _, list := range [][]string{{"search", "zzzz", "--fields", "nothing"}, {"search", "zzzz", "--searchType", "images", "--fields", "imageUrl"}} {
		if got := invoke(t, list, empty, "test-key"); got.status != 0 || got.output != `{"results":[],"zeroResults":true}`+"\n" || got.errors != "" {
			t.Errorf("%v with no records: %+v", list, got)
		}
	}
}

// A name that no record has is left out, and stderr says so, unless -q. When
// no record has any of the names, the command prints nothing and exits 2,
// listing the fields there are, and corrects --fields when each name is a
// near miss of one.
func TestAFieldNoRecordHasIsSaid(t *testing.T) {
	var sent []string
	baseURL := answering(t, fieldsAnswers, &sent)

	got := invoke(t, []string{"search", "postgres", "--fields", "title,rank"}, baseURL, "test-key")
	if got.status != 0 || got.output != `{"results":[{"title":"Vacuum & <autovacuum>"},{"title":"Tuning"}],"zeroResults":false,"entity":{"title":"Postgres"}}`+"\n" {
		t.Errorf("some names there: %+v", got)
	}
	if want := "warning: no record here has rank, which --fields names. The records have position, title, url, snippet and source.\n"; got.errors != want {
		t.Errorf("stderr %q, want %q", got.errors, want)
	}
	if got := invoke(t, []string{"search", "postgres", "--fields", "title,rank", "-q"}, baseURL, "test-key"); got.status != 0 || got.errors != "" {
		t.Errorf("-q: %+v", got)
	}

	for _, c := range []struct {
		list         []string
		message, fix string
	}{
		{[]string{"search", "postgres", "--fields", "titel,snipet"}, "No record here has titel or snipet, which --fields names. The records have position, title, url, snippet and source.", "string search postgres --fields title,snippet"},
		{[]string{"sitemap", "status", "job-1", "--fields=stauts"}, "The JSON here has no stauts, which --fields names. It has jobId, status, pagesProcessed, pending and next_commands.", "string sitemap status job-1 --fields=status"},
		{[]string{"search", "postgres", "--fields", "source.domian"}, "No record here has source.domian, which --fields names. The records have position, title, url, snippet and source.", ""},
		{[]string{"search", "postgres", "--fields", "zzz,title.x"}, "No record here has zzz or title.x, which --fields names. The records have position, title, url, snippet and source.", ""},
	} {
		got := invoke(t, append(c.list, "--json"), baseURL, "test-key")
		envelope, _ := decode(t, got.errors).(map[string]any)["error"].(map[string]any)
		if got.status != exitUsage || got.output != "" || envelope["code"] != "invalid_usage" || envelope["reason"] != "unknown_field" || envelope["message"] != c.message {
			t.Errorf("%v: exit %d, stdout %q, stderr %s", c.list, got.status, got.output, got.errors)
		}
		steps, _ := envelope["next_commands"].([]any)
		switch fix := c.fix + " --json"; {
		case c.fix == "" && len(steps) != 0:
			t.Errorf("%v: next_commands %v, want none", c.list, steps)
		case c.fix != "" && (len(steps) != 1 || steps[0].(map[string]any)["command"] != fix):
			t.Errorf("%v: next_commands %v, want %s", c.list, steps, fix)
		}
	}
}

// With -o FILE the file holds what --fields keeps, and names no record has
// save no file.
func TestFieldsChooseWhatOutputSaves(t *testing.T) {
	baseURL := answering(t, fieldsAnswers, new([]string))
	dir := t.TempDir()
	saved := filepath.Join(dir, "jobs.json")
	if got := invoke(t, []string{"sitemap", "list", "-o", saved, "--fields", "jobId"}, baseURL, "test-key"); got.status != 0 || got.output != "" {
		t.Fatalf("-o: %+v", got)
	}
	if data, err := os.ReadFile(saved); err != nil || string(data) != `{"jobs":[{"jobId":"job-1"},{"jobId":"job-2"}]}`+"\n" {
		t.Errorf("saved %q, %v", data, err)
	}
	missed := filepath.Join(dir, "missed.json")
	if got := invoke(t, []string{"sitemap", "list", "-o", missed, "--fields", "jbo"}, baseURL, "test-key"); got.status != exitUsage {
		t.Errorf("-o with no such field: %+v", got)
	}
	if _, err := os.Stat(missed); !os.IsNotExist(err) {
		t.Errorf("a file was saved for a field no record has: %v", err)
	}
}

// A command that spends or writes has done so by the time its JSON comes.
// When no record has any of the names, it prints its answer whole and says
// so on stderr, rather than exit 2 and lose the answer to a run that would
// spend or write again. -o FILE saves it whole, and -q leaves out the
// warning.
func TestAnAnswerTheFieldsMissIsKeptWholeOnceSpent(t *testing.T) {
	var sent []string
	baseURL := answering(t, fieldsAnswers, &sent)
	for _, c := range []struct {
		list           []string
		stdout, stderr string
	}{
		{[]string{"integrations", "run", "reddit", "post", "--post", "t3_1", "--fields", "titel"}, fieldsAnswers["/v1/integrations/reddit/post"],
			"warning: no record here has titel, which --fields names. The records have id, title, author and comments. The answer is printed whole, since the command has already run.\n"},
		{[]string{"request", "POST", "https://example.com", "--body", "{}", "--fields", "statsCode"}, fieldsAnswers["/v1/fetch"],
			"warning: the JSON here has no statsCode, which --fields names. It has statusCode, headers, data and finalUrl. The answer is printed whole, since the command has already run.\n"},
		{[]string{"request", "POST", "https://example.com", "--body", "{}", "--fields", "statsCode", "-q"}, fieldsAnswers["/v1/fetch"], ""},
	} {
		sent = nil
		got := invoke(t, c.list, baseURL, "test-key")
		if got.status != 0 || got.output != c.stdout+"\n" || got.errors != c.stderr || len(sent) == 0 || strings.Count(strings.Join(sent, " "), "POST") != 1 {
			t.Errorf("string %s: exit %d, sent %v\nstdout %s\nstderr %q\nwant   %q", strings.Join(c.list, " "), got.status, sent, got.output, got.errors, c.stderr)
		}
	}
	saved := filepath.Join(t.TempDir(), "post.json")
	if got := invoke(t, []string{"integrations", "run", "reddit", "post", "--post", "t3_1", "--fields", "titel", "-o", saved}, baseURL, "test-key"); got.status != 0 || got.output != "" {
		t.Errorf("-o: %+v", got)
	}
	if data, err := os.ReadFile(saved); err != nil || string(data) != fieldsAnswers["/v1/integrations/reddit/post"]+"\n" {
		t.Errorf("-o saved %q, %v", data, err)
	}
}

// --fields needs JSON on stdout, so a command that would print text exits 2
// before it sends a request or changes anything, with the command that
// prints JSON when there is one. Help, which is for reading, is left as it
// is.
func TestFieldsNeedJSONBeforeAnythingIsSent(t *testing.T) {
	var sent []string
	baseURL := answering(t, fieldsAnswers, &sent)
	for _, c := range []struct {
		list []string
		fix  string
	}{
		{[]string{"fetch", "https://example.com", "--fields", "statusCode"}, "string fetch https://example.com --fields statusCode --format json"},
		{[]string{"--json", "example.com", "--fields", "statusCode"}, "string fetch example.com --json --fields statusCode --format json"},
		{[]string{"fetch", "https://example.com", "--format", "raw", "--fields", "statusCode"}, "string fetch https://example.com --format raw --fields statusCode --format json"},
		{[]string{"request", "POST", "https://example.com", "--format", "markdown", "--fields", "statusCode"}, "string request POST https://example.com --format markdown --fields statusCode --format json"},
		{[]string{"search", "postgres", "--format", "raw", "--fields", "title"}, "string search postgres --format raw --fields title --format structured"},
		{[]string{"fetch", "https://example.com", "--format", "json", "--max-tokens", "100", "--fields", "statusCode"}, ""},
		{[]string{"status", "--fields", "version"}, "string status --fields version --json"},
		{[]string{"integrations", "--fields", "name"}, "string integrations --fields name --json"},
		{[]string{"--version", "--fields", "version"}, "string version --fields version --json"},
		{[]string{"skill", "--fields", "name"}, ""},
		{[]string{"completion", "bash", "--fields", "name"}, ""},
	} {
		got := invoke(t, c.list, baseURL, "test-key")
		if got.status != exitUsage || got.output != "" || !strings.Contains(got.errors, "--fields chooses") {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", c.list, got.status, got.output, got.errors)
		}
		if fix := "`" + c.fix + "`"; c.fix != "" && !strings.Contains(got.errors, fix) || c.fix == "" && strings.Contains(got.errors, "`") {
			t.Errorf("%v: stderr %q, want %q", c.list, got.errors, c.fix)
		}
	}
	if len(sent) > 0 {
		t.Errorf("requests went out: %v", sent)
	}

	// Nothing changes on disk: logout keeps the key, and init installs
	// nothing.
	home := t.TempDir()
	env := envOf(map[string]string{"HOME": home})
	path, _ := credentialsPath(env)
	if err := saveCredentials(path, credentials{APIKey: "sk_saved"}); err != nil {
		t.Fatal(err)
	}
	for _, list := range [][]string{{"logout", "--fields", "path"}, {"init", "--no-login", "--fields", "agents"}} {
		var stdout, stderr bytes.Buffer
		if status := run(list, runtime{env: env, client: &http.Client{Transport: noNetwork{t}}, stdout: &stdout, stderr: &stderr}); status != exitUsage {
			t.Errorf("%v: exit %d, stderr %q", list, status, stderr.String())
		}
	}
	if entries, _ := os.ReadDir(home); len(entries) != 1 {
		t.Errorf("home holds %v, want only the saved key's directory", entries)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the saved key is gone: %v", err)
	}

	// Help is read as it is, and --json makes it JSON that --fields chooses
	// from.
	for _, list := range [][]string{{"search", "--help", "--fields", "title"}, {"help", "search", "--fields", "title"}, {"--fields", "title", "sitemap"}, {"status", "--fields", "version", "-h"}, {"help", "skill", "--fields", "name"}} {
		page := list[slices.IndexFunc(list, func(w string) bool { return commandDocs[w].summary != "" })]
		if got := runOffline(t, list...); got.status != 0 || got.output != helpFor(page) {
			t.Errorf("%v: exit %d, stderr %q", list, got.status, got.errors)
		}
	}
	if got := runOffline(t, "help", "--json", "--fields", "name,schemaVersion"); got.status != 0 || got.output != `{"name":"string","schemaVersion":1}`+"\n" {
		t.Errorf("help --json --fields: %+v", got)
	}
	if got := runOffline(t, "version", "--json", "--fields", "version"); got.status != 0 || got.output != `{"version":"`+version+`"}`+"\n" {
		t.Errorf("version --json --fields: %+v", got)
	}
}

// --fields takes names split by commas, each perhaps dotted, and refuses an
// empty one before anything runs.
func TestFieldsRefuseAnEmptyName(t *testing.T) {
	for _, value := range []string{"", " ", "title,,url", "title,", "source..domain", ".domain"} {
		got := runOffline(t, "search", "postgres", "--fields", value)
		if got.status != exitUsage || got.output != "" || !strings.Contains(got.errors, "--fields") {
			t.Errorf("--fields %q: %+v", value, got)
		}
	}
	if got := runOffline(t, "sitemap", "list", "--fields"); got.status != exitUsage || !strings.Contains(got.errors, "--fields needs the names of fields") {
		t.Errorf("--fields with no value: %+v", got)
	}
	paths, err := parseFields(" title , source.domain,title")
	if err != nil || len(paths) != 2 || paths[0].String() != "title" || paths[1].String() != "source.domain" {
		t.Errorf("parseFields: %v, %v", paths, err)
	}
}

// The filter chooses from each JSON value written to it once the value is
// whole, however it was split, and lets anything else through as it came.
// What it chooses waits while no record has had any of the names.
func TestTheFieldFilterChoosesFromEachValueOnceWhole(t *testing.T) {
	var out bytes.Buffer
	f := newFieldFilter([]fieldPath{{"status"}}, &out)
	for _, piece := range []string{`{"user_code":"AB`, `CD"}`, "\n", `{"status":"logged_in","credentials":"/x"}` + "\n" + `{"status":2,"at":0}`, "  [{\"status\":1},", "{}]\n"} {
		if n, err := f.Write([]byte(piece)); n != len(piece) || err != nil {
			t.Fatalf("Write(%q): %d, %v", piece, n, err)
		}
		if piece == `CD"}` && out.Len() != 0 {
			t.Errorf("a value no record had the names of went out at once: %q", out.String())
		}
	}
	if err := f.end(io.Discard, false); err != nil {
		t.Fatal(err)
	}
	if want := "{}\n" + `{"status":"logged_in"}` + "\n" + `{"status":2}` + "\n" + `[{"status":1},{}]` + "\n"; out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}

	out.Reset()
	text := newFieldFilter([]fieldPath{{"status"}}, &out)
	text.Write([]byte(`{"code":1}`))
	text.Write([]byte("string search: search the web\n"))
	text.Write([]byte(`{"status":1}`))
	if err := text.end(io.Discard, false); err != nil || out.String() != "{}\nstring search: search the web\n"+`{"status":1}` {
		t.Errorf("text: %q, %v", out.String(), err)
	}

	out.Reset()
	none := newFieldFilter([]fieldPath{{"status"}}, &out)
	none.Write([]byte(`{"user_code":"ABCD"}` + "\n"))
	if err := none.end(io.Discard, false); err == nil || out.Len() != 0 {
		t.Errorf("no record had the names: %q, %v", out.String(), err)
	}

	// For a command that changed something, such as login's link, what has
	// none of the names goes out whole at once.
	out.Reset()
	var said bytes.Buffer
	changed := newFieldFilter([]fieldPath{{"status"}}, &out)
	changed.changes = true
	changed.Write([]byte(`{"user_code":"ABCD"}` + "\n"))
	if out.String() != `{"user_code":"ABCD"}`+"\n" {
		t.Errorf("a changing command's value went out as %q", out.String())
	}
	if err := changed.end(&said, false); err != nil || said.String() != "warning: the JSON here has no status, which --fields names. It has user_code. The answer is printed whole, since the command has already run.\n" {
		t.Errorf("end: %v, stderr %q", err, said.String())
	}
}

// A command string suggests keeps --fields where it prints the same JSON, as
// the command meant after a typo, a wait to resume and the approve a wait
// meets do, and leaves it out where it prints other JSON, as the poll a
// sign-in's or a claim's link names does: that prints the key it saves.
func TestASuggestionKeepsFieldsOnlyWhereItPrintsTheSameJSON(t *testing.T) {
	for _, c := range []struct{ list, next []string }{
		{[]string{"--fields", "title", "serch", "postgres"}, []string{"search", "postgres", "--fields", "title"}},
		{[]string{"help", "exit-code", "--fields", "status"}, []string{"help", "exit-codes", "--fields", "status"}},
	} {
		if got := runOffline(t, c.list...); got.status != exitUsage || !strings.HasSuffix(got.errors, "\n\nnext: run `string "+strings.Join(c.next, " ")+"`\n") {
			t.Errorf("string %s: %+v", strings.Join(c.list, " "), got)
		}
	}

	crawl := &crawlAPI{statuses: []string{crawlRunning}}
	base := crawl.start(t)
	got := runCrawl(base, execCtx{agent: "claude-code"}, "sitemap", "status", "job-1", "--wait", "--fields", "status")
	if got.status != exitTimeout || !strings.HasPrefix(got.errors, "job job-1 · running · resume: string sitemap status job-1 --wait --fields status\n") ||
		!strings.HasSuffix(got.errors, "error: Stopped waiting after 90s; the crawl keeps running.\n\nnext: run `string sitemap status job-1 --wait --fields status`\n") {
		t.Errorf("sitemap status job-1 --wait --fields status: %+v", got)
	}
	expectJSON(t, got.output, `{"status":"running"}`)
	waiting := &crawlAPI{statuses: []string{`{"jobId":"job-1","status":"awaiting_approval"}`}}
	got = runCrawl(waiting.start(t), execCtx{}, "sitemap", "status", "job-1", "--wait", "--fields", "status")
	if want := "error: The crawl is waiting for approval.\n\nnext: run `string sitemap approve job-1 --wait --fields status`\n"; got.status != exitUsage || !strings.HasSuffix(got.errors, want) {
		t.Errorf("a wait that meets a quote: exit %d, stderr %q, want it to end %q", got.status, got.errors, want)
	}

	browser := &signInService{status: thenExpired(browserPending)}
	browser.start(t)
	session := newSignInSession(t)
	session.env["STRING_AGENT"] = "claude-code"
	if got := session.run("login", "--browser", "--json", "--fields", "next"); got.status != 0 || got.output != `{"next":"string login --poll --json"}`+"\n" {
		t.Errorf("login --browser --json --fields next: %+v", got)
	}

	var inFlight atomic.Bool
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if inFlight.Load() {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"claimed_or_in_flight","error_description":"This registration has already been claimed."}`)
			return
		}
		io.WriteString(w, claimAttemptAnswer)
	}))
	t.Cleanup(portal.Close)
	defer func(p string) { portalURL = p }(portalURL)
	portalURL = portal.URL
	home, _ := claimHome(t)
	claim := []string{"trial", "claim", "--email", "dev@localhost", "--json", "--fields", "next"}
	var stdout, stderr bytes.Buffer
	rt := runtime{env: envOf(map[string]string{"HOME": home}), client: http.DefaultClient, stdout: &stdout, stderr: &stderr, sleep: func(time.Duration) {}}
	if status := run(claim, rt); status != 0 || stdout.String() != `{"next":"string trial claim --poll --json"}`+"\n" {
		t.Errorf("string %s: status %d, stdout %q, stderr %q", strings.Join(claim, " "), status, stdout.String(), stderr.String())
	}
	inFlight.Store(true)
	stderr.Reset()
	status := run(claim, rt)
	if envelope, _ := decode(t, lastLine(stderr.String())).(map[string]any)["error"].(map[string]any); status != exitFailure || envelope["next_step"] != "run `string trial claim --poll --json` to collect the key" {
		t.Errorf("a claim already under way: status %d, stderr %q", status, stderr.String())
	}

	service := &signInService{token: []signInAnswer{signInApproved}, key: []signInAnswer{signInKey}}
	service.start(t)
	session = newSignInSession(t)
	session.env["STRING_AGENT"] = "claude-code"
	started := session.run("login", "--json", "--fields", "next", "--timeout", "5m")
	if started.status != 0 || started.output != `{"next":"string login --poll --json --timeout 5m"}`+"\n" {
		t.Fatalf("login --json --fields next --timeout 5m: %+v", started)
	}
	poll := strings.Fields(decode(t, started.output).(map[string]any)["next"].(string))
	if done := session.run(poll[1:]...); done.status != 0 || decode(t, done.output).(map[string]any)["status"] != "logged_in" || strings.Contains(done.errors, "warning") {
		t.Errorf("%s: %+v", strings.Join(poll, " "), done)
	}
}

// Those that print other JSON repeat the global switches given but --fields,
// in any of its spellings.
func TestCarriedLeavesOutFields(t *testing.T) {
	given := []string{"--json", "--fields", "a,b", "-q", "--Fields", "c", "--fields=d", "--timeout", "30s", "--fields"}
	if got, want := carried(given), []string{"--json", "-q", "--timeout", "30s"}; !slices.Equal(got, want) {
		t.Errorf("carried(%q) = %q, want %q", given, got, want)
	}
}
