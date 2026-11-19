# Releasing the CLI

A release is a signed `v*` tag on `main`. Pushing it runs `.github/workflows/release.yml`, whose jobs run in this order:

1. **check**, in the `release` environment, before anything is built. It refuses a tag that is not `v` and a version (`v0.3.0`, or `v0.3.0-rc.1` for a pre-release), a tag whose commit is not on `main`, and a commit without `install.sh` and `install.ps1`. It runs `go run ./tools/release keys`, which fails unless `RELEASE_SIGNING_KEY` is one of the keys in that commit's `internal/release/keys.go` and `RELEASE_SIGNING_KEY_PREVIOUS`, when set, is a key. It also fails unless the R2 keys can read the bucket. Each failure names what to fix.
2. **release** tests, then builds with GoReleaser:
   - the GitHub release: archives for every platform, `checksums.txt`, `.deb`, `.rpm` and `.apk` packages, and the source archive `string-X.Y.Z.tar.gz`;
   - `ghcr.io/usestring/cli`, tagged with the version and `latest`, for linux/amd64 and linux/arm64.

   A pre-release becomes a GitHub pre-release, which `releases/latest` passes over, and leaves the image's `latest` tag where it was.
3. **npm** (`NPM_TOKEN`) publishes a package for each platform, then `@usestring/cli`, which installs the one that fits. It skips a pre-release.
4. **publish**, in the `release` environment, puts the release on the download host, `https://cli.usestring.ai`. It downloads the GitHub release and builds `manifest.json`, which lists each platform's archive with its size and SHA-256. It signs the manifest into `manifest.json.sig` and checks both as an installed copy would, then attaches them to the GitHub release. Last, it uploads every asset and the two manifest files to `/vX.Y.Z/`, with a cache header that keeps them for a year, and reads each one back through the host.
5. **smoke**, on Linux, macOS and Windows, each on Intel and on Arm, installs the release from the host with the tag's own installer and runs `string update --check`. When `/stable` names another version, it also installs that one and runs `string update --version X.Y.Z`.
6. **promote**, in the `stable` environment, runs for a version without a pre-release suffix. It first decides whether `/stable` may move to the version: on a tag push, only forward. Then it commits `Formula/string.rb` and `bucket/string.json` for the version to `main`, through GitHub's API, which signs the commit. Last, it uploads the tag's `install.sh` and `install.ps1` to the host and points `/stable` at the version.
7. **homebrew** and **scoop** run after promote. homebrew installs the release with Homebrew on macOS, checks that Homebrew lists it as the version, and runs the formula's test. scoop installs it with Scoop on Windows, on Intel and on Arm. Each runs its script in `tools/release`, which the packages workflow also runs on pull requests, on the tap or bucket at the commit promote made, and checks `string --version`.
8. **live** runs when promote moved `/stable` forward. On each of the six machines it installs the version `/stable` named before, with the hosted installer as people do. Then it runs a plain `string update` and requires the new version.

Copies of string installed by the installers check `/stable` once a day and update themselves to it, so promote is what ships a release to them. Copies installed with Homebrew or Scoop check it too, and then print the command that updates them, which is why promote commits the formula and the Scoop manifest before it moves `/stable`. The scripts and `/stable` are served with a five-minute cache header.

Each file under `/vX.Y.Z/` is published once and never changes. publish skips a file the host already has with the same bytes, as on a re-run, and stops on one with other bytes. The bucket's lock on the prefix `v` refuses to replace one in any case. If a release needs a correction, publish a new version.

## Before the first release

- The repository has two environments, `release` and `stable`, each limited to tags matching `v*` (in Settings > Environments, selected branches and tags, with the one tag rule `v*`). Each holds four secrets for the bucket behind the host: `R2_ENDPOINT`, `R2_BUCKET`, `R2_ACCESS_KEY_ID` and `R2_SECRET_ACCESS_KEY`, the key pair of an R2 API token that can read and write objects in that bucket alone.
- `release` also holds `RELEASE_SIGNING_KEY`, and `internal/release/keys.go` on `main` holds the two public keys, as [Signing keys](#signing-keys) says. Until it does, every release stops at check.
- npm needs `NPM_TOKEN`, as [npm](#npm) says.
- promote commits to `main` itself, with the workflow's own token. `main` has no branch protection or ruleset today. A rule there that requires pull requests or status checks would refuse that commit, and promote would stop before it moves `/stable`.

GitHub runs the workflow file in the tag's own commit, so this workflow releases a tag only when that commit of `main` has this workflow, both installers and `tools/release`. A tag on an older commit runs the workflow that commit has, which releases to GitHub and npm with none of check's tests, no signed manifest and nothing on the host, so never tag one. GitHub refuses a run by hand on such a tag, since its workflow has no `workflow_dispatch`. Releases made before this workflow stay on GitHub, where the installers find them.

## Who can reach the release secrets

An environment limited to `v*` tags gives its secrets to any workflow run on such a tag, and that run uses the workflow file at the tag's commit. So anyone who can push a `v*` tag to this repository can read the signing key and the R2 keys with a commit of their own. check's test that the tag is on `main` runs inside that same file, so it stops honest mistakes only. Two settings close this, and an admin decides whether to turn them on. The workflow works with either, both or neither:

- a tag ruleset on `v*` that lets only admins create, move or delete those tags;
- required reviewers on the `release` and `stable` environments. With reviewers on `release`, each release asks for approval twice, at check and at publish. With reviewers on `stable`, it asks once more, at promote.

## npm

The npm job ends by marking the old Node CLI, every version below 0.2.0, deprecated. It uses the repository secret `NPM_TOKEN`, a granular access token from an npm account that can publish the CLI's packages:

- **Packages and scopes:** read and write on the whole npm scope, not only on packages that exist, so the token can also create a platform package the first time it is published.
- **Bypass two-factor authentication:** on, because a workflow cannot answer a 2FA prompt.
- **Expiration:** write tokens expire, so check the secret is still current before tagging.

Without the secret, the npm job fails and the rest of the release still goes out. Set the secret, then choose **Re-run failed jobs** on the workflow run. The job skips any version npm already has, so a re-run after a partial publish finishes the rest.

## Making a release

Every release is public, so it needs explicit approval for that version before the tag is pushed. Check the build locally first; nothing here publishes:

```bash
goreleaser release --snapshot --clean
node npm/build.mjs dist npm-dist
```

Check browser sign-in as well, against production, from a build of the commit you will tag. A release can't be changed, and a client that the sign-in service doesn't accept fails only after the person approves. Use a new account, one that has never signed in with the CLI and has never connected an MCP client: an account that has done either can sign in where a new one fails. Run the check from a terminal, where `./string login` waits for the approval. Without one, as under a coding agent or in CI, it prints the link and code and exits, and `./string login --poll` then waits for the approval and saves the key. Leave `STRING_API_KEY` unset, so that `string status` reads the key that sign-in saved:

```bash
go build -o string ./cmd/string
./string login     # approve in the browser; without a terminal, run ./string login --poll as well
./string status    # the Key line reads "saved by string login", and there is a balance
```

Tag only when both succeed. Then, from an up-to-date `main`:

```bash
git tag -s v0.3.0 -m v0.3.0
git push origin v0.3.0
```

Never replace a release asset or move a published tag; publish a new version if a correction is needed.

## A rehearsal with a pre-release

A tag such as `v0.3.0-rc.1` runs everything up to smoke. The GitHub release is a pre-release, npm and the image's `latest` tag are left alone, and the host gets `/v0.3.0-rc.1/`. `/stable` does not move. Install the pre-release by naming it, with the tag's own installer, since the host serves `/install.sh` and `/install.ps1` only once a promote has run:

```bash
curl -fsSL https://raw.githubusercontent.com/usestring/cli/v0.3.0-rc.1/install.sh | STRING_VERSION=0.3.0-rc.1 sh
```

Before the first release, the pointer can be rehearsed too, by running the workflow by hand on the pre-release's tag:

```bash
gh workflow run release.yml --ref v0.3.0-rc.1 -f promote=true
```

That publishes the tag's release again (skipping what the host has), installs it, uploads the tag's installers and points `/stable` at it. This works only while `/stable` is missing or names a pre-release; once a release has been promoted, promote refuses a pre-release. The usual one-liner never installs a pre-release from `/stable`; it goes to the GitHub release instead. To install through the pointer, as a copy that updates itself would, name the host: `STRING_DOWNLOAD_URL=https://cli.usestring.ai`.

## Moving /stable back

```bash
gh workflow run release.yml --ref v0.3.0 -f promote=true
```

A run by hand builds nothing: after check, it starts at publish, on the tag's existing release. With `promote=true` it points the formula, the Scoop manifest and `/stable` at that version, even when that moves them back. It commits the two files as that tag's `tools/release` writes them, so if `tools/release` has changed since the tag, main's tests fail on them until a pull request rewrites them with `go test ./tools/release -run 'Formula|Scoop' -update`. New installs, and copies that have not updated yet, then get 0.3.0. Copies already on the newer version stay there: a copy never moves back by itself, and neither Homebrew nor Scoop moves back a copy it installed. The real fix for a bad release is a new, higher version.

## When a job fails

Each job is safe to re-run, and **Re-run failed jobs** runs the failed ones again along with those after them.

- **check** names what it found: a secret that is not set, a signing key that `keys.go` at the tag's commit does not have, a tag that is not on `main`. Fix it and re-run. A tag on a commit that is not on `main`, or that lacks an installer, cannot be released; tag a commit of `main` with a new version.
- **release**: if GoReleaser stopped before it made the GitHub release, re-run it. If it stopped after, do not repair that release; publish a new version.
- **publish**: re-run. If it says a file is published already with other bytes, something changed the GitHub release after it was published; publish a new version.
- **smoke** or **live** on macOS, cancelled at its limit with no step run: GitHub gave the job no machine. On 18 November 2026, 3 of 8 first attempts of this repository's `macos-latest` jobs went that way, each cancelled at exactly its 15-minute limit (runs 44378214776, 44386964726 and 44389965308), and each re-run got a machine within 6 to 30 seconds. Re-running the failed jobs is the fix, and it is safe. A longer limit would only delay the re-run, so each macOS job in this workflow stops at 20 minutes.
- **promote** on a tag push whose version is lower than the one `/stable` names, such as a patch to an older line, fails with "a tag push only moves it forward". The release is on GitHub and on the host, where anyone can install it by naming it, and `/stable` needs nothing. The other `latest` pointers did move to it: GitHub's latest release, which the installers fall back to, the image's `latest` tag and npm's `latest`. Point each back at the newest release, here 0.4.0:

  ```bash
  gh release edit v0.4.0 --latest --repo usestring/cli
  docker buildx imagetools create --tag ghcr.io/usestring/cli:latest ghcr.io/usestring/cli:0.4.0
  npm dist-tag add @usestring/cli@0.4.0 latest
  ```

- **promote**, at its commit to `main`: when `main` moves on between reading it and committing, promote reads it again and retries, up to five tries. Any other refusal fails at once with GitHub's answer in the log, before `/stable` moves. Fix the cause and re-run. If the workflow cannot commit to `main` at all, commit the two files by pull request instead, written from the release's own `checksums.txt`, then re-run promote. It finds them on `main` and goes on to move `/stable`:

  ```bash
  dir=$(mktemp -d)
  gh release download v0.3.0 --repo usestring/cli --pattern checksums.txt --dir "$dir"
  go run ./tools/release formula "$dir" 0.3.0 > Formula/string.rb
  go run ./tools/release scoop "$dir" 0.3.0 > bucket/string.json
  ```

- **promote**, otherwise: re-run. A re-run keeps each file on `main` that installs the version's archives with the release's checksums, so a fix made on `main` to the rest of either file stays. Otherwise it commits the tag's file in its place, and when both files stay it makes no commit. A file installs the same when `go run ./tools/release installs` prints the same for it as for the tag's: the version, then each archive's URL with its SHA-256. A file it cannot read counts as different. Then promote uploads the installers and `/stable` again. A re-run after it had already moved `/stable` finds `/stable` at the version and skips live; check a copy of the version before by hand if live did not run.
- **homebrew** or **scoop**: re-run. Each installs from the commit promote made, so a re-run tests the same files even after a later release has changed them. If it fails again, people who install with that manager meet the same failure. Homebrew and Scoop read `main`, so a fix in `tools/release` reaches them, without a new version, once a pull request merges it with the two files rewritten as [Homebrew and Scoop](#homebrew-and-scoop) says. The packages workflow installs the rewritten files on that pull request.
- When several releases come close together, the promote of one can still be waiting when the next one's arrives, and GitHub then cancels the waiting one. Nothing needs doing: promote only moves `/stable` forward, and the newest release wins. Re-run a cancelled promote only when it belongs to the newest version.

## Signing keys

Each release's manifest is signed with Ed25519, and a copy of string installs only a release signed by a key it was built with: the keys in `internal/release/keys.go`. There are two:

- the **primary**, whose seed is the `release` environment's secret `RELEASE_SIGNING_KEY` and signs every release;
- the **recovery** key, whose seed is kept offline, in the company's secret store, and never goes to GitHub. It stands in when the primary is lost.

### Making the keys

Make them from a clean checkout of `main`, into a new directory outside every git repository, where no commit can pick up a seed. `GOENV=off GOFLAGS= GOWORK=off` keeps saved Go settings and workspaces from building anything but that checkout. Each seed goes to a file only you can read and reaches GitHub only through stdin, never as an argument:

```bash
git clone --depth 1 https://github.com/usestring/cli.git string-keys-checkout
cd string-keys-checkout
keys=~/string-release-keys
(umask 077 && mkdir "$keys")
git -C "$keys" rev-parse --show-toplevel   # must fail: no repository may hold the seeds
GOENV=off GOFLAGS= GOWORK=off go run ./tools/release keygen "$keys/primary.seed"
GOENV=off GOFLAGS= GOWORK=off go run ./tools/release keygen "$keys/recovery.seed"
gh secret set RELEASE_SIGNING_KEY --env release --repo usestring/cli < "$keys/primary.seed"
```

keygen refuses to write over a file, and prints the new key's public key and nothing else about it. Then:

1. Open a pull request that sets the two public keys in `internal/release/keys.go`, the primary first: `var trustedKeys = "PRIMARY RECOVERY"`. Until it is merged, releases stop at check, before anything is built or published.
2. Move both seed files into the company's secret store and delete them from the machine.

### Replacing the keys

A key change always replaces both keys: `keys.go` then holds the two new public keys and neither old one. An installed copy goes straight from its own version to whatever `/stable` names, so it may skip any one release. For a while after the change, every release must therefore also carry a signature from a key that the copies in use still trust:

1. Make two new keys as above, in another new directory, and set `RELEASE_SIGNING_KEY` from the new `primary.seed`.
2. Set `RELEASE_SIGNING_KEY_PREVIOUS` from one of the old seeds, which the copies in use trust. After a leaked primary, use the old primary's seed. After a lost primary, use the old recovery seed, since the copies in use trust no other key you have:

   ```bash
   gh secret set RELEASE_SIGNING_KEY_PREVIOUS --env release --repo usestring/cli < old.seed
   ```

3. Open the pull request with the two new public keys in `keys.go`. Releases stop at check until it is merged.
4. Release as usual. sign writes two lines into `manifest.json.sig`: one with the new primary, which the new binaries trust, and one with the previous key, for the copies that trust only the old keys.
5. Copies check once a day, but a machine can be off for weeks. Keep `RELEASE_SIGNING_KEY_PREVIOUS` for at least 90 days after the first release with the new keys reaches `/stable`. A copy that comes back after that must be installed again. Then delete the secret:

   ```bash
   gh secret delete RELEASE_SIGNING_KEY_PREVIOUS --env release --repo usestring/cli
   ```

Keep the old seed files in the secret store until then.

### The manifest's format

`manifest.json` says `"schema": 1`, and copies of string refuse any other schema and ignore fields they do not know. A field added later never changes the schema. A format that old copies could not read would be a new file beside `manifest.json`, which would go on being published for them.

## Homebrew and Scoop

[usestring/cli](https://github.com/usestring/cli) is its own Homebrew tap and Scoop bucket. `Formula/string.rb` installs the release's binary for the machine's system and CPU, from the GitHub release's archives, so Homebrew needs no Go. `bucket/string.json` does the same on Windows, for Intel and for Arm, and has Scoop's `checkver` and `autoupdate`. Both name the version and the SHA-256 of each archive. The formula names its version on a line of its own: Homebrew before 6.0.14 reads a version from the archive's name, which gives 64, from arm64 or amd64. Later versions read it from the release's URL as well, and their audit calls the line redundant.

`tools/release` writes both files from the release's `checksums.txt`, and promote commits them, so neither is edited by hand: a change belongs in `tools/release`. Its tests compare what it writes with the files in `tools/release/testdata`, and check that the two files in the repository are what it writes for the version they name. After changing what it writes, rewrite all four and read the diff:

```bash
go test ./tools/release -run 'Formula|Scoop' -update
```

The packages workflow, `.github/workflows/packages.yml`, installs both files as people do, on each pull request that changes them, `tools/release` or the workflow, and on each such push to `main`: the formula on macOS and Ubuntu, the manifest on Windows on Intel and on Arm. It runs the scripts the release's homebrew and scoop jobs run, `tools/release/homebrew.sh` and `tools/release/scoop.ps1`, on its own checkout, then `brew audit` and `brew style` on the formula. The files in the repository name a release that is out, so it installs that release's archives. promote's commit to `main` goes through the workflow's own token, and GitHub starts no workflow for a push made that way, so neither this workflow nor the tests run on it; the next pull request's runs are the first to see it.

To check the formula by hand, in a clean Homebrew:

```bash
brew tap usestring/cli https://github.com/usestring/cli
brew install usestring/cli/string
brew list --versions string
brew test usestring/cli/string
brew audit --strict --online --except=version usestring/cli/string
brew style usestring/cli/string
brew livecheck usestring/cli/string
```

`--except=version` skips the audit that calls the version line redundant. From 6.0.0, Homebrew loads a formula from another tap only once it is trusted, and `brew install usestring/cli/string` trusts the formula it names; the audit before an install would refuse it. Homebrew does not quarantine what it downloads, so the binary runs on macOS though it is not signed. If `string` is already installed through npm, use `--skip-link` during local validation and run the binary directly from the new keg. The formula test checks missing-credential behavior without sending a paid API request.

The first-party tap is an interim route. Official `homebrew/core` acceptance additionally needs current source/release eligibility and public-interest evidence for this repository.

### winget

There is no winget package yet. Submitting one opens a pull request on `microsoft/winget-pkgs`, a repository we do not own, so it needs a maintainer's decision, a package identifier and a GitHub token, and nothing has been submitted. A release's Windows archives fit winget's portable zip package, a set of three manifests under `manifests/<first letter>/<publisher>/<name>/<version>/`:

- the version manifest: `PackageIdentifier`, `PackageVersion` and `DefaultLocale` (`en-US`);
- the installer manifest: `InstallerType: zip`, `NestedInstallerType: portable`, and `NestedInstallerFiles` with `RelativeFilePath: string.exe` and `PortableCommandAlias: string`, so winget puts `string` on the PATH. Under `Installers`, one entry for `x64` with `string_windows_amd64.zip` and one for `arm64` with `string_windows_arm64.zip`, each with its release URL and the SHA-256 from `checksums.txt` as `InstallerSha256`;
- the default locale manifest: `Publisher`, `PackageName`, `License: MIT`, `ShortDescription` and `PackageUrl`.

`wingetcreate`, winget's manifest tool, writes all three in the current schema. `wingetcreate new` makes the first submission from the two archive URLs. For each release after it, a step after promote would run `wingetcreate update <identifier> --version X.Y.Z --urls <x64 zip URL> <arm64 zip URL> --submit --token <token>`, which opens the pull request from a fork owned by the token's account. winget's own checks and its moderators then decide when the release appears. Before the first submission, `string update` must learn to leave a copy that winget installed to winget, as it does for Homebrew and Scoop.

## After a release

The first push to `ghcr.io/usestring/cli` creates the package, and GitHub may create it private. If `docker pull` asks for credentials, an organization owner opens the package's settings and sets its visibility to public.

Check the GitHub release, `curl -fsSL https://cli.usestring.ai/stable`, `docker run --rm ghcr.io/usestring/cli --version`, `npm view @usestring/cli version` and `npx -y @usestring/cli --version`. The homebrew and scoop jobs have installed the release with Homebrew and Scoop.
