package release

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// ErrNoTrustedKeys is Verify's answer for a build that trusts no key. Such a
// build never takes an update, rather than taking one unchecked.
var ErrNoTrustedKeys = errors.New("this build of string has no release signing key to check an update with")

// Sign returns the line manifest.json.sig carries for manifest under the key
// whose 32-byte seed is given: a standard base64 Ed25519 signature over the
// exact bytes of the manifest.
func Sign(manifest, seed []byte) (string, error) {
	if len(seed) != ed25519.SeedSize {
		return "", fmt.Errorf("a signing key seed is %d bytes, not %d", ed25519.SeedSize, len(seed))
	}
	signature := ed25519.Sign(ed25519.NewKeyFromSeed(seed), manifest)
	return base64.StdEncoding.EncodeToString(signature), nil
}

// Verify checks manifest.json.sig against the manifest's exact bytes. The sig
// file holds one signature a line, more than one only while a key is being
// replaced, and the manifest is good when any line verifies under any trusted
// key. A line that is not a signature verifies nothing.
func Verify(manifest, signatures []byte, trusted []ed25519.PublicKey) error {
	if len(trusted) == 0 {
		return ErrNoTrustedKeys
	}
	lines := 0
	for line := range bytes.SplitSeq(signatures, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		lines++
		signature, err := base64.StdEncoding.DecodeString(string(line))
		if err != nil || len(signature) != ed25519.SignatureSize {
			continue
		}
		for _, key := range trusted {
			if ed25519.Verify(key, manifest, signature) {
				return nil
			}
		}
	}
	if lines == 0 {
		return errors.New("manifest.json.sig holds no signature")
	}
	return fmt.Errorf("none of the %d signatures in manifest.json.sig is by a key this build trusts", lines)
}

// ParseKeys reads public keys written as in keys.go: standard base64, separated
// by white space.
func ParseKeys(text string) ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	for _, field := range strings.Fields(text) {
		key, err := base64.StdEncoding.DecodeString(field)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%q is not a base64 Ed25519 public key", field)
		}
		keys = append(keys, ed25519.PublicKey(key))
	}
	return keys, nil
}

// TrustedKeys are the keys this build accepts a release from.
func TrustedKeys() ([]ed25519.PublicKey, error) {
	return ParseKeys(trustedKeys)
}
