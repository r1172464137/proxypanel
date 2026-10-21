#!/bin/sh
# Only the dedicated approved acceptance stack. No host restart or global cleanup.
set -eu
root=/opt/proxypanel-acceptance-20261021
label=io.proxypanel.acceptance
value=20261021
prefix=ppaccept-20261021
image=proxypanel-acceptance:20261021
[ "$(cat "$root/.proxypanel-acceptance")" = "$value" ] || exit 40
cd "$root"
owned() { [ "$(docker container inspect --format '{{index .Config.Labels "io.proxypanel.acceptance"}}' "$prefix-$1")" = "$value" ] || exit 41; }
volume() {
 if docker volume inspect "$prefix-$1" >/dev/null 2>&1; then
  [ "$(docker volume inspect --format '{{index .Labels "io.proxypanel.acceptance"}}' "$prefix-$1")" = "$value" ] || exit 42
 else docker volume create --label "$label=$value" "$prefix-$1" >/dev/null; fi
}
run() { name=$1; shift; if docker container inspect "$prefix-$name" >/dev/null 2>&1; then owned "$name"; echo "already exists: $name"; return; fi
 docker run -d --name "$prefix-$name" --label "$label=$value" --network "$prefix-net" --network-alias "$name" --memory 192m --memory-swap 192m --cpus .5 --pids-limit 128 --security-opt no-new-privileges --log-opt max-size=5m --log-opt max-file=2 "$@" >/dev/null
}
agent() { n=$1; volume "agent$n"; test -f "$root/secrets/agent$n.json"; shift
 run "agent$n" --cap-drop ALL --restart unless-stopped --tmpfs /tmp:rw,noexec,nosuid,size=16m -v "$prefix-agent$n:/state" -v "$root/secrets/agent$n.json:/run/fixture/agent$n.json:ro" -v "$root/secrets/ca.pem:/run/fixture/ca.pem:ro" -e SSL_CERT_FILE=/run/fixture/ca.pem -p "127.0.0.1:$((20000+n*100+1)):$((20000+n*100+1))" -p "127.0.0.1:$((20000+n*100+2)):$((20000+n*100+2))" -p "127.0.0.1:$((20000+n*100+3)):$((20000+n*100+3))" -p "127.0.0.1:$((20000+n*100+4)):$((20000+n*100+4))" -p "127.0.0.1:$((20000+n*100+5)):$((20000+n*100+5))" --entrypoint /app/agent "$image" -config "/run/fixture/agent$n.json"
}
case "${1:-}" in
 up)
  if docker network inspect "$prefix-net" >/dev/null 2>&1; then
   [ "$(docker network inspect --format '{{index .Labels "io.proxypanel.acceptance"}}' "$prefix-net")" = "$value" ] || exit 43
  else docker network create --opt com.docker.network.bridge.host_binding_ipv4=127.0.0.1 --label "$label=$value" "$prefix-net" >/dev/null; fi
  volume pg; volume redis
  run postgres --restart unless-stopped --env-file "$root/secrets/postgres.env" -v "$prefix-pg:/var/lib/postgresql/data" -p 127.0.0.1:25432:5432 postgres:17-alpine
  run redis --restart unless-stopped -v "$prefix-redis:/data" -v "$root/secrets/redis.conf:/run/redis.conf:ro" redis:7-alpine redis-server /run/redis.conf
  i=0; until docker exec "$prefix-postgres" pg_isready -U acceptance -d acceptance >/dev/null; do i=$((i+1)); [ "$i" -lt 30 ] || exit 44; sleep 1; done
  run panel --cap-drop ALL --restart unless-stopped --env-file "$root/secrets/panel.env" -v "$root/secrets/ca.pem:/run/fixture/ca.pem:ro" -v "$root/secrets/tls.key:/run/fixture/tls.key:ro" -p 127.0.0.1:18443:18443 "$image"
  run target --cap-drop ALL --entrypoint /app/fixture "$image"
  echo 'Acceptance stack created with loopback-only mappings and bounded resources.'
  ;;
 agent1) agent 1;;
 crash)
  name=${2:-}; case "$name" in agent1|agent2) ;; *) exit 50;; esac; owned "$name"
  docker exec "$prefix-$name" sh -c 'kill -QUIT 1'
  echo "Injected process exit in owned $name (Docker restart policy remains enabled)";;
 restarts)
  name=${2:-}; case "$name" in agent1|agent2) ;; *) exit 51;; esac; owned "$name"
  docker container inspect --format '{{.RestartCount}}' "$prefix-$name";;
 agent2) agent 2;;
 stop|start|restart|kill|remove)
  action=$1; name=${2:-}; case "$name" in panel|postgres|redis|agent1|agent2|target|restore) ;; *) exit 45;; esac
  owned "$name"
  case "$action" in stop) docker stop -t 15 "$prefix-$name" >/dev/null;; start) docker start "$prefix-$name" >/dev/null;; restart) docker restart -t 15 "$prefix-$name" >/dev/null;; kill) docker kill "$prefix-$name" >/dev/null;; remove) docker stop -t 15 "$prefix-$name" >/dev/null; docker rm "$prefix-$name" >/dev/null;; esac
  echo "$action completed for owned $name";;
 inspect)
  for name in postgres redis panel agent1 agent2 target restore; do if docker container inspect "$prefix-$name" >/dev/null 2>&1; then owned "$name"; docker container inspect --format '{{.Name}} {{.State.Status}} restarts={{.RestartCount}} memory={{.HostConfig.Memory}} cpus={{.HostConfig.NanoCpus}} privileged={{.HostConfig.Privileged}} ports={{json .HostConfig.PortBindings}}' "$prefix-$name"; fi; done;;
 logs)
  name=${2:-}; case "$name" in panel|agent1|agent2) ;; *) exit 46;; esac; owned "$name"; docker logs --tail 60 "$prefix-$name" 2>&1;;
 backup)
  owned postgres; mkdir -p "$root/backup"; chmod 700 "$root/backup"; umask 077
  docker exec "$prefix-postgres" pg_dump -U acceptance -d acceptance -Fc > "$root/backup/acceptance.dump"
  test -s "$root/backup/acceptance.dump"; echo 'Consistent custom-format project backup created.';;
 restore)
  test -s "$root/backup/acceptance.dump"; volume restore
  run restore --env-file "$root/secrets/postgres.env" -v "$prefix-restore:/var/lib/postgresql/data" -p 127.0.0.1:25433:5432 postgres:17-alpine
  i=0; until docker exec "$prefix-restore" pg_isready -U acceptance -d acceptance >/dev/null; do i=$((i+1)); [ "$i" -lt 30 ] || exit 47; sleep 1; done
  owned restore
  [ "$(docker exec "$prefix-restore" psql -U acceptance -d acceptance -Atc "select count(*) from information_schema.tables where table_schema='public'")" = 0 ] || { echo 'Refusing overwrite of nonempty restore target'; exit 48; }
  docker exec -i "$prefix-restore" pg_restore -U acceptance -d acceptance --exit-on-error < "$root/backup/acceptance.dump"
  echo 'Backup restored to isolated target; source untouched.';;
 *) echo 'Unknown acceptance action' >&2; exit 49;;
esac
