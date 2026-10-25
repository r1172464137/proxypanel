#!/usr/bin/env bash
# proxypanel Agent 一键安装（Linux + systemd）。
#
#   curl -fsSL https://github.com/r1172464137/proxypanel/releases/latest/download/install.sh | sudo bash
#
# 交互式询问：面板地址、一次性注册码、Xray 版本。
# 也可以完全非交互：
#   sudo bash install.sh -key <注册码> --panel-url https://panel.example.com
#
# 行为：
#   1. 检测架构（amd64 / arm64），从本 Release 下载同版本 Agent 并核对 SHA256
#   2. 从官方 Xray Release 下载核心，用官方 .dgst 的 SHA2-256 校验
#   3. 写配置、建系统账号、安装 systemd unit、启动并等待注册
#
# 本脚本只做安装；升级与回退沿用 deploy-agent.sh 的维护流程。
set -euo pipefail

readonly INSTALLER_VERSION="1.0"
readonly RELEASE_REPO="r1172464137/proxypanel"
readonly SERVICE_NAME="proxypanel-agent"
readonly SERVICE_ACCOUNT="proxypanel-agent"
readonly CONFIG_DIR="/etc/proxypanel"
readonly CONFIG_FILE="$CONFIG_DIR/agent.json"
readonly STATE_DIR="/var/lib/proxypanel-agent"
readonly XRAY_BASE="/opt/proxypanel/xray"
readonly AGENT_DIR="/opt/proxypanel/agent"
readonly AGENT_BIN="$AGENT_DIR/agent"
readonly UNIT_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
readonly CA_FILE="/usr/local/share/ca-certificates/proxypanel-panel.crt"
readonly SUPPORTED_XRAY_VERSIONS=("26.3.27" "26.3.23")
readonly MANAGER_BIN="/usr/bin/proxypanel-agent"
readonly RELEASE_REPO="r1172464137/proxypanel"

ARG_PANEL_URL="${PANEL_URL:-}"
ARG_REGISTRATION_CODE="${REGISTRATION_CODE:-}"
ARG_XRAY_VERSION="${XRAY_VERSION:-}"
ARG_PANEL_CA=""
ASSUME_YES=""
DRY_RUN=""
VERSION="latest"
WORK_DIR=""

if [ -t 1 ]; then
  C_RESET=$'\033[0m'; C_BOLD=$'\033[1m'; C_DIM=$'\033[2m'
  C_OK=$'\033[32m'; C_WARN=$'\033[33m'; C_ERR=$'\033[31m'
else
  C_RESET=""; C_BOLD=""; C_DIM=""; C_OK=""; C_WARN=""; C_ERR=""
fi

info() { printf '%s\n' "$*"; }
step() { printf '\n%s==>%s %s\n' "$C_BOLD" "$C_RESET" "$*"; }
ok()   { printf '%s  ok%s %s\n' "$C_OK" "$C_RESET" "$*"; }
warn() { printf '%s  警告%s %s\n' "$C_WARN" "$C_RESET" "$*" >&2; }
die()  { printf '%s  错误%s %s\n' "$C_ERR" "$C_RESET" "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
proxypanel Agent 一键安装（Linux + systemd）

用法：
  sudo bash install.sh [选项]

选项：
  -key, --registration-code C   面板生成的一次性注册码
  --panel-url URL               面板地址（https://...）
  --xray-version V              Xray 版本：26.3.27 / 26.3.23（默认 26.3.27）
  --panel-ca PATH               面板使用私有 CA 时的 CA 证书
  --version TAG                 安装指定 Release 版本（默认 latest）
  --dry-run                     只打印计划，不做修改
  -y, --yes                     跳过确认
  -h, --help                    显示帮助

环境变量：PANEL_URL REGISTRATION_CODE XRAY_VERSION
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    -key|--key|--registration-code) ARG_REGISTRATION_CODE=${2:?}; shift 2 ;;
    --panel-url)      ARG_PANEL_URL=${2:?}; shift 2 ;;
    --xray-version)   ARG_XRAY_VERSION=${2:?}; shift 2 ;;
    --panel-ca)       ARG_PANEL_CA=${2:?}; shift 2 ;;
    --version)        VERSION=${2:?}; shift 2 ;;
    --dry-run)        DRY_RUN=1; shift ;;
    -y|--yes)         ASSUME_YES=1; shift ;;
    -h|--help)        usage; exit 0 ;;
    *)                die "未知参数：$1（用 --help 查看用法）" ;;
  esac
done

ask() {
  local __var=$1 prompt=$2 default=${3:-} reply=""
  if [ -n "$default" ]; then
    read -r -p "$prompt [$default]: " reply || die "输入被中断"
  else
    read -r -p "$prompt: " reply || die "输入被中断"
  fi
  [ -z "$reply" ] && reply=$default
  printf -v "$__var" '%s' "$reply"
}

ask_secret() {
  local __var=$1 prompt=$2 reply=""
  read -r -s -p "$prompt: " reply || die "输入被中断"
  printf '\n'
  printf -v "$__var" '%s' "$reply"
}

confirm() {
  local prompt=$1
  [ -n "$ASSUME_YES" ] && return 0
  local reply=""
  read -r -p "$prompt [y/N]: " reply || die "输入被中断"
  case "${reply:-n}" in y|Y|yes|YES) return 0 ;; *) return 1 ;; esac
}

require_root() { [ "$(id -u)" -eq 0 ] || die "需要 root 权限运行（请用 sudo）"; }
require_linux() { [ "$(uname -s)" = "Linux" ] || die "本脚本只支持 Linux（当前：$(uname -s)）"; }

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) die "不支持的架构：$(uname -m)（已支持 x86_64 / aarch64）" ;;
  esac
}

require_systemd() {
  command -v systemctl >/dev/null 2>&1 || die "找不到 systemctl，本脚本依赖 systemd"
  [ -d /run/systemd/system ] || die "systemd 未作为 init 运行，无法安装服务"
}

check_tools() {
  local missing=()
  for tool in curl sha256sum unzip install useradd realpath mktemp; do
    command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
  done
  if [ ${#missing[@]} -gt 0 ]; then
    warn "缺少依赖：${missing[*]}"
    die "请先安装：apt-get install -y curl coreutils unzip 或 dnf install -y curl coreutils unzip"
  fi
  if ! command -v runuser >/dev/null 2>&1 && ! command -v sudo >/dev/null 2>&1; then
    die "缺少 runuser 或 sudo，无法以服务账号身份做配置检查"
  fi
}

detect_nologin() {
  local candidate
  for candidate in /usr/sbin/nologin /sbin/nologin /bin/false; do
    [ -x "$candidate" ] && { printf '%s' "$candidate"; return 0; }
  done
  printf '/bin/false'
}

normalize_state_dir() {
  local value=${1:-}
  while [[ $value == */ && $value != / ]]; do value=${value%/}; done
  printf '%s' "$value"
}

validate_state_dir() {
  local value
  value=$(normalize_state_dir "${1-}")
  [[ $value =~ ^/[A-Za-z0-9_./-]+$ ]] || die "状态目录必须是无空白/控制字符的绝对路径：$value"
  case "$value" in
    /|/var|/var/lib|/opt|/srv|/mnt|/media|/etc|/etc/*|/usr|/usr/*|/bin|/bin/*|/sbin|/sbin/*|\
    /lib|/lib/*|/boot|/boot/*|/dev|/dev/*|/proc|/proc/*|/sys|/sys/*|/run|/run/*|\
    /var/log|/var/log/*|/var/lib/systemd|/var/lib/systemd/*|/var/lib/docker|/var/lib/docker/*|\
    /var/lib/postgresql|/var/lib/postgresql/*|/var/lib/mysql|/var/lib/mysql/*|\
    /var/lib/redis|/var/lib/redis/*|/tmp|/tmp/*|/var/tmp|/var/tmp/*|/home|/home/*|\
    /root|/root/*|/opt/proxypanel|/opt/proxypanel/*)
      die "拒绝将系统目录用作 Agent 状态目录：$value" ;;
  esac
  local resolved
  resolved=$(realpath -m -- "$value")
  [ "$resolved" = "$value" ] || die "状态路径必须规范化且不能穿过符号链接或包含 ..：$value"
}

collect_inputs() {
  step "收集安装参数"

  if [ -z "$ARG_PANEL_URL" ]; then
    ask ARG_PANEL_URL "面板地址（https://...）"
  fi
  case "$ARG_PANEL_URL" in
    http://127.0.0.1*|http://localhost*) warn "允许回环 http，仅用于本机调试" ;;
    https://*) ;;
    http://*) die "面板地址必须是 https://（当前：$ARG_PANEL_URL）" ;;
    *) die "面板地址必须以 https:// 开头（当前：$ARG_PANEL_URL）" ;;
  esac
  ARG_PANEL_URL=${ARG_PANEL_URL%/}

  if [ -z "$ARG_REGISTRATION_CODE" ]; then
    ask_secret ARG_REGISTRATION_CODE "一次性注册码（面板「服务器 → 一键安装」中生成，15 分钟有效）"
  fi
  [ -n "$ARG_REGISTRATION_CODE" ] || die "注册码不能为空"

  if [ -z "$ARG_XRAY_VERSION" ]; then
    info ""
    info "Xray 版本（Agent 只接受这两个）："
    local i=1 v
    for v in "${SUPPORTED_XRAY_VERSIONS[@]}"; do
      info "  $i) $v"
      i=$((i + 1))
    done
    local choice
    ask choice "选择序号" "1"
    case "$choice" in
      1) ARG_XRAY_VERSION=${SUPPORTED_XRAY_VERSIONS[0]} ;;
      2) ARG_XRAY_VERSION=${SUPPORTED_XRAY_VERSIONS[1]} ;;
      *) die "无效的选择：$choice" ;;
    esac
  fi
  local supported=""
  for v in "${SUPPORTED_XRAY_VERSIONS[@]}"; do
    [ "$v" = "$ARG_XRAY_VERSION" ] && supported="yes"
  done
  [ -n "$supported" ] || die "不支持的 Xray 版本：$ARG_XRAY_VERSION（可选：${SUPPORTED_XRAY_VERSIONS[*]}）"

  ARG_STATE_DIR=$(normalize_state_dir "$STATE_DIR")
  validate_state_dir "$ARG_STATE_DIR"

  info ""
  info "${C_BOLD}将要执行：${C_RESET}"
  info "  发布版本      : $VERSION"
  info "  面板地址      : $ARG_PANEL_URL"
  info "  架构          : linux-$ARCH"
  info "  Xray 版本     : $ARG_XRAY_VERSION → $XRAY_BASE/$ARG_XRAY_VERSION/xray"
  info "  Agent 安装到  : $AGENT_BIN"
  info "  配置文件      : $CONFIG_FILE（root:$SERVICE_ACCOUNT 0640）"
  info "  状态目录      : $ARG_STATE_DIR（$SERVICE_ACCOUNT 0700）"
  info "  服务名        : $SERVICE_NAME"

  if [ -n "$DRY_RUN" ]; then
    warn "--dry-run：未做任何修改"
    exit 0
  fi
  confirm "确认开始安装？" || die "已取消"
}

create_account() {
  step "创建系统账号 $SERVICE_ACCOUNT"
  if id "$SERVICE_ACCOUNT" >/dev/null 2>&1; then
    ok "账号已存在，复用"
    return 0
  fi
  local shell_path
  shell_path=$(detect_nologin)
  useradd --system --user-group --no-create-home --shell "$shell_path" "$SERVICE_ACCOUNT"
  ok "已创建（无登录 shell、无 home）"
}

prepare_workdir() {
  WORK_DIR=$(mktemp -d)
  trap 'rm -rf "${WORK_DIR:-}"' EXIT
}

install_xray() {
  local version=$1
  step "安装官方 Xray $version"
  local asset base
  case "$ARCH" in
    amd64) asset="Xray-linux-64.zip" ;;
    arm64) asset="Xray-linux-arm64-v8a.zip" ;;
  esac
  base="https://github.com/XTLS/Xray-core/releases/download/v${version}"

  curl -fsSL --retry 3 --connect-timeout 20 -o "$WORK_DIR/$asset" "$base/$asset" || die "下载失败：$base/$asset"
  curl -fsSL --retry 3 --connect-timeout 20 -o "$WORK_DIR/$asset.dgst" "$base/$asset.dgst" || die "下载官方摘要失败"

  local expected actual
  expected=$(sed -n 's/.*SHA2-256=[[:space:]]*\([0-9a-fA-F]\{64\}\).*/\1/p' "$WORK_DIR/$asset.dgst" | head -n1 | tr 'A-F' 'a-f')
  [ -n "$expected" ] || die "官方 .dgst 中没有可解析的 SHA2-256，拒绝继续"
  actual=$(sha256sum "$WORK_DIR/$asset" | awk '{print $1}')
  [ "$expected" = "$actual" ] || die "Xray SHA256 不匹配（官方 $expected，实际 $actual）"
  ok "官方摘要校验通过"

  unzip -q -o "$WORK_DIR/$asset" -d "$WORK_DIR/xray" || die "解压失败（需要 unzip）"
  [ -f "$WORK_DIR/xray/xray" ] || die "压缩包内没有 xray"
  "$WORK_DIR/xray/xray" version 2>&1 | head -n1 | grep -q "$version" || die "核心未报告预期版本 $version"
  ok "官方核心版本确认为 $version"

  local dest="$XRAY_BASE/$version"
  install -d -m 0755 "$dest"
  install -m 0755 "$WORK_DIR/xray/xray" "$dest/xray"
  [ -f "$WORK_DIR/xray/geoip.dat" ] && install -m 0644 "$WORK_DIR/xray/geoip.dat" "$dest/geoip.dat"
  [ -f "$WORK_DIR/xray/geosite.dat" ] && install -m 0644 "$WORK_DIR/xray/geosite.dat" "$dest/geosite.dat"
  chown -R root:root "$XRAY_BASE"
  ok "已安装到 $dest"

  # Drop the temporary zip so the next download starts clean.
  rm -rf "$WORK_DIR/xray" "$WORK_DIR/$asset" "$WORK_DIR/$asset.dgst"
}

# Downloads the Agent binary from this same GitHub Release and verifies it
# against SHA256SUMS from that release. No unverified binary is ever executed.
install_agent_binary() {
  step "下载并校验 Agent（$VERSION / linux-$ARCH）"
  local base
  if [ "$VERSION" = "latest" ]; then
    base="https://github.com/${RELEASE_REPO}/releases/latest/download"
  else
    base="https://github.com/${RELEASE_REPO}/releases/download/${VERSION}"
  fi
  local asset="proxypanel-agent-linux-${ARCH}"

  curl -fsSL --retry 3 --connect-timeout 20 -o "$WORK_DIR/$asset" "$base/$asset" || die "下载 Agent 失败：$base/$asset"
  curl -fsSL --retry 3 --connect-timeout 20 -o "$WORK_DIR/SHA256SUMS" "$base/SHA256SUMS" || die "下载 SHA256SUMS 失败"

  local expected actual
  expected=$(awk -v f="$asset" '$2 == f || $2 == "./"f {print $1}' "$WORK_DIR/SHA256SUMS" | head -n1 | tr 'A-F' 'a-f')
  [ -n "$expected" ] || die "SHA256SUMS 中没有 $asset 的条目，拒绝安装"
  actual=$(sha256sum "$WORK_DIR/$asset" | awk '{print $1}')
  [ "$expected" = "$actual" ] || die "Agent SHA256 不匹配（发布 $expected，实际 $actual）"
  ok "Agent 摘要校验通过：$actual"

  install -d -m 0755 "$AGENT_DIR"
  install -m 0755 "$WORK_DIR/$asset" "$AGENT_BIN"

  local reported
  reported=$("$AGENT_BIN" -version 2>&1 | head -n1)
  case "$reported" in
    *proxypanel-agent*) ok "Agent 版本：$reported" ;;
    *) die "该文件不是预期的 Agent 构建（-version 输出：$reported）" ;;
  esac

  install_manager
}

# install_manager ships the管理脚本 next to the service so the operator can use
# `proxypanel-agent status|log|update|config` instead of raw systemctl.
install_manager() {
  local asset="proxypanel-agent"
  if [ -f "$WORK_DIR/$asset" ]; then
    bash -n "$WORK_DIR/$asset" || die "发布的管理脚本语法错误，拒绝安装"
    install -m 0755 "$WORK_DIR/$asset" "$MANAGER_BIN"
    ok "管理脚本已安装：$MANAGER_BIN"
    return 0
  fi
  # Older releases did not ship it; fetch from the same release if available.
  local base
  if [ "$VERSION" = "latest" ]; then
    base="https://github.com/${RELEASE_REPO}/releases/latest/download"
  else
    base="https://github.com/${RELEASE_REPO}/releases/download/${VERSION}"
  fi
  if curl -fsSL --retry 2 --connect-timeout 15 -o "$WORK_DIR/$asset" "$base/$asset" 2>/dev/null; then
    bash -n "$WORK_DIR/$asset" || die "下载的管理脚本语法错误，拒绝安装"
    install -m 0755 "$WORK_DIR/$asset" "$MANAGER_BIN"
    ok "管理脚本已安装：$MANAGER_BIN"
  else
    warn "本次发布未包含管理脚本，只安装了服务（可用 systemctl 管理）"
  fi
}

prepare_state() {
  step "准备状态目录 $ARG_STATE_DIR"
  if [ -d "$ARG_STATE_DIR" ]; then
    ok "目录已存在，保留原有内容（身份、计量基线、待上传队列都不动）"
  else
    install -d -m 0700 -o "$SERVICE_ACCOUNT" -g "$SERVICE_ACCOUNT" "$ARG_STATE_DIR"
    ok "已创建"
  fi
  chown "$SERVICE_ACCOUNT:$SERVICE_ACCOUNT" "$ARG_STATE_DIR"
  chmod 0700 "$ARG_STATE_DIR"
}

write_config() {
  step "写入配置 $CONFIG_FILE"
  install -d -m 0755 "$CONFIG_DIR"

  if [ -f "$CONFIG_FILE" ]; then
    warn "已存在配置文件"
    if confirm "保留现有配置（不覆盖）？"; then
      ok "保留现有配置"
      ARG_KEEP_CONFIG=1
      return 0
    fi
    local backup="${CONFIG_FILE}.$(date -u +%Y%m%dT%H%M%SZ).bak"
    cp -p "$CONFIG_FILE" "$backup"
    chmod 0600 "$backup"
    ok "旧配置已备份到 $backup"
  fi
  ARG_KEEP_CONFIG=""

  umask 077
  cat > "$CONFIG_FILE" <<EOF
{
  "panel_url": "${ARG_PANEL_URL}",
  "registration_code": "${ARG_REGISTRATION_CODE}",
  "xray_path": "${XRAY_BASE}/${ARG_XRAY_VERSION}/xray",
  "state_dir": "${ARG_STATE_DIR}",
  "poll_interval_seconds": 5,
  "stats_interval_seconds": 5,
  "heartbeat_interval_seconds": 15,
  "allow_loopback_http": false
}
EOF
  chown root:"$SERVICE_ACCOUNT" "$CONFIG_FILE"
  chmod 0640 "$CONFIG_FILE"
  ok "已写入（root:$SERVICE_ACCOUNT 0640）"
}

install_panel_ca() {
  [ -n "$ARG_PANEL_CA" ] || return 0
  step "安装私有 CA"
  [ -f "$ARG_PANEL_CA" ] || die "找不到 CA 文件：$ARG_PANEL_CA"
  install -m 0644 "$ARG_PANEL_CA" "$CA_FILE"
  if command -v update-ca-certificates >/dev/null 2>&1; then
    update-ca-certificates >/dev/null 2>&1 || warn "update-ca-certificates 返回非零，请手工确认"
    ok "已加入系统信任库"
  else
    ok "已安装到 $CA_FILE；本机没有 update-ca-certificates"
  fi
}

render_unit() {
  local state_path
  state_path=$(normalize_state_dir "${ARG_STATE_DIR:-$STATE_DIR}")
  validate_state_dir "$state_path"
  cat <<EOF
[Unit]
Description=Proxypanel Xray node agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_ACCOUNT
Group=$SERVICE_ACCOUNT
WorkingDirectory=$AGENT_DIR
ExecStart=$AGENT_BIN -config $CONFIG_FILE
Restart=on-failure
RestartSec=5
TimeoutStopSec=75
KillMode=control-group
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$state_path
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
EOF
  if [ -n "$ARG_PANEL_CA" ]; then
    printf 'Environment=SSL_CERT_FILE=%s\n' "$CA_FILE"
  fi
  cat <<EOF

[Install]
WantedBy=multi-user.target
EOF
}

install_unit() {
  step "安装 systemd 单元"
  local unit_tmp
  unit_tmp=$(mktemp)
  chmod 0600 "$unit_tmp"
  local had_exit_trap
  had_exit_trap=$(trap -p EXIT)
  trap "rm -f -- '$unit_tmp'" EXIT
  render_unit > "$unit_tmp" || die "无法生成 systemd 单元"
  install -m 0644 "$unit_tmp" "$UNIT_FILE"
  rm -f -- "$unit_tmp"
  if [ -n "$had_exit_trap" ]; then eval "$had_exit_trap"; else trap - EXIT; fi
  systemctl daemon-reload
  ok "单元已安装：$UNIT_FILE"
}

run_config_check() {
  step "以服务账号身份检查配置"
  local runner=()
  if command -v runuser >/dev/null 2>&1; then
    runner=(runuser -u "$SERVICE_ACCOUNT" --)
  else
    runner=(sudo -u "$SERVICE_ACCOUNT" --)
  fi
  local output
  if output=$("${runner[@]}" "$AGENT_BIN" -config "$CONFIG_FILE" -check 2>&1); then
    ok "配置检查通过"
  else
    printf '%s\n' "$output" >&2
    die "配置检查失败，服务未启动。请修正后重跑。"
  fi
}

start_service() {
  step "启用并启动 $SERVICE_NAME"
  systemctl enable "$SERVICE_NAME" >/dev/null 2>&1 || warn "enable 返回非零"
  systemctl restart "$SERVICE_NAME"
  local i state
  for i in $(seq 1 20); do
    state=$(systemctl is-active "$SERVICE_NAME" 2>/dev/null || true)
    [ "$state" = "active" ] && break
    sleep 1
  done
  if [ "$state" = "active" ]; then
    ok "服务运行中"
  else
    systemctl status "$SERVICE_NAME" --no-pager -l | head -n 30 >&2 || true
    die "服务未能启动，请查看 journalctl -u $SERVICE_NAME -n 100"
  fi
}

wait_registration() {
  step "等待 Agent 注册与心跳"
  local i found=""
  for i in $(seq 1 30); do
    if journalctl -u "$SERVICE_NAME" --since "-2min" --no-pager 2>/dev/null \
        | grep -qiE 'registered|heartbeat|snapshot'; then
      found="yes"
      break
    fi
    sleep 2
  done
  if [ -n "$found" ]; then
    ok "Agent 已与面板通信"
  else
    warn "2 分钟内未在日志中看到注册/心跳，请检查："
    warn "  journalctl -u $SERVICE_NAME -n 100"
    warn "  面板地址是否可达、注册码是否已过期"
  fi
}

report() {
  step "完成"
  info "服务状态：$(systemctl is-active "$SERVICE_NAME")"
  info ""
  info "接下来："
  info "  1. 回到面板确认服务器显示 Agent 在线"
  info "  2. 创建节点并确认 已应用 revision 追上 期望 revision"
  info "  3. 用真实客户端连一次流量，才算真正可用"
  info ""
  info "常用命令："
  if [ -x "$MANAGER_BIN" ]; then
    info "  proxypanel-agent            # 交互式管理菜单"
    info "  proxypanel-agent status"
    info "  proxypanel-agent log -f"
    info "  proxypanel-agent update"
  fi
  info "  systemctl status $SERVICE_NAME"
  info "  journalctl -u $SERVICE_NAME -f"
  info "  systemctl restart $SERVICE_NAME"
  info ""
  info "${C_DIM}注意：注册码已写入 $CONFIG_FILE，首次注册成功后可以删除 registration_code 字段。"
  info "      Agent token 已存在状态目录。${C_RESET}"
  info "${C_DIM}绝对不要删除 $ARG_STATE_DIR：里面有 Agent 身份、计量基线和未上传的用量队列。${C_RESET}"
}

main() {
  info "${C_BOLD}proxypanel Agent 安装脚本${C_RESET} v${INSTALLER_VERSION}"
  info "${C_DIM}只装 Agent 与官方 Xray；不改面板、不动防火墙${C_RESET}"

  require_root
  require_linux
  detect_arch
  require_systemd
  check_tools
  collect_inputs
  prepare_workdir

  create_account
  install_agent_binary
  install_xray "$ARG_XRAY_VERSION"
  prepare_state
  write_config
  if [ -n "${ARG_KEEP_CONFIG:-}" ]; then
    ok "沿用现有配置、服务单元与 CA，跳过配置检查与重启"
    report
    return 0
  fi
  install_panel_ca
  install_unit
  run_config_check
  start_service
  wait_registration
  report
}

main "$@"
