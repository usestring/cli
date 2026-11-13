# String Web Access CLI

Use String Web Access from a terminal. The CLI supports fetch, search, direct HTTP requests, and sitemap jobs. Responses go to stdout, so they can be piped to other commands.

**It works without a key.** `string fetch` and `string search` run keyless, within a daily allowance per IP address, so the first call needs no account:

```bash
npx -y @usestring/cli search "string web access"
npx -y @usestring/cli https://example.com
```

For more, and for `request` and `sitemap`, sign in with `string login` or set `STRING_API_KEY`.

## Install

Pick whichever fits. Each installs the same native binary, `string`.

```bash
# Anywhere with Node.js: installs string, adds the skill to your coding agents and signs you in
npx -y @usestring/cli init      # or: npm install -g @usestring/cli

# macOS and Linux
curl -fsSL https://raw.githubusercontent.com/usestring/cli/main/install.sh | sh
brew tap usestring/cli https://github.com/usestring/cli && brew install usestring/cli/string

# Docker, e.g. in CI (pass -e STRING_API_KEY to use a key)
docker run --rm ghcr.io/usestring/cli search "your query"
```

Homebrew builds the binary from the release's source archive, so Go is needed only while it builds. If an npm installation already provides `string`, remove it with `npm uninstall -g @usestring/cli` first; both provide the same command. Upgrade with `brew update && brew upgrade usestring/cli/string`.

`install.sh` puts the binary in `~/.local/bin` (`STRING_INSTALL_DIR` changes it), checks it against the release checksums, then runs `string init` so your coding agents get the skill below. Pass `init` options after `sh -s --`, or `--no-init` to skip it. Each [GitHub release](https://github.com/usestring/cli/releases) also has the binaries for macOS, Linux and Windows, and `.deb`, `.rpm` and `.apk` packages for Debian, Fedora and Alpine.

## Keys

Without a key, `fetch` and `search` go to the keyless tier. Each IPv4 address, or IPv6 /64, gets 50 requests and 100 credits per UTC day: a plain fetch costs 1 credit, and a fetch with `executeJS` or a search costs 5. Keyless takes only `--format` and `--executeJS` on fetch, and `--language`, `--dateRange`, `--sortBy`, `--safeSearch`, `--autocorrect` and `--verbatim` on search. The CLI refuses anything else before sending it, and prints what is left of the allowance to stderr after each call.

A key lifts those limits and unlocks every option, `request` and `sitemap`. There are three ways to give the CLI one:

- `string login` prints a code and opens String in your browser. Once you approve and choose an API key, the CLI saves it in `~/.config/string/credentials.json` (under `$XDG_CONFIG_HOME` when that is set), readable only by you. `string init` runs it for you when you run init from a terminal with no key set. Without a terminal, as under a coding agent, or with `--no-wait`, `string login` prints the link and code and exits at once; `string login --poll` then waits for the approval and saves the key. Add `--json` to get both as JSON:

  ```bash
  string login --json   # {"verification_uri_complete":"…","user_code":"…","expires_in":600,"interval":5,"next":"string login --poll"}
  string login --poll   # after the person approves
  ```
- `string login --with-token` reads a key from [String settings](https://portal.usestring.ai/settings) on stdin, checks it with one free API call, and saves it in the same file. The key stays out of your shell history: `string login --with-token < key.txt`.
- `STRING_API_KEY` in the environment. It wins over a saved key, and the CLI never writes it to a file:

  ```bash
  export STRING_API_KEY="your-api-key"
  ```

`string whoami` shows which key commands use, and `string logout` deletes the saved one.

An agent working with nobody present can register for a trial key instead, where the server offers it. `string trial` solves a few seconds' proof of work and saves a key with a small balance that works for 24 hours, in the same file. Beside it, in `trial.json`, it keeps a claim token, which a person can use for 7 days to adopt the agent onto their String account. Neither is printed. Where trial keys are switched off, `string trial` exits 3.

`string status` shows the version and the key in use. With a key, it adds the balance left, any amount held for crawls still running, the plan and its request rate, and auto top-up. Without one, it shows what the last keyless call from this machine left of today's allowance, kept in `~/.config/string/keyless.json`, so checking spends none of it. `--json` gives the same as one JSON object.

### Build from source

```bash
CGO_ENABLED=0 go build -trimpath -o string ./cmd/string
go test ./...
```

## Commands

```bash
string https://example.com                              # the page as Markdown
string https://example.com --mainContentOnly -o example.md
string fetch https://example.com --format json          # the full response: status, headers, final URL
string search "coffee shops in Austin" --searchCount 20
string search "coffee grinders" --dateRange week --sortBy date
string request POST https://example.com/api --body '{"name":"example"}' --headers '{"Content-Type":"application/json"}'
```

**Every API field is a flag of the same name.** A field from the [API reference](https://portal.usestring.ai/docs/api-reference/fetch) works on the command line as written: `executeJS` is `--executeJS`, and `countryCode` is `--countryCode DE`. A boolean flag alone is true, and `=false` turns it off (`--solveCaptcha=false`). An object or array field takes JSON, or `@` and a file path (`--jsonSchema @./schema.json`). `string fetch --help`, `string search --help` and `string sitemap --help` list each command's fields, and a near miss such as `--max-pages` gets the right name back.

`fetch` prints the page as Markdown. `--format json` gives the full response with the status, headers and final URL, and `--format raw` gives the response bytes. A field that only JSON carries, such as `--jsonSchema` or `--screenshot`, makes JSON the default. `request`, `search` and `sitemap` print JSON. `--output FILE`, or `-o FILE`, writes the response to a file instead.

`--input FILE` reads more fields from a JSON object, or `--input -` from stdin, and flags win over it. The command's URL, query and method win over both.

A sitemap crawl starts with a quote, which costs nothing. `--approve` starts the crawl at the quoted cost, and `--wait` waits for it to finish, with a progress line on a terminal:

```bash
string sitemap submit https://example.com --maxPages 50 --approve --wait
string sitemap results JOB_ID --limit 100 --offset 0
```

Step by step, the same crawl is:

```bash
string sitemap submit https://example.com --maxPages 50   # the quote
string sitemap approve JOB_ID --wait
string sitemap status JOB_ID
string sitemap list
string sitemap cancel JOB_ID
```

Interrupting `--wait` leaves the crawl running, and `string sitemap status JOB_ID --wait` picks it back up.

## Site integrations

A [site integration](https://portal.usestring.ai/docs/sites) is a set of typed actions over one website. For example, `reddit post` reads a post with its whole comment tree, and `yelp business` reads business records. An action takes an input and returns rows in a fixed shape, and its requests run and are billed like fetches.

```bash
string integrations                     # every site and its actions; no key needed
string integrations list reddit         # reddit's actions, each with its flags and an example
string integrations run reddit post --post https://www.reddit.com/r/technology/comments/1xapaw7/ --maxPages 2
```

**Each flag is a field of the action's input, spelled exactly as the field.** Reddit's `post` action takes `post`, `sort` and `maxPages`, so its flags are `--post`, `--sort` and `--maxPages`, the same names the integration docs and the manifest use. A list field takes one item per flag (`--items a --items b`) or a JSON array. `run` prints the JSON response: `data` holds the rows, and `failures` lists any input that produced none. `list` needs no key; `run` needs one.

`string --help` lists the commands and the `STRING_API_BASE_URL` setting for a different API endpoint.

## Errors and exit codes

Errors go to stderr, followed by the `Retry-After`, keyless allowance and `x-request-id` headers when the API sends them, and the next step. Add `--json` to any command to get the error as one JSON object on stderr instead:

```json
{"error":{"status":429,"code":"keyless_refused","message":"…","retry_after_seconds":3600,"request_id":"…","next_step":"run `string login` or set STRING_API_KEY","help_url":"https://usestring.ai/auth.md"}}
```

When the API sends `code`, `retryable` or `next_actions`, they are passed through.

| Exit | Meaning |
| --- | --- |
| 0 | OK |
| 1 | Any other failure, such as a network error |
| 2 | Bad input or usage |
| 3 | Needs a key, or the key was refused (401, 403, or a keyless refusal) |
| 4 | Out of credit (402) |
| 5 | Rate limited (429) |
| 6 | Upstream failure (5xx) |
| 7 | Timeout |
| 130 | Interrupted |

## Agent skills

`skills/string-cli` is an [Agent Skills](https://agentskills.io) skill that teaches a coding agent the `string` commands. The native binary carries it, and `string init` installs it into `~/.agents/skills` (read by Codex and others) and into every agent it finds on the machine: Claude Code, Cursor, Gemini CLI, OpenCode, GitHub Copilot, Windsurf, Goose, Droid, Kiro CLI, Qwen Code, Grok Build, OpenClaw and Hermes Agent. Run it again after an upgrade to update the skill.

```bash
string init                            # the shared directory and every agent found
string init --agent claude-code,codex  # only these
npx skills add usestring/cli           # the same skill through the skills installer
```

`string init` also adds String's [hosted MCP server](https://portal.usestring.ai/docs/mcp/remote) to Claude Code and Codex as `string-web-access`, unless one is already there. Both sign in to it with String, so no key goes into their settings: run `claude mcp login string-web-access` or `codex mcp login string-web-access` and choose the key each may use. For the other agents it found, init prints the setup page instead: most connect with an API key, which init never copies into another program's settings, and OpenCode signs in only from v2. `--no-mcp` skips this step.

## Project and releases

[usestring/cli](https://github.com/usestring/cli) is the canonical upstream for the native CLI, npm client, source releases, issues, and Homebrew formula. CLI maintainers own release publication and the matching formula update; see [releasing the CLI](docs/releasing.md).
