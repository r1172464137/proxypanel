# Docker 部署

当前 `deploy/Dockerfile.panel` **不含 nginx 或入口脚本**，只启动 `/app/panel` 一个 Go 进程。
默认同时提供 API 与 SPA，TLS 由宿主机 nginx 终止：

```text
浏览器 / Agent ──HTTPS:443──▶ 宿主 nginx
                              └──HTTP──▶ 宿主 127.0.0.1:8081 ──端口发布──▶ 容器 0.0.0.0:8080
```

容器监听所有接口是为了接受 Docker bridge 转发，不等于允许公网直连。
宿主发布地址必须保持回环，容器网络不要接入不可信服务。
正式会话 Cookie 带 `Secure`，浏览器入口必须使用 HTTPS。

**不要用 `PANEL_DEV=true` 做 Docker 本机试用。** main 在 DEV 模式下强制回环监听，
镜像的 `0.0.0.0:8080` 会被拒绝；改为容器回环又无法通过 bridge 端口发布访问。
源码本机开发另见 [开发环境](../development/setup.md)。

## 两种编排

以 YAML 的实际服务定义为准：

| | `deploy/docker-compose.bundle.yml` | `deploy/docker-compose.example.yml` |
|---|---|---|
| 服务 | PostgreSQL + Redis + 面板 | PostgreSQL + Redis + 面板；Agent 在 `node` profile 中，默认不启动 |
| 面板镜像 | `ghcr.io/r1172464137/proxypanel-panel:${PANEL_TAG:-latest}` | 同左 |
| 面板发布 | `${PANEL_BIND_IP:-127.0.0.1}:${PANEL_PORT:-8081}:8080` | `127.0.0.1:8081:8080` |
| PostgreSQL / Redis | 不发布宿主端口；Redis 开 AOF | 发布到宿主回环 5432 / 6379；Redis 未启用 AOF |
| 前端 | Go 默认托管 SPA，或显式设空 `PANEL_WEB_ROOT` | 同左 |
| TLS | 宿主 nginx | 宿主 nginx |

example **不是外部数据库版**。改用外部实例需要自行修改 DSN、移除不再使用的服务及
`depends_on`，跨机数据库链路也要单独配置安全传输。example 启用 Agent 前需要准备
配置、核心目录、私有 CA 挂载和代理端口；核对好后用 `docker compose --profile node up -d`，
或只启用已配置好的 Agent 服务。

首次部署推荐 bundle，操作见 [HTTPS 快速开始](../getting-started/quickstart.md)。
将 compose 复制到仓库根目录的 `docker-compose.yml`，在同目录管理 `.env` 并执行
`docker compose ...`，避免混淆环境文件及相对路径。已有部署不要随意更换项目名或目录，
以免连接到另一组数据卷。

## 镜像与构建

当前架构已完成 amd64 本地镜像构建和隔离运行验证，见[部署烟测](../development/deployment-smoke.md)。这不是 GHCR 已发布状态的证明。
不要假定现有 `latest` 或旧标签已经移除了 nginx；选择包含这些改动的明确标签/摘要，
并在上线前核对镜像环境变量、入口与实际行为。

当前 CI 已改为新架构的构建和 SPA/API-only 冒烟逻辑，但**配置已更新不等于执行已通过**，
也不能据旧 CI 验收推断新镜像已发布。若自行构建，使用仓库现有的
[`deploy/Dockerfile.panel`](../../deploy/Dockerfile.panel)，**不需要额外 Dockerfile**。
例如在仓库根目录为 amd64 构建（需要 Go、Docker；arm64 相应替换架构）：

```bash
mkdir -p bin/amd64 bin/arm64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
  -o bin/amd64/panel ./cmd/panel
# Panel 镜像同时携带两个架构的同版 Agent，供页面生成一键安装命令。
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -buildvcs=false \
    -o "bin/$arch/agent" ./cmd/agent
done
docker build --build-arg TARGETARCH=amd64 -f deploy/Dockerfile.panel \
  -t proxypanel-panel:local .
```

构建上下文必须有 `bin/<arch>/panel`、`bin/{amd64,arm64}/agent`、`migrations/`、`deploy/deploy-agent.sh` 和前端**源码** `web/`；Dockerfile
的 Node 阶段自行执行 `npm ci` 与 `npm run build`，不依赖宿主预先生成 `web/dist`。
为避免上下文过大，不要携带不需要的 `web/node_modules`、历史构建产物或秘密文件。
在复制出的 compose 中把 `panel.image` 改成 `proxypanel-panel:local` 后再启动；
`PANEL_TAG` 只替换 bundle 的远程镜像标签，不能把它变成本地镜像名。
本地镜像不要执行下文的 `docker compose pull panel`，升级时重新构建即可。

## 一键安装 Agent

当前 Panel 镜像携带同一提交构建的 amd64/arm64 Agent 和安装脚本。配置了 HTTPS `PANEL_PUBLIC_URL` 后，服务器页面会生成：

```bash
curl --proto '=https' --tlsv1.2 -fsSL 'https://panel.example.com/install-agent.sh' \
  -o /tmp/proxypanel-agent-install.sh \
  && sudo bash /tmp/proxypanel-agent-install.sh -key '一次性注册码'
```

脚本自动检测架构、从当前 Panel 下载同版 Agent、核对启动时固定的 SHA256，再下载并核对官方 Xray、写配置和安装 systemd。注册码绑定服务器、15 分钟有效且只能消费一次；永久 Agent token 不进入浏览器。`/install-agent.sh` 与 `/install-agent/` 必须由 nginx 反代给 Panel，模板已包含对应 location。

裸进程部署可通过 `PANEL_AGENT_INSTALLER_PATH`、`PANEL_AGENT_BINARY_AMD64`、`PANEL_AGENT_BINARY_ARM64` 指定同版文件；路径缺失、文件不是普通文件或 `PANEL_PUBLIC_URL` 不是 HTTPS 时，一键安装会禁用而不是回退到未校验下载。

## Agent 镜像

`ghcr.io/r1172464137/proxypanel-agent` 是节点端镜像，**内置官方 Xray**（构建时从官方 Release 下载并用官方 `.dgst` 的 SHA2-256 校验），无需在节点机安装或挂载核心。

```bash
docker run -d --restart unless-stopped \
  --name proxypanel-agent \
  -e PANEL_URL='https://panel.example.com' \
  -e REGISTRATION_CODE='一次性注册码' \
  -v proxypanel-agent-state:/var/lib/proxypanel-agent \
  -p 20101:20101 \
  ghcr.io/r1172464137/proxypanel-agent:latest
```

配置优先级：`-config` 指定的 JSON 文件，或未指定时使用环境变量（`PANEL_URL`、`REGISTRATION_CODE`、`XRAY_PATH`、`STATE_DIR`、三个 interval、`ALLOW_LOOPBACK_HTTP`）。两种方式走同一套 `Config.Validate`，环境变量不会放宽校验。

镜像路径：`/app/agent`、`/opt/xray/xray`（含 `geoip.dat`/`geosite.dat`）。核心版本为 `26.3.27`，必须在 Agent 白名单内。状态目录 `/var/lib/proxypanel-agent` 必须持久化：内含身份、计量基线、未上传队列。

首次注册成功后可从启动参数删除 `REGISTRATION_CODE`；token 已保存在状态卷。私有 CA 用 `-v ca.crt:...:ro -e SSL_CERT_FILE=...` 挂载，不要关闭 TLS 校验。

## 宿主机 nginx 与前端归属

参考 [`deploy/nginx.conf.example`](../../deploy/nginx.conf.example)，替换域名与证书路径，
确认 upstream 为宿主 `127.0.0.1:8081`，根据 nginx 版本调整 HTTP/2 语法，再执行
`nginx -t` 和 reload。模板不是生产验收记录，不可未经检查直接上线。

### 方案 A：Go 托管 SPA（默认）

镜像默认 `PANEL_WEB_ROOT=/usr/share/proxypanel/web`，Go 从该目录提供 SPA，
宿主 nginx 对 `/` 全量反代到 `http://127.0.0.1:8081` 即可。前后端随同一镜像发布，
宿主机不用保存 dist。Go 对 SPA 路由返回 `index.html` 并设 `Cache-Control: no-store`；
缺失的带扩展名资源返回 404，保留 `/api/`、`/sub/`、`/healthz`、`/readyz` 的路由语义。

### 方案 B：API-only，宿主 nginx 托管 dist

Compose 的环境映射采用：

```yaml
PANEL_WEB_ROOT: "${PANEL_WEB_ROOT-/usr/share/proxypanel/web}"
```

这里是**单连字符**，不是 `:-`：变量未定义时使用镜像目录，显式空值则关闭 Go 静态托管。
在 `.env` 中写：

```dotenv
PANEL_WEB_ROOT=
```

`.env` 只是 Compose 插值来源，不会自动把任意变量传进容器；复制出的旧 compose
若尚未映射该变量，需要先在 `panel.environment` 添加上述一行，或直接设 `PANEL_WEB_ROOT: ""`。
改 `.env` 后需 `docker compose up -d panel` 重建容器环境，单纯 restart 不够。

在源码根目录构建并同步前端到宿主：

```bash
(cd web && npm ci && npm run build)
sudo mkdir -p /var/www/proxypanel
sudo rsync -a --delete web/dist/ /var/www/proxypanel/
```

也可以不安装 Node，直接从同版面板镜像导出前端：

```bash
cid=$(docker create proxypanel-panel:local)
mkdir -p web-export
docker cp "$cid:/usr/share/proxypanel/web/." web-export/
docker rm "$cid"
sudo mkdir -p /var/www/proxypanel
sudo rsync -a --delete web-export/ /var/www/proxypanel/
```

这里只删除 `docker create` 创建的临时未启动容器，不会停止面板服务。

按 nginx 模板的 B 段**替换** A 的 `location /`，设置 `root /var/www/proxypanel`，
保留模板中 A/B 共用的 `/api/`、`/sub/`、`/healthz`、`/readyz` 反代 location，
不要重复定义相同 location。
`/assets/` 缺失文件必须返回 404，SPA 路由回落到 `index.html`；HTML 不缓存，
只有带内容哈希的资源可长期缓存。dist 必须与 API 版本匹配。

两种方案都推荐让前端和 API 保持同一 HTTPS origin（协议、主机、端口一致），
无需 CORS，使用默认会话策略。`SameSite` 判断的是 site，不是 origin；不能简单地说
所有跨 origin 请求都会被 Strict Cookie 拒绝。真跨域的实现边界见
[分离部署](split-panel-agent.md#方案-b跨域的当前边界)。

### 反代头与信任边界

nginx 应覆盖写入 `X-Forwarded-For $remote_addr`，而不是透传客户端头；同时传递
`Host` 和 `X-Forwarded-Proto $scheme`。当前 `clientIP()` 根据 TCP 直连方是否为
回环、私网或链路本地地址决定是否读取 XFF，并取最右侧合法 IP；这**不是可配置的
精确代理白名单**，也不由 `PANEL_TRUST_REVERSE_PROXY` 开关控制。
该开关主要允许使用反代终止 TLS 的明文传输路径，代码没有根据 X-Forwarded-Proto
动态切换 Cookie Secure；Secure 由 `PANEL_DEV` 决定。

来源 IP 处理不能代替网络隔离：宿主端口只绑回环，并限制容器网络成员。
不能保证绕过 nginx 的请求一定保留公网源地址（Docker 转发可能改变对端），
不可信私网对端尤其可能伪造 XFF。不要将面板明文端口暴露给它们。

## 环境变量

默认列为**裸 Go 进程**默认值；镜像/Compose 覆盖另行注明，事实来源为
`cmd/panel/main.go`、Dockerfile 与两份 compose。

| 变量 | 必需 | 裸进程默认 | 作用 / 镜像覆盖 |
|---|---|---|---|
| `DATABASE_URL` | 是 | 无 | PostgreSQL DSN；为空或无效会启动失败 |
| `REDIS_URL` | 否 | 空 | 登录限流；未配置则使用进程内有界计数 |
| `PANEL_MASTER_KEY` | 是 | 无 | 32 字节 base64/hex，或 ≥32 字符；生成一次固定保存 |
| `PANEL_LISTEN` | 否 | `127.0.0.1:8080` | 镜像设为 `0.0.0.0:8080`，当前 bridge 编排保持此值 |
| `PANEL_PUBLIC_URL` | 否 | 空 | 空时订阅 URL 回退为 `http://` + LISTEN；Compose 强制填写正确的 HTTPS 入口 |
| `PANEL_MIGRATIONS_DIR` | 否 | `migrations` | 镜像设 `/app/migrations` |
| `PANEL_WEB_ROOT` | 否 | 空（API-only） | 镜像及 Compose 默认 `/usr/share/proxypanel/web`；显式空关闭 SPA |
| `PANEL_AGENT_INSTALLER_PATH` | 否 | 自动检测/空 | 镜像设 `/app/install/deploy-agent.sh`；裸进程可指定同版脚本，缺失则禁用一键安装 |
| `PANEL_AGENT_BINARY_AMD64` / `PANEL_AGENT_BINARY_ARM64` | 否 | 自动检测/空 | 镜像内置两个架构；裸进程只公布实际配置且通过启动指纹校验的同版 Agent |
| `PANEL_DEV` | 否 | `false` | 强制回环监听、Cookie 去 Secure；仅裸进程本机开发，不用于本篇 Docker bridge 部署 |
| `PANEL_TRUST_REVERSE_PROXY` | 否 | `false` | 允许反代终止 TLS 的明文监听；镜像设 `true`，不再强制回环 |
| `PANEL_TLS_CERT` / `PANEL_TLS_KEY` | 否 | 空 | 成对提供则 Go 自己开 TLS，可监听任意地址；优先于反代路径。本文 HTTP upstream 不应设置它们 |
| `PANEL_ADMIN_USER` / `PANEL_ADMIN_PASSWORD` | 否 | 空 | 仅 members 表为空时创建管理员；密码至少 10 位。Compose 用户名默认 admin，不提供默认密码 |
| `PANEL_ALLOWED_ORIGINS` | 否 | 空 | CORS 精确 origin 白名单；同源部署保持空 |
| `PANEL_COOKIE_SAMESITE` | 否 | `strict` | `strict` / `lax` / `none`；`none` 不允许与 DEV 同用 |
| `PANEL_COOKIE_DOMAIN` | 否 | 空 | 默认 host-only；一般无需扩大到父域 |

传输判定顺序：成对 TLS 文件 → 反代终止 TLS → DEV 开发；三者均不满足时拒绝明文。
**DEV 的回环校验独立执行**，即使同时设了 TLS 或信任反代也不能绕过。
设置非空 `PANEL_WEB_ROOT` 时，启动会检查目录及 `index.html`，不可用则退出。

Compose 必填：`PG_PASSWORD`、`REDIS_PASSWORD`、`PANEL_MASTER_KEY`、`PANEL_PUBLIC_URL`。
`${VAR:?}` 只检查非空，不会阻止 `CHANGE_ME` 占位值，首次启动必须全部替换，且设置
`PANEL_ADMIN_PASSWORD`。bundle 的 `PANEL_TAG` 默认 `latest`，`PANEL_BIND_IP` 默认
`127.0.0.1`，`PANEL_PORT` 默认 `8081`；example 的发布端口写死为 8081，不读取 `PANEL_PORT`。
密码进入 DSN，建议像快速开始一样用随机 hex，避免 URL 特殊字符编码问题。

Agent 超时覆盖（Go duration，非法值忽略）：`PROXYPANEL_XRAY_READINESS_TIMEOUT`（30s）、
`PROXYPANEL_XRAY_VALIDATION_TIMEOUT`（30s）、`PROXYPANEL_XRAY_STOP_TIMEOUT`（15s）、
`PROXYPANEL_XRAY_ROLLBACK_TIMEOUT`（60s）；其余配置在 `agent.json`。

## 数据与备份

| 卷 | 内容 | 丢失后果 |
|---|---|---|
| `pgdata` | 全部业务状态、会话、账本、审计、凭据密文 | 必须恢复数据库备份 |
| `redisdata` | 登录限流计数 | 丢失限流窗口；不丢会话或账本 |
| `agentstate`（仅 example） | Agent 身份、快照、计量基线、未上传队列 | 需重新注册，未上传用量不可恢复 |

最小恢复集合是数据库 dump + `PANEL_MASTER_KEY`，分开加密保存但必须能配对取得。
密钥丢失或改动会使原代理凭据无法解密，只能全部轮换；账号密码哈希不受影响。
Agent 状态需停 Agent 后一致备份完整目录，不能只复制 SQLite 主文件。
证书只在宿主 nginx 管理，不需要挂进面板容器。完整恢复步骤见
[备份与恢复](../operations/backup-recovery.md)，不能直接恢复旧库后对外开放。

## 健康检查

| 端点 | 行为 |
|---|---|
| `/healthz` | 进程能应答即 200，不检查依赖；用于容器存活探针 |
| `/readyz` | PostgreSQL 不通返回 503；Redis 不可用仍可返回 200，但 `data.status=degraded`；未配 Redis 时为 `redis=disabled` |

容器 HEALTHCHECK 为 `http://127.0.0.1:8080/healthz`（容器内回环 HTTP），宿主探测为：

```bash
curl -fsS http://127.0.0.1:8081/healthz; echo
curl -fsS http://127.0.0.1:8081/readyz; echo
curl -fsS https://panel.example.com/readyz; echo
```

监控必须读取就绪响应体，不能只看 HTTP 200。探针成功也不等于 HTTPS Cookie 登录、
SPA 刷新和真实 Agent 流量已验收。

## 升级

先按 [生产维护顺序与材料权限](production.md#升级与回滚) 备份并验证：管理员停新增代理流量、最终计量，保留旧 Uploader/Panel 接收直到 ACK 排空，再停 Agent/核心并取一致停止状态证据，最后停止旧 Panel 和其它授权/账本写者。不能从 PG 推断未上传队列为零，不删队列、不猜时区。

在管理原部署的 compose / `.env` 目录中 staging 候选镜像（本地构建则不 pull），**不要先 `up` 候选**。给 panel 增加只读材料目录挂载，所有引用文件均在该目录中：

```yaml
# 合并到现有 panel 服务；路径由管理员配置，不是 HTTP 请求输入
services:
  panel:
    environment:
      PANEL_UPGRADE_EVIDENCE_FILE: /run/upgrade/manifest.json
    volumes:
      - /etc/proxypanel/upgrade:/run/upgrade:ro
```

宿主目录和文件仅管理员可写，容器 UID/GID 要有目录遍历与文件读取权限（目录 0750、文件 0640，正确映射服务组）；不要 chmod 777，不把原始状态备份或秘密放到 Web 根目录。挂载完整材料目录而非只挂清单，否则相对引用不可读。网页不接受上传/任意路径。fresh 空库无旧 Agent 无需该挂载或造材料。

```bash
docker compose pull panel
# 已完成 ACK 排空、Agent 停止取证，再停止旧 Panel 写入
docker compose stop panel
# compose 此时选择候选镜像；run 不启动服务依赖，不走业务启动 Open
# 候选镜像 migrations 位于 /app/migrations；没有 --migrations flag
# --evidence 路径必须和后续 Open 的配置相同
docker compose run --rm --no-deps --entrypoint /app/panel \
  -e PANEL_MIGRATIONS_DIR=/app/migrations panel \
  check --preflight --json --evidence /run/upgrade/manifest.json
```

检查真实 JSON `status` 必须为 `ready` 且 exit=0；blocked=2、unknown=3、任意非零均保持旧 Panel 停止，**不执行 up**。只有 ready 才由管理员显式执行 `docker compose up -d --no-deps panel`，随后看启动日志、运行候选 `check --json` 并核对 readyz 内容。不能用分号无条件串接 start/up。保持停写/fence，Open 还会在锁内重验 DB/材料；预检不是永久许可。

迁移逐文件事务，失败文件回滚但本次此前成功前缀保留，并非整体回滚。保存实际 schema_migrations(version/checksum/applied_at) 与现场后修复重试。已应用 SQL 不可修改。

**镜像技术回退只允许停写核对**：保持 panel 容器 stopped，检查实际已提交前缀与旧镜像二进制/SQL 的兼容性，不示例切旧 tag 后 `up`。本项目没有旧版 readonly 启动能力，不能承诺旧 binary 只读启动，也没有 PANEL_READ_ONLY 开关。数据库恢复不是默认回退；实际恢复/代际不明必须按 [恢复协调流程](../operations/backup-recovery.md) enterRecoveryMode、轮换 generation、核对授权/账本并让每个 Agent 显式 accept，防止撤权复活。

## 验证边界

历史真实 Docker / Xray / 客户端验收见 [测试基线](../development/testing.md#已验证的结果参考基线)。
本次新镜像另做了真实构建、两种托管模式的 nginx 模板检查、HTTPS 登录、真实 Chromium 刷新与会话验证，见[部署烟测](../development/deployment-smoke.md)。nginx 以隔离容器模拟外置代理，未修改生产宿主 nginx；本轮未重跑 Agent 数据面，CI 发布状态也需单独确认。
Linux 宿主 systemd 安装/升级/回滚与多机生产规模也不在既有验收范围内。
