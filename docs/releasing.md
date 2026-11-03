# Releasing the CLI

## Native source releases and Homebrew

The canonical upstream is [usestring/cli](https://github.com/usestring/cli). CLI maintainers publish stable source releases and update `Formula/string.rb` in the same repository. Keep the tag, release version, source archive and formula checksum aligned. The Homebrew tap does not depend on npm publication.

From a clean checkout of the reviewed `main` commit, run `go test ./...` and build `./cmd/string` with `CGO_ENABLED=0` for `darwin` and `linux`, each with `GOARCH=amd64` and `GOARCH=arm64`. Test the native binary on every available target; cross-compilation alone does not prove runtime behavior.

Create a signed `vX.Y.Z` tag at that commit. Create the source archive directly from the same commit with `git archive --format=tar.gz --prefix=string-X.Y.Z/ vX.Y.Z`, save it as `string-X.Y.Z.tar.gz`, and record its SHA-256 in `SHA256SUMS`. Inspect the archive, license and release notes before publishing both files in a stable GitHub release. Never replace a release asset or move a published tag; publish a new version if a correction is needed.

Update the formula's release URL and SHA-256 from the downloaded release asset. Go is a build dependency; the installed command has no runtime dependency on Node.js or Go. Before merging a formula update:

```bash
brew style Formula/string.rb
brew install --build-from-source usestring/cli/string
brew test usestring/cli/string
brew livecheck --formula usestring/cli/string
brew audit --strict --formula usestring/cli/string
```

Use a clean Homebrew environment to test executable linking and upgrades. If `string` is already installed through npm, use `--skip-link` during local formula validation and run the binary directly from the new keg. Do not overwrite an existing installation to make a packaging test pass. The formula test checks missing-credential behavior without sending a paid API request.

The first-party tap is an interim distribution route. Official `homebrew/core` acceptance additionally needs current source/release eligibility and public-interest evidence for this canonical repository. Interest in unrelated repositories does not establish CLI notability.

## npm releases

After the CLI change is merged, prepare a release from a clean checkout of `main`:

```bash
./scripts/release.sh prepare
```

The script builds, typechecks, tests, packs, records the source commit and SHA-256 checksum, then installs the tarball in a clean temporary directory and runs `string --help`. It prints the release directory. Check its tarball, checksum, and package metadata before approval.

Public npm publishing requires explicit approval for that exact artifact and an npm account with publish access to the `@usestring` scope. Once approved, publish the reviewed release directory:

```bash
./scripts/release.sh publish release/0.1.0.EXAMPLE APPROVED_SHA256
```

Replace `release/0.1.0.EXAMPLE` with the directory printed by `prepare`, and `APPROVED_SHA256` with the approved checksum that `prepare` printed. Take the checksum from the approval record, not from the release directory. That directory is ignored and mutable, so its own `SHA256SUMS` cannot vouch for the tarball. The publish command refuses a tarball whose digest differs from the approved one. It also checks the recorded source commit against the current `origin/main` commit. It does not rebuild or change the artifact. After publishing, verify the version with `npm view @usestring/cli version` and install it from npm in a clean directory.
