#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
benchmark_root="${KOMARI_BENCH_ROOT:-${repo_root}}"
output="${1:?usage: collect-performance-benchmarks.sh OUTPUT}"
benchtime="${KOMARI_BENCHTIME:-250ms}"
count="${KOMARI_BENCH_COUNT:-5}"
pattern='^Benchmark(DecodeTelemetryV2|ShardedMinuteAccumulator|CacheHit512KiB|CacheGet|StableNextRun10000Tasks|ManifestAssetLookup|DashboardSingleDelta10000Nodes)$'

cd "${benchmark_root}"
go test \
  ./api/client \
  ./internal/telemetry \
  ./internal/historycache \
  ./internal/credentialcache \
  ./internal/scheduler \
  ./public \
  ./ws \
  -run='^$' \
  -bench="${pattern}" \
  -benchmem \
  -benchtime="${benchtime}" \
  -count="${count}" | tee "${output}"
