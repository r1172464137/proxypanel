# HTTP API 参考

面板对外提供两套接口：**浏览器/成员**用的 `/api/v1/**`，和 **Agent** 用的 `/api/v1/agent/**`。
另有 `/sub/{token}` 直接返回客户端订阅配置，不需要认证（令牌本身即凭据）。

> 本文以 `internal/panel/api/api.go` 的路由表和 `internal/panel/domain/errors.go` 的错误码为准。
> 历史提案文档在 `docs/archive/`，字段名多为过期版本，**不要照着提案写客户端**。

---

## 1. 认证

| 场景 | 方式 |
|---|---|
| 浏览器 / 成员 | 会话 Cookie `pp_session`（HttpOnly）+ CSRF 令牌 |
| Agent | `Authorization: Bearer <agent_token>`，注册时一次性发放 |
| 订阅拉取 | 路径里的 `{token}` 即凭据，无需 Cookie |

**CSRF**：除 `GET`/`HEAD` 与 `/agent/**`、`/sub/**` 外，所有写请求必须带
`X-CSRF-Token`，值与登录/`GET /auth/session` 返回的 `csrf_token` 一致。
缺失或不等 → `403 CSRF_FAILED`。这是双提交校验，不比较 Origin。

**会话 Cookie 属性**由环境变量控制：`PANEL_COOKIE_SAMESITE`（默认 `strict`）、
`PANEL_COOKIE_DOMAIN`、`PANEL_DEV`（为 true 时去掉 `Secure`）。

登录失败按「IP + 用户名」计数限流，超过阈值 `429 RATE_LIMITED`。

---

## 2. 响应包络

成功（对象）：

```json
{ "data": { "id": "srv_..." } }
```

成功（列表）：`items` 是数组，`next_cursor` 目前恒为 `null`。

```json
{ "items": [ ... ], "next_cursor": null }
```

失败：

```json
{
  "error": {
    "code": "VALIDATION_FAILED",
    "message": "node configuration is invalid",
    "details": { "field_errors": { "stream_settings.tlsSettings.certificates": "..." } },
    "request_id": "req_...",
    "retryable": false
  }
}
```

`request_id` 同时出现在 `X-Request-Id` 响应头。字段级错误统一在
`error.details.field_errors`，key 是前端表单字段 id 或点分路径。

---

## 3. 错误码

| code | HTTP | 含义 |
|---|---|---|
| `UNAUTHENTICATED` | 401 | 未登录 / 会话失效 |
| `CSRF_FAILED` | 403 | 缺 CSRF 令牌或值不匹配 |
| `FORBIDDEN` | 403 | 已登录但无权限（如成员访问 `/admin/**`） |
| `NOT_FOUND` | 404 | 对象不存在、不在可见范围内，**或已被软删除** |
| `VALIDATION_FAILED` | 400 | 入参不合法；字段细节见 `field_errors` |
| `CONFLICT` | 409 | 版本冲突（`version` 与当前不一致） |
| `LAST_ADMIN` | 409 | 会移除最后一个可用管理员 |
| `PREVIEW_REQUIRED` | 409 | 该操作必须走预览-确认两段式 |
| `PREVIEW_STALE` | 409 | 预览已失效（目标集合变了） |
| `PREVIEW_CONCLUSION_CHANGED` | 409 | 预览结论与提交时不一致 |
| `IDEMPOTENCY_KEY_REUSED` | 409 | 同一幂等键被用于不同载荷 |
| `RECOVERY_MODE` | 409 | 恢复协调模式下写操作被冻结 |
| `NODE_IN_USE` | 409 | 服务器/节点组仍被引用，不能删 |
| `SUBSCRIPTION_NOT_BINDABLE` | 409 | 目标订阅不可绑定 |
| `REVISION_STALE` | 409 | Agent 提交的 revision 落后 |
| `BATCH_PAYLOAD_MISMATCH` | 409 | 用量批次载荷与账本不一致 |
| `BATCH_OWNERSHIP_MISMATCH` | 409 | 批次不属于该 Agent |
| `PROTOCOL_NOT_SUPPORTED` | **422** | 客户端格式无法表达该节点 |
| `BATCH_CLOCK_SKEW` | **422** | 用量批次时间戳偏移过大 |
| `RATE_LIMITED` | 429 | 触发限流 |
| `DB_UNAVAILABLE` | 503 | 数据库不可用 |
| `AGENT_AUTH_FAILED` | 401 | Agent 令牌无效 |
| `SNAPSHOT_SCHEMA_UNSUPPORTED` | 400 | 快照 schema 版本不支持 |
| `INTERNAL` | 500 | 未预期错误 |

---

## 4. 乐观并发：`version` 是必填

所有 `PATCH` / `PUT` **必须**带 `version`（从对应的 `GET` 里取）。缺失或传 `0`
一律 `400 VALIDATION_FAILED`：

```json
{ "error": { "code": "VALIDATION_FAILED",
  "details": { "field_errors": { "version": "required" } } } }
```

`version` 与当前不一致 → `409 CONFLICT`。

> 为什么强制：store 层用 `expectedVersion == 0` 表示「跳过检查」，那是给内部调用
> （归档、批量 apply）用的。这个约定一旦漏到 HTTP 面，客户端只要省略 `version`，
> 两个并发 PATCH 就都能成功、后写静默覆盖先写。所以对外接口必须拒绝 0。

`DELETE` 不要求 `version`。它的语义是「把这个对象删掉」，调用方不需要知道它是第几版；
不能通过删旧库来恢复被撤销的人（见 [备份与恢复](../operations/backup-recovery.md)）。

---

## 5. 路由表

### 5.1 认证（无需登录）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/auth/login` | body `{username, password}` → `csrf_token` + 会话 Cookie |
| POST | `/api/v1/auth/logout` | 撤销当前会话 |
| POST | `/api/v1/auth/redeem` | 用邀请码兑换成员账号 |
| GET | `/api/v1/auth/session` | 当前身份 + `csrf_token` |
| GET | `/healthz` | 进程存活（不查依赖） |
| GET | `/readyz` | 依赖就绪（PostgreSQL / Redis） |
| GET | `/sub/{token}` | 客户端订阅输出，`?format=` 见下方 |

### 5.2 成员自助（`member` 及以上）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/me/status` | 配额、用量、剩余天数、可用节点 |
| GET | `/api/v1/me/usage` | 用量条目与调整记录 |
| GET | `/api/v1/me/nodes` | 我能用的节点及连接参数 |
| GET | `/api/v1/me/subscription` | 订阅令牌与链接 |
| POST | `/api/v1/me/subscription/token/rotations` | 轮换订阅令牌 ⚠️ |
| GET | `/api/v1/me/sessions` | 我的登录会话 |
| DELETE | `/api/v1/me/sessions/{id}` | 踢掉某个会话 ⚠️ |
| POST | `/api/v1/me/credentials/rotations` | 轮换我的代理凭据 ⚠️ |
| POST | `/api/v1/me/password` | 改自己的密码 |

⚠️ = 恢复协调模式期间返回 `409 RECOVERY_MODE`（它们都改授权相关状态）。
修改自己密码**不拦**：那是自救路径，不改授权。

### 5.3 管理端资源

伺服端（服务器 / 节点）：

| 方法 | 路径 |
|---|---|
| GET / POST | `/api/v1/admin/servers` |
| GET / PATCH / DELETE | `/api/v1/admin/servers/{id}` |
| POST | `/api/v1/admin/servers/{id}/registration-codes` |
| GET / POST | `/api/v1/admin/nodes` |
| GET / PATCH / DELETE | `/api/v1/admin/nodes/{id}` |
| GET / POST | `/api/v1/admin/node-groups` |
| GET / PATCH / DELETE | `/api/v1/admin/node-groups/{id}` |

订阅与模板：

| 方法 | 路径 |
|---|---|
| GET / POST | `/api/v1/admin/subscriptions` |
| GET / PATCH / DELETE | `/api/v1/admin/subscriptions/{id}` |
| GET / POST | `/api/v1/admin/templates` |
| GET / PATCH / DELETE | `/api/v1/admin/templates/{id}` |

成员与邀请：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET / POST | `/api/v1/admin/members` | |
| GET / PATCH / DELETE | `/api/v1/admin/members/{id}` | DELETE 是软删（归档） |
| POST | `/api/v1/admin/members/{id}/credentials/rotations` | 管理员替我轮换凭据 |
| POST | `/api/v1/admin/members/{id}/subscription-token/rotations` | |
| POST | `/api/v1/admin/members/{id}/quota-adjustments` | 手工调整已用量 |
| GET / POST | `/api/v1/admin/invitations` | |
| POST | `/api/v1/admin/invitations/{id}/revoke` | |

批量（预览 → 确认）：

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/admin/batches/preview` | 出影响预览，返回 `preview_id` |
| POST | `/api/v1/admin/batches` | 带 `preview_id` + `Idempotency-Key` 提交 |
| GET | `/api/v1/admin/batches` | 批次列表 |
| GET | `/api/v1/admin/batches/{id}` | 批次详情（结果内联，无单独 results 端点） |

观测与运维：

| 方法 | 路径 |
|---|---|
| GET | `/api/v1/admin/overview` |
| GET | `/api/v1/admin/agents` |
| GET | `/api/v1/admin/core-instances` |
| GET | `/api/v1/admin/usage` |
| GET | `/api/v1/admin/audit-logs` |
| GET | `/api/v1/admin/events` |
| POST | `/api/v1/admin/events/{id}/ack` |
| GET | `/api/v1/admin/tasks` |
| POST | `/api/v1/admin/tasks/{id}/retry` |
| GET / PUT | `/api/v1/admin/settings` |

恢复协调：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/admin/recovery-mode` | 当前 `control_generation` / 是否冻结 |
| POST | `/api/v1/admin/recovery-mode/enter` | 冻结写入并轮换 generation |
| POST | `/api/v1/admin/recovery-mode/exit` | 需 `confirm: "verified"` 等核对载荷 |

### 5.4 Agent

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/agent/register` | 用手注册码换 `agent_token` |
| GET | `/api/v1/agent/snapshot` | 拉授权快照（`?revision=&control_generation=`） |
| POST | `/api/v1/agent/applied` | 上报已应用的 revision 与实例结果 |
| POST | `/api/v1/agent/heartbeat` | 心跳 |
| POST | `/api/v1/agent/usage` | 提交用量批次（幂等账本） |

快照在恢复模式期间返回 `409`：面板不会在核对完成前把新授权推下去。

---

## 6. 两段式批量

改动多个成员/节点必须先生成预览。预览会冻结当时的结论；提交时结论变了就拒绝，
避免「预览时看到的是 A 结果、实际执行成 B 结果」。

```bash
# 1) 预览
curl -sS -X POST "$PANEL/api/v1/admin/batches/preview" \
  -b cookies.txt -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -d '{"operation":"set_active","member_ids":["mbr_a","mbr_b"],
       "values":{"active":false},"reason":"月底停止"}'
# → {"data":{"id":"prv_...","rules_version":41,...}}

# 2) 确认（同一个 Idempotency-Key 重复提交返回同一批次）
curl -sS -X POST "$PANEL/api/v1/admin/batches" \
  -b cookies.txt -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{"preview_id":"prv_...","confirm":true}'
```

同一 `Idempotency-Key` 配不同载荷 → `409 IDEMPOTENCY_KEY_REUSED`。

---

## 7. 完整流程示例

```bash
PANEL=https://panel.example.com
J=/tmp/pp-cookies.txt

# 登录
CSRF=$(curl -sS -c $J -X POST "$PANEL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"..."}' | jq -r .data.csrf_token)

api() { curl -sS -b $J -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' "$@"; }

# 建服务器 → 拿一次性注册码给 Agent
SRV=$(api -X POST "$PANEL/api/v1/admin/servers" \
  -d '{"name":"hk-01","address":"hk01.example.com","region":"hk"}' | jq -r .data.id)
api -X POST "$PANEL/api/v1/admin/servers/$SRV/registration-codes" -d '{}'

# 建节点（TLS 必须配证书，否则 400）
api -X POST "$PANEL/api/v1/admin/nodes" -d '{
  "name":"hk-vless","server_id":"'"$SRV"'","enabled":true,
  "host":"hk01.example.com","port":443,"external_port":8443,
  "listen":"0.0.0.0","protocol":"vless",
  "stream_settings":{"network":"ws","security":"tls",
    "tlsSettings":{"serverName":"hk01.example.com",
      "certificateFile":"/opt/proxypanel/xray/certs/fullchain.pem",
      "keyFile":"/opt/proxypanel/xray/certs/privkey.pem"},
    "wsSettings":{"path":"/ws"}},
  "protocol_settings":{"decryption":"none"},
  "client_settings":{"serverName":"hk01.example.com"}}'

# 改节点必须带 version
NODE=$(api "$PANEL/api/v1/admin/nodes?limit=200" | jq -r '.items[0]')
VER=$(echo "$NODE" | jq -r .version)
api -X PATCH "$PANEL/api/v1/admin/nodes/$(echo "$NODE" | jq -r .id)" \
  -d '{"version":'"$VER"',"enabled":false}'
```

`external_port` 是「成员客户端连接的端口」。上例里服务端监听 443，但成员拿到的是
`hk01.example.com:8443` —— 用于「中转机 8443 → 本机 443」这类转发，转发本身由你自己配。

---

## 8. 订阅输出

`GET /sub/{token}?format=<fmt>`，`fmt` ∈ `xray` | `singbox` | `mihomo` | `v2rayn`。
不带 `format` 时按 `User-Agent` 猜测。

响应头是 `Cache-Control: no-store`。配额和到期**不在响应头里**（不发
`Subscription-Userinfo`），成员在面板 `/me/status` 查看。

细节见 [订阅格式](subscription-formats.md)。
