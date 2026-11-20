package main

import (
	"fmt"
	"net/http"
	goruntime "runtime"
)

// userAgent names this build of string, the system it runs on and the coding
// agent that runs it, as in string-cli/0.3.0 (linux/amd64; agent=claude-code).
// Under DO_NOT_TRACK it names only the version.
func userAgent(c execCtx) string {
	switch {
	case c.doNotTrack:
		return "string-cli/" + version
	case c.agent != "":
		return fmt.Sprintf("string-cli/%s (%s/%s; agent=%s)", version, goruntime.GOOS, goruntime.GOARCH, c.agent)
	}
	return fmt.Sprintf("string-cli/%s (%s/%s)", version, goruntime.GOOS, goruntime.GOARCH)
}

// send sends a request to String's API, its portal or its sign-in service.
// Each request says that string sent it, and whether a person, a script, a CI
// job or which coding agent ran it, so that String can tell them apart in its
// own request logs. string sends nothing else about where it runs, and no
// events of its own. Under DO_NOT_TRACK only the version is sent. Each request
// has a time limit, which limited enforces.
func (rt runtime) send(request *http.Request) (*http.Response, error) {
	request.Header.Set("User-Agent", userAgent(rt.exec))
	if !rt.exec.doNotTrack {
		request.Header.Set("X-String-CLI-Mode", rt.exec.mode())
		if rt.exec.agent != "" {
			request.Header.Set("X-String-Agent", rt.exec.agent)
		}
		if rt.exec.agentRaw != "" {
			request.Header.Set("X-String-Agent-Raw", rt.exec.agentRaw)
		}
	}
	return rt.limited(request)
}
