# Releasing the CLI

A release is a signed `v*` tag on `main`. Pushing it runs `.github/workflows/release.yml`, which tests, builds the binaries with GoReleaser and publishes them to:

- the GitHub release: archives for every platform, `checksums.txt` (what `install.sh` downloads), `.deb`, `.rpm` and `.apk` packages, and the source archive `string-X.Y.Z.tar.gz` that the Homebrew formula builds from;
- `ghcr.io/usestring/cli`, tagged with the version and `latest`, for linux/amd64 and linux/arm64;
- npm (`NPM_TOKEN`): one `@usestring/cli-<os>-<cpu>` package per platform, then `@usestring/cli`, which installs the one that fits.

Everything except npm uses the workflow's `GITHUB_TOKEN`. npm is skipped while `NPM_TOKEN` is unset; the token needs publish access to `@usestring/cli` and its platform packages.

Every release is public, so it needs explicit approval for that version before the tag is pushed. Check the build locally first; nothing here publishes:

```bash
goreleaser release --snapshot --clean
node npm/build.mjs dist npm-dist
```

Then, from an up-to-date `main`:

```bash
git tag -s v0.2.0 -m v0.2.0
git push origin v0.2.0
```

Never replace a release asset or move a published tag; publish a new version if a correction is needed.

## Homebrew

[usestring/cli](https://github.com/usestring/cli) is its own Homebrew tap: `Formula/string.rb` builds the CLI from the release's source archive. After a release, update the formula's `url` and `sha256` from the published `string-X.Y.Z.tar.gz` (its checksum is in `checksums.txt`) and check it before merging:

```bash
brew style Formula/string.rb
brew install --build-from-source usestring/cli/string
brew test usestring/cli/string
brew livecheck --formula usestring/cli/string
brew audit --strict --formula usestring/cli/string
```

Use a clean Homebrew environment to test linking and upgrades. If `string` is already installed through npm, use `--skip-link` during local formula validation and run the binary directly from the new keg. The formula test checks missing-credential behavior without sending a paid API request.

The first-party tap is an interim route. Official `homebrew/core` acceptance additionally needs current source/release eligibility and public-interest evidence for this repository.

## After a release

Check the GitHub release, `docker run --rm ghcr.io/usestring/cli --version`, `brew install usestring/cli/string` (after the formula update), `npm view @usestring/cli version` and `npx -y @usestring/cli --version`.
