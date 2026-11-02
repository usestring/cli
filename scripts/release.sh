#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: scripts/release.sh prepare | publish RELEASE_DIRECTORY APPROVED_SHA256" >&2
  exit 2
}

mode="${1:-}"
case "$mode" in
  prepare)
    [[ $# -eq 1 ]] || usage
    ;;
  publish)
    [[ $# -eq 3 ]] || usage
    [[ "$3" =~ ^[0-9a-f]{64}$ ]] || {
      echo "Approved SHA-256 must be 64 lowercase hex characters" >&2
      exit 2
    }
    ;;
  *) usage ;;
esac

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$repo_root"

if [[ -n "$(git status --porcelain)" ]]; then
  echo "Release requires a clean checkout" >&2
  exit 1
fi

package_name="$(node -p 'JSON.parse(require("node:fs").readFileSync("package.json", "utf8")).name')"
version="$(node -p 'JSON.parse(require("node:fs").readFileSync("package.json", "utf8")).version')"
if [[ "$package_name" != "@usestring/cli" ]]; then
  echo "Unexpected package name: $package_name" >&2
  exit 1
fi
tarball_name="usestring-cli-${version}.tgz"

if [[ "$mode" == "prepare" ]]; then
  mkdir -p release
  release_dir="$(mktemp -d "$repo_root/release/${version}.XXXXXX")"

  bun install --frozen-lockfile
  rm -rf -- dist
  bun run build
  bun run typecheck
  bun test
  npm pack --pack-destination "$release_dir"

  tarball="$release_dir/$tarball_name"
  [[ -f "$tarball" ]] || { echo "Package tarball is missing: $tarball" >&2; exit 1; }
  git rev-parse HEAD > "$release_dir/SOURCE_COMMIT"
  (cd "$release_dir" && shasum -a 256 "$tarball_name" > SHA256SUMS)

  smoke_dir="$(mktemp -d /tmp/string-cli-smoke.XXXXXX)"
  trap 'rm -rf -- "$smoke_dir"' EXIT
  npm install --prefix "$smoke_dir" --offline --ignore-scripts "$tarball"
  "$smoke_dir/node_modules/.bin/string" --help >/dev/null

  echo "Release candidate: $release_dir"
  cat "$release_dir/SHA256SUMS"
  exit 0
fi

[[ "$(git branch --show-current)" == "main" ]] || {
  echo "Publish requires the main branch" >&2
  exit 1
}
local_commit="$(git rev-parse HEAD)"
remote_commit="$(git ls-remote origin refs/heads/main | awk '{print $1}')"
[[ -n "$remote_commit" && "$local_commit" == "$remote_commit" ]] || {
  echo "Publish requires the current origin/main commit" >&2
  exit 1
}

release_dir="$(cd "$2" && pwd -P)"
case "$release_dir" in
  "$repo_root"/release/*) ;;
  *) echo "Release directory must be under $repo_root/release" >&2; exit 1 ;;
esac
[[ "$(cat "$release_dir/SOURCE_COMMIT")" == "$local_commit" ]] || {
  echo "Release candidate was built from another commit" >&2
  exit 1
}
tarball="$release_dir/$tarball_name"
[[ -f "$tarball" ]] || { echo "Package tarball is missing: $tarball" >&2; exit 1; }
(cd "$release_dir" && shasum -a 256 -c SHA256SUMS)
actual_digest="$(shasum -a 256 "$tarball" | awk '{print $1}')"
[[ "$actual_digest" == "$3" ]] || {
  echo "Tarball digest $actual_digest does not match the approved digest $3" >&2
  exit 1
}

npm whoami --registry=https://registry.npmjs.org/ >/dev/null
npm publish "$tarball" --registry=https://registry.npmjs.org/ --access public
