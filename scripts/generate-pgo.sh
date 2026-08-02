#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output="${1:-${repo_root}/build/pgo/default.pgo}"
benchtime="${PGO_BENCHTIME:-5s}"
temporary_profile="$(mktemp -t komari-pgo.XXXXXX)"

cleanup() {
  if [[ -f "${temporary_profile}" ]]; then
    rm -f -- "${temporary_profile}"
  fi
}
trap cleanup EXIT

mkdir -p "$(dirname "${output}")"
cd "${repo_root}"
go test ./api/client \
  -run='^$' \
  -bench='^BenchmarkDecode(AuthenticatedJSONV1|WebSocketJSONV1|TelemetryV2)$' \
  -benchtime="${benchtime}" \
  -count=1 \
  -cpuprofile="${temporary_profile}"
go tool pprof -top "${temporary_profile}" >/dev/null
install -m 0644 "${temporary_profile}" "${output}"
printf 'wrote validated PGO profile: %s\n' "${output}"
