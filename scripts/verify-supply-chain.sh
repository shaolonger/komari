#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC1091
source "${repo_root}/build/versions.env"
cd "${repo_root}"

go_directive="$(awk '$1 == "go" { print $2; exit }' go.mod)"
if [[ "${go_directive}" != "${GO_VERSION}" ]]; then
  echo "go.mod (${go_directive}) and pinned Go (${GO_VERSION}) differ" >&2
  exit 1
fi
if [[ ! "${FRONTEND_COMMIT}" =~ ^[0-9a-f]{40}$ ||
      ! "${FRONTEND_LOCK_SHA256}" =~ ^[0-9a-f]{64}$ ||
      ! "${FRONTEND_EFFECTIVE_LOCK_SHA256}" =~ ^[0-9a-f]{64}$ ||
      ! "${FRONTEND_SOURCE_DATE_EPOCH}" =~ ^[0-9]{10}$ ||
      ! "${ZIG_LINUX_X86_64_SHA256}" =~ ^[0-9a-f]{64}$ ]]; then
  echo "one or more build inputs are not pinned by a full digest" >&2
  exit 1
fi
if ! grep -Eq '^FROM [^[:space:]]+@sha256:[0-9a-f]{64}$' Dockerfile; then
  echo "Dockerfile base image is not pinned by digest" >&2
  exit 1
fi
if [[ ! -s build/pgo/default.pgo ]]; then
  echo "release PGO profile is missing" >&2
  exit 1
fi

unpinned_actions="$(
  awk '/^[[:space:]]*uses:/ { print $2 }' .github/workflows/*.yml |
    grep -Ev '@[0-9a-f]{40}([[:space:]]|$)' || true
)"
if [[ -n "${unpinned_actions}" ]]; then
  printf 'GitHub Actions are not commit-pinned:\n%s\n' "${unpinned_actions}" >&2
  exit 1
fi
if grep -REn 'npm install|git clone .*komari-web|go-version:[[:space:]]*"1\\.23"|node-version:[[:space:]]*"23"' \
  .github/workflows; then
  echo "mutable or obsolete build command remains in workflows" >&2
  exit 1
fi
if grep -REn 'cat[[:space:]]+build/versions\\.env[[:space:]]*>>[[:space:]]*"\\$GITHUB_ENV"' \
  .github/workflows; then
  echo "workflow exports comments or blank lines to GITHUB_ENV" >&2
  exit 1
fi
performance_baseline="$(tr -d '[:space:]' < build/performance-baseline.txt)"
if [[ ! "${performance_baseline}" =~ ^[0-9a-f]{40}$ ]] ||
   ! git cat-file -e "${performance_baseline}^{commit}" 2>/dev/null; then
  echo "performance bootstrap baseline is not a reachable full commit" >&2
  exit 1
fi

GOTOOLCHAIN="go${GO_VERSION}" go mod verify
GOTOOLCHAIN="go${GO_VERSION}" go list -mod=readonly -m all >/dev/null
GOTOOLCHAIN="go${GO_VERSION}" \
  go run "github.com/rhysd/actionlint/cmd/actionlint@${ACTIONLINT_VERSION}" -color

if [[ "${KOMARI_RUN_VULN_CHECK:-0}" == "1" ]]; then
  GOTOOLCHAIN="go${GO_VERSION}" \
    go run "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" ./...
fi
