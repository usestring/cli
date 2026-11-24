package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// runWith runs string with client and the variables in vars, and decodes
// the --json error on the last line of stderr.
func runWith(t *testing.T, client *http.Client, vars map[string]string, list ...string) (result, map[string]any) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	vars["HOME"] = t.TempDir()
	status := run(list, runtime{env: envOf(vars), client: client, stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr})
	got := result{status, stdout.String(), stderr.String()}
	var envelope struct {
		Error map[string]any `json:"error"`
	}
	if slices.Contains(list, "--json") && json.Unmarshal([]byte(lastLine(got.errors)), &envelope) != nil {
		t.Fatalf("%v: stderr holds no JSON error: %q", list, got.errors)
	}
	return got, envelope.Error
}

// Every --json error says its exit code, whether running the command again
// can help, whether a person must act, and the steps out of it, each with a
// type an agent can choose by.
func TestErrorsSayTheirExitCodeAndTheStepsOutOfThem(t *testing.T) {
	answer := func(status int, header, body string) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			if name, value, ok := strings.Cut(header, ": "); ok {
				w.Header().Set(name, value)
			}
			w.WriteHeader(status)
			io.WriteString(w, body)
		}
	}
	for _, c := range []struct {
		about      string
		key        string
		list       []string
		handler    func(http.ResponseWriter, *http.Request)
		exit       int
		retryable  bool
		userAction bool
		steps      []nextCommand
	}{
		{"a keyless refusal", "", []string{"fetch", "https://example.com", "--json"}, answer(400, "", `{"error":"Keyless requests can't use mainContentOnly"}`), exitAuth, false, true,
			[]nextCommand{{Type: stepLogin, Command: "string login", Argv: []string{"string", "login"}}, {Type: stepEnv, Why: "set STRING_API_KEY to an API key from https://portal.usestring.ai/api-keys"}}},
		{"a rate limit", "test-key", []string{"search", "coffee", "--json"}, answer(429, "Retry-After: 30", `{"error":"Too many requests"}`), exitRateLimit, true, false,
			[]nextCommand{{Type: stepRetry, Why: "run the same command again"}}},
		{"no credit", "test-key", []string{"search", "coffee", "--json"}, answer(402, "", `{"error":"Out of credit","topUpUrl":"https://portal.usestring.ai/billing"}`), exitPayment, false, true,
			[]nextCommand{{Type: stepTell, Why: "add credit at https://portal.usestring.ai/billing"}}},
		{"a failure at String", "test-key", []string{"search", "coffee", "--json"}, answer(503, "", `{"error":"Unavailable"}`), exitUpstream, true, false,
			[]nextCommand{{Type: stepRetry, Why: "run the same command again"}}},
		{"one String says not to retry", "test-key", []string{"search", "coffee", "--json"}, answer(503, "", `{"error":"Unavailable","retryable":false}`), exitUpstream, false, false, []nextCommand{}},
		{"a bad request", "test-key", []string{"search", "coffee", "--json"}, answer(400, "", `{"error":"bad query"}`), exitUsage, false, false, []nextCommand{}},
		{"a crawl that waits for approval", "test-key", []string{"sitemap", "status", "job-1", "--wait", "--json"}, answer(200, "", `{"jobId":"job-1","status":"awaiting_approval"}`), exitUsage, false, true,
			[]nextCommand{{Type: stepFix, Command: "string sitemap approve job-1 --wait --json", Argv: []string{"string", "sitemap", "approve", "job-1", "--wait", "--json"}, Why: "starts the crawl at its quoted cost, which needs the user's go"}}},
	} {
		base := api(t, c.handler)
		got, envelope := runWith(t, http.DefaultClient, map[string]string{"STRING_API_KEY": c.key, "STRING_API_BASE_URL": base}, c.list...)
		var steps []nextCommand
		data, _ := json.Marshal(envelope["next_commands"])
		_ = json.Unmarshal(data, &steps)
		if steps == nil {
			steps = []nextCommand{}
		}
		if got.status != c.exit || envelope["exit_code"] != float64(c.exit) || envelope["retryable"] != c.retryable || envelope["user_action_required"] != c.userAction || !reflect.DeepEqual(steps, c.steps) {
			t.Errorf("%s: exit %d, envelope %v\nwant exit %d, retryable %t, user_action_required %t, next_commands %+v", c.about, got.status, envelope, c.exit, c.retryable, c.userAction, c.steps)
		}
	}
}

// failingTransport fails every request with err, as a network that gives no
// answer does.
type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// A request that gets no answer says why and that it can be sent again,
// unless a sandbox blocks the network: then it says so, and the step is the
// person's, since trying again there can't help.
func TestANetworkErrorSaysWhetherASandboxBlocksIt(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: &net.AddrError{Err: "connection refused", Addr: "api.example.test:443"}}
	unknown := &net.DNSError{Err: "no such host", Name: "api.example.test", IsNotFound: true}
	for _, c := range []struct {
		about   string
		err     error
		sandbox string
		reason  string
		retry   bool
	}{
		{"a refused connection", refused, "", "connect", true},
		{"a name that doesn't resolve", unknown, "", "dns", true},
		{"a sandbox's switch turned off", unknown, "0", "dns", true},
		{"a sandbox that blocks the network", unknown, "1", "sandbox", false},
	} {
		vars := map[string]string{"STRING_API_KEY": "test-key", "STRING_API_BASE_URL": "https://api.example.test/v1"}
		if c.sandbox != "" {
			vars["CODEX_SANDBOX_NETWORK_DISABLED"] = c.sandbox
		}
		got, envelope := runWith(t, &http.Client{Transport: failingTransport{c.err}}, vars, "search", "coffee", "--json")
		if got.status != exitFailure || envelope["reason"] != c.reason || envelope["retryable"] != c.retry || envelope["user_action_required"] != (c.reason == "sandbox") {
			t.Errorf("%s: exit %d, envelope %v", c.about, got.status, envelope)
		}
		if c.reason != "sandbox" {
			continue
		}
		const ask = "ask the person to allow api.example.test in the sandbox's network settings, or to run string outside it"
		data, _ := json.Marshal(envelope["next_commands"])
		if envelope["message"] != "string couldn't reach api.example.test: the sandbox this command runs in blocks the network." || string(data) != `[{"type":"tell_user","why":"`+ask+`"}]` {
			t.Errorf("%s: %v", c.about, envelope)
		}
		text, _ := runWith(t, &http.Client{Transport: failingTransport{c.err}}, vars, "search", "coffee")
		if want := "error: string couldn't reach api.example.test: the sandbox this command runs in blocks the network.\n\nnext: " + ask + "\n"; text.errors != want {
			t.Errorf("%s, as text: %q, want %q", c.about, text.errors, want)
		}
	}
}
