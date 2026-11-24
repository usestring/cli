package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// completionShells are the shells string completion writes a script for.
var completionShells = []string{"bash", "zsh", "fish", "powershell"}

// shellAliases are other names of those shells: pwsh runs PowerShell 7.
var shellAliases = map[string]string{"pwsh": "powershell"}

// requestMethods are the methods string request sends.
var requestMethods = []string{"POST", "PUT", "PATCH"}

// argValues are the words an argument can be, by its name in the usage.
var argValues = map[string][]string{"METHOD": requestMethods, "SHELL": completionShells}

// helpCommandAbout says what string help does, which has no page of its own.
const helpCommandAbout = "a command's page or a topic; --all for every command"

// completionCommand is string completion SHELL, which prints the script that
// makes SHELL complete string's commands, subcommands, flags and values.
func completionCommand(list []string, rt runtime) error {
	parsed, err := parseArgs(list)
	if err != nil {
		return usage(err)
	}
	if parsed.help {
		return write(rt.stdout, []byte(helpFor("completion")))
	}
	if _, ok := parsed.value("input"); ok {
		return usage(errors.New("--input does not apply to completion"))
	}
	if err := expectPositionals(parsed.positionals, 1, "string completion "+strings.Join(completionShells, "|")); err != nil {
		return usage(err)
	}
	shell := strings.ToLower(parsed.positionals[0])
	if alias, ok := shellAliases[shell]; ok {
		shell = alias
	}
	script, ok := completionScripts[shell]
	if !ok {
		return unknownShell(parsed.positionals[0])
	}
	return write(rt.stdout, []byte(script))
}

// unknownShell is the failure for a shell string completion has no script
// for, naming the one meant when it is a near miss.
func unknownShell(typed string) error {
	message := fmt.Sprintf("string completion has no script for %s; it has one for bash, zsh, fish and powershell", typed)
	guess, near := nearest(typed, completionShells)
	if !near {
		return &failure{exit: exitUsage, code: "invalid_usage", message: message}
	}
	line := "string completion " + guess
	return &failure{
		exit:     exitUsage,
		code:     "invalid_usage",
		message:  message,
		nextStep: "run `" + line + "`",
		next:     []nextCommand{{Type: stepFix, Command: line, Argv: strings.Fields(line), Why: "the shell meant"}},
	}
}

// completeCommand is string __complete SHELL WORD..., which the scripts of
// string completion run on each Tab. The words are those after string up to
// the cursor, and the last is the one being typed. It prints what could
// finish that word, one to a line in the form the shell's script reads, or
// :files alone when the word is a file name, which the shell completes
// itself. Whatever it can't answer, it leaves out.
func completeCommand(list []string, rt runtime) {
	if len(list) < 2 {
		return
	}
	shell, words := list[0], slices.Clone(list[1:])
	// kept is the start of the word being typed that an answer leaves as it
	// is: bash replaces only what follows its last = or :, as the j of
	// --format=j, and the other shells replace the whole word.
	kept := ""
	switch shell {
	case "bash":
		var replaced string
		words, replaced = joinedAtBreaks(words)
		kept = strings.TrimSuffix(words[len(words)-1], replaced)
	case "powershell":
		// Windows PowerShell drops an empty argument, so the script sends an
		// empty word as "".
		if words[len(words)-1] == `""` {
			words[len(words)-1] = ""
		}
	}
	found, files := suggestions(words)
	var out strings.Builder
	if files {
		out.WriteString(":files\n")
	}
	for _, c := range found {
		value, ok := strings.CutPrefix(c.value, kept)
		if !ok {
			continue
		}
		switch shell {
		case "bash":
			out.WriteString(value + "\n")
		case "zsh":
			// _describe reads name:description, so a colon in a name is escaped.
			out.WriteString(strings.ReplaceAll(value, ":", `\:`))
			if c.about != "" {
				out.WriteString(":" + c.about)
			}
			out.WriteString("\n")
		case "fish":
			out.WriteString(strings.TrimSuffix(value+"\t"+c.about, "\t") + "\n")
		case "powershell":
			// Windows PowerShell reads a program's output in the console's
			// code page, where UTF-8 shows as other characters.
			out.WriteString(strings.TrimSuffix(value+"\t"+inASCII(c.about), "\t") + "\n")
		}
	}
	_ = write(rt.stdout, []byte(out.String()))
}

// joinedAtBreaks puts back the words bash split at = and :, as it splits
// --format=json into --format, = and json, and says which part of the last
// word bash replaces with an answer: what follows its last = or :.
func joinedAtBreaks(split []string) (words []string, replaced string) {
	glue := false
	for _, word := range split {
		breaks := word != "" && strings.Trim(word, "=:") == ""
		// An empty word after a break is a new one: bash splits --format= at
		// the = and gives the word after it only once a space follows.
		if len(words) > 0 && (breaks || glue && word != "") {
			words[len(words)-1] += word
		} else {
			words = append(words, word)
		}
		glue = breaks
	}
	if last := split[len(split)-1]; !glue {
		replaced = last
	}
	return words, replaced
}

// inASCII is text with what Windows PowerShell can't show spelled in ASCII.
func inASCII(text string) string {
	text = strings.NewReplacer("·", "-", "…", "...").Replace(text)
	return strings.Map(func(r rune) rune {
		if r > unicode.MaxASCII {
			return '?'
		}
		return r
	}, text)
}

// candidate is one way to finish the word being typed, with what it does.
type candidate struct{ value, about string }

// suggestions are what could finish the word being typed, the last of words,
// on a line of string that has the other words before it. files reports that
// the word is a file name, which the shell completes itself.
func suggestions(words []string) (found []candidate, files bool) {
	typed := words[len(words)-1]
	line := readLine(words[:len(words)-1])
	switch {
	case line.ended:
		return nil, false
	case line.pending != nil:
		found, files = valuesOf(*line.pending, typed)
	case strings.HasPrefix(typed, "-") && strings.Contains(typed, "="):
		name, value, _ := strings.Cut(typed, "=")
		f, ok := line.flag(name)
		switch {
		case !ok:
		case f.value == "=false":
			found = []candidate{{name + "=false", f.said()}}
		case takesValue(f):
			found, files = valuesOf(f, value)
			for i := range found {
				found[i].value = name + "=" + found[i].value
			}
		}
	case strings.HasPrefix(typed, "-"):
		// Flags are the same whatever their case, so case doesn't narrow them.
		return slices.DeleteFunc(line.flagCandidates(typed), func(c candidate) bool {
			return !strings.HasPrefix(strings.ToLower(c.value), strings.ToLower(typed))
		}), false
	default:
		found = line.argCandidates()
	}
	return slices.DeleteFunc(found, func(c candidate) bool { return !strings.HasPrefix(c.value, typed) }), files
}

// typedLine is what the words before the one being typed say, read as string
// reads them.
type typedLine struct {
	// command is the command, or empty before one is typed, and sub its
	// subcommand, such as submit for sitemap.
	command, sub string
	// args are the arguments after them.
	args []string
	// given are the flags already given, squashed.
	given []string
	// pending is the flag whose value is the word being typed.
	pending *flagDoc
	// ended is set once -- has come, after which every word is an argument.
	ended bool
}

// readLine reads the words before the one being typed.
func readLine(words []string) typedLine {
	var line typedLine
	for i := 0; i < len(words); i++ {
		word := words[i]
		switch {
		case line.ended:
			line.args = append(line.args, word)
		case word == "--":
			line.ended = true
		case len(word) > 1 && word[0] == '-':
			f, ok := line.flag(word)
			if !ok {
				continue
			}
			line.given = append(line.given, squashed(f.name))
			if takesValue(f) && !strings.Contains(word, "=") {
				if i == len(words)-1 {
					line.pending = &f
				}
				i++
			}
		case line.command == "":
			line.command = word
			// A URL, or a word with a dot, is fetched, as by execute.
			if strings.Contains(word, "://") || strings.Contains(word, ".") {
				line.command, line.args = "fetch", []string{word}
			}
		case line.sub == "" && len(line.args) == 0 && slices.ContainsFunc(subcommandsOf(line.command), func(c candidate) bool { return c.value == word }):
			line.sub = word
		default:
			line.args = append(line.args, word)
		}
	}
	return line
}

// leaf is the command the line runs, once its words name one.
func (line typedLine) leaf() (leaf, bool) {
	path := strings.TrimSpace(line.command + " " + line.sub)
	for _, l := range leaves {
		if l.path == path || slices.Contains(l.aliases, path) {
			return l, true
		}
	}
	return leaf{}, false
}

// flags are the flags the line can take: its command's own first, which are
// each subcommand's until the line names one, then those every command
// takes.
func (line typedLine) flags() []flagDoc {
	var flags []flagDoc
	add := func(more []flagDoc) {
		for _, f := range more {
			if !slices.ContainsFunc(flags, func(have flagDoc) bool { return have.name == f.name }) {
				flags = append(flags, f)
			}
		}
	}
	if l, ok := line.leaf(); ok {
		add(ownFlags(l))
	} else {
		for _, l := range commandDocs[line.command].leaves {
			add(ownFlags(l))
		}
	}
	if line.command == "help" {
		add([]flagDoc{{name: "all", about: "every command on one page"}})
	}
	add(globalFlagDocs)
	return flags
}

// flag is the flag that word, such as --main-content-only or -o, names on
// the line, in any of its spellings or under an older name.
func (line typedLine) flag(word string) (flagDoc, bool) {
	name, _, _ := strings.Cut(word, "=")
	long, isLong := strings.CutPrefix(name, "--")
	if field, ok := fieldAliases[line.command][long]; ok && isLong {
		long = field
	}
	for _, f := range line.flags() {
		if isLong && squashed(long) == squashed(f.name) || !isLong && f.short != "" && name == "-"+f.short {
			return f, true
		}
	}
	return flagDoc{}, false
}

// ownFlags are the flags and request fields that leaf l's page lists for it,
// which can include one every command takes, as login's --no-open is.
func ownFlags(l leaf) []flagDoc {
	flags := slices.Clone(l.page().flags)
	if set := l.fieldSet(); set != "" {
		flags = append(flags, commandDocs[set].fields...)
	}
	return slices.DeleteFunc(flags, func(f flagDoc) bool {
		// FIELD stands for the fields of an integration's action, which only
		// the API knows.
		placeholder := f.name == strings.ToUpper(f.name)
		return placeholder || l.flags != nil && !slices.Contains(l.flags, f.name)
	})
}

// takesValue reports whether flag f takes the next word as its value. A
// switch takes none, and neither does a flag whose value is optional, as
// --captureXHR [JSON] is.
func takesValue(f flagDoc) bool {
	return f.value != "" && !strings.HasPrefix(f.value, "=") && !strings.HasPrefix(f.value, "[")
}

// valuesOf are the values of flag f that could finish typed: the choices its
// value names, as json|text does, or init's agents. A flag that takes a list,
// as --agent NAME[,NAME...] does, goes on after the names already typed.
// files reports that the value is a file name, which a flag's page names
// FILE.
func valuesOf(f flagDoc, typed string) (values []candidate, files bool) {
	switch {
	case f.value == "FILE":
		return nil, true
	case f.name == "agent":
		for _, a := range append([]agent{sharedAgent}, agents...) {
			values = append(values, candidate{a.name, a.label})
		}
	default:
		for choice := range strings.SplitSeq(f.value, "|") {
			if choice != strings.ToLower(choice) || strings.ContainsAny(choice, "@./[,") {
				return nil, false
			}
			values = append(values, candidate{choice, ""})
		}
	}
	if !strings.Contains(f.value, ",") {
		return values, false
	}
	before, _ := cutLast(typed, ",")
	var listed []candidate
	for _, v := range values {
		if !slices.Contains(strings.Split(before, ","), v.value) {
			listed = append(listed, candidate{strings.TrimPrefix(before+","+v.value, ","), v.about})
		}
	}
	return listed, false
}

// cutLast cuts text around the last sep in it, or gives it all as after.
func cutLast(text, sep string) (before, after string) {
	if i := strings.LastIndex(text, sep); i >= 0 {
		return text[:i], text[i+len(sep):]
	}
	return "", text
}

// flagCandidates are the flags the line can take next, each spelled as typed
// spells flags, as --main-content-only after --main-. A flag already given is
// left out, and a switch that is on unless turned off is offered as =false.
func (line typedLine) flagCandidates(typed string) []candidate {
	var found []candidate
	for _, f := range line.flags() {
		if slices.Contains(line.given, squashed(f.name)) {
			continue
		}
		value := "--" + respelled(f.name, typed)
		if f.value == "=false" {
			value += "=false"
		}
		found = append(found, candidate{value, f.said()})
		if typed == "-" && f.short != "" {
			found = append(found, candidate{"-" + f.short, f.said()})
		}
	}
	if line.command == "" {
		found = append(found, candidate{"--version", "print the version"})
	}
	return found
}

// respelled is a flag's name spelled as typed spells flags: in kebab-case
// after --main-, in snake_case after --main_, and as help spells it
// otherwise. string takes each spelling, and a shell offers only what starts
// with what was typed.
func respelled(name, typed string) string {
	typed = strings.TrimLeft(typed, "-")
	switch {
	case strings.Contains(typed, "-"):
		return kebab(name)
	case strings.Contains(typed, "_"):
		return strings.ReplaceAll(kebab(name), "-", "_")
	}
	return name
}

// kebab is a flag's name in kebab-case: main-content-only for
// mainContentOnly, and execute-js for executeJS.
func kebab(name string) string {
	runes := []rune(name)
	var b strings.Builder
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			before := runes[i-1]
			startsAWord := unicode.IsLower(before) || unicode.IsDigit(before) || unicode.IsUpper(before) && i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if startsAWord {
				b.WriteByte('-')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// argCandidates are the words the line's next argument can be: a command, a
// subcommand, a help topic, or a value its usage names, such as a method.
func (line typedLine) argCandidates() []candidate {
	switch {
	case line.command == "":
		return commandCandidates()
	case line.command == "help" && len(line.args) == 0:
		found := slices.DeleteFunc(commandCandidates(), func(c candidate) bool { return c.value == "help" })
		for _, name := range helpTopicNames {
			title, _, _ := strings.Cut(helpTopics[name], "\n")
			_, about, _ := strings.Cut(title, ": ")
			found = append(found, candidate{name, about})
		}
		return found
	case line.command == "help" && len(line.args) == 1:
		return subcommandsOf(line.args[0])
	case line.sub == "" && len(line.args) == 0 && len(subcommandsOf(line.command)) > 0:
		return subcommandsOf(line.command)
	}
	l, ok := line.leaf()
	if !ok || len(line.args) >= len(l.args) {
		return nil
	}
	var found []candidate
	for _, value := range argValues[strings.Trim(l.args[len(line.args)], "[]")] {
		found = append(found, candidate{value, ""})
	}
	return found
}

// commandCandidates are string's commands, each with what its page says it
// does.
func commandCandidates() []candidate {
	var found []candidate
	for _, name := range commands {
		about := commandDocs[name].summary
		if name == "help" {
			about = helpCommandAbout
		}
		found = append(found, candidate{name, about})
	}
	return found
}

// subcommandsOf are the subcommands of the command named name, such as
// submit of sitemap, each with what it does.
func subcommandsOf(name string) []candidate {
	var found []candidate
	for _, l := range commandDocs[name].leaves {
		if words := l.words(); len(words) == 2 {
			found = append(found, candidate{words[1], l.line})
		}
	}
	return found
}

// completionScripts are the scripts string completion prints, by shell. Each
// runs string __complete on Tab, so it completes what the copy of string
// that answers has, and needs no new script when string updates.
var completionScripts = map[string]string{
	"bash": `# string completion for bash. On each Tab it asks string what can come next,
# so it completes the commands, flags and values of the string you run.
# Add it to ~/.bashrc with: string completion bash >> ~/.bashrc
_string_completion() {
    COMPREPLY=()
    local line
    local -a found
    found=()
    while IFS= read -r line; do
        found+=("$line")
    done < <(command string __complete bash "${COMP_WORDS[@]:1:COMP_CWORD}" 2>/dev/null)
    # Nothing to offer: no file names either, where bash can turn them off.
    if [[ ${#found[@]} -eq 0 ]]; then
        compopt +o default 2>/dev/null
        return 0
    fi
    # A file name: bash completes it, as -o default below has it do.
    [[ ${found[0]} == :files ]] && return 0
    COMPREPLY=("${found[@]}")
    return 0
}
complete -o default -F _string_completion string
`,
	"zsh": `#compdef string
# string completion for zsh. On each Tab it asks string what can come next,
# so it completes the commands, flags and values of the string you run.
# Add it to ~/.zshrc, after compinit, with: string completion zsh >> ~/.zshrc
# or save it as _string in a directory on $fpath.
_string() {
    local -a found
    found=(${(f)"$(command string __complete zsh "${(@)words[2,CURRENT]}" 2>/dev/null)"})
    if [[ $found[1] == :files ]]; then
        compset -P '*='
        _files
    elif (( $#found )); then
        _describe -V string found
    fi
}
if [[ $funcstack[1] == _string ]]; then
    _string "$@"
else
    compdef _string string
fi
`,
	"fish": `# string completion for fish. On each Tab it asks string what can come next,
# so it completes the commands, flags and values of the string you run.
# fish has a builtin named string, so in fish this CLI runs as command string.
# Add it with:
#   command string completion fish > ~/.config/fish/conf.d/string-cli.fish
function __string_cli_complete
    set -l words (commandline -opc)
    set -e words[1]
    set -l typed (commandline -ct)
    set -l found (command string __complete fish $words "$typed" 2>/dev/null)
    if test "$found[1]" = :files
        set -l flag (string match -r -- '^-[^=]*=' $typed)
        __fish_complete_path (string replace -r -- '^-[^=]*=' '' $typed) | string replace -r -- '^' "$flag"
        return
    end
    string join \n -- $found
end
complete -c string -f -a '(__string_cli_complete)'
`,
	"powershell": `# string completion for PowerShell. On each Tab it asks string what can come
# next, so it completes the commands, flags and values of the string you run.
# Load it in every session with this line in your profile ($PROFILE):
#   string completion powershell | Out-String | Invoke-Expression
Register-ArgumentCompleter -Native -CommandName string -ScriptBlock {
    param($wordToComplete, $commandAst, $cursorPosition)
    $words = @($commandAst.CommandElements | Select-Object -Skip 1 |
        Where-Object { $_.Extent.EndOffset -lt $cursorPosition } |
        ForEach-Object { $_.Extent.Text })
    # Windows PowerShell drops an empty argument, so an empty word goes as "".
    $typed = if ($wordToComplete) { $wordToComplete } else { '""' }
    $found = @(& string __complete powershell @words $typed 2>$null)
    # A file name: PowerShell completes it when this returns nothing, but not
    # after the = of --output=, so this completes it there.
    if ($found.Count -eq 1 -and $found[0] -eq ':files') {
        if ($wordToComplete -match '^(-[^=]*=)(.*)$') {
            $flag = $Matches[1]
            foreach ($file in [System.Management.Automation.CompletionCompleters]::CompleteFilename($Matches[2])) {
                [System.Management.Automation.CompletionResult]::new($flag + $file.CompletionText, $file.ListItemText, $file.ResultType, $file.ToolTip)
            }
        }
        return
    }
    foreach ($line in $found) {
        $value, $about = $line -split [char]9, 2
        $type = if ($value.StartsWith('-')) { 'ParameterName' } else { 'ParameterValue' }
        if (-not $about) { $about = $value }
        [System.Management.Automation.CompletionResult]::new($value, $value, $type, $about)
    }
}
`,
}
