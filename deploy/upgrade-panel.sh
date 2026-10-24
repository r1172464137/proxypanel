#!/usr/bin/env bash
# Shared by manage-linux.sh and isolated CLI/PG tests. Never starts a service.
set -euo pipefail

panel_preflight() {
 local candidate=$1 evidence=${2:-${PANEL_UPGRADE_EVIDENCE_FILE:-}} result rc=0
 [[ -x $candidate/panel && -d $candidate/migrations ]] || { echo 'CANDIDATE_INCOMPLETE' >&2; return 2; }
 result=$(mktemp)
 PANEL_MIGRATIONS_DIR="$candidate/migrations" "$candidate/panel" check --preflight --json --evidence "$evidence" >"$result" || rc=$?
 cat "$result"
 if (( rc != 0 )); then rm -f "$result"; return "$rc"; fi
 # Exit zero alone is not a readiness assertion. Unknown JSON/status fails closed.
 if ! python3 -c 'import json,sys; sys.exit(0 if json.load(open(sys.argv[1])).get("status")=="ready" else 3)' "$result"; then
  rm -f "$result"; echo 'PREFLIGHT_NOT_READY' >&2; return 3
 fi
 rm -f "$result"
}

panel_require_stopped() {
 local state
 state=$(systemctl show --property=ActiveState --value proxypanel-panel.service) || return 2
 [[ $state == inactive || $state == failed ]] || { echo 'PANEL_MUST_BE_STOPPED' >&2; return 2; }
}

panel_activate() {
 local base=$1 name=$2 evidence=${3:-${PANEL_UPGRADE_EVIDENCE_FILE:-}} target
 [[ $name =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ && -f $base/.managed-by-proxypanel ]] || return 2
 target=$(realpath -e "$base/releases/$name") || return 2
 [[ $target == "$base/releases/$name" && ! -L $base/releases/$name ]] || return 2
 [[ ! -e $base/current || -L $base/current ]] || return 2
 if [[ -L $base/current ]]; then
  [[ $(readlink -f "$base/current") == "$base/releases/"* ]] || return 2
 fi
 panel_require_stopped || return $?
 panel_preflight "$target" "$evidence" || return $?
 panel_require_stopped || return $?
 # No migration/start here: Open will revalidate material and DB under its lock.
 ln -s "$target" "$base/.current-upgrade-$$"
 mv -Tf "$base/.current-upgrade-$$" "$base/current"
 echo 'ACTIVATED_NOT_STARTED: configure the identical evidence path for Open before explicit start.'
}

panel_rollback_check() {
 local old=$1 rows rc=0
 panel_require_stopped || return $?
 [[ -d $old/migrations ]] || return 2
 rows=$(mktemp)
 # A read-only connection; report the actual committed prefix, never undo it.
 PGOPTIONS="${PGOPTIONS:-} -c default_transaction_read_only=on" psql "$DATABASE_URL" -X -v ON_ERROR_STOP=1 -At -F '|' -c 'SELECT version,checksum FROM schema_migrations ORDER BY version' >"$rows" || rc=$?
 if (( rc != 0 )); then rm -f "$rows"; return "$rc"; fi
 cat "$rows"
 python3 - "$old/migrations" "$rows" <<'PY' || rc=$?
import hashlib,pathlib,sys
root=pathlib.Path(sys.argv[1]); rows=pathlib.Path(sys.argv[2]).read_text().splitlines()
for row in rows:
    version, checksum=row.split('|')
    file=root/version
    if file.parent!=root or not file.is_file() or hashlib.sha256(file.read_bytes()).hexdigest()!=checksum:
        print('OLD_BINARY_SCHEMA_COMPATIBILITY_UNPROVEN: '+version, file=sys.stderr)
        sys.exit(2)
print('PREFIX_MATCH_ONLY: manually review old binary compatibility; keep service stopped. No readonly startup capability is assumed.')
PY
 rm -f "$rows"; return "$rc"
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
 [[ ${1:-} == preflight && $# -ge 2 && $# -le 3 ]] || { echo "Usage: $0 preflight <candidate-dir> [evidence.json]" >&2; exit 2; }
 panel_preflight "$2" "${3:-${PANEL_UPGRADE_EVIDENCE_FILE:-}}"
fi
