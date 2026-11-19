package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	goruntime "runtime"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// string login tells the String portal which machine it runs on, and the
// portal puts that in the name of the API key it creates, as in
// "String CLI · Mac · work-laptop". An organization's API keys page then shows
// which machine holds which key.
//
// The name travels in a header of the key request and nowhere else. It is
// percent-encoded there, because a header carries ASCII only.
const deviceNameHeader = "X-String-Device-Name"

// maxDeviceName is how much of the name the portal keeps. The portal measures
// it as JavaScript measures a string, in UTF-16 code units, so a character
// outside the Basic Multilingual Plane, such as most emoji, counts as two.
const maxDeviceName = 51

// systemNames is what people call the systems string is released for.
var systemNames = map[string]string{"darwin": "Mac", "linux": "Linux", "windows": "Windows"}

// defaultDeviceName describes a machine by its system and the first label of
// its hostname, as in "Mac · work-laptop". The rest of a hostname, such as
// .local or a company's domain, says no more about which machine it is.
func defaultDeviceName(goos string, hostname func() (string, error)) string {
	system := systemNames[goos]
	if system == "" {
		system = goos
	}
	host, _ := hostname()
	host, _, _ = strings.Cut(strings.TrimSpace(host), ".")
	if host == "" {
		return system
	}
	return system + " · " + host
}

// deviceName is the default name of the machine string runs on.
func (rt runtime) deviceName() string {
	lookup := rt.osHostname
	if lookup == nil {
		lookup = os.Hostname
	}
	return defaultDeviceName(goruntime.GOOS, lookup)
}

// deviceNameOption is --device-name, or "" without the flag. It is checked
// before the sign-in starts, so a name String would cut short is refused
// while the person can still change it.
func deviceNameOption(parsed args) (string, error) {
	value, ok := parsed.value("device-name")
	if !ok {
		return "", nil
	}
	name := strings.Join(strings.Fields(value), " ")
	switch {
	case name == "":
		return "", errors.New("--device-name needs a name for this machine, such as work-laptop")
	case !utf8.ValidString(name):
		return "", errors.New("--device-name is not valid UTF-8")
	case len(utf16.Encode([]rune(name))) > maxDeviceName:
		return "", fmt.Errorf("--device-name can be at most %d characters long, and most emoji count as two", maxDeviceName)
	}
	return name, nil
}

// encodeDeviceName is name as the value of deviceNameHeader.
func encodeDeviceName(name string) string {
	return url.PathEscape(strings.ToValidUTF8(name, ""))
}
