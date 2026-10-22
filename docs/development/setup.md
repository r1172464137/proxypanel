# 开发环境（WSL2 / Linux）

本项目在 **WSL2 与 Linux** 上开发和验证。所有命令都是 bash。

## 依赖

| 工具 | 版本 | 用途 |
|---|---|---|
| Go | 1.27.3（见 `go.mod`） | 后端与 Agent |
| Node.js | ≥ 20（当前验证 24.x） | 前端构建与测试 |
| PostgreSQL | 17 | 应用数据 + store 层回归测试 |
| Redis | 7 | 登录限流（可选，留空则进程内计数） |
| Xray | 26.3.27 或 26.3.23 | 真实核心测试 |

`go.mod` 里写的是最低版本；工具链差异会导致 `go test` 结果不同，报告问题时请附 `go version`。

## 一次性准备

```bash
git clone https://github.com/r1172464137/proxypanel.git
cd proxypanel

# 后端依赖
go mod download

# 前端依赖（Playwright 只在跑真实浏览器测试时需要）
cd web && npm ci && npx playwright install chromium && cd ..

# 真实核心测试需要官方 Xray 二进制（自行下载并核对 SHA256）
curl -LO https://github.com/XTLS/Xray-core/releases/download/v26.3.27/Xray-linux-64.zip
sha256sum Xray-linux-64.zip   # 与官方 .dgst 比对
unzip -o Xray-linux-64.zip -d bin/ && chmod +x bin/xray
```

## 日常检查

```bash
# 后端
go build ./...
go vet ./...
go test ./... -count=1 -p 1     # -p 1 避免多个包同时抢数据库

# 前端
cd web
npx vue-tsc --noEmit            # 类型检查
npm test                        # 单元测试
npm run build                   # 生产构建
```

`-p 1` 不是习惯问题：store 层测试各自建独立 schema，但同时跑会让连接数打满，
报出看起来像代码问题的超时。

## 真实数据库测试

store 层和 API 集成测试**真实连 PostgreSQL**，但默认 SKIP：

```bash
export PANEL_TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/proxypanel_test?sslmode=disable'
go test ./internal/panel/store/ ./internal/panel/api/ -count=1
```

没设这个变量时，测试会打印 `PANEL_TEST_DATABASE_URL is not set; skipping ...`。
**SKIP 不等于通过** —— 报告测试结果时请说清哪些是真跑了。

测试会为每个用例建随机 schema（`ppreg_*`）并在结束时 DROP，不会污染应用自己的 schema。
指向的库必须是**专用的测试库**，指到生产库上会因为 DDL 权限或残留 schema 出问题。

Redis 是可选的；设 `REDIS_URL` 时登录限流走真实 Redis，不设则退化为进程内有界计数。

## 本地起一套

```bash
export PANEL_MASTER_KEY="$(openssl rand -base64 32)"   # 固定保存，不要每次重新生成
export DATABASE_URL='postgres://proxypanel:pass@127.0.0.1:5432/proxypanel?sslmode=disable'
export PANEL_DEV=true
export PANEL_LISTEN=127.0.0.1:8080
export PANEL_PUBLIC_URL=http://127.0.0.1:5173
export PANEL_ADMIN_USER=admin
export PANEL_ADMIN_PASSWORD='local-dev-password-123'   # ≥10 位；首次启动后删掉这两个变量

go run ./cmd/panel
```

另开终端跑前端（Vite 把 `/api` 与 `/sub` 代理到 8080）：

```bash
cd web && npm run dev -- --host 127.0.0.1
```

然后接 Agent：

```bash
cp configs/agent.example.json /tmp/agent.json
# 填 panel_url（回环 http 需 allow_loopback_http=true）、xray_path、state_dir、注册码
go run ./cmd/agent -config /tmp/agent.json -check   # 先检查
go run ./cmd/agent -config /tmp/agent.json          # 再运行
```

注册成功后可以从配置里删掉注册码，token 已持久化在 Agent 的 SQLite 里。
**不要为了"修复"上报问题去删状态目录** —— 那会丢掉独立的 token 和待上传队列。

## 环境变量速查

完整表格见 [部署说明](../deployment/docker.md)。开发时常用的：

| 变量 | 说明 |
|---|---|
| `PANEL_MASTER_KEY` | 必需。加密 `credentials.secret_enc`；丢了只能全部轮换凭据 |
| `DATABASE_URL` | 必需 |
| `PANEL_DEV=true` | 允许回环明文 HTTP，Cookie 去掉 `Secure` |
| `PANEL_LISTEN` | `DEV` 或 `TRUST_REVERSE_PROXY` 为 true 时必须是回环地址 |
| `PANEL_PUBLIC_URL` | **只**用于拼订阅链接，不影响 CSRF |
| `REDIS_URL` | 可选，仅登录限流 |

## 代码地图

```
cmd/panel/            面板入口：读环境变量、起 HTTP 服务
cmd/agent/            Agent 入口：拉快照、管实例、上报
internal/panel/
  api/                HTTP 路由与 handler（api.go 是路由表）
  store/              数据访问；乐观并发、软删守卫、修订推进都在这
  domain/             领域类型与错误码
  protocol/           节点校验、端点计算（EndpointFor 决定客户端连哪个端口）
  clientconfig/       四种订阅格式的渲染
  subscription/       订阅组装与配额判定
  limit/              Redis 登录限流
  secrets/            master key 解析与 AEAD
internal/agent/       Agent 侧：快照应用、xray 进程监管、计量队列
migrations/           编号 SQL，checksum + advisory lock，已应用的不能改
web/                  Vue 3 + TypeScript 前端
deploy/               Dockerfile、compose、systemd unit、nginx 示例、运维脚本
```

## 改代码时注意

- **迁移只能新增**。`migrations/` 下已应用的 SQL 文件改动会被 checksum 校验拒绝，
  需要变更就加新的编号文件。
- **改 `internal/panel/store/` 的更新逻辑时想一下软删**：`archivedGuard` 会拦住
  对已归档对象的修改；正常路径（先归档再改）不该发生，如果发生了说明调用方有问题。
- **`version` 语义**：store 里传 `0` 表示跳过检查，那只给内部调用用。
  API 层由 `requireVersion` 强制拒绝 0。

## 测试矩阵

不同层次的测试怎么跑、覆盖什么、结果怎么看，见 [测试](testing.md)。
