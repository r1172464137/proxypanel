#!/bin/sh
# A14, one-shot, dedicated acceptance resources only. Never source an env file.
# Install as /opt/proxypanel-acceptance-20261021/recovery.sh. No automatic sync.
set -eu
umask 077
root=/opt/proxypanel-acceptance-20261021
prefix=ppaccept-20261021
value=20261021
label=io.proxypanel.acceptance
image=proxypanel-acceptance:20261021
[ ! -L "$root" ] && [ "$(cat "$root/.proxypanel-acceptance")" = "$value" ] || exit 40
cd "$root"
owned() {
 [ "$(docker container inspect --format '{{index .Config.Labels "io.proxypanel.acceptance"}}' "$prefix-$1")" = "$value" ] || exit 41
}
volume_owned() {
 [ "$(docker volume inspect --format '{{index .Labels "io.proxypanel.acceptance"}}' "$prefix-$1")" = "$value" ] || exit 42
}
stopped() {
 owned "$1"
 [ "$(docker container inspect --format '{{.State.Running}}' "$prefix-$1")" = false ] || exit 43
}
absent_container() { ! docker container inspect "$prefix-$1" >/dev/null 2>&1 || exit 44; }
absent_volume() { ! docker volume inspect "$prefix-$1" >/dev/null 2>&1 || exit 45; }
network_owned() {
 [ "$(docker network inspect --format '{{index .Labels "io.proxypanel.acceptance"}}' "$prefix-net")" = "$value" ] || exit 46
}
restore_owned() {
 owned restore; volume_owned restore
 [ "$(docker container inspect --format '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}' "$prefix-restore")" = "$prefix-restore" ] || exit 47
}
# No Docker socket, host network, unbounded helper, or unrelated host mount.
helper() {
 name=$1; shift; absent_container "$name"
 docker run --rm --name "$prefix-$name" --label "$label=$value" --network none \
  --memory 192m --memory-swap 192m --cpus .5 --pids-limit 64 \
  --cap-drop ALL --security-opt no-new-privileges --log-opt max-size=5m --log-opt max-file=2 "$@"
}
export_db() {
 which=$1; vol=$2
 stopped "$which"; volume_owned "$vol"
 [ ! -L "$root/recovery" ] && [ -d "$root/recovery" ] || exit 48
 # A stopped, cleanly closed SQLite DB has no nonempty WAL. Refuse a partial copy.
 helper recovery-export --entrypoint /bin/sh -v "$prefix-$vol:/state:ro" -v "$root/recovery:/evidence" "$image" -ec '
  test -s /state/agent.db
  test ! -s /state/agent.db-wal
  test ! -L /evidence/'"$which"'.db
  cp /state/agent.db /evidence/'"$which"'.db
  chmod 600 /evidence/'"$which"'.db
 '
}
case "${1:-}" in
 preflight)
  [ "$#" = 1 ] || exit 49
  network_owned; owned panel; owned postgres; owned agent1; owned agent2
  volume_owned agent1
  volume_owned agent2
  [ "$(docker container inspect --format '{{range .Mounts}}{{if eq .Destination "/state"}}{{.Name}}{{end}}{{end}}' "$prefix-agent2")" = "$prefix-agent2" ] || exit 50
  [ "$(docker container inspect --format '{{range .Mounts}}{{if eq .Destination "/state"}}{{.Name}}{{end}}{{end}}' "$prefix-agent1")" = "$prefix-agent1" ] || exit 50
  for n in restore recovery-panel recovery-agent recovery-copy recovery-export recovery-accept; do absent_container "$n"; done
  absent_volume restore; absent_volume recovery-agent
  # A run that was investigated must be reset with reset-evidence, never by
  # deleting a source volume or by pruning the host.
  [ ! -e "$root/recovery" ] && [ ! -L "$root/recovery" ] || {
   echo 'preflight: retained recovery evidence exists; inspect it, then run: sh recovery.sh reset-evidence' >&2
   exit 51
  }
  mkdir -m 700 "$root/recovery"
  test -f "$root/secrets/panel.env"; test -f "$root/secrets/agent1.json"
  ;;
 export-source) [ "$#" = 1 ] || exit 49; export_db agent1 agent1;;
 export-source2) [ "$#" = 1 ] || exit 49; export_db agent2 agent2;;
state-report)
  [ "$#" = 1 ] || exit 49
  # Sanitized, read-only durable-state report the source Agent publishes next to
  # its SQLite database after every committed instance update (state codes and
  # counters only: no token, config, credential, e-mail or id). The Agent keeps
  # running, so this is the completion signal for a local expiry - not a failed
  # network fetch, and not a copy of the live WAL database.
  network_owned; owned agent1
  helper recovery-report --entrypoint /bin/sh -v "$prefix-agent1:/state:ro" "$image" -ec '
   test -f /state/status.json
   test ! -L /state/status.json
   cat /state/status.json
  '
  ;;
 copy-agent)
  [ "$#" = 1 ] || exit 49
  stopped agent1; volume_owned agent1; absent_volume recovery-agent
  docker volume create --label "$label=$value" "$prefix-recovery-agent" >/dev/null
  helper recovery-copy --entrypoint /bin/sh -v "$prefix-agent1:/source:ro" -v "$prefix-recovery-agent:/state" "$image" -ec 'test -s /source/agent.db; cp -a /source/. /state/'
  # This is explicitly a TEST COPY: same identity, baseline, outbox and expiry,
  # never a new registration or a cleared original Agent volume.
  ;;
 panel-up)
  [ "$#" = 1 ] || exit 49
  network_owned; restore_owned; absent_container recovery-panel
  [ ! -L "$root/recovery/panel.env" ] || exit 52
  [ "$(docker exec "$prefix-restore" psql -U acceptance -d acceptance -Atc 'select recovery_mode and control_generation>0 and not exists(select 1 from agents where control_generation<>control_state.control_generation) from control_state where id=1')" = t ] || exit 53
  # Preserve every original setting/secret, replacing ONLY the fixed DB host.
  # Password is never interpreted by a shell, printed, or passed on argv.
  awk '
   /^DATABASE_URL=/ {
    if ($0 !~ /@postgres:5432\/acceptance([?]|$)/) exit 1
    sub(/@postgres:5432\/acceptance/, "@restore:5432/acceptance"); n++
   }
   { print }
   END { if (n != 1) exit 1 }
  ' "$root/secrets/panel.env" > "$root/recovery/panel.env"
  chmod 600 "$root/recovery/panel.env"
  docker run -d --name "$prefix-recovery-panel" --label "$label=$value" \
   --network "$prefix-net" --network-alias recovery-panel \
   --memory 192m --memory-swap 192m --cpus .5 --pids-limit 128 \
   --cap-drop ALL --security-opt no-new-privileges --restart no \
   --log-opt max-size=5m --log-opt max-file=2 --env-file "$root/recovery/panel.env" \
   -v "$root/secrets/ca.pem:/run/fixture/ca.pem:ro" -v "$root/secrets/tls.key:/run/fixture/tls.key:ro" \
   -p 127.0.0.1:18444:18443 "$image" >/dev/null
  ;;
 agent-start)
  [ "$#" = 1 ] || exit 49
  network_owned; owned recovery-panel; restore_owned; volume_owned recovery-agent
  # Reuse the existing tunneled node1 port ONLY while original Agent1 is stopped.
  stopped agent1
  if docker container inspect "$prefix-recovery-agent" >/dev/null 2>&1; then
   owned recovery-agent; docker start "$prefix-recovery-agent" >/dev/null
  else
   ip=$(docker container inspect --format '{{with index .NetworkSettings.Networks "ppaccept-20261021-net"}}{{.IPAddress}}{{end}}' "$prefix-recovery-panel")
   case "$ip" in ''|*[!0-9.]*) exit 54;; esac
   # Keep the original TLS name "panel", but pin this copy to recovery-panel.
   docker run -d --name "$prefix-recovery-agent" --label "$label=$value" \
    --network "$prefix-net" --add-host "panel:$ip" \
    --memory 192m --memory-swap 192m --cpus .5 --pids-limit 128 \
    --cap-drop ALL --security-opt no-new-privileges --restart no \
    --log-opt max-size=5m --log-opt max-file=2 --tmpfs /tmp:rw,noexec,nosuid,size=16m \
    -v "$prefix-recovery-agent:/state" -v "$root/secrets/agent1.json:/run/fixture/agent1.json:ro" \
    -v "$root/secrets/ca.pem:/run/fixture/ca.pem:ro" -e SSL_CERT_FILE=/run/fixture/ca.pem \
    -p 127.0.0.1:20101:20101 --entrypoint /app/agent "$image" -config /run/fixture/agent1.json >/dev/null
  fi
  ;;
 agent-stop) [ "$#" = 1 ] || exit 49; owned recovery-agent; docker stop -t 30 "$prefix-recovery-agent" >/dev/null;;
 export-copy) [ "$#" = 1 ] || exit 49; export_db recovery-agent recovery-agent;;
 accept)
  [ "$#" = 2 ] || exit 49
  generation=$2
  case "$generation" in ''|*[!0-9]*|0) exit 55;; esac
  [ "${#generation}" -le 16 ] || exit 55
  stopped recovery-agent; volume_owned recovery-agent; restore_owned
  [ "$(docker exec "$prefix-restore" psql -U acceptance -d acceptance -Atc 'select control_generation from control_state where id=1 and not recovery_mode')" = "$generation" ] || exit 56
  helper recovery-accept -v "$prefix-recovery-agent:/state" -v "$root/secrets/agent1.json:/run/fixture/agent1.json:ro" \
   --entrypoint /app/agent "$image" -config /run/fixture/agent1.json -accept-generation "$generation" >/dev/null
  ;;
 stop)
  [ "$#" = 1 ] || exit 49
  # Leave owned volumes/evidence intact for inspection (stopping is not
  # deleting); stop the independent restore database too so its loopback port
  # and memory are released. No rm/prune.
  for n in recovery-agent recovery-panel restore; do
   if docker container inspect "$prefix-$n" >/dev/null 2>&1; then owned "$n"; docker stop -t 30 "$prefix-$n" >/dev/null; fi
  done
  ;;
 reset-evidence)
  [ "$#" = 1 ] || exit 49
  # Operator-only reset BETWEEN runs. Scope is exactly this project's A14
  # evidence directory: the sqlite exports and the generated recovery panel
  # env. It refuses while any owned recovery container/volume exists, refuses
  # a symlink or an unexpected entry, and never touches the source agent/pg/
  # redis/panel volumes, backup/ or anything host-global (no prune).
  network_owned
  for n in restore recovery-panel recovery-agent recovery-copy recovery-export recovery-accept; do absent_container "$n"; done
  absent_volume restore; absent_volume recovery-agent
  [ ! -L "$root/recovery" ] || exit 58
  if [ ! -e "$root/recovery" ]; then
   echo 'no owned recovery evidence directory present'
   exit 0
  fi
  [ -d "$root/recovery" ] || exit 58
  for entry in "$root/recovery"/* "$root/recovery"/.[!.]*; do
   [ -e "$entry" ] || continue
   name=${entry##*/}
   case "$name" in
    agent1.db|agent2.db|panel.env) rm -f -- "$entry"; echo "removed owned evidence file: $name";;
    *) echo "reset-evidence: unexpected entry in the owned recovery directory: $name" >&2; exit 59;;
   esac
  done
  rmdir "$root/recovery" && echo 'removed owned recovery evidence directory'
  echo 'source volumes, backup/ and unrelated host resources were not touched'
  ;;
 *) echo 'Unknown dedicated recovery action' >&2; exit 57;;
esac
