# 运维后台（维护任务）实现与验证

> 最终集成更新：主入口已接入 `maintenance.New(st, nil).Run(ctx)`。真实 PostgreSQL 集成（告警、单执行者、清理、通知失败重试）已通过；修复了清理 SQL 中间参数类型与重复告警 adopt 统计。下文父代理接入/可选测试未注入的阶段说明不再代表当前状态。Telegram 接收端仅为本地 HTTP 模拟，外部实际送达仍未验证；详见 [统一验收记录](integration.md)。


范围：主计划 F11（监控、事件与通知）与 F12（页面与可用性）中属于后端周期任务的部分：
实际告警状态机、Telegram 通知 outbox、保留期清理。前端与 API 展示不在本文件范围内。

本次新增（独占文件，未修改任何现有 store/api/cmd/web 文件）：

| 文件 | 作用 |
| --- | --- |
| `internal/panel/store/maintenance.go` | PG 权威的持久化原语：告警观察、状态机写入口、通知 outbox、有界清理、运行记录 |
| `internal/panel/store/maintenance_test.go` | 保留期下限、清理白名单/有界性/FK 顺序、告警键等单元测试 |
| `internal/panel/maintenance/maintenance.go` | `Service`、`RunOnce`、`Run`、纯函数 `reconcile`、Telegram 投递与脱敏 |
| `internal/panel/maintenance/maintenance_test.go` | 状态机、退避、HTTP 200/429/500/网络错误、脱敏、超时等单元测试 |
| `internal/panel/maintenance/maintenance_integration_test.go` | 可选 PG 集成测试（独立 schema，仅 `PANEL_TEST_DATABASE_URL`） |
| `migrations/0003_maintenance.sql` | `notification_outbox`、`maintenance_state`、清理所需索引 |

## 1. 给父代理的调用方式

```go
import "proxypanel/internal/panel/maintenance"

// st 是已打开的 *store.Store（cmd/panel 中现有变量）。
svc := maintenance.New(st, nil)          // 第二个参数可注入 *http.Client，nil 表示内部创建 10s 超时客户端
go svc.Run(ctx)                          // 立即执行一次，之后每 15s 一次，ctx 取消即退出

// 或者使用父代理自己的 ticker（与现有 st.SweepTimeouts 的 15s 循环合并）：
if _, err := svc.RunOnce(ctx); err != nil {
    log.Printf("maintenance: %v", err)
}
```

- `RunOnce` 返回 `maintenance.Stats`（raised/adopted/recovered/enqueued/sent/failed/skipped + 清理计数）。
  另一个进程/实例持有 advisory lock 时返回 `Stats.Skipped=true` 且 `err == nil`，不是错误。
- 失败语义：只有 Telegram 返回 **HTTP 200 且 JSON `ok=true`** 才计入 `Sent`；429/5xx/网络错误/非 JSON
  都记为失败并按退避重排，绝不报告送达。
- 端点固定：`https://api.telegram.org/bot<token>/sendMessage`。`New` 没有 baseURL 参数，
  `baseURL` 字段不导出且只能由包内测试覆盖，因此调用方无法把通知重定向到任意地址（防 SSRF）。
- 不需要新增环境变量；token/chat_id/开关/retention 都读 `settings` 表（token 用现有 `secrets.Key` 解密）。

建议 wiring（父代理执行，本文件不修改 `cmd/panel/main.go`）：

```go
// 现有 15s 循环里，或新起一个 goroutine：
maintenanceSvc := maintenance.New(st, nil)
go maintenanceSvc.Run(ctx)
```

## 2. 告警状态机

事实来源是 PostgreSQL，告警状态持久化在 `events` 表：同一 (kind, subject) 只有一个 `state='open'` 的事件，
由 `events.dedupe_key = "<kind>:<subject_id>"` 和既有部分唯一索引 `events_open_dedupe` 保证；
恢复时把该事件置为 `state='recovered'`（`acked_by='maintenance'`）并**另外写入一条恢复事件**，
因此重启、跨进程、每 15s tick 都不会重复产生告警事件。

| kind | subject | 触发条件 | severity | 恢复条件 |
| --- | --- | --- | --- | --- |
| `agent_offline` | `agent` | `COALESCE(last_heartbeat_at, created_at) <= now-45s`（注册后 45s 宽限） | warning | 心跳恢复 |
| `core_instance_failed` | `core_instance` | `core_instances.state = 'failed'` | error | state 变化 |
| `member_expired` | `member` | 到期候选后由 access 算法确认 `binding_expired` | warning | 续期/解绑 |
| `over_quota` | `member` | 超额候选后由 access 算法确认 `over_quota` | warning | 周期重算/调整额度 |
| `server_expiring` | `server` | `servers.expires_at <= now+7d`（已过期升级为 error） | warning/error | 续期/归档 |

要点：

- 成员到期/超额**只**使用已有 `internal/panel/access` 决策确认（候选 SQL 仅做索引友好收窄），不重复实现授权规则。
- `over_quota` 直接复用用量账本已写入的 `over_quota:<member>` 去重键：维护任务**接管（adopt）**该事件，
  不会为同一条件再建一条事件；恢复时把该事件关闭。
- 扫描有界：每类候选最多 `AlertScan`（默认 200）行。命中上限时该类标记为 `Incomplete`，
  **禁止**据此自动恢复——"没扫到"不等于"已恢复"。
- 其它组件写入的事件（如 `apply_failed:*`）不在 `MaintenanceAlertKinds` 内，不会被动到。

## 3. Telegram 通知 outbox

- 表 `notification_outbox`：`event_id UNIQUE` 提供幂等（一个事件最多一条通知，跨 tick/重启/多进程）。
  **故意不对 `events` 建外键**：事件被清理也不能让未完成通知消失。
- 事件正文由维护任务生成（kind/subject/消息/时间），只包含工单事实，**不含 token、密码、密钥**；
  发送前仍会把 token 字符串从正文中抹掉（防御性）。
- 是否发送**读最新 settings**：`telegram_enabled=false`、chat_id 为空或未配置 token 时保持 pending 不发，
  不消耗重试次数；配置变更后的下一个 tick 即按新配置生效。
- 发送：`POST` JSON `{chat_id, text, disable_web_page_preview}`，请求上下文有界超时（默认 10s，`Limits.Timeout`）。
- 成功判定：HTTP 200 且 `ok=true` → `state='sent'`, `sent_at=now`。其它情况：
  - 429 读取 `parameters.retry_after`（或 `Retry-After` 头），夹在 [1s, 1h]；
  - 网络/5xx/200+`ok=false`：指数退避 30s→60s…封顶 1h；
  - 失败原因写 `last_error`（截断 300 字节），**不含 token、不含含 token 的 raw URL**：
    `*url.Error` 会被拆掉 URL 只保留内层原因，并且 token 会被替换为 `[redacted]`；
  - 达到 `Limits.MaxAttempts`（默认 8）后 `state='failed'`，行**保留不删**，便于审计。
- 领取用 `FOR UPDATE SKIP LOCKED` + 5 分钟租约，多进程不会重复发送；attempts 在领取时计数，
  发送进程崩溃也会退避而不是热循环。
- 每 tick 最多发送 `Limits.Notifications`（默认 5）条，剩余继续排队。

## 4. 保留清理（有界、白名单）

每次 tick 在同一个 advisory lock 事务内执行，每条语句都带 `LIMIT`（默认 500，`Limits.Bounds.Cleanup`）。
`$1 = now-1d`（安全边界），`$2 = now-retention_days`，`$3 = limit`。

| 表 | 删除条件 |
| --- | --- |
| `previews` | 未消费且 `expires_at < now-1d`，或已消费且 `consumed_at < now-retention` |
| `sessions` | `expires_at < now-1d` 或 `revoked_at < now-1d` |
| `registration_codes` | 未使用且 `expires_at < now-1d`；已使用且 `used_at < now-retention` |
| `invitations` | 未使用/未撤销且过期 > 1d；已使用且 `used_at < now-retention`；已撤销 > 1d |
| `events` | 仅 `state IN ('recovered','acked')` 且 `created_at < now-retention`，且没有未送达的 outbox（`open` 告警永不删除） |
| `audit_logs` | `created_at < now-retention` |
| `batch_items` | 所属批次 `state='completed'` 且 `completed_at < now-retention`，且该批次没有 `pending/dispatched` 任务（先删条目） |
| `batches` | 同上条件 + 条目已清空（后删父行，FK 顺序正确） |

**绝不删除**：`usage_entries`、`usage_batches`、`credentials`、`idempotency_records`、`agent_receipts`、
`tasks`（含未完成任务）、`notification_outbox`（含未完成/失败行）、以及成员/节点/服务器/订阅等业务记录。
单元测试 `TestCleanupNeverDeletesProtectedTables` 会在这份白名单被改动时失败。

保留期下限：`store.MinRetentionDays = 7`，`NormalizeRetentionDays` 会把设置值夹到 [7, 3650]，
即使 settings 被写成 0 或负数也不会删到 7 天内的数据。

## 5. 单执行者与事务边界

- `store.InMaintenanceTx`：`BEGIN` → `pg_try_advisory_xact_lock(7401942)`（与迁移锁 7401941 区分）
  → 观察 → 状态机写入 → 入队 → 清理 → `COMMIT`。锁在事务结束时自动释放；
  拿不到锁立即返回 `store.ErrMaintenanceBusy`（`RunOnce` 转成 `Stats.Skipped`，不报错、不写任何行）。
- 网络投递在事务之外，靠 `notification_outbox` 的幂等与租约保证一致性：事务里不会做任何网络 I/O。
- 运行摘要写入 `maintenance_state`（单行）：runs、各类计数、`last_run_at`、脱敏后的 `last_error`。

## 6. 验证

本机 Go：`E:\SDK\Go\go1.27.3\go\bin`（PATH 中没有 go，需显式加目录）。

```powershell
$env:PATH = "E:\SDK\Go\go1.27.3\go\bin;" + $env:PATH
gofmt -l internal\panel\maintenance internal\panel\store\maintenance.go internal\panel\store\maintenance_test.go
go build ./...
go vet ./internal/panel/maintenance/... ./internal/panel/store/...
go test ./internal/panel/maintenance/... ./internal/panel/store/...
```

结果：

- `gofmt`：本任务新增/修改的文件均无需格式化（仓库既有若干文件未 gofmt，本任务未触碰）。
- `go build ./...`：通过。
- `go vet`（两个包）：通过。
- `go test ./internal/panel/maintenance/... ./internal/panel/store/...`：通过（单元测试）。
- PG 集成测试（`maintenance_integration_test.go`）：**本次未执行**——未设置 `PANEL_TEST_DATABASE_URL`，
  且按边界要求不读 `私钥.txt`、不做 SSH 隧道。设置后运行：

```powershell
$env:PANEL_TEST_DATABASE_URL = "postgres://proxypanel:<password>@127.0.0.1:15432/proxypanel?sslmode=disable"
go test ./internal/panel/maintenance/... -run Integration -v
```

集成测试只允许 `127.0.0.1:15432/proxypanel`，每次运行创建并删除自己的 schema（`ppmt_*`）。

### 未验证 / 不可宣称的部分

- **真实 Telegram 送达未验证**：没有真实 bot token / chat_id，所有"送达"结论来自 httptest 模拟
  `200 + {"ok":true}`、`429`、`500`。不得据此宣称生产环境实际送达。
- 真实网络下的退避与限流行为、以及 Docker/远程测试库上的清理性能未实测（无数据库连接）。
- 前端"事件/告警"页面展示与移动端可用性属于 F12 前端工作，不在本实现内。
