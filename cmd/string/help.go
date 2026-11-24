package main

import (
	"encoding/json"
	"maps"
	goruntime "runtime"
	"runtime/debug"
	"slices"
	"strings"
)

// helpAnywhere reads -h as --help, wherever it is before --.
func helpAnywhere(list []string) []string {
	end := slices.Index(list, "--")
	if end < 0 {
		end = len(list)
	}
	if !slices.Contains(list[:end], "-h") {
		return list
	}
	list = slices.Clone(list)
	for i := range end {
		if list[i] == "-h" {
			list[i] = "--help"
		}
	}
	return list
}

// versionCommand prints the version, then the commit and its date where the
// build recorded them, the system and the Go version.
func versionCommand(list []string, rt runtime) error {
	if done, err := noOptions(list, "version", rt); done {
		return err
	}
	build := struct {
		Version string `json:"version"`
		Commit  string `json:"commit,omitempty"`
		Date    string `json:"date,omitempty"`
		OS      string `json:"os"`
		Arch    string `json:"arch"`
		Go      string `json:"go"`
	}{Version: version, OS: goruntime.GOOS, Arch: goruntime.GOARCH, Go: goruntime.Version()}
	if info, ok := debug.ReadBuildInfo(); ok {
		modified := false
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				build.Commit = setting.Value
			case "vcs.modified":
				modified = setting.Value == "true"
			case "vcs.time":
				build.Date = setting.Value
			}
		}
		if modified && build.Commit != "" {
			build.Commit += "-modified"
		}
	}
	if rt.json {
		shown, _ := json.Marshal(build)
		return write(rt.stdout, append(shown, '\n'))
	}
	commit := "commit unknown"
	if build.Commit != "" {
		commit = "commit " + build.Commit
		if build.Date != "" {
			commit += " (" + build.Date + ")"
		}
	}
	return write(rt.stdout, []byte("string "+version+"\n"+commit+"\n"+build.OS+"/"+build.Arch+", "+build.Go+"\n"))
}

// commandDoc is one command's help page, in the order an agent reads it: how
// to call it, examples, what it prints and how it exits, what it costs, its
// flags, and where to read more. string COMMAND --help, -h anywhere on the
// line and string help COMMAND all print it, and none of them touches the
// network.
type commandDoc struct {
	summary string
	// usage are command lines, each perhaps with a note three spaces after
	// it. A line that starts with a space goes on with the line before it.
	usage    []string
	examples []string
	// returns are whole lines: what stdout holds, then the exit codes that
	// matter.
	returns []string
	cost    string
	// flags are the command's own flags, and fields the API's request fields
	// it takes, spelled as the API spells them. fieldsAs names the command
	// whose fields this one takes, so that its page points there.
	flags    []flagDoc
	fields   []flagDoc
	fieldsAs string
	more     string
	// leaves are the commands the page covers, in the order string help
	// --all lists them: the page's command itself, or each subcommand.
	leaves []leaf
}

// flagDoc is one flag on a command's page.
type flagDoc struct {
	// name is the spelling help shows, without the dashes.
	name string
	// short is a one-letter spelling, such as o for -o.
	short string
	// value is what follows the flag, such as N, FILE or [JSON]. It is empty
	// for a switch, and =false for a switch that is on unless turned off.
	value string
	// about says what the flag does, in one line or sentence that the page
	// wraps.
	about string
	// scope says which of the page's commands take it, as " (submit only)",
	// for the page to add after about. help --json, which gives each command
	// its own flags, leaves it out.
	scope string
}

// said is what the page says the flag does.
func (f flagDoc) said() string { return f.about + f.scope }

// spelled is the flag as its page shows it.
func (f flagDoc) spelled() string {
	spelled := "--" + f.name
	if f.short != "" {
		spelled = "-" + f.short + ", " + spelled
	}
	switch {
	case strings.HasPrefix(f.value, "="):
		spelled += f.value
	case f.value != "":
		spelled += " " + f.value
	}
	return spelled
}

// helpWidth is how wide a help line may be.
const helpWidth = 80

// termDoc is one row of an aligned list on a help page: a term, such as a
// flag or a variable, and what it means.
type termDoc struct{ term, about string }

// flagLines lays flags out as help pages do.
func flagLines(flags []flagDoc, at int) string {
	terms := make([]termDoc, len(flags))
	for i, f := range flags {
		terms[i] = termDoc{f.spelled(), f.said()}
	}
	return termLines(terms, at)
}

// termLines lays terms out as help pages do: each term from the third column,
// and what it means from column at, wrapped to helpWidth.
func termLines(terms []termDoc, at int) string {
	var b strings.Builder
	for _, t := range terms {
		line := "  " + t.term
		if len(line) >= at-1 {
			b.WriteString(line + "\n")
			line = ""
		}
		for _, word := range strings.Fields(t.about) {
			switch {
			case len(line) <= at:
				line += strings.Repeat(" ", at-len(line)) + word
			case len(line)+1+len(word) > helpWidth:
				b.WriteString(line + "\n")
				line = strings.Repeat(" ", at) + word
			default:
				line += " " + word
			}
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// page writes the help page of the command named name.
func (d commandDoc) page(name string) string {
	var b strings.Builder
	b.WriteString("string " + name + ": " + d.summary + "\n")
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		b.WriteString("\n" + title + "\n")
		for _, line := range lines {
			b.WriteString("  " + line + "\n")
		}
	}
	block := func(title, text string) {
		if text != "" {
			b.WriteString("\n" + title + "\n" + text)
		}
	}
	section("Usage", d.usage)
	section("Examples", d.examples)
	section("Returns", d.returns)
	section("Cost", strings.Split(d.cost, "\n"))
	if len(d.flags) > 0 {
		block("Flags", flagLines(d.flags, 27))
	}
	if d.fieldsAs != "" {
		block("API fields, spelled as in the API", "  The same as string "+d.fieldsAs+": string "+d.fieldsAs+" --help.\n")
	} else if len(d.fields) > 0 {
		block("API fields, spelled as in the API", flagLines(d.fields, 35))
	}
	b.WriteString("\nLearn more: " + d.more + "\n")
	return b.String()
}

// helpFor is the help page of command, or the help for all of string when
// command has none of its own.
func helpFor(command string) string {
	if doc, ok := commandDocs[command]; ok {
		return doc.page(command)
	}
	return help
}

// outputFlags are the flags of each command that sends one request and prints
// its response.
var outputFlags = []flagDoc{
	{name: "output", short: "o", value: "FILE", about: "write the response to FILE instead of stdout"},
	{name: "input", value: "FILE", about: "more request fields, from a JSON object in FILE or - for stdin"},
}

// exitsOfARequest are the exit codes of a command that sends one request.
var exitsOfARequest = []string{
	"exit     0 ok · 2 bad input · 3 needs a key · 4 out of credit · 5 rate limited",
	"         · 6 the site or String failed · 7 timed out",
}

const (
	keysDocs = "https://github.com/usestring/cli#keys"
)

var commandDocs = map[string]commandDoc{
	"fetch": {
		summary: "get a web page as Markdown",
		usage: []string{
			"string fetch URL [flags]",
			"string URL [flags]                     the same",
		},
		examples: []string{
			"string fetch https://example.com",
			"string fetch https://example.com --mainContentOnly -o page.md",
			"string fetch https://example.com --format json",
			"string fetch https://app.example.com --executeJS --waitUntil networkidle0",
		},
		returns: append([]string{
			"stdout   the page as Markdown, or the API's JSON with --format json or a field",
			"         only JSON carries: jsonSchema, screenshot, actions, captureXHR,",
			"         captureIframes or captureCookies",
		}, exitsOfARequest...),
		cost: "1 credit, 5 with --executeJS. Without a key: 50 requests and 100 credits a day\nper IP address, with --format and --executeJS only. Parts after the first,\nwith --offset, come from a local copy for 15 minutes and cost nothing.",
		flags: append([]flagDoc{
			{name: "max-tokens", value: "N", about: "print about N tokens of the page, cut at a line's end; stderr says which part it is and the next part's command"},
			{name: "offset", value: "N", about: "start the part N tokens into the page"},
		}, outputFlags...),
		fields: []flagDoc{
			{name: "format", value: "json|markdown|raw", about: "the response format"},
			{name: "markdownMode", value: "readable|full", about: "how much of the page the Markdown keeps"},
			{name: "mainContentOnly", about: "drop navigation, headers and footers"},
			{name: "executeJS", about: "render the page in a browser"},
			{name: "waitUntil", value: "STATE", about: "load, domcontentloaded, networkidle0 or networkidle2"},
			{name: "countryCode", value: "CC", about: "the proxy country, such as DE"},
			{name: "headers", value: "JSON", about: "request headers as a JSON object"},
			{name: "jsonSchema", value: "JSON|@./FILE", about: "extract into this JSON Schema (JSON output)"},
			{name: "screenshot", about: "capture a screenshot (JSON output)"},
			{name: "actions", value: "JSON|@./FILE", about: "browser actions to run (JSON output)"},
			{name: "captureXHR", value: "[JSON]", about: "return the page's XHR and fetch() calls (JSON output)"},
			{name: "captureIframes", value: "[JSON]", about: "list the page's iframes (JSON output)"},
			{name: "includeIframes", about: "inline each iframe's content"},
			{name: "captureCookies", about: "return the page's cookies (JSON output)"},
			{name: "requireWSS", about: "require a browser WebSocket fetch path"},
			{name: "solveCaptcha", value: "=false", about: "fail on a challenge instead of solving it"},
			{name: "ignoreCertificateErrors", about: "skip TLS certificate checks"},
			{name: "maxRedirects", value: "N", about: "follow at most N redirects"},
			{name: "followRedirects", value: "=false", about: "return the redirect response itself"},
		},
		more: "https://docs.usestring.ai/api-reference/fetch",
		leaves: []leaf{
			{path: "fetch", args: []string{"URL"}, aliases: []string{"URL"}, line: "a page as Markdown; -o FILE; --max-tokens N", costs: "1 credit, keyless ok", keyless: true, effects: "read", exits: requestExits, stdout: "text/markdown"},
		},
	},
	"request": {
		summary: "send a POST, PUT or PATCH to a URL through String",
		usage:   []string{"string request METHOD URL [--body TEXT | --body-file FILE] [flags]"},
		examples: []string{
			`string request POST https://api.example.com/orders --body '{"sku":"A1"}'`,
			"string request PUT https://api.example.com/items/7 --body-file item.json",
		},
		returns: append([]string{"stdout   the response as JSON"}, exitsOfARequest...),
		cost:    "Needs a key. Prices: https://docs.usestring.ai/get-started/pricing",
		flags: append([]flagDoc{
			{name: "body", value: "TEXT", about: "the request body, as written, @ included"},
			{name: "body-file", value: "FILE", about: "the request body, read from FILE, or - for stdin"},
		}, outputFlags...),
		fieldsAs: "fetch",
		more:     "https://docs.usestring.ai/api-reference/fetch",
		leaves: []leaf{
			{path: "request", args: []string{"METHOD", "URL"}, line: "a POST, PUT or PATCH through String", costs: "key", effects: "write", exits: requestExits, stdout: "application/json", json: true},
		},
	},
	"search": {
		summary: "search the web",
		usage:   []string{"string search QUERY [flags]"},
		examples: []string{
			`string search "postgres vacuum tuning"`,
			`string search "postgres vacuum tuning" --limit 3 --fields title,url`,
			`string search "coffee grinders" --dateRange week --searchCount 20`,
			`string search "ramen" --searchType places --location Tokyo`,
		},
		returns: append([]string{
			"stdout   the results as JSON: results (title, url, snippet), entity, places,",
			"         zeroResults",
		}, exitsOfARequest...),
		cost: "Each results page, about ten results, is billed as one search. Without a key a\nsearch costs 5 of the 100 credits a day per IP address, and takes --language,\n--dateRange, --sortBy, --safeSearch, --autocorrect and --verbatim only.",
		flags: append([]flagDoc{
			{name: "limit", value: "N", about: "print only the first N results; string cuts them, so it works without a key and costs nothing"},
		}, outputFlags...),
		fields: []flagDoc{
			{name: "engine", value: "NAME", about: "google (the default), duckduckgo, brave or google_ai_mode"},
			{name: "searchType", value: "TYPE", about: "web, images, videos, shopping, books, places or forums"},
			{name: "searchCount", value: "N", about: "how many results, 1 to 300 (--count works too)"},
			{name: "page", value: "N", about: "the results page to start from"},
			{name: "country", value: "CC", about: "the country to localize results for"},
			{name: "language", value: "CODE", about: "the result language, such as en or pt-br"},
			{name: "location", value: "PLACE", about: "where to search from, such as London"},
			{name: "coordinates", value: "JSON", about: "{\"latitude\":…,\"longitude\":…,\"radius\":…}"},
			{name: "dateRange", value: "RANGE", about: "hour, day, week, month, year, or {\"from\":…,\"to\":…}"},
			{name: "sortBy", value: "relevance|date", about: "the result order"},
			{name: "safeSearch", about: "filter out explicit results"},
			{name: "verbatim", about: "match the query's words exactly"},
			{name: "autocorrect", value: "=false", about: "search the query as typed"},
			{name: "includeOmittedResults", about: "include results hidden as similar"},
			{name: "restrictCountry", value: "CC", about: "only pages from this country"},
			{name: "aiOverview", about: "wait for the AI Overview too (costs 2x)"},
			{name: "format", value: "structured|raw", about: "results as JSON, or the results page as HTML"},
			{name: "solveCaptcha", value: "=false", about: "never solve a captcha for this search"},
		},
		more: "https://docs.usestring.ai/api-reference/search",
		leaves: []leaf{
			{path: "search", args: []string{"QUERY"}, line: "web results as JSON; --limit N --fields A,B", costs: "1 search, keyless ok", keyless: true, effects: "read", exits: requestExits, stdout: "application/json", json: true, records: "results"},
		},
	},
	"sitemap": {
		summary: "crawl one site: quote it, approve it, read its pages",
		usage: []string{
			"string sitemap submit URL [flags] [--approve] [--wait]",
			"string sitemap approve JOB_ID [--wait]",
			"string sitemap status JOB_ID [--wait]",
			"string sitemap results JOB_ID [--limit N] [--offset N]",
			"string sitemap list [--limit N] [--offset N]",
			"string sitemap cancel JOB_ID",
		},
		examples: []string{
			"string sitemap submit https://docs.example.com --maxPages 200",
			"string sitemap approve JOB_ID --wait",
			"string sitemap results JOB_ID --limit 100",
		},
		returns: []string{
			"stdout   JSON: the quote (jobId, estimatedCostUsd, estimatedPages), the job,",
			"         or its pages",
			"exit     0 ok · 1 the crawl failed or was canceled · 2 bad input",
			"         · 3 needs a key · 4 out of credit · 7 the wait stopped, and the crawl",
			"         keeps running",
		},
		cost: "submit quotes the crawl for nothing. approve, or submit --approve, starts it\nat the quoted cost, which --budgetUsd caps.",
		flags: []flagDoc{
			{name: "approve", about: "approve the quote at once", scope: " (submit only)"},
			{name: "wait", about: "wait for the crawl to end: 90s at most under an agent"},
			{name: "limit", value: "N", about: "how many results or jobs to show", scope: ", for results and list"},
			{name: "offset", value: "N", about: "how many to skip first", scope: ", for results and list"},
			{name: "output", short: "o", value: "FILE", about: "write the response to FILE instead of stdout"},
			{name: "input", value: "FILE", about: "more request fields, from a JSON object in FILE or - for stdin", scope: " (submit only)"},
		},
		fields: []flagDoc{
			{name: "maxPages", value: "N", about: "pages to crawl, 1 to 10,000 (10 by default)"},
			{name: "maxDepth", value: "N", about: "link depth from URL, 1 to 100 (2 by default)"},
			{name: "pathPrefix", value: "PATH", about: "only URLs whose path starts with PATH"},
			{name: "budgetUsd", value: "USD", about: "the spend cap (the quote by default)"},
			{name: "useSitemap", about: "also start from the site's /sitemap.xml"},
		},
		more: "https://docs.usestring.ai/api-reference/sitemap",
		leaves: []leaf{
			{path: "sitemap submit", args: []string{"URL"}, line: "quote a crawl; --approve starts it", costs: "key; spends on --approve", effects: "spend", flags: []string{"approve", "wait", "output", "input", "maxPages", "maxDepth", "pathPrefix", "budgetUsd", "useSitemap"}, exits: requestExits, stdout: "application/json", json: true},
			{path: "sitemap approve", args: []string{"JOB_ID"}, line: "start a quoted crawl; --wait", costs: "key; spends the quote", effects: "spend", flags: jobFlags, exits: requestExits, stdout: "application/json", json: true},
			{path: "sitemap status", args: []string{"JOB_ID"}, line: "a crawl's state; --wait follows it", costs: "key", effects: "read", flags: jobFlags, examples: []string{"string sitemap status JOB_ID --wait"}, exits: askExits, stdout: "application/json", json: true},
			{path: "sitemap results", args: []string{"JOB_ID"}, line: "a crawl's pages; --limit N --offset N", costs: "key", effects: "read", flags: listFlags, exits: askExits, stdout: "application/json", json: true, records: "urls"},
			{path: "sitemap list", line: "your crawls; --limit N --offset N", costs: "key", effects: "read", flags: listFlags, examples: []string{"string sitemap list --limit 20"}, exits: askExits, stdout: "application/json", json: true, records: "jobs"},
			{path: "sitemap cancel", args: []string{"JOB_ID"}, line: "stop a crawl", costs: "key", effects: "write", flags: []string{"output"}, examples: []string{"string sitemap cancel JOB_ID"}, exits: askExits, stdout: "application/json", json: true},
		},
	},
	"integrations": {
		summary: "run typed actions over one website, such as reddit post",
		usage: []string{
			"string integrations list                 every site and its actions",
			"string integrations list SITE            SITE's actions, each with its flags",
			"string integrations run SITE ACTION [--FIELD VALUE ...]",
			"string integrations                      the same as string integrations list",
		},
		examples: []string{
			"string integrations list reddit",
			"string integrations run reddit post --post https://www.reddit.com/r/technology/comments/1xapaw7/ --maxPages 2",
		},
		returns: []string{
			"stdout   list: the sites and actions, or the JSON catalog with --json. run:",
			"         JSON, with the rows in data and any input that gave none in failures",
			"exit     0 ok · 2 bad input · 3 needs a key · 4 out of credit · 6 the site or",
			"         String failed · 7 timed out",
		},
		cost: "list is free and needs no key. run needs one: most actions bill the requests\nthey make, some bill per row. Prices:\nhttps://docs.usestring.ai/get-started/pricing#site-integrations",
		flags: append([]flagDoc{
			{name: "FIELD", value: "VALUE", about: "a field of the action's input, spelled as the field: the post field is --post. A list field takes one item per flag, or a JSON array."},
		}, outputFlags...),
		more: "https://docs.usestring.ai/sites",
		leaves: []leaf{
			{path: "integrations list", args: []string{"[SITE]"}, aliases: []string{"integrations"}, line: "sites and their actions; SITE's flags", costs: "free, no key", keyless: true, effects: "read", flags: []string{}, exits: askExits, stdout: "text/plain", json: true, records: "sites"},
			{path: "integrations run", args: []string{"SITE", "ACTION"}, line: "one action; --FIELD VALUE per input field", costs: "key; per request or row", effects: "spend", exits: requestExits, stdout: "application/json", json: true, records: "data"},
		},
	},
	"init": {
		summary: "install the string-cli skill for your coding agents",
		usage: []string{
			"string init [--agent NAME[,NAME...]] [--no-mcp] [--no-login] [--browser]",
			"            [--device-name NAME]",
		},
		examples: []string{
			"string init",
			"string init --agent claude-code --no-login",
		},
		returns: []string{
			"stdout   where it installed the skill: ~/.agents/skills, which most agents",
			"         read, ~/.claude/skills when Claude Code is here, and its own for any",
			"         other agent here. Then, with no key set, the sign-in: a person at a",
			"         terminal waits for it; anyone else gets a trial key where String",
			"         offers one, or else the link and code",
			"exit     0 ok, whether or not the sign-in finishes · 2 bad input",
		},
		cost: "Free.",
		flags: []flagDoc{
			{name: "agent", value: "NAME[,NAME...]", about: "only these agents, each in its own skills directory, such as claude-code, codex, cursor or gemini-cli"},
			{name: "no-mcp", about: "don't add String's MCP server to Claude Code and Codex"},
			{name: "no-login", about: "skip the sign-in and the trial key"},
			{name: "browser", about: "sign in as string login --browser does"},
			{name: "device-name", value: "NAME", about: "the machine's name in the new key's name"},
		},
		more: "https://github.com/usestring/cli#agent-skills",
		leaves: []leaf{
			{path: "init", line: "the skill for your agents, MCP and sign-in", costs: "free", keyless: true, effects: "write", exits: basicExits, stdout: "text/plain"},
		},
	},
	"skill": {
		summary: "print the string-cli skill, the guide for coding agents",
		usage:   []string{"string skill [--path]"},
		examples: []string{
			"string skill                         the guide, to read or to save anywhere",
			"string skill --path                  where it is installed",
		},
		returns: []string{
			"stdout   the skill's SKILL.md, the one this release carries; with --path,",
			"         the SKILL.md of each copy in an agent's skills directory, one a line",
			"exit     0 ok · 2 bad input",
		},
		cost: "Free, and it sends no request.",
		flags: []flagDoc{
			{name: "path", about: "list the installed copies instead: the SKILL.md in each agent's skills directory that has the skill"},
		},
		more: "https://github.com/usestring/cli#agent-skills",
		leaves: []leaf{
			{path: "skill", line: "the guide for coding agents; --path", costs: "free", keyless: true, effects: "local", exits: basicExits, stdout: "text/markdown"},
		},
	},
	"completion": {
		summary: "print a script that completes string's commands and flags",
		usage:   []string{"string completion bash|zsh|fish|powershell"},
		examples: []string{
			"string completion bash >> ~/.bashrc",
			"string completion zsh >> ~/.zshrc",
			"string completion fish > ~/.config/fish/conf.d/string-cli.fish",
			"string completion powershell | Out-String | Invoke-Expression   in $PROFILE",
		},
		returns: []string{
			"stdout   the script for that shell. On each Tab it asks string what can come",
			"         next, so it completes what the string you run has",
			"exit     0 ok · 2 bad input",
		},
		cost: "Free, and it sends no request, nor does a Tab.",
		more: "https://github.com/usestring/cli#shell-completion",
		leaves: []leaf{
			{path: "completion", args: []string{"SHELL"}, line: "Tab completion: bash, zsh, fish, PowerShell", costs: "free", keyless: true, effects: "local", exits: basicExits, stdout: "text/plain"},
		},
	},
	"login": {
		summary: "sign in through your browser and save a new API key",
		usage: []string{
			"string login [--no-wait | --poll | --with-token] [--browser] [--env-file FILE]",
			"             [--device-name NAME]",
		},
		examples: []string{
			"string login",
			"string login --json                  for an agent: hand the link to the user",
			"string login --poll                  then save the key once they approve",
			"string login --with-token < key.txt",
		},
		returns: []string{
			"stdout   where to approve and the code; then, once the key is saved, the",
			"         account. With --json, verification_uri_complete, user_code,",
			`         expires_in and next; then {"status":"logged_in",…}`,
			"exit     0 ok · 2 bad input · 3 denied, expired or refused · 5 busy · 6 the",
			"         sign-in service failed · 7 the wait stopped, and the code still works",
		},
		cost: "Free.",
		flags: []flagDoc{
			{name: "no-wait", about: "print the link and code and exit; the default without a person at a terminal"},
			{name: "poll", about: "wait for the sign-in started earlier and save the key: 90s at most under an agent, --timeout to change it"},
			{name: "with-token", about: "read an API key from stdin, check it and save it"},
			{name: "browser", about: "sign in on a String page, with no code; it works for a new account too"},
			{name: "env-file", value: "FILE", about: "also write the key to FILE as its STRING_API_KEY line, keeping the file's other lines; a new file is readable only by you"},
			{name: "device-name", value: "NAME", about: "the machine's name in the new key's name, which your organization sees; by default the system and short hostname, as in Mac · work-laptop. Up to 51 characters"},
			{name: "no-open", about: "never open a browser"},
		},
		more: keysDocs,
		leaves: []leaf{
			{path: "login", line: "sign in, save a key; --json, then --poll", costs: "free", keyless: true, effects: "write", exits: askExits, stdout: "text/plain", json: true},
		},
	},
	"logout": {
		summary:  "delete the key that string login saved",
		usage:    []string{"string logout"},
		examples: []string{"string logout"},
		returns: []string{
			"stdout   that the saved key is gone from this machine; it still works until",
			"         you delete it at https://portal.usestring.ai/api-keys",
			"exit     0 ok",
		},
		cost: "Free.",
		more: keysDocs,
		leaves: []leaf{
			{path: "logout", line: "delete the saved key", costs: "free", keyless: true, effects: "write", exits: basicExits, stdout: "text/plain"},
		},
	},
	"whoami": {
		summary:  "say which key string uses, and check it with String",
		usage:    []string{"string whoami [--offline]"},
		examples: []string{"string whoami", "string whoami --offline"},
		returns: []string{
			"stdout   where the key comes from, STRING_API_KEY or the saved file, the",
			"         account it was saved for, the key's start, and whether String",
			"         accepts it. --json gives one object",
			"exit     0 String accepts the key · 3 no key, or String refused it · 6 string",
			"         couldn't ask String",
		},
		cost: "Free: one request that spends no credit, or none with --offline.",
		flags: []flagDoc{
			{name: "offline", about: "say where the key comes from, without asking String"},
		},
		more: keysDocs,
		leaves: []leaf{
			{path: "whoami", line: "the key in use, checked; --offline", costs: "free", keyless: true, effects: "read", exits: []int{0, 1, 2, 3, 6}, stdout: "text/plain", json: true},
		},
	},
	"trial": {
		summary: "get a trial key for an agent, with no account",
		usage: []string{
			"string trial",
			"string trial claim --email ADDRESS [--no-wait]",
			"string trial claim --poll",
		},
		examples: []string{
			"string trial",
			"string trial claim --email ADDRESS --json",
			"string trial claim --poll",
		},
		returns: []string{
			"stdout   that the trial key is saved. claim: the link and code for the person,",
			"         then that a key on their account replaced the trial key. --json gives",
			"         each as JSON",
			"exit     0 ok · 2 bad input · 3 no trial keys here, or the claim expired",
			"         · 7 the claim's wait stopped, and the code still works",
		},
		cost: "Free, after a few seconds' proof of work: the key comes with a small balance.",
		flags: []flagDoc{
			{name: "email", value: "ADDRESS", about: "who claims the agent onto their String account"},
			{name: "no-wait", about: "print the link and code and exit; the default without a person at a terminal"},
			{name: "poll", about: "wait for the claim started earlier and save the key: 90s at most under an agent, --timeout to change it"},
		},
		more: keysDocs,
		leaves: []leaf{
			{path: "trial", line: "a trial key when nobody can approve one", costs: "free", keyless: true, effects: "write", flags: []string{}, exits: askExits, stdout: "text/plain", json: true},
			{path: "trial claim", line: "move a trial agent to an account", costs: "free", keyless: true, effects: "write", exits: askExits, stdout: "text/plain", json: true},
		},
	},
	"status": {
		summary:  "show the version, the key in use, and what you can spend",
		usage:    []string{"string status"},
		examples: []string{"string status", "string status --json"},
		returns: []string{
			"stdout   the version and the key. With a key, the balance, any amount held by",
			"         running crawls, the plan and its request rate; without one, what is",
			"         left of today's keyless allowance. --json gives one object",
			"exit     0 ok, even when the API can't answer · 3 String refused the key",
		},
		cost: "Free, and it spends none of the keyless allowance.",
		more: "https://github.com/usestring/cli#commands",
		leaves: []leaf{
			{path: "status", line: "the key, balance or keyless allowance", costs: "free", keyless: true, effects: "read", exits: []int{0, 1, 2, 3}, stdout: "text/plain", json: true},
		},
	},
	"balance": {
		summary:  "print the balance, plan and auto top-up as JSON",
		usage:    []string{"string balance"},
		examples: []string{"string balance"},
		returns: []string{
			"stdout   the API's balance JSON, as it sends it",
			"exit     0 ok · 3 needs a key · 6 String failed · 7 timed out",
		},
		cost: "Free. Needs a key.",
		more: "https://github.com/usestring/cli#commands",
		leaves: []leaf{
			{path: "balance", line: "the balance as JSON", costs: "key", effects: "read", exits: askExits, stdout: "application/json", json: true},
		},
	},
	"update": {
		summary: "replace this copy of string with the latest release",
		usage: []string{
			"string update [--version X.Y.Z]",
			"string update --check",
		},
		examples: []string{"string update --check", "string update"},
		returns: []string{
			"stdout   the version it installed, once the release's signature and checksum",
			"         check out; for a copy from Homebrew, npm or Scoop, the command that",
			"         updates it",
			"exit     0 ok · 1 the update failed",
		},
		cost: "Free.",
		flags: []flagDoc{
			{name: "check", about: "show the version, the latest release, how this copy was installed and whether it updates itself"},
			{name: "version", value: "X.Y.Z", about: "install that release, which then stays until a plain string update"},
		},
		more: "https://github.com/usestring/cli#updating",
		leaves: []leaf{
			{path: "update", line: "install the latest release; --check", costs: "free", keyless: true, effects: "write", exits: basicExits, stdout: "text/plain", json: true},
		},
	},
	"version": {
		summary: "print this copy's version and build",
		usage: []string{
			"string version [--json]",
			"string --version, string -v           the version alone",
		},
		examples: []string{"string version", "string version --json"},
		returns: []string{
			"stdout   string VERSION, then the commit, its date, the system and the Go",
			"         version. --json gives one object",
			"exit     0",
		},
		cost: "Free, and it sends no request.",
		more: "https://github.com/usestring/cli#updating",
		leaves: []leaf{
			{path: "version", line: "the version and build; --json", costs: "free", keyless: true, effects: "local", exits: basicExits, stdout: "text/plain", json: true},
		},
	},
}

// envDocs are the variables string reads, as string help environment and
// string help --json list them.
var envDocs = []termDoc{
	{"STRING_API_KEY", "the API key; it wins over the key string login saved"},
	{"STRING_API_BASE_URL", "another API endpoint"},
	{"STRING_AGENT", "NAME says the coding agent NAME runs string, and human says a person does; it wins over what string finds"},
	{"AI_AGENT, AGENT", "an agent's name, as some agents set it"},
	{"STRING_NO_INPUT", "1 makes string wait for no person, as --no-input does"},
	{"STRING_AUTO_UPDATE", "0 turns off the daily update check, and the rewrite of the skill after an update"},
	{"STRING_DOWNLOAD_URL", "where string update takes releases from, for tests"},
	{"STRING_NO_DEPRECATION_WARNINGS", "1 silences what string says ahead of a change, such as the notice for fetch --json"},
	{"STRING_COMPAT", "0.2 will keep 0.2's output and exit codes once 0.4 changes them, and silences those notices now"},
	{"DO_NOT_TRACK", "1 sends String only string's version"},
	{"CI", "set to anything but false or 0: a CI job, where nobody approves a sign-in and nothing updates itself"},
	{"XDG_CONFIG_HOME", "where the saved key lives: $XDG_CONFIG_HOME/string, or ~/.config/string without it"},
	{"XDG_CACHE_HOME", "where fetch keeps a page read in parts for 15 minutes: $XDG_CACHE_HOME/string/fetch, or ~/.cache/string/fetch"},
	{"CLAUDE_CONFIG_DIR", "where init finds Claude Code's settings"},
	{"CODEX_HOME", "where init finds Codex's settings"},
}

// helpTopics are the pages of string help TOPIC that no one command owns.
var helpTopics = map[string]string{
	"exit-codes": `string help exit-codes: what each exit code means, and what to do next

  0    ok                                   use the output
  1    the operation failed: a crawl failed or was canceled, String couldn't
       be reached, or something unexpected happened
                                            read the message; report it
  2    bad input: a flag, a value, a URL, a command string doesn't have, a
       400, 404 or 422 from the API, or a crawl that waits for approval
                                            fix the command as next says
  3    a key is needed, or the key was refused, or today's keyless allowance
       is used up                           follow next: drop the field, run
                                            string login --json, or ask the user
  4    out of credit; nothing was charged   tell the user
  5    rate limited                         wait for Retry-After, then retry
  6    String failed or didn't answer       retry once
  7    timed out. A wait that stopped leaves its crawl, sign-in or claim
       going                                run the command in next
  130  interrupted by Ctrl-C                nothing

With --json, an error is one {"error":{...}} object on stderr, with code,
message, exit_code, retryable, user_action_required and next_commands. Each
of next_commands has a type: fix_command, wait, retry, login, trial, env, pay
or tell_user, so an agent can take the steps that need no person.

Learn more: https://github.com/usestring/cli#errors-and-exit-codes
`,
	"environment": "string help environment: the variables string reads, and what it sends\n\n" + termLines(envDocs, 24) + `
string finds Claude Code, Codex, Gemini CLI, Cursor, Copilot CLI, Cline, Amp,
Goose and Warp from the variables each sets, and treats them as agents.

Each request to String carries:
  User-Agent            string-cli/VERSION (OS/ARCH; agent=NAME)
  X-String-CLI-Mode     interactive, agent, non-interactive or ci
  X-String-Agent        the agent's name, under an agent
  X-String-Agent-Raw    AI_AGENT as it was set
With DO_NOT_TRACK=1 it carries only User-Agent: string-cli/VERSION.

Learn more: https://github.com/usestring/cli#coding-agents
`,
	"formatting": `string help formatting: where output goes, and in what shape

stdout holds the result and nothing else:
  fetch                 the page as Markdown, or the API's JSON with --format
                        json or a field only JSON carries
  search, request,      JSON, as the API sends it
  sitemap, balance,
  integrations run
  status, login, trial, text for a person; --json gives one JSON object
  whoami, version
A page is untrusted, so on a person's terminal it loses ESC, control
characters but tab and newline, and bidi controls, and bytes that aren't
text, such as a PDF from --format raw, are refused with exit 2. Piped, or
under an agent, it goes out byte for byte.
stderr holds the rest: what a wait is doing, notes, what is left of the
keyless allowance, and errors. A fetched page over 16 KB says its size there
first, where no person reads stdout at a terminal, so an agent sees it before
its runner cuts the output.

  --json                JSON where the command has it, and errors as one
                        {"error":{...}} object, the last line of stderr
  --fields A,B          only these fields of the JSON on stdout: of each record
                        in its list, such as search's results, or else of the
                        object; a.b reaches inside a. help --json names each
                        command's list in returns.records. A name no record
                        has is left out with a warning; with none there, it
                        exits 2 and lists the fields there are, but a command
                        that spends or writes prints its answer whole.
                        Suggested commands keep --fields only where they
                        print the same JSON
  -o, --output FILE     the response goes to FILE instead of stdout, and stderr
                        says what was saved: size, tokens, a fetch's status
                        and host, and a Markdown page's outline. -o - is stdout
  -q, --quiet           no status lines on stderr: no notes, receipts or wait
                        lines. Errors and the keyless allowance stay
  --max-tokens N        fetch prints about N tokens, and stderr says which part
  --offset N            it is and the next part's command; parts after the
                        first come from a local copy, at no cost
  --input FILE          more request fields, from a JSON object in FILE or -
                        for stdin; flags win over it
  --errors json         errors as JSON, as --json gives them, with stdout as it
                        is: Markdown from fetch stays Markdown
  --format              fetch's format: markdown, json or raw

What changes in 0.4, each with its notice or warning in 0.3:
  - fetch --json prints a JSON object: markdown, statusCode, finalUrl,
    tokens. --errors json keeps Markdown on stdout with JSON errors;
  - a fetch whose site answers 404 or 410 exits 8, and another error 6,
    with the page still printed; --no-fail keeps exit 0;
  - an API 404, such as an unknown job, exits 8, and a request that gets no
    answer exits 6.
STRING_COMPAT=0.2 will keep 0.2's behavior for all of them.

Exit codes: string help exit-codes.

Learn more: https://github.com/usestring/cli#commands
`,
	"agents": `string help agents: what changes when a coding agent runs string

Under an agent, string:
  - never opens a browser, and never waits for a person: login, init and
    trial claim print the link and code, and --poll waits for the approval;
  - stops --wait and --poll after 90s with exit 7, and next says how to go
    on; each request to String may take 90s too. --timeout sets another limit;
  - shows no update notices;
  - names the agent in each request: string help environment.
What it prints on stdout and its exit codes are the same as for anyone.

string finds the agent from the variables agents set. STRING_AGENT=NAME names
it instead, and STRING_AGENT=human says a person runs string.

Ways that work well:
  string fetch URL -o page.md           a big page goes to a file, not context
  string login --json                   give the user the link, then run
  string login --poll                   which saves the key once they approve
  string trial                          a key when nobody can approve one
  --json                                errors as one JSON object
  string help --all                     every command on one page, and
  string help --json                    each one's flags, output and exit codes
  string COMMAND --help                 one command's examples, output, exit
                                        codes, cost and flags
  string skill                          the whole guide for agents, the skill
                                        string init installs

Learn more: https://github.com/usestring/cli#coding-agents
`,
}

// agentPreamble starts the help for all of string under an agent. No command
// in it is split across lines.
const agentPreamble = `For agents: string help --all lists every command on one page, and
string help --json adds the flags, output and exit codes. One call is enough.
Pages that curl or WebFetch can't read: string fetch URL. Big pages: -o FILE.

`

// help is the help for all of string: how to start without a key, a line for
// each command from its page, and where the rest is, in at most 25 lines.
// Each command's page has its details, and string help --all every command.
var help = func() string {
	var b strings.Builder
	b.WriteString(`String Web Access CLI: web pages, search and crawls for agents and people

string fetch and string search work without a key, within a daily allowance per
IP address. Past it, and for request, sitemap, integrations run and balance, use
string login, or set STRING_API_KEY from https://portal.usestring.ai/api-keys.

Usage: string COMMAND [ARGS] [--FLAG VALUE ...], or string URL to fetch a page
`)
	var terms []termDoc
	at := 0
	for _, name := range commands {
		page, ok := commandDocs[name]
		if !ok {
			continue
		}
		term := name
		if len(page.leaves) == 1 {
			term = strings.TrimSpace(name + " " + strings.Join(page.leaves[0].args, " "))
		}
		terms = append(terms, termDoc{term, page.summary})
		at = max(at, len(term)+4)
	}
	b.WriteString(termLines(terms, at))
	b.WriteString(`
One command: string COMMAND --help. Every command on one page: string help --all
`)
	return b.String()
}()

// rootHelp is the help for all of string, which starts with a few lines for
// agents under one.
func rootHelp(rt runtime) string {
	if rt.exec.agent != "" {
		return agentPreamble + help
	}
	return help
}

// unknownTopic is the failure for string help WORD when WORD is no command
// but a near miss of a topic, which it names with switches, the global
// switches given.
func unknownTopic(word string, switches []string) error {
	if slices.Contains(commands, word) {
		return nil
	}
	topic, near := nearest(word, slices.Sorted(maps.Keys(helpTopics)))
	if !near {
		return nil
	}
	return &failure{exit: exitUsage, code: "unknown_command", message: "No help on " + word + ". Did you mean " + topic + "?", nextStep: "run `" + commandLine(withSwitches([]string{"help", topic}, switches)) + "`"}
}
