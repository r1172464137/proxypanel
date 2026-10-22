#!/usr/bin/env bash
# 前端快速部署：本地构建 dist，直接替换容器里的静态文件。
#
# 适用场景：只改了 web/ 下的前端代码（Vue / CSS / 文案）。
# 不重新构建镜像，不走 GitHub Actions，约 30 秒看到效果。
#
# 原理：面板镜像里的 nginx 用 try_files 直接从 /usr/share/proxypanel/web
# 读文件，每次请求都读磁盘，所以替换目录内容后不需要 reload —— 但旧文件的
# 哈希文件名会残留，先清空再拷。
#
# 注意：这只改动运行中的容器。容器被 `docker compose up --force-recreate`
# 重建后，改动会退回镜像里的版本。正式发布仍要走 git push + CI。
set -euo pipefail

W=/home/compile/Work
REPO="$W/proxypanel"
WEB="$REPO/web"
CONTAINER=${CONTAINER:-proxypanel-panel-1}
DEST=/usr/share/proxypanel/web
S="ssh -i $W/.keys/db_ed25519 -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=$W/.keys/known_hosts root@192.168.5.203"

step() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }

step "1/5 本地构建（typecheck + build）"
cd "$WEB"
export NODE_ENV=development
npm run typecheck
npm run build
echo "  dist: $(du -sh dist | cut -f1), $(find dist -type f | wc -l) 个文件"

step "2/5 打包上传"
TAR=/tmp/panel-dist-$$.tar.gz
trap 'rm -f "$TAR"' EXIT
tar -C "$WEB/dist" -czf "$TAR" .
echo "  archive: $(du -h "$TAR" | cut -f1)"
scp -q -i "$W/.keys/db_ed25519" -o IdentitiesOnly=yes -o BatchMode=yes \
  -o StrictHostKeyChecking=yes -o UserKnownHostsFile="$W/.keys/known_hosts" \
  "$TAR" root@192.168.5.203:/tmp/panel-dist.tar.gz

step "3/5 校验容器在运行"
$S "docker inspect $CONTAINER --format '{{.State.Status}}/{{.State.Health.Status}}'" | sed 's/^/  /'

step "4/5 替换容器内静态文件"
# 先清空再解包：旧构建的 index-<oldhash>.js 留着不会被引用，但会越积越多。
$S "docker exec $CONTAINER sh -c 'rm -rf $DEST/* && mkdir -p $DEST'"
$S "docker cp /tmp/panel-dist.tar.gz $CONTAINER:/tmp/panel-dist.tar.gz"
$S "docker exec $CONTAINER sh -c 'tar -xzf /tmp/panel-dist.tar.gz -C $DEST && rm -f /tmp/panel-dist.tar.gz'"
$S "rm -f /tmp/panel-dist.tar.gz"
echo "  容器内文件数: $($S "docker exec $CONTAINER sh -c 'find $DEST -type f | wc -l'")"

step "5/5 验证（从 203 宿主访问，等价于浏览器的路径）"
# 容器内只有 wget，没有 curl；而且真正要验的是"用户能不能打开页面"，
# 所以从宿主经发布端口访问，而不是在容器里自测。
probe() { $S "curl -sk -o /dev/null -w '%{http_code}' 'https://192.168.5.203:8443$1'"; }
HEALTH=$(probe /healthz)
INDEX=$(probe /)
echo "  GET /healthz = $HEALTH"
echo "  GET /        = $INDEX"
ASSET=$($S "curl -sk https://192.168.5.203:8443/ | grep -oE '/assets/index-[A-Za-z0-9_-]+\.js' | head -1" | tr -d '\r')
if [ -n "$ASSET" ]; then
  ACODE=$(probe "$ASSET")
  echo "  GET $ASSET = $ACODE"
  if [ "$ACODE" != "200" ]; then
    echo "  !! 入口脚本无法加载" >&2
    exit 1
  fi
fi
if [ "$HEALTH" != "200" ] || [ "$INDEX" != "200" ]; then
  echo "  !! 部署后健康检查异常" >&2
  exit 1
fi
echo
echo "完成。浏览器刷新即可（静态文件已换，无需 reload nginx）。"
echo "访问: https://192.168.5.203:8443"
