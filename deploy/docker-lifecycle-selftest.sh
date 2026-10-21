#!/bin/sh
# No-Docker, no-SSH regression for deploy/docker-lifecycle.sh (goal A13).
#
# The lifecycle actions are real Docker operations, so this selftest never talks
# to a Docker host: it puts a stub `docker` first on PATH that records every
# invocation and answers `inspect` from a throwaway state directory. The script
# under test runs unmodified except for its one hardcoded root line, which points
# at that throwaway directory (every path of the script is derived from `root`,
# `stage`, `statevolume`, `network` and `image`).
#
# It reproduces the first real A13 failure (scratch/docker-agent-upgrade-1.log):
# the helper `assert_exclusive` used the loop variable `name`, and /bin/sh has no
# scope, so after `run_lifecycle_agent` called it the caller's `name` held the
# last loop entry (agent1core). `docker run` therefore created
# ppaccept-20261021-agent1core and the caller's next `mount-check agent1v2`
# failed with exit 52 "the lifecycle container is missing".
#
# It also reproduces the second real A13 failure (scratch/docker-core-switch-2.log):
# the staged core was uploaded without its geo assets, so it started and died with
#   failed to load GeoIP: private: open /s/geoip.dat: no such file or directory
# after the serving Agent had already been stopped. The staged set is therefore
# one unit and incomplete sets are refused *before* anything is stopped.
#
# Covered here without a daemon:
#   1. agent-upgrade starts exactly ppaccept-20261021-agent1v2 (staged agent,
#      image core) and never ppaccept-20261021-agent1core,
#   2. core-switch verifies the staged set, then starts exactly
#      ppaccept-20261021-agent1core, stops (does not delete) the agent1v2
#      rollback target and derives the staged xray_path,
#   3. the exclusivity refusal still fires (exit 51) before any container is
#      created when the shipped agent1 still runs - a `docker stop` that did not
#      take effect must never let a second container hold the state volume,
#   4. core-switch without the upgraded container still refuses with exit 60,
#   5. an upload set whose SHA256SUMS still lists only agent and core is refused
#      (exit 50) before the shipped agent1 is stopped,
#   6. a missing geoip.dat is refused (exit 50) before the upgraded agent stops,
#   7. a digest mismatch is refused (exit 73) before the upgraded agent stops.
#
# Usage: sh deploy/docker-lifecycle-selftest.sh [path-to-docker-lifecycle.sh]
# Exit code 0 means every check passed.
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
script=${1:-$here/docker-lifecycle.sh}
[ -f "$script" ] || {
  echo "selftest: the script under test is missing: $script" >&2
  exit 1
}

work=$(mktemp -d "${TMPDIR:-/tmp}/lifecycle-selftest.XXXXXX")
cleanup() {
  if [ -n "${work:-}" ] && [ -d "$work" ]; then rm -rf "$work"; fi
}
trap cleanup 0 1 2 15

root=$work/root
bin=$work/bin
state=$work/state
mkdir -p "$root/secrets" "$root/lifecycle" "$bin" "$state/c"

# Ownership marker and staged artifacts. require_stage demands the whole set
# (agent, core, both geo assets) and a SHA256SUMS that lists all four; the
# digest itself is computed inside the image and is stubbed away here.
printf '20261021\n' > "$root/.proxypanel-acceptance"
printf 'not-a-real-ca\n' > "$root/secrets/ca.pem"
cat > "$root/secrets/agent1.json" <<'JSON'
{
  "panel_url": "https://127.0.0.1:8443",
  "agent_token": "selftest-not-a-real-token",
  "xray_path": "/app/xray",
  "stats_interval_seconds": 5
}
JSON
printf '#!/bin/sh\necho 0.1.1\n' > "$root/lifecycle/agent-new"
printf '#!/bin/sh\necho "Xray 26.3.23 (selftest)"\n' > "$root/lifecycle/xray-core"
printf 'selftest geoip placeholder\n' > "$root/lifecycle/geoip.dat"
printf 'selftest geosite placeholder\n' > "$root/lifecycle/geosite.dat"
zero=0000000000000000000000000000000000000000000000000000000000000000
printf '%s  agent-new\n%s  xray-core\n%s  geoip.dat\n%s  geosite.dat\n' \
  "$zero" "$zero" "$zero" "$zero" > "$root/lifecycle/SHA256SUMS"
# The manifest the first real upload actually had: agent and core only.
printf '%s  agent-new\n%s  xray-core\n' "$zero" "$zero" > "$work/SHA256SUMS-no-geo"

# Stub Docker. It knows only the primitives the lifecycle actions use and keeps
# one file per container: its content is the State.Running value.
cat > "$bin/docker" <<'STUB'
#!/bin/sh
set -eu
state=${STUB_STATE:?STUB_STATE is not set}
cpath() { printf '%s/c/%s' "$state" "$1"; }

cmd=${1:-}
[ -n "$cmd" ] || { echo 'stub docker: no command' >&2; exit 90; }
shift

case $cmd in
container)
  [ "${1:-}" = inspect ] || { echo "stub docker: unsupported container command" >&2; exit 91; }
  shift
  fmt=''
  if [ "${1:-}" = --format ]; then fmt=${2:-}; shift 2; fi
  name=${1:-}
  [ -n "$name" ] || { echo 'stub docker: container inspect needs a name' >&2; exit 92; }
  file=$(cpath "$name")
  [ -f "$file" ] || { echo "Error: No such container: $name" >&2; exit 1; }
  case $fmt in
  '') exit 0 ;;
  *Config.Labels*) printf '%s\n' "$STUB_LABEL" ;;
  *State.Running*) cat "$file" ;;
  *HostConfig.NetworkMode*) printf '%s\n' "$STUB_NETWORK" ;;
  *Mounts*) printf 'volume:%s->/state \n' "$STUB_VOLUME" ;;
  *HostConfig.Binds*) printf 'null\n' ;;
  '{{.Name}}') printf '/%s\n' "$name" ;;
  *) echo "stub docker: unsupported inspect format: $fmt" >&2; exit 93 ;;
  esac
  ;;
volume | network)
  [ "${1:-}" = inspect ] || { echo "stub docker: unsupported $cmd command" >&2; exit 94; }
  shift
  if [ "${1:-}" = --format ]; then printf '%s\n' "$STUB_LABEL"; exit 0; fi
  case $cmd in
  volume) [ "${1:-}" = "$STUB_VOLUME" ] || { echo 'Error: no such volume' >&2; exit 1; } ;;
  network) [ "${1:-}" = "$STUB_NETWORK" ] || { echo 'Error: no such network' >&2; exit 1; } ;;
  esac
  exit 0
  ;;
image)
  [ "${1:-}" = inspect ] || { echo 'stub docker: unsupported image command' >&2; exit 95; }
  shift
  if [ "${1:-}" = --format ]; then printf '%s\n' "$STUB_LABEL"; fi
  exit 0
  ;;
run)
  printf 'run %s\n' "$*" >> "$state/runs.log"
  # The staged-set digest check runs as a one-shot container. It can be forced
  # to fail so the refusal path before `docker stop` is covered here without
  # hashing anything.
  case "$*" in
  *'sha256sum -c SHA256SUMS'*)
    if [ "${STUB_BAD_STAGE:-0}" = 1 ]; then
      echo 'sha256sum: WARNING: 1 computed checksum did NOT match' >&2
      exit 1
    fi
    ;;
  esac
  name=''
  detach=no
  prev=''
  for arg in "$@"; do
    case $prev in --name) name=$arg ;; esac
    case $arg in -d | --detach) detach=yes ;; esac
    prev=$arg
  done
  if [ "$detach" = yes ] && [ -n "$name" ]; then
    printf 'true\n' > "$(cpath "$name")"
  fi
  printf 'stub-container-id\n'
  ;;
stop)
  last=''
  for arg in "$@"; do last=$arg; done
  [ -f "$(cpath "$last")" ] || { echo "Error: No such container: $last" >&2; exit 1; }
  printf 'stop %s\n' "$last" >> "$state/stops.log"
  if [ "${STUB_STOP_NOOP:-0}" = 1 ]; then exit 0; fi
  printf 'false\n' > "$(cpath "$last")"
  ;;
start)
  last=''
  for arg in "$@"; do last=$arg; done
  [ -f "$(cpath "$last")" ] || { echo "Error: No such container: $last" >&2; exit 1; }
  printf 'start %s\n' "$last" >> "$state/starts.log"
  printf 'true\n' > "$(cpath "$last")"
  ;;
rm)
  last=''
  for arg in "$@"; do last=$arg; done
  [ -f "$(cpath "$last")" ] || { echo "Error: No such container: $last" >&2; exit 1; }
  printf 'rm %s\n' "$last" >> "$state/rms.log"
  rm -f "$(cpath "$last")"
  ;;
*)
  echo "stub docker: unsupported command: $cmd" >&2
  exit 96
  ;;
esac
STUB
chmod 0755 "$bin/docker"

export PATH=$bin:$PATH
export STUB_STATE=$state
export STUB_LABEL=20261021
export STUB_VOLUME=ppaccept-20261021-agent1
export STUB_NETWORK=ppaccept-20261021-net

failures=0
ok() { printf 'ok   %s\n' "$1"; }
bad() {
  printf 'FAIL %s\n' "$1"
  failures=$((failures + 1))
}
check() {
  ck_desc=$1
  shift
  if "$@" >/dev/null 2>&1; then ok "$ck_desc"; else bad "$ck_desc"; fi
}
check_grep() {
  if grep -q -F -e "$2" "$3" 2>/dev/null; then ok "$1"; else bad "$1"; fi
}
check_no_grep() {
  if grep -q -F -e "$2" "$3" 2>/dev/null; then bad "$1"; else ok "$1"; fi
}
check_line() {
  if grep -q -x -F -e "$2" "$3" 2>/dev/null; then ok "$1"; else bad "$1"; fi
}
container_state() { cat "$state/c/$1" 2>/dev/null || true; }
is_running() { [ "$(container_state "$1")" = true ]; }
is_stopped() { [ "$(container_state "$1")" = false ]; }
is_absent() { [ ! -f "$state/c/$1" ]; }
seed_running() { printf 'true\n' > "$state/c/$1"; }
reset_state() {
  rm -rf "$state/c"
  mkdir -p "$state/c"
  : > "$state/runs.log"
  : > "$state/stops.log"
  : > "$state/rms.log"
}

# The stub must really be the docker the script under test will use.
case $(command -v docker) in
"$bin/docker") ok 'the stub docker is first on PATH' ;;
*)
  bad "the stub docker is not first on PATH: $(command -v docker)"
  printf '\n1 check(s) FAILED\n'
  exit 1
  ;;
esac

# Only the single hardcoded root line of the real script is redirected; the
# shipped script itself must keep /opt/proxypanel-acceptance-20261021.
grep -qx 'root=/opt/proxypanel-acceptance-20261021' "$script" ||
  { bad "the script under test lost its hardcoded root line: $script"; }
copy=$work/lifecycle.sh
sed -e "s|^root=/opt/proxypanel-acceptance-20261021\$|root=$root|" "$script" > "$copy"
grep -q -F -e "root=$root" "$copy" || bad 'the copy under test does not use the throwaway root'
check 'the copied script keeps its ownership marker check' grep -q -F -e 'Refusing: the project root marker does not prove ownership' "$copy"

echo '== 1. agent-upgrade starts the staged agent as agent1v2 =='
reset_state
seed_running ppaccept-20261021-agent1
rc=0
sh "$copy" agent-upgrade > "$work/upgrade.out" 2>&1 || rc=$?
if [ "$rc" -eq 0 ]; then
  ok 'agent-upgrade exits 0'
else
  bad "agent-upgrade exits 0 (got $rc: $(cat "$work/upgrade.out"))"
fi
check 'agent-upgrade starts exactly one container' [ "$(grep -c -- '--name ppaccept-20261021-' "$state/runs.log" || true)" -eq 1 ]
check_grep 'agent-upgrade starts ppaccept-20261021-agent1v2' '--name ppaccept-20261021-agent1v2' "$state/runs.log"
check_no_grep 'agent-upgrade never starts agent1core' 'agent1core' "$state/runs.log"
check_grep 'the unchanged state volume is mounted at /state' '-v ppaccept-20261021-agent1:/state' "$state/runs.log"
check_grep 'the staged directory is mounted read-only' "-v $root/lifecycle:/s:ro" "$state/runs.log"
check_grep 'the staged agent binary is the entrypoint' '--entrypoint /s/agent-new' "$state/runs.log"
check_grep 'config and test CA are mounted read-only' "-v $root/secrets/agent1.json:/run/fixture/agent1.json:ro -v $root/secrets/ca.pem:/run/fixture/ca.pem:ro" "$state/runs.log"
check_grep 'the owned network and the fixed limits are reused' '--network ppaccept-20261021-net --memory 192m --memory-swap 192m --cpus .5 --pids-limit 128' "$state/runs.log"
check 'agent1v2 runs' is_running ppaccept-20261021-agent1v2
check 'agent1core was never created' is_absent ppaccept-20261021-agent1core
check 'the shipped agent1 was stopped' is_stopped ppaccept-20261021-agent1
check_line 'the shipped agent1 was stopped by name' 'stop ppaccept-20261021-agent1' "$state/stops.log"
rc=0
sh "$copy" mount-check agent1v2 > "$work/mount1.out" 2>&1 || rc=$?
if [ "$rc" -eq 0 ]; then
  ok 'mount-check agent1v2 accepts the container that was started'
else
  bad "mount-check agent1v2 accepts the container that was started (got $rc: $(cat "$work/mount1.out"))"
fi
check_grep 'mount-check reports agent1v2 on the owned volume' 'ppaccept-20261021-agent1v2 ppaccept-20261021-agent1=/state' "$work/mount1.out"
rc=0
sh "$copy" mount-check agent1core > /dev/null 2>&1 || rc=$?
check 'mount-check agent1core refuses with exit 52' [ "$rc" -eq 52 ]

echo '== 2. core-switch starts the staged core as agent1core =='
: > "$state/runs.log"
: > "$state/stops.log"
: > "$state/rms.log"
rc=0
sh "$copy" core-switch > "$work/core.out" 2>&1 || rc=$?
if [ "$rc" -eq 0 ]; then
  ok 'core-switch exits 0'
else
  bad "core-switch exits 0 (got $rc: $(cat "$work/core.out"))"
fi
check 'core-switch verifies the staged set first' [ "$(grep -c -F -e 'sha256sum -c SHA256SUMS' "$state/runs.log" || true)" -eq 1 ]
check_grep 'the verification mounts the staged directory read-only' "-v $root/lifecycle:/s:ro --entrypoint /bin/sh" "$state/runs.log"
check 'core-switch starts exactly one container' [ "$(grep -c -- '--name ppaccept-20261021-' "$state/runs.log" || true)" -eq 1 ]
check_grep 'core-switch starts ppaccept-20261021-agent1core' '--name ppaccept-20261021-agent1core' "$state/runs.log"
check_no_grep 'core-switch never starts a second agent1v2' '--name ppaccept-20261021-agent1v2' "$state/runs.log"
check_grep 'the derived config is mounted at the agent config path' "-v $root/lifecycle/agent1-core.json:/run/fixture/agent1.json:ro" "$state/runs.log"
check_grep 'the same staged agent binary runs' '--entrypoint /s/agent-new' "$state/runs.log"
check_grep 'the same state volume is reused' '-v ppaccept-20261021-agent1:/state' "$state/runs.log"
check 'agent1core runs' is_running ppaccept-20261021-agent1core
check 'agent1v2 is kept as the rollback target' is_stopped ppaccept-20261021-agent1v2
check_line 'agent1v2 was stopped, not deleted' 'stop ppaccept-20261021-agent1v2' "$state/stops.log"
check_no_grep 'agent1v2 was not removed' 'ppaccept-20261021-agent1v2' "$state/rms.log"
check_grep 'the derived agent config points at the staged core' '"xray_path": "/s/xray-core"' "$root/lifecycle/agent1-core.json"
rc=0
sh "$copy" mount-check agent1core > "$work/mount2.out" 2>&1 || rc=$?
if [ "$rc" -eq 0 ]; then
  ok 'mount-check agent1core accepts the container that was started'
else
  bad "mount-check agent1core accepts the container that was started (got $rc: $(cat "$work/mount2.out"))"
fi

echo '== 3. the exclusivity refusal still fires before anything starts =='
reset_state
seed_running ppaccept-20261021-agent1
rc=0
STUB_STOP_NOOP=1 sh "$copy" agent-upgrade > "$work/refuse.out" 2>&1 || rc=$?
check 'agent-upgrade refuses with exit 51 while agent1 still runs' [ "$rc" -eq 51 ]
check_grep 'the refusal names the colliding container and the target' \
  'Refusing: ppaccept-20261021-agent1 still runs and would share the state volume/ports with ppaccept-20261021-agent1v2' "$work/refuse.out"
check 'no container was created' [ ! -s "$state/runs.log" ]
check 'agent1v2 was not created' is_absent ppaccept-20261021-agent1v2

echo '== 4. core-switch without the upgraded container still refuses =='
reset_state
seed_running ppaccept-20261021-agent1
rc=0
sh "$copy" core-switch > "$work/core-refuse.out" 2>&1 || rc=$?
check 'core-switch refuses with exit 60' [ "$rc" -eq 60 ]
check_grep 'the refusal explains that the new agent must stay in place' \
  'core-switch needs the upgraded agent container first' "$work/core-refuse.out"
check 'nothing was started' [ ! -s "$state/runs.log" ]

echo '== 5. a manifest without the geo assets refuses before agent1 stops =='
reset_state
seed_running ppaccept-20261021-agent1
cp "$root/lifecycle/SHA256SUMS" "$work/SHA256SUMS-full"
cp "$work/SHA256SUMS-no-geo" "$root/lifecycle/SHA256SUMS"
rc=0
sh "$copy" agent-upgrade > "$work/nogeo.out" 2>&1 || rc=$?
check 'agent-upgrade refuses an incomplete manifest with exit 50' [ "$rc" -eq 50 ]
check_grep 'the refusal names the missing geoip.dat entry' 'does not list geoip.dat' "$work/nogeo.out"
check 'the shipped agent1 is still running' is_running ppaccept-20261021-agent1
check 'nothing was stopped' [ ! -s "$state/stops.log" ]
check 'no container was created' [ ! -s "$state/runs.log" ]
cp "$work/SHA256SUMS-full" "$root/lifecycle/SHA256SUMS"

echo '== 6. a missing geo asset refuses before the upgraded agent stops =='
reset_state
printf 'false\n' > "$state/c/ppaccept-20261021-agent1"
seed_running ppaccept-20261021-agent1v2
mv "$root/lifecycle/geoip.dat" "$work/geoip.dat.off"
rc=0
sh "$copy" core-switch > "$work/nogeo-file.out" 2>&1 || rc=$?
check 'core-switch refuses a missing geoip.dat with exit 50' [ "$rc" -eq 50 ]
check_grep 'the refusal explains the staged core needs its own assets' \
  'the staged core resolves geoip.dat/geosite.dat next to its own binary' "$work/nogeo-file.out"
check 'agent1v2 is still serving' is_running ppaccept-20261021-agent1v2
check 'nothing was stopped' [ ! -s "$state/stops.log" ]
check 'no container was created' [ ! -s "$state/runs.log" ]
mv "$work/geoip.dat.off" "$root/lifecycle/geoip.dat"

echo '== 7. a digest mismatch refuses before the upgraded agent stops =='
reset_state
printf 'false\n' > "$state/c/ppaccept-20261021-agent1"
seed_running ppaccept-20261021-agent1v2
rc=0
STUB_BAD_STAGE=1 sh "$copy" core-switch > "$work/badstage.out" 2>&1 || rc=$?
check 'core-switch refuses a digest mismatch with exit 73' [ "$rc" -eq 73 ]
check_grep 'the refusal says the staged artifacts do not match' 'the staged artifacts do not match SHA256SUMS' "$work/badstage.out"
check 'agent1v2 is still serving' is_running ppaccept-20261021-agent1v2
check 'nothing was stopped' [ ! -s "$state/stops.log" ]
check 'no container was created' [ "$(grep -c -- '--name ppaccept-20261021-' "$state/runs.log" || true)" -eq 0 ]

if [ "$failures" -ne 0 ]; then
  printf '\n%d check(s) FAILED\n' "$failures"
  exit 1
fi
printf '\nall checks passed\n'
