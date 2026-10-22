# 前后端联调结果（2026-10-22）

真实链路：面板容器（nginx + Go）+ 真实 PostgreSQL/Redis + 真实 Agent（5 节点）。
不拦截、不伪造。原始明细：`/home/compile/Work/evidence/integration/api-integration.json`。

## 覆盖与结果

**64 项全部通过。**

| 组 | 项数 | 内容 |
|---|---|---|
| 认证与会话 | 4 | 登录、会话有效、匿名 401 |
| 资源 CRUD | 20 | 服务器/节点组/订阅/成员的创建·读取·更新·删除，配额覆盖开关与回退 |
| 批量操作 | 5 | 预览 → 提交 → 同 Idempotency-Key 幂等重放 |
| 批量列表 | 4 | 新增的 `GET /admin/batches` 与详情一致性 |
| 运维接口 | 13 | 总览/Agent/实例/任务/事件/审计/用量/模板/设置/恢复模式；`from/to` 筛选生效 |
| 成员端 | 11 | 状态/订阅/用量/节点/会话；admin 接口 403；四格式导出 |
| CSRF 与校验 | 5 | 缺/错 token 403、空 body 400、缺幂等键、未知对象 404 |
| 清理 | 4 | 测试数据全部删除 |

## 联调中发现的产品缺陷（未修，登记待办）

### D6 · 批量停用会永久关闭代理权限，重新启用不还原

**现象**：用批量操作把成员 `active` 设为 `false` 后，再单独把 `active` 改回 `true`，
该成员永久处于 `proxy_paused`，订阅输出返回：

```
403 {"code":"FORBIDDEN","message":"no proxy access: proxy_paused"}
```

**根因**（`internal/panel/store/batch_submit.go:343-345`）：

```go
case OpSetActive:
    ...
    if !active {
        m.ProxyEnabled = false   // 停用时顺带关掉代理
    }
    // 恢复 active=true 时没有任何对应逻辑还原 ProxyEnabled
```

**更大的问题是两条路径语义不一致**：

| 路径 | `active=false` 时是否改 `proxy_enabled` |
|---|---|
| 单成员 `PATCH /admin/members/{id}` | **不改**（只有显式传 `proxy_enabled` 才改） |
| 批量 `set_active` | **改成 false** |

同一个"停用"，两种做法。而且批量那条没有测试覆盖，也没有注释说明意图。

**影响**：管理员批量停用一批人再启用，这些人会全部失去代理权限且界面不提示，
必须逐个手动重开代理开关（或重新绑定）。

**建议修法**（二选一）：

- A：批量 `set_active` 也**不**动 `proxy_enabled`，与单成员路径一致；
  撤权职责交给 `proxy_enabled` 字段本身。
- B：保留联动，但**恢复时对称还原**：`set_active(false)` 时记下原
  `proxy_enabled`，`set_active(true)` 时恢复。需要新增字段或从审计推导。

倾向 A：语义单一，不引入隐藏状态。若担心"停用后旧连接仍在"，硬撤权本来就由
`proxy_enabled=false` 显式触发，不该由 `active` 隐式代办。

### 观察（非缺陷）

- `DELETE /api/v1/admin/subscriptions/{id}` 实际是**归档**（`store.DeleteSubscription`
  只把 `Archived=true`），归档后仍引用节点组、仍阻止节点组删除。删除节点组前
  必须先用批量 `update_subscription` 清空 `node_group_ids`。
  → 这是设计选择，但 `DELETE` 语义与行为不符，界面上应说明"归档"而非"删除"。
- `GET /sub/{token}?format=xray` 对含 `allow_insecure` 但无内联证书的节点返回
  `422 PROTOCOL_NOT_SUPPORTED` 并指明字段与节点。**这是正确的显式拒绝**，
  符合"不支持就明确失败，绝不静默丢参数"的契约。

## 我修正过的测试预期（不是产品问题）

| 一开始的失败 | 实际原因 |
|---|---|
| 配额覆盖关闭后 `effective_quota_bytes=None` | 读错层级：详情接口返回 `{member, access}`，额度在 `access` 里 |
| 订阅输出 403 `proxy_paused` | D6 缺陷导致（已在上方登记），测试显式恢复 `proxy_enabled` 绕过 |
| 删除节点组 409 | 归档订阅仍引用节点组，测试清理顺序错误 |
| v2rayN 输出 1 字节 | 测试用的节点组是空的，没有节点可导出 |
| lab 账号 xray 输出 422 | 正确的显式拒绝，改为断言"422 带字段与节点" |
