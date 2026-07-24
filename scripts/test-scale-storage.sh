#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
compose_file="${repo_dir}/deploy/performance/docker-compose.scale-test.yml"
project_name="komari-scale-storage-test"
cert_dir="$(mktemp -d "${TMPDIR:-/tmp}/komari-scale-storage-certs.XXXXXX")"
export KOMARI_SCALE_TEST_CERT_DIR="${cert_dir}"

cleanup() {
  docker compose --project-name "${project_name}" --file "${compose_file}" down --volumes --remove-orphans
  rm -r "${cert_dir}"
}
trap cleanup EXIT

openssl req -x509 -newkey rsa:2048 -sha256 -nodes -days 1 \
  -subj "/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" \
  -keyout "${cert_dir}/server.key" \
  -out "${cert_dir}/server.crt" >/dev/null 2>&1
chmod 0755 "${cert_dir}"
chmod 0644 "${cert_dir}/server.key" "${cert_dir}/server.crt"

docker compose --project-name "${project_name}" --file "${compose_file}" up --detach --wait
# The ClickHouse image briefly starts a bootstrap server before exec'ing the
# final process. Compose can observe that temporary process as healthy.
sleep 5

export KOMARI_TEST_POSTGRES_URL="postgres://komari:komari_test_only@127.0.0.1:55432/komari_test?sslmode=disable"
export KOMARI_TEST_POSTGRES_TLS_URL="postgres://komari:komari_test_only@localhost:55432/komari_test?sslmode=verify-full&sslrootcert=${cert_dir}/server.crt"
export KOMARI_TEST_CLICKHOUSE_ADDR="127.0.0.1:59000"
export KOMARI_TEST_CLICKHOUSE_TLS_ADDR="localhost:59440"
export KOMARI_TEST_TLS_CA="${cert_dir}/server.crt"
export KOMARI_TEST_CLICKHOUSE_CONTAINER="${project_name}-clickhouse-1"

cd "${repo_dir}"
go test -tags="scale integration" -count=1 -timeout=5m \
  ./database/controlstore ./database/telemetrystore
