package main

import (
	"encoding/json"
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
	summary  string
	usage    []string
	examples []string
	// returns are whole lines: what stdout holds, then the exit codes that
	// matter.
	returns []string
	cost    string
	// flags and fields are aligned lines: the command's own flags, then the
	// API's request fields.
	flags  string
	fields string
	more   string
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
	block("Flags", d.flags)
	block("API fields, spelled as in the API", d.fields)
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

// exitsOfARequest are the exit codes of a command that sends one request.
var exitsOfARequest = []string{
	"exit     0 ok · 2 bad input · 3 needs a key · 4 out of credit · 5 rate limited",
	"         · 6 the site or String failed · 7 timed out",
}

const (
	outputFlags = `  -o, --output FILE        write the response to FILE instead of stdout
  --input FILE             more request fields, from a JSON object in FILE or - for stdin
`
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
		cost:  "1 credit, 5 with --executeJS. Without a key: 50 requests and 100 credits a day\nper IP address, with --format and --executeJS only.",
		flags: outputFlags,
		fields: `  --format json|markdown|raw       the response format
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
`,
		more: "https://docs.usestring.ai/api-reference/fetch",
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
		flags: `  --body TEXT              the request body, as written
  --body-file FILE         the request body, read from FILE
` + outputFlags,
		fields: "  The same as string fetch: string fetch --help.\n",
		more:   "https://docs.usestring.ai/api-reference/fetch",
	},
	"search": {
		summary: "search the web",
		usage:   []string{"string search QUERY [flags]"},
		examples: []string{
			`string search "postgres vacuum tuning"`,
			`string search "coffee grinders" --dateRange week --searchCount 20`,
			`string search "ramen" --searchType places --location Tokyo`,
		},
		returns: append([]string{
			"stdout   the results as JSON: results (title, url, snippet), entity, places,",
			"         zeroResults",
		}, exitsOfARequest...),
		cost:  "Each results page, about ten results, is billed as one search. Without a key a\nsearch costs 5 of the 100 credits a day per IP address, and takes --language,\n--dateRange, --sortBy, --safeSearch, --autocorrect and --verbatim only.",
		flags: outputFlags,
		fields: `  --engine NAME                    google (the default), duckduckgo, brave or google_ai_mode
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
`,
		more: "https://docs.usestring.ai/api-reference/search",
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
			"stdout   JSON: the quote (jobId, estimatedCostUsd, estimatedPages), the job, or",
			"         its pages",
			"exit     0 ok · 1 the crawl failed or was canceled · 2 bad input · 3 needs a key",
			"         · 4 out of credit · 7 the wait stopped, and the crawl keeps running",
		},
		cost: "submit quotes the crawl for nothing. approve, or submit --approve, starts it at\nthe quoted cost, which --budgetUsd caps.",
		flags: `  --approve                approve the quote at once (submit only)
  --wait                   wait for the crawl to end: 90s at most under an agent
  --limit N, --offset N    page through results and list
  -o, --output FILE        write the response to FILE instead of stdout
`,
		fields: `  --maxPages N                     pages to crawl, 1 to 10,000 (10 by default)
  --maxDepth N                     link depth from URL, 1 to 100 (2 by default)
  --pathPrefix PATH                only URLs whose path starts with PATH
  --budgetUsd USD                  the spend cap (the quote by default)
  --useSitemap                     also start from the site's /sitemap.xml
`,
		more: "https://docs.usestring.ai/api-reference/sitemap",
	},
	"integrations": {
		summary: "run typed actions over one website, such as reddit post",
		usage: []string{
			"string integrations [list]               every site and its actions",
			"string integrations list SITE            SITE's actions, each with its flags",
			"string integrations run SITE ACTION [--FIELD VALUE ...]",
		},
		examples: []string{
			"string integrations list reddit",
			"string integrations run reddit post --post https://www.reddit.com/r/technology/comments/1xapaw7/ --maxPages 2",
		},
		returns: []string{
			"stdout   list: the sites and actions, or the JSON catalog with --json. run: JSON,",
			"         with the rows in data and any input that gave none in failures",
			"exit     0 ok · 2 bad input · 3 needs a key · 4 out of credit · 6 the site or",
			"         String failed · 7 timed out",
		},
		cost: "list is free and needs no key. run needs one: most actions bill the requests\nthey make, some bill per row. Prices:\nhttps://docs.usestring.ai/get-started/pricing#site-integrations",
		flags: `  --FIELD VALUE            a field of the action's input, spelled as the field: the
                           post field is --post. A list field takes one item per
                           flag, or a JSON array.
` + outputFlags,
		more: "https://docs.usestring.ai/sites",
	},
	"init": {
		summary: "install the string-cli skill for the coding agents on this machine",
		usage: []string{
			"string init [--agent NAME[,NAME...]] [--no-mcp] [--no-login] [--browser]",
			"            [--device-name NAME]",
		},
		examples: []string{
			"string init",
			"string init --agent claude-code --no-login",
		},
		returns: []string{
			"stdout   what it installed and where, then the sign-in when no key is set: a",
			"         person at a terminal waits for it, anyone else gets the link and code",
			"exit     0 ok, whether or not the sign-in finishes · 2 bad input",
		},
		cost: "Free.",
		flags: `  --agent NAME[,NAME...]   only these agents: claude-code, codex, cursor,
                           gemini-cli or opencode
  --no-mcp                 don't add String's MCP server to Claude Code and Codex
  --no-login               skip the sign-in
  --browser                sign in as string login --browser does
  --device-name NAME       the machine's name in the new key's name
`,
		more: "https://github.com/usestring/cli#agent-skills",
	},
	"login": {
		summary: "sign in through your browser and save a new API key",
		usage: []string{
			"string login [--no-wait | --poll | --with-token] [--browser] [--env-file PATH]",
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
		flags: `  --no-wait                print the link and code and exit; the default without
                           a person at a terminal
  --poll                   wait for the sign-in started earlier and save the key:
                           90s at most under an agent, --timeout to change it
  --with-token             read an API key from stdin, check it and save it
  --browser                sign in on a String page, with no code; it works for a
                           new account too
  --env-file PATH          also write the key to PATH as its STRING_API_KEY line
  --device-name NAME       the machine's name in the new key's name, up to 51
                           characters
  --no-open                never open a browser
`,
		more: keysDocs,
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
	},
	"whoami": {
		summary:  "say which key string uses",
		usage:    []string{"string whoami"},
		examples: []string{"string whoami"},
		returns: []string{
			"stdout   where the key comes from, STRING_API_KEY or the saved file, and the",
			"         account it was saved for",
			"exit     0 a key is set · 3 none is",
		},
		cost: "Free, and it sends no request.",
		more: keysDocs,
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
			"exit     0 ok · 2 bad input · 3 no trial keys here, or the claim expired · 7 the",
			"         claim's wait stopped, and the code still works",
		},
		cost: "Free: the trial key comes with a small balance.",
		flags: `  --email ADDRESS          who claims the agent onto their String account
  --no-wait                print the link and code and exit; the default without
                           a person at a terminal
  --poll                   wait for the claim started earlier and save the key:
                           90s at most under an agent, --timeout to change it
`,
		more: keysDocs,
	},
	"status": {
		summary:  "show the version, the key in use, and what you can spend",
		usage:    []string{"string status"},
		examples: []string{"string status", "string status --json"},
		returns: []string{
			"stdout   the version and the key. With a key, the balance, any amount held by",
			"         running crawls, the plan and its request rate; without one, what is",
			"         left of today's keyless allowance. --json gives one object",
			"exit     0 ok, even when the API can't answer",
		},
		cost: "Free, and it spends none of the keyless allowance.",
		more: "https://github.com/usestring/cli#commands",
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
	},
	"update": {
		summary: "replace this copy of string with the latest release",
		usage: []string{
			"string update [--version X.Y.Z]",
			"string update --check",
		},
		examples: []string{"string update --check", "string update"},
		returns: []string{
			"stdout   the version it installed; for a copy from Homebrew, npm or Scoop, the",
			"         command that updates it",
			"exit     0 ok · 1 the update failed",
		},
		cost: "Free.",
		flags: `  --check                  show the version, the latest release, how this copy was
                           installed and whether it updates itself
  --version X.Y.Z          install that release, which then stays until a plain
                           string update
`,
		more: "https://github.com/usestring/cli#updating",
	},
	"version": {
		summary: "print this copy's version and build",
		usage: []string{
			"string version [--json]",
			"string --version, string -v           the version alone",
		},
		examples: []string{"string version", "string -v"},
		returns: []string{
			"stdout   string VERSION, then the commit, its date, the system and the Go",
			"         version. --json gives one object",
			"exit     0",
		},
		cost: "Free, and it sends no request.",
		more: "https://github.com/usestring/cli#updating",
	},
}
