#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC1091
source "${repo_root}/build/versions.env"

output="${1:-${repo_root}/komari}"
version="${VERSION:-dev}"
version_hash="${VERSION_HASH:-$(git -C "${repo_root}" rev-parse HEAD)}"
pgo_profile="${PGO_PROFILE:-${repo_root}/build/pgo/default.pgo}"
build_tags="${BUILD_TAGS:-}"

if [[ "${KOMARI_REQUIRE_EXACT_GO:-0}" == "1" ]]; then
  actual_go="$(go env GOVERSION)"
  if [[ "${actual_go}" != "go${GO_VERSION}" ]]; then
    echo "Go toolchain mismatch: expected go${GO_VERSION}, got ${actual_go}" >&2
    exit 1
  fi
fi
if [[ "${version_hash}" != "unknown" && ! "${version_hash}" =~ ^[0-9a-f]{40}$ ]]; then
  echo "VERSION_HASH must be a full lowercase Git commit or 'unknown'" >&2
  exit 1
fi
if [[ "${version}" =~ [[:space:]] ]]; then
  echo "VERSION must not contain whitespace" >&2
  exit 1
fi
if [[ "${pgo_profile}" != "off" && ! -s "${pgo_profile}" ]]; then
  echo "PGO profile not found or empty: ${pgo_profile}" >&2
  exit 1
fi

mkdir -p "$(dirname "${output}")"
ldflags="-s -w -buildid= -X github.com/komari-monitor/komari/utils.CurrentVersion=${version} -X github.com/komari-monitor/komari/utils.VersionHash=${version_hash}"
target_goos="${GOOS:-$(go env GOOS)}"
if [[ "${target_goos}" == "linux" ]]; then
  ldflags+=" -extldflags=-Wl,--build-id=none"
fi
args=(
  build
  -trimpath
  -buildvcs=false
  -mod=readonly
  "-pgo=${pgo_profile}"
  "-ldflags=${ldflags}"
  -o "${output}"
)
if [[ -n "${build_tags}" ]]; then
  args+=("-tags=${build_tags}")
fi
args+=(".")

export GOFLAGS="${GOFLAGS:-}"
export GIN_MODE="${GIN_MODE:-release}"
cd "${repo_root}"
go "${args[@]}"
