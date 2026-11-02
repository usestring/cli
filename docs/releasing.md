# Releasing the CLI

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
