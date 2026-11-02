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
	"strings"
	"time"
)

// string login signs in with the OAuth device flow. The user approves in a
// browser and picks an API key on the String portal's consent page; the portal
// then releases that key to this client, and the CLI saves it. Release builds
// can point these at another environment with -ldflags "-X main.name=value".
var (
	authURL   = "https://clerk.portal.usestring.ai"
	portalURL = "https://portal.usestring.ai"
	// oauthClientID is the CLI's OAuth client. Without one, string login
	// refuses and STRING_API_KEY is the only way to supply a key.
	oauthClientID = ""
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

// saveCredentials writes through a temporary file so the key is never in a
// file that others can read, even briefly or when replacing an older copy.
func saveCredentials(path string, saved credentials) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(saved, "", "  ")
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
// saved.
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
	return "", errors.New("Set STRING_API_KEY to a String Web Access API key, or run string login")
}

// noOptions parses a command that takes nothing but --help and prints the help
// when asked. done means the command has nothing left to do.
func noOptions(list []string, command string, rt runtime) (done bool, err error) {
	parsed, err := parseArgs(list)
	if err != nil {
		return true, err
	}
	if parsed.help {
		return true, write(rt.stdout, []byte(help))
	}
	if err := expectPositionals(parsed.positionals, 0, "string "+command); err != nil {
		return true, err
	}
	if _, ok := parsed.value("input"); ok {
		return true, fmt.Errorf("--input does not apply to %s", command)
	}
	return false, nil
}

// oauthCall sends one request to the authorization server or the portal and
// decodes the JSON answer into out, whatever the status.
func oauthCall(rt runtime, request *http.Request, out any) (int, error) {
	request.Header.Set("Accept", "application/json")
	response, err := rt.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return response.StatusCode, fmt.Errorf("HTTP %d from %s: %s", response.StatusCode, request.URL.Host, truncateUTF16(decodeText(data), 2000))
	}
	return response.StatusCode, nil
}

func postForm(rt runtime, endpoint string, form url.Values, out any) (int, error) {
	request, err := http.NewRequest("POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return oauthCall(rt, request, out)
}

func getWithToken(rt runtime, endpoint, token string, out any) (int, error) {
	request, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	return oauthCall(rt, request, out)
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
			return "", fmt.Errorf("Sign-in failed: HTTP %d", status)
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return "", errors.New("Sign-in was denied")
		case "expired_token":
			return "", errors.New("The code expired; run string login again")
		default:
			return "", fmt.Errorf("Sign-in failed: %s", answer.Error)
		}
	}
	return "", errors.New("The code expired; run string login again")
}

func loginCommand(list []string, rt runtime) error {
	if done, err := noOptions(list, "login", rt); done {
		return err
	}
	if oauthClientID == "" {
		return errors.New("This build of string cannot sign in. Set STRING_API_KEY to an API key from https://portal.usestring.ai/settings.")
	}
	path, ok := credentialsPath(rt.env)
	if !ok {
		return errors.New("Cannot find your home directory; set HOME")
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
		return err
	}
	if status != http.StatusOK || device.DeviceCode == "" {
		return errors.New(strings.TrimSpace(fmt.Sprintf("Sign-in failed: HTTP %d %s", status, device.Error)))
	}
	link := device.VerificationURIComplete
	if link == "" {
		link = device.VerificationURI
	}
	if err := write(rt.stdout, []byte(fmt.Sprintf("Open %s and confirm the code %s, then choose the API key to use.\n", link, device.UserCode))); err != nil {
		return err
	}
	_ = rt.openURL(link)
	// Without an interval from the server, RFC 8628 has clients wait five seconds.
	interval := 5 * time.Second
	if device.Interval > 0 {
		interval = time.Duration(device.Interval) * time.Second
	}
	lifetime := 10 * time.Minute
	if device.ExpiresIn > 0 {
		lifetime = time.Duration(device.ExpiresIn) * time.Second
	}
	token, err := deviceToken(rt, device.DeviceCode, interval, lifetime)
	if err != nil {
		return err
	}
	var released struct {
		APIKey string `json:"apiKey"`
		Error  string `json:"error"`
	}
	status, err = getWithToken(rt, portalURL+"/api/oauth/api-key", token, &released)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", status, released.Error)
	}
	if released.APIKey == "" || !validKey(released.APIKey) {
		return errors.New("The portal did not return a usable API key")
	}
	saved := credentials{APIKey: released.APIKey}
	var profile struct {
		Email string `json:"email"`
	}
	// The email only labels the saved key for string whoami.
	if status, err := getWithToken(rt, authURL+"/oauth/userinfo", token, &profile); err == nil && status == http.StatusOK {
		saved.Email = profile.Email
	}
	if err := saveCredentials(path, saved); err != nil {
		return err
	}
	who := ""
	if saved.Email != "" {
		who = " as " + saved.Email
	}
	return write(rt.stdout, []byte(fmt.Sprintf("Logged in%s. The key is saved in %s.\n", who, path)))
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
		fmt.Fprintf(&report, "Deleted the key saved in %s. The key itself still works until you delete it in https://portal.usestring.ai/settings.\n", path)
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
	if key, _ := rt.env("STRING_API_KEY"); key != "" {
		return write(rt.stdout, []byte("Using STRING_API_KEY from the environment.\n"))
	}
	if path, ok := credentialsPath(rt.env); ok {
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
	return errors.New("Not logged in. Run string login, or set STRING_API_KEY.")
}
