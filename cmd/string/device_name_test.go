package main

import (
	"cmp"
	"errors"
	"reflect"
	goruntime "runtime"
	"testing"
)

func TestTheDefaultDeviceNameIsTheSystemAndTheShortHostname(t *testing.T) {
	for _, c := range []struct{ goos, hostname, want string }{
		{"darwin", "Work-Laptop.local", "Mac · Work-Laptop"},
		{"linux", "build-7.corp.example.test", "Linux · build-7"},
		{"windows", "DESKTOP-1", "Windows · DESKTOP-1"},
		// A system string has no name for, on a machine that reports no hostname.
		{"freebsd", "", "freebsd"},
	} {
		got := defaultDeviceName(c.goos, func() (string, error) {
			if c.hostname == "" {
				return "", errors.New("no hostname")
			}
			return c.hostname, nil
		})
		if got != c.want {
			t.Errorf("%s on %q is named %q, want %q", c.goos, c.hostname, got, c.want)
		}
	}
}

// The portal names the new key after the header of the key request, which it
// percent-decodes. The requests to the sign-in service carry no name: it has
// no use for a hostname.
func TestLoginNamesItsMachineOnTheKeyRequestAndNowhereElse(t *testing.T) {
	system := cmp.Or(systemNames[goruntime.GOOS], goruntime.GOOS)
	for _, c := range []struct {
		about    string
		terminal bool
		commands [][]string
		want     string
	}{
		{"the default name", true, [][]string{{"login"}}, system + "%20%C2%B7%20build-7"},
		{"--device-name", true, [][]string{{"login", "--device-name", "dev's Mac · büro"}}, "dev%27s%20Mac%20%C2%B7%20b%C3%BCro"},
		{"a name given to --no-wait, which --poll sends", false, [][]string{{"login", "--no-wait", "--device-name", "work laptop"}, {"login", "--poll"}}, "work%20laptop"},
	} {
		service := &signInService{token: []signInAnswer{signInApproved}, key: []signInAnswer{signInKey}}
		service.start(t)
		session := newSignInSession(t)
		session.terminal = c.terminal
		for _, command := range c.commands {
			if got := session.run(command...); got.status != 0 {
				t.Fatalf("%s: string %v: %+v", c.about, command, got)
			}
		}
		if got, want := service.deviceNames(), []string{"/api/oauth/api-key " + c.want}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the requests that carried a device name are %q, want only %q", c.about, got, want)
		}
	}
}
