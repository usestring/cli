package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Read part after part, a page comes back whole, with nothing lost or
// repeated, and each part holds about as many tokens as asked for. Most parts
// end at a line's end; a line longer than a part goes on in the next part.
func TestPagesCoverThePageWhole(t *testing.T) {
	var lines []string
	for i := range 300 {
		lines = append(lines, fmt.Sprintf("line %d of the page, %s", i, strings.Repeat("words ", i%9)))
	}
	text := strings.Join(lines, "\n") + "\n"
	short := "a\nbc\n" + strings.Repeat("abcdefghij", 101)
	for _, c := range []struct {
		text      string
		maxTokens int
	}{{text, 1}, {text, 7}, {text, 50}, {text, 333}, {text, 100000}, {short, 1}, {short, 10}, {strings.Repeat("abcdefghij", 101), 10}} {
		var whole strings.Builder
		atLineEnd, parts := 0, 0
		for offset := 0; ; parts++ {
			if parts > len(c.text) {
				t.Fatalf("--max-tokens %d: the parts never end", c.maxTokens)
			}
			part, start, end, next := page(c.text, c.maxTokens, offset)
			if start != whole.Len() || len(part) > c.maxTokens*4+3 {
				t.Fatalf("--max-tokens %d: a part of %d characters starts at %d, after %d", c.maxTokens, len(part), start, whole.Len())
			}
			whole.WriteString(part)
			if strings.HasSuffix(part, "\n") {
				atLineEnd++
			}
			if next == 0 {
				if end != len(c.text) {
					t.Errorf("--max-tokens %d: the last part ends at %d of %d", c.maxTokens, end, len(c.text))
				}
				break
			}
			offset = next
		}
		if whole.String() != c.text {
			t.Errorf("--max-tokens %d: the parts together are %d characters, want the page's %d", c.maxTokens, whole.Len(), len(c.text))
		}
		if c.text == text && c.maxTokens == 50 && atLineEnd*2 < parts {
			t.Errorf("--max-tokens 50: only %d of %d parts end at a line's end", atLineEnd, parts)
		}
	}
}

// --max-tokens prints one part and says which, with the command for the
// next; the next part comes from the local copy, which spends no request
// until it is 15 minutes old.
func TestAPageReadInPartsSpendsOneFetch(t *testing.T) {
	text := strings.Repeat("A line of the page that is long enough.\n", 30)
	var calls atomic.Int32
	base := api(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, text)
	})
	cache, clock := t.TempDir(), time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	fetch := func(list ...string) result {
		var stdout, stderr bytes.Buffer
		status := run(list, runtime{
			env:    envOf(map[string]string{"STRING_API_KEY": "test-key", "STRING_API_BASE_URL": base, "XDG_CACHE_HOME": cache}),
			client: http.DefaultClient,
			stdin:  strings.NewReader(""),
			stdout: &stdout,
			stderr: &stderr,
			now:    func() time.Time { return clock },
		})
		return result{status, stdout.String(), stderr.String()}
	}

	first := fetch("fetch", "https://example.com", "--max-tokens", "100")
	if want := "part 1 of 3 (tokens 0–100 of ~300). next: string fetch https://example.com --max-tokens 100 --offset 100\n"; first.status != 0 || first.output != text[:400] || first.errors != want || calls.Load() != 1 {
		t.Errorf("the first part: %+v after %d requests, want stderr %q", first, calls.Load(), want)
	}
	second := fetch("fetch", "https://example.com", "--max-tokens", "100", "--offset", "100")
	if want := "part 2 of 3 (tokens 100–200 of ~300), from the local copy, no credit used. next: string fetch https://example.com --max-tokens 100 --offset 200\n"; second.status != 0 || second.output != text[400:800] || second.errors != want || calls.Load() != 1 {
		t.Errorf("the second part: %+v after %d requests, want stderr %q", second, calls.Load(), want)
	}
	last := fetch("fetch", "https://example.com", "--maxTokens=100", "--offset=200")
	if want := "part 3 of 3 (tokens 200–300 of ~300), from the local copy, no credit used\n"; last.status != 0 || last.output != text[800:] || last.errors != want {
		t.Errorf("the last part: %+v, want stderr %q", last, want)
	}
	if goruntime.GOOS != "windows" {
		files, _ := filepath.Glob(filepath.Join(cache, "string", "fetch", "*.page"))
		if info, err := os.Stat(files[0]); len(files) != 1 || err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("the local copy: %v, %v", files, err)
		}
	}
	// A copy past 15 minutes isn't used: the part is fetched again.
	clock = clock.Add(16 * time.Minute)
	if again := fetch("fetch", "https://example.com", "--max-tokens", "100", "--offset", "100"); again.status != 0 || strings.Contains(again.errors, "local copy") || calls.Load() != 2 {
		t.Errorf("after 16 minutes: %+v after %d requests", again, calls.Load())
	}
	// Other fields are another page, with a copy of their own.
	if other := fetch("fetch", "https://example.com", "--mainContentOnly", "--max-tokens", "100", "--offset", "100"); strings.Contains(other.errors, "local copy") || calls.Load() != 3 {
		t.Errorf("another field: %+v after %d requests", other, calls.Load())
	}

	for list, want := range map[string]string{
		"fetch https://example.com --offset 100":                    "error: --offset needs --max-tokens, which sets the size of each part\n",
		"fetch https://example.com --max-tokens 0":                  "error: --max-tokens must be a positive integer\n",
		"fetch https://example.com --max-tokens 100 -o page.md":     "error: Use --max-tokens to read a page in parts, or -o to save all of it, not both\n",
		"fetch https://example.com --max-tokens 100 --format raw":   "error: --max-tokens pages text, not --format raw\n",
		"fetch https://example.com --max-tokens 100 --offset minus": "error: --offset must be a nonnegative integer\n",
	} {
		if got := runOffline(t, strings.Fields(list)...); got.status != exitUsage || got.errors != want {
			t.Errorf("string %s: %+v, want %q", list, got, want)
		}
	}
	// The next command keeps the flags as given, with the new offset.
	if got := withOffset([]string{"fetch", "https://example.com", "--max-tokens", "100", "--offset=100", "--json"}, 200); strings.Join(got, " ") != "fetch https://example.com --max-tokens 100 --json --offset "+strconv.Itoa(200) {
		t.Errorf("withOffset: %v", got)
	}
}

// A command string suggests repeats the global switches given, wherever
// they were, so that it runs as the one given did: the next part's command,
// and the size notice's.
func TestASuggestedCommandRepeatsTheGlobalSwitches(t *testing.T) {
	body := "# Example Domain\n\nThis domain is for use in examples.\n"
	base := api(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	})
	for list, next := range map[string]string{
		"fetch https://example.com --max-tokens 5 --errors json":    "string fetch https://example.com --max-tokens 5 --errors json --offset 5",
		"--json fetch https://example.com --max-tokens 5":           "string fetch https://example.com --max-tokens 5 --json --offset 5",
		"fetch https://example.com --max-tokens 5 --timeout=30s -q": "string fetch https://example.com --max-tokens 5 --timeout=30s -q --offset 5",
	} {
		if got := runFetching(base, map[string]string{"HOME": t.TempDir()}, strings.Fields(list)...); got.status != 0 || !strings.HasSuffix(got.errors, ". next: "+next+"\n") {
			t.Errorf("string %s: %+v, want next: %s", list, got, next)
		}
	}
	body = strings.Repeat("Example text.\n", 1500)
	if got := runFetching(base, map[string]string{"HOME": t.TempDir()}, "fetch", "https://example.com", "--errors", "json"); !strings.Contains(got.errors, "Save it and read parts: string fetch https://example.com --errors json -o page.md\nOr page it: string fetch https://example.com --errors json --max-tokens 6000\n") {
		t.Errorf("a big page: stderr %q", got.errors)
	}
}
