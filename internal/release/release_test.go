package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

// The order decides whether a copy updates, so a pre-release must never sort
// above its release and 0.10.0 must sort above 0.9.0.
func TestVersionsSortBySemverPrecedence(t *testing.T) {
	ordered := []string{
		"0.2.0", "0.9.0", "0.10.0",
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0",
		"1.0.1-SNAPSHOT-1f08b44", "1.0.1", "1.1.0", "2.0.0",
	}
	parsed := make([]Version, len(ordered))
	for i, text := range ordered {
		v, err := ParseVersion(text)
		if err != nil {
			t.Fatal(err)
		}
		parsed[i] = v
	}
	for i := range parsed {
		for j := range parsed {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := parsed[i].Compare(parsed[j]); got != want {
				t.Errorf("%s compared with %s gives %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
	a, _ := ParseVersion("1.0.0+linux")
	b, _ := ParseVersion("1.0.0+darwin")
	if a.Compare(b) != 0 {
		t.Error("build metadata changed the order")
	}
	for text, want := range map[string]bool{"1.0.0": false, "1.0.0+rc.1": false, "1.0.0-rc.1": true, "1.0.0-0+linux": true} {
		if v, _ := ParseVersion(text); v.Prerelease() != want {
			t.Errorf("%s is a pre-release: %v", text, v.Prerelease())
		}
	}
}

func TestParseVersionRefusesWhatIsNotAVersion(t *testing.T) {
	for _, text := range []string{"", "dev", "v0.3.0", "0.3", "0.3.0.1", "00.3.0", "0.03.0", "0.3.0-", "0.3.0-rc..1", "0.3.0-01", "0.3.0+", "0.3.0-rc_1", " 0.3.0", "0.3.0\n", "-1.0.0"} {
		if _, err := ParseVersion(text); err == nil {
			t.Errorf("%q parsed as a version", text)
		}
	}
}

const goodManifest = `{"schema":1,"version":"0.3.0","published":"2026-11-25T12:00:00Z","assets":{
  "linux_amd64":{"name":"string_linux_amd64.tar.gz","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":2837339},
  "windows_arm64":{"name":"string_windows_arm64.zip","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":2900000}}}`

func TestParseManifestRefusesWhatAClientCouldNotSafelyUse(t *testing.T) {
	m, err := ParseManifest([]byte(goodManifest))
	if err != nil || m.Version != "0.3.0" || m.Assets[Platform("windows", "arm64")].Name != "string_windows_arm64.zip" {
		t.Fatalf("the good manifest: %+v, %v", m, err)
	}
	for name, replace := range map[string][2]string{
		"another schema":          {`"schema":1`, `"schema":2`},
		"a version with a v":      {`"version":"0.3.0"`, `"version":"v0.3.0"`},
		"a name with a slash":     {`"string_linux_amd64.tar.gz"`, `"../string_linux_amd64.tar.gz"`},
		"a name with a backslash": {`"string_linux_amd64.tar.gz"`, `"x\\string_linux_amd64.tar.gz"`},
		"a name with two dots":    {`"string_linux_amd64.tar.gz"`, `"..string_linux_amd64.tar.gz"`},
		"a name with a query":     {`"string_linux_amd64.tar.gz"`, `"string_linux_amd64.tar.gz?x=1"`},
		"an empty name":           {`"string_linux_amd64.tar.gz"`, `""`},
		"a short checksum":        {`"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"aaaa"`},
		"an uppercase checksum":   {`"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"`},
		"a size of zero":          {`"size":2837339`, `"size":0`},
		"no archives":             {goodManifest[strings.Index(goodManifest, `"assets"`):], `"assets":{}}`},
		"text after the manifest": {`}}}`, `}}} x`},
	} {
		if _, err := ParseManifest([]byte(strings.Replace(goodManifest, replace[0], replace[1], 1))); err == nil {
			t.Errorf("a manifest with %s parsed", name)
		}
	}
}

func newKey(t *testing.T) ([]byte, ed25519.PublicKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return private.Seed(), public
}

func TestVerifyAcceptsOnlyAManifestSignedByATrustedKey(t *testing.T) {
	manifest := []byte(goodManifest)
	seed, public := newKey(t)
	otherSeed, otherPublic := newKey(t)
	signed, err := Sign(manifest, seed)
	if err != nil {
		t.Fatal(err)
	}
	byOther, _ := Sign(manifest, otherSeed)
	trusted := []ed25519.PublicKey{public}

	for name, check := range map[string]struct {
		manifest   []byte
		signatures string
		trusted    []ed25519.PublicKey
		good       bool
	}{
		"signed by the trusted key":                        {manifest, signed + "\n", trusted, true},
		"signed by another key":                            {manifest, byOther + "\n", trusted, false},
		"altered after signing":                            {[]byte(strings.Replace(goodManifest, "2837339", "2837340", 1)), signed + "\n", trusted, false},
		"with no trusted key":                              {manifest, signed + "\n", nil, false},
		"two lines, the second good, as in a key rotation": {manifest, byOther + "\r\n" + signed + "\r\n", trusted, true},
		"a line that is not a signature and a good one":    {manifest, "not base64!\n" + signed, trusted, true},
		"no signature at all":                              {manifest, "\n\n", trusted, false},
		"trusting the other key as well":                   {manifest, byOther, []ed25519.PublicKey{public, otherPublic}, true},
	} {
		err := Verify(check.manifest, []byte(check.signatures), check.trusted)
		if (err == nil) != check.good {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := Verify(manifest, []byte(signed), nil); err != ErrNoTrustedKeys {
		t.Errorf("with no trusted key the error is %v, not ErrNoTrustedKeys", err)
	}
}

func TestParseKeysRefusesAMalformedKey(t *testing.T) {
	_, public := newKey(t)
	encoded := base64.StdEncoding.EncodeToString(public)
	if keys, err := ParseKeys(" " + encoded + "\n" + encoded + " "); err != nil || len(keys) != 2 {
		t.Errorf("two good keys: %v, %v", keys, err)
	}
	for _, text := range []string{encoded[:20], "not base64!", base64.StdEncoding.EncodeToString(append(public, 0))} {
		if _, err := ParseKeys(text); err == nil {
			t.Errorf("%q parsed as a key", text)
		}
	}
	if _, err := Sign([]byte(goodManifest), make([]byte, 31)); err == nil {
		t.Error("Sign took a seed of the wrong length")
	}
}
