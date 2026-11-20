package main

import (
	"mime"
	"strings"
)

// forTerminal is untrusted text as it may reach a person's terminal: no ESC,
// so no escape sequence; no other control character but tab and newline; and
// no bidi override or isolate, which can make text read differently from its
// bytes.
func forTerminal(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n':
			return r
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return -1
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return -1
		}
		return r
	}, text)
}

// textual reports whether a response of contentType is text a terminal can
// show, rather than bytes such as a PDF or an image.
func textual(contentType string, data []byte) bool {
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil || media == "" {
		return !strings.ContainsRune(string(data[:min(len(data), 8000)]), 0)
	}
	switch {
	case strings.HasPrefix(media, "text/"), strings.HasSuffix(media, "+json"), strings.HasSuffix(media, "+xml"):
		return true
	}
	switch media {
	case "application/json", "application/xml", "application/javascript", "application/x-javascript", "application/ecmascript":
		return true
	}
	return false
}
