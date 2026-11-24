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

- `string login` prints a code and, for a person at a terminal, opens String in the browser; `--no-open` stops that. Once you approve, String creates a new API key for that sign-in and the CLI saves it in `~/.config/string/credentials.json` (under `$XDG_CONFIG_HOME` when that is set), readable only by you. `string init` runs it for you when no key is set, unless you pass `--no-login`: a person at a terminal waits for the approval there, and under a coding agent, with `--no-input`, or when its output is captured outside CI, it first registers a trial key, as `string trial` below does, where the server offers one, and otherwise prints the link and code as `string login` does there. `--browser` makes that sign-in the one `string login --browser` gives, and `--device-name`, which names a sign-in's key, asks for the sign-in instead of the trial key. If that sign-in doesn't finish, init still succeeds and says how to sign in later. Under a coding agent, in CI, in a pipe, or with `--no-input` or `--no-wait`, `string login` prints the link and code and exits at once; `string login --poll` then waits for the approval and saves the key. Add `--json` to get both as JSON:

  ```bash
  string login --json          # {"verification_uri_complete":"…","user_code":"…","expires_in":600,"interval":5,"next":"string login --poll --json"}
  string login --poll --json   # after the person approves
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

`string whoami` shows which key commands use, the account it was saved for and the key's first characters, and checks it with String in one request that spends no credit. It exits 3 when String refuses the key, and 6 when it couldn't ask. `string whoami --offline` only says where the key comes from, as `whoami` did before. With `--json`, either one prints one JSON object; only the checked one has `accepted`. `string logout` deletes the saved key.

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

A page over 16 KB says its size on stderr before the page, wherever no person reads stdout at a terminal. It says what the agent running `string` keeps of output that long, and gives the same command with `-o page.md`. `-o FILE` checks the path before the request and then says what it saved:

```
saved page.md · 293 KB · ~75,000 tokens · 200 · en.wikipedia.org
  outline: 1 # List of countries by population · 618 ## Sovereign states · 3790 ## See also
```

The outline gives each of a Markdown page's headings after the line it starts on, so `sed -n 618,700p page.md` reads one section. `-o -` is stdout, and `-q` leaves status lines like these out of stderr.

`--max-tokens N` prints a page in parts of about N tokens, at four characters a token, each cut at a line's end. stderr says which part it is and gives the command for the next:

```
part 1 of 13 (tokens 0–6,000 of ~75,000). next: string fetch https://en.wikipedia.org/wiki/List_of_countries --max-tokens 6000 --offset 6000
```

The parts after the first come from a local copy that the first part saves for 15 minutes, under `$XDG_CACHE_HOME/string/fetch` or `~/.cache/string/fetch`. They send no request and cost nothing, keyless allowance included.

A field that only comes back in JSON, such as `--jsonSchema` or `--screenshot`, needs JSON output. With `--format markdown` or `--format raw` it exits 2 and says so, where the field used to be dropped without a word. For `request`, `--body-file -` reads the body from stdin. `--body` sends its text as written, so `--body @order.json` warns when `order.json` is a file, since `--body-file order.json` was likely meant.

A page is untrusted. On a person's terminal, string strips what could take the terminal over: escape sequences, control characters other than tab and newline, and bidi controls, which can make text read differently from its bytes. Bytes that aren't text, such as a PDF from `--format raw`, are refused there with exit 2 and `use -o FILE`. Piped, or under a coding agent, output goes out byte for byte.

A bare domain works too: `string example.com` fetches `https://example.com`. When `fetch`, `request` or `sitemap submit` gets a URL with no scheme, it uses `https://` and says so on stderr.

**Every API field is a flag of the same name.** A field from the [API reference](https://docs.usestring.ai/api-reference/fetch) works on the command line as written: `executeJS` is `--executeJS`, and `countryCode` is `--countryCode DE`. A boolean flag alone is true, and `=false` turns it off (`--solveCaptcha=false`). An object or array field takes JSON, or `@` and a file path (`--jsonSchema @./schema.json`). `string fetch --help`, `string search --help` and `string sitemap --help` list each command's fields. Every flag of more than one word also takes its kebab-case and snake_case spellings, so `--main-content-only` and `--main_content_only` are `--mainContentOnly`, and `--envFile` is `--env-file`. A near miss of a flag, such as `--mainContentOnyl`, gets the right name back, and so does a near miss of a command, or a word other tools use for one: `string scrape URL` answers `Did you mean fetch?` with `string fetch URL` as the next step, and exits 2 without running it.

`fetch` prints the page as Markdown. `--format json` gives the full response with the status, headers and final URL, and `--format raw` gives the response bytes. A field that only JSON carries, such as `--jsonSchema` or `--screenshot`, makes JSON the default. `request`, `search` and `sitemap` print JSON. `--output FILE`, or `-o FILE`, writes the response to a file instead.

`--input FILE` reads more fields from a JSON object, or `--input -` from stdin, and flags win over it. The command's URL, query and method win over both.

`--fields` keeps only the fields it names of the JSON a command prints, so an agent reads only what it needs. Where the JSON holds a list of records, the names apply to each record and the rest of the answer stays as it came. The lists are search's `results` (or `images`, `places` or `products` for those search types), a crawl's `urls`, `sitemap list`'s `jobs`, and the `sites` and `data` of integrations. Elsewhere the names apply to the object itself, and a dotted name reaches inside one:

```bash
string search "postgres vacuum tuning" --fields title,url
# {"results":[{"title":"…","url":"…"},…],"zeroResults":false}
string sitemap status JOB_ID --fields status,pagesProcessed
string integrations run reddit post --post URL --fields title,comments.score
```

`string help --json` names each command's list in `returns.records`. A name that no record has is left out, and stderr warns about it. When none of the names is there, the command prints nothing, exits 2 and lists the fields there are. A command that spends or writes, such as `integrations run`, has done so by then, so it prints its answer whole instead and says so on stderr: there is no need to run it again. `--fields` needs JSON on stdout, so before anything is sent it refuses a command that would print text: `fetch` needs `--format json`, and `status` or `login` need `--json`. Help is printed as it is.

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

`string --help`, or `string` alone, fits on one screen: what works without a key, a line for each command, and where the rest is. Each command has its own page, from `string COMMAND --help`, `-h` anywhere on the line, or `string help COMMAND`: examples, what it prints, its exit codes, what it costs, and its flags. `string help --all` puts every command on one page, a line each with what it costs, then the flags every command takes and what each does, the exit codes and the topics. `string help --json` gives all of it as one JSON document for programs and agents: each command's usage, arguments, flags and the API fields it takes, what it prints, what it costs, whether it reads, spends, writes or stays on this machine, and its exit codes. A flag that several commands take is described once, under `sharedFlags`. `string help --json COMMAND`, or `--json` on any `--help`, gives one command's part, with its examples and what each API field does. `string version` gives the version with the commit, its date, the system and the Go version, and `--json` gives them as one object; `string -v` prints the version alone. Help and version send no request. Four topics cover what no one command owns: `string help exit-codes`, `string help environment` (every variable string reads, such as `STRING_API_BASE_URL` for another API endpoint, and what each request tells String), `string help formatting` and `string help agents`.

## Errors and exit codes

An error goes to stderr as `error:` and one sentence. That's the API's own message, with the HTTP status, when the API sends one. It's never an HTML page, a raw answer or Go's words for a network failure. The details follow, indented: the `Retry-After`, keyless allowance and `x-request-id` headers when the API sends them. Then, after a blank line, `next:` gives the step out:

```
error: Keyless requests can't use mainContentOnly (HTTP 400)
  X-Keyless-Requests-Remaining: 41
  x-request-id: req_7f3a

next: run `string login` or set STRING_API_KEY
```

Add `--json` to any command to get the error as one JSON object on stderr instead. `--errors json` does the same and leaves stdout as it is, so `string fetch URL --errors json` still prints Markdown. From 0.4, `--json` on fetch prints a JSON object. 0.3 says so with a notice when it runs, and warns when a fetched site answers 404, 410 or another error, whose exit codes change in 0.4. `string help formatting` lists each change. `STRING_NO_DEPRECATION_WARNINGS=1` silences the notices, and `STRING_COMPAT=0.2` will keep 0.2's behavior:

```json
{"error":{"status":429,"code":"keyless_refused","exit_code":3,"message":"…","retryable":false,"retry_after_seconds":3600,"user_action_required":true,"request_id":"…","next_step":"run `string login` or set STRING_API_KEY","next_commands":[{"type":"login","command":"string login","argv":["string","login"]},{"type":"env","why":"set STRING_API_KEY to an API key from https://portal.usestring.ai/api-keys"}],"help_url":"https://usestring.ai/auth.md"}}
```

`exit_code`, `retryable`, `user_action_required` and `next_commands` are always there. `retryable` says whether running the same command again can help. `user_action_required` says a person must act: sign in, pay, approve a crawl, or let `string` out of a sandbox that blocks the network. Each entry of `next_commands` has a `type` an agent can choose by, without reading prose:

| `type` | The step |
| --- | --- |
| `fix_command` | run the corrected `command` |
| `wait` | run `command` to pick up a crawl, sign-in or claim that keeps going |
| `retry` | run the same command again, as `why` says |
| `login`, `trial` | get a key with `command` |
| `env` | set the variable `why` names |
| `pay` | pay for the call |
| `tell_user` | tell the person what `why` says |

`reason`, when there is one, is finer than `code`: a request that got no answer says `dns`, `connect`, `tls` or, inside a sandbox that blocks the network, `sandbox`. When the API sends `code`, `retryable` or `next_actions`, they are passed through.

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

## Shell completion

```bash
string completion bash >> ~/.bashrc
string completion zsh >> ~/.zshrc                                  # after compinit runs
command string completion fish > ~/.config/fish/conf.d/string-cli.fish
string completion powershell | Out-String | Invoke-Expression      # a line for $PROFILE
```

Tab then completes commands, subcommands and flags, the values a flag names, such as `--format json` or `init --agent claude-code,cursor`, and file names after `-o`, `--input`, `--body-file` and `--env-file`. A flag completes in the spelling you started it in, so `--main-c` becomes `--main-content-only`. On each Tab the script asks the `string` you run what can come next, so it completes what that version has, and needs no new script when `string` updates. A Tab sends no request and starts no update check. zsh needs its completion system on, as `autoload -Uz compinit && compinit` turns it on, and the script also works saved as `_string` in a directory on `$fpath`.

fish has a builtin of its own named `string`, which runs before any program of that name, so in fish run this CLI as `command string`. Tab completes it there, and at the first word it lists the builtin's subcommands beside the CLI's commands.

## Coding agents

`string` works out whether a coding agent runs it from the variables the agent sets for the commands it runs: `AI_AGENT`, or Claude Code's, Codex's, Gemini CLI's, Cursor's, OpenCode's, Goose's, Amp's, Cline's, GitHub Copilot's or Warp's own. `CLAUDECODE` and `CLAUDE_CODE_ENTRYPOINT` count only when stdin is not a terminal, since IDE terminals set them for people too, and `AGENT` comes last. `string` never guesses from the terminal type or from the processes around it. `STRING_AGENT=NAME` names the agent instead, and `STRING_AGENT=human` says a person runs `string`.

Under an agent, `string` never waits for a person: `login`, `init` and `trial claim` print the link and code and return, and only `--poll` waits for the approval. It also opens no browser and shows no update notices. What it prints on stdout and its exit codes are the same for an agent, a script and a person. Bare `string`, `string help` and `string --help` start with three lines for an agent: `string help --all` lists every command on one page and `string help --json` adds the flags, output and exit codes, so one call is enough, and `-o FILE` keeps a big page out of its context.

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

`skills/string-cli` is an [Agent Skills](https://agentskills.io) skill that teaches a coding agent the `string` commands: keyless first, big pages to a file or in parts, exit codes and `next_commands`, and what never to spend without asking. The native binary carries it, and `string skill` prints it, so an agent can read it without installing anything.

The skill's last section lists every command by what it can change: read, local (no request at all), spend or write. Its `allowed-tools` let Claude Code run the read and local ones without asking. That includes `-o FILE`, with which `fetch`, `search` and the `sitemap` reads save the response to whatever file the agent names. A command that spends, or changes a key, an install or a crawl, goes through Claude Code's usual permission check. Both come from the command table in `cmd/string/help.go`: after changing a command there, `go test ./cmd/string -run TestTheSkillListsEveryCommand -update` rewrites them.

`string init` installs it in two places that between them reach almost every coding agent:

- `~/.agents/skills`, which Codex, Cursor, Gemini CLI, OpenCode, GitHub Copilot, Goose, Droid and others read;
- `~/.claude/skills`, for Claude Code, when it is on the machine (under `$CLAUDE_CONFIG_DIR` when that is set).

An agent found on the machine that reads neither, such as Windsurf, Kiro CLI, Qwen Code, Grok Build, OpenClaw or Hermes Agent, also gets a copy in its own directory. Releases before 0.3 put a copy in each agent's directory. Gemini CLI reads both `~/.gemini/skills` and `~/.agents/skills` and warns of a skill conflict when it finds the skill twice, so `string init` removes its copy in `~/.gemini/skills` and says so. It removes only a plain copy as `string init` wrote it: a directory, not a link, with just the skill's `SKILL.md`. Anything else there stays, and init says why. The older copies for other agents stay where they are.

`--agent NAME` installs in exactly the agents it names, each in its own directory, and touches nothing else. That covers an agent version too old to read `~/.agents/skills`.

```bash
string init                            # ~/.agents/skills, ~/.claude/skills, and agents that read neither
string init --agent claude-code,codex  # only these, each in its own directory
string init --no-login                 # without signing in or a trial key, even at a terminal
string skill                           # print the skill
string skill --path                    # list the installed copies
npx skills add usestring/cli           # the same skill through the skills installer
```

The first command of a new release also rewrites the skill wherever it is already installed, however `string` was installed, and never installs it anywhere new. To move it where this release puts it, run `string init` again.

`string init` also adds String's [hosted MCP server](https://docs.usestring.ai/mcp/remote) to Claude Code and Codex as `string`, unless the server is already there or another server has that name. An entry for it under another name, such as `string-web-access`, stays as it is, and init prints that name for you to use in place of `string`. Both sign in to it with String, so no key goes into their settings: run `claude mcp login string` or `codex mcp login string` and choose the key each may use. For the other agents it found, init prints the setup page instead: most connect with an API key, which init never copies into another program's settings, and OpenCode signs in only from v2. `--no-mcp` skips this step.

## Project and releases

[usestring/cli](https://github.com/usestring/cli) is the canonical upstream for the native CLI, npm client, source releases, issues, the Homebrew formula and the Scoop manifest. CLI maintainers own release publication, and the release workflow updates the formula and the manifest; see [releasing the CLI](docs/releasing.md).

### Mutation testing

`scripts/mutation-test.sh` runs [gremlins](https://github.com/go-gremlins/gremlins) (pinned to v0.6.0) on one package. It plants small faults and reports which ones no test catches. The report has no pass threshold.

To run the release package check before every local push, enable the tracked Git hook once per clone.

```bash
git config --local core.hooksPath .githooks
```

If you already use custom hooks, call `scripts/mutation-test.sh ./internal/release` from your existing pre-push hook. The tracked hook prints the report or a warning and lets the push proceed. To check any package manually, run the script directly.

```bash
scripts/mutation-test.sh ./internal/release [--out report.json] [--workers 2]
```

Score only code where a missed fault does real damage, such as release signature checks, credentials and the output contract other tools read. Read each `LIVED` mutant there: either add an assertion to an existing test that kills it, or record why it is equivalent. A surviving mutant anywhere else is not a reason to write a test.
