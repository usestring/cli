# String Web Access CLI

Use String Web Access from a terminal. The CLI supports fetch, search, direct HTTP requests, and sitemap jobs. JSON responses go to stdout, so they can be piped to other commands.

## Install

### Homebrew

Install the native CLI from this repository's first-party tap:

```bash
brew tap usestring/cli https://github.com/usestring/cli.git
brew install usestring/cli/string
```

This builds the Go CLI without a Node.js runtime dependency. Go is needed only while building. This tap is maintained by String; it is separate from Homebrew's official `homebrew/core` repository.

If an npm installation already provides `string`, remove it with `npm uninstall -g @usestring/cli` before installing the Homebrew formula. Both packages provide the same command.

Upgrade with:

```bash
brew update
brew upgrade usestring/cli/string
```

### npm

Requires Node.js 22 or newer:

```bash
npm install -g @usestring/cli
```

To try the source package before release, run `bun run build` in this directory, then `npm install -g .`.

Create an API key in [String settings](https://portal.usestring.ai/settings) and set it in your shell:

```bash
export STRING_API_KEY="your-api-key"
```

The CLI reads the key from the environment and does not save it to a file.

### Native binary

`cmd/string` is the same CLI in Go, built as one static binary with no Node.js dependency. It takes the same commands, flags and environment variables, and gives the same output and exit codes. Stable source releases and SHA-256 checksums are available from [GitHub releases](https://github.com/usestring/cli/releases). Standalone binary downloads are not published; build one from a checkout with Go 1.25 or newer:

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

## Project and releases

[usestring/cli](https://github.com/usestring/cli) is the canonical upstream for the native CLI, npm client, source releases, issues, and Homebrew formula. CLI maintainers own release publication and the matching formula update; see [releasing the CLI](docs/releasing.md).
