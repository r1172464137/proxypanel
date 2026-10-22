# 全量端到端测试矩阵

目标：面板 / Web UI / 数据库 / Agent / 节点功能 全覆盖，任何一项不正常都要报出来。

环境：面板容器（nginx + Go）+ 真实 PostgreSQL/Redis + WSL 真实 Agent（0.1.1）+ 真实 Xray 26.3.27（5 节点）。

---

## 层面 1 · API 端点全矩阵（76 个端点）

### 1.1 认证与会话（8）
| 端点 | 正常 | 异常 |
|---|---|---|
| `POST /auth/login` | 正确凭据 200 + csrf + cookie | 错密码 401、限流 429、空字段 400 |
| `POST /auth/logout` | 200 且会话失效 | 无 CSRF 403 |
| `POST /auth/redeem` | 有效邀请码 200 | 无效码、已用过、过期 |
| `GET /auth/session` | 200 带用户 | 未登录 401 |
| `POST /me/password` | 改密成功 | 旧密码错 401、新密码 <10 位 400 |
| `GET /me/sessions` | 列出本人会话 | 只能看到自己 |
| `DELETE /me/sessions/{id}` | 撤销 | 撤别人的 404/403 |
| `GET /me/status` | 200 | 未登录 401 |

### 1.2 资源 CRUD（7 类 × 5 操作 = 35）
对 servers / nodes / node-groups / subscriptions / members / invitations / templates：
- `POST` 创建 → 201
- `GET` 列表 → 200 含新项
- `GET {id}` → 200
- `PATCH {id}` → 200 且字段生效
- `DELETE {id}` → 200 且真的没了

每类额外验证：
- servers：注册码生成
- nodes：protocol/stream 必填校验、端口范围
- node-groups：被订阅引用时 PATCH 需预览（409 PREVIEW_REQUIRED）
- subscriptions：有成员绑定时删除 409；归档成员不阻止删除
- members：配额覆盖开关与回退、停用/启用、凭据轮换、订阅令牌轮换
- invitations：创建、撤销
- templates：content JSON 校验

### 1.3 批量操作（4）
- `POST /batches/preview` → 200 含 items/before/after/consequences
- `POST /batches` → 200，需要 Idempotency-Key
- 同 key 重放 → `idempotent_replay=true` 且同 batch id
- `GET /batches` 列表与 `GET /batches/{id}` 详情 state 一致

### 1.4 运维（13）
overview / agents / core-instances / tasks / events / audit-logs / usage（含 from/to）/
settings（GET+PUT）/ recovery-mode（GET/enter/exit）/ events/{id}/ack / tasks/{id}/retry

### 1.5 成员端（7）
`/me/status` `/me/subscription` `/me/usage` `/me/nodes` `/me/sessions`
`POST /me/subscription/token/rotations` `POST /me/credentials/rotations`

### 1.6 Agent 协议（5）
`/agent/register` `/agent/snapshot` `/agent/applied` `/agent/usage` `/agent/heartbeat`

### 1.7 订阅输出（1）
`GET /sub/{token}?format=xray|singbox|mihomo|v2rayn`
- 有节点 → 200 且内容含节点
- 无权限 → 403 + 明确原因
- 无法表达的格式 → 422 + 字段与节点名

### 1.8 权限矩阵（关键）
| 角色 | 访问 admin 端点 | 访问 /me 端点 |
|---|---|---|
| 未登录 | 401 | 401 |
| member | 403 | 200 |
| admin | 200 | 200（可看自己） |
| 归档成员 | 401（登录应失败） |

---

## 层面 2 · Web UI 全矩阵（30 路由）

### 2.1 渲染矩阵
每个路由 × 4 组合（1440/390 × 明/暗）：
- HTTP 200
- 主内容非空
- 0 console error / 0 pageerror
- 0 横向溢出
- 深色模式下前景/背景对比度可读

### 2.2 状态矩阵
每类列表页：空态、加载态、真实错误态（404/500）
每类表单页：初始态、校验错误态、提交中、提交成功

### 2.3 交互功能
| 功能 | 验证点 |
|---|---|
| 登录/登出 | 跳转正确、`next` 回跳、会话失效 |
| 新建服务器 | 出现在列表 |
| 新建节点（5 模板） | 协议/端口/密钥正确 |
| 节点字段级校验 | 输入框标红 + 就地错误 |
| 新建节点组 → 挂节点 | 需要预览流程 |
| 新建订阅 → 绑成员 | 列表可见 |
| 成员编辑 | 配额覆盖单选、启用/停用开关 |
| 成员批量操作 | 选择 → 预览 → 提交 |
| 凭据/令牌轮换 | 弹窗、确认、结果 |
| 设置保存 | 重载后仍生效 |
| 改密码 | 旧密码失效 |
| 主题切换 | 全站生效且持久化 |
| 移动端抽屉 | 汉堡打开/Esc 关闭/遮罩关闭 |

---

## 层面 3 · 节点功能（Agent + Xray）

### 3.1 Agent 生命周期
- 注册（一次性码）→ 心跳新鲜 → `applied_revision == desired_revision`
- 重复注册应被拒
- 心跳丢失 → 面板标记离线 → 恢复

### 3.2 五协议真实流量
5 节点 × 3 客户端（Xray/sing-box/Mihomo）= **15 格**，每格拉 69,632 字节并逐字节比对 SHA-256。

### 3.3 撤销与时延
- 停用成员 → 新连接拒绝 + 长连接断开，均 < 60s
- 停用节点 → 端口不可用
- 撤回后凭据轮换 → 旧凭据永久失效

### 3.4 离线与队列
- 面板不可达 → Agent 继续服务 + 队列增长
- 恢复 → 队列排空（`n acknowledged 0 retained`）

### 3.5 采样可见性
heartbeat metrics 含 `sampling_errors` / `sampling_retries` / `sampling_instances` / `sampling_counters`

---

## 层面 4 · 账本与配额

- 打 N 次请求 → 账本增量 ≈ 应用层字节（比值 ≤1.5，无重复记账）
- 配额超限 → `over_quota` → 代理被拒
- 清除超额 → 恢复
- 配额覆盖开 → 用覆盖值；关 → 回退订阅额度
- 跨周期：周期切换后用量归零、账本保留

---

## 层面 5 · 数据库

- `schema_migrations` 与 `migrations/*.sql` 数量一致
- 关键表存在且字段齐全
- 外键完整性：无孤儿行（credentials/member_bindings/subscription_node_groups）
- 删除订阅 → `subscription_node_groups` 级联清空
- 归档成员 → `subscription_id` 被解除
- 审计日志无明文密码/token

---

## 层面 6 · 基础设施

- 3 个容器 healthy
- 面板重启后数据仍在、Agent 自动重连
- 面板容器 `--force-recreate` 后仍正常
- 磁盘/内存无异常增长

---

## 执行方式

| 批次 | 内容 | 负责 |
|---|---|---|
| A | 层面 1（API 76 端点 × 角色/异常） | 子代理 1 |
| B | 层面 2（30 路由 × 4 组合 + 交互） | 子代理 2 |
| C | 层面 3+4（节点 / 账本） | 子代理 3 |
| D | 层面 5+6（数据库 / 基础设施） | 我 |
| E | 汇总所有失败项 → 修复 → 复验 | 我 |

**判定标准：任何 FAIL 都必须记录到清单，不允许"基本通过"这种说法。**
