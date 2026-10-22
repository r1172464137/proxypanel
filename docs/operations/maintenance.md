# 维护与保留清理

面板会自己清掉过期的临时数据和已经结束的历史记录。没有任何一步需要你手动触发，
但你需要知道它删什么、保留多久，以及哪些数据**永远不会被删**。

## 受上限保护的清理

保留天数在设置里配（`retention_days`），硬边界 `7 … 3650` 天
（`store.MinRetentionDays` / `MaxRetentionDays`）。配太小会被夹到 7，
配成荒谬的大数会被夹到 3650 —— 防止一个笔误把数据永久钉住。

| 表 | 删除条件 |
|---|---|
| `previews` | 未消费且已过期（超过安全边界：`now - 1 day`） |
| `sessions` | 已过期，或已撤销且撤销时间超过安全边界 |
| `registration_codes` | 未使用且已过期 |
| `invitations` | 从未使用、未撤销且已过期 |
| `events` | 状态为 `recovered` / `acked`，且关联的 `notification_outbox` 都已发出（`state <> 'sent'` 的不删） |
| `audit_logs` | 早于保留期限 |
| `batch_items` / `batches` | 已完成、`completed_at` 早于保留期限，且没有 pending/dispatched 的关联任务 |

共同点：**只删「结束了的」**。任何还可能被引用的行都不删 ——
比如事件还有没发出去的通知，就整条留着；批次还有未完成的任务，就不动它。

## 永远不会被自动删除的

- `members`、`nodes`、`servers`、`subscriptions` —— 靠软删（`archived`），不物理删
- `usage_entries`、`usage_batches` —— 用量账本是计费依据，清掉就无法解释历史
- `credentials` —— 只标 `revoked`，保留轮换历史
- `agent_receipts` —— Agent 上报回执，用于幂等去重

想真正减少体积只能备份后重建数据库，没有「一键清理用量」的开关。这是刻意的：
「上个月用了多少」这件事必须永远答得出来。

## 告警与通知

`events` 存告警状态，`notification_outbox` 是待发送队列（持久化，不依赖内存）。
事件被 `ack` 之后才算处理完；通知没发出去的不会被清理掉。

管理端可以 `POST /admin/events/{id}/ack` 手动确认。Telegram 渠道需要在设置里
配真实的 bot token 和 chat id —— 本地 HTTP 模拟测试不能当作真实送达验证。

## 任务与批次

批量的状态流转：预览 → 确认 → 创建任务 → 逐节点分发 → 完成 / 部分失败。

失败的任务在 `GET /admin/tasks` 里能看到，可以 `POST /admin/tasks/{id}/retry`。
重试是幂等的：同一个任务重试不会产生重复的授权变更。

Agent 离线时任务会停在 `pending`；Agent 回来拉快照后会自己收敛，不需要手工重放。

## 健康检查

| 端点 | 含义 | 用途 |
|---|---|---|
| `/healthz` | 进程活着 | 容器存活探针、entrypoint 等它就绪 |
| `/readyz` | PostgreSQL / Redis 都可用 | 就绪探针、外部监控 |

**不要用 `/healthz` 判断服务可用** —— 数据库挂了它照样 200。
反过来也别用 `/readyz` 做存活探针，依赖抖动会导致容器被反复重启。

## Agent 侧要看的指标

`GET /admin/agents` 里：

| 字段 | 正常状态 | 异常时意味着 |
|---|---|---|
| `applied_revision` vs `desired_revision` | 相等 | 不等 = 还没收敛，等下个轮询周期 |
| `last_heartbeat_at` | 秒级推进 | 停住 = Agent 挂了或网络断了 |
| `queue_depth` | 0 或很小 | 持续增长 = 用量上报失败，检查 `/agent/usage` 的响应 |
| `instance 结果` | 每个节点都是 running | 有 failed = 该节点的 Xray 起不来，看原因（多为证书路径或端口占用） |

**Agent 在线不等于节点可用。** 心跳正常但某个实例 failed 是很常见的状态，
必须看实例结果。

## 常见故障

| 现象 | 先查 |
|---|---|
| 成员说连不上，但面板显示正常 | 该成员是否在本节点有 `active` 凭据；节点实例是否 running；`external_port` 与转发是否对上 |
| 改了成员设置没生效 | `applied_revision == desired_revision`；不等就等，或看 Agent 日志 |
| Agent 一直不来 | 注册码是否一次性已用；`panel_url` 是否 https（http 仅回环）；私有 CA 是否通过 `SSL_CERT_FILE` 提供 |
| Agent 日志反复报 generation 不符 | 进出过恢复协调模式。走一次 `-accept-generation`，见 [架构](../design/architecture.md) |
| 面板 500 且 `/readyz` 失败 | 数据库或 Redis；先 `docker compose logs postgres` |
| 登录一直被限流 | Redis 里的 `pp:rl:login:<ip>:<user>` 键，按 IP+用户名计数，60 秒窗口 |
| 容器起来但全站 502 | entrypoint 等不到面板 `/healthz`；多数是数据库连不上或 `PANEL_MASTER_KEY` 格式错 |
