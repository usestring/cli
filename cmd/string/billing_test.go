package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// runPolling runs the CLI with env as its whole environment, a fixed clock and
// the waits it asks for recorded instead of slept.
func runPolling(t *testing.T, list []string, env map[string]string, now time.Time) (result, []time.Duration) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	var waits []time.Duration
	status := run(list, runtime{
		env: func(name string) (string, bool) {
			value, ok := env[name]
			return value, ok
		},
		client: http.DefaultClient,
		stdin:  strings.NewReader(""),
		stdout: &stdout,
		stderr: &stderr,
		sleep:  func(d time.Duration) { waits = append(waits, d) },
		now:    func() time.Time { return now },
	})
	return result{status, stdout.String(), stderr.String()}, waits
}

func TestTopupPrintsTheApprovalLinkAndWaitsForThePayment(t *testing.T) {
	answer := func(status string) string {
		return `{"id":"cs_test_a1","status":"` + status + `","kind":"credit_pack","amountUsd":20,"approvalUrl":"https://portal.example/usage/approve/cs_test_a1",` +
			`"pollUrl":"https://portal.example/api/agent/checkout/cs_test_a1","expiresAt":"2026-11-05T11:00:00.000Z"}`
	}
	var posts []string
	var statuses []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("%s %s without the key", r.Method, r.URL.Path)
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /api/agent/checkout":
			posts = append(posts, requestBody(t, r))
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, answer("pending"))
		case "GET /api/agent/checkout/cs_test_a1":
			io.WriteString(w, answer(statuses[0]))
			statuses = statuses[1:]
		case "GET /api/agent/checkout/cs_signin":
			io.WriteString(w, "<html>Sign in</html>")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	defer func(p string) { portalURL = p }(portalURL)
	portalURL = server.URL
	env := map[string]string{"STRING_API_KEY": "test-key"}
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

	got, _ := runPolling(t, []string{"topup", "--packPlanId", "2"}, env, now)
	want := `To add a $20.00 credit pack, open this link signed in to String, then approve and pay:
https://portal.example/usage/approve/cs_test_a1
It expires at 11:00 UTC. string topup status cs_test_a1 --wait waits for the payment.
`
	if got.status != 0 || got.output != want {
		t.Errorf("got %+v, want output\n%s", got, want)
	}
	got, _ = runPolling(t, []string{"topup", "--json"}, env, now)
	if got.status != 0 {
		t.Errorf("--json %+v", got)
	}
	expectJSON(t, got.output, answer("pending"))
	if !reflect.DeepEqual(posts, []string{`{"packPlanId":2}`, `{}`}) {
		t.Errorf("posts %v", posts)
	}

	statuses = []string{"pending", "processing", "completed"}
	got, waits := runPolling(t, []string{"topup", "status", "cs_test_a1", "--wait"}, env, now)
	if got.status != 0 || got.output != "Paid: a $20.00 credit pack.\n" || len(statuses) != 0 || !reflect.DeepEqual(waits, []time.Duration{checkoutPoll, checkoutPoll}) {
		t.Errorf("wait %+v, waits %v, unread %v", got, waits, statuses)
	}
	statuses = []string{"expired"}
	got, _ = runPolling(t, []string{"topup", "status", "cs_test_a1", "--wait", "--json"}, env, now)
	refusal := decode(t, got.errors).(map[string]any)["error"].(map[string]any)
	if got.status != 1 || refusal["code"] != "checkout_expired" || refusal["next_step"] != "run `string topup` for a new link" {
		t.Errorf("expired %+v", got)
	}
	expectJSON(t, got.output, answer("expired"))
	if got, _ := runPolling(t, []string{"topup", "status", "cs_signin"}, env, now); got.status != 1 || got.output != "" || !strings.Contains(got.errors, "named no checkout request") {
		t.Errorf("sign-in page %+v", got)
	}

	if got, _ := runPolling(t, []string{"topup", "--wait"}, env, now); got.status != 2 || !strings.Contains(got.errors, "--wait applies to string topup status ID") {
		t.Errorf("--wait without an id %+v", got)
	}
	if got, _ := runPolling(t, []string{"topup"}, map[string]string{}, now); got.status != 3 || !strings.Contains(got.errors, "string topup needs a key.") || len(posts) != 2 {
		t.Errorf("keyless %+v, posts %v", got, posts)
	}
}

func TestUpgradePrintsTheLinkToChooseAPlan(t *testing.T) {
	defer func(p string) { portalURL = p }(portalURL)
	portalURL = "https://portal.example"
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

	for _, c := range []struct {
		list []string
		want string
	}{
		{[]string{"upgrade"}, "https://portal.example/usage\n"},
		{[]string{"upgrade", "Growth"}, "https://portal.example/usage?plan=growth\n"},
		{[]string{"upgrade", "scale", "--json"}, `{"url":"https://portal.example/usage?plan=scale"}` + "\n"},
	} {
		if got, _ := runPolling(t, c.list, map[string]string{}, now); got.status != 0 || got.output != c.want {
			t.Errorf("%v: got %+v, want %q", c.list, got, c.want)
		}
	}
	if got, _ := runPolling(t, []string{"upgrade", "gold"}, map[string]string{}, now); got.status != 2 || !strings.Contains(got.errors, "Choose one of: starter, growth, scale, volume.") {
		t.Errorf("unknown plan %+v", got)
	}
}
