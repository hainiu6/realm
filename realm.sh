#!/bin/bash

# ==========================================
# Realm 一键转发脚本 v3.9.2
# 更新日志:
# 1. 仓库迁移至 hainiu6/realm
# 2. 安装包与脚本更新地址同步迁移，保留全部面板功能
# ==========================================

# --- 基础配置 ---
sh_ver="3.9.2"
panel_ver="v3.9.2"

# 颜色定义
RED="\033[31m"
GREEN="\033[32m"
YELLOW="\033[33m"
PLAIN="\033[0m"

# 路径定义
REALM_DIR="/root/realm"
REALM_BIN="${REALM_DIR}/realm"
CONFIG_DIR="/root/.realm"
CONFIG_FILE="${CONFIG_DIR}/config.toml"
REALM_SYSTEMD_SERVICE_FILE="/etc/systemd/system/realm.service"
REALM_OPENRC_SERVICE_FILE="/etc/init.d/realm"
PANEL_DIR="${REALM_DIR}/web"
PANEL_BIN="${PANEL_DIR}/realm_web"
PANEL_SYSTEMD_SERVICE_FILE="/etc/systemd/system/realm-panel.service"
PANEL_OPENRC_SERVICE_FILE="/etc/init.d/realm-panel"

command_exists() {
    command -v "$1" >/dev/null 2>&1
}

is_alpine() {
    [ -f /etc/alpine-release ]
}

detect_init_system() {
    if [ -n "${REALM_INIT_SYSTEM:-}" ]; then
        echo "$REALM_INIT_SYSTEM"
        return
    fi
    if is_alpine; then
        echo "openrc"
        return
    fi
    if command_exists systemctl; then
        echo "systemd"
        return
    fi
    if command_exists rc-service; then
        echo "openrc"
        return
    fi
    echo "unknown"
}

detect_package_manager() {
    if [ -n "${REALM_PACKAGE_MANAGER:-}" ]; then
        echo "$REALM_PACKAGE_MANAGER"
        return
    fi
    if command_exists apk; then
        echo "apk"
    elif command_exists apt-get; then
        echo "apt"
    elif command_exists yum; then
        echo "yum"
    else
        echo "unknown"
    fi
}

is_musl_system() {
    is_alpine || { command_exists ldd && ldd --version 2>&1 | grep -qi musl; }
}

select_realm_filename() {
    local arch=${1:-$(uname -m)}
    local libc=${REALM_LIBC:-gnu}
    if [ -z "${REALM_LIBC:-}" ] && is_musl_system; then
        libc="musl"
    fi

    case "$arch" in
        x86_64) echo "realm-x86_64-unknown-linux-${libc}.tar.gz" ;;
        aarch64|arm64) echo "realm-aarch64-unknown-linux-${libc}.tar.gz" ;;
        *) return 1 ;;
    esac
}

service_action() {
    local service_name=$1
    local action=$2
    local manager
    manager=$(detect_init_system)

    case "$manager" in
        systemd)
            case "$action" in
                enable) systemctl enable "$service_name" ;;
                disable) systemctl disable "$service_name" ;;
                daemon-reload) systemctl daemon-reload ;;
                is-active) systemctl is-active --quiet "$service_name" ;;
                *) systemctl "$action" "$service_name" ;;
            esac
            ;;
        openrc)
            case "$action" in
                enable) rc-update add "$service_name" default ;;
                disable) rc-update del "$service_name" default >/dev/null 2>&1 || true ;;
                daemon-reload) return 0 ;;
                is-active) rc-service "$service_name" status >/dev/null 2>&1 ;;
                *) rc-service "$service_name" "$action" ;;
            esac
            ;;
        *)
            echo -e "${RED}错误: 不支持的服务管理器，请安装 systemd 或 OpenRC。${PLAIN}"
            return 1
            ;;
    esac
}

service_start() { service_action "$1" start; }
service_stop() { service_action "$1" stop; }
service_restart() { service_action "$1" restart; }
service_enable() { service_action "$1" enable; }
service_disable() { service_action "$1" disable; }
service_daemon_reload() { service_action "" daemon-reload; }
service_is_active() { service_action "$1" is-active >/dev/null 2>&1; }

# --- 状态检测函数 ---

get_status() {
    if service_is_active realm; then
        echo -e "${GREEN}运行中${PLAIN}"
    else
        echo -e "${RED}未运行${PLAIN}"
    fi
}

get_panel_status() {
    if [ ! -f "$PANEL_BIN" ]; then
        echo -e "${RED}未安装${PLAIN}"
    elif service_is_active realm-panel; then
        echo -e "${GREEN}运行中${PLAIN}"
    else
        echo -e "${YELLOW}已安装但未启动${PLAIN}"
    fi
}

# --- 核心校验函数 ---

validate_port() {
    local port
    # 使用 [:cntrl:] 字符类清理控制字符，xargs 去除前后空白
    port=$(printf '%s' "$1" | tr -d '[:cntrl:]' | xargs 2>/dev/null)
    if [[ "$port" =~ ^[0-9]+$ ]] && [ "$port" -ge 1 ] && [ "$port" -le 65535 ]; then
        return 0
    else
        echo -e "${RED}错误: 端口必须是 1-65535 之间的数字。${PLAIN}"
        return 1
    fi
}

validate_ip() {
    local ip
    # 使用 [:cntrl:] 字符类清理控制字符，xargs 去除前后空白
    ip=$(printf '%s' "$1" | tr -d '[:cntrl:]' | xargs 2>/dev/null)
    if [[ -z "$ip" ]]; then
        echo -e "${RED}错误: 地址不能为空。${PLAIN}"
        return 1
    fi
    if [[ "$ip" =~ ^[][a-zA-Z0-9.:-]+$ ]]; then
        return 0
    else
        echo -e "${RED}错误: 无效的 IP 或域名格式。${PLAIN}"
        return 1
    fi
}

check_port_available() {
    local port
    # 使用 [:cntrl:] 字符类清理控制字符，xargs 去除前后空白
    port=$(printf '%s' "$1" | tr -d '[:cntrl:]' | xargs 2>/dev/null)
    if command -v ss >/dev/null; then
        if ss -tulpn | grep ":${port} " | grep -qv "realm"; then
            echo -e "${RED}错误: 本机端口 ${port} 已被其他程序占用。${PLAIN}"
            return 1
        fi
    fi
    return 0
}

check_rule_exists() {
    local port
    # 使用 [:cntrl:] 字符类清理控制字符，xargs 去除前后空白
    port=$(printf '%s' "$1" | tr -d '[:cntrl:]' | xargs 2>/dev/null)
    if [ -f "$CONFIG_FILE" ]; then
        if grep -qE "listen = \"(\\[::]:${port}|0\\.0\\.0\\.0:${port})\"" "$CONFIG_FILE"; then
            echo -e "${RED}错误: 端口 ${port} 的规则已存在。${PLAIN}"
            return 0
        fi
    fi
    return 1
}

# --- 基础功能 ---

init_env() {
    mkdir -p "$REALM_DIR"
    mkdir -p "$CONFIG_DIR"
    [ ! -f "$CONFIG_FILE" ] && write_config_header
    ensure_endpoints_field
}

write_config_header() {
    cat <<EOF > "$CONFIG_FILE"
endpoints = []

[network]
no_tcp = false
use_udp = true

EOF
}

prepare_config_for_endpoint_append() {
    # endpoints = [] 只用于空配置；添加第一条规则前必须移除，
    # 否则会与后面的 [[endpoints]] 重复定义。
    sed -i '/^[[:space:]]*endpoints[[:space:]]*=[[:space:]]*\[\][[:space:]]*$/d' "$CONFIG_FILE"
}

ensure_endpoints_field() {
    if grep -qE '^([[:space:]]*endpoints[[:space:]]*=|[[:space:]]*\[\[endpoints\]\])' "$CONFIG_FILE"; then
        return
    fi

    local repaired_config="${CONFIG_FILE}.repair.$$"
    {
        echo 'endpoints = []'
        echo
        cat "$CONFIG_FILE"
    } > "$repaired_config"
    mv "$repaired_config" "$CONFIG_FILE"
    echo -e "${YELLOW}已修复缺少 endpoints 的空 Realm 配置。${PLAIN}"
}

add_package() {
    local package=$1
    local existing
    for existing in "${packages[@]}"; do
        [ "$existing" = "$package" ] && return
    done
    packages+=("$package")
}

require_command_package() {
    local command_name=$1
    local package_name=$2
    command_exists "$command_name" || add_package "$package_name"
}

check_dependencies() {
    local manager
    local package_manager
    local packages=()
    manager=$(detect_init_system)
    package_manager=$(detect_package_manager)

    case "$package_manager" in
        apt)
            require_command_package wget wget
            require_command_package tar tar
            require_command_package sed sed
            require_command_package grep grep
            require_command_package curl curl
            require_command_package unzip unzip
            require_command_package ss iproute2
            if [ "$manager" = "systemd" ]; then
                require_command_package systemctl systemd
            else
                require_command_package rc-service openrc
                require_command_package rc-update openrc
            fi
            ;;
        yum)
            require_command_package wget wget
            require_command_package tar tar
            require_command_package sed sed
            require_command_package grep grep
            require_command_package curl curl
            require_command_package unzip unzip
            require_command_package ss iproute
            if [ "$manager" = "systemd" ]; then
                require_command_package systemctl systemd
            else
                require_command_package rc-service openrc
                require_command_package rc-update openrc
            fi
            ;;
        apk)
            require_command_package bash bash
            require_command_package wget wget
            require_command_package tar tar
            require_command_package sed sed
            require_command_package grep grep
            require_command_package curl curl
            require_command_package unzip unzip
            require_command_package ss iproute2
            require_command_package update-ca-certificates ca-certificates
            require_command_package rc-service openrc
            require_command_package rc-update openrc
            ;;
        *)
            echo -e "${RED}请手动安装依赖: wget tar sed grep curl unzip ss。${PLAIN}"
            exit 1
            ;;
    esac

    require_command_package nft nftables

    if [ ${#packages[@]} -gt 0 ]; then
        echo -e "${YELLOW}安装依赖: ${packages[*]} ...${PLAIN}"
        case "$package_manager" in
            apt) apt-get update -y >/dev/null 2>&1 && apt-get install -y "${packages[@]}" ;;
            yum) yum install -y "${packages[@]}" ;;
            apk) apk add --no-cache "${packages[@]}" ;;
        esac
    fi
}

set_service_file_permissions() {
    local file_path=$1
    local mode=$2
    chown root:root "$file_path" 2>/dev/null || true
    chmod "$mode" "$file_path"
}

write_realm_service() {
    case "$(detect_init_system)" in
        systemd)
            cat <<EOF > "$REALM_SYSTEMD_SERVICE_FILE"
[Unit]
Description=Realm Forwarding Service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
Restart=on-failure
RestartSec=5s
WorkingDirectory=${REALM_DIR}
ExecStart=${REALM_BIN} -c ${CONFIG_FILE}

[Install]
WantedBy=multi-user.target
EOF
            set_service_file_permissions "$REALM_SYSTEMD_SERVICE_FILE" 0644
            ;;
        openrc)
            cat <<EOF > "$REALM_OPENRC_SERVICE_FILE"
#!/sbin/openrc-run
name="Realm Forwarding Service"
description="Realm Forwarding Service"
supervisor="supervise-daemon"
command="${REALM_BIN}"
command_args="-c ${CONFIG_FILE}"
directory="${REALM_DIR}"
command_user="root"
respawn_delay=5
respawn_max=0

depend() {
    need net
    after firewall
}
EOF
            set_service_file_permissions "$REALM_OPENRC_SERVICE_FILE" 0755
            ;;
        *)
            echo -e "${RED}无法创建服务文件: 不支持的服务管理器。${PLAIN}"
            return 1
            ;;
    esac
}

write_panel_service() {
    case "$(detect_init_system)" in
        systemd)
            cat <<EOF > "$PANEL_SYSTEMD_SERVICE_FILE"
[Unit]
Description=Realm Web Panel
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=${PANEL_DIR}
ExecStart=${PANEL_BIN}
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF
            set_service_file_permissions "$PANEL_SYSTEMD_SERVICE_FILE" 0644
            ;;
        openrc)
            cat <<EOF > "$PANEL_OPENRC_SERVICE_FILE"
#!/sbin/openrc-run
name="Realm Web Panel"
description="Realm Web Panel"
supervisor="supervise-daemon"
command="${PANEL_BIN}"
directory="${PANEL_DIR}"
command_user="root"
respawn_delay=5
respawn_max=0

depend() {
    need net
}
EOF
            set_service_file_permissions "$PANEL_OPENRC_SERVICE_FILE" 0755
            ;;
        *)
            echo -e "${RED}无法创建面板服务文件: 不支持的服务管理器。${PLAIN}"
            return 1
            ;;
    esac
}

install_realm() {
    echo -e "${GREEN}> 部署 Realm...${PLAIN}"
    check_dependencies; init_env
    local version=$(curl -s https://api.github.com/repos/zhboner/realm/releases/latest | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
    [ -z "$version" ] && version="v2.6.0"

    local arch=$(uname -m)
    local filename
    if ! filename=$(select_realm_filename "$arch"); then
        echo -e "${RED}不支持架构: $arch${PLAIN}"
        return 1
    fi

    local realm_install_dir
    realm_install_dir=$(mktemp -d /tmp/realm_install.XXXXXX) || return 1
    local realm_archive="${realm_install_dir}/realm.tar.gz"
    local realm_candidate
    local realm_check_output

    if ! wget -O "$realm_archive" "https://github.com/zhboner/realm/releases/download/${version}/${filename}"; then
        echo -e "${RED}下载失败${PLAIN}"
        rm -rf "$realm_install_dir"
        return 1
    fi
    if ! tar -xzf "$realm_archive" -C "$realm_install_dir"; then
        echo -e "${RED}解压失败${PLAIN}"
        rm -rf "$realm_install_dir"
        return 1
    fi
    realm_candidate=$(find "$realm_install_dir" -type f -name realm | head -1)
    [ -z "$realm_candidate" ] && { echo -e "${RED}安装包内未找到 Realm 程序${PLAIN}"; rm -rf "$realm_install_dir"; return 1; }
    chmod +x "$realm_candidate"

    if ! realm_check_output=$("$realm_candidate" --version 2>&1); then
        if [[ "$filename" == *"linux-gnu.tar.gz" ]] && grep -q "GLIBC_" <<< "$realm_check_output"; then
            echo -e "${YELLOW}系统 glibc 版本较旧，自动切换到 musl 静态版本...${PLAIN}"
            filename=${filename/linux-gnu.tar.gz/linux-musl.tar.gz}
            rm -f "$realm_archive" "$realm_candidate"
            if ! wget -O "$realm_archive" "https://github.com/zhboner/realm/releases/download/${version}/${filename}" || \
               ! tar -xzf "$realm_archive" -C "$realm_install_dir"; then
                echo -e "${RED}musl 版本下载或解压失败${PLAIN}"
                rm -rf "$realm_install_dir"
                return 1
            fi
            realm_candidate=$(find "$realm_install_dir" -type f -name realm | head -1)
            [ -z "$realm_candidate" ] && { echo -e "${RED}musl 安装包内未找到 Realm 程序${PLAIN}"; rm -rf "$realm_install_dir"; return 1; }
            chmod +x "$realm_candidate"
            if ! realm_check_output=$("$realm_candidate" --version 2>&1); then
                echo -e "${RED}musl 版本仍无法运行: ${realm_check_output}${PLAIN}"
                rm -rf "$realm_install_dir"
                return 1
            fi
        else
            echo -e "${RED}Realm 程序无法运行: ${realm_check_output}${PLAIN}"
            rm -rf "$realm_install_dir"
            return 1
        fi
    fi

    service_stop realm >/dev/null 2>&1 || true
    cp "$realm_candidate" "$REALM_BIN"
    chmod +x "$REALM_BIN"
    rm -rf "$realm_install_dir"

    write_realm_service || return 1
    service_daemon_reload
    service_enable realm
    service_restart realm
    echo -e "${GREEN}安装完成${PLAIN}"
}

uninstall_realm() {
    read -p "确定卸载 Realm? [y/N]: " confirm
    [[ "$confirm" != "y" && "$confirm" != "Y" ]] && return
    service_stop realm
    service_disable realm
    rm -f "$REALM_SYSTEMD_SERVICE_FILE" "$REALM_OPENRC_SERVICE_FILE"
    service_daemon_reload
    rm -rf "$REALM_DIR"
    read -p "删除配置? [y/N]: " del_conf
    [[ "$del_conf" == "y" || "$del_conf" == "Y" ]] && rm -rf "$CONFIG_DIR"
    echo -e "${GREEN}已卸载${PLAIN}"
}

# --- 转发管理 (已添加重试限制) ---

add_forward() {
    echo -e "${YELLOW}>>> 添加转发 (连续错误2次自动返回)${PLAIN}"
    
    # 1. 本机端口
    local attempt=0
    while true; do
        read -e -p "本机端口: " lp
        # 依次校验：格式、占用、重复
        if ! validate_port "$lp"; then
            ((attempt++)); [ $attempt -ge 2 ] && { echo -e "${RED}错误过多，返回主菜单${PLAIN}"; return; }
            continue
        fi
        if ! check_port_available "$lp"; then
            ((attempt++)); [ $attempt -ge 2 ] && { echo -e "${RED}错误过多，返回主菜单${PLAIN}"; return; }
            continue
        fi
        if check_rule_exists "$lp"; then
            ((attempt++)); [ $attempt -ge 2 ] && { echo -e "${RED}错误过多，返回主菜单${PLAIN}"; return; }
            continue
        fi
        break
    done

    # 2. 落地IP
    attempt=0
    while true; do
        read -e -p "落地IP/域名: " rip
        if ! validate_ip "$rip"; then
             ((attempt++)); [ $attempt -ge 2 ] && { echo -e "${RED}错误过多，返回主菜单${PLAIN}"; return; }
             continue
        fi
        break
    done

    # 3. 落地端口
    attempt=0
    while true; do
        read -e -p "落地端口: " rp
        if ! validate_port "$rp"; then
            ((attempt++)); [ $attempt -ge 2 ] && { echo -e "${RED}错误过多，返回主菜单${PLAIN}"; return; }
            continue
        fi
        break
    done

    prepare_config_for_endpoint_append
    cat <<EOF >> "$CONFIG_FILE"

[[endpoints]]
listen = "[::]:$lp"
remote = "$rip:$rp"
EOF
    restart_service
}

add_range_forward() {
    echo -e "${YELLOW}>>> 端口段转发 (连续错误2次自动返回)${PLAIN}"
    local attempt=0
    
    while true; do read -e -p "落地IP: " rip; validate_ip "$rip" && break; ((attempt++)); [ $attempt -ge 2 ] && return; done
    attempt=0; while true; do read -e -p "起始端口: " sp; validate_port "$sp" && break; ((attempt++)); [ $attempt -ge 2 ] && return; done
    attempt=0; while true; do read -e -p "结束端口: " ep; validate_port "$ep" && break; ((attempt++)); [ $attempt -ge 2 ] && return; done
    attempt=0; while true; do read -e -p "落地基准端口: " rbp; validate_port "$rbp" && break; ((attempt++)); [ $attempt -ge 2 ] && return; done

    [ "$sp" -ge "$ep" ] && { echo -e "${RED}起始必须小于结束${PLAIN}"; return; }

    echo "生成中..."
    prepare_config_for_endpoint_append
    local rp=$rbp
    for ((p=$sp; p<=$ep; p++)); do
        if ! grep -Fq "listen = \"[::]:$p\"" "$CONFIG_FILE"; then
            cat <<EOF >> "$CONFIG_FILE"

[[endpoints]]
listen = "[::]:$p"
remote = "$rip:$rp"
EOF
        fi
        ((rp++))
    done
    restart_service
}

delete_forward() {
    [ ! -f "$CONFIG_FILE" ] && return
    local listens=($(grep "listen =" "$CONFIG_FILE" | awk -F'"' '{print $2}'))
    local remotes=($(grep "remote =" "$CONFIG_FILE" | awk -F'"' '{print $2}'))
    [ ${#listens[@]} -eq 0 ] && { echo "无规则"; return; }

    echo "==============="
    for ((i=0; i<${#listens[@]}; i++)); do
        echo -e "${GREEN}$((i+1)).${PLAIN} ${listens[i]} -> ${remotes[i]}"
    done
    echo "==============="
    read -p "删除序号(0取消): " c
    [[ "$c" == "0" || -z "$c" ]] && return
    if ! [[ "$c" =~ ^[0-9]+$ ]] || [ "$c" -lt 1 ] || [ "$c" -gt "${#listens[@]}" ]; then
        echo -e "${RED}无效序号${PLAIN}"; return
    fi
    
    cp "$CONFIG_FILE" "${CONFIG_FILE}.bak"
    # 删除所选 endpoints 配置块，而不是重新生成所有规则，避免丢失
    # extra_remotes、balance 以及其他 Realm 高级配置。
    awk -v target="$c" '
        /^\[\[endpoints\]\][[:space:]]*$/ {
            endpoint_index++
            skipping = (endpoint_index == target)
            if (!skipping) print
            next
        }
        { if (!skipping) print }
    ' "${CONFIG_FILE}.bak" > "$CONFIG_FILE"
    if ! grep -q '^\[\[endpoints\]\][[:space:]]*$' "$CONFIG_FILE"; then
        local empty_config_tmp="${CONFIG_FILE}.empty.$$"
        {
            echo 'endpoints = []'
            echo
            cat "$CONFIG_FILE"
        } > "$empty_config_tmp"
        mv "$empty_config_tmp" "$CONFIG_FILE"
    fi
    restart_service
}

# --- 服务控制 ---
start_service() {
    service_start realm && echo "已启动" || echo -e "${RED}启动失败${PLAIN}"
}

stop_service() {
    service_stop realm && echo "已停止" || echo -e "${RED}停止失败${PLAIN}"
}

restart_service() {
    service_daemon_reload
    service_restart realm
    sleep 1
    service_is_active realm && echo -e "${GREEN}重启成功${PLAIN}" || echo -e "${RED}重启失败${PLAIN}"
}

# --- 面板管理 ---
panel_management() {
    while true; do
        clear
        echo "=== Realm 面板管理 ($panel_ver) ==="
        echo -e "面板状态: $(get_panel_status)"
        echo "============================="
        echo "1. 安装面板"
        echo "2. 启动面板"
        echo "3. 停止面板"
        echo "4. 卸载面板"
        echo "0. 返回上级"
        read -p "选择: " pc
        case $pc in
            1) install_panel ;;
            2) service_start realm-panel && echo "尝试启动..." || echo -e "${RED}启动失败${PLAIN}" ;;
            3) service_stop realm-panel && echo "已停止" || echo -e "${RED}停止失败${PLAIN}" ;;
            4) uninstall_panel ;;
            0) break ;;
            *) echo "无效选择" ;;
        esac
        read -p "按回车继续..."
    done
}

install_panel() {
    check_dependencies
    local arch=$(uname -m)
    local p_file=""
    case "$arch" in
        x86_64) p_file="realm-panel-linux-amd64.zip" ;;
        aarch64|arm64) p_file="realm-panel-linux-arm64.zip" ;;
        *) echo "不支持架构: $arch"; return ;;
    esac

    mkdir -p "$PANEL_DIR"
    local url="https://github.com/hainiu6/realm/releases/download/${panel_ver}/${p_file}"
    local tmp_zip="/tmp/${p_file}"
    local tmp_dir="/tmp/realm_panel_$$"

    if ! wget -O "$tmp_zip" "$url"; then
        echo -e "${RED}下载失败${PLAIN}"
        rm -f "$tmp_zip"
        return 1
    fi

    mkdir -p "$tmp_dir"
    unzip -o "$tmp_zip" -d "$tmp_dir"
    rm -f "$tmp_zip"

    # 无论 zip 内部目录结构如何，都能找到 realm_web 二进制
    local found_bin
    found_bin=$(find "$tmp_dir" -name "realm_web" -type f 2>/dev/null | head -1)
    if [ -z "$found_bin" ]; then
        # 兜底：找第一个非文本可执行文件
        found_bin=$(find "$tmp_dir" -maxdepth 3 -type f ! -name "*.txt" ! -name "*.md" 2>/dev/null | head -1)
    fi

    if [ -z "$found_bin" ]; then
        echo -e "${RED}解压后未找到可执行文件，请手动安装${PLAIN}"
        rm -rf "$tmp_dir"
        return 1
    fi

    # 升级时先停止旧面板，避免替换正在运行的二进制失败。
    service_stop realm-panel >/dev/null 2>&1 || true

    cp "$found_bin" "$PANEL_BIN"
    chmod +x "$PANEL_BIN"

    # 完整替换前端资源，避免 cp 到已有目录后形成 static/static、
    # templates/templates，导致新版后端继续使用旧版页面。
    rm -rf "$PANEL_DIR/static" "$PANEL_DIR/templates"
    [ -d "$tmp_dir/static" ]    && cp -r "$tmp_dir/static"    "$PANEL_DIR/"
    [ -d "$tmp_dir/templates" ] && cp -r "$tmp_dir/templates" "$PANEL_DIR/"
    # 兼容 zip 内有子目录的情况
    local sub_dir
    sub_dir=$(find "$tmp_dir" -mindepth 1 -maxdepth 1 -type d | head -1)
    if [ -n "$sub_dir" ]; then
        [ -d "$sub_dir/static" ]    && cp -r "$sub_dir/static"    "$PANEL_DIR/"
        [ -d "$sub_dir/templates" ] && cp -r "$sub_dir/templates" "$PANEL_DIR/"
    fi
    # 复制默认配置（不覆盖已有配置）
    [ -f "$tmp_dir/config.toml" ] && [ ! -f "$PANEL_DIR/config.toml" ] && cp "$tmp_dir/config.toml" "$PANEL_DIR/"
    [ -n "$sub_dir" ] && [ -f "$sub_dir/config.toml" ] && [ ! -f "$PANEL_DIR/config.toml" ] && cp "$sub_dir/config.toml" "$PANEL_DIR/"

    rm -rf "$tmp_dir"

    write_panel_service || return 1
    service_daemon_reload
    service_enable realm-panel
    service_start realm-panel
    echo -e "${GREEN}面板安装成功!${PLAIN}"
}

uninstall_panel() {
    service_stop realm-panel
    service_disable realm-panel
    rm -f "$PANEL_SYSTEMD_SERVICE_FILE" "$PANEL_OPENRC_SERVICE_FILE"
    service_daemon_reload
    rm -rf "$PANEL_DIR"
    echo "已卸载"
}

# --- 脚本更新 ---
Update_Shell() {
    local url="https://raw.githubusercontent.com/hainiu6/realm/main/realm.sh"
    local new_ver=$(wget -qO- "$url" | grep 'sh_ver="' | awk -F "=" '{print $NF}' | tr -d '"' | head -1)
    [[ -z "$new_ver" ]] && { echo -e "${RED}检测失败${PLAIN}"; return; }
    [[ "$new_ver" == "$sh_ver" ]] && { echo "已是最新"; return; }
    read -p "更新到 $new_ver? [y/N]: " yn
    [[ "$yn" =~ ^[Yy]$ ]] && wget -N "$url" -O realm.sh && chmod +x realm.sh && echo "已更新" && exit 0
}

# --- 主菜单 ---
show_menu() {
    clear
    echo "################################################"
    echo "#        Realm 一键转发脚本 (v${sh_ver})         #"
    echo "################################################"
    echo -e " Realm 状态: $(get_status)"
    echo -e " 面板 状态: $(get_panel_status)"
    echo "------------------------------------------------"
    echo "  1. 安装 / 重置 Realm"
    echo "  2. 卸载 Realm"
    echo "------------------------------------------------"
    echo "  3. 添加转发规则"
    echo "  4. 添加端口段转发"
    echo "  5. 删除转发规则"
    echo "  6. 查看当前配置"
    echo "------------------------------------------------"
    echo "  7. 启动服务"
    echo "  8. 停止服务"
    echo "  9. 重启服务"
    echo "------------------------------------------------"
    echo "  10. 更新脚本"
    echo "  11. 面板管理"
    echo "  0. 退出脚本"
    echo "################################################"
}

main() {
    check_dependencies; init_env
    while true; do
        show_menu
        read -p "选择 [0-11]: " opt
        case $opt in
            1) install_realm ;;
            2) uninstall_realm ;;
            3) add_forward ;;
            4) add_range_forward ;;
            5) delete_forward ;;
            6) cat "$CONFIG_FILE" ;;
            7) start_service ;;
            8) stop_service ;;
            9) restart_service ;;
            10) Update_Shell ;;
            11) panel_management ;;
            0) exit 0 ;;
            *) echo "无效" ;;
        esac
        [ "$opt" != "0" ] && read -p "按回车返回..."
    done
}

if [ "${REALM_TESTING:-0}" != "1" ]; then
    main
fi
