# PGO profile provenance

`default.pgo` is generated from the pinned Go toolchain and the representative
authenticated JSON v1, WebSocket JSON v1, and Telemetry v2 decode benchmarks:

```bash
GOTOOLCHAIN=go1.25.12 PGO_BENCHTIME=5s ./scripts/generate-pgo.sh
```

The profile is validated with `go tool pprof` before installation. It is a
versioned release input; do not regenerate it implicitly in a release workflow.
See [`../../BUILD_ENGINEERING.md`](../../BUILD_ENGINEERING.md).
