#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC1091
source "${repo_root}/build/versions.env"
temporary_root="$(mktemp -d -t komari-frontend-repro.XXXXXX)"
source_dir="${temporary_root}/source"

cleanup() {
  rm -rf -- "${temporary_root}"
}
trap cleanup EXIT

if [[ "${KOMARI_REQUIRE_EXACT_NODE:-0}" == "1" && "$(node --version)" != "v${NODE_VERSION}" ]]; then
  echo "Node.js mismatch: expected v${NODE_VERSION}, got $(node --version)" >&2
  exit 1
fi

git init --quiet "${source_dir}"
git -C "${source_dir}" remote add origin "${FRONTEND_REPOSITORY}"
git -C "${source_dir}" fetch --quiet --depth=1 --no-tags origin "${FRONTEND_COMMIT}"
git -C "${source_dir}" checkout --quiet --detach FETCH_HEAD

FRONTEND_SOURCE_DIR="${source_dir}" \
  "${repo_root}/scripts/build-frontend.sh" "${temporary_root}/online"
FRONTEND_SOURCE_DIR="${source_dir}" KOMARI_FRONTEND_OFFLINE=1 \
  "${repo_root}/scripts/build-frontend.sh" "${temporary_root}/offline"

(
  cd "${temporary_root}/online"
  find . -type f -print0 | sort -z | xargs -0 shasum -a 256
) >"${temporary_root}/online.sha256"
(
  cd "${temporary_root}/offline"
  find . -type f -print0 | sort -z | xargs -0 shasum -a 256
) >"${temporary_root}/offline.sha256"
cmp "${temporary_root}/online.sha256" "${temporary_root}/offline.sha256"
