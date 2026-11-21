package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/usestring/cli/skills"
)

// skillCommand prints the string-cli skill that this release carries, so an
// agent can read it without installing it, or with --path, the SKILL.md of
// each copy installed in an agent's skills directory.
func skillCommand(list []string, rt runtime) error {
	parsed, err := parse(list, spec{switches: []string{"path"}})
	if err != nil {
		return usage(err)
	}
	if parsed.help {
		return write(rt.stdout, []byte(helpFor("skill")))
	}
	if err := expectPositionals(parsed.positionals, 0, "string skill [--path]"); err != nil {
		return usage(err)
	}
	if _, ok := parsed.value("input"); ok {
		return usage(errors.New("--input does not apply to skill"))
	}
	if !parsed.switches["path"] {
		data, err := fs.ReadFile(skills.FS, skillName+"/SKILL.md")
		if err != nil {
			return err
		}
		return write(rt.stdout, data)
	}
	home, err := homeDir(rt.env)
	if err != nil {
		return err
	}
	var paths strings.Builder
	for _, dir := range skillCopies(home, rt.env) {
		path := filepath.Join(dir, "SKILL.md")
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			paths.WriteString(path + "\n")
		}
	}
	if paths.Len() == 0 && !rt.quiet {
		fmt.Fprintln(rt.stderr, "The string-cli skill isn't installed for any agent. string init installs it, and string skill prints it.")
	}
	return write(rt.stdout, []byte(paths.String()))
}
