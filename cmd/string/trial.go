package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"net/http"
	"path/filepath"
	"strconv"
	"time"
)

// string trial gets an agent a short-lived key with a small balance, with no
// account and nobody present. The portal asks for a proof of work first, so
// each key costs a few seconds of CPU.

// maxTrialDifficulty bounds the work a challenge can ask for, so a misbehaving
// server can't keep the CLI hashing for hours. The portal asks for 22 bits.
const maxTrialDifficulty = 28

// trialRegistration is what string trial keeps beside the key: the claim token
// a person needs to adopt the agent once the key runs out.
type trialRegistration struct {
	RegistrationID    string    `json:"registrationId"`
	KeyExpiresAt      time.Time `json:"keyExpiresAt"`
	ClaimURL          string    `json:"claimUrl"`
	ClaimToken        string    `json:"claimToken"`
	ClaimTokenExpires time.Time `json:"claimTokenExpires"`
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
	Error              string      `json:"error"`
	ErrorDescription   string      `json:"error_description"`
}

func trialPath(env func(string) (string, bool)) (string, bool) {
	path, ok := credentialsPath(env)
	return filepath.Join(filepath.Dir(path), "trial.json"), ok
}

func trialCommand(list []string, rt runtime) error {
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
	if status == http.StatusNotFound {
		return &failure{exit: exitAuth, code: "trial_unavailable", status: status, message: "This String server does not offer trial keys.", nextStep: needsKey}
	}
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return trialFailure(status, answer)
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

func trialFailure(status int, answer trialAnswer) *failure {
	message := answer.ErrorDescription
	if message == "" {
		message = fmt.Sprintf("Trial registration failed: HTTP %d %s", status, answer.Error)
	}
	f := &failure{exit: exitFailure, code: answer.Error, status: status, message: message}
	switch {
	case answer.Error == "anonymous_not_enabled":
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
