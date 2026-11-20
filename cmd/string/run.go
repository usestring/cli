package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

type runtime struct {
	env      func(string) (string, bool)
	lookPath func(string) (string, error)
	client   *http.Client
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
	openURL  func(string) error
	sleep    func(time.Duration)
	// program runs another program, such as claude for init, and returns
	// what it printed.
	program func(name string, args ...string) ([]byte, error)
	// exec is where this run happens: which streams are terminals, and
	// whether a coding agent or a CI job runs it.
	exec execCtx
	// json is set by --json: machine-readable output for agents.
	json bool
	// timeout is --timeout when it was given: how long each request to String
	// may take, or zero for no limit.
	timeout *time.Duration
	// usualLimit is how long a request may take when neither --timeout nor
	// an agent sets the limit. Zero means requestLimit.
	usualLimit time.Duration
	now        func() time.Time
	// executable gives the path of the running binary, which tells init how
	// this copy was installed.
	executable func() (string, error)
	// spawn starts string update --background from the binary at path, for
	// the daily update check. When it is nil, as in tests, nothing starts.
	spawn func(path string) error
	// versionOf runs the binary at path with --version, as the updater does
	// to a new release before putting it in place.
	versionOf func(path string) ([]byte, error)
	// keys are the release signing keys an update must be signed with: the
	// ones the build was made with, unless a test gives its own.
	keys func() ([]ed25519.PublicKey, error)
	// osHostname gives this machine's hostname, which string login puts in
	// the name of the key it gets: os.Hostname, unless a test gives its own.
	osHostname func() (string, error)
	// interrupt is what a Ctrl-C would leave behind, which commands note
	// while they wait or send. Tests that don't look at it leave it nil.
	interrupt *interruption
}

func (rt runtime) binary() (string, error) {
	if rt.executable == nil {
		return os.Executable()
	}
	return rt.executable()
}

func (rt runtime) pause(d time.Duration) {
	if rt.sleep == nil {
		time.Sleep(d)
		return
	}
	rt.sleep(d)
}

func (rt runtime) clock() time.Time {
	if rt.now == nil {
		return time.Now()
	}
	return rt.now()
}

// openLink opens a link string has already printed in the browser, when
// execCtx says it may.
func (rt runtime) openLink(link string) {
	if rt.exec.opensBrowser() && rt.openURL != nil {
		_ = rt.openURL(link)
	}
}

const help = `String Web Access CLI

Start here: string fetch and string search work without a key, within a daily
allowance per IP address. For more, and for request and sitemap, run string
login, or set STRING_API_KEY to an API key from https://portal.usestring.ai/api-keys.

Usage:
  string URL [--FIELD VALUE ...]          the same as string fetch URL; example.com
                                          works too, as https://example.com
  string fetch URL [--FIELD VALUE ...]
  string search QUERY [--FIELD VALUE ...]
  string request METHOD URL [--body TEXT|--body-file FILE] [--FIELD VALUE ...]
  string sitemap submit URL [--FIELD VALUE ...] [--approve] [--wait]
  string sitemap approve JOB_ID [--wait]
  string sitemap status JOB_ID [--wait]
  string sitemap results JOB_ID [--limit N] [--offset N]
  string sitemap list [--limit N] [--offset N]
  string sitemap cancel JOB_ID
  string integrations [list [SITE]]
  string integrations run SITE ACTION [--FIELD VALUE ...]
  string init [--agent NAME[,NAME...]] [--no-mcp] [--no-login] [--browser]
              [--device-name NAME]
  string login [--with-token | --no-wait | --poll] [--browser] [--env-file PATH]
               [--device-name NAME]
  string logout
  string whoami
  string trial
  string trial claim (--email ADDRESS [--no-wait] | --poll)
  string status
  string balance
  string update [--version X.Y.Z]
  string update --check
  string version [--json]
  string --version, string -v
  string help COMMAND                     the same as string COMMAND --help
  string help TOPIC                       exit-codes, environment, formatting or agents

Each API request field is a flag spelled exactly as the field, so the API
reference reads straight onto the command line:
  string fetch https://example.com --mainContentOnly --countryCode DE
A boolean flag alone is true, =false turns it off (--solveCaptcha=false), and
an object or array field takes JSON, or @ and a file path (--jsonSchema @./schema.json).
A flag of more than one word takes its kebab-case and snake_case spellings too:
--main-content-only is --mainContentOnly. A near miss of a flag or a command
names the one meant, and string exits 2 without running it.
string COMMAND --help, or -h anywhere on the line, shows the command's own page:
examples, what it prints, its exit codes, what it costs, and its flags and fields.
version adds the build to the version: the commit, its date, the system and the
Go version. Help and version send no request.

fetch prints the page as Markdown, unless --format asks for json or raw or a
field that only JSON carries is set: jsonSchema, screenshot, captureXHR,
captureIframes, captureCookies or actions. request, search and sitemap print JSON.
--output FILE, or -o FILE, writes the response to FILE instead of stdout.
--input reads more request fields from a JSON object in FILE, or from stdin
with -; flags win over it.
sitemap submit only quotes a crawl. --approve starts it at the quoted cost, and
--wait waits for the crawl to end. The wait first prints the job and the
command that resumes it, then its progress: redrawn on a person's terminal,
and elsewhere a plain line every 15s. Under an agent it stops after 90s, and
--timeout sets another limit; a wait that reaches it, or a status string
doesn't know, exits 7. The crawl keeps running then, as when the wait is
interrupted, and string sitemap status JOB_ID --wait resumes it.
integrations lists the site integrations, typed actions over one website such
as reddit post. list SITE shows each action's flags, which are its input fields
by name, and run calls one: string integrations run reddit post --post URL.

Without a key, fetch takes --format and --executeJS, and search takes
--language, --dateRange, --sortBy, --safeSearch, --autocorrect and --verbatim.
Other fields, request and sitemap need a key. What is left of the allowance is
printed to stderr after each keyless call.
init installs the string-cli skill for the coding agents found on this machine,
adds String's MCP server to Claude Code and Codex unless --no-mcp is given,
then signs in when no key is set; --browser picks login --browser's sign-in.
A person at a terminal waits for the approval there. Under an agent, with
--no-input, or when its output is captured outside CI, it prints the link and
code as login does. --no-login skips the sign-in, and init exits 0 whether or
not the sign-in finishes.
login signs in through your browser and saves a new API key that String
creates for that sign-in. Without a person at a terminal, or with --no-wait,
it prints the link and code and exits; string login --poll then waits for the
approval and saves the key. With --json both print JSON. --browser needs no
code: on the String page the link opens, you sign in or create an account and
click Authorize. login --with-token reads an API key from stdin, checks it and
saves it. --env-file PATH also writes the key to that file as its
STRING_API_KEY line and keeps the file's other lines; a new file is readable
only by you.
Where no person watches stderr at a terminal, login --poll first says how to
resume, then prints a line every 15s. Under an agent it stops after 90s, and
--timeout sets another limit; a wait that reaches it exits 7, and running
string login --poll again goes on with the same code until it expires.
With the key request, login sends String a name for this machine: its system
and the first part of its hostname, as in Mac · work-laptop. String puts it in
the new key's name, which your organization sees on its API keys page.
--device-name NAME sends NAME instead, and init takes it too. --browser and
--with-token send no name.
STRING_API_KEY wins over a saved key; whoami says which one is in use.
trial registers an agent for a trial key with no account: after a few seconds'
proof of work, it saves a key with a small balance that doesn't expire, and a
claim token a person can use to adopt the agent onto their account.
trial claim --email ADDRESS shows a link and a code for that person. Once they
sign in to String with that email and enter the code, a key on their account
replaces the trial key. Without a person at a terminal, or with --no-wait, it
exits after showing them; trial claim --poll then waits and saves the key.
That wait stops as login --poll's does, and trial claim --poll resumes it.
status shows the version and the key in use. With a key it adds the balance
left, any amount held by running crawls, the plan and its request rate, and
auto top-up; without one, what the last keyless call left of today's allowance.
balance prints the same balance, plan and auto top-up as JSON, exactly as the
API sends them, and needs a key. Unlike status, it fails when the API can't
answer.
update replaces this copy of string with the latest release once its signature
and checksum check out, or with the release --version names, which then stays
until a plain string update. Homebrew, npm and Scoop update the copies they
installed, so for those update prints their command instead. --check shows the
version, the latest release, how this copy was installed and whether it
updates itself: a copy from the install script checks once a day and installs
a new release in the background. A new release's first command also rewrites
the skill wherever init installed it. STRING_AUTO_UPDATE=0 turns both off, and
so does CI set to anything but false or 0.
Set STRING_API_BASE_URL only when targeting a different API endpoint.
string finds a coding agent that runs it from the variables agents set, and
then shows no update notices. STRING_AGENT=NAME names the agent instead, and
STRING_AGENT=human says a person runs string. Requests to String carry
string's version, the system and the agent's name; DO_NOT_TRACK=1 sends only
the version.
string opens a sign-in link in the browser only for a person at a terminal,
never under an agent, in CI or in a pipe, and --no-open stops it there too.
--no-input, or STRING_NO_INPUT=1, says nobody will answer: string then waits
for no person, as under an agent, and only --poll waits for an approval. -y
and --yes confirm what a command would ask first; none asks yet.
--timeout DURATION, as 90s, 5m or a number of seconds, limits how long each
request to String may take, or the whole wait of a command that waits, as with
--wait and --poll, and 0 means no limit. Without it a request may take 120s,
an integrations run 300s, and any request under an agent 90s. A request that
runs past its limit, or whose answer stops arriving for 60s, exits 7.
Ctrl-C says what keeps running, such as a crawl or a sign-in, and the command
that picks it up, or that a request may already have been processed. string
then ends as an interrupted program does, so a shell sees 130 and a script
that runs it stops too.
Errors go to stderr as error: and one sentence, the API's own message when it
sends one, then the details under it, such as Retry-After, the keyless
allowance and x-request-id, then next: and the step out. They never carry an
HTML page or a raw answer. --json prints them as {"error":{...}} instead.

Exit codes: 0 ok, 1 other failure, 2 bad input, 3 needs a key or the key was
refused, 4 out of credit, 5 rate limited, 6 upstream failure, 7 timeout,
130 interrupted.
`

const defaultBaseURL = "https://request.usestring.ai/v1"

// operation is one API call. A nil body sends no request body.
type operation struct {
	command string
	method  string
	path    string
	body    map[string]any
	binary  bool
	// output is the --output file the response goes to instead of stdout.
	output string
	// flagged are the body fields set by flags rather than --input.
	flagged []string
	// approve and wait carry a sitemap job on after this call: approve the
	// quote, then wait for the crawl to end. job is the id when known.
	approve bool
	wait    bool
	job     string
	// changes is set for a call that does something beyond reading, which a
	// Ctrl-C while it is sent may leave done.
	changes bool
	// note goes to stderr before the call, as when https:// was added to a URL.
	note string
}

// args is a parsed command line. Every command takes --help and --input;
// values are the command's own options, and fields are API request fields.
type args struct {
	help        bool
	values      map[string]string
	switches    map[string]bool
	fields      map[string]any
	positionals []string
}

func (a args) value(name string) (string, bool) {
	v, ok := a.values[name]
	return v, ok
}

// kind is how a field flag's text becomes the JSON value the API expects.
type kind int

const (
	text kind = iota
	boolean
	integer
	number
	object
	booleanOrObject
	textOrObject
	// The list kinds add one item per flag, or take a whole JSON array.
	textList
	integerList
	numberList
)

// itemKind is the kind of one item of a list kind.
var itemKind = map[kind]kind{textList: text, integerList: integer, numberList: number}

// spec is what a command takes besides --help and --input. Each API request
// field is a flag spelled exactly as the field, so the API reference reads
// straight onto the command line.
type spec struct {
	options  []string
	switches []string
	fields   map[string]kind
	// aliases are older flag names that still set a field.
	aliases map[string]string
}

// parseArgs follows node:util parseArgs in strict mode with positionals
// allowed, plus the field flags in s.
func parseArgs(list []string, options ...string) (args, error) {
	return parse(list, spec{options: options})
}

func parse(list []string, s spec) (args, error) {
	options := append(slices.Clone(s.options), "input")
	known := append(append([]string{"help"}, s.switches...), options...)
	for field := range s.fields {
		known = append(known, field)
	}
	parsed := args{values: map[string]string{}, switches: map[string]bool{}, fields: map[string]any{}}
	for i := 0; i < len(list); i++ {
		arg := list[i]
		if arg == "-o" && slices.Contains(options, "output") {
			arg = "--output"
		}
		switch {
		case arg == "--":
			parsed.positionals = append(parsed.positionals, list[i+1:]...)
			return parsed, nil
		case len(arg) > 1 && arg[0] == '-' && arg[1] != '-':
			first, _ := utf8.DecodeRuneInString(arg[1:])
			return args{}, unknownOption("-"+string(first), known)
		case len(arg) > 2 && strings.HasPrefix(arg, "--"):
			name, value, inline := arg[2:], "", false
			if eq := strings.IndexByte(arg[2:], '='); eq > 0 {
				name, value, inline = arg[2:2+eq], arg[3+eq:], true
			}
			raw := "--" + name
			if field, ok := s.aliases[name]; ok {
				name = field
			}
			// Each flag takes its camelCase, kebab-case and snake_case
			// spellings: --main-content-only is --mainContentOnly.
			if !slices.Contains(known, name) {
				if spelled, ok := spelledAs(name, known); ok {
					name = spelled
				}
			}
			takeValue := func() (string, error) {
				if inline {
					return value, nil
				}
				if i+1 == len(list) {
					return "", fmt.Errorf("Option '%s <value>' argument missing", raw)
				}
				i++
				if next := list[i]; len(next) > 1 && next[0] == '-' {
					return "", fmt.Errorf("Option '%[1]s' argument is ambiguous.\nDid you forget to specify the option argument for '%[1]s'?\nTo specify an option argument starting with a dash use '%[1]s=-XYZ'.", raw)
				}
				return list[i], nil
			}
			fieldKind, isField := s.fields[name]
			switch {
			case name == "help" || slices.Contains(s.switches, name):
				if inline {
					return args{}, fmt.Errorf("Option '%s' does not take an argument", raw)
				}
				if name == "help" {
					parsed.help = true
				} else {
					parsed.switches[name] = true
				}
			case slices.Contains(options, name):
				v, err := takeValue()
				if err != nil {
					return args{}, err
				}
				parsed.values[name] = v
			case isField:
				// A bare boolean flag is true. booleanOrObject also takes a JSON
				// object as its next argument, which no positional starts with.
				bare := !inline && (fieldKind == boolean || fieldKind == booleanOrObject && !(i+1 < len(list) && strings.HasPrefix(list[i+1], "{")))
				if bare {
					parsed.fields[name] = true
					continue
				}
				v, err := takeValue()
				if err != nil {
					return args{}, err
				}
				value, err := fieldValue(raw, fieldKind, v)
				if err != nil {
					return args{}, err
				}
				if _, isList := itemKind[fieldKind]; isList {
					if _, whole := value.(json.RawMessage); !whole {
						items, _ := parsed.fields[name].([]any)
						value = append(items, value)
					}
				}
				parsed.fields[name] = value
			default:
				return args{}, unknownOption(raw, known)
			}
		default:
			parsed.positionals = append(parsed.positionals, arg)
		}
	}
	return parsed, nil
}

// fieldValue reads a field flag's text as the API field's JSON value.
func fieldValue(raw string, k kind, value string) (any, error) {
	switch k {
	case boolean, booleanOrObject:
		if value == "true" || value == "false" {
			return value == "true", nil
		}
		if k == boolean {
			return nil, fmt.Errorf("%s takes no value, or =true or =false", raw)
		}
		return jsonValue(raw, value)
	case integer:
		n, ok := jsNumber(value)
		if !ok || strings.TrimSpace(value) == "" || n != math.Trunc(n) || math.Abs(n) > 1<<53-1 {
			return nil, fmt.Errorf("%s must be an integer", raw)
		}
		return int64(n), nil
	case number:
		n, ok := jsNumber(value)
		if !ok || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s must be a number", raw)
		}
		return n, nil
	case object:
		return jsonValue(raw, value)
	case textList, integerList, numberList:
		if strings.HasPrefix(strings.TrimSpace(value), "[") {
			return jsonValue(raw, value)
		}
		return fieldValue(raw, itemKind[k], value)
	case textOrObject:
		if strings.HasPrefix(strings.TrimSpace(value), "{") {
			return jsonValue(raw, value)
		}
	}
	return value, nil
}

// jsonValue reads JSON text, or the file named after an @, keeping the JSON
// exactly as written.
func jsonValue(raw, value string) (any, error) {
	source := []byte(value)
	if path, ok := strings.CutPrefix(value, "@"); ok {
		var err error
		if source, err = os.ReadFile(path); err != nil {
			return nil, err
		}
	}
	if !json.Valid(source) {
		return nil, fmt.Errorf("%s must be JSON, or @ and the path of a file holding JSON", raw)
	}
	return json.RawMessage(bytes.TrimSpace(source)), nil
}

// unknownOption is the error for a flag the command doesn't take, naming
// the one of known it is a near miss of.
func unknownOption(raw string, known []string) error {
	if strings.HasPrefix(raw, "--") {
		if guess, ok := nearest(strings.TrimPrefix(raw, "--"), known); ok {
			return fmt.Errorf("Unknown option '%s'. Did you mean '--%s'?", raw, guess)
		}
	}
	quoted, _ := json.Marshal(raw)
	return fmt.Errorf("Unknown option '%s'. To specify a positional argument starting with a '-', place it at the end of the command after '--', as in '-- %s", raw, quoted)
}

func expectPositionals(positionals []string, count int, usage string) error {
	if len(positionals) != count || slices.Contains(positionals, "") {
		return errors.New("Usage: " + usage)
	}
	return nil
}

func jsonObject(path string, stdin io.Reader) (map[string]any, error) {
	body := map[string]any{}
	if path == "" {
		return body, nil
	}
	var source []byte
	var err error
	if path == "-" {
		source, err = io.ReadAll(stdin)
	} else {
		source, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	var value json.RawMessage
	if err := json.Unmarshal(source, &value); err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(bytes.TrimSpace(value), []byte("{")) {
		return nil, errors.New("--input must contain a JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(value, &fields); err != nil {
		return nil, err
	}
	for key, field := range fields {
		body[key] = field
	}
	return body, nil
}

// field decodes one request field, from --input or a flag; an absent field
// decodes as null.
func field(body map[string]any, key string) any {
	raw, ok := body[key].(json.RawMessage)
	if !ok {
		return body[key]
	}
	var value any
	_ = json.Unmarshal(raw, &value)
	return value
}

// requestFields is the --input object with each field flag laid over it.
func requestFields(parsed args, stdin io.Reader) (map[string]any, error) {
	input, _ := parsed.value("input")
	body, err := jsonObject(input, stdin)
	if err != nil {
		return nil, err
	}
	for name, value := range parsed.fields {
		body[name] = value
	}
	return body, nil
}

// flagged lists the fields set by flags, so a refusal can name the flag.
func flagged(parsed args) []string {
	names := slices.Collect(maps.Keys(parsed.fields))
	slices.Sort(names)
	return names
}

// jsString is JavaScript's String() of a decoded JSON value, which the Node
// build applies to the method and format fields before checking them.
func jsString(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case []any:
		parts := make([]string, len(v))
		for i, element := range v {
			if element != nil {
				parts[i] = jsString(element)
			}
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}

func jsTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		return v != ""
	default:
		return true
	}
}

// jsNumber reads a flag the way JavaScript's Number() does for decimal
// input, reporting false where Number() would give NaN.
func jsNumber(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, true
	}
	if strings.ContainsAny(value, "_xX") {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(value, 64)
	return parsed, err == nil || errors.Is(err, strconv.ErrRange)
}

func boundedInteger(value string, name string, minimum float64, rule string) (int64, error) {
	parsed, ok := jsNumber(value)
	if !ok || parsed != math.Trunc(parsed) || math.Abs(parsed) > 1<<53-1 || parsed < minimum {
		return 0, fmt.Errorf("%s must be a %s integer", name, rule)
	}
	return int64(parsed), nil
}

func fetchFormat(value string) (string, error) {
	switch value {
	case "json", "markdown", "raw":
		return value, nil
	}
	return "", errors.New("--format must be json, markdown, or raw")
}

func formatFor(body map[string]any, fallback string) (string, error) {
	if format := field(body, "format"); format != nil {
		return fetchFormat(jsString(format))
	}
	return fallback, nil
}

// jsonOnlyFields come back only in a JSON response.
var jsonOnlyFields = []string{"jsonSchema", "screenshot", "captureXHR", "captureIframes", "captureCookies", "actions"}

// fetchFormatFor makes a fetch print Markdown, the page itself, unless it
// asks for something only the JSON response carries.
func fetchFormatFor(body map[string]any) (string, error) {
	for _, name := range jsonOnlyFields {
		if jsTruthy(field(body, name)) {
			return formatFor(body, "json")
		}
	}
	return formatFor(body, "markdown")
}

var errInvalidURL = errors.New("Invalid URL")

// parseURL parses an absolute http(s) URL and serializes it the way the
// WHATWG URL parser does for ordinary input: lower-case scheme and host, no
// default port, and "/" for an empty path. The API parses the URL again, so
// rarer forms such as IDNA hosts or dot segments are left for it to settle.
func parseURL(value string) (*url.URL, error) {
	value = strings.TrimFunc(value, func(r rune) bool { return r <= ' ' })
	value = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(value)
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" {
		return nil, errInvalidURL
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return u, nil
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if host == "" || u.Opaque != "" {
		return nil, errInvalidURL
	}
	if port != "" {
		if number, err := strconv.Atoi(port); err != nil || number > 65535 {
			return nil, errInvalidURL
		}
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" && !(u.Scheme == "http" && port == "80") && !(u.Scheme == "https" && port == "443") {
		host += ":" + port
	}
	u.Host = host
	if u.Path == "" && u.RawPath == "" {
		u.Path = "/"
	}
	return u, nil
}

// targetOf is the URL a command sends: value as given, or with https:// in
// front when it names no scheme, as example.com doesn't, and then a note that
// says so.
func targetOf(value string) (target, note string, err error) {
	if given := strings.TrimSpace(value); given != "" && !strings.Contains(given, "://") {
		if target, err = targetURL("https://" + given); err == nil {
			note = given + " names no scheme, so string uses " + target
		}
		return target, note, err
	}
	target, err = targetURL(value)
	return target, "", err
}

func targetURL(value string) (string, error) {
	u, err := parseURL(value)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("URL must use http or https")
	}
	return u.String(), nil
}

func apiBaseURL(env func(string) (string, bool)) (string, error) {
	value, ok := env("STRING_API_BASE_URL")
	if !ok {
		value = defaultBaseURL
	}
	u, err := parseURL(value)
	if err != nil {
		return "", err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || strings.ContainsAny(value, "?#") || u.User.String() != "" {
		return "", errors.New("STRING_API_BASE_URL must be an HTTP(S) URL without credentials, query, or fragment")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

// fetchFields are /fetch's request fields. string request takes them too.
var fetchFields = map[string]kind{
	"format":                  text,
	"markdownMode":            text,
	"mainContentOnly":         boolean,
	"jsonSchema":              object,
	"executeJS":               boolean,
	"requireWSS":              boolean,
	"waitUntil":               text,
	"captureXHR":              booleanOrObject,
	"includeIframes":          boolean,
	"captureIframes":          booleanOrObject,
	"captureCookies":          boolean,
	"headers":                 object,
	"countryCode":             text,
	"solveCaptcha":            boolean,
	"ignoreCertificateErrors": boolean,
	"maxRedirects":            integer,
	"followRedirects":         boolean,
	"screenshot":              boolean,
	"actions":                 object,
}

// searchFields are /search's request fields besides the query.
var searchFields = map[string]kind{
	"engine":                text,
	"country":               text,
	"language":              text,
	"searchCount":           integer,
	"location":              text,
	"coordinates":           object,
	"page":                  integer,
	"dateRange":             textOrObject,
	"sortBy":                text,
	"format":                text,
	"aiOverview":            boolean,
	"solveCaptcha":          boolean,
	"searchType":            text,
	"safeSearch":            boolean,
	"includeOmittedResults": boolean,
	"autocorrect":           boolean,
	"restrictCountry":       text,
	"verbatim":              boolean,
}

// sitemapFields are the fields of a sitemap submit besides the URL.
var sitemapFields = map[string]kind{
	"maxPages":   integer,
	"maxDepth":   integer,
	"pathPrefix": text,
	"budgetUsd":  number,
	"useSitemap": boolean,
}

func fetchCommand(list []string, stdin io.Reader) (*operation, error) {
	parsed, err := parse(list, spec{options: []string{"output"}, fields: fetchFields})
	if err != nil || parsed.help {
		return nil, err
	}
	if err := expectPositionals(parsed.positionals, 1, "string fetch URL [--FIELD VALUE ...] [--input FILE] [--output FILE]"); err != nil {
		return nil, err
	}
	body, err := requestFields(parsed, stdin)
	if err != nil {
		return nil, err
	}
	if method := field(body, "method"); jsTruthy(method) && strings.ToUpper(jsString(method)) != "GET" {
		return nil, errors.New("Use string request for POST, PUT, or PATCH")
	}
	if _, ok := body["body"]; ok {
		return nil, errors.New("Fetch cannot send a body; use string request")
	}
	format, err := fetchFormatFor(body)
	if err != nil {
		return nil, err
	}
	target, note, err := targetOf(parsed.positionals[0])
	if err != nil {
		return nil, err
	}
	body["url"], body["method"], body["format"] = target, "GET", format
	return &operation{method: "POST", path: "/fetch", body: body, binary: format == "raw", output: parsed.values["output"], flagged: flagged(parsed), note: note}, nil
}

func requestCommand(list []string, stdin io.Reader) (*operation, error) {
	parsed, err := parse(list, spec{options: []string{"body", "body-file", "output"}, fields: fetchFields})
	if err != nil || parsed.help {
		return nil, err
	}
	if err := expectPositionals(parsed.positionals, 2, "string request METHOD URL [--body TEXT|--body-file FILE] [--FIELD VALUE ...] [--input FILE] [--output FILE]"); err != nil {
		return nil, err
	}
	method := strings.ToUpper(parsed.positionals[0])
	if method != "POST" && method != "PUT" && method != "PATCH" {
		return nil, errors.New("Request method must be POST, PUT, or PATCH")
	}
	text, hasText := parsed.value("body")
	file, hasFile := parsed.value("body-file")
	if hasText && hasFile {
		return nil, errors.New("Use only one of --body and --body-file")
	}
	body, err := requestFields(parsed, stdin)
	if err != nil {
		return nil, err
	}
	if hasFile {
		contents, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		text, hasText = string(contents), true
	}
	format, err := formatFor(body, "json")
	if err != nil {
		return nil, err
	}
	target, note, err := targetOf(parsed.positionals[1])
	if err != nil {
		return nil, err
	}
	body["url"], body["method"], body["format"] = target, method, format
	if hasText {
		body["body"] = text
	}
	return &operation{method: "POST", path: "/fetch", body: body, binary: format == "raw", output: parsed.values["output"], flagged: flagged(parsed), changes: true, note: note}, nil
}

func searchCommand(list []string, stdin io.Reader) (*operation, error) {
	parsed, err := parse(list, spec{options: []string{"output"}, fields: searchFields, aliases: map[string]string{"count": "searchCount"}})
	if err != nil || parsed.help {
		return nil, err
	}
	if err := expectPositionals(parsed.positionals, 1, "string search QUERY [--FIELD VALUE ...] [--input FILE] [--output FILE]"); err != nil {
		return nil, err
	}
	body, err := requestFields(parsed, stdin)
	if err != nil {
		return nil, err
	}
	body["query"] = parsed.positionals[0]
	return &operation{method: "POST", path: "/search", body: body, output: parsed.values["output"], flagged: flagged(parsed)}, nil
}

func sitemapCommand(list []string, stdin io.Reader) (*operation, error) {
	if len(list) == 0 || list[0] == "" || list[0] == "--help" {
		return nil, nil
	}
	action := list[0]
	parsed, err := parse(list[1:], spec{options: []string{"limit", "offset", "output"}, switches: []string{"approve", "wait"}, fields: sitemapFields})
	if err != nil || parsed.help {
		return nil, err
	}
	limit, hasLimit := parsed.value("limit")
	offset, hasOffset := parsed.value("offset")
	approve, wait := parsed.switches["approve"], parsed.switches["wait"]
	errPaging := errors.New("--limit and --offset apply to list or results")
	if action != "submit" {
		if fields := flagged(parsed); len(fields) > 0 {
			return nil, fmt.Errorf("--%s applies only to sitemap submit", fields[0])
		}
		if approve {
			return nil, errors.New("--approve applies only to sitemap submit; use string sitemap approve JOB_ID")
		}
	}
	if wait && action != "submit" && action != "approve" && action != "status" {
		return nil, errors.New("--wait applies to sitemap submit, approve or status")
	}
	output := parsed.values["output"]
	if action == "submit" {
		if err := expectPositionals(parsed.positionals, 1, "string sitemap submit URL [--FIELD VALUE ...] [--approve] [--wait] [--input FILE]"); err != nil {
			return nil, err
		}
		if hasLimit || hasOffset {
			return nil, errPaging
		}
		if wait && !approve {
			return nil, errors.New("A submitted crawl waits for approval, so --wait needs --approve")
		}
		body, err := requestFields(parsed, stdin)
		if err != nil {
			return nil, err
		}
		target, note, err := targetOf(parsed.positionals[0])
		if err != nil {
			return nil, err
		}
		body["url"] = target
		return &operation{method: "POST", path: "/sitemap", body: body, output: output, approve: approve, wait: wait, changes: true, note: note}, nil
	}
	if _, ok := parsed.value("input"); ok {
		return nil, errors.New("--input applies only to sitemap submit")
	}
	isList := action == "list"
	usage, count, id := "string sitemap "+action, 0, ""
	if !isList {
		usage, count = usage+" JOB_ID", 1
	}
	if err := expectPositionals(parsed.positionals, count, usage); err != nil {
		return nil, err
	}
	if !isList {
		id = "/" + encodeURIComponent(parsed.positionals[0])
	}
	switch action {
	case "approve", "status", "cancel":
		if hasLimit || hasOffset {
			return nil, errPaging
		}
		methods := map[string]string{"approve": "POST", "status": "GET", "cancel": "DELETE"}
		path := "/sitemap" + id
		if action == "approve" {
			path += "/approve"
		}
		return &operation{method: methods[action], path: path, output: output, wait: wait, job: parsed.positionals[0], changes: action != "status"}, nil
	case "results", "list":
		var query []string
		if hasLimit {
			n, err := boundedInteger(limit, "--limit", 1, "positive")
			if err != nil {
				return nil, err
			}
			query = append(query, "limit="+strconv.FormatInt(n, 10))
		}
		if hasOffset {
			n, err := boundedInteger(offset, "--offset", 0, "nonnegative")
			if err != nil {
				return nil, err
			}
			query = append(query, "offset="+strconv.FormatInt(n, 10))
		}
		path := "/sitemap" + id
		if action == "results" {
			path += "/urls"
		}
		if len(query) > 0 {
			path += "?" + strings.Join(query, "&")
		}
		return &operation{method: "GET", path: path, output: output}, nil
	}
	return nil, fmt.Errorf("Unknown sitemap command: %s", action)
}

// encodeURIComponent escapes everything but JavaScript's unreserved set.
func encodeURIComponent(value string) string {
	var out strings.Builder
	for _, b := range []byte(value) {
		if 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || '0' <= b && b <= '9' || strings.IndexByte("-_.!~*'()", b) >= 0 {
			out.WriteByte(b)
		} else {
			fmt.Fprintf(&out, "%%%02X", b)
		}
	}
	return out.String()
}

func operationFor(command string, list []string, stdin io.Reader) (*operation, error) {
	switch command {
	case "fetch":
		return fetchCommand(list, stdin)
	case "request":
		return requestCommand(list, stdin)
	case "search":
		return searchCommand(list, stdin)
	case "sitemap":
		return sitemapCommand(list, stdin)
	}
	return nil, unknownCommand(command, list)
}

// decodeText decodes a response body as fetch's Response.text() does:
// UTF-8 with a leading byte order mark dropped and bad bytes replaced.
func decodeText(data []byte) string {
	return strings.ToValidUTF8(string(bytes.TrimPrefix(data, []byte("\uFEFF"))), "\uFFFD")
}

// truncateUTF16 keeps the first limit UTF-16 code units, the unit
// JavaScript's String.prototype.slice counts in.
func truncateUTF16(text string, limit int) string {
	units := 0
	for i, r := range text {
		width := utf16.RuneLen(r)
		if units+width > limit {
			if units < limit {
				return text[:i] + "\uFFFD"
			}
			return text[:i]
		}
		units += width
	}
	return text
}

// globalSwitches are the switches every command takes, wherever they are on
// the command line before "--".
type globalSwitches struct {
	json       bool   // --json
	noOpen     bool   // --no-open
	noInput    bool   // --no-input
	hasTimeout bool   // --timeout
	timeout    string // --timeout's value
	errors     string // --errors's value: json or text
}

// takeGlobalSwitches removes the global switches from the arguments before
// any "--" and reports which were there.
func takeGlobalSwitches(list []string) ([]string, globalSwitches) {
	var found globalSwitches
	kept := make([]string, 0, len(list))
	for i := 0; i < len(list); i++ {
		arg := list[i]
		if global, ok := flagNamed(arg, []string{"--json", "--no-open", "--no-input", "--yes", "--timeout", "--errors"}); ok {
			arg = global
		}
		switch {
		case arg == "--":
			return append(kept, list[i:]...), found
		case arg == "--json":
			found.json = true
		case arg == "--no-open":
			found.noOpen = true
		case arg == "--no-input":
			found.noInput = true
		case arg == "-y", arg == "--yes":
			// They confirm what a command would ask before going on. None
			// asks yet; taking them here lets scripts and agents pass them.
		case arg == "--timeout":
			// The next argument is always its value, so a missing one is
			// reported as a bad duration rather than run as a command.
			found.hasTimeout, found.timeout = true, ""
			if i+1 < len(list) {
				i++
				found.timeout = list[i]
			}
		case strings.HasPrefix(arg, "--timeout="):
			found.hasTimeout, found.timeout = true, strings.TrimPrefix(arg, "--timeout=")
		case arg == "--errors":
			// Its value always follows, as --timeout's does.
			found.errors = "--errors"
			if i+1 < len(list) {
				i++
				found.errors = list[i]
			}
		case strings.HasPrefix(arg, "--errors="):
			found.errors = strings.TrimPrefix(arg, "--errors=")
		default:
			kept = append(kept, arg)
		}
	}
	return kept, found
}

func run(list []string, rt runtime) int {
	list, global := takeGlobalSwitches(list)
	rt.json, rt.exec.noOpen = global.json, global.noOpen
	rt.exec.noInput = rt.exec.noInput || global.noInput
	// --errors json gives JSON errors, as --json does, and leaves stdout as
	// it is.
	jsonErrors := rt.json || global.errors == "json"
	rt.interrupt.answer(jsonErrors, rt.exec.redraws())
	if global.errors != "" && global.errors != "json" && global.errors != "text" {
		return report(rt.stderr, usage(fmt.Errorf("--errors takes json or text, not %q", global.errors)), jsonErrors)
	}
	if global.hasTimeout {
		limit, err := parseTimeout(global.timeout)
		if err != nil {
			return report(rt.stderr, usage(err), jsonErrors)
		}
		rt.timeout = &limit
	}
	if err := execute(list, rt); err != nil {
		return report(rt.stderr, err, jsonErrors)
	}
	return 0
}

func execute(list []string, rt runtime) error {
	list = helpAnywhere(list)
	// --help before a command asks for that command's help.
	if len(list) > 1 && list[0] == "--help" && !strings.HasPrefix(list[1], "-") {
		list = append(slices.Clone(list[1:]), "--help")
	}
	if len(list) == 0 || slices.Contains([]string{"", "--help"}, list[0]) {
		return write(rt.stdout, []byte(rootHelp(rt)))
	}
	if list[0] == "help" {
		if len(list) == 1 || strings.HasPrefix(list[1], "-") {
			return write(rt.stdout, []byte(rootHelp(rt)))
		}
		if topic, ok := helpTopics[list[1]]; ok {
			return write(rt.stdout, []byte(topic))
		}
		if err := unknownTopic(list[1]); err != nil {
			return err
		}
		// string help COMMAND is string COMMAND --help.
		return execute(append(slices.Clone(list[1:]), "--help"), rt)
	}
	// A URL, or a word with a dot such as example.com, which no command
	// has, is fetched.
	if !strings.HasPrefix(list[0], "-") && (strings.Contains(list[0], "://") || strings.Contains(list[0], ".")) {
		list = append([]string{"fetch"}, list...)
	}
	switch list[0] {
	case "--version", "-v":
		return write(rt.stdout, []byte("string "+version+"\n"))
	case "version":
		return versionCommand(list[1:], rt)
	case "init":
		return initCommand(list[1:], rt)
	case "login":
		return loginCommand(list[1:], rt)
	case "logout":
		return logoutCommand(list[1:], rt)
	case "whoami":
		return whoamiCommand(list[1:], rt)
	case "integrations":
		return integrationsCommand(list[1:], rt)
	case "trial":
		return trialCommand(list[1:], rt)
	case "status":
		return statusCommand(list[1:], rt)
	case "balance":
		return balanceCommand(list[1:], rt)
	case "update":
		return updateCommand(list[1:], rt)
	}
	op, err := operationFor(list[0], list[1:], rt.stdin)
	if err != nil {
		return usage(err)
	}
	if op == nil {
		return write(rt.stdout, []byte(helpFor(list[0])))
	}
	op.command = list[0]
	if op.note != "" {
		fmt.Fprintln(rt.stderr, op.note)
	}
	if op.command == "fetch" && rt.json && announcing(rt.env) {
		fmt.Fprint(rt.stderr, fetchJSONNotice)
	}
	key, err := apiKey(rt.env)
	if err != nil {
		return &failure{exit: exitAuth, code: "invalid_key", message: err.Error()}
	}
	if key == "" {
		if err := keylessOperation(op); err != nil {
			return err
		}
	}
	base, err := apiBaseURL(rt.env)
	if err != nil {
		return usage(err)
	}
	caller := apiClient{rt: rt, base: base, key: key}
	started := rt.clock()
	data, err := caller.call(op)
	if err != nil {
		return err
	}
	var ended error
	if op.approve || op.wait {
		if data, ended = caller.followCrawl(op, data, started); data == nil {
			return ended
		}
	}
	if err := caller.emit(op, data); err != nil {
		return err
	}
	return ended
}

type apiClient struct {
	rt   runtime
	base string
	key  string
}

// call sends one API request and returns the response body, or the API's
// refusal as a failure.
func (c apiClient) call(op *operation) ([]byte, error) {
	var payload io.Reader
	if op.body != nil {
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(op.body); err != nil {
			return nil, err
		}
		payload = bytes.NewReader(bytes.TrimSuffix(encoded.Bytes(), []byte("\n")))
	}
	request, err := http.NewRequest(op.method, c.base+op.path, payload)
	if err != nil {
		return nil, err
	}
	// Any Authorization header, even an empty one, would take the key path.
	if c.key != "" {
		request.Header.Set("Authorization", "Bearer "+c.key)
	}
	if op.body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if op.changes {
		defer c.rt.interrupt.note("the request may already have been processed", "")()
	}
	response, err := c.rt.send(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if c.key == "" {
		recordKeyless(c.rt, response.Header)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, httpFailure(response, data, c.key == "")
	}
	if err != nil {
		return nil, err
	}
	if op.command == "fetch" && announcing(c.rt.env) {
		if warning := siteStatusWarning(response.Header.Get("X-Status-Code"), jsString(op.body["url"])); warning != "" {
			fmt.Fprint(c.rt.stderr, warning)
		}
	}
	if notes := keylessNotes(response.Header); len(notes) > 0 {
		fmt.Fprintf(c.rt.stderr, "%s\nRan without a key. For more, %s.\n", strings.Join(notes, "\n"), needsKey)
	}
	return data, nil
}

// emit writes a response to stdout, or to the --output file.
func (c apiClient) emit(op *operation, data []byte) error {
	if !op.binary {
		text := decodeText(data)
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		data = []byte(text)
	}
	if op.output == "" {
		return write(c.rt.stdout, data)
	}
	if err := os.WriteFile(op.output, data, 0o644); err != nil {
		return err
	}
	if !c.rt.json {
		fmt.Fprintf(c.rt.stderr, "Saved %d bytes to %s\n", len(data), op.output)
	}
	return nil
}

const crawlPoll = 3 * time.Second

// followCrawl carries a sitemap job on from the first response: it approves
// the quoted job for --approve, then for --wait polls the job until the crawl
// ends or the wait's limit passes. The limit counts from started, when the
// command sent its first request, so the whole command stays within it. It
// returns the last response.
func (c apiClient) followCrawl(op *operation, data []byte, started time.Time) ([]byte, error) {
	var quote struct {
		JobID string `json:"jobId"`
		// The API sends the quote's ceiling as a string, as in "1.05".
		Cost json.RawMessage `json:"estimatedCostUsd"`
	}
	_ = json.Unmarshal(data, &quote)
	job := op.job
	if job == "" {
		if quote.JobID == "" {
			return data, errors.New("The sitemap response named no job")
		}
		job = quote.JobID
	}
	path := "/sitemap/" + encodeURIComponent(job)
	if op.approve {
		approved, err := c.call(&operation{method: "POST", path: path + "/approve", changes: true})
		if err != nil {
			return nil, err
		}
		data = approved
	}
	if !op.wait {
		return data, nil
	}
	// The job and the command that resumes the wait come first, so an agent
	// or a person stopped partway can pick the crawl up again.
	resume := "string sitemap status " + job + " --wait"
	first := "job " + job
	var now struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(data, &now) == nil && now.Status != "" {
		first += " · " + now.Status
	}
	if cost := strings.Trim(string(quote.Cost), `"`); op.approve && cost != "" && cost != "null" {
		first += " · up to $" + cost
	}
	fmt.Fprintf(c.rt.stderr, "%s · resume: %s\n", first, resume)
	defer c.rt.interrupt.note("the crawl "+job+" keeps running", resume)()

	w := newWaiter(c.rt, started)
	defer w.end()
	for last := data; ; {
		rt, ok := w.next()
		if !ok {
			return last, w.stopped("the crawl keeps running", resume)
		}
		poll := c
		poll.rt = rt
		status, err := poll.call(&operation{method: "GET", path: path})
		if err != nil {
			if w.over() {
				return last, w.stopped("the crawl keeps running", resume)
			}
			return nil, err
		}
		last = status
		var state struct {
			Status       string `json:"status"`
			Pending      int64  `json:"pending"`
			Processed    int64  `json:"processed"`
			ErrorMessage string `json:"errorMessage"`
		}
		_ = json.Unmarshal(status, &state)
		switch state.Status {
		case "completed", "token_cap_exceeded":
			return status, nil
		case "failed":
			return status, &failure{exit: exitFailure, code: "crawl_failed", message: strings.TrimSpace("The crawl failed. " + state.ErrorMessage)}
		case "canceled":
			return status, &failure{exit: exitFailure, code: "crawl_canceled", message: "The crawl was canceled."}
		case "awaiting_approval":
			approve := "string sitemap approve " + job + " --wait"
			return status, &failure{exit: exitUsage, code: "awaiting_approval", message: "The crawl is waiting for approval.", nextStep: "run `" + approve + "`",
				next: []nextCommand{{Type: stepFix, Command: approve, Argv: strings.Fields(approve), Why: "starts the crawl at its quoted cost, which needs the user's go"}}}
		case "running":
		default:
			// Waiting on a status string doesn't know could last forever.
			message := fmt.Sprintf("Stopped waiting: the crawl's status is %q, which string doesn't know.", state.Status)
			if state.Status == "" {
				message = "Stopped waiting: the answer gave the crawl no status."
			}
			return status, &failure{exit: exitTimeout, code: "wait_timeout", message: message, nextStep: "run `" + resume + "`"}
		}
		w.progress(fmt.Sprintf("Crawling: %d pages done, %d queued", state.Processed, state.Pending),
			fmt.Sprintf("running · %s done, %s queued", count(state.Processed, "page"), thousands(state.Pending)))
		w.pause(crawlPoll)
	}
}

// write treats a reader that has gone away as success, as the Node build does.
func write(w io.Writer, data []byte) error {
	if _, err := w.Write(data); err != nil && !errors.Is(err, syscall.EPIPE) {
		return err
	}
	return nil
}
