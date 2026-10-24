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
# No binary/deb can be injected: both packages are fetched over HTTPS only and
# verified against Ubuntu's GPG-signed Packages metadata (see fetch_verified_deb).
#   PPF_NODE_MODULES  Playwright node_modules (default /home/compile/Work/proxypanel/web/node_modules)
#   PPF_GO            go binary
#   PPF_PG_BIN        directory with initdb/pg_ctl (default /home/compile/sdk/pkg/usr/lib/postgresql/17/bin)
#   PPF_LOCK_WAIT     seconds to wait for the local runtime lock (default 60)
#
# Runtime lock: this run starts a local PG + panel + nginx, so it holds
# /home/compile/Work/.locks/proxypanel-webdemo-local-runtime.lock from before the
# "before" snapshot until all owned processes are reaped. No remote resource is
# created, so the remote-runtime lock is deliberately NOT touched.
#
# Guarantees: no root/sudo, no apt install, no PATH mutation, no system unit,
# every port on 127.0.0.1 random, every file under the evidence dir (0700/0600),
# no existing private key read, no docker/podman use, no host identity mutation.
set -euo pipefail

umask 077
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$(openssl rand -hex 4)"
EVIDENCE="${PPF_EVIDENCE:-$ROOT/.ai/evidence/web-demo/F/host-nginx-$RUN_ID}"
LOCK="${PPF_LOCK:-/home/compile/Work/.locks/proxypanel-webdemo-local-runtime.lock}"
LOCK_WAIT="${PPF_LOCK_WAIT:-60}"
NODE_MODULES="${PPF_NODE_MODULES:-/home/compile/Work/proxypanel/web/node_modules}"
GO_BIN="${PPF_GO:-/home/compile/sdk/go1.27.3/go/bin/go}"
PG_BIN="${PPF_PG_BIN:-/home/compile/sdk/pkg/usr/lib/postgresql/17/bin}"
PG_LIB="${PPF_PG_LIB:-/home/compile/sdk/pkg/usr/lib/x86_64-linux-gnu}"
export LD_LIBRARY_PATH="$PG_LIB${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
# HTTPS only. Pinned version; the digest is NOT taken from the download, it comes
# from Ubuntu's GPG-verified Packages index for this pinned version.
NGINX_VERSION_PIN="1.24.0-2ubuntu7.18"
NSS_VERSION_PIN="3.98-1ubuntu0.3"
NSS_EPOCH_PIN="2"
UBUNTU_HTTPS="https://archive.ubuntu.com/ubuntu"
KEYRING="/usr/share/keyrings/ubuntu-archive-keyring.gpg"
CURL_HTTPS=(--proto '=https' --tlsv1.2 --max-redirs 3 --noproxy '*')
CURL_LOCAL=(--noproxy '*' --max-time 10)

REPORT="$EVIDENCE/result.json"
MANIFEST="$EVIDENCE/manifest.json"
WORK=""            # private runtime dir (inside EVIDENCE)
HOME_ISO=""        # temporary HOME used only for the NSS browser trust test
NGINX_PID=""
PANELPID=""
PG_STARTED=0
LOCK_HELD=0
FAIL_REASON=""
RUN_STARTED="$(date +%s)"
LEASE_STARTED=""
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
  stop_pg
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
  release_lock
  exit $rc
}
trap cleanup EXIT
trap 'log "ERR line $LINENO: $BASH_COMMAND"' ERR
trap 'exit 130' INT TERM

acquire_lock() {
  mkdir -p "$(dirname "$LOCK")"
  # A local PG/panel/nginx is started by this run, so the local runtime lock is
  # mandatory per the plan: taken before the "before" snapshot and held until all
  # owned processes are reaped. No remote/preview/203 resource is created, so the
  # remote-runtime lock is not used at all.
  exec 9>"$LOCK"
  if flock -w "$LOCK_WAIT" 9; then
    LOCK_HELD=1
    LEASE_STARTED="$(date +%s)"
    { echo "lock=$LOCK"; echo "state=held"; echo "role=local-runtime"; } >"$EVIDENCE/lock-note.txt"
    check_ok "local-runtime lock held (fd 9) before the before-snapshot"
  else
    # Bounded wait only. Contention is an ENV failure, never a reason to run
    # unprotected: an unlocked run could not legitimately assert "no listener lost".
    { echo "lock=$LOCK"; echo "state=contended"; fuser -v "$LOCK" 2>&1; } >"$EVIDENCE/lock-note.txt" 2>&1 || true
    fail "local-runtime lock not acquired within ${LOCK_WAIT}s (another owned runtime is active); rerun when free"
  fi
}

release_lock() {
  if [ "${LOCK_HELD:-0}" = 1 ]; then
    flock -u 9 2>/dev/null || true
    exec 9>&- 2>/dev/null || true
    LOCK_HELD=0
    check_ok "local-runtime lock released after all owned processes were reaped"
  fi
  return 0
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
  # Loopback listeners only. The host has other users/tasks on other addresses
  # (e.g. LAN addresses); those are outside this run's boundary but still
  # recorded, so `listen_127` filters to 127.0.0.1 wildcard-free entries.
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
 'listen_127': sorted({
   a for a in os.popen("ss -ltnH 2>/dev/null | awk '{print $4}'").read().split() if a.startswith('127.0.0.1:')
 }),
 'listen_all': sorted({
   a for a in os.popen("ss -ltnH 2>/dev/null | awk '{print $4}'").read().split()
 }),
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
           'note': 'loopback-only comparison; other-interface listeners are informational (listen_all)',
           'server_process_identity_unchanged': not any(
               k in diff for k in ('pid1_cmdline','pid1_ns_pid','host_ns_pid','host_ns_mnt','host_ns_net'))},
          open(out,'w'), indent=2)
assert not diff, f'protected host identity changed: {diff}'
# The only loopback listeners this run may add are its own random panel/HTTPS
# ports, and they must already be gone when the after-snapshot is taken.
assert not new_listen, f'loopback listener left behind: {new_listen}'
# Symmetric requirement: an unrelated process disappearing is NOT ignorable.
# Any change is the caller's signal to re-run under a quiet local-runtime lease.
assert not stale_listen, (f'loopback listener disappeared during this run: {stale_listen}; '
                          're-run when the host runtime is quiet (unrelated tasks in scope)')
print('host identity unchanged (no new and no lost loopback listener)')
PY
  chmod 600 "$EVIDENCE/host-identity-diff.json"
  check_ok "protected host identity unchanged before/after (pid1/namespaces/cgroup, no leftover 127 listener)"
}

leftover_check() {
  local prefix="$WORK/nginx/prefix"
  # A run that exceeded its own budget is not a valid quiet-runtime observation.
  local elapsed=$(( $(date +%s) - RUN_STARTED ))
  echo "elapsed_seconds=$elapsed lease_seconds=$(( $(date +%s) - LEASE_STARTED ))" >"$EVIDENCE/timing.txt"
  chmod 600 "$EVIDENCE/timing.txt"
  [ "$elapsed" -le "${PPF_MAX_SECONDS:-180}" ] || fail "run took ${elapsed}s, over the ${PPF_MAX_SECONDS:-180}s budget"
  [ ! -e "$prefix/logs/nginx.pid" ] || fail "nginx pidfile left behind"
  if pgrep -f "$WORK/nginx/nginx -p $prefix" >/dev/null 2>&1; then fail "nginx process left behind"; fi
  if ss -ltn "sport = :$HTTPS_PORT" 2>/dev/null | grep -q LISTEN; then fail "https port $HTTPS_PORT still listening"; fi
  if ss -ltn "sport = :$PANEL_PORT" 2>/dev/null | grep -q LISTEN; then fail "panel port $PANEL_PORT still listening"; fi
  check_ok "no leftover nginx/panel process, pidfile or listener; run finished in ${elapsed}s (<= ${PPF_MAX_SECONDS:-180}s)"
}

# --------------------------------------------------- real user-space nginx ELF
# ------------------------------------------------- verified package acquisition
# The executed nginx ELF must be the *official* Ubuntu build for the pinned
# version, so the digest cannot come from the download itself. Chain:
#   1. HTTPS fetch of dists/<suite>/InRelease
#   2. gpgv against the distro's own ubuntu-archive-keyring -> good signature
#   3. InRelease's own SHA256 section authenticates main/binary-amd64/Packages.gz
#   4. the authenticated index gives the .deb SHA256 for the pinned version
#   5. HTTPS fetch of that pool path; the digest must equal step 4
# Nothing is installed; the payload is only unpacked into the private prefix.
# There is no caller-supplied binary/deb override, by design.
fetch_verified_deb() {
  local suite="$1" package="$2" version="$3" dest="$4" log="$5"
  local meta="$WORK/pkgmeta" dir="$WORK/pkgmeta/$package"
  mkdir -p "$meta" "$dir"
  chmod 700 "$meta" "$dir"
  local inrelease="$meta/${suite//\//_}.InRelease"
  local pkgs="$dir/Packages.gz"
  local base="$UBUNTU_HTTPS/dists/$suite"
  curl "${CURL_HTTPS[@]}" -fsS -o "$inrelease" "$base/InRelease" \
    || fail "cannot fetch $base/InRelease over HTTPS"
  gpgv --keyring "$KEYRING" "$inrelease" >"$dir/gpgv.log" 2>&1 \
    || { cat "$dir/gpgv.log" >&2; fail "Ubuntu InRelease signature verification failed for $suite"; }
  grep -q 'Good signature' "$dir/gpgv.log" || fail "gpgv did not report a good signature for $suite"
  local expected_pkgs
  expected_pkgs="$(awk '/^SHA256:/{f=1;next} /^[A-Za-z]/{f=0} f && $3=="main/binary-amd64/Packages.gz"{print $1}' "$inrelease")"
  [ -n "$expected_pkgs" ] || fail "InRelease ($suite) has no SHA256 for main/binary-amd64/Packages.gz"
  curl "${CURL_HTTPS[@]}" -fsS -o "$pkgs" "$base/main/binary-amd64/Packages.gz" \
    || fail "cannot fetch $base/main/binary-amd64/Packages.gz over HTTPS"
  [ "$(sha256 "$pkgs")" = "$expected_pkgs" ] \
    || fail "Packages.gz digest mismatch for $suite (authenticated=$expected_pkgs actual=$(sha256 "$pkgs"))"
  gzip -dc "$pkgs" >"$dir/Packages" || fail "cannot decompress authenticated Packages.gz"
  local filename expected_deb
  filename="$(awk -v p="$package" -v v="$version" '
    /^Package: /{cur=$2} cur==p && /^Version: /{ver=$2}
    cur==p && ver==v && /^Filename: /{fn=$2}
    cur==p && ver==v && /^SHA256: /{print fn" "$2; exit}' "$dir/Packages")"
  [ -n "$filename" ] || fail "pinned $package=$version not found in authenticated $suite index"
  expected_deb="${filename##* }"
  local pool_path="${filename%% *}"
  curl "${CURL_HTTPS[@]}" -fsS -o "$dest" "$UBUNTU_HTTPS/$pool_path" \
    || fail "cannot fetch $UBUNTU_HTTPS/$pool_path over HTTPS"
  [ "$(sha256 "$dest")" = "$expected_deb" ] \
    || fail "$package deb digest does not match authenticated index (expected=$expected_deb actual=$(sha256 "$dest"))"
  {
    echo "package=$package"
    echo "version=$version"
    echo "suite=$suite"
    echo "origin=$UBUNTU_HTTPS"
    echo "inrelease=$base/InRelease"
    echo "inrelease_sha256=$(sha256 "$inrelease")"
    echo "signing_key=$(grep -o 'using RSA key [0-9A-F]*' "$dir/gpgv.log" | awk '{print $4}')"
    echo "gpgv_reported=$(grep -m1 'Good signature' "$dir/gpgv.log")"
    echo "packages_index=$base/main/binary-amd64/Packages.gz"
    echo "packages_index_sha256=$expected_pkgs"
    echo "deb_url=$UBUNTU_HTTPS/$pool_path"
    echo "deb_sha256_verified=$expected_deb"
    echo "installed=no (unpacked only)"
    echo "protocol=https-only"
  } >>"$log"
  chmod 600 "$log"
}

obtain_nginx() {
  local dir="$WORK/nginx"
  mkdir -p "$dir"
  chmod 700 "$dir"
  local deb="$dir/nginx_${NGINX_VERSION_PIN}_amd64.deb"
  : >"$dir/provenance.txt"
  fetch_verified_deb "noble-updates" "nginx" "$NGINX_VERSION_PIN" "$deb" "$dir/provenance.txt"
  dpkg-deb -e "$deb" "$dir/control"
  dpkg-deb -x "$deb" "$dir/rootfs"
  # Package payloads carry world-readable modes; keep the whole private prefix
  # owner-only as required for this evidence tree.
  chmod -R go-rwx "$dir/control" "$dir/rootfs"
  cp "$dir/rootfs/usr/sbin/nginx" "$dir/nginx"
  grep -E '^(Package|Version|Source|Architecture):' "$dir/control/control" >>"$dir/provenance.txt"
  chmod 700 "$dir/nginx"
  NGINX_ELF="$dir/nginx"
  NGINX_VERSION="$("$NGINX_ELF" -v 2>&1 | tr -d '\r')"
  [ "$(sha256 "$NGINX_ELF")" = "$(sha256 "$dir/rootfs/usr/sbin/nginx")" ] \
    || fail "extracted nginx ELF does not match the verified package payload"
  echo "nginx_elf_sha256=$(sha256 "$NGINX_ELF")" >>"$dir/provenance.txt"
  check_ok "official nginx ELF from GPG-verified Ubuntu index ($NGINX_VERSION, https-only, not installed)"
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
  local certutil=""
  local deb="$dir/libnss3-tools_${NSS_VERSION_PIN}_amd64.deb"
  if fetch_verified_deb "noble-updates" "libnss3-tools" "$NSS_EPOCH_PIN:$NSS_VERSION_PIN" \
       "$deb" "$dir/provenance.txt" >>"$dir/fetch.log" 2>&1; then
    dpkg-deb -x "$deb" "$dir/rootfs"
    chmod -R go-rwx "$dir/rootfs"
    certutil="$dir/rootfs/usr/bin/certutil"
  else
    tail -5 "$dir/fetch.log" >&2 || true
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
# Build-only phase: no runtime resource, so it runs before the local lease.
build_panel() {
  local panelbin="$WORK/bin/panel"
  mkdir -p "$WORK/bin"
  chmod 700 "$WORK/bin"
  ( cd "$ROOT" && "$GO_BIN" build -trimpath -buildvcs=false -o "$panelbin" ./cmd/panel ) \
    >"$EVIDENCE/go-build.log" 2>&1 || fail "panel build failed"
  PANELBIN="$panelbin"
  PANEL_BIN_SHA="$(sha256 "$panelbin")"

  WEBROOT="$ROOT/web/dist"
  if [ ! -f "$WEBROOT/index.html" ]; then
    ( cd "$ROOT/web" && npm run build ) >"$EVIDENCE/web-build.log" 2>&1 \
      || fail "web build failed (web/dist/index.html unavailable)"
  fi
  WEB_INDEX_SHA="$(sha256 "$WEBROOT/index.html")"
  ASSET_JS="$(grep -o 'assets/[^"]*\.js' "$WEBROOT/index.html" | head -1)"
  [ -n "$ASSET_JS" ] || fail "could not locate hashed asset in dist/index.html"
  WEB_ASSET_SHA="$(sha256 "$WEBROOT/$ASSET_JS")"
  ASSET_PATH="/$ASSET_JS"
  check_ok "built owned panel + recorded web/dist asset sha (pre-lease, no runtime)"
}

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
  check_ok "owned private PG cluster on 127.0.0.1:$PG_PORT (temp prefix, own data dir, under local lease)"

  PANEL_PORT="$(rand_port)"
  export DATABASE_URL="postgres://postgres@127.0.0.1:$PG_PORT/webdemo_host_nginx?sslmode=disable"
  export PANEL_LISTEN="127.0.0.1:$PANEL_PORT"
  export PANEL_TRUST_REVERSE_PROXY=true
  export PANEL_DEV=false
  export PANEL_MASTER_KEY="$(openssl rand -base64 32)"
  export PANEL_MIGRATIONS_DIR="$ROOT/migrations"
  export PANEL_PUBLIC_URL="https://localhost:$HTTPS_PORT"
  export PANEL_ADMIN_USER=admin
  export PANEL_ADMIN_PASSWORD="ppf-$(openssl rand -hex 12)"
  start_panel spa
}

# start_panel <spa|api>: (re)start the SAME owned binary against the SAME owned
# database with the SAME master key. Only PANEL_WEB_ROOT differs, which is what
# actually makes hosting #2 an API-only panel instead of a second SPA.
start_panel() {
  local mode="$1"
  stop_panel
  if [ "$mode" = "spa" ]; then
    export PANEL_WEB_ROOT="$WEBROOT"
  else
    export PANEL_WEB_ROOT=""
  fi
  ( cd "$ROOT" && exec "$PANELBIN" ) >"$EVIDENCE/panel-$mode.log" 2>&1 &
  PANELPID=$!
  local ready=0
  for _ in $(seq 1 120); do
    if [ "$(curl "${CURL_LOCAL[@]}" -sS -o /dev/null -w '%{http_code}' \
          "http://127.0.0.1:$PANEL_PORT/api/v1/auth/session" 2>/dev/null)" = "401" ]; then
      ready=1; break
    fi
    kill -0 "$PANELPID" 2>/dev/null || fail "panel ($mode) exited during startup (see panel-$mode.log)"
    sleep 0.5
  done
  [ "$ready" = 1 ] || fail "panel ($mode) did not become ready on 127.0.0.1:$PANEL_PORT"
  if grep -qi 'panic\|fatal' "$EVIDENCE/panel-$mode.log"; then fail "panel ($mode) log shows fatal error"; fi
  local web_root_log
  web_root_log="$(grep -c 'web: serving SPA from' "$EVIDENCE/panel-$mode.log" || true)"
  if [ "$mode" = "spa" ]; then
    [ "$web_root_log" = "1" ] || fail "spa panel did not report serving the SPA (PANEL_WEB_ROOT missing?)"
  else
    [ "$web_root_log" = "0" ] || fail "api panel still serves the SPA (PANEL_WEB_ROOT not empty)"
    grep -q 'serving API only' "$EVIDENCE/panel-$mode.log" \
      || fail "api panel did not report API-only mode"
  fi
  check_ok "owned panel ($mode) ready on 127.0.0.1:$PANEL_PORT, PANEL_WEB_ROOT=$([ "$mode" = spa ] && echo set || echo empty)"
}

# Direct-to-Go probes. These prove what the panel itself answers, before any
# nginx static layer is involved, so hosting #2 cannot look "API only" merely
# because nginx happens to serve the files.
go_direct_probes() {
  local mode="$1"
  local out="$EVIDENCE/go-direct-$mode.txt"
  local base="http://127.0.0.1:$PANEL_PORT"
  : >"$out"
  probe_code() { # name path expect
    local name="$1" path="$2" expect="$3"
    local body="$EVIDENCE/go-direct-$mode-$name.body" code ctype
    code="$(curl "${CURL_LOCAL[@]}" -sS -o "$body" -w '%{http_code}' -D "$body.h" "$base$path" 2>>"$out" || true)"
    ctype="$(tr -d '\r' <"$body.h" | awk 'tolower($1)=="content-type:"{print $2; exit}')"
    echo "$name path=$path code=$code content_type=${ctype:-none} expect=$expect" >>"$out"
    rm -f "$body.h"
    printf '%s' "$code"
  }
  if [ "$mode" = "spa" ]; then
    [ "$(probe_code root / 200)" = 200 ] || fail "spa: Go root is not 200"
    grep -q 'id="app"' "$EVIDENCE/go-direct-spa-root.body" || fail "spa: Go root is not the SPA shell"
    [ "$(probe_code deeplink /admin/nodes 200)" = 200 ] || fail "spa: Go deeplink is not 200"
    grep -q 'id="app"' "$EVIDENCE/go-direct-spa-deeplink.body" || fail "spa: Go deeplink is not the SPA shell"
    [ "$(probe_code asset "$ASSET_PATH" 200)" = 200 ] || fail "spa: Go hashed asset is not 200"
    check_ok "spa: Go itself serves root/deeplink/asset as SPA (200)"
  else
    local code
    code="$(probe_code root / 404)"
    [ "$code" = 404 ] || fail "api: Go root returned $code, expected non-HTML 404"
    ! grep -qi '<html\|id="app"' "$EVIDENCE/go-direct-api-root.body" || fail "api: Go root returned SPA HTML"
    code="$(probe_code deeplink /admin/nodes 404)"
    [ "$code" = 404 ] || fail "api: Go deeplink returned $code, expected non-HTML 404"
    ! grep -qi '<html\|id="app"' "$EVIDENCE/go-direct-api-deeplink.body" || fail "api: Go deeplink returned SPA HTML"
    code="$(probe_code asset "$ASSET_PATH" 404)"
    [ "$code" = 404 ] || fail "api: Go hashed asset returned $code, expected non-HTML 404"
    [ "$(probe_code apisession /api/v1/auth/session 401)" = 401 ] || fail "api: Go API is not answering"
    [ "$(probe_code readyz /readyz 200)" = 200 ] || fail "api: Go /readyz is not 200"
    check_ok "api: Go itself answers non-HTML 404 for root/deeplink/asset while the API works"
  fi
  chmod 600 "$out"
  chmod 600 "$EVIDENCE"/go-direct-"$mode"-*.body 2>/dev/null || true
}

session_cookie_value() {
  local jar_headers="$1"
  tr -d '\r' <"$jar_headers" | awk 'tolower($1)=="set-cookie:" && $2 ~ /^pp_session=/{v=$2; sub(/^pp_session=/,"",v); sub(/;.*/,"",v); print v; exit}'
}

# Swap hosting #1 (Go SPA) to hosting #2 (API only) on the same DB/binary/key and
# prove an already-issued session survives the restart.
switch_to_api_only() {
  local hdr="$EVIDENCE/session-before-restart.headers"
  curl "${CURL_LOCAL[@]}" -sS -o /dev/null -D "$hdr" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$PANEL_ADMIN_USER\",\"password\":\"$PANEL_ADMIN_PASSWORD\"}" \
    "http://127.0.0.1:$PANEL_PORT/api/v1/auth/login" || fail "spa panel: direct login failed"
  local cookie
  cookie="$(session_cookie_value "$hdr")"
  [ -n "$cookie" ] || { cat "$hdr" >&2; fail "spa panel: login set no pp_session cookie"; }
  tr -d '\r' <"$hdr" | grep -qi 'set-cookie: .*pp_session=.*Secure' || fail "session cookie is not Secure"
  tr -d '\r' <"$hdr" | grep -qi 'set-cookie: .*pp_session=.*HttpOnly' || fail "session cookie is not HttpOnly"
  echo "session_cookie_secure_httponly=1" >"$EVIDENCE/session-continuity.txt"
  start_panel api
  [ "$(curl "${CURL_LOCAL[@]}" -sS -o /dev/null -w '%{http_code}' \
        -H "Cookie: pp_session=$cookie" "http://127.0.0.1:$PANEL_PORT/api/v1/auth/session")" = "200" ] \
    || fail "session issued before the API-only restart is no longer valid"
  echo "session_survives_api_only_restart=1 (same DB, same master key, same binary)" >>"$EVIDENCE/session-continuity.txt"
  chmod 600 "$EVIDENCE/session-continuity.txt" "$hdr"
  check_ok "panel restarted in API-only mode with the same DB/key/binary; pre-restart session still valid"
  go_direct_probes api
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
    if curl "${CURL_LOCAL[@]}" -fsS --cacert "$WORK/pki/ca.crt" "https://localhost:$HTTPS_PORT/login" -o /dev/null; then break; fi
    sleep 0.25
  done
  nginx_proc_identity "$mode"
  check_ok "nginx master started as host user from extracted ELF (mode=$mode, prefix=$prefix)"
}

stop_pg() {
  if [ "${PG_STARTED:-0}" = 1 ] && [ -n "${WORK:-}" ]; then
    "$PG_BIN/pg_ctl" -D "$WORK/pg/data" -m fast stop >>"$EVIDENCE/pg-stop.log" 2>&1 || true
    PG_STARTED=0
  fi
  # The socket/port may take a moment to be released; the after-snapshot must not
  # race it, otherwise our own port would look like an unrelated lost listener.
  local _i
  for _i in $(seq 1 50); do
    ss -ltn 2>/dev/null | grep -q ":$PG_PORT " || break
    sleep 0.1
  done
  return 0
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
  local rc err

  # 1. Default trust store must reject: curl exit 60 (peer certificate cannot be
  # authenticated) plus the issuer diagnostic, with NO --cacert. A network error,
  # a timeout, or a bare 000 is explicitly not accepted as "rejected".
  set +e
  err="$(curl "${CURL_LOCAL[@]}" -sS -o /dev/null -w '%{http_code}' "$base/login" 2>&1)"
  rc=$?
  set -e
  echo "default_store_curl_exit=$rc" >>"$out"
  echo "default_store_output=$err" >>"$out"
  [ "$rc" = "60" ] || fail "default store probe exited $rc (expected 60); a non-certificate failure cannot prove rejection"
  grep -qi 'unable to get local issuer certificate' <<<"$err" \
    || fail "default store rejection lacks the CA-issuer diagnostic: $err"

  # 2. Positive control: strict trust on both SAN entries.
  curl "${CURL_LOCAL[@]}" -fsS --cacert "$WORK/pki/ca.crt" "$base/login" -o "$EVIDENCE/tls-login.html" \
    >>"$out" 2>&1 || fail "curl --cacert to https://localhost failed"
  curl "${CURL_LOCAL[@]}" -fsS --cacert "$WORK/pki/ca.crt" "https://127.0.0.1:$HTTPS_PORT/login" -o /dev/null \
    >>"$out" 2>&1 || fail "curl --cacert to https://127.0.0.1 (SAN IP) failed"
  echo 'ca_trusted_localhost_and_san_ip=1' >>"$out"

  # 3. Hostname mismatch must fail for the certificate reason (exit 60 +
  # SAN text), not for connectivity. --noproxy '*' keeps HTTPS_PROXY from
  # turning wrong.invalid into a proxy error.
  set +e
  err="$(curl "${CURL_LOCAL[@]}" -sS -o /dev/null --cacert "$WORK/pki/ca.crt" \
          --connect-to "wrong.invalid:$HTTPS_PORT:127.0.0.1:$HTTPS_PORT" \
          "https://wrong.invalid:$HTTPS_PORT/login" 2>&1)"
  rc=$?
  set -e
  echo "hostname_mismatch_curl_exit=$rc" >>"$out"
  echo "hostname_mismatch_output=$err" >>"$out"
  [ "$rc" = "60" ] || fail "hostname mismatch exited $rc (expected 60); not a certificate refusal"
  grep -Eqi 'no alternative certificate subject name|does not match|subject name' <<<"$err" \
    || fail "hostname mismatch rejection lacks a SAN/hostname diagnostic: $err"
  echo 'hostname_mismatch_rejected_by_certificate=1' >>"$out"

  # 4. Node client with the private CA and rejectUnauthorized (no bypass).
  node -e '
const https=require("https"), fs=require("fs");
const ca=fs.readFileSync(process.argv[1]); const port=process.argv[2];
const req=https.request({host:"localhost",port,path:"/login",ca,rejectUnauthorized:true,servername:"localhost"},res=>{
  let n=0; res.on("data",c=>{n+=c.length}); res.on("end",()=>{console.log(JSON.stringify({status:res.statusCode,bytes:n,authorized:req.socket.authorized,protocol:req.socket.getProtocol()})); process.exit(res.statusCode===200?0:1)});
});
req.on("error",e=>{console.error("ERR",e.message);process.exit(2)}); req.end();
' "$WORK/pki/ca.crt" "$HTTPS_PORT" >>"$out" 2>&1 || fail "Node rejectUnauthorized TLS client failed"
  # ...and the same client must fail without the CA.
  set +e
  node -e '
const https=require("https");
const req=https.request({host:"localhost",port:process.argv[1],path:"/login",rejectUnauthorized:true},res=>{console.log("UNEXPECTED",res.statusCode);process.exit(1)});
req.on("error",e=>{console.log("expected_error="+e.code);process.exit(e.code==="UNABLE_TO_VERIFY_LEAF_SIGNATURE"?0:2)}); req.end();
' "$HTTPS_PORT" >>"$out" 2>&1
  rc=$?
  set -e
  [ "$rc" = "0" ] || fail "Node without the private CA did not fail with UNABLE_TO_VERIFY_LEAF_SIGNATURE (rc=$rc)"

  # 5. hostile XFF must not expand the trusted network. Each attempt spoofs a
  # DIFFERENT client address; if the panel trusted the override every attempt
  # would be a fresh source and never hit the per-source budget, so the observed
  # 429 proves the address actually used is the nginx peer (127.0.0.1), not XFF.
  # A throwaway username is used so the real admin budget is untouched.
  local i code_i
  for i in $(seq 1 12); do
    code_i="$(curl "${CURL_LOCAL[@]}" --cacert "$WORK/pki/ca.crt" \
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
  check_ok "strict TLS: default store exit60+issuer, --cacert/Node verify, SAN mismatch exit60"
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
# Same-invocation Go evidence: raw -json plus the raw exit status of the owned
# contract test. A skip or 0 executed cases must never be readable as a pass.
go_contract_run() {
  local json="$EVIDENCE/go-test.json" rawexit="$EVIDENCE/go-test.rawexit"
  ( cd "$ROOT" && "$GO_BIN" test ./tests/e2e/ -run '^TestWebdemoHostNginxVerifyScriptContract$' \
      -count=1 -v -json ) >"$json" 2>"$EVIDENCE/go-test.stderr.log"
  echo "$?" >"$rawexit"
  chmod 600 "$json" "$rawexit" "$EVIDENCE/go-test.stderr.log"
  [ "$(cat "$rawexit")" = "0" ] || { tail -5 "$EVIDENCE/go-test.stderr.log" >&2; fail "Go contract test exited $(cat "$rawexit")"; }
  python3 - "$json" "$EVIDENCE/go-test.summary.json" <<'PY'
import json, sys
acts = {'run': 0, 'pass': 0, 'skip': 0, 'fail': 0}
for line in open(sys.argv[1]):
    line = line.strip()
    if not line.startswith('{'):
        continue
    ev = json.loads(line)
    if ev.get('Action') in acts:
        acts[ev['Action']] += 1
json.dump(acts, open(sys.argv[2], 'w'), indent=2)
assert acts['run'] > 0, f'no Go test executed: {acts}'
assert acts['skip'] == 0, f'Go test skipped: {acts}'
assert acts['fail'] == 0, f'Go test failed: {acts}'
# -json emits a package-level pass in addition to test-level ones, so pass >= run.
assert acts['pass'] >= acts['run'], f'Go test did not pass: {acts}'
print('go test json ok', acts)
PY
  chmod 600 "$EVIDENCE/go-test.summary.json"
  check_ok "Go contract test passed in this same run (raw -json + rawexit, no skip/0 cases)"
}

browser_report_assert() {
  local mode="$1"
  local dir="$EVIDENCE/browser-$mode"
  local expect_asset="$WEB_ASSET_SHA"
  python3 - "$mode" "$dir" "$expect_asset" <<'PY'
import json, sys, os
mode, d, expect = sys.argv[1], sys.argv[2], sys.argv[3]
code = open(os.path.join(d, 'exit-code.txt')).read().strip()
assert code == '0', f'{mode}: browser exit code {code}'
rep = json.load(open(os.path.join(d, 'report.json')))
assert rep['status'] == 'passed', f"{mode}: report status {rep['status']}"
assert rep['errors'] == [], f"{mode}: browser errors {rep['errors']}"
asset = rep.get('asset') or {}
assert asset.get('sha256') == expect, f"{mode}: asset sha {asset.get('sha256')} != manifest {expect}"
print(f'{mode}: report passed, asset sha matched {expect}')
PY
  check_ok "browser $mode report status/exit 0 and hashed asset sha matches the recorded build"
}

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
 'uid': os.getuid(), 'gid': os.getgid(), 'euid_is_root': os.geteuid() == 0,
 'hostings': {
   'spa': {'panel_web_root': 'set', 'go_serves_spa': True,
           'nginx_terminates_tls_and_proxies_to_go': True},
   'api': {'panel_web_root': 'empty', 'go_serves_api_only': True,
           'nginx_serves_static_dist': True,
           'same_db_same_key_same_binary': True},
 },
 'local_runtime_lock': {
   'path': os.path.join(os.sep, 'home/compile/Work/.locks/proxypanel-webdemo-local-runtime.lock'),
   'role': 'local-runtime', 'held_before_snapshot': True, 'released_after_cleanup': True,
 },
 'remote_runtime_lock_used': False,
 'system_level_install': 'not_run (no root, no disposable PID1)',
 'public_ca_or_dns': 'not_run (not required by the story source)',
}
for mode in ('spa', 'api'):
    d = os.path.join(ev, 'browser-' + mode)
    rep = json.load(open(os.path.join(d, 'report.json')))
    m['hostings'][mode]['browser'] = {
        'status': rep.get('status'), 'checks': rep.get('checks'),
        'asset_path': (rep.get('asset') or {}).get('path'),
        'asset_sha256': (rep.get('asset') or {}).get('sha256'),
        'browser_matched_build': (rep.get('asset') or {}).get('sha256') == asset_sha,
        'ignore_https_errors': False,
        'trusted_via_temporary_home_nss': os.path.exists(os.path.join(work, 'nss/trust.txt')),
    }
if os.path.exists(os.path.join(work,'nss/provenance.txt')):
    m['certutil_provenance'] = open(os.path.join(work,'nss/provenance.txt')).read().strip().splitlines()
assert m['uid'] != 0 and not m['euid_is_root'], 'must not run as root'
assert not m['remote_runtime_lock_used'], 'no remote resource may be created by this round'
for mode in ('spa','api'):
    assert m['hostings'][mode]['browser']['browser_matched_build'], f'{mode} asset sha != build sha'
    assert m['hostings'][mode]['browser']['status'] == 'passed', f'{mode} browser not passed'
json.dump(m, open(os.path.join(ev,'manifest.json'),'w'), indent=2)
print('manifest ok')
PY
  chmod 600 "$MANIFEST"
}

dump_arrays() {
  [ -n "$WORK" ] && [ -d "$WORK" ] || return 0
  printf '%s\n' ${CHECKS[@]+"${CHECKS[@]}"} >"$WORK/checks.txt" 2>/dev/null || : >"$WORK/checks.txt"
  if [ "${#NOTRUN[@]}" -gt 0 ]; then
    printf '%s\n' "${NOTRUN[@]}" >"$WORK/notrun.txt"
  else
    : >"$WORK/notrun.txt"
  fi
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
  [ "$(id -u)" != "0" ] || fail "refusing to run as root: this verifies the unprivileged host-user path"
  check_ok "running as uid $(id -u) (not root)"
  # Build steps (package download/verify, panel build, web build) are safe without
  # the lease; the lease starts before the "before" snapshot and the fixtures.
  obtain_nginx
  make_pki
  build_panel
  acquire_lock
  snapshot_host_identity "$EVIDENCE/host-before.json"
  check_ok "host identity snapshot (pid1 / namespaces / non-docker) recorded"
  go_contract_run
  HTTPS_PORT="$(rand_port)"
  start_fixture            # build + PG + panel, hosting #1 = Go SPA
  go_direct_probes spa
  nginx_write_config spa
  nginx_start spa
  strict_probes
  if install_nss_trust; then
    browser_mode spa || fail "spa browser (trusted NSS) failed"
    NSS_OK=1
  else
    NSS_OK=0
    # Narrow fallback while nginx still fronts hosting #1: labelled bypass only.
    browser_mode_selfsigned spa || fail "spa browser (self-signed fallback) failed"
  fi
  nginx_stop
  # Hosting #2 is a genuine API-only panel on the same owned DB/binary/key;
  # only the hosting layer (nginx static dist) changes afterwards.
  switch_to_api_only
  nginx_write_config api
  nginx_start api
  if [ "$NSS_OK" = 1 ]; then
    browser_mode api || fail "api browser (trusted NSS) failed"
  else
    browser_mode_selfsigned api || fail "api browser (self-signed fallback) failed"
  fi
  nginx_stop
  stop_panel
  stop_pg
  # Chromium creates caches under the temporary HOME; re-assert owner-only modes.
  chmod -R go-rwx "$HOME_ISO" "$WORK/nginx" "$WORK/nss" "$WORK/pg" "$WORK/pki" 2>/dev/null || true
  browser_report_assert spa
  browser_report_assert api
  leftover_check
  snapshot_host_identity "$EVIDENCE/host-after.json"
  compare_host_identity
  write_manifest
  release_lock
  dump_arrays
  write_result passed
  log "HOST_NGINX_VERIFIED evidence=$EVIDENCE"
}

main "$@"
