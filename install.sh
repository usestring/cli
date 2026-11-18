#!/bin/sh
# Installs the string CLI on macOS or Linux, then runs `string init` to give
# the coding agents on this machine the string-cli skill.
#
#   curl -fsSL https://raw.githubusercontent.com/usestring/cli/main/install.sh | sh
#
# Arguments after `sh -s --` go to `string init`, e.g. `--agent claude-code`;
# `--no-init` skips it. STRING_VERSION installs that release, as 0.3.0 or
# v0.3.0, instead of the latest, and pins this copy to it. STRING_INSTALL_DIR
# sets where the binary goes (default: ~/.local/bin).
#
# Releases come from https://cli.usestring.ai, or from the GitHub release when
# that host fails in any way. STRING_DOWNLOAD_URL replaces the host, with no
# fallback, for tests and rehearsals.
#
# Everything runs from main, at the end, so that a download cut short runs
# nothing, and so that no command reads the rest of this script from stdin.
set -eu

github="https://github.com/usestring/cli/releases"

fail() {
  echo "install.sh: $*" >&2
  exit 1
}

# release_version prints $1 without a leading v when it names a release, such
# as 0.3.0 or 0.3.0-rc.1, and fails otherwise. A version goes into URLs.
release_version() {
  v="${1#v}"
  case "$v" in
    "" | *[!0-9A-Za-z.-]*) return 1 ;;
  esac
  printf '%s\n' "$v" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$' || return 1
  printf '%s\n' "$v"
}

# fetch downloads $1 to $2 within five minutes, so that a stalled transfer
# ends. From an https URL it follows redirects over https only. When it cannot
# download, it says why in $problem, with curl's reason, rather than leaving
# curl to print a line of its own.
fetch() {
  url="$1"
  out="$2"
  set --
  case "$url" in
    https://*) set -- --proto =https --proto-redir =https ;;
  esac
  if curl -fsSL --connect-timeout 15 --max-time 300 "$@" -o "$out" "$url" 2>"$tmp/curl-error"; then
    return 0
  fi
  reason="$(sed -n 's/^curl: ([0-9]*) //p' "$tmp/curl-error" | tail -n 1)"
  problem="could not download $url${reason:+ ($reason)}"
  return 1
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{ print $1 }'
  else
    shasum -a 256 "$1" | awk '{ print $1 }'
  fi
}

# download fetches the archive and checksums.txt from $1, checks the one
# against the other and unpacks the binary to $tmp/string. It returns nonzero
# with the reason in $problem, so that the caller can try elsewhere.
download() {
  rm -f "$tmp/$archive" "$tmp/checksums.txt" "$tmp/string"
  fetch "$1/$archive" "$tmp/$archive" || return 1
  fetch "$1/checksums.txt" "$tmp/checksums.txt" || return 1
  expected="$(awk -v name="$archive" '$2 == name { print $1 }' "$tmp/checksums.txt")"
  if [ -z "$expected" ] || [ "$expected" != "$(sha256 "$tmp/$archive")" ]; then
    problem="$1/$archive does not match its checksums.txt"
    return 1
  fi
  if ! tar -xzf "$tmp/$archive" -C "$tmp" string; then
    problem="could not unpack string from $1/$archive"
    return 1
  fi
}

# from_host downloads $version, or the release $host/stable names, from $host.
from_host() {
  if [ -z "$version" ]; then
    fetch "$host/stable" "$tmp/stable" || return 1
    if ! version="$(release_version "$(sed -n 1p "$tmp/stable")")"; then
      version=
      problem="$host/stable does not name a release"
      return 1
    fi
  fi
  download "$host/v$version"
}

from_github() {
  if [ -n "$version" ]; then
    download "$github/download/v$version"
  else
    download "$github/latest/download"
  fi
}

cleanup() {
  if [ -n "${part:-}" ]; then rm -f "$part"; fi
  if [ -n "${tmp:-}" ]; then rm -rf "$tmp"; fi
}

main() {
  init=yes
  for arg do
    shift
    if [ "$arg" = --no-init ]; then
      init=
    else
      set -- "$@" "$arg"
    fi
  done

  case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    MINGW* | MSYS* | CYGWIN*) fail "this is Windows; install string from PowerShell instead: irm https://raw.githubusercontent.com/usestring/cli/main/install.ps1 | iex" ;;
    *) fail "unsupported system $(uname -s)" ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) fail "unsupported architecture $(uname -m)" ;;
  esac
  # A shell under Rosetta sees an Intel Mac, where the Arm build runs natively.
  if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null)" = 1 ]; then
    arch=arm64
  fi
  for tool in curl tar awk; do
    command -v "$tool" >/dev/null 2>&1 || fail "install.sh needs $tool"
  done
  command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1 || fail "install.sh needs sha256sum or shasum"

  version=
  pinned=
  if [ -n "${STRING_VERSION:-}" ]; then
    version="$(release_version "$STRING_VERSION")" || fail "STRING_VERSION is $STRING_VERSION, which names no release; use one such as 0.3.0"
    pinned=yes
  fi
  install_dir="${STRING_INSTALL_DIR:-$HOME/.local/bin}"
  archive="string_${os}_${arch}.tar.gz"

  tmp="$(mktemp -d)"
  part=
  trap cleanup EXIT
  trap 'exit 1' HUP INT TERM

  if [ -n "${STRING_DOWNLOAD_URL:-}" ]; then
    host="${STRING_DOWNLOAD_URL%/}"
    from_host || fail "$problem"
  else
    host="https://cli.usestring.ai"
    if ! from_host; then
      echo "install.sh: $problem; trying the GitHub release instead" >&2
      from_github || fail "$problem"
    fi
  fi

  # The binary goes in with a rename inside its directory, which replaces the
  # path and never writes into a file that a running string may be executing.
  mkdir -p "$install_dir" || fail "could not create $install_dir"
  install_dir="$(cd "$install_dir" && pwd)" || fail "could not open $install_dir"
  part="$(mktemp "$install_dir/.string-install.XXXXXX")" || fail "could not write to $install_dir"
  if ! cp "$tmp/string" "$part" || ! chmod 755 "$part"; then
    fail "could not write to $install_dir"
  fi
  reported="$("$part" --version 2>/dev/null)" || fail "the string built for $os/$arch does not run here"
  case "$reported" in
    "string "*) ;;
    *) fail "the string built for $os/$arch does not run here" ;;
  esac
  if [ -n "$version" ] && [ "$reported" != "string $version" ]; then
    fail "the archive for $version holds $reported"
  fi
  mv -f "$part" "$install_dir/string" || fail "could not move string into $install_dir"
  part=
  echo "Installed $reported to $install_dir/string"

  # string update keeps a copy registered this way up to date. A release from
  # before that command fails here, which leaves the copy as it is.
  if [ -n "$pinned" ]; then
    "$install_dir/string" update --register --pin >/dev/null 2>&1 || true
  else
    "$install_dir/string" update --register >/dev/null 2>&1 || true
  fi

  case ":$PATH:" in
    *":$install_dir:"*)
      found="$(command -v string 2>/dev/null || true)"
      if [ -n "$found" ] && [ "$found" != "$install_dir/string" ]; then
        echo "$found comes first on your PATH, so run this one as $install_dir/string, or remove that one."
      fi
      ;;
    *)
      # The line that adds the directory in the login shell's own settings.
      # The user runs it: this script edits none of their files.
      case "${SHELL:-}" in
        */zsh) add="echo 'export PATH=\"$install_dir:\$PATH\"' >> ~/.zshrc" ;;
        */bash)
          # Terminals on macOS start bash as a login shell, which reads
          # .bash_profile and not .bashrc.
          if [ "$os" = darwin ]; then rc=.bash_profile; else rc=.bashrc; fi
          add="echo 'export PATH=\"$install_dir:\$PATH\"' >> ~/$rc"
          ;;
        */fish) add="fish_add_path '$install_dir'" ;;
        *) add= ;;
      esac
      if [ -n "$add" ]; then
        printf '%s is not on your PATH. To add it for new terminals, run:\n\n  %s\n' "$install_dir" "$add"
      else
        echo "Add $install_dir to your PATH to run string from any shell."
      fi
      ;;
  esac

  if [ -z "$init" ]; then
    return 0
  fi
  echo
  PATH="$install_dir:$PATH" "$install_dir/string" init "$@"
}

main "$@"
