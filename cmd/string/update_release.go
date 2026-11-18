package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/usestring/cli/internal/release"
)

// The most each download may be and how long it may take. The host is ours,
// but a proxy or a captive portal can answer for it with anything.
const (
	pointerLimit   = 1 << 10
	manifestLimit  = 1 << 20
	signatureLimit = 4 << 10
	archiveLimit   = 64 << 20
	binaryLimit    = 64 << 20
	smallTimeout   = 15 * time.Second
	archiveTimeout = 5 * time.Minute
)

// staleAfter is how old an update lock or temporary file must be before it is
// taken for one left by an update that died: longer than an update can take.
const staleAfter = 10 * time.Minute

// The temporary files of an update. The new binary is created beside the old
// one, so that putting it in place is a rename; the archive goes to the
// system's temporary directory.
const (
	binaryTempPrefix  = ".string-update-"
	archiveTempPrefix = "string-update-"
)

// downloadBase is the host releases come from: STRING_DOWNLOAD_URL when it is
// set, and otherwise String's.
func downloadBase(env func(string) (string, bool)) (string, error) {
	value, _ := env("STRING_DOWNLOAD_URL")
	if value == "" {
		return downloadURL, nil
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(value, "?#") {
		return "", errors.New("STRING_DOWNLOAD_URL must be an HTTP(S) URL without credentials, query, or fragment")
	}
	return strings.TrimSuffix(value, "/"), nil
}

// updater fetches releases from the download host. keys are the signing keys
// a release's manifest must verify under; looking up the latest version needs
// none.
type updater struct {
	rt   runtime
	base string
	keys []ed25519.PublicKey
}

// open sends a GET for path on the download host. It sends the version and
// platform in the User-Agent and nothing else about the machine or the person.
func (u updater) open(ctx context.Context, path string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, "GET", u.base+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", fmt.Sprintf("string/%s (%s; %s)", version, goruntime.GOOS, goruntime.GOARCH))
	// An archive must arrive as the bytes its checksum is of.
	request.Header.Set("Accept-Encoding", "identity")
	response, err := u.rt.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("GET %s: HTTP %d", request.URL, response.StatusCode)
	}
	return response, nil
}

// get reads a small file from the download host, refusing one over limit.
func (u updater) get(path string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), smallTimeout)
	defer cancel()
	response, err := u.open(ctx, path)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("GET %s%s: %w", u.base, path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("GET %s%s: the response is larger than %d bytes", u.base, path, limit)
	}
	return data, nil
}

// latest is the version /stable names.
func (u updater) latest() (release.Version, error) {
	data, err := u.get("/stable", pointerLimit)
	if err != nil {
		return release.Version{}, err
	}
	v, err := release.ParseVersion(strings.TrimSpace(string(data)))
	if err != nil {
		return release.Version{}, fmt.Errorf("%s/stable: %w", u.base, err)
	}
	return v, nil
}

// unwritableDirectory is an update refused because string cannot create a
// file beside the binary it would replace.
type unwritableDirectory struct {
	path string
	err  error
}

func (e *unwritableDirectory) Error() string {
	return fmt.Sprintf("string cannot write to %s, so it cannot replace %s: %v", filepath.Dir(e.path), e.path, e.err)
}

func (e *unwritableDirectory) Unwrap() error { return e.err }

// install puts release v in place of the binary at path. Each step checks
// what the one before it fetched: the manifest's signature, its version, the
// archive's size and checksum, and the version the new binary prints. Nothing
// at path changes unless every check passes.
func (u updater) install(v release.Version, path string) error {
	dir := "/v" + v.String() + "/"
	manifestData, err := u.get(dir+"manifest.json", manifestLimit)
	if err != nil {
		return err
	}
	signatures, err := u.get(dir+"manifest.json.sig", signatureLimit)
	if err != nil {
		return err
	}
	if err := release.Verify(manifestData, signatures, u.keys); err != nil {
		return fmt.Errorf("v%s/manifest.json: %w", v, err)
	}
	manifest, err := release.ParseManifest(manifestData)
	if err != nil {
		return fmt.Errorf("v%s/manifest.json: %w", v, err)
	}
	if manifest.Version != v.String() {
		return fmt.Errorf("v%s/manifest.json is the manifest of %s", v, manifest.Version)
	}
	platform := release.Platform(goruntime.GOOS, goruntime.GOARCH)
	asset, ok := manifest.Assets[platform]
	if !ok {
		return fmt.Errorf("string %s has no archive for %s", v, platform)
	}
	if asset.Size > archiveLimit {
		return fmt.Errorf("string %s's archive for %s is %d bytes, more than an update takes", v, platform, asset.Size)
	}

	// Creating the new binary before the archive is downloaded finds a
	// directory string cannot write to without downloading anything large.
	pattern := binaryTempPrefix + "*"
	if goruntime.GOOS == "windows" {
		// Windows runs only a file named as a program.
		pattern += ".exe"
	}
	binary, err := os.CreateTemp(filepath.Dir(path), pattern)
	if err != nil {
		return &unwritableDirectory{path: path, err: err}
	}
	placed := false
	defer func() {
		binary.Close()
		if !placed {
			os.Remove(binary.Name())
		}
	}()
	archive, err := os.CreateTemp("", archiveTempPrefix+"*")
	if err != nil {
		return err
	}
	defer func() {
		archive.Close()
		os.Remove(archive.Name())
	}()
	if err := u.download(archive, dir+asset.Name, asset); err != nil {
		return err
	}
	if err := extractBinary(archive, asset, binary); err != nil {
		return err
	}
	// On disk before the rename, or a power loss soon after it can leave an
	// empty file at path on filesystems that write data after metadata.
	if err := binary.Sync(); err != nil {
		return err
	}
	if err := binary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(binary.Name(), 0o755); err != nil {
		return err
	}
	want := "string " + v.String() + "\n"
	printed, err := u.rt.printedVersion(binary.Name())
	if err != nil {
		return fmt.Errorf("the new binary did not run: %w", err)
	}
	if string(printed) != want {
		return fmt.Errorf("the new binary printed %q for its version, not %q", printed, want)
	}
	if err := swapBinary(binary.Name(), path); err != nil {
		return err
	}
	placed = true
	return nil
}

// download writes the archive to file, checking it has exactly the size and
// SHA-256 its manifest gives.
func (u updater) download(file *os.File, path string, asset release.Asset) error {
	ctx, cancel := context.WithTimeout(context.Background(), archiveTimeout)
	defer cancel()
	response, err := u.open(ctx, path)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, asset.Size+1))
	if err != nil {
		return fmt.Errorf("downloading %s: %w", asset.Name, err)
	}
	if size != asset.Size {
		return fmt.Errorf("%s came to %d bytes, and its manifest gives %d", asset.Name, size, asset.Size)
	}
	if hex.EncodeToString(hash.Sum(nil)) != asset.SHA256 {
		return fmt.Errorf("%s does not have the SHA-256 its manifest gives", asset.Name)
	}
	return nil
}

// extractBinary copies the archive's binary into out: the regular file named
// string, or string.exe on Windows, at the archive's root. An entry with a
// path in its name is never it.
func extractBinary(archive *os.File, asset release.Asset, out io.Writer) error {
	entry := "string"
	if goruntime.GOOS == "windows" {
		entry = "string.exe"
	}
	switch {
	case strings.HasSuffix(asset.Name, ".zip"):
		files, err := zip.NewReader(archive, asset.Size)
		if err != nil {
			return fmt.Errorf("%s: %w", asset.Name, err)
		}
		for _, file := range files.File {
			if file.Name != entry {
				continue
			}
			if !file.Mode().IsRegular() {
				return fmt.Errorf("%s: %s is not a regular file", asset.Name, entry)
			}
			contents, err := file.Open()
			if err != nil {
				return fmt.Errorf("%s: %w", asset.Name, err)
			}
			defer contents.Close()
			return copyBinary(out, contents, asset.Name)
		}
	case strings.HasSuffix(asset.Name, ".tar.gz"):
		if _, err := archive.Seek(0, io.SeekStart); err != nil {
			return err
		}
		unzipped, err := gzip.NewReader(archive)
		if err != nil {
			return fmt.Errorf("%s: %w", asset.Name, err)
		}
		entries := tar.NewReader(unzipped)
		for {
			header, err := entries.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("%s: %w", asset.Name, err)
			}
			if header.Name != entry {
				continue
			}
			if header.Typeflag != tar.TypeReg {
				return fmt.Errorf("%s: %s is not a regular file", asset.Name, entry)
			}
			return copyBinary(out, entries, asset.Name)
		}
	default:
		return fmt.Errorf("%s is neither a .tar.gz nor a .zip archive", asset.Name)
	}
	return fmt.Errorf("%s holds no %s", asset.Name, entry)
}

func copyBinary(out io.Writer, in io.Reader, archive string) error {
	size, err := io.Copy(out, io.LimitReader(in, binaryLimit+1))
	if err != nil {
		return fmt.Errorf("%s: %w", archive, err)
	}
	if size > binaryLimit {
		return fmt.Errorf("%s: the binary is larger than %d bytes", archive, binaryLimit)
	}
	return nil
}

// errUpdateRunning is takeUpdateLock's answer while another update holds it.
var errUpdateRunning = errors.New("another update is running")

// updateLock is update.lock, held by the one update that may replace a binary
// at a time. It is a file that exists while held, not one kept open, so that
// a stale lock can be renamed aside on Windows.
type updateLock struct {
	path string
	file os.FileInfo
}

// takeUpdateLock creates update.lock, or takes it over when the update that
// created it has been gone for longer than any update runs. Taking over moves
// the stale file aside first: only one process can win that rename, whereas
// two that each removed the file could both go on to create it.
func takeUpdateLock(path string) (*updateLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	for range 2 {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			info, err := file.Stat()
			file.Close()
			if err != nil {
				os.Remove(path)
				return nil, err
			}
			return &updateLock{path: path, file: info}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		held, err := os.Stat(path)
		if err != nil {
			continue
		}
		if time.Since(held.ModTime()) < staleAfter {
			return nil, errUpdateRunning
		}
		aside := path + "." + randomSuffix() + ".stale"
		if os.Rename(path, aside) != nil {
			return nil, errUpdateRunning
		}
		// Another process may have taken the stale lock over between the
		// check and the rename, so what was moved aside could be its live one.
		if moved, err := os.Stat(aside); err != nil || !os.SameFile(held, moved) {
			os.Rename(aside, path)
			return nil, errUpdateRunning
		}
		os.Remove(aside)
	}
	return nil, errUpdateRunning
}

// release removes the lock, unless another update has since taken it over.
func (l *updateLock) release() {
	if current, err := os.Stat(l.path); err == nil && os.SameFile(current, l.file) {
		os.Remove(l.path)
	}
}

func randomSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// removeLeftovers deletes what earlier updates could leave behind: a binary
// Windows had them move aside, and the temporary files of one that was killed.
// A file still in use, or one a running update may still be writing, stays.
func removeLeftovers(path string) {
	removeAsides(path)
	removeStale := func(dir, prefix string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > staleAfter {
				os.Remove(filepath.Join(dir, entry.Name()))
			}
		}
	}
	removeStale(filepath.Dir(path), binaryTempPrefix)
	removeStale(os.TempDir(), archiveTempPrefix)
}

// updateState is update.json: what the daily check last found and did, what
// the notices have already said, so that each says it once, and the release
// that last rewrote the skill.
type updateState struct {
	CheckedAt time.Time `json:"checked_at,omitzero"`
	Latest    string    `json:"latest,omitempty"`
	Error     string    `json:"error,omitempty"`
	// The last update the background installed, and whether a command has
	// said so on a terminal yet.
	UpdatedFrom     string    `json:"updated_from,omitempty"`
	UpdatedTo       string    `json:"updated_to,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitzero"`
	UpdateAnnounced bool      `json:"update_announced,omitempty"`
	// The release a notice last said was out, and when.
	NoticedVersion string    `json:"noticed_version,omitempty"`
	NoticedAt      time.Time `json:"noticed_at,omitzero"`
	SeenVersion    string    `json:"seen_version,omitempty"`
}

func readUpdateState(path string) updateState {
	var state updateState
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	return state
}

// changeUpdateState rereads update.json just before writing it back with
// change made, so that what other string processes wrote meanwhile stays.
func changeUpdateState(path string, change func(*updateState)) error {
	state := readUpdateState(path)
	change(&state)
	return writePrivate(path, state)
}
