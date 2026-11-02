# Releasing the CLI

A release is a signed `v*` tag on `main`. Pushing it runs `.github/workflows/release.yml`, which tests, builds the binaries with GoReleaser and publishes them to:

- the GitHub release, with `checksums.txt` (what `install.sh` downloads);
- `usestring/homebrew-tap` (`HOMEBREW_TAP_TOKEN`) and `usestring/scoop-bucket` (`SCOOP_BUCKET_TOKEN`);
- a pull request from the `usestring/winget-pkgs` fork to `microsoft/winget-pkgs` (`WINGET_TOKEN`);
- npm (`NPM_TOKEN`): one `@usestring/cli-<os>-<cpu>` package per platform, then `@usestring/cli`, which installs the one that fits.

A channel whose token secret is unset is skipped, so the first releases can go out before every channel is set up. Each token needs contents write on its repository; the winget token also opens the pull request. The npm token needs publish access to `@usestring/cli` and its platform packages.

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

Afterwards, check the GitHub release, `brew install usestring/tap/string`, `npm view @usestring/cli version` and `npx -y @usestring/cli --version`.
