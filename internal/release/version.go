// Package release holds what a published release is made of and how a copy of
// string decides to trust one: the version order, the manifest listing each
// platform's archive, and the signature over that manifest. The CLI's updater
// and the release tool share it, so what the release workflow signs is exactly
// what an installed copy checks.
package release

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"
)

// Version is a semantic version (https://semver.org), the form a release tag
// takes after its v.
type Version struct {
	major, minor, patch uint64
	pre                 []string
	text                string
}

// ParseVersion reads a version such as 0.3.0 or 0.3.0-rc.1. It takes no v,
// since neither the stable pointer nor a build's version carries one.
func ParseVersion(text string) (Version, error) {
	invalid := fmt.Errorf("%q is not a version such as 0.3.0", text)
	rest, build, hasBuild := strings.Cut(text, "+")
	if hasBuild && !validIdentifiers(build, false) {
		return Version{}, invalid
	}
	core, pre, hasPre := strings.Cut(rest, "-")
	if hasPre && !validIdentifiers(pre, true) {
		return Version{}, invalid
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, invalid
	}
	numbers := make([]uint64, 3)
	for i, part := range parts {
		if !numeric(part) {
			return Version{}, invalid
		}
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return Version{}, invalid
		}
		numbers[i] = n
	}
	v := Version{major: numbers[0], minor: numbers[1], patch: numbers[2], text: text}
	if hasPre {
		v.pre = strings.Split(pre, ".")
	}
	return v, nil
}

func (v Version) String() string { return v.text }

// Prerelease reports whether v is a pre-release, such as 0.3.0-rc.1.
func (v Version) Prerelease() bool { return len(v.pre) > 0 }

// Compare orders versions by semver precedence: a pre-release comes before
// its release, and build metadata counts for nothing.
func (v Version) Compare(w Version) int {
	if c := cmp.Compare(v.major, w.major); c != 0 {
		return c
	}
	if c := cmp.Compare(v.minor, w.minor); c != 0 {
		return c
	}
	if c := cmp.Compare(v.patch, w.patch); c != 0 {
		return c
	}
	switch {
	case len(v.pre) == 0 && len(w.pre) == 0:
		return 0
	case len(v.pre) == 0:
		return 1
	case len(w.pre) == 0:
		return -1
	}
	for i := range min(len(v.pre), len(w.pre)) {
		if c := compareIdentifiers(v.pre[i], w.pre[i]); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(v.pre), len(w.pre))
}

// compareIdentifiers orders two pre-release identifiers: numbers by value and
// below any word, words by their ASCII text.
func compareIdentifiers(a, b string) int {
	aNumber, bNumber := numeric(a), numeric(b)
	switch {
	case aNumber && bNumber:
		// Neither has a leading zero, so the longer is the larger.
		if c := cmp.Compare(len(a), len(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	case aNumber:
		return -1
	case bNumber:
		return 1
	}
	return strings.Compare(a, b)
}

// numeric reports whether s is a number as semver writes one: digits, with no
// leading zero unless it is 0.
func numeric(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	return strings.Trim(s, "0123456789") == ""
}

// validIdentifiers checks a dot-separated pre-release or build part. Only a
// pre-release forbids a number with a leading zero.
func validIdentifiers(part string, pre bool) bool {
	for identifier := range strings.SplitSeq(part, ".") {
		if identifier == "" || strings.Trim(identifier, "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-") != "" {
			return false
		}
		if pre && strings.Trim(identifier, "0123456789") == "" && !numeric(identifier) {
			return false
		}
	}
	return true
}
