package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
}

const help = `String Web Access CLI

Start here: string fetch and string search work without a key, within a daily
allowance per IP address. For more, and for request and sitemap, run string
login, or set STRING_API_KEY to an API key from https://portal.usestring.ai/settings.

Usage:
  string fetch URL [--format json|markdown|raw] [--input FILE]
  string search QUERY [--language CODE] [--engine NAME] [--country CODE] [--count N] [--input FILE]
  string request METHOD URL [--body TEXT|--body-file FILE] [--input FILE]
  string sitemap submit URL [--input FILE]
  string sitemap approve JOB_ID
  string sitemap status JOB_ID
  string sitemap results JOB_ID [--limit N] [--offset N]
  string sitemap list [--limit N] [--offset N]
  string sitemap cancel JOB_ID
  string init [--agent NAME[,NAME...]]
  string login [--with-token]
  string logout
  string whoami
  string --version

Without a key, fetch takes --format and search takes --language; --engine,
--country, --location, --count, request and sitemap need a key. What is left
of the allowance is printed to stderr after each keyless call.
init installs the string-cli skill for the coding agents found on this machine,
then signs in when run from a terminal with no key set.
login signs in through your browser and saves the API key you choose.
login --with-token reads an API key from stdin, checks it and saves it.
STRING_API_KEY wins over a saved key; whoami says which one is in use.
Set STRING_API_BASE_URL only when targeting a different API endpoint.
--input reads additional request fields from a JSON object in FILE, or from stdin with -.
Responses are written to stdout as JSON by default. Fetch and request also support
markdown or raw output with --format. Errors go to stderr, with Retry-After,
the keyless allowance and x-request-id when the API sends them; --json prints
them as {"error":{...}} instead.

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
}

// args is a parsed command line. Every command takes --help and --input;
// the other options are strings named by the command.
type args struct {
	help        bool
	values      map[string]string
	positionals []string
}

func (a args) value(name string) (string, bool) {
	v, ok := a.values[name]
	return v, ok
}

// parseArgs follows node:util parseArgs in strict mode with positionals
// allowed, so the Go and Node builds accept and reject the same command
// lines with the same messages.
func parseArgs(list []string, options ...string) (args, error) {
	options = append(options, "input")
	parsed := args{values: map[string]string{}}
	for i := 0; i < len(list); i++ {
		arg := list[i]
		switch {
		case arg == "--":
			parsed.positionals = append(parsed.positionals, list[i+1:]...)
			return parsed, nil
		case len(arg) > 1 && arg[0] == '-' && arg[1] != '-':
			first, _ := utf8.DecodeRuneInString(arg[1:])
			return args{}, unknownOption("-" + string(first))
		case len(arg) > 2 && strings.HasPrefix(arg, "--"):
			name, value, inline := arg[2:], "", false
			if eq := strings.IndexByte(arg[2:], '='); eq > 0 {
				name, value, inline = arg[2:2+eq], arg[3+eq:], true
			}
			raw := "--" + name
			switch {
			case name == "help":
				if inline {
					return args{}, fmt.Errorf("Option '%s' does not take an argument", raw)
				}
				parsed.help = true
			case slices.Contains(options, name):
				if !inline {
					if i+1 == len(list) {
						return args{}, fmt.Errorf("Option '%s <value>' argument missing", raw)
					}
					i++
					value = list[i]
					if len(value) > 1 && value[0] == '-' {
						return args{}, fmt.Errorf("Option '%[1]s' argument is ambiguous.\nDid you forget to specify the option argument for '%[1]s'?\nTo specify an option argument starting with a dash use '%[1]s=-XYZ'.", raw)
					}
				}
				parsed.values[name] = value
			default:
				return args{}, unknownOption(raw)
			}
		default:
			parsed.positionals = append(parsed.positionals, arg)
		}
	}
	return parsed, nil
}

func unknownOption(raw string) error {
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

// field decodes one --input field; an absent field decodes as null.
func field(body map[string]any, key string) any {
	var value any
	if raw, ok := body[key].(json.RawMessage); ok {
		_ = json.Unmarshal(raw, &value)
	}
	return value
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

func formatFor(parsed args, body map[string]any) (string, error) {
	if format, ok := parsed.value("format"); ok {
		return fetchFormat(format)
	}
	if format := field(body, "format"); format != nil {
		return fetchFormat(jsString(format))
	}
	return "json", nil
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

func fetchCommand(list []string, stdin io.Reader) (*operation, error) {
	parsed, err := parseArgs(list, "format")
	if err != nil || parsed.help {
		return nil, err
	}
	if err := expectPositionals(parsed.positionals, 1, "string fetch URL [--format json|markdown|raw] [--input FILE]"); err != nil {
		return nil, err
	}
	input, _ := parsed.value("input")
	body, err := jsonObject(input, stdin)
	if err != nil {
		return nil, err
	}
	if method := field(body, "method"); jsTruthy(method) && strings.ToUpper(jsString(method)) != "GET" {
		return nil, errors.New("Use string request for POST, PUT, or PATCH")
	}
	if _, ok := body["body"]; ok {
		return nil, errors.New("Fetch cannot send a body; use string request")
	}
	format, err := formatFor(parsed, body)
	if err != nil {
		return nil, err
	}
	target, err := targetURL(parsed.positionals[0])
	if err != nil {
		return nil, err
	}
	body["url"], body["method"], body["format"] = target, "GET", format
	return &operation{method: "POST", path: "/fetch", body: body, binary: format == "raw"}, nil
}

func requestCommand(list []string, stdin io.Reader) (*operation, error) {
	parsed, err := parseArgs(list, "format", "body", "body-file")
	if err != nil || parsed.help {
		return nil, err
	}
	if err := expectPositionals(parsed.positionals, 2, "string request METHOD URL [--body TEXT|--body-file FILE] [--input FILE]"); err != nil {
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
	input, _ := parsed.value("input")
	body, err := jsonObject(input, stdin)
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
	format, err := formatFor(parsed, body)
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
	return &operation{method: "POST", path: "/fetch", body: body, binary: format == "raw"}, nil
}

func searchCommand(list []string, stdin io.Reader) (*operation, error) {
	parsed, err := parseArgs(list, "engine", "country", "language", "count", "location")
	if err != nil || parsed.help {
		return nil, err
	}
	if err := expectPositionals(parsed.positionals, 1, "string search QUERY [--engine NAME] [--country CODE] [--count N] [--input FILE]"); err != nil {
		return nil, err
	}
	input, _ := parsed.value("input")
	body, err := jsonObject(input, stdin)
	if err != nil {
		return nil, err
	}
	body["query"] = parsed.positionals[0]
	for _, name := range []string{"engine", "country", "language", "location"} {
		if value, ok := parsed.value(name); ok {
			body[name] = value
		}
	}
	if value, ok := parsed.value("count"); ok {
		count, err := boundedInteger(value, "--count", 1, "positive")
		if err != nil {
			return nil, err
		}
		body["searchCount"] = count
	}
	return &operation{method: "POST", path: "/search", body: body}, nil
}

func sitemapCommand(list []string, stdin io.Reader) (*operation, error) {
	if len(list) == 0 || list[0] == "" || list[0] == "--help" {
		return nil, nil
	}
	action := list[0]
	parsed, err := parseArgs(list[1:], "limit", "offset")
	if err != nil || parsed.help {
		return nil, err
	}
	limit, hasLimit := parsed.value("limit")
	offset, hasOffset := parsed.value("offset")
	errPaging := errors.New("--limit and --offset apply to list or results")
	if action == "submit" {
		if err := expectPositionals(parsed.positionals, 1, "string sitemap submit URL [--input FILE]"); err != nil {
			return nil, err
		}
		if hasLimit || hasOffset {
			return nil, errPaging
		}
		input, _ := parsed.value("input")
		body, err := jsonObject(input, stdin)
		if err != nil {
			return nil, err
		}
		target, err := targetURL(parsed.positionals[0])
		if err != nil {
			return nil, err
		}
		body["url"] = target
		return &operation{method: "POST", path: "/sitemap", body: body}, nil
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
		return &operation{method: methods[action], path: path}, nil
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
		return &operation{method: "GET", path: path}, nil
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
	list, asJSON := takeJSONFlag(list)
	if err := execute(list, rt); err != nil {
		return report(rt.stderr, err, asJSON)
	}
	return 0
}

func execute(list []string, rt runtime) error {
	if len(list) == 0 || slices.Contains([]string{"", "--help", "-h", "help"}, list[0]) {
		return write(rt.stdout, []byte(help))
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
	}
	op, err := operationFor(list[0], list[1:], rt.stdin)
	if err != nil {
		return usage(err)
	}
	if op == nil {
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
	var payload io.Reader
	if op.body != nil {
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(op.body); err != nil {
			return err
		}
		payload = bytes.NewReader(bytes.TrimSuffix(encoded.Bytes(), []byte("\n")))
	}
	request, err := http.NewRequest(op.method, base+op.path, payload)
	if err != nil {
		return err
	}
	// Any Authorization header, even an empty one, would take the key path.
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	if op.body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := rt.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return httpFailure(response, data, key == "")
	}
	if err != nil {
		return err
	}
	if notes := keylessNotes(response.Header); len(notes) > 0 {
		fmt.Fprintf(rt.stderr, "%s\nRan without a key. For more, %s.\n", strings.Join(notes, "\n"), needsKey)
	}
	if !op.binary {
		text := decodeText(data)
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		data = []byte(text)
	}
	return write(rt.stdout, data)
}

// write treats a reader that has gone away as success, as the Node build does.
func write(w io.Writer, data []byte) error {
	if _, err := w.Write(data); err != nil && !errors.Is(err, syscall.EPIPE) {
		return err
	}
	return nil
}
