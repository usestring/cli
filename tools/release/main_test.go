package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/usestring/cli/internal/release"
)

type outcome struct {
	code           int
	stdout, stderr string
}

// runTool runs the command with env as its environment and trusted as the
// keys the build trusts.
func runTool(t *testing.T, env map[string]string, trusted []ed25519.PublicKey, args ...string) outcome {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr, func(name string) string { return env[name] }, func() ([]ed25519.PublicKey, error) { return trusted, nil })
	return outcome{code, stdout.String(), stderr.String()}
}

// newKey is a signing key as RELEASE_SIGNING_KEY holds one, with its public
// half.
func newKey(t *testing.T) (string, ed25519.PublicKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(private.Seed()), public
}

// leaks reports whether text holds the key whose seed is given, in any of the
// forms a mistake would print it in.
func leaks(text, seed string) bool {
	raw, _ := base64.StdEncoding.DecodeString(seed)
	private := ed25519.NewKeyFromSeed(raw)
	for _, form := range []string{seed, hex.EncodeToString(raw), base64.StdEncoding.EncodeToString(private), base64.RawStdEncoding.EncodeToString(raw)} {
		if strings.Contains(text, form) {
			return true
		}
	}
	return false
}

func TestKeygenWritesTheSeedToItsFileAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary.seed")
	got := runTool(t, nil, nil, "keygen", path)
	if got.code != 0 || got.stderr != "" {
		t.Fatalf("keygen: %+v", got)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(string(written), "\n"))
	if err != nil || len(seed) != ed25519.SeedSize || !strings.HasSuffix(string(written), "\n") || strings.Count(string(written), "\n") != 1 {
		t.Fatalf("the file holds %q, not the base64 of a seed and a newline", written)
	}
	public := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if got.stdout != base64.StdEncoding.EncodeToString(public)+"\n" {
		t.Errorf("keygen printed %q, not the seed's public key", got.stdout)
	}
	if leaks(got.stdout, strings.TrimSpace(string(written))) {
		t.Error("keygen printed the seed")
	}
	if _, err := release.ParseKeys(got.stdout); err != nil {
		t.Errorf("the printed key cannot go in keys.go: %v", err)
	}
	// Windows files have no Unix permission bits to check.
	if info, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("the seed file has mode %v: %v", info.Mode(), err)
	}

	again := runTool(t, nil, nil, "keygen", path)
	if after, _ := os.ReadFile(path); again.code != 1 || again.stdout != "" || !strings.Contains(again.stderr, "already exists") || !bytes.Equal(after, written) {
		t.Errorf("keygen over an existing seed: %+v, the file now holds %q", again, after)
	}
	other := runTool(t, nil, nil, "keygen", filepath.Join(t.TempDir(), "recovery.seed"))
	if other.code != 0 || other.stdout == got.stdout {
		t.Errorf("a second key: %+v", other)
	}
	if missing := runTool(t, nil, nil, "keygen", filepath.Join(t.TempDir(), "no-such-dir", "primary.seed")); missing.code != 1 || missing.stdout != "" {
		t.Errorf("keygen into a directory that does not exist: %+v", missing)
	}
}

func TestKeysTakesOnlyKeysThatCanSignARelease(t *testing.T) {
	primary, primaryPublic := newKey(t)
	recovery, recoveryPublic := newKey(t)
	previous, previousPublic := newKey(t)
	_, otherPublic := newKey(t)
	trusted := []ed25519.PublicKey{primaryPublic, recoveryPublic}
	short := base64.StdEncoding.EncodeToString(make([]byte, 31))
	encode := base64.StdEncoding.EncodeToString
	listed := "This build trusts these release signing keys:\n  " + encode(primaryPublic) + "\n  " + encode(recoveryPublic) + "\n"
	holds := func(public ed25519.PublicKey) string {
		return "RELEASE_SIGNING_KEY has the public key " + encode(public) + ", one of them.\n"
	}
	alsoSigns := "RELEASE_SIGNING_KEY_PREVIOUS is set, with the public key " + encode(previousPublic) + ", so each release is signed with it as well.\n"
	shows := func(t *testing.T, got outcome) {
		t.Helper()
		for _, seed := range []string{primary, recovery, previous} {
			if leaks(got.stdout+got.stderr, seed) {
				t.Errorf("keys printed a seed: %+v", got)
			}
		}
	}

	for name, check := range map[string]struct {
		env  map[string]string
		want string
	}{
		"the primary key": {map[string]string{signingKey: primary}, listed + holds(primaryPublic)},
		// A secret set from a file can end in a newline, or CRLF on Windows.
		"the primary key set from a file":              {map[string]string{signingKey: primary + "\n"}, listed + holds(primaryPublic)},
		"the primary key set from a file with CRLF":    {map[string]string{signingKey: primary + "\r\n"}, listed + holds(primaryPublic)},
		"the primary key pasted with spaces around it": {map[string]string{signingKey: " " + primary + " "}, listed + holds(primaryPublic)},
		"the other key this build trusts":              {map[string]string{signingKey: recovery}, listed + holds(recoveryPublic)},
		// After a rotation, the build no longer trusts the key it replaced.
		"a previous key while one key replaces another": {map[string]string{signingKey: primary, previousSigningKey: previous + "\r\n"}, listed + holds(primaryPublic) + alsoSigns},
	} {
		t.Run(name, func(t *testing.T) {
			got := runTool(t, check.env, trusted, "keys")
			if got.code != 0 || got.stdout != check.want || got.stderr != "" {
				t.Errorf("%+v, want stdout %q", got, check.want)
			}
			shows(t, got)
		})
	}

	for name, check := range map[string]struct {
		env     map[string]string
		trusted []ed25519.PublicKey
		reason  string
	}{
		"with no key set":                  {map[string]string{}, trusted, "RELEASE_SIGNING_KEY is not set"},
		"with an empty key":                {map[string]string{signingKey: ""}, trusted, "RELEASE_SIGNING_KEY is not set"},
		"with a key of white space alone":  {map[string]string{signingKey: " \r\n"}, trusted, "RELEASE_SIGNING_KEY is not set"},
		"with only the previous key set":   {map[string]string{previousSigningKey: previous}, trusted, "RELEASE_SIGNING_KEY is not set"},
		"with a key that is not base64":    {map[string]string{signingKey: "not base64 " + primary}, trusted, "RELEASE_SIGNING_KEY is not the base64"},
		"with a key that is too short":     {map[string]string{signingKey: short}, trusted, "RELEASE_SIGNING_KEY is not the base64"},
		"with both seed files in a secret": {map[string]string{signingKey: primary + "\n" + recovery + "\n"}, trusted, "RELEASE_SIGNING_KEY is not the base64"},
		// The refusal names the public key, to compare with keys.go.
		"with a key the build does not trust":    {map[string]string{signingKey: primary}, []ed25519.PublicKey{otherPublic}, "RELEASE_SIGNING_KEY has the public key " + encode(primaryPublic) + ", which is not one of the keys this build trusts"},
		"with only the previous key trusted":     {map[string]string{signingKey: primary, previousSigningKey: previous}, []ed25519.PublicKey{previousPublic}, "RELEASE_SIGNING_KEY has the public key " + encode(primaryPublic) + ", which is not one of the keys this build trusts"},
		"in a build that trusts no key":          {map[string]string{signingKey: primary}, nil, "this build trusts no release signing key"},
		"with a previous key that is not base64": {map[string]string{signingKey: primary, previousSigningKey: previous + "!"}, trusted, "RELEASE_SIGNING_KEY_PREVIOUS is not the base64"},
		"with a previous key that is too short":  {map[string]string{signingKey: primary, previousSigningKey: short}, trusted, "RELEASE_SIGNING_KEY_PREVIOUS is not the base64"},
		"with the same key twice":                {map[string]string{signingKey: primary, previousSigningKey: primary}, trusted, "RELEASE_SIGNING_KEY_PREVIOUS holds the same key as RELEASE_SIGNING_KEY"},
		"with the same key twice, set two ways":  {map[string]string{signingKey: primary + "\n", previousSigningKey: " " + primary + "\r\n"}, trusted, "RELEASE_SIGNING_KEY_PREVIOUS holds the same key as RELEASE_SIGNING_KEY"},
	} {
		t.Run(name, func(t *testing.T) {
			got := runTool(t, check.env, check.trusted, "keys")
			if got.code != 1 || got.stdout != "" || !strings.HasPrefix(got.stderr, "release keys: ") || !strings.Contains(got.stderr, check.reason) {
				t.Errorf("%+v, want a refusal that mentions %q", got, check.reason)
			}
			shows(t, got)
		})
	}

	t.Run("when the trusted keys cannot be read", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"keys"}, &stdout, &stderr, func(name string) string { return map[string]string{signingKey: primary}[name] }, func() ([]ed25519.PublicKey, error) {
			return nil, errors.New("not a key")
		})
		if code != 1 || stdout.Len() > 0 || !strings.Contains(stderr.String(), "the keys this build trusts: not a key") {
			t.Errorf("keys: %d %q %q", code, stdout.String(), stderr.String())
		}
	})
}

// releaseDir is a release's assets as the publish job downloads them: the six
// archives, a package and the source archive beside them, and checksums.txt
// listing them all.
func releaseDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	names := []string{"string_0.3.0_linux_amd64.deb", "string-0.3.0.tar.gz"}
	for _, p := range platforms {
		names = append(names, "string_"+p.goos+"_"+p.goarch+p.ext)
	}
	var sums strings.Builder
	for _, name := range names {
		data := []byte("the contents of " + name)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(data), name)
	}
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestManifestListsEachArchiveWithItsChecksumAndSize(t *testing.T) {
	dir := releaseDir(t)
	var want strings.Builder
	want.WriteString("{\n  \"schema\": 1,\n  \"version\": \"0.3.0-rc.1\",\n  \"published\": \"2026-11-25T12:00:00Z\",\n  \"assets\": {\n")
	for i, p := range platforms {
		name := "string_" + p.goos + "_" + p.goarch + p.ext
		data := "the contents of " + name
		fmt.Fprintf(&want, "    %q: {\n      \"name\": %q,\n      \"sha256\": \"%x\",\n      \"size\": %d\n    }", p.goos+"_"+p.goarch, name, sha256.Sum256([]byte(data)), len(data))
		if i < len(platforms)-1 {
			want.WriteString(",")
		}
		want.WriteString("\n")
	}
	want.WriteString("  }\n}\n")

	// The publication time is written in UTC whatever zone it is given in.
	got := runTool(t, nil, nil, "manifest", dir, "0.3.0-rc.1", "2026-11-25T13:00:00+01:00")
	if got.code != 0 || got.stdout != want.String() {
		t.Fatalf("manifest: %+v\nwant:\n%s", got, want.String())
	}
	if again := runTool(t, nil, nil, "manifest", dir, "0.3.0-rc.1", "2026-11-25T12:00:00Z"); again.stdout != got.stdout {
		t.Errorf("a second run gave other bytes:\n%s", again.stdout)
	}
	if m, err := release.ParseManifest([]byte(got.stdout)); err != nil || len(m.Assets) != 6 {
		t.Errorf("string reads the manifest as %+v, %v", m, err)
	}
}

func TestManifestRefusesWhatItCannotVouchFor(t *testing.T) {
	archive := "string_linux_arm64.tar.gz"
	for name, check := range map[string]struct {
		change           func(t *testing.T, dir string)
		version, publish string
		reason           string
	}{
		"an archive that is missing": {change: func(t *testing.T, dir string) { remove(t, filepath.Join(dir, archive)) }, reason: archive},
		"an archive checksums.txt does not list": {change: func(t *testing.T, dir string) {
			rewrite(t, filepath.Join(dir, "checksums.txt"), func(s string) string { return dropLine(s, archive) })
		}, reason: "lists no " + archive},
		"an archive that does not match its checksum": {change: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, archive), []byte("other bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, reason: "but checksums.txt says"},
		"a line that is not a checksum and a name": {change: func(t *testing.T, dir string) {
			rewrite(t, filepath.Join(dir, "checksums.txt"), func(s string) string { return s + "abc def ghi\n" })
		}, reason: "line 9"},
		"a name listed twice": {change: func(t *testing.T, dir string) {
			rewrite(t, filepath.Join(dir, "checksums.txt"), func(s string) string { return s + strings.Repeat("0", 64) + "  " + archive + "\n" })
		}, reason: "twice"},
		// It matches its checksum, but no copy of string would take it.
		"an empty archive": {change: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, archive), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			rewrite(t, filepath.Join(dir, "checksums.txt"), func(s string) string {
				return dropLine(s, archive) + fmt.Sprintf("%x  %s\n", sha256.Sum256(nil), archive)
			})
		}, reason: "string would refuse this manifest"},
		"no checksums.txt":          {change: func(t *testing.T, dir string) { remove(t, filepath.Join(dir, "checksums.txt")) }, reason: "checksums.txt"},
		"a version with a v":        {version: "v0.3.0", reason: "v0.3.0"},
		"a development version":     {version: "dev", reason: "dev"},
		"a time that is not a time": {publish: "yesterday", reason: "yesterday"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := releaseDir(t)
			if check.change != nil {
				check.change(t, dir)
			}
			version, published := "0.3.0", "2026-11-25T12:00:00Z"
			if check.version != "" {
				version = check.version
			}
			if check.publish != "" {
				published = check.publish
			}
			got := runTool(t, nil, nil, "manifest", dir, version, published)
			if got.code != 1 || got.stdout != "" || !strings.Contains(got.stderr, check.reason) {
				t.Errorf("%+v, want a refusal that mentions %q", got, check.reason)
			}
		})
	}
}

func remove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func rewrite(t *testing.T, path string, change func(string) string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(change(string(data))), 0o644); err != nil {
		t.Fatal(err)
	}
}

func dropLine(text, containing string) string {
	var kept []string
	for _, line := range strings.SplitAfter(text, "\n") {
		if !strings.Contains(line, containing) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "")
}

// manifestFile is a manifest the release tool built, in a file of its own.
func manifestFile(t *testing.T) (string, []byte) {
	t.Helper()
	got := runTool(t, nil, nil, "manifest", releaseDir(t), "0.3.0", "2026-11-25T12:00:00Z")
	if got.code != 0 {
		t.Fatalf("manifest: %+v", got)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(got.stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, []byte(got.stdout)
}

func TestSignSignsOnlyWithAKeyThisBuildTrusts(t *testing.T) {
	path, data := manifestFile(t)
	primary, primaryPublic := newKey(t)
	previous, previousPublic := newKey(t)
	_, otherPublic := newKey(t)
	trusted := []ed25519.PublicKey{primaryPublic}
	short := base64.StdEncoding.EncodeToString(make([]byte, 31))

	for name, check := range map[string]struct {
		env     map[string]string
		trusted []ed25519.PublicKey
		reason  string
	}{
		"with no key set":                        {env: map[string]string{}, trusted: trusted, reason: "RELEASE_SIGNING_KEY is not set"},
		"with a key that is not base64":          {env: map[string]string{signingKey: "not base64 " + primary}, trusted: trusted, reason: "is not the base64"},
		"with a key that is too short":           {env: map[string]string{signingKey: short}, trusted: trusted, reason: "is not the base64"},
		"in a build that trusts no key":          {env: map[string]string{signingKey: primary}, reason: "trusts no release signing key"},
		"with a key the build does not trust":    {env: map[string]string{signingKey: primary}, trusted: []ed25519.PublicKey{otherPublic}, reason: "not one of the keys this build trusts"},
		"with only the previous key trusted":     {env: map[string]string{signingKey: primary, previousSigningKey: previous}, trusted: []ed25519.PublicKey{previousPublic}, reason: "not one of the keys this build trusts"},
		"with the same key twice":                {env: map[string]string{signingKey: primary, previousSigningKey: primary}, trusted: trusted, reason: "the same key"},
		"with a previous key that is not base64": {env: map[string]string{signingKey: primary, previousSigningKey: previous + "!"}, trusted: trusted, reason: "RELEASE_SIGNING_KEY_PREVIOUS is not the base64"},
	} {
		t.Run(name, func(t *testing.T) {
			got := runTool(t, check.env, check.trusted, "sign", path)
			if got.code != 1 || got.stdout != "" || !strings.Contains(got.stderr, check.reason) {
				t.Errorf("%+v, want a refusal that mentions %q", got, check.reason)
			}
			if leaks(got.stderr, primary) || leaks(got.stderr, previous) {
				t.Errorf("the refusal shows a key: %q", got.stderr)
			}
		})
	}

	t.Run("with the primary key", func(t *testing.T) {
		got := runTool(t, map[string]string{signingKey: primary}, trusted, "sign", path)
		if got.code != 0 || strings.Count(got.stdout, "\n") != 1 || got.stderr != "" {
			t.Fatalf("sign: %+v", got)
		}
		if err := release.Verify(data, []byte(got.stdout), trusted); err != nil {
			t.Errorf("the signature does not verify: %v", err)
		}
		if leaks(got.stdout, primary) {
			t.Error("sign printed the key")
		}
		// Ed25519 signatures are deterministic, so a re-run of the publish
		// job writes the same bytes again.
		if again := runTool(t, map[string]string{signingKey: primary}, trusted, "sign", path); again.stdout != got.stdout {
			t.Error("signing again gave another signature")
		}
		// A secret set from a file can end in a newline, or CRLF on Windows.
		for _, end := range []string{"\n", "\r\n"} {
			if again := runTool(t, map[string]string{signingKey: primary + end}, trusted, "sign", path); again.code != 0 || again.stdout != got.stdout {
				t.Errorf("with a key that ends in %q: %+v", end, again)
			}
		}
	})

	t.Run("with the previous key as well, while one key replaces another", func(t *testing.T) {
		// After a leak the new build no longer trusts the previous key, and
		// the copies still in use trust only that one.
		got := runTool(t, map[string]string{signingKey: primary, previousSigningKey: previous}, trusted, "sign", path)
		lines := strings.Split(strings.TrimSuffix(got.stdout, "\n"), "\n")
		if got.code != 0 || len(lines) != 2 || !strings.Contains(got.stderr, previousSigningKey) {
			t.Fatalf("sign: %+v", got)
		}
		if release.Verify(data, []byte(lines[0]), trusted) != nil || release.Verify(data, []byte(lines[1]), []ed25519.PublicKey{previousPublic}) != nil {
			t.Errorf("the first line is not the primary key's or the second not the previous key's: %q", lines)
		}
		if err := release.Verify(data, []byte(got.stdout), []ed25519.PublicKey{previousPublic}); err != nil {
			t.Errorf("a copy that trusts only the previous key refuses the release: %v", err)
		}
		if leaks(got.stdout+got.stderr, primary) || leaks(got.stdout+got.stderr, previous) {
			t.Error("sign printed a key")
		}
		if again := runTool(t, map[string]string{signingKey: primary + "\r\n", previousSigningKey: previous + "\r\n"}, trusted, "sign", path); again.code != 0 || again.stdout != got.stdout {
			t.Errorf("with both keys set from files with CRLF: %+v", again)
		}
	})

	t.Run("a manifest string would refuse", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), "manifest.json")
		if err := os.WriteFile(bad, bytes.Replace(data, []byte(`"schema": 1`), []byte(`"schema": 2`), 1), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := runTool(t, map[string]string{signingKey: primary}, trusted, "sign", bad); got.code != 1 || got.stdout != "" {
			t.Errorf("sign: %+v", got)
		}
	})

	t.Run("when the trusted keys cannot be read", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"sign", path}, &stdout, &stderr, func(string) string { return primary }, func() ([]ed25519.PublicKey, error) {
			return nil, errors.New("not a key")
		})
		if code != 1 || stdout.Len() > 0 || !strings.Contains(stderr.String(), "not a key") {
			t.Errorf("sign: %d %q %q", code, stdout.String(), stderr.String())
		}
	})
}

func TestVerifyChecksAsAnInstalledCopyWould(t *testing.T) {
	path, data := manifestFile(t)
	primary, primaryPublic := newKey(t)
	signed := runTool(t, map[string]string{signingKey: primary}, []ed25519.PublicKey{primaryPublic}, "sign", path)
	signatures := filepath.Join(t.TempDir(), "manifest.json.sig")
	if err := os.WriteFile(signatures, []byte(signed.stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	_, otherPublic := newKey(t)
	altered := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(altered, bytes.Replace(data, []byte(`"size": `), []byte(`"size": 1`), 1), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := runTool(t, nil, []ed25519.PublicKey{primaryPublic}, "verify", path, signatures); got.code != 0 || !strings.Contains(got.stdout, "0.3.0") {
		t.Errorf("a good release: %+v", got)
	}
	for name, check := range map[string]struct {
		manifest string
		trusted  []ed25519.PublicKey
		reason   string
	}{
		"signed by a key the build does not trust": {path, []ed25519.PublicKey{otherPublic}, "none of the 1 signatures"},
		"changed after it was signed":              {altered, []ed25519.PublicKey{primaryPublic}, "none of the 1 signatures"},
		"in a build that trusts no key":            {path, nil, release.ErrNoTrustedKeys.Error()},
	} {
		if got := runTool(t, nil, check.trusted, "verify", check.manifest, signatures); got.code != 1 || got.stdout != "" || !strings.Contains(got.stderr, check.reason) {
			t.Errorf("a release %s: %+v", name, got)
		}
	}
}

func TestTagGivesTheVersionAndWhetherItIsAPrerelease(t *testing.T) {
	for tag, want := range map[string]string{
		"v0.3.0":         "version=0.3.0\nprerelease=false\n",
		"v0.3.0-rc.1":    "version=0.3.0-rc.1\nprerelease=true\n",
		"v1.10.0-beta.2": "version=1.10.0-beta.2\nprerelease=true\n",
	} {
		if got := runTool(t, nil, nil, "tag", tag); got.code != 0 || got.stdout != want {
			t.Errorf("tag %s: %+v", tag, got)
		}
	}
	for _, tag := range []string{"", "0.3.0", "V0.3.0", "v0.3", "v0.3.0.1", "v00.3.0", "v0.3.0-rc.01", "v0.3.0+linux", "v0.3.0-rc.1+linux", "vnext", "v0.3.0 ", "refs/tags/v0.3.0", "release-v0.3.0"} {
		if got := runTool(t, nil, nil, "tag", tag); got.code != 1 || got.stdout != "" || !strings.Contains(got.stderr, "is not a release tag") {
			t.Errorf("tag %q: %+v", tag, got)
		}
	}
}

func TestPromoteMovesStableOnlyAsTheReleaseRulesAllow(t *testing.T) {
	for _, check := range []struct {
		event, version, current string
		want                    string // the direction, or what the refusal mentions
		refused                 bool
	}{
		{event: "push", version: "0.3.0", want: "first"},
		{event: "push", version: "0.3.1", current: "0.3.0", want: "forward"},
		{event: "push", version: "0.3.0", current: "0.3.0", want: "same"},
		{event: "push", version: "0.3.0", current: "0.3.1", want: "only moves it forward", refused: true},
		{event: "push", version: "0.3.0", current: "0.3.0-rc.2", want: "forward"},
		{event: "push", version: "0.10.0", current: "0.9.0", want: "forward"},
		{event: "workflow_dispatch", version: "0.3.0", current: "0.3.1", want: "back"},
		{event: "workflow_dispatch", version: "0.3.1", current: "0.3.0", want: "forward"},
		{event: "workflow_dispatch", version: "0.3.0", want: "first"},
		// A rehearsal before the first release, and none after it.
		{event: "push", version: "0.3.0-rc.1", want: "never points /stable at a pre-release", refused: true},
		{event: "workflow_dispatch", version: "0.3.0-rc.1", want: "first"},
		{event: "workflow_dispatch", version: "0.3.0-rc.2", current: "0.3.0-rc.1", want: "forward"},
		{event: "workflow_dispatch", version: "0.3.0-rc.1", current: "0.3.0-rc.2", want: "back"},
		{event: "workflow_dispatch", version: "0.3.1-rc.1", current: "0.3.0", want: "only before the first release", refused: true},
		{event: "workflow_dispatch", version: "0.4.0-rc.1", current: "0.3.1", want: "only before the first release", refused: true},
		{event: "push", version: "0.3.1", current: "not a version", want: "/stable does not name a version", refused: true},
		{event: "push", version: "v0.3.1", current: "0.3.0", want: "v0.3.1", refused: true},
		{event: "schedule", version: "0.3.1", current: "0.3.0", want: "not push or workflow_dispatch", refused: true},
	} {
		got := runTool(t, nil, nil, "promote", check.event, check.version, check.current)
		switch {
		case check.refused && (got.code != 1 || got.stdout != "" || !strings.Contains(got.stderr, check.want)):
			t.Errorf("%s of %s over %q: %+v, want a refusal that mentions %q", check.event, check.version, check.current, got, check.want)
		case !check.refused && (got.code != 0 || got.stdout != check.want+"\n"):
			t.Errorf("%s of %s over %q: %+v, want %s", check.event, check.version, check.current, got, check.want)
		}
	}
}

func TestUsageForACommandLineItDoesNotKnow(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"publish"}, {"keygen"}, {"keygen", "a", "b"}, {"keys", "RELEASE_SIGNING_KEY"}, {"manifest", "dir", "0.3.0"}, {"promote", "push", "0.3.0"}, {"formula", "dir"}, {"scoop", "dir", "0.3.0", "0.3.1"}} {
		if got := runTool(t, nil, nil, args...); got.code != 2 || got.stdout != "" || !strings.HasPrefix(got.stderr, "Usage:") {
			t.Errorf("%q: %+v", args, got)
		}
	}
}

// update rewrites the files the formula and Scoop manifest tests compare
// with: go test ./tools/release -run 'Formula|Scoop' -update
var update = flag.Bool("update", false, "rewrite the expected formula and Scoop manifest, in testdata and in the repository")

// expect compares got with the file at path, or with -update writes got there.
// A checkout on Windows may have turned the file's line endings into CRLF.
func expect(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("%s is not what the release tool writes, which is:\n%s\nIf the change is meant, run go test ./tools/release -run 'Formula|Scoop' -update and read the diff.", path, got)
	}
}

// sum is the SHA-256 releaseDir lists for an archive.
func sum(name string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte("the contents of "+name)))
}

func TestFormulaInstallsTheArchiveForEachSystemAndCPU(t *testing.T) {
	got := runTool(t, nil, nil, "formula", releaseDir(t), "0.3.0")
	if got.code != 0 || got.stderr != "" {
		t.Fatalf("formula: %+v", got)
	}
	expect(t, filepath.Join("testdata", "string.rb"), got.stdout)

	// Homebrew takes the url and sha256 in the on_macos or on_linux block
	// for the machine's system, and in it the on_arm or on_intel block for
	// its CPU.
	systems := regexp.MustCompile(`(?ms)^  on_(macos|linux) do\n(.*?)^  end$`)
	cpus := regexp.MustCompile(`(?m)^    on_(arm|intel) do\n      url "([^"]*)"\n      sha256 "([^"]*)"\n    end$`)
	found := map[string]string{}
	for _, system := range systems.FindAllStringSubmatch(got.stdout, -1) {
		for _, cpu := range cpus.FindAllStringSubmatch(system[2], -1) {
			platform := map[string]string{"macos": "darwin", "linux": "linux"}[system[1]] + "_" + map[string]string{"arm": "arm64", "intel": "amd64"}[cpu[1]]
			found[platform] = cpu[2] + " " + cpu[3]
		}
	}
	want := map[string]string{}
	for _, p := range platforms {
		if p.goos != "windows" {
			name := "string_" + p.goos + "_" + p.goarch + p.ext
			want[p.goos+"_"+p.goarch] = "https://github.com/usestring/cli/releases/download/v0.3.0/" + name + " " + sum(name)
		}
	}
	if !maps.Equal(found, want) {
		t.Errorf("the formula gives these archives:\n%v\nwant:\n%v", found, want)
	}
	if strings.Contains(got.stdout, "depends_on") || strings.Contains(got.stdout, "system ") {
		t.Error("the formula builds string rather than installing the release's binary")
	}
}

func TestScoopManifestInstallsTheWindowsArchiveForEachCPU(t *testing.T) {
	got := runTool(t, nil, nil, "scoop", releaseDir(t), "0.3.0")
	if got.code != 0 || got.stderr != "" {
		t.Fatalf("scoop: %+v", got)
	}
	expect(t, filepath.Join("testdata", "string.json"), got.stdout)

	var m struct {
		Version      string
		Bin          string
		Checkver     string
		Architecture map[string]struct{ URL, Hash string }
		Autoupdate   struct {
			Architecture map[string]map[string]string
			Hash         struct{ URL string }
		}
	}
	if err := json.Unmarshal([]byte(got.stdout), &m); err != nil {
		t.Fatal(err)
	}
	base := "https://github.com/usestring/cli/releases/download/"
	for arch, name := range map[string]string{"64bit": "string_windows_amd64.zip", "arm64": "string_windows_arm64.zip"} {
		if a := m.Architecture[arch]; a.URL != base+"v0.3.0/"+name || a.Hash != sum(name) {
			t.Errorf("%s installs %+v, want %s with its SHA-256", arch, a, name)
		}
		// Scoop's own tools fill a later release in from these, with the
		// hash from that release's checksums.txt.
		if a := m.Autoupdate.Architecture[arch]; !maps.Equal(a, map[string]string{"url": base + "v$version/" + name}) {
			t.Errorf("%s updates from %v", arch, a)
		}
	}
	if len(m.Architecture) != 2 || len(m.Autoupdate.Architecture) != 2 || m.Version != "0.3.0" || m.Bin != "string.exe" || m.Checkver != "github" || m.Autoupdate.Hash.URL != "$baseurl/checksums.txt" {
		t.Errorf("the manifest reads as %+v", m)
	}
}

func TestFormulaAndScoopManifestRefuseWhatTheyCannotName(t *testing.T) {
	// missing is the last archive in platforms, so a check that stops short
	// of it fails.
	missing, listed := "string_windows_arm64.zip", "string_darwin_amd64.tar.gz"
	hash := func(change func(string) string) func(t *testing.T, dir string) {
		return func(t *testing.T, dir string) {
			rewrite(t, filepath.Join(dir, "checksums.txt"), func(s string) string {
				return strings.Replace(s, sum(listed), change(sum(listed)), 1)
			})
		}
	}
	for name, check := range map[string]struct {
		change  func(t *testing.T, dir string)
		version string
		reason  string
	}{
		// Homebrew and Scoop install what main names as soon as it is there.
		"a pre-release":         {version: "0.3.0-rc.1", reason: "0.3.0-rc.1 is a pre-release"},
		"a version with a v":    {version: "v0.3.0", reason: "v0.3.0"},
		"a development version": {version: "dev", reason: "dev"},
		// The error names the file, as in "open DIR/checksums.txt: ...".
		"no checksums.txt": {change: func(t *testing.T, dir string) { remove(t, filepath.Join(dir, "checksums.txt")) }, reason: "checksums.txt: "},
		"an archive checksums.txt does not list": {change: func(t *testing.T, dir string) {
			rewrite(t, filepath.Join(dir, "checksums.txt"), func(s string) string { return dropLine(s, missing) })
		}, reason: "lists no " + missing},
		"a line that is not a checksum and a name": {change: func(t *testing.T, dir string) {
			rewrite(t, filepath.Join(dir, "checksums.txt"), func(s string) string { return s + "abc def ghi\n" })
		}, reason: "line 9"},
		// Each is refused by one of the three checks alone.
		"a SHA-256 in capitals":          {change: hash(strings.ToUpper), reason: "not 64 lowercase hex digits"},
		"a SHA-256 that is short":        {change: hash(func(s string) string { return s[:62] }), reason: "not 64 lowercase hex digits"},
		"a SHA-256 that is long":         {change: hash(func(s string) string { return s + "00" }), reason: "not 64 lowercase hex digits"},
		"a SHA-256 with one digit extra": {change: hash(func(s string) string { return s + "0" }), reason: "not 64 lowercase hex digits"},
		"a SHA-256 that is not hex":      {change: hash(func(s string) string { return s[:63] + "g" }), reason: "not 64 lowercase hex digits"},
	} {
		for _, command := range []string{"formula", "scoop"} {
			t.Run(command+" with "+name, func(t *testing.T) {
				dir := releaseDir(t)
				if check.change != nil {
					check.change(t, dir)
				}
				version := "0.3.0"
				if check.version != "" {
					version = check.version
				}
				got := runTool(t, nil, nil, command, dir, version)
				if got.code != 1 || got.stdout != "" || !strings.HasPrefix(got.stderr, "release "+command+": ") || !strings.Contains(got.stderr, check.reason) {
					t.Errorf("%+v, want a refusal that mentions %q", got, check.reason)
				}
			})
		}
	}
}

// The formula and the Scoop manifest in the repository are what the release
// tool writes for the version and checksums they name, so the release
// workflow's commit changes nothing else in them, and a change made to either
// file by hand fails here.
func TestTheRepositorysFormulaAndScoopManifestAreTheReleaseToolsOwn(t *testing.T) {
	formulaPath, scoopPath := filepath.Join("..", "..", "Formula", "string.rb"), filepath.Join("..", "..", "bucket", "string.json")
	formulaText, err := os.ReadFile(formulaPath)
	if err != nil {
		t.Fatal(err)
	}
	scoopText, err := os.ReadFile(scoopPath)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Version      string
		Architecture map[string]struct{ URL, Hash string }
	}
	if err := json.Unmarshal(scoopText, &m); err != nil {
		t.Fatal(err)
	}
	var sums strings.Builder
	for _, a := range m.Architecture {
		fmt.Fprintf(&sums, "%s  %s\n", a.Hash, a.URL[strings.LastIndex(a.URL, "/")+1:])
	}
	for _, a := range regexp.MustCompile(`url "[^"]*/([^/"]*)"\s+sha256 "([^"]*)"`).FindAllStringSubmatch(string(formulaText), -1) {
		fmt.Fprintf(&sums, "%s  %s\n", a[2], a[1])
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	for command, path := range map[string]string{"formula": formulaPath, "scoop": scoopPath} {
		got := runTool(t, nil, nil, command, dir, m.Version)
		if got.code != 0 {
			t.Fatalf("%s for the version and checksums in the repository: %+v", command, got)
		}
		expect(t, path, got.stdout)
	}
}
