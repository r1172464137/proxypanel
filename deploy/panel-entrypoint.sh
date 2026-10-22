#!/bin/sh
# 容器入口：面板跑在回环，nginx 对外。
#
# 两个进程的权限不同：nginx 需要 root 才能读 /certs 里的私钥（挂载属主通常
# 是宿主 uid，不一定是容器内的某个用户），而面板必须以非特权用户运行。
# 这里用 su-exec 降权启动面板，nginx 以 root master + nobody worker 运行。
set -eu

# nginx 是容器里唯一读证书的进程；面板走明文回环，由 nginx 终止 TLS。
CERT=${NGINX_TLS_CERT:-/certs/fullchain.pem}
KEY=${NGINX_TLS_KEY:-/certs/privkey.pem}

# 证书缺失时给出明确诊断，而不是让 nginx 起来后每个请求都 500。
if [ ! -r "$CERT" ]; then
  echo "entrypoint: cannot read TLS certificate at $CERT" >&2
  echo "entrypoint: mount the certificate directory at /certs (fullchain.pem + privkey.pem)" >&2
  exit 1
fi
if [ ! -r "$KEY" ]; then
  echo "entrypoint: cannot read TLS private key at $KEY" >&2
  echo "entrypoint: the key must be readable by uid 0 (nginx master) - check its mode" >&2
  exit 1
fi

# nginx 自己也会在 master 阶段再读一次；提前验证能省掉一个不清晰的报错。
nginx -t 2>&1 || {
  echo "entrypoint: nginx configuration test failed" >&2
  exit 1
}

# 面板在同一个命名空间内监听回环，nginx 反代到它。
run_as_panel() {
  if command -v su-exec >/dev/null 2>&1; then
    su-exec 65532:65532 "$@"
    return $?
  fi
  "$@"
}

echo "entrypoint: starting panel on 127.0.0.1:8080 as uid 65532"
run_as_panel /app/panel &
PANEL_PID=$!

# 先确认面板真的活着再起 nginx。否则面板因配置错误（数据库连不上、主密钥
# 格式不对、管理员密码不足 10 位等）立刻退出时，nginx 已经起来对外提供 502，
# 监控看到的是"容器在跑但全站 502"，比直接失败更难诊断。
READY=0
TRIES=0
while [ "$TRIES" -lt 30 ]; do
  TRIES=$((TRIES + 1))
  if ! kill -0 "$PANEL_PID" 2>/dev/null; then
    echo "entrypoint: panel exited during startup; not starting nginx" >&2
    wait "$PANEL_PID" 2>/dev/null
    exit 1
  fi
  # /healthz 由面板自身提供，能应答就说明它已经绑好端口。
  if wget -q -O- --no-check-certificate http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
    READY=1
    break
  fi
  sleep 1
done
if [ "$READY" -ne 1 ]; then
  echo "entrypoint: panel did not answer /healthz within 30s; not starting nginx" >&2
  kill "$PANEL_PID" 2>/dev/null || true
  wait "$PANEL_PID" 2>/dev/null || true
  exit 1
fi
echo "entrypoint: panel is answering /healthz"

echo "entrypoint: starting nginx on 0.0.0.0:8443"
nginx -g 'daemon off;' &
NGINX_PID=$!

# 任一进程退出就整体退出，让编排层重建容器，避免"半死"状态。
while kill -0 "$PANEL_PID" 2>/dev/null && kill -0 "$NGINX_PID" 2>/dev/null; do
  sleep 2
done

echo "entrypoint: a process exited; shutting down" >&2
kill "$PANEL_PID" "$NGINX_PID" 2>/dev/null || true
wait 2>/dev/null || true
exit 1
