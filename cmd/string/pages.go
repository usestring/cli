package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// copyLife is how long a page's local copy serves the pages after the first,
// so reading a long page in parts spends one fetch.
const copyLife = 15 * time.Minute

// page is the part of text that --max-tokens maxTokens and --offset offset
// ask for, both in tokens of four characters. Every part but the last ends on
// a whole token, so the next part starts exactly at its offset, and parts read
// one after another give the page back with nothing lost or repeated. A part
// ends at a line's end that falls on a whole token, when its second half has
// one; else at its last line's end, taking up to three characters of the next
// line; and inside a line longer than the part, at the part's size. next is
// the --offset of the part after it, or 0 for the last part.
func page(text string, maxTokens, offset int) (part string, start, end, next int) {
	start = min(offset*4, len(text))
	end = min(start+maxTokens*4, len(text))
	if end < len(text) {
		window := text[start:end]
		if last := strings.LastIndexByte(window, '\n'); last >= 0 {
			end = min((start+last+1+3)/4*4, len(text))
			for p := last; p >= len(window)/2; p = strings.LastIndexByte(window[:p], '\n') {
				if (start+p+1)%4 == 0 {
					end = start + p + 1
					break
				}
			}
		}
		next = end / 4
	}
	return text[start:end], start, end, next
}

// pageFooter is what stderr says after a part: which part it is, the tokens it
// holds of the whole, and the command for the next part.
func pageFooter(line []string, total, maxTokens, start, end, next int, fromCopy bool) string {
	totalTokens := tokens(total)
	parts := max(1, (totalTokens+maxTokens-1)/maxTokens)
	said := fmt.Sprintf("part %d of %d (tokens %s–%s of ~%s)", min(start/4/maxTokens+1, parts), parts, thousands(int64(start/4)), thousands(int64(tokens(end))), thousands(int64(totalTokens)))
	if fromCopy {
		said += ", from the local copy, no credit used"
	}
	if next > 0 {
		said += ". next: " + commandLine(withOffset(line, next))
	}
	return said + "\n"
}

// withOffset is line with its --offset, in any spelling, set to offset.
func withOffset(line []string, offset int) []string {
	out := make([]string, 0, len(line)+2)
	for i := 0; i < len(line); i++ {
		name, _, inline := strings.Cut(line[i], "=")
		if spelled, ok := flagNamed(name, []string{"--offset"}); ok && spelled == "--offset" {
			if !inline {
				i++
			}
			continue
		}
		out = append(out, line[i])
	}
	return append(out, "--offset", strconv.Itoa(offset))
}

// copyPath is where the local copy of a fetch with body lives: the cache
// directory, under a name made from the request.
func copyPath(env func(string) (string, bool), body map[string]any) (string, bool) {
	dir, ok := env("XDG_CACHE_HOME")
	if !ok || !filepath.IsAbs(dir) {
		home, err := homeDir(env)
		if err != nil {
			return "", false
		}
		dir = filepath.Join(home, ".cache")
	}
	request, _ := json.Marshal(body)
	sum := sha256.Sum256(request)
	return filepath.Join(dir, "string", "fetch", hex.EncodeToString(sum[:16])+".page"), true
}

// readCopy is the local copy of the fetch with body, when one was saved in
// the last copyLife.
func readCopy(rt runtime, body map[string]any) ([]byte, bool) {
	path, ok := copyPath(rt.env, body)
	if !ok {
		return nil, false
	}
	info, err := os.Stat(path)
	if err != nil || rt.clock().Sub(info.ModTime()) > copyLife {
		return nil, false
	}
	data, err := os.ReadFile(path)
	return data, err == nil
}

// saveCopy keeps data as the local copy of the fetch with body, readable only
// by its owner, and clears copies past their life. It fails silently.
func saveCopy(rt runtime, body map[string]any, data []byte) {
	path, ok := copyPath(rt.env, body)
	if !ok || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	if old, err := os.ReadDir(filepath.Dir(path)); err == nil {
		for _, entry := range old {
			if info, err := entry.Info(); err == nil && rt.clock().Sub(info.ModTime()) > copyLife {
				os.Remove(filepath.Join(filepath.Dir(path), entry.Name()))
			}
		}
	}
	if os.WriteFile(path, data, 0o600) == nil {
		os.Chtimes(path, rt.clock(), rt.clock())
	}
}
