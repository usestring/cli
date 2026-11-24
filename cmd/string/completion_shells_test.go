package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// The script each shell runs hands string the words of the line as that
// shell splits them, and hands back what string answers: words to offer, a
// file name for the shell to complete, or nothing. Each script runs in its
// own shell, where that shell is installed.
func TestCompletionThroughEachShellsScript(t *testing.T) {
	if testing.Short() {
		t.Skip("builds string")
	}
	goCommand, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go command to build string with: %v", err)
	}
	bin := t.TempDir()
	if output, err := exec.Command(goCommand, "build", "-o", filepath.Join(bin, binaryName()), ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	// The shells start where there is one file to complete.
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "page.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fields := commandDocs["fetch"].fields
	formats := strings.ReplaceAll(fields[slices.IndexFunc(fields, func(f flagDoc) bool { return f.name == "format" })].value, "|", " ")
	// in runs a shell with args, with this string first on PATH, and returns
	// what it printed.
	in := func(t *testing.T, shell string, args ...string) string {
		t.Helper()
		path, err := exec.LookPath(shell)
		if err != nil {
			t.Skipf("no %s here", shell)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, path, args...)
		command.Dir = work
		// PowerShell looks for a release of itself and reports that it ran,
		// unless told not to.
		command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "POWERSHELL_UPDATECHECK=Off", "POWERSHELL_TELEMETRY_OPTOUT=1")
		var stderr bytes.Buffer
		command.Stderr = &stderr
		output, err := command.Output()
		if err != nil {
			t.Fatalf("%s: %v, stderr %q", shell, err, stderr.String())
		}
		// Windows PowerShell can report its own progress there, so it is
		// shown rather than failed on.
		if stderr.Len() > 0 {
			t.Logf("%s wrote to stderr: %q", shell, stderr.String())
		}
		return strings.ReplaceAll(string(output), "\r\n", "\n")
	}

	t.Run("bash", func(t *testing.T) {
		if goruntime.GOOS == "windows" {
			t.Skip("the bash on PATH may be another system's, under WSL")
		}
		// bash sets COMP_WORDS and COMP_CWORD only while it completes a
		// line, with the line split at = and : as well as at spaces, and
		// compopt works only then. The word completed is the last, or the
		// one at says.
		got := in(t, "bash", "--norc", "--noprofile", "-c", `eval "$(string completion bash)"
complete -p string
compopt() { echo "compopt $*"; }
tab() { COMP_WORDS=("$@"); COMP_CWORD=${at:-$(($# - 1))}; _string_completion; echo "${COMPREPLY[*]}"; }
tab string fe
(at=1; tab string fe https : //example.com)
tab string fetch --format ''
tab string fetch --format = m
tab string fetch -o ''
tab string fetch -- ''`)
		want := "complete -o default -F _string_completion string\n" +
			"fetch\n" +
			"fetch\n" +
			formats + "\n" +
			"markdown\n" +
			// A file name, which bash completes when nothing is in COMPREPLY.
			"\n" +
			// Nothing at all.
			"compopt +o default\n\n"
		if got != want {
			t.Errorf("got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("zsh", func(t *testing.T) {
		if goruntime.GOOS == "windows" {
			t.Skip("zsh runs on Windows only under another system")
		}
		// zsh sets words and CURRENT only while it completes a line, and its
		// completion helpers work only then, so these stand in for them.
		// The word completed is the last, or the one at says. The script is
		// also saved as _string in a directory on $fpath, where compinit
		// would find it.
		got := in(t, "zsh", "-f", "-c", `compdef() { print -r -- "compdef $*" }
compset() { print -r -- "compset $*" }
_files() { print -r -- _files }
_describe() { local -a offered; offered=("${(@P)argv[-1]}"); print -r -- "_describe $argv[1,-2]: ${offered%%:*}" }
tab() { words=("$@"); CURRENT=${at:-$#}; _string; print -r -- . }
eval "$(string completion zsh)"
tab string fe
(at=2; tab string fe https://example.com)
tab string fetch --format ''
tab string fetch --format=m
tab string fetch --output=
tab string fetch -- ''
string completion zsh > $1/_string
unfunction _string
fpath=($1 $fpath)
autoload -Uz _string
tab string fe`, "zsh", t.TempDir())
		want := "compdef _string string\n" +
			"_describe -V string: fetch\n.\n" +
			"_describe -V string: fetch\n.\n" +
			"_describe -V string: " + formats + "\n.\n" +
			"_describe -V string: --format=markdown\n.\n" +
			"compset -P *=\n_files\n.\n" +
			".\n" +
			"_describe -V string: fetch\n.\n"
		if got != want {
			t.Errorf("got:\n%s\nwant:\n%s", got, want)
		}
	})

	// Windows PowerShell is powershell, and PowerShell 7 is pwsh.
	for _, shell := range []string{"pwsh", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			// The word completed is at the end of the line, or at the
			// column given.
			script := `$ProgressPreference = 'SilentlyContinue'
string completion powershell | Out-String | Invoke-Expression
function tab($line, $at = $line.Length) {
    (TabExpansion2 -inputScript $line -cursorColumn $at).CompletionMatches.CompletionText -join ' '
}
tab 'string fe'
tab 'string fe https://example.com' 9
tab 'string fetch --format '
tab 'string fetch --format=m'
tab 'string fetch -o '
tab 'string fetch --output=pa'`
			got := strings.Split(in(t, shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", encodedCommand(script)), "\n")
			// PowerShell completes a file name when the script offers
			// nothing, as .\page.md or ./page.md, and the script completes
			// one after --output=.
			if len(got) != 7 || got[0] != "fetch" || got[1] != "fetch" || got[2] != formats || got[3] != "--format=markdown" ||
				!strings.HasSuffix(got[4], "page.md") || !strings.HasPrefix(got[5], "--output=") || !strings.HasSuffix(got[5], "page.md") || got[6] != "" {
				t.Errorf("got %q", got)
			}
		})
	}
}

// encodedCommand is script as PowerShell's -EncodedCommand takes it: UTF-16
// in little-endian order, in base64.
func encodedCommand(script string) string {
	var encoded []byte
	for _, unit := range utf16.Encode([]rune(script)) {
		encoded = binary.LittleEndian.AppendUint16(encoded, unit)
	}
	return base64.StdEncoding.EncodeToString(encoded)
}
