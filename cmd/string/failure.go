package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// Exit codes, documented in the help so scripts and agents can branch on them.
const (
	exitFailure     = 1
	exitUsage       = 2
	exitAuth        = 3
	exitPayment     = 4
	exitRateLimit   = 5
	exitUpstream    = 6
	exitTimeout     = 7
	exitInterrupted = 130
)

const helpURL = "https://usestring.ai/auth.md"

// needsKey is the next step for anything keyless can't do.
const needsKey = "run `string login` or set STRING_API_KEY"

// failure is an error with an exit code and the fields of the --json error
// envelope. Refusals from the API may carry code, retryable,
// retry_after_seconds and next_actions; they are passed through when present.
type failure struct {
	exit    int
	status  int
	code    string
	message string
	// summary replaces message in the --json envelope: the API's own message
	// rather than the raw body.
	summary     string
	retryable   *bool
	retryAfter  *int64
	requestID   string
	nextStep    string
	helpURL     string
	nextActions json.RawMessage
	// notes are extra stderr lines, such as the Retry-After header.
	notes []string
}

func (f *failure) Error() string {
	lines := append([]string{f.message}, f.notes...)
	if f.nextStep != "" {
		lines = append(lines, "Next: "+f.nextStep)
	}
	return strings.Join(lines, "\n")
}

func usage(err error) error {
	if err == nil {
		return nil
	}
	var f *failure
	if errors.As(err, &f) {
		return err
	}
	return &failure{exit: exitUsage, code: "invalid_usage", message: err.Error()}
}

func asFailure(err error) *failure {
	var f *failure
	if errors.As(err, &f) {
		return f
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return &failure{exit: exitTimeout, code: "timeout", message: err.Error()}
	}
	return &failure{exit: exitFailure, code: "failed", message: err.Error()}
}

// report prints a failure to stderr, as text or as the --json envelope, and
// returns the exit code.
func report(w io.Writer, err error, asJSON bool) int {
	f := asFailure(err)
	if !asJSON {
		fmt.Fprintln(w, f)
		return f.exit
	}
	message := f.message
	if f.summary != "" {
		message = f.summary
	}
	envelope := map[string]any{
		"code":                f.code,
		"message":             message,
		"retry_after_seconds": f.retryAfter,
	}
	optional := map[string]string{"request_id": f.requestID, "next_step": f.nextStep, "help_url": f.helpURL}
	for name, value := range optional {
		if value != "" {
			envelope[name] = value
		}
	}
	if f.status != 0 {
		envelope["status"] = f.status
	}
	if f.retryable != nil {
		envelope["retryable"] = *f.retryable
	}
	if len(f.nextActions) > 0 {
		envelope["next_actions"] = f.nextActions
	}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.Encode(map[string]any{"error": envelope})
	return f.exit
}

// takeJSONFlag removes --json from the arguments before any "--".
func takeJSONFlag(list []string) ([]string, bool) {
	kept, found := make([]string, 0, len(list)), false
	for i, arg := range list {
		if arg == "--" {
			return append(kept, list[i:]...), found
		}
		if arg == "--json" {
			found = true
			continue
		}
		kept = append(kept, arg)
	}
	return kept, found
}

// refusal is the JSON body of an API error. Older API versions send only
// error, plus message and signupUrl on keyless refusals.
type refusal struct {
	Error             string          `json:"error"`
	Code              string          `json:"code"`
	Message           string          `json:"message"`
	Retryable         *bool           `json:"retryable"`
	RetryAfterSeconds *int64          `json:"retry_after_seconds"`
	NextActions       json.RawMessage `json:"next_actions"`
	HelpURL           string          `json:"helpUrl"`
	TopUpURL          string          `json:"topUpUrl"`
}

// httpFailure turns an API error response into a failure. keyless is true when
// the request went without a key.
func httpFailure(response *http.Response, data []byte, keyless bool) *failure {
	status := response.StatusCode
	f := &failure{status: status, message: fmt.Sprintf("HTTP %d", status)}
	if detail := truncateUTF16(decodeText(data), 2000); detail != "" {
		f.message += ": " + detail
	}
	var body refusal
	_ = json.Unmarshal(data, &body)
	keylessRefusal := keyless && strings.HasPrefix(body.Error, "Keyless")
	switch {
	case keylessRefusal && status != http.StatusServiceUnavailable:
		f.exit, f.code = exitAuth, "keyless_refused"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		f.exit, f.code = exitAuth, "unauthorized"
	case status == http.StatusPaymentRequired:
		f.exit, f.code = exitPayment, "payment_required"
	case status == http.StatusTooManyRequests:
		f.exit, f.code = exitRateLimit, "rate_limited"
	case status >= 500:
		f.exit, f.code = exitUpstream, "upstream_error"
	case status >= 400:
		f.exit, f.code = exitUsage, "invalid_request"
	default:
		f.exit, f.code = exitFailure, "unexpected_status"
	}
	if body.Code != "" {
		f.code = body.Code
	}
	for _, text := range []string{body.Message, body.Error} {
		if text != "" {
			f.summary = text
			break
		}
	}
	f.retryable = body.Retryable
	if seconds, err := strconv.ParseInt(strings.TrimSpace(response.Header.Get("Retry-After")), 10, 64); err == nil {
		f.retryAfter = &seconds
		f.notes = append(f.notes, fmt.Sprintf("Retry-After: %d", seconds))
	} else if body.RetryAfterSeconds != nil {
		f.retryAfter = body.RetryAfterSeconds
		f.notes = append(f.notes, fmt.Sprintf("Retry-After: %d", *body.RetryAfterSeconds))
	}
	f.notes = append(f.notes, keylessNotes(response.Header)...)
	if id := response.Header.Get("X-Request-Id"); id != "" {
		f.requestID = id
		f.notes = append(f.notes, "x-request-id: "+id)
	}
	f.nextActions = body.NextActions
	f.helpURL = body.HelpURL
	f.nextStep = nextCommand(body.NextActions)
	switch {
	case f.nextStep != "":
	case keyless && (keylessRefusal || status == http.StatusUnauthorized):
		f.nextStep, f.helpURL = needsKey, helpURL
	case status == http.StatusUnauthorized:
		f.nextStep = "check STRING_API_KEY, or run `string login` to save a new key"
	case status == http.StatusPaymentRequired && body.TopUpURL != "":
		f.nextStep = "add credit at " + body.TopUpURL
	}
	return f
}

// nextCommand is the first command among the API's next_actions.
func nextCommand(actions json.RawMessage) string {
	var list []struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(actions, &list)
	for _, action := range list {
		if action.Command != "" {
			return action.Command
		}
	}
	return ""
}

// keylessNotes reports what is left of today's keyless allowance.
func keylessNotes(header http.Header) []string {
	var notes []string
	for _, name := range []string{"X-Keyless-Requests-Remaining", "X-Keyless-Credits-Remaining"} {
		if value := header.Get(name); value != "" {
			notes = append(notes, name+": "+value)
		}
	}
	return notes
}
