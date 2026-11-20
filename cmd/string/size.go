package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// largeBody is the size above which fetch says how big a body is before it
// prints it where no person reads stdout at a terminal.
const largeBody = 16 * 1024

// agentCuts is what each coding agent keeps of a command's output this long,
// as each one's own command runner does it.
var agentCuts = map[string]string{
	"claude-code":    "Claude Code keeps only the first 2 KB of output this long.",
	"codex":          "Codex keeps only the first and last 5,000 tokens of output this long.",
	"gemini-cli":     "Gemini CLI keeps only 40,000 characters of output this long, most of them from its end.",
	"cursor":         "Cursor keeps only the first and last 5,000 characters of output this long.",
	"opencode":       "opencode keeps only the last 50 KiB of output this long.",
	"goose":          "Goose keeps only the last 50 lines of output this long.",
	"cline":          "Cline keeps only the start and end of output over 48,000 characters.",
	"github-copilot": "Copilot CLI keeps only 20 KiB of output this long.",
}

// sizeNotice is the note fetch puts on stderr before a large body: how big
// it is, what the agent running string would keep of it, and the command
// that saves it instead. line is the command as it was given.
func sizeNotice(rt runtime, target string, size int, line []string) string {
	cut, ok := agentCuts[rt.exec.agent]
	if !ok {
		cut = "Most agents cut output this long."
	}
	return fmt.Sprintf("note: %s returned %s bytes (~%s tokens). %s\nSave it and read parts: %s\n",
		hostOf(target), thousands(int64(size)), thousands(int64(tokens(size))), cut, commandLine(append(line, "-o", "page.md")))
}

// tokens estimates the tokens in size bytes of text, at four bytes each.
func tokens(size int) int {
	return (size + 3) / 4
}

// receipt is what string says on stderr for a response it saved to path:
// its size and tokens, for a fetch the site's status and host, and for a
// Markdown page the outline of its headings, with the line each starts on.
func receipt(path string, data []byte, status, target string, markdown bool) string {
	parts := []string{"saved " + path, sizeText(len(data)), "~" + thousands(int64(tokens(len(data)))) + " tokens"}
	if status != "" {
		parts = append(parts, status)
	}
	if target != "" {
		parts = append(parts, hostOf(target))
	}
	said := strings.Join(parts, " · ") + "\n"
	if markdown {
		if headings := outline(string(data)); headings != "" {
			said += "  outline: " + headings + "\n"
		}
	}
	return said
}

// outlineAtMost is how many headings an outline names.
const outlineAtMost = 20

// outline lists a Markdown page's headings of the first three levels, each
// after the line it starts on, so a reader can go to one section, as with
// sed -n. Fenced code is skipped.
func outline(page string) string {
	var headings []string
	more, fenced := 0, false
	for n, line := range strings.Split(page, "\n") {
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fenced = !fenced
			continue
		}
		level := len(line) - len(strings.TrimLeft(line, "#"))
		if fenced || level == 0 || level > 3 || !strings.HasPrefix(line[level:], " ") {
			continue
		}
		if len(headings) == outlineAtMost {
			more++
			continue
		}
		headings = append(headings, strconv.Itoa(n+1)+" "+strings.TrimSpace(line))
	}
	if more > 0 {
		headings = append(headings, fmt.Sprintf("%d more", more))
	}
	return strings.Join(headings, " · ")
}

// sizeText writes a size the way people read it: 812 B, 84 KB, 1.4 MB.
func sizeText(size int) string {
	switch {
	case size < 1024:
		return strconv.Itoa(size) + " B"
	case size < 1024*1024:
		return strconv.Itoa((size+512)/1024) + " KB"
	}
	return strconv.FormatFloat(float64(size)/(1024*1024), 'f', 1, 64) + " MB"
}

// hostOf is target's host, or target itself when it has none.
func hostOf(target string) string {
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		return u.Host
	}
	return target
}

// commandLine writes string and its arguments as a shell reads them.
func commandLine(args []string) string {
	words := []string{"string"}
	for _, arg := range args {
		words = append(words, shellQuote(arg))
	}
	return strings.Join(words, " ")
}
