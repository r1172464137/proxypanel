#!/usr/bin/env bash
# Isolated smoke test of a built panel image, never modifies the live stack.
# Usage: bash scripts/verify-panel-image.sh <panel-image>
# Requires Docker, Python 3, curl, openssl; pulls nginx:alpine if missing.
set -euo pipefail
IMAGE=${1:?usage: verify-panel-image.sh IMAGE}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
PREFIX="pp-web-smoke-$(date +%s)-$$"
TMP=$(mktemp -d)
DB="$PREFIX-db"; APP="$PREFIX-app"; WEB="$PREFIX-web"; EXTRACT="$PREFIX-extract"
cleanup() {
  docker rm -fv "$WEB" "$APP" "$DB" "$EXTRACT" >/dev/null 2>&1 || true
  docker network rm "$PREFIX" >/dev/null 2>&1 || true
  rm -rf "$TMP"
}
trap cleanup EXIT
fail() { echo "FAIL: $*" >&2; docker logs "$APP" 2>&1 | tail -20 || true; exit 1; }
code() { curl -ksS --max-time 10 -o /dev/null -w '%{http_code}' "$1"; }
wait_http() {
  for i in $(seq 1 60); do
    if [ "$(code "$1" 2>/dev/null || true)" = 200 ]; then return; fi
    sleep 1
  done
  fail "timeout waiting for $1"
}

docker run --rm --entrypoint /bin/sh "$IMAGE" -ec '
  test "$(id -u)" = 65532
  ! command -v nginx
  test -f /usr/share/proxypanel/web/index.html
  test -d /app/migrations
'
# Export exactly the frontend bundled in the image for mode B.
docker create --name "$EXTRACT" "$IMAGE" >/dev/null
docker cp "$EXTRACT:/usr/share/proxypanel/web" "$TMP/dist"
docker rm "$EXTRACT" >/dev/null
chmod -R a+rX "$TMP/dist"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=panel.example.com \
  -keyout "$TMP/key.pem" -out "$TMP/cert.pem" >/dev/null 2>&1

docker network create "$PREFIX" >/dev/null
docker run -d --name "$DB" --network "$PREFIX" \
  -e POSTGRES_PASSWORD=smoke-password postgres:17-alpine >/dev/null
for i in $(seq 1 60); do
  if docker exec "$DB" pg_isready -U postgres >/dev/null 2>&1; then break; fi
  sleep 1
done
KEY=$(openssl rand -base64 32)
for MODE in spa api; do
  WEBROOT=/usr/share/proxypanel/web
  [ "$MODE" = api ] && WEBROOT=''
  docker run -d --name "$APP" --network "$PREFIX" -p 127.0.0.1::8080 \
    -e "PANEL_MASTER_KEY=$KEY" -e "PANEL_WEB_ROOT=$WEBROOT" \
    -e "DATABASE_URL=postgres://postgres:smoke-password@$DB:5432/postgres?sslmode=disable" \
    -e PANEL_PUBLIC_URL=https://panel.example.com \
    -e PANEL_ADMIN_USER=admin -e PANEL_ADMIN_PASSWORD=smoke-password-123 "$IMAGE" >/dev/null
  PORT=$(docker port "$APP" 8080/tcp | awk -F: '{print $NF}')
  API="http://127.0.0.1:$PORT"
  wait_http "$API/healthz"
  curl -fsS "$API/readyz" >/dev/null
  [ "$(code "$API/assets/missing.js")" = 404 ] || fail 'missing assets returned HTML'
  [ "$(code "$API/api/unknown")" = 404 ] || fail 'API swallowed by SPA'
  if [ "$MODE" = spa ]; then
    curl -fsS "$API/admin/nodes" | grep -q 'id="app"' || fail 'SPA route missing'
    curl -fsSI "$API/index.html" | grep -iq 'cache-control: no-store' || fail 'index is cacheable'
  else
    [ "$(code "$API/")" = 404 ] || fail 'API-only still serves HTML'
  fi
  # Validate the actual host-nginx template in an isolated nginx container.
  # Only upstream and filesystem paths change, not the location logic.
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
    active=s[start:end].replace('    # ', '    ')
    s=s[:start]+active+s[end:]
open(dest,'w').write(s)
PY
  docker run --rm --network "$PREFIX" \
    -v "$TMP/default.conf:/etc/nginx/conf.d/default.conf:ro" \
    -v "$TMP:/certs:ro" -v "$TMP/dist:/var/www/proxypanel:ro" nginx:alpine nginx -t
  docker run -d --name "$WEB" --network "$PREFIX" -p 127.0.0.1::443 \
    -v "$TMP/default.conf:/etc/nginx/conf.d/default.conf:ro" \
    -v "$TMP:/certs:ro" -v "$TMP/dist:/var/www/proxypanel:ro" nginx:alpine >/dev/null
  PORT=$(docker port "$WEB" 443/tcp | awk -F: '{print $NF}')
  HTTPS="https://127.0.0.1:$PORT"
  wait_http "$HTTPS/healthz"
  curl -kfsS "$HTTPS/admin/nodes" | grep -q 'id="app"' || fail 'nginx SPA route failed'
  [ "$(code "$HTTPS/assets/missing.js")" = 404 ] || fail 'nginx missing asset returned HTML'
  [ "$(code "$HTTPS/api/unknown")" = 404 ] || fail 'nginx API routing failed'
  curl -kfsSI "$HTTPS/index.html" | grep -iq 'cache-control: no-store' || fail 'nginx index cache'
  ASSET=$(find "$TMP/dist/assets" -name '*.js' -printf '%f\n' | head -1)
  test -n "$ASSET"
  curl -kfsS "$HTTPS/assets/$ASSET" -o "$TMP/asset.js"
  cmp "$TMP/dist/assets/$ASSET" "$TMP/asset.js"
  # Real HTTPS login, Secure cookie replay and authenticated request.
  curl -kfsS -D "$TMP/headers" -c "$TMP/cookies" -H 'Content-Type: application/json' \
    -d '{"username":"admin","password":"smoke-password-123"}' \
    "$HTTPS/api/v1/auth/login" -o "$TMP/login.json"
  grep -iq 'set-cookie:.*Secure' "$TMP/headers" || fail 'missing Secure session cookie'
  curl -kfsS -b "$TMP/cookies" "$HTTPS/api/v1/auth/session" >"$TMP/session.json"
  python3 - "$TMP/session.json" <<'PY'
import json,sys
assert json.load(open(sys.argv[1]))['data']['user']['username']=='admin'
PY
  echo "PASS: $MODE — non-root, no nginx, real PostgreSQL, SPA/assets/404/cache, HTTPS login/session"
  # Optional real-browser hook while the disposable HTTPS fixture is still up.
  # The hook receives mode, loopback HTTPS port and test-only credentials.
  if [ -n "${PANEL_BROWSER_HOOK:-}" ]; then
    PANEL_SMOKE_MODE="$MODE" PANEL_SMOKE_PORT="$PORT" \
      PANEL_BASE="$HTTPS" PANEL_ADMIN_PASSWORD=smoke-password-123 \
      bash -c "$PANEL_BROWSER_HOOK"
  fi
  docker rm -fv "$WEB" "$APP" >/dev/null
done
echo 'PANEL_IMAGE_VERIFIED'
