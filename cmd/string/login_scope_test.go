package main

import (
	"bytes"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// string login asks the sign-in service for the email scope and no other. The
// email labels the saved key. When the request also asks for web_access, an
// account that has never granted that scope cannot sign in, and on a first
// install most have not: the approval page says the device is approved, the
// token request keeps answering authorization_pending, and string login ends
// with "The code expired". An account that granted it before, by connecting an
// MCP client, gets its token either way, so signing in with one shows nothing
// wrong.
func TestLoginAsksTheSignInServiceForTheEmailScopeOnly(t *testing.T) {
	var scope []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/device_authorization" {
			http.NotFound(w, r)
			return
		}
		r.ParseForm()
		scope = r.Form["scope"]
		io.WriteString(w, `{"device_code":"device-1","user_code":"ABCD-EFGH","verification_uri":"https://example.test/device","expires_in":600,"interval":5}`)
	}))
	t.Cleanup(server.Close)
	auth, client := authURL, oauthClientID
	t.Cleanup(func() { authURL, oauthClientID = auth, client })
	authURL, oauthClientID = server.URL, "test-client"

	if got := newSignInSession(t).run("login", "--no-wait"); got.status != 0 {
		t.Fatalf("login --no-wait: %+v", got)
	}
	if want := []string{"email"}; !reflect.DeepEqual(scope, want) {
		t.Errorf("the device request asked for the scope %q, want %q and nothing more", scope, want)
	}
}

// The sign-in service's device response links its hosted approval page, even
// when String's own page is set as the device page, and some approvals made
// there stay pending. string login sends people to String's page with the same
// code instead, and uses any other link, such as another environment's, as sent.
func TestApprovalLinkSwapsOnlyTheHostedPage(t *testing.T) {
	portal := portalURL
	t.Cleanup(func() { portalURL = portal })
	portalURL = "https://portal.usestring.ai"

	for _, c := range []struct{ name, sent, code, want string }{
		{"hosted page", "https://accounts.portal.usestring.ai/device?user_code=ABCD-EFGH", "ABCD-EFGH", "https://portal.usestring.ai/device?user_code=ABCD-EFGH"},
		{"hosted page without the code", "https://accounts.portal.usestring.ai/device", "ABCD-EFGH", "https://portal.usestring.ai/device?user_code=ABCD-EFGH"},
		{"hosted host in capitals", "https://Accounts.Portal.UseString.ai/device?user_code=ABCD-EFGH", "ABCD-EFGH", "https://portal.usestring.ai/device?user_code=ABCD-EFGH"},
		{"code that needs escaping", "https://accounts.portal.usestring.ai/device", "AB CD&next=/x", "https://portal.usestring.ai/device?user_code=AB+CD%26next%3D%2Fx"},
		{"another environment", "https://accounts.dev.example.test/device?user_code=ABCD-EFGH", "ABCD-EFGH", "https://accounts.dev.example.test/device?user_code=ABCD-EFGH"},
		{"String's page already", "https://portal.usestring.ai/device?user_code=ABCD-EFGH", "ABCD-EFGH", "https://portal.usestring.ai/device?user_code=ABCD-EFGH"},
		{"another hosted page", "https://accounts.portal.usestring.ai/sign-in", "ABCD-EFGH", "https://accounts.portal.usestring.ai/sign-in"},
		{"no user code", "https://accounts.portal.usestring.ai/device", "", "https://accounts.portal.usestring.ai/device"},
	} {
		if got := approvalLink(c.sent, c.code); got != c.want {
			t.Errorf("%s: approvalLink(%q, %q) = %q, want %q", c.name, c.sent, c.code, got, c.want)
		}
	}
}

// Every way string login shows the approval link, and the browser it opens,
// gets String's page: the JSON under an agent and the --no-wait text. The
// terminal flow and init print the same link through the same code.
func TestLoginShowsAndOpensStringsApprovalPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/device_authorization" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"device_code":"device-1","user_code":"ABCD-EFGH","verification_uri":"https://accounts.portal.usestring.ai/device","verification_uri_complete":"https://accounts.portal.usestring.ai/device?user_code=ABCD-EFGH","expires_in":600,"interval":5}`)
	}))
	t.Cleanup(server.Close)
	auth, portal, client := authURL, portalURL, oauthClientID
	t.Cleanup(func() { authURL, portalURL, oauthClientID = auth, portal, client })
	authURL, portalURL, oauthClientID = server.URL, "https://portal.usestring.ai", "test-client"
	const want = "https://portal.usestring.ai/device?user_code=ABCD-EFGH"

	for _, args := range [][]string{{"login", "--json"}, {"login", "--no-wait"}} {
		env := map[string]string{"HOME": t.TempDir()}
		var stdout, stderr bytes.Buffer
		var opened []string
		status := run(args, runtime{
			env: func(name string) (string, bool) {
				value, ok := env[name]
				return value, ok
			},
			client:  http.DefaultClient,
			stdin:   strings.NewReader(""),
			stdout:  &stdout,
			stderr:  &stderr,
			openURL: func(link string) error { opened = append(opened, link); return nil },
			sleep:   func(time.Duration) {},
		})
		if status != 0 {
			t.Fatalf("%v: status %d, stderr %q", args, status, stderr.String())
		}
		if !reflect.DeepEqual(opened, []string{want}) {
			t.Errorf("%v opened %q, want %q", args, opened, want)
		}
		if args[1] == "--json" {
			started := decode(t, stdout.String()).(map[string]any)
			if fields := slices.Sorted(maps.Keys(started)); !reflect.DeepEqual(fields, []string{"expires_in", "interval", "next", "user_code", "verification_uri_complete"}) {
				t.Errorf("--json fields %q", fields)
			}
			if started["verification_uri_complete"] != want || started["user_code"] != "ABCD-EFGH" {
				t.Errorf("--json printed %q", stdout.String())
			}
		} else if !strings.HasPrefix(stdout.String(), "Open "+want+" and confirm the code ABCD-EFGH.\n") {
			t.Errorf("--no-wait printed %q", stdout.String())
		}
	}
}
