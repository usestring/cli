package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A field only JSON carries, asked for with another format, exits 2 with the
// fix, where it used to be dropped without a word.
func TestAJSONOnlyFieldNeedsJSON(t *testing.T) {
	for list, want := range map[string]string{
		`fetch https://example.com --jsonSchema {} --format markdown`:         "error: --jsonSchema comes back only in JSON, so --format markdown would lose it; drop --format, or use --format json\n",
		`fetch https://example.com --screenshot --format raw`:                 "error: --screenshot comes back only in JSON, so --format raw would lose it; drop --format, or use --format json\n",
		`request POST https://example.com --captureCookies --format markdown`: "error: --captureCookies comes back only in JSON, so --format markdown would lose it; drop --format, or use --format json\n",
	} {
		if got := runOffline(t, strings.Fields(list)...); got.status != exitUsage || got.errors != want {
			t.Errorf("string %s: %+v, want %q", list, got, want)
		}
	}
	base, _ := fetchAPI(t, `{"statusCode":200}`)
	if got := runAs(base, execCtx{}, "fetch", "https://example.com", "--jsonSchema", "{}", "--format", "json"); got.status != 0 {
		t.Errorf("--jsonSchema --format json: %+v", got)
	}
}

// --body-file - reads the body from stdin, and --body @FILE warns when FILE
// exists, since --body sends its text as written.
func TestRequestBodiesFromStdinAndTheAtWarning(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	base := api(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Body string `json:"body"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		sent = append(sent, body.Body)
		mu.Unlock()
		io.WriteString(w, `{"statusCode":200}`)
	})
	request := func(stdin string, list ...string) result {
		var stdout, stderr bytes.Buffer
		status := run(append([]string{"request", "POST", "https://example.com/orders"}, list...), runtime{env: envOf(map[string]string{"STRING_API_KEY": "test-key", "STRING_API_BASE_URL": base}), client: http.DefaultClient, stdin: strings.NewReader(stdin), stdout: &stdout, stderr: &stderr})
		return result{status, stdout.String(), stderr.String()}
	}
	if got := request(`{"sku":"A1"}`, "--body-file", "-"); got.status != 0 || got.errors != "" || len(sent) != 1 || sent[0] != `{"sku":"A1"}` {
		t.Errorf("--body-file -: %+v, sent %q", got, sent)
	}
	if got := request("{}", "--input", "-", "--body-file", "-"); got.status != exitUsage || got.errors != "error: Only one of --input - and --body-file - can read stdin\n" {
		t.Errorf("--input - --body-file -: %+v", got)
	}

	dir := t.TempDir()
	order := filepath.Join(dir, "order.json")
	if err := os.WriteFile(order, []byte(`{"sku":"A1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sent = nil
	if got := request("", "--body", "@"+order); got.status != 0 || got.errors != "warning: --body @"+order+" sends the text @"+order+"; did you mean --body-file "+order+"?\n" || sent[0] != "@"+order {
		t.Errorf("--body @FILE: %+v, sent %q", got, sent)
	}
	if got := request("", "--body", "@"+filepath.Join(dir, "missing.json")); got.status != 0 || got.errors != "" {
		t.Errorf("--body @MISSING: %+v", got)
	}
}
