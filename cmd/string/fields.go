package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// fieldPath is one name --fields gives: a field, or a.b for the field b of
// the object in a.
type fieldPath []string

func (p fieldPath) String() string { return strings.Join(p, ".") }

// parseFields reads the value of --fields: names split by commas, each a
// field or a dotted path into one.
func parseFields(value string) ([]fieldPath, error) {
	if strings.TrimSpace(value) == "" {
		return nil, errors.New("--fields needs the names of fields, as --fields title,url")
	}
	var paths []fieldPath
	for name := range strings.SplitSeq(value, ",") {
		path := fieldPath(strings.Split(strings.TrimSpace(name), "."))
		if slices.Contains(path, "") {
			return nil, fmt.Errorf("--fields %s has an empty name: commas split the names, and dots the fields inside one, as in --fields title,source.domain", value)
		}
		if !slices.ContainsFunc(paths, func(p fieldPath) bool { return slices.Equal(p, path) }) {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// fieldFilter keeps only the fields that --fields names of the JSON a
// command prints. It stands in for stdout, so whatever writes JSON there
// goes through it, each JSON value once it is whole, and anything else goes
// out as it came, as a help page does.
type fieldFilter struct {
	paths []fieldPath
	// records is the key of the list in the command's JSON whose records the
	// names apply to, such as results for search. Outside such a list they
	// apply to the object itself.
	records string
	// line is the command as given, for the error to correct.
	line []string
	out  io.Writer
	// pending is what was written that isn't a whole JSON value yet, and held
	// what was chosen while no record has had any of the names.
	pending, held []byte
	// passing is set once stdout turns out to hold something other than JSON.
	passing bool
	// changes is set for a command that spends or writes, which has done so
	// by the time its JSON comes. JSON that has none of the names goes out
	// whole, with a warning, rather than be lost, so that no one runs the
	// command again for it.
	changes bool
	// found says which names some record had, seen counts the records, keys
	// are the fields the records had, and listed says they came in a list.
	found  []bool
	seen   int
	keys   []string
	listed bool
}

func newFieldFilter(paths []fieldPath, out io.Writer) *fieldFilter {
	return &fieldFilter{paths: paths, found: make([]bool, len(paths)), out: out}
}

func (f *fieldFilter) Write(p []byte) (int, error) {
	if f.passing {
		return len(p), write(f.out, p)
	}
	f.pending = append(f.pending, p...)
	for {
		rest := bytes.TrimLeft(f.pending, " \t\r\n")
		if len(rest) == 0 {
			f.pending = nil
			return len(p), nil
		}
		if rest[0] != '{' && rest[0] != '[' {
			return len(p), f.pass()
		}
		decoder := json.NewDecoder(bytes.NewReader(rest))
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				f.pending = rest
				return len(p), nil
			}
			return len(p), f.pass()
		}
		f.pending = rest[decoder.InputOffset():]
		chosen := append(f.choose(value), '\n')
		if f.changes && f.missedAll() {
			chosen = append(value, '\n')
		}
		if err := f.put(chosen); err != nil {
			return len(p), err
		}
	}
}

// pass sends what is held and pending on as it came, and so all that
// follows.
func (f *fieldFilter) pass() error {
	data := append(f.held, f.pending...)
	f.passing, f.held, f.pending = true, nil, nil
	return write(f.out, data)
}

// put writes chosen, or holds it while no record has had any of the names,
// so that names none of the JSON has print nothing, unless the command
// changes something.
func (f *fieldFilter) put(chosen []byte) error {
	f.held = append(f.held, chosen...)
	if f.missedAll() && !f.changes {
		return nil
	}
	data := f.held
	f.held = nil
	return write(f.out, data)
}

// end is the failure when no record had any of the names, once the command
// has printed all it does. When only some had none, or the command changed
// something and printed its answer whole, stderr says so. Once stdout has
// held something other than JSON, which went out as it came, it says
// nothing.
func (f *fieldFilter) end(stderr io.Writer, quiet bool) error {
	if len(f.pending) > 0 {
		if err := f.pass(); err != nil {
			return err
		}
	}
	switch absent, ok := f.absent(); {
	case f.passing:
	case f.missedAll() && !f.changes:
		return f.missed()
	case ok && !quiet:
		if f.missedAll() {
			absent += " The answer is printed whole, since the command has already run."
		}
		fmt.Fprintln(stderr, "warning: "+strings.ToLower(absent[:1])+absent[1:])
	}
	return nil
}

// missedAll says that records came, and none had any of the names.
func (f *fieldFilter) missedAll() bool {
	return f.seen > 0 && !slices.Contains(f.found, true)
}

// missed is the failure for names that no record has, which lists the
// fields there are, and corrects the command when each name is a near miss
// of one.
func (f *fieldFilter) missed() error {
	absent, _ := f.absent()
	missed := &failure{exit: exitUsage, code: "invalid_usage", reason: "unknown_field", message: absent}
	var meant []string
	for _, p := range f.paths {
		guess, ok := nearest(p[0], f.keys)
		if !ok || slices.Contains(f.keys, p[0]) {
			return missed
		}
		meant = append(meant, strings.Join(append([]string{guess}, p[1:]...), "."))
	}
	if line, ok := withFields(f.line, strings.Join(meant, ",")); ok {
		missed.nextStep = "run `" + commandLine(line) + "`"
	}
	return missed
}

// absent says which names no record had, and which fields the records have.
func (f *fieldFilter) absent() (string, bool) {
	var names []string
	for i, p := range f.paths {
		if !f.found[i] {
			names = append(names, p.String())
		}
	}
	if len(names) == 0 || f.seen == 0 {
		return "", false
	}
	missing := strings.Join(names, " or ")
	if f.listed {
		return fmt.Sprintf("No record here has %s, which --fields names. The records have %s.", missing, joinAnd(f.keys)), true
	}
	return fmt.Sprintf("The JSON here has no %s, which --fields names. It has %s.", missing, joinAnd(f.keys)), true
}

// withFields is line with value as the value of its --fields.
func withFields(line []string, value string) ([]string, bool) {
	line = slices.Clone(line)
	for i, arg := range beforeDashes(line) {
		if _, ok := flagNamed(arg, []string{"--fields"}); ok && i+1 < len(line) {
			line[i+1] = value
			return line, true
		}
		if strings.HasPrefix(arg, "--fields=") {
			line[i] = "--fields=" + value
			return line, true
		}
	}
	return nil, false
}

// carried are the global switches given but --fields, for a command string
// suggests that prints other JSON than this one, such as the poll a sign-in's
// link names, which prints the key it saves. The fields --fields names are
// this command's. A suggestion that prints the same JSON, such as a wait to
// resume or a sign-in to start over, keeps every switch.
func carried(switches []string) []string {
	var kept []string
	for i := 0; i < len(switches); i++ {
		switch _, isFields := flagNamed(switches[i], []string{"--fields"}); {
		case isFields:
			i++
		case strings.HasPrefix(switches[i], "--fields="):
		default:
			kept = append(kept, switches[i])
		}
	}
	return kept
}

// choose is value with only the fields named: in each record of its list
// of records, when it has one; in each item, when it is a list; or else in
// value itself. What has no such fields stays as it came.
func (f *fieldFilter) choose(value json.RawMessage) []byte {
	if items, ok := elements(value); ok {
		return f.chooseEach(items)
	}
	fields, ok := members(value)
	if !ok {
		return compacted(value)
	}
	if f.records == "" {
		return f.chooseOne(value)
	}
	at := slices.IndexFunc(fields, func(m member) bool { return m.key == f.records })
	if at < 0 {
		return compacted(value)
	}
	items, ok := elements(fields[at].value)
	if !ok {
		return compacted(value)
	}
	fields[at].value = f.chooseEach(items)
	return encodeObject(fields)
}

// chooseEach is a list of records, each with only the fields named.
func (f *fieldFilter) chooseEach(items []json.RawMessage) []byte {
	f.listed = true
	chosen := make([]json.RawMessage, len(items))
	for i, item := range items {
		chosen[i] = f.chooseOne(item)
	}
	return encodeArray(chosen)
}

// chooseOne is one record with only the fields named, which notes the names
// it had and the fields it has.
func (f *fieldFilter) chooseOne(record json.RawMessage) []byte {
	fields, ok := members(record)
	if !ok {
		return compacted(record)
	}
	f.seen++
	for _, m := range fields {
		if !slices.Contains(f.keys, m.key) {
			f.keys = append(f.keys, m.key)
		}
	}
	chosen, had := pick(fields, f.paths)
	for i := range had {
		f.found[i] = f.found[i] || had[i]
	}
	return chosen
}

// pick is the object of fields with only what paths name, its keys in the
// order the paths first name them, and which of paths it has.
func pick(fields []member, paths []fieldPath) ([]byte, []bool) {
	had := make([]bool, len(paths))
	var kept []member
	for i, p := range paths {
		key := p[0]
		if slices.ContainsFunc(paths[:i], func(before fieldPath) bool { return before[0] == key }) {
			continue
		}
		at := slices.IndexFunc(fields, func(m member) bool { return m.key == key })
		if at < 0 {
			continue
		}
		// The names that start with key: all of it, or fields inside it.
		whole := false
		var inside []fieldPath
		var insideAt []int
		for j, q := range paths {
			switch {
			case q[0] != key:
			case len(q) == 1:
				whole, had[j] = true, true
			default:
				inside, insideAt = append(inside, q[1:]), append(insideAt, j)
			}
		}
		value, found := within(fields[at].value, inside)
		for k, j := range insideAt {
			had[j] = found[k]
		}
		if whole {
			value = fields[at].value
		}
		if value != nil {
			kept = append(kept, member{key, value})
		}
	}
	return encodeObject(kept), had
}

// within is what paths name inside value: in it, when it is an object, or
// in each object of it, when it is a list. It is nil when value has none of
// them, and found says which it has.
func within(value json.RawMessage, paths []fieldPath) (json.RawMessage, []bool) {
	found := make([]bool, len(paths))
	if len(paths) == 0 {
		return nil, found
	}
	if fields, ok := members(value); ok {
		chosen, had := pick(fields, paths)
		if !slices.Contains(had, true) {
			return nil, had
		}
		return chosen, had
	}
	items, ok := elements(value)
	if !ok {
		return nil, found
	}
	var chosen []json.RawMessage
	for _, item := range items {
		if fields, ok := members(item); ok {
			kept, had := pick(fields, paths)
			for i := range had {
				found[i] = found[i] || had[i]
			}
			chosen = append(chosen, kept)
		}
	}
	if !slices.Contains(found, true) {
		return nil, found
	}
	return encodeArray(chosen), found
}

// member is one key of a JSON object, with its value as it came.
type member struct {
	key   string
	value json.RawMessage
}

// members are the keys of the JSON object raw, in order, or false when raw
// is no object.
func members(raw json.RawMessage) ([]member, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields := []member{}
	for decoder.More() {
		token, err := decoder.Token()
		key, isKey := token.(string)
		var value json.RawMessage
		if err != nil || !isKey || decoder.Decode(&value) != nil {
			return nil, false
		}
		fields = append(fields, member{key, value})
	}
	return fields, true
}

// elements are the items of the JSON array raw, or false when raw is no
// array.
func elements(raw json.RawMessage) ([]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('[') {
		return nil, false
	}
	items := []json.RawMessage{}
	for decoder.More() {
		var item json.RawMessage
		if decoder.Decode(&item) != nil {
			return nil, false
		}
		items = append(items, item)
	}
	return items, true
}

// compacted is the JSON value raw without the spaces between its tokens.
func compacted(raw json.RawMessage) []byte {
	var out bytes.Buffer
	if json.Compact(&out, raw) != nil {
		return raw
	}
	return out.Bytes()
}

func encodeObject(fields []member) []byte {
	out := []byte{'{'}
	for i, m := range fields {
		if i > 0 {
			out = append(out, ',')
		}
		var key bytes.Buffer
		encoder := json.NewEncoder(&key)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(m.key)
		out = append(append(append(out, bytes.TrimSuffix(key.Bytes(), []byte("\n"))...), ':'), compacted(m.value)...)
	}
	return append(out, '}')
}

func encodeArray(items []json.RawMessage) []byte {
	out := []byte{'['}
	for i, item := range items {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, compacted(item)...)
	}
	return append(out, ']')
}

// allows is nil when what list runs prints JSON on stdout, which --fields
// needs, and otherwise the failure that says so before anything is sent.
// fetch, request and search are left to allowsOp, since their request's
// format decides.
func (f *fieldFilter) allows(list []string, rt runtime) error {
	f.line = withSwitches(list, rt.switches)
	if list[0] == "--version" || list[0] == "-v" {
		fix := append([]string{"version"}, rt.switches...)
		if !rt.json {
			fix = append(fix, "--json")
		}
		return notJSON("string "+list[0]+" prints text", fix)
	}
	l, ok := readLine(list).leaf()
	f.changes = ok && (l.effects == "spend" || l.effects == "write")
	switch {
	case !ok || slices.Contains([]string{"fetch", "request", "search"}, l.words()[0]):
		return nil
	case l.stdout == "application/json", l.json && rt.json:
		f.records = l.records
		return nil
	case l.json:
		return notJSON("string "+l.path+" prints text without --json", append(slices.Clone(f.line), "--json"))
	}
	return notJSON("string "+l.path+" prints no JSON", nil)
}

// allowsOp is nil when op prints JSON on stdout, which --fields needs:
// fetch does with --format json, request unless its format is markdown or
// raw, and search unless it is raw.
func (f *fieldFilter) allowsOp(op *operation) error {
	f.line = op.line
	format := jsString(field(op.body, "format"))
	switch {
	case op.maxTokens > 0:
		return notJSON("string fetch --max-tokens prints a part of the page as text", nil)
	case op.command == "fetch" && format != "json":
		return notJSON("string fetch prints the page as "+map[string]string{"markdown": "Markdown", "raw": "it came"}[format], withSwitches(op.line, []string{"--format", "json"}))
	case op.command == "request" && format != "json":
		return notJSON("string request --format "+format+" prints no JSON", withSwitches(op.line, []string{"--format", "json"}))
	case op.command == "search" && format == "raw":
		return notJSON("string search --format raw prints the results page as HTML", withSwitches(op.line, []string{"--format", "structured"}))
	case op.records != "":
		f.records = op.records
	}
	return nil
}

// firstRecords is data, a JSON object, with only the first n records of its
// list named key, or data as it came when the list has no more than n.
func firstRecords(data []byte, key string, n int) []byte {
	fields, ok := members(data)
	if !ok {
		return data
	}
	at := slices.IndexFunc(fields, func(m member) bool { return m.key == key })
	if at < 0 {
		return data
	}
	items, ok := elements(fields[at].value)
	if !ok || len(items) <= n {
		return data
	}
	fields[at].value = encodeArray(items[:n])
	return append(encodeObject(fields), '\n')
}

// notJSON is the failure for --fields on a command that prints no JSON,
// with the command that does when there is one.
func notJSON(said string, fix []string) error {
	refused := &failure{exit: exitUsage, code: "invalid_usage", reason: "not_json", message: said + ", and --fields chooses fields of JSON."}
	if fix != nil {
		refused.nextStep = "run `" + commandLine(fix) + "`"
	}
	return refused
}
