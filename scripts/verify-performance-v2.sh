#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

go test ./...
go test -race ./api/client ./api/jsonRpc ./database/dbcore ./database/records ./database/tasks ./database/telemetrywriter ./internal/storage ./utils ./ws
go vet ./...

go test ./protocol/telemetryv2 -run '^$' -fuzz '^FuzzDecodeNeverPanics$' -fuzztime=2s
go test ./protocol/telemetryv3 -run '^$' -fuzz '^FuzzDecodeV3NeverPanics$' -fuzztime=2s
go test ./api/client -run '^$' -fuzz '^FuzzDecodeAuthenticatedJSONReport$' -fuzztime=2s
go test ./api/client -run '^$' -fuzz '^FuzzDecodeAgentMessage$' -fuzztime=2s

GOMAXPROCS=1 GOMEMLIMIT=128MiB go test ./database/tasks ./internal/replay \
  -run 'TestNanoPingFixtureHasExactAssignmentAndRowScanBudgets|TestRunHTTP|TestRunWebSocket' -count=1
go test ./database/dbcore \
  -run 'TestSchemaMigrationMatrixFromEveryPriorVersion|TestSchemaMigrationRollbackAndChecksumProtection|TestClientNotificationOrphanCleanupRollsBackWithMigrationFailure' \
  -count=1
go test ./api/admin ./api/client ./database/tasks \
  -run 'Reject|Security|Authorization|DeleteClient|NotAssigned|Orphan' -count=1

node --test scripts/verify-frontend-rpc-contract.test.mjs
go build -tags lite ./...

echo "Komari V2 one-core replay, race, fuzz, fault and security gates passed"
