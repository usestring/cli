package main

import (
	"bytes"
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

// string login --browser signs in without a code. The person opens a link to
// String's /agent-auth page, signs in or creates an account there, and clicks
// Authorize. The link carries a random session id in its fragment and the
// hash of a PKCE verifier in its query. The CLI then asks the portal for the
// key with the session id and the verifier, which only pending.json holds, so
// whoever else sees the link cannot collect the key.
const (
	// browserWindow is how long the CLI waits for the person.
	browserWindow = 15 * time.Minute
	// browserInterval is how often it asks the portal.
	browserInterval = 3 * time.Second
)

var (
	errBrowserUnavailable = &failure{exit: exitAuth, code: "login_unavailable", message: "This String server does not offer string login --browser yet. Use string login."}
	errBrowserExpired     = &failure{exit: exitAuth, code: "login_expired", message: "The sign-in link expired; run string login --browser again"}
)

// codeChallenge is the PKCE S256 challenge for verifier (RFC 7636).
func codeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// startBrowserSignIn makes a session id and a verifier, saves them as pending
// and shows the person the link. wait says whether this command goes on to
// wait.
func startBrowserSignIn(rt runtime, wait bool, envFile string) (pendingLogin, error) {
	path, ok := pendingPath(rt.env)
	if !ok {
		return pendingLogin{}, errors.New("Cannot find your home directory; set HOME")
	}
	session, verifier := make([]byte, 32), make([]byte, 32)
	rand.Read(session)
	rand.Read(verifier)
	pending := pendingLogin{
		SessionID:    hex.EncodeToString(session),
		CodeVerifier: base64.RawURLEncoding.EncodeToString(verifier),
		Interval:     int(browserInterval / time.Second),
		ExpiresAt:    rt.clock().Add(browserWindow),
		EnvFile:      envFile,
	}
	// The portal answers pending for a session nobody has acted on. A 404
	// means it has no such page yet, so the person is not sent to one.
	if status, _, _, _ := askPortal(rt, pending); status == http.StatusNotFound {
		return pendingLogin{}, errBrowserUnavailable
	}
	if err := writePrivate(path, pending); err != nil {
		return pendingLogin{}, err
	}
	link := portalURL + "/agent-auth?" + url.Values{"code_challenge": {codeChallenge(pending.CodeVerifier)}, "source": {"cli"}}.Encode() + "#session_id=" + pending.SessionID
	const open = "Open this link, sign in to String or create an account, and click Authorize:\n"
	var shown []byte
	switch {
	case rt.json:
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		// An agent copies the link as it reads it, so its & stays as it is.
		encoder.SetEscapeHTML(false)
		encoder.Encode(map[string]any{
			"verification_uri_complete": link,
			"expires_in":                int(browserWindow / time.Second),
			"interval":                  pending.Interval,
			"next":                      "string login --poll",
		})
		shown = encoded.Bytes()
	case wait:
		shown = []byte(open + link + "\n")
	default:
		shown = []byte(fmt.Sprintf("%s%s\nThen run string login --poll to save the key. The link expires in %d minutes.\n", open, link, int(browserWindow/time.Minute)))
	}
	if err := write(rt.stdout, shown); err != nil {
		return pendingLogin{}, err
	}
	rt.openLink(link)
	return pending, nil
}

// agentAuthAnswer is the portal's answer about a browser sign-in. The CLI acts
// on status and error; message is a sentence for people.
type agentAuthAnswer struct {
	Status   string `json:"status"`
	APIKey   string `json:"apiKey"`
	TeamName string `json:"teamName"`
	Error    string `json:"error"`
	Message  string `json:"message"`
}

// askPortal asks the portal once whether the person has authorized the
// sign-in.
func askPortal(rt runtime, pending pendingLogin) (int, http.Header, agentAuthAnswer, error) {
	var answer agentAuthAnswer
	body, _ := json.Marshal(map[string]string{"session_id": pending.SessionID, "code_verifier": pending.CodeVerifier})
	request, err := http.NewRequest("POST", portalURL+"/api/agent-auth/status", bytes.NewReader(body))
	if err != nil {
		return 0, nil, answer, err
	}
	request.Header.Set("Content-Type", "application/json")
	status, header, err := oauthCallHeader(rt, request, &answer)
	return status, header, answer, err
}

// browserKey asks the portal every few seconds whether the person has
// authorized the sign-in, until it gives a final answer, the sign-in expires
// or the wait reaches its limit. A busy or unreachable portal is asked again,
// no sooner than its Retry-After says.
func browserKey(w *waiter, pending pendingLogin) (agentAuthAnswer, error) {
	defer w.end()
	defer w.rt.interrupt.note("the sign-in stays open until its link expires", "string login --poll")()
	first := "sign-in · waiting for authorization · the link expires in " + elapsed(pending.ExpiresAt.Sub(w.rt.clock())) + " · resume: string login --poll"
	for wait := browserInterval; ; {
		if w.rt.clock().Add(wait).After(pending.ExpiresAt) {
			return agentAuthAnswer{}, errBrowserExpired
		}
		w.begin(first)
		w.pause(wait)
		wait = browserInterval
		rt, ok := w.next()
		if !ok {
			return agentAuthAnswer{}, w.stoppedBefore(pending.ExpiresAt, "the link", "string login --poll", errBrowserExpired)
		}
		status, header, answer, err := askPortal(rt, pending)
		switch {
		case status == http.StatusNotFound:
			return answer, errBrowserUnavailable
		case status == 0 || status == http.StatusTooManyRequests || status >= 500:
			// A wait longer than the whole window ends the sign-in above.
			if seconds, ok := retryAfterSeconds(header); ok {
				wait = max(wait, time.Duration(min(seconds, int64(browserWindow/time.Second)))*time.Second)
			}
			busy := "no answer from the portal"
			if status != 0 {
				busy = fmt.Sprintf("the portal answered HTTP %d", status)
			}
			w.progress("", busy)
			continue
		case err != nil:
			return answer, &failure{exit: exitAuth, code: "login_failed", status: status, message: "Sign-in failed: " + err.Error()}
		}
		switch answer.Status {
		case "pending":
			w.progress("", "not authorized yet")
		case "complete":
			if answer.APIKey == "" || !validKey(answer.APIKey) {
				return answer, errors.New("The portal did not return a usable API key")
			}
			return answer, nil
		case "denied":
			return answer, errLoginDenied
		case "expired":
			return answer, errBrowserExpired
		default:
			return answer, browserRefusal(status, answer)
		}
	}
}

// browserRefusal is a final answer other than denied or expired. One this CLI
// doesn't know ends the sign-in with what the portal said.
func browserRefusal(status int, answer agentAuthAnswer) *failure {
	again := "run `string login --browser` again"
	switch answer.Error {
	case "invalid_verifier", "invalid_request":
		return &failure{exit: exitAuth, status: status, code: answer.Error, message: "String did not accept this sign-in (" + answer.Error + ")", nextStep: again}
	case "key_limit":
		return &failure{exit: exitAuth, status: status, code: answer.Error, message: "Your organization already has as many API keys as it may have", nextStep: "delete one at https://portal.usestring.ai/api-keys, then " + again}
	}
	said := cmp.Or(answer.Message, answer.Error, answer.Status, fmt.Sprintf("HTTP %d", status))
	return &failure{exit: exitAuth, status: status, code: "login_failed", message: "Sign-in failed: " + said}
}

// finishBrowserSignIn waits for the person to authorize a browser sign-in,
// then saves the key, which the portal hands over only once. The wait's limit
// counts from started.
func finishBrowserSignIn(rt runtime, pending pendingLogin, started time.Time) error {
	pendingFile, _ := pendingPath(rt.env)
	path, ok := credentialsPath(rt.env)
	if !ok {
		return errors.New("Cannot find your home directory; set HOME")
	}
	answer, err := browserKey(newWaiter(rt, started), pending)
	// A final answer spends the session. A wait that reached its limit got
	// none, so string login --poll can still finish the sign-in.
	var stopped *failure
	if !errors.As(err, &stopped) || stopped.exit != exitTimeout {
		os.Remove(pendingFile)
	}
	if err != nil {
		return err
	}
	who := ""
	if answer.TeamName != "" {
		who = " to " + answer.TeamName
	}
	return loggedIn(rt, path, credentials{APIKey: answer.APIKey}, pending.EnvFile, who, map[string]string{"team": answer.TeamName})
}
