package main

import (
	"path/filepath"

	"github.com/usestring/cli/internal/release"
)

// refreshSkills rewrites the skill wherever string init installed it, on the
// first command of a release newer than any that ran here before, so that the
// skill an agent reads describes the commands it runs. It never installs the
// skill where there was none. Only a newer release rewrites it, so that two
// copies of string on one machine do not write their skills back and forth.
//
// The release is recorded as seen_version before anything is rewritten, and
// where that cannot be recorded nothing is, so that such a machine does not
// rewrite the skill on every command.
func refreshSkills(rt runtime, statePath, seen string, current release.Version) {
	if !newerThanSeen(current, seen) {
		return
	}
	// Another copy of string may have recorded a newer release since.
	newest := false
	err := changeUpdateState(statePath, func(s *updateState) {
		if newest = newerThanSeen(current, s.SeenVersion); newest {
			s.SeenVersion = version
		}
	})
	if err != nil || !newest {
		return
	}
	home, err := homeDir(rt.env)
	if err != nil {
		return
	}
	for _, dir := range skillCopies(home, rt.env) {
		_ = installSkills(filepath.Dir(dir))
	}
}

// newerThanSeen reports whether current is newer than seen, the release that
// last rewrote the skill, or no release has.
func newerThanSeen(current release.Version, seen string) bool {
	v, err := release.ParseVersion(seen)
	return err != nil || current.Compare(v) > 0
}
