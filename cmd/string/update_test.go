package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/usestring/cli/internal/release"
)

func binaryName() string {
	if goruntime.GOOS == "windows" {
		return "string.exe"
	}
	return "string"
}

func archiveName() string {
	if goruntime.GOOS == "windows" {
		return "string_windows_" + goruntime.GOARCH + ".zip"
	}
	return "string_" + goruntime.GOOS + "_" + goruntime.GOARCH + ".tar.gz"
}

func thisPlatform() string { return release.Platform(goruntime.GOOS, goruntime.GOARCH) }

func setVersion(t *testing.T, v string) {
	t.Helper()
	previous := version
	version = v
	t.Cleanup(func() { version = previous })
}

func testKey(t *testing.T) ([]byte, ed25519.PublicKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return private.Seed(), public
}

// archiveOf is an archive in this platform's format with one entry, a regular
// file unless link makes it a symbolic link.
func archiveOf(t *testing.T, entry string, body []byte, link bool) []byte {
	t.Helper()
	var out bytes.Buffer
	if goruntime.GOOS == "windows" {
		files := zip.NewWriter(&out)
		header := &zip.FileHeader{Name: entry, Method: zip.Deflate}
		header.SetMode(0o755)
		if link {
			header.SetMode(os.ModeSymlink | 0o777)
		}
		w, err := files.CreateHeader(header)
		if err == nil {
			_, err = w.Write(body)
		}
		if err == nil {
			err = files.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	zipped := gzip.NewWriter(&out)
	entries := tar.NewWriter(zipped)
	header := &tar.Header{Name: entry, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
	if link {
		header, body = &tar.Header{Name: entry, Typeflag: tar.TypeSymlink, Linkname: "/bin/sh"}, nil
	}
	err := entries.WriteHeader(header)
	if err == nil {
		_, err = entries.Write(body)
	}
	if err == nil {
		err = entries.Close()
	}
	if err == nil {
		err = zipped.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// testRelease is a release as the download host serves it from /vVERSION/.
// goodRelease builds one that string update takes, and a test spoils one part.
type testRelease struct {
	version  string
	manifest release.Manifest
	archive  []byte
	seed     []byte
	// replace serves these bodies in place of the ones the parts above give.
	replace map[string][]byte
	// truncate ends the archive's response halfway, after a Content-Length
	// that promised all of it.
	truncate bool
}

// goodRelease is release v, signed with seed, whose binary prints
// "string v" for --version: the tests' binaries are that text.
func goodRelease(t *testing.T, v string, seed []byte) *testRelease {
	return releaseWith(v, seed, archiveOf(t, binaryName(), []byte("string "+v+"\n"), false))
}

func releaseWith(v string, seed, archive []byte) *testRelease {
	sum := sha256.Sum256(archive)
	return &testRelease{
		version: v,
		manifest: release.Manifest{Schema: 1, Version: v, Published: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC), Assets: map[string]release.Asset{
			thisPlatform(): {Name: archiveName(), SHA256: hex.EncodeToString(sum[:]), Size: int64(len(archive))},
		}},
		archive: archive,
		seed:    seed,
		replace: map[string][]byte{},
	}
}

// updateHost stands in for the download host and records each request.
type updateHost struct {
	url      string
	mu       sync.Mutex
	files    map[string][]byte
	handlers map[string]http.HandlerFunc
	paths    []string
	headers  []http.Header
}

func newUpdateHost(t *testing.T) *updateHost {
	h := &updateHost{files: map[string][]byte{}, handlers: map[string]http.HandlerFunc{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.paths = append(h.paths, r.URL.Path)
		h.headers = append(h.headers, r.Header.Clone())
		body, found := h.files[r.URL.Path]
		handler := h.handlers[r.URL.Path]
		h.mu.Unlock()
		switch {
		case handler != nil:
			handler(w, r)
		case found:
			w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	h.url = server.URL
	return h
}

// point makes /stable name v.
func (h *updateHost) point(v string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.files["/stable"] = []byte(v + "\n")
}

// add serves release r beside the ones already there.
func (h *updateHost) add(t *testing.T, r *testRelease) {
	t.Helper()
	manifest, err := json.Marshal(r.manifest)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := release.Sign(manifest, r.seed)
	if err != nil {
		t.Fatal(err)
	}
	dir := "/v" + r.version + "/"
	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[dir+"manifest.json"] = manifest
	h.files[dir+"manifest.json.sig"] = []byte(signature + "\n")
	h.files[dir+archiveName()] = r.archive
	maps.Copy(h.files, r.replace)
	if r.truncate {
		archive := r.archive
		h.handlers[dir+archiveName()] = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
			w.Write(archive[:len(archive)/2])
		}
	}
}

// requested is the path of every request so far.
func (h *updateHost) requested() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.paths)
}

// testCopy is a copy of string in a throwaway home, as the update tests see
// it: its binary is the text it prints for --version.
type testCopy struct {
	t        *testing.T
	home     string
	binary   string
	env      map[string]string
	keys     []ed25519.PublicKey
	spawned  []string
	terminal bool   // a person at a terminal, unless agent is set
	agent    string // the coding agent that runs the copy's commands
	piped    bool   // stdout goes somewhere other than the terminal
	now      time.Time
}

// newTestCopy installs the running version of string in dir under a new home,
// where dir is a path inside the home such as .local/bin.
func newTestCopy(t *testing.T, host *updateHost, keys []ed25519.PublicKey, dir string) *testCopy {
	t.Helper()
	// The path string resolves its binary to, which is not the one the
	// temporary directory is named by where that is under a symbolic link, as
	// on macOS, or a short name, as on Windows.
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(home, filepath.FromSlash(dir), binaryName())
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("string "+version+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The archive goes to the system's temporary directory, which each test
	// gets a fresh one of, so that a leftover shows.
	temp := t.TempDir()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, temp)
	}
	return &testCopy{
		t:      t,
		home:   home,
		binary: binary,
		env: map[string]string{
			"HOME":                home,
			"STRING_DOWNLOAD_URL": host.url,
			"STRING_API_BASE_URL": "http://127.0.0.1:1/v1",
		},
		keys: keys,
		now:  time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC),
	}
}

// rt is the runtime main would give a command of this copy. spawn records the
// check it would start, rather than starting one.
func (c *testCopy) rt(stdout, stderr io.Writer) runtime {
	return runtime{
		env: func(name string) (string, bool) {
			value, ok := c.env[name]
			return value, ok
		},
		lookPath:   func(string) (string, error) { return "", os.ErrNotExist },
		client:     http.DefaultClient,
		stdin:      strings.NewReader(""),
		stdout:     stdout,
		stderr:     stderr,
		sleep:      func(time.Duration) {},
		now:        func() time.Time { return c.now },
		executable: func() (string, error) { return c.binary, nil },
		spawn:      func(path string) error { c.spawned = append(c.spawned, path); return nil },
		versionOf:  os.ReadFile,
		keys:       func() ([]ed25519.PublicKey, error) { return c.keys, nil },
		exec:       execCtx{stdinTTY: c.terminal, stdoutTTY: c.terminal && !c.piped, stderrTTY: c.terminal, agent: c.agent},
	}
}

// run runs a command the way main does: the command, then what follows it.
func (c *testCopy) run(list ...string) result {
	var stdout, stderr bytes.Buffer
	rt := c.rt(&stdout, &stderr)
	status := run(list, rt)
	afterCommand(list, rt)
	return result{status, stdout.String(), stderr.String()}
}

// runAlone runs the command with nothing after it, which is how it must end
// with the update check after it too.
func (c *testCopy) runAlone(list ...string) result {
	var stdout, stderr bytes.Buffer
	status := run(list, c.rt(&stdout, &stderr))
	return result{status, stdout.String(), stderr.String()}
}

// notice runs the command and returns what the update check added to stderr
// after the command's own output, failing the test if it changed anything
// else about how the command ended.
func (c *testCopy) notice(list ...string) string {
	c.t.Helper()
	alone := c.runAlone(list...)
	got := c.run(list...)
	if got.status != alone.status || got.output != alone.output || !strings.HasPrefix(got.errors, alone.errors) {
		c.t.Fatalf("the update check changed how %v ended:\n%+v\nwithout it:\n%+v", list, got, alone)
	}
	return strings.TrimPrefix(got.errors, alone.errors)
}

// record writes update.json as earlier checks would have left it.
func (c *testCopy) record(state updateState) {
	c.t.Helper()
	if err := writePrivate(c.file("update.json"), state); err != nil {
		c.t.Fatal(err)
	}
}

// installed is what the copy's binary prints for --version.
func (c *testCopy) installed() string {
	c.t.Helper()
	data, err := os.ReadFile(c.binary)
	if err != nil {
		c.t.Fatal(err)
	}
	return string(data)
}

func (c *testCopy) file(name string) string {
	return filepath.Join(c.home, ".config", "string", name)
}

func (c *testCopy) state() updateState { return readUpdateState(c.file("update.json")) }

func (c *testCopy) receipt() installReceipt {
	var receipt installReceipt
	data, _ := os.ReadFile(c.file("install.json"))
	_ = json.Unmarshal(data, &receipt)
	return receipt
}

// leftovers are the files an update left in the binary's directory or the
// temporary one, other than the binary and, on Windows, the old one it moved
// aside.
func (c *testCopy) leftovers() []string {
	var found []string
	for _, dir := range []string{filepath.Dir(c.binary), os.TempDir()} {
		entries, _ := os.ReadDir(dir)
		for _, entry := range entries {
			name := entry.Name()
			aside := goruntime.GOOS == "windows" && strings.HasPrefix(name, binaryName()+".") && strings.HasSuffix(name, ".old")
			if name != binaryName() && !aside {
				found = append(found, filepath.Join(dir, name))
			}
		}
	}
	return found
}

func TestUpdateReplacesTheBinaryOnlyWhenEveryCheckPasses(t *testing.T) {
	setVersion(t, "0.3.0")
	seed, public := testKey(t)
	otherSeed, _ := testKey(t)
	withAsset := func(r *testRelease, change func(*release.Asset)) *testRelease {
		asset := r.manifest.Assets[thisPlatform()]
		change(&asset)
		r.manifest.Assets[thisPlatform()] = asset
		return r
	}
	const sig = "/v0.3.1/manifest.json.sig"
	// Each case spoils one part of a release that is otherwise good.
	// signed is whether the spoiled part comes after the signature check,
	// which nothing is downloaded before.
	cases := []struct {
		name    string
		release func() *testRelease
		signed  bool
	}{
		{"signed with a key this build does not trust", func() *testRelease { r := goodRelease(t, "0.3.1", seed); r.seed = otherSeed; return r }, false},
		{"with a signature file over 4 KiB", func() *testRelease {
			r := goodRelease(t, "0.3.1", seed)
			r.replace[sig] = bytes.Repeat([]byte("A"), 4<<10+1)
			return r
		}, false},
		{"whose manifest is of another version than /stable names", func() *testRelease {
			r := goodRelease(t, "0.3.1", seed)
			r.manifest.Version = "0.3.2"
			return r
		}, false},
		{"whose manifest has another schema", func() *testRelease { r := goodRelease(t, "0.3.1", seed); r.manifest.Schema = 2; return r }, false},
		{"with no archive for this platform", func() *testRelease {
			r := goodRelease(t, "0.3.1", seed)
			r.manifest.Assets = map[string]release.Asset{"plan9_mips": r.manifest.Assets[thisPlatform()]}
			return r
		}, false},
		{"whose archive has another checksum", func() *testRelease {
			return withAsset(goodRelease(t, "0.3.1", seed), func(a *release.Asset) { a.SHA256 = strings.Repeat("0", 64) })
		}, true},
		{"whose archive is longer than its manifest says", func() *testRelease {
			return withAsset(goodRelease(t, "0.3.1", seed), func(a *release.Asset) { a.Size-- })
		}, true},
		{"whose archive is shorter than its manifest says", func() *testRelease {
			return withAsset(goodRelease(t, "0.3.1", seed), func(a *release.Asset) { a.Size++ })
		}, true},
		{"whose archive is cut off on the way", func() *testRelease { r := goodRelease(t, "0.3.1", seed); r.truncate = true; return r }, true},
		{"whose archive holds no binary", func() *testRelease {
			return releaseWith("0.3.1", seed, archiveOf(t, "README.md", []byte("string 0.3.1\n"), false))
		}, true},
		{"whose binary has a path in its name", func() *testRelease {
			return releaseWith("0.3.1", seed, archiveOf(t, "bin/"+binaryName(), []byte("string 0.3.1\n"), false))
		}, true},
		{"whose binary is a symbolic link", func() *testRelease {
			return releaseWith("0.3.1", seed, archiveOf(t, binaryName(), []byte("/bin/sh"), true))
		}, true},
		{"whose binary prints another version", func() *testRelease {
			return releaseWith("0.3.1", seed, archiveOf(t, binaryName(), []byte("string 0.3.9\n"), false))
		}, true},
	}

	// The same release unspoiled goes in, so each case below is refused for
	// its one spoiled part and nothing else.
	t.Run("a good release", func(t *testing.T) {
		host := newUpdateHost(t)
		host.point("0.3.1")
		host.add(t, goodRelease(t, "0.3.1", seed))
		c := newTestCopy(t, host, []ed25519.PublicKey{public}, ".local/bin")
		c.env["STRING_API_KEY"] = "secret-key"
		got := c.run("update")
		if got.status != 0 || c.installed() != "string 0.3.1\n" {
			t.Fatalf("%+v, and the binary prints %q", got, c.installed())
		}
		if left := c.leftovers(); len(left) > 0 {
			t.Errorf("the update left %v behind", left)
		}
		if info, _ := os.Stat(c.binary); goruntime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
			t.Errorf("the new binary has mode %v", info.Mode())
		}
		want := "string/0.3.0 (" + goruntime.GOOS + "; " + goruntime.GOARCH + ")"
		for i, header := range host.headers {
			if header.Get("User-Agent") != want || header.Get("Authorization") != "" || header.Get("Cookie") != "" || strings.Contains(strings.Join(slices.Concat(slices.Collect(maps.Values(header))...), " "), "secret-key") {
				t.Errorf("request %d for %s carried %v", i, host.paths[i], header)
			}
		}
	})
	for _, check := range cases {
		t.Run(check.name, func(t *testing.T) {
			host := newUpdateHost(t)
			host.point("0.3.1")
			host.add(t, check.release())
			c := newTestCopy(t, host, []ed25519.PublicKey{public}, ".local/bin")
			got := c.run("update")
			if got.status == 0 || c.installed() != "string 0.3.0\n" {
				t.Errorf("%+v, and the binary prints %q", got, c.installed())
			}
			if left := c.leftovers(); len(left) > 0 {
				t.Errorf("the update left %v behind", left)
			}
			if downloaded := slices.Contains(host.requested(), "/v0.3.1/"+archiveName()); downloaded != check.signed {
				t.Errorf("the archive was downloaded: %v; requests %v", downloaded, host.requested())
			}
		})
	}
	t.Run("with a /stable over 1 KiB", func(t *testing.T) {
		host := newUpdateHost(t)
		host.files["/stable"] = append(bytes.Repeat([]byte(" "), 1<<10), "0.3.1\n"...)
		host.add(t, goodRelease(t, "0.3.1", seed))
		c := newTestCopy(t, host, []ed25519.PublicKey{public}, ".local/bin")
		if got := c.run("update"); got.status == 0 || c.installed() != "string 0.3.0\n" || !slices.Equal(host.requested(), []string{"/stable"}) {
			t.Errorf("%+v, requests %v", got, host.requested())
		}
	})
	t.Run("from a build with no signing key", func(t *testing.T) {
		host := newUpdateHost(t)
		host.point("0.3.1")
		host.add(t, goodRelease(t, "0.3.1", seed))
		c := newTestCopy(t, host, nil, ".local/bin")
		if got := c.run("update"); got.status == 0 || c.installed() != "string 0.3.0\n" || len(host.requested()) > 0 {
			t.Errorf("%+v, requests %v", got, host.requested())
		}
	})
}

func TestUpdateLeavesACopyThatIsCurrentOrNewer(t *testing.T) {
	seed, public := testKey(t)
	for _, check := range []struct{ running, stable string }{
		{"0.3.0", "0.3.0"},
		{"0.3.0", "0.2.9"},
		{"0.3.1-rc.1", "0.3.0"},
		// A stable build never takes a pre-release from /stable, however new.
		{"0.3.0", "0.4.0-rc.1"},
	} {
		setVersion(t, check.running)
		host := newUpdateHost(t)
		host.point(check.stable)
		host.add(t, goodRelease(t, check.stable, seed))
		c := newTestCopy(t, host, []ed25519.PublicKey{public}, ".local/bin")
		got := c.run("update")
		if got.status != 0 || c.installed() != "string "+check.running+"\n" || !slices.Equal(host.requested(), []string{"/stable"}) {
			t.Errorf("%s with %s on /stable: %+v, requests %v", check.running, check.stable, got, host.requested())
		}
	}
	// A release candidate moves on to its release and to a later candidate,
	// and --version installs a pre-release over a stable build by name.
	for _, check := range []struct {
		running string
		list    []string
		want    string
	}{
		{"0.3.1-rc.1", []string{"update"}, "0.3.1"},
		{"0.3.1-rc.1", []string{"update"}, "0.3.1-rc.2"},
		{"0.3.0", []string{"update", "--version", "0.4.0-rc.1"}, "0.4.0-rc.1"},
	} {
		setVersion(t, check.running)
		host := newUpdateHost(t)
		host.point(check.want)
		host.add(t, goodRelease(t, check.want, seed))
		c := newTestCopy(t, host, []ed25519.PublicKey{public}, ".local/bin")
		if got := c.run(check.list...); got.status != 0 || c.installed() != "string "+check.want+"\n" {
			t.Errorf("%s, %v with %s on /stable: %+v", check.running, check.list, check.want, got)
		}
	}
}

func TestUpdateVersionInstallsThatReleaseAndPinsTheCopyUntilAPlainUpdate(t *testing.T) {
	setVersion(t, "0.3.0")
	seed, public := testKey(t)
	host := newUpdateHost(t)
	host.point("0.3.1")
	host.add(t, goodRelease(t, "0.3.1", seed))
	host.add(t, goodRelease(t, "0.2.9", seed))
	c := newTestCopy(t, host, []ed25519.PublicKey{public}, ".local/bin")
	if got := c.run("update", "--register"); got.status != 0 || got.output != "" || c.receipt() != (installReceipt{Path: c.binary}) {
		t.Fatalf("register: %+v, receipt %+v", got, c.receipt())
	}

	// An older release, because it was asked for by name.
	if got := c.run("update", "--version", "v0.2.9"); got.status != 0 || c.installed() != "string 0.2.9\n" || !c.receipt().Pinned {
		t.Fatalf("--version v0.2.9: %+v, the binary prints %q, receipt %+v", got, c.installed(), c.receipt())
	}
	setVersion(t, "0.2.9")
	if got := c.run("update", "--version", "0.2.9", "--json"); got.status != 0 || !c.receipt().Pinned || decode(t, got.output).(map[string]any)["updated"] != false {
		t.Errorf("--version of the version it has: %+v", got)
	}
	if got := c.run("update"); got.status != 0 || c.installed() != "string 0.3.1\n" || c.receipt().Pinned {
		t.Errorf("a plain update: %+v, the binary prints %q, receipt %+v", got, c.installed(), c.receipt())
	}
	if got := c.run("update", "--version", "0.9.9"); got.status == 0 || c.installed() != "string 0.3.1\n" {
		t.Errorf("--version of a release that does not exist: %+v", got)
	}
	for _, list := range [][]string{{"update", "--version", "latest"}, {"update", "--pin"}, {"update", "--check", "--register"}, {"update", "now"}} {
		if got := c.run(list...); got.status != 2 {
			t.Errorf("%v: %+v", list, got)
		}
	}
}

func TestUpdateClassifiesEachInstallFromItsPath(t *testing.T) {
	for _, check := range []struct {
		path, registered, goos string
		want                   installKind
	}{
		{"/home/u/.local/bin/string", "/home/u/.local/bin/string", "linux", installScript},
		{"/home/u/.local/bin/string", "/home/u/bin/string", "linux", installOther},
		{"/home/u/.local/bin/string", "/home/u/.local/bin/String", "linux", installOther},
		{`D:\Tools\string.exe`, `d:\tools\STRING.EXE`, "windows", installScript},
		{"/opt/homebrew/Cellar/string/0.3.0/bin/string", "", "darwin", installHomebrew},
		{"/opt/homebrew/bin/string", "", "darwin", installHomebrew},
		// npm before Homebrew: an npm that Homebrew installed keeps its global
		// packages under Homebrew's directory.
		{"/opt/homebrew/lib/node_modules/@usestring/cli/bin/string", "", "darwin", installNPM},
		{"/home/u/.npm/_npx/4f3a/node_modules/@usestring/cli/bin/string", "", "linux", installNPX},
		{`C:\ProgramData\scoop\apps\string\current\string.exe`, "", "windows", installScoop},
		{"/usr/bin/string", "", "linux", installOther},
		{"/home/u/scoop/string", "", "linux", installOther},
	} {
		if got := classifyInstall(check.path, check.registered, check.goos); got != check.want {
			t.Errorf("%s (install.json %q, %s) is %s, want %s", check.path, check.registered, check.goos, got, check.want)
		}
	}
}

func TestUpdateLeavesACopyAPackageManagerInstalledToThatManager(t *testing.T) {
	setVersion(t, "0.3.0")
	seed, public := testKey(t)
	for dir, command := range map[string]string{
		"Cellar/string/0.3.0/bin":             "brew upgrade usestring/cli/string",
		"lib/node_modules/@usestring/cli/bin": "npm install -g @usestring/cli@latest",
		"scoop/apps/string/current":           "scoop update scoop string",
	} {
		host := newUpdateHost(t)
		host.point("0.3.1")
		host.add(t, goodRelease(t, "0.3.1", seed))
		c := newTestCopy(t, host, []ed25519.PublicKey{public}, dir)
		got := c.run("update", "--json")
		var refusal struct {
			Error struct {
				NextStep string `json:"next_step"`
			} `json:"error"`
		}
		_ = json.Unmarshal([]byte(got.errors), &refusal)
		if got.status != 1 || !strings.Contains(refusal.Error.NextStep, command) || c.installed() != "string 0.3.0\n" || !slices.Equal(host.requested(), []string{"/stable"}) {
			t.Errorf("%s: %+v, requests %v", dir, got, host.requested())
		}
		for _, stable := range []string{"0.3.0", "0.4.0-rc.1"} {
			host.point(stable)
			if got := c.run("update"); got.status != 0 || got.errors != "" {
				t.Errorf("%s with %s on /stable: %+v", dir, stable, got)
			}
		}
	}
}

func TestUpdateLeavesWhileAnotherUpdateHoldsTheLockAndTakesOverAStaleOne(t *testing.T) {
	setVersion(t, "0.3.0")
	seed, public := testKey(t)
	host := newUpdateHost(t)
	host.point("0.3.1")
	host.add(t, goodRelease(t, "0.3.1", seed))
	// locked is a registered copy whose update.lock was last modified at made.
	locked := func(made time.Time) (*testCopy, string) {
		t.Helper()
		c := newTestCopy(t, host, []ed25519.PublicKey{public}, ".local/bin")
		c.run("update", "--register")
		lock := c.file("update.lock")
		if err := os.WriteFile(lock, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(lock, made, made); err != nil {
			t.Fatal(err)
		}
		return c, lock
	}

	c, _ := locked(time.Now())
	if got := c.run("update", "--background"); got.status != 0 || got.output+got.errors != "" || len(host.requested()) > 0 || c.state() != (updateState{}) {
		t.Errorf("the background update ran beside a live lock: %+v, requests %v, state %+v", got, host.requested(), c.state())
	}
	if got := c.run("update", "--json"); got.status != 1 || !strings.Contains(got.errors, `"code":"update_running"`) || c.installed() != "string 0.3.0\n" {
		t.Errorf("string update ran beside a live lock: %+v", got)
	}

	for _, made := range []time.Time{
		time.Now().Add(-staleAfter - time.Minute), // by an update that was killed
		time.Now().Add(staleAfter + time.Minute),  // while the clock was ahead
	} {
		c, lock := locked(made)
		if got := c.run("update", "--background"); got.status != 0 || c.installed() != "string 0.3.1\n" {
			t.Errorf("the background update did not take over a lock made at %v: %+v, state %+v", made, got, c.state())
		}
		if _, err := os.Stat(lock); !os.IsNotExist(err) {
			t.Errorf("the lock made at %v is still there after the update: %v", made, err)
		}
		if entries, _ := os.ReadDir(filepath.Dir(lock)); slices.ContainsFunc(entries, func(e os.DirEntry) bool { return strings.HasPrefix(e.Name(), "update.lock.") }) {
			t.Errorf("the lock made at %v was left aside: %v", made, entries)
		}
	}
}

func TestUpdateCheckReportsHowThisCopyIsUpdated(t *testing.T) {
	setVersion(t, "0.3.0")
	_, public := testKey(t)
	host := newUpdateHost(t)
	host.point("0.3.1")
	check := func(c *testCopy) map[string]any {
		t.Helper()
		got := c.run("update", "--check", "--json")
		if got.status != 0 {
			t.Fatalf("--check: %+v", got)
		}
		return decode(t, got.output).(map[string]any)
	}
	c := newTestCopy(t, host, []ed25519.PublicKey{public}, ".local/bin")
	c.run("update", "--register")
	if report := check(c); report["install"] != "script" || report["updates"] != "automatic" || report["latest"] != "0.3.1" || report["command"] != "string update" {
		t.Errorf("a copy from the install script: %v", report)
	}
	c.run("update", "--register", "--pin")
	if report := check(c); report["updates"] != "off" || report["reason"] == nil {
		t.Errorf("a pinned copy: %v", report)
	}
	c.run("update", "--register")
	c.env["CI"] = "true"
	if report := check(c); report["updates"] != "off" || report["reason"] != "CI is set" {
		t.Errorf("in CI: %v", report)
	}
	for _, value := range []string{"false", "0"} {
		c.env["CI"] = value
		if report := check(c); report["updates"] != "automatic" {
			t.Errorf("with CI=%s: %v", value, report)
		}
	}
	brew := newTestCopy(t, host, []ed25519.PublicKey{public}, "Cellar/string/0.3.0/bin")
	if report := check(brew); report["install"] != "homebrew" || report["updates"] != "notify" || report["command"] != "brew upgrade usestring/cli/string" {
		t.Errorf("a copy from Homebrew: %v", report)
	}
	host.files["/stable"] = []byte("not a version\n")
	if report := check(brew); report["latest"] != nil || report["latest_error"] == nil {
		t.Errorf("with /stable unreadable: %v", report)
	}
}
