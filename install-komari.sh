#!/bin/bash

# Color definitions for terminal output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m' # No Color

# Logging functions
log_info() {
    echo -e "$1"
}

log_success() {
    echo -e "${GREEN}$1${NC}"
}

log_error() {
    echo -e "${RED}$1${NC}"
}

log_step() {
    echo -e "${YELLOW}$1${NC}"
}

RELEASE_REPO="${KOMARI_RELEASE_REPO:-shaolonger/komari}"
SYSTEMD_UNIT_DIR="${KOMARI_SYSTEMD_UNIT_DIR:-/etc/systemd/system}"

# Global variables
INSTALL_DIR="/opt/komari"
DATA_DIR="/opt/komari"
SERVICE_NAME="komari"
BINARY_PATH="$INSTALL_DIR/komari"
DEFAULT_PORT="25774"
LISTEN_PORT=""
SERVICE_READY_TIMEOUT_SECONDS="${KOMARI_SERVICE_READY_TIMEOUT_SECONDS:-120}"
SERVICE_ACTIVE_STABILITY_SECONDS="${KOMARI_SERVICE_ACTIVE_STABILITY_SECONDS:-5}"
UPGRADE_BACKUP_PATH=""

sha256_file() {
    local path="$1"
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$path" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$path" | awk '{print $1}'
    elif command -v openssl >/dev/null 2>&1; then
        openssl dgst -sha256 "$path" | awk '{print $NF}'
    else
        log_error "未找到 SHA-256 校验工具（sha256sum/shasum/openssl）"
        return 1
    fi
}

download_binary() {
    local arch="$1"
    local file_name="komari-linux-${arch}"
    local download_url="https://github.com/${RELEASE_REPO}/releases/latest/download/${file_name}"
    local tmp_path="${BINARY_PATH}.download.$$"
    local checksum_path="${tmp_path}.sha256"
    local expected_checksum=""
    local actual_checksum=""

    log_step "下载 Komari 二进制文件..."
    log_info "URL: $download_url"

    rm -f "$tmp_path" "$checksum_path"
    if ! curl -fL -o "$tmp_path" "$download_url"; then
        rm -f "$tmp_path" "$checksum_path"
        log_error "下载失败。请确认 ${RELEASE_REPO} 的 release 资源中存在 ${file_name}。"
        return 1
    fi
    if ! curl -fL -o "$checksum_path" "${download_url}.sha256"; then
        rm -f "$tmp_path" "$checksum_path"
        log_error "无法下载 ${file_name} 的 SHA-256 校验文件，已拒绝安装未经验证的二进制文件。"
        return 1
    fi

    expected_checksum=$(awk 'NR == 1 {print $1}' "$checksum_path")
    if [[ ! "$expected_checksum" =~ ^[0-9a-fA-F]{64}$ ]]; then
        rm -f "$tmp_path" "$checksum_path"
        log_error "发布文件中的 SHA-256 校验值格式无效。"
        return 1
    fi
    if ! actual_checksum=$(sha256_file "$tmp_path"); then
        rm -f "$tmp_path" "$checksum_path"
        return 1
    fi
    actual_checksum=$(printf '%s' "$actual_checksum" | tr '[:upper:]' '[:lower:]')
    expected_checksum=$(printf '%s' "$expected_checksum" | tr '[:upper:]' '[:lower:]')
    if [ "$actual_checksum" != "$expected_checksum" ]; then
        rm -f "$tmp_path" "$checksum_path"
        log_error "二进制文件 SHA-256 校验失败，已停止安装。"
        return 1
    fi

    if ! mv -f "$tmp_path" "$BINARY_PATH"; then
        rm -f "$tmp_path" "$checksum_path"
        log_error "无法把已验证的二进制文件安装到 $BINARY_PATH"
        return 1
    fi
    rm -f "$checksum_path"
    log_success "二进制文件 SHA-256 校验通过"
}

# Show banner
show_banner() {
    clear
    echo "=============================================================="
    echo "            Komari Monitoring System Installer"
    echo "       https://github.com/shaolonger/komari"
    echo "=============================================================="
    echo
}

# Check if running as root
check_root() {
    if [ "$EUID" -ne 0 ]; then
        log_error "请使用 root 权限运行此脚本"
        exit 1
    fi
}

# Check for systemd
check_systemd() {
    if ! command -v systemctl >/dev/null 2>&1; then
        return 1
    else
        return 0
    fi
}

get_service_port() {
    local service_file="${SYSTEMD_UNIT_DIR}/${SERVICE_NAME}.service"
    if [ ! -r "$service_file" ]; then
        return 1
    fi
    awk '
        /^ExecStart=/ {
            for (index = 1; index <= NF; index++) {
                if ($index == "-l" || $index == "--listen") {
                    count = split($(index + 1), address, ":")
                    print address[count]
                    exit
                }
            }
        }
    ' "$service_file"
}

service_config_files() {
    local service_file="${SYSTEMD_UNIT_DIR}/${SERVICE_NAME}.service"
    local dropin_dir="${SYSTEMD_UNIT_DIR}/${SERVICE_NAME}.service.d"
    local file=""
    if [ -r "$service_file" ]; then
        printf '%s\n' "$service_file"
    fi
    for file in "$dropin_dir"/*.conf; do
        if [ -r "$file" ]; then
            printf '%s\n' "$file"
        fi
    done
}

get_last_service_directive() {
    local key="$1"
    local file=""
    local candidate=""
    local value=""
    while IFS= read -r file; do
        candidate=$(awk -v key="$key" '
            $0 ~ "^[[:space:]]*" key "=" {
                line=$0
                sub("^[[:space:]]*" key "=[[:space:]]*", "", line)
                value=line
            }
            END { if (value != "") print value }
        ' "$file")
        if [ -n "$candidate" ]; then
            value="$candidate"
        fi
    done < <(service_config_files)
    if [ -n "$value" ]; then
        printf '%s\n' "$value"
        return 0
    fi
    return 1
}

strip_unit_quotes() {
    local value="$1"
    if [[ "$value" == \"*\" ]] || [[ "$value" == \'*\' ]]; then
        value="${value:1:${#value}-2}"
    fi
    printf '%s\n' "$value"
}

extract_service_option() {
    local command="$1"
    local long_name="$2"
    local short_name="$3"
    local pattern=""
    pattern="(^|[[:space:]])(${long_name}|${short_name})[=[:space:]]+\"([^\"]+)\""
    if [[ "$command" =~ $pattern ]]; then
        printf '%s\n' "${BASH_REMATCH[3]}"
        return 0
    fi
    pattern="(^|[[:space:]])(${long_name}|${short_name})[=[:space:]]+([^[:space:]]+)"
    if [[ "$command" =~ $pattern ]]; then
        strip_unit_quotes "${BASH_REMATCH[3]}"
        return 0
    fi
    return 1
}

extract_service_environment() {
    local name="$1"
    local file=""
    local line=""
    local pattern=""
    local value=""
    pattern="\"${name}=([^\"]*)\""
    while IFS= read -r file; do
        while IFS= read -r line; do
            [[ "$line" =~ ^[[:space:]]*Environment= ]] || continue
            if [[ "$line" =~ $pattern ]]; then
                value="${BASH_REMATCH[1]}"
                continue
            fi
            pattern="(^|[[:space:]])${name}=([^[:space:]]+)"
            if [[ "$line" =~ $pattern ]]; then
                value=$(strip_unit_quotes "${BASH_REMATCH[2]}")
            fi
            pattern="\"${name}=([^\"]*)\""
        done <"$file"
    done < <(service_config_files)
    if [ -n "$value" ]; then
        printf '%s\n' "$value"
        return 0
    fi
    return 1
}

canonicalize_existing_parent() {
    local path="$1"
    local directory=""
    local basename=""
    if [ -d "$path" ]; then
        (cd "$path" 2>/dev/null && printf '%s\n' "$PWD") || printf '%s\n' "$path"
        return
    fi
    directory=$(dirname "$path")
    basename=$(basename "$path")
    if [ -d "$directory" ]; then
        (cd "$directory" 2>/dev/null && printf '%s/%s\n' "$PWD" "$basename") || printf '%s\n' "$path"
    else
        printf '%s\n' "$path"
    fi
}

get_service_working_directory() {
    local working_directory=""
    working_directory=$(get_last_service_directive "WorkingDirectory" || true)
    working_directory=$(strip_unit_quotes "${working_directory:-$DATA_DIR}")
    canonicalize_existing_parent "$working_directory"
}

get_service_database_path() {
    local exec_start=""
    local database_path=""
    local working_directory=""
    exec_start=$(get_last_service_directive "ExecStart" || true)
    database_path=$(extract_service_option "$exec_start" "--database" "-d" || true)
    if [ -z "$database_path" ]; then
        database_path=$(extract_service_environment "KOMARI_DB_FILE" || true)
    fi
    if [ -z "$database_path" ]; then
        database_path="./data/komari.db"
    fi
    if [[ "$database_path" != /* ]]; then
        working_directory=$(get_service_working_directory)
        database_path="${working_directory%/}/${database_path}"
    fi
    canonicalize_existing_parent "$database_path"
}

wait_for_service_ready() {
    local port="${1:-}"
    local elapsed=0
    local stable_seconds=0

    while [ "$elapsed" -lt "$SERVICE_READY_TIMEOUT_SECONDS" ]; do
        if systemctl is-failed --quiet "${SERVICE_NAME}.service"; then
            return 1
        fi
        if systemctl is-active --quiet "${SERVICE_NAME}.service"; then
            if [ -n "$port" ]; then
                if curl --fail --silent --show-error --max-time 2 \
                    "http://127.0.0.1:${port}/api/version" >/dev/null 2>&1; then
                    return 0
                fi
            else
                stable_seconds=$((stable_seconds + 1))
                if [ "$stable_seconds" -ge "$SERVICE_ACTIVE_STABILITY_SECONDS" ]; then
                    return 0
                fi
            fi
        else
            stable_seconds=0
        fi
        sleep 1
        elapsed=$((elapsed + 1))
    done
    return 1
}

show_service_failure() {
    log_error "Komari 服务未能通过启动健康检查"
    systemctl status "${SERVICE_NAME}.service" --no-pager -l || true
    journalctl -u "${SERVICE_NAME}.service" -n 50 --no-pager -o cat || true
}

create_upgrade_backup() {
    local timestamp
    local database_path=""
    local backup_data_path=""
    local backup_root="$INSTALL_DIR/upgrade-backups"
    local previous_umask=""

    database_path=$(get_service_database_path)
    timestamp=$(date +%Y%m%d_%H%M%S)
    UPGRADE_BACKUP_PATH="$backup_root/${timestamp}-$$"
    backup_data_path="$UPGRADE_BACKUP_PATH/data"

    previous_umask=$(umask)
    umask 077
    if ! mkdir -p "$backup_root" ||
       ! mkdir "$UPGRADE_BACKUP_PATH" ||
       ! mkdir "$backup_data_path" ||
       ! cp -p "$BINARY_PATH" "$UPGRADE_BACKUP_PATH/komari"; then
        umask "$previous_umask"
        return 1
    fi
    # Newer Komari binaries can make a transactionally consistent SQLite
    # backup without an external sqlite3 executable. Keep the stopped-service
    # file-copy path for upgrades from older releases.
    if [ -f "$database_path" ] && [ -x "$BINARY_PATH" ] &&
       "$BINARY_PATH" --database "$database_path" database-backup \
           --output "$backup_data_path/komari.db" >/dev/null 2>&1; then
        log_info "已使用 Komari 内置备份引擎生成 SQLite 快照"
    else
        if [ -f "$database_path" ] && ! cp -p "$database_path" "$backup_data_path/komari.db"; then
            umask "$previous_umask"
            return 1
        fi
        if [ -f "${database_path}-wal" ] && ! cp -p "${database_path}-wal" "$backup_data_path/komari.db-wal"; then
            umask "$previous_umask"
            return 1
        fi
        if [ -f "${database_path}-shm" ] && ! cp -p "${database_path}-shm" "$backup_data_path/komari.db-shm"; then
            umask "$previous_umask"
            return 1
        fi
    fi
    printf '%s\n' "$database_path" >"$UPGRADE_BACKUP_PATH/database-path.txt"
    umask "$previous_umask"
    log_info "SQLite 数据库路径: $database_path"
    log_success "升级前备份已保存到 $UPGRADE_BACKUP_PATH"
}

restore_upgrade_binary() {
    if [ -z "$UPGRADE_BACKUP_PATH" ] || [ ! -f "$UPGRADE_BACKUP_PATH/komari" ]; then
        log_error "找不到本次升级的二进制备份，无法自动回滚"
        return 1
    fi
    if ! cp -p "$UPGRADE_BACKUP_PATH/komari" "$BINARY_PATH" ||
       ! chmod 0755 "$BINARY_PATH" ||
       ! chown komari:komari "$BINARY_PATH"; then
        log_error "恢复升级前二进制文件失败"
        return 1
    fi
}

# Detect system architecture
detect_arch() {
    local arch
    arch=$(uname -m)
    case $arch in
        x86_64)
            echo "amd64"
            ;;
        aarch64)
            echo "arm64"
            ;;
        i386|i686)
            echo "386"
            ;;
        riscv64)
            echo "riscv64"
            ;;
        *)
            log_error "不支持的架构: $arch"
            exit 1
            ;;
    esac
}

# Check if Komari is already installed
is_installed() {
    if [ -f "$BINARY_PATH" ]; then
        return 0
    else
        return 1
    fi
}

# Install dependencies
install_dependencies() {
    log_step "检查并安装依赖..."

    if ! command -v curl >/dev/null 2>&1; then
        if command -v apt >/dev/null 2>&1; then
            log_info "使用 apt 安装依赖..."
            apt update
            apt install -y curl
        elif command -v yum >/dev/null 2>&1; then
            log_info "使用 yum 安装依赖..."
            yum install -y curl
        elif command -v apk >/dev/null 2>&1; then
            log_info "使用 apk 安装依赖..."
            apk add curl
        else
            log_error "未找到支持的包管理器 (apt/yum/apk)"
            exit 1
        fi
    fi
}

# Binary installation
install_binary() {
    log_step "开始二进制安装..."

    if is_installed; then
        log_info "Komari 已安装。要升级，请使用升级选项。"
        return
    fi


    # 监听端口输入，校验范围 1-65535
    while true; do
        read -p "请输入监听端口 [默认: $DEFAULT_PORT]: " input_port
        if [[ -z "$input_port" ]]; then
            LISTEN_PORT="$DEFAULT_PORT"
            break
        elif [[ "$input_port" =~ ^[0-9]+$ ]] && (( input_port >= 1 && input_port <= 65535 )); then
            LISTEN_PORT="$input_port"
            break
        else
            log_error "端口号无效，请输入 1-65535 之间的数字。"
        fi
    done

    install_dependencies

    local arch
    arch=$(detect_arch)
    log_info "检测到架构: $arch"

    log_step "创建安装目录: $INSTALL_DIR"
    mkdir -p "$INSTALL_DIR"

    log_step "创建数据目录: $DATA_DIR"
    mkdir -p "$DATA_DIR"

    # Ensure system user 'komari' exists
    if ! id -u komari >/dev/null 2>&1; then
        log_step "创建无登录权限的系统账户 'komari'..."
        useradd -r -s /bin/false komari
    fi

    if ! download_binary "$arch"; then
        return 1
    fi

    chmod +x "$BINARY_PATH"
    chown -R komari:komari "$INSTALL_DIR"
    chown -R komari:komari "$DATA_DIR"
    log_success "Komari 二进制文件安装完成并已设置非 root 用户权限"

    if ! check_systemd; then
        log_step "警告：未检测到 systemd，跳过服务创建。"
        log_step "您可以从命令行以 komari 用户手动运行 Komari："
        log_step "    sudo -u komari $BINARY_PATH server -l 0.0.0.0:$LISTEN_PORT"
        echo
        log_success "安装完成！"
        return
    fi

    create_systemd_service "$LISTEN_PORT"

    systemctl daemon-reload
    systemctl enable ${SERVICE_NAME}.service
    systemctl start ${SERVICE_NAME}.service

    if wait_for_service_ready "$LISTEN_PORT"; then
        log_success "Komari 服务启动成功"
        
        log_step "正在获取初始密码..."
        local password=""
        local password_file="$DATA_DIR/data/init_password.txt"
        for _ in $(seq 1 30); do
            if [ -s "$password_file" ]; then
                password=$(cat "$password_file")
                break
            fi
            sleep 1
        done
        if [ -z "$password" ]; then
            log_error "未能自动读取初始密码，请检查服务状态"
            log_info "如果这是首次安装，请稍后手动执行: sudo cat $password_file"
            log_info "如果该文件不存在，可能是已有旧数据库，需使用原管理员密码登录；若已遗忘，可进入 $INSTALL_DIR 后执行 ./komari chpasswd -p '<新密码>' 并重启服务。"
        fi
        show_access_info "$password" "$LISTEN_PORT"
    else
        log_error "Komari 服务启动失败"
        show_service_failure
        return 1
    fi
}

# Create systemd service file
create_systemd_service() {
    local port="$1"
    log_step "创建 systemd 服务..."

    local service_file="${SYSTEMD_UNIT_DIR}/${SERVICE_NAME}.service"
    mkdir -p "$SYSTEMD_UNIT_DIR"
    cat > "$service_file" << EOF
[Unit]
Description=Komari Monitor Service
After=network.target

[Service]
Type=simple
ExecStart=${BINARY_PATH} server -l 0.0.0.0:${port}
WorkingDirectory=${DATA_DIR}
Restart=always
User=komari
Group=komari

[Install]
WantedBy=multi-user.target
EOF

    ensure_systemd_hardening

    log_success "systemd 服务文件创建完成"
}

# Keep resource protection in a drop-in so upgrades do not overwrite a local
# administrator's ExecStart/listen-port customization in the primary unit.
ensure_systemd_hardening() {
    local dropin_dir="${SYSTEMD_UNIT_DIR}/${SERVICE_NAME}.service.d"
    local dropin_file="${dropin_dir}/20-performance-security.conf"
    local database_path=""
    local working_directory=""
    local writable_paths=()
    local unique_writable_paths=()
    local path=""
    local existing=""

    database_path=$(get_service_database_path)
    working_directory=$(get_service_working_directory)
    writable_paths=("$INSTALL_DIR" "$working_directory" "$(dirname "$database_path")")

    mkdir -p "$dropin_dir"
    cat > "$dropin_file" << EOF
[Service]
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=tmpfs
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
RestrictRealtime=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
LimitNOFILE=65536
TasksMax=4096
UMask=0077
MemoryHigh=75%
MemoryMax=90%
EOF

    for path in "${writable_paths[@]}"; do
        [ -n "$path" ] || continue
        path=$(canonicalize_existing_parent "$path")
        for existing in "${unique_writable_paths[@]:-}"; do
            if [ "$existing" = "$path" ]; then
                path=""
                break
            fi
        done
        [ -n "$path" ] || continue
        unique_writable_paths+=("$path")
        printf 'ReadWritePaths=%q\n' "$path" >>"$dropin_file"
        case "$path" in
            /home/*|/root|/root/*|/run/user/*)
                printf 'BindPaths=%q\n' "$path" >>"$dropin_file"
                ;;
        esac
    done
}

# Show access information
show_access_info() {
    local password=$1
    local port=${2:-$DEFAULT_PORT}
    echo
    log_success "安装完成！"
    echo
    log_info "访问信息："
    log_info "  URL: http://$(hostname -I | awk '{print $1}'):${port}"
    if [ -n "$password" ]; then
        log_info "初始登录信息（仅显示一次）: $password"
    fi
    echo
    log_info "服务管理命令："
    log_info "  状态:  systemctl status $SERVICE_NAME"
    log_info "  启动:   systemctl start $SERVICE_NAME"
    log_info "  停止:    systemctl stop $SERVICE_NAME"
    log_info "  重启: systemctl restart $SERVICE_NAME"
    log_info "  日志:    journalctl -u $SERVICE_NAME -f"
}

# Upgrade function
upgrade_komari() {
    log_step "升级 Komari..."

    if ! is_installed; then
        log_error "Komari 未安装。请先安装它。"
        return 1
    fi

    if ! check_systemd; then
        log_error "未检测到 systemd。无法管理服务。"
        return 1
    fi

    local service_port=""
    local arch=""

    service_port=$(get_service_port || true)

    log_step "停止 Komari 服务..."
    if ! systemctl stop ${SERVICE_NAME}.service; then
        log_error "无法停止 Komari 服务，升级已取消"
        return 1
    fi
    if systemctl is-active --quiet ${SERVICE_NAME}.service; then
        log_error "Komari 服务仍在运行，升级已取消"
        return 1
    fi

    log_step "备份当前二进制文件和 SQLite 数据库..."
    if ! create_upgrade_backup; then
        log_error "升级前备份失败，未替换现有二进制文件"
        systemctl start ${SERVICE_NAME}.service || true
        return 1
    fi

    arch=$(detect_arch)

    log_step "下载最新版本..."
    if ! download_binary "$arch"; then
        log_error "下载或校验失败，现有二进制文件保持不变"
        systemctl reset-failed ${SERVICE_NAME}.service || true
        systemctl start ${SERVICE_NAME}.service || true
        return 1
    fi

    if ! chmod 0755 "$BINARY_PATH" || ! chown komari:komari "$BINARY_PATH"; then
        log_error "无法设置新二进制文件的权限，准备自动回滚"
        systemctl stop ${SERVICE_NAME}.service || true
        restore_upgrade_binary || true
        systemctl reset-failed ${SERVICE_NAME}.service || true
        systemctl start ${SERVICE_NAME}.service || true
        return 1
    fi

    ensure_systemd_hardening
    systemctl daemon-reload

    log_step "重启 Komari 服务..."
    systemctl reset-failed ${SERVICE_NAME}.service || true
    if ! systemctl start ${SERVICE_NAME}.service; then
        log_error "systemd 无法启动新版本，准备自动回滚"
    elif wait_for_service_ready "$service_port"; then
        log_success "Komari 升级成功并已通过健康检查"
        log_info "本次升级备份: $UPGRADE_BACKUP_PATH"
        return 0
    else
        show_service_failure
    fi

    log_step "恢复升级前的 Komari 二进制文件..."
    systemctl stop ${SERVICE_NAME}.service || true
    if ! restore_upgrade_binary; then
        return 1
    fi
    systemctl reset-failed ${SERVICE_NAME}.service || true
    if systemctl start ${SERVICE_NAME}.service &&
       wait_for_service_ready "$service_port"; then
        log_error "新版本启动失败，已自动恢复并启动升级前版本"
        log_info "故障排查备份: $UPGRADE_BACKUP_PATH"
    else
        log_error "新旧版本均未能启动，请保留 $UPGRADE_BACKUP_PATH 并检查上方日志"
    fi
    return 1
}

# Uninstall function
uninstall_komari() {
    log_step "卸载 Komari..."

    if ! is_installed; then
        log_info "Komari 未安装"
        return 0
    fi

    read -p "这将删除 Komari。您确定吗？(Y/n): " confirm
    if [[ $confirm =~ ^[Nn]$ ]]; then
        log_info "卸载已取消"
        return 0
    fi

    if check_systemd; then
        log_step "停止并禁用服务..."
        systemctl stop ${SERVICE_NAME}.service >/dev/null 2>&1
        systemctl disable ${SERVICE_NAME}.service >/dev/null 2>&1
        rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
        systemctl daemon-reload
        log_success "systemd 服务已删除"
    fi

    log_step "删除二进制文件..."
    rm -f "$BINARY_PATH"
    # 尝试在目录为空时删除该目录
    rmdir "$INSTALL_DIR" 2>/dev/null || log_info "数据目录 $INSTALL_DIR 不为空，未删除"
    log_success "Komari 二进制文件已删除"

    log_success "Komari 卸载完成"
    log_info "数据文件保留在 $DATA_DIR"
}

# Show service status
show_status() {
    if ! is_installed; then
        log_error "Komari 未安装"
        return
    fi
    if ! check_systemd; then
        log_error "未检测到 systemd。无法获取服务状态。"
        return
    fi
    log_step "Komari 服务状态:"
    systemctl status ${SERVICE_NAME}.service --no-pager -l
}

# Show service logs
show_logs() {
    if ! is_installed; then
        log_error "Komari 未安装"
        return
    fi
    if ! check_systemd; then
        log_error "未检测到 systemd。无法获取服务日志。"
        return
    fi
    log_step "查看 Komari 服务日志..."
    journalctl -u ${SERVICE_NAME} -f --no-pager
}

# Restart service
restart_service() {
    if ! is_installed; then
        log_error "Komari 未安装"
        return
    fi
    if ! check_systemd; then
        log_error "未检测到 systemd。无法重启服务。"
        return
    fi
    log_step "重启 Komari 服务..."
    systemctl restart ${SERVICE_NAME}.service
    if systemctl is-active --quiet ${SERVICE_NAME}.service; then
        log_success "服务重启成功"
    else
        log_error "服务重启失败"
    fi
}

# Stop service
stop_service() {
    if ! is_installed; then
        log_error "Komari 未安装"
        return
    fi
    if ! check_systemd; then
        log_error "未检测到 systemd。无法停止服务。"
        return
    fi
    log_step "停止 Komari 服务..."
    systemctl stop ${SERVICE_NAME}.service
    log_success "服务已停止"
}


# Main menu
main_menu() {
    show_banner
    echo "请选择操作："
    echo "  1) 安装 Komari"
    echo "  2) 升级 Komari"
    echo "  3) 卸载 Komari"
    echo "  4) 查看状态"
    echo "  5) 查看日志"
    echo "  6) 重启服务"
    echo "  7) 停止服务"
    echo "  8) 退出"
    echo

    read -p "输入选项 [1-8]: " choice

    case $choice in
        1) install_binary ;;
        2) upgrade_komari ;;
        3) uninstall_komari ;;
        4) show_status ;;
        5) show_logs ;;
        6) restart_service ;;
        7) stop_service ;;
        8) exit 0 ;;
        *) log_error "无效选项" ;;
    esac
}

# Main execution. Tests may source the installer without opening its menu.
if [ "${KOMARI_INSTALLER_LIBRARY_ONLY:-0}" != "1" ]; then
    check_root
    main_menu
fi
