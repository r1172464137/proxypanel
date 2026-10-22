# 测试

分四层，每层解决的问题不同。**不要把上层的结果当成本层通过** ——
尤其别拿「单元测试绿了」当成「真实链路能用」。

| 层 | 命令 | 需要什么 | 覆盖什么 |
|---|---|---|---|
| 单元 | `go test ./... -count=1 -p 1` | 无（数据库相关用例 SKIP） | 协议校验、端点计算、订阅渲染、周期/计量算法、Agent 状态机 |
| 存储/接口 | 同上 + `PANEL_TEST_DATABASE_URL` | 专用 PostgreSQL | 真实 SQL、迁移、乐观并发、软删守卫、HTTP 契约 |
| 前端 | `cd web && npx vue-tsc --noEmit && npm test` | 无 | 组件渲染、表单校验、store 逻辑 |
| 真实链路 | `node web/tests/e2e-ui-full.mjs` 等 | 跑起来的面板 + 数据库 + Agent + Xray | 浏览器交互、API 全量、并发、节点流量 |

## 单元与存储

```bash
# 只有单元（store/api 里依赖数据库的会 SKIP）
go test ./... -count=1 -p 1

# 连真实数据库（store + api 的全部用例）
export PANEL_TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/proxypanel_test?sslmode=disable'
go test ./internal/panel/store/ ./internal/panel/api/ -count=1
```

`-p 1` 是必须的：store 层每个用例建独立 schema，并行会把连接数打满，
表现成莫名其妙的超时。

看 SKIP 了哪些：

```bash
go test ./internal/panel/store/ -count=1 -v 2>&1 | grep SKIP
```

**SKIP 不等于通过**。写报告时请区分「跑了且通过」和「没跑」。

### 真实 Xray 核心测试

```bash
export XRAY_BIN=/abs/path/to/xray
go test -tags xrayreal ./internal/agent/xray ./internal/agent/supervisor \
  -run TestReal -count=1 -timeout 180s
```

会真的用 Xray 起进程、加载配置、跑通一次转发。要求 `XRAY_BIN` 存在且版本在白名单里
（26.3.27 / 26.3.23）。

## 前端

```bash
cd web
npx vue-tsc --noEmit    # 类型检查：必须零错误
npm test                # 单元测试（vitest）
npm run build           # 生产构建：必须成功
```

前端单测覆盖表单校验逻辑（例如节点的 `external_port` 越界、与 `port` 相同会被拒），
不覆盖真实浏览器行为 —— 那属于下一层。

## 真实链路测试

这层需要一个**跑起来的完整系统**：面板 + PostgreSQL + Redis + Agent + 真实 Xray，
并且 Agent 已收敛（`applied_revision == desired_revision`）。
搭法见 [部署](../deployment/docker.md)。

### 浏览器矩阵

```bash
cd web
PANEL_ADMIN_PASSWORD='...' node tests/e2e-ui-full.mjs
```

覆盖：每条路由 × 桌面/移动 × 亮/暗（含溢出检测、对比度粗检、console error、
pageerror）+ 状态矩阵（真空态 / 注入 loading / 真实 404）+ 交互用例
（模板建节点、字段校验、组→订阅→成员串联、批量预览提交、对外端口保存等）
+ 成员/管理员权限矩阵。

唯一被允许的 console error 是「未登录页面启动时探测会话得到的 401」——
浏览器网络层记录，JS 无法抑制；其余任何 console error 都算失败。

### API 全量

```bash
PANEL_ADMIN_PASSWORD='...' python3 /path/to/e2e-api-full.py
```

按资源逐个跑 CRUD + 错误分支 + 权限矩阵，并统计**端点覆盖率**（要求所有路由至少被打到一次）。

> 该脚本的请求封装会对 `PATCH`/`PUT` 自动补 `version`（服务端强制要求）。
> 专门验证「缺 version 必须 400」的用例直接走原始请求，不经过这个封装。

### 并发与竞态

```bash
PANEL_ADMIN_PASSWORD='...' python3 /path/to/e2e-concurrency.py
```

用多线程 + 独立会话验证：同版本并发 PATCH 只有一个成功、5 会话并发
1 成功 4 冲突、同一幂等键并发只有一条批次落库、并发同名创建只有一条、
并发 DELETE/PATCH 不产生「删了又被改活」、Redis 限流计数精确、两个管理员并发批量只有一次生效。

### 节点流量与账本

```bash
python3 /path/to/e2e-node-ledger.py
```

真实 Xray 客户端连每个节点，比对载荷 SHA-256 与直连一致，并核对账本里的用量增量。

## 已验证的结果（参考基线）

在 WSL2 + 真实 Docker 栈上跑过：

| 项目 | 结果 |
|---|---|
| Go 包 | 19/19 ok |
| 前端单测 | 178 passed（28 files） |
| 浏览器矩阵 | 867/867 PASS |
| API 全量 | 479/479 PASS，端点覆盖 76/76 |
| 并发 | 73 项 |
| 恢复协调 | 89 项 |
| 真实流量 | 5 节点 × 真实客户端，69632 字节，SHA-256 与直连一致 |

这些数字会随代码变化，**当基线用而不是当承诺**。自己跑出来的才作数。

## 写测试的约定

- 每个修复都带一条会**在修之前失败**的用例。写完把实现改动临时 stash 掉，
  确认测试变红，再恢复 —— 否则你无法确定测试真的在守那条行为。
- 测试名说人话，注释写「为什么需要这条」，不是重复断言内容。
  例如 `TestArchivedObjectsAreNotUpdatable` 要说明「软删后行还在，按 id 更新仍能命中」。
- 涉及并发的用例必须真的并发（多线程 + 独立会话），顺序执行验证不了竞态。
