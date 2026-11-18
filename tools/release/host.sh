#!/usr/bin/env bash
# Reads and writes the download host for the release workflow: the R2 bucket
# behind it, through R2's S3 API with the AWS CLI, and its public address,
# $HOST, to see what people get from it.
#
#   host.sh check                  fail unless the R2 keys can read the bucket
#   host.sh stable FILE            write the version /stable names to FILE,
#                                  which stays empty while there is no /stable
#   host.sh publish VERSION DIR    put each file in DIR at /vVERSION/, for good
#   host.sh put NAME FILE          put FILE at /NAME, replacing what is there
#
# It reads the secrets R2_ENDPOINT, R2_BUCKET, R2_ACCESS_KEY_ID and
# R2_SECRET_ACCESS_KEY from the release or stable environment.
set -euo pipefail

die() {
  echo "::error::$*"
  exit 1
}

usage() {
  echo "usage: host.sh check | stable FILE | publish VERSION DIR | put NAME FILE" >&2
  exit 2
}

case "${1:-} $#" in
  "check 1" | "stable 2" | "publish 3" | "put 3") ;;
  *) usage ;;
esac

[ -n "${HOST:-}" ] || die "HOST, the download host's address, is not set"
for name in R2_ENDPOINT R2_BUCKET R2_ACCESS_KEY_ID R2_SECRET_ACCESS_KEY; do
  [ -n "${!name:-}" ] ||
    die "The secret $name is not set in this job's environment. docs/releasing.md says how to set the R2 secrets in the release and stable environments."
done
export AWS_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID" AWS_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY"
export AWS_ENDPOINT_URL="$R2_ENDPOINT" AWS_DEFAULT_REGION=auto AWS_PAGER=""
# AWS CLI 2.23 and later send a whole-object CRC32 with each upload, which R2
# does not take, and look for one in each answer. These keep checksums to the
# operations that require one.
export AWS_REQUEST_CHECKSUM_CALCULATION=when_required AWS_RESPONSE_CHECKSUM_VALIDATION=when_required

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# fetch writes the object $1 to the file $2 and succeeds, or fails when the
# bucket has no $1. Any other failure stops the script.
fetch() {
  local error
  if error=$(aws s3api get-object --bucket "$R2_BUCKET" --key "$1" "$2" 2>&1 >/dev/null); then
    return 0
  fi
  case "$error" in
    *"(NoSuchKey)"*) return 1 ;;
  esac
  die "could not read $1 from the bucket: $error"
}

# served waits until the host serves the bytes of the file $2 at /$1. The edge
# can keep a 404 for a few minutes when a file was asked for before it was
# uploaded, so it tries for six.
served() {
  local tries=24 got="$work/served" error
  while :; do
    if error=$(curl -fsS --max-time 120 -o "$got" "$HOST/$1" 2>&1); then
      cmp -s "$got" "$2" && break
      error="it serves other bytes"
    fi
    tries=$((tries - 1))
    [ "$tries" -gt 0 ] || die "$HOST/$1 does not serve what was uploaded there, six minutes after the upload: $error"
    sleep 15
  done
  echo "$HOST/$1 serves it."
}

type_of() {
  case "$1" in
    *.tar.gz) echo application/gzip ;;
    *.zip) echo application/zip ;;
    *.json) echo application/json ;;
    *.txt | *.sig) echo "text/plain; charset=utf-8" ;;
    *) echo application/octet-stream ;;
  esac
}

case "$1" in
  check)
    if ! error=$(aws s3api list-objects-v2 --bucket "$R2_BUCKET" --max-keys 1 2>&1 >/dev/null); then
      die "R2_ACCESS_KEY_ID and R2_SECRET_ACCESS_KEY cannot read the bucket R2_BUCKET at R2_ENDPOINT, so nothing was published: $error"
    fi
    echo "The R2 keys can read the bucket."
    ;;

  stable)
    : > "$2"
    if fetch stable "$work/stable"; then
      head -n 1 "$work/stable" | tr -d '\r\n' > "$2"
      echo "/stable names $(cat "$2")."
    else
      echo "There is no /stable yet."
    fi
    ;;

  # A version's files never change once published, and the bucket's lock on
  # the prefix v refuses to replace one. A file the bucket has already is
  # compared instead, and the same bytes, from an earlier run, are skipped.
  publish)
    version=$2
    dir=$3
    files=("$dir"/*)
    [ -f "${files[0]}" ] || die "$dir holds no files to publish"
    for file in "${files[@]}"; do
      key="v$version/$(basename "$file")"
      if fetch "$key" "$work/published"; then
        cmp -s "$file" "$work/published" ||
          die "$key is published already with other bytes than this run's, and a published file is never replaced. Publish a new version instead."
        echo "$key is published already, with the same bytes."
        continue
      fi
      aws s3api put-object --bucket "$R2_BUCKET" --key "$key" --body "$file" \
        --content-type "$(type_of "$file")" --cache-control "public, max-age=31536000, immutable" \
        --if-none-match "*" >/dev/null || die "could not upload $key"
      echo "Uploaded $key."
    done
    for file in "${files[@]}"; do
      served "v$version/$(basename "$file")" "$file"
    done
    ;;

  put)
    aws s3api put-object --bucket "$R2_BUCKET" --key "$2" --body "$3" \
      --content-type "text/plain; charset=utf-8" --cache-control "public, max-age=300" >/dev/null ||
      die "could not upload $2"
    echo "Uploaded $2."
    served "$2" "$3"
    ;;
esac
