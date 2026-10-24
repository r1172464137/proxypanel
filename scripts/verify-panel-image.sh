#!/usr/bin/env bash
# Disposable Docker fixture. Never targets an existing deployment.
# IMAGE [UPGRADE_IMAGE]; optional PANEL_BROWSER_HOOK runs once per mode/phase.
# Hook receives PANEL_BASE, PANEL_SMOKE_{MODE,PHASE,PORT}, test credentials.
# Set PANEL_EVIDENCE_DIR to a private persistent directory; browser hook is required
# for browser evidence (absence is explicitly reported as NOT_RUN).
set -euo pipefail
IMAGE=${1:?usage: verify-panel-image.sh IMAGE [UPGRADE_IMAGE]}
UPGRADE_IMAGE=${2:-$IMAGE}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
PREFIX=${PANEL_FIXTURE_OWNER:-"pp-dual-$(date +%s)-$(openssl rand -hex 6)"}
[[ "$PREFIX" =~ ^pp-dual-[a-z0-9-]+$ ]] || exit 2
LABEL="io.proxypanel.dual-hosting=$PREFIX"
TMP=$(mktemp -d)
EVIDENCE=${PANEL_EVIDENCE_DIR:-$TMP/evidence}
mkdir -p "$EVIDENCE"; chmod 700 "$EVIDENCE"
DB="$PREFIX-db"; APP="$PREFIX-app"; WEB="$PREFIX-web"; EXTRACT="$PREFIX-extract"
audit() {
  docker ps -a --filter "label=$LABEL" --format '{{.ID}} {{.Names}} {{.Labels}}'
  docker network ls --filter "label=$LABEL" --format '{{.ID}} {{.Name}} {{.Labels}}'
}
cleanup() {
  rc=$?; trap - EXIT
  audit >"$EVIDENCE/owned-before-cleanup.txt"
  for name in "$WEB" "$APP" "$DB" "$EXTRACT"; do
    if [ "$(label_of "$name")" = "$PREFIX" ]; then
      docker logs "$name" >"$EVIDENCE/$name.log" 2>&1 || true
      docker rm -fv "$name" >/dev/null || true
    fi
  done
  if [ "$(docker network inspect -f '{{index .Labels "io.proxypanel.dual-hosting"}}' "$PREFIX" 2>/dev/null || true)" = "$PREFIX" ]; then
    docker network rm "$PREFIX" >/dev/null || true
  fi
  audit >"$EVIDENCE/owned-after-cleanup.txt"
  printf '%s\n' "$rc" >"$EVIDENCE/exit-code"
  rm -rf "$TMP"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 124' TERM INT
printf '%s\n' "$PREFIX" >"$EVIDENCE/run-id"
audit >"$EVIDENCE/owned-before-create.txt"
fail() { echo "FAIL: $*" >&2; exit 1; }
# Read a container label without failing when the image/container has no Labels
# map at all: buildx-built images and containers created without a label have no
# .Config.Labels entry, and `index` aborts the whole template in that case.
label_of() {
  docker inspect -f '{{if .Config.Labels}}{{index .Config.Labels "io.proxypanel.dual-hosting"}}{{end}}' "$1" 2>/dev/null || true
}
code() { curl -ksS --max-time 10 -o /dev/null -w '%{http_code}' "$1"; }
wait_http() {
  for i in $(seq 1 90); do
    if [ "$(code "$1" 2>/dev/null || true)" = 200 ]; then return; fi
    sleep 1
  done
  fail "timeout waiting for $1"
}
PASSWORD=$(openssl rand -hex 24)
DBPASS=$(openssl rand -hex 24)
KEY=$(openssl rand -base64 32)
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=panel.example.com \
  -keyout "$TMP/key.pem" -out "$TMP/cert.pem" >/dev/null 2>&1
docker network create --label "$LABEL" "$PREFIX" >/dev/null
docker run -d --label "$LABEL" --name "$DB" --network "$PREFIX" \
  -e "POSTGRES_PASSWORD=$DBPASS" postgres:17-alpine >/dev/null
for i in $(seq 1 60); do
  if docker exec "$DB" pg_isready -U postgres >/dev/null 2>&1; then break; fi
  sleep 1
done
BROWSER_FAILED=0
for MODE in spa api; do
  # Each hosting mode must independently exercise the old schema, not reuse 0006.
  docker exec "$DB" createdb -U postgres "$MODE"
  for PHASE in current upgrade; do
    ACTIVE=$IMAGE; [ "$PHASE" = upgrade ] && ACTIVE=$UPGRADE_IMAGE
    OUT="$EVIDENCE/$MODE-$PHASE"; mkdir -p "$OUT"
    docker image inspect "$ACTIVE" --format '{{.Id}} {{json .Config.Labels}} {{json .Config.Env}}' >"$OUT/image.txt" 2>/dev/null || docker image inspect "$ACTIVE" --format '{{.Id}} null {{json .Config.Env}}' >"$OUT/image.txt"
    # Copy OUT of a newly labelled extraction container only. Never into any container.
    docker create --label "$LABEL" --name "$EXTRACT" "$ACTIVE" >/dev/null
    rm -rf "$TMP/dist" "$TMP/migrations"
    docker cp "$EXTRACT:/usr/share/proxypanel/web" "$TMP/dist"
    docker cp "$EXTRACT:/app/panel" "$TMP/panel"
    docker cp "$EXTRACT:/app/migrations" "$TMP/migrations"
    docker rm "$EXTRACT" >/dev/null
    (cd "$TMP"; sha256sum panel; find dist migrations -type f -print0 | sort -z | xargs -0 sha256sum) >"$OUT/fingerprints.sha256"
    chmod -R a+rX "$TMP/dist"
    # In spa mode omit PANEL_WEB_ROOT entirely, proving the image default.
    # In api mode explicitly pass the empty value, never substitute a default.
    ENV_ROOT=(); [ "$MODE" = api ] && ENV_ROOT=(-e PANEL_WEB_ROOT=)
    docker run -d --label "$LABEL" --name "$APP" --network "$PREFIX" -p 127.0.0.1::8080 \
      "${ENV_ROOT[@]}" -e "PANEL_MASTER_KEY=$KEY" \
      -e "DATABASE_URL=postgres://postgres:$DBPASS@$DB:5432/$MODE?sslmode=disable" \
      -e "PANEL_PUBLIC_URL=${PANEL_SMOKE_PUBLIC_URL:-https://panel.example.com}" \
      -e PANEL_ADMIN_USER=admin -e "PANEL_ADMIN_PASSWORD=$PASSWORD" "$ACTIVE" >/dev/null
    docker exec "$APP" /bin/sh -ec 'test "$(id -u)" = 65532; ! command -v nginx; test -d /app/migrations'
    docker top "$APP" >"$OUT/processes.txt"
    PORT=$(docker port "$APP" 8080/tcp | awk -F: '{print $NF}')
    API="http://127.0.0.1:$PORT"; wait_http "$API/healthz"
    curl -fsS "$API/readyz" >"$OUT/readyz.json"
    docker exec "$DB" psql -U postgres -d "$MODE" -Atc 'SELECT * FROM schema_migrations ORDER BY version' >"$OUT/schema_migrations.txt"
    VERSION=$(docker exec "$DB" psql -U postgres -d "$MODE" -Atc 'SELECT count(*) FROM schema_migrations')
    EXPECTED=${PANEL_EXPECT_CURRENT_SCHEMA:-}
    [ "$PHASE" = upgrade ] && EXPECTED=${PANEL_EXPECT_UPGRADE_SCHEMA:-}
    [ -z "$EXPECTED" ] || [ "$VERSION" = "$EXPECTED" ] || fail "schema $VERSION expected $EXPECTED"
    docker inspect "$APP" --format '{{.Image}} {{json .Config.Env}}' >"$OUT/runtime.txt"
    if [ "$MODE" = api ]; then
      [ "$(code "$API/")" = 404 ] || fail 'explicit empty PANEL_WEB_ROOT did not disable SPA'
    else
      curl -fsS "$API/admin/nodes" | grep -q 'id="app"' || fail 'default SPA missing'
    fi
    python3 - "$ROOT/deploy/nginx.conf.example" "$TMP/default.conf" "$MODE" "$APP" <<'PY'
import sys
source,dest,mode,app=sys.argv[1:]
s=open(source).read().replace('127.0.0.1:8081',app+':8080')
s=s.replace('/etc/letsencrypt/live/panel.example.com/fullchain.pem','/certs/cert.pem')
s=s.replace('/etc/letsencrypt/live/panel.example.com/privkey.pem','/certs/key.pem')
if mode=='api':
    s=s.replace('    location / { proxy_pass http://proxypanel_api; }','')
    start=s.index('    # root /var/www/proxypanel;')
    end=s.index('    # A/B 共用',start)
    s=s[:start]+s[start:end].replace('    # ', '    ')+s[end:]
open(dest,'w').write(s)
PY
    cp "$TMP/default.conf" "$OUT/nginx.conf"
    # Separate container simulates the host TLS layer, NOT the real host nginx.
    # Reuse its random loopback port across replacement so browser cookies persist.
    BIND=127.0.0.1::443
    [ "$PHASE" = upgrade ] && BIND="127.0.0.1:$HTTPS_PORT:443"
    docker run -d --label "$LABEL" --name "$WEB" --network "$PREFIX" -p "$BIND" \
      -v "$TMP/default.conf:/etc/nginx/conf.d/default.conf:ro" \
      -v "$TMP:/certs:ro" -v "$TMP/dist:/var/www/proxypanel:ro" nginx:alpine >/dev/null
    docker exec "$WEB" nginx -t >"$OUT/nginx-test.txt" 2>&1
    HTTPS_PORT=$(docker port "$WEB" 443/tcp | awk -F: '{print $NF}')
    HTTPS="https://127.0.0.1:$HTTPS_PORT"; wait_http "$HTTPS/healthz"
    curl -kfsSI "$HTTPS/index.html" | tee "$OUT/index-headers.txt" | grep -iq 'cache-control: no-store' || fail 'index is cacheable'
    for PATHNAME in /assets/missing-smoke.js /api/unknown /sub/unknown; do
      STATUS=$(curl -ksS -D "$OUT/$(echo "$PATHNAME" | tr / _).headers" -o "$TMP/body" -w '%{http_code}' "$HTTPS$PATHNAME")
      cp "$TMP/body" "$OUT/$(echo "$PATHNAME" | tr / _).body"
      EXPECTED=404
      # Invalid subscription tokens intentionally use the uniform forbidden response.
      [ "$PATHNAME" = /sub/unknown ] && EXPECTED=403
      [ "$STATUS" = "$EXPECTED" ] || fail "$PATHNAME status $STATUS expected $EXPECTED"
      if [ "$PATHNAME" = /assets/missing-smoke.js ]; then
        ! grep -iq 'id="app"' "$TMP/body" || fail 'missing asset returned SPA'
      else
        ! grep -iq '<html\|id="app"' "$TMP/body" || fail "$PATHNAME returned HTML"
      fi
    done
    ASSET=$(find "$TMP/dist/assets" -name '*.js' -printf '%f\n' | sort | head -1)
    curl -kfsS "$HTTPS/assets/$ASSET" -o "$TMP/asset.js"
    cmp "$TMP/dist/assets/$ASSET" "$TMP/asset.js"
    if [ -n "${PANEL_BROWSER_HOOK:-}" ]; then
      PANEL_SMOKE_MODE="$MODE" PANEL_SMOKE_PHASE="$PHASE" PANEL_SMOKE_PORT="$HTTPS_PORT" \
        PANEL_BASE="$HTTPS" PANEL_ADMIN_USER=admin PANEL_ADMIN_PASSWORD="$PASSWORD" \
        bash -c "$PANEL_BROWSER_HOOK" >"$OUT/browser-hook.log" 2>&1 || BROWSER_FAILED=1
    else
      echo 'NOT_RUN: real HTTPS browser verification (PANEL_BROWSER_HOOK unset)' | tee "$OUT/browser-hook.log"
    fi
    docker logs "$APP" >"$OUT/app.log" 2>&1
    docker logs "$WEB" >"$OUT/nginx.log" 2>&1
    docker rm -fv "$WEB" "$APP" >/dev/null
    echo "PASS: $MODE $PHASE image/HTTP checks (browser result recorded separately)"
  done
done
[ "$BROWSER_FAILED" = 0 ] || fail 'one or more real browser checks failed; see per-phase evidence'
echo PANEL_IMAGE_VERIFIED
