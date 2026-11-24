package main

import (
	"bytes"
	"cmp"
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
	"path/filepath"
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
	// quiet is -q: no status lines on stderr, only errors and the keyless
	// allowance.
	quiet bool
	// switches are the global switches as given, such as --json, which a
	// command string suggests repeats.
	switches []string
	// fields is --fields when it was given, which stands in for stdout and
	// keeps only the fields it names of the JSON printed there.
	fields *fieldFilter
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
	// line is the command as it was given, global switches included, which a
	// suggestion repeats.
	line []string
	// siteStatus is the status the fetched site answered, from X-Status-Code.
	siteStatus string
	// maxTokens and offset are --max-tokens and --offset: the part of the
	// page to print, in tokens. maxTokens is 0 for all of it.
	maxTokens, offset int
	// fromCopy says the page came from its local copy, not a request.
	fromCopy bool
	// contentType is the answer's Content-Type, the site's own for --format
	// raw.
	contentType string
	// records is the key of the list in the answer that holds its records,
	// such as search's results, and limit is search's --limit: how many of
	// them to print, or 0 for all.
	records string
	limit   int
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
	format := field(body, "format")
	if format == nil {
		return fallback, nil
	}
	chosen, err := fetchFormat(jsString(format))
	if err != nil || chosen == "json" {
		return chosen, err
	}
	// A field that only JSON carries would be dropped from any other format.
	for _, name := range jsonOnlyFields {
		if jsTruthy(field(body, name)) {
			return "", fmt.Errorf("--%s comes back only in JSON, so --format %s would lose it; drop --format, or use --format json", name, chosen)
		}
	}
	return chosen, nil
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
	parsed, err := parse(list, spec{options: []string{"output", "max-tokens", "offset"}, fields: fetchFields})
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
	op := &operation{method: "POST", path: "/fetch", body: body, binary: format == "raw", output: parsed.values["output"], flagged: flagged(parsed), note: note}
	return op, pagesOf(op, parsed)
}

// pagesOf reads --max-tokens and --offset into op: a part of the page, the
// size of an agent's reading, in place of all of it.
func pagesOf(op *operation, parsed args) error {
	tokens, paged := parsed.values["max-tokens"]
	offset, hasOffset := parsed.values["offset"]
	switch {
	case !paged && hasOffset:
		return errors.New("--offset needs --max-tokens, which sets the size of each part")
	case !paged:
		return nil
	case op.binary:
		return errors.New("--max-tokens pages text, not --format raw")
	case op.output != "":
		return errors.New("Use --max-tokens to read a page in parts, or -o to save all of it, not both")
	}
	n, err := boundedInteger(tokens, "--max-tokens", 1, "positive")
	if err != nil {
		return err
	}
	op.maxTokens = int(n)
	if hasOffset {
		n, err := boundedInteger(offset, "--offset", 0, "nonnegative")
		if err != nil {
			return err
		}
		op.offset = int(n)
	}
	return nil
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
	if !slices.Contains(requestMethods, method) {
		return nil, errors.New("Request method must be POST, PUT, or PATCH")
	}
	text, hasText := parsed.value("body")
	file, hasFile := parsed.value("body-file")
	if hasText && hasFile {
		return nil, errors.New("Use only one of --body and --body-file")
	}
	if input, _ := parsed.value("input"); input == "-" && file == "-" {
		return nil, errors.New("Only one of --input - and --body-file - can read stdin")
	}
	// --body is sent as written, even when it starts with @, but @ and a
	// file that exists most likely meant --body-file.
	var warning string
	if name, ok := strings.CutPrefix(text, "@"); hasText && ok && name != "" {
		if info, err := os.Stat(name); err == nil && !info.IsDir() {
			warning = fmt.Sprintf("warning: --body %s sends the text %s; did you mean --body-file %s?", text, text, name)
		}
	}
	body, err := requestFields(parsed, stdin)
	if err != nil {
		return nil, err
	}
	if hasFile {
		var contents []byte
		if file == "-" {
			contents, err = io.ReadAll(stdin)
		} else {
			contents, err = os.ReadFile(file)
		}
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
	note = strings.TrimPrefix(note+"\n"+warning, "\n")
	note = strings.TrimSuffix(note, "\n")
	body["url"], body["method"], body["format"] = target, method, format
	if hasText {
		body["body"] = text
	}
	return &operation{method: "POST", path: "/fetch", body: body, binary: format == "raw", output: parsed.values["output"], flagged: flagged(parsed), changes: true, note: note}, nil
}

// searchAliases are older names of search's fields, which string help --json
// lists with each field.
var searchAliases = map[string]string{"count": "searchCount"}

// searchRecords are the lists that search's answer holds its records in for
// each searchType that doesn't answer in results.
var searchRecords = map[string]string{"images": "images", "places": "places", "shopping": "products"}

func searchCommand(list []string, stdin io.Reader) (*operation, error) {
	parsed, err := parse(list, spec{options: []string{"output", "limit"}, fields: searchFields, aliases: searchAliases})
	if err != nil || parsed.help {
		return nil, err
	}
	if err := expectPositionals(parsed.positionals, 1, "string search QUERY [--FIELD VALUE ...] [--limit N] [--input FILE] [--output FILE]"); err != nil {
		return nil, err
	}
	body, err := requestFields(parsed, stdin)
	if err != nil {
		return nil, err
	}
	body["query"] = parsed.positionals[0]
	op := &operation{method: "POST", path: "/search", body: body, output: parsed.values["output"], flagged: flagged(parsed),
		records: cmp.Or(searchRecords[jsString(field(body, "searchType"))], "results")}
	// --limit cuts the results here, after the API answers: it sends nothing,
	// so it works without a key and costs nothing.
	if limit, ok := parsed.value("limit"); ok {
		n, err := boundedInteger(limit, "--limit", 1, "positive")
		if err != nil {
			return nil, err
		}
		if jsString(field(body, "format")) == "raw" {
			return nil, errors.New("--limit cuts the results, and --format raw prints the results page as HTML; use one or the other")
		}
		op.limit = int(n)
	}
	return op, nil
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

// operationFor is the request command makes with the arguments in list. For
// a command string doesn't have, the command meant is suggested with list
// and switches, the global switches given.
func operationFor(command string, list, switches []string, stdin io.Reader) (*operation, error) {
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
	return nil, unknownCommand(command, withSwitches(list, switches))
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
	hasFields  bool   // --fields
	fields     string // --fields's value
	quiet      bool   // -q or --quiet
	// given are the switches as they were given, in order, which a command
	// string suggests repeats.
	given []string
}

// takeGlobalSwitches removes the global switches from the arguments before
// any "--" and reports which were there.
func takeGlobalSwitches(list []string) ([]string, globalSwitches) {
	var found globalSwitches
	kept := make([]string, 0, len(list))
	for i := 0; i < len(list); i++ {
		arg, from := list[i], i
		if global, ok := flagNamed(arg, []string{"--json", "--no-open", "--no-input", "--yes", "--timeout", "--errors", "--fields", "--quiet"}); ok {
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
		case arg == "-q", arg == "--quiet":
			found.quiet = true
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
		case arg == "--fields":
			// Its value always follows, as --timeout's does.
			found.hasFields, found.fields = true, ""
			if i+1 < len(list) {
				i++
				found.fields = list[i]
			}
		case strings.HasPrefix(arg, "--fields="):
			found.hasFields, found.fields = true, strings.TrimPrefix(arg, "--fields=")
		default:
			kept = append(kept, arg)
			continue
		}
		found.given = append(found.given, list[from:i+1]...)
	}
	return kept, found
}

// withSwitches is list with switches, the global switches given, put back
// before any "--": the command as it was given, for a suggestion to repeat
// so that it runs as this one did.
func withSwitches(list, switches []string) []string {
	end := slices.Index(list, "--")
	if end < 0 {
		end = len(list)
	}
	return slices.Concat(list[:end], switches, list[end:])
}

// suggested is the command string for args with the global switches given,
// put back as withSwitches puts them: a command that carries this one on,
// such as sitemap status JOB --wait after a wait stops, so that it runs as
// this one did. One that prints other JSON takes carried's switches instead.
func (rt runtime) suggested(args ...string) string {
	return commandLine(withSwitches(args, rt.switches))
}

func run(list []string, rt runtime) int {
	// A shell asks what can come next on the line it completes, which is
	// words as typed and not flags for string to act on.
	if len(list) > 0 && list[0] == "__complete" {
		completeCommand(list[1:], rt)
		return 0
	}
	list, global := takeGlobalSwitches(list)
	rt.json, rt.exec.noOpen = global.json, global.noOpen
	rt.exec.noInput = rt.exec.noInput || global.noInput
	rt.quiet, rt.switches = global.quiet, global.given
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
	if global.hasFields {
		paths, err := parseFields(global.fields)
		if err != nil {
			return report(rt.stderr, usage(err), jsonErrors)
		}
		rt.fields = newFieldFilter(paths, rt.stdout)
		rt.stdout = rt.fields
	}
	err := execute(list, rt)
	if err == nil && rt.fields != nil {
		err = rt.fields.end(rt.stderr, rt.quiet)
	}
	if err != nil {
		return report(rt.stderr, err, jsonErrors)
	}
	return 0
}

// asksHelp says that list asks for help, which --fields leaves as it is
// unless --json makes it JSON.
func asksHelp(list []string) bool {
	return len(list) == 0 || slices.Contains([]string{"", "--help", "help"}, list[0]) || slices.Contains(beforeDashes(list), "--help")
}

func execute(list []string, rt runtime) error {
	list = helpAnywhere(list)
	// --help before a command asks for that command's help.
	if len(list) > 1 && list[0] == "--help" && !strings.HasPrefix(list[1], "-") {
		list = append(slices.Clone(list[1:]), "--help")
	}
	if rt.fields != nil && !asksHelp(list) {
		if err := rt.fields.allows(list, rt); err != nil {
			return err
		}
	}
	if len(list) == 0 || slices.Contains([]string{"", "--help"}, list[0]) {
		if rt.json {
			return helpJSON(rt, nil)
		}
		return write(rt.stdout, []byte(rootHelp(rt)))
	}
	if list[0] == "help" {
		rest := list[1:]
		switch {
		case rt.json && len(rest) > 0 && helpTopics[rest[0]] != "":
			return helpJSON(rt, nil)
		case rt.json:
			return helpJSON(rt, commandWords(rest))
		case slices.Contains(rest, "--all"):
			return write(rt.stdout, []byte(helpAll()))
		case len(rest) == 0 || strings.HasPrefix(rest[0], "-"):
			return write(rt.stdout, []byte(rootHelp(rt)))
		}
		if topic, ok := helpTopics[list[1]]; ok {
			return write(rt.stdout, []byte(topic))
		}
		if err := unknownTopic(list[1], rt.switches); err != nil {
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
	// With --json, a command's help is its part of string help --json.
	if rt.json && slices.Contains(beforeDashes(list), "--help") {
		return helpJSON(rt, commandWords(list))
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
	case "skill":
		return skillCommand(list[1:], rt)
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
	case "completion":
		return completionCommand(list[1:], rt)
	}
	op, err := operationFor(list[0], list[1:], rt.switches, rt.stdin)
	if err != nil {
		return usage(err)
	}
	if op == nil {
		return write(rt.stdout, []byte(helpFor(list[0])))
	}
	op.command, op.line = list[0], withSwitches(list, rt.switches)
	if rt.fields != nil {
		if err := rt.fields.allowsOp(op); err != nil {
			return err
		}
	}
	if op.output != "" && op.output != "-" {
		if err := checkOutput(op.output); err != nil {
			return err
		}
	}
	if op.note != "" && !rt.quiet {
		fmt.Fprintln(rt.stderr, op.note)
	}
	if op.command == "fetch" && rt.json && announcing(rt.env) && !rt.quiet {
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
	// The parts of a page after the first come from the local copy the first
	// part saved, so reading a long page in parts spends one fetch.
	var data []byte
	if op.maxTokens > 0 && op.offset > 0 {
		data, op.fromCopy = readCopy(rt, op.body)
	}
	if !op.fromCopy {
		if data, err = caller.call(op); err != nil {
			return err
		}
		if op.maxTokens > 0 {
			saveCopy(rt, op.body, data)
		}
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
	op.siteStatus, op.contentType = response.Header.Get("X-Status-Code"), response.Header.Get("Content-Type")
	if op.command == "fetch" && announcing(c.rt.env) && !c.rt.quiet {
		if warning := siteStatusWarning(op.siteStatus, jsString(op.body["url"])); warning != "" {
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
	// Pages are untrusted. On a person's terminal, bytes that aren't text are
	// refused, and text loses what could take the terminal over; anywhere
	// else, output goes out as it came.
	person := c.rt.exec.stdoutTTY && c.rt.exec.agent == "" && (op.output == "" || op.output == "-")
	if person && op.binary && !textual(op.contentType, data) {
		media := cmp.Or(strings.TrimSpace(strings.Split(op.contentType, ";")[0]), "no content type")
		return &failure{exit: exitUsage, code: "invalid_usage", reason: "binary_on_tty", message: fmt.Sprintf("binary content (%s, %s): use -o FILE", media, sizeText(len(data))), nextStep: "run `" + commandLine(append(slices.Clone(op.line), "-o", "page.bin")) + "`"}
	}
	if person {
		op.binary = false
	}
	if !op.binary {
		text := decodeText(data)
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		data = []byte(text)
	}
	fetched := op.command == "fetch"
	if op.maxTokens > 0 {
		whole := decodeText(data)
		part, start, end, next := page(whole, op.maxTokens, op.offset)
		if person {
			part = forTerminal(part)
		}
		if !strings.HasSuffix(part, "\n") {
			part += "\n"
		}
		if err := write(c.rt.stdout, []byte(part)); err != nil {
			return err
		}
		fmt.Fprint(c.rt.stderr, pageFooter(op.line, len(whole), op.maxTokens, start, end, next, op.fromCopy))
		return nil
	}
	if op.limit > 0 {
		data = firstRecords(data, op.records, op.limit)
	}
	// --fields keeps only the fields it names, so a size or a saved file is
	// that of what it keeps. When none of them is there, the answer of a
	// command that changed something stays whole.
	stdout := c.rt.stdout
	if f := c.rt.fields; f != nil && json.Valid(data) {
		chosen := append(f.choose(data), '\n')
		stdout = f.out
		switch {
		case !f.missedAll():
			data = chosen
		case !f.changes:
			return f.missed()
		}
	}
	if person {
		data = []byte(forTerminal(string(data)))
	}
	if op.output == "" || op.output == "-" {
		// Where no person reads stdout at a terminal, a large page's size
		// comes first, before an agent's runner cuts the output.
		if fetched && len(data) > largeBody && !c.rt.quiet && !(c.rt.exec.stdoutTTY && c.rt.exec.agent == "") {
			fmt.Fprint(c.rt.stderr, sizeNotice(c.rt, jsString(op.body["url"]), len(data), op.line))
		}
		return write(stdout, data)
	}
	if err := os.WriteFile(op.output, data, 0o644); err != nil {
		return err
	}
	if !c.rt.json && !c.rt.quiet {
		status, target := "", ""
		if fetched {
			status, target = op.siteStatus, jsString(op.body["url"])
		}
		fmt.Fprint(c.rt.stderr, receipt(op.output, data, status, target, fetched && jsString(op.body["format"]) == "markdown"))
	}
	return nil
}

// checkOutput refuses an -o path that can't take a file before any request
// spends the call: a directory, or a file in a directory that doesn't exist.
func checkOutput(path string) error {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return usage(fmt.Errorf("-o %s is a directory; name a file in it, as %s", path, filepath.Join(path, "page.md")))
	}
	dir := filepath.Dir(path)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return usage(fmt.Errorf("Can't save to %s: there is no directory %s", path, dir))
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
	resume := c.rt.suggested("sitemap", "status", job, "--wait")
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
	if !c.rt.quiet {
		fmt.Fprintf(c.rt.stderr, "%s · resume: %s\n", first, resume)
	}
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
			approve := withSwitches([]string{"sitemap", "approve", job, "--wait"}, c.rt.switches)
			line := commandLine(approve)
			return status, &failure{exit: exitUsage, code: "awaiting_approval", message: "The crawl is waiting for approval.", nextStep: "run `" + line + "`",
				next: []nextCommand{{Type: stepFix, Command: line, Argv: append([]string{"string"}, approve...), Why: "starts the crawl at its quoted cost, which needs the user's go"}}}
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
