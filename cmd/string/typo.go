package main

import (
	"fmt"
	"slices"
	"strings"
)

// commands are string's commands, in the order a close guess prefers them.
var commands = []string{"fetch", "search", "request", "sitemap", "integrations", "init", "login", "logout", "whoami", "trial", "status", "balance", "update", "version", "help"}

// commandSynonyms are words other tools taught people and agents for one of
// string's commands.
var commandSynonyms = map[string][]string{
	"scrape":  {"fetch"},
	"get":     {"fetch"},
	"read":    {"fetch"},
	"crawl":   {"sitemap", "submit"},
	"map":     {"sitemap", "submit"},
	"setup":   {"init"},
	"auth":    {"login"},
	"signin":  {"login"},
	"sign-in": {"login"},
	"credits": {"balance"},
	"doctor":  {"status"},
}

// unknownCommand is the failure for a command string doesn't have. When the
// word is a synonym of a command, or within two edits of one, it names the
// command, and its next step is the same command line with that command,
// which string never runs itself.
func unknownCommand(typed string, rest []string) error {
	meant, ok := commandSynonyms[strings.ToLower(typed)]
	if ok && meant[0] == "init" && len(rest) > 0 && rest[0] == "agent" {
		rest = rest[1:]
	}
	if !ok {
		if guess, near := nearest(typed, commands); near {
			meant, ok = []string{guess}, true
		}
	}
	if !ok {
		return &failure{exit: exitUsage, code: "unknown_command", message: "Unknown command: " + typed, nextStep: "run `string --help` for the commands"}
	}
	line := append([]string{"string"}, meant...)
	for _, arg := range rest {
		line = append(line, shellQuote(arg))
	}
	argv := append([]string{"string"}, meant...)
	return &failure{
		exit:     exitUsage,
		code:     "unknown_command",
		message:  fmt.Sprintf("Unknown command: %s. Did you mean %s?", typed, strings.Join(meant, " ")),
		nextStep: "run `" + strings.Join(line, " ") + "`",
		next:     []nextCommand{{Type: stepFix, Command: strings.Join(line, " "), Argv: append(argv, rest...), Why: "the command meant"}},
	}
}

// squashed is a flag's name in lowercase without - or _, the same for its
// camelCase, kebab-case and snake_case spellings.
func squashed(name string) string {
	return strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(name))
}

// spelledAs is the one flag of known that name spells another way, such as
// main-content-only for mainContentOnly or envFile for env-file.
func spelledAs(name string, known []string) (string, bool) {
	var found []string
	for _, flag := range known {
		if squashed(flag) == squashed(name) {
			found = append(found, flag)
		}
	}
	if len(found) != 1 {
		return "", false
	}
	return found[0], true
}

// nearest is the word of known within two edits of typed, the first of the
// closest, with case, - and _ left out of the count.
func nearest(typed string, known []string) (string, bool) {
	best, bestDistance := "", 3
	for _, word := range known {
		if d := editDistance(squashed(typed), squashed(word)); d < bestDistance {
			best, bestDistance = word, d
		}
	}
	return best, best != ""
}

// editDistance is how many single-character inserts, deletes and changes turn
// a into b.
func editDistance(a, b string) int {
	x, y := []rune(a), []rune(b)
	row := make([]int, len(y)+1)
	for j := range row {
		row[j] = j
	}
	for i := 1; i <= len(x); i++ {
		diagonal := row[0]
		row[0] = i
		for j := 1; j <= len(y); j++ {
			above := row[j]
			change := diagonal
			if x[i-1] != y[j-1] {
				change++
			}
			row[j] = min(above+1, row[j-1]+1, change)
			diagonal = above
		}
	}
	return row[len(y)]
}

// flagNamed is the flag of names that arg spells, in any of its spellings,
// as in --noWait or --no_wait for --no-wait. It never matches a value given
// with =.
func flagNamed(arg string, names []string) (string, bool) {
	if !strings.HasPrefix(arg, "--") || strings.Contains(arg, "=") {
		return "", false
	}
	if slices.Contains(names, arg) {
		return arg, true
	}
	for _, name := range names {
		if squashed(name) == squashed(arg) {
			return name, true
		}
	}
	return "", false
}
