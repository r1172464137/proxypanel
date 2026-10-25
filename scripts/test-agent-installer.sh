#!/usr/bin/env bash
# End-to-end regression for deploy/install.sh and deploy/agent.
#
# Runs entirely inside a private user+mount namespace with a tmpfs-backed
# prefix, so the host filesystem and any real systemd unit are untouched.
# Only privileged side effects (useradd/chown/systemctl/runuser) are stubbed;
# every file operation and every download is real.
#
#   bash scripts/test-agent-installer.sh            # offline checks only
#   bash scripts/test-agent-installer.sh --network  # also download release artifacts
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
NETWORK=0
[ "${1:-}" = "--network" ] && NETWORK=1

if [ "$(id -u)" -ne 0 ]; then
  exec unshare -r -m --map-root-user "$0" "$@"
fi

FAILED=0
log() { printf '  ok   %s\n' "$*"; }
fail() { printf '  FAIL %s\n' "$*"; FAILED=1; }
section() { printf '\n== %s ==\n' "$*"; }

SHIM=$(mktemp -d)
# The installer's state-dir deny list rejects /tmp, /var/tmp and /home, so a
# prefix under those paths can never be installed. Use a tmpfs on a neutral
# mount point instead; it disappears with the namespace.
TMPROOT=/srv/ppinstaller-test
if ! (mkdir -p "$TMPROOT" 2>/dev/null && [ -w "$TMPROOT" ]); then
  mount -t tmpfs tmpfs /srv 2>/dev/null || { echo "cannot create a writable test root"; exit 1; }
  mkdir -p "$TMPROOT"
fi
trap 'rm -rf "$SHIM" "$TMPROOT"' EXIT

printf '#!/bin/bash\nexit 0\n' > "$SHIM/useradd"
printf '#!/bin/bash\nexit 0\n' > "$SHIM/groupadd"
printf '#!/bin/bash\nexit 0\n' > "$SHIM/chown"
cat > "$SHIM/systemctl" <<'EOF'
#!/bin/bash
case "$1" in
  is-active) cat "${PP_SHIM_STATE:-/tmp/pp-active}" 2>/dev/null || echo inactive ;;
  restart|start) echo active > "${PP_SHIM_STATE:-/tmp/pp-active}" ;;
  stop) echo inactive > "${PP_SHIM_STATE:-/tmp/pp-active}" ;;
esac
exit 0
EOF
cat > "$SHIM/runuser" <<'EOF'
#!/bin/bash
shift 3
exec "$@"
EOF
cat > "$SHIM/install" <<'EOF'
#!/bin/bash
# Drop -o/-g: the service account is stubbed and does not exist here.
args=()
while [ $# -gt 0 ]; do
  case "$1" in
    -o|-g) shift 2 ;;
    *) args+=("$1"); shift ;;
  esac
done
exec /usr/bin/install "${args[@]}"
EOF
chmod +x "$SHIM"/*
export PATH="$SHIM:$PATH"
export PP_SHIM_STATE="$TMPROOT/active"
export PP_ROOT_PREFIX="$TMPROOT/root"
export PP_STATE_DIR="$TMPROOT/root/agentstate"
mkdir -p "$PP_ROOT_PREFIX/usr/bin"

section "usage and privilege gates"
bash "$ROOT/deploy/install.sh" --help | grep -q 'proxypanel Agent' && log "install.sh --help" || fail "install.sh --help"
bash "$ROOT/deploy/agent" --help >/dev/null 2>&1 && log "agent --help" || fail "agent --help"
bash "$ROOT/deploy/agent" bogus >/dev/null 2>&1
[ $? -eq 2 ] && log "agent rejects unknown subcommand (rc=2)" || fail "agent unknown subcommand rc"

section "input validation"
out=$(bash "$ROOT/deploy/install.sh" -key K --panel-url http://evil.example -y 2>&1)
echo "$out" | grep -q '必须是 https' && log "rejects plaintext public panel URL" || fail "plaintext URL accepted"
out=$(bash "$ROOT/deploy/install.sh" -key K --panel-url https://p.example --xray-version 1.2.3 -y 2>&1)
echo "$out" | grep -q '不支持的 Xray 版本' && log "rejects unsupported Xray version" || fail "bad Xray version accepted"

section "state directory deny list (shipped validator)"
for bad in /etc /etc/ /var/lib /tmp/x / /opt/proxypanel/data /var/lib/docker; do
  out=$(bash -c "source '$ROOT/deploy/deploy-agent.sh'; validate_state_dir '$bad'" 2>&1)
  echo "$out" | grep -qE '拒绝将系统目录|必须是无空白|必须规范化' && log "rejects $bad" || fail "accepted $bad"
done
out=$(bash -c "source '$ROOT/deploy/deploy-agent.sh'; validate_state_dir /var/lib/proxypanel-agent" 2>&1)
[ -z "$out" ] && log "positive control accepted" || fail "rejected a valid state dir"

section "unit contract"
bash -c "source '$ROOT/deploy/deploy-agent.sh'; ARG_STATE_DIR=/var/lib/proxypanel-agent; render_unit" > "$TMPROOT/unit.txt" 2>/dev/null
for pat in '^User=proxypanel-agent$' '^NoNewPrivileges=true$' '^ProtectSystem=strict$' '^Restart=on-failure$'; do
  grep -qE "$pat" "$TMPROOT/unit.txt" && log "unit has $pat" || fail "unit missing $pat"
done

if [ "$NETWORK" = 1 ]; then
  section "real install (downloads release + official Xray)"
  out=$(bash "$ROOT/deploy/install.sh" -key E2E-KEY --panel-url https://p.example --xray-version 26.3.27 -y 2>&1)
  rc=$?
  [ $rc -eq 0 ] && log "installer exit 0" || fail "installer exit $rc"

  for f in "$PP_ROOT_PREFIX/opt/proxypanel/agent/agent" \
           "$PP_ROOT_PREFIX/etc/proxypanel/agent.json" \
           "$PP_ROOT_PREFIX/etc/systemd/system/proxypanel-agent.service" \
           "$PP_ROOT_PREFIX/opt/proxypanel/xray/26.3.27/xray"; do
    [ -e "$f" ] && log "installed ${f#$PP_ROOT_PREFIX}" || fail "missing ${f#$PP_ROOT_PREFIX}"
  done

  "$PP_ROOT_PREFIX/opt/proxypanel/agent/agent" -version | grep -q proxypanel-agent \
    && log "agent binary reports version" || fail "agent version"
  "$PP_ROOT_PREFIX/opt/proxypanel/xray/26.3.27/xray" version | grep -q 26.3.27 \
    && log "bundled Xray reports 26.3.27" || fail "xray version"

  "$PP_ROOT_PREFIX/opt/proxypanel/agent/agent" -config "$PP_ROOT_PREFIX/etc/proxypanel/agent.json" -check \
    | grep -q 'configuration ok' && log "real agent accepts generated config" || fail "agent -check"

  section "negative: broken config must be refused"
  printf '{"panel_url":"https://p.example","registration_code":"K","xray_path":"/opt/xray/MISSING","state_dir":"/var/lib/proxypanel-agent"}' > "$TMPROOT/bad1.json"
  out=$("$PP_ROOT_PREFIX/opt/proxypanel/agent/agent" -config "$TMPROOT/bad1.json" -check 2>&1); rc=$?
  [ $rc -ne 0 ] && echo "$out" | grep -q 'xray_path' \
    && log "rejects missing core (rc=$rc)" || fail "accepted missing core: $out"
  printf '{"panel_url":"http://evil.example","registration_code":"K","xray_path":"%s","state_dir":"/var/lib/proxypanel-agent"}' \
    "$PP_ROOT_PREFIX/opt/proxypanel/xray/26.3.27/xray" > "$TMPROOT/bad2.json"
  out=$("$PP_ROOT_PREFIX/opt/proxypanel/agent/agent" -config "$TMPROOT/bad2.json" -check 2>&1); rc=$?
  [ $rc -ne 0 ] && echo "$out" | grep -q 'plain http' \
    && log "rejects cleartext non-loopback panel (rc=$rc)" || fail "accepted cleartext panel: $out"

  section "manager: update and uninstall"
  before=$(sha256sum "$PP_ROOT_PREFIX/opt/proxypanel/agent/agent" | awk '{print $1}')
  bash "$ROOT/deploy/agent" update v9.9.9 >/dev/null 2>&1 \
    && fail "update accepted a nonexistent tag" || log "update rejects nonexistent tag"
  after=$(sha256sum "$PP_ROOT_PREFIX/opt/proxypanel/agent/agent" | awk '{print $1}')
  [ "$before" = "$after" ] && log "failed update left the binary untouched" || fail "failed update mutated the binary"

  out=$(bash "$ROOT/deploy/agent" update 2>&1)
  echo "$out" | grep -q '更新完成' && log "update to latest succeeded" || fail "update to latest: $(echo "$out" | tail -3)"

  printf 'y\nn\nn\n' | bash "$ROOT/deploy/agent" uninstall >/dev/null 2>&1
  [ -d "$PP_STATE_DIR" ] && log "uninstall preserved the state directory" || fail "uninstall deleted state"
  [ -f "$PP_ROOT_PREFIX/etc/proxypanel/agent.json" ] && log "uninstall preserved the config" || fail "uninstall deleted config"
  [ -f "$PP_ROOT_PREFIX/etc/systemd/system/proxypanel-agent.service" ] && fail "uninstall left the unit" || log "uninstall removed the unit"
fi

printf '\n'
if [ $FAILED -eq 0 ]; then echo "AGENT_INSTALLER_TESTS_PASSED"; else echo "AGENT_INSTALLER_TESTS_FAILED"; fi
exit $FAILED
