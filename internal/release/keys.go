package release

// trustedKeys are the public halves of the release signing keys, in standard
// base64 and separated by white space: the primary, which signs each release
// in the release workflow, and the recovery key, which is kept offline and
// signs the release that replaces a lost or leaked primary. A copy of string
// installs only a release signed by a key it was built with, so with none it
// does not update itself.
//
// Tests build the CLI with a key of their own through
// -ldflags "-X github.com/usestring/cli/internal/release.trustedKeys=KEY".
var trustedKeys = ""
