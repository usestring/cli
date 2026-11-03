// Package skills holds the agent skills that `string init` installs, in the
// skills/<name>/SKILL.md layout skill installers look for in a repository.
package skills

import "embed"

//go:embed string-cli
var FS embed.FS
