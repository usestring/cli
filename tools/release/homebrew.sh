#!/usr/bin/env bash
# Installs string with Homebrew, as people do, from the formula at COMMIT of a
# copy of this repository, and checks that Homebrew and the copy it installs
# both say VERSION and that the formula's test passes.
#
#   homebrew.sh REPOSITORY COMMIT VERSION
#
# The release workflow runs it on the commit promote made or found, taken from
# GitHub, and the packages workflow on a pull request's own checkout, so the
# release's script has run before any release depends on it.
set -euo pipefail

if [ $# -ne 3 ]; then
  echo "usage: homebrew.sh REPOSITORY COMMIT VERSION" >&2
  exit 2
fi
repository=$1 commit=$2 version=$3

# Homebrew would otherwise update the tap before installing, to whatever
# commit its main is on by then.
export HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_ENV_HINTS=1

brew tap usestring/cli "$repository"
git -C "$(brew --repository usestring/cli)" checkout --quiet "$commit"
brew install usestring/cli/string
listed=$(brew list --versions string)
if [ "$listed" != "string $version" ]; then
  echo "::error::Homebrew lists the copy it installed as $listed."
  exit 1
fi
got=$("$(brew --prefix)/bin/string" --version)
if [ "$got" != "string $version" ]; then
  echo "::error::Homebrew installed a copy that says $got."
  exit 1
fi
brew test usestring/cli/string
