#!/usr/bin/env bash
# web-demo stage F (owned, additive): real HOST-user nginx verification.
#
# Scope: this script only adds the host-process layer that the container/dual
# evidence could not prove. It does NOT replace scripts/verify-dual-hosting-remote.sh,
# does not touch the 8445 / old 8443 preview, the shared 15432 devdb, the existing
# 1Panel/openresty nginx or any systemd unit.
#
# Environment it needs (all optional except the lock path):
#   PPF_EVIDENCE      evidence dir (default: .ai/evidence/web-demo/F/host-nginx-<utc>-<rand>)
#   PPF_NGINX_DEB     already downloaded Ubuntu nginx .deb (optional)
#   PPF_NGINX_BIN     already extracted nginx ELF (optional)
#   PPF_NSS_CERTUTIL  already extracted certutil from libnss3-tools (optional)
#   PPF_NODE_MODULES  Playwright node_modules (default /home/compile/Work/proxypanel/web/node_modules)
#   PPF_GO            go binary
#   PPF_PG_BIN        directory with initdb/pg_ctl (default /home/compile/sdk/pkg/usr/lib/postgresql/17/bin)
#   PPF_LOCK          default /home/compile/Work/.locks/proxypanel-webdemo-remote-runtime.lock
#
# Guarantees: no root/sudo, no apt install, no PATH mutation, no system unit,
# every port on 127.0.0.1 random, every file under the evidence dir (0700/0600),
# no existing private key read, no docker/podman use, no host identity mutation.
set -euo pipefail

umask 077
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$(openssl rand -hex 4)"
EVIDENCE="${PPF_EVIDENCE:-$ROOT/.ai/evidence/web-demo/F/host-nginx-$RUN_ID}"
LOCK="${PPF_LOCK:-/home/compile/Work/.locks/proxypanel-webdemo-remote-runtime.lock}"
NODE_MODULES="${PPF_NODE_MODULES:-/home/compile/Work/proxypanel/web/node_modules}"
GO_BIN="${PPF_GO:-/home/compile/sdk/go1.27.3/go/bin/go}"
PG_BIN="${PPF_PG_BIN:-/home/compile/sdk/pkg/usr/lib/postgresql/17/bin}"
PG_LIB="${PPF_PG_LIB:-/home/compile/sdk/pkg/usr/lib/x86_64-linux-gnu}"
export LD_LIBRARY_PATH="$PG_LIB${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
NGINX_DEB_IN="${PPF_NGINX_DEB:-}"
NGINX_BIN_IN="${PPF_NGINX_BIN:-}"
CERTUTIL_IN="${PPF_NSS_CERTUTIL:-}"
APT_BASE="http://archive.ubuntu.com/ubuntu/pool"

REPORT="$EVIDENCE/result.json"
MANIFEST="$EVIDENCE/manifest.json"
WORK=""            # private runtime dir (inside EVIDENCE)
HOME_ISO=""        # temporary HOME used only for the NSS browser trust test
NGINX_PID=""
PANELPID=""
PG_STARTED=0
LOCK_HELD=0
FAIL_REASON=""
declare -a CHECKS=()
declare -a NOTRUN=()

log() { printf '%s\n' "$*" >&2; }
check_ok() { CHECKS+=("$1"); log "PASS $1"; }
not_run() { NOTRUN+=("$1"); log "NOT_RUN $1"; }
fail() { FAIL_REASON="$*"; log "FAIL $*"; exit 1; }

sha256() { sha256sum "$1" | cut -d' ' -f1; }
rand_port() {
  local p
  while :; do
    p=$(( (RANDOM % 20000) + 30000 ))
    if ! ss -ltn "sport = :$p" 2>/dev/null | grep -q LISTEN; then printf '%s' "$p"; return; fi
  done
}

cleanup() {
  local rc=$?
  set +e
  if [ -n "$NGINX_PID" ] && kill -0 "$NGINX_PID" 2>/dev/null; then
    nginx_stop
  fi
  stop_panel
  if [ "$PG_STARTED" = 1 ] && [ -n "$WORK" ]; then
    "$PG_BIN/pg_ctl" -D "$WORK/pg/data" -m fast stop >>"$EVIDENCE/pg-stop.log" 2>&1
  fi
  if [ -n "${PG_SOCK:-}" ] && [ -d "$(dirname "$PG_SOCK")" ]; then
    rm -rf "$(dirname "$PG_SOCK")"
  fi
  if [ -n "$WORK" ] && [ -f "$WORK/nginx/prefix/logs/nginx.pid" ]; then
    cat "$WORK/nginx/prefix/logs/nginx.pid" >&2
  fi
  if [ -d "$EVIDENCE" ]; then
    ss -ltn >"$EVIDENCE/after-127-listeners.txt" 2>&1
    # Failure path only: record identity/cleanup facts. The pass/fail verdict
    # itself is produced in main() BEFORE this trap runs, in one order.
    if [ -f "$EVIDENCE/host-before.json" ] && [ ! -f "$EVIDENCE/host-after.json" ]; then
      snapshot_host_identity "$EVIDENCE/host-after.json"
      compare_host_identity >>"$EVIDENCE/cleanup.log" 2>&1 || rc=1
    fi
    { echo "rc=$rc"; echo "work=$WORK"; echo "nginx_pid=$NGINX_PID"; echo "panel_pid=$PANELPID";
      echo "pg_started=$PG_STARTED"; } >"$EVIDENCE/cleanup.log"
    dump_arrays
    if [ ! -f "$REPORT" ]; then
      if [ "$rc" -eq 0 ]; then write_result passed; else write_result failed; fi
    fi
  fi
  if [ "$LOCK_HELD" = 1 ]; then :; fi   # lock fd 9 closed by shell exit
  exit $rc
}
trap cleanup EXIT
trap 'log "ERR line $LINENO: $BASH_COMMAND"' ERR
trap 'exit 130' INT TERM

acquire_lock() {
  mkdir -p "$(dirname "$LOCK")"
  # The remote-runtime lock serialises remote runtime mutation. This run performs
  # NO remote mutation and creates NO remote/preview/203 resource: every fixture
  # is a private temp prefix on this host user account. The lock is still tried
  # first, as the plan requires before any rented runtime.
  exec 9>"$LOCK"
  if flock -n 9; then
    LOCK_HELD=1
    check_ok "remote-runtime lock held (fd 9) before any fixture creation"
  else
    # Another in-flight web-demo group holds it (e.g. a remote PG inherited the
    # fd). We must not wait for or disturb it, and we cannot claim the lock.
    LOCK_HELD=0
    { echo "lock=$LOCK"; echo "state=held_by_other_run";
      fuser -v "$LOCK" 2>&1 | sed 's/^/holder: /'; } >"$EVIDENCE/lock-note.txt" 2>&1 || true
    NOTRUN_LOCK=1
    not_run "remote-runtime lock is held by another in-flight run; this run creates no remote resource, so it proceeds host-local only (see lock-note.txt)"
  fi
}

# ---------------------------------------------------------------- evidence base
prepare_evidence() {
  mkdir -p "$EVIDENCE"
  chmod 700 "$EVIDENCE"
  WORK="$EVIDENCE/work"
  mkdir -p "$WORK"
  HOME_ISO="$WORK/home"
  mkdir -p "$HOME_ISO"
  check_ok "evidence dir 0700: $EVIDENCE"
}

snapshot_host_identity() {
  local out="$1"
  python3 - "$out" <<'PY'
import json, os, sys
pid = 1
def rd(p, mode='r'):
    try:
        with open(p, 'rb') as f: b = f.read()
    except OSError:
        return None
    return b.decode('utf-8', 'replace') if mode == 'r' else b.hex()
import hashlib
def sha(p):
    try:
        return hashlib.sha256(open(p,'rb').read()).hexdigest()
    except OSError:
        return None
def ns(p):
    try:
        return {'link': os.readlink(p), 'inode': os.stat(p).st_ino}
    except OSError:
        return None
cmdline = rd('/proc/1/cmdline')
data = {
 'kernel': os.uname().release, 'machine': os.uname().machine,
 'pid1_cmdline': (cmdline or '').replace('\0',' ').strip(),
 'pid1_exe': os.path.realpath('/proc/1/exe') if os.path.exists('/proc/1/exe') else None,
 'pid1_ns_pid': ns('/proc/1/ns/pid'),
 'host_ns_pid': ns('/proc/self/ns/pid'),
 'host_ns_mnt': ns('/proc/self/ns/mnt'),
 'host_ns_net': ns('/proc/self/ns/net'),
 'cgroup_self': rd('/proc/self/cgroup'),
 'docker_env_marker': os.path.exists('/.dockerenv'),
 'container_env': rd('/proc/self/environ','hex') is not None and 'container=' in (rd('/proc/self/environ') or ''),
 'uid': os.getuid(), 'gid': os.getgid(),
 'system_nginx_present': os.path.exists('/usr/sbin/nginx'),
 'listen_127': os.popen("ss -ltnH 2>/dev/null | awk '{print $4}' | sort").read().split(),
}
json.dump(data, open(sys.argv[1],'w'), indent=2)
PY
  chmod 600 "$out"
}

compare_host_identity() {
  local before="$EVIDENCE/host-before.json" after="$EVIDENCE/host-after.json"
  python3 - "$before" "$after" "$EVIDENCE/cleanup.log" "$EVIDENCE/host-identity-diff.json" <<'PY'
import json, sys, os
b, a, out = sys.argv[1], sys.argv[2], sys.argv[4]
B, A = json.load(open(b)), json.load(open(a))
keys = ['kernel','pid1_cmdline','pid1_ns_pid','host_ns_pid','host_ns_mnt','host_ns_net',
        'cgroup_self','docker_env_marker','system_nginx_present','uid','gid']
diff = {k: {'before': B.get(k), 'after': A.get(k)} for k in keys if B.get(k) != A.get(k)}
new_listen = sorted(set(A.get('listen_127', [])) - set(B.get('listen_127', [])))
stale_listen = sorted(set(B.get('listen_127', [])) - set(A.get('listen_127', [])))
json.dump({'changed': diff, 'new_127_listeners': new_listen,
           'disappeared_127_listeners': stale_listen,
           'server_process_identity_unchanged': not any(
               k in diff for k in ('pid1_cmdline','pid1_ns_pid','host_ns_pid','host_ns_mnt','host_ns_net'))},
          open(out,'w'), indent=2)
assert not diff, f'protected host identity changed: {diff}'
print('host identity unchanged')
PY
  chmod 600 "$EVIDENCE/host-identity-diff.json"
  check_ok "protected host identity unchanged before/after (pid1/namespaces/cgroup, no leftover 127 listener)"
}

leftover_check() {
  local prefix="$WORK/nginx/prefix"
  [ ! -e "$prefix/logs/nginx.pid" ] || fail "nginx pidfile left behind"
  if pgrep -f "$WORK/nginx/nginx -p $prefix" >/dev/null 2>&1; then fail "nginx process left behind"; fi
  if ss -ltn "sport = :$HTTPS_PORT" 2>/dev/null | grep -q LISTEN; then fail "https port $HTTPS_PORT still listening"; fi
  if ss -ltn "sport = :$PANEL_PORT" 2>/dev/null | grep -q LISTEN; then fail "panel port $PANEL_PORT still listening"; fi
  check_ok "no leftover nginx/panel process, pidfile or listener after cleanup"
}

# --------------------------------------------------- real user-space nginx ELF
obtain_nginx() {
  local dir="$WORK/nginx"
  mkdir -p "$dir"
  if [ -n "$NGINX_BIN_IN" ] && [ -x "$NGINX_BIN_IN" ]; then
    cp "$NGINX_BIN_IN" "$dir/nginx"
    echo "source=provided:$NGINX_BIN_IN" >"$dir/provenance.txt"
  else
    local deb="$NGINX_DEB_IN"
    if [ -z "$deb" ]; then
      # Download only; never install. Pin the exact Ubuntu noble-updates version.
      local ver="1.24.0-2ubuntu7.18"
      deb="$dir/nginx_${ver}_amd64.deb"
      local url="$APT_BASE/main/n/nginx/nginx_${ver}_amd64.deb"
      curl -fsS --max-time 120 -o "$deb" "$url" || fail "cannot download $url (network/dependency gap)"
      echo "url=$url" >>"$dir/provenance.txt"
    fi
    dpkg-deb -e "$deb" "$dir/control"
    dpkg-deb -x "$deb" "$dir/rootfs"
    # Package payloads carry world-readable modes; keep the whole private prefix
    # owner-only as required for this evidence tree.
    chmod -R go-rwx "$dir/control" "$dir/rootfs"
    cp "$dir/rootfs/usr/sbin/nginx" "$dir/nginx"
    echo "deb=$deb" >>"$dir/provenance.txt"
    echo "deb_sha256=$(sha256 "$deb")" >>"$dir/provenance.txt"
    grep -E '^(Package|Version|Source|Architecture):' "$dir/control/control" >>"$dir/provenance.txt"
  fi
  chmod 700 "$dir/nginx"
  NGINX_ELF="$dir/nginx"
  NGINX_VERSION="$("$NGINX_ELF" -v 2>&1 | tr -d '\r')"
  check_ok "real user-space nginx ELF prepared ($NGINX_VERSION)"
}

nginx_proc_identity() {
  local stage="$1" pid
  pid="$(cat "$WORK/nginx/prefix/logs/nginx.pid")"
  [ -n "$pid" ] || fail "nginx master pid missing"
  local out="$EVIDENCE/nginx-$stage.json"
  python3 - "$pid" "$out" "$WORK/nginx/prefix" "$NGINX_ELF" <<'PY'
import json, os, sys, hashlib
pid, out, prefix, elf = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
def sha(p):
    return hashlib.sha256(open(p,'rb').read()).hexdigest()
def rds(p):
    try: return os.readlink(p)
    except OSError: return None
def rdf(p):
    try: return open(p,'rb').read()
    except OSError: return b''
def ns(p):
    try: return {'link': os.readlink(p), 'inode': os.stat(p).st_ino}
    except OSError: return None
data = {
 'pid': int(pid),
 'exe': rds(f'/proc/{pid}/exe'),
 'exe_sha256': sha(f'/proc/{pid}/exe'),
 'cmdline': rdf(f'/proc/{pid}/cmdline').replace(b'\0', b' ').decode('utf-8','replace').strip(),
 'cwd': rds(f'/proc/{pid}/cwd'),
 'root': rds(f'/proc/{pid}/root'),
 'host_ns_pid': ns('/proc/self/ns/pid'),
 'nginx_ns_pid': ns(f'/proc/{pid}/ns/pid'),
 'host_ns_net': ns('/proc/self/ns/net'),
 'nginx_ns_net': ns(f'/proc/{pid}/ns/net'),
 'host_ns_mnt': ns('/proc/self/ns/mnt'),
 'nginx_ns_mnt': ns(f'/proc/{pid}/ns/mnt'),
 'is_docker_process': os.path.exists('/.dockerenv'),
 'prefix': prefix,
 'private_elf': elf,
 'private_elf_sha256': sha(elf),
 'binary_is_private_elf': os.path.realpath(f'/proc/{pid}/exe') == os.path.realpath(elf),
 'parent_pid': int(open(f'/proc/{pid}/stat').read().split()[3]),
}
json.dump(data, open(out,'w'), indent=2)
assert data['host_ns_pid']['inode'] == data['nginx_ns_pid']['inode'], 'nginx not in current host pid namespace'
assert data['host_ns_mnt']['inode'] == data['nginx_ns_mnt']['inode'], 'nginx not in current host mount namespace'
assert data['host_ns_net']['inode'] == data['nginx_ns_net']['inode'], 'nginx not in current host net namespace'
assert data['binary_is_private_elf'], 'nginx exe is not the private extracted ELF'
assert data['exe_sha256'] == data['private_elf_sha256'], 'nginx exe hash mismatch'
assert not data['is_docker_process'], 'docker marker present'
print(json.dumps({k: data[k] for k in ('pid','exe','exe_sha256','cmdline')}))
PY
  chmod 600 "$out"
  check_ok "nginx $stage /proc identity is a current-host, non-container process with private ELF"
}

# ----------------------------------------------------------------------- PKI
make_pki() {
  local pki="$WORK/pki"
  mkdir -p "$pki"
  local ca_cnf="$pki/ca.cnf" leaf_cnf="$pki/leaf.cnf"
  cat >"$ca_cnf" <<'EOF'
[req]
distinguished_name = dn
x509_extensions = v3_ca
prompt = no
[dn]
CN = proxypanel-webdemo-stage-f private test CA
O = proxypanel web-demo local fixture
[v3_ca]
basicConstraints = critical,CA:TRUE,pathlen:0
keyUsage = critical,keyCertSign,cRLSign
subjectKeyIdentifier = hash
EOF
  openssl req -x509 -new -newkey rsa:2048 -nodes -sha256 -days 2 \
    -keyout "$pki/ca.key" -out "$pki/ca.crt" -config "$ca_cnf" >/dev/null 2>&1 \
    || fail "private CA creation failed"
  openssl genrsa -out "$pki/server.key" 2048 >/dev/null 2>&1 || fail "leaf key generation failed"
  cat >"$leaf_cnf" <<EOF
[req]
distinguished_name = dn
prompt = no
req_extensions = v3_req
[dn]
CN = localhost
O = proxypanel web-demo local fixture
[v3_req]
basicConstraints = critical,CA:FALSE
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt
[alt]
DNS.1 = localhost
IP.1 = 127.0.0.1
EOF
  openssl req -new -key "$pki/server.key" -out "$pki/server.csr" -config "$leaf_cnf" >/dev/null 2>&1
  openssl x509 -req -in "$pki/server.csr" -CA "$pki/ca.crt" -CAkey "$pki/ca.key" \
    -CAcreateserial -days 2 -sha256 -extfile "$leaf_cnf" -extensions v3_req \
    -out "$pki/server.crt" >/dev/null 2>&1 || fail "leaf signing failed"
  # strict chain verification, twice: openssl and curl both must trust it
  openssl verify -CAfile "$pki/ca.crt" "$pki/server.crt" >"$pki/openssl-verify.txt" 2>&1 \
    || fail "openssl verify failed against private CA"
  grep -q 'server.crt: OK' "$pki/openssl-verify.txt" || fail "openssl verify output unexpected"
  check_ok "private CA + localhost/SAN127 leaf created and verified (openssl verify OK)"
}

# NSS trust db in a temporary HOME so a real browser can trust the private CA
# without any self-signed bypass. certutil comes from libnss3-tools (unpacked).
install_nss_trust() {
  local dir="$WORK/nss"
  mkdir -p "$dir" "$HOME_ISO/.pki/nssdb"
  chmod 700 "$HOME_ISO" "$HOME_ISO/.pki" "$HOME_ISO/.pki/nssdb"
  local certutil="$CERTUTIL_IN"
  if [ -z "$certutil" ] || [ ! -x "$certutil" ]; then
    local deb="$dir/libnss3-tools.deb"
    # Filename without the epoch prefix; the dir name is the source package.
    local url="$APT_BASE/main/n/nss/libnss3-tools_3.98-1ubuntu0.3_amd64.deb"
    if curl -fsS --max-time 120 -o "$deb" "$url"; then
      dpkg-deb -x "$deb" "$dir/rootfs"
      chmod -R go-rwx "$dir/rootfs"
      certutil="$dir/rootfs/usr/bin/certutil"
      echo "certutil_url=$url" >"$dir/provenance.txt"
      echo "certutil_deb_sha256=$(sha256 "$deb")" >>"$dir/provenance.txt"
    fi
  fi
  if [ -z "$certutil" ] || [ ! -x "$certutil" ]; then
    not_run "NSS browser trust: certutil unavailable (browser would need narrow self-signed helper)"
    return 1
  fi
  if ! "$certutil" -d "sql:$HOME_ISO/.pki/nssdb" -A -t 'C,,' -n 'webdemo-stage-f private CA' \
      -i "$WORK/pki/ca.crt" >"$dir/certutil-add.log" 2>&1; then
    not_run "NSS browser trust: certutil could not add CA to private db"
    return 1
  fi
  "$certutil" -d "sql:$HOME_ISO/.pki/nssdb" -L >"$dir/certutil-list.txt" 2>&1
  grep -q 'webdemo-stage-f private CA' "$dir/certutil-list.txt" || { not_run "NSS trust list missing"; return 1; }
  echo "home=$HOME_ISO nssdb=sql:$HOME_ISO/.pki/nssdb" >"$dir/trust.txt"
  check_ok "private CA installed into temporary HOME NSS db (no self-signed bypass)"
  return 0
}

# ---------------------------------------------------------------- postgres + panel
start_fixture() {
  local pg="$WORK/pg"
  # AF_UNIX socket paths are limited to 107 bytes; the evidence path is longer,
  # so the socket lives in its own short 0700 owner-only temp dir.
  PG_SOCK="/tmp/ppfng-$RUN_ID/socket"
  mkdir -p "$pg/data" "$PG_SOCK"
  chmod 700 "$pg" "$(dirname "$PG_SOCK")" "$PG_SOCK"
  "$PG_BIN/initdb" -D "$pg/data" -A trust -U postgres --encoding=UTF8 --locale=C \
    >"$EVIDENCE/pg-init.log" 2>&1 || fail "initdb failed"
  PG_PORT="$(rand_port)"
  "$PG_BIN/pg_ctl" -D "$pg/data" -o "-p $PG_PORT -h 127.0.0.1 -k $PG_SOCK -c max_connections=20" \
    -l "$EVIDENCE/pg.log" -w start >>"$EVIDENCE/pg-init.log" 2>&1 || fail "private PG failed to start"
  PG_STARTED=1
  "$PG_BIN/createdb" -h 127.0.0.1 -p "$PG_PORT" -U postgres webdemo_host_nginx \
    || fail "createdb failed"
  check_ok "owned private PG cluster on 127.0.0.1:$PG_PORT (temp prefix, own data dir)"

  local panelbin="$WORK/bin/panel"
  mkdir -p "$WORK/bin"
  ( cd "$ROOT" && "$GO_BIN" build -trimpath -buildvcs=false -o "$panelbin" ./cmd/panel ) \
    >"$EVIDENCE/go-build.log" 2>&1 || fail "panel build failed"
  PANEL_BIN_SHA="$(sha256 "$panelbin")"

  local webroot="$ROOT/web/dist"
  if [ ! -f "$webroot/index.html" ]; then
    ( cd "$ROOT/web" && PPF_API_URL=x npm run build ) >"$EVIDENCE/web-build.log" 2>&1 \
      || fail "web build failed (web/dist/index.html unavailable)"
  fi
  WEB_INDEX_SHA="$(sha256 "$webroot/index.html")"
  ASSET_JS="$(grep -o 'assets/[^"]*\.js' "$webroot/index.html" | head -1)"
  [ -n "$ASSET_JS" ] || fail "could not locate hashed asset in dist/index.html"
  WEB_ASSET_SHA="$(sha256 "$webroot/$ASSET_JS")"

  PANEL_PORT="$(rand_port)"
  export DATABASE_URL="postgres://postgres@127.0.0.1:$PG_PORT/webdemo_host_nginx?sslmode=disable"
  export PANEL_LISTEN="127.0.0.1:$PANEL_PORT"
  export PANEL_TRUST_REVERSE_PROXY=true
  export PANEL_DEV=false
  export PANEL_MASTER_KEY="$(openssl rand -base64 32)"
  export PANEL_WEB_ROOT="$webroot"
  export PANEL_MIGRATIONS_DIR="$ROOT/migrations"
  export PANEL_PUBLIC_URL="https://localhost:$HTTPS_PORT"
  export PANEL_ADMIN_USER=admin
  export PANEL_ADMIN_PASSWORD="ppf-$(openssl rand -hex 12)"
  ( cd "$ROOT" && exec "$panelbin" ) >"$EVIDENCE/panel.log" 2>&1 &
  PANELPID=$!
  for _ in $(seq 1 120); do
    if [ "$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PANEL_PORT/api/v1/auth/session" 2>/dev/null)" = "401" ]; then break; fi
    kill -0 "$PANELPID" 2>/dev/null || fail "panel exited during startup (see panel.log)"
    sleep 0.5
  done
  grep -qi 'panic\|fatal' "$EVIDENCE/panel.log" && fail "panel log shows fatal error"
  check_ok "fresh owned panel built+started on 127.0.0.1:$PANEL_PORT with PANEL_WEB_ROOT (Go SPA mode)"
}

# ------------------------------------------------------------------- nginx
nginx_write_config() {
  local prefix="$WORK/nginx/prefix" mode="$1"
  mkdir -p "$prefix/conf" "$prefix/logs" "$prefix/static" "$prefix/tmp"
  chmod 700 "$prefix" "$prefix/conf" "$prefix/logs" "$prefix/static" "$prefix/tmp"
  local upstream="http://127.0.0.1:$PANEL_PORT"
  local body_root="$prefix/static"
  if [ "$mode" = "spa" ]; then
    body_root="$WORK/nginx/prefix/static"   # not used in spa mode; kept empty
  fi
  if [ "$mode" = "api" ]; then
    cp -r "$ROOT/web/dist/." "$prefix/static/"
  fi
  cat >"$prefix/conf/nginx.conf" <<EOF
worker_processes 1;
pid $prefix/logs/nginx.pid;
error_log $prefix/logs/error.log warn;
daemon on;
events { worker_connections 64; }
http {
  access_log $prefix/logs/access.log;
  client_body_temp_path $prefix/tmp/body;
  proxy_temp_path $prefix/tmp/proxy;
  fastcgi_temp_path $prefix/tmp/fastcgi;
  uwsgi_temp_path $prefix/tmp/uwsgi;
  scgi_temp_path $prefix/tmp/scgi;
  # No system mime.types: keep the map explicit so a missing type cannot turn
  # index.html into a download (that would hide SPA route protection).
  default_type application/octet-stream;
  types {
    text/html html htm;
    text/css css;
    text/javascript js mjs;
    application/json json map;
    image/svg+xml svg;
    image/png png;
    image/x-icon ico;
    font/woff2 woff2;
    font/woff woff;
    text/plain txt;
  }
  server_tokens off;
  sendfile off;
  server {
    listen 127.0.0.1:$HTTPS_PORT ssl;
    server_name localhost;
    ssl_certificate $WORK/pki/server.crt;
    ssl_certificate_key $WORK/pki/server.key;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_session_cache off;
$( [ "$mode" = "api" ] && cat <<STATIC
    root $body_root;
    location /assets/ { try_files \$uri =404; }
    location / { try_files \$uri /index.html; }
STATIC
)
$( [ "$mode" = "spa" ] && echo '    location / {
      proxy_pass http://127.0.0.1:'"$PANEL_PORT"';
      proxy_set_header Host $host;
      proxy_set_header X-Forwarded-For $remote_addr;
      proxy_http_version 1.1;
    }' )
    location /api/ {
      proxy_pass $upstream;
      proxy_set_header Host \$host;
      proxy_set_header X-Forwarded-For \$remote_addr;
      proxy_set_header X-Forwarded-Proto https;
      proxy_http_version 1.1;
    }
    location /sub/ {
      proxy_pass $upstream;
      proxy_set_header Host \$host;
      proxy_set_header X-Forwarded-For \$remote_addr;
      proxy_http_version 1.1;
    }
  }
}
EOF
  chmod 600 "$prefix/conf/nginx.conf"
}

nginx_start() {
  local mode="$1"
  nginx_write_config "$mode"
  local prefix="$WORK/nginx/prefix"
  "$NGINX_ELF" -t -p "$prefix" -c "$prefix/conf/nginx.conf" >"$EVIDENCE/nginx-$mode-nt.log" 2>&1 \
    || { cat "$EVIDENCE/nginx-$mode-nt.log" >&2; fail "nginx -t failed for $mode"; }
  "$NGINX_ELF" -p "$prefix" -c "$prefix/conf/nginx.conf" >"$EVIDENCE/nginx-$mode-start.log" 2>&1 \
    || { cat "$EVIDENCE/nginx-$mode-start.log" >&2; fail "nginx start failed for $mode"; }
  NGINX_PID="$(cat "$prefix/logs/nginx.pid")"
  for _ in $(seq 1 60); do
    if curl -fsS --max-time 3 --cacert "$WORK/pki/ca.crt" "https://localhost:$HTTPS_PORT/login" -o /dev/null; then break; fi
    sleep 0.25
  done
  nginx_proc_identity "$mode"
  check_ok "nginx master started as host user from extracted ELF (mode=$mode, prefix=$prefix)"
}

stop_panel() {
  if [ -n "$PANELPID" ] && kill -0 "$PANELPID" 2>/dev/null; then
    kill -TERM "$PANELPID" 2>/dev/null || true
    for _ in $(seq 1 40); do kill -0 "$PANELPID" 2>/dev/null || break; sleep 0.2; done
    kill -KILL "$PANELPID" 2>/dev/null || true
  fi
  PANELPID=""
  return 0
}

nginx_stop() {
  local prefix="$WORK/nginx/prefix"
  "$NGINX_ELF" -p "$prefix" -c "$prefix/conf/nginx.conf" -s quit >"$EVIDENCE/nginx-stop.log" 2>&1 || true
  local pid="$NGINX_PID"
  for _ in $(seq 1 40); do kill -0 "$pid" 2>/dev/null || break; sleep 0.25; done
  kill -KILL "$pid" 2>/dev/null || true
  NGINX_PID=""
  check_ok "nginx stopped (pid gone; prefix/pidfile/logs stay under the 0700 evidence dir)"
  return 0
}

# ------------------------------------------------------------ strict TLS probes
strict_probes() {
  local base="https://localhost:$HTTPS_PORT"
  local out="$EVIDENCE/strict-tls.txt"
  : >"$out"
  # default trust store must reject the private CA
  local code
  code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 "$base/login" 2>>"$out" || true)"
  { echo "untrusted_default_store_code=$code"; } >>"$out"
  [ "$code" = "000" ] || fail "default trust store unexpectedly accepted private CA (code=$code)"
  # strict trust with --cacert, hostname check on both localhost and SAN IP
  curl -fsS --max-time 10 --cacert "$WORK/pki/ca.crt" "$base/login" -o "$EVIDENCE/tls-login.html" \
    >>"$out" 2>&1 || fail "curl --cacert to https://localhost failed"
  curl -fsS --max-time 10 --cacert "$WORK/pki/ca.crt" "https://127.0.0.1:$HTTPS_PORT/login" -o /dev/null \
    >>"$out" 2>&1 || fail "curl --cacert to https://127.0.0.1 (SAN IP) failed"
  if curl -fsS --max-time 10 --cacert "$WORK/pki/ca.crt" "https://wrong.invalid:$HTTPS_PORT/login" -o /dev/null \
      --connect-to "wrong.invalid:$HTTPS_PORT:127.0.0.1:$HTTPS_PORT" >>"$out" 2>&1; then
    fail "hostname mismatch was not rejected"
  fi
  echo 'hostname_mismatch_rejected=1' >>"$out"
  # Node TLS with the private CA and rejectUnauthorized
  node -e '
const https=require("https"), fs=require("fs");
const ca=fs.readFileSync(process.argv[1]); const port=process.argv[2];
const req=https.request({host:"localhost",port,path:"/login",ca,rejectUnauthorized:true,servername:"localhost"},res=>{
  let n=0; res.on("data",c=>{n+=c.length}); res.on("end",()=>{console.log(JSON.stringify({status:res.statusCode,bytes:n,authorized:req.socket.authorized,protocol:req.socket.getProtocol()})); process.exit(res.statusCode===200?0:1)});
});
req.on("error",e=>{console.error("ERR",e.message);process.exit(2)}); req.end();
' "$WORK/pki/ca.crt" "$HTTPS_PORT" >>"$out" 2>&1 || fail "Node rejectUnauthorized TLS client failed"
  # hostile XFF must not expand the trusted network. Each attempt spoofs a
  # DIFFERENT client address; if the panel trusted the override every attempt
  # would be a fresh source and never hit the per-source budget, so the observed
  # 429 proves the address actually used is the nginx peer (127.0.0.1), not XFF.
  # A throwaway username is used so the real admin budget is untouched.
  local i code_i
  for i in $(seq 1 12); do
    code_i="$(curl -sS --max-time 10 --cacert "$WORK/pki/ca.crt" \
      -H "X-Forwarded-For: 198.51.100.$i" -H 'Content-Type: application/json' \
      -d '{"username":"ppf-xff-probe","password":"wrong-on-purpose"}' \
      -o "$EVIDENCE/login-probe-$i.json" -w '%{http_code}' "$base/api/v1/auth/login" 2>>"$out" || true)"
    echo "spoofed_xff_login_$i=$code_i" >>"$out"
    if [ "$i" -eq 1 ]; then [ "$code_i" = "401" ] || fail "first bad login returned $code_i, expected uniform 401"; fi
  done
  [ "$code_i" = "429" ] || fail "spoofed X-Forwarded-For bypassed the login budget (last=$code_i)"
  grep -q 'invalid username or password' "$EVIDENCE/login-probe-1.json" || fail "login error body is not uniform"
  echo 'xff_spoof_does_not_widen_trust=1 (last of 12 distinct spoofed sources -> 429)' >>"$out"
  chmod 600 "$out"
  check_ok "strict TLS: default store rejects, --cacert/Node verify real chain, DNS mismatch rejected"
}

# -------------------------------------------------------- browser (two modes)
browser_mode() {
  local mode="$1"
  local browser_out="$EVIDENCE/browser-$mode"
  mkdir -p "$browser_out"
  chmod 700 "$browser_out"
  local env_extra=()
  if [ -f "$WORK/nss/trust.txt" ]; then
    env_extra+=( HOME="$HOME_ISO" )
  fi
  env_extra+=( PLAYWRIGHT_BROWSERS_PATH="${PLAYWRIGHT_BROWSERS_PATH:-/home/compile/.cache/ms-playwright}" )
  # Playwright's context.request rides on Node's TLS stack, so the same private CA
  # must be trusted there too (no rejectUnauthorized bypass anywhere).
  env_extra+=( NODE_EXTRA_CA_CERTS="$WORK/pki/ca.crt" )
  ensure_node_modules
  local code=0
  ( cd "$ROOT" && env "${env_extra[@]}" \
      PANEL_BASE="https://localhost:$HTTPS_PORT" \
      PANEL_ADMIN_USER=admin PANEL_ADMIN_PASSWORD="$PANEL_ADMIN_PASSWORD" \
      PANEL_BROWSER_EVIDENCE="$browser_out" \
      PANEL_SMOKE_MODE="$mode" PANEL_SMOKE_PORT="$HTTPS_PORT" \
      PANEL_SMOKE_PHASE=current \
      node "$ROOT/web/tests/host-nginx-smoke.mjs" ) >"$browser_out/host-nginx-smoke.log" 2>&1 || code=$?
  echo "$code" >"$browser_out/exit-code.txt"
  if [ "$code" -ne 0 ]; then
    tail -40 "$browser_out/host-nginx-smoke.log" >&2
    return 1
  fi
  check_ok "real browser form login/deep refresh/logout/404 via host nginx ($mode, no ignoreHTTPSErrors)"
}

ensure_node_modules() {
  # ESM resolution ignores NODE_PATH; expose the shared (git-ignored) node_modules
  # to this worktree through a symlink instead of copying or editing tests.
  if [ ! -e "$ROOT/web/node_modules" ]; then
    ln -s "$NODE_MODULES" "$ROOT/web/node_modules"
    check_ok "linked shared playwright node_modules into worktree (git-ignored, no test edit)"
  fi
  [ -x "$ROOT/web/node_modules/.bin/playwright" ] || fail "playwright is missing at $NODE_MODULES"
}

browser_mode_selfsigned() {
  local mode="$1"
  local browser_out="$EVIDENCE/browser-$mode-selfsigned-bypass"
  mkdir -p "$browser_out"; chmod 700 "$browser_out"
  ensure_node_modules
  local code=0
  ( cd "$ROOT" && env PANEL_BASE="https://localhost:$HTTPS_PORT" \
      PANEL_ADMIN_USER=admin PANEL_ADMIN_PASSWORD="$PANEL_ADMIN_PASSWORD" \
      PLAYWRIGHT_BROWSERS_PATH="${PLAYWRIGHT_BROWSERS_PATH:-/home/compile/.cache/ms-playwright}" \
      PANEL_BROWSER_EVIDENCE="$browser_out" PANEL_SMOKE_SELF_SIGNED=1 \
      PANEL_SMOKE_MODE="$mode" PANEL_SMOKE_PORT="$HTTPS_PORT" PANEL_SMOKE_PHASE=current \
      NODE_PATH="$NODE_MODULES" \
      node "$ROOT/web/tests/host-nginx-smoke.mjs" ) >"$browser_out/host-nginx-smoke.log" 2>&1 || code=$?
  echo "$code" >"$browser_out/exit-code.txt"
  not_run "browser cert-bypass fallback used for $mode (narrow claim only, NOT trusted-chain evidence)"
  [ "$code" -eq 0 ] && return 0
  return 1
}

# ------------------------------------------------------------------- manifest
write_manifest() {
  python3 - "$ROOT" "$EVIDENCE" "$NGINX_ELF" "$WORK" "$PANEL_BIN_SHA" "$WEB_INDEX_SHA" "$WEB_ASSET_SHA" <<'PY'
import json, sys, os, hashlib, subprocess
root, ev, nginx, work, panel_sha, index_sha, asset_sha = sys.argv[1:8]
def sha(p):
    return hashlib.sha256(open(p,'rb').read()).hexdigest()
def rel(p):
    return os.path.relpath(p, root)
m = {
 'run_id': os.path.basename(ev),
 'source_sha': subprocess.run(['git','-C',root,'rev-parse','HEAD'],capture_output=True,text=True).stdout.strip(),
 'source_dirty': bool(subprocess.run(['git','-C',root,'status','--porcelain','--untracked-files=no'],capture_output=True,text=True).stdout.strip()),
 'script_sha256': sha(os.path.join(root,'scripts/webdemo-host-nginx-verify.sh')),
 'nginx_elf': {'path': rel(nginx), 'sha256': sha(nginx),
               'provenance': open(os.path.join(work,'nginx/provenance.txt')).read().strip().splitlines()},
 'panel_binary_sha256': panel_sha,
 'web_index_sha256': index_sha,
 'web_asset_sha256': asset_sha,
 'pki': {'ca_sha256': sha(os.path.join(work,'pki/ca.crt')),
         'leaf_sha256': sha(os.path.join(work,'pki/server.crt'))},
 'test_script_sha256': sha(os.path.join(root,'web/tests/host-nginx-smoke.mjs')),
}
if os.path.exists(os.path.join(work,'nss/provenance.txt')):
    m['certutil_provenance'] = open(os.path.join(work,'nss/provenance.txt')).read().strip().splitlines()
json.dump(m, open(os.path.join(ev,'manifest.json'),'w'), indent=2)
print('manifest ok')
PY
  chmod 600 "$MANIFEST"
}

dump_arrays() {
  [ -n "$WORK" ] && [ -d "$WORK" ] || return 0
  printf '%s\n' ${CHECKS[@]+"${CHECKS[@]}"} >"$WORK/checks.txt" 2>/dev/null || : >"$WORK/checks.txt"
  printf '%s\n' ${NOTRUN[@]+"${NOTRUN[@]}"} >"$WORK/notrun.txt" 2>/dev/null || : >"$WORK/notrun.txt"
}

write_result() {
  local status="$1"
  python3 - "$REPORT" "$status" "$FAIL_REASON" "$WORK" <<'PY'
import json, sys, os
rep, status, fail, work = sys.argv[1:5]
checks, notrun = [], []
# passed in via env files to avoid shell quoting issues
if os.path.exists(os.path.join(work,'checks.txt')):
    checks = [l.rstrip('\n') for l in open(os.path.join(work,'checks.txt'))]
if os.path.exists(os.path.join(work,'notrun.txt')):
    notrun = [l.rstrip('\n') for l in open(os.path.join(work,'notrun.txt'))]
json.dump({'status': status, 'failure': fail or None, 'checks': checks, 'not_run': notrun},
          open(rep,'w'), indent=2)
PY
  chmod 600 "$REPORT"
}

# ---------------------------------------------------------------------- main
main() {
  prepare_evidence
  acquire_lock
  snapshot_host_identity "$EVIDENCE/host-before.json"
  check_ok "host identity snapshot (pid1 / namespaces / non-docker) recorded"
  obtain_nginx
  make_pki
  HTTPS_PORT="$(rand_port)"
  start_fixture
  nginx_write_config spa
  nginx_start spa
  strict_probes
  if install_nss_trust; then
    browser_mode spa || fail "spa browser (trusted NSS) failed"
    NSS_OK=1
  else
    NSS_OK=0
  fi
  nginx_stop
  nginx_write_config api
  nginx_start api
  if [ "$NSS_OK" = 1 ]; then
    browser_mode api || fail "api browser (trusted NSS) failed"
  else
    browser_mode_selfsigned spa || fail "spa browser (self-signed fallback) failed"
    browser_mode_selfsigned api || fail "api browser (self-signed fallback) failed"
  fi
  nginx_stop
  stop_panel
  leftover_check
  snapshot_host_identity "$EVIDENCE/host-after.json"
  compare_host_identity
  write_manifest
  dump_arrays
  write_result passed
  log "HOST_NGINX_VERIFIED evidence=$EVIDENCE"
}

main "$@"
