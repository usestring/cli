package release

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// schema is the manifest format this package reads. A manifest with another
// schema is refused rather than half understood.
const schema = 1

// Manifest is a release's manifest.json: the archive for each platform, with
// the size and SHA-256 it must have. Signing the manifest is what makes every
// archive it lists trusted.
type Manifest struct {
	Schema    int       `json:"schema"`
	Version   string    `json:"version"`
	Published time.Time `json:"published"`
	// Assets is keyed by GOOS_GOARCH, as in linux_amd64.
	Assets map[string]Asset `json:"assets"`
}

// Asset is one archive, fetched from the release's directory by Name.
type Asset struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Platform is the key of a platform's archive in Manifest.Assets.
func Platform(goos, goarch string) string {
	return goos + "_" + goarch
}

// ParseManifest reads manifest.json and checks everything a client goes on to
// rely on, so a manifest that passes can be used without checking it again.
// It does not check the signature: Verify does, over the same bytes.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("the manifest is not valid JSON: %w", err)
	}
	if m.Schema != schema {
		return Manifest{}, fmt.Errorf("the manifest has schema %d, and this build reads only schema %d", m.Schema, schema)
	}
	if _, err := ParseVersion(m.Version); err != nil {
		return Manifest{}, fmt.Errorf("the manifest's version: %w", err)
	}
	if len(m.Assets) == 0 {
		return Manifest{}, fmt.Errorf("the manifest lists no archives")
	}
	for platform, asset := range m.Assets {
		if !validName(asset.Name) {
			return Manifest{}, fmt.Errorf("the manifest's %s archive is named %q, which is not a bare file name", platform, asset.Name)
		}
		if decoded, err := hex.DecodeString(asset.SHA256); err != nil || len(decoded) != 32 || asset.SHA256 != strings.ToLower(asset.SHA256) {
			return Manifest{}, fmt.Errorf("the manifest's %s archive has %q for a SHA-256, which is not 64 lowercase hex digits", platform, asset.SHA256)
		}
		if asset.Size <= 0 {
			return Manifest{}, fmt.Errorf("the manifest's %s archive has size %d", platform, asset.Size)
		}
	}
	return m, nil
}

// validName allows only the characters archive names use, so a name can never
// reach outside the release's directory when it is put into a URL.
func validName(name string) bool {
	return name != "" && !strings.Contains(name, "..") &&
		strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-") == ""
}
