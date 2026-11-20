package main

import (
	"io"
	"net/http"
	"testing"
)

// On a person's terminal, a page loses what could take the terminal over:
// ESC, control characters but tab and newline, and bidi overrides and
// isolates. Piped, or to an agent, it goes out byte for byte.
func TestAPageCantTakeOverAPersonsTerminal(t *testing.T) {
	const page = "# Title\x1b[31m red\x1b[0m ‮evil‬ ⁦iso⁩\r\nline\x07bell\u0085next\ttab\n"
	contentType := "text/markdown"
	base := api(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		io.WriteString(w, page)
	})
	person := execCtx{stdinTTY: true, stdoutTTY: true, stderrTTY: true}
	if got := runAs(base, person, "fetch", "https://example.com"); got.status != 0 || got.output != "# Title[31m red[0m evil iso\nlinebellnext\ttab\n" {
		t.Errorf("a person: %q", got.output)
	}
	for about, exec := range map[string]execCtx{"a pipe": {stdinTTY: true, stderrTTY: true}, "an agent at a terminal": {stdinTTY: true, stdoutTTY: true, stderrTTY: true, agent: "codex"}} {
		if got := runAs(base, exec, "fetch", "https://example.com"); got.status != 0 || got.output != page {
			t.Errorf("%s: %q, want the page as it came", about, got.output)
		}
	}
	// So does JSON, which stays JSON.
	if got := runAs(base, person, "search", "coffee"); got.output != forTerminal(page) {
		t.Errorf("search for a person: %q", got.output)
	}
	// A part of a page is cleaned the same way.
	if got := runAs(base, person, "fetch", "https://example.com", "--max-tokens", "1000"); got.status != 0 || got.output != "# Title[31m red[0m evil iso\nlinebellnext\ttab\n" {
		t.Errorf("a part for a person: %q", got.output)
	}

	// Bytes that aren't text are refused on a person's terminal, with the fix.
	contentType = "application/pdf"
	if got := runAs(base, person, "fetch", "https://example.com/a.pdf", "--format", "raw"); got.status != exitUsage || got.output != "" || got.errors != "error: binary content (application/pdf, 63 B): use -o FILE\n\nnext: run `string fetch https://example.com/a.pdf --format raw -o page.bin`\n" {
		t.Errorf("a PDF for a person: %+v", got)
	}
	if got := runAs(base, execCtx{stdinTTY: true, stderrTTY: true}, "fetch", "https://example.com/a.pdf", "--format", "raw"); got.status != 0 || got.output != page {
		t.Errorf("a PDF piped: %+v", got)
	}
	// Raw text is shown, cleaned.
	contentType = "text/html; charset=utf-8"
	if got := runAs(base, person, "fetch", "https://example.com", "--format", "raw"); got.status != 0 || got.output != forTerminal(page) {
		t.Errorf("raw HTML for a person: %+v", got)
	}
	if forTerminal("a⁧b⁨c‪d‫e") != "abcde" || forTerminal("\x00\x1f\x7f\u009f") != "" {
		t.Errorf("forTerminal leaves bidi or control characters")
	}
}
