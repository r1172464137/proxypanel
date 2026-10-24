#!/usr/bin/env bash
# web-demo stage F (deployment / install capability) launcher — revision 2.
#
# Owner: webdemo_platform_* / web-demo F. It never touches the 203 preview
# (8445), the legacy 8443 stack, 1Panel, PostgreSQL/Redis or any host systemd
# unit. Every mode is opt-in so a reviewer can see exactly what ran.
#
#   unit     shipped deploy-agent.sh render/validate (privileged side effects
#            tripwired to exit 97), exact-message denylist oracle, non-root
#            refusal, systemd-analyze verify                          (always safe)
#   systemd  REAL built Agent binary under a REAL transient *user* unit with the
#            REAL official core path, local fake panel, /proc + kill/restart
#            proof                                                       (no root)
#   dual     NOT re-run here: the already-recorded dual-hosting run is referenced
#            read-only (it is independent 4-artifact evidence and re-running it
#            would spend the shared remote lock for no new information).
#
# Every mode writes, in the SAME directory:
#   <mode>.go-test.json     raw `go test -json` stream (not a summary)
#   <mode>.rawexit          the exit status of that exact invocation
#   <mode>.stdout.log       combined human log
#   systemd-agent-restart.json  (systemd mode) same-run unit/pid/proc receipt
# plus run-manifest.json with git HEAD, script SHA-256, warm core SHA-256 and the
# Agent binary SHA produced by the same run.
#
# Evidence goes to a private dir under .ai/evidence/web-demo/F/ (0700 dirs, 0600
# files). Nothing is pushed.
set -euo pipefail
umask 077

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

PPF_MAIN_ROOT=${PPF_MAIN_ROOT:-/home/compile/Work/proxypanel}
PPF_EVIDENCE_ROOT=${PPF_EVIDENCE_ROOT:-$PPF_MAIN_ROOT/.ai/evidence/web-demo/F}
PPF_EVIDENCE=${PPF_EVIDENCE:-$PPF_EVIDENCE_ROOT/run-$(date -u +%Y%m%dT%H%M%SZ)-$(openssl rand -hex 4)}
PPF_MODES=${*:-unit systemd}
PPF_XRAY=${PPF_XRAY:-/home/compile/Work/bin/xray}
PPF_DUAL_REF=${PPF_DUAL_REF:-$PPF_MAIN_ROOT/.ai/evidence/web-demo/F/dual-hosting-reused.json}

mkdir -p "$PPF_EVIDENCE"
chmod 700 "$PPF_EVIDENCE"

if [ -n "${PPF_GO:-}" ]; then
  export PATH="$(dirname "$PPF_GO"):$PATH"
elif [ -x /home/compile/sdk/go1.27.3/go/bin/go ]; then
  export PATH="/home/compile/sdk/go1.27.3/go/bin:$PATH"
fi

sha_of() { [ -f "$1" ] && sha256sum "$1" | awk '{print $1}' || echo "MISSING"; }

# One run id so a reviewer can tell "same run" from "collage of debug logs".
RUN_ID=$(basename "$PPF_EVIDENCE")
cat > "$PPF_EVIDENCE/run-manifest.json" <<EOF
{
  "run_id": "$RUN_ID",
  "source_sha": "$(git rev-parse HEAD)",
  "source_dirty": $(git diff --quiet && echo false || echo true),
  "script_sha256": "$(sha_of scripts/webdemo-platform-verify.sh)",
  "test_file_sha256": "$(sha_of tests/e2e/webdemo_platform_hostunit_test.go)",
  "core_binary": "$PPF_XRAY",
  "core_binary_sha256": "$(sha_of "$PPF_XRAY")",
  "go_version": "$(go version | awk '{print $3}')",
  "modes": "$PPF_MODES",
  "started_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF

# Runs one mode with its own raw JSON stream + raw exit status. The JSON is the
# authoritative artifact; the log is only a convenience rendering of it.
run_mode() {
  local mode=$1 name=$2
  shift 2
  local rc=0
  go test ./tests/e2e -run "$name" -count=1 -v -json "$@" \
    >"$PPF_EVIDENCE/$mode.go-test.json" 2>"$PPF_EVIDENCE/$mode.stderr.log" || rc=$?
  printf '%s\n' "$rc" >"$PPF_EVIDENCE/$mode.rawexit"
  # A successful go command with zero cases or skipped cases is not a pass.
  if ! python3 - "$PPF_EVIDENCE/$mode.go-test.json" "$mode" <<'PY'
import json, sys
expected = {
    'unit': {'TestWebDemoPlatformUnitContract', 'TestWebDemoPlatformOwnedCoreDetection'},
    'systemd': {'TestWebDemoPlatformRealAgentRestart'},
}[sys.argv[2]]
with open(sys.argv[1]) as src:
    events = [json.loads(line) for line in src if line.strip()]
passed = {e.get('Test') for e in events if e.get('Action') == 'pass'}
assert expected <= passed, f'expected cases not passed: {expected - passed}'
assert not any(e.get('Action') in ('fail', 'skip') for e in events), 'failure or skip'
assert any(e.get('Action') == 'pass' and 'Test' not in e for e in events), 'package not passed'
PY
  then
    rc=1
  fi
  # Human-readable rendering is derived from the recorded JSON, never re-run.
  python3 - "$PPF_EVIDENCE/$mode.go-test.json" "$PPF_EVIDENCE/$mode.stdout.log" <<'PY' || true
import json, sys
src, dst = sys.argv[1], sys.argv[2]
with open(src, "r", errors="replace") as f, open(dst, "w") as out:
    for line in f:
        try:
            ev = json.loads(line)
        except Exception:
            continue
        if ev.get("Action") == "output":
            out.write(ev.get("Output", ""))
PY
  # Record the Agent binary the same run actually executed, when it published one.
  if [ "$mode" = systemd ] && [ -f "$PPF_EVIDENCE/systemd-agent-restart.json" ]; then
    python3 - "$PPF_EVIDENCE" <<'PY' || true
import json, sys, pathlib
d = pathlib.Path(sys.argv[1])
ev = json.loads((d / "systemd-agent-restart.json").read_text())
(d / "agent-binary.sha256").write_text(ev.get("agent_binary_sha256", "MISSING") + "\n")
(d / "core-binary.sha256").write_text(ev.get("core_binary_sha256", "MISSING") + "\n")
PY
  fi
  echo "==> $mode exit=$rc"
  return "$rc"
}

run_unit() {
  run_mode unit '^(TestWebDemoPlatformUnitContract|TestWebDemoPlatformOwnedCoreDetection)$'
}

run_systemd() {
  PP_WEB_DEMO_SYSTEMD=1 PP_WEB_DEMO_EVIDENCE_DIR="$PPF_EVIDENCE" \
    run_mode systemd TestWebDemoPlatformRealAgentRestart
}

# Reference-only: the dual-hosting run is separate, already recorded evidence.
# Re-running it would consume the shared remote runtime lock without adding new
# information, so this mode never executes it and says so explicitly.
run_dual_reference() {
  local rc=0
  if [ -f "$PPF_DUAL_REF" ]; then
    cp "$PPF_DUAL_REF" "$PPF_EVIDENCE/dual-hosting-reused.json"
    printf '%s\n' "not re-run: reused $(sha_of "$PPF_DUAL_REF") from $PPF_DUAL_REF" \
      >"$PPF_EVIDENCE/dual.rawexit.note"
  else
    printf '%s\n' "not re-run and reference missing: $PPF_DUAL_REF" \
      >"$PPF_EVIDENCE/dual.rawexit.note"
    rc=3
  fi
  printf '%s\n' "$rc" >"$PPF_EVIDENCE/dual.rawexit"
  return "$rc"
}

rc=0
for mode in $PPF_MODES; do
  echo "==> webdemo_platform: $mode"
  case "$mode" in
    unit)    run_unit    || rc=1 ;;
    systemd) run_systemd || rc=1 ;;
    dual)    run_dual_reference || rc=1 ;;
    *) echo "unknown mode: $mode" >&2; rc=2 ;;
  esac
done

# Integrity of the same run: the Agent binary hash inside the systemd receipt
# must match the hashes recorded in the manifest/sidecar files.
if [ -f "$PPF_EVIDENCE/agent-binary.sha256" ]; then
  agent_sha=$(tr -d '[:space:]' <"$PPF_EVIDENCE/agent-binary.sha256")
  if ! python3 - "$PPF_EVIDENCE" "$agent_sha" <<'PY'
import json, sys, pathlib
d = pathlib.Path(sys.argv[1]); sha = sys.argv[2]
ev = json.loads((d / "systemd-agent-restart.json").read_text())
first = ev.get("first_process", {}).get("exe_sha256")
second = ev.get("restart_after_kill", {}).get("second_process", {}).get("exe_sha256")
manifest = json.loads((d / 'run-manifest.json').read_text())
if ev.get('core_binary_sha256') != manifest.get('core_binary_sha256') or ev.get('core_binary') != manifest.get('core_binary'):
    raise SystemExit('CORE_INPUT_MISMATCH')
if not (first == second == sha == ev.get('agent_binary_sha256')):
    print(f"AGENT_SHA_MISMATCH sha={sha} first={first} second={second}")
    sys.exit(4)
print(f"AGENT_SHA_CONSISTENT {sha}")
PY
  then
    rc=1
  fi
fi

if [[ " $PPF_MODES " == *" systemd "* ]] && [ ! -f "$PPF_EVIDENCE/agent-binary.sha256" ]; then
  echo 'SYSTEMD_RECEIPT_MISSING' >&2
  rc=1
fi

find "$PPF_EVIDENCE" -type d -exec chmod 700 {} + 2>/dev/null || true
find "$PPF_EVIDENCE" -type f -exec chmod 600 {} + 2>/dev/null || true
echo "Evidence: $PPF_EVIDENCE (exit $rc)"
exit "$rc"
