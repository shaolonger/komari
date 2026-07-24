#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
temporary_root="$(mktemp -d -t komari-repro.XXXXXX)"
version_hash="${KOMARI_REPRO_VERSION_HASH:-0000000000000000000000000000000000000000}"

cleanup() {
  rm -rf -- "${temporary_root}"
}
trap cleanup EXIT

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

export VERSION="reproducible-test"
export VERSION_HASH="${version_hash}"
export SOURCE_DATE_EPOCH="$(git -C "${repo_root}" show -s --format=%ct HEAD)"
export CGO_ENABLED="${CGO_ENABLED:-1}"

PGO_PROFILE="${repo_root}/build/pgo/default.pgo" \
  "${repo_root}/scripts/build-release.sh" "${temporary_root}/pgo-a/komari"
PGO_PROFILE="${repo_root}/build/pgo/default.pgo" \
  "${repo_root}/scripts/build-release.sh" "${temporary_root}/pgo-b/komari"

hash_a="$(sha256_file "${temporary_root}/pgo-a/komari")"
hash_b="$(sha256_file "${temporary_root}/pgo-b/komari")"
if [[ "${hash_a}" != "${hash_b}" ]]; then
  echo "repeated PGO builds differ: ${hash_a} != ${hash_b}" >&2
  exit 1
fi

go version -m "${temporary_root}/pgo-a/komari" | tail -n +2 >"${temporary_root}/metadata-a"
go version -m "${temporary_root}/pgo-b/komari" | tail -n +2 >"${temporary_root}/metadata-b"
cmp "${temporary_root}/metadata-a" "${temporary_root}/metadata-b"
if grep -F "${repo_root}" "${temporary_root}/metadata-a"; then
  echo "absolute source path leaked into Go build metadata" >&2
  exit 1
fi
if grep -E $'^[[:space:]]*build[[:space:]]+vcs\\.' "${temporary_root}/metadata-a"; then
  echo "mutable VCS metadata remained in release binary" >&2
  exit 1
fi
if [[ -n "$(go tool buildid "${temporary_root}/pgo-a/komari")" ]]; then
  echo "Go build ID was not removed" >&2
  exit 1
fi

PGO_PROFILE=off "${repo_root}/scripts/build-release.sh" "${temporary_root}/no-pgo/komari"
"${temporary_root}/pgo-a/komari" --help >/dev/null
"${temporary_root}/no-pgo/komari" --help >/dev/null
printf 'reproducible_sha256=%s\n' "${hash_a}"
