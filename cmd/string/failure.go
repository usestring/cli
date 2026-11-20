package main

import (
	"cmp"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"slices"
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
	// reason is finer than code, such as dns or connect for a request that
	// got no answer.
	reason string
	// userAction says a person must act, as when the network is blocked.
	userAction bool
	// next are the steps out of the failure. Without them, the --json
	// envelope reads them from nextStep.
	next []nextCommand
}

// nextCommand is one step out of a failure, in the --json envelope's
// next_commands: a command to run, or something to tell the user. Its type
// lets an agent pick the steps it can take alone.
type nextCommand struct {
	Type    string   `json:"type"`
	Command string   `json:"command,omitempty"`
	Argv    []string `json:"argv,omitempty"`
	Why     string   `json:"why,omitempty"`
}

// The types of step: get a trial key, sign in, set a variable, pay, wait on
// or resume something still going, run the same command again, run a
// corrected command, or tell the user something only they can act on.
const (
	stepTrial = "trial"
	stepLogin = "login"
	stepEnv   = "env"
	stepPay   = "pay"
	stepWait  = "wait"
	stepRetry = "retry"
	stepFix   = "fix_command"
	stepTell  = "tell_user"
)

// backticked finds the commands that a next step names in backticks.
var backticked = regexp.MustCompile("`(string [^`]+)`")

// nextCommands are the steps out of f: its own, or those its next step names.
// A command in it is a wait when it resumes something still going, a login
// or a trial when it gets a key, and otherwise a corrected command.
func (f *failure) nextCommands() []nextCommand {
	if f.next != nil {
		return f.next
	}
	steps := []nextCommand{}
	for _, match := range backticked.FindAllStringSubmatch(f.nextStep, -1) {
		argv := shellWords(match[1])
		kind := stepFix
		switch {
		case slices.Contains(argv, "--poll") || slices.Contains(argv, "--wait"):
			kind = stepWait
		case len(argv) > 1 && argv[1] == "login":
			kind = stepLogin
		case len(argv) == 2 && argv[1] == "trial":
			kind = stepTrial
		}
		steps = append(steps, nextCommand{Type: kind, Command: match[1], Argv: argv})
	}
	switch {
	case strings.Contains(f.nextStep, "STRING_API_KEY"):
		steps = append(steps, nextCommand{Type: stepEnv, Why: "set STRING_API_KEY to an API key from https://portal.usestring.ai/api-keys"})
	case f.exit == exitPayment:
		steps = append(steps, nextCommand{Type: stepTell, Why: cmp.Or(f.nextStep, "the account is out of credit")})
	case len(steps) == 0 && f.retryableOrNot():
		steps = append(steps, nextCommand{Type: stepRetry, Why: cmp.Or(f.nextStep, "run the same command again")})
	}
	return steps
}

// retryableOrNot is the envelope's retryable: what the API said, and
// otherwise whether running the command again can help: after a rate limit,
// a failure at String or a timeout.
func (f *failure) retryableOrNot() bool {
	if f.retryable != nil {
		return *f.retryable
	}
	return f.exit == exitRateLimit || f.exit == exitUpstream || f.exit == exitTimeout
}

// shellWords splits a command line as a shell does for plain words and
// single- or double-quoted ones.
func shellWords(line string) []string {
	var words []string
	var word strings.Builder
	inWord, quote := false, rune(0)
	for _, r := range line {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			word.WriteRune(r)
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		words = append(words, word.String())
	}
	return words
}

// Error is the text form of f: error: and what failed, the details under it,
// then next: and the step out, after a blank line. The API's own message
// stands in for its status and body.
func (f *failure) Error() string {
	said := f.message
	if f.summary != "" {
		said = fmt.Sprintf("%s (HTTP %d)", f.summary, f.status)
	}
	lines := strings.Split(said, "\n")
	var text strings.Builder
	text.WriteString("error: " + lines[0])
	for _, line := range append(lines[1:], f.notes...) {
		text.WriteString("\n  " + line)
	}
	if f.nextStep != "" {
		text.WriteString("\n\nnext: " + f.nextStep)
	}
	return text.String()
}

// bodyText is an answer's body as a sentence a message may carry: short
// plain text, and nothing for an HTML page, JSON, or anything longer.
func bodyText(data []byte) string {
	text := strings.TrimSpace(decodeText(data))
	if text == "" || len(text) > 200 || strings.ContainsAny(text, "<{\n") {
		return ""
	}
	return text
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

// networkFailure is the failure for a request to host that got no answer:
// the name didn't resolve, the connection was refused, or TLS failed. Inside
// a sandbox that blocks the network, it says so instead, since trying again
// there can't help: only the person who runs the agent can let string out.
func networkFailure(rt runtime, host string, err error) *failure {
	if switchedOn(rt.env, "CODEX_SANDBOX_NETWORK_DISABLED") {
		no := false
		return &failure{
			exit:       exitFailure,
			code:       "failed",
			reason:     "sandbox",
			message:    "string couldn't reach " + host + ": the sandbox this command runs in blocks the network.",
			retryable:  &no,
			userAction: true,
			nextStep:   "ask the person to allow " + host + " in the sandbox's network settings, or to run string outside it",
			next:       []nextCommand{{Type: stepTell, Why: "ask the person to allow " + host + " in the sandbox's network settings, or to run string outside it"}},
		}
	}
	f := &failure{exit: exitFailure, code: "failed", message: "string lost the connection to " + host + " before an answer came."}
	var dns *net.DNSError
	var dial *net.OpError
	var record tls.RecordHeaderError
	var certificate *tls.CertificateVerificationError
	switch {
	case errors.As(err, &dns):
		f.reason, f.message = "dns", "string couldn't find "+host+": the name doesn't resolve."
	case errors.As(err, &record), errors.As(err, &certificate):
		f.reason, f.message = "tls", "string couldn't make a secure connection to "+host+"."
	case errors.As(err, &dial) && dial.Op == "dial":
		f.reason, f.message = "connect", "string couldn't connect to "+host+"."
	default:
		return f
	}
	// The request never reached host, so sending it again is safe.
	yes := true
	f.retryable = &yes
	return f
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
		"code":                 f.code,
		"message":              message,
		"retry_after_seconds":  f.retryAfter,
		"exit_code":            f.exit,
		"retryable":            f.retryableOrNot(),
		"user_action_required": f.userAction || f.exit == exitAuth || f.exit == exitPayment || f.code == "awaiting_approval",
		"next_commands":        f.nextCommands(),
	}
	if f.reason != "" {
		envelope["reason"] = f.reason
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
	if len(f.nextActions) > 0 {
		envelope["next_actions"] = f.nextActions
	}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.Encode(map[string]any{"error": envelope})
	return f.exit
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
	f := &failure{status: status, message: fmt.Sprintf("HTTP %d from %s", status, response.Request.URL.Host)}
	if detail := bodyText(data); detail != "" {
		f.message += ": " + detail
	}
	var body refusal
	_ = json.Unmarshal(data, &body)
	keylessRefusal := keyless && (strings.HasPrefix(body.Error, "Keyless") || strings.HasPrefix(body.Code, "keyless_"))
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
	// The CLI's own hint wins on key refusals: an API next action of
	// `string login` can't fix a bad STRING_API_KEY, which overrides any saved key.
	switch {
	case keyless && (keylessRefusal || status == http.StatusUnauthorized):
		f.nextStep, f.helpURL = needsKey, helpURL
	case status == http.StatusUnauthorized:
		f.nextStep = "check STRING_API_KEY, or run `string login` to save a new key"
	default:
		f.nextStep = apiNextStep(body.NextActions)
		if f.nextStep == "" && status == http.StatusPaymentRequired && body.TopUpURL != "" {
			f.nextStep = "add credit at " + body.TopUpURL
		}
	}
	return f
}

var billingSteps = map[string]string{
	"top_up":                "add credit at ",
	"choose_plan":           "choose a plan at ",
	"update_payment_method": "update the payment method at ",
}

// apiNextStep is the first of the API's next_actions that reads as a step: a
// command, or a billing page.
func apiNextStep(actions json.RawMessage) string {
	var list []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Action  string `json:"action"`
		URL     string `json:"url"`
	}
	_ = json.Unmarshal(actions, &list)
	for _, action := range list {
		if action.Command != "" {
			return action.Command
		}
		if prefix, ok := billingSteps[action.Action]; ok && action.Type == "billing" && action.URL != "" {
			return prefix + action.URL
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
