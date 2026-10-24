# Proxypanel product-readiness 后端技术设计

**生成时间**: 2026-10-23
**状态**: Draft
**主文档**: `.ai/design/product-readiness.md`（由主会话维护；本文件不创建主文档）
**技术栈事实**: Go 1.27.3 + PostgreSQL + Redis（仅登录限流）+ Agent 本地 SQLite（modernc.org/sqlite）+ 官方 Xray。模板中的 Rust/Cargo 默认不适用；本项目不转栈（`DEC-product-readiness-009`）。
**契约地位**: 第 4 章是本方案 API 契约的**唯一设计源**。frontend 分端设计只引用、不重定义。

## 1. 目标与范围（后端）

- 承接主文档目标的后端边界：在不改技术栈、不新增支付/多租户的前提下，把“授权—快照—回执—账本—周期—Agent 执行—升级恢复”做成同一候选版本内可证明一致的可信底座，并给现有前端提供真实分页/检索、可解释不可用原因、可追踪的批量与同步结果。
- 本设计只处理服务端与 Agent 协议边界、数据模型、迁移与后端验证；页面布局、组件、状态分工由 frontend 设计负责。
- 不包含：支付/订单/余额/退款（`DEC-product-readiness-001`）；新增角色或租户（`DEC-product-readiness-003`）；更换语言/框架/网关（`DEC-product-readiness-007`、`DEC-product-readiness-009`）；把“订阅”实体改名或新增收费实体（`DEC-product-readiness-008`）；对任意规模与不可终止 OS 进程的生产 SLA；跨版本拼接通过数字（`DEC-product-readiness-006`）。
- 旧候选补丁（security / agent / store / frontend-protocol / deploy 独立副本与 `/tmp/*.patch`）只作为**候选来源**：本设计规定先完整导出、记录内容指纹、逐项审查、分批整合并回归，任何“补丁自述通过”都不构成通过（`DEC-product-readiness-006`，tech-research §3.2–3.4）。

## 2. 需求来源与决策追踪

- 需求来源：`.ai/prd/core/product-readiness.md`（§4 业务规则、§5 旅程与验收、§8 阶段门禁）、`.ai/user-stories/core/product-readiness.md`（US-PR-001…018）、`.ai/tech-research/product-readiness.md`（§2 依赖事实、§3 差距、§5 风险）。
- 正式 `docs/prd`、`docs/user-stories` 索引不存在，本设计不引用、不伪造。
- 业务规则摘要：两角色（admin/member）；账号状态/代理开关/绑定有效期/订阅授权/额度/节点范围逐层收紧，任何个人覆盖只能替换数值、不能绕过上层限制；保存≠同步≠连接；恢复核对期冻结普通写，自助改密例外；周期按发生区间归属，重发不重复计量；未知数据不得显示为零或正常。

| Decision ID | 状态 | 设计落点 | 说明 |
|---|---|---|---|
| `DEC-product-readiness-001` | Applied | §1、§2 | 只做私有面板，不加交易实体与端点 |
| `DEC-product-readiness-002` | Applied | §4.4、§6、§10 | 流程/文案改动配套可核对后端状态与错误语义 |
| `DEC-product-readiness-003` | Applied | §4.1、§7 | 仅 admin/member 两个身份作用域 |
| `DEC-product-readiness-012` | Applied | §10、§11 | 子代理结论不单独作为证据，必须有可复核运行证据 |
| `DEC-product-readiness-005` | Applied | §10、§11 | 后端测试计划划分“本阶段可验证”和“端到端承接”，不假报 |
| `DEC-product-readiness-006` | Applied | §3、§9.9、§10、§11 | 候选整合门禁、单候选版本、迁移审查 |
| `DEC-product-readiness-007` | Applied | §8、§9.10、§12 | 面板容器不跑 nginx；两种前端托管都保留 |
| `DEC-product-readiness-008` | Applied | §4.3、§4.4、§6 | 仅 UI 术语分离；DTO 字段名保持兼容，不加收费实体 |
| `DEC-product-readiness-009` | Applied | §3、§9 | 复用 Go/Vue 与既有模块，新增应用框架数 0 |

### 2.1 故事覆盖（后端落点）

| 故事 | 后端设计落点 | 契约/实现 | 验证承接 |
|---|---|---|---|
| US-PR-001 | §6.9/§9.10 两种托管、TLS 与启动门禁、`getReady` 版本 | `getHealth`/`getReady`、`cmd/panel/main.go` | web-demo（`scripts/verify-panel-image.sh`） |
| US-PR-002 | §4.2 `getAdminOverview.setup` 真实进度（对象是否存在，不写勾选） | `getAdminOverview` | backend 场景 + frontend 消费 |
| US-PR-003 | §6.6/§6.9 注册码一次性、Agent 在线与回执状态、离线/未知区分 | `createRegistrationCode`/`registerAgent`/`listAgents` | backend 场景 + web-demo |
| US-PR-004 | §6.7 无损编辑、REALITY 成对换钥、字段级校验 | `createNode`/`updateNode`/`getNode` | backend 场景 + web-demo 客户端矩阵 |
| US-PR-005 | §6.4 方案额度/期限/锚日语义、影响面预览 | `createSubscription`/`updateSubscription`/`previewBatch` | backend 场景 |
| US-PR-006 | §6.2 邀请初始化事务、失效/超次/无方案 | `redeemInvitation`/`createInvitation` | backend 场景 |
| US-PR-007 | §6.7 投影与 `format_status`、不可表达 422 | `getMeSubscription`/`getSubscriptionOutput` | backend 场景 + web-demo |
| US-PR-008 | §6.1/§6.4 唯一判定 `access/v1`、`data_complete`、未知不等于零 | `getMeStatus`/`getMeUsage` | backend 场景 + frontend 状态 |
| US-PR-009 | §6.2/§6.3 会话撤销矩阵、旧密码并发、令牌/凭据重置语义 | `changeMePassword`/`listMeSessions`/`deleteMeSession`/轮换端点 | backend 场景 |
| US-PR-010 | §6.5 预览/提交/幂等/旧回执 | `previewBatch`/`submitBatch`/`listBatches`/`getBatch` | backend 场景 |
| US-PR-011 | §6.1/§6.6 撤权真实生效、60 秒窗口由既有验收口径承接 | `getAgentSnapshot`/Agent 收紧与停机 | web-demo（真实核心） |
| US-PR-012 | §6.5/§6.6 任务/回执/重试、坏节点不拖累 | `listTasks`/`retryTask`/`listEvents`/`ackEvent` | backend 场景 + web-demo |
| US-PR-013 | §6.4 周期、迟报、幂等计量、额度恢复 | `submitAgentUsage`/`getAdminUsage`/`getMeUsage` | backend 场景 + web-demo 真实流量 |
| US-PR-014 | §6.3 恢复门禁、自助改密例外、退出核对 | `enterRecoveryMode`/`exitRecoveryMode`/`getRecoveryMode` | backend 场景 + web-demo |
| US-PR-015 | 不适用（后端无新增；可访问性属前端职责，后端只保证错误/空/加载可区分） | — | frontend 设计 |
| US-PR-016 | §6.9/§9.9 升级预检、迁移、备份/回退 | `getUpgradeReadiness`、`panel check` | backend 场景 + web-demo |
| US-PR-017 | §7 作用域、秘密只写、错误/日志不泄露 | 全部端点 + `security_regression_test.go` | backend 场景 |
| US-PR-018 | §6.8 真实分页/检索、批量目标集合 | `listMembers`/`listNodes`/`listBatches` 等 | backend 场景 |

## 3. 现有实现分析（后端，仅现状）

- `internal/panel/api/api.go`：`http.ServeMux` 路由 + `requireUser`/`requireCSRF`/`requireAdmin`/`requireAgent`/`requireWritable` 中间件；响应壳 `writeData`→`{"data":…,"request_id":""}`、`writeList`→`{"items":[…],"next_cursor":…}`、`writeError`→`{"error":{code,message,retryable,request_id,details?}}`；`X-Request-Id` 由 `withRequestID` 写头。
- `internal/panel/api/handlers.go`：登录（限流键 `login:ip:username`，10 次/分钟）、session、登出、邀请兑换、`/sub/{token}` 输出、me 系列、Agent 五个端点。
- `internal/panel/api/admin.go`、`admin2.go`、`batches.go`、`helpers.go`：admin CRUD、批量预览/提交、事件/任务/审计/设置/恢复模式；`requireVersion` 要求 PATCH/PUT 携带 `version>0`；`nullable[T]` 区分“省略”和“显式 null”；`previewRequired` 对影响已绑定成员的订阅/节点组编辑返回 409 `PREVIEW_REQUIRED`。
- `internal/panel/store`：`access.go`（唯一授权证据装载 + `Evidence.Decision`）、`snapshot.go`（`BuildSnapshot` 先聚合后回读 `desired_revision`）、`members.go`（成员/会话/邀请/令牌/凭据）、`usage.go`（批量幂等、周期推进、额度落账）、`batch_preview.go`/`batch_submit.go`（预览 TTL 10 分钟、`rules_version`+`member_set_digest`、幂等 key 指纹）、`ops.go`（Agent 注册/心跳/回执、任务/事件/总览、恢复模式）、`maintenance.go`（告警与通知 outbox）、`resources.go`（资源 CRUD 与 `listAllNodes`/`internalPageSize=500`）。
- `internal/panel/access/access.go`：`access/v1` 冻结判定顺序；`internal/panel/period/period.go`：`period/v1` 账期算法（锚日夹取、DST 处理、`time/tzdata` 内嵌）。
- `internal/agent`：`supervisor`（同步/应用/采样/心跳/到期/关闭）、`xray/manager.go`（Apply/Stop/StopAll/QueryStats/NextExpiry、pidfile 与占用校验）、`state/store.go`（SQLite 元数据/实例/计量基线/待发批次）、`reporting`（累计计数采样→成员增量）。
- `internal/contracts/agent.go`：`proxypanel.agent/v1`（Snapshot/ApplyReport/UsageBatch/Heartbeat）。
- 迁移现状：`migrations/0001_init.sql`…`0004_node_external_port.sql`；迁移器按文件名字典序在 PG advisory lock 下执行并校验 sha256（`store.Migrate`），已应用文件被改动即失败。
- 前端实际消费面：`web/src/api/endpoints.ts`（全部 admin/me/auth 端点）与 `web/src/views/admin/BatchListView.vue`（直接 `api.page('/api/v1/admin/batches')`，因 endpoints 只封装了 preview/submit/get）。
- 可复用点：统一授权算法与周期算法、预览/提交/回执/事件/审计链路、`ImpactPreviewDialog` 所需的服务端影响数据、`scripts/verify-panel-image.sh` 的双托管隔离烟测、`tests/e2e/*` 真核心/浏览器入口。
- 受影响边界（详见 §12）：`api` 列表与错误语义、`store` 快照/周期/账本/幂等/门禁、`period`、`access`、`agent`（supervisor/xray/state/reporting）、`contracts`、`protocol`/`clientconfig` 投影、迁移与 `cmd/panel`、测试与部署脚本。

**现状关键缺口（本设计要关闭，均有源码依据）**

1. 列表固定 `clampLimit(1..200)` 且 `next_cursor` 恒为 `null`（`docs/design/implementation-contract.md:29`），成员/节点页的前端过滤只是当前页过滤；US-PR-018 要求的“真实全部结果”不成立。
2. `BuildSnapshot` 先读成员/证据/密钥，再单独回读 `agents.desired_revision` 贴到旧内容上，中间提交的撤权可能被当成“最新 revision”发布（tech-research §5 风险）。
3. `UpdateMember`/`RevokeAllSessions`/`CreateSession` 之间存在旧密码并发窗口；邀请兑换只建成员，不保证周期/到期/凭据与邀请声明一致。
4. 恢复门禁只在 HTTP 层（`requireWritable`）做预检，store 事务本身不复查 `recovery_mode`；密码自助例外没有统一定义。
5. `usage_entries` 只按重算窗口落账，没有“已记录区间”这一权威；迟报/规则变更可能把字节记到错误周期。
6. 批量幂等 key 只存在于 `batches` 表，保留期清理后可被复用；`tasks` 无 revision 绑定，旧回执可能完成新任务。
7. 候选补丁中的 0009 自动暂停/自动补算、0005/0007 触发器依赖 Go/DB 双时钟，均未审查通过，不能直接采用。

## 4. API 接口设计

> 本章是唯一契约源。所有路径参数占位符为 camelCase。返回契约摘要时，列表操作使用信封字段 `items`/`next_cursor`，单对象/动作操作使用 `data` 内业务字段；两类都按本节声明解释，不混用。响应壳沿用现状（见 §3）：单对象/动作 `{"data": …}`；列表 `{"items":[…], "next_cursor": <string|null>}`（仅带额外顶层键者在本章单独标明）；错误 `{"error":{code,message,retryable,request_id,details?}}`。下文“请求字段/响应字段”若标注**业务字段**，指已解包后的对象（`data` 内或 `items[]` 元素），不是信封路径；契约摘要使用同一口径。

### 4.1 接口清单

权限缩写：`public`=无需会话；`session`=任意已登录且未归档账号；`admin`=session + `role=admin`；`agent`=Bearer Agent token。写操作（非 GET/HEAD）均额外要求 `X-CSRF-Token`（`session/admin`）且受恢复门禁约束，例外见 §4.4。

| Operation ID | 方法 | 路径 | 用途 | 权限/身份 | 调用方 |
|---|---|---|---|---|---|
| getHealth | GET | `/healthz` | 存活探测 | public | internal（部署/探针） |
| getReady | GET | `/readyz` | 依赖就绪（PG/Redis 降级） | public | internal（部署/探针） |
| login | POST | `/api/v1/auth/login` | 账号密码登录，建会话与 CSRF；跨站 Origin 拒绝 | public | frontend |
| getSession | GET | `/api/v1/auth/session` | 当前会话用户与 CSRF | session | frontend |
| logout | POST | `/api/v1/auth/logout` | 撤销当前会话 | session | frontend |
| redeemInvitation | POST | `/api/v1/auth/redeem` | 邀请码注册并开通 | public | frontend |
| getSubscriptionOutput | GET | `/sub/{token}` | 按格式输出成员客户端配置 | public（持有 token） | frontend（复制/下载链接；客户端直接拉取的是同一 URL） |
| getMeStatus | GET | `/api/v1/me/status` | 本人授权判定、状态与用量摘要 | session（自身） | frontend |
| listMeNodes | GET | `/api/v1/me/nodes` | 本人可用节点 | session（自身） | frontend |
| getMeUsage | GET | `/api/v1/me/usage` | 本人用量明细与配额调整 | session（自身） | frontend |
| getMeSubscription | GET | `/api/v1/me/subscription` | 订阅链接、令牌（本人）与格式可用性 | session（自身） | frontend |
| rotateMeSubscriptionToken | POST | `/api/v1/me/subscription/token/rotations` | 重置订阅链接 | session（自身） | frontend |
| rotateMeCredentials | POST | `/api/v1/me/credentials/rotations` | 重置连接凭据 | session（自身） | frontend |
| changeMePassword | POST | `/api/v1/me/password` | 自助改密并撤销其他会话 | session（自身） | frontend |
| listMeSessions | GET | `/api/v1/me/sessions` | 本人会话列表 | session（自身） | frontend |
| deleteMeSession | DELETE | `/api/v1/me/sessions/{sessionId}` | 撤销本会话以外的指定会话 | session（自身） | frontend |
| registerAgent | POST | `/api/v1/agent/register` | 注册码换取 Agent token；跨 server/已用/过期拒绝 | public（一次性注册码） | agent |
| getAgentSnapshot | GET | `/api/v1/agent/snapshot` | 拉取权威快照；revision+generation 相同返回 204 | agent | agent |
| reportAgentApplied | POST | `/api/v1/agent/applied` | 上报应用回执 | agent | agent |
| submitAgentUsage | POST | `/api/v1/agent/usage` | 上报用量批次，持久提交后 ACK | agent | agent |
| agentHeartbeat | POST | `/api/v1/agent/heartbeat` | 心跳与实例/健康状态 | agent | agent |
| getRecoveryMode | GET | `/api/v1/admin/recovery-mode` | 读取恢复模式与 control generation | admin | frontend |
| enterRecoveryMode | POST | `/api/v1/admin/recovery-mode/enter` | 进入恢复核对，轮换 generation | admin | frontend |
| exitRecoveryMode | POST | `/api/v1/admin/recovery-mode/exit` | 显式核对后退出恢复模式 | admin | frontend |
| getAdminOverview | GET | `/api/v1/admin/overview` | 工作台计数、用量、在线、待办、最近事件、首次设置进度（新增字段） | admin | frontend |
| getUpgradeReadiness | GET | `/api/v1/admin/upgrade-readiness` | 迁移/存量资料升级前检结果（**新增**） | admin | frontend |
| listAgents | GET | `/api/v1/admin/agents` | Agent 列表（含在线/修订） | admin | frontend |
| listCoreInstances | GET | `/api/v1/admin/core-instances` | 核心实例状态 | admin | frontend |
| listServers | GET | `/api/v1/admin/servers` | 服务器列表 | admin | frontend |
| createServer | POST | `/api/v1/admin/servers` | 新建服务器 | admin | frontend |
| getServer | GET | `/api/v1/admin/servers/{serverId}` | 服务器详情 | admin | frontend |
| updateServer | PATCH | `/api/v1/admin/servers/{serverId}` | 编辑服务器（version 必填） | admin | frontend |
| deleteServer | DELETE | `/api/v1/admin/servers/{serverId}` | 归档服务器（现状即归档；有节点时 409） | admin | frontend |
| createRegistrationCode | POST | `/api/v1/admin/servers/{serverId}/registration-codes` | 生成一次性接入注册码 | admin | frontend |
| listNodes | GET | `/api/v1/admin/nodes` | 节点列表（server_id、q、cursor） | admin | frontend |
| createNode | POST | `/api/v1/admin/nodes` | 新建节点（协议参数校验+REALITY 密钥） | admin | frontend |
| getNode | GET | `/api/v1/admin/nodes/{nodeId}` | 节点详情（不含明文密钥） | admin | frontend |
| updateNode | PATCH | `/api/v1/admin/nodes/{nodeId}` | 无损编辑节点（version 必填） | admin | frontend |
| deleteNode | DELETE | `/api/v1/admin/nodes/{nodeId}` | 归档节点（现状即归档，并清节点组成员） | admin | frontend |
| listNodeGroups | GET | `/api/v1/admin/node-groups` | 节点组列表 | admin | frontend |
| createNodeGroup | POST | `/api/v1/admin/node-groups` | 新建节点组 | admin | frontend |
| getNodeGroup | GET | `/api/v1/admin/node-groups/{groupId}` | 节点组详情 | admin | frontend |
| updateNodeGroup | PATCH | `/api/v1/admin/node-groups/{groupId}` | 编辑节点组（影响已用订阅需预览） | admin | frontend |
| deleteNodeGroup | DELETE | `/api/v1/admin/node-groups/{groupId}` | 物理删除节点组（现状即物理删；被订阅引用时 409） | admin | frontend |
| listSubscriptions | GET | `/api/v1/admin/subscriptions` | 服务方案列表 | admin | frontend |
| createSubscription | POST | `/api/v1/admin/subscriptions` | 新建服务方案 | admin | frontend |
| getSubscription | GET | `/api/v1/admin/subscriptions/{subscriptionId}` | 服务方案详情 | admin | frontend |
| updateSubscription | PATCH | `/api/v1/admin/subscriptions/{subscriptionId}` | 编辑方案（影响已绑定成员需预览） | admin | frontend |
| deleteSubscription | DELETE | `/api/v1/admin/subscriptions/{subscriptionId}` | 退役方案：有绑定成员 409；有绑定历史则归档、否则物理删（**策略变更，见 §4.3**） | admin | frontend |
| listMembers | GET | `/api/v1/admin/members` | 成员列表（subscription_id、include_archived、q、cursor；派生字段） | admin | frontend |
| createMember | POST | `/api/v1/admin/members` | 直接创建成员并开通 | admin | frontend |
| getMember | GET | `/api/v1/admin/members/{memberId}` | 成员详情（`{member, access}`） | admin | frontend |
| updateMember | PATCH | `/api/v1/admin/members/{memberId}` | 编辑/停用/绑定成员（version 必填） | admin | frontend |
| deleteMember | DELETE | `/api/v1/admin/members/{memberId}` | 归档成员（现状即归档）并撤会话/凭据 | admin | frontend |
| rotateMemberSubscriptionToken | POST | `/api/v1/admin/members/{memberId}/subscription-token/rotations` | 管理员重置成员订阅链接 | admin | frontend |
| rotateMemberCredentials | POST | `/api/v1/admin/members/{memberId}/credentials/rotations` | 管理员重置成员连接凭据 | admin | frontend |
| createMemberQuotaAdjustment | POST | `/api/v1/admin/members/{memberId}/quota-adjustments` | 审计化额度调整 | admin | frontend |
| listTemplates | GET | `/api/v1/admin/templates` | 客户端模板列表 | admin | frontend |
| createTemplate | POST | `/api/v1/admin/templates` | 新建模板 | admin | frontend |
| getTemplate | GET | `/api/v1/admin/templates/{templateId}` | 模板详情 | admin | frontend |
| updateTemplate | PATCH | `/api/v1/admin/templates/{templateId}` | 编辑模板 | admin | frontend |
| deleteTemplate | DELETE | `/api/v1/admin/templates/{templateId}` | 物理删除模板（**新增被引用时 409**，见 §4.3） | admin | frontend |
| listInvitations | GET | `/api/v1/admin/invitations` | 邀请列表（不返回明文 code） | admin | frontend |
| createInvitation | POST | `/api/v1/admin/invitations` | 创建邀请（可预分配方案） | admin | frontend |
| revokeInvitation | POST | `/api/v1/admin/invitations/{invitationId}/revoke` | 撤销未兑换邀请 | admin | frontend |
| getAdminUsage | GET | `/api/v1/admin/usage` | 用量查询（member_id/node_id/from/to/limit/cursor） | admin | frontend |
| previewBatch | POST | `/api/v1/admin/batches/preview` | 生成影响预览（10 分钟有效） | admin | frontend |
| submitBatch | POST | `/api/v1/admin/batches` | 提交预览（Idempotency-Key 必填） | admin | frontend |
| listBatches | GET | `/api/v1/admin/batches` | 变更记录列表 | admin | frontend |
| getBatch | GET | `/api/v1/admin/batches/{batchId}` | 变更记录详情与逐对象结果 | admin | frontend |
| listEvents | GET | `/api/v1/admin/events` | 告警/事件列表（state、q、cursor） | admin | frontend |
| ackEvent | POST | `/api/v1/admin/events/{eventId}/ack` | 确认事件 | admin | frontend |
| listTasks | GET | `/api/v1/admin/tasks` | 同步任务列表（state、batch_id、agent_id、cursor） | admin | frontend |
| retryTask | POST | `/api/v1/admin/tasks/{taskId}/retry` | 重试失败任务（新 revision 绑定） | admin | frontend |
| listAuditLogs | GET | `/api/v1/admin/audit-logs` | 审计日志（action、actor、cursor） | admin | frontend |
| getSettings | GET | `/api/v1/admin/settings` | 面板设置（token 只回 set 标志） | admin | frontend |
| updateSettings | PUT | `/api/v1/admin/settings` | 更新设置（version 必填） | admin | frontend |

**新增操作说明（仅 1 个新端点）**：`getUpgradeReadiness` 为交付门禁所需的最小只读端点；其余能力通过扩展既有端点实现，不新铺后台。首次设置进度放在 `getAdminOverview` 的响应中新增 `setup` 对象，不新增端点。

### 4.2 关键接口详情

**getHealth / getReady（探针，复用 + 附加构建信息）**

- `GET /healthz` → `data:{status:"ok"}`，不触库。
- `GET /readyz` → `data:{status:"ok"|"degraded", postgres:"ok", redis:"ok"|"disabled"|"unavailable", version, commit}`；PG 不可用返回 503 `DB_UNAVAILABLE`；Redis 不可用只降级（登录限流走本地有界兜底），不判失败。`version`/`commit` 来自 `-ldflags`，用于部署比对前后端版本。

**login / getSession（复用，字段不变）**

- `POST /api/v1/auth/login`，`public`；请求业务字段：`username`(string,必填)、`password`(string,必填)；响应 `data`：`user`(Member 视图，见 §4.3 `user` 子集：`id,username,role,active,proxy_enabled,archived`) + `csrf_token`(string)。`Set-Cookie: pp_session`（HttpOnly、Secure、SameSite 由配置）。
- 错误：400 `VALIDATION_FAILED`（`details.field_errors`，含非 `application/json` 请求体）；403 `CSRF_FAILED`（`Origin` 不在同源/`PANEL_PUBLIC_URL`/`PANEL_ALLOWED_ORIGINS` 白名单内，或 `Sec-Fetch-Site: cross-site` 且无 Origin）；401 `UNAUTHENTICATED`（统一文案，不区分用户不存在/密码错/停用）；429 `RATE_LIMITED`。
- 设计变更：`CreateSessionForPassword` 在同一事务内锁定成员行并复核密码哈希，关闭“改密与登录并发”窗口（§9.3）；响应字段不变。

**registerAgent / agent 端鉴权与归属错误（补充口径）**

- `POST /api/v1/agent/register`：注册码与目标 server 绑定，因此不存在“跨 server 注册码”；无效/已用/过期/跨 server 误用都表现为 `401 AGENT_AUTH_FAILED`，响应不透露 code 属于哪个 server。注册在事务内受恢复门禁约束 → 恢复冻结期 **409 `RECOVERY_MODE`**。错误：400 `VALIDATION_FAILED`、401 `AGENT_AUTH_FAILED`、409 `RECOVERY_MODE`。
- 其余 Agent 端点用 Bearer token 鉴权：token 无效/已撤销 → 401 `AGENT_AUTH_FAILED`；**归属不符给 403**：`submitAgentUsage` 中 `node_id` 不属于该 Agent 的 server（按样本时点归属，§6.4.2）→ 403 `BATCH_OWNERSHIP_MISMATCH`；`member_id` 在该节点无凭据历史 → 403 同码；`reportAgentApplied`、`agentHeartbeat` 上报的 `instance_id` 不属于本 Agent 的 server（`validateAgentInstancesTx`：实例不存在或 server 不符，含移机并发复核）→ **403 `FORBIDDEN`**。恢复冻结期快照 → 409 `RECOVERY_MODE`。`getAgentSnapshot`/`reportAgentApplied` 的 revision/generation 不符 → 409 `REVISION_STALE`。
- 不把归属错误改成 401：401 会让 Agent 误判凭据失效并停止上报，403 才能区分“凭据有效但不能报这个对象”。

**redeemInvitation（邀请初始化，语义修正）**

- `POST /api/v1/auth/redeem`，`public`；请求：`code`(string,必填)、`username`(string,必填)、`password`(string,必填)。
- 响应 `data`：成员视图（`id,username,role,active,proxy_enabled,subscription_id,expires_at,period_start,period_end,period_timezone,period_anchor_day,bound_at`），**不返回**密码哈希、令牌或邀请他人信息。
- 事务内保证：邀请行 `FOR UPDATE` 校验未撤销/未用尽/未过期/未超次；插入成员；若邀请带 `subscription_id`，同一事务内按该方案写入绑定、当前周期区间（`member_periods` 记录）、`default_valid_days` 到期、有效凭据并发布 revision；邀请 `used_count+1`、`used_at`、`used_by_member_id` 同事务。不带方案的邀请只建账号，`subscription_id=null`。
- 错误：400 `VALIDATION_FAILED`（邀请不可用统一文案，不泄露邀请状态细节；用户名冲突 `field_errors.username=duplicate`）；**429 `RATE_LIMITED`**（来源/兑换工作量限流）；409 `RECOVERY_MODE`（恢复核对期禁止注册）。
- 并发：同一 code 并发兑换只有一个成功（行锁 + 唯一 `code_hash`）；不同成员并发不影响。

**listMeNodes（成员可用节点）**

- `GET /api/v1/me/nodes` → `items[]`（已解包）：`id,name,protocol,host,port,network,security,flow`；`next_cursor` 由服务端计算（成员节点数超过单页上限时）。只返回当前判定允许的节点，不返回凭据、私钥或服务端密钥。

**getMeStatus / getMeSubscription（成员可见状态）**

- `GET /api/v1/me/status` → `data`：`member`(Member 视图含派生 `used_bytes,effective_quota_bytes,over_quota,access_reasons,proxy_access,allowed_node_ids`)、`access`(Decision：`algorithm_version,member_id,access,proxy_access,access_reasons,access_chain[],effective_quota_bytes,quota_source,used_bytes,expires_at,allowed_node_ids[],node_reasons`)、`nodes_allowed`(int)、**新增** `data_complete`(bool) 与 `data_issues`(string[]，稳定码如 `cycle_unproven`、`ledger_unattributable`)：数据不完整时既不得报 0 用量，也不得静默按“不限量”放行。
- `GET /api/v1/me/subscription` → `data`：`url`(string)、`token`(string，本人可见；正常模式可惰性创建；恢复期间已有令牌只读返回，没有令牌则409 `RECOVERY_MODE`且不创建；成员首页仅导入区降级，状态/用量读取继续)、`formats`(string[]，保持既有四种)、**新增** `format_status`(`[{format,available,reason}]`，reason 为稳定码：`ok`、`nodes_empty`、`protocol_unsupported:{field}`、`node_incomplete:{field}`)、**新增** `expires_at`、`period_end`、`rotation_impact`(固定说明码 `old_link_invalid_new_required`)。`formats` 语义不变，新增字段为附加。
- 错误：401 `UNAUTHENTICATED`；轮换类写接口在恢复冻结期 409 `RECOVERY_MODE`（见 §6.3）。

**listMembers / getMember（真实分页与检索）**

- `GET /api/v1/admin/members?subscription_id=&include_archived=&q=&limit=&cursor=`：
  - `limit` 1..200（默认 50，越界夹取）；`cursor` 为不透明 base64url（§9.8）；`q` 对 `username` 做转义后的 `ILIKE '%q%'`（≤64 字符）。
  - 排序固定 `lower(username), id`；`next_cursor` 仅在还有下一页时非空。
  - 响应元素为**已解包**成员视图：存储字段 + 派生 `used_bytes,effective_quota_bytes,access_reasons[],allowed_node_ids[],proxy_access,over_quota,data_complete,data_issues[]`；`access_reasons`/`allowed_node_ids` 恒为数组（不出现 null）。
- `GET /api/v1/admin/members/{memberId}` → `data`：`{member, access}`（保持现状；frontend 解包）。`member` 新增 `data_complete,data_issues`。
- 错误：400 `VALIDATION_FAILED`（`field_errors.cursor=invalid|expired_scope`、`q=too_long`）；404 `NOT_FOUND`。
- 兼容：不存在的 `cursor` 不静默从头开始（避免“旧选择范围”被当作新结果）。

**createMember / updateMember（开通、停用、绑定）**

- `POST /api/v1/admin/members`：请求 `username,password,role("admin"|"member"),active,proxy_enabled,subscription_id,expires_at,quota_override_bytes,quota_override_enabled`；`quota_override_enabled=true` 时 `quota_override_bytes` 必填。响应 201 + 成员视图。
- `PATCH /api/v1/admin/members/{memberId}`：`version`(必填)、`username,password,role,active,proxy_enabled,subscription_id,expires_at,quota_override_bytes,quota_override_enabled`；`null` 表示清除（解绑/去掉个人到期/恢复继承）。
- 事务内保证：绑定校验方案未归档且 `allow_new_bindings`；建立/保持运行周期并在 `member_periods` 记录区间；绑定变更不重写已记录区间；`active=false` 或改密后撤销会话；凭据同步与 revision 发布同事务。
- 错误：400 `VALIDATION_FAILED`；404 `NOT_FOUND`；409 `CONFLICT`（version 冲突，`details.current_version`）、`SUBSCRIPTION_NOT_BINDABLE`、`LAST_ADMIN`（撤销最后一个管理员）、`PREVIEW_REQUIRED`（方案影响面）。

**previewBatch / submitBatch（批量提交与核对）**

- `POST /api/v1/admin/batches/preview`：`operation`(`assign_subscription|set_active|set_expiry|clear_override|update_subscription|update_node_group`)、`member_ids[]`、`values{}`、`reason`。响应 `data`：`id,operation,version,rules_version,member_set_digest,member_count,affected_count,affected_nodes,affected_agents,items[{member_id,username,before,after,consequences[],blocked,blocked_reason,over_quota_after,has_access_after,nodes_added,nodes_removed,expiring}],exceptions[],warnings[],conclusion,expires_at,created_at`。
- `POST /api/v1/admin/batches`（必须 `Idempotency-Key` 头）：请求 `preview_id,rules_version,member_set_digest,confirm`。响应 `data`：`id,preview_id,operation,state,results[{member_id,node_id,agent_id,state,task_id,error,retryable}],summary,member_count,idempotent_replay,created_at`。
- 门禁顺序（同一事务）：锁 `control_state`（恢复门禁）→ 幂等 key 查重（同 key 同指纹回放持久结果；不同指纹 409 `IDEMPOTENCY_KEY_REUSED`）→ 锁 preview（已消费 409 `CONFLICT`；过期 409 `PREVIEW_STALE`）→ `rules_version`/成员集合摘要比对（不一致 409 `PREVIEW_STALE`，`retryable=true`）→ 动态结论重算（变化 409 `PREVIEW_CONCLUSION_CHANGED`）→ 应用变更、写 `batches`/`batch_items`/`tasks`/`batch_idempotency`、审计。**新增**：`batch_idempotency` 保留期外仍能回答“key 已用”，回放不重新执行。
- 错误：400 `VALIDATION_FAILED`（缺 `Idempotency-Key`/`confirm=true`）；409 上列各码（含恢复冻结 `RECOVERY_MODE`）。

**getAdminUsage / getMeUsage（账本查询）**

- `GET /api/v1/admin/usage?member_id=&node_id=&from=&to=&limit=&cursor=`：`from/to` 为 RFC3339 或 `YYYY-MM-DD`（UTC 零点），`to>from`；响应 `items[]` 为 `UsageRow`（`member_id,node_id,period_start,period_end,up_bytes,down_bytes,estimated,boundary_split,correction,direction,algorithm_version`）+ 顶层 `next_cursor`（**新增**真实游标）。
- `GET /api/v1/me/usage`：**现状是 `items` 为对象而非数组**——`handlers.go` 调用 `writeList(w, map[string]any{"usage": rows, "adjustments": adj}, nil)`，`writeList` 原样 JSON 编码 `items`（`api.go`），实际响应为 `{"items":{"usage":[UsageRow…],"adjustments":[QuotaAdjustment…]},"next_cursor":null}`。本设计**保留该对象形状**，只把 `next_cursor` 变为真实游标；不改为数组、不改为顶层 `adjustments`。
  - 契约口径：`items` 是对象 `{usage,adjustments}`；`usage[]` 字段同 `UsageRow`；`adjustments[]` 为 `QuotaAdjustment`。
  - 已知消费缺陷（不是契约破坏）：`web/src/api/endpoints.ts` 的 `meApi.usage` 现在用 `Array.isArray(payload.items)` 判断，对象形状会落到空列表，成员用量页显示为空。frontend 分端设计必须改为按对象解包；后端不为了让旧解包“看起来能用”而改成数组。
  - 分页：`next_cursor` 按 `usage` 行游标；`adjustments` 只在第一页返回完整集合（它是成员级小集合，不做逐页拆分），后续页 `adjustments=[]`，不重复展示。
- 错误：400 `VALIDATION_FAILED`（`field_errors.from|to|limit|cursor`）；401/403 按作用域。

**getAgentSnapshot（授权快照一致性）**

- `GET /api/v1/agent/snapshot?revision=&control_generation=`：请求与响应字段沿用 `contracts.Snapshot`（`schema_version,control_generation,revision,issued_at,expected_user_count,instances[{id,node_id,config,users[{member_id,email,expires_at}]}]`）。当查询参数的 revision 与 generation 都等于当前权威值 → `204` + 响应头 `X-Current-Revision`、`X-Control-Generation`。
- 一致性强约束（§6.1）：`revision` 必须与 `instances/users` 来自**同一个只读 REPEATABLE READ 快照**；恢复模式下 409 `RECOVERY_MODE`（不推送可能过期的权限）。
- 错误：401 `AGENT_AUTH_FAILED`；409 `RECOVERY_MODE`；500 `INTERNAL`（不得返回部分实例）。

**exitRecoveryMode（恢复门禁）**

- `POST /api/v1/admin/recovery-mode/exit`：请求 `confirm="verified"`、`agent_revisions_checked`(bool)、`ledger_checked`(bool)，三者必须同时成立。响应 `data`：`control_generation,revision_counter,recovery_mode,recovery_entered_at,updated_at`，**新增** `unverified_agents[]`（最近一次应用 revision≠desired 的 Agent id/name）与 `advisories[]`。
- 语义：退出只解除冻结，不撤销 `control_generation` 轮换；Agent 必须本地接受新 generation 后才算恢复（设计不把“退出成功”等同“节点已恢复”）。
- 错误：400 `VALIDATION_FAILED`（核对项缺失）；409 `CONFLICT`（当前不在恢复态，无需退出）。

**getUpgradeReadiness（新增，最小字段）**

- `GET /api/v1/admin/upgrade-readiness`，`admin`。响应 `data`：`status`(`ready|blocked|unknown`)、`checked_at`、`candidate_version`、`schema`(`{applied_migrations:string[],pending_migrations:string[],checksum_ok:boolean}`)、`checks[]`(`{id,severity("blocker"|"warning"),state("pass"|"fail"|"unknown"),summary,affected_count,evidence}`)、`blockers[]`(string[] 稳定码)。
- 稳定检查码（`blocker` 除非注明）：
- `member_cycle_unproven`（成员已绑定但当前区间与账本/绑定历史无法互相证明）
- `ledger_rows_without_period`（账本行没有可用区间；**只报告，不改行**）
- `member_period_overlap`（已登记区间重叠）
- `usage_batch_clock_skew`、`batch_task_orphan`、`agent_state_unreadable`、`migration_checksum_mismatch`、`pending_migration`
- `node_unattributable_pending`（**blocker**：升级前存在无法证明归属的未确认用量/回执队列；不能只告警后放行，否则升级后卡队头并触发停机）
- 预检**不引用** `member_periods`/`batch_idempotency`/`tasks.revision`，因此可在仅 `0001–0004` 的库和空库运行；空库直接 `ready`。
- 语义：只读；不自动暂停、不自动补算、不改授权。`status=blocked` 时部署脚本与 `panel check` 拒绝升级（§9.9），运行中也不因读该端点产生写。
- 错误：401/403；500 `INTERNAL`。

### 4.3 DTO 与契约兼容

- **复用 DTO（字段沿用 `internal/panel/domain` 的 JSON 形状）**：`Member`（含派生字段）、`Server`、`Node`（`server_key` 永不返回，仅 `server_key_set`）、`NodeGroup`、`Subscription`、`Template`、`Invitation`（`code` 仅创建响应出现一次）、`Session`、`Agent`、`CoreInstance`、`Task`、`Event`、`Batch`/`BatchItem`、`Preview`/`PreviewItem`、`UsageRow`、`QuotaAdjustment`、`AuditLog`、`Settings`（`telegram_token` 只写、`telegram_token_set` 只读）、`ControlState`、`access.Decision`、`contracts.Snapshot|ApplyReport|UsageBatch|UsageAck|Heartbeat`。
- **新增 DTO**（均落在 `internal/panel/domain` 或 `internal/contracts`，不另建 DTO 层）：
  - `Member.DataComplete bool` + `Member.DataIssues []string`（JSON `data_complete`,`data_issues`）。
  - `MeStatus.DataComplete/DataIssues`（同一口径）。
  - `FormatStatus{Format,Available,Reason}` → `getMeSubscription.format_status`。
  - `OverviewSetup{Steps []SetupStep, CompletedCount int, BlockingReason string}`，`SetupStep{Key,State,Reason}`（`servers|nodes|node_groups|subscriptions|members|traffic_observed`）。
  - `UpgradeReadiness{Status,CheckedAt,CandidateVersion,Schema,Checks,Blockers}` 及 `ReadinessCheck`。
  - `MeUsageEnvelope{Usage []UsageRow, Adjustments []QuotaAdjustment}` → `getMeUsage.items`（**对象**，沿用现有 `usage`/`adjustments` 键；不新增 `page_meta`，调整集合只在第一页给全）。
  - `BatchIdempotency`（内部，不直接出网）。
- **与 OpenAPI/SDK 的关系**：本项目未引入 OpenAPI 生成器与 SDK；契约由本文件 + `internal/contracts` 结构体 + `docs/design/implementation-contract.md` 共同冻结。前端类型 `web/src/api/types.ts` 是消费侧镜像，不构成第二契约源；任何字段变更先改本文件，再同步前端类型。
- **版本与兼容策略**：
  - 路径版本 `v1` 不升。响应扩展以**附加字段/列表游标**为主；例外是两处 DELETE 语义（订阅退役、模板引用 409），已在下方逐项声明为行为变更并同步文档/前端提示，不把它们伪装成“仅附加字段”。
  - 首个调用者适配点：`web/src/views/admin/SubscriptionsView.vue` 删除按钮需按 `retired` 文案处理；`web/src/views/admin/TemplatesView.vue` 需展示 409 原因。
  - 本轮**无破坏性变更**：`GET /api/v1/me/usage` 保留现状 `items={usage,adjustments}` 对象；只新增真实 `next_cursor` 值。前端现有解包缺陷按 §4.2 修正，不属于后端契约破坏。
  - `GET /api/v1/admin/members/{memberId}` 仍返回 `{member, access}`；列表仍为扁平成员视图（附派生字段），不改变既有解包逻辑。
  - **Agent 协议兼容方向是不对称的，不能凭“字段可选”推断旧服务端会忽略**：面板用 `decodeJSON`（`json.Decoder.DisallowUnknownFields`）解析 Agent 请求，**旧面板遇到新 Agent 的新增顶层字段会直接 400**；Agent 侧 `internal/agent/control` 用 `json.Unmarshal`，可忽略面板新增的响应字段。因此本轮**不新增任何 Agent 请求顶层字段**：Agent 健康/未确认停机信息放进既有可扩展容器——
    - `contracts.Heartbeat.Metrics map[string]float64`：新增键 `health_degraded`、`stop_unconfirmed`、`sampling_errors`（0/1 或计数），旧面板按普通指标透传/忽略，不会 400；
    - `contracts.InstanceResult.State`：沿用既有状态字符串（新增 `stopping` 表示停机未确认），不新增字段；
    - `contracts.Snapshot` / `ApplyReport` / `UsageBatch` 字段集合不变。
    升级顺序因此为“面板先升、Agent 后可升”，旧 Agent 对旧面板、新 Agent 对新面板均不产生未知字段；若未来必须加顶层字段，先做面板侧宽松解码/版本协商，再升 Agent，并在兼容矩阵中登记。
  - 删除类端点**不能统称“归档”**，现状与目标如下：

| 端点 | 现状（3435f1d） | 本轮目标 | 调用者可见变化 | 回归要求 |
|---|---|---|---|---|
| `deleteServer` | 软删：`archived=true`，返回对象 | 不变 | 无 | 列表不再出现；GET 仍可读；有节点 409 |
| `deleteMember` | 软删：`archived=true` + 撤会话 + 清凭据（`PurgeCredentials`） | 不变 | 无 | 列表默认隐藏；`include_archived=true` 可查；账本保留 |
| `deleteNode` | 软删：`archived=true`、`enabled=false`，并删 `node_group_members` | 不变 | 无 | 组内不再出现该节点；授权不再包含 |
| `deleteNodeGroup` | **物理删**；被订阅引用 409；成功时发布 revision | 不变 | 无 | 引用检查、revision 发布、404 |
| `deleteTemplate` | **物理删**；`subscriptions.template_id ON DELETE SET NULL`，删除会静默清掉已绑定成员的模板 | 被**未归档订阅**引用时返回 409 `CONFLICT`（`details.reason=template_in_use`）；否则物理删 | 原先“200 且悄悄清空模板”变为 409；已归档订阅引用不阻止删除 | 引用 409、解绑后可删、无引用正常删 |
| `deleteSubscription` | **物理删**（`DELETE FROM subscriptions`）；先解绑归档成员、检查未归档绑定；`subscription_node_groups` 级联删；`member_bindings.subscription_id` 外键无级联，**存在绑定历史时物理删会被 FK 拒绝（当前落到 400 VALIDATION_FAILED）** | “退役”：有未归档绑定成员 → 409 `NODE_IN_USE`；**存在 `member_bindings` 历史 → 置 `archived=true` 并返回归档快照（响应新增 `retired=true`）**；无任何绑定历史 → 保持物理删 | 有历史的方案不再报 400/500，而是成功归档并保留历史；响应多一个布尔字段 | 有历史→归档且账本/绑定行数不变；无历史→物理删；有绑定 409；已归档再删幂等返回归档态 |
| `deleteMeSession` | 撤销会话（非物理删） | 不变 | 无 | 只能删本人非当前会话 |

- **`version` 不用于 DELETE**：现状 `handleDelete*` **不校验也不要求** `version`（前端把它放 query 传，服务端忽略）。本设计不把 PATCH/PUT 的 `version>0` 规则外推到 DELETE：`DELETE` 请求体/查询参数都不要求 `version`，保持既有调用方可用；DELETE 的并发保护由“删除条件本身”（引用检查、已归档幂等）提供，若要加乐观并发需另立 DEC。
- 上述模板/订阅两处**属于语义变更，不是纯附加字段**：`docs/design/implementation-contract.md`、`docs/reference/api.md` 与前端提示需同批更新；发布说明单列。
- `deleteSubscription` 目标实现的判定顺序（同一事务）：锁订阅行 → 统计**未归档**绑定成员（>0 → 409 `NODE_IN_USE`，行为不变）→ 统计 `member_bindings` 历史（>0 → `archived=true`、返回快照 + `retired=true`，**不删订阅行、不级联删组关联**）→ 无历史 → 保持现状物理删（含既有“解绑归档成员”步骤）。归档态再次调用：未归档绑定仍 409；否则幂等返回归档快照。
- `deleteTemplate` 判定顺序：统计引用该模板且 `NOT archived` 的订阅（>0 → 409 `CONFLICT` + `details.reason=template_in_use` + 引用数）→ 否则物理删（`ON DELETE SET NULL` 只对已归档订阅生效，不再静默清空在用模板）。
- 新增端点不使用 201 以外的歧义码；所有成功 JSON 保持 `data` 信封。

### 4.4 错误码与状态码约定（全量）

- 统一错误体 `{"error":{"code","message","retryable","request_id","details?"}}`；`details.field_errors` 为字段级错误，`details` 中禁止出现密码、私钥、完整 token。
- 现有稳定码（不改名，见 `internal/panel/domain/errors.go`）：`UNAUTHENTICATED, CSRF_FAILED, FORBIDDEN, NOT_FOUND, VALIDATION_FAILED, CONFLICT, LAST_ADMIN, PREVIEW_REQUIRED, PREVIEW_STALE, PREVIEW_CONCLUSION_CHANGED, IDEMPOTENCY_KEY_REUSED, SUBSCRIPTION_NOT_BINDABLE, PROTOCOL_NOT_SUPPORTED, NODE_IN_USE, DB_UNAVAILABLE, RATE_LIMITED, AGENT_AUTH_FAILED, REVISION_STALE, SNAPSHOT_SCHEMA_UNSUPPORTED, BATCH_PAYLOAD_MISMATCH, BATCH_OWNERSHIP_MISMATCH, BATCH_CLOCK_SKEW, RECOVERY_MODE, INTERNAL`。
- **新增稳定码（3 个，最小集）**：`LEDGER_UNATTRIBUTABLE`（422，用量样本无法归属到已记录区间，拒绝且不 ACK，不写账本）、`READINESS_BLOCKED`（409，`getUpgradeReadiness` 在 `status=blocked` 时用于**不安全升级/业务启动**的显式阻止；不阻止进入恢复核对）、`CURSOR_INVALID`（400，游标解析/作用域失效；仍附 `details.field_errors.cursor`）。
- 状态码约定：写成功 200/201；创建码/邀请返回 201；无内容仅 Agent 快照 204；恢复冻结 409 `RECOVERY_MODE`；未认证 401、越权 403、不存在 404、版本冲突/预览过期/幂等复用/引用阻止删除 409、校验 400、协议不可表达 422、依赖不可用 503。
- Agent 归属错误固定 403（凭据有效但对象不属本机），不与 401 混用；注册码无效/已用/过期固定 401。

### 4.5 前端消费边界（契约摘要口径）

- 前端的 `web/src/api/endpoints.ts` 是消费侧封装，不构成第二契约源；任何字段/路径变化先改本章，再同步前端类型与封装。
- `contract_summary` 逐项对应本章 §4.1 的 operation ID（77 项，唯一）；`request_fields`/`response_fields` 使用本章口径：列表为 `items`/`next_cursor` 信封字段，单对象/动作为 `data` 内业务字段；路径参数不列入字段数组。
- 前端会消费的关键组：auth（login/session/logout/redeem）、me（status/subscription/usage/nodes/sessions/password/令牌与凭据轮换）、admin 资源（servers/nodes/node-groups/subscriptions/members/invitations/templates）、运行（overview/agents/core-instances/events/tasks/audit/settings/recovery-mode/upgrade-readiness）、批量（preview/submit/list/get）、用量（admin/usage）。
- 组件可原样复用后端 DTO；本设计不要求前端另造模型，也不允许前端自行推导授权（`used_bytes`/`proxy_access`/`allowed_node_ids` 必须来自服务端）。
- 两个探针 `getHealth`/`getReady` 的调用方是部署/探针（`internal`），不由前端消费；`getSubscriptionOutput` 返回客户端配置文本（非 JSON 信封），其 `response_fields` 为空数组是契约口径，不代表无输出。
- 前端只消费本表列出的 operation；若需要新能力，先回到本文件增补契约，再由 frontend 设计声明依赖，禁止直接改前端路径/字段。

### 4.6 完整操作字段索引（规范性契约）

本节逐操作固定请求/响应字段集合与拒绝分支；与§4.1操作清单、§4.2关键语义和§4.3复用DTO共同构成本文件内的唯一设计源。类型和业务语义沿用前文DTO，下面不是第二套独立schema。`backend-contract.json`只能从本节导出，不得手工补字段。前端的`*_fields_used`是消费子集，合法省略未使用字段不是契约不一致；它不能反过来删改服务端字段。

<!-- canonical-operation-index:start -->
```json
[
{"operation_id":"getHealth","method":"GET","path":"/healthz","request_fields":[],"response_fields":["status"],"error_codes":[],"callers":["internal"]},
{"operation_id":"getReady","method":"GET","path":"/readyz","request_fields":[],"response_fields":["status","postgres","redis","version","commit"],"error_codes":[503],"callers":["internal"]},
{"operation_id":"login","method":"POST","path":"/api/v1/auth/login","request_fields":["username","password"],"response_fields":["user","csrf_token"],"error_codes":[400,401,403,429],"callers":["frontend"]},
{"operation_id":"getSession","method":"GET","path":"/api/v1/auth/session","request_fields":[],"response_fields":["user","csrf_token"],"error_codes":[401],"callers":["frontend"]},
{"operation_id":"logout","method":"POST","path":"/api/v1/auth/logout","request_fields":[],"response_fields":["revoked"],"error_codes":[401,403],"callers":["frontend"]},
{"operation_id":"redeemInvitation","method":"POST","path":"/api/v1/auth/redeem","request_fields":["code","username","password"],"response_fields":["id","username","role","active","proxy_enabled","subscription_id","expires_at","period_start","period_end","period_timezone","period_anchor_day","bound_at"],"error_codes":[400,409,429],"callers":["frontend"]},
{"operation_id":"getSubscriptionOutput","method":"GET","path":"/sub/{token}","request_fields":["format","base64"],"response_fields":[],"error_codes":[400,403,422],"callers":["frontend"]},
{"operation_id":"getMeStatus","method":"GET","path":"/api/v1/me/status","request_fields":[],"response_fields":["member","access","nodes_allowed","data_complete","data_issues"],"error_codes":[401],"callers":["frontend"]},
{"operation_id":"listMeNodes","method":"GET","path":"/api/v1/me/nodes","request_fields":["limit","cursor"],"response_fields":["items","next_cursor"],"error_codes":[401],"callers":["frontend"]},
{"operation_id":"getMeUsage","method":"GET","path":"/api/v1/me/usage","request_fields":["limit","cursor"],"response_fields":["items","next_cursor"],"error_codes":[400,401],"callers":["frontend"]},
{"operation_id":"getMeSubscription","method":"GET","path":"/api/v1/me/subscription","request_fields":[],"response_fields":["url","token","formats","format_status","expires_at","period_end","rotation_impact"],"error_codes":[401,409],"callers":["frontend"]},
{"operation_id":"rotateMeSubscriptionToken","method":"POST","path":"/api/v1/me/subscription/token/rotations","request_fields":[],"response_fields":["token","url"],"error_codes":[401,403,409],"callers":["frontend"]},
{"operation_id":"rotateMeCredentials","method":"POST","path":"/api/v1/me/credentials/rotations","request_fields":["scope","node_id"],"response_fields":["rotated","scope"],"error_codes":[400,401,403,409],"callers":["frontend"]},
{"operation_id":"changeMePassword","method":"POST","path":"/api/v1/me/password","request_fields":["current_password","new_password"],"response_fields":["updated","sessions_revoked"],"error_codes":[400,401],"callers":["frontend"]},
{"operation_id":"listMeSessions","method":"GET","path":"/api/v1/me/sessions","request_fields":["limit","cursor"],"response_fields":["items","next_cursor"],"error_codes":[401],"callers":["frontend"]},
{"operation_id":"deleteMeSession","method":"DELETE","path":"/api/v1/me/sessions/{sessionId}","request_fields":[],"response_fields":["revoked"],"error_codes":[400,401,404],"callers":["frontend"]},
{"operation_id":"registerAgent","method":"POST","path":"/api/v1/agent/register","request_fields":["code","name","version"],"response_fields":["agent_id","token","control_generation"],"error_codes":[400,401,409],"callers":["agent"]},
{"operation_id":"getAgentSnapshot","method":"GET","path":"/api/v1/agent/snapshot","request_fields":["revision","control_generation"],"response_fields":["schema_version","control_generation","revision","issued_at","expected_user_count","instances"],"error_codes":[401,409,500],"callers":["agent"]},
{"operation_id":"reportAgentApplied","method":"POST","path":"/api/v1/agent/applied","request_fields":["control_generation","revision","state","applied_at","results"],"response_fields":["accepted"],"error_codes":[400,401,403,409],"callers":["agent"]},
{"operation_id":"submitAgentUsage","method":"POST","path":"/api/v1/agent/usage","request_fields":["schema_version","batch_id","samples"],"response_fields":["batch_id","status"],"error_codes":[400,401,403,409,422],"callers":["agent"]},
{"operation_id":"agentHeartbeat","method":"POST","path":"/api/v1/agent/heartbeat","request_fields":["version","xray_version","revision","sent_at","uptime_seconds","queue_depth","metrics","instances"],"response_fields":["accepted"],"error_codes":[400,401,403],"callers":["agent"]},
{"operation_id":"getRecoveryMode","method":"GET","path":"/api/v1/admin/recovery-mode","request_fields":[],"response_fields":["control_generation","revision_counter","recovery_mode","recovery_entered_at","updated_at"],"error_codes":[401,403],"callers":["frontend"]},
{"operation_id":"enterRecoveryMode","method":"POST","path":"/api/v1/admin/recovery-mode/enter","request_fields":[],"response_fields":["control_generation","revision_counter","recovery_mode","recovery_entered_at","updated_at"],"error_codes":[401,403,409],"callers":["frontend"]},
{"operation_id":"exitRecoveryMode","method":"POST","path":"/api/v1/admin/recovery-mode/exit","request_fields":["confirm","agent_revisions_checked","ledger_checked"],"response_fields":["control_generation","revision_counter","recovery_mode","recovery_entered_at","updated_at","unverified_agents","advisories"],"error_codes":[400,401,403,409],"callers":["frontend"]},
{"operation_id":"getAdminOverview","method":"GET","path":"/api/v1/admin/overview","request_fields":[],"response_fields":["counts","usage_bytes","agents_online","pending_tasks","recent_events","setup"],"error_codes":[401,403],"callers":["frontend"]},
{"operation_id":"getUpgradeReadiness","method":"GET","path":"/api/v1/admin/upgrade-readiness","request_fields":[],"response_fields":["status","checked_at","candidate_version","schema","checks","blockers"],"error_codes":[401,403,500],"callers":["frontend"]},
{"operation_id":"listAgents","method":"GET","path":"/api/v1/admin/agents","request_fields":["limit","cursor"],"response_fields":["items","next_cursor"],"error_codes":[400,401,403],"callers":["frontend"]},
{"operation_id":"listCoreInstances","method":"GET","path":"/api/v1/admin/core-instances","request_fields":["limit","cursor"],"response_fields":["items","next_cursor"],"error_codes":[400,401,403],"callers":["frontend"]},
{"operation_id":"listServers","method":"GET","path":"/api/v1/admin/servers","request_fields":["limit","cursor"],"response_fields":["items","next_cursor"],"error_codes":[400,401,403],"callers":["frontend"]},
{"operation_id":"createServer","method":"POST","path":"/api/v1/admin/servers","request_fields":["name","address","region","notes","expires_at","cost_minor","currency"],"response_fields":["id","name","address","region","notes","expires_at","cost_minor","currency","archived","version","created_at","updated_at"],"error_codes":[400,401,403,409],"callers":["frontend"]},
{"operation_id":"getServer","method":"GET","path":"/api/v1/admin/servers/{serverId}","request_fields":[],"response_fields":["id","name","address","region","notes","expires_at","cost_minor","currency","archived","version","created_at","updated_at"],"error_codes":[401,403,404],"callers":["frontend"]},
{"operation_id":"updateServer","method":"PATCH","path":"/api/v1/admin/servers/{serverId}","request_fields":["version","name","address","region","notes","expires_at","cost_minor","currency"],"response_fields":["id","name","address","region","notes","expires_at","cost_minor","currency","archived","version","created_at","updated_at"],"error_codes":[400,401,403,404,409],"callers":["frontend"]},
{"operation_id":"deleteServer","method":"DELETE","path":"/api/v1/admin/servers/{serverId}","request_fields":[],"response_fields":["id","name","address","region","notes","expires_at","cost_minor","currency","archived","version","created_at","updated_at"],"error_codes":[401,403,404,409],"callers":["frontend"]},
{"operation_id":"createRegistrationCode","method":"POST","path":"/api/v1/admin/servers/{serverId}/registration-codes","request_fields":[],"response_fields":["id","code","expires_at"],"error_codes":[400,401,403,404,409],"callers":["frontend"]},
{"operation_id":"listNodes","method":"GET","path":"/api/v1/admin/nodes","request_fields":["server_id","q","limit","cursor"],"response_fields":["items","next_cursor"],"error_codes":[400,401,403],"callers":["frontend"]},
{"operation_id":"createNode","method":"POST","path":"/api/v1/admin/nodes","request_fields":["name","server_id","enabled","host","port","external_port","listen","protocol","flow","cipher","server_key","stream_settings","protocol_settings","client_settings","sort_order"],"response_fields":["id","name","server_id","enabled","host","port","external_port","listen","protocol","flow","cipher","stream_settings","protocol_settings","client_settings","sort_order","archived","version","server_key_set","created_at","updated_at"],"error_codes":[400,401,403,404,409,422],"callers":["frontend"]},
{"operation_id":"getNode","method":"GET","path":"/api/v1/admin/nodes/{nodeId}","request_fields":[],"response_fields":["id","name","server_id","enabled","host","port","external_port","listen","protocol","flow","cipher","stream_settings","protocol_settings","client_settings","sort_order","archived","version","server_key_set","created_at","updated_at"],"error_codes":[401,403,404],"callers":["frontend"]},
{"operation_id":"updateNode","method":"PATCH","path":"/api/v1/admin/nodes/{nodeId}","request_fields":["version","name","server_id","enabled","host","port","external_port","listen","protocol","flow","cipher","server_key","stream_settings","protocol_settings","client_settings","sort_order"],"response_fields":["id","name","server_id","enabled","host","port","external_port","listen","protocol","flow","cipher","stream_settings","protocol_settings","client_settings","sort_order","archived","version","server_key_set","created_at","updated_at"],"error_codes":[400,401,403,404,409,422],"callers":["frontend"]},
{"operation_id":"deleteNode","method":"DELETE","path":"/api/v1/admin/nodes/{nodeId}","request_fields":[],"response_fields":["id","name","server_id","enabled","host","port","external_port","listen","protocol","flow","cipher","stream_settings","protocol_settings","client_settings","sort_order","archived","version","server_key_set","created_at","updated_at"],"error_codes":[401,403,404,409],"callers":["frontend"]},
{"operation_id":"listNodeGroups","method":"GET","path":"/api/v1/admin/node-groups","request_fields":["limit","cursor"],"response_fields":["items","next_cursor"],"error_codes":[400,401,403],"callers":["frontend"]},
{"operation_id":"createNodeGroup","method":"POST","path":"/api/v1/admin/node-groups","request_fields":["name","description","node_ids"],"response_fields":["id","name","description","node_ids","version","created_at","updated_at"],"error_codes":[400,401,403,409],"callers":["frontend"]},
{"operation_id":"getNodeGroup","method":"GET","path":"/api/v1/admin/node-groups/{groupId}","request_fields":[],"response_fields":["id","name","description","node_ids","version","created_at","updated_at"],"error_codes":[401,403,404],"callers":["frontend"]},
{"operation_id":"updateNodeGroup","method":"PATCH","path":"/api/v1/admin/node-groups/{groupId}","request_fields":["version","name","description","node_ids"],"response_fields":["id","name","description","node_ids","version","created_at","updated_at"],"error_codes":[400,401,403,404,409],"callers":["frontend"]},
{"operation_id":"deleteNodeGroup","method":"DELETE","path":"/api/v1/admin/node-groups/{groupId}","request_fields":[],"response_fields":["deleted","id"],"error_codes":[401,403,404,409],"callers":["frontend"]},
{"operation_id":"listSubscriptions","method":"GET","path":"/api/v1/admin/subscriptions","request_fields":["limit","cursor"],"response_fields":["items","next_cursor"],"error_codes":[400,401,403],"callers":["frontend"]},
{"operation_id":"createSubscription","method":"POST","path":"/api/v1/admin/subscriptions","request_fields":["name","description","node_group_ids","quota_bytes","timezone","anchor_day","default_valid_days","allow_new_bindings","access_enabled","template_id","archived"],"response_fields":["id","name","description","node_group_ids","quota_bytes","timezone","anchor_day","default_valid_days","allow_new_bindings","access_enabled","archived","template_id","pending_period_rule","bound_members","version","created_at","updated_at"],"error_codes":[400,401,403,409],"callers":["frontend"]},
{"operation_id":"getSubscription","method":"GET","path":"/api/v1/admin/subscriptions/{subscriptionId}","request_fields":[],"response_fields":["id","name","description","node_group_ids","quota_bytes","timezone","anchor_day","default_valid_days","allow_new_bindings","access_enabled","archived","template_id","pending_period_rule","bound_members","version","created_at","updated_at"],"error_codes":[401,403,404],"callers":["frontend"]},
{"operation_id":"updateSubscription","method":"PATCH","path":"/api/v1/admin/subscriptions/{subscriptionId}","request_fields":["version","name","description","node_group_ids","quota_bytes","timezone","anchor_day","default_valid_days","allow_new_bindings","access_enabled","template_id","archived"],"response_fields":["id","name","description","node_group_ids","quota_bytes","timezone","anchor_day","default_valid_days","allow_new_bindings","access_enabled","archived","template_id","pending_period_rule","bound_members","version","created_at","updated_at"],"error_codes":[400,401,403,404,409],"callers":["frontend"]},
{"operation_id":"deleteSubscription","method":"DELETE","path":"/api/v1/admin/subscriptions/{subscriptionId}","request_fields":[],"response_fields":["id","name","description","node_group_ids","quota_bytes","timezone","anchor_day","default_valid_days","allow_new_bindings","access_enabled","archived","template_id","pending_period_rule","bound_members","version","created_at","updated_at","retired"],"error_codes":[401,403,404,409],"callers":["frontend"]},
{"operation_id":"listMembers","method":"GET","path":"/api/v1/admin/members","request_fields":["subscription_id","include_archived","q","limit","cursor"],"response_fields":["items","next_cursor"],"error_codes":[400,401,403],"callers":["frontend"]},
{"operation_id":"createMember","method":"POST","path":"/api/v1/admin/members","request_fields":["username","password","role","active","proxy_enabled","subscription_id","expires_at","quota_override_bytes","quota_override_enabled"],"response_fields":["id","username","role","active","proxy_enabled","archived","subscription_id","expires_at","quota_override_bytes","quota_override_enabled","over_quota","bound_at","period_start","period_end","period_timezone","period_anchor_day","pending_period_rule","last_login_at","used_bytes","effective_quota_bytes","access_reasons","proxy_access","allowed_node_ids","data_complete","data_issues","version","created_at","updated_at"],"error_codes":[400,401,403,404,409],"callers":["frontend"]},
{"operation_id":"getMember","method":"GET","path":"/api/v1/admin/members/{memberId}","request_fields":[],"response_fields":["member","access"],"error_codes":[401,403,404],"callers":["frontend"]},
{"operation_id":"updateMember","method":"PATCH","path":"/api/v1/admin/members/{memberId}","request_fields":["version","username","password","role","active","proxy_enabled","subscription_id","expires_at","quota_override_bytes","quota_override_enabled"],"response_fields":["id","username","role","active","proxy_enabled","archived","subscription_id","expires_at","quota_override_bytes","quota_override_enabled","over_quota","bound_at","period_start","period_end","period_timezone","period_anchor_day","pending_period_rule","last_login_at","used_bytes","effective_quota_bytes","access_reasons","proxy_access","allowed_node_ids","data_complete","data_issues","version","created_at","updated_at"],"error_codes":[400,401,403,404,409],"callers":["frontend"]},
{"operation_id":"deleteMember","method":"DELETE","path":"/api/v1/admin/members/{memberId}","request_fields":[],"response_fields":["id","username","role","active","proxy_enabled","archived","subscription_id","expires_at","quota_override_bytes","quota_override_enabled","over_quota","bound_at","period_start","period_end","period_timezone","period_anchor_day","pending_period_rule","last_login_at","used_bytes","effective_quota_bytes","access_reasons","proxy_access","allowed_node_ids","data_complete","data_issues","version","created_at","updated_at"],"error_codes":[401,403,404,409],"callers":["frontend"]},
{"operation_id":"rotateMemberSubscriptionToken","method":"POST","path":"/api/v1/admin/members/{memberId}/subscription-token/rotations","request_fields":[],"response_fields":["token","url"],"error_codes":[401,403,404,409],"callers":["frontend"]},
{"operation_id":"rotateMemberCredentials","method":"POST","path":"/api/v1/admin/members/{memberId}/credentials/rotations","request_fields":["scope","node_id"],"response_fields":["rotated","scope"],"error_codes":[400,401,403,404,409],"callers":["frontend"]},
{"operation_id":"createMemberQuotaAdjustment","method":"POST","path":"/api/v1/admin/members/{memberId}/quota-adjustments","request_fields":["bytes","reason"],"response_fields":["id","member_id","bytes","reason","period_start","created_by","created_at"],"error_codes":[400,401,403,404,409],"callers":["frontend"]},
{"operation_id":"listTemplates","method":"GET","path":"/api/v1/admin/templates","request_fields":["limit","cursor"],"response_fields":["items","next_cursor"],"error_codes":[400,401,403],"callers":["frontend"]},
{"operation_id":"createTemplate","method":"POST","path":"/api/v1/admin/templates","request_fields":["name","format","content","description"],"response_fields":["id","name","format","content","description","version","created_at","updated_at"],"error_codes":[400,401,403,409],"callers":["frontend"]},
{"operation_id":"getTemplate","method":"GET","path":"/api/v1/admin/templates/{templateId}","request_fields":[],"response_fields":["id","name","format","content","description","version","created_at","updated_at"],"error_codes":[401,403,404],"callers":["frontend"]},
{"operation_id":"updateTemplate","method":"PATCH","path":"/api/v1/admin/templates/{templateId}","request_fields":["version","name","format","content","description"],"response_fields":["id","name","format","content","description","version","created_at","updated_at"],"error_codes":[400,401,403,404,409],"callers":["frontend"]},
{"operation_id":"deleteTemplate","method":"DELETE","path":"/api/v1/admin/templates/{templateId}","request_fields":[],"response_fields":["deleted","id"],"error_codes":[401,403,404,409],"callers":["frontend"]},
{"operation_id":"listInvitations","method":"GET","path":"/api/v1/admin/invitations","request_fields":["limit","cursor"],"response_fields":["items","next_cursor"],"error_codes":[400,401,403],"callers":["frontend"]},
{"operation_id":"createInvitation","method":"POST","path":"/api/v1/admin/invitations","request_fields":["subscription_id","expires_at","max_uses"],"response_fields":["id","code","expires_at"],"error_codes":[400,401,403,404,409],"callers":["frontend"]},
{"operation_id":"revokeInvitation","method":"POST","path":"/api/v1/admin/invitations/{invitationId}/revoke","request_fields":[],"response_fields":["revoked","id"],"error_codes":[401,403,404,409],"callers":["frontend"]},
{"operation_id":"getAdminUsage","method":"GET","path":"/api/v1/admin/usage","request_fields":["member_id","node_id","from","to","limit","cursor"],"response_fields":["items","next_cursor"],"error_codes":[400,401,403],"callers":["frontend"]},
{"operation_id":"previewBatch","method":"POST","path":"/api/v1/admin/batches/preview","request_fields":["operation","member_ids","values","reason"],"response_fields":["id","operation","version","rules_version","member_set_digest","member_count","affected_count","affected_nodes","affected_agents","items","exceptions","warnings","conclusion","expires_at","created_at"],"error_codes":[400,401,403,409],"callers":["frontend"]},
{"operation_id":"submitBatch","method":"POST","path":"/api/v1/admin/batches","request_fields":["preview_id","rules_version","member_set_digest","confirm"],"response_fields":["id","preview_id","operation","state","results","summary","member_count","idempotent_replay","created_at"],"error_codes":[400,401,403,409],"callers":["frontend"]},
{"operation_id":"listBatches","method":"GET","path":"/api/v1/admin/batches","request_fields":["limit","cursor"],"response_fields":["items","next_cursor"],"error_codes":[400,401,403],"callers":["frontend"]},
{"operation_id":"getBatch","method":"GET","path":"/api/v1/admin/batches/{batchId}","request_fields":[],"response_fields":["batch","results","summary","id","state","operation"],"error_codes":[401,403,404],"callers":["frontend"]},
{"operation_id":"listEvents","method":"GET","path":"/api/v1/admin/events","request_fields":["state","q","limit","cursor"],"response_fields":["items","next_cursor"],"error_codes":[400,401,403],"callers":["frontend"]},
{"operation_id":"ackEvent","method":"POST","path":"/api/v1/admin/events/{eventId}/ack","request_fields":[],"response_fields":["acked","id"],"error_codes":[401,403,404,409],"callers":["frontend"]},
{"operation_id":"listTasks","method":"GET","path":"/api/v1/admin/tasks","request_fields":["state","batch_id","agent_id","limit","cursor"],"response_fields":["items","next_cursor"],"error_codes":[400,401,403],"callers":["frontend"]},
{"operation_id":"retryTask","method":"POST","path":"/api/v1/admin/tasks/{taskId}/retry","request_fields":[],"response_fields":["id","kind","agent_id","node_id","member_id","batch_id","state","attempts","max_attempts","last_error","available_at","locked_at","created_at","updated_at"],"error_codes":[401,403,404,409],"callers":["frontend"]},
{"operation_id":"listAuditLogs","method":"GET","path":"/api/v1/admin/audit-logs","request_fields":["action","actor","limit","cursor"],"response_fields":["items","next_cursor"],"error_codes":[400,401,403],"callers":["frontend"]},
{"operation_id":"getSettings","method":"GET","path":"/api/v1/admin/settings","request_fields":[],"response_fields":["timezone","retention_days","telegram_enabled","telegram_chat_id","telegram_token_set","template_defaults","version","updated_at"],"error_codes":[401,403],"callers":["frontend"]},
{"operation_id":"updateSettings","method":"PUT","path":"/api/v1/admin/settings","request_fields":["version","timezone","retention_days","telegram_enabled","telegram_chat_id","telegram_token","template_defaults"],"response_fields":["timezone","retention_days","telegram_enabled","telegram_chat_id","telegram_token_set","template_defaults","version","updated_at"],"error_codes":[400,401,403,409],"callers":["frontend"]}
]
```
<!-- canonical-operation-index:end -->

## 5. 数据库设计

### 5.1 设计原则

- 只加当前必需的表与列；不改 `0001`–`0004` 的字节（迁移器校验 sha256，改动会使已升级库启动失败）。
- 历史事实只追加、不改写；不复用触发器实现业务不变量（候选 0005/0007 的双时钟缺陷已证明不可靠），改由**应用事务 + 表级 CHECK/唯一约束**保证。
- 账本、绑定、凭据、成员行一律不删除；归档与 tombstone 区分。

### 5.2 表结构或字段变更

**表: `member_periods`（CREATE）**

| 字段 | 类型 | 约束 | 说明 |
|---|---|---|---|
| `member_id` | `text` | `NOT NULL REFERENCES members(id)` | 成员 |
| `period_start` | `timestamptz` | `NOT NULL`，主键组成 | 区间起点（含） |
| `period_end` | `timestamptz` | `NOT NULL`，`CHECK (period_end > period_start)` | 区间终点（不含） |
| `anchor_day` | `integer` | `NOT NULL CHECK (1..31)` | 当时生效锚日 |
| `timezone` | `text` | `NOT NULL` | 当时生效时区 |
| `source` | `text` | `NOT NULL CHECK IN ('binding','roll_forward','proven_current','proven_ledger')` | 区间来源：`binding`/`roll_forward` 为运行期写入；`proven_current`/`proven_ledger` 为升级时**可证明**的区间登记（见 §6.4.1），不从历史反推 |
| `created_at` | `timestamptz` | `NOT NULL DEFAULT now()` | 记录时间 |

- 主键 `(member_id, period_start)`；唯一约束同主键；索引 `member_periods_member_end_idx (member_id, period_end)`；外键 `member_id`。
- 非重叠由应用事务保证：写前 `SELECT … FOR UPDATE` 同成员相邻区间校验（重叠即 409/422 并回滚），并在同一事务内插入。**不采用**触发器方案（见 §5.4）。
- 时间字段：仅 `created_at`，不设 `updated_at`（区间不可改，改期=新区间）。

**表: `node_server_history`（CREATE，迟报归属与移机交接必需）**

| 字段 | 类型 | 约束 | 说明 |
|---|---|---|---|
| `id` | `text` | `PRIMARY KEY`，沿用项目不透明随机 ID 策略 | 一条独立授权窗口的稳定标识，不用可能相同的时钟值作身份 |
| `node_id` | `text` | `NOT NULL REFERENCES nodes(id)` | 节点 |
| `server_id` | `text` | `NOT NULL REFERENCES servers(id)` | 该区间的**实际执行授权**所在服务器 |
| `agent_id` | `text` | `NOT NULL REFERENCES agents(id)` | 获得执行授权的具体 Agent；同一服务器多条合法 Agent 身份分别记录，不用空身份猜测归属 |
| `valid_from` | `timestamptz` | `NOT NULL` | 授权开始（含） |
| `valid_to` | `timestamptz` | `NULL`；`CHECK (valid_to IS NULL OR valid_to >= valid_from)` | 授权结束（不含）；`NULL`=仍持有授权 |
| `released_revision` | `bigint` | `NULL`（`NOT NULL` 当 `valid_to IS NOT NULL`） | 关闭该窗口的**释放 revision**（移机发布的那次） |
| `release_confirmed_at` | `timestamptz` | `NULL`（`NOT NULL` 当 `valid_to IS NOT NULL`） | 旧 Agent 确认 `old_connections_terminated` 的时刻；关闭时间取它，不取点击移机的时间 |

- 主键 `id`；索引 `(node_id, valid_to)`、`(agent_id, valid_to)`、`(server_id, valid_from)`。同一 `(node_id, agent_id, released_revision)` 的非空释放修订唯一；零长度窗口仅作无流量的历史事实，不能覆盖正时长样本。人工导入材料的内容指纹写现有审计记录并关联本窗口 ID，不另建一套证据库。
- **允许交接期及合法同服务器身份分别持有窗口**：不对整个 node 建单一未关闭窗口索引；当前未释放窗口的唯一键为 `(node_id, agent_id) WHERE valid_to IS NULL AND released_revision IS NULL`。逻辑当前服务器的唯一性由 `nodes.server_id` 表达，不能错误限制为全节点只有一个历史 Agent。未接入 Agent 的节点不伪造实际授权行；注册/授权下发时登记真实身份。
- 逻辑期望归属仍是 `nodes.server_id`（UI/授权目标）；历史表表达“实际执行授权”，两者在交接期可以不同。
- 回填：升级时只登记“从升级时刻起”的当前归属（`INSERT … SELECT id, server_id, agent_id, now() FROM nodes LEFT JOIN agents …`），不反推历史；升级前的移机历史未知 → 由预检阻断（见 §6.4.3），不靠猜测。
- 写入：节点创建/移机由应用事务写入，与 `nodes.server_id`、revision 同事务；**旧窗口不在移机提交时关闭**；节点归档不删行。

**表: `batch_idempotency`（CREATE，保留期外仍生效）**

| 字段 | 类型 | 约束 | 说明 |
|---|---|---|---|
| `key` | `text` | `PK` | `Idempotency-Key` |
| `fingerprint` | `text` | `NOT NULL` | `sha256(preview_id|confirm)` |
| `batch_id` | `text` | `NOT NULL` | 关联批次（不设外键：批次可被保留期清理） |
| `result` | `jsonb` | `NULL` | 提交时持久化的回放结果（批次摘要 + 逐对象结果） |
| `created_at` | `timestamptz` | `NOT NULL DEFAULT now()` | 创建时间 |

- 索引 `batch_idempotency_batch_idx (batch_id)`；回填：`INSERT … SELECT idempotency_key, request_fingerprint, id, created_at FROM batches ON CONFLICT DO NOTHING`。

**表: `tasks`（MODIFY 加列）**

| 字段 | 类型 | 约束 | 说明 |
|---|---|---|---|
| `revision` | `bigint` | `NOT NULL DEFAULT 0` | 任务绑定的发布修订；回执/重试按 revision 匹配 |

- 回填：`UPDATE tasks t SET revision = x.max_revision FROM (SELECT batch_id,agent_id,max(revision) FROM batch_items GROUP BY 1,2) x WHERE …`；新增唯一索引 `tasks_batch_agent_revision_key (batch_id, agent_id, revision) WHERE batch_id <> ''`，索引 `tasks_agent_revision_idx (agent_id, revision, state)`。
- 孤儿修复：`batch_items.task_id` 指向不存在任务时，仅在“该 (batch,agent) 恰好只有一个任务”的确定条件下回填；否则置空并记事件（不删除历史）。

**表: `usage_batches` / `usage_entries`（MODIFY 索引；不改既有行语义）**

- `usage_entries` 只新增索引 `(member_id, period_start, node_id)`（现状已有 member/period 与 node/period，补组合以减少分页查询回表）。**不加 `period_recorded` 列，不把任何既有行标记为“不参与额度”**：账本原值、`period_start/period_end`、当前周期 `used_bytes` 在升级前后保持完全一致。
- 新写入的可归属性由应用校验保证（写入前切片必须命中 `member_periods` 或当前运行区间，未命中整批 422 `LEDGER_UNATTRIBUTABLE` 且不 ACK），这是**写入约束**，不是对历史行的重分类；历史行没有“可信/不可信”二值标记，避免用标记把历史移出额度。
- `usage_batches` 已有 `clock_skew`；新增索引 `(agent_id, received_at DESC)` 供积压与迟到排查。

**表: `subscriptions` / `members`（不改结构，仅语义）**

- `members.pending_period_rule` 保留（下一周期规则）；`subscriptions.pending_period_rule` 保留。**不**加 `deleted_at`：成员/服务器/节点继续用 `archived`；订阅仅在“有绑定历史”时归档（否则物理删，见 §4.3），模板仍物理删；不以新增软删列替代既有语义。
- 不新增 `member_billing_reviews` 表：候选 0009 的“自动暂停 + 自动补算”未经审查，本设计要求升级前由 `getUpgradeReadiness` 阻断、由管理员通过既有 `PATCH /members/{memberId}` 重新绑定（该操作会在同一事务记录可证明区间）解决；运行期不做静默修复。

### 5.3 迁移策略

| 文件（CREATE） | 内容 | 回填 | 兼容/部署影响 |
|---|---|---|---|
| `migrations/0005_period_and_owner_history.sql` | `member_periods` + `node_server_history` + 索引 | `member_periods` 只登记**可证明的当前区间**（`members.period_start/end` 合法且与账本一致），`source='proven_current'`；`node_server_history` 只登记升级时刻起的当前归属；均不反推、不改账本 | 纯新增表；旧二进制忽略新表；不改变任何既有行的额度/授权语义 |
| `migrations/0006_batch_task_durability.sql` | `batch_idempotency` + `tasks.revision` + 回填 + 唯一/普通索引 + 孤儿修复 | 幂等 key 从 `batches` 回填；task revision 从 `batch_items` 最大值回填 | 新增列有默认值，旧行安全；唯一索引可能暴露既有数据冲突 → 迁移在事务内失败并阻断启动（不静默删行），由 `panel check` 报告并人工核对 |
| `migrations/0007_usage_queried_indexes.sql` | `usage_entries(member_id, period_start, node_id)` + `usage_batches(agent_id, received_at DESC)` 索引 | 无数据回填；**不改任何既有行** | 纯索引迁移，历史账本与额度语义不变；新写入归属由应用校验（§6.4），不依赖列标记 |

- 顺序与锁：沿用 `store.Migrate` 的 advisory lock 与校验和；新增迁移只允许追加。**升级顺序固定为「只读预检 → 迁移 → 业务启动」**：
  1. `panel check --preflight`（在迁移前运行，只读）：只依赖 `0001–0004` 已有表与 `schema_migrations`，不查询 `member_periods`（该表此时可能不存在）。空库直接 `ready`（无历史可核对）；仅 `0001–0004` 的旧库按现有 `members`、`usage_entries`、`credentials`、`batches`/`tasks` 判断。
  2. 预检 `blocked`：**不启动面板、不执行迁移**，打印稳定码、受影响计数与核对步骤；管理员用既有成员编辑重建可证明区间，或恢复备份后重跑预检。预检本身不写库。
  3. 只有预检 `ready` 才启动面板执行迁移；`blocked` 与 `unknown` 都**不**执行迁移（`unknown` 表示查询失败/无法证明，不能当作通过）。沿用迁移器真实的逐文件事务：失败文件的变更与登记整体回滚，之前成功文件的登记保留，不能声称整个升级回到零进度。面板以非零退出拒绝以未完成的迁移集合提供业务，并展示已应用前缀与失败文件。
  4. 迁移后启动时再跑同一实现的 `panel check`（此时可用新表），必须仍为 `ready` 才对外服务；`warning` 只展示，`blocked/unknown` 停止对外服务并保留只读诊断入口。
- **兼容旧二进制的技术回退 ≠ 恢复数据库**：迁移只追加列/表/索引时，旧二进制可继续读取既有列并正常工作；但一旦回退到旧二进制后有人写入（尤其授权/账本），就不能再用“回退代码”掩盖数据变化。数据库恢复到升级前备份会**带回旧授权与旧账本**，属于高风险动作，必须走 `enterRecoveryMode` 核对并轮换 generation，不能在常规升级里用“恢复备份”当默认回退。
- 常规升级失败（迁移错误、启动失败）优先：保留失败现场 → 修复后重跑；确需技术回退时只回退二进制且**禁止旧二进制写入**（停写/只读），并核对实际已应用迁移前缀及旧二进制兼容条件，不假设先前成功文件未登记。
- 只有实际执行了数据库回滚（或代际不明、control generation 可能倒退）时才进入恢复协调模式并轮换 generation；**不使用 `enterRecoveryMode` 作为常规升级步骤**，以免制造不必要的全站冻结。
- 部署顺序：备份 → `panel check --preflight`（只读、仅旧表）→ 通过后面板启动（自动迁移）→ 启动后 `panel check` 复核 → 业务可用。Agent 保持旧 generation 直至新 generation 接受。`enterRecoveryMode` **只**用于实际数据库回滚或代际不明的情况，不是常规升级步骤。
- 兼容矩阵：新面板+旧库（启动时补迁移，预检先跑）；旧面板+新库（迁移只加表/索引，旧二进制可启动，但**不要**在旧二进制上做授权/账本写）；新面板+旧 Agent（`Metrics` 无保留键 → 健康显示 `unknown`，不判健康）；**旧面板+新 Agent 不可直接互操作**（`DisallowUnknownFields` 会拒绝新顶层字段），因此 Agent 不新增顶层字段、且发布顺序固定面板先行。

### 5.4 关键取舍

| 取舍 | 选择 | 理由与替代方案 |
|---|---|---|
| 区间唯一性用触发器还是应用事务 | 应用事务（同成员行锁 + 相邻区间校验） | 候选 0005/0008 的触发器依赖 DB 时钟与 Go 时钟一致，实测会在节点迁移/周期推进时误报约束失败；应用事务可在同一处同时完成“记录区间 + 发布 revision”，失败语义明确。替代（纯触发器）留作未来加固，不作为本轮正确性前提 |
| 历史不明成员自动修复 | 只读预检阻断 + 管理员显式重新绑定 | 候选 0009 的“自动暂停 + 自动补算”无法证明依据，会把未知写成事实；本设计把决定权留给人，且写路径会留下可审计区间 |
| 软删用 `deleted_at` 还是 `archived` | 复用 `archived`，不加 `deleted_at` | 现有 DELETE 端点与列表过滤都基于 `archived`，再加一列会形成两套“已删除”语义；候选 0005 的 `deleted_at` 仅为其下游查询服务，本设计不需要 |
| 幂等 key 存 `batches` 还是独立 tombstone | 独立 `batch_idempotency`（不设外键、不被保留期清理） | 保留期清理会删 `batches`/`batch_items`，key 若随之消失就能被复用；独立表只存 key/指纹/回放结果，最小且可审计 |
| 升级预检做成新 CLI 还是 `panel check` 子命令 | `panel check` 子命令 | 复用 `cmd/panel` 的配置解析与连接逻辑，少一个二进制与部署入口；`cmd/dbcheck` 已是独立运维工具，不得混用其“必须 127.0.0.1:15432”的测试端点约束 |
| 节点-服务器归属历史 | 本轮**建** `node_server_history`（当前必需） | US-PR-013 的历史补报要求 + 旧 B18：现有 API 支持节点移机，`nodes.server_id` 只是当前归属，移机后 A 的合法未报流量会被 403 并卡住上传队头；仅记录“升级时刻起”的归属，不反推历史。替代方案（放宽 ownership 校验、按当前 server 别名接受）会允许跨 server 伪报，否决 |

## 6. 领域逻辑设计

### 6.1 授权与快照 revision 一致性

- 单一判定源：`access.Decide`（`access/v1`）继续是唯一算法；面板成员视图、`/sub/{token}`、Agent 快照都调用它，禁止新增第二套判定。
- 快照构建（`BuildSnapshot`）改为：`inReadTx(REPEATABLE READ, read only)` 内一次读取 `control_state`、全部成员（keyset 分页 `listAllMembersQ`）、证据批（`loadEvidenceBatchQ`）、当前服务器节点、有效凭据密钥、`agents.desired_revision`；全部内容与 revision 来自**同一 MVCC 快照**。发现“有已结束但未推进的周期”时**不在读事务内写**：返回内部哨兵，推进周期（独立写事务）后重试，最多 4 次；仍冲突返回 409 可重试，不返回旧内容配新 revision。
- `204` 判定仍只在 HTTP 层：仅当查询参数 `revision` 与 `control_generation` 都等于快照值。
- 恢复模式：快照构建读到 `recovery_mode=true` → 409，不推送可能来自旧备份的权限。
- 失败语义：任一步失败整次失败（不返回部分实例）；错误体不泄露成员凭据。

### 6.2 邀请初始化与会话撤销

- 兑换事务（§4.2）：邀请行锁 → 成员插入 → 带方案时绑定/周期/到期/凭据 → revision 发布 → 邀请消费计数；任一失败整体回滚，邀请不被消耗。
- 周期初始化只在兑换事务内发生：`period.Containing(anchor,tz,now)` + `member_periods` 插入（`source='binding'`）；不读取“今天的方案规则”反推历史。
- 会话撤销矩阵：停用/归档/删除成员 → 撤销该成员全部会话；自助改密 → 撤销除当前外的会话；管理员改密 → 撤销该成员全部会话；会话删除只允许操作本人且非当前会话（当前会话返回 400 `VALIDATION_FAILED`）。
- 改密与登录并发：`CreateSessionForPassword(sess, verifiedHash)` 在事务内锁成员行并复核 `password_hash == verifiedHash`，旧哈希登录请求失败（401），不会拿到“已作废密码签发的新会话”。
- 邀请泄露防护：列表永不返回明文 `code`；创建响应只出现一次并提示不可恢复；撤销只对未兑换生效（409 否则）。

### 6.3 恢复事务门禁与自助改密例外

- 单一实现：`store.requireWritableTx(ctx, tx)`（新建 `internal/panel/store/write_guard.go`）作为**所有**授权/账本/任务/凭据写事务的第一次加锁：`SELECT recovery_mode FROM control_state WHERE id=1 FOR UPDATE`，持锁到提交；`true` 返回 `domain.E(CodeRecoveryMode,409,…)`。锁序固定：`control_state` → 对象行（成员/节点/Agent/任务），禁止反向。
- HTTP 层 `requireWritable` 保留为快速失败，但不再作为唯一保证；事务内复查是权威。
- 身份救援保持可用：登录创建会话、退出登录、本人撤销Web会话允许进行（不创建/轮换代理授权）；否则会在管理员会话失效后形成无法登录解除核对的死锁。登录仍在同一控制行锁下复核当前密码/成员资格，但不因恢复态拒绝必要身份会话；撤销操作仍必须是本人有权撤销的会话。
- 其余允许的例外（明确、最小）：`POST /api/v1/me/password`（仅当变更字段集为 `password_hash`，由 `passwordOnlyMemberChange(before,after)` 判定；混合了授权/资料变更则拒绝）；`exitRecoveryMode`；Agent 心跳/用量/回执等必要遥测（允许进行，但不得放宽授权；账本写入仍受 §6.5 归属约束）；管理员只读端点。
- 冻结范围：成员创建/编辑/绑定/停用、授权轮换（令牌/凭据）、批量预览提交、任务重试、方案与节点组编辑、邀请创建/兑换、注册码、设置写入、周期推进（后台 tick 在冻结期直接返回“推进 0”，不算失败也不发布 revision）。
- 退出恢复：必须 `confirm=verified` + 两项核对为真；退出不清 `control_generation`。`enterRecoveryMode` **不**受 `getUpgradeReadiness` 阻断：进入恢复是冻结授权、准备人工核对的救援入口，数据正异常时更不能被阻止；readiness 只阻止不安全升级/业务启动。退出仍需原有人工核对（`confirm=verified` + 两项核对）。
- **何时才进入恢复模式**：只在①实际把数据库回滚到旧备份、②`control_generation` 可能倒退/代际不明、③从快照恢复到不同实例 时进入并轮换 generation。常规发布升级（含迁移成功/失败重试）**不**进入恢复模式；旧二进制技术回退且保持停写也不需要。避免把全站冻结当成升级流程的常规步骤。
- **已核对状态不得被还原放宽**：恢复备份可能带回已停用成员/已撤销授权。解除冻结前必须逐项核对：成员 `active/proxy_enabled/archived` 与备份前核对结果一致、`credentials` 中已撤销的不复活、`member_periods`/`usage_entries` 不被旧备份截断；核对不通过则保持冻结并报告，不放行。

#### 6.3.1 恢复期间的成员只读与订阅令牌

恢复期间不因读取推进周期、创建凭据或创建令牌。已有令牌的`getMeSubscription`只读返回；没有令牌时明确409 `RECOVERY_MODE`，不伪造空链接或写授权材料。`getMeStatus`、用量和会话等允许的读取按已持久化资料返回。成员首页分别加载状态/用量与导入能力：令牌读取被维护阻止时，只在导入区显示“服务维护核对中，暂时无法生成订阅链接”；已有状态与已知用量继续可见，不整页失败，也不尝试调用管理员恢复接口。管理端持续显示核对横幅；成员只在受影响区域提示，不能查询/泄露管理员核对细节。

### 6.4 周期、迟报与账本（历史守恒优先）

- **不变量（升级前后必须成立）**：同一成员在升级前后，其**当前周期**的 `period_start/period_end`、`used_bytes`、`effective_quota_bytes`、`proxy_access`/`allowed_node_ids` 完全一致；`usage_entries` 既有行的字节数与 `period_start/period_end` **一律不改**，不新增任何“把历史行移出额度”的标记或过滤条件。升级的前置条件是“能证明”而不是“先让迁移成功”。
- 周期推进：只从**已记录区间的终点**前进（`transitionCycle(oldEnd, anchor, loc, now)`），不按“当前规则重算包含 now 的窗口”，避免新旧规则区间重叠；跳过的中间边界按 `[oldEnd, nextBoundary)` 逐段记录，使迟报仍能命中其计费区间。下一周期规则（`pending_period_rule`）只在边界被消费。
- 迟报归属：`usagecalc.Allocate` 仍按样本发生区间切片；写账本前要求每个切片命中 `member_periods`（或该成员当前运行区间）→ 未命中整批 422 `LEDGER_UNATTRIBUTABLE` + 事件，**不 ACK**。缺失区间只能通过“证明”补建（§6.4.1），不能靠放宽校验或改历史行让批次通过。已确认数据不重复：`usage_entries` 唯一键 + `(agent_id,batch_id)` 幂等 + 同批不同 payload 409。
- 时钟异常：`usagecalc.Validate` 继续拒绝未来/回退/超窗样本；`clock_skew=true` 的批次可入账但标记并告警（`estimated` 语义保留），不得静默丢弃。
- 额度：`used_bytes` 仍按**当前 `period_start` 精确匹配**的 `usage_entries` 汇总 + 审计调整（与现状 `UsedBytes` 查询一致），不引入任何“可信/不可信”过滤；周期推进后重新判定；超额置 `over_quota` 并撤凭据、恢复时在同一事务重建凭据。个人覆盖只替换配额数值，不绕过账号/订阅/节点限制。
- 未知数据：成员 `data_complete=false` 时 `me/status` 与列表**保留原 `used_bytes` 数值**并附 `data_issues`，不得用 0 或“未知”覆盖真实账本；前端按字段区分展示（见 §4.2）。`data_complete=false` 只影响“能否据此继续授权写入”，不改变已有额度数值。

#### 6.4.1 存量区间的证明、预检与迁移（不静默排除历史）
#### 6.4.2 节点移机交接、迟报归属与终止回执（旧 B18）

- 现状缺陷：`usagecalc.Validate` 用 `nodes.server_id`（**当前**归属）校验；安全候选的 `validateAgentInstancesTx` 也只比 `core_instances.server_id`。节点 A→B 后，A 上确已产生但未上报的合法流量被判 `BATCH_OWNERSHIP_MISMATCH`（403），A 对已移走节点的最后采样/停止回执也被 403；`reporting.RunOnce` 遇失败 `break` 保留队头 → 整队卡住，进而触发队列超限停机。
- **模型分层**：`nodes.server_id` = 逻辑期望归属（授权目标）；`node_server_history` = 实际执行授权窗口（可并存）。移机不立刻截断旧窗口：旧窗口在**旧 Agent 确认收到该移机 revision 并终止旧连接**（`old_connections_terminated=true`）时才关闭，并记录 `released_revision` 与 `release_confirmed_at`。
- 移机事务（`PATCH /nodes/{nodeId}` 改 `server_id`）：更新 `nodes.server_id` → 为旧 server/旧 agent 打开“释放窗口”（`valid_to=NULL`、`released_revision=新 revision`）→ 为新 server/agent 开新窗口（`valid_to=NULL`、`released_revision=NULL`）→ 发布 revision。此刻两条并存：新的是逻辑当前归属，旧的是尚未确认释放的旧授权。
- **迟报校验按样本整个半开区间**：`NodeAuthorizedForRange(nodeID, agentServerID, agentID, startedAt, endedAt)` 要求存在一个历史窗口，其 `[valid_from, valid_to)` 与**样本半开区间** `[startedAt, endedAt)` 的覆盖关系成立：
  - 完全包含 → 通过；
  - 与窗口边界相等（`valid_to == endedAt` 或 `valid_from == startedAt`）→ 通过（半开区间语义，边界点只属于一侧）；
  - 同一个 Agent/server 的连续授权窗口并集覆盖整个采样半开区间 → 通过；不能用不同 Agent 的窗口拼出授权。窗口间存在真实未授权间隙或未知归属 → 拒绝并保留原批次供核对，不自行估算授权。
  - **不能要求 Agent 重写既有批次**：批次内容与幂等标识一经落盘保持不变，面板移机窗口也没有通过当前协议完整下发。合法交接尾部应由旧 Agent 的释放窗口覆盖，不把改批次/换 ID 当作自动恢复手段。
  - 窗口 `release_confirmed_at` 之前（即旧窗口未关闭）仍算有效授权；之后仍按未关闭窗口判定，直到确认关闭。
  - 多段匹配必须是**同一 `agent_id` 的连续窗口**（防止 A→B→A 后旧回执借用新授权）。
- 跨 server 伪报仍拒绝：无任何匹配窗口 → 403 `BATCH_OWNERSHIP_MISMATCH`（`node_id`/`member_id` 分别给出）。保留 `NodeHasHistory` 凭据历史检查。
- **终止回执（terminal-only）**：旧 A 对已移走节点的 `reportAgentApplied` / `agentHeartbeat` 中 `InstanceResult{State:"stopped", OldConnectionsTerminated:true}`，仅当同时满足：
  1. 该 `(node, agent=A)` 存在历史窗口且 `released_revision == 回执.revision`；
  2. 回执 revision ≤ 已发布 revision（不得是未来 revision）；
  3. 回执确实声明终止（`OldConnectionsTerminated=true`）且状态为 `stopped`；
  则接受该终止证据并只关闭匹配的旧窗口（写 `valid_to=release_confirmed_at=now()`），记录旧 A 的回执/事件，**不因单条终止结果推进整体 applied 水位**。A 的整体水位仍按完整报告 revision、全实例结果及既有成功规则判定；部分失败报告可证明某个旧窗口已经关闭，但不能宣称整个快照成功。不得把迟到释放回执作用于较新的授权窗口。
- **不污染 B**：该回执**不得**写 `core_instances`（那是 B 的当前运行状态），不得改 `nodes.server_id`，不得把 A 的 applied 水位抬到 B 的 revision。B 的 `core_instances` 只由 B 的 Agent 回执更新。
- **SEC02 合并接线**：安全候选的 `validateAgentInstancesTx` 改为两段：
  - 非 terminal 回执/心跳实例：仍要求 `core_instances.server_id == agent.server_id`（当前执行者校验，保留）；
  - terminal-only 回执（`stopped` + `OldConnectionsTerminated` + 命中上述历史窗口与 `released_revision`）：走交接分支，只允许写历史窗口关闭与自身水位；
  - 无历史窗口的 A 写 B 的实例 → 仍 403 `FORBIDDEN`。合并时删除候选里“仅按当前 server_id”的单段实现，避免两套判定。
- 升级回填：只登记升级时刻起的当前归属；升级前既有未确认队列不可证明归属时按 §6.4.3 **阻断升级**，不靠猜测放行。
- 验证点（独立回归，§10）：①移机前最后采样在 A 上被接受并按原周期入账；②A 的 terminal-only 回执被接受但 B 的 `core_instances`/`applied_revision` 不变；③A→B→A 后迟到的旧 A 回执不能关闭新 A 窗口、也不能把 A 水位推过新 revision；④A 无历史写 B → 403；⑤同Agent连续授权并集覆盖的交接尾部接受；存在未知间隙时422且不ACK，不要求改写原批次。

#### 6.4.3 升级前的未确认队列：阻断而非猜测

- 问题：升级前可能已有“旧 Agent 已采样/已停机但面板未 ACK”的批次与回执。若升级只把归属历史从 `now` 起登记，这些队列项的归属**不可证明**；放行升级会让它们在升级后被 403/422 拒绝并卡在队头，进而把正常 Agent 推入停机阈值。
- 因此预检把无法证明的旧队列列为 **blocker**，但必须诚实区分证据来源：PostgreSQL 的 `usage_batches` 只知道已接收批次，**无法枚举 Agent 尚未上传或尚未确认的本地队列**。禁止用面板 SQL 推断“所有队列已清空”。数据库预检之外，由维护管理员提供可核对的 Agent 一致停止/队列证据（只读本地备份或受控导出，绑定 Agent 身份、版本与内容指纹）；缺少材料、存在无法覆盖的旧样本或状态不一致均返回 `unknown/blocked`，不得迁移或启动业务。只读网页检查不得接受任意服务器文件路径，缺少离线材料时同样如实报告阻断。
- 解除路径（只允许这两种，不做第三种“丢弃”）：
  1. **维护窗口内停止新增代理流量并排空**：管理员明确执行有影响的停流与最终计量，保留旧面板的用量接收能力，让旧 Uploader 确认排空后再停止旧 Agent 并取一致状态证据；不能先杀上传进程再“等待队列清空”。工具不自主暂停成员、删数据或宣称零尾差；最后采样失败须报告，不能以空计数器蒙混通过。
  2. **补齐可证明归属**：管理员核对后提供只读的归属/周期核对材料，在预检内存视图中证明旧样本可归属；材料先独立保存并带内容指纹。只有预检通过后，迁移事务才消费这些已核对材料登记历史窗口并写审计，不改账本原值。
- 明确不做：自动暂停成员、把行排除出额度、静默丢弃批次、按“当前 server”猜归属。若无法证明且无法排空，保持 `blocked`，由人处理。
- 预检可在仅 `0001–0004` 的库和空库运行：只读旧表与管理员提供的核对材料，不依赖尚未创建的新表。**不允许 blocked 后先迁移建表再补材料**；必须先完成证明，再在迁移事务中登记。迁移时重新核对材料与数据库指纹，发生变化则回滚并重新预检。

#### 6.4.4 升级核对材料的唯一输入与恢复路径

- 节点侧入口：`agent -inspect-state <已一致停止并检查点完成的状态备份目录>`，只读导出证据到标准输出，不启动Agent、不访问面板、不迁移/写入输入库、不输出token或完整config/users秘密。输出绑定Agent身份、状态结构版本/指纹、文件内容指纹、未确认批次标识与样本时间/节点覆盖摘要；服务器身份由面板按Agent身份复核，不凭空假设本地保存了server_id。
- 检查点前提必须验证：存在非空WAL/回滚日志或结构不支持时拒绝并要求取得完整一致备份，不能忽略日志后把队列算空。工具只能验证输入可读、自洽和指纹，不能替代管理员对真实停流/停服务的外部确认；管理员保留对应操作证据。
- 面板CLI唯一材料入口：`panel check --preflight --evidence <只读核对清单.json> --json`。清单最小内容为：格式版本、导出文件相对路径及SHA256、对应Agent身份和停止确认材料引用、已人工核对的周期/执行归属断言与依据摘要。仅引用清单所在受控目录内普通文件，拒绝越界、未知格式、过大或摘要不符；不接受秘密或任意可执行内容。
- 启动/网页使用同一已验证材料：维护管理员通过`PANEL_UPGRADE_EVIDENCE_FILE`配置本地只读清单，容器以只读文件挂载。`getUpgradeReadiness`不提供上传或文件路径请求参数，只消费该服务端配置，返回摘要/阻断项；未配置且存在需证明旧队列时为unknown/blocked，不假绿。空库无旧Agent时无需材料。
- 预检只读，清单也不因检查而改写。有效材料的SHA256与数据库控制代际/修订、已应用迁移前缀、相关账本和归属资料指纹形成核对依据；迁移前重新核对，变化则中止。仅预检ready后，追加迁移在事务内登记已证明资料和审计，保留原账本/额度语义。blocked时只允许补齐/纠正外部清单后重查，不先迁移再绕门禁。
- 材料由操作员在维护窗口从自己的停止状态备份生成，不把文件进入公共前端目录或验收截图。真实密码、私钥、Bearer token与完整连接配置不出现在导出物/网页结果。材料不齐或无法证明时保持blocked；没有“忽略阻断继续”或“丢弃队列”选项。

### 6.5 批量、任务与回执

- 预览：`resolveTargets` 按操作解析真实目标集合；影响面（成员数/节点数/Agent 数）、逐成员 before/after、consequences、例外与警告持久化；TTL 10 分钟。
- 提交：§4.2 门禁顺序；`rules_version` 取 `control_state.revision_counter`；成员集合摘要 = 排序后 `(memberId, memberVersion, subscriptionId, subscriptionVersion)` 的 sha256；不一致即 `PREVIEW_STALE`。
- 幂等：同 key 同指纹 → 回放 `batch_idempotency.result`（`idempotent_replay=true`，不重复推送）；同 key 不同指纹 → 409 `IDEMPOTENCY_KEY_REUSED`；key 在批次被保留期清理后仍生效。
- 任务：每个 `(batch, agent, revision)` 至多一个任务；`tasks.revision` 绑定发布修订；`batch_items.task_id` 指向该任务。回执处理：`report.Revision > desired` → 409 `REVISION_STALE`；`< applied` → 只入 `agent_receipts`，不推进、不完成任何任务；等于 applied 且状态 `failed` → 仅完成**同 revision** 任务，旧回执不能完成新任务。
- 重试：沿用真实既有能力，允许 `failed` 或 `timeout`，重开**同一 task ID**、提升 attempts 并绑定新 revision，同时重开该任务对应 items；不平行创建新 task ID。已排队、已成功或次数用尽时拒绝，不重复消耗次数；迟到旧 revision 回执不能完成此次重试。
- 结果语义：批次 `state=applied` 只表示业务变更已提交，不表示所有节点已生效；节点生效状态由 tasks/batch_items/agent_receipts/`applied_revision` 表达；未确认状态不写“成功”。

### 6.6 Agent 收紧授权、采样、崩溃、持久化与停机确认

- 收紧授权优先：新增/删除用户、减少节点、到期收窄等“收紧”类变更先独立执行（移除用户/停核），不被其他节点的慢校验阻塞；扩权变更才需要完整校验。实现落在 `xray.Manager.Apply` + `supervisor.applySnapshotSerialized`。
- **同一实例的本地到期不得被慢校验阻塞**：把 apply gate 从“全局一把”细化为“按实例”的可取消锁（外加跨节点收紧的快速通道）。本地到期路径只需该实例的锁；若该实例正被一次慢校验持有，到期等待有界（超时后按 fail-closed 处理：先移除该实例用户/停核，而不是无限等待）。跨节点收紧（多实例同时收窄）先于任何单实例完整校验执行，且不因某节点校验超时推迟其他节点的撤权。
- 采样：`reporting.Sampler` 保留有界重试（3 次、指数退避、上限 10s）且每 `(node, coreGeneration, counter)` 基线幂等；停机/替换前必须先跑最终采样钩子（`SetSampleHook` 覆盖所有 stop 路径），失败则记录但不重复计费（基线不前进）。
- 崩溃：`state.Store` 新增 `apply_intent` 写前日志 + `CommitSnapshot` 单 SQLite 事务提交（实例集合、回执、仅完全成功时推进 generation/applied 水位并清 intent）；崩溃恢复先读 intent，若存在则不得回退到更宽授权；pidfile 归属校验（pid + 进程身份）后才回收，未确认退出保留记录。
- 持久化：SQLite 打开使用既有 `state/secure_*` 权限与 `lock_*` 单实例锁；迁移在事务内执行；写探测用真实写事务；磁盘不可写 → 上报 `health=degraded`，不静默丢账。
- 停机确认：`Stop`/`StopAll` 返回“已确认退出”才允许删除跟踪记录；未确认必须保留并上报（`InstanceResult` 状态 + 心跳 `health`），shutdown 汇总未确认实例并非零退出，避免“假停机”。
- 协议：**不新增 Agent 请求顶层字段**（旧面板 `DisallowUnknownFields` 会 400）。健康与未确认停机信息通过既有 `Heartbeat.Metrics` 的保留键（`health_degraded`/`stop_unconfirmed`/`sampling_errors`）与 `InstanceResult.State=stopping` 表达；旧面板按普通指标/状态透传，旧 Agent 缺键时面板按 `unknown` 显示，不判健康。发布顺序固定“面板先升、Agent 后升”；未来若必须加顶层字段，先做面板宽松解码/版本协商并在兼容矩阵登记。
- 归机历史：仅面板 store 的期望归属、执行授权历史与 revision 在同一 PostgreSQL 事务提交。Agent 实际执行与终止回执是异步过程，不能伪装成跨主机原子事务。`usagecalc.Validator` 按完整采样区间核验来源授权（§6.4.2），保留原有凭据历史检查。

### 6.7 TLS/REALITY 与服务端配置投影

- 输入校验：`protocol.Sanitize` + `Validate` 保持唯一入口；新增/编辑节点都走同一路径；错误字段定位到具体字段（`details.field_errors`）。
- REALITY：仅在 VLESS+`security=reality` 且未提供服务端密钥时生成 X25519 密钥对；私钥加密存 `server_key_enc` 且仅写；公钥写 `client_settings.public_key` 供客户端投影；换钥/换协议/换传输必须同一事务内成对更新并发布 revision。
- 无损编辑：`PATCH /nodes/{nodeId}` 只覆盖请求中出现的字段；`stream_settings`/`protocol_settings`/`client_settings` 为 **整体替换**（省略=不变），提交时必须携带完整原生对象，服务端 `protocol.Sanitize` 只规范化已知键、未知键原样保留在 `domain.Object` 中，不得为“简化表单”删除参数；前端高级编辑入口负责把回读到的完整对象改完再整体提交（对应“不能只发改动字段”的既有语义）。
- 投影：`clientconfig.Render` 对无法表达的节点/字段返回 `UnsupportedError` → 422 `PROTOCOL_NOT_SUPPORTED`（附 `node`/`field`/`reason`），不生成残缺配置、不静默丢字段；格式可用性在 `getMeSubscription.format_status` 提前暴露。
- TLS 证书材料：不放入仓库；节点级证书路径/内容按现有字段处理；错误在创建/编辑时返回 400，不在输出时才失败。

### 6.8 真实分页与检索

- 统一列表契约：`limit`（默认 50，1..200）、`cursor`（不透明）、`q`（可选）、以及各端点既有过滤参数；`clampLimit` 只作为兜底，不再作为分页实现。
- 游标编码：`base64url(JSON{k:[排序键值…], id:"…", v:1})`；服务端校验 `v` 与端点作用域（`scope` 参与签名/校验），非法 → 400 `CURSOR_INVALID`；游标不包含敏感数据。
- 各端点排序键（稳定、唯一）：
  - members `(lower(username), id)`；nodes `(sort_order, lower(name), id)`；servers/node-groups/subscriptions/templates `(lower(name), id)`；agents `(lower(name), id)`；core-instances `(lower(node name), node_id)`；invitations/batches/events/tasks/audit `(created_at DESC, id DESC)`；usage `(period_start DESC, member_id, node_id)`。
- 查询实现：keyset 条件 `(k1,k2) > ($1,$2)`（DESC 用 `<`），每页多取 1 行判断 `next_cursor`；`total` 不返回假总数（不提供 `count`，前端只显示“是否还有下一页”）。
- 检索：`q` 先 trim、长度 ≤64、`%`/`_`/`\` 转义后 `ILIKE`；只对索引友好列（`username`/`name`/`action`）生效；`ILIKE '%…%'` 规模上限见 §8。
- 批量范围：`member_ids` 必须显式枚举；若前端要“按筛选全选”，只能通过预览返回的 `member_set_digest` + 服务端解析的筛选条件（新增 `values.filter` 不被接受，避免服务端隐式全表操作）；提交时摘要不一致 → `PREVIEW_STALE`。
- 兼容：无 `cursor` 的旧调用仍可用（第一页）；`next_cursor` 语义从“恒 null”变为真实值，旧前端使用 `usePagedList` 可直接受益。

### 6.9 部署升级与历史资料预检

- `panel check` 子命令（`cmd/panel/main.go` 内实现，只读；命名取舍见 §5.4）：连接 `DATABASE_URL`，执行迁移集合校验（已应用/待应用/校验和）、`member_periods` 回填完整性、账本无法归属行数、绑定无周期成员、区间重叠、批次/任务孤儿、Agent 状态不可读等检查；输出 JSON（默认）与人类可读摘要；任一 `blocker` → 退出码 2 并列出码与计数；`warning` → 退出码 0 但打印。**不做任何修复**。
- 升级门禁：`deploy/manage-linux.sh` 与镜像发布流程在迁移前调用 `panel check`；`getUpgradeReadiness` 与 CLI 共用同一查询实现（`store.CheckUpgradeReadiness`），避免两套结论。
- 历史不明资料：账本行无区间、成员绑定无周期、区间重叠、批次时钟异常等一律 `blocker`，阻断升级/交付；处理路径仅两种：管理员用既有成员编辑重建可证明周期（同事务记录区间），或恢复备份；不提供“自动补算/自动暂停”按钮。
- 两种前端托管（`DEC-product-readiness-007`）：面板容器不运行 nginx；`PANEL_WEB_ROOT` 为空时只提供 API（宿主 nginx 托管静态），非空时面板托管 SPA；两种模式共享同一 API 契约与 `X-Request-Id`；升级核对需覆盖两种模式的刷新与登录（测试承接 §10）。
- 版本一致性：`getReady` 之外新增构建信息（响应附加 `version`/`commit`，来自 `-ldflags`），部署脚本比对面板与前端资源版本，避免“前端新/后端旧”。

### 6.10 候选修复整合顺序与门禁（DEC-product-readiness-006）

候选来源仅作输入，不因“patch 自述通过”进入主仓：security、agent、store、frontend/protocol、deploy/dependency 五个独立副本与 `/tmp/*.patch`、`docs/development/full-review.md` 的现状都视为未验证候选。

1. **完整导出**：每个候选从最终 WIP 重新导出完整 diff（不采用缺文件的旧 patch，例如 agent 现存 patch 缺 3 个测试文件），记录文件清单、增删行数与内容指纹（`git diff` 的 blob hash + 文件 sha256）。
2. **逐项审查**：按本设计的边界逐包审查，重点核对重叠文件（`api/admin2.go`、`store/members.go`、`store/ops.go`）与互斥实现（两套恢复门禁只能保留 `store/write_guard.go` 的一套）。
3. **分批整合**：按“迁移与存储 → 授权/门禁 → Agent → 协议/投影 → 前端 → 部署/依赖”顺序，每批单独提交、可回退；不把多个候选压成一个不可定位的大提交。
4. **回归**：每批运行受影响包的定向 `go test`（经 `scripts/backend-test.py` 选择）+ 重叠处独立回归；迁移批必须跑空库/旧库升级与失败回滚。
5. **单候选版本收口**：全部批次合并后形成唯一候选，执行跨模块全量与端到端承接（§10），任何一批未通过则该候选不得进入验收，不跨版本拼接通过数字。
6. **否决项**：候选 0009 的自动暂停/自动补算、依赖 DB 时钟的触发器方案、删除/放宽既有断言、以文件名清单冒充内容核对，均不得进入整合。

## 7. 权限与安全

- 权限模型：`public` / `session`（本人数据）/ `admin` / `agent`（Bearer）。无租户维度；成员不可通过路径参数读取他人资源（所有 me 端点用会话成员 ID，忽略请求体中的 member id）。
- 鉴权顺序：`withRequestID` → CORS → session/agent → CSRF（写）→ admin → 恢复门禁 → handler。CSRF 用会话内 `csrf_token` 常量时间比较。会话 Cookie 默认 `SameSite=Strict`（`api.Server.cookieSameSite`），跨站部署显式配 `PANEL_COOKIE_SAMESITE=none` + `Secure`。
- 敏感数据：密码 bcrypt 哈希；订阅令牌只存 hash + 密文（`PANEL_MASTER_KEY`），响应中仅本人可读明文；凭据密钥加密存储、成员输出按需解密；`server_key` 只写；`telegram_token` 只写；错误/日志/审计/事件不写秘密与完整 token；Agent token 只存 hash。
- 审计：所有管理写动作写 `audit_logs`（actor、action、target、ip、request_id），新增动作：`recovery.*`、`batch.*`、`readiness.read`（只读不审计，避免噪音）。
- 越权负例（测试承接）：非 admin 访问 admin 端点 403；成员访问他人会话/凭据 404 或 403；失效/撤销会话 401；归档成员 401；恢复冻结期普通写 409 且不产生副作用。
- 部署安全：生产必须 TLS（`PANEL_TLS_*` 或 `PANEL_TRUST_REVERSE_PROXY=true`），否则拒绝明文启动；跨站部署要求 `SameSite=None; Secure` 且 CORS 白名单精确回显。

## 8. 非功能设计（仅当前必需）

- 性能：快照构建为 O(成员数×节点数) 内存计算、查询次数常数级；分页统一 keyset，单页 ≤200；`ILIKE` 检索在成员量级较大（≳1 万）时，若 `EXPLAIN` 证据显示全表扫描不可接受，才在 0007 追加 `members (lower(username) text_pattern_ops)` 索引；本轮不预埋该索引，也不以“加索引”代替查询证据。
- 一致性：快照单 MVCC 快照、账本单事务、幂等键持久化；不引入缓存层（Redis 仅限流），避免第二真相源。
- 可观测性：`X-Request-Id` 全链路、审计与事件、Agent `status.json`（仅状态码/计数，无 id/email/配置）用于不停机核对；`/readyz` 区分 `ok|degraded`；新增指标字段仅随心跳 `metrics` 传递，不新增监控栈。
- 无其他当前必需项（无限流扩容、无多活、无缓存策略变更）。

## 9. 详细设计

### 9.1 核心数据结构（Go）

```go
// internal/panel/store/periods.go
type PeriodRecord struct {
    MemberID    string
    Start       time.Time
    End         time.Time
    AnchorDay   int
    Timezone    string
    Source      string // binding | roll_forward | proven_current | proven_ledger
}

// internal/panel/store/readiness.go
type ReadinessCheck struct {
    ID            string   `json:"id"`
    Severity      string   `json:"severity"` // blocker | warning
    State         string   `json:"state"`    // pass | fail | unknown
    Summary       string   `json:"summary"`
    AffectedCount int      `json:"affected_count"`
    Evidence      []string `json:"evidence"`
}
type UpgradeReadiness struct {
    Status      string           `json:"status"` // ready | blocked | unknown
    CheckedAt   time.Time        `json:"checked_at"`
    Candidate   string           `json:"candidate_version"`
    Schema      SchemaState      `json:"schema"`
    Checks      []ReadinessCheck `json:"checks"`
    Blockers    []string         `json:"blockers"`
}

// internal/panel/api 列表游标
type listCursor struct {
    Version int      `json:"v"`
    Scope   string   `json:"s"` // members | nodes | batches | usage | ...
    Keys    []string `json:"k"`
    ID      string   `json:"id"`
}
```

### 9.2 公开函数签名（新增/调整）

```go
// store
func (s *Store) inReadTx(ctx context.Context, fn func(*sql.Tx) error) error
func (s *Store) buildSnapshotOnce(ctx context.Context, agent *domain.Agent) (*contracts.Snapshot, error)
func (s *Store) readSnapshotTx(ctx context.Context, agent *domain.Agent, now time.Time) (*contracts.Snapshot, []domain.Member, error)
func (s *Store) recordMemberPeriodTx(ctx context.Context, tx *sql.Tx, rec PeriodRecord) error
func (s *Store) advanceMemberPeriodTx(ctx context.Context, tx *sql.Tx, m *domain.Member, now time.Time) (bool, error)
// package period; store.computeAdvance calls this shared calculation directly.
func Transition(oldEnd time.Time, anchorDay int, loc *time.Location, now time.Time) Boundary
// Options.UpgradeEvidenceFile由--evidence或PANEL_UPGRADE_EVIDENCE_FILE选定，
// 保存在Store私有字段；网页不得设置路径。只读连接不得走自动迁移的Open。
func OpenReadOnly(ctx context.Context, opts Options) (*Store, error)
func LoadUpgradeEvidence(path string) (*UpgradeEvidence, error)
func (s *Store) verifyUpgradeEvidence(ctx context.Context, q querier, input *UpgradeEvidence) (*VerifiedUpgradeEvidence, error)
func (s *Store) importUpgradeEvidenceTx(ctx context.Context, tx *sql.Tx, evidence *VerifiedUpgradeEvidence) error
func (s *Store) CheckUpgradeReadiness(ctx context.Context, candidate string) (*UpgradeReadiness, error)
func (s *Store) lookupBatchIdempotencyTx(ctx context.Context, tx *sql.Tx, key string) (fingerprint string, result []byte, err error)
func (s *Store) NodeAuthorizedForRange(ctx context.Context, q querier, nodeID, serverID, agentID string, startedAt, endedAt time.Time) (authorized bool, known bool, err error)
func (s *Store) recordNodeOwnerTx(ctx context.Context, tx *sql.Tx, nodeID, serverID string, at time.Time) error
func (s *Store) CreateSessionForPassword(ctx context.Context, sess *domain.Session, verifiedHash string) error
func requireWritableTx(ctx context.Context, tx *sql.Tx) error
func passwordOnlyMemberChange(before, after *domain.Member) bool
func (s *Store) listMembersPage(ctx context.Context, f MemberPageFilter) ([]domain.Member, string, error)

// api
func parseListCursor(raw, scope string) (listCursor, error)
func encodeListCursor(c listCursor) string
func (s *Server) handleUpgradeReadiness(w http.ResponseWriter, r *http.Request)

// period
func Transition(start time.Time, anchorDay int, loc *time.Location, at time.Time) Boundary

// agent
func (m *Manager) Apply(ctx context.Context, targets []Target, opts ApplyOptions) []Result // 收紧优先，不因他节点校验阻塞
func (s *Store) SaveApplyIntent(ctx context.Context, snap contracts.Snapshot) error
func (s *Store) CommitSnapshot(ctx context.Context, recs []InstanceRecord, report contracts.ApplyReport, applied bool) error
func (s *Manager) StopConfirmed(ctx context.Context, id, reason string) (Result, bool)
```

### 9.3 错误类型与文案（新增部分）

```go
// 复用 domain.E；仅新增稳定码，不新建错误族
LEDGER_UNATTRIBUTABLE = "LEDGER_UNATTRIBUTABLE" // 422，用量样本无法归属已记录区间
READINESS_BLOCKED     = "READINESS_BLOCKED"     // 409，存量资料未通过预检
CURSOR_INVALID        = "CURSOR_INVALID"        // 400，游标非法/作用域不符
```

面向用户的 `message`（服务端只给事实，不做营销；中文文案由前端 `web/src/lib/accessReasons.ts` 映射）：
- `LEDGER_UNATTRIBUTABLE`：`usage sample cannot be attributed to a recorded billing period`，`details.rejects[]` 携带批次/样本索引与原因码，不含成员身份以外的敏感数据。
- `READINESS_BLOCKED`：`upgrade readiness check is blocked; verify historical records before continuing`，`details.blockers[]`。
- `CURSOR_INVALID`：`invalid list cursor`，`details.field_errors.cursor`。

### 9.4 周期状态机

```text
[无区间] --binding--> [running(start,end)]
[running] --now >= end--> [running(next start=oldEnd, next end=Transition)] --record--> member_periods
[running] --pending_rule.submitted & 到达边界--> [running(新规则从 oldEnd 起)]（不重写已记录区间）
[任一状态] --升级预检 blocker--> [blocked_at_upgrade]（不自动改成员状态）
```

### 9.5 快照构建算法

```text
attempt = 0
loop:
  read tx (REPEATABLE READ, read only):
    control_state; recovery? -> 409
    members = keyset 全量
    任一成员 period_end <= now 且 subscription != nil -> 返回 needsAdvance(members)
    evidence = loadEvidenceBatchQ; nodes(该 server); secrets; revision
    build instances/users; expected = sum(users)
  needsAdvance -> 对返回成员逐一并独立事务推进周期 -> attempt++ (<=4) 继续
  否则返回 snapshot（revision 与内容同快照）
```

### 9.6 批量提交门禁顺序

```text
control_state FOR UPDATE (恢复门禁)
→ batch_idempotency[key] : 命中且指纹同 → 回放；指纹异 → 409
→ previews FOR UPDATE : 已消费 → 409；过期 → 409 PREVIEW_STALE
→ rules_version == control_state.revision_counter ? 否则 409
→ member_set_digest == 现算 ? 否则 409
→ conclusion 重算一致 ? 否则 409 PREVIEW_CONCLUSION_CHANGED
→ 应用变更 + batches/batch_items/tasks(revision) + batch_idempotency + audit
→ commit（失败整体回滚，key 不占位）
```

### 9.7 回执与任务状态

```text
receipt.revision > desired            → 409 REVISION_STALE（不写 applied）
receipt.revision < applied            → 仅写 agent_receipts（审计），不动任务
receipt.revision == desired:
  state=applied        → 完成同 revision 任务；applied_revision=revision
  applied_with_errors  → 标记失败项与事件，不宣称全部生效
  failed               → 同 revision 任务 failed + 事件
task.state: pending → dispatched → succeeded | failed → retry(new revision)
```

### 9.8 游标编解码

```text
encode: base64url({"v":1,"s":"members","k":["alice","m_01"],"id":"m_01"})
decode: 校验 v==1、s==端点作用域、k/id 长度与类型；失败 → CURSOR_INVALID
SQL:    (lower(username), id) > ($1,$2) ORDER BY lower(username), id LIMIT $3+1
```

### 9.9 升级预检判定

```text
panel check [--preflight] [--evidence <核对清单.json>] --json
material_path = CLI --evidence 优先，否则 PANEL_UPGRADE_EVIDENCE_FILE
Options.UpgradeEvidenceFile = material_path  # 只由本机配置进入，网页不能传
store = OpenReadOnly(ctx, options)           # 不调用会自动Migrate的Open
phase = preflight | post-migrate
input = LoadUpgradeEvidence(material_path)  # 缺省只在无旧证明需求时允许
proof = verifyUpgradeEvidence(read_snapshot, input)
checks = 迁移前缀/校验和 + 原账本/周期/归属 + 各Agent队列证据/摘要与外部停止确认
status = blocked if 存在已确认失败阻断项
       | unknown if 必要查询/材料缺失或无法证明
       | ready otherwise
exit = 2 blocked, 3 unknown, 0 ready/warning-only
``` 

只读连接显式禁止写事务，OpenReadOnly不要求主密钥且不得解密秘密；不建表、不改行、不推进周期，不注册/启动Agent。正常启动仍走`Open(ctx, Options)`，由同一`Options.UpgradeEvidenceFile`选择材料；`Migrate(ctx, dir)`在现有专用连接/迁移锁下、任何建表前重查预检与材料/数据库指纹，不ready即退出。新库无旧数据可不提供材料。

`internal/panel/store/upgrade_evidence.go`唯一负责清单读取、文件边界/摘要/身份校验与已证明记录的事务登记；`readiness.go`只编排只读检查并输出状态。追加迁移`0005_period_and_owner_history.sql`创建目标表后、该文件事务提交前，由`Migrate`调用`importUpgradeEvidenceTx`导入已证明窗口/周期并写审计，再登记本文件成功。导入失败当前文件整体回滚；先前成功迁移不消失。所有资料保持原账本/额度语义，不在SQL中读取文件，不用网页参数或另一个隐藏入口绕过预检。

### 9.10 两种前端托管

```text
PANEL_WEB_ROOT 为空 → 仅 API（宿主 nginx 提供 dist；/api、/sub 反代到面板）
PANEL_WEB_ROOT 非空 → 面板提供 SPA（具体路由优先于 "/"，未知 API 不落入 index.html）
共同：容器内无 nginx；TLS 由宿主终止；X-Request-Id 透传
```

## 10. 后端测试策略

- 入口与环境：Go 定向选择用 `go test ./<包> -run '<正则>' -count=1`；场景/集成测试在项目内统一入口 `scripts/backend-test.py`（**本次 CREATE**，见 §12 与下段），保留 `uv run scripts/backend-test.py -- [filter]` 形态；PostgreSQL 使用项目既有测试库/隧道（`cmd/dbcheck` 端点约定），不碰现有线上栈；Docker 仅远端已获授权环境，本机无 Docker CLI 时不执行。
- **`scripts/backend-test.py` 设计约束**（插件同名脚本以 `cargo nextest` 为核心，不能套用到本项目；项目脚本必须自包含 Go 逻辑）：
  - 入口：`uv run scripts/backend-test.py -- [filter]`（filter 可省略=全部选择），另支持 `--list`（只打印计划，不执行）、`--check-manifest`（只校验清单与真实用例名匹配）、`--json`（机器可读摘要）。**不调用 cargo/cargo-nextest**，不通过 `CLAUDE_PLUGIN_ROOT` 回退到插件 Rust 脚本；执行失败不得换脚本重试。
  - filter 语义：按 `包短名[/用例关键词]` 匹配清单条目，例如 `-- store/period` → `go test ./internal/panel/store -run 'TestPeriod|TestCycle|TestMemberPeriod' -count=1`；`-- api/recovery` → `go test ./internal/panel/api -run 'TestRecovery'`。Go 作用域只用真实包路径（`./internal/panel/...`、`./internal/agent/...`、`./cmd/...`、`./tests/e2e`）与 `-run` 正则表达，不写 Rust 风格包名/nextest 表达式。
  - Expected Test Manifest：`scripts/backend-test-manifest.json`（CREATE）声明 `[{id, packages[], run, expected_tests[], stories[], requires:[]}]`；执行前用 `go test -list '.*' ./<pkg>` 的结果核对 `expected_tests` 全部存在，缺失或为空 → 失败；运行后用 `-json` 或 verbose 输出核对至少执行了一个期望用例，0 用例不得判通过。
  - 证据：每选择记录实际命令、工作目录、退出码、起止时间、通过/失败/跳过用例名、日志文件路径（默认 `.ai/evidence/backend-test/<timestamp>.log`，不提交秘密）；整体退出码 = 任一选择非零或清单校验失败。
  - 环境预检：`go` 存在且版本匹配 `go.mod`；`DATABASE_URL`/`REDIS_URL` 可选（仅为集成选择必需）；Docker 相关选择需显式 `--with-docker` 且本机/远端已授权，缺省不执行并在摘要标 `not_run`。`uv` 不在 PATH 属于执行前工具准备动作，脚本本身在 `uv` 已就绪后运行。
- 执行前置：`uv` 当前不在 PATH → 列为执行前工具准备动作（安装并 `uv --version` 验证）后由执行阶段完成；本设计不安装、不运行、不声称通过。MCP/Chrome DevTools 未发现，浏览器证据只承接既有 Playwright 入口，不用 MCP 名义替代。
- 日常以受影响场景定向验证；**跨模块全量**（`scripts/backend-test.py` 无 filter + `web/tests` 目标用例）只在阶段收尾/单一候选版本执行，作为明确门禁，不跨版本拼数字（`DEC-product-readiness-006`）。
- 场景验证表（未执行；“承接”列标注本设计后端阶段或 web-demo 阶段）：

| 场景/验收目标 | 入口与关键断言 | 承接 | 结果/证据 |
|---|---|---|---|
| 邀请带方案兑换即开通（US-PR-006） | `POST /auth/redeem` → 断言成员有 `subscription_id`、`period_start/end`、凭据可用、邀请 `used_count=1`；重复兑换 400 且不建成员 | backend 场景（api） | 未执行 |
| 无方案邀请/失效/超量（US-PR-006） | 兑换后 `subscription_id=null` 且可登录；撤销/过期/超次均 400，无新成员、无他人信息 | backend 场景（api） | 未执行 |
| 停用与改密撤销会话（US-PR-009） | 停用后旧会话 401；改密后其他会话 401、当前会话可用；旧密码并发登录 401 | backend 场景（api+store） | 未执行 |
| 恢复冻结与自助改密例外（US-PR-014） | 恢复模式：成员创建/轮换/批量/兑换 409 `RECOVERY_MODE` 且无副作用；仅改密 200；快照 409；退出缺核对项 400 | backend 场景（api+store） | 未执行 |
| 快照 revision 一致性（US-PR-011/013） | 构建快照与撤权并发：断言返回的 revision 与内容同快照；agent 回执该 revision 后 `applied_revision` 前进；旧内容不得配新 revision | backend 场景（store，可注入时钟/事务） | 未执行 |
| 周期推进不重叠与迟报归属（US-PR-013） | 改锚日/时区后下一周期从 `oldEnd` 开始；`member_periods` 无重叠；跨周期样本切片正确；未记录区间批次 422 且不 ACK | backend 场景（store+usagecalc） | 未执行 |
| 批量幂等跨保留期（US-PR-010） | 同 key 同指纹回放 `idempotent_replay=true` 不重复推送；同 key 异指纹 409；清理 `batches` 后仍拒绝复用 | backend 场景（store） | 未执行 |
| 旧回执不完成新任务（US-PR-012） | 回执 revision<applied 只入 `agent_receipts`；>desired 409；failed 只完成同 revision 任务 | backend 场景（store） | 未执行 |
| 真实分页与检索（US-PR-018） | 造 >200 成员：逐页不重不漏、`next_cursor` 到末页为 null、`q` 过滤全量正确；非法 cursor 400；批量预览目标集合=筛选结果 | backend 场景（store+api） | 未执行 |
| Agent 收紧/采样/停机确认（US-PR-011/012） | 一个节点校验超时不影响另一节点撤权；停机前最终采样；未确认停机保留记录并非零退出；崩溃后 intent 不回退宽授权 | backend 场景（`internal/agent/...`，需真实核心/隔离环境；无则记未执行） | 未执行 |
| TLS/REALITY 与投影（US-PR-004/007） | 只改名称 PATCH 后证书/密钥/传输参数不变；换钥配对一致；不可表达格式 422 且 `format_status` 提前提示 | backend 场景（protocol+clientconfig+api） | 未执行 |
| 移机末采样与交接（US-PR-013） | A→B 后 A 的末采样（区间落在旧窗口内）被接受并按原周期入账；样本横跨交接点 → 422 且不 ACK；B 的当前实例状态正常 | backend 场景（store+usagecalc） | 未执行 |
| 旧 Agent 终止回执不污染 B（US-PR-011/013） | A 对已移走节点上报 `stopped` + `old_connections_terminated` 且 `released_revision` 匹配 → 关闭旧窗口、只更新 A 自身水位；`core_instances`（B）与 `nodes.server_id` 不变；A 无历史写 B → 403 | backend 场景（store+api） | 未执行 |
| A→B→A 晚到回执（US-PR-013） | A→B→A 后旧 A 的迟到终止回执不能关闭新 A 窗口／不能把 A 水位推过新 revision；仍按窗口与 revision 判定 | backend 场景（store） | 未执行 |
| 升级前未确认队列阻断（US-PR-016） | 造不可证明归属的未 ACK 批次：`panel check --preflight` = blocked 且不迁移不启动；排空或补登归属后 ready；无“丢弃批次”路径 | backend 场景（store+cmd/panel check） | 未执行 |
| 本地到期 vs 同实例慢校验（US-PR-011） | 单实例慢校验持锁时本地到期仍按时撤权（有界等待 + fail-closed），不被拖到校验结束；跨节点收紧先执行 | backend 场景（`internal/agent/xray`，需真实/隔离核心） | 未执行 |
| 节点移机后的迟到上报（US-PR-013 / 旧 B18） | 节点 A→B 后，A 的合法未报批次按样本时点归属被接受并进入原周期；B 区间由 B 的 Agent 上报成功；A 上报 B 区间样本 403 `BATCH_OWNERSHIP_MISMATCH`；无历史/归属未知 422 `LEDGER_UNATTRIBUTABLE` 且不 ACK、队列不整队失败 | backend 场景（store+usagecalc+agent reporting） | 未执行 |
| 升级历史守恒（US-PR-013/016） | 升级前后同一成员当前周期 `used_bytes/effective_quota_bytes/proxy_access/allowed_node_ids` 逐字段相等；`usage_entries` 行数与字节数不变；不可证明库在预检即 `blocked` 且未执行迁移；空库/仅 0001-0004 库可跑预检 | backend 场景（store+cmd/panel check） | 未执行 |
| DELETE 现状与退役语义（US-PR-010/016） | 订阅有绑定历史→归档 + `retired=true` 且绑定/账本行数不变、无 FK 400；无历史→物理删；有未归档绑定→409；模板被未归档订阅引用→409；DELETE 不要求 `version` | backend 场景（api+store） | 未执行 |
| 升级预检阻断（US-PR-016） | 造“绑定无周期/账本无区间/区间重叠/校验和不符”库：`panel check` 退出码 2、`getUpgradeReadiness=blocked`；无自动修复写入 | backend 场景（store+cmd/panel check） | 未执行 |
| 两种前端托管与登录刷新（US-PR-001） | 隔离容器：`PANEL_WEB_ROOT` 有/无两种模式 `/healthz`、`/readyz`、登录、刷新深链、未知 API 不返回 HTML | web-demo（`scripts/verify-panel-image.sh` 扩展；需 Docker 远端） | 未执行 |
| 注册码接入与离线/未知（US-PR-003） | 生成注册码 → Agent 注册成功且 code 一次性；失效/重复注册 400；心跳超时后 `listAgents.online=false`，无心跳记录显示未知而非离线；`createRegistrationCode` 不写成员授权 | backend 场景 + web-demo | 未执行 |
| 节点无损编辑与换钥（US-PR-004） | 仅改 `name` 的 PATCH 后回读 `stream_settings`/`protocol_settings`/`client_settings` 与 `server_key_set` 语义不变；换 REALITY 密钥后 `server_key_set=true` 且客户端投影公钥配对；缺证书/参数不兼容 400 且字段定位 | backend 场景（api+protocol） | 未执行 |
| 服务方案默认值与个人覆盖（US-PR-005） | 方案 `quota_bytes=null`/`default_valid_days=null` 与个人覆盖组合：覆盖只改数值不绕过订阅暂停/到期/节点范围；改锚日/时区不改当前周期 | backend 场景（access+store） | 未执行 |
| 成员首页状态原因（US-PR-007/008） | 无方案/暂停/到期/超额/节点禁用分别返回对应 `proxy_access`+`access_reasons`；`data_complete=false` 时 `used_bytes` 不被消费为 0；`format_status` 对不可表达节点给出 reason | backend 场景（api） | 未执行 |
| 首次设置进度（US-PR-002） | 空库 `setup` 全未完成；逐项创建后对应步骤变为完成；已有对象复用不重复创建；进度来自真实查询而非配置项 | backend 场景（store+api） | 未执行 |
| 越权与秘密保护（US-PR-017） | 非 admin 403；成员读他人资源 404/403；响应/日志/审计不含密码、私钥、完整 token；`server_key`/`telegram_token` 只写 | backend 场景（api） | 未执行 |
| 真实客户端矩阵（US-PR-005） | 既有 `tests/e2e/client_matrix_test.go` 固定矩阵，单协议导入与 69,632 字节回放，记录失败/未运行 | web-demo（远端 Docker/SSH） | 未执行 |

- 局部单元测试（按需）：游标编解码、`transitionCycle` 边界（月末/闰年/DST）、`passwordOnlyMemberChange`、`format_status` 映射，放对应包 `_test.go`；其余规则优先场景覆盖，不重复造同路径用例。
- 测试可信度：断言必须能在修复前失败（新增回归用例先红后绿）；不得删断言或放宽预期；SKIP/NOT-RUN 不计 PASS；运行证据含命令、退出码、用例清单、日志路径与代码指纹。

## 11. 风险与验证动作（后端范围）

| 风险项 | 等级 | 缓解或验证动作 | 负责人 | 完成条件 |
|---|---|---|---|---|
| 候选 patch 与最终 WIP 不一致、日志早于改动 | P0 | 从各独立副本完整导出、记录内容指纹与文件清单；缺测试文件（agent 3 个）必须补齐后重跑 | backend-dev（主会话复核） | 候选树指纹 + 同树运行证据齐全，重叠文件单独回归 |
| 迁移与历史资料改写/自动排除历史 | P0 | 不采用 0009 自动逻辑；不加 `period_recorded` 之类“移出额度”标记；预检只读阻断；迁移只登记可证明区间 | backend-dev | 空库/旧库/不可证明库、失败回滚、升级前后当前周期字段逐项相等 |
| 节点移机导致合法迟报被拒并卡住队列 | P0 | `node_server_history` + 按样本时点归属；未知归属 422 不猜；不删 ownership 校验 | backend-dev | 移机前后/并发/未知归属四类场景通过 |
| DELETE 语义变化引发调用方误解 | P1 | 订阅退役 `retired` 与模板引用 409 单独发布说明并同步契约/前端提示 | backend-dev | 两处语义变更有用例与文档更新 |
| 恢复门禁与自助改密冲突、双重实现 | P0 | 单一 `requireWritableTx`；合并后删除 security patch 同名定义；组合变更拒绝 | backend-dev | 并发进入恢复+写、仅改密、组合改密三类场景通过 |
| 快照/回执 revision 错配导致撤权延迟 | P0 | 单 MVCC 快照 + 同 revision 回执完成；注入时钟/事务并发复现 | backend-dev | 并发撤权测试在修复前失败、修复后通过 |
| 周期重叠/迟报错期导致重复计量 | P0 | 已记录区间权威 + 未归属批次拒绝；不自动补算 | backend-dev | 跨周期、改规则、迟到/重发、时钟异常用例通过 |
| 幂等 key 保留期后可复用 | P1 | `batch_idempotency` 持久 tombstone + 回填 | backend-dev | 清理批次后仍拒绝复用并回放 |
| 分页/检索在规模下退化 | P1 | keyset + 必要索引；`EXPLAIN` 证据；不做前端假分页 | backend-dev | >200 对象逐页正确，查询计划无全表扫描（除非有证据说明可接受） |
| TLS/REALITY 参数丢失或不可连接 | P0 | 无损 PATCH + 成对换钥 + 投影失败显式 422；真实客户端矩阵承接 | backend-dev + web-demo | 浏览器编辑回读 + 核心加载/握手在同一候选版本验证 |
| 工具缺失导致假验证（uv/Docker/MCP） | P1 | 执行前预检；缺工具则该阶段记未执行并交回编排层；不借插件 Rust 脚本绕过 | 主会话 | 每阶段报告工具状态与未执行项 |
| 前后端版本不一致 | P1 | `/readyz` 附加版本 + 部署脚本比对 + 两种托管刷新验证 | backend-dev + web-demo | 两种托管刷新登录通过且版本一致 |
| 新增迁移与 `cmd/panel` 变更导致升级/回退失败 | P0 | 区分“旧二进制技术回退（停写）”“数据库备份恢复（需恢复核对）”；`panel check` 门禁；生产/部署文档同步 | backend-dev | 三类库 + 两种回退路径均有可复现步骤与证据 |
| 文档与契约漂移（`docs/design/implementation-contract.md` 仍写“next_cursor 恒 null”） | P1 | 实现与契约文档同批更新；check 脚本/评审核对 operation 集合与字段 | backend-dev | 契约文档与新响应一致，无“实现改了文档没改” |

## 12. 文件影响范围（后端文件）

> 只列本端文件；全量汇总由主文档 §8 负责。MODIFY/DELETE 路径均存在于主仓 `3435f1d`；CREATE 路径父目录均存在（`internal/panel/{api,store,domain,period,access}`、`internal/agent/{supervisor,xray,state,reporting}`、`internal/contracts`、`cmd`、`migrations`、`scripts`、`tests/e2e`、`docs/...`）。

| 文件 | 操作 | 说明 |
|---|---|---|
| `internal/panel/api/api.go` | MODIFY | 列表参数/cursor 装配；`getUpgradeReadiness` 路由；写路由统一恢复门禁顺序 |
| `internal/panel/api/handlers.go` | MODIFY | login 走 `CreateSessionForPassword`；redeem 事务语义；me/status、me/subscription 新字段；me/usage 保留嵌套 + 真实游标 |
| `internal/panel/api/admin.go` | MODIFY | 列表分页与 `q`；overview `setup`；节点无损 PATCH 保持 |
| `internal/panel/api/admin2.go` | MODIFY | 成员/方案/模板/邀请列表分页检索；成员详情 `data_complete`；恢复端点核对项 |
| `internal/panel/api/batches.go` | MODIFY | 列表分页；错误码/重试语义；幂等复用回放 |
| `internal/panel/api/helpers.go` | MODIFY | 通用字段错误映射与 `requireVersion`；游标编解码只在 pagination.go，不重复实现 |
| `internal/panel/api/readiness.go` | CREATE | `handleUpgradeReadiness` + 响应映射（命名依据：同目录按领域拆分 `batches.go`/`static.go`） |
| `internal/panel/api/pagination.go` | CREATE | 游标编解码、limit 夹取、keyset 条件构造（同目录既有 `helpers.go` 的辅助风格） |
| `internal/panel/api/security_regression_test.go` | CREATE | 越权、CSRF、限流、秘密不外泄负例（候选 patch 已有同名资产，需按本设计重建） |
| `internal/panel/api/recovery_gate_test.go` | CREATE | 恢复冻结 + 改密例外 + 并发进入 |
| `internal/panel/api/pagination_test.go` | CREATE | 分页/检索/cursor 非法/末页 |
| `internal/panel/api/integration_test.go` | MODIFY | 既有集成断言跟进真实分页与新字段 |
| `internal/panel/api/acceptance_test.go` | MODIFY | 端到端 API 场景（邀请、恢复、批量）更新断言 |
| `internal/panel/store/store.go` | MODIFY | 新增 `inReadTx`；迁移器对新增文件的兼容；`querier` 复用 |
| `internal/panel/store/access.go` | MODIFY | `loadEvidenceBatchQ`/`nodeGroupMapQ` 快照变体；`data_complete` 判定 |
| `internal/panel/store/snapshot.go` | MODIFY | 单 MVCC 快照构建 + 周期推进重试 + revision 同源 |
| `internal/panel/store/members.go` | MODIFY | 邀请兑换开通事务；`CreateSessionForPassword`；会话撤销矩阵；`UpdateMember` 门禁与区间记录；`listMembersPage` |
| `internal/panel/store/periods.go` | CREATE | 复用候选真实落点承接 PeriodRecord、区间历史与重叠检查，按本设计审查改造，不另建重复 period_history.go |
| `internal/panel/store/batch_idempotency.go` | CREATE | 持久幂等 tombstone 读写与回放（与 `batch_submit.go` 同域） |
| `internal/panel/store/batch_submit.go` | MODIFY | 门禁顺序、幂等回放、任务 revision 绑定、`batch_items` 回填修复 |
| `internal/panel/store/batch_preview.go` | MODIFY | 目标集合解析、conclusion 重算、分页相关过滤 |
| `internal/panel/store/usage.go` | MODIFY | 归属校验（完整区间 `NodeAuthorizedForRange`、`LEDGER_UNATTRIBUTABLE`）、迟报/时钟、额度恢复重建凭据；不改既有账本行语义 |
| `internal/panel/store/ops.go` | MODIFY | 回执 revision 语义、任务列表过滤、`retryTask` 新 revision、overview `setup`、恢复退出核对；`validateAgentInstancesTx` 接线（terminal-only 走历史窗口，其余保持当前 server 校验，SEC02 合并） |
| `internal/panel/store/resources.go` | MODIFY | keyset 分页变体、`q` 过滤；节点移机时同事务关闭/开启 `node_server_history` 并发布 revision；DELETE 语义按 §4.3（订阅退役、模板引用 409） |
| `internal/panel/store/readiness.go` | CREATE | `UpgradeReadiness` 查询与稳定检查码（与 `maintenance.go` 同目录的只读诊断模块） |
| `internal/panel/store/write_guard.go` | CREATE | `requireWritableTx`/`controlModeTx`/`inWritableTx`/`passwordOnlyMemberChange`（单一门禁实现） |
| `internal/panel/store/maintenance.go` | MODIFY | 保留期清理不删 `batch_idempotency`；告警类型补充 |
| `internal/panel/store/snapshot_consistency_test.go` | CREATE | 快照 revision 与内容同源、并发撤权 |
| `internal/panel/store/batch_unit_test.go` | MODIFY | 预览/提交/幂等断言跟进 tombstone 与 revision 绑定 |
| `internal/panel/store/list_batches_test.go` | MODIFY | 列表分页与时间排序断言 |
| `internal/panel/api/patch_test.go` | MODIFY | 版本冲突、`nullable` 清除、无损编辑断言跟进 |
| `internal/panel/api/batches_test.go` | CREATE | 幂等回放、`PREVIEW_STALE`/`PREVIEW_CONCLUSION_CHANGED`、恢复冻结（同目录按领域拆分测试文件） |
| `internal/panel/store/periods_test.go` | CREATE | 区间记录、推进不重叠、迟报归属 |
| `internal/panel/store/readiness_test.go` | CREATE | 各类 blocker、空库/旧库/校验和 |
| `internal/panel/store/write_guard_test.go` | CREATE | 门禁锁序、改密例外、组合变更拒绝 |
| `internal/panel/period/period.go` | MODIFY | `Transition(oldEnd, anchor, loc, at)`；边界/DST 单测跟进 |
| `internal/panel/period/period_test.go` | MODIFY | 追加 Transition 边界用例 |
| `internal/panel/access/access.go` | MODIFY | 保持 `access/v1` 判定顺序；补充 `data_complete` 所需输入/原因码（不改已有原因码语义） |
| `internal/panel/domain/errors.go` | MODIFY | 新增 3 个稳定码 |
| `internal/panel/domain/types.go` | MODIFY | `Member.DataComplete/DataIssues`、`OverviewSetup`、`FormatStatus`、`UpgradeReadiness` 等 DTO |
| `internal/contracts/agent.go` | MODIFY | 注释冻结“不新增 Agent 请求顶层字段”，登记 `Metrics` 保留键与 `stopping` 状态；结构体字段集合不变 |
| `internal/contracts/agent_test.go` | CREATE | 新旧 Agent/面板 JSON 互操作：旧面板 `DisallowUnknownFields` 拒绝新顶层字段的负例、Metrics 保留键透传、缺键按 unknown |
| `internal/agent/supervisor/supervisor.go` | MODIFY | 收紧优先 apply、停机确认汇总、健康上报 |
| `internal/agent/supervisor/gate.go` | CREATE | 可取消 apply/sample gate（候选已实现，需重审后按本设计落地） |
| `internal/agent/supervisor/status.go` | MODIFY | `status.json` 增加 `health`/未确认停机标记（仅状态码） |
| `internal/agent/supervisor/gate_test.go` | CREATE | ctxMutex 取消不累积 goroutine、不丢 token |
| `internal/agent/supervisor/review_crash_real_test.go` | CREATE | 真实核心崩溃/过载停机持久化（需真实核心，缺环境记未执行） |
| `internal/agent/xray/manager.go` | MODIFY | 收紧类变更优先、Stop 确认、采样钩子覆盖所有 stop 路径 |
| `internal/agent/xray/runner.go` | MODIFY | 进程退出确认与身份校验（pidfile 归属） |
| `internal/agent/xray/review_tighten_test.go` | CREATE | 他节点慢校验不阻塞撤权、多节点撤权、停止失败不回退 |
| `internal/agent/xray/review_stop_test.go` | CREATE | 未确认停机保留记录、幸存核心阻止替换、过期实例 fail-closed |
| `internal/agent/xray/manager_test.go` | MODIFY | 适配收紧/停机语义 |
| `internal/agent/xray/review_handoff_test.go` | CREATE | 同实例慢校验下本地到期仍撤权（有界等待/fail-closed）、跨节点收紧先执行 |
| `internal/panel/store/handoff_regression_test.go` | CREATE | 移机末采样/全区间覆盖与未知间隙拒绝、terminal-only 回执不污染 B、A-B-A 晚到回执、无历史 403 |
| `internal/agent/state/store.go` | MODIFY | `apply_intent`、`CommitSnapshot` 事务、`NarrowExpiredInstance` 不覆盖新提交 |
| `internal/agent/state/review_commit_test.go` | CREATE | 全有或全无、失败不推进水位、计量串行化 |
| `internal/agent/reporting/reporting.go` | MODIFY | 采样重试与最终采样失败语义、健康暴露 |
| `internal/agent/reporting/sampling_retry_test.go` | MODIFY | 断言不重复计费与失败可见 |
| `internal/panel/protocol/protocol.go` | MODIFY | REALITY 密钥成对校验、无损字段保留、错误字段定位 |
| `internal/panel/protocol/reality_protocol_test.go` | MODIFY | 换钥/配对/TLS 材料负例 |
| `internal/panel/clientconfig/clientconfig.go` | MODIFY | `format_status` 可用性计算、不可表达显式错误 |
| `internal/panel/clientconfig/clientconfig_test.go` | MODIFY | 四种格式可用性与不可表达断言 |
| `cmd/panel/main.go` | MODIFY | 增加 `check` 子命令调用只读预检（默认仍是启动服务）、构建版本注入、启动失败信息 |
| `cmd/panel/check_test.go` | CREATE | 预检退出码 0/2/3 与 JSON 输出（同目录已有 `cookie_samesite_test.go`/`transport_test.go`，命名依据充分） |
| `migrations/0005_period_and_owner_history.sql` | CREATE | `member_periods` + `node_server_history` + 索引 + 可证明当前区间/当前归属回填 |
| `migrations/0006_batch_task_durability.sql` | CREATE | `batch_idempotency` + `tasks.revision` + 回填/索引/孤儿修复 |
| `migrations/0007_usage_queried_indexes.sql` | CREATE | 仅新增 `usage_entries(member_id,period_start,node_id)` 与 `usage_batches(agent_id,received_at DESC)` 索引，不改数据 |
| `scripts/backend-test.py` | CREATE | Go 定向选择、Expected Test Manifest 核验、真实退出码/日志证据、`-- [filter]`、`--list`/`--check-manifest`/`--json`；不调用 Cargo |
| `scripts/backend-test-manifest.json` | CREATE | 选择→包/`-run`/期望用例/故事映射清单（父目录 `scripts` 已存在；与执行脚本同目录，命名沿用 `verify-panel-image.sh` 的 `名词-动词` 风格） |
| `scripts/verify-panel-image.sh` | MODIFY | 双托管模式断言扩展到登录/刷新/未知 API；版本一致性 |
| `scripts/run-remote-tests.sh` | MODIFY | 远端 Docker 门禁串行化与清理核对 |
| `tests/e2e/upgrade_restore_test.go` | CREATE | 隔离库升级/预检阻断/恢复核对（需 Docker 远端） |
| `tests/e2e/authorization_window_test.go` | CREATE | 在线/离线撤权 60 秒窗口与恢复（需真实核心） |
| `tests/e2e/usage_late_report_test.go` | CREATE | 真实流量跨周期/迟报/重发（需真实核心） |
| `tests/e2e/browser_test.go` | MODIFY | 浏览器入口断言跟进真实分页与新状态 |
| `tests/e2e/docker_lifecycle_test.go` | MODIFY | 停机确认与清理断言 |
| `deploy/manage-linux.sh` | MODIFY | 升级前 `panel check` 门禁、备份与回退说明 |
| `deploy/nginx.conf.example` | MODIFY | 两种托管模式的 `/api`、`/sub`、SPA 深链与 `X-Request-Id` 透传 |
| `.github/workflows/images.yml` | MODIFY | 发布前迁移/预检门禁、前端与后端版本一致性 |
| `docs/design/implementation-contract.md` | MODIFY | 同步列表游标、新字段、`me/usage` 形状与新错误码 |
| `docs/reference/api.md` | MODIFY | 端点目录与示例（不含秘密） |
| `docs/deployment/production.md` | MODIFY | 备份/预检/升级/恢复/两种托管步骤 |
| `docs/deployment/docker.md` | MODIFY | 镜像与升级顺序；技术回退与数据库恢复分别核对，不混同 |
| `docs/development/testing.md` | MODIFY | 新增 `scripts/backend-test.py` 入口、Go 定向与全量门禁、uv 前置 |
| `README.md` | MODIFY | 快速开始与验证入口同步（不写“已验证”） |
| `deploy/deploy-agent.sh` | MODIFY | 复用独立安装器的可执行路径、服务单元、状态目录与权限修复 |
| `go.mod` | MODIFY | 安全依赖版本与锁定，统一候选树重新扫描/回归 |
| `go.sum` | MODIFY | 安全依赖版本与锁定，统一候选树重新扫描/回归 |
| `internal/panel/api/static.go` | MODIFY | 复用静态目录/符号链接/特殊文件护栏，保持SPA与API边界 |
| `internal/panel/api/static_boundary_test.go` | CREATE | 复用静态目录/符号链接/特殊文件护栏，保持SPA与API边界 |
| `internal/panel/api/static_open_other.go` | CREATE | 复用静态目录/符号链接/特殊文件护栏，保持SPA与API边界 |
| `internal/panel/api/static_open_unix.go` | CREATE | 复用静态目录/符号链接/特殊文件护栏，保持SPA与API边界 |
| `tests/e2e/deploy_agent_test.go` | CREATE | 复核并复用 deploy 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/api/oracle_regression_test.go` | CREATE | 复核并复用 store 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/api/store_binding_regression_test.go` | CREATE | 复核并复用 store 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/maintenance/maintenance_integration_test.go` | MODIFY | 复核并复用 store 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/store/authoritative_regression_test.go` | MODIFY | 复核并复用 store 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/store/batch_node_group_unit_test.go` | MODIFY | 复核并复用 store 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/store/migration_upgrade_test.go` | CREATE | 复核并复用 store 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/store/oracle_regression_test.go` | CREATE | 复核并复用 store 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/store/period_advance_test.go` | MODIFY | 复核并复用 store 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/usagecalc/usagecalc.go` | MODIFY | 完整区间历史归属、跨周期和迟报守恒，保留跨来源拒绝 |
| `internal/panel/api/auth_security.go` | CREATE | 登录来源、廉价拒绝与有界公平限流，保留合法认证策略 |
| `internal/panel/api/rotation_scope_test.go` | MODIFY | 复核并复用 security 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/limit/limit.go` | MODIFY | 登录来源、廉价拒绝与有界公平限流，保留合法认证策略 |
| `internal/panel/limit/limit_test.go` | MODIFY | 复核并复用 security 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/limit/security_test.go` | CREATE | 复核并复用 security 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/store/security.go` | CREATE | 邀请/会话/归属逻辑复用；恢复门禁仅保留write_guard.go的一份实现 |
| `internal/panel/store/security_concurrency_test.go` | CREATE | 复核并复用 security 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/store/security_invitation_test.go` | CREATE | 复核并复用 security 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `docs/deployment/split-panel-agent.md` | MODIFY | 按最终实现事实同步项目说明，不冒称已完成运行验收 |
| `internal/panel/clientconfig/clients.go` | MODIFY | 真实配置与客户端投影、TLS/REALITY归一化和兼容性，禁止静默丢参数 |
| `internal/panel/clientconfig/realcore_test.go` | MODIFY | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/clientconfig/reality_alpn_test.go` | MODIFY | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/clientconfig/review_regression_test.go` | CREATE | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/clientconfig/tls_handshake_test.go` | CREATE | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/protocol/endpoint.go` | MODIFY | 真实配置与客户端投影、TLS/REALITY归一化和兼容性，禁止静默丢参数 |
| `internal/panel/protocol/tls.go` | CREATE | 真实配置与客户端投影、TLS/REALITY归一化和兼容性，禁止静默丢参数 |
| `internal/panel/protocol/tls_cert_test.go` | MODIFY | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/protocol/tls_review_test.go` | CREATE | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/panel/xrayconf/xrayconf.go` | MODIFY | 真实配置与客户端投影、TLS/REALITY归一化和兼容性，禁止静默丢参数 |
| `internal/agent/state/commit.go` | CREATE | 复用持久化提交、故障状态与进程身份实现；按本设计补齐收紧/恢复/计量边界 |
| `internal/agent/state/review_regression_test.go` | CREATE | 复核并复用 agent 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/agent/state/store_test.go` | MODIFY | 复核并复用 agent 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/agent/supervisor/health.go` | CREATE | 复用持久化提交、故障状态与进程身份实现；按本设计补齐收紧/恢复/计量边界 |
| `internal/agent/supervisor/review_gate_test.go` | CREATE | 复核并复用 agent 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/agent/supervisor/review_regression_test.go` | CREATE | 复核并复用 agent 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/agent/supervisor/supervisor_test.go` | MODIFY | 复核并复用 agent 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/agent/xray/health.go` | CREATE | 复用持久化提交、故障状态与进程身份实现；按本设计补齐收紧/恢复/计量边界 |
| `internal/agent/xray/procidentity_linux.go` | MODIFY | 复用持久化提交、故障状态与进程身份实现；按本设计补齐收紧/恢复/计量边界 |
| `internal/agent/xray/procidentity_windows.go` | MODIFY | 复用持久化提交、故障状态与进程身份实现；按本设计补齐收紧/恢复/计量边界 |
| `internal/agent/xray/regression_p0_test.go` | MODIFY | 复核并复用 agent 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `internal/agent/xray/review_regression_test.go` | CREATE | 复核并复用 agent 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |

主审补齐说明：以上实物候选路径用于明确复用/核验边界，不表示批准原实现或旧报告。复用能力落在一个实现处，不能平行保留同义helper；测试须证明增量价值，不复制错误期望。候选压缩测试 `internal/panel/store/review_fixes_test.go` 不自动采用，其有效场景应由已列oracle/场景测试覆盖并在Handoff交代。外部候选SQL不自动采用，追加迁移按本设计单独审查。

| `cmd/agent/main.go` | MODIFY | 增加只读inspect-state分支，早于注册/核心初始化，不启动进程 |
| `internal/agent/state/upgrade_evidence.go` | CREATE | 读取一致状态备份并导出非秘密队列/身份/版本证据，拒绝未合并日志 |
| `internal/agent/state/upgrade_evidence_test.go` | CREATE | 导出不改输入/无秘密、WAL与未知结构拒绝、队列非空不能报空 |
| `cmd/agent/inspect_state_test.go` | CREATE | CLI不启动服务/不访问网络、stdout与退出码、错误输入 |

| `internal/panel/store/upgrade_evidence.go` | CREATE | 单一材料加载/路径和摘要校验/DB身份绑定/事务登记实现，供预检与迁移复用 |
| `internal/panel/store/upgrade_evidence_test.go` | CREATE | 材料缺失/越界/摘要变化/错身份/只读与导入失败回滚/原账本守恒 |

注册码生成与Agent注册属于恢复期冻结的普通业务写，统一409 `RECOVERY_MODE`；既有Agent心跳/用量/回执仅按既有权限接收，不因此授予新身份。兼容错误码`NODE_IN_USE`仍用于被引用方案的删除阻断，本轮不改稳定码；界面按方案上下文解释“先迁移或解除绑定”，不显示误导性的节点占用文案。
