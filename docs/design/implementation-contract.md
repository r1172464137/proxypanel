# 冻结实现契约 v1

状态：负责人批准的实现基线。优先级：用户最新决定 > 主计划 > 本契约 > product/acceptance > api-proposal。提案中的示例不是已实现功能。任何接口改动先报告主负责人，不自行修改本文件。

## 最新用户决定与工程决策

- VLESS 参数完整支持，以固定官方 Xray 26.3.27 实际接受的配置字段为边界；其他常用协议覆盖 VMess、Trojan、Shadowsocks（含2022）。被核心移除的传输不可伪称支持。
- 客户端：sing-box、Mihomo/Clash 系、v2rayN。各客户端仍按自身能力输出，不能因服务端接受某参数就保证三者均支持。不可表示的参数/组合返回明确错误，不静默删除。
- Go 1.27.3，module `proxypanel`；HTTP 使用标准库 ServeMux；pgx/v5、redis/v9、x/crypto、yaml.v3、modernc/sqlite 已由主负责人锁定依赖。
- 开发与验证环境是 **Linux**，命令用 bash；只主负责人修改根 go.mod/go.sum。
- 独立 Xray 二进制由部署方从官方发行下载并核对 SHA256，放在受控目录（例如 `/opt/proxypanel/xray/26.3.27/xray`）。
  Agent 可通过 `xray_path` 指向经校验路径，不接受任意 URL 动态下载。
- 节点技术/用户配置变化默认重启该独立核心实例，确保断开旧连接；一个节点一个实例作为当前执行策略，界面明确短暂中断。未来动态API优化不能降低撤权语义。
- 默认业务时区 Asia/Shanghai、每月1日00:00重置，支持每月固定1–31日并按月末裁剪；周期起点首次绑定时间，终点下个anchor；直接改规则/换订阅均保留当前周期，新规则下周期生效。DST按product提案定义。
- 流量跨周期按采样区间重叠时长分摊，整数余数归最后片段，必须标estimated；同批次不可因补报重复计量。
- Agent轮询5s，统计采样5s，上报5s，心跳15s；HTTP超时10s，有界指数退避；单配置校验/停启总预算<60s。控制面配额为权威，不引入离线分布式配额分配。
- 单实例面板默认，无微服务。成员规模小允许事务内锁定控制修订行来保证并发规则更新和最后管理员保护；持久任务不能只放内存。
- 节点测试在独立的验证栈上进行（Linux 上的 Docker + 真实 Agent + 真实 Xray），不把数据库宿主当节点。

## HTTP约定

- 单对象/动作成功：`{"data": ...,"request_id":""}`（当前对象包络 request_id 为空；追踪用 X-Request-Id）；列表：`{"items":[],"next_cursor":null}`；错误：`{"error":{"code":"...","message":"...","retryable":false,"request_id":"...","details":{}}}`（details 可省略）。`/me/usage` 的 items 是对象 `{usage:[],adjustments:[]}`，不是数组。
- 时间RFC3339 UTC，ID使用服务端随机不透明字符串，版本int64。分页 items 空集合使用[]；readiness 诊断失败等对象中的切片可能为 null，按 [API 参考](../reference/api.md#11-升级-readiness只读诊断与本地材料边界) 的真实 JSON 类型消费。
- 正式浏览器/Agent 入口使用 HTTPS；宿主 nginx 可终止 TLS，再通过受控明文链路访问面板。Docker 镜像无 nginx，仅单 Go 进程，宿主回环 8081 → 容器 `0.0.0.0:8080`，不再要求反代模式监听回环；DEV 仍必须回环，只用于裸进程本机开发，不用于普通 Docker bridge。
- 镜像默认 `PANEL_WEB_ROOT=/usr/share/proxypanel/web` 托管 SPA，显式空为 API-only（宿主 nginx 托管 dist）；裸进程默认 API-only。直接 TLS 可任意监听，成对证书优先于反代路径；DEV 回环检查独立生效。
- 会话Cookie `pp_session`，HttpOnly，SameSite=Strict，生产Secure；写接口要求X-CSRF-Token（登录/邀请兑换/Agent Bearer除外）。GET session返回csrf_token。
- 初始管理员仅服务端bootstrap：`PANEL_ADMIN_USER`、`PANEL_ADMIN_PASSWORD`，只在用户表为空时创建；不公开无保护的bootstrap HTTP接口。不提供默认密码。
- 变更需校验JSON/字段/关系；客户端版本通过body.version；批量submit需要Idempotency-Key。验证错误400，未登录401，CSRF/权限403，不存在404，版本/幂等冲突409，依赖不可用503。
- 六个管理核心集合、七个运维集合及本人 nodes/sessions/usage 已实现 keyset 分页：默认 limit=50，整数夹取1..200；有后续才给不透明 next_cursor，否则 null。cursor 绑定端点及有效过滤（运维另绑定管理员，本人列表绑定本人），改过滤必须从第一页开始；核心 nodes/members 与运维 events 支持各自作用域的 q，不提供全接口通用搜索。稳定排序、过滤表与400字段错误详见 [分页契约](../reference/api.md#9-分页检索与对象形状)。
- 已批准但待闭合的差异：管理 usage 仍未接入 cursor，其 limit 错误规则也未统一；不能把这个缺口描述为所有列表未分页，或把单页当全集。见 API 参考 §9.4。

### 公共认证

| 方法 | 路径 | 输入/输出 |
|---|---|---|
| POST | /api/v1/auth/login | username,password → data:{user,csrf_token} + Cookie |
| GET | /api/v1/auth/session | data:{user,csrf_token} |
| POST | /api/v1/auth/logout | 撤销当前会话 |
| POST | /api/v1/auth/redeem | code,username,password → 创建被邀请成员，不授予管理员 |
| GET | /healthz | 进程活性，不泄漏凭据 |
| GET | /readyz | PG可用性；Redis故障标降级不伪造PG健康 |

### 管理资源

前缀 `/api/v1/admin`。以下集合支持GET列表、POST创建；`/{id}`支持GET、PATCH、DELETE（不能统称归档：模板/节点组物理删，方案按历史决定退役或物理删），无权操作必须后端拒绝。DELETE 不要求 version，兼容调用者携带但不校验该参数。PATCH/PUT **必须**传 `version`（缺失或 0 → 400 `VALIDATION_FAILED`），版本不符 → 409，返回更新后对象。

| 集合 | 对象可写字段（通用id/version/created_at/updated_at由服务端） |
|---|---|
| /servers | name,address,region,notes,expires_at(nullable),cost_minor,currency |
| /nodes | name,server_id,enabled,host,port,**external_port**(nullable=与port相同),listen,protocol,flow,cipher,server_key,stream_settings(object),protocol_settings(object),client_settings(object),sort_order |
| /node-groups | name,description,node_ids:string[] |
| /subscriptions | name,description,node_group_ids:string[],quota_bytes(nullable=不限量),timezone,anchor_day,default_valid_days(nullable),allow_new_bindings,access_enabled,archived,template_id(nullable) |
| /members | username,password(仅写),role(admin/member),active,proxy_enabled,subscription_id(nullable),expires_at(nullable),quota_override_bytes(nullable),quota_override_enabled |
| /templates | name,format(xray/mihomo/singbox/v2rayn),content(object),description |

成员输出禁止password/hash；附used_bytes、effective_quota_bytes、period_start、period_end、access_reasons:string[]等派生值。Node GET不默认给成员暴露server_key/服务端privateKey；管理员编辑可读取必要配置，但日志脱敏。

Node协议配置：stream_settings为Xray原生streamSettings（network,security,tlsSettings,realitySettings,sockopt等）；protocol_settings为入站settings的除clients外字段（VLESS包括decryption/fallbacks等），clients由后端注入，不能让管理员绕过授权设置共享clients。client_settings是该节点面向客户端的非秘密参数（serverName/publicKey/shortId/fingerprint/alpn等）。flow单独字段。后端不得把服务端私钥塞进成员配置。未知结构必须校验/明确报错，而不是被JSON解码静默丢弃。

| 方法 | 路径 | 输入/输出与行为 |
|---|---|---|
| GET | /overview | data:{counts:{members,servers,nodes,subscriptions},usage_bytes,agents_online,pending_tasks,recent_events:[],setup} |
| GET | /upgrade-readiness | data:store.UpgradeReadiness；只读 ready/blocked/unknown，HTTP不接收任意材料路径；真实字段见 API 参考 §11 |
| POST | /servers/{id}/registration-codes | data:{code,expires_at} 一次性代码，15分钟 |
| GET | /agents | 脱敏Agent列表及心跳/修订 |
| GET | /core-instances | 实例与节点/回执状态 |
| POST | /members/{id}/subscription-token/rotations | data:{token,url}，旧令牌失效不撤代理 |
| POST | /members/{id}/credentials/rotations | scope:all_nodes/single_node,node_id可选；硬撤权旧凭据 |
| POST | /members/{id}/quota-adjustments | bytes(有符号),reason必填；修改当期额度不改原始用量 |
| POST/GET | /invitations | 创建(subscription_id,expires_at)，返回code一次；列表不泄漏code |
| POST | /invitations/{id}/revoke | 撤销未兑换邀请 |
| POST | /batches/preview | operation:assign_subscription/set_active/set_expiry/clear_override；member_ids:[]；values:{subscription_id/active/expires_at} → data:{id,version,affected_count,affected_nodes,exceptions:[],warnings:[],expires_at} |
| POST | /batches | preview_id,confirm:true + Idempotency-Key → data:{id,state,results:[]}，事务修改与记录任务 |
| GET | /batches/{id} | data:批量任务和逐对象执行状态 |
| GET | /usage | member_id/node_id/from/to可选；items为按周期/节点统计，含estimated |
| GET | /events | 事件列表 |
| POST | /events/{id}/ack | 确认告警 |
| GET | /tasks | 任务列表 |
| POST | /tasks/{id}/retry | 限制白名单、幂等重试 |
| GET | /audit-logs | 脱敏审计 |
| GET/PUT | /settings | timezone,retention_days,telegram_enabled,telegram_chat_id,telegram_token(仅写且返回是否已设置),template defaults |
| GET | /recovery-mode | 读取实际 recovery_mode、control_generation 与修订状态 |
| POST | /recovery-mode/enter | 进入恢复协调：冻结普通管理员授权写及成员令牌/凭据轮换（409 RECOVERY_MODE），轮换 `control_generation`；login/logout、本人改密及撤销本人其他会话为救援例外，保留身份/CSRF等校验 |
| POST | /recovery-mode/exit | 必须 confirm="verified"、agent_revisions_checked=true、ledger_checked=true；仅显式核对通过后退出，各 Agent 仍需本地接受新generation，不盲目放行旧权限 |

### DELETE 行为变更（批准契约，修复合入后待验证）

- 服务方案：同事务锁方案；有未归档绑定成员 → 409 NODE_IN_USE；无当前绑定但有 member_bindings 历史 → archived=true，归档快照带 retired=true，保留绑定/账本/节点组关联；完全无绑定历史 → 物理删除。已归档重复删除仍先检查绑定，无绑定则幂等返回归档态。
- 模板：有未归档方案引用 → 409 CONFLICT，details.reason=template_in_use 并带引用数；否则物理删，只有归档方案的引用允许由外键清空。不能静默清空未归档方案模板。
- 基线 6d1345c 尚无这两处目标分支；本轮仅同步文档，不宣称已验证。最终成功快照包装与 details 引用计数字段待主负责人在 DELETE 合入后核对，见 [API DELETE 契约](../reference/api.md#10-两个-delete-的确定契约行为变更)。

订阅/节点组对已有成员的影响编辑也需预览：允许 /batches/preview operation=update_subscription/update_node_group，values包含id/version/patch；submit同上。直接PATCH若影响现有授权，应拒绝并返回PREVIEW_REQUIRED，前端转预览流程。

### 成员端

`GET /api/v1/me/status` 返回个人有效权限/绑定/用量；`GET /api/v1/me/nodes`、`GET /api/v1/me/usage`、`GET /api/v1/me/subscription`（data:{url,token,formats:[...]};凭据仅本人受控读取）；`POST /api/v1/me/subscription/token/rotations`；`POST /api/v1/me/credentials/rotations`；`POST /api/v1/me/password` 输入current_password,new_password；`GET /api/v1/me/sessions`；`DELETE /api/v1/me/sessions/{id}`，严格自身范围且仅允许撤销非当前会话；当前会话 DELETE 返回400 VALIDATION_FAILED且不撤销，必须用 logout。恢复期间上述救援操作可用。

`/me/usage` 保留 `{"items":{"usage":[],"adjustments":[]},"next_cursor":null}` 对象形状；分页只推进 usage，adjustments 第一页全量、后续为[]。完整周期摘要取 /me/status，不累加当前页代替。状态展示必须区别 known（完整证据）、unknown（data_complete=false / proxy_access=unknown）、unlimited（完整证据且 effective_quota_bytes=null）、zero（数字0，不是缺失）。这些解释状态不是新增统一枚举字段。

恢复期间 `/me/subscription` 有既存 token 时只读返回；无 token 时创建被409 RECOVERY_MODE拒绝，只在链接/导入区局部提示，status/usage仍可读取。

公共 `GET /sub/{token}?format=xray|mihomo|singbox|v2rayn` 返回对应配置，Cache-Control:no-store；每次校验令牌与权限。完全无权或过期返回403，不输出陈旧缓存。不能表示的协议组合返回422并指出节点名/不兼容字段（无密钥）。xray是原生客户端JSON；v2rayn为标准节点URI文本（可选base64参数），两者不能混称格式完全相同。

## Agent API（权威类型 internal/contracts/agent.go）

- `POST /api/v1/agent/register` RegistrationRequest → data:RegistrationResponse，仅一次性注册码；之后Bearer独立token。
- `GET /api/v1/agent/snapshot?revision=N&control_generation=G` → data:Snapshot；仅客户端 revision 与 generation 均一致才返回204（不能以数据库保存的 applied_revision 代替客户端当前状态）；新generation或新版本即使instances=[]也必须200完整快照。恢复模式先检查并返回409。Agent只看到本server配置，不允许由query参数选择别人的ID。
- `POST /api/v1/agent/applied` ApplyReport → data:{accepted:true}。
- `POST /api/v1/agent/usage` UsageBatch → data:UsageAck，accepted/duplicate只有PG持久提交后返回；同batch不同body返回409 BATCH_PAYLOAD_MISMATCH。
- `POST /api/v1/agent/heartbeat` Heartbeat → data:{accepted:true}。
- 所有JSON成功响应包裹data，204不解析JSON；所有非2xx明确失败。
- Snapshot.expected_user_count是instances[].users长度之和（同一成员在不同节点分别计数）；客户端必须校验schema、generation、revision、空数组和总数。
- Instance.Config为完整声明式Xray JSON但不含管理API。节点配置由面板生成，Agent验证仅允许预期的入站/出站与受控证书路径；自动加入本机随机管理端口、stats/policy/routing。独立实例ID等于node_id，不允许任意路径。
- Instance.Users.Email与inbound clients.email匹配，使用member_id稳定映射；每个member×node独立UUID/password。过期成员本地过滤并重启实例，不能因离线让过期用户继续。
- Agent修订变化不是盲目重启全部：比较每实例有效配置哈希，只重启有变更实例；删除实例必须停止旧进程；失败不推进完整applied_revision。
- 核心重启前尽可能采集末次用量。计数器来自本机 StatsService，调用固定xray CLI api statsquery（非清零）或受控gRPC；不依赖代理访问日志计数。
- 新控制generation必须进入安全协调，不能自动相信备份恢复后的旧授权。
- Agent本地事务存储是独占状态目录SQLite，不直接连PG/Redis。采样基线+队列同事务；可靠重发；队列上限/磁盘错误停受影响实例。日志不输出完整config/token。

## 执行边界

- 面板后端可以使用PG规范化表+JSONB保留扩展字段，不可将全部业务仅保存在内存。
- 统计、会话、授权、注册、幂等/任务必须可恢复且有测试；不能用假数据接口凑齐页面。
- 前端所有页面只展示真实API结果；未支持能力如实说明，不给假成功反馈。
- 优先保持本契约一致；无法完整实现的具体条目必须报告，不自行删除目标或宣称全部完成。
