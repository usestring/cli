package main

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"time"

	"github.com/usestring/cli/internal/release"
)

// downloadURL is the host that string update and the install scripts take
// releases from. STRING_DOWNLOAD_URL replaces it, for tests and rehearsals.
const downloadURL = "https://cli.usestring.ai"

// releasesPage is where a person can download a release by hand.
const releasesPage = "https://github.com/usestring/cli/releases/latest"

// installKind is how this copy of string was installed, which decides what
// string update and the daily check do with it.
type installKind string

const (
	installScript   installKind = "script"
	installHomebrew installKind = "homebrew"
	installNPM      installKind = "npm"
	installNPX      installKind = "npx"
	installScoop    installKind = "scoop"
	installOther    installKind = "other"
)

// installedBy says who installed each kind of copy, for string update --check.
var installedBy = map[installKind]string{
	installScript:   "by the install script",
	installHomebrew: "by Homebrew",
	installNPM:      "by npm",
	installNPX:      "by npx",
	installScoop:    "by Scoop",
	installOther:    "by hand or by a program string does not know",
}

// manager is a program that installs string and updates the copies it
// installed, which string update therefore leaves to it.
type manager struct {
	name    string
	command string
}

var managers = map[installKind]manager{
	installHomebrew: {"Homebrew", "brew upgrade usestring/cli/string"},
	installNPM:      {"npm", "npm install -g @usestring/cli@latest"},
	installNPX:      {"npx", "npx -y @usestring/cli@latest"},
	installScoop:    {"Scoop", "scoop update string"},
}

// classifyInstall names how the copy at path was installed. path has its
// symlinks resolved, and registered is the path install.json names, which
// only the install scripts write. Each path element is matched whole and in
// any case, whichever separator the system uses.
func classifyInstall(path, registered, goos string) installKind {
	same := path == registered
	if goos == "windows" {
		same = strings.EqualFold(path, registered)
	}
	if registered != "" && same {
		return installScript
	}
	elements := strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
	has := func(names ...string) bool {
		for i := 0; i+len(names) <= len(elements); i++ {
			if slices.EqualFunc(elements[i:i+len(names)], names, strings.EqualFold) {
				return true
			}
		}
		return false
	}
	// npm and npx first: an npm that Homebrew or Scoop installed keeps its
	// global packages under their directories.
	switch {
	case has("_npx"):
		return installNPX
	case has("node_modules"):
		return installNPM
	case has("Cellar") || has("homebrew") || has("linuxbrew"):
		return installHomebrew
	case has("scoop", "apps"):
		return installScoop
	}
	return installOther
}

// takesLatest reports whether this copy moves to latest, the release /stable
// names: when latest is newer and, unless this copy is a pre-release itself,
// not a pre-release. The release workflow keeps pre-releases off /stable once
// a stable release is out; this is the copy's own guard should one get there.
// string update --version installs a pre-release by name.
func takesLatest(latest release.Version) bool {
	current, err := release.ParseVersion(version)
	if err != nil {
		// A development build moves to any stable release.
		return !latest.Prerelease()
	}
	if latest.Prerelease() && !current.Prerelease() {
		return false
	}
	return latest.Compare(current) > 0
}

// upToDate is what string update says when this copy stays as it is because
// it does not take latest.
func upToDate(latest release.Version) string {
	current, err := release.ParseVersion(version)
	switch {
	case latest.Prerelease() && (err != nil || !current.Prerelease()):
		return fmt.Sprintf("string %s is up to date. The latest release, %s, is a pre-release, which only a pre-release build updates to.\n", version, latest)
	case err == nil && latest.Compare(current) < 0:
		return fmt.Sprintf("string %s is newer than the latest release, %s.\n", version, latest)
	}
	return fmt.Sprintf("string %s is the latest version.\n", version)
}

// installReceipt is install.json. Only the install scripts create it, through
// string update --register, so a copy it names is one string update may
// replace and the daily check may update.
type installReceipt struct {
	Path string `json:"path"`
	// Pinned is set when the person asked for one version, which the daily
	// check then leaves in place.
	Pinned bool `json:"pinned,omitempty"`
}

// installedCopy is the running binary: where it is and how it got there.
type installedCopy struct {
	path    string
	kind    installKind
	receipt installReceipt
}

// stringFile is the path of one of the files string keeps in its config
// directory. ok is false when there is no home directory to put it under.
func stringFile(env func(string) (string, bool), name string) (string, bool) {
	home, err := homeDir(env)
	if err != nil {
		return "", false
	}
	return filepath.Join(underConfig("string")(home, env), name), true
}

// resolvedBinary is the running binary's path with its symlinks resolved, so
// a link on PATH leads to the file an update replaces.
func resolvedBinary(rt runtime) (string, error) {
	path, err := rt.binary()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	}
	return path, nil
}

func findCopy(rt runtime) (installedCopy, error) {
	path, err := resolvedBinary(rt)
	if err != nil {
		return installedCopy{}, err
	}
	var receipt installReceipt
	if file, ok := stringFile(rt.env, "install.json"); ok {
		if data, err := os.ReadFile(file); err == nil {
			_ = json.Unmarshal(data, &receipt)
		}
	}
	return installedCopy{path: path, kind: classifyInstall(path, receipt.Path, goruntime.GOOS), receipt: receipt}, nil
}

// switchedOff says why nothing happens in the background on this machine, or
// "". STRING_AUTO_UPDATE is the one switch, and CI turns it off too.
func switchedOff(env func(string) (string, bool)) string {
	if value, _ := env("STRING_AUTO_UPDATE"); slices.Contains([]string{"0", "false", "off", "no"}, strings.ToLower(strings.TrimSpace(value))) {
		return "STRING_AUTO_UPDATE is " + value
	}
	if value, _ := env("CI"); value != "" {
		return "CI is set"
	}
	return ""
}

// updatesOff says why the daily check leaves this copy alone, or "" when it
// runs: to install a new release over a copy that string replaces itself, and
// to say that one is out for a copy that a package manager updates.
func updatesOff(env func(string) (string, bool), copy installedCopy, keys []ed25519.PublicKey, keysErr error) string {
	if _, err := release.ParseVersion(version); err != nil {
		return "this is a development build"
	}
	if reason := switchedOff(env); reason != "" {
		return reason
	}
	if _, managed := managers[copy.kind]; managed {
		if copy.kind == installNPX {
			return "npx fetches a new release itself"
		}
		return ""
	}
	switch {
	case copy.kind == installScript && copy.receipt.Pinned:
		return "this copy is pinned to " + version + ", until a plain string update"
	case keysErr != nil:
		return keysErr.Error()
	case len(keys) == 0:
		return "this build has no release signing key"
	}
	return ""
}

const updateUsage = "string update [--version X.Y.Z | --check]"

func updateCommand(list []string, rt runtime) error {
	parsed, err := parse(list, spec{options: []string{"version"}, switches: []string{"check", "register", "pin", "background"}})
	if err != nil {
		return usage(err)
	}
	if parsed.help {
		return write(rt.stdout, []byte(help))
	}
	if err := expectPositionals(parsed.positionals, 0, updateUsage); err != nil {
		return usage(err)
	}
	if _, ok := parsed.value("input"); ok {
		return usage(errors.New("--input does not apply to update"))
	}
	wanted, pinning := parsed.value("version")
	check, register, pin, background := parsed.switches["check"], parsed.switches["register"], parsed.switches["pin"], parsed.switches["background"]
	modes := 0
	for _, on := range []bool{pinning, check, register, background} {
		if on {
			modes++
		}
	}
	if modes > 1 || pin && !register {
		return usage(errors.New("Usage: " + updateUsage))
	}
	switch {
	case background:
		backgroundUpdate(rt)
		return nil
	case register:
		return registerCopy(rt, pin)
	case check:
		return updateCheck(rt)
	}
	var target *release.Version
	if pinning {
		v, err := release.ParseVersion(strings.TrimPrefix(wanted, "v"))
		if err != nil {
			return usage(fmt.Errorf("--version takes a release, such as 0.3.0: %w", err))
		}
		target = &v
	}
	return updateNow(rt, target)
}

// registerCopy is string update --register, which the install scripts run
// once the binary is in place, so that the binary itself, not the script,
// writes down where it is.
func registerCopy(rt runtime, pin bool) error {
	file, ok := stringFile(rt.env, "install.json")
	if !ok {
		return errors.New("Cannot find your home directory; set HOME")
	}
	path, err := resolvedBinary(rt)
	if err != nil {
		return err
	}
	return writePrivate(file, installReceipt{Path: path, Pinned: pin})
}

// updateResult is what string update --json prints.
type updateResult struct {
	Updated bool   `json:"updated"`
	From    string `json:"from"`
	To      string `json:"to"`
	Path    string `json:"path"`
	Pinned  bool   `json:"pinned"`
}

// updateNow is string update: it puts the latest release, or exactly target
// when --version names one, in place of this copy.
func updateNow(rt runtime, target *release.Version) error {
	copy, err := findCopy(rt)
	if err != nil {
		return err
	}
	base, err := downloadBase(rt.env)
	if err != nil {
		return usage(err)
	}
	if m, managed := managers[copy.kind]; managed {
		return updateElsewhere(updater{rt: rt, base: base}, copy, m, target)
	}
	keys, err := rt.releaseKeys()
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return &failure{exit: exitFailure, code: "no_signing_key", message: "This build of string has no release signing key to check an update with, so it does not update itself.", nextStep: "download the latest release from " + releasesPage}
	}
	u := updater{rt: rt, base: base, keys: keys}
	pinning := target != nil
	if !pinning {
		latest, err := u.latest()
		if err != nil {
			return err
		}
		target = &latest
	}
	// A plain update moves only to a release this copy takes. --version
	// installs what it names, older or a pre-release, unless this copy is it.
	current, currentErr := release.ParseVersion(version)
	alreadyThere := !takesLatest(*target)
	if pinning {
		alreadyThere = currentErr == nil && target.Compare(current) == 0
	}
	if !alreadyThere {
		if err := installOver(u, copy.path, *target); err != nil {
			return err
		}
	}
	pinned := pinning && copy.kind == installScript
	unpinned := copy.kind == installScript && copy.receipt.Pinned && !pinned
	if copy.kind == installScript && copy.receipt.Pinned != pinned {
		file, _ := stringFile(rt.env, "install.json")
		if err := writePrivate(file, installReceipt{Path: copy.receipt.Path, Pinned: pinned}); err != nil {
			return fmt.Errorf("string %s is in %s, but its pin could not be changed: %w", target, copy.path, err)
		}
	}
	if rt.json {
		to := version
		if !alreadyThere {
			to = target.String()
		}
		shown, _ := json.Marshal(updateResult{Updated: !alreadyThere, From: version, To: to, Path: copy.path, Pinned: pinned})
		return write(rt.stdout, append(shown, '\n'))
	}
	var report strings.Builder
	switch {
	case !alreadyThere && pinned:
		fmt.Fprintf(&report, "Installed string %s in %s. It stays at %[1]s until you run string update.\n", target, copy.path)
	case !alreadyThere && pinning:
		fmt.Fprintf(&report, "Installed string %s in %s.\n", target, copy.path)
	case !alreadyThere:
		fmt.Fprintf(&report, "Updated string from %s to %s in %s.\n", version, target, copy.path)
	case pinned:
		fmt.Fprintf(&report, "string is already %s. It stays at %[1]s until you run string update.\n", target)
	case pinning:
		fmt.Fprintf(&report, "string is already %s.\n", target)
	default:
		report.WriteString(upToDate(*target))
	}
	if unpinned {
		report.WriteString("It is no longer pinned, so it updates itself again.\n")
	}
	return write(rt.stdout, []byte(report.String()))
}

// installOver installs release v over the binary at path, holding the update
// lock so that no other string update or daily check does the same at once.
func installOver(u updater, path string, v release.Version) error {
	lockPath, ok := stringFile(u.rt.env, "update.lock")
	if !ok {
		return errors.New("Cannot find your home directory; set HOME")
	}
	lock, err := takeUpdateLock(lockPath)
	if errors.Is(err, errUpdateRunning) {
		return &failure{exit: exitFailure, code: "update_running", message: "Another string update is running. Try again once it has finished."}
	}
	if err != nil {
		return err
	}
	defer lock.release()
	removeLeftovers(path)
	err = u.install(v, path)
	var unwritable *unwritableDirectory
	if errors.As(err, &unwritable) {
		return &failure{exit: exitFailure, code: "update_not_writable", message: unwritable.Error(), nextStep: "update it with the program that installed it, or download the latest release from " + releasesPage}
	}
	return err
}

// updateElsewhere is string update for a copy that a package manager
// installed. The manager keeps track of what it installed, so string says how
// to update rather than replacing the binary under it.
func updateElsewhere(u updater, copy installedCopy, m manager, target *release.Version) error {
	refusal := &failure{exit: exitFailure, code: "managed_install", message: fmt.Sprintf("%s installed this copy of string, so %[1]s updates it.", m.name), nextStep: "run `" + m.command + "`"}
	if target != nil {
		refusal.message = fmt.Sprintf("%s installed this copy of string, so string update does not put %s in its place.", m.name, target)
		return refusal
	}
	latest, err := u.latest()
	if err != nil {
		return refusal
	}
	if !takesLatest(latest) {
		if u.rt.json {
			shown, _ := json.Marshal(updateResult{From: version, To: version, Path: copy.path})
			return write(u.rt.stdout, append(shown, '\n'))
		}
		return write(u.rt.stdout, []byte(upToDate(latest)))
	}
	refusal.message = fmt.Sprintf("string %s is out. ", latest) + refusal.message
	return refusal
}

// updateReport is what string update --check shows.
type updateReport struct {
	Version     string      `json:"version"`
	Latest      *string     `json:"latest"`
	LatestError string      `json:"latest_error,omitempty"`
	Install     installKind `json:"install"`
	Path        string      `json:"path"`
	// Updates is automatic when the daily check installs new releases, notify
	// when it only says that one is out, and off when it does not run.
	Updates   string     `json:"updates"`
	Reason    string     `json:"reason,omitempty"`
	Command   string     `json:"command"`
	CheckedAt *time.Time `json:"checked_at"`
	Error     string     `json:"error,omitempty"`
}

// updateCheck is string update --check. It asks the download host for the
// latest release and changes nothing.
func updateCheck(rt runtime) error {
	copy, err := findCopy(rt)
	if err != nil {
		return err
	}
	base, err := downloadBase(rt.env)
	if err != nil {
		return usage(err)
	}
	keys, keysErr := rt.releaseKeys()
	var state updateState
	if file, ok := stringFile(rt.env, "update.json"); ok {
		state = readUpdateState(file)
	}
	report := updateReport{Version: version, Install: copy.kind, Path: copy.path, Command: "string update", Error: state.Error}
	if latest, err := (updater{rt: rt, base: base}).latest(); err == nil {
		text := latest.String()
		report.Latest = &text
	} else {
		report.LatestError = err.Error()
	}
	if m, managed := managers[copy.kind]; managed {
		report.Command = m.command
	}
	if !state.CheckedAt.IsZero() {
		report.CheckedAt = &state.CheckedAt
	}
	switch off := updatesOff(rt.env, copy, keys, keysErr); {
	case off != "":
		report.Updates, report.Reason = "off", off
	case copy.kind == installScript:
		report.Updates = "automatic"
	default:
		report.Updates = "notify"
	}
	if rt.json {
		shown, err := json.Marshal(report)
		if err != nil {
			return err
		}
		return write(rt.stdout, append(shown, '\n'))
	}
	var out strings.Builder
	line := func(label, format string, values ...any) {
		fmt.Fprintf(&out, "%-9s %s\n", label, fmt.Sprintf(format, values...))
	}
	fmt.Fprintf(&out, "string %s\n", report.Version)
	if report.Latest != nil {
		line("Latest", "%s", *report.Latest)
	} else {
		line("Latest", "unknown: %s", report.LatestError)
	}
	line("Install", "%s, at %s", installedBy[report.Install], report.Path)
	switch report.Updates {
	case "automatic":
		line("Updates", "automatic: string checks once a day and installs a new release in the background")
	case "notify":
		line("Updates", "string checks once a day and says when a release is out. Update with: %s", report.Command)
	default:
		line("Updates", "off, because %s", report.Reason)
	}
	if report.CheckedAt != nil {
		line("Checked", "%s", clockTime(*report.CheckedAt, rt.clock()))
	}
	if report.Error != "" {
		line("Error", "%s", report.Error)
	}
	return write(rt.stdout, []byte(out.String()))
}
