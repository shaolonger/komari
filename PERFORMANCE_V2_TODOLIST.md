# Komari Performance V2 Todo

Each checked item requires focused tests, the repository-wide relevant suite,
and a dedicated commit. Cross-repository items are not complete until their
golden/contract counterpart passes in every consumer.

## K2-0 Baseline and contract

- [x] **K2-001 Freeze the three-repository design and clean baseline**
- [x] **K2-002 Add Nano Ping replay, memory and row-scan performance fixtures**
- [x] **K2-003 Add a checked-in versioned RPC contract and discovery manifest**

## K2-1 Embedded runtime

- [x] **K2-101 Add cgroup-aware auto/nano/standard/scale runtime profiles**
- [x] **K2-102 Apply profile-sized SQLite pools/cache/mmap/temp settings**
- [x] **K2-103 CPU-budget startup-independent compaction and retention**
- [x] **K2-104 Harden installer systemd resources and in-process DB backup**

## K2-2 Ping data plane

- [x] **K2-201 Replace hot Ping ORM records with compact wire/storage rows**
- [x] **K2-202 Publish an immutable normalized Ping assignment index**
- [x] **K2-203 Coalesce Ping submissions into durable bounded micro-batches**
- [x] **K2-204 Add raw/1m/15m/1h Ping tiers and crash-safe retention**
- [x] **K2-205 Push Ping point/row budgets into storage query planning**
- [x] **K2-206 Add one set-based Ping overview/stats API**

## K2-3 Metric/RPC compatibility

- [x] **K2-301 Implement capability-gated admin/public metric definitions**
- [x] **K2-302 Implement metric query and Ping metric stats methods**
- [x] **K2-303 Implement checkpointed metric migration status/start/cancel**
- [x] **K2-304 Reject unknown settings and protect storage secrets**
- [x] **K2-305 Gate pinned official frontend builds on RPC compatibility**

## K2-4 Realtime and scale

- [x] **K2-401 Expose resumable sequence delta subscriptions over RPC2**
- [x] **K2-402 Integrate Agent v3 acknowledgements and Ping leases**
- [x] **K2-403 Preserve SQLite Lite and validate scale-store parity**

## K2-5 Acceptance and release

- [x] **K2-501 Real legacy/corrupt/orphan database migration matrix**
- [x] **K2-502 One-core Nano replay, race, fault and security gates**
- [x] **K2-503 24h/72h-equivalent flat-resource soak and browser contract gate**
- [x] **K2-504 Version, push, tag and publish the Komari release**
