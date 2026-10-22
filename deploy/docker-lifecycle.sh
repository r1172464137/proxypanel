#!/bin/sh
# Docker lifecycle version-switch actions for the owned acceptance stack
# (goal criterion 8 / A13). Every name, path and limit is hardcoded on purpose:
# no argument other than the fixed action strings below is accepted.
#
# Ownership is re-verified before any container, volume or network is touched:
#   - the project root marker $root/.proxypanel-acceptance,
#   - the label io.proxypanel.acceptance=20261021 on every container, on the
#     state volumes, on the owned network and on the acceptance image,
#   - the owned state volume must really be mounted at /state of the target
#     container, and no other lifecycle container may still hold it.
#
# Version switching model (all inside the same SQLite state volume):
#   shipped agent1    : image agent 0.1.0 + image core 26.3.27   (never deleted)
#   agent1v2          : staged agent 0.1.1 + image core 26.3.27
#   agent1core        : staged agent 0.1.1 + staged core 26.3.23
# so the core really switches 26.3.27 -> 26.3.23 -> 26.3.27 under the same new
# agent, and `cleanup` returns to the shipped 0.1.0 image. At most one of the
# three containers runs at any time; the others are stopped or removed first.
# The staged set is one unit: agent-new + xray-core + geoip.dat + geosite.dat +
# SHA256SUMS. Xray resolves its geo assets next to its own executable, so the
# staged core needs its own official geoip.dat/geosite.dat in /s. A switch that
# only uploaded the binary starts a core that dies with
#   failed to load GeoIP: private: open /s/geoip.dat: no such file or directory
# and takes both nodes down. require_stage therefore refuses an incomplete set
# or a SHA256SUMS that does not cover all four files, and core-switch re-verifies
# the digests before it stops the Agent that is still serving.
#
# Actions:
#   stage          upload verification step: set file modes and check the
#                  four-line SHA256SUMS inside a bounded container that only
#                  mounts the staged directory (agent-new, xray-core, geoip.dat,
#                  geosite.dat). Nothing is started or stopped.
#   baseline       SHA256 + version of the image agent/core and of the staged
#                  artifacts, measured by running them (no host artifact is used
#                  as a stand-in for the image binary)
#   state-summary  sizes + SHA256 of agent.db / agent.db-wal only (no content)
#   state-transfer framed base64 of agent.db / agent.db-wal: transfer data for
#                  the offline per-batch check. The caller must not log it.
#   mount-check N  ownership label, state volume mount, network label and host
#                  binds of N in agent1|agent1v2|agent1core
#   listeners N P  how many sockets inside the owned container N are LISTENING on
#                  the fixed port P (P in 20101..20105). It reads /proc/net/tcp and
#                  /proc/net/tcp6 inside that container's network namespace and
#                  matches the local address column plus state 0A only, so a local
#                  SSH forward on the host can never be mistaken for the node
#                  listener. Output: "listeners container=N port=P count=C".
#                  A probe that cannot read at least one kernel table, or whose
#                  awk exits non-zero, fails with exit 71 instead of reporting a
#                  silent count=0 that would read as "no listener".
#   agent-upgrade  stop the shipped agent1, start agent1v2 (staged agent 0.1.1,
#                  image core 26.3.27) on the same state volume/config/limits
#   core-switch    verify the staged set, then stop agent1v2 and start
#                  agent1core (staged agent 0.1.1, staged core 26.3.23) on the
#                  same state volume
#   core-rollback  remove agent1core, start agent1v2 again (staged agent 0.1.1,
#                  image core 26.3.27)
#   agent-rollback remove agent1v2/agent1core, start the shipped agent1 (0.1.0)
#   cleanup        same as agent-rollback, idempotent
#   status         ownership label, state and limits of the lifecycle containers
#   logs NAME      docker logs of agent1v2 or agent1core
#
# Helper functions never assign the fixed action variable names (`name`, `target`,
# `port`): each helper uses its own prefix (rs_/dl_/ex_/mc_/lla_) because /bin/sh
# has no local scope, and a nested helper that reused `name` would leave the
# caller creating or starting a container other than the one it asked for.
set -eu

root=/opt/proxypanel-acceptance-20261021
label=io.proxypanel.acceptance
value=20261021
prefix=ppaccept-20261021
image=proxypanel-acceptance:20261021
network=$prefix-net
statevolume=$prefix-agent1
stage=$root/lifecycle
upgraded=$prefix-agent1v2
coretest=$prefix-agent1core

[ "$(cat "$root/.proxypanel-acceptance" 2>/dev/null || true)" = "$value" ] || {
  echo 'Refusing: the project root marker does not prove ownership' >&2
  exit 40
}
cd "$root"

owned() {
  docker container inspect "$prefix-$1" >/dev/null 2>&1 || {
    echo "Refusing: $1 is not part of the owned acceptance stack" >&2
    exit 41
  }
  [ "$(docker container inspect --format "{{index .Config.Labels \"$label\"}}" "$prefix-$1")" = "$value" ] || {
    echo "Refusing: $1 does not carry $label=$value" >&2
    exit 42
  }
}

owned_lifecycle() {
  docker container inspect "$1" >/dev/null 2>&1 || {
    echo "Refusing: the lifecycle container is missing: $1" >&2
    exit 43
  }
  [ "$(docker container inspect --format "{{index .Config.Labels \"$label\"}}" "$1")" = "$value" ] || {
    echo "Refusing: $1 does not carry $label=$value" >&2
    exit 44
  }
}

owned_volume() {
  docker volume inspect "$statevolume" >/dev/null 2>&1 || {
    echo "Refusing: the owned state volume $statevolume is missing" >&2
    exit 45
  }
  [ "$(docker volume inspect --format "{{index .Labels \"$label\"}}" "$statevolume")" = "$value" ] || {
    echo "Refusing: the state volume $statevolume does not carry $label=$value" >&2
    exit 46
  }
}

owned_network() {
  docker network inspect "$network" >/dev/null 2>&1 || {
    echo 'Refusing: the owned acceptance network is missing' >&2
    exit 47
  }
  [ "$(docker network inspect --format "{{index .Labels \"$label\"}}" "$network")" = "$value" ] || {
    echo 'Refusing: the acceptance network is not owned by this project' >&2
    exit 48
  }
}

owned_image() {
  [ "$(docker image inspect --format "{{index .Config.Labels \"$label\"}}" "$image" 2>/dev/null || true)" = "$value" ] || {
    echo 'Refusing: the acceptance image is not owned by this project' >&2
    exit 49
  }
}

require_stage() {
  # The staged set is one unit. The manifest is checked on the host first:
  # hashing ~30MB of geo assets is only meaningful when SHA256SUMS really
  # covers the staged core's own geoip.dat/geosite.dat, and an old three-line
  # manifest (agent and core only) must not pass as "complete".
  [ -s "$stage/SHA256SUMS" ] || {
    echo "Refusing: the staged manifest $stage/SHA256SUMS is missing (upload it with scp)" >&2
    exit 50
  }
  for rs_file in agent-new xray-core geoip.dat geosite.dat; do
    [ -s "$stage/$rs_file" ] || {
      echo "Refusing: the staged artifact $stage/$rs_file is missing (the staged core resolves geoip.dat/geosite.dat next to its own binary; upload the complete set with scp)" >&2
      exit 50
    }
    grep -q "  $rs_file\$" "$stage/SHA256SUMS" || {
      echo "Refusing: $stage/SHA256SUMS does not list $rs_file (regenerate the upload set; the release packaging script lives outside this repo now)" >&2
      exit 50
    }
  done
}

# Digest of the whole staged set inside a bounded, network-less container that
# only mounts $stage read-only. It runs *before* the serving Agent is stopped:
# a core whose assets are wrong must never replace a working one.
verify_stage() {
  one_shot -v "$stage:/s:ro" --entrypoint /bin/sh "$image" -c \
    'cd /s || exit 1; sha256sum -c SHA256SUMS' || {
    echo 'Refusing: the staged artifacts do not match SHA256SUMS; the switch would start a core whose assets are wrong' >&2
    exit 73
  }
}

one_shot() {
  # Network-less, capability-dropped, CPU/memory/PID limited and labelled. It
  # only ever sees what it mounts.
  docker run --rm --network none --label "$label=$value" --memory 128m --memory-swap 128m \
    --cpus .5 --pids-limit 64 --security-opt no-new-privileges --cap-drop ALL "$@"
}

drop_lifecycle() {
  dl_name=$1
  if docker container inspect "$dl_name" >/dev/null 2>&1; then
    owned_lifecycle "$dl_name"
    docker rm -f "$dl_name" >/dev/null
  fi
}

# The state volume carries the agent identity, the outbox and the ledger cache:
# exactly one container may hold it, and it must always be at /state.
assert_exclusive() {
  ex_target=$1
  # The loop variable is prefixed on purpose: /bin/sh has no scoping, so a plain
  # `for name in ...` would leave the caller's `name` holding the last entry and
  # the caller would then create/start a container other than the requested one
  # (that is how `agent-upgrade` once started agent1core instead of agent1v2).
  for ex_name in "$prefix-agent1" "$upgraded" "$coretest"; do
    [ "$ex_name" = "$ex_target" ] && continue
    if docker container inspect "$ex_name" >/dev/null 2>&1; then
      [ "$(docker container inspect --format '{{.State.Running}}' "$ex_name")" = "false" ] || {
        echo "Refusing: $ex_name still runs and would share the state volume/ports with $ex_target" >&2
        exit 51
      }
    fi
  done
}

# Provenance of one container: label, state volume mounted at /state, the owned
# bridge network and the host binds (no socket, nothing outside the owned root).
mount_check() {
  mc_name=$1
  docker container inspect "$mc_name" >/dev/null 2>&1 || {
    echo "Refusing: $mc_name is missing" >&2
    exit 52
  }
  owned_lifecycle "$mc_name"
  [ "$(docker container inspect --format '{{.HostConfig.NetworkMode}}' "$mc_name")" = "$network" ] || {
    echo "Refusing: $mc_name is not attached to the owned network $network" >&2
    exit 53
  }
  docker container inspect --format '{{range .Mounts}}{{.Type}}:{{.Name}}->{{.Destination}} {{end}}' "$mc_name" |
    tr ' ' '\n' | grep -qx "volume:$statevolume->/state" || {
      echo "Refusing: $mc_name does not mount the owned state volume $statevolume at /state" >&2
      exit 54
    }
  mc_binds=$(docker container inspect --format '{{json .HostConfig.Binds}}' "$mc_name")
  case "$mc_binds" in
  *docker.sock*)
    echo "Refusing: $mc_name mounts the Docker socket" >&2
    exit 55
    ;;
  esac
  # Only the owned project root may be bind mounted; an empty bind list prints
  # as null.
  case "$mc_binds" in
  null) ;;
  *"$root/"*) ;;
  *)
    echo "Refusing: $mc_name has an unexpected host bind: $mc_binds" >&2
    exit 56
    ;;
  esac
  echo "$mc_name $statevolume=/state network=$network binds=$mc_binds"
}

# Reuse every boundary of the real agent1 container and mount the staged
# artifacts read-only. $1 name, $2 agent binary, $3 config path on the host.
# The parameters live in prefixed variables and the container is read back by
# name after it starts: /bin/sh has no scoping, so a nested helper must never be
# able to make this function start a container other than the requested one.
run_lifecycle_agent() {
  lla_name=$1
  lla_agent=$2
  lla_config=$3
  assert_exclusive "$lla_name"
  docker run -d --name "$lla_name" --label "$label=$value" --network "$network" \
    --memory 192m --memory-swap 192m --cpus .5 --pids-limit 128 --restart no \
    --security-opt no-new-privileges --cap-drop ALL \
    --log-opt max-size=5m --log-opt max-file=2 \
    --tmpfs /tmp:rw,noexec,nosuid,size=16m \
    -v "$statevolume:/state" \
    -v "$stage:/s:ro" \
    -v "$lla_config:/run/fixture/agent1.json:ro" \
    -v "$root/secrets/ca.pem:/run/fixture/ca.pem:ro" \
    -e SSL_CERT_FILE=/run/fixture/ca.pem \
    -p 127.0.0.1:20101:20101 -p 127.0.0.1:20102:20102 -p 127.0.0.1:20103:20103 \
    -p 127.0.0.1:20104:20104 -p 127.0.0.1:20105:20105 \
    --entrypoint "$lla_agent" "$image" -config /run/fixture/agent1.json >/dev/null
  [ "$(docker container inspect --format '{{.Name}}' "$lla_name")" = "/$lla_name" ] || {
    echo "Refusing: the started container is not the one the action asked for: $lla_name" >&2
    exit 72
  }
  mount_check "$lla_name" >/dev/null
}

stop_shipped_agent() {
  owned agent1
  docker stop -t 15 "$prefix-agent1" >/dev/null
}

start_shipped_agent() {
  owned agent1
  docker start "$prefix-agent1" >/dev/null
  mount_check "$prefix-agent1" >/dev/null
}

case "${1:-}" in
stage)
  owned_image
  mkdir -p "$stage"
  chmod 700 "$stage"
  require_stage
  # File modes and the digest check inside bounded containers that only mount
  # the staged directory; a mismatch aborts before anything is started.
  one_shot -v "$stage:/s" --entrypoint /bin/sh "$image" -c \
    'cd /s || exit 1; chmod 0755 agent-new xray-core || exit 1; chmod 0644 geoip.dat geosite.dat || exit 1; chmod 0600 SHA256SUMS || exit 1'
  verify_stage
  one_shot -v "$stage:/s:ro" --entrypoint /bin/sh "$image" -c \
    'cd /s || exit 1; echo "staged agent: $(./agent-new -version)"; echo "staged core:  $(./xray-core version | head -n 1)"; echo "staged assets: geoip.dat=$(wc -c < geoip.dat) geosite.dat=$(wc -c < geosite.dat) bytes"'
  ;;
baseline)
  owned_image
  echo "installed image: $(docker image inspect --format '{{.Id}}' "$image")"
  echo "image agent:  $(one_shot --entrypoint /app/agent "$image" -version) sha256=$(one_shot --entrypoint /bin/sh "$image" -c 'sha256sum /app/agent' | cut -d" " -f1)"
  echo "image core:   $(one_shot --entrypoint /app/xray "$image" version | head -n 1) sha256=$(one_shot --entrypoint /bin/sh "$image" -c 'sha256sum /app/xray' | cut -d" " -f1)"
  if [ -s "$stage/agent-new" ] && [ -s "$stage/xray-core" ]; then
    echo "staged agent: $(one_shot -v "$stage:/s:ro" --entrypoint /s/agent-new "$image" -version) sha256=$(one_shot -v "$stage:/s:ro" --entrypoint /bin/sh "$image" -c 'sha256sum /s/agent-new' | cut -d" " -f1)"
    echo "staged core:  $(one_shot -v "$stage:/s:ro" --entrypoint /s/xray-core "$image" version | head -n 1) sha256=$(one_shot -v "$stage:/s:ro" --entrypoint /bin/sh "$image" -c 'sha256sum /s/xray-core' | cut -d" " -f1)"
  else
    echo 'staged agent: not uploaded yet'
    echo 'staged core:  not uploaded yet'
  fi
  ;;
state-summary)
  owned_volume
  # Sizes and digests only: the state database also holds the agent token and
  # member credentials, so its content is never printed by this action.
  one_shot -v "$statevolume:/state:ro" --entrypoint /bin/sh "$image" -c \
    'for f in agent.db agent.db-wal; do [ -f "/state/$f" ] || continue; printf "%s size=%s sha256=" "$f" "$(wc -c < "/state/$f")"; sha256sum "/state/$f" | cut -d" " -f1; done'
  ;;
state-transfer)
  owned_volume
  # Transfer only: the framed base64 is consumed by the offline checker and must
  # never be logged by the caller.
  one_shot -v "$statevolume:/state:ro" --entrypoint /bin/sh "$image" -c \
    'for f in agent.db agent.db-wal; do [ -f "/state/$f" ] || continue; echo "FILE $f $(wc -c < "/state/$f")"; base64 < "/state/$f"; echo "END $f"; done'
  ;;
mount-check)
  name=${2:-}
  case "$name" in
  agent1) mount_check "$prefix-agent1" ;;
  agent1v2) mount_check "$upgraded" ;;
  agent1core) mount_check "$coretest" ;;
  *) echo 'Refusing: mount-check needs agent1, agent1v2 or agent1core' >&2; exit 57 ;;
  esac
  ;;
listeners)
  # Deterministic listener probe inside the owned container's network namespace.
  # Only the fixed container/port lists are accepted, and the count comes from the
  # kernel tables (/proc/net/tcp*, local address column + state 0A = LISTEN), so a
  # host side SSH forward can never be mistaken for the node itself.
  name=${2:-}
  port=${3:-}
  case "$name" in
  agent1) target=$prefix-agent1 ;;
  agent2) target=$prefix-agent2 ;;
  agent1v2) target=$upgraded ;;
  agent1core) target=$coretest ;;
  *) echo 'Refusing: listeners needs agent1, agent2, agent1v2 or agent1core' >&2; exit 66 ;;
  esac
  case "$port" in
  20101 | 20102 | 20103 | 20104 | 20105) ;;
  *) echo 'Refusing: listeners only accepts the fixed port list 20101-20105' >&2; exit 67 ;;
  esac
  docker container inspect "$target" >/dev/null 2>&1 || {
    echo "Refusing: the owned container $name is missing" >&2
    exit 68
  }
  owned_lifecycle "$target"
  [ "$(docker container inspect --format '{{.State.Running}}' "$target")" = "true" ] || {
    echo "Refusing: $name is not running, its network namespace cannot be inspected" >&2
    exit 69
  }
  hex=$(printf '%04X' "$port")
  count=$(docker exec "$target" sh -c 'hex=$1
    total=0
    tables=0
    for f in /proc/net/tcp /proc/net/tcp6; do
      [ -r "$f" ] || continue
      tables=$((tables + 1))
      if ! c=$(awk -v want=":$hex" "NR > 1 && \$2 ~ (want \"\$\") && \$4 == \"0A\" { n++ } END { print n + 0 }" "$f"); then
        echo "the listener probe could not read $f" >&2
        exit 1
      fi
      case "$c" in
      "" | *[!0-9]*)
        echo "the listener probe got unexpected output from awk for $f: $c" >&2
        exit 1
        ;;
      esac
      total=$((total + c))
    done
    if [ "$tables" -lt 1 ]; then
      echo "neither /proc/net/tcp nor /proc/net/tcp6 is readable inside this container" >&2
      exit 1
    fi
    echo "$total"' sh "$hex") || {
    echo "Refusing: the listener probe inside $name failed, its count is unknown" >&2
    exit 71
  }
  case "$count" in
  '' | *[!0-9]*)
    echo "Refusing: the listener probe returned unexpected output" >&2
    exit 70
    ;;
  esac
  echo "listeners container=$name port=$port count=$count"
  ;;
agent-upgrade)
  owned agent1
  owned_volume
  owned_network
  owned_image
  require_stage
  [ -f "$root/secrets/agent1.json" ] || { echo 'Refusing: the owned agent1 config is missing' >&2; exit 58; }
  [ -f "$root/secrets/ca.pem" ] || { echo 'Refusing: the owned test CA is missing' >&2; exit 59; }
  drop_lifecycle "$upgraded"
  drop_lifecycle "$coretest"
  stop_shipped_agent
  run_lifecycle_agent "$upgraded" /s/agent-new "$root/secrets/agent1.json"
  echo "agent1 upgraded: $upgraded runs the staged agent with the image core on the unchanged state volume $statevolume"
  ;;
core-switch)
  owned agent1
  owned_volume
  owned_network
  owned_image
  require_stage
  if ! docker container inspect "$upgraded" >/dev/null 2>&1; then
    echo 'Refusing: core-switch needs the upgraded agent container first (the new agent stays in place, only the core changes)' >&2
    exit 60
  fi
  # Same ownership check the other actions use: a hand-written --format with the
  # label name inside single quotes never expands $label and never matches.
  owned_lifecycle "$upgraded"
  # The staged core only starts after its own geoip.dat/geosite.dat are proven
  # complete and intact, and never before: the second real A13 run stopped the
  # serving Agent and started a core that could not load /s/geoip.dat.
  verify_stage
  umask 077
  # Only the xray_path value changes; nothing of the original config is printed.
  sed -e 's#"xray_path"[[:space:]]*:[[:space:]]*"[^"]*"#"xray_path": "/s/xray-core"#' \
    "$root/secrets/agent1.json" > "$stage/agent1-core.json" || {
    echo 'Refusing: cannot derive the core-switch agent config' >&2
    exit 61
  }
  chmod 600 "$stage/agent1-core.json"
  grep -q '"xray_path": "/s/xray-core"' "$stage/agent1-core.json" || {
    echo 'Refusing: the derived agent config does not point at the staged core' >&2
    exit 62
  }
  drop_lifecycle "$coretest"
  # The upgraded container is stopped, not deleted: it is the rollback target and
  # it must not hold the state volume or the loopback ports while the core test
  # container runs.
  docker stop -t 15 "$upgraded" >/dev/null
  run_lifecycle_agent "$coretest" /s/agent-new "$stage/agent1-core.json"
  echo "core switched: $coretest runs the same staged agent with the staged core on the unchanged state volume $statevolume"
  ;;
core-rollback)
  drop_lifecycle "$coretest"
  owned agent1
  owned_volume
  owned_network
  if ! docker container inspect "$upgraded" >/dev/null 2>&1; then
    echo 'Refusing: core-rollback needs the upgraded agent container to return to' >&2
    exit 63
  fi
  # Reuse the shared ownership check: the hand-written --format with the label
  # name inside single quotes never expanded $label and never matched.
  owned_lifecycle "$upgraded"
  docker start "$upgraded" >/dev/null
  mount_check "$upgraded" >/dev/null
  echo 'core rolled back: agent1core removed and agent1v2 (image core) restarted on the same state volume'
  ;;
agent-rollback | cleanup)
  drop_lifecycle "$upgraded"
  drop_lifecycle "$coretest"
  rm -f "$stage/agent1-core.json"
  owned_volume
  owned_network
  start_shipped_agent
  echo 'rollback completed: lifecycle containers removed and the shipped agent1 container (0.1.0) is running on the same state volume'
  ;;
status)
  for name in "$prefix-agent1" "$upgraded" "$coretest"; do
    if docker container inspect "$name" >/dev/null 2>&1; then
      owned_lifecycle "$name"
      docker container inspect --format '{{.Name}} status={{.State.Status}} restarts={{.RestartCount}} label={{index .Config.Labels "io.proxypanel.acceptance"}} memory={{.HostConfig.Memory}} cpus={{.HostConfig.NanoCpus}} pids={{json .HostConfig.PidsLimit}} privileged={{.HostConfig.Privileged}} network={{.HostConfig.NetworkMode}} ports={{json .HostConfig.PortBindings}}' "$name"
    fi
  done
  ;;
logs)
  name=${2:-}
  case "$name" in
  agent1v2) owned_lifecycle "$upgraded"; docker logs --tail 80 "$upgraded" 2>&1 ;;
  agent1core) owned_lifecycle "$coretest"; docker logs --tail 80 "$coretest" 2>&1 ;;
  *) echo 'Refusing: logs needs agent1v2 or agent1core' >&2; exit 64 ;;
  esac
  ;;
*)
  echo 'Unknown lifecycle action' >&2
  exit 65
  ;;
esac
