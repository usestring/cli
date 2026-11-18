// Command release makes and checks what the release workflow publishes on the
// download host beside a release's archives: manifest.json, which lists each
// platform's archive with its size and SHA-256, and manifest.json.sig, the
// signatures an installed copy of string checks it with. It also makes the
// signing keys, and answers the workflow's questions about a tag and about
// moving /stable, so that the workflow orders versions exactly as string does.
//
//	go run ./tools/release keygen FILE
//	go run ./tools/release manifest DIR VERSION PUBLISHED > manifest.json
//	go run ./tools/release sign manifest.json > manifest.json.sig
//	go run ./tools/release verify manifest.json manifest.json.sig
//	go run ./tools/release tag TAG
//	go run ./tools/release promote EVENT VERSION CURRENT
//
// Versions are written without a v, as in 0.3.0. Run from the commit being
// released, sign and verify check against the keys that commit's binaries
// trust (internal/release/keys.go), so a release they would refuse is never
// published.
package main

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/usestring/cli/internal/release"
)

const usage = `Usage:
  release keygen FILE                     make a signing key: its seed goes to FILE, the public key to stdout
  release manifest DIR VERSION PUBLISHED  print manifest.json for the archives and checksums.txt in DIR
  release sign MANIFEST                   print manifest.json.sig, signed with RELEASE_SIGNING_KEY
  release verify MANIFEST SIGNATURES      check both as a copy of string built from this commit would
  release tag TAG                         print a release tag's version and whether it is a pre-release
  release promote EVENT VERSION CURRENT   print how /stable moves from CURRENT to VERSION, or refuse
`

// The environment variables sign reads each key from, as the base64 of its
// 32-byte seed. The second is set only while one key replaces another.
const (
	signingKey         = "RELEASE_SIGNING_KEY"
	previousSigningKey = "RELEASE_SIGNING_KEY_PREVIOUS"
)

// platforms are the archives a manifest lists, named as .goreleaser.yaml
// names them.
var platforms = []struct{ goos, goarch, ext string }{
	{"darwin", "amd64", ".tar.gz"},
	{"darwin", "arm64", ".tar.gz"},
	{"linux", "amd64", ".tar.gz"},
	{"linux", "arm64", ".tar.gz"},
	{"windows", "amd64", ".zip"},
	{"windows", "arm64", ".zip"},
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv, release.TrustedKeys))
}

// run is the command with what it takes from outside passed in. trusted gives
// the keys that copies built from this commit accept, which tests replace.
func run(args []string, stdout, stderr io.Writer, getenv func(string) string, trusted func() ([]ed25519.PublicKey, error)) int {
	arity := map[string]int{"keygen": 1, "manifest": 3, "sign": 1, "verify": 2, "tag": 1, "promote": 3}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if n, ok := arity[args[0]]; !ok || n != len(args)-1 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	command, args := args[0], args[1:]
	var err error
	switch command {
	case "keygen":
		err = keygen(args[0], stdout)
	case "manifest":
		err = manifest(args[0], args[1], args[2], stdout)
	case "sign":
		err = sign(args[0], getenv, trusted, stdout, stderr)
	case "verify":
		err = verify(args[0], args[1], trusted, stdout)
	case "tag":
		err = tag(args[0], stdout)
	case "promote":
		err = promote(args[0], args[1], args[2], stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "release %s: %v\n", command, err)
		return 1
	}
	return 0
}

// keygen makes a new signing key. Its seed goes to path, as base64 and a
// newline, in a file that only its owner can read and that keygen creates:
// it never replaces a file, which may hold the only copy of another key. The
// public key goes to stdout, for internal/release/keys.go.
func keygen(path string, stdout io.Writer) error {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s already exists, and keygen never replaces a key", path)
	}
	if err != nil {
		return err
	}
	_, err = file.WriteString(base64.StdEncoding.EncodeToString(private.Seed()) + "\n")
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		// The file is the one created above, so nothing else is lost.
		os.Remove(path)
		return err
	}
	_, err = fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(public))
	return err
}

// manifest prints manifest.json for the release whose archives and
// checksums.txt are in dir. published is when the GitHub release was
// published, so that building the manifest again gives the same bytes, and
// so the same signatures, as the first time.
func manifest(dir, versionText, publishedText string, stdout io.Writer) error {
	v, err := release.ParseVersion(versionText)
	if err != nil {
		return err
	}
	published, err := time.Parse(time.RFC3339, publishedText)
	if err != nil {
		return fmt.Errorf("%q is not a time such as 2026-11-25T12:00:00Z", publishedText)
	}
	sums, err := readChecksums(filepath.Join(dir, "checksums.txt"))
	if err != nil {
		return err
	}
	m := release.Manifest{Schema: 1, Version: v.String(), Published: published.UTC(), Assets: map[string]release.Asset{}}
	for _, p := range platforms {
		name := "string_" + p.goos + "_" + p.goarch + p.ext
		listed, ok := sums[name]
		if !ok {
			return fmt.Errorf("checksums.txt lists no %s", name)
		}
		sum, size, err := hashFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if sum != listed {
			return fmt.Errorf("%s has SHA-256 %s, but checksums.txt says %s", name, sum, listed)
		}
		m.Assets[release.Platform(p.goos, p.goarch)] = release.Asset{Name: name, SHA256: sum, Size: size}
	}
	// encoding/json writes struct fields in order and map keys sorted, so the
	// bytes depend only on what the manifest says.
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// Each copy of string checks a manifest as ParseManifest does, and one
	// it refuses, such as one listing an empty archive, would stop them all.
	if _, err := release.ParseManifest(data); err != nil {
		return fmt.Errorf("string would refuse this manifest: %w", err)
	}
	_, err = stdout.Write(data)
	return err
}

// readChecksums reads GoReleaser's checksums.txt, a SHA-256 and a file name on
// each line, into a map from name to SHA-256.
func readChecksums(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sums := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; scanner.Scan(); n++ {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return nil, fmt.Errorf("line %d of checksums.txt is not a SHA-256 and a file name", n)
		}
		if _, seen := sums[fields[1]]; seen {
			return nil, fmt.Errorf("checksums.txt lists %s twice", fields[1])
		}
		sums[fields[1]] = fields[0]
	}
	return sums, scanner.Err()
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

// sign prints manifest.json.sig for the manifest at path: a line signed with
// RELEASE_SIGNING_KEY, then, while one key replaces another, a line signed
// with RELEASE_SIGNING_KEY_PREVIOUS for the copies that trust only the old
// one. It refuses unless the first line verifies under the keys this commit's
// binaries trust. The previous key need not be one of them: after a leak it
// is not. Nothing sign prints holds a key.
func sign(path string, getenv func(string) string, trusted func() ([]ed25519.PublicKey, error), stdout, stderr io.Writer) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, err := release.ParseManifest(data); err != nil {
		return err
	}
	primary, err := seedFrom(getenv, signingKey)
	if err != nil {
		return err
	}
	if primary == nil {
		return fmt.Errorf("%s is not set", signingKey)
	}
	previous, err := seedFrom(getenv, previousSigningKey)
	if err != nil {
		return err
	}
	keys, err := trusted()
	if err != nil {
		return fmt.Errorf("the keys this build trusts: %w", err)
	}
	if len(keys) == 0 {
		return errors.New("this build trusts no release signing key, so no copy built from it could check a release; put the public keys in internal/release/keys.go first")
	}
	signers := [][]byte{primary}
	if previous != nil {
		if bytes.Equal(previous, primary) {
			return fmt.Errorf("%s holds the same key as %s", previousSigningKey, signingKey)
		}
		signers = append(signers, previous)
	}
	var lines []string
	for i, seed := range signers {
		line, err := release.Sign(data, seed)
		if err != nil {
			return err
		}
		own := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		if err := release.Verify(data, []byte(line), []ed25519.PublicKey{own}); err != nil {
			return fmt.Errorf("the signature made with %s does not verify under its own public key: %w", []string{signingKey, previousSigningKey}[i], err)
		}
		lines = append(lines, line)
	}
	if release.Verify(data, []byte(lines[0]), keys) != nil {
		return fmt.Errorf("%s is not one of the keys this build trusts, so the copies built from it would refuse this release", signingKey)
	}
	if previous != nil {
		fmt.Fprintf(stderr, "Signed with %s as well, for the copies that trust only the key it replaces.\n", previousSigningKey)
	}
	_, err = io.WriteString(stdout, strings.Join(lines, "\n")+"\n")
	return err
}

// seedFrom reads the seed in the environment variable name, or nil when it is
// not set. Its error never quotes the value.
func seedFrom(getenv func(string) string, name string) ([]byte, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return nil, nil
	}
	seed, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s is not the base64 of a %d-byte Ed25519 seed", name, ed25519.SeedSize)
	}
	return seed, nil
}

// verify checks a manifest and its signatures as a copy of string built from
// this commit would.
func verify(manifestPath, signaturesPath string, trusted func() ([]ed25519.PublicKey, error), stdout io.Writer) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	signatures, err := os.ReadFile(signaturesPath)
	if err != nil {
		return err
	}
	m, err := release.ParseManifest(data)
	if err != nil {
		return err
	}
	keys, err := trusted()
	if err != nil {
		return fmt.Errorf("the keys this build trusts: %w", err)
	}
	if err := release.Verify(data, signatures, keys); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "The manifest of %s is signed by a key this build trusts.\n", m.Version)
	return err
}

// tag checks that name is a release tag, v and a version as in v0.3.0 or
// v0.3.0-rc.1, and prints what the workflow keeps from it: the version
// without its v, and whether it is a pre-release. A tag with build metadata
// is refused: the install scripts take no + in a version, and two tags that
// differed only there would name the same release.
func tag(name string, stdout io.Writer) error {
	refused := fmt.Errorf("%q is not a release tag, which is v and a version such as v0.3.0 or v0.3.0-rc.1", name)
	text, ok := strings.CutPrefix(name, "v")
	if !ok || strings.Contains(text, "+") {
		return refused
	}
	v, err := release.ParseVersion(text)
	if err != nil {
		return refused
	}
	_, err = fmt.Fprintf(stdout, "version=%s\nprerelease=%t\n", v, v.Prerelease())
	return err
}

// promote decides whether the release workflow may point /stable at version
// while it names current, or nothing when current is "". event is what
// started the run: push for a tag pushed, workflow_dispatch for a run on a
// tag with promote=true. A tag push only moves /stable forward, and only to a
// release; a run by hand may move it back. A pre-release may take /stable
// only while it is missing or names a pre-release, which allows a rehearsal
// before the first release and refuses one after it. promote prints first,
// forward, same or back.
func promote(event, versionText, currentText string, stdout io.Writer) error {
	if event != "push" && event != "workflow_dispatch" {
		return fmt.Errorf("%q is not push or workflow_dispatch", event)
	}
	v, err := release.ParseVersion(versionText)
	if err != nil {
		return err
	}
	var current *release.Version
	if currentText != "" {
		c, err := release.ParseVersion(currentText)
		if err != nil {
			return fmt.Errorf("/stable does not name a version: %w", err)
		}
		current = &c
	}
	switch {
	case v.Prerelease() && event == "push":
		return fmt.Errorf("a tag push never points /stable at a pre-release; to rehearse with %s, run the workflow on v%[1]s with promote=true", v)
	case v.Prerelease() && current != nil && !current.Prerelease():
		return fmt.Errorf("/stable names %s, a release, and a pre-release takes its place only before the first release", current)
	case event == "push" && current != nil && v.Compare(*current) < 0:
		return fmt.Errorf("/stable names %s, which is newer than %s, and a tag push only moves it forward; to move it back, run the workflow on v%[2]s with promote=true", current, v)
	}
	direction := "first"
	if current != nil {
		direction = map[int]string{1: "forward", 0: "same", -1: "back"}[v.Compare(*current)]
	}
	_, err = fmt.Fprintln(stdout, direction)
	return err
}
