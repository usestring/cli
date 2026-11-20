package main

import (
	"fmt"
	"strconv"
)

// What 0.3 says ahead of the changes 0.4 makes to fetch, on stderr only.
// STRING_NO_DEPRECATION_WARNINGS=1 silences it, and so does STRING_COMPAT=0.2,
// which keeps 0.2's behavior once 0.4 is out.

// announcing reports whether string says what changes in 0.4.
func announcing(env func(string) (string, bool)) bool {
	compat, _ := env("STRING_COMPAT")
	return !switchedOn(env, "STRING_NO_DEPRECATION_WARNINGS") && compat != "0.2"
}

// fetchJSONNotice is said when fetch runs with --json, whose stdout becomes a
// JSON object in 0.4.
const fetchJSONNotice = `notice: from 0.4, --json on fetch prints a JSON object (markdown, statusCode, finalUrl, tokens). To keep
Markdown on stdout with JSON errors, use --errors json (works now). STRING_COMPAT=0.2 keeps the old behavior.
`

// siteStatusWarning is said when the site that fetch read answered status,
// as X-Status-Code gives it, and the exit code for that changes in 0.4: a
// missing page exits 8, another error 6. It is empty for any other status.
func siteStatusWarning(status, url string) string {
	code, err := strconv.Atoi(status)
	switch {
	case err != nil || code < 400:
		return ""
	case code == 404 || code == 410:
		return fmt.Sprintf("warning: %s answered %d. From 0.4, string fetch exits 8 for that (target_not_found); --no-fail will keep exit 0.\n", url, code)
	}
	return fmt.Sprintf("warning: %s answered %d. From 0.4, string fetch exits 6 for that (target_http_error), with the page still printed; --no-fail will keep exit 0.\n", url, code)
}
