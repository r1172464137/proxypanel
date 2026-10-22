# 数据模型

31 张表，全部在 PostgreSQL。本文只讲「哪张表为什么存在」和几个需要理解才能正确操作的约定；
逐列定义看 `migrations/*.sql`，那是唯一权威。

## 关系概览

```
servers ──┬── nodes ──┬── core_instances      （每节点一个 Xray 实例）
          │           ├── credentials ──────── 成员 × 节点 的独立凭据
          │           └── node_group_members ─┐
          └── agents ────────────────────────┐│
                                             ││
node_groups ──┬── node_group_members ◀───────┘│
              └── subscription_node_groups ── subscriptions
                                                    │
members ──┬── credentials                          │
          ├── sessions                       member_bindings
          ├── subscription_tokens ───────────────────┘
          ├── quota_adjustments
          └── usage_entries ──── usage_batches ──── agent_receipts

control_state      （单行：revision_counter、control_generation、recovery_mode）
batches ── batch_items
previews, idempotency_records, tasks, events, notification_outbox
audit_logs, settings, maintenance_state, invitations, registration_codes
```

## 核心表

| 表 | 存什么 | 为什么单独 |
|---|---|---|
| `servers` | 一台节点机的身份（名称、地址、区域） | 节点归属于它；Agent 也注册到它下面 |
| `nodes` | 一个可连接的代理入口（协议、端口、传输、加密层） | 授权和凭据的最小粒度 |
| `core_instances` | 节点在某台服务器上的 Xray 进程状态 | 一个节点将来可能迁机，实例与节点不是 1:1 语义 |
| `agents` | 节点机上的 Agent（token 哈希、已应用 revision、心跳） | 与会话体系完全隔离的认证域 |
| `credentials` | 成员 × 节点 的凭据（**加密存储**） | 改一个人不影响其他人；一轮换就精确撤权 |
| `node_groups` + `node_group_members` | 节点分组 | 订阅挂组，成员挂订阅，避免逐节点授权 |
| `subscriptions` + `subscription_node_groups` | 订阅规则（配额、周期、可用节点组） | 一组人共享同一套规则 |
| `subscription_tokens` | 成员的订阅拉取令牌（哈希） | 可独立轮换、可多枚并存 |
| `members` | 账号、角色、状态、配额覆盖 | |
| `sessions` | 登录会话（token 哈希 + csrf） | 在数据库而非 Redis，重启面板不掉登录 |
| `usage_batches` / `usage_entries` | Agent 上报的用量批次与逐条明细 | 按 `batch_id` 幂等入账，重试不会重复计 |
| `control_state` | **单行**：`revision_counter`、`control_generation`、`recovery_mode` | 全局状态必须原子更新，单独一行 + 行锁最简单 |
| `audit_logs` | 谁在什么时候改了什么 | 出了问题要能查，不能只靠应用日志 |

## 软删 vs 硬删

**软删**（有 `archived` 列）：`servers`、`nodes`、`subscriptions`、`members`、`agents`。

原因：这些对象被历史数据引用 —— 用量账本、审计日志、凭据轮换记录都指向它们。
真删会让历史无法解释（「这条 3GB 的用量属于谁的哪个节点？」）。

**硬删**：`node_groups`、`templates`、`invitations` 等。它们要么不被历史引用，
要么被引用时由外键直接拒绝删除（`NODE_IN_USE`）。

### 软删对象不可修改

`archived = true` 的对象再 `PATCH` 返回 `404 NOT_FOUND`。store 层有统一守卫
（`archivedGuard`）：`wasArchived && nowArchived` 就拒绝，唯一放行的是
`archived` 改回 false（恢复）。

为什么要在 store 层拦：软删只把行标了标记，行还在，按 id 的更新依然能命中。
不拦的话 `DELETE` 之后的 `PATCH` 仍会成功（实测过：`notes` 真的被写进去了），
并发时表现为「DELETE 和 PATCH 都成功」。

> 目前 HTTP 面不暴露 `archived` 字段（`PATCH` 收到它会按未知字段 400 拒绝），
> 所以「恢复」只能走 store 层调用。守卫里的恢复分支是给将来加恢复入口留的。

## 乐观并发：`version`

有 `version` 列的表：`servers`、`nodes`、`node_groups`、`subscriptions`、`members`、
`agents`、`templates`、`settings`。

写入时 `UPDATE ... WHERE id=$1` 并校验 `version`，成功后 `version + 1`。

**对外接口强制要求带 `version`**：`PATCH`/`PUT` 缺少或传 `0` 直接
`400 VALIDATION_FAILED`（`requireVersion`）。这是 2026-10 修的一个真缺陷 ——
之前 store 用 `expectedVersion == 0` 表示「跳过检查」，这个内部约定漏到了 API 面，
客户端省略 `version` 就能让两个并发 PATCH 都成功、后写静默覆盖先写。

内部调用（归档、批量 apply、改密码）仍然传 `0` 走「跳过」分支，但它们不经过 HTTP 层。

## 凭据加密

```
members.password_hash   → bcrypt，单向，不可解密
credentials.secret_enc  → AEAD 加密（bytea），密钥由 PANEL_MASTER_KEY 派生
agents.token_hash       → 哈希，不可逆
sessions.token_hash     → 哈希
subscription_tokens     → 哈希
```

**只有 `credentials.secret_enc` 是可解密数据**，靠 `PANEL_MASTER_KEY`。
所以：

- 丢了 master key → 所有成员的代理密码/UUID 无法恢复，只能全部轮换
- 密码本身是哈希，丢了 master key 也能正常登录（这点常被说错）

master key 必须与数据库一起备份，且不能每次启动重新生成。

## 周期计算

`members` 上的 `period_start` / `period_end` / `period_timezone` / `period_anchor_day`
决定「这个成员的计费周期从哪天到哪天」。算法版本记在
`usage_entries.algorithm_version`，跨版本的历史数据不会被重新解释。

`pending_period_rule` 用来存「下次周期切换时该用什么规则」，避免在周期中途改锚点
导致已用量被重新归属。

## 备份时的最小集

必须一起备份：`pg_dump` 全量 + `PANEL_MASTER_KEY`。

只备份前者的后果：数据在，但所有凭据解不开，成员全部需要重置。
