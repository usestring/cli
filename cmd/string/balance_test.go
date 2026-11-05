package main

import (
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestBalancePrintsTheAPIsBalanceAndFailsWhenItCant(t *testing.T) {
	const balance = `{"currency":"USD","balance":25,"spent":10.5,"held":2.25,"remaining":12.25,"exhausted":false,"asOf":"2026-11-05T09:30:00Z","plan":{"tier":"starter","name":"Starter"},"autoTopUp":{"enabled":false}}`
	code, body := http.StatusOK, balance
	var calls []string
	baseURL := api(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		w.WriteHeader(code)
		io.WriteString(w, body)
	})

	got := invoke(t, []string{"balance"}, baseURL, "test-key")
	if got.status != 0 || got.output != balance+"\n" || !reflect.DeepEqual(calls, []string{"GET /v1/account/balance Bearer test-key"}) {
		t.Errorf("got %+v, calls %v", got, calls)
	}

	code, body = http.StatusNotFound, `{"error":"Not found"}`
	if got := invoke(t, []string{"balance"}, baseURL, "test-key"); got.status != 2 || got.output != "" || !strings.Contains(got.errors, "HTTP 404") || strings.Contains(got.errors, "test-key") {
		t.Errorf("unavailable %+v", got)
	}
	code, body = http.StatusOK, "<html>Sign in</html>"
	if got := invoke(t, []string{"balance"}, baseURL, "test-key"); got.status != 1 || got.output != "" || !strings.Contains(got.errors, "not a balance") {
		t.Errorf("not JSON %+v", got)
	}

	keyless := invoke(t, []string{"balance", "--json"}, baseURL, "")
	if keyless.status != 3 || decode(t, keyless.errors).(map[string]any)["error"].(map[string]any)["code"] != "key_required" || len(calls) != 3 {
		t.Errorf("keyless %+v, calls %v", keyless, calls)
	}
}
