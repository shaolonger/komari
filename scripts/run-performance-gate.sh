#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
baseline_root="${1:?usage: run-performance-gate.sh BASELINE_CHECKOUT}"
temporary_root="$(mktemp -d -t komari-benchgate.XXXXXX)"

cleanup() {
  rm -rf -- "${temporary_root}"
}
trap cleanup EXIT

if [[ ! -f "${baseline_root}/go.mod" ]]; then
  echo "baseline checkout is not a Komari source tree: ${baseline_root}" >&2
  exit 1
fi

KOMARI_BENCH_ROOT="${baseline_root}" \
  "${repo_root}/scripts/collect-performance-benchmarks.sh" "${temporary_root}/baseline.txt"
KOMARI_BENCH_ROOT="${repo_root}" \
  "${repo_root}/scripts/collect-performance-benchmarks.sh" "${temporary_root}/candidate.txt"

cd "${repo_root}"
go run ./tools/benchguard \
  -baseline "${temporary_root}/baseline.txt" \
  -candidate "${temporary_root}/candidate.txt" \
  -time-limit "${KOMARI_BENCH_TIME_LIMIT:-0.20}" \
  -memory-limit "${KOMARI_BENCH_MEMORY_LIMIT:-0.02}" \
  -alloc-limit "${KOMARI_BENCH_ALLOC_LIMIT:-0}" \
  -min-samples "${KOMARI_BENCH_COUNT:-5}"
