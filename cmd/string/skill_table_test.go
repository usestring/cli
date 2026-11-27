package main

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// rewriteSkill makes TestTheSkillListsEveryCommandByWhatItCanChange write the
// parts of the skill that come from the command table, instead of comparing
// them: go test ./cmd/string -run TestTheSkillListsEveryCommand -update
var rewriteSkill = flag.Bool("update", false, "rewrite the parts of skills/string-cli/SKILL.md that come from the command table")

// skillEffects are what a command can change, in the order the skill lists
// them.
var skillEffects = []string{"read", "local", "spend", "write"}

// skillCommands are the commands the skill names: every leaf, and string help,
// which has no page of its own and never touches the network.
var skillCommands = append(slices.Clone(leaves), leaf{path: "help", effects: "local"})

// allowedTools are the skill's allowed-tools: a rule for each command that
// only reads or stays on this machine, which lets Claude Code run it without
// asking, whatever follows it, and one for string --version, which the skill
// runs first.
func allowedTools() []string {
	var rules []string
	for _, l := range skillCommands {
		if l.effects == "read" || l.effects == "local" {
			rules = append(rules, "Bash(string "+l.path+":*)")
		}
	}
	return append(rules, "Bash(string --version)")
}

// everyCommand is the skill's last section: each command, named as string
// help --all names it, after what it can change.
func everyCommand(t *testing.T) string {
	t.Helper()
	named := map[string][]string{}
	for _, l := range skillCommands {
		named[l.effects] = append(named[l.effects], "`"+strings.TrimSpace("string "+l.path+" "+strings.Join(l.args, " "))+"`")
	}
	section := "## Every command, by what it can change\n\n"
	for _, effect := range skillEffects {
		section += "- " + effect + ": " + strings.Join(named[effect], ", ") + "\n"
		delete(named, effect)
	}
	for effect, commands := range named {
		t.Errorf("the skill has no line for what %s can change: %s", strings.Join(commands, ", "), effect)
	}
	return section
}

// The skill's allowed-tools and its last section come from the command table,
// so that the skill names every command string has, and Claude Code runs
// without asking each one that only reads or stays on this machine.
func TestTheSkillListsEveryCommandByWhatItCanChange(t *testing.T) {
	skill := embeddedSkill(t)
	head, rest, inFront := strings.Cut(skill, "\nallowed-tools: ")
	_, rest, _ = strings.Cut(rest, "\n")
	body, _, last := strings.Cut(rest, "\n## Every command")
	if !inFront || !last {
		t.Fatal("the skill's frontmatter has no allowed-tools line, or the skill no section headed ## Every command")
	}
	tools := "allowed-tools: " + strings.Join(allowedTools(), ", ")
	want := head + "\n" + tools + "\n" + body + "\n" + everyCommand(t)
	if *rewriteSkill {
		if err := os.WriteFile(filepath.Join("..", "..", "skills", "string-cli", "SKILL.md"), []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if skill != want {
		t.Errorf("skills/string-cli/SKILL.md doesn't name the commands as the command table has them, which is:\n%s\n\n%s\nIf the change is meant, run go test ./cmd/string -run TestTheSkillListsEveryCommand -update and read the diff.", tools, everyCommand(t))
	}
}

// covers reports whether an allowed-tools rule lets Claude Code run line
// without asking: a rule that ends in :* runs whatever starts with its words,
// and any other rule only its words.
func covers(rule, line string) bool {
	words := strings.TrimSuffix(strings.TrimPrefix(rule, "Bash("), ")")
	if prefix, ok := strings.CutSuffix(words, ":*"); ok {
		return strings.HasPrefix(line+" ", prefix+" ")
	}
	return line == words
}

// No rule may let a command that spends or writes run without asking, as
// Bash(string integrations:*) would let string integrations run.
func TestTheSkillLetsNoCommandThatSpendsOrWritesRunUnasked(t *testing.T) {
	for _, rule := range allowedTools() {
		for _, l := range skillCommands {
			if (l.effects == "spend" || l.effects == "write") && covers(rule, "string "+l.path) {
				t.Errorf("%s lets string %s run without asking", rule, l.path)
			}
		}
	}
}

// Each command the skill shows that only reads or stays on this machine runs
// without asking, so its advice never waits on a question its allowed-tools
// could have answered, as string integrations would: it runs string
// integrations list, and no rule names it.
func TestEachReadTheSkillShowsRunsWithoutAsking(t *testing.T) {
	rules := allowedTools()
	for _, shown := range regexp.MustCompile("(?m)(?:`|^)(string [^`\n]*)").FindAllStringSubmatch(embeddedSkill(t), -1) {
		line := strings.TrimSpace(shown[1])
		// The command it runs is the one with the longest path, or alias,
		// that starts it.
		runs, effects := "", ""
		for _, l := range skillCommands {
			for _, path := range append([]string{l.path}, l.aliases...) {
				if strings.HasPrefix(line+" ", "string "+path+" ") && len(path) > len(runs) {
					runs, effects = path, l.effects
				}
			}
		}
		if words := strings.Fields(line); runs == "" && len(words) > 1 && slices.Contains(commands, words[1]) {
			t.Errorf("the skill shows %s, which runs a command the skill's list doesn't have", line)
		}
		if (effects == "read" || effects == "local") && !slices.ContainsFunc(rules, func(rule string) bool { return covers(rule, line) }) {
			t.Errorf("the skill shows %s, which runs string %s, but no allowed-tools rule lets it run without asking", line, runs)
		}
	}
}
