# String Web Access CLI

Use String Web Access from a terminal. The CLI supports fetch, search, direct HTTP requests, and sitemap jobs. JSON responses go to stdout, so they can be piped to other commands.

**It works without a key.** `string fetch` and `string search` run keyless, within a daily allowance per IP address, so the first call needs no account:

```bash
npx -y @usestring/cli search "string web access"
npx -y @usestring/cli fetch https://example.com --format markdown
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

Without a key, `fetch` and `search` go to the keyless tier. Each IPv4 address, or IPv6 /64, gets 50 requests and 100 credits per UTC day: a plain fetch costs 1 credit, and a fetch with `executeJS` or a search costs 5. Keyless takes only `--format` and `executeJS` on fetch and `--language` (plus `dateRange`, `sortBy`, `safeSearch`, `autocorrect` and `verbatim` through `--input`) on search. The CLI refuses anything else before sending it, and prints what is left of the allowance to stderr after each call.

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

### Build from source

```bash
CGO_ENABLED=0 go build -trimpath -o string ./cmd/string
go test ./...
```

## Commands

```bash
string fetch https://example.com
string fetch https://example.com --format markdown
string search "coffee shops in Austin" --engine google --count 20
string request POST https://example.com/api --body 'hello'
```

`fetch` and `request` use JSON output by default. Use `--format markdown` for page text or `--format raw` for response bytes. `search` and `sitemap` return JSON.

For API options that do not have a named flag, put them in a JSON object and pass `--input FILE`. The command's URL, query, and method take precedence over matching fields in the file. Use `--input -` to read JSON from stdin.

```bash
printf '%s\n' '{"executeJS":true,"countryCode":"GB"}' > fetch-options.json
string fetch https://example.com --input fetch-options.json
```

For a JSON request body, put the object and destination headers in the input file:

```bash
printf '%s\n' '{"body":{"name":"example"},"headers":{"Content-Type":"application/json"}}' > request-options.json
string request POST https://example.com/api --input request-options.json
```

Sitemap jobs require a separate approval after the quote. Submitting a job does not start a crawl or charge the account.

```bash
string sitemap submit https://example.com
string sitemap approve JOB_ID
string sitemap status JOB_ID
string sitemap results JOB_ID --limit 100 --offset 0
string sitemap list
string sitemap cancel JOB_ID
```

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

## Project and releases

[usestring/cli](https://github.com/usestring/cli) is the canonical upstream for the native CLI, npm client, source releases, issues, and Homebrew formula. CLI maintainers own release publication and the matching formula update; see [releasing the CLI](docs/releasing.md).
