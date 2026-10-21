# 接口提案：面板 REST 与 Agent 快照 / 批次结构

- 状态：**仅提案**。字段名、路径、错误码、版本策略由主负责人**冻结**后生效；冻结前不得据此写实现契约或前端联调代码。
- 依据：冻结计划 `§2`、`§3 F01–F12`、`§8`、`§11.1`；业务语义见 `docs/specs/product.md`，测试见 `docs/specs/acceptance.md`。
- 秘密：示例中的令牌、凭据、密码一律为 `__REDACTED__` / `<opaque>` / `<uuid>` 占位，本项目不写入真实值。
- 本文不含实现，也不代表任何接口已可用。

---

## 1. 约定

| 项 | 提案 |
| --- | --- |
| 传输 | HTTPS（生产）；面板 ↔ 浏览器 JSON；面板 ↔ Agent JSON + 单向长轮询或短轮询 |
| 版本前缀 | `/api/v1/admin/**`（管理员）、`/api/v1/me/**`（成员本人）、`/api/v1/agent/**`（Agent） |
| 面板认证 | `HttpOnly; Secure; SameSite` 会话 Cookie + CSRF 令牌（写入类请求头 `X-CSRF-Token`） |
| Agent 认证 | `Authorization: Bearer <agent_token>`；建议附加 mTLS（待冻结）；凭据独立可轮换，仅授权本 Agent 的服务器范围 |
| 时间 | 一律 RFC 3339 UTC（`2026-11-01T00:00:00Z`）；业务时区以字段 `timezone` 显式给出 |
| 流量单位 | 整数字节（`*_bytes`），不传派生单位 |
| 金额/成本 | 可选字段，`currency` + 整数最小单位；不引入结算语义 |
| 分页 | `?limit=&cursor=`，响应 `{"items":[...],"next_cursor":"..."}`；不使用 offset 深分页 |
| 幂等 | 写入类请求支持 `Idempotency-Key`（UUIDv7，服务端保存键 + 请求指纹 + 结果） |
| 一致性前置 | 关键写请求携带 `If-Match` 或 body 内 `*_version`；不匹配返回 `409` |
| 并发安全 | 版本号为单调整数；客户端不得自行生成业务版本 |
| 错误结构 | 统一 envelope（`§2`）；HTTP 状态码表达类别，`code` 表达具体原因 |
| 日志 | 令牌、密码、凭据、私钥、完整订阅 URL 一律脱敏；`request_id` 贯穿 |
| 限流 | `429` + `Retry-After`；登录限流不因 Redis 故障而无界放行 |
| 缓存 | 订阅输出与敏感响应 `Cache-Control: no-store`；禁止共享代理缓存 |

分层与职责：

```text
浏览器 ──REST──> 面板(Go 单体) ──PG(权威) / Redis(可降级)──
                      │
                      └──HTTPS 长轮询──> Agent ──本机管理 API──> 官方 Xray 实例
```

面板不向 Agent 发送任意命令/路径；Agent 只接受声明式快照与受限任务（计划 `§3 F05`、`§4.1`）。

---

## 2. 通用响应与错误

成功（单对象）：

```json
{ "data": { "id": "mbr_01J9Z3K8V6Q0A1B2C3D4E5F6G7" }, "request_id": "req_01J9Z3K8" }
```

成功（列表）：

```json
{
  "items": [],
  "next_cursor": null,
  "request_id": "req_01J9Z3K8"
}
```

错误：

```json
{
  "error": {
    "code": "PREVIEW_STALE",
    "message": "预览已过期，请重新预览",
    "details": { "changed": ["rules_version"] },
    "retryable": true,
    "request_id": "req_01J9Z3K8"
  }
}
```

错误码提案：

| code | HTTP | 场景 | retryable |
| --- | --- | --- | --- |
| `UNAUTHENTICATED` | 401 | 无会话/会话已撤销 | 否 |
| `CSRF_FAILED` | 403 | CSRF 不匹配 | 否 |
| `FORBIDDEN` | 403 | 角色/数据范围不允许（不泄露对象是否存在） | 否 |
| `NOT_FOUND` | 404 | 对象不存在或在可见范围外 | 否 |
| `VALIDATION_FAILED` | 400 | 字段类型/取值域错误（`details.field_errors`） | 否 |
| `CONFLICT` | 409 | 版本冲突、状态不允许 | 视情况 |
| `LAST_ADMIN` | 409 | 停用/删除/降级会移除最后一名可用管理员 | 否 |
| `PREVIEW_STALE` | 409 | 预览后规则或成员集合变化 | 是（重新预览） |
| `PREVIEW_CONCLUSION_CHANGED` | 409 | 动态值导致超额/到期结论变化 | 是（重新确认） |
| `IDEMPOTENCY_KEY_REUSED` | 409 | 同幂等键不同指纹 | 否 |
| `SUBSCRIPTION_NOT_BINDABLE` | 409 | 归档/不允新绑定 | 否 |
| `PROTOCOL_NOT_SUPPORTED` | 400 | 协议/传输/安全组合不在已验证矩阵（待 D1） | 否 |
| `NODE_IN_USE` | 409 | 需先迁移/解除绑定 | 否 |
| `DB_UNAVAILABLE` | 503 | 不可确认写入（**不得**返回成功） | 是 |
| `RATE_LIMITED` | 429 | 限流 | 是 |
| `AGENT_AUTH_FAILED` | 401 | Agent 凭据失效/越权服务器 | 否 |
| `REVISION_STALE` | 409 | 回执/请求版本低于已知已应用版本 | 否 |
| `SNAPSHOT_SCHEMA_UNSUPPORTED` | 400 | Agent 不支持该 `schema_version` | 否 |
| `BATCH_PAYLOAD_MISMATCH` | 409 | 同 `batch_id` 不同载荷 | 否 |
| `BATCH_OWNERSHIP_MISMATCH` | 403 | 上报的节点/成员不属于该 Agent 或历史映射 | 否 |
| `BATCH_CLOCK_SKEW` | 422 | 采样时间超出漂移阈值，需隔离确认 | 是（人工） |

`data` 与 `error` 互斥；所有列表端点返回空数组而不是 `null`（空列表语义安全，见 `§6.3`）。

---

## 3. 管理员端点提案（`/api/v1/admin`）

| 方法 | 路径 | 用途 | 关键约束 |
| --- | --- | --- | --- |
| POST | `/auth/login` | 登录 | 限流；错误不区分账号存在性 |
| POST | `/auth/logout` | 退出 | 撤销当前会话 |
| GET | `/auth/session` | 当前身份与 CSRF 令牌 | — |
| GET | `/overview` | 总览聚合 | 只读 |
| GET/POST | `/servers`、`/servers/{id}`（PATCH/DELETE） | 服务器档案 | 删除=归档；有 Agent/节点需确认 |
| GET | `/servers/{id}/runtime` | Agent/核心实例/同步状态 | 分层状态 |
| POST | `/servers/{id}/agents/registration-codes` | 一次性注册码 | 单次使用、限时 |
| GET | `/core-instances`、`/core-instances/{id}` | 核心实例与世代 | — |
| POST | `/core-instances/{id}/restart` | 重启（影响预览先行） | 返回影响的节点/连接数 |
| GET/POST | `/nodes`、`/nodes/{id}`（PATCH） | 节点 CRUD/排序/启停/维护 | 协议组合须在矩阵内 |
| GET/POST | `/node-groups`、`/node-groups/{id}` | 节点组 | 成员变更需影响预览 |
| GET/POST | `/subscriptions`、`/subscriptions/{id}` | 订阅规则 | 有绑定成员时变更需预览 |
| POST | `/subscriptions/{id}/archive` | 归档 | 有绑定需先迁移/解除 |
| GET | `/subscriptions/{id}/bindings` | 绑定成员列表 | 分页 |
| GET/POST | `/members`、`/members/{id}` | 成员 | — |
| POST | `/members/{id}/suspend`、`/resume`、`/archive` | 账户状态 | 撤销会话；`LAST_ADMIN` 保护 |
| POST | `/members/{id}/proxy-access/pause`、`/resume` | 代理访问（独立状态） | 硬撤权链路 |
| POST | `/members/{id}/session-revocations` | 撤销会话 | — |
| POST | `/invitations` | 创建邀请 | 一次性、限时、可撤销 |
| POST | `/invitations/{id}/revoke` | 撤销邀请 | — |
| GET/PUT | `/members/{id}/binding` | 绑定/切换订阅 | 返回切换历史（`?include_history=true`） |
| GET/PUT/DELETE | `/members/{id}/overrides/{field}` | 个人覆盖（`quota`、`expires_at`）；DELETE=恢复继承 | 返回来源 |
| GET | `/members/{id}/effective-access` | 生效值 + 来源 + 判定链 | 与 Agent 快照同算法版本 |
| POST | `/members/{id}/credentials/rotations` | 轮换（`scope=single_node\|all_nodes`） | 需确认参数 `confirm=true` |
| POST | `/members/{id}/subscription-token/rotations` | 轮换订阅链接 | **不**触发代理撤权 |
| POST | `/members/{id}/quota-adjustments` | 审计化额度调整 | 必填 `reason` |
| POST | `/batches/preview` | 生成预览 | 返回 `preview_id` + 版本戳 |
| POST | `/batches` | 提交批量（需 `Idempotency-Key`） | 单 PG 事务写规则 + outbox |
| GET | `/batches/{id}`、`/batches/{id}/results` | 逐对象结果 | 逐成员×节点状态 |
| POST | `/batches/{id}/retry` | 重试失败/超时项 | 幂等、版本检查 |
| GET | `/usage` | 成员×节点×周期查询 | 含 `estimated`/`boundary_split` |
| GET | `/usage/adjustments` | 额度调整记录 | 审计 |
| GET | `/events`、`POST /events/{id}/ack`、`/events/{id}/silence` | 事件与告警 | 去重/抑制 |
| GET | `/tasks`、`POST /tasks/{id}/retry`、`POST /tasks/{id}/cancel` | 任务与同步结果 | 取消仅限未开始 |
| GET/PUT | `/settings` | 业务时区默认、通知渠道、保留期、模板 | 审计 |
| GET | `/audit-logs` | 审计查询 | 不含秘密 |
| POST | `/recovery-mode/enter`、`/exit` | 恢复协调模式 | 需明确确认与核对步骤 |

成员端点（`/api/v1/me`）：`GET /subscription`（含格式选择 `?format=`）、`GET /subscription/token`、`POST /subscription/token/rotations`、`GET /nodes`、`GET /usage`、`GET /credentials`、`POST /credentials/rotations`、`GET /status`、`POST /password`、`GET /sessions`、`DELETE /sessions/{id}`。所有端点强制个人范围，改 ID 一律 403（不泄露存在性）。

---

## 4. 关键 JSON 示例（管理员侧）

### 4.1 成员详情（生效值与来源）

```json
{
  "data": {
    "id": "mbr_01J9Z3K8V6Q0A1B2C3D4E5F6G7",
    "username": "alice",
    "account_status": "active",
    "proxy_access": "enabled",
    "binding": {
      "subscription_id": "sub_01J9Z4A1",
      "subscription_name": "合租基础",
      "bound_at": "2026-09-01T02:00:00Z",
      "expires_at": "2026-12-01T00:00:00Z",
      "period": {
        "start": "2026-10-15T16:00:00Z",
        "end": "2026-11-15T16:00:00Z",
        "timezone": "Asia/Shanghai",
        "anchor_day": 1,
        "algorithm_version": "period/v1",
        "next_rule": null
      }
    },
    "effective": {
      "quota_bytes": 5368709120,
      "quota_unlimited": false,
      "used_bytes": 3221225472,
      "expires_at": "2026-12-01T00:00:00Z",
      "sources": [
        { "field": "quota_bytes", "source": "override", "subscription_default": 10737418240 },
        { "field": "expires_at", "source": "binding" }
      ]
    },
    "overrides": [
      { "field": "quota_bytes", "value": 5368709120, "updated_at": "2026-10-02T03:00:00Z", "reason": "临时降额" }
    ],
    "access_chain": [
      { "step": "account_status", "result": "pass" },
      { "step": "proxy_access", "result": "pass" },
      { "step": "binding_validity", "result": "pass" },
      { "step": "subscription_access", "result": "pass" },
      { "step": "quota", "result": "pass", "used_bytes": 3221225472 },
      { "step": "node_authorization", "result": "pass", "allowed_nodes": 3 }
    ],
    "sync": {
      "agents": [
        { "agent_id": "agt_01J9Z5", "desired_revision": 142, "applied_revision": 142, "applied_at": "2026-10-20T06:00:12Z", "revocation_latency_ms": 8100 }
      ]
    }
  },
  "request_id": "req_01J9Z3K8"
}
```

### 4.2 订阅（周期规则 + 待生效规则）

```json
{
  "data": {
    "id": "sub_01J9Z4A1",
    "name": "合租基础",
    "rules_version": 41,
    "node_group_ids": ["ng_01", "ng_02"],
    "quota": { "mode": "limited", "bytes": 10737418240 },
    "period_rule": {
      "mode": "monthly_anchor",
      "anchor_day": 1,
      "timezone": "Asia/Shanghai",
      "algorithm_version": "period/v1"
    },
    "pending_period_rule": {
      "anchor_day": 31,
      "timezone": "Asia/Shanghai",
      "effective_from": "2026-11-15T16:00:00Z",
      "next_period": { "start": "2026-11-15T16:00:00Z", "end": "2026-11-30T16:00:00Z" },
      "submitted_at": "2026-10-20T05:10:00Z"
    },
    "default_term_days": 90,
    "client_template_id": "tpl_01",
    "allow_new_binding": true,
    "access_paused": false,
    "archived": false,
    "bound_members": 7,
    "limits": [
      { "key": "stream_per_user", "value": "verified", "verified_at": "2026-10-18T00:00:00Z" }
    ]
  },
  "request_id": "req_01J9Z3K9"
}
```

说明：`pending_period_rule` 表示“下一周期生效”；当前 `period_rule` 不对当期生效（`product.md §8.3`）。未验证的限制不得出现为可开关项。

### 4.3 批量预览

请求：

```json
{
  "operation": "switch_subscription",
  "target": { "type": "explicit", "member_ids": ["mbr_A", "mbr_B", "mbr_C"] },
  "params": { "subscription_id": "sub_01J9Z4A1" },
  "reason": "2026-11 起换基础订阅"
}
```

响应：

```json
{
  "data": {
    "preview_id": "prv_01J9Z6",
    "expires_at": "2026-10-20T06:20:00Z",
    "rules_version": 41,
    "member_set_digest": "sha256:0f3c…",
    "counts": {
      "members": 3,
      "agents": 2,
      "nodes": 5,
      "overrides_cleared": 1,
      "will_be_over_quota": 1,
      "will_expire": 0,
      "blocked": 0
    },
    "items": [
      {
        "member_id": "mbr_A",
        "before": { "subscription_id": "sub_OLD", "quota_bytes": 10737418240 },
        "after": { "subscription_id": "sub_01J9Z4A1", "quota_bytes": 10737418240 },
        "consequences": ["nodes_added:1", "nodes_removed:2"],
        "sessions_at_risk": 2
      },
      {
        "member_id": "mbr_B",
        "before": { "subscription_id": "sub_OLD", "quota_bytes": 5368709120 },
        "after": { "subscription_id": "sub_01J9Z4A1", "quota_bytes": 10737418240 },
        "overrides": [{ "field": "quota_bytes", "action": "kept", "value": 5368709120 }],
        "consequences": ["override_preserved"]
      },
      {
        "member_id": "mbr_C",
        "before": { "subscription_id": "sub_OLD", "quota_bytes": 10737418240, "used_bytes": 9663676416 },
        "after": { "subscription_id": "sub_01J9Z4A1", "quota_bytes": 10737418240 },
        "consequences": ["over_quota"],
        "sessions_at_risk": 1
      }
    ],
    "blocked": [],
    "warnings": ["mbr_C 当前已用 9.0 GiB，切换后仍超过配额，提交后将立即撤权"],
    "period_impact": [
      { "member_id": "mbr_A", "current_period_end": "2026-11-15T16:00:00Z", "next_period_end": "2026-11-30T16:00:00Z" }
    ]
  },
  "request_id": "req_01J9Z7"
}
```

### 4.4 批量提交与结果

提交（`Idempotency-Key: 0192f0c1-1111-7000-8000-00000000abcd`）：

```json
{
  "preview_id": "prv_01J9Z6",
  "rules_version": 41,
  "member_set_digest": "sha256:0f3c…",
  "confirm": true
}
```

202：

```json
{
  "data": {
    "batch_id": "bat_01J9Z8",
    "status": "accepted",
    "rules_committed": true,
    "dispatch": { "pending": 5, "dispatched": 0, "succeeded": 0, "failed": 0 },
    "preview_recheck": { "numeric_changes": ["mbr_A.used_bytes"], "conclusion_changed": false }
  },
  "request_id": "req_01J9Z8"
}
```

结果（`GET /batches/bat_01J9Z8/results`）：

```json
{
  "items": [
    { "member_id": "mbr_A", "node_id": "nd_01", "agent_id": "agt_01", "status": "succeeded", "task_id": "tsk_1", "receipt_revision": 142, "applied_at": "2026-10-20T06:05:11Z" },
    { "member_id": "mbr_A", "node_id": "nd_02", "agent_id": "agt_02", "status": "failed", "task_id": "tsk_2", "error": { "code": "AGENT_OFFLINE", "message": "Agent 离线 12 分钟" }, "retryable": true },
    { "member_id": "mbr_C", "node_id": "nd_01", "agent_id": "agt_01", "status": "superseded", "task_id": "tsk_3", "note": "被 revision 144 取代" }
  ],
  "summary": { "succeeded": 1, "failed": 1, "superseded": 1, "pending": 0 },
  "next_cursor": null,
  "request_id": "req_01J9Z9"
}
```

`rules_committed=true` 只表示规则在面板事务内提交；`dispatch` 与逐对象结果是节点生效的唯一依据（`product.md §9.4`）。

### 4.5 冲突与保护示例

```json
{ "error": { "code": "LAST_ADMIN", "message": "不能停用最后一名可用管理员", "details": { "active_admins": 1 }, "retryable": false, "request_id": "req_01J9ZA" } }
```

```json
{ "error": { "code": "PREVIEW_CONCLUSION_CHANGED", "message": "成员 mbr_C 已超额，需重新确认", "details": { "members": ["mbr_C"] }, "retryable": true, "request_id": "req_01J9ZB" } }
```

### 4.6 用量查询（周期与估计标记）

```json
{
  "items": [
    {
      "member_id": "mbr_A",
      "node_id": "nd_01",
      "period": { "start": "2026-10-15T16:00:00Z", "end": "2026-11-15T16:00:00Z", "timezone": "Asia/Shanghai" },
      "up_bytes": 1048576,
      "down_bytes": 1048576000,
      "metering": { "direction": "ingress_user", "includes_protocol_overhead": true, "algorithm_version": "metre/v1" },
      "estimated": false,
      "corrections": [
        { "reason": "boundary_split", "up_bytes": 1024, "down_bytes": 2048, "received_at": "2026-11-16T02:00:00Z" }
      ]
    }
  ],
  "nic_counters": [{ "server_id": "srv_01", "rx_bytes": 99999999, "tx_bytes": 88888888 }],
  "next_cursor": null,
  "request_id": "req_01J9ZC"
}
```

`nic_counters` 与代理计量分开呈现，不参与配额判断（计划 `§3 F08`）。

---

## 5. Agent 端点提案（`/api/v1/agent`）

| 方法 | 路径 | 用途 | 约束 |
| --- | --- | --- | --- |
| POST | `/register` | 一次性注册码兑换 Agent 凭据 | 单次使用、限时、绑定服务器；不返回其他服务器信息 |
| POST | `/credentials/rotate` | 凭据轮换（旧凭据按策略失效） | 不允许无限并存 |
| POST | `/heartbeat` | 心跳 + 版本 + 负载摘要 | 频率与超时待冻结（建议 5s / 离线阈值 30s） |
| GET | `/snapshot?since_revision=N&wait=20` | 拉取授权快照（长轮询） | 建议：有新版本返回 `200` + 完整快照；无变化返回 `204` + `X-Current-Revision` |
| POST | `/snapshot/receipt` | 提交应用回执 | 幂等（按 `revision`）；旧版本回执被忽略 |
| GET | `/node-configs?revision=N` | 技术配置（与授权分离） | 校验和 + 版本 |
| POST | `/node-configs/receipt` | 技术配置回执 | 与授权回执分开 |
| POST | `/usage/batches` | 用量批次上报 | 幂等 `batch_id`；明确确认才清理 |
| POST | `/tasks/ack` | 受限任务（启动/停止/重建/升级）确认 | 任务类型白名单 |
| POST | `/telemetry` | 主机与运行状态采集 | 不采集访问历史；来源 IP 仅在明确功能中 |

Agent 不接收任意命令、路径或 shell；任务类型为固定枚举（计划 `§3 F05`、`§4.1`）。

### 5.1 注册

请求 `POST /api/v1/agent/register`：

```json
{ "registration_code": "<opaque>", "server_hint": "srv_01", "agent_version": "agent/0.1.0", "capabilities": ["snapshot/v1", "metre/v1", "receipt/v1"] }
```

```json
{ "data": { "agent_id": "agt_01J9Z5", "agent_token": "__REDACTED__", "control_generation": 7, "heartbeat_interval_seconds": 5 } }
```

`control_generation` 为控制面世代，用于面板恢复后重建（`§6.4`）。

### 5.2 授权快照（完整结构）

```json
{
  "schema_version": "agent.snapshot/1",
  "snapshot_type": "full",
  "agent_id": "agt_01J9Z5",
  "control_generation": 7,
  "revision": 142,
  "issued_at": "2026-10-20T06:00:00Z",
  "period_algorithm_version": "period/v1",
  "metering_algorithm_version": "metre/v1",
  "expected_member_count": 0,
  "expected_credential_count": 0,
  "policy": {
    "enforce_local_expiry": true,
    "revocation_strategy": "api_and_verify",
    "clock_skew_alert_seconds": 30
  },
  "core_instances": [
    {
      "instance_id": "ci_01",
      "generation": 12,
      "config_revision": 88,
      "nodes": [
        {
          "node_id": "nd_01",
          "protocol": "vless",
          "adapter": { "name": "xray-vless-reality", "version": "1" },
          "listen": { "address": "0.0.0.0", "port": 443 },
          "public": { "address": "node.example.invalid", "port": 443, "sni": "www.example.invalid" },
          "security": { "type": "reality", "public_key": "<opaque>", "short_id": "<opaque>", "flow": "xtls-rprx-vision" },
          "transport": { "type": "tcp" },
          "enabled": true
        }
      ]
    }
  ],
  "members": [
    {
      "member_id": "mbr_01J9Z3K8",
      "display_name": "alice",
      "enabled": true,
      "expires_at": "2026-12-01T00:00:00Z",
      "quota": { "limit_bytes": 5368709120, "used_bytes": 3221225472, "period_end": "2026-11-15T16:00:00Z" },
      "credentials": [
        { "credential_id": "crd_01", "node_id": "nd_01", "user_key": "__REDACTED__", "state": "active" }
      ]
    }
  ],
  "revoked_credentials": [],
  "revocation_events": [
    { "event_id": "rev_01", "credential_id": "crd_99", "reason": "member_suspended", "committed_at": "2026-10-20T05:59:31Z", "deadline_at": "2026-10-20T06:00:31Z" }
  ]
}
```

要点：

| 字段 | 语义 |
| --- | --- |
| `revision` | 单调递增；Agent 只接受 `>` 已应用版本 |
| `snapshot_type` | 仅 `full`；不支持增量，避免“差异缺失”歧义 |
| `expected_member_count` / `expected_credential_count` | **显式声明**期望数量，使 `members: []` 无法被误读为“无变化” |
| `members[].quota` | 仅用于展示/本地告警；**配额判定权威在面板**（提案，待冻结），Agent 不自行按配额撤权，避免双口径 |
| `revocation_events[].deadline_at` | 面板侧的 60 秒截止参考；Agent 不等截止，立即执行 |
| `enforce_local_expiry` | Agent 本地到点执行到期撤权（面板离线也生效） |
| `revocation_strategy` | `api_only` / `api_and_verify` / `instance_restart`（待 D8 实测后冻结） |
| `control_generation` | 面板恢复/重建时递增；Agent 拒绝低于已知世代的快照并告警 |

### 5.3 快照拉取协议（空列表与“无变化”的区分）

```text
GET /api/v1/agent/snapshot?since_revision=142&wait=20

有新版本 → 200 + 完整快照（snapshot_type=full, revision=143, 可能 members=[]）
无新版本 → 204 + 头 X-Current-Revision: 142（响应体为空，Agent 不得改变本地状态）
```

规则：

1. **禁止**用“响应体为空”或“`members` 缺失”表达“无变化”；`members` 必须是数组（可为 `[]`）。
2. `members: []` 且 `revision > applied_revision` 必须执行“清空全部用户”（计划 `§3 F05`）。
3. Agent 必须在回执中带 `applied_member_count`，使 0 值可见。

### 5.4 应用回执（与“规则提交”分离）

成功应用（含清空）：

```json
{
  "agent_id": "agt_01J9Z5",
  "control_generation": 7,
  "revision": 142,
  "result": "applied",
  "applied_revision": 142,
  "applied_member_count": 0,
  "applied_credential_count": 0,
  "core_results": [
    { "instance_id": "ci_01", "generation": 12, "action": "rebuild", "result": "succeeded", "duration_ms": 1180, "users_removed": 1 }
  ],
  "revocation_results": [
    { "event_id": "rev_01", "credential_id": "crd_99", "new_connections_rejected": true, "existing_sessions_terminated": true, "terminated_tcp": 1, "terminated_udp": 1, "method": "api", "completed_at": "2026-10-20T06:00:08Z" }
  ],
  "agent_time": "2026-10-20T06:00:08Z",
  "clock_offset_ms": 12
}
```

无变化：

```json
{ "agent_id": "agt_01J9Z5", "control_generation": 7, "revision": 142, "result": "unchanged", "applied_revision": 142, "applied_member_count": 5 }
```

失败：

```json
{
  "agent_id": "agt_01J9Z5",
  "control_generation": 7,
  "revision": 143,
  "result": "failed",
  "applied_revision": 142,
  "error": { "code": "CONFIG_VALIDATION_FAILED", "message": "xray -test 失败", "retryable": true },
  "core_results": [{ "instance_id": "ci_01", "result": "unchanged", "note": "保留上一可用配置" }]
}
```

回执处理规则（提案）：

| 规则 | 说明 |
| --- | --- |
| 幂等 | 同 `revision` 回执可重复提交，服务端只记一次（按 `(agent_id, revision)`） |
| 版本 | `revision < 已应用版本` → 忽略并记 `REVISION_STALE`；不推进状态 |
| 失败 | 失败回执不推进“已生效版本”；不覆盖上一可用配置 |
| 撤权 | `revocation_results` 必须显式给出“新连接拒绝”和“旧会话终止”两项；缺项视为未完成 |
| 超时 | 超过 SLA 未收到回执 → 任务 `timeout` + 告警，不显示“已撤销” |

### 5.5 用量批次上报

请求 `POST /api/v1/agent/usage/batches`：

```json
{
  "batch_id": "0192f0c1-2222-7000-8000-00000000beef",
  "agent_id": "agt_01J9Z5",
  "core_instance_id": "ci_01",
  "core_generation": 12,
  "metering_algorithm_version": "metre/v1",
  "sampling": { "started_at": "2026-10-20T05:59:00Z", "ended_at": "2026-10-20T06:00:00Z", "interval_seconds": 60, "source": "ingress_user" },
  "entries": [
    { "member_id": "mbr_01J9Z3K8", "node_id": "nd_01", "credential_id": "crd_01", "up_bytes": 10240, "down_bytes": 1048576 }
  ],
  "payload_sha256": "9f2c…"
}
```

首次接受：

```json
{ "data": { "status": "accepted", "batch_id": "0192f0c1-…beef", "accounted": true, "receipt_id": "rcp_01J9ZD", "duplicate_of": null }, "request_id": "req_01J9ZD" }
```

重复（幂等）：

```json
{ "data": { "status": "duplicate", "batch_id": "0192f0c1-…beef", "accounted": false, "duplicate_of": "rcp_01J9ZD", "original_received_at": "2026-10-20T06:00:05Z" }, "request_id": "req_01J9ZE" }
```

同 ID 异载荷：

```json
{ "error": { "code": "BATCH_PAYLOAD_MISMATCH", "message": "batch_id 已存在但载荷指纹不同", "details": { "batch_id": "0192f0c1-…beef", "existing_payload_sha256": "9f2c…" }, "retryable": false, "request_id": "req_01J9ZF" } }
```

边界与分账（提案）：

| 情况 | 服务端行为 |
| --- | --- |
| 采样区间完全在已关闭周期 | 记入该历史周期；不追溯撤权；标记为更正 |
| 区间跨周期边界 | 按区间时长比例分摊，落账条目附 `estimated=true`、`boundary_split=true` |
| 采样时间漂移超阈值 | `422 BATCH_CLOCK_SKEW`，隔离为待人工确认，不挪到当前周期 |
| 成员已停用/凭据已轮换 | 依据历史映射入账，不拒收合法历史数据 |
| 负数/超范围/归属不符 | 拒绝（`4xx`），不入账并告警 |

### 5.6 受限任务（示例）

请求 `POST /api/v1/agent/tasks`：

```json
{ "task_id": "tsk_01J9ZG", "type": "core.restart", "core_instance_id": "ci_01", "params": { "graceful_timeout_seconds": 10 }, "reason": "apply_new_authorization" }
```

任务类型白名单（提案）：`core.start`、`core.stop`、`core.restart`、`core.config_apply`、`core.rollback`、`agent.upgrade`、`cert.install`。**不包含**任意 shell、任意路径、任意下载 URL。

---

## 6. 版本、幂等与恢复语义

### 6.1 版本类型

| 版本 | 归属 | 用途 |
| --- | --- | --- |
| `rules_version` | 面板（订阅/成员规则） | 预览与提交的一致性前置 |
| `member_set_digest` | 面板 | 预览的目标集合指纹 |
| `revision` | 面板 → 单个 Agent | 授权快照单调序号 |
| `config_revision` | 面板 → 单个 Agent | 技术配置版本（与授权分离） |
| `core generation` | Agent/核心 | 每次启动的新世代，用于区分计数器 |
| `control_generation` | 面板 | 面板恢复/重建后的控制面世代 |
| `period_algorithm_version` / `metering_algorithm_version` | 共享契约 | 判定算法一致性（面板、快照、账本引用同版本） |

### 6.2 幂等

| 对象 | 键 | 冲突处理 |
| --- | --- | --- |
| 管理写入（批量/轮换/调整） | `Idempotency-Key` + 指纹 | 同键同指纹→返回原结果；同键异指纹→`409 IDEMPOTENCY_KEY_REUSED` |
| 用量批次 | `(agent_id, batch_id)` | 同载荷→`duplicate` 不重复入账；异载荷→`409 BATCH_PAYLOAD_MISMATCH` |
| 应用回执 | `(agent_id, revision)` | 重复提交幂等；低版本忽略 |
| 邀请兑换 | 邀请码 + 单次原子状态 | 只有一次成功 |

### 6.3 空列表与空值规则（契约级硬要求）

1. 所有成员/节点集合字段必须是数组；服务端不得省略空数组字段。
2. `204` 仅表示“无新版本”（拉取语义），**不表示**“清空”；清空必须通过 `200` + `members: []` + 更高 `revision` 表达。
3. 客户端（Agent）必须校验 `expected_member_count == len(members)`，不一致视为契约错误并拒绝应用。
4. 列表型 REST 响应返回 `items: []` 与 `next_cursor: null`，禁止 `data: null` 表示空集合。

### 6.4 恢复与降级

| 场景 | 契约行为 |
| --- | --- |
| 面板恢复旧备份 | `control_generation` 递增；进入恢复协调模式：普通写入 `409`/只读；不向 Agent 推送旧授权 |
| Agent 已知更高世代/版本 | 拒绝更低快照并告警；保留已知更严格状态 |
| PostgreSQL 不可用 | 写入与上报 `503 DB_UNAVAILABLE`；不返回伪成功；Agent 保留队列 |
| Redis 不可用 | 授权回查 PG；登录限流降级为受限本地或拒绝；不以缓存伪造结果 |
| Agent 离线 | 面板写请求仍成功（规则已保存），任务显示 `pending/timeout`；不显示“已生效” |

---

## 7. 协议适配边界（提案）

```json
{
  "node_id": "nd_01",
  "protocol": "vless",
  "adapter": { "name": "xray-vless-reality", "version": "1" },
  "params": {
    "security": { "type": "reality", "public_key": "<opaque>", "short_id": "<opaque>", "flow": "xtls-rprx-vision" },
    "transport": { "type": "tcp" }
  }
}
```

| 规则 | 内容 |
| --- | --- |
| 面板职责 | 仅按声明式 schema 校验必填/类型/取值域；不解析 Xray 语义，不拼接核心配置 |
| Agent 职责 | 按 `adapter.name+version` 映射为官方 Xray 配置；`xray -test` 校验；失败不替换 |
| 未知适配器 | Agent 拒绝应用并回执 `SNAPSHOT_SCHEMA_UNSUPPORTED` / `ADAPTER_UNSUPPORTED` |
| 未验证组合 | 面板保存节点时拒绝或标记未验证（`PROTOCOL_NOT_SUPPORTED`），不得因“核心支持”而默认本项目支持 |
| 能力矩阵 | 待用户清单（`product.md §15 D1`）；VLESS + REALITY + Vision 仅**建议验证组合**，本文不声称已支持其他组合 |
| 客户端兼容 | 以固定版本矩阵的真实导入/连接测试为准，不以“格式生成成功”判定 |

---

## 8. 待冻结清单（主负责人）

| 编号 | 事项 | 影响 |
| --- | --- | --- |
| C1 | 路径与命名规范（`sub_/mbr_/nd_` 前缀是否保留） | 全站契约 |
| C2 | 认证细节（Cookie 名、CSRF 头、Agent 是否启用 mTLS） | 安全与联调 |
| C3 | 快照轮询机制（长轮询 vs 短轮询）与间隔/超时默认值（建议：5s 心跳、20s 长轮询、60s 撤权 SLA） | 60 秒撤权余量分配 |
| C4 | `revocation_strategy` 枚举与默认值（待 D8 实测） | 验收 A06/A10 |
| C5 | 配额判定权威是否严格留在面板（本提案如此，Agent 仅展示） | 语义一致性 |
| C6 | 跨周期分摊公式与容差（`product.md §8.5`、`D5`） | 账本口径与验收 |
| C7 | 保留期、去重/重放窗口、`BATCH_CLOCK_SKEW` 阈值 | A14 与清理策略 |
| C8 | 错误码最终集合与本地化文案 | 前端与测试 |
| C9 | 协议/客户端矩阵与适配器注册表（D1） | 节点表单、Agent 适配器 |
| C10 | OpenAPI 文件归属（`api/openapi.yaml`、`api/agent.openapi.yaml` 由负责人维护） | 契约发布 |

本文所有端点、字段与示例均为**提案**；冻结前不得视为可用接口，更不得据此宣称功能已实现。
