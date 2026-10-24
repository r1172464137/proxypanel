#!/bin/bash
# web-demo group A owned launcher: real browser + real Agent + official Xray +
# a NEW private PostgreSQL cluster. It never touches the product-readiness
# cluster (15432), the 8445 preview, the old 8443 stack or any agent lab.
#
# Runtime isolation: this script opens the shared host-wide runtime lock itself
# on its own file descriptor *before* any snapshot, Xray version call, private
# PostgreSQL start or fixture start, and holds it until cleanup has finished.
# There is no environment flag that can claim the lock: either this process owns
# the real flock or the run is reported as blocked. Authoring never holds it.
set -euo pipefail
umask 077

LOCK=/home/compile/Work/.locks/proxypanel-webdemo-local-runtime.lock
LOCK_WAIT=${WEBDEMO_LOCK_WAIT:-90}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
MAIN=${WEBDEMO_MAIN_ROOT:-/home/compile/Work/proxypanel}
source "${WEBDEMO_GO_ENV:-/home/compile/Work/env.sh}"
GO=${GO_BIN:-$(command -v go || true)}
PG_BIN=${PG_BIN:-/home/compile/sdk/pkg/usr/lib/postgresql/17/bin}
XRAY=${WEBDEMO_XRAY_BIN:-/home/compile/Work/bin/xray}
export LD_LIBRARY_PATH=${PG_LIB:-/home/compile/sdk/pkg/usr/lib/x86_64-linux-gnu}${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
EVIDENCE=${WEBDEMO_EVIDENCE_DIR:-$MAIN/.ai/evidence/web-demo/A/$STAMP}
NODE_DEADLINE_MS=${WEBDEMO_DEADLINE_MS:-$((6 * 60 * 1000))}
NODE_HARD_TIMEOUT=${WEBDEMO_SHELL_TIMEOUT:-480}
SCHEMA_DB=wdemo_$(head -c8 /dev/urandom | od -An -tx1 | tr -d ' \n')
PRIVATE=""
PGPORT=""
linked=false
fixture_pid=""
pg_pid=""
LOCK_FD=""

log() { printf '%s\n' "$*" | tee -a "${PRIVATE:-/dev/stderr}/launcher.log" >&2; }

# Precise dependency gate: a missing tool is `not_run`, never a red business
# assertion and never a half-started fixture.
missing=()
[[ -x "$XRAY" ]] || missing+=("xray:$XRAY")
[[ -x "$PG_BIN/initdb" && -x "$PG_BIN/pg_ctl" ]] || missing+=("postgres17:$PG_BIN")
[[ -n "$GO" && -x "$GO" ]] || missing+=("go")
command -v node >/dev/null || missing+=("node")
command -v flock >/dev/null || missing+=("flock")
[[ -d "$MAIN/web/node_modules" ]] || missing+=("node_modules:$MAIN/web/node_modules")
if [[ ${#missing[@]} -gt 0 ]]; then
  mkdir -p "$EVIDENCE" && chmod 700 "$EVIDENCE"
  printf '{"status":"not_run","reason":"missing owned dependencies","missing":["%s"],"candidate":"%s"}\n' \
    "$(IFS=','; echo "${missing[*]}")" "$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || echo unknown)" \
    > "$EVIDENCE/not-run.json"
  printf 'not_run: missing %s\n' "${missing[*]}" >&2
  exit 75
fi

mkdir -p "$(dirname "$LOCK")"
exec {LOCK_FD}>"$LOCK"
if ! flock -w "$LOCK_WAIT" "$LOCK_FD"; then
  mkdir -p "$EVIDENCE" && chmod 700 "$EVIDENCE"
  printf '{"status":"blocked","reason":"host runtime lease still held after %ss","candidate":"%s"}\n' \
    "$LOCK_WAIT" "$(git -C "$ROOT" rev-parse HEAD)" > "$EVIDENCE/blocked.json"
  printf 'blocked: web-demo runtime lease busy after %ss (ENV, not a product verdict)\n' "$LOCK_WAIT" >&2
  exit 75
fi

PRIVATE=$(mktemp -d /tmp/wdemo.XXXXXXXX)

# ── process identity snapshot (PID + starttime + exe + cwd + argv) ──────────
# Capture existing Agent/Panel/core/PG/Redis identities before our processes
# exist. A /tmp path is not proof of another fixture owner: all such daemons
# remain protected. The shared runtime lease serialises the observation window.
# Only exact one-shot Xray CLI commands are excluded from daemon identities.
snapshot_procs() {
  python3 - "$1" <<'PY'
import os, sys
lines = []
for pid in os.listdir('/proc'):
    if not pid.isdigit():
        continue
    try:
        raw = open(f'/proc/{pid}/cmdline', 'rb').read().replace(b'\0', b' ').decode('utf8', 'replace').strip()
        if not raw:
            continue
        exe = os.readlink(f'/proc/{pid}/exe')
        cwd = os.readlink(f'/proc/{pid}/cwd')
        start = open(f'/proc/{pid}/stat').read().split()[21]
    except Exception:
        continue
    name = os.path.basename(exe)
    if name not in ('xray', 'postgres', 'redis-server', 'agent', 'proxypanel-agent', 'panel', 'proxypanel-panel'):
        continue
    argv = open(f'/proc/{pid}/cmdline', 'rb').read().split(b'\0')
    if name == 'xray' and len(argv) > 1 and argv[1] in (b'version', b'api', b'x25519', b'uuid'):
        continue
    scope = 'protected'
    lines.append(f'{scope}|{pid}|{start}|{exe}|{cwd}|{raw}')
open(sys.argv[1], 'w').write('\n'.join(sorted(lines)) + ('\n' if lines else ''))
PY
}

stop_owned_pg() {
  [[ -n "$pg_pid" ]] || return 0
  if kill -0 "$pg_pid" 2>/dev/null; then
    "$PG_BIN/pg_ctl" -D "$PRIVATE/pgdata" -m immediate stop >"$PRIVATE/pg-stop.log" 2>&1 || kill -9 "$pg_pid" 2>/dev/null || true
    for _ in $(seq 1 40); do kill -0 "$pg_pid" 2>/dev/null || break; sleep 0.5; done
    if kill -0 "$pg_pid" 2>/dev/null; then
      log "ERROR: owned PostgreSQL $pg_pid did not exit"
      return 1
    fi
  fi
  pg_pid=""
  return 0
}

cleanup() {
  local status=$?
  if [[ -n "$fixture_pid" ]] && kill -0 "$fixture_pid" 2>/dev/null; then
    touch "$PRIVATE/done"
    for _ in $(seq 1 120); do kill -0 "$fixture_pid" 2>/dev/null || break; sleep 0.5; done
    if kill -0 "$fixture_pid" 2>/dev/null; then
      kill -TERM "$fixture_pid" 2>/dev/null || true
      for _ in $(seq 1 20); do kill -0 "$fixture_pid" 2>/dev/null || break; sleep 1; done
    fi
    if kill -0 "$fixture_pid" 2>/dev/null; then
      log 'ERROR: owned fixture refused SIGTERM; SIGKILL'
      kill -9 "$fixture_pid" 2>/dev/null || true
      status=1
    fi
    wait "$fixture_pid" 2>/dev/null || true
    fixture_pid=""
  fi
  stop_owned_pg || status=1
  if [[ -d "$EVIDENCE" && -n "$PRIVATE" ]]; then
    for f in launcher.log fixture.log pg.log build.log build-go.log xray-version.log manifest.txt procs-before.txt procs-after.txt protected-lost.txt; do
      [[ -f "$PRIVATE/$f" ]] && cp "$PRIVATE/$f" "$EVIDENCE/" 2>/dev/null || true
    done
  fi
  $linked && rm -f "$ROOT/web/node_modules"
  log "cleanup: owned processes stopped, private dir $PRIVATE retained=$([[ ${KEEP:-0} == 1 ]] && echo yes || echo no)"
  [[ ${KEEP:-0} == 1 ]] || rm -rf -- "$PRIVATE"
  [[ -n "$LOCK_FD" ]] && flock -u "$LOCK_FD" 2>/dev/null || true
  exit $status
}
trap cleanup EXIT INT TERM

mkdir -p "$EVIDENCE"
chmod 700 "$EVIDENCE"
export WEBDEMO_RUN_DIR=$PRIVATE WEBDEMO_EVIDENCE_DIR=$EVIDENCE WEBDEMO_MAIN_ROOT=$MAIN WEBDEMO_XRAY_BIN=$XRAY
export WEBDEMO_DEADLINE_MS=$NODE_DEADLINE_MS

snapshot_procs "$PRIVATE/procs-before.txt"
cp "$PRIVATE/procs-before.txt" "$EVIDENCE/procs-before.txt" 2>/dev/null || true

# ── fingerprints (source + tools + build), written under the held lock ─────
{
  echo "candidate=$(git -C "$ROOT" rev-parse HEAD)"
  echo "worktree_sha=$(git -C "$ROOT" status --porcelain | sha256sum | cut -d' ' -f1)"
  echo "xray=$(sha256sum "$XRAY" | cut -d' ' -f1)"
  "$XRAY" version 2>&1 | head -1 > "$PRIVATE/xray-version.log"
  echo "xray_version=$(cat "$PRIVATE/xray-version.log")"
  echo "go=$($GO version)"
  echo "postgres=$("$PG_BIN/postgres" --version)"
  echo "runner_sha=$(sha256sum "$ROOT/web/tests/product-readiness-e2e.mjs" "$ROOT/web/tests/webdemo-pom.mjs" "$ROOT/web/tests/webdemo-env.sh" "$ROOT/tests/e2e/webdemo_ui_fixture_test.go" | cut -d' ' -f1 | tr '\n' ' ')"
} > "$PRIVATE/manifest.txt"

if [[ ! -e "$ROOT/web/node_modules" ]]; then ln -s "$MAIN/web/node_modules" "$ROOT/web/node_modules"; linked=true; fi

# ── owned PostgreSQL cluster (new, private, /tmp) ───────────────────────────
"$PG_BIN/initdb" -D "$PRIVATE/pgdata" -U wdemo_owner --auth=trust --encoding=UTF8 --locale=C.UTF-8 >"$PRIVATE/initdb.log" 2>&1
mkdir -p "$PRIVATE/socket"
PGPORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
"$PG_BIN/pg_ctl" -D "$PRIVATE/pgdata" -l "$PRIVATE/pg.log" -w -t 60 -o "-p $PGPORT -k $PRIVATE/socket -c listen_addresses=127.0.0.1 -c max_connections=40" start >/dev/null
pg_pid=$(head -1 "$PRIVATE/pgdata/postmaster.pid")
"$PG_BIN/createdb" -h 127.0.0.1 -p "$PGPORT" -U wdemo_owner "$SCHEMA_DB"
export WEBDEMO_TEST_DATABASE_URL="postgres://wdemo_owner@127.0.0.1:$PGPORT/$SCHEMA_DB?sslmode=disable"

# ── build the Go fixture binary once; bind it to the source fingerprint ─────
(cd "$ROOT" && "$GO" test -c -o "$PRIVATE/fixture.test" ./tests/e2e) >"$PRIVATE/build-go.log" 2>&1
echo "fixture_binary=$(sha256sum "$PRIVATE/fixture.test" | cut -d' ' -f1)" >> "$PRIVATE/manifest.txt"

# ── one SPA build, used by this run only ───────────────────────────────────
(cd "$ROOT/web" && npm run build -- --outDir "$PRIVATE/dist") >"$PRIVATE/build.log" 2>&1
export WEBDEMO_WEB_ROOT=$PRIVATE/dist
echo "web_root_sha=$(find "$PRIVATE/dist" -type f -print0 | sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1)" >> "$PRIVATE/manifest.txt"

# ── fixture (real Panel + real AgentSupervisor on demand) ──────────────────
(
  cd "$ROOT/tests/e2e"
  exec "$PRIVATE/fixture.test" -test.run '^TestWebDemoUIFixture$' -test.v -test.count=1 -test.timeout 10m
) >"$PRIVATE/fixture.log" 2>&1 &
fixture_pid=$!

for _ in $(seq 1 600); do
  [[ -f "$PRIVATE/state.json" ]] && break
  kill -0 "$fixture_pid" 2>/dev/null || { log 'fixture exited before ready'; exit 1; }
  sleep 0.2
done
[[ -f "$PRIVATE/state.json" ]] || { log 'fixture never became ready'; exit 1; }
log "fixture ready: $(python3 -c 'import json,sys;d=json.load(open(sys.argv[1]));print(d["schema"])' "$PRIVATE/state.json")"

status=0
# Hard wall clock for the browser runner. The Node runner also enforces its own
# deadline (default 6 min); this outer bound is the last-resort lock guard.
timeout --kill-after=30 "$NODE_HARD_TIMEOUT" node "$ROOT/web/tests/product-readiness-e2e.mjs" || status=$?
echo "browser_exit=$status" >> "$PRIVATE/manifest.txt"
echo "node_deadline_ms=$NODE_DEADLINE_MS" >> "$PRIVATE/manifest.txt"
echo "shell_timeout_s=$NODE_HARD_TIMEOUT" >> "$PRIVATE/manifest.txt"

touch "$PRIVATE/done"
fixture_stopped=false
for _ in $(seq 1 120); do
  if ! kill -0 "$fixture_pid" 2>/dev/null; then fixture_stopped=true; break; fi
  sleep 0.5
done
if $fixture_stopped; then
  # collect the real exit code of the fixture (t.Fatalf must not be hidden)
  wait "$fixture_pid" || { log 'ERROR: Go fixture reported failure'; status=1; }
  fixture_pid=""
else
  # Keep the pid so cleanup still owns this exact child; never orphan it.
  log 'fixture did not exit on done signal; terminating owned fixture'
  kill -TERM "$fixture_pid" 2>/dev/null || true
  for _ in $(seq 1 20); do kill -0 "$fixture_pid" 2>/dev/null || break; sleep 1; done
  if kill -0 "$fixture_pid" 2>/dev/null; then
    log 'ERROR: owned fixture refused SIGTERM; SIGKILL'
    kill -9 "$fixture_pid" 2>/dev/null || true
  fi
  wait "$fixture_pid" 2>/dev/null || true
  fixture_pid=""
  status=1
fi

# ── protected pre-existing processes + own leftovers ──────────────────────
# The protected set (host/preview/15432 processes) must survive with the exact
# same pid+starttime+exe+argv, including /tmp-based services. No path prefix
# alone can confer another owner's lease. Our children are identified only by
# this run's exact private directory.
# Owned PostgreSQL is stopped *before* the after-snapshot, so the snapshot
# proves both directions: protected host processes unchanged, nothing of ours left.
stop_owned_pg || status=1
snapshot_procs "$PRIVATE/procs-after.txt"
cp "$PRIVATE/procs-after.txt" "$EVIDENCE/procs-after.txt" 2>/dev/null || true
protected_checked=0
protected_lost=0
while IFS= read -r line; do
  [[ -z "$line" ]] && continue
  [[ "$line" == protected\|* ]] || continue
  protected_checked=$((protected_checked + 1))
  if ! grep -Fqx -- "$line" "$PRIVATE/procs-after.txt"; then
    log "ERROR: pre-existing protected process changed identity or vanished: ${line}"
    printf '%s\n' "$line" >> "$PRIVATE/protected-lost.txt"
    protected_lost=$((protected_lost + 1))
  fi
done < "$PRIVATE/procs-before.txt"
own_leftovers=$(grep -F -- "$PRIVATE" "$PRIVATE/procs-after.txt" | grep -c . || true)
other_owner_ephemeral=$(grep -c '^/tmp/|' "$PRIVATE/procs-before.txt" || true)
echo "protected_before=$(grep -c '^protected|' "$PRIVATE/procs-before.txt" || true)" >> "$PRIVATE/manifest.txt"
echo "protected_checked=$protected_checked" >> "$PRIVATE/manifest.txt"
echo "protected_lost=$protected_lost" >> "$PRIVATE/manifest.txt"
echo "other_owner_ephemeral_before=$other_owner_ephemeral" >> "$PRIVATE/manifest.txt"
echo "owned_process_leftovers=$own_leftovers" >> "$PRIVATE/manifest.txt"
if [[ "$protected_lost" != 0 ]]; then
  log 'ERROR: a protected pre-existing Xray/PostgreSQL process was lost or replaced'
  status=1
fi
if [[ ${own_leftovers:-0} != 0 ]]; then
  log 'ERROR: an owned Xray/PostgreSQL/fixture process survived cleanup'
  status=1
fi
cp "$PRIVATE/manifest.txt" "$EVIDENCE/manifest.txt"

log "evidence: $EVIDENCE"
exit "$status"
