package main

import (
	"bytes"
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
	// terminal is true when a person is at the keyboard, so init can sign in.
	terminal bool
	// progress is true when stderr is a terminal, so a long wait can show
	// how far it has got without cluttering an agent's output.
	progress bool
	// json is set by --json: machine-readable output for agents.
	json bool
	now  func() time.Time
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

const help = `String Web Access CLI

Start here: string fetch and string search work without a key, within a daily
allowance per IP address. For more, and for request and sitemap, run string
login, or set STRING_API_KEY to an API key from https://portal.usestring.ai/settings.

Usage:
  string URL [--FIELD VALUE ...]          the same as string fetch URL
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
  string init [--agent NAME[,NAME...]]
  string login [--with-token | --no-wait | --poll]
  string logout
  string whoami
  string trial
  string status
  string --version

Each API request field is a flag spelled exactly as the field, so the API
reference reads straight onto the command line:
  string fetch https://example.com --mainContentOnly --countryCode DE
A boolean flag alone is true, =false turns it off (--solveCaptcha=false), and
an object or array field takes JSON, or @ and a file path (--jsonSchema @./schema.json).
string COMMAND --help lists the command's fields.

fetch prints the page as Markdown, unless --format asks for json or raw or a
field that only JSON carries is set: jsonSchema, screenshot, captureXHR,
captureIframes, captureCookies or actions. request, search and sitemap print JSON.
--output FILE, or -o FILE, writes the response to FILE instead of stdout.
--input reads more request fields from a JSON object in FILE, or from stdin
with -; flags win over it.
sitemap submit only quotes a crawl. --approve starts it at the quoted cost, and
--wait waits for the crawl to end, showing progress on a terminal. Interrupting
the wait leaves the crawl running; string sitemap status JOB_ID --wait resumes it.
integrations lists the site integrations, typed actions over one website such
as reddit post. list SITE shows each action's flags, which are its input fields
by name, and run calls one: string integrations run reddit post --post URL.

Without a key, fetch takes --format and --executeJS, and search takes
--language, --dateRange, --sortBy, --safeSearch, --autocorrect and --verbatim.
Other fields, request and sitemap need a key. What is left of the allowance is
printed to stderr after each keyless call.
init installs the string-cli skill for the coding agents found on this machine,
then signs in when run from a terminal with no key set.
login signs in through your browser and saves the API key you choose. Without
a terminal, or with --no-wait, it prints the link and code and exits; string
login --poll then waits for the approval and saves the key. With --json both
print JSON. login --with-token reads an API key from stdin, checks it and saves it.
STRING_API_KEY wins over a saved key; whoami says which one is in use.
trial registers an agent for a trial key with no account: after a few seconds'
proof of work, it saves a key with a small balance that works for 24 hours,
and a claim token a person can use to adopt the agent onto their account.
status shows the version and the key in use. With a key it adds the balance
left, any amount held by running crawls, the plan and its request rate, and
auto top-up; without one, what the last keyless call left of today's allowance.
Set STRING_API_BASE_URL only when targeting a different API endpoint.
Errors go to stderr, with Retry-After, the keyless allowance and x-request-id
when the API sends them; --json prints them as {"error":{...}} instead.

Exit codes: 0 ok, 1 other failure, 2 bad input, 3 needs a key or the key was
refused, 4 out of credit, 5 rate limited, 6 upstream failure, 7 timeout,
130 interrupted.
`

// commandHelp is each API command's --help: its fields, named as in the API
// reference it links.
var commandHelp = map[string]string{
	"fetch": `Usage:
  string fetch URL [--FIELD VALUE ...] [--input FILE] [--output FILE]
  string URL [--FIELD VALUE ...]

Fetches URL and prints the page as Markdown. Each flag is the /fetch request
field of the same name: https://portal.usestring.ai/docs/api-reference/fetch

  --format json|markdown|raw       the response format
  --markdownMode readable|full     how much of the page the Markdown keeps
  --mainContentOnly                drop navigation, headers and footers
  --executeJS                      render the page in a browser
  --waitUntil STATE                load, domcontentloaded, networkidle0 or networkidle2
  --countryCode CC                 the proxy country, such as DE
  --headers JSON                   request headers as a JSON object
  --jsonSchema JSON|@./FILE        extract into this JSON Schema (JSON output)
  --screenshot                     capture a screenshot (JSON output)
  --actions JSON|@./FILE           browser actions to run (JSON output)
  --captureXHR [JSON]              return the page's XHR and fetch() calls (JSON output)
  --captureIframes [JSON]          list the page's iframes (JSON output)
  --includeIframes                 inline each iframe's content
  --captureCookies                 return the page's cookies (JSON output)
  --requireWSS                     require a browser WebSocket fetch path
  --solveCaptcha=false             fail on a challenge instead of solving it
  --ignoreCertificateErrors        skip TLS certificate checks
  --maxRedirects N                 follow at most N redirects
  --followRedirects=false          return the redirect response itself

Example:
  string https://example.com --mainContentOnly -o example.md
`,
	"request": `Usage:
  string request METHOD URL [--body TEXT|--body-file FILE] [--FIELD VALUE ...] [--input FILE] [--output FILE]

Sends a POST, PUT or PATCH to URL and prints the JSON response. It takes the
same fields as string fetch (string fetch --help).
`,
	"search": `Usage:
  string search QUERY [--FIELD VALUE ...] [--input FILE] [--output FILE]

Searches the web and prints the results as JSON. Each flag is the /search
request field of the same name: https://portal.usestring.ai/docs/api-reference/search

  --engine NAME                    google (the default), duckduckgo, brave or google_ai_mode
  --searchType TYPE                web, images, videos, shopping, books, places or forums
  --searchCount N                  how many results, 1 to 300 (--count works too)
  --page N                         the results page to start from
  --country CC                     the country to localize results for
  --language CODE                  the result language, such as en or pt-br
  --location PLACE                 where to search from, such as London
  --coordinates JSON               {"latitude":…,"longitude":…,"radius":…}
  --dateRange RANGE                hour, day, week, month, year, or {"from":…,"to":…}
  --sortBy relevance|date          the result order
  --safeSearch                     filter out explicit results
  --verbatim                       match the query's words exactly
  --autocorrect=false              search the query as typed
  --includeOmittedResults          include results hidden as similar
  --restrictCountry CC             only pages from this country
  --aiOverview                     wait for the AI Overview too (costs 2x)
  --format structured|raw          results as JSON, or the results page as HTML
  --solveCaptcha=false             never solve a captcha for this search

Example:
  string search "coffee grinders" --dateRange week --searchCount 20
`,
	"sitemap": `Usage:
  string sitemap submit URL [--FIELD VALUE ...] [--approve] [--wait]
  string sitemap approve JOB_ID [--wait]
  string sitemap status JOB_ID [--wait]
  string sitemap results JOB_ID [--limit N] [--offset N]
  string sitemap list [--limit N] [--offset N]
  string sitemap cancel JOB_ID

Crawls one site. submit quotes the crawl and costs nothing; approving starts it
at the quoted cost. Each field flag is the sitemap request field of the same
name: https://portal.usestring.ai/docs/api-reference/sitemap

  --maxPages N                     pages to crawl, 1 to 10,000 (10 by default)
  --maxDepth N                     link depth from URL, 1 to 100 (2 by default)
  --pathPrefix PATH                only URLs whose path starts with PATH
  --budgetUsd USD                  the spend cap (the quote by default)
  --useSitemap                     also start from the site's /sitemap.xml
  --approve                        approve the quote at once (submit only)
  --wait                           wait for the crawl to end, with progress on a terminal

Example:
  string sitemap submit https://example.com --maxPages 50 --approve --wait
`,
}

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
			return args{}, unknownOption("-"+string(first), s)
		case len(arg) > 2 && strings.HasPrefix(arg, "--"):
			name, value, inline := arg[2:], "", false
			if eq := strings.IndexByte(arg[2:], '='); eq > 0 {
				name, value, inline = arg[2:2+eq], arg[3+eq:], true
			}
			raw := "--" + name
			if field, ok := s.aliases[name]; ok {
				name = field
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
				return args{}, unknownOption(raw, s)
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

func unknownOption(raw string, s spec) error {
	// Flags are spelled as the API's fields, so --max-pages means --maxPages.
	squash := func(name string) string { return strings.ToLower(strings.ReplaceAll(name, "-", "")) }
	for name := range s.fields {
		if squash(name) == squash(strings.TrimPrefix(raw, "--")) {
			return fmt.Errorf("Unknown option '%s'. Did you mean '--%s'? Flags are spelled exactly as the API's fields.", raw, name)
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
	target, err := targetURL(parsed.positionals[0])
	if err != nil {
		return nil, err
	}
	body["url"], body["method"], body["format"] = target, "GET", format
	return &operation{method: "POST", path: "/fetch", body: body, binary: format == "raw", output: parsed.values["output"], flagged: flagged(parsed)}, nil
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
	target, err := targetURL(parsed.positionals[1])
	if err != nil {
		return nil, err
	}
	body["url"], body["method"], body["format"] = target, method, format
	if hasText {
		body["body"] = text
	}
	return &operation{method: "POST", path: "/fetch", body: body, binary: format == "raw", output: parsed.values["output"], flagged: flagged(parsed)}, nil
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
		target, err := targetURL(parsed.positionals[0])
		if err != nil {
			return nil, err
		}
		body["url"] = target
		return &operation{method: "POST", path: "/sitemap", body: body, output: output, approve: approve, wait: wait}, nil
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
		return &operation{method: methods[action], path: path, output: output, wait: wait, job: parsed.positionals[0]}, nil
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
	return nil, fmt.Errorf("Unknown command: %s", command)
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

func run(list []string, rt runtime) int {
	list, rt.json = takeJSONFlag(list)
	if err := execute(list, rt); err != nil {
		return report(rt.stderr, err, rt.json)
	}
	return 0
}

func execute(list []string, rt runtime) error {
	if len(list) == 0 || slices.Contains([]string{"", "--help", "-h", "help"}, list[0]) {
		return write(rt.stdout, []byte(help))
	}
	if strings.HasPrefix(list[0], "https://") || strings.HasPrefix(list[0], "http://") {
		list = append([]string{"fetch"}, list...)
	}
	switch list[0] {
	case "--version":
		return write(rt.stdout, []byte("string "+version+"\n"))
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
	}
	op, err := operationFor(list[0], list[1:], rt.stdin)
	if err != nil {
		return usage(err)
	}
	if op == nil {
		if text, ok := commandHelp[list[0]]; ok {
			return write(rt.stdout, []byte(text))
		}
		return write(rt.stdout, []byte(help))
	}
	op.command = list[0]
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
	data, err := caller.call(op)
	if err != nil {
		return err
	}
	var ended error
	if op.approve || op.wait {
		if data, ended = caller.followCrawl(op, data); data == nil {
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
	response, err := c.rt.client.Do(request)
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
// ends, keeping a progress line on a terminal. It returns the last response.
func (c apiClient) followCrawl(op *operation, data []byte) ([]byte, error) {
	job := op.job
	if job == "" {
		var quote struct {
			JobID string `json:"jobId"`
		}
		if err := json.Unmarshal(data, &quote); err != nil || quote.JobID == "" {
			return data, errors.New("The sitemap response named no job")
		}
		job = quote.JobID
	}
	path := "/sitemap/" + encodeURIComponent(job)
	if op.approve {
		approved, err := c.call(&operation{method: "POST", path: path + "/approve"})
		if err != nil {
			return nil, err
		}
		data = approved
	}
	if !op.wait {
		return data, nil
	}
	shown := false
	defer func() {
		if shown {
			fmt.Fprintln(c.rt.stderr)
		}
	}()
	for {
		status, err := c.call(&operation{method: "GET", path: path})
		if err != nil {
			return nil, err
		}
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
			return status, &failure{exit: exitUsage, code: "awaiting_approval", message: "The crawl is waiting for approval.", nextStep: "run `string sitemap approve " + job + " --wait`"}
		}
		if c.rt.progress {
			fmt.Fprintf(c.rt.stderr, "\r\x1b[KCrawling: %d pages done, %d queued", state.Processed, state.Pending)
			shown = true
		}
		c.rt.pause(crawlPoll)
	}
}

// write treats a reader that has gone away as success, as the Node build does.
func write(w io.Writer, data []byte) error {
	if _, err := w.Write(data); err != nil && !errors.Is(err, syscall.EPIPE) {
		return err
	}
	return nil
}
