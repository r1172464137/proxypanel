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

**CSRF**：会话认证的写请求（包括 logout）必须带
`X-CSRF-Token`，值与登录/`GET /auth/session` 返回的 `csrf_token` 一致。
缺失或不等 → `403 CSRF_FAILED`。login/redeem 不要求会话 CSRF，但另有 Origin / Sec-Fetch-Site 跨站校验；不能把这两个公开入口当成不受跨站保护。

**会话 Cookie 属性**由环境变量控制：`PANEL_COOKIE_SAMESITE`（默认 `strict`）、
`PANEL_COOKIE_DOMAIN`、`PANEL_DEV`（为 true 时去掉 `Secure`）。

登录失败按「IP + 用户名」计数限流，超过阈值 `429 RATE_LIMITED`。

---

## 2. 响应包络

成功（对象）：

```json
{ "data": { "status": "ok" }, "request_id": "" }
```

上例为 `/healthz` 的完整 JSON 形状。对象/动作包络当前另带空字符串 `request_id`；请求追踪使用响应头 `X-Request-Id`，错误体中也有真实请求 ID。

成功（列表）：通常 `items` 是数组；`/me/usage` 是下文规定的对象例外。空页示例：

```json
{ "items": [], "next_cursor": null }
```

已实现的 keyset 分页范围、排序及过滤绑定见 [分页契约](#9-分页检索与对象形状)。`next_cursor` 是不透明字符串或 `null`，不是恒空占位字段。

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
| `VALIDATION_FAILED` | 400 | 非游标入参不合法；字段细节见 `field_errors` |
| `CURSOR_INVALID` | 400 | 游标非法或作用域失效；附 `field_errors.cursor` |
| `CONFLICT` | 409 | 版本冲突或引用阻止删除（模板见 §10） |
| `LAST_ADMIN` | 409 | 会移除最后一个可用管理员 |
| `PREVIEW_REQUIRED` | 409 | 该操作必须走预览-确认两段式 |
| `PREVIEW_STALE` | 409 | 预览已失效（目标集合变了） |
| `PREVIEW_CONCLUSION_CHANGED` | 409 | 预览结论与提交时不一致 |
| `IDEMPOTENCY_KEY_REUSED` | 409 | 同一幂等键被用于不同载荷 |
| `RECOVERY_MODE` | 409 | 恢复协调下普通授权写冻结；救援例外见 §5.2 |
| `READINESS_BLOCKED` | 409 | 不安全升级/业务启动被拒绝，附真实 `details.blockers`；不用于拒绝只读诊断或进入恢复 |
| `NODE_IN_USE` | 409 | 服务器/节点组仍被引用，或方案仍有未归档绑定成员 |
| `SUBSCRIPTION_NOT_BINDABLE` | 409 | 目标订阅不可绑定 |
| `REVISION_STALE` | 409 | Agent 提交的 revision 落后 |
| `BATCH_PAYLOAD_MISMATCH` | 409 | 用量批次载荷与账本不一致 |
| `BATCH_OWNERSHIP_MISMATCH` | 403 | 用量样本不属于该 Agent |
| `PROTOCOL_NOT_SUPPORTED` | **422** | 客户端格式无法表达该节点 |
| `BATCH_CLOCK_SKEW` | **422** | 用量批次时间戳偏移过大 |
| `RATE_LIMITED` | 429 | 触发限流 |
| `DB_UNAVAILABLE` | 503 | 数据库连接、关闭/重启或超时故障，可重试；不返回驱动原文 |
| `AGENT_AUTH_FAILED` | 401 | Agent 令牌无效 |
| `SNAPSHOT_SCHEMA_UNSUPPORTED` | 400 | 快照 schema 版本不支持 |
| `LEDGER_UNATTRIBUTABLE` | 422 | 样本无法归属已证明区间，不落账、不 ACK |
| `INTERNAL` | 500 | 未预期错误 |

非法/失效游标返回 `400 CURSOR_INVALID`；limit、q、未知/重复参数等非游标验证仍为 `400 VALIDATION_FAILED`。readiness 的 `blocked/unknown` 是只读诊断响应中的状态，不等同该 GET 返回 HTTP 409（见 §11）。

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
| GET | `/install-agent.sh` | 当前 Panel 渲染的一键安装脚本；不含注册码或永久 token |
| GET | `/install-agent/linux-{amd64\|arm64}` | 与当前 Panel 同提交构建的 Agent 二进制；脚本按启动时固定的 SHA256 校验 |
| GET | `/sub/{token}` | 客户端订阅输出，`?format=` 见下方 |

### 5.2 成员自助（`member` 及以上）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/me/status` | 配额、用量、剩余天数、可用节点 |
| GET | `/api/v1/me/usage` | `items={usage,adjustments}` 对象；按 usage 分页 |
| GET | `/api/v1/me/nodes` | 我能用的节点及连接参数 |
| GET | `/api/v1/me/subscription` | 订阅令牌与链接 |
| POST | `/api/v1/me/subscription/token/rotations` | 轮换订阅令牌 ⚠️ |
| GET | `/api/v1/me/sessions` | 我的登录会话 |
| DELETE | `/api/v1/me/sessions/{id}` | 仅撤销本人非当前会话；恢复期间允许 |
| POST | `/api/v1/me/credentials/rotations` | 轮换我的代理凭据 ⚠️ |
| POST | `/api/v1/me/password` | 改自己的密码 |

⚠️ = 恢复协调模式期间返回 `409 RECOVERY_MODE`（它们都改授权相关状态）。
修改自己密码、撤销本人的其他会话、login/logout **不拦**：这些是救援路径，仍保留身份、CSRF 或登录跨站/限流校验。DELETE 当前 session 返回 `400 VALIDATION_FAILED`，不撤销；退出当前会话应调用 `POST /api/v1/auth/logout`。不能操作他人 session（404）。

恢复期间 `GET /me/subscription` 对**已有 token**只读返回；没有 token 时惰性创建被拒，返回 `409 RECOVERY_MODE`。客户端只降级订阅导入/链接区域，不要把仍可读取的 status/usage 整页替换成错误。轮换 token 与代理凭据仍冻结。

### 5.3 管理端资源

伺服端（服务器 / 节点）：

| 方法 | 路径 |
|---|---|
| GET / POST | `/api/v1/admin/servers` |
| GET / PATCH / DELETE | `/api/v1/admin/servers/{id}` |
| POST | `/api/v1/admin/servers/{id}/registration-codes` | 生成一次性注册码；配置安装器时同时返回 `installer_available=true` 与 `install_command` |
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
| GET | `/api/v1/admin/upgrade-readiness` |
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
# 从响应 .data.id 取 preview_id；预览对象还包含 rules_version / member_set_digest 等核对材料。

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

# 建服务器 → 生成一键安装命令
SRV=$(api -X POST "$PANEL/api/v1/admin/servers" \
  -d '{"name":"hk-01","address":"hk01.example.com","region":"hk"}' | jq -r .data.id)
INSTALL=$(api -X POST "$PANEL/api/v1/admin/servers/$SRV/registration-codes" -d '{}')
echo "$INSTALL" | jq -r .data.install_command
# 在目标节点机执行上面的命令；若 installer_available=false，读取 installer_reason 并走手工安装。

# 建节点（TLS 必须配证书，否则 400）
api -X POST "$PANEL/api/v1/admin/nodes" -d '{
  "name":"hk-vless","server_id":"'"$SRV"'","enabled":true,
  "host":"hk01.example.com","port":443,"external_port":8443,
  "listen":"0.0.0.0","protocol":"vless",
  "stream_settings":{"network":"ws","security":"tls",
    "tlsSettings":{"serverName":"hk01.example.com",
      "certificates":[{"certificateFile":"/opt/proxypanel/xray/certs/fullchain.pem",
      "keyFile":"/opt/proxypanel/xray/certs/privkey.pem"}]},
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

## 9. 分页、检索与对象形状

以下分页均为 keyset（非 offset），默认 `limit=50`，整数夹取到 1..200；非整数返回 `400 VALIDATION_FAILED`，`details.field_errors.limit=invalid`。每页多读一条判断后续：确有下一页才返回非空 `next_cursor`，否则 `null`。续页原样 URL 编码 cursor，保持有效过滤参数；可改变 limit。过滤或排序字段在遍历中变化不构成跨请求快照保证，刷新应去掉 cursor，从第一页开始。不要解析/伪造游标或把它当页码。

### 9.1 六个管理核心集合

路径前缀 `/api/v1/admin/`；所有排序元组均升序，字符串使用数据库 lower 与 C 排序，末尾 ID 打破同名平局。

| 集合 | 有效过滤参数（除 limit/cursor） | 稳定排序 |
|---|---|---|
| servers | 无；隐藏 archived | lower(name), id |
| nodes | server_id、q；隐藏 archived | sort_order, lower(name), id |
| node-groups | 无 | lower(name), id |
| subscriptions | 无；包含 archived | lower(name), id |
| members | subscription_id、include_archived、q；默认隐藏 archived | lower(username), id |
| templates | 无 | lower(name), id |

`q` 仅 nodes/members 支持，去首尾空白、至多64字符，大小写不敏感的**字面子串**；`%`、`_`、反斜线不是通配符。`include_archived` 仅大小写不敏感的 `true` 表示包含。cursor 绑定集合、当前管理员 ID 和规范化后的全部过滤值。未知/重复参数返回 `400 VALIDATION_FAILED`，对应 `field_errors.<参数>=invalid`；过长 q 为 `q=too_long`。非法 cursor、跨集合/管理员或改过滤续页返回 `400 CURSOR_INVALID`，保留 `field_errors.cursor=invalid`。旧的未绑定管理员游标需要重新从第一页获取。

### 9.2 七个运维集合

同一 `/api/v1/admin/` 前缀，已全部实现 keyset；时间排序均以 ID 同方向破平局。

| 集合 | 有效过滤参数（除 limit/cursor） | 稳定排序 |
|---|---|---|
| agents | 无 | lower(name), id 升序 |
| core-instances | 无 | lower(node_name), id 升序 |
| invitations | 无 | created_at, id 降序 |
| batches | 无 | created_at, id 降序 |
| events | state、q | created_at, id 降序 |
| tasks | state、batch_id、agent_id | created_at, id 降序 |
| audit-logs | action、actor | created_at, id 降序 |

state/batch_id/agent_id/action 是精确匹配；actor 精确匹配 actor_id 或 actor_username。events.q 对 message 做大小写不敏感的字面子串检索，不去首尾空白，至多64字符（无效 UTF-8 也拒绝）。cursor 绑定**当前管理员、端点、有效过滤值**；改过滤/跨身份/跨集合为 `400 CURSOR_INVALID` + `field_errors.cursor=expired_scope`，损坏/非法为 `cursor=invalid`，q 过长为 `q=too_long`。这组只读取表内声明的有效参数，其他参数当前被忽略，不能据此声称支持 q 搜索 agents/tasks 或全字段搜索。

### 9.3 本人列表与用量对象

`/api/v1/me/nodes`、`/me/sessions`、`/me/usage` 支持 limit/cursor，作用域绑定端点及本人 ID；无 q/额外过滤能力。非法、跨本人或跨端点 cursor 返回 `400 CURSOR_INVALID` + `field_errors.cursor=invalid`。

- nodes：先求**完整授权结果**再分页，按 sort_order、lower(name)、id 升序；不会用分页输入裁剪授权判定。
- sessions：created_at、id 降序，元素含 current 标志；仅返回本人会话。
- usage：period_start 降序、node_id 升序、period_end 升序。`next_cursor` 仅推进 usage；adjustments 只在第一页返回完整集合，后续页为 []。空结果的完整结构：

```json
{"items":{"usage":[],"adjustments":[]},"next_cursor":null}
```

`usage[]` 为 `domain.UsageRow`：member_id、node_id、period_start、period_end、up_bytes、down_bytes、estimated、boundary_split、correction、direction、algorithm_version。`adjustments[]` 为 `domain.QuotaAdjustment`：id、member_id、bytes、reason、period_start（可 null）、created_by、created_at。不能用 `Array.isArray(items)` 解包此接口，也不能累加当前页冒充完整周期摘要。

状态摘要读取 `/me/status`：`data={member,access,nodes_allowed,data_complete,data_issues}`。**known / unknown / unlimited / zero 是解释状态，不是新增 JSON 字段或统一枚举**：数据完整时数值才可按已知（known）展示；`data_complete=false` 时展示未知（unknown），保留已知数值但不伪报正常；完整证据下 `effective_quota_bytes=null` 表示不限量（unlimited），数字0是真实零额度（zero），`used_bytes=0` 是零用量而非未知。`proxy_access="unknown"` 与 `data_issues`（如 cycle_unproven、ledger_unattributable）必须显式呈现，不可把缺周期解释成不限量。

### 9.4 管理用量

`/api/v1/admin/usage?member_id=&node_id=&from=&to=&limit=&cursor=` 返回 `items:UsageRow[]` 和真实 `next_cursor`。from/to 接受 RFC3339 或 YYYY-MM-DD（UTC 零点），按周期起点过滤，to 必须晚于 from。member_id/node_id 为精确过滤；默认 limit=50，整数夹取1..200，非整数、未知/重复参数和非法时间条件为400 VALIDATION_FAILED。

排序为 **period_start DESC、member_id ASC、node_id ASC、period_end ASC**，字符串按 C 排序；聚合保留完整 member/node/period 身份，不切割同一聚合行，sum、flags、direction、algorithm_version 语义不变。cursor 绑定端点、管理员和规范化过滤，不绑定 limit；非法或过滤/身份作用域失配为400 CURSOR_INVALID。采用实时 keyset，不承诺跨请求数据库快照；不能把一页当全集。

## 10. 两个 DELETE 的确定契约（行为变更）

DELETE 不要求 body/query 的 version；旧调用者携带 version 仍兼容，不能外推 PATCH/PUT 的必填规则。

- **方案 `/admin/subscriptions/{id}`**：同事务锁方案，先查未归档绑定成员；有绑定 → `409 NODE_IN_USE`。无当前绑定但存在 `member_bindings` 历史 → 归档（archived=true），返回归档快照并带 `data.retired=true`，保留账本、绑定历史及方案节点组关联；没有绑定历史 → 物理删除。已归档再 DELETE：仍先检查当前绑定；无绑定则幂等返回归档态。不能删历史绕过外键。
- **模板 `/admin/templates/{id}`**：被未归档方案引用 → `409 CONFLICT`，`error.details.reason="template_in_use"`，`details.reference_count` 给出引用数，引用保持不变；解除引用后可物理删，只有已归档方案引用也可删（外键清空那些归档方案的 template_id）。

方案成功响应的 `data` 仍是原 Subscription 快照，并附加 `retired:boolean`：历史退役或归档幂等返回true，物理删除返回false；不存在额外的 subscription 嵌套，GET不增加该字段。物理删除后的重复请求仍按不存在返回404。模板成功保持 `data={deleted:true,id}`；引用冲突返回稳定的 `reason` 与 `reference_count`，不能通过忽略冲突自动解除在用引用。

## 11. 升级 readiness：只读诊断与本地材料边界

`GET /api/v1/admin/upgrade-readiness` 为管理员只读端点，恢复期间可读，返回 `200` + data 对象；诊断状态为 ready / blocked / unknown，检查不可用也回 unknown，不自动修改授权/账本、不执行迁移。

HTTP 和 CLI 从同一内部结果投影公共字段，不另做一套核对：`status`、本次UTC核对时间 `checked_at`、实际Go构建模块版本 `candidate_version`、`schema={applied_migrations:[],pending_migrations:[],checksum_ok}`、`checks[]`、`blockers[]`。没有构建版本元数据时如实返回 `(devel)` 或 `devel`，不据路径猜造发布版本。

`checks[]` 含 `id,severity,state,summary,affected_count,evidence[]`：severity 为 blocker/warning，state 为 pass/fail/unknown；只投影实际存在的核对项，不补造全通过行。blockers 为真实 fail/unknown 的稳定检查ID去重列表。为区分“确知不匹配”和“无法核验”，`schema.checksum_ok` 为 **boolean|null**：完整核验成功 true，已知校验和不符 false，检查不可用/未知迁移等无法判定时 null。不能把 null 显示为已核验成功，也不能凭校验和通过绕过其它业务阻断。

兼容字段继续保留：phase、applied_migrations、pending_migrations、pending_batches、duplicate_batches，可选 stop_verification/database_sha256/evidence_sha256；每条 check 也保留 code/status/count/remedy。新旧字段由同一结果生成。异常 fallback 的旧队列计数0仅是兼容占位，**不是 Agent 本地未确认队列为空的证据**；应先按 status/checks 判定未知。数组空集合为[]。

诊断不可用示例（仅列公共字段，兼容别名省略）：

```json
{"data":{"status":"unknown","checked_at":"2026-10-24T04:00:00Z","candidate_version":"(devel)","schema":{"applied_migrations":[],"pending_migrations":[],"checksum_ok":null},"checks":[{"id":"inspection_unavailable","severity":"blocker","state":"unknown","summary":"Verify database access, schema and configured migration files, then rerun the read-only check.","affected_count":1,"evidence":["check=INSPECTION_UNAVAILABLE; status=unknown; affected_count=1"]}],"blockers":["inspection_unavailable"]},"request_id":""}
```

命令行 `panel check --preflight --evidence /srv/proxypanel-review/manifest.json --json` 使用 **Panel 所在服务器/容器内**的本地只读材料路径；默认材料路径取 `PANEL_UPGRADE_EVIDENCE_FILE`，迁移目录取 `PANEL_MIGRATIONS_DIR`。HTTP query/body 不能选择任意文件，不提供上传/任意路径读取 API。材料必须由操作者预先审查并在服务端准备；不能把浏览器本机路径传给 HTTP 冒充预检。

CLI 输出直接是 UpgradeReadiness 对象（无 data 包络），默认 JSON；ready 退出0，blocked 退出2，unknown 退出3（参数错误也为2）。该命令走 OpenReadOnly，不执行迁移、bootstrap 或启动服务；ready 是材料/数据库检查结果，不等于 Panel 独立观察到了 Agent 停止。升级与回退操作另见 [生产部署](../deployment/production.md) 和 [备份与恢复](../operations/backup-recovery.md)。

本项目是原生 Go 路由/结构体契约，无 OpenAPI/SDK 自动生成链；前端类型是消费镜像，不是另一个接口真相源。
