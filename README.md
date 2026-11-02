# String Web Access CLI

Use String Web Access from a terminal. The CLI supports fetch, search, direct HTTP requests, and sitemap jobs. JSON responses go to stdout, so they can be piped to other commands.

## Install

Pick whichever fits. Each installs the same native binary, `string`.

```bash
# macOS and Linux
brew install usestring/tap/string
curl -fsSL https://raw.githubusercontent.com/usestring/cli/main/install.sh | sh

# Anywhere with Node.js
npx -y @usestring/cli init      # or: npm install -g @usestring/cli

# Windows
scoop bucket add usestring https://github.com/usestring/scoop-bucket
scoop install string
winget install usestring.string
```

`install.sh` puts the binary in `~/.local/bin` (`STRING_INSTALL_DIR` changes it), checks it against the release checksums, then runs `string init` so your coding agents get the skill below. Pass `init` options after `sh -s --`, or `--no-init` to skip it. Release binaries are also attached to each [GitHub release](https://github.com/usestring/cli/releases).

Sign in with `string login`. It prints a code and opens String in your browser. Once you approve and choose an API key, the CLI saves that key in `~/.config/string/credentials.json` (under `$XDG_CONFIG_HOME` when that is set), readable only by you. `string whoami` shows which key commands use, and `string logout` deletes the saved one.

You can also create an API key in [String settings](https://portal.usestring.ai/settings) and set it in your shell. `STRING_API_KEY` takes precedence over a saved key, and the CLI never writes it to a file:

```bash
export STRING_API_KEY="your-api-key"
```

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

`string --help` lists the commands and the `STRING_API_BASE_URL` setting for a different API endpoint. HTTP errors go to stderr and return a nonzero exit status.

## Agent skills

`skills/string-cli` is an [Agent Skills](https://agentskills.io) skill that teaches a coding agent the `string` commands. The native binary carries it, and `string init` installs it into `~/.agents/skills` (read by Codex and others) and into every agent it finds on the machine: Claude Code, Cursor, Gemini CLI, OpenCode, GitHub Copilot, Windsurf, Goose, Droid, Kiro CLI, Qwen Code, Grok Build, OpenClaw and Hermes Agent. Run it again after an upgrade to update the skill.

```bash
string init                            # the shared directory and every agent found
string init --agent claude-code,codex  # only these
npx skills add usestring/cli           # the same skill through the skills installer
```
