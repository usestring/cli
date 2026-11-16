package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// string login signs in with the OAuth device flow. The user approves in a
// browser, the String portal mints a new API key for that sign-in, and the CLI
// saves it. Release builds can point these at another environment with
// -ldflags "-X main.name=value".
var (
	authURL   = "https://clerk.portal.usestring.ai"
	portalURL = "https://portal.usestring.ai"
	// oauthClientID is the CLI's public OAuth client on String's production
	// sign-in. A build that clears it refuses string login, and STRING_API_KEY
	// is then the only way to supply a key.
	oauthClientID = "PsMbudYnIDuliEjP"
)

type credentials struct {
	APIKey string `json:"apiKey"`
	Email  string `json:"email,omitempty"`
}

// credentialsPath is where string login saves the key. ok is false when there
// is no home directory to put it in.
func credentialsPath(env func(string) (string, bool)) (string, bool) {
	home, err := homeDir(env)
	if err != nil {
		return "", false
	}
	return filepath.Join(underConfig("string")(home, env), "credentials.json"), true
}

func loadCredentials(path string) (credentials, error) {
	var saved credentials
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return saved, nil
	}
	if err != nil {
		return saved, err
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		return saved, fmt.Errorf("%s is not valid JSON; delete it with string logout", path)
	}
	return saved, nil
}

func saveCredentials(path string, saved credentials) error {
	return writePrivate(path, saved)
}

// writePrivate writes JSON through a temporary file so a key or device code is
// never in a file that others can read, even briefly or when replacing an
// older copy.
func writePrivate(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".credentials-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func validKey(key string) bool {
	return strings.IndexFunc(key, func(r rune) bool { return r < 0x21 || r > 0x7e }) < 0
}

// apiKey is STRING_API_KEY when it is set, and otherwise the key string login
// saved. With neither it is empty, and fetch and search run keyless.
func apiKey(env func(string) (string, bool)) (string, error) {
	if key, _ := env("STRING_API_KEY"); key != "" {
		if !validKey(key) {
			return "", errors.New("STRING_API_KEY contains characters that are not valid in an API key")
		}
		return key, nil
	}
	path, ok := credentialsPath(env)
	if ok {
		saved, err := loadCredentials(path)
		if err != nil {
			return "", err
		}
		if saved.APIKey != "" {
			if !validKey(saved.APIKey) {
				return "", fmt.Errorf("The key saved in %s is not a valid API key; run string login again", path)
			}
			return saved.APIKey, nil
		}
	}
	return "", nil
}

// noOptions parses a command that takes nothing but --help and prints the help
// when asked. done means the command has nothing left to do.
func noOptions(list []string, command string, rt runtime) (done bool, err error) {
	parsed, err := parseArgs(list)
	if err != nil {
		return true, usage(err)
	}
	if parsed.help {
		return true, write(rt.stdout, []byte(help))
	}
	if err := expectPositionals(parsed.positionals, 0, "string "+command); err != nil {
		return true, usage(err)
	}
	if _, ok := parsed.value("input"); ok {
		return true, usage(fmt.Errorf("--input does not apply to %s", command))
	}
	return false, nil
}

// oauthCall sends one request to the authorization server or the portal and
// decodes the JSON answer into out, whatever the status.
func oauthCall(rt runtime, request *http.Request, out any) (int, error) {
	status, _, err := oauthCallHeader(rt, request, out)
	return status, err
}

// oauthCallHeader is oauthCall that also returns the answer's headers, for a
// caller that reads Retry-After.
func oauthCallHeader(rt runtime, request *http.Request, out any) (int, http.Header, error) {
	request.Header.Set("Accept", "application/json")
	response, err := rt.client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, nil, err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return response.StatusCode, response.Header, fmt.Errorf("HTTP %d from %s: %s", response.StatusCode, request.URL.Host, truncateUTF16(decodeText(data), 2000))
	}
	return response.StatusCode, response.Header, nil
}

func postForm(rt runtime, endpoint string, form url.Values, out any) (int, error) {
	request, err := http.NewRequest("POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return oauthCall(rt, request, out)
}

func getWithToken(rt runtime, endpoint, token string, out any) (int, http.Header, error) {
	request, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	return oauthCallHeader(rt, request, out)
}

// refusalExit is the exit code for a refusal from the sign-in servers, by its
// HTTP status: the same codes an API refusal with that status gets.
func refusalExit(status int) int {
	switch {
	case status == http.StatusTooManyRequests:
		return exitRateLimit
	case status >= 500:
		return exitUpstream
	}
	return exitAuth
}

// deviceToken waits for the user to approve the device code and returns the
// access token, asking no more often than RFC 8628 allows.
func deviceToken(rt runtime, deviceCode string, interval, lifetime time.Duration) (string, error) {
	for waited := time.Duration(0); waited < lifetime; waited += interval {
		rt.sleep(interval)
		var answer struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
		}
		status, err := postForm(rt, authURL+"/oauth/token", url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {deviceCode},
			"client_id":   {oauthClientID},
		}, &answer)
		if err != nil {
			return "", err
		}
		switch answer.Error {
		case "":
			if status == http.StatusOK && answer.AccessToken != "" {
				return answer.AccessToken, nil
			}
			return "", &failure{exit: refusalExit(status), code: "login_failed", message: fmt.Sprintf("Sign-in failed: HTTP %d", status)}
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return "", &failure{exit: exitAuth, code: "login_denied", message: "Sign-in was denied"}
		case "expired_token":
			return "", errLoginExpired
		default:
			return "", &failure{exit: exitAuth, code: "login_failed", message: "Sign-in failed: " + answer.Error}
		}
	}
	return "", errLoginExpired
}

var errLoginExpired = &failure{exit: exitAuth, code: "login_expired", message: "The code expired; run string login again"}

func loginCommand(list []string, rt runtime) error {
	modes := []string{"--with-token", "--no-wait", "--poll"}
	var chosen []string
	list = slices.DeleteFunc(slices.Clone(list), func(arg string) bool {
		if slices.Contains(modes, arg) {
			chosen = append(chosen, arg)
			return true
		}
		return false
	})
	if done, err := noOptions(list, "login", rt); done {
		return err
	}
	if len(chosen) > 1 {
		return usage(errors.New("Use only one of --with-token, --no-wait and --poll"))
	}
	switch {
	case slices.Contains(chosen, "--with-token"):
		return saveToken(rt)
	case slices.Contains(chosen, "--poll"):
		return finishPending(rt)
	case slices.Contains(chosen, "--no-wait") || rt.json || !rt.terminal:
		_, err := startSignIn(rt, false)
		return err
	}
	return signIn(rt)
}

// saveToken saves a key read from stdin, so it never appears in argv or the
// shell history, after checking it with one free API call.
func saveToken(rt runtime) error {
	if rt.terminal {
		return usage(errors.New("Pipe the key on stdin, as in: string login --with-token < key.txt"))
	}
	data, err := io.ReadAll(io.LimitReader(rt.stdin, 4096))
	if err != nil {
		return err
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return usage(errors.New("No key on stdin. Pipe one in, as in: string login --with-token < key.txt"))
	}
	if !validKey(key) {
		return usage(errors.New("The key on stdin contains characters that are not valid in an API key"))
	}
	path, ok := credentialsPath(rt.env)
	if !ok {
		return errors.New("Cannot find your home directory; set HOME")
	}
	base, err := apiBaseURL(rt.env)
	if err != nil {
		return usage(err)
	}
	// Listing sitemap jobs needs a valid key and costs nothing.
	request, err := http.NewRequest("GET", base+"/sitemap?limit=1", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	response, err := rt.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	var report strings.Builder
	switch status := response.StatusCode; {
	case status == http.StatusPaymentRequired:
		report.WriteString("The key works, but its organization is out of credit.\n")
	case status < 200 || status > 299:
		f := httpFailure(response, body, false)
		f.message = "The key was not saved. " + f.message
		return f
	}
	if err := saveCredentials(path, credentials{APIKey: key}); err != nil {
		return err
	}
	fmt.Fprintf(&report, "Saved the key in %s.\n", path)
	if env, _ := rt.env("STRING_API_KEY"); env != "" {
		report.WriteString("STRING_API_KEY is set, and commands keep using it instead.\n")
	}
	return write(rt.stdout, []byte(report.String()))
}

// pendingLogin is a device code waiting for approval. string login saves it so
// string login --poll can finish a sign-in that an earlier command started.
type pendingLogin struct {
	DeviceCode string    `json:"deviceCode"`
	Interval   int       `json:"interval"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

func pendingPath(env func(string) (string, bool)) (string, bool) {
	path, ok := credentialsPath(env)
	return filepath.Join(filepath.Dir(path), "pending.json"), ok
}

// signIn waits in the foreground for the person to approve.
func signIn(rt runtime) error {
	pending, err := startSignIn(rt, true)
	if err != nil {
		return err
	}
	return finishSignIn(rt, pending)
}

// startSignIn asks for a device code, saves it as pending and shows the person
// where to approve it. wait says whether this command goes on to wait.
func startSignIn(rt runtime, wait bool) (pendingLogin, error) {
	if oauthClientID == "" {
		return pendingLogin{}, &failure{exit: exitAuth, code: "login_unavailable", message: "This build of string cannot sign in through the browser. Set STRING_API_KEY, or pipe a key to string login --with-token. Keys are at https://portal.usestring.ai/api-keys."}
	}
	path, ok := pendingPath(rt.env)
	if !ok {
		return pendingLogin{}, errors.New("Cannot find your home directory; set HOME")
	}
	var device struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
		Error                   string `json:"error"`
	}
	status, err := postForm(rt, authURL+"/oauth/device_authorization", url.Values{
		"client_id": {oauthClientID},
		"scope":     {"web_access email"},
	}, &device)
	if err != nil {
		return pendingLogin{}, err
	}
	if status != http.StatusOK || device.DeviceCode == "" {
		return pendingLogin{}, &failure{exit: exitAuth, code: "login_failed", message: strings.TrimSpace(fmt.Sprintf("Sign-in failed: HTTP %d %s", status, device.Error))}
	}
	// Without these from the server, RFC 8628 has clients wait five seconds,
	// and codes commonly live ten minutes.
	if device.Interval <= 0 {
		device.Interval = 5
	}
	if device.ExpiresIn <= 0 {
		device.ExpiresIn = 600
	}
	pending := pendingLogin{
		DeviceCode: device.DeviceCode,
		Interval:   device.Interval,
		ExpiresAt:  rt.clock().Add(time.Duration(device.ExpiresIn) * time.Second),
	}
	if err := writePrivate(path, pending); err != nil {
		return pendingLogin{}, err
	}
	link := device.VerificationURIComplete
	if link == "" {
		link = device.VerificationURI
	}
	var shown []byte
	switch {
	case rt.json:
		shown, _ = json.Marshal(map[string]any{
			"verification_uri_complete": link,
			"user_code":                 device.UserCode,
			"expires_in":                device.ExpiresIn,
			"interval":                  device.Interval,
			"next":                      "string login --poll",
		})
		shown = append(shown, '\n')
	case wait:
		shown = []byte(fmt.Sprintf("Open %s and confirm the code %s.\n", link, device.UserCode))
	default:
		shown = []byte(fmt.Sprintf("Open %s and confirm the code %s.\nThen run string login --poll to save the key. The code expires in %d minutes.\n", link, device.UserCode, device.ExpiresIn/60))
	}
	if err := write(rt.stdout, shown); err != nil {
		return pendingLogin{}, err
	}
	if rt.openURL != nil {
		_ = rt.openURL(link)
	}
	return pending, nil
}

func finishPending(rt runtime) error {
	path, ok := pendingPath(rt.env)
	if !ok {
		return errors.New("Cannot find your home directory; set HOME")
	}
	var pending pendingLogin
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return usage(errors.New("No sign-in is waiting. Run string login --no-wait first."))
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &pending); err != nil || pending.DeviceCode == "" {
		os.Remove(path)
		return usage(errors.New("The waiting sign-in could not be read. Run string login --no-wait again."))
	}
	return finishSignIn(rt, pending)
}

// finishSignIn waits for approval of a pending device code, then fetches and
// saves the key. The pending code is dropped once it can no longer succeed.
func finishSignIn(rt runtime, pending pendingLogin) error {
	pendingFile, _ := pendingPath(rt.env)
	path, ok := credentialsPath(rt.env)
	if !ok {
		return errors.New("Cannot find your home directory; set HOME")
	}
	token, err := deviceToken(rt, pending.DeviceCode, time.Duration(pending.Interval)*time.Second, pending.ExpiresAt.Sub(rt.clock()))
	var refused *failure
	if err == nil || errors.As(err, &refused) {
		os.Remove(pendingFile)
	}
	if err != nil {
		return err
	}
	key, err := releasedKey(rt, token)
	if err != nil {
		return err
	}
	saved := credentials{APIKey: key}
	var profile struct {
		Email string `json:"email"`
	}
	// The email only labels the saved key for string whoami.
	if status, _, err := getWithToken(rt, authURL+"/oauth/userinfo", token, &profile); err == nil && status == http.StatusOK {
		saved.Email = profile.Email
	}
	if err := saveCredentials(path, saved); err != nil {
		return err
	}
	// STRING_API_KEY wins over the key just saved. Someone who signed in to
	// replace a key that stopped working would otherwise keep sending it.
	inUse := ""
	if env, _ := rt.env("STRING_API_KEY"); env != "" {
		inUse = "STRING_API_KEY is set, and commands keep using it instead until you unset it.\n"
	}
	if rt.json {
		// stdout holds only the JSON object, so the note goes to stderr.
		if inUse != "" {
			fmt.Fprint(rt.stderr, inUse)
		}
		shown, _ := json.Marshal(map[string]string{"status": "logged_in", "email": saved.Email, "credentials": path})
		return write(rt.stdout, append(shown, '\n'))
	}
	who := ""
	if saved.Email != "" {
		who = " as " + saved.Email
	}
	return write(rt.stdout, []byte(fmt.Sprintf("Logged in%s. The key is saved in %s.\n%s", who, path, inUse)))
}

// keyPauses are the waits before the second, third and fourth tries of the key
// request. By then the person has approved and the device code is spent, so
// giving up on a busy portal at once would send them back to the browser.
var keyPauses = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

// keyWait is the most those waits may add up to when the portal asks for
// longer ones with Retry-After.
const keyWait = 30 * time.Second

// tokenUnchecked is what the portal's 503 says when it could not check the
// access token, which it does before it creates a key.
const tokenUnchecked = "Could not verify the access token"

// keyByHand is the way to a key that needs no sign-in.
const keyByHand = "set STRING_API_KEY to an API key from https://portal.usestring.ai/api-keys, or pipe one to `string login --with-token`"

// releasedKey asks the portal for the key of an approved sign-in. The portal
// creates a key each time this request succeeds, so it is sent again only
// after an answer that cannot have created one: a 429, which turns the request
// away before it is handled, and the portal's own 503 for a token it could not
// check. Any other failure may follow a key that was created, and is final.
func releasedKey(rt runtime, token string) (string, error) {
	var waited time.Duration
	for try := 0; ; try++ {
		var released struct {
			APIKey string `json:"apiKey"`
			Error  string `json:"error"`
		}
		status, header, err := getWithToken(rt, portalURL+"/api/oauth/api-key", token, &released)
		busy := status == http.StatusTooManyRequests || status >= 500
		switch {
		case status == http.StatusOK && err == nil:
			if released.APIKey == "" || !validKey(released.APIKey) {
				return "", errors.New("The portal did not return a usable API key")
			}
			return released.APIKey, nil
		case err != nil && !busy:
			return "", err
		}
		refusal := &failure{exit: refusalExit(status), code: "key_release_failed", status: status, message: fmt.Sprintf("HTTP %d: %s", status, released.Error), nextStep: keyByHand}
		if err != nil {
			// The answer was not the portal's JSON, as when a gateway answers for it.
			refusal.message = err.Error()
		}
		if busy {
			refusal.nextStep = "run `string login` again later, or " + keyByHand
		}
		if seconds, ok := retryAfterSeconds(header); ok {
			refusal.retryAfter = &seconds
			refusal.notes = append(refusal.notes, fmt.Sprintf("Retry-After: %d", seconds))
		}
		createdNoKey := status == http.StatusTooManyRequests || (err == nil && status == http.StatusServiceUnavailable && released.Error == tokenUnchecked)
		if !createdNoKey || try == len(keyPauses) {
			return "", refusal
		}
		// Ask again no sooner than the portal says, and not at all when that
		// would take the waits past keyWait.
		wait := keyPauses[try]
		if asked := refusal.retryAfter; asked != nil {
			if *asked > int64(keyWait/time.Second) {
				return "", refusal
			}
			wait = max(wait, time.Duration(*asked)*time.Second)
		}
		if waited+wait > keyWait {
			return "", refusal
		}
		if try == 0 && rt.progress {
			fmt.Fprintln(rt.stderr, "Approved. Waiting for String to hand over the key...")
		}
		rt.pause(wait)
		waited += wait
	}
}

// retryAfterSeconds is an answer's Retry-After header, when it gives whole
// seconds.
func retryAfterSeconds(header http.Header) (int64, bool) {
	seconds, err := strconv.ParseInt(strings.TrimSpace(header.Get("Retry-After")), 10, 64)
	return seconds, err == nil && seconds >= 0
}

func logoutCommand(list []string, rt runtime) error {
	if done, err := noOptions(list, "logout", rt); done {
		return err
	}
	path, ok := credentialsPath(rt.env)
	if !ok {
		return errors.New("Cannot find your home directory; set HOME")
	}
	var report strings.Builder
	switch err := os.Remove(path); {
	case errors.Is(err, fs.ErrNotExist):
		report.WriteString("No key is saved.\n")
	case err != nil:
		return err
	default:
		fmt.Fprintf(&report, "Deleted the key saved in %s. The key itself still works until you delete it in https://portal.usestring.ai/api-keys.\n", path)
	}
	if key, _ := rt.env("STRING_API_KEY"); key != "" {
		report.WriteString("STRING_API_KEY is still set, and commands keep using it.\n")
	}
	return write(rt.stdout, []byte(report.String()))
}

func whoamiCommand(list []string, rt runtime) error {
	if done, err := noOptions(list, "whoami", rt); done {
		return err
	}
	path, ok := credentialsPath(rt.env)
	if key, _ := rt.env("STRING_API_KEY"); key != "" {
		if saved, err := loadCredentials(path); ok && err == nil && saved.APIKey != "" {
			return write(rt.stdout, []byte(fmt.Sprintf("Using STRING_API_KEY from the environment. It wins over the key saved in %s.\n", path)))
		}
		return write(rt.stdout, []byte("Using STRING_API_KEY from the environment.\n"))
	}
	if ok {
		saved, err := loadCredentials(path)
		if err != nil {
			return err
		}
		if saved.APIKey != "" && saved.Email != "" {
			return write(rt.stdout, []byte(fmt.Sprintf("Logged in as %s, with the key saved in %s.\n", saved.Email, path)))
		}
		if saved.APIKey != "" {
			return write(rt.stdout, []byte(fmt.Sprintf("Using the key saved in %s.\n", path)))
		}
	}
	return &failure{
		exit:     exitAuth,
		code:     "not_logged_in",
		message:  "Not logged in. string fetch and string search run keyless, within a daily allowance.",
		nextStep: needsKey,
		helpURL:  helpURL,
	}
}
