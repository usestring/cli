package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// leaf is one command that string help --all lists on a line and string help
// --json describes. Its page in commandDocs holds its usage, examples, flags
// and fields; a leaf adds what the page says in prose, in a form a program
// can read.
type leaf struct {
	// path is the command's words, such as sitemap submit. The first is the
	// name of its page.
	path string
	// args are its arguments as the usage names them. [SITE] is optional.
	args []string
	// aliases are other words that run it, such as URL for fetch URL.
	aliases []string
	// line says what it does, with the flags that matter most, for help --all.
	line string
	// costs says what a call costs and whether it needs a key, for help --all.
	costs string
	// keyless is true when it works without a key.
	keyless bool
	// effects is read, spend, write or local: what running it can change.
	// A coding agent may run a read or a local command without asking.
	effects string
	// flags names the page's flags and fields that it takes; nil is all of
	// them.
	flags []string
	// examples add to the page's examples that it owns.
	examples []string
	// exits are the exit codes it can end with, but for 130, which any
	// command ends with when interrupted.
	exits []int
	// stdout is what it prints: text/markdown, application/json or
	// text/plain. json is true when, with --json, stdout is one JSON object.
	stdout string
	json   bool
	// records is the key of the list of records in that JSON, such as
	// results, whose records --fields chooses the fields of.
	records string
}

var (
	// requestExits are the exit codes of a command whose request can spend.
	requestExits = []int{0, 1, 2, 3, 4, 5, 6, 7}
	// askExits are those of a command that asks String and spends nothing.
	askExits = []int{0, 1, 2, 3, 5, 6, 7}
	// basicExits are those of a command that can only fail or be misused.
	basicExits = []int{0, 1, 2}
	jobFlags   = []string{"wait", "output"}
	listFlags  = []string{"limit", "offset", "output"}
)

// leaves are every command, in the order help --all lists them: the leaves of
// each page, in the order of commands.
var leaves = func() []leaf {
	var all []leaf
	for _, name := range commands {
		all = append(all, commandDocs[name].leaves...)
	}
	return all
}()

// words splits a leaf's path into its words.
func (l leaf) words() []string { return strings.Fields(l.path) }

// page is the help page the leaf is on.
func (l leaf) page() commandDoc { return commandDocs[l.words()[0]] }

// exitCodeDoc is one exit code, as string help exit-codes explains it.
type exitCodeDoc struct {
	code    int
	meaning string
	// retry is true when running the same command again can succeed.
	retry bool
}

var exitCodeDocs = []exitCodeDoc{
	{0, "ok", false},
	{1, "the operation failed, or String couldn't be reached", false},
	{2, "bad input", false},
	{3, "a key is needed or was refused, or the keyless allowance is used up", false},
	{4, "out of credit; nothing was charged", false},
	{5, "rate limited; retry after Retry-After", true},
	{6, "String failed or didn't answer; retry once", true},
	{7, "timed out; a wait that stopped leaves its job going, and next resumes it", true},
	{130, "interrupted", false},
}

// globalFlagDocs are the flags every command takes, wherever they are.
var globalFlagDocs = []flagDoc{
	{name: "json", about: "JSON where the command has it, and errors as one JSON object on stderr"},
	{name: "errors", value: "json|text", about: "errors as JSON, as --json gives them, with stdout as it is"},
	{name: "fields", value: "A,B", about: "only these fields of the JSON on stdout: of each record in its list of records, such as search's results, or else of the object; a.b reaches inside a"},
	{name: "quiet", short: "q", about: "no status lines on stderr; errors and the keyless allowance stay"},
	{name: "timeout", value: "DURATION", about: "how long each request may take (120s, 300s for integrations run, 90s under an agent), and a whole --wait or --poll (90s under an agent); 0 for no limit"},
	{name: "no-input", about: "wait for no person, as under an agent"},
	{name: "no-open", about: "never open a browser"},
	{name: "yes", short: "y", about: "confirm what a command would ask first"},
	{name: "help", short: "h", about: "the command's page; with --json, its schema"},
}

// helpTopicNames are the pages of string help TOPIC, in the order help lists
// them.
var helpTopicNames = []string{"exit-codes", "environment", "formatting", "agents"}

// helpAll is string help --all: one line for each command, then the global
// flags and what each does, the exit codes and the topics.
func helpAll() string {
	var b strings.Builder
	b.WriteString("string " + version + ": web pages, search and crawls for agents and people\n\n")
	names := make([]string, len(leaves))
	nameWidth, lineWidth := 0, 0
	for i, l := range leaves {
		names[i] = strings.TrimSpace(l.path + " " + strings.Join(l.args, " "))
		nameWidth, lineWidth = max(nameWidth, len(names[i])), max(lineWidth, len(l.line))
	}
	for i, l := range leaves {
		fmt.Fprintf(&b, "%-*s  %-*s  %s\n", nameWidth, names[i], lineWidth, l.line, l.costs)
	}
	var codes []string
	for _, e := range exitCodeDocs {
		meaning, _, _ := strings.Cut(e.meaning, ";")
		meaning, _, _ = strings.Cut(meaning, ",")
		codes = append(codes, fmt.Sprintf("%d %s", e.code, meaning))
	}
	b.WriteString("\nEvery command takes:\n" + flagLines(globalFlagDocs, 27))
	b.WriteString("\n" + wrapItems("Exit codes:", codes))
	b.WriteString("\nTopics, a page each:\n")
	topics := make([]termDoc, len(helpTopicNames))
	for i, name := range helpTopicNames {
		title, _, _ := strings.Cut(helpTopics[name], "\n")
		_, about, _ := strings.Cut(title, ": ")
		topics[i] = termDoc{"string help " + name, about}
	}
	b.WriteString(termLines(topics, 27))
	b.WriteString("\nAPI fields are flags, spelled as in the API: --mainContentOnly --countryCode DE\n")
	b.WriteString("One command: string COMMAND --help. All of it as JSON: string help --json\n")
	return b.String()
}

// wrapItems lists items after lead, separated by dots and wrapped to
// helpWidth between items, with each line after the first indented.
func wrapItems(lead string, items []string) string {
	var b strings.Builder
	line := lead
	for i, item := range items {
		switch {
		case i == 0:
			line += " " + item
		case len(line)+3+len(item) > helpWidth:
			b.WriteString(line + " ·\n")
			line = "  " + item
		default:
			line += " · " + item
		}
	}
	b.WriteString(line + "\n")
	return b.String()
}

// The shapes of string help --json. Every token of it costs an agent that
// reads it, so what is empty, false or the same as elsewhere is left out:
// a flag that several commands take is said once, in sharedFlags.
type (
	helpSchema struct {
		Name          string       `json:"name"`
		Version       string       `json:"version"`
		SchemaVersion int          `json:"schemaVersion"`
		Spellings     string       `json:"spellings"`
		GlobalFlags   []flagSchema `json:"globalFlags"`
		// SharedFlags are the flags more than one command takes, by name.
		SharedFlags map[string]flagSchema     `json:"sharedFlags,omitempty"`
		Commands    []commandSchema           `json:"commands"`
		APIFields   map[string]fieldSetSchema `json:"apiFields"`
		ExitCodes   []exitCodeSchema          `json:"exitCodes"`
		Env         []envSchema               `json:"env"`
		Topics      []string                  `json:"topics"`
		Schemas     map[string]any            `json:"schemas"`
	}
	commandSchema struct {
		Path    []string   `json:"path"`
		Aliases [][]string `json:"aliases,omitempty"`
		Summary string     `json:"summary"`
		// Usage is left out when it says only the path and the args.
		Usage string      `json:"usage,omitempty"`
		Args  []argSchema `json:"args,omitempty"`
		// Flags are the command's own, SharedFlags name those of sharedFlags
		// it takes, and APIFields names the set in apiFields whose request
		// fields it takes as flags too.
		Flags       []flagSchema  `json:"flags,omitempty"`
		SharedFlags []string      `json:"sharedFlags,omitempty"`
		APIFields   string        `json:"apiFields,omitempty"`
		Returns     returnsSchema `json:"returns"`
		Cost        costSchema    `json:"cost"`
		Effects     string        `json:"effects"`
		ExitCodes   []int         `json:"exitCodes"`
		// Examples are in the help of the commands named, not of all of them.
		Examples []string `json:"examples,omitempty"`
	}
	// fieldSetSchema is one API endpoint's request fields, which every
	// command that sends to it takes as flags, spelled as the API spells them.
	// Each field's description is in the help of the commands named.
	fieldSetSchema struct {
		Docs string `json:"docs"`
		// Keyless are the fields a keyless call takes; any other needs a key.
		Keyless []string     `json:"keyless,omitempty"`
		Fields  []flagSchema `json:"fields"`
	}
	argSchema struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Optional bool   `json:"optional,omitempty"`
	}
	// flagSchema is a flag. Its value is what follows it, such as N or FILE,
	// or =false for a switch that is on unless turned off.
	flagSchema struct {
		Name        string   `json:"name,omitempty"`
		Short       string   `json:"short,omitempty"`
		Aliases     []string `json:"aliases,omitempty"`
		Type        string   `json:"type"`
		Value       string   `json:"value,omitempty"`
		Description string   `json:"description,omitempty"`
	}
	returnsSchema struct {
		Stdout  string `json:"stdout"`
		JSON    bool   `json:"json,omitempty"`
		Records string `json:"records,omitempty"`
	}
	costSchema struct {
		Summary string `json:"summary"`
		Keyless bool   `json:"keyless,omitempty"`
	}
	exitCodeSchema struct {
		Code    int    `json:"code"`
		Meaning string `json:"meaning"`
		Retry   bool   `json:"retry,omitempty"`
	}
	envSchema struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
)

// spellings says once, for every flag, what string help --json would
// otherwise list as each flag's aliases.
const spellings = "A flag is the same whatever its case, - and _: --mainContentOnly, --main-content-only and --main_content_only are one flag."

// kindTypes name each field kind in help --json.
var kindTypes = map[kind]string{
	text: "string", boolean: "bool", integer: "int", number: "number", object: "json",
	booleanOrObject: "bool|json", textOrObject: "string|json",
	textList: "string[]", integerList: "int[]", numberList: "number[]",
}

// fieldKinds are the request fields of each page that has them, by its name.
var fieldKinds = map[string]map[string]kind{"fetch": fetchFields, "search": searchFields, "sitemap": sitemapFields}

// fieldAliases are the older names of those fields.
var fieldAliases = map[string]map[string]string{"search": searchAliases}

// valueTypes name the type of a flag of the CLI's own from its value.
var valueTypes = map[string]string{"": "bool", "=false": "bool", "N": "int", "USD": "number"}

// flagSchemaOf describes f, a flag of the CLI's own, or an API field of the
// set named set.
func flagSchemaOf(f flagDoc, set string) flagSchema {
	described := flagSchema{Name: f.name, Short: f.short, Type: cmp.Or(valueTypes[f.value], "string"), Value: f.value, Description: f.about}
	if f.name == strings.ToUpper(f.name) {
		// It stands for every field of an action's input.
		described.Type = "any"
	}
	if k, ok := fieldKinds[set][f.name]; ok {
		described.Type = kindTypes[k]
	}
	for alias, name := range fieldAliases[set] {
		if name == f.name {
			described.Aliases = append(described.Aliases, alias)
		}
	}
	slices.Sort(described.Aliases)
	return described
}

// fieldSetOf describes the request fields of the page named set, with what
// each does when described is set.
func fieldSetOf(set string, described bool) fieldSetSchema {
	page := commandDocs[set]
	fields := fieldSetSchema{Docs: page.more, Fields: []flagSchema{}}
	for _, f := range page.fields {
		field := flagSchemaOf(f, set)
		if !described {
			field.Description = ""
		}
		fields.Fields = append(fields.Fields, field)
		if slices.Contains(keylessFields[set], f.name) {
			fields.Keyless = append(fields.Keyless, f.name)
		}
	}
	return fields
}

// fieldSet names the set of request fields that l takes, or is empty when it
// takes none.
func (l leaf) fieldSet() string {
	page := l.page()
	set := cmp.Or(page.fieldsAs, l.words()[0])
	fields := commandDocs[set].fields
	if len(fields) == 0 || l.flags != nil && !slices.ContainsFunc(fields, func(f flagDoc) bool { return slices.Contains(l.flags, f.name) }) {
		return ""
	}
	return set
}

// argTypes name the type of each argument in help --json.
var argTypes = map[string]string{"URL": "url"}

// uncommented is a usage line or an example without the note after it, which
// starts three spaces on.
func uncommented(line string) string {
	line, _, _ = strings.Cut(strings.TrimSpace(line), "   ")
	return line
}

// usageLines are a page's usage lines, each with the line it wraps onto.
func usageLines(page commandDoc) []string {
	var lines []string
	for _, line := range page.usage {
		if strings.HasPrefix(line, " ") && len(lines) > 0 {
			if more := uncommented(line); more != "" {
				lines[len(lines)-1] += " " + more
			}
			continue
		}
		lines = append(lines, uncommented(line))
	}
	return lines
}

// owner is the leaf of page that line, a command line, runs: the one with the
// longest path that starts it.
func owner(page commandDoc, line string) (leaf, bool) {
	var found leaf
	for _, l := range page.leaves {
		if (line == "string "+l.path || strings.HasPrefix(line, "string "+l.path+" ")) && len(l.path) > len(found.path) {
			found = l
		}
	}
	return found, found.path != ""
}

// schemaOf describes the leaf l from its page.
func schemaOf(l leaf) commandSchema {
	words, page := l.words(), l.page()
	described := commandSchema{
		Path:      words,
		Summary:   page.summary,
		Returns:   returnsSchema{Stdout: l.stdout, JSON: l.json, Records: l.records},
		Cost:      costSchema{Summary: l.costs, Keyless: l.keyless},
		Effects:   l.effects,
		ExitCodes: l.exits,
		APIFields: l.fieldSet(),
	}
	if len(words) > 1 {
		// A group's summary is the group's; the line says what this one does.
		described.Summary = l.line
	}
	for _, alias := range l.aliases {
		described.Aliases = append(described.Aliases, strings.Fields(alias))
	}
	var usages []string
	for _, line := range usageLines(page) {
		if mine, ok := owner(page, line); ok && mine.path == l.path {
			usages = append(usages, line)
		}
	}
	// A usage is left out when the path, the args and the flags say it all:
	// one line that offers no choice, as between --poll and --no-wait.
	if described.Usage = strings.Join(usages, "\n"); !strings.ContainsAny(described.Usage, "|\n") {
		described.Usage = ""
	}
	for _, arg := range l.args {
		name := strings.Trim(arg, "[]")
		described.Args = append(described.Args, argSchema{Name: strings.ToLower(name), Type: cmp.Or(argTypes[name], "string"), Optional: strings.HasPrefix(arg, "[")})
	}
	for _, f := range page.flags {
		global := slices.ContainsFunc(globalFlagDocs, func(g flagDoc) bool { return g.name == f.name })
		if !global && (l.flags == nil || slices.Contains(l.flags, f.name)) {
			described.Flags = append(described.Flags, flagSchemaOf(f, ""))
		}
	}
	for _, example := range page.examples {
		example = uncommented(example)
		if mine, ok := owner(page, example); ok && mine.path == l.path {
			described.Examples = append(described.Examples, example)
		}
	}
	described.Examples = append(described.Examples, l.examples...)
	return described
}

// sharedFlagsOf are the flags that more than one of every command takes,
// each the same in all of them, by name and without it.
func sharedFlagsOf() map[string]flagSchema {
	takers := map[string][]flagSchema{}
	for _, l := range leaves {
		for _, f := range schemaOf(l).Flags {
			takers[f.Name] = append(takers[f.Name], f)
		}
	}
	shared := map[string]flagSchema{}
	for name, all := range takers {
		if len(all) > 1 && !slices.ContainsFunc(all[1:], func(f flagSchema) bool { return !reflect.DeepEqual(f, all[0]) }) {
			f := all[0]
			f.Name = ""
			shared[name] = f
		}
	}
	return shared
}

// beforeDashes is list up to its first --, after which every word is an
// argument.
func beforeDashes(list []string) []string {
	if end := slices.Index(list, "--"); end >= 0 {
		return list[:end]
	}
	return list
}

// commandWords are the words at the start of list that can name a command
// and one of its subcommands.
func commandWords(list []string) []string {
	var words []string
	for _, word := range list {
		if strings.HasPrefix(word, "-") || len(words) == 2 {
			break
		}
		words = append(words, word)
	}
	return words
}

// leavesNamed are the leaves that words name: a command's, or a single
// subcommand when the second word names one.
func leavesNamed(words []string) []leaf {
	var named, narrowed []leaf
	for _, l := range leaves {
		path := l.words()
		if path[0] != words[0] {
			continue
		}
		named = append(named, l)
		if len(words) > 1 && len(path) > 1 && path[1] == words[1] {
			narrowed = append(narrowed, l)
		}
	}
	if len(narrowed) > 0 {
		return narrowed
	}
	return named
}

// errorSchema is the JSON Schema of the error object that --json and
// --errors json print on stderr.
var errorSchema = map[string]any{
	"type":     "object",
	"required": []string{"error"},
	"properties": map[string]any{
		"error": map[string]any{
			"type":     "object",
			"required": []string{"code", "message", "exit_code", "retryable", "user_action_required", "next_commands"},
			"properties": map[string]any{
				"code":                 map[string]any{"type": "string"},
				"message":              map[string]any{"type": "string"},
				"exit_code":            map[string]any{"type": "integer"},
				"retryable":            map[string]any{"type": "boolean"},
				"user_action_required": map[string]any{"type": "boolean"},
				"reason":               map[string]any{"type": "string"},
				"status":               map[string]any{"type": "integer", "description": "the API's HTTP status"},
				"retry_after_seconds":  map[string]any{"type": "integer"},
				"request_id":           map[string]any{"type": "string"},
				"next_step":            map[string]any{"type": "string"},
				"help_url":             map[string]any{"type": "string"},
				"next_actions":         map[string]any{"type": "array", "description": "the API's next_actions, as it sent them"},
				"next_commands": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":     "object",
						"required": []string{"type"},
						"properties": map[string]any{
							"type":    map[string]any{"enum": []string{stepFix, stepWait, stepRetry, stepLogin, stepTrial, stepEnv, stepPay, stepTell}},
							"command": map[string]any{"type": "string"},
							"argv":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							"why":     map[string]any{"type": "string"},
						},
					},
				},
			},
		},
	},
}

// helpJSON is string help --json, for the commands that words name, or every
// command when words is empty. The help of the commands named adds their
// examples and what each request field does.
func helpJSON(rt runtime, words []string) error {
	chosen, named := leaves, len(words) > 0 && words[0] != "help"
	if named {
		if chosen = leavesNamed(words); len(chosen) == 0 {
			// Its next step asks for the schema of the command meant.
			return unknownCommand(words[0], append(slices.Clone(words[1:]), "--help", "--json"))
		}
	}
	described := helpSchema{
		Name: "string", Version: version, SchemaVersion: 1, Spellings: spellings,
		GlobalFlags: []flagSchema{}, SharedFlags: map[string]flagSchema{}, Commands: []commandSchema{}, APIFields: map[string]fieldSetSchema{}, ExitCodes: []exitCodeSchema{}, Env: []envSchema{},
		Topics:  helpTopicNames,
		Schemas: map[string]any{"Error": errorSchema},
	}
	for _, f := range globalFlagDocs {
		described.GlobalFlags = append(described.GlobalFlags, flagSchemaOf(f, ""))
	}
	shared := sharedFlagsOf()
	for _, l := range chosen {
		command := schemaOf(l)
		// A shared flag is named here and said once, in sharedFlags.
		command.Flags = slices.DeleteFunc(command.Flags, func(f flagSchema) bool {
			if s, ok := shared[f.Name]; ok {
				command.SharedFlags = append(command.SharedFlags, f.Name)
				described.SharedFlags[f.Name] = s
				return true
			}
			return false
		})
		if !named {
			command.Examples = nil
		}
		described.Commands = append(described.Commands, command)
		if set := command.APIFields; set != "" {
			described.APIFields[set] = fieldSetOf(set, named)
		}
	}
	for _, e := range exitCodeDocs {
		described.ExitCodes = append(described.ExitCodes, exitCodeSchema{e.code, e.meaning, e.retry})
	}
	for _, v := range envDocs {
		for name := range strings.SplitSeq(v.term, ", ") {
			described.Env = append(described.Env, envSchema{name, v.about})
		}
	}
	encoded, err := json.Marshal(described)
	if err != nil {
		return err
	}
	return write(rt.stdout, append(encoded, '\n'))
}
