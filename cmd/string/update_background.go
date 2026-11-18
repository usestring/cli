package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/usestring/cli/internal/release"
)

// checkEvery is how often the background looks for a new release.
const checkEvery = 24 * time.Hour

// aDayPast reports whether a day has passed since then. A then in the future
// counts as long past, so that a clock that was wrong once does not hold off
// the checks and notices until that date.
func aDayPast(then, now time.Time) bool {
	return now.Sub(then) >= checkEvery || then.After(now)
}

// versionTimeout is how long a new binary has to print its version.
const versionTimeout = 10 * time.Second

func (rt runtime) releaseKeys() ([]ed25519.PublicKey, error) {
	if rt.keys == nil {
		return release.TrustedKeys()
	}
	return rt.keys()
}

// printedVersion is what the binary at path prints for --version.
func (rt runtime) printedVersion(path string) ([]byte, error) {
	if rt.versionOf != nil {
		return rt.versionOf(path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()
	return exec.CommandContext(ctx, path, "--version").Output()
}

// spawnDetached starts string update --background from the binary at path
// and does not wait for it. The child has the null device for stdio and its
// own session or process group, so it holds none of the pipes an agent reads
// this command through and outlives the terminal it was started from.
func spawnDetached(path string) error {
	command := exec.Command(path, "update", "--background")
	// Not the caller's directory, which the child would otherwise keep busy.
	command.Dir = filepath.Dir(path)
	command.SysProcAttr = detachedAttributes()
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

// probeOrUpdate reports whether the command only prints the version or help,
// or is string update itself. Install scripts, package managers' tests and
// the updater run --version to look at a binary that may not be installed
// yet, so nothing may follow it.
func probeOrUpdate(list []string) bool {
	if len(list) == 0 || slices.Contains([]string{"", "--help", "-h", "help", "--version", "update"}, list[0]) {
		return true
	}
	options := list
	if end := slices.Index(list, "--"); end >= 0 {
		options = list[:end]
	}
	switch {
	case slices.Contains(options, "--help"):
		return true
	case list[0] == "sitemap":
		return len(list) == 1 || list[1] == ""
	case list[0] == "integrations":
		return len(list) > 1 && (list[1] == "-h" || list[1] == "help")
	}
	return false
}

// afterCommand follows every command but a probe once the command's own
// output is done. On the first command of a new release it rewrites the skill
// where init installed it. Once a day it starts the update check in the
// background, and on a terminal it passes on what an earlier check found.
// When nothing is due it reads one small file. It never writes to stdout,
// never changes how the command ended, and fails silently.
func afterCommand(list []string, rt runtime) {
	defer func() { _ = recover() }()
	list, asJSON := takeJSONFlag(list)
	if probeOrUpdate(list) {
		return
	}
	current, err := release.ParseVersion(version)
	if err != nil {
		return
	}
	statePath, ok := stringFile(rt.env, "update.json")
	if !ok || switchedOff(rt.env) != "" {
		return
	}
	state := readUpdateState(statePath)
	refreshSkills(rt, statePath, state.SeenVersion, current)
	now := rt.clock()
	due := aDayPast(state.CheckedAt, now)
	updated := state.UpdatedTo == version && !state.UpdateAnnounced
	latest, err := release.ParseVersion(state.Latest)
	available := err == nil && takesLatest(latest)
	if !due && !updated && !available {
		return
	}
	copy, err := findCopy(rt)
	if err != nil {
		return
	}
	keys, keysErr := rt.releaseKeys()
	if updatesOff(rt.env, copy, keys, keysErr) != "" {
		return
	}
	notices := rt.progress && !asJSON
	if notices && updated && copy.kind == installScript {
		fmt.Fprintf(rt.stderr, "string updated itself from %s to %s. Turn automatic updates off with STRING_AUTO_UPDATE=0.\n", state.UpdatedFrom, version)
		_ = changeUpdateState(statePath, func(s *updateState) { s.UpdateAnnounced = true })
	}
	if notices && available && copy.kind != installScript && (state.NoticedVersion != state.Latest || aDayPast(state.NoticedAt, now)) {
		command := "string update"
		if m, managed := managers[copy.kind]; managed {
			command = m.command
		}
		fmt.Fprintf(rt.stderr, "string %s is available. Update with: %s\n", state.Latest, command)
		_ = changeUpdateState(statePath, func(s *updateState) { s.NoticedVersion, s.NoticedAt = state.Latest, now.UTC() })
	}
	// A copy that only gets notices is checked only when a person could see
	// one. The check is recorded first, so that a machine where it can never
	// succeed, or where nothing can be recorded, starts no more than one a day.
	if due && rt.spawn != nil && (copy.kind == installScript || rt.progress) {
		if changeUpdateState(statePath, func(s *updateState) { s.CheckedAt = now.UTC() }) == nil {
			_ = rt.spawn(copy.path)
		}
	}
}

// backgroundUpdate is string update --background, which the daily check
// starts. Nobody sees its output, so what goes wrong is kept in update.json
// for string update --check, written before the lock is released so that a
// released lock means the check is over.
func backgroundUpdate(rt runtime) {
	statePath, ok := stringFile(rt.env, "update.json")
	lockPath, _ := stringFile(rt.env, "update.lock")
	if !ok {
		return
	}
	record := func(err error) {
		message := ""
		if err != nil {
			message = err.Error()
		}
		_ = changeUpdateState(statePath, func(s *updateState) { s.Error = message })
	}
	copy, err := findCopy(rt)
	if err != nil {
		record(err)
		return
	}
	keys, keysErr := rt.releaseKeys()
	if updatesOff(rt.env, copy, keys, keysErr) != "" {
		return
	}
	lock, err := takeUpdateLock(lockPath)
	if err != nil {
		if !errors.Is(err, errUpdateRunning) {
			record(err)
		}
		return
	}
	defer lock.release()
	record(checkAndInstall(rt, statePath, copy, keys))
}

// checkAndInstall looks up the latest release and, for a copy that string
// replaces itself, installs it when it is newer than this one.
func checkAndInstall(rt runtime, statePath string, copy installedCopy, keys []ed25519.PublicKey) error {
	base, err := downloadBase(rt.env)
	if err != nil {
		return err
	}
	if copy.kind == installScript {
		removeLeftovers(copy.path)
	}
	u := updater{rt: rt, base: base, keys: keys}
	latest, err := u.latest()
	if err != nil {
		return err
	}
	if err := changeUpdateState(statePath, func(s *updateState) { s.Latest = latest.String() }); err != nil {
		return err
	}
	if !takesLatest(latest) || copy.kind != installScript {
		return nil
	}
	if err := u.install(latest, copy.path); err != nil {
		return err
	}
	return changeUpdateState(statePath, func(s *updateState) {
		s.UpdatedFrom, s.UpdatedTo, s.UpdatedAt, s.UpdateAnnounced = version, latest.String(), rt.clock().UTC(), false
	})
}
