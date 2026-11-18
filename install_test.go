// The installers, run the way a person runs them, against a local server that
// holds builds of string in a release's layout: install.sh through sh on macOS
// and Linux, and install.ps1 through each PowerShell on Windows.
package cli_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
)

// These make this test binary stand in for string. runningEnv makes it a copy
// that is running while an installer replaces it. noisyEnv makes it release
// 0.3.1 with an init that writes to stderr, as init does while it signs in,
// and leaves a file named initialized in the directory noisyEnv names.
const (
	runningEnv = "STRING_INSTALL_TEST_RUNNING"
	noisyEnv   = "STRING_INSTALL_TEST_NOISY"
)

var buildDir string

func TestMain(m *testing.M) {
	if os.Getenv(runningEnv) != "" {
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	}
	if dir := os.Getenv(noisyEnv); dir != "" {
		switch {
		case slices.Equal(os.Args[1:], []string{"--version"}):
			fmt.Println("string 0.3.1")
		case len(os.Args) > 1 && os.Args[1] == "init":
			fmt.Fprintln(os.Stderr, "Signing in...")
			os.WriteFile(filepath.Join(dir, "initialized"), nil, 0o644)
		}
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "string-builds-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	buildDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

type build struct {
	once   sync.Once
	binary []byte
	err    error
}

var builds sync.Map

// release is string built as version v for this system.
func release(t *testing.T, v string) []byte {
	t.Helper()
	stored, _ := builds.LoadOrStore(v, &build{})
	b := stored.(*build)
	b.once.Do(func() {
		out := filepath.Join(buildDir, v, exe("string"))
		// With no OAuth client a build cannot sign in, so string init, which
		// the installers run, never starts a sign-in with String's production
		// service, whatever the environment the tests give it.
		ldflags := "-s -w -X main.version=" + v + " -X main.oauthClientID="
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", ldflags, "-o", out, "./cmd/string")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if output, err := cmd.CombinedOutput(); err != nil {
			b.err = fmt.Errorf("go build: %v\n%s", err, output)
			return
		}
		b.binary, b.err = os.ReadFile(out)
	})
	if b.err != nil {
		t.Fatal(b.err)
	}
	return b.binary
}

var registering = sync.OnceValues(func() (bool, error) {
	home, err := os.MkdirTemp("", "string-register-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(home)
	binary := filepath.Join(home, exe("string"))
	data, err := os.ReadFile(filepath.Join(buildDir, "0.3.1", exe("string")))
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(binary, data, 0o755); err != nil {
		return false, err
	}
	cmd := exec.Command(binary, "update", "--register")
	cmd.Env = []string{"HOME=" + home, "USERPROFILE=" + home}
	if cmd.Run() != nil {
		return false, nil
	}
	_, err = os.Stat(filepath.Join(home, ".config", "string", "install.json"))
	return err == nil, nil
})

// registers reports whether this build of string has string update
// --register, which releases from before it lack.
func registers(t *testing.T) bool {
	t.Helper()
	release(t, "0.3.1")
	ok, err := registering()
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func archiveName() string {
	if runtime.GOOS == "windows" {
		return "string_windows_" + runtime.GOARCH + ".zip"
	}
	return "string_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
}

// releaseArchive packs files the way a release packs the binary, beside
// README.md and LICENSE.
func releaseArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	names := slices.Sorted(func(yield func(string) bool) {
		for name := range files {
			if !yield(name) {
				return
			}
		}
	})
	var out bytes.Buffer
	if runtime.GOOS == "windows" {
		w := zip.NewWriter(&out)
		for _, name := range names {
			f, err := w.Create(name)
			if err == nil {
				_, err = f.Write(files[name])
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	gz := gzip.NewWriter(&out)
	w := tar.NewWriter(gz)
	for _, name := range names {
		err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(files[name])), Typeflag: tar.TypeReg})
		if err == nil {
			_, err = w.Write(files[name])
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func checksums(files map[string][]byte) []byte {
	var out bytes.Buffer
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for name := range files {
			if !yield(name) {
				return
			}
		}
	}) {
		fmt.Fprintf(&out, "%x  %s\n", sha256.Sum256(files[name]), name)
	}
	return out.Bytes()
}

// releaseHost serves what cli.usestring.ai does, /stable and each release
// under /v<version>/, or, through publishOnGitHub, what GitHub serves under
// the repository's /releases.
type releaseHost struct {
	url      string
	mu       sync.Mutex
	files    map[string][]byte
	requests []string
}

func newReleaseHost(t *testing.T) *releaseHost {
	h := &releaseHost{files: map[string][]byte{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.requests = append(h.requests, r.URL.Path)
		body, ok := h.files[r.URL.Path]
		h.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(server.Close)
	h.url = server.URL
	return h
}

func (h *releaseHost) put(path string, body []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[path] = body
}

func (h *releaseHost) drop(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.files, path)
}

// holding packs binary as a release's archive for this system.
func holding(t *testing.T, binary []byte) []byte {
	t.Helper()
	return releaseArchive(t, map[string][]byte{exe("string"): binary, "README.md": []byte("# string\n"), "LICENSE": []byte("MIT\n")})
}

var archives sync.Map

// releaseArchiveOf is the archive of release v for this system, packed once.
func releaseArchiveOf(t *testing.T, v string) []byte {
	t.Helper()
	if archive, ok := archives.Load(v); ok {
		return archive.([]byte)
	}
	archive := holding(t, release(t, v))
	archives.Store(v, archive)
	return archive
}

// publish puts release v for this system on the host, with the archive for
// another system listed in checksums.txt as well.
func (h *releaseHost) publish(t *testing.T, v string) {
	h.publishArchive(v, releaseArchiveOf(t, v))
}

func (h *releaseHost) publishArchive(v string, archive []byte) {
	h.put("/v"+v+"/"+archiveName(), archive)
	h.put("/v"+v+"/checksums.txt", checksums(map[string][]byte{archiveName(): archive, "string_plan9_amd64.tar.gz": []byte("another system's")}))
}

func (h *releaseHost) point(v string) { h.put("/stable", []byte(v+"\n")) }

// publishOnGitHub puts release v where GitHub serves a release's files, and
// where it serves the latest release's as well when latest is set.
func (h *releaseHost) publishOnGitHub(t *testing.T, v string, latest bool) {
	archive := releaseArchiveOf(t, v)
	sums := checksums(map[string][]byte{archiveName(): archive})
	dirs := []string{"/download/v" + v}
	if latest {
		dirs = append(dirs, "/latest/download")
	}
	for _, dir := range dirs {
		h.put(dir+"/"+archiveName(), archive)
		h.put(dir+"/checksums.txt", sums)
	}
}

func (h *releaseHost) requested() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.requests)
}

// machine is a home of its own, with a temporary directory of its own, whose
// installers download from stand-ins for the release host and for GitHub.
type machine struct {
	t            *testing.T
	home, temp   string
	host, github *releaseHost
	env          map[string]string
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	// Real paths: on macOS the temporary directory is under a symbolic link,
	// which the path string registers itself under is not.
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	host := newReleaseHost(t)
	host.publish(t, "0.3.0")
	host.publish(t, "0.3.1")
	host.point("0.3.1")
	github := newReleaseHost(t)
	github.publishOnGitHub(t, "0.3.0", false)
	github.publishOnGitHub(t, "0.3.1", true)
	return &machine{t: t, home: home, temp: t.TempDir(), host: host, github: github, env: map[string]string{
		// The host with no fallback to GitHub, unless a test takes it out.
		"STRING_DOWNLOAD_URL": host.url,
		// Otherwise string init, from a release that has the daily check,
		// starts one that outlives the test.
		"STRING_AUTO_UPDATE": "0",
	}}
}

func (m *machine) path(elements ...string) string {
	return filepath.Join(append([]string{m.home}, elements...)...)
}

// script is the installer name with this machine's stand-ins in place of the
// default host and the GitHub releases URL, each quoted exactly once in it.
func (m *machine) script(name string) []byte {
	m.t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		m.t.Fatal(err)
	}
	text := string(data)
	for _, swap := range []struct{ real, local string }{
		{"https://cli.usestring.ai", m.host.url},
		{"https://github.com/usestring/cli/releases", m.github.url},
	} {
		found := 0
		for _, quote := range []string{`"`, `'`} {
			found += strings.Count(text, quote+swap.real+quote)
			text = strings.ReplaceAll(text, quote+swap.real+quote, quote+swap.local+quote)
		}
		if found != 1 {
			m.t.Fatalf("%s quotes %s %d times, want once", name, swap.real, found)
		}
	}
	return []byte(text)
}

// scriptFile is script(name) saved under that name, to run by its path.
func (m *machine) scriptFile(name string) string {
	m.t.Helper()
	path := filepath.Join(m.t.TempDir(), name)
	if err := os.WriteFile(path, m.script(name), 0o644); err != nil {
		m.t.Fatal(err)
	}
	return path
}

// place puts data at path as an executable, the way an earlier install left it.
func (m *machine) place(path string, data []byte) {
	m.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o755); err != nil {
		m.t.Fatal(err)
	}
}

// thisBinary is this test binary, to stand in for string.
func thisBinary(t *testing.T) []byte {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// start runs a stand-in for string from path until the test ends, and returns
// a function that reports whether it is still running.
func (m *machine) start(path string) func() bool {
	m.t.Helper()
	m.place(path, thisBinary(m.t))
	cmd := exec.Command(path)
	cmd.Env = append(os.Environ(), runningEnv+"=1")
	if err := cmd.Start(); err != nil {
		m.t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()
	m.t.Cleanup(func() {
		cmd.Process.Kill()
		<-done
	})
	return func() bool {
		select {
		case <-done:
			return false
		default:
			return true
		}
	}
}

// run runs the installer and reports whether it succeeded, and what it printed.
func (m *machine) run(cmd *exec.Cmd) (bool, string) {
	m.t.Helper()
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		m.t.Fatal(err)
	}
	return err == nil, string(output)
}

func versionAt(path string) string {
	output, err := exec.Command(path, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func names(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var list []string
	for _, entry := range entries {
		list = append(list, entry.Name())
	}
	return list
}

// leftovers is what an installer left in its temporary directory. PowerShell
// keeps files of its own there, so on Windows only the installer's count.
func (m *machine) leftovers() []string {
	var list []string
	for _, name := range names(m.temp) {
		if runtime.GOOS != "windows" || strings.HasPrefix(name, "string-install-") {
			list = append(list, name)
		}
	}
	return list
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// registration is what string update --register wrote under this home.
func (m *machine) registration() (receipt struct {
	Path   string `json:"path"`
	Pinned bool   `json:"pinned"`
}, found bool) {
	data, err := os.ReadFile(m.path(".config", "string", "install.json"))
	if err != nil {
		return receipt, false
	}
	if err := json.Unmarshal(data, &receipt); err != nil {
		m.t.Fatal(err)
	}
	return receipt, true
}

// checkRegistered fails unless the installer registered binary, pinned or
// not, wherever this build of string can register a copy.
func (m *machine) checkRegistered(binary string, pinned bool) {
	m.t.Helper()
	receipt, found := m.registration()
	if want := registers(m.t); found != want || found && (receipt.Path != binary || receipt.Pinned != pinned) {
		m.t.Errorf("install.json is %+v (found %t), want %s registered with pinned %t (this build registers: %t)", receipt, found, binary, pinned, want)
	}
}

func skipUnlessUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is for macOS and Linux")
	}
}

// installSh runs install.sh the way its one-liner does, piped into sh, with
// this machine's home and only the system's own tools on PATH.
func (m *machine) installSh(args ...string) (bool, string) {
	m.t.Helper()
	return m.installShWith("/usr/bin:/bin:/usr/sbin:/sbin", args...)
}

func (m *machine) installShWith(path string, args ...string) (bool, string) {
	m.t.Helper()
	cmd := exec.Command("sh", append([]string{"-s", "--"}, args...)...)
	cmd.Stdin = bytes.NewReader(m.script("install.sh"))
	cmd.Env = []string{"PATH=" + path, "HOME=" + m.home, "TMPDIR=" + m.temp}
	for name, value := range m.env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	return m.run(cmd)
}

func TestInstallShReplacesARunningCopyWithTheLatestRelease(t *testing.T) {
	skipUnlessUnix(t)
	m := newMachine(t)
	binary := m.path(".local", "bin", "string")
	running := m.start(binary)
	before, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}

	// --no-init wherever it is, and init's own options then unused.
	if ok, output := m.installSh("--agent", "claude-code", "--no-init"); !ok {
		t.Fatalf("install.sh failed:\n%s", output)
	}
	if got := versionAt(binary); got != "string 0.3.1" {
		t.Errorf("%s reports %q", binary, got)
	}
	// A new file at the path, not new bytes in the one the running copy runs.
	if after, err := os.Stat(binary); err != nil || os.SameFile(before, after) || !running() {
		t.Errorf("the running copy was written over: %v, still running: %t", err, running())
	}
	if got := names(filepath.Dir(binary)); !slices.Equal(got, []string{"string"}) {
		t.Errorf("the directory holds %v", got)
	}
	if got := m.leftovers(); len(got) > 0 {
		t.Errorf("install.sh left %v in the temporary directory", got)
	}
	if got, want := m.host.requested(), []string{"/stable", "/v0.3.1/" + archiveName(), "/v0.3.1/checksums.txt"}; !slices.Equal(got, want) {
		t.Errorf("requests %v, want %v", got, want)
	}
	if exists(m.path(".agents")) || exists(m.path(".claude")) {
		t.Errorf("init ran: %v", names(m.home))
	}
	m.checkRegistered(binary, false)
}

func TestInstallShInstallsAndPinsTheVersionItIsGiven(t *testing.T) {
	skipUnlessUnix(t)
	m := newMachine(t)
	m.env["STRING_VERSION"] = "v0.3.0"
	m.env["STRING_INSTALL_DIR"] = m.path("tools")

	if ok, output := m.installSh("--agent", "claude-code"); !ok {
		t.Fatalf("install.sh failed:\n%s", output)
	}
	binary := m.path("tools", "string")
	if got := versionAt(binary); got != "string 0.3.0" {
		t.Errorf("%s reports %q", binary, got)
	}
	if got := m.host.requested(); slices.Contains(got, "/stable") {
		t.Errorf("install.sh asked which release is the latest: %v", got)
	}
	// init ran with the option it was given: Claude Code's skill, and not the
	// shared one that a plain init installs.
	if !exists(m.path(".claude", "skills", "string-cli", "SKILL.md")) || exists(m.path(".agents")) {
		t.Errorf("init did not run with --agent claude-code: %v", names(m.home))
	}
	m.checkRegistered(binary, true)
}

// failures are the ways an install cannot go ahead, each with the reason both
// installers give and what they ask the host for before they stop. None of
// them asks GitHub instead.
func failures() []struct {
	name, reason string
	setUp        func(m *machine)
	asked        []string
} {
	latest := []string{"/stable", "/v0.3.1/" + archiveName(), "/v0.3.1/checksums.txt"}
	return []struct {
		name, reason string
		setUp        func(m *machine)
		asked        []string
	}{
		// One that unpacks to the right release, so that only the checksum
		// can stop it.
		{"an archive that does not match checksums.txt", "does not match its checksums.txt", func(m *machine) {
			archive := releaseArchive(m.t, map[string][]byte{exe("string"): release(m.t, "0.3.1"), "README.md": []byte("# changed\n")})
			m.host.put("/v0.3.1/"+archiveName(), archive)
		}, latest},
		{"an archive that holds another release", "holds string 0.3.0", func(m *machine) {
			m.host.publishArchive("0.3.1", holding(m.t, release(m.t, "0.3.0")))
		}, latest},
		{"a /stable that names no release", "does not name a release", func(m *machine) { m.host.put("/stable", []byte("<html>\n")) }, []string{"/stable"}},
		{"a STRING_VERSION that names no release", "names no release", func(m *machine) { m.env["STRING_VERSION"] = "0.3" }, nil},
		// GitHub's stand-in has 0.3.0 and 0.3.1 only, so a fallback that
		// should not happen shows in what it is asked.
		{"a release the host set by STRING_DOWNLOAD_URL lacks", "could not download", func(m *machine) { m.env["STRING_VERSION"] = "0.2.0" }, []string{"/v0.2.0/" + archiveName()}},
	}
}

var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;]*m")

// mentions reports whether output says reason, wherever PowerShell wrapped
// the line, and whatever gutter and colours it wrapped it with.
func mentions(output, reason string) bool {
	squeeze := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) || r == '|' {
				return -1
			}
			return r
		}, ansiEscape.ReplaceAllString(s, ""))
	}
	return strings.Contains(squeeze(output), squeeze(reason))
}

// checkUnchanged fails unless the copy at binary is still release 0.3.0, alone
// in its directory, and the installer left nothing in the temporary directory.
func (m *machine) checkUnchanged(binary string) {
	m.t.Helper()
	if data, _ := os.ReadFile(binary); !bytes.Equal(data, release(m.t, "0.3.0")) {
		m.t.Error("the copy there before was changed")
	}
	if got := names(filepath.Dir(binary)); !slices.Equal(got, []string{filepath.Base(binary)}) {
		m.t.Errorf("the directory holds %v", got)
	}
	if got := m.leftovers(); len(got) > 0 {
		m.t.Errorf("the installer left %v in the temporary directory", got)
	}
}

// choice is a release an installer picks: the version it installs, whether
// it pins the copy to it, and what it asks the host and GitHub for.
type choice struct {
	name          string
	setUp         func(m *machine)
	want          string
	pinned        bool
	asked, github []string
}

// choices are made without STRING_DOWNLOAD_URL, so that the installers use
// their default host and GitHub, except where a case sets it again.
func choices() []choice {
	files := func(dir string) []string { return []string{dir + "/" + archiveName(), dir + "/checksums.txt"} }
	defaults := func(m *machine) { delete(m.env, "STRING_DOWNLOAD_URL") }
	return []choice{
		{"the release /stable names", defaults, "0.3.1", false, append([]string{"/stable"}, files("/v0.3.1")...), nil},
		{"from GitHub, a release the host lacks", func(m *machine) {
			defaults(m)
			m.host.drop("/v0.3.1/" + archiveName())
		}, "0.3.1", false, []string{"/stable", "/v0.3.1/" + archiveName()}, files("/download/v0.3.1")},
		// While a release is rehearsed.
		{"GitHub's latest over a pre-release /stable names", func(m *machine) {
			defaults(m)
			m.host.publish(m.t, "0.3.2-rc.1")
			m.host.point("0.3.2-rc.1")
		}, "0.3.1", false, []string{"/stable"}, files("/latest/download")},
		{"a pre-release STRING_VERSION names", func(m *machine) {
			defaults(m)
			m.host.publish(m.t, "0.3.2-rc.1")
			m.env["STRING_VERSION"] = "0.3.2-rc.1"
		}, "0.3.2-rc.1", true, files("/v0.3.2-rc.1"), nil},
		// How a rehearsal installs its release candidate through /stable.
		{"a pre-release /stable names on the host STRING_DOWNLOAD_URL sets", func(m *machine) {
			m.host.publish(m.t, "0.3.2-rc.1")
			m.host.point("0.3.2-rc.1")
		}, "0.3.2-rc.1", false, append([]string{"/stable"}, files("/v0.3.2-rc.1")...), nil},
	}
}

// checkChoice fails unless binary is release c.want, registered as pinned or
// not, and the installer asked each host for exactly what c lists.
func (m *machine) checkChoice(binary string, c choice) {
	m.t.Helper()
	if got := versionAt(binary); got != "string "+c.want {
		m.t.Errorf("%s reports %q, want string %s", binary, got, c.want)
	}
	if got := m.host.requested(); !slices.Equal(got, c.asked) {
		m.t.Errorf("requests to the host %v, want %v", got, c.asked)
	}
	if got := m.github.requested(); !slices.Equal(got, c.github) {
		m.t.Errorf("requests to GitHub %v, want %v", got, c.github)
	}
	m.checkRegistered(binary, c.pinned)
}

func TestInstallShChoosesTheReleaseAndWhereToGetIt(t *testing.T) {
	skipUnlessUnix(t)
	for _, check := range choices() {
		t.Run(check.name, func(t *testing.T) {
			m := newMachine(t)
			check.setUp(m)
			if ok, output := m.installSh("--no-init"); !ok {
				t.Fatalf("install.sh failed:\n%s", output)
			}
			m.checkChoice(m.path(".local", "bin", "string"), check)
		})
	}
}

func TestInstallShChangesNothingWhenItCannotInstall(t *testing.T) {
	skipUnlessUnix(t)
	for _, check := range failures() {
		t.Run(check.name, func(t *testing.T) {
			m := newMachine(t)
			binary := m.path(".local", "bin", "string")
			m.place(binary, release(t, "0.3.0"))
			check.setUp(m)
			if ok, output := m.installSh("--no-init"); ok || !mentions(output, check.reason) {
				t.Errorf("install.sh succeeded (%t) or did not say %q:\n%s", ok, check.reason, output)
			}
			if got := m.host.requested(); !slices.Equal(got, check.asked) {
				t.Errorf("requests %v, want %v", got, check.asked)
			}
			if got := m.github.requested(); len(got) > 0 {
				t.Errorf("requests to GitHub %v", got)
			}
			m.checkUnchanged(binary)
		})
	}
}

func TestInstallShFollowsNoRedirectFromHttpsToHttp(t *testing.T) {
	skipUnlessUnix(t)
	m := newMachine(t)
	binary := m.path(".local", "bin", "string")
	m.place(binary, release(t, "0.3.0"))
	// A host on https that sends each request on to the same path on the plain
	// host, which has the release.
	var mu sync.Mutex
	var asked []string
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()
		http.Redirect(w, r, m.host.url+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(secure.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: secure.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	m.env["STRING_DOWNLOAD_URL"] = secure.URL
	m.env["CURL_CA_BUNDLE"] = ca

	if ok, output := m.installSh("--no-init"); ok {
		t.Errorf("install.sh succeeded:\n%s", output)
	}
	// It reached the https host, so the redirect is what stopped it.
	mu.Lock()
	overHTTPS := slices.Clone(asked)
	mu.Unlock()
	if overHTTP := m.host.requested(); !slices.Equal(overHTTPS, []string{"/stable"}) || len(overHTTP) > 0 {
		t.Errorf("requests over https %v, and over http %v", overHTTPS, overHTTP)
	}
	m.checkUnchanged(binary)
}

// fakeTools is a directory of scripts standing in for the system's commands,
// each printing its line of output, for a machine the test does not run on.
func fakeTools(t *testing.T, scripts map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestInstallShAsksForTheBuildThatRunsNativelyHere(t *testing.T) {
	skipUnlessUnix(t)
	for _, check := range []struct {
		name, uname, sysctl, want string
	}{
		{"an Intel Mac", `case "$1" in -s) echo Darwin ;; -m) echo x86_64 ;; esac`, "echo 0", "string_darwin_amd64.tar.gz"},
		{"a shell under Rosetta on an Arm Mac", `case "$1" in -s) echo Darwin ;; -m) echo x86_64 ;; esac`, "echo 1", "string_darwin_arm64.tar.gz"},
		{"Linux on Arm", `case "$1" in -s) echo Linux ;; -m) echo aarch64 ;; esac`, "exit 1", "string_linux_arm64.tar.gz"},
	} {
		t.Run(check.name, func(t *testing.T) {
			m := newMachine(t)
			tools := fakeTools(t, map[string]string{"uname": check.uname, "sysctl": check.sysctl})
			m.installShWith(tools+":/usr/bin:/bin:/usr/sbin:/sbin", "--no-init")
			if got := m.host.requested(); !slices.Contains(got, "/v0.3.1/"+check.want) {
				t.Errorf("requests %v, want one for %s", got, check.want)
			}
		})
	}
}

// powershells are the shells install.ps1 runs in: Windows PowerShell and
// PowerShell 7 built for this machine's CPU, and Windows PowerShell built for
// x86, which finds x86 in PROCESSOR_ARCHITECTURE on any machine.
type powershells struct{ windows, core, x86 string }

func findPowershells(t *testing.T) powershells {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("install.ps1 is for Windows")
	}
	if os.Getenv("CI") == "" {
		t.Skip("install.ps1 changes the user's PATH, so these tests run in CI only")
	}
	shells := powershells{x86: filepath.Join(os.Getenv("SystemRoot"), "SysWOW64", "WindowsPowerShell", "v1.0", "powershell.exe")}
	var err error
	if shells.windows, err = exec.LookPath("powershell.exe"); err != nil {
		t.Fatal(err)
	}
	if shells.core, err = exec.LookPath("pwsh.exe"); err != nil {
		t.Fatal(err)
	}
	return shells
}

// userPath is the user's PATH as the registry keeps it: the kind of value,
// such as ExpandString, or None, and the value with its variables unexpanded.
func userPath(t *testing.T) (kind, value string) {
	t.Helper()
	output, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		`$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment'); `+
			`$value = $key.GetValue('Path', $null, 'DoNotExpandEnvironmentNames'); `+
			`if ($null -eq $value) { 'None' } else { $key.GetValueKind('Path').ToString(); $value }`).Output()
	if err != nil {
		t.Fatal(err)
	}
	kind, value, _ = strings.Cut(strings.TrimRight(string(output), "\r\n"), "\r\n")
	return kind, value
}

// keepUserPath puts the user's PATH back as it is now once the test ends.
func keepUserPath(t *testing.T) (kind, value string) {
	t.Helper()
	kind, value = userPath(t)
	t.Cleanup(func() {
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
			`$key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment'); `+
				`if ($env:TEST_KIND -eq 'None') { $key.DeleteValue('Path', $false) } else { $key.SetValue('Path', $env:TEST_VALUE, $env:TEST_KIND) }`)
		cmd.Env = append(os.Environ(), "TEST_KIND="+kind, "TEST_VALUE="+value)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("putting PATH back: %v\n%s", err, output)
		}
	})
	return kind, value
}

// powershell runs shell with args in this machine's home and temporary
// directory, and with nothing else of string's in the environment.
func (m *machine) powershell(shell string, args ...string) (bool, string) {
	m.t.Helper()
	cmd := exec.Command(shell, append([]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass"}, args...)...)
	drop := []string{"HOME", "USERPROFILE", "TEMP", "TMP", "XDG_CONFIG_HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR"}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(name), "STRING_") || slices.ContainsFunc(drop, func(d string) bool { return strings.EqualFold(d, name) }) {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "HOME="+m.home, "USERPROFILE="+m.home, "TEMP="+m.temp, "TMP="+m.temp)
	for name, value := range m.env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	return m.run(cmd)
}

func TestInstallPs1InstallsTheLatestReleaseThroughIrmIex(t *testing.T) {
	shells := findPowershells(t)
	for _, check := range []struct{ name, shell string }{
		{"Windows PowerShell", shells.windows},
		{"PowerShell 7", shells.core},
		// The build for this machine's CPU all the same.
		{"Windows PowerShell as x86", shells.x86},
	} {
		t.Run(check.name, func(t *testing.T) {
			kind, before := keepUserPath(t)
			m := newMachine(t)
			m.host.put("/install.ps1", m.script("install.ps1"))
			m.env["STRING_NO_INIT"] = "1"
			ok, output := m.powershell(check.shell, "-Command", "irm '"+m.host.url+"/install.ps1' | iex; (Get-Command string -CommandType Application).Source")
			if !ok {
				t.Fatalf("install.ps1 failed:\n%s", output)
			}
			dir := m.path(".local", "bin")
			binary := filepath.Join(dir, "string.exe")
			lines := strings.Split(strings.TrimSpace(output), "\n")
			if got := strings.TrimSpace(lines[len(lines)-1]); got != binary {
				t.Errorf("the session runs string from %q, want %s\n%s", got, binary, output)
			}
			if got := versionAt(binary); got != "string 0.3.1" {
				t.Errorf("%s reports %q", binary, got)
			}
			if got, want := m.host.requested(), []string{"/install.ps1", "/stable", "/v0.3.1/" + archiveName(), "/v0.3.1/checksums.txt"}; !slices.Equal(got, want) {
				t.Errorf("requests %v, want %v", got, want)
			}
			// In front of what was there, which stays as it was, and as the
			// same kind of value, so that its variables still expand.
			want := []string{dir}
			for _, entry := range strings.Split(before, ";") {
				if entry != "" {
					want = append(want, entry)
				}
			}
			wantKind := kind
			if kind == "None" {
				wantKind = "ExpandString"
			}
			if gotKind, got := userPath(t); gotKind != wantKind || got != strings.Join(want, ";") {
				t.Errorf("the user's PATH is %s %q, want %s %q", gotKind, got, wantKind, strings.Join(want, ";"))
			}
			if got := names(dir); !slices.Equal(got, []string{"string.exe"}) {
				t.Errorf("the directory holds %v", got)
			}
			if got := m.leftovers(); len(got) > 0 {
				t.Errorf("install.ps1 left %v in the temporary directory", got)
			}
			if exists(m.path(".agents")) {
				t.Errorf("init ran: %v", names(m.home))
			}
			m.checkRegistered(binary, false)
		})
	}
}

func TestInstallPs1ReplacesARunningCopyWithTheVersionItIsGiven(t *testing.T) {
	shells := findPowershells(t)
	for _, check := range []struct{ name, shell string }{
		{"Windows PowerShell", shells.windows},
		{"PowerShell 7", shells.core},
	} {
		t.Run(check.name, func(t *testing.T) {
			keepUserPath(t)
			m := newMachine(t)
			dir := m.path("tools")
			binary := filepath.Join(dir, "string.exe")
			running := m.start(binary)
			ok, output := m.powershell(check.shell, "-File", m.scriptFile("install.ps1"), "-Version", "v0.3.0", "-InstallDir", dir, "-Agent", "claude-code")
			if !ok {
				t.Fatalf("install.ps1 failed:\n%s", output)
			}
			if got := versionAt(binary); got != "string 0.3.0" || !running() {
				t.Errorf("%s reports %q, and the copy that was running there is still running: %t", binary, got, running())
			}
			if got := m.host.requested(); slices.Contains(got, "/stable") {
				t.Errorf("install.ps1 asked which release is the latest: %v", got)
			}
			if !exists(m.path(".claude", "skills", "string-cli", "SKILL.md")) || exists(m.path(".agents")) {
				t.Errorf("init did not run with --agent claude-code: %v", names(m.home))
			}
			m.checkRegistered(binary, true)
		})
	}
}

// Windows PowerShell, when the caller redirects the error stream, makes each
// line a native command writes to stderr an error. PowerShell 7 does not.
func TestInstallPs1SucceedsWhenInitWritesToARedirectedStderr(t *testing.T) {
	shells := findPowershells(t)
	keepUserPath(t)
	m := newMachine(t)
	m.host.publishArchive("0.3.1", holding(t, thisBinary(t)))
	m.env[noisyEnv] = m.home
	ok, output := m.powershell(shells.windows, "-Command", "try { & '"+m.scriptFile("install.ps1")+"' *>&1 | Out-String } catch { $_; exit 1 }; exit 0")
	if !ok {
		t.Fatalf("install.ps1 failed:\n%s", output)
	}
	if !exists(m.path("initialized")) {
		t.Errorf("init did not run:\n%s", output)
	}
}

func TestInstallPs1ChangesNothingWhenItCannotInstall(t *testing.T) {
	shells := findPowershells(t)
	for _, shell := range []struct{ name, path string }{
		{"Windows PowerShell", shells.windows},
		{"PowerShell 7", shells.core},
	} {
		for _, check := range failures() {
			t.Run(shell.name+"/"+check.name, func(t *testing.T) {
				m := newMachine(t)
				binary := m.path(".local", "bin", "string.exe")
				m.place(binary, release(t, "0.3.0"))
				m.env["STRING_NO_INIT"] = "1"
				check.setUp(m)
				if ok, output := m.powershell(shell.path, "-File", m.scriptFile("install.ps1")); ok || !mentions(output, check.reason) {
					t.Errorf("install.ps1 succeeded (%t) or did not say %q:\n%s", ok, check.reason, output)
				}
				if got := m.host.requested(); !slices.Equal(got, check.asked) {
					t.Errorf("requests %v, want %v", got, check.asked)
				}
				if got := m.github.requested(); len(got) > 0 {
					t.Errorf("requests to GitHub %v", got)
				}
				m.checkUnchanged(binary)
			})
		}
	}
}

// One PowerShell is enough here: the choice is the script's own logic.
func TestInstallPs1ChoosesTheReleaseAndWhereToGetIt(t *testing.T) {
	shells := findPowershells(t)
	for _, check := range choices() {
		t.Run(check.name, func(t *testing.T) {
			keepUserPath(t)
			m := newMachine(t)
			m.env["STRING_NO_INIT"] = "1"
			check.setUp(m)
			if ok, output := m.powershell(shells.windows, "-File", m.scriptFile("install.ps1")); !ok {
				t.Fatalf("install.ps1 failed:\n%s", output)
			}
			m.checkChoice(m.path(".local", "bin", "string.exe"), check)
		})
	}
}

// install.ps1 takes the machine's CPU from the system's environment in the
// registry, which Windows does not redirect for a 32-bit process, so x86
// PowerShell reads the machine's own CPU there, also on Arm.
func TestWindowsPowerShellAsX86ReadsTheMachinesCPUFromTheRegistry(t *testing.T) {
	shells := findPowershells(t)
	output, err := exec.Command(shells.x86, "-NoProfile", "-NonInteractive", "-Command",
		`[Environment]::Is64BitProcess; `+
			`[Microsoft.Win32.Registry]::LocalMachine.OpenSubKey('SYSTEM\CurrentControlSet\Control\Session Manager\Environment').GetValue('PROCESSOR_ARCHITECTURE')`).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Fields(string(output)), []string{"False", strings.ToUpper(runtime.GOARCH)}; !slices.Equal(got, want) {
		t.Errorf("Windows PowerShell as x86 prints %v, want %v", got, want)
	}
}

// Windows PowerShell reads a script file that has no byte-order mark in the
// machine's ANSI code page, so the installers keep to ASCII.
func TestInstallersAreASCII(t *testing.T) {
	for _, name := range []string{"install.sh", "install.ps1"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, b := range data {
			if b > 0x7f {
				t.Errorf("%s has byte %#x on line %d", name, b, bytes.Count(data[:i], []byte("\n"))+1)
				break
			}
		}
	}
}
