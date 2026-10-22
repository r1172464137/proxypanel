# Docker 部署

这篇讲怎么把这套东西真的跑在服务器上：选哪种编排、每个环境变量是什么、数据在哪个卷里、怎么升级和备份。面向已经决定自建、需要长期维护的人。只想先看一眼的话看 [快速开始](../getting-started/quickstart.md)。

## 两种编排

仓库里有两个 compose 文件，区别不只是「数据库在不在里面」：

| | `deploy/docker-compose.bundle.yml` | `deploy/docker-compose.example.yml` |
|---|---|---|
| 组成 | PostgreSQL + Redis + 面板 | PostgreSQL + Redis + 面板 + Agent |
| 面板镜像 | `ghcr.io/r1172464137/proxypanel-panel`（镜像内 nginx + 面板，`deploy/Dockerfile.panel`；没发布就用这个 Dockerfile 自建） | 自己提供，模板里是 `ghcr.io/CHANGE_ME/...` 占位 |
| TLS 谁终止 | 镜像里的 nginx，只对外 8443 | 面板自己（`PANEL_TLS_CERT` / `PANEL_TLS_KEY`） |
| 前端静态文件 | 镜像内 nginx 托管 | 宿主 nginx 托管 `web/dist` |
| 面板发布地址 | `${PANEL_BIND_IP}:8443` | `127.0.0.1:8443` |
| PostgreSQL / Redis 端口 | 不发布 | 发布到 `127.0.0.1`（方便本机 `psql`） |
| 适用 | 一台机器、一个对外端口，最省事 | 已有宿主 nginx / 外部数据库 / 要接 Agent 容器 |

**一体化（bundle）是默认推荐。** 面板镜像里已经放好 nginx：它在容器内 `0.0.0.0:8443` 终止 TLS、托管 SPA、把 `/api` 与 `/sub` 反代给回环上的面板进程。这样对外只有一个端口，反代头只可能由同容器内的 nginx 产生，信任边界没有变大。

```bash
cp deploy/docker-compose.bundle.yml docker-compose.yml
cp deploy/.env.bundle.example .env
mkdir -p certs    # 放 fullchain.pem / privkey.pem
docker compose up -d
```

`./certs` 与 `.env` 都是相对 compose 文件所在目录解析的，所以把 compose 复制到仓库根目录、在根目录执行最省事。

### 分体示例的注意事项

`docker-compose.example.yml` 里的面板环境变量是给**纯 API 镜像**写的：

```yaml
PANEL_LISTEN: "0.0.0.0:8443"
PANEL_TLS_CERT: "/certs/fullchain.pem"
PANEL_TLS_KEY: "/certs/privkey.pem"
```

而发布的 `proxypanel-panel` 镜像里 `PANEL_LISTEN=127.0.0.1:8080`、`PANEL_TRUST_REVERSE_PROXY=true` 由镜像 `ENV` 固定，入口脚本会先等 `http://127.0.0.1:8080/healthz` 应答。**把上面三行套到发布镜像上，容器会启动超时退出**（面板在 8443 上开了 TLS，入口脚本等不到 8080），即使面板先起来，镜像里的 nginx 也会和它抢 8443。

两种正确做法：

**A. 用发布镜像 + 宿主 nginx** —— 保留镜像默认值，只发布回环端口，宿主 nginx 再反代一次：

```yaml
services:
  panel:
    image: ghcr.io/r1172464137/proxypanel-panel:latest
    environment:
      DATABASE_URL: "postgres://proxypanel:${PG_PASSWORD}@postgres:5432/proxypanel?sslmode=disable"
      REDIS_URL: "redis://default:${REDIS_PASSWORD}@redis:6379/0"
      PANEL_MASTER_KEY: "${PANEL_MASTER_KEY:?set PANEL_MASTER_KEY}"
      PANEL_PUBLIC_URL: "${PANEL_PUBLIC_URL:?set PANEL_PUBLIC_URL}"
      # 不要覆盖 PANEL_LISTEN / PANEL_TRUST_REVERSE_PROXY / PANEL_TLS_*
    volumes:
      - /etc/proxypanel/certs:/certs:ro
    ports:
      - "127.0.0.1:8443:8443"
```

宿主 nginx 用 `proxy_pass https://127.0.0.1:8443;`（需要 `proxy_ssl_server_name on;` 与自签证书的 `proxy_ssl_verify off;` 或把 CA 配进去）。

**B. 自建纯 API 镜像** —— 按 [split-panel-agent.md](../deployment/split-panel-agent.md) 步骤 3 的 Dockerfile 片段构建一个只有 `panel` 的镜像，然后原样使用 example 里的 `PANEL_LISTEN` + `PANEL_TLS_*`，宿主 nginx 托管 `web/dist`。仓库里没有这个 Dockerfile 文件，需要自己建。

## 环境变量

面板进程读取的全部变量（`cmd/panel/main.go`）。「默认值」是代码里的默认；compose 模板可能覆盖，见上表。

| 变量 | 必需 | 默认值 | 作用 |
|---|---|---|---|
| `DATABASE_URL` | **是** | 无 | PostgreSQL 连接串。为空或不合法时面板直接启动失败 |
| `REDIS_URL` | 否 | 空 | Redis 连接串，只用于登录限流计数。为空时退化为进程内**有界**本地限流，`/readyz` 报 `redis=disabled` 但仍是 `ok` |
| `PANEL_MASTER_KEY` | **是** | 无 | 加密代理凭据的密钥。32 字节的 base64/hex，或 ≥32 字符的字符串；长度不符直接启动失败 |
| `PANEL_LISTEN` | 否 | `127.0.0.1:8080` | 监听地址。`PANEL_DEV=true` 或 `PANEL_TRUST_REVERSE_PROXY=true` 时必须是回环地址 |
| `PANEL_PUBLIC_URL` | 否（强烈建议） | 空 → `http://` + `PANEL_LISTEN` | 只用于拼订阅链接（`/sub/<token>`）。填错成员会拿到打不开的地址 |
| `PANEL_MIGRATIONS_DIR` | 否 | `migrations` | SQL 迁移目录，镜像里是 `/app/migrations` |
| `PANEL_DEV` | 否 | `false` | 开发模式：允许回环明文 HTTP、Cookie 不带 `Secure`。生产不要开 |
| `PANEL_TRUST_REVERSE_PROXY` | 否 | `false` | 信任前置代理的转发的头。开启时 `PANEL_LISTEN` 必须是回环，否则启动失败 |
| `PANEL_TLS_CERT` / `PANEL_TLS_KEY` | 否 | 空 | 面板自己终止 TLS。**必须成对**给出，且文件可读。设置后不再走「反代 + 明文回环」那条路 |
| `PANEL_ADMIN_USER` | 否 | 空 | 仅在**成员表为空**时用于创建第一个管理员。两个都得非空才会创建 |
| `PANEL_ADMIN_PASSWORD` | 否 | 空 | 同上。至少 10 位，否则启动失败 |
| `PANEL_ALLOWED_ORIGINS` | 否 | 空 | 逗号分隔的允许来源。为空时不发任何 CORS 头（同源部署的正确形态） |
| `PANEL_COOKIE_SAMESITE` | 否 | `strict` | `strict` / `lax` / `none`。`none` 不能与 `PANEL_DEV=true` 同时用（启动会报错） |
| `PANEL_COOKIE_DOMAIN` | 否 | 空 | 会话 Cookie 的 Domain，例如 `.example.com`。留空 = 仅当前主机 |

传输模式的三选一（代码里的判定顺序）：

1. 设了 `PANEL_TLS_CERT`+`PANEL_TLS_KEY` → 面板自己终止 TLS，监听任意地址；
2. 否则 `PANEL_TRUST_REVERSE_PROXY=true` → 明文回环，TLS 由反代终止；
3. 否则 `PANEL_DEV=true` → 明文回环（开发用）。

三者都不满足时面板拒绝启动：`refusing cleartext HTTP in production`。

compose 层面（不是面板进程读取的）：

| 变量 | 必需 | 默认值 | 作用 |
|---|---|---|---|
| `PG_PASSWORD` | **是** | 无（`${PG_PASSWORD:?...}`） | PostgreSQL 密码，同时用于 `DATABASE_URL` |
| `REDIS_PASSWORD` | **是** | 无 | Redis 密码（`--requirepass`），同时用于 `REDIS_URL` |
| `PANEL_TAG` | 否 | `latest` | 面板镜像标签（`latest` / `main` / `sha-<短哈希>` / `v1.2.3`）。生产建议钉版本 |
| `PANEL_BIND_IP` | 否 | `192.168.5.203` | 仅 bundle：宿主侧绑定地址。**这是本项目测试机的内网 IP，必须改** |
| `NGINX_TLS_CERT` / `NGINX_TLS_KEY` | 否 | `/certs/fullchain.pem`、`/certs/privkey.pem` | 仅发布镜像：容器内 nginx 读的证书路径 |
| `SSL_CERT_FILE` | 否 | 系统默认 | 仅 Agent 容器：面板用私有 CA 时指向挂载进来的 CA 文件 |

Agent 还有四个可选的超时覆盖（Go duration 语法，非法值忽略），默认值足够；只有嵌套虚拟化等慢机器才需要调大：

| 变量 | 默认值 | 作用 |
|---|---|---|
| `PROXYPANEL_XRAY_READINESS_TIMEOUT` | `30s` | 启动后等 Xray 管理 API 应答的预算 |
| `PROXYPANEL_XRAY_VALIDATION_TIMEOUT` | `30s` | 一次 `xray -test` 的预算 |
| `PROXYPANEL_XRAY_STOP_TIMEOUT` | `15s` | 停止一个核心实例的预算 |
| `PROXYPANEL_XRAY_ROLLBACK_TIMEOUT` | `60s` | 整次回滚（校验 + 启动 + 等就绪）的预算 |

### 为什么 `PANEL_MASTER_KEY` 必须固定保存

它派生出的密钥用来加解密 `credentials.secret_enc`（每个「成员 × 节点」的密码 / UUID）。这个值不是启动校验用的口令，换掉不会报错，只会让已有密文解不开：

- 面板照常启动、成员照常登录（登录密码存的是哈希，与 master key 无关）；
- 订阅输出里少了节点、Agent 拿不到这些用户的凭据，现象是「配置在，但连不上」；
- 唯一恢复方式是轮换全部凭据，成员要重新导入订阅。

所以：**生成一次，和数据库备份放在一起**。不要写进镜像、不要每次启动重新生成。

## 卷与数据

| 卷（compose 名） | 容器路径 | 里面是什么 | 丢了会怎样 |
|---|---|---|---|
| `pgdata` | `/var/lib/postgresql/data` | 全部业务状态：成员、订阅、节点组、授权决定、凭据密文、用量账本、审计 | **丢不得**。这是唯一权威数据源 |
| `redisdata` | `/data` | 登录限流计数（bundle 里开了 AOF，example 里没开） | 可以丢。丢的只是限流窗口，不会踢会话、不会丢账 |
| `agentstate` | `/var/lib/proxypanel-agent` | Agent 身份 token、最后一次可信快照/授权、计量基线、未上传用量队列 | **丢不得**。丢了要重新注册，且未上传的用量会产生不可恢复缺口 |

外加两个文件，它们不在卷里但同样丢不得：

- `PANEL_MASTER_KEY`（在 `.env` 或密码管理器里）—— 没有它 `pgdata` 里的密文是废数据；
- `certs/fullchain.pem` + `certs/privkey.pem` —— 丢了重新申请，但证书轮换期间 Agent 会因为信任根变化连不上。

注意 docker 命名卷默认属主是 root。Agent 镜像以 uid 65532 运行，用命名卷挂状态目录时如果写不进去，改成 bind mount 并 `chown 65532:65532`（快速开始里就是这么做的）。

## 健康检查

| 端点 | 检查什么 | 返回 |
|---|---|---|
| `GET /healthz` | 什么都不检查，只证明 HTTP 进程能应答 | `200`，`data.status=ok` |
| `GET /readyz` | ping PostgreSQL；再查 Redis（有连接才查） | PostgreSQL 不通 → `503`；Redis 不可用 → `200`，`data.status=degraded` 且 `data.redis=unavailable`；都正常 → `200`，`status=ok, postgres=ok, redis=ok` |

用哪个：

- 容器 / systemd 的存活探针用 `/healthz`（数据库短暂抖动不该重启面板）；
- 负载均衡摘流量、监控告警用 `/readyz`；
- 没配 `REDIS_URL` 时 `/readyz` 报 `redis=disabled` 是预期状态（进程内回退限流），不是故障。

两个端点都在 8443 上（发布镜像内），`/healthz` 不需要认证。自签证书下探针要跳过校验，compose 里的 `wget --no-check-certificate` 就是这个原因。

## 升级

面板启动时会自动跑迁移：按文件名顺序应用未执行的 `.sql`，全程持有 PostgreSQL advisory lock，并校验每个已应用文件的 checksum。改了已经应用过的 SQL 文件，启动会直接失败而不是静默重放。

```bash
cd proxypanel

# 1. 先备份（见下一节），确认备份文件能读
# 2. 换镜像
sed -i 's/^PANEL_TAG=.*/PANEL_TAG=v1.2.3/' .env
docker compose pull panel
docker compose up -d panel

# 3. 看启动与迁移（在容器内探，不受 PANEL_BIND_IP 影响）
docker compose logs -f panel
docker compose exec panel wget -q -O- --no-check-certificate https://127.0.0.1:8443/readyz; echo
```

要停服务吗：

- 单实例：不用先停，`up -d` 会重建容器。代价是重启窗口内登录和 API 不可用（通常几十秒，含迁移时间），节点上的 Agent 会继续按最后一次快照跑，流量不断。
- 迁移较重或多副本：先 `docker compose stop panel`，升级完再 `start`。多副本同时启动是安全的（advisory lock 串行化），但会让不可用窗口变长。

**镜像回退 ≠ 数据库回退。** 迁移是单向的，仓库里没有 down migration。把 `PANEL_TAG` 改回旧值、旧二进制不认识新 schema 时面板会启动失败；此时唯一出路是恢复备份，而恢复备份会让已被撤销的成员复活——处理方式见 [production.md](production.md#升级与回滚)。想保住回滚能力，就在升级前先做一份可验证的备份。

## 备份

最小可恢复集合 = **数据库 dump + master key**。两个分开存，但必须成对拿得到。

```bash
cd proxypanel
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"

# 数据库（custom 格式，可选择性还原）
docker compose exec -T postgres pg_dump -U proxypanel --format=custom proxypanel > "backup-${STAMP}.dump"
ls -l "backup-${STAMP}.dump"

# master key：和 dump 分开放，但两者要能对上同一个时间点
grep '^PANEL_MASTER_KEY=' .env > "masterkey-${STAMP}.txt"
```

要点：

- **不要**在面板运行时直接复制 `pgdata` 目录当备份（可能拿到撕裂的页），要么 `pg_dump`，要么停库后做文件系统快照；
- **不要**只备份数据库不备份 master key：那堆密文没有 key 就是废数据；
- Agent 的状态目录单独备份（先停 Agent，确认核心被停，再整体复制），里面有未上传的用量队列；
- 恢复顺序与「先隔离、核对授权、再解除恢复门禁」的完整流程见 [operations/backup-recovery.md](../operations/backup-recovery.md)。恢复完成后**必须**走一次恢复协调模式，否则旧备份会把已撤销的人放回来。

还原到独立目标验证（不要覆盖生产库）：

```bash
docker compose exec -T postgres createdb -U proxypanel verify_restore
docker compose exec -T postgres pg_restore -U proxypanel -d verify_restore --no-owner < "backup-${STAMP}.dump"
docker compose exec postgres psql -U proxypanel -d verify_restore -c 'select count(*) from members;'
docker compose exec -T postgres dropdb -U proxypanel verify_restore
```

## 已验证到什么程度

在真实的 Docker 栈上跑通过：面板 + PostgreSQL + Redis + 真实 Agent + 真实官方 Xray + 真实客户端流量（含撤权重连、计量与账本、容器内升级/回滚、隔离环境备份还原演练）。宿主 Linux + systemd 的安装、升级、回滚**没有**在目标生产机上验收；公网直连、多节点规模下的 SLA 也没有实测。相关操作在 [production.md](production.md) 里按「需要你自己验证」标注。
