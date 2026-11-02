#!/bin/sh
# Installs the string CLI from its GitHub release, then runs `string init` to
# give the coding agents on this machine the string-cli skill.
#
#   curl -fsSL https://raw.githubusercontent.com/usestring/cli/main/install.sh | sh
#
# Arguments after `sh -s --` go to `string init`, e.g. `--agent claude-code`;
# `--no-init` skips it. STRING_VERSION pins a release tag (default: latest),
# STRING_INSTALL_DIR sets where the binary goes (default: ~/.local/bin).
set -eu

repo="https://github.com/usestring/cli/releases"
install_dir="${STRING_INSTALL_DIR:-$HOME/.local/bin}"

fail() {
  echo "install.sh: $*" >&2
  exit 1
}

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "unsupported system $(uname -s); on Windows use scoop or winget" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "unsupported architecture $(uname -m)" ;;
esac

if [ -n "${STRING_VERSION:-}" ]; then
  base="${STRING_DOWNLOAD_URL:-$repo/download/$STRING_VERSION}"
else
  base="${STRING_DOWNLOAD_URL:-$repo/latest/download}"
fi
archive="string_${os}_${arch}.tar.gz"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
curl -fsSL "$base/$archive" -o "$tmp/$archive" || fail "could not download $base/$archive"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || fail "could not download $base/checksums.txt"

expected="$(awk -v name="$archive" '$2 == name { print $1 }' "$tmp/checksums.txt")"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$archive" | awk '{ print $1 }')"
else
  actual="$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')"
fi
[ -n "$expected" ] && [ "$expected" = "$actual" ] || fail "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp" string
mkdir -p "$install_dir"
mv "$tmp/string" "$install_dir/string"
chmod 755 "$install_dir/string"
echo "Installed $("$install_dir/string" --version) to $install_dir/string"

case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) echo "Add $install_dir to your PATH to run string from any shell." ;;
esac

if [ "${1:-}" = "--no-init" ]; then
  exit 0
fi
echo
PATH="$install_dir:$PATH" "$install_dir/string" init "$@"
