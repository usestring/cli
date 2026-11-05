package main

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/bits"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

// string trial gets an agent a short-lived key with a small balance, with no
// account and nobody present. The portal asks for a proof of work first, so
// each key costs a few seconds of CPU.

// maxTrialDifficulty bounds the work a challenge can ask for, so a misbehaving
// server can't keep the CLI hashing for hours. The portal asks for 22 bits.
const maxTrialDifficulty = 28

// claimGrantType is the grant the portal's token endpoint takes for a claimed
// key. The specification the portal follows names it.
const claimGrantType = "urn:workos:agent-auth:grant-type:claim"

// trialRegistration is what string trial keeps beside the key: the claim token
// a person needs to adopt the agent once the key runs out.
type trialRegistration struct {
	RegistrationID    string    `json:"registrationId"`
	KeyExpiresAt      time.Time `json:"keyExpiresAt"`
	ClaimURL          string    `json:"claimUrl"`
	ClaimToken        string    `json:"claimToken"`
	ClaimTokenExpires time.Time `json:"claimTokenExpires"`
	// The claim string trial claim started last, which --poll waits on.
	ClaimEmail       string    `json:"claimEmail,omitempty"`
	ClaimCodeExpires time.Time `json:"claimCodeExpires,omitzero"`
	ClaimInterval    int       `json:"claimInterval,omitempty"`
}

// portalRefusal is the error half of every answer from the portal's agent
// sign-up endpoints. Rate limits carry message instead of error_description.
type portalRefusal struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	Message          string `json:"message"`
}

type proofOfWork struct {
	Challenge  string `json:"challenge"`
	Difficulty int    `json:"difficulty"`
	Algorithm  string `json:"algorithm"`
}

type trialAnswer struct {
	RegistrationID     string      `json:"registration_id"`
	AccessToken        string      `json:"access_token"`
	AccessTokenExpires time.Time   `json:"access_token_expires"`
	BalanceUSD         string      `json:"balance_usd"`
	ClaimURL           string      `json:"claim_url"`
	ClaimToken         string      `json:"claim_token"`
	ClaimTokenExpires  time.Time   `json:"claim_token_expires"`
	ProofOfWork        proofOfWork `json:"proof_of_work"`
	portalRefusal
}

func trialPath(env func(string) (string, bool)) (string, bool) {
	path, ok := credentialsPath(env)
	return filepath.Join(filepath.Dir(path), "trial.json"), ok
}

func trialCommand(list []string, rt runtime) error {
	if len(list) > 0 && list[0] == "claim" {
		return trialClaimCommand(list[1:], rt)
	}
	if done, err := noOptions(list, "trial", rt); done {
		return err
	}
	if key, _ := rt.env("STRING_API_KEY"); key != "" {
		return usage(errors.New("STRING_API_KEY is set, and commands would keep using it instead of a trial key. Unset it to register one."))
	}
	path, ok := credentialsPath(rt.env)
	if !ok {
		return errors.New("Cannot find your home directory; set HOME")
	}
	saved, err := loadCredentials(path)
	if err != nil {
		return err
	}
	if saved.APIKey != "" {
		return usage(fmt.Errorf("A key is already saved in %s. Run string logout first to replace it with a trial key.", path))
	}
	registrationFile, _ := trialPath(rt.env)

	endpoint := portalURL + "/api/agent-signup/identity"
	request := map[string]any{"type": "anonymous"}
	var answer trialAnswer
	status, err := postJSON(rt, endpoint, request, &answer)
	// A challenge can expire or be spent while it's solved, so one fresh
	// challenge is worth a second try.
	for attempt := 0; err == nil && status == http.StatusUnauthorized && answer.Error == "proof_of_work_required" && attempt < 2; attempt++ {
		nonce, solveErr := solveProofOfWork(rt, answer.ProofOfWork)
		if solveErr != nil {
			return solveErr
		}
		request["proof"] = map[string]string{"challenge": answer.ProofOfWork.Challenge, "nonce": nonce}
		answer = trialAnswer{}
		status, err = postJSON(rt, endpoint, request, &answer)
	}
	if notOffered(status, err, answer.portalRefusal) {
		return trialUnavailable(status)
	}
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return trialFailure(status, answer.portalRefusal, "Trial registration")
	}
	if answer.AccessToken == "" || !validKey(answer.AccessToken) || answer.ClaimToken == "" {
		return errors.New("The portal did not return a usable trial key")
	}

	registration := trialRegistration{
		RegistrationID:    answer.RegistrationID,
		KeyExpiresAt:      answer.AccessTokenExpires,
		ClaimURL:          answer.ClaimURL,
		ClaimToken:        answer.ClaimToken,
		ClaimTokenExpires: answer.ClaimTokenExpires,
	}
	if err := writePrivate(registrationFile, registration); err != nil {
		return err
	}
	if err := saveCredentials(path, credentials{APIKey: answer.AccessToken}); err != nil {
		return err
	}
	if rt.json {
		shown, _ := json.Marshal(map[string]any{
			"status":              "registered",
			"balance_usd":         answer.BalanceUSD,
			"expires_at":          answer.AccessTokenExpires,
			"credentials":         path,
			"claim_token_expires": answer.ClaimTokenExpires,
			"trial":               registrationFile,
		})
		return write(rt.stdout, append(shown, '\n'))
	}
	return write(rt.stdout, []byte(fmt.Sprintf(
		"Registered a trial key with $%s of credit. It works until %s and is saved in %s.\nA person can claim this agent onto their String account until %s. The claim token is saved in %s.\n",
		answer.BalanceUSD, answer.AccessTokenExpires.UTC().Format(time.RFC1123), path, answer.ClaimTokenExpires.UTC().Format(time.RFC1123), registrationFile)))
}

// notOffered reports an answer from a portal that doesn't serve the agent
// sign-up endpoints: a 404, or a bare 403 from the cross-origin check in front
// of its API routes.
func notOffered(status int, err error, refusal portalRefusal) bool {
	return status == http.StatusNotFound || (err == nil && status == http.StatusForbidden && refusal.ErrorDescription == "")
}

func trialUnavailable(status int) *failure {
	return &failure{exit: exitAuth, code: "trial_unavailable", status: status, message: "This String server does not offer trial keys.", nextStep: needsKey}
}

// trialFailure gives a refusal from the portal its exit code. step names what
// failed, for a refusal that doesn't say.
func trialFailure(status int, refusal portalRefusal, step string) *failure {
	message := cmp.Or(refusal.ErrorDescription, refusal.Message, fmt.Sprintf("%s failed: HTTP %d %s", step, status, refusal.Error))
	f := &failure{exit: exitFailure, code: refusal.Error, status: status, message: message}
	switch {
	case refusal.Error == "anonymous_not_enabled":
		f.exit, f.code, f.nextStep = exitAuth, "trial_unavailable", needsKey
	case status == http.StatusTooManyRequests:
		f.exit = exitRateLimit
	case status >= 500:
		f.exit = exitUpstream
	}
	if f.code == "" {
		f.code = "trial_failed"
	}
	return f
}

// trialClaimCommand lets a person adopt the agent. It shows them a code to
// enter in the portal, then saves the key the portal makes on their account
// in place of the trial key, which the claim revokes.
func trialClaimCommand(list []string, rt runtime) error {
	modes := []string{"--no-wait", "--poll"}
	var chosen []string
	list = slices.DeleteFunc(slices.Clone(list), func(arg string) bool {
		if slices.Contains(modes, arg) {
			chosen = append(chosen, arg)
			return true
		}
		return false
	})
	parsed, err := parseArgs(list, "email")
	if err != nil {
		return usage(err)
	}
	if parsed.help {
		return write(rt.stdout, []byte(help))
	}
	email, _ := parsed.value("email")
	_, input := parsed.value("input")
	poll := slices.Contains(chosen, "--poll")
	if len(chosen) > 1 || len(parsed.positionals) > 0 || input || (poll && email != "") || (!poll && email == "") {
		return usage(errors.New("Usage: string trial claim --email ADDRESS [--no-wait], then string trial claim --poll"))
	}
	registrationFile, registration, err := loadTrialRegistration(rt.env)
	if err != nil {
		return err
	}
	if poll {
		if registration.ClaimEmail == "" {
			return usage(errors.New("No claim is waiting. Run string trial claim --email ADDRESS first."))
		}
		return collectClaimedKey(rt, registrationFile, registration)
	}
	wait := !slices.Contains(chosen, "--no-wait") && !rt.json && rt.terminal
	registration, err = startClaim(rt, registrationFile, registration, email, wait)
	if err != nil || !wait {
		return err
	}
	return collectClaimedKey(rt, registrationFile, registration)
}

func loadTrialRegistration(env func(string) (string, bool)) (string, trialRegistration, error) {
	var registration trialRegistration
	path, ok := trialPath(env)
	if !ok {
		return "", registration, errors.New("Cannot find your home directory; set HOME")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", registration, usage(errors.New("No trial registration is waiting to be claimed. Run string trial first."))
	}
	if err != nil {
		return "", registration, err
	}
	if err := json.Unmarshal(data, &registration); err != nil || registration.ClaimToken == "" {
		return "", registration, fmt.Errorf("%s has no claim token, so this agent can't be claimed", path)
	}
	return path, registration, nil
}

// startClaim asks the portal for a code that the person at email enters on its
// claim page, shows them both, and records the claim for --poll.
func startClaim(rt runtime, registrationFile string, registration trialRegistration, email string, wait bool) (trialRegistration, error) {
	var answer struct {
		portalRefusal
		ClaimAttempt struct {
			UserCode        string `json:"user_code"`
			ExpiresIn       int    `json:"expires_in"`
			VerificationURI string `json:"verification_uri"`
			Interval        int    `json:"interval"`
		} `json:"claim_attempt"`
	}
	status, err := postJSON(rt, portalURL+"/api/agent-signup/identity/claim", map[string]string{"claim_token": registration.ClaimToken, "email": email}, &answer)
	if notOffered(status, err, answer.portalRefusal) {
		return registration, trialUnavailable(status)
	}
	if err != nil {
		return registration, err
	}
	if status != http.StatusOK {
		f := trialFailure(status, answer.portalRefusal, "Starting the claim")
		if answer.Error == "claimed_or_in_flight" {
			f.nextStep = "run `string trial claim --poll` to collect the key"
		}
		return registration, f
	}
	attempt := answer.ClaimAttempt
	if attempt.UserCode == "" || attempt.VerificationURI == "" {
		return registration, errors.New("The portal did not return a claim code")
	}
	// Without these, poll every five seconds, as RFC 8628 has device clients
	// do, and expect the code to live ten minutes, as the portal's do.
	if attempt.Interval <= 0 {
		attempt.Interval = 5
	}
	if attempt.ExpiresIn <= 0 {
		attempt.ExpiresIn = 600
	}
	registration.ClaimEmail = email
	registration.ClaimInterval = attempt.Interval
	registration.ClaimCodeExpires = rt.clock().Add(time.Duration(attempt.ExpiresIn) * time.Second)
	if err := writePrivate(registrationFile, registration); err != nil {
		return registration, err
	}
	// The person may be anywhere, so the link is shown rather than opened here.
	var shown string
	if rt.json {
		data, _ := json.Marshal(map[string]any{
			"email":            email,
			"verification_uri": attempt.VerificationURI,
			"user_code":        attempt.UserCode,
			"expires_in":       attempt.ExpiresIn,
			"interval":         attempt.Interval,
			"next":             "string trial claim --poll",
		})
		shown = string(data) + "\n"
	} else {
		shown = fmt.Sprintf("Ask %[1]s to open %[2]s, sign in to String as %[1]s and enter the code %[3]s.\n", email, attempt.VerificationURI, attempt.UserCode)
		if !wait {
			shown += fmt.Sprintf("Then run string trial claim --poll to collect the key. The code expires in %d minutes.\n", attempt.ExpiresIn/60)
		}
	}
	return registration, write(rt.stdout, []byte(shown))
}

// collectClaimedKey polls until the person enters the code, then saves the key
// on their account. It stops once the code can no longer be entered.
func collectClaimedKey(rt runtime, registrationFile string, registration trialRegistration) error {
	interval := time.Duration(registration.ClaimInterval) * time.Second
	for {
		var answer struct {
			portalRefusal
			AccessToken string `json:"access_token"`
		}
		status, err := postForm(rt, portalURL+"/api/agent-signup/token", url.Values{
			"grant_type":  {claimGrantType},
			"claim_token": {registration.ClaimToken},
		}, &answer)
		if notOffered(status, err, answer.portalRefusal) {
			return trialUnavailable(status)
		}
		if err != nil {
			return err
		}
		switch {
		case status == http.StatusOK:
			return saveClaimedKey(rt, registrationFile, registration, answer.AccessToken)
		case answer.Error == "authorization_pending" && rt.clock().Before(registration.ClaimCodeExpires.Add(interval)):
			rt.pause(interval)
		case answer.Error == "authorization_pending", answer.Error == "expired_token":
			return &failure{
				exit:     exitAuth,
				code:     "claim_expired",
				status:   status,
				message:  "The claim code expired, or was entered wrong too many times.",
				nextStep: fmt.Sprintf("run `string trial claim --email %s` for a new code", registration.ClaimEmail),
			}
		default:
			return trialFailure(status, answer.portalRefusal, "Collecting the key")
		}
	}
}

func saveClaimedKey(rt runtime, registrationFile string, registration trialRegistration, key string) error {
	if key == "" || !validKey(key) {
		return errors.New("The portal did not return a usable key")
	}
	path, ok := credentialsPath(rt.env)
	if !ok {
		return errors.New("Cannot find your home directory; set HOME")
	}
	if err := saveCredentials(path, credentials{APIKey: key, Email: registration.ClaimEmail}); err != nil {
		return err
	}
	// The claim token is a secret, and nothing needs it once the key is saved.
	if err := os.Remove(registrationFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if rt.json {
		shown, _ := json.Marshal(map[string]string{"status": "claimed", "email": registration.ClaimEmail, "credentials": path})
		return write(rt.stdout, append(shown, '\n'))
	}
	shown := fmt.Sprintf("%s claimed this agent. A key on their account is saved in %s, in place of the trial key.\n", registration.ClaimEmail, path)
	if env, _ := rt.env("STRING_API_KEY"); env != "" {
		shown += "STRING_API_KEY is set, and commands keep using it instead.\n"
	}
	return write(rt.stdout, []byte(shown))
}

func postJSON(rt runtime, endpoint string, body any, out any) (int, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequest("POST", endpoint, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	return oauthCall(rt, request, out)
}

// solveProofOfWork finds a nonce such that SHA-256 of challenge + ":" + nonce
// starts with the challenge's number of zero bits.
func solveProofOfWork(rt runtime, proof proofOfWork) (string, error) {
	if proof.Challenge == "" || proof.Algorithm != "sha256" || proof.Difficulty < 1 || proof.Difficulty > maxTrialDifficulty {
		return "", fmt.Errorf("The portal asked for a proof of work this CLI can't solve: %s at %d bits", proof.Algorithm, proof.Difficulty)
	}
	if rt.progress {
		fmt.Fprintln(rt.stderr, "Solving the proof of work...")
	}
	prefix := []byte(proof.Challenge + ":")
	input := make([]byte, 0, len(prefix)+20)
	for nonce := uint64(0); ; nonce++ {
		input = strconv.AppendUint(append(input[:0], prefix...), nonce, 10)
		if leadingZeroBits(sha256.Sum256(input)) >= proof.Difficulty {
			return strconv.FormatUint(nonce, 10), nil
		}
	}
}

func leadingZeroBits(digest [sha256.Size]byte) int {
	zeros := 0
	for _, b := range digest {
		if b != 0 {
			return zeros + bits.LeadingZeros8(b)
		}
		zeros += 8
	}
	return zeros
}
