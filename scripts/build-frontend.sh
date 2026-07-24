#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC1091
source "${repo_root}/build/versions.env"

output_dir="${1:-${repo_root}/public/defaultTheme}"
verified_source="${FRONTEND_SOURCE_DIR:-}"
temporary_root="$(mktemp -d -t komari-frontend.XXXXXX)"

cleanup() {
  if [[ -d "${temporary_root}" ]]; then
    rm -rf -- "${temporary_root}"
  fi
}
trap cleanup EXIT

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

if [[ -z "${verified_source}" ]]; then
  if [[ "${KOMARI_FRONTEND_OFFLINE:-0}" == "1" ]]; then
    echo "KOMARI_FRONTEND_OFFLINE=1 requires FRONTEND_SOURCE_DIR at the pinned commit" >&2
    exit 1
  fi
  verified_source="${temporary_root}/checkout"
  git init --quiet "${verified_source}"
  git -C "${verified_source}" remote add origin "${FRONTEND_REPOSITORY}"
  git -C "${verified_source}" fetch --quiet --depth=1 --no-tags origin "${FRONTEND_COMMIT}"
  git -C "${verified_source}" checkout --quiet --detach FETCH_HEAD
fi

actual_commit="$(git -C "${verified_source}" rev-parse HEAD)"
if [[ "${actual_commit}" != "${FRONTEND_COMMIT}" ]]; then
  echo "frontend commit mismatch: expected ${FRONTEND_COMMIT}, got ${actual_commit}" >&2
  exit 1
fi

actual_source_epoch="$(git -C "${verified_source}" show -s --format=%ct HEAD)"
if [[ "${actual_source_epoch}" != "${FRONTEND_SOURCE_DATE_EPOCH}" ]]; then
  echo "frontend source epoch mismatch: expected ${FRONTEND_SOURCE_DATE_EPOCH}, got ${actual_source_epoch}" >&2
  exit 1
fi

source_dir="${temporary_root}/source"
mkdir -p "${source_dir}"
git -C "${verified_source}" archive --format=tar HEAD | tar -xf - -C "${source_dir}"

lockfile="${source_dir}/package-lock.json"
if [[ ! -f "${lockfile}" ]]; then
  echo "pinned frontend is missing package-lock.json" >&2
  exit 1
fi
actual_lock_sha="$(sha256_file "${lockfile}")"
if [[ "${actual_lock_sha}" != "${FRONTEND_LOCK_SHA256}" ]]; then
  echo "frontend lockfile mismatch: expected ${FRONTEND_LOCK_SHA256}, got ${actual_lock_sha}" >&2
  exit 1
fi

# The upstream commit predates fixes for high-severity build-chain advisories.
# Apply the reviewed, repository-pinned lock-only update and verify its complete
# resulting digest before npm sees it.
git -C "${source_dir}" apply "${repo_root}/build/frontend-build-security.patch"
git -C "${source_dir}" apply "${repo_root}/build/frontend-security.patch"
effective_lock_sha="$(sha256_file "${lockfile}")"
if [[ "${effective_lock_sha}" != "${FRONTEND_EFFECTIVE_LOCK_SHA256}" ]]; then
  echo "effective frontend lockfile mismatch: expected ${FRONTEND_EFFECTIVE_LOCK_SHA256}, got ${effective_lock_sha}" >&2
  exit 1
fi

# The pinned upstream config embeds wall-clock time. Replace only that exact
# expression with the pinned commit epoch so chunk names and PWA manifests are
# reproducible. The exact-match guard fails closed if upstream changes.
node - "${source_dir}/vite.config.ts" "${FRONTEND_SOURCE_DATE_EPOCH}" <<'NODE'
const fs = require("node:fs");
const [path, epoch] = process.argv.slice(2);
const source = fs.readFileSync(path, "utf8");
const needle = "const buildTime = new Date().toISOString();";
const replacement =
  `const buildTime = new Date(${JSON.stringify(Number(epoch))} * 1000).toISOString();`;
if (source.split(needle).length !== 2) {
  throw new Error("pinned frontend build-time expression was not found exactly once");
}
fs.writeFileSync(path, source.replace(needle, replacement));
NODE

npm_args=(ci --ignore-scripts --no-audit --no-fund)
if [[ "${KOMARI_FRONTEND_OFFLINE:-0}" == "1" ]]; then
  npm_args+=(--offline)
fi
(
  cd "${source_dir}"
  npm "${npm_args[@]}"
  if [[ "${KOMARI_FRONTEND_OFFLINE:-0}" != "1" &&
        "${KOMARI_FRONTEND_AUDIT:-1}" == "1" ]]; then
    npm audit --audit-level=high
  fi
  npm run build
)

after_lock_sha="$(sha256_file "${lockfile}")"
if [[ "${after_lock_sha}" != "${FRONTEND_EFFECTIVE_LOCK_SHA256}" ]]; then
  echo "npm changed the pinned lockfile" >&2
  exit 1
fi

mkdir -p "${output_dir}/dist"
find "${output_dir}/dist" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
cp -R "${source_dir}/dist/." "${output_dir}/dist/"
install -m 0644 "${source_dir}/komari-theme.json" "${output_dir}/komari-theme.json"

if [[ -f "${source_dir}/preview.png" ]]; then
  install -m 0644 "${source_dir}/preview.png" "${output_dir}/preview.png"
fi
if [[ -f "${source_dir}/perview.png" ]]; then
  install -m 0644 "${source_dir}/perview.png" "${output_dir}/perview.png"
fi
if [[ -f "${output_dir}/preview.png" && ! -f "${output_dir}/perview.png" ]]; then
  install -m 0644 "${output_dir}/preview.png" "${output_dir}/perview.png"
fi
if [[ -f "${output_dir}/perview.png" && ! -f "${output_dir}/preview.png" ]]; then
  install -m 0644 "${output_dir}/perview.png" "${output_dir}/preview.png"
fi

printf 'frontend_commit=%s\nfrontend_lock_sha256=%s\n' \
  "${FRONTEND_COMMIT}" "${FRONTEND_EFFECTIVE_LOCK_SHA256}" >"${output_dir}/BUILD_PROVENANCE"
printf 'frontend_source_date_epoch=%s\n' "${FRONTEND_SOURCE_DATE_EPOCH}" \
  >>"${output_dir}/BUILD_PROVENANCE"
