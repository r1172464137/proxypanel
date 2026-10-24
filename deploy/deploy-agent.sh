#!/usr/bin/env bash
# 节点端 Agent 交互式部署脚本（裸机 + systemd，不使用 Docker）。
#
# 它做什么：
#   1. 检查主机环境（root、systemd、架构、依赖工具、磁盘）
#   2. 按官方发行包安装 Xray 核心，并用官方 .dgst 核对 SHA2-256
#   3. 安装 Agent 可执行文件（本地文件或 URL + SHA256 校验）
#   4. 创建系统账号与状态目录，写入 /etc/proxypanel/agent.json（0600/0640 权限）
#   5. 安装 systemd 单元，先 -check 再启用启动
#
# 它不做什么：
#   - 不安装/重启数据库，不碰面板
#   - 不修改防火墙、不安装 Docker、不下载未校验的二进制
#   - 不删除已存在的状态目录（里面有 Agent 身份、计量基线和未上传队列）
#
# 用法：
#   sudo bash deploy-agent.sh                 # 交互式
#   sudo bash deploy-agent.sh --dry-run       # 只打印将要执行的操作
#   sudo bash deploy-agent.sh --yes ...       # 非交互（参数见 --help）
#
# systemd 安装路径尚未在目标生产机验证；首次使用先在隔离节点验收。
# 脚本级回归不等于已安装/重启过真实宿主服务。
set -euo pipefail

readonly SCRIPT_VERSION="1.1"

# 与 internal/agent/xray/version.go 的闭合白名单保持一致；
# 版本不符时 Agent 会明确拒绝启动，不会静默降级。
readonly SUPPORTED_XRAY_VERSIONS=("26.3.27" "26.3.23")

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

# ReadWritePaths must not grant writes to system files or other services' data.
# This deny list is defence in depth, not an exhaustive security boundary.
# ProtectHome/PrivateTmp also make home and temporary paths unusable for state.
readonly -a FORBIDDEN_STATE_TREES=(
  /etc /usr /bin /sbin /lib /lib32 /lib64 /libx32 /boot /dev /proc /sys
  /run /var/run /var/log /var/lock /var/spool /var/lib/systemd
  /var/lib/docker /var/lib/postgresql /var/lib/pgsql /var/lib/mysql
  /var/lib/redis /tmp /var/tmp /home /root /opt/proxypanel
)
readonly -a FORBIDDEN_STATE_EXACT=(
  / /var /var/lib /opt /srv /mnt /media
)

ASSUME_YES="${PROXYPANEL_INSTALL_ASSUME_YES:-}"
DRY_RUN=""
ARG_AGENT_BINARY=""
ARG_AGENT_URL=""
ARG_AGENT_SHA256=""
ARG_XRAY_VERSION="${PROXYPANEL_INSTALL_XRAY_VERSION:-}"
ARG_PANEL_URL="${PROXYPANEL_INSTALL_PANEL_URL:-}"
ARG_REGISTRATION_CODE=""
ARG_PANEL_CA=""
ARG_STATE_DIR=""
ARCH=""
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
节点端 Agent 交互式部署（裸机 + systemd）

用法：
  sudo bash deploy-agent.sh [选项]

选项：
  --agent-binary PATH     使用本地已有的 Agent 可执行文件
  --agent-url URL         从 URL 下载 Agent 可执行文件（必须同时给 --agent-sha256）
  --agent-sha256 HEX      下载文件的 SHA256（不校验会拒绝执行）
  --xray-version V        指定 Xray 版本，可选：26.3.27 / 26.3.23
  --panel-url URL         面板地址（必须是 https://）
  -key C                  面板生成的一次性注册码（--registration-code 的简写）
  --registration-code C   一次性注册码（会进入 shell 历史，建议交互输入）
  --panel-ca PATH         面板使用私有 CA 时的 CA 证书文件
  --state-dir PATH        状态目录，默认 /var/lib/proxypanel-agent
  --dry-run               只打印将要执行的操作，不做任何修改
  -y, --yes               跳过确认（用于已有参数的非交互部署）
  -h, --help              显示本帮助

说明：
  - 官方 Xray 从 GitHub Releases 下载，并用同发布的 .dgst 校验 SHA2-256。
  - Agent 二进制必须能通过 -version 输出 proxypanel-agent。
  - 已存在的状态目录永远不会被删除或重建。
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --agent-binary)      ARG_AGENT_BINARY=${2:?}; shift 2 ;;
    --agent-url)         ARG_AGENT_URL=${2:?}; shift 2 ;;
    --agent-sha256)      ARG_AGENT_SHA256=${2:?}; shift 2 ;;
    --xray-version)      ARG_XRAY_VERSION=${2:?}; shift 2 ;;
    --panel-url)         ARG_PANEL_URL=${2:?}; shift 2 ;;
    -key|--key|--registration-code) ARG_REGISTRATION_CODE=${2:?}; shift 2 ;;
    --panel-ca)          ARG_PANEL_CA=${2:?}; shift 2 ;;
    --state-dir)         ARG_STATE_DIR=${2:?}; shift 2 ;;
    --dry-run)           DRY_RUN=1; shift ;;
    -y|--yes)            ASSUME_YES=1; shift ;;
    -h|--help)           usage; exit 0 ;;
    *)                   die "未知参数：$1（用 --help 查看用法）" ;;
  esac
done

# ---------------------------------------------------------------- 交互辅助

ask() { # ask <变量名> <提示> [默认值]
  local __var=$1 prompt=$2 default=${3:-} reply=""
  if [ -n "$default" ]; then
    read -r -p "$prompt [$default]: " reply || die "输入被中断"
  else
    read -r -p "$prompt: " reply || die "输入被中断"
  fi
  [ -z "$reply" ] && reply=$default
  printf -v "$__var" '%s' "$reply"
}

ask_secret() { # 不回显；注册码等同凭据，不留在终端历史里
  local __var=$1 prompt=$2 reply=""
  read -r -s -p "$prompt: " reply || die "输入被中断"
  printf '\n'
  printf -v "$__var" '%s' "$reply"
}

confirm() { # confirm <提示> [默认 n]
  local prompt=$1
  [ -n "$ASSUME_YES" ] && return 0
  local reply=""
  read -r -p "$prompt [y/N]: " reply || die "输入被中断"
  case "${reply:-n}" in y|Y|yes|YES) return 0 ;; *) return 1 ;; esac
}

# ---------------------------------------------------------------- 预检

require_root() {
  [ "$(id -u)" -eq 0 ] || die "需要 root 权限运行（请用 sudo）"
}

require_linux() {
  [ "$(uname -s)" = "Linux" ] || die "本脚本只支持 Linux（当前：$(uname -s)）"
}

detect_arch() {
  local machine
  machine=$(uname -m)
  case "$machine" in
    x86_64|amd64)  ARCH="x86_64" ;;
    aarch64|arm64) ARCH="aarch64" ;;
    *) die "不支持的架构：$machine（已支持 x86_64 / aarch64）" ;;
  esac
  # 不能写成 `[ ... ] && warn ...`：条件不成立时该语句返回 1，而它是函数最后一句，
  # 在 set -e 下会让整个脚本以 1 退出（真机上就是这么被拦住的）。
  if [ "$ARCH" = "aarch64" ]; then
    warn "aarch64 未做过运行时验收，只在构建层面验证过。"
  fi
}

# The panel-served installer injects same-release artifact URLs and hashes.
# Explicit CLI --agent-* options always win. Missing architecture data falls
# back to the original interactive source selection instead of an unchecked URL.
apply_generated_defaults() {
  [ -z "$ARG_AGENT_BINARY" ] || return 0
  [ -z "$ARG_AGENT_URL" ] || return 0
  case "$ARCH" in
    x86_64)
      ARG_AGENT_URL=${PROXYPANEL_INSTALL_AGENT_URL_AMD64:-}
      ARG_AGENT_SHA256=${PROXYPANEL_INSTALL_AGENT_SHA256_AMD64:-} ;;
    aarch64)
      ARG_AGENT_URL=${PROXYPANEL_INSTALL_AGENT_URL_ARM64:-}
      ARG_AGENT_SHA256=${PROXYPANEL_INSTALL_AGENT_SHA256_ARM64:-} ;;
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
    info "Debian/Ubuntu: apt-get install -y curl coreutils unzip"
    info "RHEL/CentOS:   dnf install -y curl coreutils unzip"
    die "请先安装上述工具"
  fi
  if ! command -v runuser >/dev/null 2>&1 && ! command -v sudo >/dev/null 2>&1; then
    die "缺少 runuser 或 sudo，无法以服务账号身份做配置检查"
  fi
}

check_disk() {
  local avail_kb
  avail_kb=$(df -Pk /opt 2>/dev/null | awk 'NR==2 {print $4}')
  [ -n "${avail_kb:-}" ] || return 0
  if [ "$avail_kb" -lt 204800 ]; then
    warn "/opt 可用空间不足 200MB（当前约 $((avail_kb / 1024))MB）"
    confirm "仍然继续？" || die "已取消"
  fi
}

detect_nologin() {
  for candidate in /usr/sbin/nologin /sbin/nologin /bin/false; do
    [ -x "$candidate" ] && { printf '%s' "$candidate"; return 0; }
  done
  printf '/bin/false'
}

# ---------------------------------------------------------------- 输入收集

collect_inputs() {
  step "收集部署参数"

  if [ -z "$ARG_PANEL_URL" ]; then
    ask ARG_PANEL_URL "面板地址（https://...）"
  fi
  case "$ARG_PANEL_URL" in
    https://*) ;;
    http://*)
      warn "纯 http 仅允许指向本机回环地址，且需要 allow_loopback_http=true。"
      confirm "确认这是本机调试用的 http 面板？" || die "已取消"
      ;;
    *) die "面板地址必须以 https:// 开头（收到：$ARG_PANEL_URL）" ;;
  esac
  case "$ARG_PANEL_URL" in
    */) ARG_PANEL_URL=${ARG_PANEL_URL%/} ;;
  esac

  if [ -z "$ARG_REGISTRATION_CODE" ]; then
    ask_secret ARG_REGISTRATION_CODE "一次性注册码（输入不显示）"
  fi
  [ -n "$ARG_REGISTRATION_CODE" ] || die "注册码不能为空（请在面板「服务器」页生成）"

  if [ -z "$ARG_XRAY_VERSION" ]; then
    info ""
    info "可选 Xray 版本（Agent 只接受这个闭合白名单）："
    local i=1 v
    for v in "${SUPPORTED_XRAY_VERSIONS[@]}"; do
      info "  $i) $v"
      i=$((i + 1))
    done
    local choice
    ask choice "选择版本序号" "1"
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

  if [ -z "$ARG_AGENT_BINARY" ] && [ -z "$ARG_AGENT_URL" ]; then
    info ""
    info "Agent 可执行文件来源："
    info "  1) 本地已有文件（例如发布包 build/linux-amd64/agent）"
    info "  2) 从 URL 下载（需要提供 SHA256）"
    local src
    ask src "选择来源序号" "1"
    case "$src" in
      1) ask ARG_AGENT_BINARY "Agent 可执行文件路径" ;;
      2) ask ARG_AGENT_URL "下载地址"
         ask ARG_AGENT_SHA256 "该文件的 SHA256" ;;
      *) die "无效的选择：$src" ;;
    esac
  fi

  if [ -n "$ARG_AGENT_URL" ]; then
    case "$ARG_AGENT_URL" in https://*) ;; *) die "Agent 下载地址必须使用 https://" ;; esac
    [ -n "$ARG_AGENT_SHA256" ] || die "从 URL 安装必须提供 --agent-sha256，否则无法校验下载内容"
    [[ $ARG_AGENT_SHA256 =~ ^[0-9a-fA-F]{64}$ ]] || die "Agent SHA256 必须是 64 位十六进制"
  fi

  ARG_STATE_DIR=$(normalize_state_dir "${ARG_STATE_DIR:-$STATE_DIR}")
  validate_state_dir "$ARG_STATE_DIR"

  # 非交互模式（-y）下不再提问可选值：没有 --panel-ca 就按"不用私有 CA"处理，
  # 否则 read 在无 tty 的环境里会立即 EOF 并把部署判定为失败。
  if [ -z "$ARG_PANEL_CA" ] && [ -z "$ASSUME_YES" ] && confirm "面板是否使用私有 CA 证书？（公共证书选 N）"; then
    ask ARG_PANEL_CA "CA 证书文件路径"
  fi

  info ""
  info "${C_BOLD}将要执行：${C_RESET}"
  info "  面板地址      : $ARG_PANEL_URL"
  info "  Xray 版本     : $ARG_XRAY_VERSION  → $XRAY_BASE/$ARG_XRAY_VERSION/xray"
  info "  Agent 来源    : ${ARG_AGENT_BINARY:-$ARG_AGENT_URL}"
  info "  安装路径      : $AGENT_BIN"
  info "  配置文件      : $CONFIG_FILE（root:$SERVICE_ACCOUNT 0640）"
  info "  状态目录      : $ARG_STATE_DIR（$SERVICE_ACCOUNT 0700）"
  info "  服务名        : $SERVICE_NAME"
  [ -n "$ARG_PANEL_CA" ] && info "  私有 CA       : $ARG_PANEL_CA"
  info ""

  if [ -n "$DRY_RUN" ]; then
    warn "--dry-run：以上为计划，未做任何修改"
    exit 0
  fi
  confirm "确认开始安装？" || die "已取消"
}

# ---------------------------------------------------------------- 安装步骤

create_account() {
  step "创建系统账号 $SERVICE_ACCOUNT"
  if id "$SERVICE_ACCOUNT" >/dev/null 2>&1; then
    ok "账号已存在，复用"
  else
    local shell_path
    shell_path=$(detect_nologin)
    useradd --system --user-group --no-create-home --shell "$shell_path" "$SERVICE_ACCOUNT"
    ok "已创建系统账号（无登录 shell、无 home）"
  fi
}

install_xray() {
  local version=$1
  step "安装官方 Xray $version"

  local asset
  case "$ARCH" in
    x86_64)  asset="Xray-linux-64.zip" ;;
    aarch64) asset="Xray-linux-arm64-v8a.zip" ;;
  esac
  local base="https://github.com/XTLS/Xray-core/releases/download/v${version}"

  WORK_DIR=$(mktemp -d)
  trap 'rm -rf "${WORK_DIR:-}"' EXIT

  info "下载 $asset …"
  curl -fsSL --retry 3 --connect-timeout 20 -o "$WORK_DIR/$asset" "$base/$asset" \
    || die "下载失败：$base/$asset"
  curl -fsSL --retry 3 --connect-timeout 20 -o "$WORK_DIR/$asset.dgst" "$base/$asset.dgst" \
    || die "下载官方摘要失败：$base/$asset.dgst"

  local expected actual
  expected=$(sed -n 's/.*SHA2-256=[[:space:]]*\([0-9a-fA-F]\{64\}\).*/\1/p' "$WORK_DIR/$asset.dgst" | head -n1 | tr 'A-F' 'a-f')
  [ -n "$expected" ] || die "官方 .dgst 中没有可解析的 SHA2-256，拒绝继续"
  actual=$(sha256sum "$WORK_DIR/$asset" | awk '{print $1}')
  [ "$expected" = "$actual" ] || die "SHA256 不匹配（官方 $expected，实际 $actual）"
  ok "官方摘要校验通过：$actual"

  unzip -q -o "$WORK_DIR/$asset" -d "$WORK_DIR/extract" || die "解压失败（需要 unzip）"
  [ -f "$WORK_DIR/extract/xray" ] || die "压缩包内没有 xray 可执行文件"
  [ -f "$WORK_DIR/extract/geoip.dat" ] || die "压缩包内没有 geoip.dat"
  [ -f "$WORK_DIR/extract/geosite.dat" ] || die "压缩包内没有 geosite.dat"

  "$WORK_DIR/extract/xray" version 2>&1 | head -n1 | grep -q "$version" \
    || die "下载的核心未报告预期版本 $version"
  ok "官方核心版本确认为 $version"

  # 核心必须与自己的 geo 数据同目录：Agent 按 xray_path 所在目录解析它们。
  local dest="$XRAY_BASE/$version"
  install -d -m 0755 "$dest"
  install -m 0755 "$WORK_DIR/extract/xray" "$dest/xray"
  install -m 0644 "$WORK_DIR/extract/geoip.dat" "$dest/geoip.dat"
  install -m 0644 "$WORK_DIR/extract/geosite.dat" "$dest/geosite.dat"
  chown -R root:root "$XRAY_BASE"
  ok "已安装到 $dest（root 所有，Agent 只读执行）"
}

install_agent_binary() {
  step "安装 Agent 可执行文件"

  local source_path="$ARG_AGENT_BINARY"
  if [ -n "$ARG_AGENT_URL" ]; then
    source_path="$WORK_DIR/agent-download"
    info "下载 Agent …"
    curl -fsSL --retry 3 --connect-timeout 20 -o "$source_path" "$ARG_AGENT_URL" \
      || die "下载失败：$ARG_AGENT_URL"
    local actual
    actual=$(sha256sum "$source_path" | awk '{print $1}')
    local expected
    expected=$(printf '%s' "$ARG_AGENT_SHA256" | tr 'A-F' 'a-f')
    [ "$expected" = "$actual" ] || die "Agent SHA256 不匹配（期望 $expected，实际 $actual）"
    ok "Agent 下载内容校验通过"
  fi

  [ -f "$source_path" ] || die "找不到 Agent 文件：$source_path"

  install -d -m 0755 "$AGENT_DIR"
  install -m 0755 "$source_path" "$AGENT_BIN"

  local reported
  reported=$("$AGENT_BIN" -version 2>&1 | head -n1)
  case "$reported" in
    *proxypanel-agent*) ok "Agent 版本：$reported" ;;
    *) die "该文件不是预期的 Agent 构建（-version 输出：$reported）" ;;
  esac
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
    if confirm "保留现有配置（不覆盖）？" ; then
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

  # 值只写进文件，不回显到终端。
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
    info "单元已通过 Environment=SSL_CERT_FILE=$CA_FILE 指定它"
  fi
}

# The release installer uses /opt/proxypanel/current; this standalone installer
# uses /opt/proxypanel/agent. Never copy the release unit into this layout.
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
  # A nonexistent SSL_CERT_FILE breaks TLS, so override only for an explicit CA.
  if [ -n "$ARG_PANEL_CA" ]; then
    printf 'Environment=SSL_CERT_FILE=%s\n' "$CA_FILE"
  fi
  cat <<EOF

[Install]
WantedBy=multi-user.target
EOF
}

# Canonical form: absolute, exactly one leading slash, no trailing slash.
normalize_state_dir() {
  local value=${1:-}
  while [[ $value == */ && $value != / ]]; do
    value=${value%/}
  done
  printf '%s' "$value"
}

validate_state_dir() {
  local value part current='' forbidden
  local -a parts=()
  # Normalize first: a typed trailing slash must not change the meaning of the
  # deny list below (/etc/ has to be rejected exactly like /etc).
  value=$(normalize_state_dir "${1-}")
  # No whitespace, controls or systemd %-specifiers: both the unit file and the
  # JSON config embed this value verbatim.
  [[ $value =~ ^/[A-Za-z0-9_./-]+$ ]] || die "状态目录必须是无空白/控制字符的绝对路径：$value"
  for forbidden in "${FORBIDDEN_STATE_EXACT[@]}"; do
    [ "$value" != "$forbidden" ] || die "拒绝将系统目录用作 Agent 状态目录：$value"
  done
  for forbidden in "${FORBIDDEN_STATE_TREES[@]}"; do
    case "$value" in "$forbidden"|"$forbidden"/*)
      die "拒绝将系统目录用作 Agent 状态目录：$value" ;;
    esac
  done
  local resolved
  resolved=$(realpath -m -- "$value")
  [ "$resolved" = "$value" ] || die "状态路径必须规范化且不能穿过符号链接或包含 ..：$value"
  IFS='/' read -r -a parts <<< "$value"
  for part in "${parts[@]}"; do
    [ -n "$part" ] || continue
    current="$current/$part"
    [ ! -L "$current" ] || die "状态路径不能包含符号链接：$current"
  done
}

install_unit() {
  step "安装 systemd 单元"
  local unit_tmp
  # The unit embeds the state path and the CA path. Write it 0600 and clean up
  # through an EXIT trap: validate_state_dir calls die(), which exits the shell
  # immediately, so an explicit `rm` after render_unit would never run.
  unit_tmp=$(mktemp)
  chmod 0600 "$unit_tmp"
  local had_exit_trap
  had_exit_trap=$(trap -p EXIT)
  # shellcheck disable=SC2064 # unit_tmp is expanded now on purpose.
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
    die "配置检查失败，服务未启动。请修正后重跑本脚本。"
  fi
}

start_service() {
  step "启用并启动 $SERVICE_NAME"
  systemctl enable "$SERVICE_NAME" >/dev/null 2>&1 || warn "enable 返回非零"
  systemctl restart "$SERVICE_NAME"
  sleep 3
  if systemctl is-active --quiet "$SERVICE_NAME"; then
    ok "服务运行中"
  else
    systemctl status "$SERVICE_NAME" --no-pager -l | head -n 30 >&2 || true
    die "服务未能启动，请查看 journalctl -u $SERVICE_NAME -n 100"
  fi
}

report() {
  step "完成"
  info "服务状态：$(systemctl is-active "$SERVICE_NAME")"
  info ""
  info "接下来："
  info "  1. 回到面板查看该服务器是否显示 Agent 在线"
  info "  2. 在面板创建节点，确认 applied_revision == desired_revision"
  info "  3. 用真实客户端连一次，才算真正可用"
  info ""
  info "常用命令："
  info "  systemctl status $SERVICE_NAME"
  info "  journalctl -u $SERVICE_NAME -f"
  info "  systemctl restart $SERVICE_NAME"
  info ""
  info "${C_DIM}注意：注册码已写入 $CONFIG_FILE。首次注册成功后可以删除该字段，"
  info "      Agent 的 token 已经存在状态目录里。${C_RESET}"
  info "${C_DIM}绝对不要删除 $ARG_STATE_DIR：里面有 Agent 身份、计量基线和未上传的用量队列。${C_RESET}"
}

# ---------------------------------------------------------------- 主流程

main() {
  info "${C_BOLD}proxypanel 节点端 Agent 部署脚本${C_RESET} v${SCRIPT_VERSION}"
  info "${C_DIM}裸机 + systemd；不使用 Docker；不改动面板与防火墙${C_RESET}"

  require_root
  require_linux
  detect_arch
  apply_generated_defaults
  require_systemd
  check_tools
  check_disk
  collect_inputs

  create_account
  install_xray "$ARG_XRAY_VERSION"
  install_agent_binary
  prepare_state
  write_config
  if [ -n "${ARG_KEEP_CONFIG:-}" ]; then
    # The preserved config may use a different state directory or CA. Do not
    # replace its existing unit or trust store using newly supplied parameters.
    ok "沿用现有配置、服务单元与 CA，跳过配置检查与重启"
    if [ ! -f "$UNIT_FILE" ]; then
      warn "现有配置被保留，但 $UNIT_FILE 不存在：本次没有安装单元，服务不会启动。"
      warn "如确需重新生成单元，请重跑并选择覆盖现有配置。"
    fi
    report
    return 0
  fi

  install_panel_ca
  install_unit
  run_config_check
  start_service
  report
}

# Sourced by tests (BASH_SOURCE is set) or executed normally. Also tolerate
# `bash < deploy-agent.sh`, where BASH_SOURCE is unset and set -u would abort.
if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then
  main "$@"
fi
