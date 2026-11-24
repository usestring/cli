package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A command string doesn't have names the one meant, when it is a synonym or
// a near miss, and the next step is the same line with that command. string
// never runs the guess.
func TestAnUnknownCommandNamesTheOneMeant(t *testing.T) {
	for _, c := range []struct {
		list    []string
		meant   string
		command string
	}{
		{[]string{"fecth", "https://example.com"}, "fetch", "string fetch https://example.com"},
		{[]string{"statsu"}, "status", "string status"},
		{[]string{"serch", "coffee shops", "--searchCount", "5"}, "search", "string search 'coffee shops' --searchCount 5"},
		{[]string{"scrape", "https://example.com"}, "fetch", "string fetch https://example.com"},
		{[]string{"get", "https://example.com"}, "fetch", "string fetch https://example.com"},
		{[]string{"read", "https://example.com"}, "fetch", "string fetch https://example.com"},
		{[]string{"crawl", "https://docs.example.com", "--maxPages", "5"}, "sitemap submit", "string sitemap submit https://docs.example.com --maxPages 5"},
		{[]string{"map", "https://docs.example.com"}, "sitemap submit", "string sitemap submit https://docs.example.com"},
		{[]string{"setup"}, "init", "string init"},
		{[]string{"setup", "agent", "--no-login"}, "init", "string init --no-login"},
		{[]string{"auth"}, "login", "string login"},
		{[]string{"signin", "--browser"}, "login", "string login --browser"},
		{[]string{"sign-in"}, "login", "string login"},
		{[]string{"credits"}, "balance", "string balance"},
		{[]string{"doctor"}, "status", "string status"},
		{[]string{"help", "fecth"}, "fetch", "string fetch --help"},
	} {
		typed := c.list[0]
		if typed == "help" {
			typed = c.list[1]
		}
		got := runOffline(t, c.list...)
		if want := "error: Unknown command: " + typed + ". Did you mean " + c.meant + "?\n\nnext: run `" + c.command + "`\n"; got.status != exitUsage || got.output != "" || got.errors != want {
			t.Errorf("string %s: %+v, want exit 2 and %q", strings.Join(c.list, " "), got, want)
		}
	}
	if got := runOffline(t, "xyzzy"); got.status != exitUsage || got.errors != "error: Unknown command: xyzzy\n\nnext: run `string --help` for the commands\n" {
		t.Errorf("string xyzzy: %+v", got)
	}
	// The command meant keeps --json, wherever it was given.
	got := runOffline(t, "--json", "fecth", "https://example.com")
	expectJSON(t, got.errors, "{\"error\":{\"code\":\"unknown_command\",\"message\":\"Unknown command: fecth. Did you mean fetch?\",\"next_step\":\"run `string fetch https://example.com --json`\",\"retry_after_seconds\":null,\"exit_code\":2,\"retryable\":false,\"user_action_required\":false,\"next_commands\":[{\"type\":\"fix_command\",\"command\":\"string fetch https://example.com --json\",\"argv\":[\"string\",\"fetch\",\"https://example.com\",\"--json\"],\"why\":\"the command meant\"}]}}")
}

// Every multi-word flag takes its camelCase, kebab-case and snake_case
// spellings, and a near miss names the flag meant.
func TestFlagsTakeEverySpellingAndNameANearMiss(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	base := api(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, r.URL.Path+" "+string(data))
		mu.Unlock()
		if r.URL.Path == "/v1/sitemap" {
			io.WriteString(w, `{"jobId":"job-1","status":"awaiting_approval"}`)
			return
		}
		io.WriteString(w, "# Example Domain\n")
	})
	sent := map[string]string{
		"/v1/fetch":   `{"url":"https://example.com/","method":"GET","format":"markdown","mainContentOnly":true,"countryCode":"DE"}`,
		"/v1/sitemap": `{"url":"https://example.com/","maxPages":5,"pathPrefix":"/docs"}`,
	}
	for _, list := range [][]string{
		{"fetch", "https://example.com", "--mainContentOnly", "--countryCode", "DE"},
		{"fetch", "https://example.com", "--main-content-only", "--country-code", "DE"},
		{"fetch", "https://example.com", "--main_content_only", "--country_code=DE"},
		{"sitemap", "submit", "https://example.com", "--max-pages", "5", "--path_prefix", "/docs"},
	} {
		mu.Lock()
		bodies = nil
		mu.Unlock()
		got := invoke(t, list, base, "test-key")
		mu.Lock()
		called := bodies
		mu.Unlock()
		if got.status != 0 || len(called) != 1 {
			t.Errorf("%s: exit %d after %v: %s", strings.Join(list, " "), got.status, called, got.errors)
			continue
		}
		path, body, _ := strings.Cut(called[0], " ")
		expectJSON(t, body, sent[path])
	}

	// The CLI's own flags too: --envFile and --env_file are --env-file, and
	// --noWait is --no-wait.
	service := &signInService{}
	service.start(t)
	envFile, _ := filepath.Abs(".env")
	for _, list := range [][]string{{"login", "--noWait", "--envFile", ".env", "--device_name", "ci-runner"}, {"login", "--no_wait", "--env_file", ".env", "--deviceName", "ci-runner"}} {
		session := newSignInSession(t)
		got := session.run(list...)
		var pending pendingLogin
		data, err := os.ReadFile(session.file("pending.json"))
		if got.status != 0 || err != nil || json.Unmarshal(data, &pending) != nil || pending.EnvFile != envFile || pending.DeviceName != "ci-runner" {
			t.Errorf("%s: %+v, pending %+v (%v)", strings.Join(list, " "), got, pending, err)
		}
	}
	// --Poll is --poll for trial claim, which then finds no trial to claim.
	if got := runOffline(t, "trial", "claim", "--Poll"); got.status != exitUsage || !strings.HasPrefix(got.errors, "error: No trial registration is waiting to be claimed.") {
		t.Errorf("trial claim --Poll: %+v", got)
	}
	// So are the switches every command takes.
	list, global := takeGlobalSwitches([]string{"fetch", "--no_input", "--noOpen", "--JSON", "https://example.com"})
	if !global.noInput || !global.noOpen || !global.json || strings.Join(list, " ") != "fetch https://example.com" {
		t.Errorf("takeGlobalSwitches: %v, %+v", list, global)
	}

	// A near miss names the flag meant, of the API's fields and the CLI's own.
	for list, want := range map[string]string{
		"fetch https://example.com --mainContentOnyl": "Unknown option '--mainContentOnyl'. Did you mean '--mainContentOnly'?",
		"fetch https://example.com --outptu x.md":     "Unknown option '--outptu'. Did you mean '--output'?",
		"login --devce-name ci":                       "Unknown option '--devce-name'. Did you mean '--device-name'?",
		"search coffee --serchCount 5":                "Unknown option '--serchCount'. Did you mean '--searchCount'?",
	} {
		if got := runOffline(t, strings.Fields(list)...); got.status != exitUsage || got.errors != "error: "+want+"\n" {
			t.Errorf("string %s: %+v, want %q", list, got, want)
		}
	}
}
