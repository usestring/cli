# String Web Access CLI

Use String Web Access from a terminal. The CLI supports fetch, search, direct HTTP requests, and sitemap jobs. Responses go to stdout, so they can be piped to other commands.

**It works without a key.** Once `string` is [installed](#install), `string fetch` and `string search` run keyless, within a daily allowance per IP address, so the first call needs no account:

```bash
string search "string web access"
string https://example.com
```

For more, and for `request` and `sitemap`, sign in with `string login` or set `STRING_API_KEY`.

## Install

Pick whichever fits. Each installs the same native binary, `string`.

```bash
# macOS and Linux: installs string and adds the skill to your coding agents
curl -fsSL https://raw.githubusercontent.com/usestring/cli/main/install.sh | sh
brew tap usestring/cli https://github.com/usestring/cli && brew install usestring/cli/string

# Docker, e.g. in CI (pass -e STRING_API_KEY to use a key)
docker run --rm ghcr.io/usestring/cli search "your query"

# Anywhere with Node.js, once npm serves 0.2.0: installs string, adds the skill and signs you in
npx -y @usestring/cli init      # or: npm install -g @usestring/cli
```

```powershell
# Windows, in PowerShell: installs string and adds the skill to your coding agents
irm https://raw.githubusercontent.com/usestring/cli/main/install.ps1 | iex
scoop bucket add usestring https://github.com/usestring/cli; scoop install string
```

Until `npm view @usestring/cli version` prints 0.2.0 or later, `npx -y @usestring/cli` and `npm install -g @usestring/cli` get 0.1.0, the old Node CLI, which needs `STRING_API_KEY` and has none of the commands here.

Homebrew and Scoop install the release's own binary, so neither needs Go. If an npm installation already provides `string`, remove it with `npm uninstall -g @usestring/cli` first; each provides the same command. Upgrade with `brew update && brew upgrade usestring/cli/string`, or with `scoop update scoop string`.

`install.sh` and `install.ps1` put the binary in `~/.local/bin`, or `%USERPROFILE%\.local\bin` on Windows. `install.ps1` adds that directory to your PATH, and `install.sh` prints the line that adds it in zsh, bash or fish. Each checks the binary against the release checksums, then runs `string init` so your coding agents get the skill below. They download from `https://cli.usestring.ai`, and from the GitHub release whenever that host cannot serve the release. `STRING_VERSION`, such as `0.3.0`, installs that release instead of the latest and keeps the copy at it, and `STRING_INSTALL_DIR` puts the binary somewhere else. Pass `init` options after `sh -s --`, or `--no-init` to skip it. `irm | iex` takes no options, so in PowerShell set `$env:STRING_NO_INIT = 1` or `$env:STRING_INIT_AGENT` first, or run a saved `install.ps1` with `-NoInit`, `-Agent`, `-Version` and `-InstallDir`.

Each [GitHub release](https://github.com/usestring/cli/releases) also has the binaries for macOS, Linux and Windows, and `.deb`, `.rpm` and `.apk` packages for Debian, Fedora and Alpine. The packages are not signed yet, so `rpm -i` and a plain `apk add` refuse them. Install one with these commands from the directory you saved it in, with `arm64` in place of `amd64` on Arm:

```bash
sudo apt install ./string_*_linux_amd64.deb           # Debian and Ubuntu
sudo dnf install ./string_*_linux_amd64.rpm           # Fedora
apk add --allow-untrusted ./string_*_linux_amd64.apk  # Alpine, as root
```

## Keys

Without a key, `fetch` and `search` go to the keyless tier. Each IPv4 address, or IPv6 /64, gets 50 requests and 100 credits per UTC day: a plain fetch costs 1 credit, and a fetch with `executeJS` or a search costs 5. Keyless takes only `--format` and `--executeJS` on fetch, and `--language`, `--dateRange`, `--sortBy`, `--safeSearch`, `--autocorrect` and `--verbatim` on search. The CLI refuses anything else before sending it, and prints what is left of the allowance to stderr after each call.

A key lifts those limits and unlocks every option, `request` and `sitemap`. There are three ways to give the CLI one:

- `string login` prints a code and, for a person at a terminal, opens String in the browser; `--no-open` stops that. Once you approve, String creates a new API key for that sign-in and the CLI saves it in `~/.config/string/credentials.json` (under `$XDG_CONFIG_HOME` when that is set), readable only by you. `string init` runs it for you when no key is set, unless you pass `--no-login`: a person at a terminal waits for the approval there, and under a coding agent, with `--no-input`, or when its output is captured outside CI, it prints the link and code as `string login` does there. If that sign-in doesn't finish, init still succeeds and says how to sign in later. Under a coding agent, in CI, in a pipe, or with `--no-input` or `--no-wait`, `string login` prints the link and code and exits at once; `string login --poll` then waits for the approval and saves the key. Add `--json` to get both as JSON:

  ```bash
  string login --json   # {"verification_uri_complete":"…","user_code":"…","expires_in":600,"interval":5,"next":"string login --poll"}
  string login --poll   # after the person approves
  ```

  Where no person watches stderr at a terminal, `string login --poll` first prints what it waits for and the command that resumes it, then a line every 15 seconds:

  ```
  sign-in · waiting for approval · the code expires in 10m · resume: string login --poll
  waiting: not approved yet · 15s
  ```

  Under a coding agent it stops after 90 seconds, under the 2 minutes some agents allow a command. `--timeout DURATION` sets another limit for anyone, and `--timeout 0` none; otherwise it waits until the person approves or the code expires. A wait that reaches its limit exits 7 with code `wait_timeout`, and the code still works: run `string login --poll` again to go on with it.

  Add `--env-file .env` to `string login`, to `--poll` or to `--with-token` to also write the key to that file as its `STRING_API_KEY=` line, without printing it. Every other line stays as it was, and a new file is created readable only by you.

  When it asks for the key, `string login` sends String a name for the machine it runs on: its system and the first part of its hostname, as in `Mac · work-laptop`. String puts it in the name of the key it creates, `String CLI · Mac · work-laptop`, so your organization's [API keys page](https://portal.usestring.ai/api-keys) shows which machine holds which key. To send another name, add `--device-name NAME`, of up to 51 characters, to `string login`, to `--no-wait` or `--poll`, or to `string init`. `string login --browser` and `--with-token` send no name.

  `string login --browser` needs no code. It prints a link to a String page, and opens it for a person at a terminal; there you sign in or create an account and click Authorize. The link works for 15 minutes. `--no-wait`, `--poll`, `--json` and `--env-file` work with it as above, and `string init --browser` signs in the same way.
- `string login --with-token` reads a key from [String's API keys page](https://portal.usestring.ai/api-keys) on stdin, checks it with one free API call, and saves it in the same file. The key stays out of your shell history: `string login --with-token < key.txt`.
- `STRING_API_KEY` in the environment. It wins over a saved key, and the CLI never writes it to a file:

  ```bash
  export STRING_API_KEY="your-api-key"
  ```

`string whoami` shows which key commands use, and `string logout` deletes the saved one.

An agent working with nobody present can register for a trial key instead, where the server offers it. `string trial` solves a few seconds' proof of work and saves a key with a small balance in the same file. The key doesn't expire; it works until its credit runs out. Beside it, in `trial.json`, it keeps a claim token, which a person can use for 7 days to adopt the agent onto their String account. Neither is printed. Where trial keys are switched off, `string trial` exits 3.

`string trial claim --email ADDRESS` starts that claim. It prints a link and a code for the person at that address, who signs in to String with that email and enters the code. The CLI then saves a key on their account in place of the trial key, which stops working, and deletes `trial.json`. Without a person at a terminal, or with `--no-wait`, it prints the link and code and exits, and `string trial claim --poll` waits for the person and saves the key. `--json` gives both as JSON. That wait stops as `string login --poll` does, after 90 seconds under a coding agent or at `--timeout`, with exit 7, and `string trial claim --poll` goes on while the code works.

`string status` shows the version and the key in use. With a key, it adds the balance left, any amount held for crawls still running, the plan and its request rate, and auto top-up. Without one, it shows what the last keyless call from this machine left of today's allowance, kept in `~/.config/string/keyless.json`, so checking spends none of it. `--json` gives the same as one JSON object.

`string balance` prints the account's balance as JSON, exactly as `GET /account/balance` returns it, including `remaining`, `held`, `exhausted`, `asOf`, the `plan` and `autoTopUp`. It needs a key. `string status` still exits 0 when it can't read the balance; `string balance` fails with one of the [exit codes](#errors-and-exit-codes) instead, so a script can rely on its output:

```bash
string balance | jq .remaining
```

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

**Every API field is a flag of the same name.** A field from the [API reference](https://docs.usestring.ai/api-reference/fetch) works on the command line as written: `executeJS` is `--executeJS`, and `countryCode` is `--countryCode DE`. A boolean flag alone is true, and `=false` turns it off (`--solveCaptcha=false`). An object or array field takes JSON, or `@` and a file path (`--jsonSchema @./schema.json`). `string fetch --help`, `string search --help` and `string sitemap --help` list each command's fields, and a near miss such as `--max-pages` gets the right name back.

`fetch` prints the page as Markdown. `--format json` gives the full response with the status, headers and final URL, and `--format raw` gives the response bytes. A field that only JSON carries, such as `--jsonSchema` or `--screenshot`, makes JSON the default. `request`, `search` and `sitemap` print JSON. `--output FILE`, or `-o FILE`, writes the response to a file instead.

`--input FILE` reads more fields from a JSON object, or `--input -` from stdin, and flags win over it. The command's URL, query and method win over both.

A sitemap crawl starts with a quote, which costs nothing. `--approve` starts the crawl at the quoted cost, and `--wait` waits for it to finish:

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

`--wait` first prints the job and the command that resumes the wait to stderr:

```
job JOB_ID · running · up to $1.05 · resume: string sitemap status JOB_ID --wait
waiting: running · 409 pages done, 641 queued · 15s
```

Then it shows the crawl's progress. A person at a terminal sees one line redrawn in place. Anywhere else, a plain line comes every 15 seconds, which shows progress in CI logs and tells an agent the command is still alive.

Under a coding agent the wait stops after 90 seconds, under the 2 minutes some agents allow a command. `--timeout DURATION` sets another limit for anyone, and `--timeout 0` none; otherwise a person or a script waits until the crawl ends. A wait that reaches its limit exits 7 with code `wait_timeout`, prints the last status on stdout, and names the command that resumes it. So does a status this version of `string` doesn't know. The crawl keeps running either way, as it does when you interrupt the wait, and `string sitemap status JOB_ID --wait` picks it back up.

## Site integrations

A [site integration](https://docs.usestring.ai/sites) is a set of typed actions over one website. For example, `reddit post` reads a post with its whole comment tree, and `yelp business` reads business records. An action takes an input and returns rows in a fixed shape. Most actions bill the requests they make, like fetches. Some bill per row returned instead: the Reddit actions do, and each post and each comment is a row, so one large thread can cost far more than a fetch. Cap a per-row call with the action's paging or limit flag, such as `--maxPages` on `reddit post`. The response's `billing` block shows how a call was charged, and the [pricing page](https://docs.usestring.ai/get-started/pricing#site-integrations) has the rates.

```bash
string integrations                     # every site and its actions; no key needed
string integrations list reddit         # reddit's actions, each with its flags and an example
string integrations run reddit post --post https://www.reddit.com/r/technology/comments/1xapaw7/ --maxPages 2
```

**Each flag is a field of the action's input, spelled exactly as the field.** Reddit's `post` action takes `post`, `sort` and `maxPages`, so its flags are `--post`, `--sort` and `--maxPages`, the same names the integration docs and the manifest use. A list field takes one item per flag (`--items a --items b`) or a JSON array. `run` prints the JSON response: `data` holds the rows, and `failures` lists any input that produced none. `list` needs no key; `run` needs one.

`string --help` lists the commands and the `STRING_API_BASE_URL` setting for a different API endpoint. Each command has its own page too, from `string COMMAND --help`, `-h` anywhere on the line, or `string help COMMAND`: examples, what it prints, its exit codes, what it costs, and its flags. `string version` gives the version with the commit, its date, the system and the Go version, and `--json` gives them as one object; `string -v` prints the version alone. Help and version send no request.

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

Each request to String has a time limit: 120 seconds, 300 for `integrations run`, and 90 for any request under a coding agent, which stays under the 2 minutes some agents allow a command. `--timeout DURATION` sets another, as `90s`, `5m` or a number of seconds, and `--timeout 0` sets none. With `--wait` or `--poll`, it limits the whole wait instead (see [sitemap crawls](#commands) and [keys](#keys)). A request that runs past its limit, or whose answer stops arriving for 60 seconds, exits 7 with code `timeout`, and the error suggests a longer `--timeout`.

Ctrl-C prints what it leaves behind: a crawl or a sign-in that keeps going, with the command that picks it up, or a request that may already have been processed. Under `--json` that is the error envelope, with code `interrupted`. `string` then ends the way SIGINT ends a program, so the shell sees 130 and a script that runs `string` stops too. On Windows it exits 130.

## Updating

```bash
string update                  # the latest release
string update --version 0.3.1  # exactly this release, kept until a plain string update
string update --check          # the version, the latest release, and whether this copy updates itself
```

From 0.3.0, `string update` puts the latest release in place of the binary that runs it, once the release's signature and checksum check out. Homebrew, npm and Scoop keep track of the copies they installed, so for one of those `string update` changes nothing and prints the command that updates it, such as `brew upgrade usestring/cli/string`. `--check --json` gives the same report as one JSON object.

A copy that the install script installed checks for a new release once a day and installs it in the background after a command has finished, without changing that command's output or exit code. The next command a person runs at a terminal says it did. Any other copy only tells a person at a terminal that a release is out and how to update. Notices need both stdout and stderr to be a terminal, and never show under a [coding agent](#coding-agents). The daily check asks `https://cli.usestring.ai/stable` which release is the latest, and fetches a release's manifest, signature and archive from the same host only to install it. Each of these requests carries the same `User-Agent` as [every request to String](#coding-agents): the version of `string`, the system and the CPU type, as in `string-cli/0.3.0 (linux/amd64)`, and under a coding agent the agent's name. Nothing else goes with them: no key, no account, none of the `X-String` headers and no other detail of the machine.

`STRING_AUTO_UPDATE=0` (or `false`, `off`, `no`) turns off everything `string` does by itself: the daily check, the background install, the notices and the [skill refresh](#agent-skills). `string update` still works when you run it, and `string init` refreshes the skill by hand. A `CI` variable turns them off too, unless it is `false` or `0`. The Docker image sets `STRING_AUTO_UPDATE=0`, since every run in a fresh container would otherwise look like the first of the day.

## Coding agents

`string` works out whether a coding agent runs it from the variables the agent sets for the commands it runs: `AI_AGENT`, or Claude Code's, Codex's, Gemini CLI's, Cursor's, OpenCode's, Goose's, Amp's, Cline's, GitHub Copilot's or Warp's own. `CLAUDECODE` and `CLAUDE_CODE_ENTRYPOINT` count only when stdin is not a terminal, since IDE terminals set them for people too, and `AGENT` comes last. `string` never guesses from the terminal type or from the processes around it. `STRING_AGENT=NAME` names the agent instead, and `STRING_AGENT=human` says a person runs `string`.

Under an agent, `string` never waits for a person: `login`, `init` and `trial claim` print the link and code and return, and only `--poll` waits for the approval. It also opens no browser and shows no update notices. What it prints on stdout and its exit codes are the same for an agent, a script and a person.

`--no-input`, or `STRING_NO_INPUT=1`, does the same for anyone who sets it, such as a script run from a terminal: `string` then waits for no person. `-y` and `--yes` are taken by every command, to confirm what it would ask first; none asks yet.

Each request to String's API, portal and sign-in service says how `string` runs:

| Header | What it carries |
| --- | --- |
| `User-Agent` | `string-cli/0.3.0 (linux/amd64; agent=claude-code)` under an agent, `string-cli/0.3.0 (linux/amd64)` otherwise |
| `X-String-CLI-Mode` | `interactive` for a person at a terminal, `agent`, `non-interactive` or `ci` |
| `X-String-Agent` | the agent's name, such as `claude-code`, only under an agent |
| `X-String-Agent-Raw` | `AI_AGENT` as the agent set it, such as `claude-code_2-1-322_agent`, when that is how `string` found the agent |

String uses them to tell people, scripts, CI and each agent apart in its own request logs. `string` sends nothing else about where it runs, and no events of its own. With `DO_NOT_TRACK=1`, requests carry only `string-cli/<version>` and none of the `X-String` headers. The download host gets only the `User-Agent`, as [Updating](#updating) describes.

## Agent skills

`skills/string-cli` is an [Agent Skills](https://agentskills.io) skill that teaches a coding agent the `string` commands. The native binary carries it, and `string init` installs it into `~/.agents/skills` (read by Codex and others) and into every agent it finds on the machine: Claude Code, Cursor, Gemini CLI, OpenCode, GitHub Copilot, Windsurf, Goose, Droid, Kiro CLI, Qwen Code, Grok Build, OpenClaw and Hermes Agent. Run it again after an upgrade to update the skill.

The first command of a new release also rewrites the skill wherever it is already installed, however `string` was installed, and never installs it anywhere new.

```bash
string init                            # the shared directory and every agent found
string init --agent claude-code,codex  # only these
string init --no-login                 # without signing in, even at a terminal
npx skills add usestring/cli           # the same skill through the skills installer
```

`string init` also adds String's [hosted MCP server](https://docs.usestring.ai/mcp/remote) to Claude Code and Codex as `string`, unless the server is already there or another server has that name. An entry for it under another name, such as `string-web-access`, stays as it is, and init prints that name for you to use in place of `string`. Both sign in to it with String, so no key goes into their settings: run `claude mcp login string` or `codex mcp login string` and choose the key each may use. For the other agents it found, init prints the setup page instead: most connect with an API key, which init never copies into another program's settings, and OpenCode signs in only from v2. `--no-mcp` skips this step.

## Project and releases

[usestring/cli](https://github.com/usestring/cli) is the canonical upstream for the native CLI, npm client, source releases, issues, the Homebrew formula and the Scoop manifest. CLI maintainers own release publication, and the release workflow updates the formula and the manifest; see [releasing the CLI](docs/releasing.md).
