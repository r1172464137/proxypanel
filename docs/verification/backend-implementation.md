# 面板后端实现报告（Go panel）

> 最终集成更新：本文主体为阶段实现记录；以 [统一验收记录](integration.md) 为当前状态。真实 API/store/maintenance PostgreSQL 回归现已通过。普通变更与修订发布同事务、自动周期推进及 15 秒 tick、新节点关联、完整内部列表、批量快照证据加载、恢复随机代次原子更新均已补齐并验证。快照 204 比较客户端 revision **和** control_generation；维护告警/outbox/有界清理已接入。下文“未注入数据库”“通知投递未实现”“无清理任务”等是早期状态，不再适用；外部 Telegram 真送达与真实灾备仍未验证。


状态：**主体完成**（含批量、限流、恢复门禁、回执硬化、TLS、集成测试，以及第 7 节的前端实报缺陷定向修正）。仍有明确未完成项，见第 6 节。

## 1. 目录与文件（本任务独占范围）

- `cmd/panel/main.go` — 入口：env、master key、PG 连接+迁移、空表 bootstrap 管理员、**传输模式**（`PANEL_TLS_CERT/KEY` 直连 HTTPS / `PANEL_TRUST_REVERSE_PROXY=true` 受信任反代回环 / `PANEL_DEV=true` 开发）、Redis 限流器装配、15s 分发超时清扫协程、优雅退出。
- `migrations/0001_init.sql`、`0002_batch_preview.sql` — 全部业务表 + 批量/回执补充列。
- `internal/panel/domain/` — 实体、线上 JSON、`Object`（保留未知嵌套字段）、统一错误码。
- `internal/panel/period/` — `period/v1` 月周期（锚点、月末裁剪、DST、跨周期分摊）。
- `internal/panel/access/` — 唯一授权算法 `access/v1`。
- `internal/panel/protocol/` — 协议/传输/安全校验、服务端秘密抽取、X25519、客户端可见 Endpoint。
- `internal/panel/xrayconf/` — 完整服务端 Xray 文档（无管理 API）。
- `internal/panel/clientconfig/` — xray / sing-box / Mihomo / v2rayN 输出与不可表达参数报错。
- `internal/panel/usagecalc/` — 批次校验、载荷指纹、分账。
- `internal/panel/secrets/`、`ids/` — AES-256-GCM、token 哈希、bcrypt(12)、不透明 ID。
- `internal/panel/limit/` — **登录限流**：Redis 固定窗口 + 受限本地降级（表满即拒绝）。
- `internal/panel/store/` — PostgreSQL 持久层：资源、成员/会话/邀请/令牌/凭据、账本、**批量预览与提交**、回执、任务/事件/审计、设置、恢复模式、超时清扫、快照。
- `internal/panel/api/` — ServeMux 路由、包络、Cookie/CSRF、管理员/成员/Agent 中间件、**恢复模式写入门禁**、批量端点、全部资源端点。

## 2. 本轮补齐的 7 项要求

1. **批量全流程**（`/api/v1/admin/batches/preview`、`POST /batches`、`GET /batches/{id}`）
   - 六种操作：`assign_subscription`、`set_active`、`set_expiry`、`clear_override`、`update_subscription`、`update_node_group`。
   - 预览持久化到 `previews`（plan/conclusion/rules_version/member_set_digest/10 分钟 TTL），返回 `id/version/affected_count/affected_nodes/exceptions/warnings/expires_at` 以及逐成员 before/after、后果、节点增减、Agent 数。
   - 提交在**单个 PG 事务**内：幂等键查询（同键同指纹重放原结果 `idempotent_replay=true`，同键异指纹 409 `IDEMPOTENCY_KEY_REUSED`）→ 预览行 `FOR UPDATE` → 过期 409 `PREVIEW_STALE` → `rules_version`/`member_set_digest` 校验 → 重算结论集合（超额/失去访问变化 → 409 `PREVIEW_CONCLUSION_CHANGED`）→ 应用变更 → 凭据按新授权收敛（撤销/新建）→ 额度状态与撤权事件 → 修订推进 → 写 `batches`/`batch_items`/`tasks`/审计。
   - 最后管理员保护：`set_active(false)` 命中最后管理员时整批 409 `LAST_ADMIN`（事务内计数，不留半成品）。
   - 逐对象状态：`batch_items` 按成员×节点×Agent 记录 `pending/applied/succeeded/failed/timeout/superseded`，`GET /batches/{id}` 返回逐对象结果与汇总。
2. **直接编辑必须走预览**：`PATCH /subscriptions/{id}` 在存在绑定成员且补丁触及 `node_group_ids/quota_bytes/timezone/anchor_day/default_valid_days/access_enabled/archived` 时返回 409 `PREVIEW_REQUIRED`；`PATCH /node-groups/{id}` 在 `node_ids` 变化且被订阅引用时同样返回 409（含 `preview_endpoint` 提示）。
3. **真实 Redis 登录限流**：`internal/panel/limit` 使用 `REDIS_URL`（INCR+EXPIRE 固定窗口），Redis 失败自动降级到有界本地计数（`MaxLocalKeys=4096`，满则拒绝，不放行）；`/readyz` 用真实 PING 报告 `ok/unavailable/disabled`；未配置 Redis 时明确报告本地降级并在启动日志提示。
4. **恢复模式**：`requireWritable` 中间件拦截全部管理员写接口（`/recovery-mode/exit` 除外）→ 409 `RECOVERY_MODE`；`BuildSnapshot` 在恢复模式返回 409，**不向节点推送可能过期的授权**；进入时 `control_generation+1` 并同步到 Agent；退出需 `confirm:"verified"` + `agent_revisions_checked:true` + `ledger_checked:true`，退出后推进修订号，Agent 重新拉取全新计算的授权而不是旧权限。
5. **Agent 回执硬化**：拒绝 `revision > desired_revision` 的回执与 `control_generation` 不匹配（409 `REVISION_STALE`）；`applied_at` 超前超过 5 分钟拒绝；成功回执推进 `applied_revision`，更新 `core_instances.state/config_revision/applied_at` 且**每个新配置修订只推进一次 `generation`/`last_started_at`**；失败回执写事件、任务与逐对象失败；低版本回执只入审计不推进状态；15s 清扫把超时任务/对象标记 `timeout`、被更高修订取代的标记 `superseded`，并同步批量状态为 `completed`。
6. **可选环境变量驱动的 PostgreSQL 集成测试**：`internal/panel/api/integration_test.go`，未设置 `PANEL_TEST_DATABASE_URL` 时 skip；强制校验 DSN 必须是 `127.0.0.1:15432/proxypanel`；每次运行创建独立 schema（`pptest_<random>`）作为 `search_path`，结束时只 `DROP` 自己的 schema；不读取 `.local`、不 SSH。覆盖：登录与 CSRF、节点/组/订阅/成员创建与生效判定、批量预览→提交→结果、直接编辑 409、过期预览 409、幂等重放与幂等冲突、用量去重与异载荷 409、快照（含空授权 `users: []`、无变化 204）、伪造 revision 被拒、恢复模式写入与快照拦截。
7. **TLS/传输**：支持 `PANEL_TLS_CERT`/`PANEL_TLS_KEY` 直接 HTTPS（启动时校验文件可读）；或显式 `PANEL_TRUST_REVERSE_PROXY=true` 且监听回环（反代终止 TLS）；`PANEL_DEV=true` 仅开发；其余情况拒绝启动并给出具体配置指引，不会静默明文暴露公网。
8. **VLESS 原生字段**：`protocol.Sanitize` 只抽取服务端私钥/PSK，其余原生字段（`tcpSettings`、`realitySettings` 的 dest/xver/serverNames/minClientVer/maxTimeDiff/shortIds/fingerprint/spiderX、`sockopt` 的 mark/tcpFastOpen/tcpNoDelay/tcpKeepAliveIdle/tcpUserTimeout/domainStrategy/tcpMptcp、`decryption`、`fallbacks` 全部子字段）原样保留并在 `xrayconf` 中进入核心文档；有专门测试断言逐字段保留、私钥只在服务端文档出现。

## 3. 实际执行的命令与结果（工作区，未改 go.mod）

```
. .\scripts\Use-DevEnvironment.ps1
go build ./cmd/panel ./internal/panel/...   → exit 0
go vet   ./cmd/panel ./internal/panel/...   → exit 0
go test  ./internal/panel/...               → ok（含 api/limit/protocol/xrayconf/store 新测试）
```

`go test ./internal/panel/...` 输出（摘要）：`access`、`api`、`clientconfig`、`limit`、`period`、`protocol`、`store`、`usagecalc`、`xrayconf` 全部 ok；`domain/ids/secrets` 无测试文件。

`go build ./...` 当前失败于 `internal/agent/state/store.go:97`（Agent 模块，非本任务范围，作者在并行修改）。

集成测试（需要主负责人注入凭据后执行）：

```
$env:PANEL_TEST_DATABASE_URL = "postgres://proxypanel:<password>@127.0.0.1:15432/proxypanel?sslmode=disable"
go test ./internal/panel/api/ -run TestPanelFlow -v
```

本环境未注入凭据，集成测试**只做了编译与 skip 验证，未实际连接数据库**（不访问远程服务、不读秘密文件）。

## 4. 关键实现语义备忘

- `rules_version` = `control_state.revision_counter`；任何影响授权的变更（节点、节点组、订阅、成员、批量、额度、超权撤权）都会推进它，因此预览会按契约变成 `PREVIEW_STALE`。
- `member_set_digest` = 排序后的 `member_id:version:subscription_id:subscription_version` 的 SHA-256。
- 每次成功的批量提交都会推进 `revision_counter`，因此**其他未提交的预览会立即变为 `PREVIEW_STALE`**（失败关闭，符合"相关变化需重新预览"）。
- 预览只锁规则与成员集合；已用量变化不锁死操作，提交时重算结论，结论变化才要求重新确认（`PREVIEW_CONCLUSION_CHANGED`）。
- 换订阅/改周期规则：保留当前周期与已用量，新规则写入 `pending_period_rule`（下一周期生效）；额度变更立即生效。
- 额度调整只写 `quota_adjustments` 并按周期叠加到生效额度，不删除原始用量。
- 用量：`(agent_id, batch_id)` 主键 + 载荷 SHA-256；同 ID 异载荷 409 `BATCH_PAYLOAD_MISMATCH`；归属/历史映射/负数/时钟漂移/大小校验；跨边界分摊 `estimated+boundary_split`；关闭周期补报 `correction`。
- 秘密：REALITY 私钥、SS2022 PSK、订阅令牌、Telegram token 均以主密钥加密存储；审计与错误信息不含明文；成员输出只用 `client_settings`。

## 5. HTTP 契约对照（已注册端点）

公共：`POST /auth/login`、`GET /auth/session`、`POST /auth/logout`、`POST /auth/redeem`、`GET /healthz`、`GET /readyz`、`GET /sub/{token}?format=`。
管理：`overview`、`servers`(CRUD + `registration-codes`)、`agents`、`core-instances`、`nodes`(CRUD)、`node-groups`(CRUD)、`subscriptions`(CRUD)、`members`(CRUD + `subscription-token/rotations` + `credentials/rotations` + `quota-adjustments`)、`templates`(CRUD)、`invitations`(列表/创建/`revoke`)、`batches/preview`、`batches`、`batches/{id}`、`usage`、`events`(+`ack`)、`tasks`(+`retry`)、`audit-logs`、`settings`(GET/PUT)、`recovery-mode/enter|exit`。
成员：`me/status`、`me/nodes`、`me/usage`、`me/subscription`、`me/subscription/token/rotations`、`me/credentials/rotations`、`me/password`、`me/sessions`、`me/sessions/{id}`。
Agent：`agent/register`、`agent/snapshot`、`agent/applied`、`agent/usage`、`agent/heartbeat`。

## 6. 仍未完成 / 已知限制

1. **Telegram 通知未发送**：`settings.telegram_*` 可写可加密保存，但没有实际投递与送达验证（契约要求"配置后验证实际送达"）。
2. **预览/批量历史清理**未实现（`previews` 过期行、`batches` 保留策略依赖 `retention_days`，目前无清理任务）。
3. **超时清扫是进程内 15s 协程**；多面板实例会并发执行同一批幂等 UPDATE（结果正确、可能重复写事件），未做基于 advisory lock 的单执行者。
4. **快照为短轮询语义**：只实现 `revision` 比较与 204，未实现长轮询等待。
5. **`update_node_group`/`update_subscription` 预览范围**限于引用该组的订阅下的现有成员；不影响未绑定成员（与"影响现有授权"契约一致，但对"未来绑定"的提示仅体现在订阅字段本身）。
6. **`config_revision`/`generation`** 只在成功回执时推进；Agent 未上报的实例启动信息（真实进程 PID/版本）仍不可见。
7. **集成测试未在本机执行**（无注入凭据）；断言按契约编写，实际通过与否需主负责人跑一遍。若首次运行暴露差异，应优先修 store SQL 而不是改断言。
8. 未实现 `GET /servers/{id}/runtime`、`POST /core-instances/{id}/restart`、`POST /members/{id}/suspend|resume|proxy-access/*`、`/usage/adjustments` 列表端点、`/events/{id}/silence`、`/tasks/{id}/cancel`、`/members/{id}/binding`、`/members/{id}/effective-access` 等提案里出现但冻结契约未列出的端点；对应能力目前通过 PATCH 成员/任务重试覆盖。

## 7. 前端实报缺陷定向修正（PATCH null 语义 / 成员列表派生值 / usage 时间过滤）

### 7.1 根因与修法

`*int64` / `*string` / `*time.Time` 无法区分"字段缺失"与"显式 null"，两者都解码成 `nil`，因此 `{"quota_bytes":null}` 被当成没传。修法是 `internal/panel/api/admin2.go` 中的泛型：

```go
type nullable[T any] struct { Set, Null bool; Value T }
func (n *nullable[T]) UnmarshalJSON(raw []byte) error // null -> Set+Null
```

`decodeJSON` 仍启用 `DisallowUnknownFields`（单元测试断言未知字段照旧被拒绝）。

| 端点 | 字段 | 缺失（omitted） | 显式 `null` | 具体值 |
|---|---|---|---|---|
| `PATCH /subscriptions/{id}` | `quota_bytes` | 不变 | 不限量 | 字节数（<0 → 400） |
| | `default_valid_days` | 不变 | 无默认有效期 | 正整数天数（<=0 → 400） |
| | `template_id` | 不变 | 解除模板 | 模板 id（`""` 等同 null） |
| `PATCH /members/{id}` | `expires_at` | 不变 | 清除个人到期 | RFC3339 |
| | `quota_override_bytes` | 不变 | 恢复继承并关闭覆盖 | 设值并开启覆盖（<0 → 400） |
| | `subscription_id` | 不变 | 解绑并清空周期/挂起规则 | 校验可绑定后绑定 |
| `PATCH /servers/{id}` | `expires_at` | 不变 | 清空 | RFC3339 |
| | `cost_minor` | 不变 | 清空 | 最小货币单位（<0 → 400） |

补充约定：
- `quota_override_enabled=true` 但没有覆盖值 → 400（否则会静默变成"不限量覆盖"）；创建成员同样校验。
- `node_group_ids: null` 仍视为缺失（数组没有 null 语义，`[]` 表示清空）。
- `quota_override_enabled: null` 视为缺失（bool 无 null 语义）。
- `PATCH member subscription_id` 绑定时：可绑定校验（归档/禁止新绑定 → 409 `SUBSCRIPTION_NOT_BINDABLE`）；无有效周期则按新订阅规则初始化当前周期，已有运行中周期则保留并把新规则写入 `pending_period_rule`（下一周期生效）。
- 解绑（`subscription_id:null`/`""`）同时清空周期字段与挂起规则，账本用量保留；随后凭据按新授权收敛（全部撤销），会话不变。
- `affectsMembers()` 计入显式 null：`quota_bytes`/`default_valid_days` 的 null 会让有绑定成员的订阅 PATCH 继续返回 409 `PREVIEW_REQUIRED`（走预览）。`template_id` 属于展示层，null 会被真正写入但不强制预览。
- 批次配合：`internal/panel/api/batches.go` 新增 `validatePreviewValues()`，在 `POST /batches/preview` 前置校验 `values.id`、`values.patch` 形状，**显式 null 原样透传**给 store（store 的 `applySubscriptionPatchObject` 本来就按 `null` 处理，未修改该文件）。

### 7.2 成员列表派生值（保持扁平响应）

`GET /api/v1/admin/members` 仍是 `{"items":[member...]}`，没有改成嵌套。每行在原有成员字段之外，用**同一授权算法**填充：`used_bytes`、`effective_quota_bytes`（null=不限量）、`period_start`/`period_end`/`period_timezone`/`period_anchor_day`、`access_reasons`、`proxy_access`、`allowed_node_ids`、`over_quota`。数组字段恒为 `[]`（不再是 null）；授权计算失败时 `proxy_access="unknown"` 且数组为空数组。`GET /members/{id}` 保持原来的 `{member, access}` 形状不变。

### 7.3 usage 时间过滤

`GET /api/v1/admin/usage?from=&to=` 之前被忽略，现在生效：按周期起点过滤（`period_start >= from` 且 `period_start < to`）。时间接受 RFC3339 / RFC3339Nano / `YYYY-MM-DD`（UTC 零点）；非法值 → 400 `VALIDATION_FAILED`，`details.field_errors.from|to` 指明字段；`from >= to` → 400 指向 `to`；`limit` 非数字忽略、有效值下传（store 夹在 1..200）。

### 7.4 本轮检查

```
gofmt -w internal/panel/api/{admin.go,admin2.go,batches.go,patch_test.go}   → 无差异
go build ./cmd/panel ./internal/panel/...                                    → exit 0
go vet   ./cmd/panel ./internal/panel/...                                    → exit 0
go test  ./internal/panel/... -count=1                                       → 全部 ok
```

新增测试 `internal/panel/api/patch_test.go`（9 个，全部通过）：
`TestSubscriptionPatchDistinguishesOmittedNullAndValue`、`TestSubscriptionPatchRejectsUnknownInvalidsAndKeepsStrictDecoding`、`TestSubscriptionAffectsMembersCountsExplicitNull`、`TestMemberPatchClearsExpiryOverrideAndBinding`、`TestMemberPatchOmittedFieldsAndValidation`、`TestServerPatchNullSemantics`、`TestUsageWindowParsing`、`TestDecorateMemberSetsFlatDerivedValues`、`TestValidatePreviewValuesKeepsExplicitNulls`。
测试使用真实的 `decodeJSON`（含 `DisallowUnknownFields`），不是另写一套解码路径。

### 7.5 本轮遗留

- `template_id` 的 null 不触发 `PREVIEW_REQUIRED`（展示层字段，实际值会被写入）。
- `PATCH member subscription_id` 只做"缺失/过期周期初始化 + 新规则挂下一周期"；完整周期重算仍在批次路径（`internal/panel/store/batch_submit.go::syncMemberCycle`，非本任务文件）。
- `internal/panel/store/batch_preview.go::applySubscriptionPatchObject` 未修改（不在本任务文件清单内），读代码确认其已按 `null` 处理；若要统一校验规则需该文件所有者处理。
- `store/members.go`、`store/resources.go`、`store/usage.go` 无需改动即已支持（列写入用 `nullableString/nullableInt64/nullableTime`，usage 的 `UsageFilter.From/To` 早已实现但从未被 API 传入）。
- 未触碰 `cmd/panel`：`PANEL_DEV=true` 时公网绑定放行的问题按分工由父代理处理。
