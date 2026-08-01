# Komari Performance V2

Status: complete; v1.4.0 release candidate

This document defines the second performance program shared by Komari,
komari-agent and LuminaPlus. The first performance program remains the baseline;
V2 addresses the Ping data plane, the frontend/backend contract and resource
adaptation that were exposed by a 1-core/2.5-GiB installation with 30 nodes and
roughly 105 Ping assignments.

## Non-negotiable properties

- Authentication, authorization, credential invalidation and Ping SSRF controls
  must not be weakened for throughput.
- Every queue, frame, query, migration page, cache and retry loop has a hard
  bound and a cancellation path.
- SQLite remains the default embedded store. PostgreSQL/ClickHouse remain
  explicit scale-mode dependencies.
- JSON telemetry v1 and binary telemetry v2 remain compatible while v3 is
  negotiated explicitly.
- Large data rewrites never run in the startup-critical migration transaction.
- The official frontend, LuminaPlus and the server consume one versioned RPC
  contract; unsupported settings cannot be silently persisted.

## Target data path

1. An authenticated Agent report updates one immutable live snapshot.
2. Ping results are checked against an immutable assignment revision, avoiding
   a database read per sample.
3. Small submissions are coalesced into a bounded micro-batch and committed by
   the existing single SQLite writer.
4. Ping raw samples feed 1-minute, 15-minute and 1-hour mergeable summaries.
5. Query planning chooses the coarsest loss-safe tier before scanning storage.
6. Dashboard clients receive a snapshot followed by sequence-numbered deltas;
   history and Ping overview requests are set queries, never per-node/task N+1.

## Embedded resource profiles

`KOMARI_PROFILE=auto|nano|standard|scale` controls only performance/resource
parameters, never security policy. Auto uses cgroup v2/v1 limits where present.
Nano (one CPU or no more than 3 GiB) uses 1-2 SQLite readers, a small page cache,
file-backed temporary storage, CPU-budgeted compaction and a Go soft memory
limit. Scale preserves the external-store build and fail-closed TLS contract.

## Storage evolution

Control metadata and telemetry receive separate SQLite handles/files in a
backwards-compatible transition. Ping task membership is normalized into
`ping_task_clients`; hot samples use compact integer node identifiers and never
materialize ORM relationship fields. Old tables remain readable during a
checkpointed shadow migration. Counts, boundaries and aggregate values are
compared before the read switch, and rollback only changes the read generation.

## RPC and metric contract

The checked-in contract declares method name, namespace, permission, params,
result, limits, minimum version and capability. Go registration and TypeScript
clients are generated/verified from the same source. `rpc.discover` returns the
API version, contract hash and capabilities. Built-in metrics continue to use
efficient wide storage; Ping keeps a specialized table; only extensible custom
metrics use a generic representation.

## Release gates

The primary Nano scenario is 30 nodes and 105 Ping assignments on one CPU.
At 60-second Ping intervals the cgroup memory target is 128 MiB p95 and CPU is
5% average; at one-second Ping intervals the release ceiling is 256 MiB p95 and
20% average CPU. Ping overview p99 must stay below 100 ms without scanning more
rows than its point budget. A 24-hour deterministic soak is required in CI and
a 72-hour canary-equivalent accelerated soak must demonstrate a flat heap.

Cross-repository release requires unit, race, fuzz, migration, fault-injection,
RPC contract, browser, bundle, replay and supply-chain gates to pass at the same
commits recorded in the release notes.
