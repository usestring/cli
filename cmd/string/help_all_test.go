package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// string help --all is one line for each command, then the flags every
// command takes, the exit codes and a line for each topic, on a page an agent
// reads in one call.
func TestHelpAllListsEveryCommandOnOnePage(t *testing.T) {
	got := runOffline(t, "help", "--all")
	if got.status != 0 || got.errors != "" || got.output != helpAll() {
		t.Fatalf("string help --all: exit %d, stderr %q", got.status, got.errors)
	}
	lines := strings.Split(strings.TrimSuffix(got.output, "\n"), "\n")
	for _, l := range leaves {
		named := strings.TrimSpace(l.path + " " + strings.Join(l.args, " "))
		if !slices.ContainsFunc(lines, func(line string) bool {
			return strings.HasPrefix(line, named+"  ") && strings.Contains(line, "  "+l.line+"  ") && strings.HasSuffix(line, "  "+l.costs)
		}) {
			t.Errorf("string help --all has no line for %s", named)
		}
	}
	for _, f := range globalFlagDocs {
		if !strings.Contains(got.output, f.spelled()) {
			t.Errorf("string help --all doesn't name %s", f.spelled())
		}
	}
	for _, e := range exitCodeDocs {
		if !regexp.MustCompile(`(: | · |\n  )` + strconv.Itoa(e.code) + ` [A-Za-z]`).MatchString(got.output) {
			t.Errorf("string help --all doesn't explain exit %d", e.code)
		}
	}
	if !slices.Equal(slices.Sorted(maps.Keys(helpTopics)), slices.Sorted(slices.Values(helpTopicNames))) {
		t.Errorf("string help --all lists the topics %v, and string help has %v", helpTopicNames, slices.Sorted(maps.Keys(helpTopics)))
	}
	// Each topic has a line, which says what its page says it is about.
	for _, name := range helpTopicNames {
		title, _, _ := strings.Cut(helpTopics[name], "\n")
		_, about, _ := strings.Cut(title, ": ")
		if !regexp.MustCompile(`\n  string help `+name+` +`+regexp.QuoteMeta(about)+`\n`).MatchString(got.output) || about == "" {
			t.Errorf("string help --all has no line for string help %s: %q", name, about)
		}
	}
	for _, line := range lines {
		if utf8.RuneCountInString(line) > 100 {
			t.Errorf("string help --all has a line of %d columns: %q", utf8.RuneCountInString(line), line)
		}
	}
	if n := tokens(len(got.output)); n >= 2000 {
		t.Errorf("string help --all is about %d tokens, want under 2,000", n)
	}
	// Under an agent, the help for all of string names the commands to run
	// next, none of them split across lines.
	for _, command := range []string{"string help --all", "string help --json", "string fetch URL"} {
		if !slices.ContainsFunc(strings.Split(agentPreamble, "\n"), func(line string) bool { return strings.Contains(line, command) }) {
			t.Errorf("the lines for agents split or leave out %q:\n%s", command, agentPreamble)
		}
	}
}

// describedBy runs string with list and decodes the schema it prints.
func describedBy(t *testing.T, list ...string) helpSchema {
	t.Helper()
	got := runOffline(t, list...)
	var described helpSchema
	if got.status != 0 || got.errors != "" || json.Unmarshal([]byte(got.output), &described) != nil {
		t.Fatalf("string %s: exit %d, stderr %q", strings.Join(list, " "), got.status, got.errors)
	}
	return described
}

// string help --json describes each command: how to run it, its flags and the
// request fields it takes, what it prints, costs and changes, and how it
// exits, in under 8k tokens for the whole CLI.
func TestHelpJSONDescribesEveryCommand(t *testing.T) {
	got := runOffline(t, "help", "--json")
	// 8k tokens at 3 bytes a token, which JSON like this averages.
	if len(got.output) >= 24000 {
		t.Errorf("string help --json is %d bytes, want under 24,000 (8k tokens)", len(got.output))
	}
	described := describedBy(t, "help", "--json")
	exitCodes := map[int]bool{}
	for _, e := range exitCodeDocs {
		exitCodes[e.code] = true
	}
	for _, c := range described.Commands {
		path := strings.Join(c.Path, " ")
		// Each usage line and example runs this command, and not a longer
		// one, as string trial claim is longer than string trial.
		runs := func(line string) bool {
			longer := slices.ContainsFunc(leaves, func(other leaf) bool {
				return len(other.path) > len(path) && strings.HasPrefix(other.path, path+" ") && strings.HasPrefix(line+" ", "string "+other.path+" ")
			})
			return strings.HasPrefix(line+" ", "string "+path+" ") && !longer && !strings.Contains(line, "   ")
		}
		for line := range strings.SplitSeq(c.Usage, "\n") {
			if !runs(line) {
				t.Errorf("%s: usage %q", path, line)
			}
		}
		if len(c.Examples) == 0 || slices.ContainsFunc(c.Examples, func(example string) bool { return !runs(example) }) {
			t.Errorf("%s: examples %q", path, c.Examples)
		}
		if c.Summary == "" || !slices.Contains([]string{"read", "spend", "write", "local"}, c.Effects) || !slices.Contains([]string{"text/markdown", "application/json", "text/plain"}, c.Returns.Stdout) {
			t.Errorf("%s: summary %q, effects %q, stdout %q", path, c.Summary, c.Effects, c.Returns.Stdout)
		}
		// Every command can succeed, fail or be misused; 130 is anyone's.
		if !slices.IsSorted(c.ExitCodes) || len(slices.Compact(slices.Clone(c.ExitCodes))) != len(c.ExitCodes) || !slices.Contains(c.ExitCodes, 0) || !slices.Contains(c.ExitCodes, exitFailure) || !slices.Contains(c.ExitCodes, exitUsage) || slices.Contains(c.ExitCodes, exitInterrupted) || slices.ContainsFunc(c.ExitCodes, func(code int) bool { return !exitCodes[code] }) {
			t.Errorf("%s exits %v", path, c.ExitCodes)
		}
		for _, f := range c.Flags {
			if f.Name == "" || f.Type == "" || f.Description == "" || slices.ContainsFunc(globalFlagDocs, func(g flagDoc) bool { return g.name == f.Name }) {
				t.Errorf("%s: flag %+v", path, f)
			}
		}
		if _, ok := described.APIFields[c.APIFields]; c.APIFields != "" && !ok {
			t.Errorf("%s takes the fields of %q, which apiFields doesn't have", path, c.APIFields)
		}
	}
	// Each page's usage loses nothing in help --json: every flag its usage
	// lines name is in the usage of one of its commands, and another way to
	// run one, such as string URL, is that command's alias.
	for name, page := range commandDocs {
		var usages strings.Builder
		var aliases []string
		for _, c := range described.Commands {
			if c.Path[0] == name {
				usages.WriteString(c.Usage + "\n")
				for _, alias := range c.Aliases {
					aliases = append(aliases, strings.Join(alias, " "))
				}
			}
		}
		for _, line := range page.usage {
			line = uncommented(line)
			if _, ok := owner(page, line); !ok && strings.HasPrefix(line, "string ") {
				if rest := strings.TrimPrefix(line, "string "); !strings.HasPrefix(rest, "-") {
					if alias, _, _ := strings.Cut(rest, " ["); !slices.Contains(aliases, alias) {
						t.Errorf("the %s page runs a command as %q, which help --json has no alias for", name, line)
					}
				}
				continue
			}
			for _, flag := range regexp.MustCompile(`--[a-zA-Z-]+`).FindAllString(line, -1) {
				if !regexp.MustCompile(regexp.QuoteMeta(flag) + `\b`).MatchString(usages.String()) {
					t.Errorf("the %s page's usage names %s, which help --json's usage leaves out", name, flag)
				}
			}
		}
	}
	// Each set of request fields is the one the command sends, the keyless
	// fields are those the keyless tier takes, and a switch that is on unless
	// turned off says so.
	sets := map[string]map[string]kind{"fetch": fetchFields, "search": searchFields, "sitemap": sitemapFields}
	if keys := slices.Sorted(maps.Keys(described.APIFields)); !slices.Equal(keys, slices.Sorted(maps.Keys(sets))) {
		t.Errorf("apiFields has %v", keys)
	}
	for name, set := range described.APIFields {
		var fields []string
		for _, f := range set.Fields {
			fields = append(fields, f.Name)
			i := slices.IndexFunc(commandDocs[name].fields, func(doc flagDoc) bool { return doc.name == f.Name })
			onUnlessOff := i >= 0 && commandDocs[name].fields[i].value == "=false"
			if f.Type != kindTypes[sets[name][f.Name]] || f.Description == "" || (f.Default != nil) != onUnlessOff || onUnlessOff && !*f.Default || strings.HasPrefix(f.Value, "=") {
				t.Errorf("%s field %+v", name, f)
			}
		}
		if slices.Sort(fields); !slices.Equal(fields, slices.Sorted(maps.Keys(sets[name]))) {
			t.Errorf("apiFields.%s has %v, and the command sends %v", name, fields, slices.Sorted(maps.Keys(sets[name])))
		}
		// An older name the command still takes is listed with its field.
		for alias, field := range fieldAliases[name] {
			if !slices.ContainsFunc(set.Fields, func(f flagSchema) bool { return f.Name == field && slices.Contains(f.Aliases, alias) }) {
				t.Errorf("apiFields.%s doesn't list --%s with %s", name, alias, field)
			}
		}
		var keyless []string
		for _, field := range keylessFields[name] {
			if _, ok := sets[name][field]; ok {
				keyless = append(keyless, field)
			}
		}
		if !slices.Equal(slices.Sorted(slices.Values(set.Keyless)), slices.Sorted(slices.Values(keyless))) || !strings.HasPrefix(set.Docs, "https://") {
			t.Errorf("apiFields.%s: keyless %v, want %v; docs %q", name, set.Keyless, keyless, set.Docs)
		}
	}
	// Every variable string help environment lists is in env.
	for _, match := range regexp.MustCompile(`\n  ([A-Z][A-Z_]+)[ ,\n]`).FindAllStringSubmatch(helpTopics["environment"], -1) {
		if !slices.ContainsFunc(described.Env, func(v envSchema) bool { return v.Name == match[1] && v.Description != "" }) {
			t.Errorf("env has no %s", match[1])
		}
	}
	if _, ok := described.Schemas["Error"]; !ok {
		t.Errorf("schemas has no Error")
	}
}

// unreachable is a network where every request fails.
type unreachable struct{}

func (unreachable) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("no network in this test")
}

// returns.json says whether --json gives one JSON object on stdout, as each
// command that succeeds here without the network shows.
func TestReturnsSaysWhatJSONGives(t *testing.T) {
	described := describedBy(t, "help", "--json")
	for _, list := range [][]string{{"version"}, {"skill"}, {"logout"}, {"whoami", "--offline"}, {"init", "--no-login", "--no-mcp"}, {"status"}} {
		var stdout, stderr bytes.Buffer
		status := run(append(list, "--json"), runtime{
			env:      envOf(map[string]string{"HOME": t.TempDir(), "STRING_API_KEY": "test-key"}),
			lookPath: func(name string) (string, error) { return "/opt/bin/" + name, nil },
			client:   &http.Client{Transport: unreachable{}},
			stdout:   &stdout,
			stderr:   &stderr,
		})
		var object map[string]any
		gave := json.Unmarshal(stdout.Bytes(), &object) == nil
		i := slices.IndexFunc(described.Commands, func(c commandSchema) bool { return c.Path[0] == list[0] })
		if status != 0 || gave != described.Commands[i].Returns.JSON {
			t.Errorf("string %s --json: exit %d, one JSON object %v, and help --json says %v; stderr %q", strings.Join(list, " "), status, gave, described.Commands[i].Returns.JSON, stderr.String())
		}
	}
}

// string help --json COMMAND, and COMMAND --help with --json, describe that
// command alone: all of a group's commands, or one of them.
func TestHelpJSONForOneCommand(t *testing.T) {
	for _, c := range []struct {
		list    []string
		paths   []string
		fieldOf []string
	}{
		{[]string{"help", "--json", "fetch"}, []string{"fetch"}, []string{"fetch"}},
		{[]string{"help", "fetch", "--json"}, []string{"fetch"}, []string{"fetch"}},
		{[]string{"https://example.com", "-h", "--json"}, []string{"fetch"}, []string{"fetch"}},
		{[]string{"request", "POST", "https://example.com", "--json", "-h"}, []string{"request"}, []string{"fetch"}},
		{[]string{"sitemap", "status", "job-1", "--help", "--json"}, []string{"sitemap status"}, nil},
		{[]string{"help", "--json", "sitemap", "submit"}, []string{"sitemap submit"}, []string{"sitemap"}},
		{[]string{"help", "--json", "sitemap"}, []string{"sitemap submit", "sitemap approve", "sitemap status", "sitemap results", "sitemap list", "sitemap cancel"}, []string{"sitemap"}},
		{[]string{"trial", "--help", "--json"}, []string{"trial", "trial claim"}, nil},
		{[]string{"trial", "claim", "--poll", "--json", "-h"}, []string{"trial claim"}, nil},
		// It needs a key to run, but not to say how.
		{[]string{"integrations", "run", "reddit", "post", "--help", "--json"}, []string{"integrations run"}, nil},
		{[]string{"version", "--json", "-h"}, []string{"version"}, nil},
	} {
		described := describedBy(t, c.list...)
		var paths []string
		for _, command := range described.Commands {
			paths = append(paths, strings.Join(command.Path, " "))
		}
		if sets := slices.Sorted(maps.Keys(described.APIFields)); !slices.Equal(paths, c.paths) || !slices.Equal(sets, c.fieldOf) {
			t.Errorf("string %s: commands %q, apiFields %v", strings.Join(c.list, " "), paths, sets)
		}
	}
	if a, b := runOffline(t, "fetch", "--help", "--json"), runOffline(t, "help", "--json", "fetch"); a.output != b.output {
		t.Errorf("fetch --help --json and help --json fetch differ")
	}
	// The whole schema, wherever --json asks for help and names no command.
	for _, list := range [][]string{{"--json"}, {"help", "--json"}, {"--help", "--json"}, {"help", "--all", "--json"}, {"help", "exit-codes", "--json"}, {"help", "help", "--json"}} {
		if described := describedBy(t, list...); len(described.Commands) != len(leaves) {
			t.Errorf("string %s: %d commands, want %d", strings.Join(list, " "), len(described.Commands), len(leaves))
		}
	}
	// A command string doesn't have is an error, whose next step asks for the
	// schema of the command meant.
	got := runOffline(t, "help", "--json", "scrape")
	lines := strings.Split(strings.TrimSpace(got.errors), "\n")
	envelope, _ := decode(t, lines[len(lines)-1]).(map[string]any)
	fields, _ := envelope["error"].(map[string]any)
	if got.status != exitUsage || got.output != "" || fields["code"] != "unknown_command" || fields["next_step"] != "run `string fetch --help --json`" {
		t.Errorf("string help --json scrape: %+v", got)
	}
	if got := runOffline(t, "bogus", "--help", "--json"); got.status != exitUsage || got.output != "" {
		t.Errorf("string bogus --help --json: %+v", got)
	}
}

// spelledEachWay is a flag's name in camelCase, kebab-case and snake_case,
// when it has more than one word.
func spelledEachWay(name string) []string {
	var words []string
	start := 0
	for i, r := range name {
		if r == '-' || unicode.IsUpper(r) && i > 0 && !unicode.IsUpper(rune(name[i-1])) {
			words = append(words, strings.ToLower(name[start:i]))
			start = i
			if r == '-' {
				start++
			}
		}
	}
	words = append(words, strings.ToLower(name[start:]))
	if len(words) < 2 {
		return nil
	}
	camel := words[0]
	for _, word := range words[1:] {
		camel += strings.ToUpper(word[:1]) + word[1:]
	}
	return []string{camel, strings.Join(words, "-"), strings.Join(words, "_")}
}

// Every flag and request field that help --json gives a command parses on
// that command's line, in each of its spellings and under each alias.
func TestEveryDocumentedFlagParses(t *testing.T) {
	samples := map[string]string{"URL": "https://example.com", "QUERY": "coffee", "METHOD": "POST", "JOB_ID": "job-1"}
	valueOf := map[string]string{"int": "1", "number": "1", "string": "x", "json": "{}", "string|json": "x", "string[]": "x", "int[]": "1", "number[]": "1"}
	described := describedBy(t, "help", "--json")
	for _, c := range described.Commands {
		// integrations run reads the action's fields from the API first.
		if strings.Join(c.Path, " ") == "integrations run" {
			continue
		}
		line := slices.Clone(c.Path)
		for _, arg := range c.Args {
			if arg.Required {
				line = append(line, samples[strings.ToUpper(arg.Name)])
			}
		}
		flags := slices.Clone(c.Flags)
		if c.APIFields != "" {
			flags = append(flags, described.APIFields[c.APIFields].Fields...)
		}
		for _, f := range flags {
			for _, spelling := range slices.Concat([]string{f.Name}, spelledEachWay(f.Name), f.Aliases) {
				list := append(slices.Clone(line), "--"+spelling)
				switch {
				case f.Default != nil:
					list[len(list)-1] += "=false"
				case valueOf[f.Type] != "":
					list = append(list, valueOf[f.Type])
				}
				if got := runOffline(t, append(list, "--help")...); got.status != 0 || got.output != helpFor(c.Path[0]) {
					t.Errorf("string %s --help: exit %d, stderr %q", strings.Join(list, " "), got.status, got.errors)
				}
			}
		}
	}
	// The flags every command takes parse on any command line.
	for _, f := range globalFlagDocs {
		if f.name == "json" || f.name == "help" {
			continue
		}
		for _, spelling := range append([]string{f.name}, spelledEachWay(f.name)...) {
			list := []string{"search", "coffee", "--" + spelling}
			if f.value != "" {
				list = append(list, map[string]string{"errors": "json", "timeout": "5s"}[f.name])
			}
			if got := runOffline(t, append(list, "--help")...); got.status != 0 || got.output != helpFor("search") {
				t.Errorf("string %s --help: exit %d, stderr %q", strings.Join(list, " "), got.status, got.errors)
			}
		}
	}
}

// Every command has a page, every page its leaves, and what a page says of
// its leaves holds for them, so help --all and help --json say what the
// pages say.
func TestThePagesAndTheirLeavesAgree(t *testing.T) {
	var named []string
	for _, name := range commands {
		if name != "help" {
			named = append(named, name)
		}
	}
	if pages := slices.Sorted(maps.Keys(commandDocs)); !slices.Equal(pages, slices.Sorted(slices.Values(named))) {
		t.Errorf("the pages are %v, and the commands %v", pages, named)
	}
	var paths []string
	for name, page := range commandDocs {
		if len(page.leaves) == 0 {
			t.Errorf("the %s page has no leaves", name)
		}
		every := map[int]bool{}
		for _, l := range page.leaves {
			paths = append(paths, l.path)
			if l.words()[0] != name {
				t.Errorf("the %s page has the leaf %q", name, l.path)
			}
			for _, code := range l.exits {
				every[code] = true
			}
			usages := 0
			for _, line := range usageLines(page) {
				if mine, ok := owner(page, line); ok && mine.path == l.path {
					usages++
				}
			}
			if usages == 0 {
				t.Errorf("%s has no usage line on its page", l.path)
			}
			takes := map[string]bool{}
			for _, f := range append(slices.Clone(page.flags), page.fields...) {
				takes[f.name] = true
			}
			fields := 0
			for _, flag := range l.flags {
				if !takes[flag] {
					t.Errorf("%s takes --%s, which its page doesn't have", l.path, flag)
				}
				if slices.ContainsFunc(page.fields, func(f flagDoc) bool { return f.name == flag }) {
					fields++
				}
			}
			if fields != 0 && fields != len(page.fields) {
				t.Errorf("%s takes %d of its page's %d fields, want all or none", l.path, fields, len(page.fields))
			}
		}
		// Every flag on a page is one of its leaves'.
		for _, f := range page.flags {
			if !slices.ContainsFunc(page.leaves, func(l leaf) bool { return l.flags == nil || slices.Contains(l.flags, f.name) }) {
				t.Errorf("the %s page has --%s, which none of its leaves takes", name, f.name)
			}
		}
		// The exit codes a page names are its leaves' exit codes.
		for _, line := range page.returns {
			if !strings.HasPrefix(line, "exit") && !strings.HasPrefix(line, " ") {
				continue
			}
			for _, match := range regexp.MustCompile(`(?:^exit|·)\s+(\d+) `).FindAllStringSubmatch(line, -1) {
				if code, _ := strconv.Atoi(match[1]); !every[code] {
					t.Errorf("the %s page says it exits %d, which none of its leaves does", name, code)
				}
			}
		}
		// Every example on a page runs one of its leaves.
		for _, example := range page.examples {
			if _, ok := owner(page, uncommented(example)); !ok {
				t.Errorf("the %s page's example %q runs none of its leaves", name, example)
			}
		}
	}
	if len(slices.Compact(slices.Sorted(slices.Values(paths)))) != len(paths) || len(paths) != len(leaves) {
		t.Errorf("the leaves %v aren't each listed once", paths)
	}
	// string help exit-codes explains the exit codes help --all and help
	// --json list.
	var codes []int
	for _, match := range regexp.MustCompile(`\n  (\d+) `).FindAllStringSubmatch(helpTopics["exit-codes"], -1) {
		code, _ := strconv.Atoi(match[1])
		codes = append(codes, code)
	}
	var documented []int
	for _, e := range exitCodeDocs {
		documented = append(documented, e.code)
	}
	if !slices.Equal(codes, documented) {
		t.Errorf("string help exit-codes explains %v, and exitCodeDocs %v", codes, documented)
	}
}
