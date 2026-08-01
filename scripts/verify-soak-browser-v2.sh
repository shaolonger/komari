#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output_root="$(mktemp -d "${TMPDIR:-/tmp}/komari-browser-v2.XXXXXX")"
cleanup() { rm -rf -- "${output_root}"; }
trap cleanup EXIT HUP INT TERM

cd "${repo_root}"
GOMAXPROCS=1 GOMEMLIMIT=128MiB go test ./internal/telemetry \
  -run '^TestSeventyTwoHourEquivalentTelemetrySoakStaysFlat$' -count=1

"${repo_root}/scripts/build-frontend.sh" "${output_root}/theme"
test -s "${output_root}/theme/dist/index.html"
grep -q '^rpc_contract=komari.rpc.v2.3$' "${output_root}/theme/BUILD_PROVENANCE"

bundle_bytes="$(du -sk "${output_root}/theme/dist" | awk '{print $1 * 1024}')"
if (( bundle_bytes > 8 * 1024 * 1024 )); then
  echo "pinned frontend bundle exceeds 8 MiB: ${bundle_bytes}" >&2
  exit 1
fi

echo "72h-equivalent flat-resource soak and pinned browser contract gate passed"
