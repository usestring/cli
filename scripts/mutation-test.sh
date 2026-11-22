#!/usr/bin/env bash
# Mutation-test Go packages with gremlins. Opt-in and report-only: nothing in CI runs it, and it
# never fails on a low score.
#
# Usage:
#   scripts/mutation-test.sh <package-path> [--out <report.json>] [--workers <n>]
#
# Example:
#   scripts/mutation-test.sh ./internal/release
#
# gremlins mutates copies in its own temp workdirs and leaves the checkout untouched, so it runs in
# place from the module root. A mutant whose tests fail is KILLED; one they still pass is LIVED.
# Score only code in a protected category (money, auth and secrets, safety gates, data correctness,
# contracts) and read each LIVED mutant there.
set -euo pipefail

GREMLINS_VERSION=v0.6.0

usage() {
  sed -n '5,9p' "$0" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

[[ $# -ge 1 && "$1" != -* ]] || usage
pkg="$1"
shift
args=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --out)
      [[ $# -ge 2 ]] || usage
      if [[ "$2" = /* ]]; then
        out="$2"
      else
        out="$PWD/$2"
      fi
      args+=(--output "$out")
      shift 2
      ;;
    --workers)
      [[ $# -ge 2 ]] || usage
      args+=(--workers "$2")
      shift 2
      ;;
    *) usage ;;
  esac
done

root="$(cd "$(dirname "$0")/.." && pwd)"
tools="${XDG_CACHE_HOME:-$HOME/.cache}/go-mutation-test/gremlins-$GREMLINS_VERSION"
if [[ ! -x "$tools/gremlins" ]]; then
  GOBIN="$tools" go install "github.com/go-gremlins/gremlins/cmd/gremlins@$GREMLINS_VERSION"
fi

cd "$root"
GOWORK=off "$tools/gremlins" unleash ${args[@]+"${args[@]}"} "$pkg"
