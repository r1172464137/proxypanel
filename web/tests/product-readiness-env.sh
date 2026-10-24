#!/bin/bash
# Owned loopback development acceptance only. No real credentials are loaded.
set -euo pipefail
umask 077
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
MAIN=${PR_MAIN_ROOT:-/home/compile/Work/proxypanel}
cd "$ROOT"
source "${PR_GO_ENV:-/home/compile/Work/env.sh}"
source "${PR_TEST_ENV:-/home/compile/Work/.devdb-product-readiness/test.env}"
export PANEL_TEST_DATABASE_URL
PRIVATE=$(mktemp -d /tmp/pr-browser.XXXXXXXX)
export PR_PRIVATE=$PRIVATE PR_MAIN_ROOT=$MAIN
linked=false
cleanup() {
  if [[ -f "$PRIVATE/owner.json" && -x "$PRIVATE/db" ]]; then
    "$PRIVATE/db" drop "$PRIVATE/owner.json" >"$PRIVATE/drop.log" 2>&1 || { echo 'ERROR: schema cleanup failed; private directory retained'; return; }
    if [[ -f "$PRIVATE/evidence-path" ]]; then
      evidence=$(<"$PRIVATE/evidence-path")
      cp "$PRIVATE/drop.log" "$evidence/cleanup.json"
    fi
  fi
  $linked && rm "$ROOT/web/node_modules"
  rm -rf -- "$PRIVATE"
}
trap cleanup EXIT
if [[ ! -e "$ROOT/web/node_modules" ]]; then ln -s "$MAIN/web/node_modules" "$ROOT/web/node_modules"; linked=true; fi
go build -o "$PRIVATE/db" "$ROOT/web/tests/product-readiness-db.go"
go build -o "$PRIVATE/panel" "$ROOT/cmd/panel"
"$PRIVATE/db" init "$PRIVATE/owner.json" > "$PRIVATE/schema-proof.json"
# Exactly one build, shared by every subsequent browser run in this invocation.
(cd "$ROOT/web" && npm run build -- --outDir "$PRIVATE/dist") > "$PRIVATE/build.log" 2>&1
# Optional probe entry: PR_PROBE=frontend-phase selects the additional technical
# UI probe. Default (unset/anything else) keeps product-readiness-e2e.mjs only.
PROBE_SCRIPT=product-readiness-e2e.mjs
if [[ ${PR_PROBE:-} == frontend-phase ]]; then PROBE_SCRIPT=frontend-phase-probe.mjs; fi
status=0
node "$ROOT/web/tests/$PROBE_SCRIPT" || status=$?
if [[ ${PR_ALLOW_RETEST:-0} == 1 ]]; then
  echo "RETEST_PRIVATE=$PRIVATE (touch retry to rerun without rebuilding; touch done to clean)"
  for ((i=0;i<900;i++)); do
    [[ -f "$PRIVATE/done" ]] && break
    if [[ -f "$PRIVATE/retry" ]]; then
      rm "$PRIVATE/retry"
      "$PRIVATE/db" drop "$PRIVATE/owner.json" > "$PRIVATE/drop.log"
      if [[ -f "$PRIVATE/evidence-path" ]]; then
        evidence=$(<"$PRIVATE/evidence-path")
        cp "$PRIVATE/drop.log" "$evidence/cleanup.json"
      fi
      "$PRIVATE/db" init "$PRIVATE/owner.json" > "$PRIVATE/schema-proof.json"
      status=0
      node "$ROOT/web/tests/$PROBE_SCRIPT" || status=$?
      echo 'RETEST_COMPLETE'
    fi
    sleep 1
  done
fi
exit "$status"
