# Releasing the CLI

A release is a signed `v*` tag on `main`. Pushing it runs `.github/workflows/release.yml`, which tests, builds the binaries with GoReleaser and publishes them to:

- the GitHub release, with `checksums.txt` (what `install.sh` downloads) and `.deb`, `.rpm` and `.apk` packages;
- `ghcr.io/usestring/cli`, tagged with the version and `latest`, for linux/amd64 and linux/arm64 (the workflow's `GITHUB_TOKEN`);
- a pull request against `main` that updates `Casks/string.rb`, since this repository is its own Homebrew tap. Merge it to publish the version to Homebrew;
- npm (`NPM_TOKEN`): one `@usestring/cli-<os>-<cpu>` package per platform, then `@usestring/cli`, which installs the one that fits.

Everything except npm uses the workflow's `GITHUB_TOKEN`. The cask pull request needs **Allow GitHub Actions to create and approve pull requests** turned on in the repository's Actions settings. npm is skipped while `NPM_TOKEN` is unset; the token needs publish access to `@usestring/cli` and its platform packages.

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

Afterwards, check the GitHub release, `docker run --rm ghcr.io/usestring/cli --version`, `brew install usestring/cli/string` (after merging the cask pull request), `npm view @usestring/cli version` and `npx -y @usestring/cli --version`.
