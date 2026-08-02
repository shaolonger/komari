#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC1091
source "${repo_root}/build/versions.env"

destination="${1:?usage: install-zig.sh DESTINATION}"
archive="$(mktemp -t komari-zig.XXXXXX.tar.xz)"
extract_root="$(mktemp -d -t komari-zig-extract.XXXXXX)"

cleanup() {
  rm -f -- "${archive}"
  rm -rf -- "${extract_root}"
}
trap cleanup EXIT

url="https://ziglang.org/download/${ZIG_VERSION}/zig-x86_64-linux-${ZIG_VERSION}.tar.xz"
curl --fail --location --proto '=https' --tlsv1.2 --output "${archive}" "${url}"
printf '%s  %s\n' "${ZIG_LINUX_X86_64_SHA256}" "${archive}" | sha256sum --check --status
tar -xJf "${archive}" -C "${extract_root}"
mkdir -p "${destination}"
find "${destination}" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
cp -R "${extract_root}/zig-x86_64-linux-${ZIG_VERSION}/." "${destination}/"
"${destination}/zig" version
