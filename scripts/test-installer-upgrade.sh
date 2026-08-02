#!/usr/bin/env bash
# shellcheck disable=SC2030,SC2031,SC2034,SC2329

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export KOMARI_INSTALLER_LIBRARY_ONLY=1
# shellcheck source=../install-komari.sh
source "${repo_root}/install-komari.sh"

temporary_root="$(mktemp -d)"
trap 'rm -rf "${temporary_root}"' EXIT

printf '%s\n' "komari-checksum-probe" >"${temporary_root}/checksum-probe"
[ "$(sha256_file "${temporary_root}/checksum-probe")" = \
  "2f7fa6cd2825756756022595f60033288f2734ed1bb552d4081424f190046d30" ]

mock_service_state="stopped"
mock_fail_new_binary=0
mock_start_count=0

log_info() { :; }
log_success() { :; }
log_error() { :; }
log_step() { :; }
check_systemd() { return 0; }
detect_arch() { printf '%s\n' "amd64"; }
get_service_port() { return 1; }
journalctl() { :; }
sleep() { :; }
chown() { :; }

test_download_verification() (
    BINARY_PATH="${temporary_root}/download/komari"
    mkdir -p "$(dirname "$BINARY_PATH")"
    printf '%s\n' "old-binary" >"$BINARY_PATH"
    mock_invalid_checksum=0

    curl() {
        local output_path="$3"
        local url="$4"
        if [[ "$url" == *.sha256 ]]; then
            local checksum
            checksum=$(sha256_file "${BINARY_PATH}.download.$$")
            if [ "$mock_invalid_checksum" -eq 1 ]; then
                checksum="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
            fi
            printf '%s  %s\n' "$checksum" "komari-linux-amd64" >"$output_path"
        else
            printf '%s\n' "verified-new-binary" >"$output_path"
        fi
    }

    download_binary "amd64"
    [ "$(sed -n '1p' "$BINARY_PATH")" = "verified-new-binary" ]

    printf '%s\n' "old-binary" >"$BINARY_PATH"
    mock_invalid_checksum=1
    if download_binary "amd64"; then
        printf '%s\n' "download accepted an invalid checksum" >&2
        exit 1
    fi
    [ "$(sed -n '1p' "$BINARY_PATH")" = "old-binary" ]
)
test_download_verification

systemctl() {
    local action="${1:-}"
    case "$action" in
        stop)
            mock_service_state="stopped"
            ;;
        start)
            mock_start_count=$((mock_start_count + 1))
            if [ "$mock_fail_new_binary" -eq 1 ] &&
               [ "$(sed -n '1p' "$BINARY_PATH")" = "new-binary" ]; then
                mock_service_state="failed"
                return 1
            fi
            mock_service_state="active"
            ;;
        reset-failed)
            if [ "$mock_service_state" = "failed" ]; then
                mock_service_state="stopped"
            fi
            ;;
        daemon-reload)
            :
            ;;
        is-active)
            [ "$mock_service_state" = "active" ]
            ;;
        is-failed)
            [ "$mock_service_state" = "failed" ]
            ;;
        status)
            :
            ;;
        *)
            printf 'unexpected systemctl action: %s\n' "$action" >&2
            return 1
            ;;
    esac
}

download_binary() {
    printf '%s\n' "new-binary" >"$BINARY_PATH"
}

configure_fixture() {
    local name="$1"
    INSTALL_DIR="${temporary_root}/${name}/opt/komari"
    DATA_DIR="$INSTALL_DIR"
    BINARY_PATH="$INSTALL_DIR/komari"
    SYSTEMD_UNIT_DIR="${temporary_root}/${name}/systemd"
    SERVICE_READY_TIMEOUT_SECONDS=3
    SERVICE_ACTIVE_STABILITY_SECONDS=2
    UPGRADE_BACKUP_PATH=""
    mock_service_state="active"
    mock_start_count=0

    mkdir -p "$DATA_DIR/data"
    mkdir -p "$SYSTEMD_UNIT_DIR"
    printf '%s\n' "old-binary" >"$BINARY_PATH"
    printf '%s\n' "database-state" >"$DATA_DIR/data/komari.db"
    chmod 0755 "$BINARY_PATH"
    cat >"${SYSTEMD_UNIT_DIR}/${SERVICE_NAME}.service" <<EOF
[Service]
ExecStart=${BINARY_PATH} server -l 0.0.0.0:25774
WorkingDirectory=${DATA_DIR}
EOF
}

assert_upgrade_backup() {
    [ -f "$UPGRADE_BACKUP_PATH/komari" ]
    [ "$(sed -n '1p' "$UPGRADE_BACKUP_PATH/komari")" = "old-binary" ]
    [ -f "$UPGRADE_BACKUP_PATH/data/komari.db" ]
    [ "$(sed -n '1p' "$UPGRADE_BACKUP_PATH/data/komari.db")" = "database-state" ]
}

assert_systemd_hardening() {
    local dropin="${SYSTEMD_UNIT_DIR}/${SERVICE_NAME}.service.d/20-performance-security.conf"
    [ -f "$dropin" ]
    grep -qx 'NoNewPrivileges=true' "$dropin"
    grep -qx 'ProtectSystem=strict' "$dropin"
    grep -qx "ReadWritePaths=${INSTALL_DIR}" "$dropin"
    grep -qx 'MemoryHigh=75%' "$dropin"
    grep -qx 'MemoryMax=90%' "$dropin"
}

configure_fixture "success"
mock_fail_new_binary=0
upgrade_komari
[ "$(sed -n '1p' "$BINARY_PATH")" = "new-binary" ]
[ "$mock_service_state" = "active" ]
assert_upgrade_backup
assert_systemd_hardening

configure_fixture "rollback"
mock_fail_new_binary=1
if upgrade_komari; then
    printf '%s\n' "upgrade unexpectedly succeeded with a failing new binary" >&2
    exit 1
fi
[ "$(sed -n '1p' "$BINARY_PATH")" = "old-binary" ]
[ "$mock_service_state" = "active" ]
[ "$mock_start_count" -ge 2 ]
assert_upgrade_backup
assert_systemd_hardening

configure_fixture "custom-path"
custom_working_directory="${temporary_root}/custom-path/state"
custom_database_directory="${temporary_root}/custom-path/external-db"
mkdir -p "$custom_working_directory" "$custom_database_directory"
printf '%s\n' "custom-database-state" >"$custom_database_directory/custom.db"
cat >"${SYSTEMD_UNIT_DIR}/${SERVICE_NAME}.service" <<EOF
[Service]
ExecStart=${BINARY_PATH} --database ${custom_database_directory}/custom.db server -l 0.0.0.0:25774
WorkingDirectory=${custom_working_directory}
EOF
create_upgrade_backup
[ "$(sed -n '1p' "$UPGRADE_BACKUP_PATH/data/komari.db")" = "custom-database-state" ]
ensure_systemd_hardening
custom_dropin="${SYSTEMD_UNIT_DIR}/${SERVICE_NAME}.service.d/20-performance-security.conf"
grep -qx "ReadWritePaths=${custom_working_directory}" "$custom_dropin"
grep -qx "ReadWritePaths=${custom_database_directory}" "$custom_dropin"

configure_fixture "environment-path"
environment_database_directory="${temporary_root}/environment-path/database"
mkdir -p "$environment_database_directory"
printf '%s\n' "environment-database-state" >"$environment_database_directory/environment.db"
cat >"${SYSTEMD_UNIT_DIR}/${SERVICE_NAME}.service" <<EOF
[Service]
Environment="KOMARI_DB_FILE=${environment_database_directory}/environment.db"
ExecStart=${BINARY_PATH} server -l 0.0.0.0:25774
WorkingDirectory=${DATA_DIR}
EOF
create_upgrade_backup
[ "$(sed -n '1p' "$UPGRADE_BACKUP_PATH/data/komari.db")" = "environment-database-state" ]

printf '%s\n' "installer upgrade success/backup/rollback tests passed"
