# proxypanel

面向个人和小团队的自托管代理管理面板：**Go 控制面 + Vue 3 前端 + PostgreSQL + Go Agent + 官方 Xray**。

它把“谁能访问哪些节点、还有多少额度、什么时候失效、变更是否真正下发、流量是否正确入账”放到一个可审计的管理闭环中。项目只提供管理员和成员两种角色，不包含支付、订单、返利、分销或多租户商业系统。

> 适合可信小团队自行部署和运维。它不是商业机场平台，也不替你提供公网证书、节点网络、监控值守或 SLA。

## 适合什么场景

你已经维护一组 Xray 节点，希望：

- 用服务方案统一分配节点、额度、周期和有效期；
- 让成员自己查看状态、导入配置、查询用量和管理账号；
- 暂停、到期、超额或轮换凭据时，节点上的授权真正收紧；
- 在批量操作前看到影响范围，提交后追踪每个 Agent 的实际结果；
- 对重发、离线补报、跨周期流量和备份恢复保留可核对证据；
- 自己掌握数据库、主密钥、TLS、节点和备份，不依赖第三方控制面。

如果你需要支付计费、工单、多租户、自动售卖、全协议或全客户端承诺，这个项目不适合直接使用。

## 主要能力

### 管理员交付

- **引导式首次设置**：服务器 → 节点 → 节点组 → 服务方案 → 成员/邀请 → 真实连接验证。
- **资源管理**：服务器、节点、节点组、模板和服务方案；高级协议字段无损往返，秘密默认不回显。
- **成员开通**：直接创建或发一次性邀请；邀请可带服务方案，也可只创建待开通账号。
- **影响预览与批量任务**：先计算真实目标和前后状态，再以幂等键提交；逐对象展示成功、等待和失败。
- **运行状态**：Agent 心跳、核心实例、期望/已应用 revision、任务、事件和升级 readiness 分开呈现，不用一个绿色状态掩盖部分失败。

### 成员自助

- **“我的服务”首页**：服务状态、不可用原因、额度、有效期、周期和节点集中展示；未知数据不会伪装成 0。
- **客户端导入**：复制订阅链接、二维码、配置下载和明确的手动导入步骤；取回配置不冒充真实连通。
- **用量与账号安全**：用量明细、密码修改、会话撤销、订阅链接轮换和连接凭据轮换分别处理。
- **权限边界**：成员只能访问本人数据；管理员页面、服务器秘密、完整 token 和私钥不会因前端隐藏而放行。

### 授权、计量与恢复

- **按成员 × 节点生成独立凭据**，单人轮换不影响其他成员。
- **真实撤权**：授权收紧会下发到 Agent；新连接和已建立连接都纳入验证，不只改数据库状态。
- **账本幂等**：Agent 使用持久队列上报，批次 ID 与 payload 指纹防重复计量和异载荷重放。
- **民用日历周期**：按服务方案的 IANA 时区和锚定日计算，不把 UTC 月份冒充本地月份。
- **迟报归属**：样本必须落入已记录的成员周期和节点归属窗口；无法证明时拒绝并保留队列，不静默挪到当前周期。
- **恢复协调模式**：数据库恢复后先隔离入口、轮换 control generation、冻结普通写入并核对授权与账本；旧备份不能自动复活已撤销权限。
- **安全升级门禁**：有待迁移时要求一致停止的 Agent 队列/进程核对材料；schema-current 普通重启不会把旧升级材料当永久许可证，但 checksum、账本、周期和孤儿任务检查仍会执行。

## 架构

```text
浏览器 / Agent
      │ HTTPS
      ▼
宿主 nginx（TLS 终止）
      │ HTTP，仅宿主回环或受控容器网络
      ▼
┌─────────────────────────────────────────────┐
│ panel（单个非 root Go 进程）               │
│ API · SPA（可选）· 授权 · 订阅 · 账本 · 审计 │
└───────────────┬─────────────────────┬───────┘
                │                     │
          PostgreSQL 17          Redis 7
          业务权威数据           仅登录限流

节点机：Agent ──拉取快照──> panel
          │
          └──管理官方 Xray 实例、采样并持久化待上报用量
```

| 组件 | 职责 |
|---|---|
| `cmd/panel` | HTTP API、可选 SPA 托管、订阅输出、授权计算、账本、审计与迁移 |
| `cmd/agent` | 拉取权威快照、管理 Xray、执行本地到期收紧、持久化并上报用量 |
| `web/` | Vue 3 + TypeScript 管理端和成员端 |
| PostgreSQL 17 | 会话、授权、任务、账本、周期历史和审计的唯一权威数据库 |
| Redis 7 | 登录限流；不可用时退化为单进程有界限流，不承载业务权威数据 |
| 官方 Xray | 代理核心；项目生成配置和管理进程，不修改核心 |

Agent 对 Xray 使用闭合版本白名单：**26.3.27、26.3.23**。它不会静默下载、替换或接受未知版本。

## 快速开始

生产路径是 **HTTPS + 宿主 nginx**。不要把 panel 的明文 HTTP 端口直接暴露给浏览器，也不要用 `PANEL_DEV=true` 绕过生产传输检查。

### 1. 准备环境

需要：

- Linux、Docker Engine 24+、Compose v2；
- 已解析到宿主机的域名；
- 浏览器和 Agent 信任的 TLS 证书；
- 至少 2 vCPU、2 GiB 内存和 10 GiB 磁盘作为小规模起点；
- 节点机上的官方 Xray 二进制。

### 2. 生成并保存配置

```bash
git clone https://github.com/r1172464137/proxypanel.git
cd proxypanel
cp deploy/docker-compose.bundle.yml docker-compose.yml

PANEL_HOST=panel.example.com
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
printf '首次管理员密码：%s\n' "$ADMIN_PASSWORD"
```

`PANEL_MASTER_KEY` 用来加密代理凭据。**丢失或更换它会使数据库里的既有代理凭据无法解密**；请与数据库备份分开保存，但确保恢复时能配对取得。

镜像发布状态可能落后于仓库，部署前必须确认 compose 中的镜像标签包含当前提交。没有对应发布标签时，按 [Docker 部署：镜像与构建](docs/deployment/docker.md#镜像与构建) 使用仓库现有 Dockerfile 构建，不要假定 `latest` 自动等于主分支。

### 3. 启动并配置 HTTPS

```bash
docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:8081/healthz; echo
curl -fsS http://127.0.0.1:8081/readyz; echo
```

然后使用 [`deploy/nginx.conf.example`](deploy/nginx.conf.example) 配宿主 nginx：

```text
https://panel.example.com:443
        ↓
宿主 nginx
        ↓
http://127.0.0.1:8081
        ↓
panel 容器 0.0.0.0:8080
```

执行 `nginx -t` 后再 reload，并从 HTTPS 入口验证登录和深链刷新。私有 CA 使用 `--cacert` 或安装信任根，不把 `-k` 当作验收依据。

完整、可复制的步骤见 [HTTPS 快速开始](docs/getting-started/quickstart.md)。

### 4. 建立可交付服务

登录后按下面顺序操作：

1. 创建服务器；
2. 创建节点；
3. 创建节点组；
4. 创建服务方案（节点组、额度、时区、周期、有效期）；
5. 创建成员或发邀请；
6. 在服务器页面生成一次性 Agent 注册码；
7. 在节点机启动 Agent；
8. 确认 Agent 在线、实例运行、已应用 revision 追上期望 revision；
9. 用成员导出的官方客户端配置传一次真实流量，并在用量页核对账本增量。

页面显示在线、配置能下载、核心进程存在，都不能替代第 9 步。

## 两种前端托管方式

Panel 镜像里不运行 nginx，只运行非 root Go 进程。

### A. Go 托管 SPA（默认）

```text
PANEL_WEB_ROOT=/usr/share/proxypanel/web
```

宿主 nginx 将 `/`、`/api/`、`/sub/` 全部反代给 panel。前后端同镜像，版本最容易保持一致。

### B. 宿主 nginx 托管静态文件

```text
PANEL_WEB_ROOT=
```

Panel 只提供 API；宿主 nginx 直接提供 `web/dist`，并将 `/api/`、`/sub/`、`/healthz`、`/readyz` 反代给 panel。必须保留 SPA fallback，且 `index.html` 不应长缓存。

两种方式都推荐使用**同一个 HTTPS origin**，避免额外的 Cookie、CORS 和 CSRF 配置面。详见 [生产部署](docs/deployment/production.md#nginx终止-tls-与选择前端归属)。

## Agent 与节点

Agent 配置示例：

```json
{
  "panel_url": "https://panel.example.com",
  "registration_code": "一次性注册码",
  "xray_path": "/opt/proxypanel/xray/26.3.27/xray",
  "state_dir": "/var/lib/proxypanel-agent",
  "poll_interval_seconds": 5,
  "stats_interval_seconds": 5,
  "heartbeat_interval_seconds": 15,
  "allow_loopback_http": false
}
```

上线前执行：

```bash
proxypanel-agent -config /etc/proxypanel/agent.json -check
```

注册成功后可从配置中删除一次性注册码。**不要删除 Agent 状态目录**：它保存身份、已应用代次、核心恢复记录、计量基线和未确认队列。恢复数据库产生新 generation 后，必须停 Agent、核对授权和账本，再显式运行：

```bash
proxypanel-agent -config /etc/proxypanel/agent.json -accept-generation <exact-generation>
```

裸机安装与 unit 说明见 [生产部署](docs/deployment/production.md#权限与文件所有权)；面板与 Agent 分机部署见 [分离部署](docs/deployment/split-panel-agent.md)。

## 客户端与协议范围

当前订阅输出：

- Xray JSON；
- sing-box JSON；
- Mihomo YAML；
- v2rayN 分享链接。

已使用固定版本官方核心验证真实配置加载、完整流量和关键拒绝：Xray 26.3.27、sing-box 1.14.3、Mihomo 1.19.33；覆盖 VLESS、VMess、Trojan、Shadowsocks、TLS、REALITY/Vision 及支持的传输组合。某个格式无法表达节点配置时会明确拒绝，不会删字段后假装可用。

这不等于 Windows、Android、iOS 图形客户端全部实机验收。v2rayN 当前验证到分享链接形状，不宣称其 GUI 导入已经自动化验证。详细矩阵和字段差异见 [订阅格式](docs/reference/subscription-formats.md)。

## 备份、升级与恢复

至少备份：

- PostgreSQL custom-format dump；
- 与该数据库匹配的 `PANEL_MASTER_KEY`；
- 停止并完成 checkpoint 后的完整 Agent 状态目录；
- 配置、证书、发布包和迁移 checksum。

升级前先执行只读 readiness，处理 pending queue、周期/归属不明、checksum 错误和孤儿任务；不要通过删队列、改 batch ID、手工标 ready 或恢复旧数据库来绕过门禁。

恢复数据库时：

1. 在专用目标还原，保持面板与 Agent 的外部通信隔离；
2. schema-current 数据库可以完成内部 readiness 后启动，但此时旧授权仍不可信；
3. 只向本机管理员开放，立即进入恢复协调模式并轮换 generation；
4. 核对备份后发生的停用、到期、凭据轮换和账本缺口；
5. 退出恢复模式后，每个 Agent 显式接受新 generation；
6. 重新导出配置并验证新授权真实流量，旧凭据继续拒绝。

完整流程和不可恢复缺口的处理见 [备份与恢复](docs/operations/backup-recovery.md)。

## 验证基线

当前主线已经覆盖下列范围；这些是项目测试环境的证据，不是你部署环境的 SLA：

- 18 个管理员/成员用户故事及关键异常场景；
- 真实浏览器、真实 PostgreSQL、真实 AgentSupervisor、官方 Xray、固定文件 SHA 和账本对应；
- 双 Agent、真实 SOCKS5 UDP、离线到期、部分完成、旧回执与核心崩溃回滚；
- 真实 `pg_dump` / `pg_restore`、恢复协调、队列精确重放、ACK 已删除缺口和旧权限不复活；
- 从已知历史提交构建旧 Agent，复用旧 SQLite，在 Xray 26.3.23 / 26.3.27 间升级与回退并逐阶段验证真实流量；
- Vision 3 格、客户端主矩阵 21 格及凭据守卫；
- Go 托管 SPA 与 API-only 两种真实宿主 nginx 模式、严格私有 CA 信任链；
- 前端单元测试、类型检查、生产构建及独立 backend/frontend/web-demo 验收。

仍需部署方自行验证：公有 DNS/CA、Windows/移动 GUI、读屏器、完整 system-level Agent 安装、生产 WAL/PITR/离机备份、真实 Telegram 送达和生产规模容量。测试入口与分层说明见 [开发测试指南](docs/development/testing.md)。

## 开发

项目使用 Go 1.27.3、Node 22、Vue 3 和 TypeScript；在 Linux/WSL2 环境开发和验证。

```bash
# 后端快速反馈
go test ./... -count=1 -p 1
go vet ./...

# 前端
cd web
npm ci
npm run typecheck
npm test
npm run build
```

涉及 PostgreSQL、Redis、Agent 或官方核心的用例需要**独立测试环境**。缺少依赖时的 `SKIP` / `not_run` 不等于通过；不要对共享或生产数据库运行测试。环境和统一入口见 [开发环境](docs/development/setup.md) 与 [测试说明](docs/development/testing.md)。

## 文档导航

完整索引：[docs/README.md](docs/README.md)

| 我想…… | 文档 |
|---|---|
| 第一次部署并打通真实流量 | [快速开始](docs/getting-started/quickstart.md) |
| 使用 Docker 部署 | [Docker 部署](docs/deployment/docker.md) |
| 做生产 TLS、权限和升级 | [生产部署](docs/deployment/production.md) |
| 分开部署 panel 与 Agent | [分离部署](docs/deployment/split-panel-agent.md) |
| 备份、恢复或处理 generation | [备份与恢复](docs/operations/backup-recovery.md) |
| 排障、清理与看指标 | [维护与排障](docs/operations/maintenance.md) |
| 理解授权、revision 和计量 | [架构](docs/design/architecture.md) |
| 查 HTTP API | [API 参考](docs/reference/api.md) |
| 查订阅格式兼容性 | [订阅格式](docs/reference/subscription-formats.md) |
| 本地开发和测试 | [开发环境](docs/development/setup.md) · [测试](docs/development/testing.md) |

`docs/archive/` 是不再维护的历史验收、交接和旧方案，只用于追溯当时决策；不要按其中的字段、路由或环境说明实现新代码。

## 安全红线

- 不提交 `.env`、master key、数据库凭据、Agent 状态、订阅 token 或客户端配置。
- 不把 panel HTTP、PostgreSQL、Redis 或 Xray Stats API 暴露到公网。
- nginx 必须用 `$remote_addr` **覆盖** `X-Forwarded-For`；信任私网来源不能代替网络隔离。
- 不通过清空 Agent 状态、删除队列、改账本或回滚数据库来“修好”升级。
- 不把进程存在、API 200、订阅可下载或页面提示当作真实代理可用；最终判断必须包含官方客户端的完整流量。
- 已泄露的订阅链接与连接凭据分别轮换；二者不是同一个动作。

## 许可证

仓库目前未附开源许可证。默认保留所有权利；如需复制、修改或分发，请先取得作者许可。

## 致谢

- [Xray-core](https://github.com/XTLS/Xray-core) — 官方代理核心，本项目不修改它；
- [Vue](https://vuejs.org/) / [Vite](https://vite.dev/) — 前端框架与构建工具；
- [Reka UI](https://reka-ui.com/) / [shadcn-vue](https://www.shadcn-vue.com/) — 组件基础；
- [PostgreSQL](https://www.postgresql.org/) — 业务和账本权威存储。
