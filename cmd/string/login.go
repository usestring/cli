package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
	return replaceFile(path, append(data, '\n'), 0o600)
}

// replaceFile writes data to path through a temporary file beside it, which
// only its owner can read until it takes mode, so a reader never sees the file
// half written.
func replaceFile(path string, data []byte, mode fs.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if mode != 0o600 {
		if err := file.Chmod(mode); err != nil {
			file.Close()
			return err
		}
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// envFileOption is --env-file as an absolute path, so the command that
// finishes a sign-in writes the same file from any directory. The path is
// checked before the sign-in starts: once the person has approved, a file that
// can't be written would leave the key only in credentials.json.
func envFileOption(parsed args) (string, error) {
	value, ok := parsed.value("env-file")
	if !ok {
		return "", nil
	}
	if value == "" {
		return "", errors.New("--env-file needs the path of a file, such as .env")
	}
	path, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return "", fmt.Errorf("--env-file %s is a directory; name a file in it, such as %s", value, filepath.Join(value, ".env"))
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		return "", fmt.Errorf("--env-file %s is in a directory that does not exist", value)
	}
	return path, nil
}

// envKeyLine matches a line of an env file that sets STRING_API_KEY, with or
// without the export that lets a shell source the file.
var envKeyLine = regexp.MustCompile(`^(\s*(?:export\s+)?)STRING_API_KEY\s*=`)

// setEnvKey makes key the value of every line of the env file at path that
// sets STRING_API_KEY, or adds that line, and leaves every other line as it
// was. Every line is set because tools differ on which of two lines wins. A
// new file is readable only by its owner; an existing one keeps its mode.
func setEnvKey(path, key string) error {
	// Writing through a symlink keeps the link, and every file that shares it.
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	mode := fs.FileMode(0o600)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		mode = info.Mode().Perm()
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	lines := strings.SplitAfter(string(data), "\n")
	set := false
	for i, line := range lines {
		text := strings.TrimRight(line, "\r\n")
		if prefix := envKeyLine.FindStringSubmatch(text); prefix != nil {
			lines[i] = prefix[1] + "STRING_API_KEY=" + key + line[len(text):]
			set = true
		}
	}
	content := strings.Join(lines, "")
	if !set {
		newline := "\n"
		if strings.Contains(content, "\r\n") {
			newline = "\r\n"
		}
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += newline
		}
		content += "STRING_API_KEY=" + key + newline
	}
	return replaceFile(path, []byte(content), mode)
}

// keepKey saves a sign-in's key in credentials.json at path, where commands
// read it, and in envFile when --env-file named one.
func keepKey(path string, saved credentials, envFile string) error {
	if err := saveCredentials(path, saved); err != nil {
		return err
	}
	if envFile == "" {
		return nil
	}
	if err := setEnvKey(envFile, saved.APIKey); err != nil {
		return fmt.Errorf("The key is saved in %s, but it could not be written to %s: %w", path, envFile, err)
	}
	return nil
}

// envFileNote is the line that says where --env-file put the key.
func envFileNote(envFile string) string {
	if envFile == "" {
		return ""
	}
	return fmt.Sprintf("Wrote it to %s as STRING_API_KEY.\n", envFile)
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
		return true, write(rt.stdout, []byte(helpFor(command)))
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
	response, err := rt.send(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, nil, err
	}
	if err := json.Unmarshal(data, out); err != nil {
		said := fmt.Sprintf("HTTP %d from %s", response.StatusCode, request.URL.Host)
		if detail := bodyText(data); detail != "" {
			said += ": " + detail
		}
		return response.StatusCode, response.Header, errors.New(said)
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
// access token, asking no more often than RFC 8628 allows. It stops at the
// wait's limit too, and then string login --poll goes on with the same code.
func deviceToken(w *waiter, deviceCode string, interval time.Duration, expires time.Time) (string, error) {
	defer w.end()
	resume, expired := w.rt.suggested("login", "--poll"), loginExpired(w.rt)
	defer w.rt.interrupt.note("the sign-in stays open until its code expires", resume)()
	lifetime := expires.Sub(w.rt.clock())
	first := "sign-in · waiting for approval · the code expires in " + elapsed(lifetime) + " · resume: " + resume
	for waited := time.Duration(0); waited < lifetime; waited += interval {
		w.begin(first)
		w.pause(interval)
		rt, ok := w.next()
		if !ok {
			return "", w.stoppedBefore(expires, "the code", resume, expired)
		}
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
			if w.over() {
				return "", w.stoppedBefore(expires, "the code", resume, expired)
			}
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
			return "", errLoginDenied
		case "expired_token":
			return "", expired
		default:
			return "", &failure{exit: exitAuth, code: "login_failed", message: "Sign-in failed: " + answer.Error}
		}
		w.progress("", "not approved yet")
	}
	return "", expired
}

var errLoginDenied = &failure{exit: exitAuth, code: "login_denied", message: "Sign-in was denied"}

// loginExpired is the failure for a device code that expired, which a new
// sign-in replaces: string login, with the global switches given.
func loginExpired(rt runtime) *failure {
	return &failure{exit: exitAuth, code: "login_expired", message: "The code expired; run " + rt.suggested("login") + " again"}
}

func loginCommand(list []string, rt runtime) error {
	modes := []string{"--with-token", "--no-wait", "--poll"}
	var chosen []string
	list = slices.DeleteFunc(slices.Clone(list), func(arg string) bool {
		if mode, ok := flagNamed(arg, modes); ok {
			chosen = append(chosen, mode)
			return true
		}
		return false
	})
	parsed, err := parse(list, spec{options: []string{"env-file", "device-name"}, switches: []string{"browser"}})
	if err != nil {
		return usage(err)
	}
	if parsed.help {
		return write(rt.stdout, []byte(helpFor("login")))
	}
	if err := expectPositionals(parsed.positionals, 0, "string login [--with-token | --no-wait | --poll] [--browser] [--env-file FILE] [--device-name NAME]"); err != nil {
		return usage(err)
	}
	if _, ok := parsed.value("input"); ok {
		return usage(errors.New("--input does not apply to login"))
	}
	if len(chosen) > 1 {
		return usage(errors.New("Use only one of --with-token, --no-wait and --poll"))
	}
	browser := parsed.switches["browser"]
	if browser && slices.Contains(chosen, "--with-token") {
		return usage(errors.New("--browser does not apply to --with-token"))
	}
	envFile, err := envFileOption(parsed)
	if err != nil {
		return usage(err)
	}
	deviceName, err := deviceNameOption(parsed)
	if err != nil {
		return usage(err)
	}
	if browser && deviceName != "" {
		return usage(errors.New("--device-name does not apply to --browser"))
	}
	switch {
	case slices.Contains(chosen, "--with-token"):
		return saveToken(rt, envFile)
	case slices.Contains(chosen, "--poll"):
		return finishPending(rt, envFile, deviceName)
	case slices.Contains(chosen, "--no-wait") || rt.json || !rt.exec.interactive():
		_, err := startSignIn(rt, browser, false, envFile, deviceName)
		return err
	}
	return signIn(rt, browser, envFile, deviceName)
}

// saveToken saves a key read from stdin, so it never appears in argv or the
// shell history, after checking it with one free API call.
func saveToken(rt runtime, envFile string) error {
	if rt.exec.stdinTTY {
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
	response, err := rt.send(request)
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
	if err := keepKey(path, credentials{APIKey: key}, envFile); err != nil {
		return err
	}
	fmt.Fprintf(&report, "Saved the key in %s.\n%s", path, envFileNote(envFile))
	if env, _ := rt.env("STRING_API_KEY"); env != "" {
		report.WriteString("STRING_API_KEY is set, and commands keep using it instead.\n")
	}
	return write(rt.stdout, []byte(report.String()))
}

// pendingLogin is a sign-in waiting for approval: a device code, or a browser
// sign-in's session id and verifier. string login saves it so string login
// --poll can finish a sign-in that an earlier command started.
type pendingLogin struct {
	DeviceCode   string    `json:"deviceCode,omitempty"`
	SessionID    string    `json:"sessionId,omitempty"`
	CodeVerifier string    `json:"codeVerifier,omitempty"`
	Interval     int       `json:"interval"`
	ExpiresAt    time.Time `json:"expiresAt"`
	// EnvFile is where --env-file asked for the key when the sign-in started.
	EnvFile string `json:"envFile,omitempty"`
	// DeviceName is what the key request calls this machine, kept so that
	// string login --poll sends the name the sign-in started with.
	DeviceName string `json:"deviceName,omitempty"`
}

func pendingPath(env func(string) (string, bool)) (string, bool) {
	path, ok := credentialsPath(env)
	return filepath.Join(filepath.Dir(path), "pending.json"), ok
}

// signIn waits in the foreground for the person to approve. browser picks
// the sign-in of string login --browser over a device code. deviceName is
// --device-name, or "" for this machine's default name.
func signIn(rt runtime, browser bool, envFile, deviceName string) error {
	started := rt.clock()
	pending, err := startSignIn(rt, browser, true, envFile, deviceName)
	if err != nil {
		return err
	}
	return finishSignIn(rt, pending, started)
}

// startSignIn asks for a device code, or with browser makes a browser
// sign-in, saves it as pending and shows the person where to approve it. wait
// says whether this command goes on to wait.
func startSignIn(rt runtime, browser, wait bool, envFile, deviceName string) (pendingLogin, error) {
	// The browser sign-in needs no OAuth client, but a build that clears it
	// signs nobody in, and its tests rely on that.
	if oauthClientID == "" {
		return pendingLogin{}, &failure{exit: exitAuth, code: "login_unavailable", message: "This build of string cannot sign in through the browser. Set STRING_API_KEY, or pipe a key to string login --with-token. Keys are at https://portal.usestring.ai/api-keys."}
	}
	if browser {
		return startBrowserSignIn(rt, wait, envFile)
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
		// The email labels the saved key. With web_access in the request too,
		// the sign-in of an account that has never granted it stays pending
		// after the person approves, until the code expires.
		"scope": {"email"},
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
		EnvFile:    envFile,
		DeviceName: cmp.Or(deviceName, rt.deviceName()),
	}
	if err := writePrivate(path, pending); err != nil {
		return pendingLogin{}, err
	}
	link := device.VerificationURIComplete
	if link == "" {
		link = device.VerificationURI
	}
	link = approvalLink(link, device.UserCode)
	var shown []byte
	poll := rt.suggested("login", "--poll")
	switch {
	case rt.json:
		shown, _ = json.Marshal(map[string]any{
			"verification_uri_complete": link,
			"user_code":                 device.UserCode,
			"expires_in":                device.ExpiresIn,
			"interval":                  device.Interval,
			"next":                      poll,
		})
		shown = append(shown, '\n')
	case wait:
		shown = []byte(fmt.Sprintf("Open %s and confirm the code %s.\n", link, device.UserCode))
	default:
		shown = []byte(fmt.Sprintf("Open %s and confirm the code %s.\nThen run %s to save the key. The code expires in %d minutes.\n", link, device.UserCode, poll, device.ExpiresIn/60))
	}
	if err := write(rt.stdout, shown); err != nil {
		return pendingLogin{}, err
	}
	rt.openLink(link)
	return pending, nil
}

// approvalLink sends people to String's approval page; the sign-in service links its hosted page, where some approvals stay pending.
func approvalLink(sent, userCode string) string {
	parsed, err := url.Parse(sent)
	if err != nil || userCode == "" || !strings.EqualFold(parsed.Host, "accounts.portal.usestring.ai") || parsed.Path != "/device" {
		return sent
	}
	return portalURL + "/device?user_code=" + url.QueryEscape(userCode)
}

// finishPending finishes the sign-in an earlier command started. envFile and
// deviceName, from --env-file and --device-name now, win over the ones given
// when it started.
func finishPending(rt runtime, envFile, deviceName string) error {
	started := rt.clock()
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
	if err := json.Unmarshal(data, &pending); err != nil || (pending.DeviceCode == "" && pending.SessionID == "") {
		os.Remove(path)
		return usage(errors.New("The waiting sign-in could not be read. Run string login --no-wait again."))
	}
	if envFile != "" {
		pending.EnvFile = envFile
	}
	if deviceName != "" {
		pending.DeviceName = deviceName
	}
	return finishSignIn(rt, pending, started)
}

// finishSignIn waits for approval of a pending sign-in, then fetches and saves
// the key. A device code is dropped once it can no longer succeed. The wait's
// limit counts from started, when the command began.
func finishSignIn(rt runtime, pending pendingLogin, started time.Time) error {
	if pending.SessionID != "" {
		return finishBrowserSignIn(rt, pending, started)
	}
	pendingFile, _ := pendingPath(rt.env)
	path, ok := credentialsPath(rt.env)
	if !ok {
		return errors.New("Cannot find your home directory; set HOME")
	}
	token, err := deviceToken(newWaiter(rt, started), pending.DeviceCode, time.Duration(pending.Interval)*time.Second, pending.ExpiresAt)
	// A busy sign-in server, one that didn't answer in time, or a wait that
	// reached its limit has not spent the code, so the code stays saved and
	// the failure says that string login --poll can still finish with it.
	var refused *failure
	if err == nil || (errors.As(err, &refused) && refused.exit != exitRateLimit && refused.exit != exitUpstream && refused.exit != exitTimeout) {
		os.Remove(pendingFile)
	} else if refused != nil {
		refused.nextStep = "run `" + rt.suggested("login", "--poll") + "` to finish this sign-in with the same code"
	}
	if err != nil {
		return err
	}
	// A sign-in that a release from before the name started has none saved.
	key, err := releasedKey(rt, token, cmp.Or(pending.DeviceName, rt.deviceName()))
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
	who := ""
	if saved.Email != "" {
		who = " as " + saved.Email
	}
	return loggedIn(rt, path, saved, pending.EnvFile, who, map[string]string{"email": saved.Email})
}

// loggedIn saves the key a sign-in returned and says so. who names the account
// in the message, as in " as dev@localhost", and fields name it in the JSON.
func loggedIn(rt runtime, path string, saved credentials, envFile, who string, fields map[string]string) error {
	if err := keepKey(path, saved, envFile); err != nil {
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
		fields["status"], fields["credentials"] = "logged_in", path
		if envFile != "" {
			fields["env_file"] = envFile
		}
		shown, _ := json.Marshal(fields)
		return write(rt.stdout, append(shown, '\n'))
	}
	return write(rt.stdout, []byte(fmt.Sprintf("Logged in%s. The key is saved in %s.\n%s%s", who, path, envFileNote(envFile), inUse)))
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
// deviceName goes with the request, and the portal names the key after it.
func releasedKey(rt runtime, token, deviceName string) (string, error) {
	var waited time.Duration
	for try := 0; ; try++ {
		var released struct {
			APIKey string `json:"apiKey"`
			Error  string `json:"error"`
		}
		request, err := http.NewRequest("GET", portalURL+"/api/oauth/api-key", nil)
		if err != nil {
			return "", err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set(deviceNameHeader, encodeDeviceName(deviceName))
		status, header, err := oauthCallHeader(rt, request, &released)
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
			refusal.nextStep = "run `" + rt.suggested("login") + "` again later, or " + keyByHand
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
		if try == 0 && rt.exec.stderrTTY {
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
	parsed, err := parse(list, spec{switches: []string{"offline"}})
	if err != nil {
		return usage(err)
	}
	if parsed.help {
		return write(rt.stdout, []byte(helpFor("whoami")))
	}
	if err := expectPositionals(parsed.positionals, 0, "string whoami [--offline]"); err != nil {
		return usage(err)
	}
	if _, ok := parsed.value("input"); ok {
		return usage(errors.New("--input does not apply to whoami"))
	}
	path, ok := credentialsPath(rt.env)
	var saved credentials
	if ok {
		if saved, err = loadCredentials(path); err != nil {
			return err
		}
	}
	env, _ := rt.env("STRING_API_KEY")
	key, from := saved.APIKey, "the key saved in "+path
	said := fmt.Sprintf("Using the key saved in %s", path)
	switch {
	case env != "" && saved.APIKey != "":
		key, from, said = env, "the key in STRING_API_KEY", fmt.Sprintf("Using STRING_API_KEY from the environment. It wins over the key saved in %s", path)
	case env != "":
		key, from, said = env, "the key in STRING_API_KEY", "Using STRING_API_KEY from the environment"
	case saved.APIKey != "" && saved.Email != "":
		said = fmt.Sprintf("Logged in as %s, with the key saved in %s", saved.Email, path)
	case saved.APIKey == "":
		return &failure{
			exit:     exitAuth,
			code:     "not_logged_in",
			message:  "Not logged in. string fetch and string search run keyless, within a daily allowance.",
			nextStep: needsKey,
			helpURL:  helpURL,
		}
	}
	source := "saved"
	if key == env {
		source = "STRING_API_KEY"
	}
	shown := map[string]any{"source": source, "credentials": path, "email": saved.Email, "key_prefix": keyPrefix(key)}
	if parsed.switches["offline"] {
		// Nothing asked String, so the object says nothing of accepted.
		if rt.json {
			encoded, _ := json.Marshal(shown)
			return write(rt.stdout, append(encoded, '\n'))
		}
		return write(rt.stdout, []byte(said+".\n"))
	}
	// One request, which spends no credit, says whether String takes the key.
	base, err := apiBaseURL(rt.env)
	if err != nil {
		return usage(err)
	}
	if _, err := (apiClient{rt: rt, base: base, key: key}).call(&operation{method: "GET", path: "/account/balance"}); err != nil {
		if refused := asFailure(err); refused.exit == exitAuth {
			next := "run `string login` to save a new key"
			if from == "the key in STRING_API_KEY" {
				next = "check STRING_API_KEY, or unset it and run `string login`"
			}
			return &failure{exit: exitAuth, code: "key_refused", status: refused.status, requestID: refused.requestID, message: "String refused " + from + " (" + keyPrefix(key) + ").", nextStep: next}
		}
		yes := true
		return &failure{exit: exitUpstream, code: "unverified", retryable: &yes, message: said + " (" + keyPrefix(key) + "), but string couldn't check it with String.", notes: []string{strings.TrimPrefix(asFailure(err).Error(), "error: ")}, nextStep: "run it again, or `" + rt.suggested("whoami", "--offline") + "` to see the key without checking it"}
	}
	if rt.json {
		shown["accepted"] = true
		encoded, _ := json.Marshal(shown)
		return write(rt.stdout, append(encoded, '\n'))
	}
	return write(rt.stdout, []byte(said+" ("+keyPrefix(key)+"). String accepts it.\n"))
}

// keyPrefix is the start of key, enough to tell keys apart and never more
// than a third of it.
func keyPrefix(key string) string {
	return key[:min(8, len(key)/3)] + "…"
}
