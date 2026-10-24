# 快速开始

这篇通过宿主 nginx 的 HTTPS 入口启动面板、接上节点，并指导你打一次真实流量。
单 Go 进程镜像的两种托管模式已完成隔离 HTTPS 与真实浏览器烟测，见[验证记录](../development/deployment-smoke.md)。你的宿主 nginx、证书和真实节点仍需按下面步骤验证。完整参数见 [Docker 部署](../deployment/docker.md)。

## 前置条件

| 需要 | 说明 |
|---|---|
| Docker Engine 24+ / Compose v2 | `docker compose version` 有输出 |
| 2 vCPU / 2 GiB 内存 / 10 GiB 磁盘 | 面板、PostgreSQL、Redis、Agent 与一个 Xray 实例同机的起点，不是容量保证 |
| 已解析到宿主机的域名、受信任的 TLS 证书、宿主 nginx | 证书须覆盖域名；浏览器与 Agent 都需信任它 |
| 官方 Xray 二进制 | 接 Agent 与验证真实流量时需要，下载与校验见下文 |

证书申请、DNS 和防火墙需先按自己的环境准备好；本文不会自动配置它们。
私有 CA / 自签证书必须显式加入浏览器和 Agent 信任，不以关闭校验代替。
下面面板管理命令在仓库根目录执行，节点准备命令在节点机执行，需要 root 权限的路径请使用相应权限。

## HTTPS 部署路径

```text
浏览器 / Agent → https://你的域名:443 → 宿主 nginx
              → http://127.0.0.1:8081（宿主回环）→ 容器 0.0.0.0:8080
```

镜像无 nginx 或入口脚本，单 Go 进程默认从 `/usr/share/proxypanel/web` 托管 SPA。
这里不提供 Docker DEV 试用方式：`PANEL_DEV=true` 强制回环监听，与普通 bridge 发布
不兼容；不要通过公开明文端口绕过。本地源码调试见 [开发环境](../development/setup.md)。

### 步骤 1 · 密钥与配置

```bash
git clone https://github.com/r1172464137/proxypanel.git
cd proxypanel
PANEL_HOST=panel.example.com       # 改成已解析、证书覆盖的域名
cp deploy/docker-compose.bundle.yml docker-compose.yml

# 仅首次部署生成；已有数据库的密钥与密码不能重新生成覆盖
PG_PASSWORD="$(openssl rand -hex 32)"
REDIS_PASSWORD="$(openssl rand -hex 32)"
PANEL_MASTER_KEY="$(openssl rand -base64 32)"
ADMIN_PASSWORD="$(openssl rand -base64 12)"

umask 077
cat > .env <<EOF
PG_PASSWORD=$PG_PASSWORD
REDIS_PASSWORD=$REDIS_PASSWORD
PANEL_MASTER_KEY=$PANEL_MASTER_KEY
PANEL_PUBLIC_URL=https://$PANEL_HOST
PANEL_BIND_IP=127.0.0.1
PANEL_PORT=8081
PANEL_ADMIN_USER=admin
PANEL_ADMIN_PASSWORD=$ADMIN_PASSWORD
EOF
chmod 600 .env
echo "一次性管理员密码：$ADMIN_PASSWORD"
```

这里**不启用 DEV**，也**不把证书挂进面板容器**：TLS 由宿主 nginx 终止。
`.env` 未定义 `PANEL_WEB_ROOT` 时保持默认 SPA 托管；显式写 `PANEL_WEB_ROOT=`
才是 API-only，需要宿主另外托管 dist。变量必须通过 compose 映射进容器，
详见 [静态托管切换](../deployment/docker.md#方案-bapi-only宿主-nginx-托管-dist)。

启动前确认 `panel.image` 指向包含本次改动的镜像，**不要假定 `latest` 已更新**。
尚无对应发布标签时，按 [镜像与构建](../deployment/docker.md#镜像与构建) 使用仓库
现有 Dockerfile 自建，并修改复制出的 compose 的镜像名。无需额外 Dockerfile。

**`PANEL_MASTER_KEY` 必须固定保存**：它加密数据库里的代理凭据，丢失或换值会让原
密码/UUID 无法解密，只能全部轮换。账号密码哈希不受影响。将密钥和数据库备份
分开安全保存，但确保恢复时能配对取得；不要把 `.env` 提交到仓库。

### 步骤 2 · 起容器

```bash
docker compose up -d
docker compose ps
docker compose logs -f panel
```

检查日志是否选择了反代终止 TLS、启用了 SPA，并最终监听 `0.0.0.0:8080`。
非回环监听的 warning 在此拓扑下是预期提醒，不是启动失败；仍须保持宿主只发布
回环端口，限制容器网络中的直连方，不能只依赖 XFF 来源校验。

在宿主验证（这是待执行命令，不是本次实测输出）：

```bash
curl -fsS http://127.0.0.1:8081/healthz; echo
curl -fsS http://127.0.0.1:8081/readyz; echo
```

`/healthz` 仅代表进程活着。`/readyz` 的 PostgreSQL 故障返回 503；Redis 故障可能
仍为 200，但 `data.status=degraded`。需读取响应体，不只看状态码。

### 步骤 3 · 配宿主 nginx

完整起点见 [nginx 模板](../../deploy/nginx.conf.example)，替换域名、证书路径，
根据 nginx 版本调整 HTTP/2 语法。下面是 Go 托管 SPA 的同源示例；请作为一个站点
配置安装到宿主 nginx 的 `http` 配置上下文中，不要叠加重复 upstream/server：

```nginx
upstream proxypanel_api {
    server 127.0.0.1:8081;
    keepalive 16;
}

server {
    listen 443 ssl;
    server_name panel.example.com;
    ssl_certificate     /etc/letsencrypt/live/panel.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/panel.example.com/privkey.pem;

    client_max_body_size 4m;
    proxy_set_header Host              $host;
    proxy_set_header X-Forwarded-For   $remote_addr;  # 覆盖，不透传客户端值
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_http_version 1.1;
    proxy_set_header Connection "";

    location / {
        proxy_pass http://proxypanel_api;
        proxy_read_timeout 60s;
    }
    location /sub/ {
        proxy_pass http://proxypanel_api;
        access_log off;
        add_header Cache-Control "no-store" always;
    }
}

server {
    listen 80;
    server_name panel.example.com;
    return 301 https://$host$request_uri;
}
```

证书已存在且路径正确后，再启用配置并检查：

```bash
sudo nginx -t && sudo systemctl reload nginx
curl -fsS https://panel.example.com/healthz; echo
curl -fsS https://panel.example.com/readyz; echo
```

私有 CA 使用 `curl --cacert /path/to/your-ca.crt ...`，不使用 `-k` 作为验收依据。

### 步骤 4 · 登录

浏览器打开 `https://panel.example.com`（替换为实际域名），用户名 `admin`，
密码是步骤 1 打印的那串。检查 SPA 子路由刷新、缺失资源 404 和登录后的会话请求。

管理员引导只在 **members 表为空**时生效，密码至少 10 位。
登录后先到 **我的账户 → 账号安全** 改掉密码，然后删除 `.env` 中的引导密码并重建：

```bash
sed -i '/^PANEL_ADMIN_/d' .env
docker compose up -d panel
```

不删除不会重复创建管理员，但不应继续在配置里保留首次密码。

## 接下来 · 建到能出订阅

最短路径是「服务器 → 节点 → 节点组 → 订阅 → 成员 → Agent」。

### 1. 建服务器

左侧 **资源 → 服务器 → 新建服务器**，填节点机名称、地址、地区，保存。

### 2. 建节点

**资源 → 节点 → 新建节点**，选择 **Shadowsocks 2022** 模板（节点代理本身不需要
域名或证书，但面板入口仍必须 HTTPS），核对：

- **所属服务器**：上一步的节点机。
- **对外地址**：成员实际连接的节点 IP 或域名；只有同机且直达节点时才能复用面板域名，不能误填 CDN 或另一台面板机域名。
- **监听端口**：模板默认 `8388`，需在节点机发布并允许客户端访问。
- **对外端口**：留空即等于监听端口；若另有中转，转发由你自己配置。
- **监听地址**：`0.0.0.0`；勾选启用后保存。

节点技术配置变化会重启该节点核心，已有连接会短暂中断。

### 3. 建节点组

**资源 → 节点组 → 新建节点组**，命名并勾选该节点，保存。
组是订阅授权单位；已有成员时修改组成员需要影响预览。

### 4. 建订阅

**资源 → 订阅 → 新建订阅**，选择节点组、配额、周期起始日、时区，保存。
默认有效期可留空，绑定成员时显式填到期时间。

### 5. 建成员

**成员 → 新建成员**，填用户名、至少 10 位的初始密码，角色选成员，绑定刚建的订阅，
设置到期时间后保存。在成员行的 **订阅链接** 中取得该成员的 URL。

### 6. 接上 Agent

Agent 在节点机上运行，主动通过面板 HTTPS 入口拉取快照。
在 **资源 → 服务器 → 一键安装 Agent** 中生成命令，只能用一次，15 分钟内有效。优先直接在目标节点机执行页面给出的命令：

```bash
curl --proto '=https' --tlsv1.2 -fsSL 'https://panel.example.com/install-agent.sh' \
  -o /tmp/proxypanel-agent-install.sh \
  && sudo bash /tmp/proxypanel-agent-install.sh -key '页面生成的一次性注册码'
```

脚本自动检测 amd64/arm64，从当前 Panel 下载同版 Agent 并核对 SHA256，再安装官方 Xray、配置和 systemd。执行后回到页面等待“Agent 在线”；命令成功不等于代理流量已验证，仍需完成下文真实客户端测试。

私有 CA 尚未进入系统信任、裸进程 Panel 未配置同版 Agent 文件，或需要手工控制安装目录时，使用下面的手工路径。

在节点机下载官方 Xray 并核对摘要（示例 amd64，arm64 替换为 `Xray-linux-arm64-v8a.zip`）：

```bash
mkdir -p /opt/proxypanel/xray /var/lib/proxypanel-agent /etc/proxypanel
cd /tmp
curl -fsSLo xray.zip https://github.com/XTLS/Xray-core/releases/download/v26.3.27/Xray-linux-64.zip
curl -fsSLo xray.zip.dgst https://github.com/XTLS/Xray-core/releases/download/v26.3.27/Xray-linux-64.zip.dgst
expected="$(sed -n 's/.*SHA2-256=[[:space:]]*\([0-9a-fA-F]\{64\}\).*/\1/p' xray.zip.dgst | head -n1)"
test -n "$expected" && echo "$expected  xray.zip" | sha256sum -c -
# 只在校验成功后继续
unzip -q -o xray.zip -d /opt/proxypanel/xray/26.3.27
/opt/proxypanel/xray/26.3.27/xray version | head -1
```

写 Agent 配置，路径按**容器内**填写，替换域名与注册码：

```bash
cat > /etc/proxypanel/agent.json <<'EOF'
{
  "panel_url": "https://REPLACE_WITH_PANEL_HOST",
  "registration_code": "REPLACE_WITH_ONE_TIME_CODE",
  "xray_path": "/opt/xray/26.3.27/xray",
  "state_dir": "/var/lib/proxypanel-agent",
  "poll_interval_seconds": 5,
  "stats_interval_seconds": 5,
  "heartbeat_interval_seconds": 15,
  "allow_loopback_http": false
}
EOF
chown root:65532 /etc/proxypanel/agent.json
chmod 640 /etc/proxypanel/agent.json           # 容器 uid/gid 65532 必须可读
chown 65532:65532 /var/lib/proxypanel-agent
chmod 700 /var/lib/proxypanel-agent

docker run -d --name proxypanel-agent --restart unless-stopped \
  -v /etc/proxypanel/agent.json:/etc/proxypanel/agent.json:ro \
  -v /var/lib/proxypanel-agent:/var/lib/proxypanel-agent \
  -v /opt/proxypanel/xray:/opt/xray:ro \
  -p 8388:8388 \
  ghcr.io/r1172464137/proxypanel-agent:latest

docker logs -f proxypanel-agent
```

公共 CA 通常无需额外配置。私有 CA 或自签证书则需先将信任根放到节点机
`/etc/proxypanel/panel-ca.crt`，确保容器用户可读，再在上述 `docker run` 添加：

```text
-e SSL_CERT_FILE=/usr/local/share/ca-certificates/panel-ca.crt
-v /etc/proxypanel/panel-ca.crt:/usr/local/share/ca-certificates/panel-ca.crt:ro
```

仅复制证书到宿主不会让容器信任它。自签叶证书可以作为该连接的信任根。
仓库 `images` workflow 已更新构建与冒烟逻辑，但这不代表本次任务已执行或通过它；
不能据配置存在推断镜像已发布或验证。
私有包需先登录 GHCR；Agent 也可使用现有 `deploy/Dockerfile.agent` 自建，需准备 `bin/<arch>/agent`。

注册成功后可删除 `registration_code`，token 已持久化在状态目录。
**不要删除状态目录**：它还保存计量基线与未上传用量。
回到界面确认 Agent 在线、实例 running、`已应用` 追上 `期望`，任务与同步无失败。
这些仍不等于代理真实可用。

## 确认真的通了

在装有官方 Xray 的测试客户端上，以成员订阅生成客户端配置；它自带
`127.0.0.1:10808` SOCKS 入站。测试配置包含凭据，限制文件权限：

```bash
umask 077
SUB_URL='https://REPLACE_WITH_PANEL_HOST/sub/REPLACE_WITH_MEMBER_TOKEN'
curl -fsS "$SUB_URL?format=xray" -o /tmp/pp-client.json
# 私有 CA 时给 curl 加 --cacert /path/to/your-ca.crt
/opt/proxypanel/xray/26.3.27/xray run -c /tmp/pp-client.json &
CLIENT_PID=$!
sleep 2
curl -fsS --socks5-hostname 127.0.0.1:10808 https://www.cloudflare.com/cdn-cgi/trace | grep '^ip='
kill "$CLIENT_PID"
rm -f /tmp/pp-client.json
```

确认出口 IP 为节点出口，再看 **运行 → 用量** 是否出现该成员增量。
这是要求部署方执行的验证步骤，不是本次新镜像的已通过声明。

## 常见卡点

- **容器立即退出**：看 `docker compose logs panel`。检查 master key、数据库连接、管理员密码长度和 `PANEL_WEB_ROOT` 目录内的 `index.html`；不要用另一个只复制二进制的 Dockerfile 代替现有多阶段构建。
- **DEV requires loopback**：误启用了 DEV。删除该配置，保持镜像监听 `0.0.0.0:8080`，由宿主 nginx 提供 HTTPS。
- **拒绝生产明文**：检查是否使用当前镜像及 `PANEL_TRUST_REVERSE_PROXY=true`，不要改开 DEV。
- **宿主 nginx 502**：检查 Go 是否启动、发布端口是否为回环 8081、upstream 是否为 HTTP 而非 HTTPS。
- **能开页面但登录后仍未登录**：用浏览器核对 `Set-Cookie` 和后续请求 Cookie；正式入口必须 HTTPS。分域还需核对 Cookie 作用范围、CORS 与 credentials，同源默认无需额外设置。
- **订阅 URL 错误**：`PANEL_PUBLIC_URL` 只控制链接拼接，不控制 Cookie 或 CSRF；必须填实际 HTTPS 对外入口。
- **Agent 在线但代理不通**：查实例结果、修订收敛、节点地址及端口发布。面板只发布回环 8081，代理 8388 在 Agent 容器上另行发布。

长期维护见 [Docker 部署](../deployment/docker.md)、[生产部署](../deployment/production.md)
与 [分离部署](../deployment/split-panel-agent.md)。
