#!/usr/bin/env bash
# Run the proxypanel Go test suite against the real test PostgreSQL/Redis on
# 192.168.5.203, from WSL.
#
# Why this script exists
# ----------------------
# 1. The suite has two environment inputs that must be set explicitly:
#    PANEL_TEST_DATABASE_URL and PANEL_TEST_REDIS_URL. Without them ~40 tests
#    silently SKIP and the run looks greener than it is.
# 2. `go test ./...` uses one process per package (GOMAXPROCS packages in
#    parallel). Every package creates its own isolated schema, but they all hit
#    the same PostgreSQL instance, and tests/e2e additionally starts a real Xray
#    core and proves the 60s hard-revocation budget. Under that contention the
#    revocation deadline can be missed for reasons that have nothing to do with
#    the change under test. `-p 1` removes the confound.
# 3. XRAY_BIN must point at a real Xray binary or the whole tests/e2e package
#    fails on a missing file instead of exercising the core.
#
# Usage: scripts/run-remote-tests.sh [serial|parallel]
#   serial   (default) -p 1, the authoritative result
#   parallel -p default, useful for a fast smoke run only
set -euo pipefail

MODE="${1:-serial}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(dirname "$REPO")"

export PATH="/home/compile/sdk/go1.27.3/go/bin:$PATH"
export GOTOOLCHAIN=local
export PANEL_BASH=/bin/bash
export XRAY_BIN="$WORK/bin/xray"

DB_JSON="$REPO/.local/test-databases.json"
if [ ! -f "$DB_JSON" ]; then
  echo "missing $DB_JSON (holds the isolated test credentials)" >&2
  exit 1
fi
export PANEL_TEST_DATABASE_URL="$(python3 -c "import json,sys;print(json.load(open(sys.argv[1]))['database_url'])" "$DB_JSON")"
export PANEL_TEST_REDIS_URL="$(python3 -c "import json,sys;print(json.load(open(sys.argv[1]))['redis_url'])" "$DB_JSON")"

# Preconditions: the tunnel and the two test containers must be up, otherwise the
# suite degrades into skips or fails to connect.
need_tcp() { timeout 3 bash -c "cat < /dev/null > /dev/tcp/127.0.0.1/$1" 2>/dev/null; }
for port in 15432 16379; do
  if ! need_tcp "$port"; then
    echo "127.0.0.1:$port is not reachable; start the SSH tunnel and the proxypanel-test containers first" >&2
    exit 1
  fi
done
if [ ! -x "$XRAY_BIN" ]; then
  echo "$XRAY_BIN is missing; copy the official linux-amd64 build there" >&2
  exit 1
fi

cd "$REPO"
case "$MODE" in
  serial)
    echo "== go test ./... -p 1 (authoritative) =="
    exec go test ./... -count=1 -p 1
    ;;
  parallel)
    echo "== go test ./... (contended, smoke only) =="
    exec go test ./... -count=1
    ;;
  *)
    echo "usage: $0 [serial|parallel]" >&2
    exit 2
    ;;
esac
