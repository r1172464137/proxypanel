#!/bin/sh
# Bounded fault actions for the owned acceptance stack (project scope 20261021).
#
# Scope is hardcoded on purpose: no container name, path or size is accepted
# from the caller. Every action re-verifies the project root marker and the
# ownership label io.proxypanel.acceptance=20261021 before touching a container
# or a named volume, and it only ever creates loopback-published,
# capability-dropped, memory/CPU/PID limited containers inside the owned
# network. It never prunes, never removes anything outside $root/faultstate, and
# never mounts a host path other than the dedicated state copy below.
#
# Actions:
#   capacity-up    stop the owned agent1 and start the bounded capacity agent:
#                  its /state is a 16 MiB tmpfs seeded with an ownership-checked
#                  copy of the agent1 state that is staged only in
#                  $root/faultstate/agent1; the copy gets the same test CA
#                  (SSL_CERT_FILE=/run/fixture/ca.pem) as the real agent1 so
#                  panel TLS authentication is unchanged
#   capacity-fill  fill that tmpfs until the kernel refuses writes (ENOSPC)
#   capacity-free  remove the filler files again
#   capacity-info  print the runtime limits (privileged/network/memory/CPU/PID/
#                  security options/capabilities/tmpfs/binds), the mounted test
#                  CA, the state ownership and the state filesystem usage
#   capacity-down  remove the capacity agent and start the owned agent1 again
#   state-dump NAME  base64 of agent.db / agent.db-wal for NAME in
#                  agent1|agent2|agent1cap
set -eu

root=/opt/proxypanel-acceptance-20261021
label=io.proxypanel.acceptance
value=20261021
prefix=ppaccept-20261021
image=proxypanel-acceptance:20261021
network=$prefix-net
capacity=$prefix-agent1cap
seed=$root/faultstate/agent1
tmpfs=/state:rw,noexec,nosuid,size=16m

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

owned_network() {
  docker network inspect "$network" >/dev/null 2>&1 || {
    echo 'Refusing: the owned acceptance network is missing' >&2
    exit 43
  }
  [ "$(docker network inspect --format "{{index .Labels \"$label\"}}" "$network")" = "$value" ] || {
    echo 'Refusing: the acceptance network is not owned by this project' >&2
    exit 44
  }
}

# The state copy must come from the owned agent1 named volume. Docker silently
# creates a missing named volume, so the name alone is not proof of ownership.
owned_volume() {
  docker volume inspect "$prefix-$1" >/dev/null 2>&1 || {
    echo "Refusing: the owned source volume $prefix-$1 is missing" >&2
    exit 45
  }
  [ "$(docker volume inspect --format "{{index .Labels \"$label\"}}" "$prefix-$1")" = "$value" ] || {
    echo "Refusing: the source volume $prefix-$1 does not carry $label=$value" >&2
    exit 46
  }
}

drop_capacity() {
  if docker container inspect "$capacity" >/dev/null 2>&1; then
    owned agent1cap
    docker rm -f "$capacity" >/dev/null
  fi
}

case "${1:-}" in
capacity-up)
  owned agent1
  owned_network
  owned_volume agent1
  [ -f "$root/secrets/agent1.json" ] || { echo 'Refusing: the owned agent1 config is missing' >&2; exit 47; }
  [ -f "$root/secrets/ca.pem" ] || { echo 'Refusing: the owned test CA is missing' >&2; exit 48; }
  # Never leave two holders of one agent identity running: the real container is
  # stopped first and only ever restarted from its own named state volume.
  drop_capacity
  docker stop -t 15 "$prefix-agent1" >/dev/null
  mkdir -p "$root/faultstate"
  chmod 700 "$root/faultstate"
  rm -rf "$seed"
  mkdir -p "$seed"
  chmod 700 "$seed"
  # One-shot, network-less, capability-dropped and CPU/PID/memory limited copy of
  # the owned agent state. It fails when the copy does not preserve the file
  # ownership of the source volume: the agent may only ever read and write its
  # state as the same user, so a silently unwritable copy must never start.
  docker run --rm --network none --label "$label=$value" --memory 64m --memory-swap 64m \
    --cpus .5 --pids-limit 64 --security-opt no-new-privileges \
    --cap-drop ALL --entrypoint /bin/sh \
    -v "$prefix-agent1:/src:ro" -v "$seed:/dst" "$image" \
    -c 'cp -a /src/. /dst/ || exit 1; src=$(stat -c %u:%g /src/agent.db) || exit 1; dst=$(stat -c %u:%g /dst/agent.db) || exit 1; [ "$src" = "$dst" ] || { echo "Refusing: staged state ownership $dst differs from the source volume $src" >&2; exit 1; }' >/dev/null ||
    { echo 'Refusing: the staged agent state copy failed' >&2; exit 49; }
  test -s "$seed/agent.db" || { echo 'Refusing: the staged agent state copy is empty' >&2; exit 49; }
  # The 16 MiB tmpfs is a hard bound, so the seeded copy has to leave the agent
  # room to run before the capacity fault is injected.
  seed_kb=$(du -sk "$seed" | awk '{print $1}')
  [ "$seed_kb" -le 8192 ] || {
    echo "Refusing: the staged state copy holds ${seed_kb} KiB, more than half of the 16 MiB capacity filesystem" >&2
    exit 49
  }
  # The capacity agent keeps every boundary of the real agent1 container
  # (memory/CPU/PID limits, dropped capabilities, no-new-privileges, loopback
  # only port publishing) and additionally gets the test CA it authenticates the
  # panel with: without that mount SSL_CERT_FILE points at a missing file and
  # the agent can never fetch a snapshot.
  docker run -d --name "$capacity" --label "$label=$value" --network "$network" \
    --memory 192m --memory-swap 192m --cpus .5 --pids-limit 128 --restart no \
    --security-opt no-new-privileges --cap-drop ALL \
    --log-opt max-size=5m --log-opt max-file=2 \
    --tmpfs "$tmpfs" --tmpfs /tmp:rw,noexec,nosuid,size=16m \
    -v "$seed:/run/seed:ro" \
    -v "$root/secrets/agent1.json:/run/fixture/agent1.json:ro" \
    -v "$root/secrets/ca.pem:/run/fixture/ca.pem:ro" \
    -e SSL_CERT_FILE=/run/fixture/ca.pem \
    -p 127.0.0.1:20101:20101 -p 127.0.0.1:20102:20102 -p 127.0.0.1:20103:20103 \
    -p 127.0.0.1:20104:20104 -p 127.0.0.1:20105:20105 \
    --entrypoint /bin/sh "$image" \
    -c 'cp -a /run/seed/. /state/ || exit 1; sync; owner=$(stat -c %u /state/agent.db) || exit 1; [ "$owner" = "$(id -u)" ] || { echo "Refusing: /state/agent.db is owned by uid $owner but the agent runs as uid $(id -u)" >&2; exit 1; }; exec /app/agent -config /run/fixture/agent1.json' >/dev/null
  owned agent1cap
  echo 'Capacity agent created: 16 MiB tmpfs state seeded from the owned agent1 state copy; test CA mounted for panel TLS.'
  ;;
capacity-fill)
  owned agent1cap
  # The tmpfs size is the hard bound: the loop cannot consume anything outside
  # it and stops at the first refused write.
  docker exec "$capacity" sh -c 'i=0; while [ "$i" -lt 600 ]; do dd if=/dev/zero of=/state/fill.$i bs=64k count=1 2>/dev/null || break; i=$((i+1)); done; sync; if dd if=/dev/zero of=/state/fill-probe bs=1k count=1 2>/dev/null; then echo "enospc=no"; else echo "enospc=yes"; fi; rm -f /state/fill-probe; echo "fillers=$i"; df -k /state | tail -n 1'
  ;;
capacity-free)
  owned agent1cap
  docker exec "$capacity" sh -c 'rm -f /state/fill.* /state/fill-probe; sync; echo "fillers=$(ls -A /state | grep -c fill || true)"; df -k /state | tail -n 1'
  ;;
capacity-info)
  owned agent1cap
  docker container inspect --format 'container name={{.Name}} status={{.State.Status}} privileged={{.HostConfig.Privileged}} network={{.HostConfig.NetworkMode}} memory={{.HostConfig.Memory}} cpus={{.HostConfig.NanoCpus}} pids={{json .HostConfig.PidsLimit}} restarts={{.RestartCount}}' "$capacity"
  docker container inspect --format 'securityopt {{json .HostConfig.SecurityOpt}}' "$capacity"
  docker container inspect --format 'capdrop {{json .HostConfig.CapDrop}}' "$capacity"
  docker container inspect --format 'tmpfs {{json .HostConfig.Tmpfs}}' "$capacity"
  docker container inspect --format 'binds {{json .HostConfig.Binds}}' "$capacity"
  docker exec "$capacity" sh -c 'echo "uid=$(id -u)"; if [ -r /run/fixture/ca.pem ]; then echo "ca=present"; else echo "ca=missing"; fi; echo "state owner=$(stat -c %u /state/agent.db)"; echo "state files=$(ls -A /state | wc -l)"; echo "state df: $(df -k /state | tail -n 1)"'
  ;;
capacity-down)
  drop_capacity
  rm -rf "$seed"
  owned agent1
  docker start "$prefix-agent1" >/dev/null
  echo 'Capacity agent removed and the owned agent1 restarted from its named state volume.'
  ;;
state-dump)
  name=${2:-}
  case "$name" in
  agent1 | agent2 | agent1cap) ;;
  *) echo 'Refusing: state-dump needs one of the fixed owned names' >&2; exit 50 ;;
  esac
  owned "$name"
  docker exec "$prefix-$name" sh -c 'command -v base64 >/dev/null || { echo "Refusing: the owned image has no base64" >&2; exit 51; }; for f in agent.db agent.db-wal; do [ -f "/state/$f" ] || continue; echo "FILE $f $(wc -c < "/state/$f")"; base64 < "/state/$f"; echo "END $f"; done'
  ;;
*)
  echo 'Unknown fault action' >&2
  exit 60
  ;;
esac
