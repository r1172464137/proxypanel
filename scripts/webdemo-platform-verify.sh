#!/usr/bin/env bash
# web-demo stage F (deployment / install capability) launcher.
#
# Owner: webdemo_platform_* / web-demo F. It never touches the 203 preview
# (8445), the legacy 8443 stack, 1Panel, PostgreSQL/Redis or any host systemd
# unit. Every mode is opt-in so a reviewer can see exactly what ran.
#
#   unit     shipped deploy-agent.sh render/validate + systemd-analyze verify    (always safe)
#   systemd  REAL agent binary in a transient *user* unit with unit hardening   (no root needed)
#   dual     reuse scripts/verify-dual-hosting-remote.sh for the two-hosting TLS
#            upgrade proof on 203, isolated containers/labels/images only,
#            serialised by the web-demo remote runtime lock
#
# Evidence goes to a private dir under .ai/evidence/deployment/dual-hosting/
# (0700 dirs, 0600 files). Nothing is pushed.
set -euo pipefail
umask 077

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

PPF_MAIN_ROOT=${PPF_MAIN_ROOT:-/home/compile/Work/proxypanel}
PPF_REMOTE_LOCK=${PPF_REMOTE_LOCK:-/home/compile/Work/.locks/proxypanel-webdemo-remote-runtime.lock}
PPF_GO=${PPF_GO:-/home/compile/sdk/go1.27.3/go/bin/go}
PPF_BASE_REF=${PPF_BASE_REF:-7feee36b82416e2e5833d458340b1e4e7724f76c}
PPF_EVIDENCE_ROOT=${PPF_EVIDENCE_ROOT:-$PPF_MAIN_ROOT/.ai/evidence/deployment/dual-hosting}
PPF_EVIDENCE=${PPF_EVIDENCE:-$PPF_EVIDENCE_ROOT/run-webdemoF-$(date -u +%Y%m%dT%H%M%SZ)-$(openssl rand -hex 4)}
PPF_MODES=${*:-unit systemd}

mkdir -p "$PPF_EVIDENCE"
chmod 700 "$PPF_EVIDENCE"
export PATH="$(dirname "$PPF_GO"):$PATH"

run_unit() {
  go test ./tests/e2e -run 'TestWebDemoPlatformUnitContract' -count=1 -v
}

run_systemd() {
  PP_WEB_DEMO_SYSTEMD=1 PP_WEB_DEMO_EVIDENCE_DIR="$PPF_EVIDENCE" \
    go test ./tests/e2e -run 'TestWebDemoPlatformRealSystemdTransient' -count=1 -v
}

run_dual() {
  # Serialise with any other web-demo remote scenario. The inner script owns
  # its own before/cleanup snapshots and exact-owner label cleanup.
  ( flock -w 300 "$PPF_REMOTE_LOCK" \
      env DUAL_BASE_REF="$PPF_BASE_REF" DUAL_EVIDENCE="$PPF_EVIDENCE/dual" \
      bash scripts/verify-dual-hosting-remote.sh )
}

rc=0
for mode in $PPF_MODES; do
  echo "==> webdemo_platform: $mode"
  case "$mode" in
    unit)    run_unit    || rc=1 ;;
    systemd) run_systemd || rc=1 ;;
    dual)    mkdir -p "$PPF_EVIDENCE/dual"; run_dual || rc=1 ;;
    *) echo "unknown mode: $mode" >&2; rc=2 ;;
  esac
done

find "$PPF_EVIDENCE" -type d -exec chmod 700 {} + 2>/dev/null || true
find "$PPF_EVIDENCE" -type f -exec chmod 600 {} + 2>/dev/null || true
echo "Evidence: $PPF_EVIDENCE (exit $rc)"
exit "$rc"
