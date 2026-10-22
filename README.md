# proxypanel

小圈子自用的代理管理面板：**Go 面板 + PostgreSQL + Redis + Vue 3 + 自研 Go Agent + 官方 Xray**。
没有支付、订单、返利、邀请分佣这类商业系统 —— 就是给几个熟人分流量、发订阅、随时能撤权。

> 私人项目，自用 + 少量朋友。不是商业机场系统，不提供服务承诺。

## 它解决什么

自己搭了节点给朋友用，手动发配置迟早出事：谁还剩多少流量、谁该断了、给了哪些节点、
某个人是不是该踢了。这套东西就是把「谁能用哪个节点、用了多少、什么时候失效」
变成面板上能看见、能一键改的状态，并且**撤权是真的立刻生效**，不是等下次重连。

## 能力

- **成员与订阅** — 管理员/成员两种角色，成员有自己的订阅令牌，一个订阅可挂多个节点组。
- **按节点授权的凭据** — 每个（成员 × 节点）一条独立凭据；改一条不影响其他人。
- **批量变更** — 先出影响预览（谁会被改成什么），再确认提交，幂等键防重复提交。
- **硬撤权** — 停用成员/改凭据会真的重启该节点核心实例，已在连的会话也断。
- **流量计量** — Agent 上报用量，面板做归属；漏报会重试，账本幂等不会重复计。
- **订阅格式** — 输出 Xray、sing-box、Mihomo、v2rayN 四种。
- **恢复协调** — 从备份恢复后先冻结写入，核对完再显式解除，避免旧状态把已撤销的人放回来。
- **对外端口分离** — 节点在本机监听 20101，成员去连中转机的 443；端口转发由你自己配。

## 组件

| 组件 | 作用 |
|---|---|
| `cmd/panel` | 面板：HTTP API、订阅分发、授权计算、审计 |
| `cmd/agent` | 部署在节点机上：拉快照、跑 Xray 实例、上报用量 |
| `web/` | 面板前端（Vue 3 + TypeScript + Tailwind + shadcn-vue） |
| PostgreSQL 17 | 全部业务数据（含会话、账本、审计）；迁移带 checksum + advisory lock |
| Redis 7 | 只做登录限流计数。`REDIS_URL` 留空时自动退化为进程内有界计数 |
| Xray | 官方原版，Agent 只做配置生成与进程管理，不改核心 |

Agent 精确接受 **Xray 26.3.27** 与 **26.3.23**（闭合白名单），不会自动下载或自动升级。

## 快速开始

需要 Docker 与 Docker Compose。完整说明见 [快速开始](docs/getting-started/quickstart.md)。

```bash
git clone https://github.com/r1172464137/proxypanel.git
cd proxypanel

# 生成 master key：它加密数据库里的凭据，丢了就再也解不开
export PANEL_MASTER_KEY="$(openssl rand -base64 32)"

cp deploy/docker-compose.example.yml docker-compose.yml
docker compose up -d
```

首次启动提供管理员账号，之后就不要再传密码变量：

```bash
PANEL_ADMIN_USER=admin PANEL_ADMIN_PASSWORD='换成你自己的强密码' docker compose up -d
```

打开 `http://<你的IP>:8080`，登录 → 建服务器 → 建节点 → 建节点组 → 建订阅 → 建成员 →
在服务器页面生成一次性注册码 → 把 Agent 接上。

## 部署方式

- **Docker 单栈**（面板 + PostgreSQL + Redis + Agent 都在容器里）：[deployment/docker.md](docs/deployment/docker.md)
- **面板与 Agent 分开部署**（面板在一台，节点在另一批机器上）：[deployment/split-panel-agent.md](docs/deployment/split-panel-agent.md)
- **生产注意**（TLS、反向代理、权限、不要暴露的端口）：[deployment/production.md](docs/deployment/production.md)

## 运维

- 备份与恢复、控制代际协调：[operations/backup-recovery.md](docs/operations/backup-recovery.md)
- 维护任务、保留清理、告警：[operations/maintenance.md](docs/operations/maintenance.md)

## 开发

本项目在 **WSL2 / Linux** 上开发和验证。命令都是 bash。

```bash
# 后端
go build ./...
go vet ./...
go test ./... -count=1 -p 1

# 前端
cd web
npm ci
npx vue-tsc --noEmit
npm test
npm run build
```

真实数据库测试需要独立的 PostgreSQL/Redis（不能用别人的库），配置见
[开发环境](docs/development/setup.md)。缺少这些环境时相关用例会 **SKIP**，
不是「已经用真实数据库测过」。

## 文档

完整索引见 [docs/README.md](docs/README.md)。常看的几篇：

| 我想…… | 看这篇 |
|---|---|
| 最快跑起来 | [getting-started/quickstart.md](docs/getting-started/quickstart.md) |
| 搞懂它怎么工作 | [design/architecture.md](docs/design/architecture.md) |
| 调 API | [reference/api.md](docs/reference/api.md) |
| 出了事怎么救 | [operations/backup-recovery.md](docs/operations/backup-recovery.md) |
| 本地开发 | [development/setup.md](docs/development/setup.md) |

`docs/archive/` 是历史记录（验收快照、交接、事故复盘、旧计划），不再维护，只作为决策出处保留。

## 安全须知

- **`PANEL_MASTER_KEY` 一旦丢失，数据库里的代理凭据无法恢复**。请和数据库一起备份。
- 面板默认只监听回环；对外一律走 HTTPS 反向代理，别把 Agent 的 Stats 端口暴露到公网。
- 不提供默认管理员密码，仓库里不含任何真实密钥、数据库凭据或 Agent 状态。
- 订阅令牌等同于访问凭据，泄露后请立刻在成员页面轮换。

## 已知边界

- 在 WSL2 上完成了完整的功能与真实链路验证（含真实 Docker 栈、真实 Xray、真实客户端流量）；
  宿主 Linux + systemd 的安装/升级/回滚没有在目标生产机验收。
- v2rayN 的图形界面导入未验收（其无官方非 GUI 导入接口）；已验收的是配置能够正确生成并被加载。
- 公网直连、真实 Telegram 送达、多节点大规模 SLA 需要在你自己的环境里实测。

## 许可证

私人项目，未附许可证文件。要复用请先开 issue 说一声。

## 致谢

- [Xray-core](https://github.com/XTLS/Xray-core) — 代理核心，本项目不修改它
- [shadcn-vue](https://github.com/unovue/shadcn-vue) / [Reka UI](https://github.com/unovue/reka-ui) — 前端组件基础
