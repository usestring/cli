package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
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
