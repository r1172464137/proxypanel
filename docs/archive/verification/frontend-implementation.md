# 前端实现报告（Vue 3 + TypeScript + Vite）

> 最终集成更新：以 [统一验收记录](integration.md) 为当前状态。批量路由、PATCH null 三态、usage 时间过滤、恢复 GET/退出双确认、成员派生值等后续已接入；原第 7.3 节前六项为历史联调发现，不是当前待实现清单。真实无 mock 浏览器已覆盖 12 管理页、5 成员页及 390px 移动视口、界面建服务器和权限拒绝，无 pageerror；不是全部断点/错误状态视觉验收。65 测试、typecheck、build 通过；Vitest 5.0.5 定向升级后 npm audit 当轮为 0 漏洞。


- 范围：`web/`（管理员端 + 成员端）、`docs/archive/verification/frontend-implementation.md`
- 依据：`docs/archive/implementation-contract.md`（冻结契约，字段/端点权威）、`docs/archive/product.md`、主计划 `§3 F01–F12`
- 后端现状：`internal/panel` 的管理端/成员端 API 已落地（`internal/panel/api/api.go` 路由表）。本工程按契约实现，并在落地后**逐条比对**了路由与 JSON 字段（结果见 §7）。本次仍**未启动 PostgreSQL/Redis/面板进程做真实登录往返**：后端不可达时页面显示明确的网络/错误状态，不出现假数据或假成功。开发代理已按约定指向 `127.0.0.1:8080`。

## 1. 交付内容

### 1.1 工程与基础设施

| 文件 | 说明 |
| --- | --- |
| `web/package.json` | Vue 3.5 / vue-router 4 / qrcode；devDeps: vite、vitest、vue-tsc、@vue/test-utils、jsdom |
| `web/vite.config.ts` | `/api`、`/sub` 代理到 `PANEL_DEV_TARGET`（默认 `http://127.0.0.1:8080`）；Vitest 配置 |
| `web/tsconfig.json` | strict、`noUnusedLocals/Parameters`、`verbatimModuleSyntax`、`@/*` 别名 |
| `web/index.html` | 中文 lang、无外部字体/脚本、`referrer=no-referrer` |
| `web/src/api/http.ts` | `{data}`/`{items,next_cursor}` 信封解析、错误规范化、`X-CSRF-Token`、`Idempotency-Key`、401 全局登出、非契约响应防护 |
| `web/src/api/endpoints.ts` | 65 个端点封装，路径全部取自契约，并与 `api.go` 路由逐条一致 |
| `web/src/api/types.ts` | 契约资源类型 + 枚举标签；已对齐 `internal/panel/domain/types.go`；未知字段保留 |
| `web/src/stores/session.ts` | 会话/CSRF、登录/登出/兑换、角色首页 |
| `web/src/stores/theme.ts` | 浅色/深色主题切换（localStorage，默认跟随系统） |
| `web/src/router/index.ts` | 登录/兑换、管理员路由守卫、成员路由、404、标题 |
| `web/src/lib/*` | 格式化、JSON 保留式编辑、错误文案、批量状态机、节点/订阅/成员表单、游标分页、toast、剪贴板 |
| `web/src/styles/tokens.css`、`base.css` | 设计令牌（间距/字体/色板/状态色）、深色侧栏 + 内容区主题、移动端断点 |

### 1.2 页面（真实请求）

管理员（17）：

- `OverviewView` `/admin/overview`：`GET /overview` 实时聚合（成员/服务器/节点/订阅、当期用量、在线 Agent、待处理任务、近期事件）；无数据时显示“尚未添加服务器”主行动；刷新按钮 + loading/错误/重试。
- `ServersView` `/admin/servers`：CRUD（`POST/PATCH/DELETE /servers`，PATCH/DELETE 带 version）、到期/成本/货币；`POST /servers/{id}/registration-codes` 一次性注册码弹窗 + 复制 + 过期时间。
- `ServerRuntimeView` `/admin/servers/:id/runtime`：`GET /agents`、`GET /core-instances`；Agent 在线/心跳/授权版本与核心实例状态分层展示；字段缺失时明示“服务端未提供”，并提示“Agent 在线 ≠ 代理可用”；可生成注册码。
- `NodesView` `/admin/nodes`：列表/分页/删除（NODE_IN_USE 等错误原样展示）。
- `NodeEditorView` `/admin/nodes/:id`、`/admin/nodes/new`：重点实现（见 §2）。
- `NodeGroupsView` `/admin/node-groups`：CRUD；成员调整影响现有授权时捕获 `PREVIEW_REQUIRED` 并转入影响预览流程。
- `SubscriptionsView` `/admin/subscriptions`：列表展示节点组数、每人配额（null=不限量）、月周期/时区、允许新绑定、访问状态、归档状态、绑定人数。
- `SubscriptionEditorView` `/admin/subscriptions/:id|new`：节点组多选、GiB 配额或不限量（`quota_bytes=null`）、`anchor_day` 1–31（含月末裁剪说明）、IANA 时区校验、默认有效期（可空）、`allow_new_bindings` 与 `access_enabled` 分离、`archived`、模板；影响现有成员的 PATCH 被 409 拒绝后进入 `update_subscription` 预览提交。
- `MembersView` `/admin/members`：游标分页、当前页过滤、勾选（跨页选择计数明示）、创建/编辑（密码仅写、角色、订阅、有效期、配额覆盖）、停用/启用、代理暂停、凭据轮换（全部/单节点）、订阅令牌轮换（新 URL + 令牌 + 二维码，旧链接失效提示）、删除确认；批量：`assign_subscription / set_active / set_expiry / clear_override` → `POST /batches/preview` → 影响摘要/警告/不可执行项 → 勾选确认 → `POST /batches`（`Idempotency-Key`）→ 跳转批量结果页。
- `MemberDetailView` `/admin/members/:id`：生效值/来源（覆盖 vs 订阅默认）、proxy_access/access_reasons、已用/额度、周期（业务时区）、原始响应；操作：启停、代理暂停/恢复、额度调整（`bytes` 有符号 + 必填原因）、凭据轮换、订阅令牌轮换。
- `InvitationsView` `/admin/invitations`：创建（可选订阅与过期时间）→ 只展示一次 code + 复制；撤销未兑换邀请；状态/使用次数徽标。
- `BatchDetailView` `/admin/batches/:id`：state、汇总计数、逐对象（成员×节点×Agent）状态/错误/回执时间；明确“只有 succeeded 且版本匹配才算生效”，重试引导到任务页。
- `UsageView` `/admin/usage`：成员/节点/时间过滤、上下行、周期与时区、estimated/跨周期分摊/更正标记、计量方向与算法版本、网卡流量单列（不参与配额）、加载更多、当前结果 CSV 导出（客户端生成，无伪造数据）。
- `EventsView` `/admin/events`：事件列表、级别过滤、确认告警（`POST /events/{id}/ack`）、原始字段展开。
- `TasksView` `/admin/tasks`：任务列表、失败/超时重试确认（`POST /tasks/{id}/retry`）、10 秒自动刷新开关、回执详情。
- `AuditView` `/admin/audit`：脱敏审计列表（`GET /audit-logs`）。
- `SettingsView` `/admin/settings`：`GET/PUT /settings`（时区、保留天数、Telegram 开关/chat_id/只写 token + “已配置”状态、版本号）、模板默认值（`template_defaults` 存在才编辑）、客户端模板 CRUD（`/templates`，content 原生 JSON）、恢复协调模式进出（危险操作二次确认 + 影响说明；退出固定发送 `confirm=verified`）。

成员（5）：

- `MeSubscriptionView` `/me/subscription`：订阅链接脱敏复制/二维码/打开下载、格式选择（仅 `formats` 声明项可选，未声明项禁用并标注）、`校验该格式` 直接请求 `/sub/{token}?format=` 并展示 422/错误消息（不展示配置正文）、格式能力不兼容明确报错、授权节点列表（传输/安全）、额度/周期摘要；无订阅时说明“可登录但无代理权限”。
- `MeUsageView` `/me/usage`：已用/额度/剩余/重置时间、周期与业务时区、按节点明细、estimated/跨周期标记、额度调整记录。
- `MeCredentialsView` `/me/credentials`：订阅令牌与链接（脱敏）、轮换令牌（不撤代理）、轮换代理凭据（全部/单节点，硬撤权警告）。
- `MeStatusView` `/me/status`：`member` + `access` 结构：账户状态/代理访问/原因/到期/额度/周期/判定链/原始字段。
- `MeSecurityView` `/me/security`：修改密码（当前/新/确认，≥10 位）、会话列表与撤销（撤销当前会话即退出）。
- 登录 `LoginView`、邀请兑换 `RedeemView`、404：不区分账号存在性、限流提示、兑换后自动登录。

### 1.3 通用部件（14 个组件）

`AppShell`（深色侧栏、分组导航、移动抽屉、主题切换、退出）、`PageHeader`、`StatusBadge`、`EmptyState`、`LoadingBlock`（骨架）、`ErrorState`（标题/服务端消息/提示/错误码/请求号/重试）、`AppModal`、`ConfirmDialog`（影响说明 + 二次确认）、`ImpactPreviewDialog`（自动预览 → 影响数据 → 勾选确认 → 幂等提交 → 跳转）、`CopyField`（默认脱敏/显示/复制，失败不报成功）、`QrPanel`（qrcode 生成失败明确报错）、`JsonEditor`（解析失败阻止保存）、`ToastHost`（仅服务端确认成功后调用）、`ThemeToggle`。

## 2. 节点编辑（VLESS 重点）

- 结构化表单与原生 JSON 编辑**同一份对象**：
  - stream：`network`、`security`、TLS（`tlsSettings.serverName/alpn/certificates[0].certificateFile|keyFile`）、REALITY（`dest/serverNames/privateKey/shortIds`、`settings.publicKey/fingerprint/spiderX`）。
  - protocol：VLESS `decryption`、`fallbacks`（JSON 数组）；VMess `disableInsecureEncryption`；Trojan `fallbacks`；Shadowsocks `method/password/network`（方法含 `2022-blake3-*`，`cipher` 与 `settings.method` 同步）。
  - client：`serverName/publicKey/shortId/fingerprint/alpn/allowInsecure`。
- 写入使用路径合并（`src/lib/json.ts#setPath`）：只改已知路径，`sockopt`、`wsSettings` 等未识别键**原样保留**，并在界面列出“将原样保留的键”。
- 切换 `security` 不隐式删除另一分支配置；清理 `tlsSettings/realitySettings` 需显式点击。
- `protocol_settings.clients` 提交前移除并给出警告（客户端凭据只能由后端注入）。
- `fallbacks` 非数组、端口越界、REALITY 缺 `dest/serverNames/privateKey`、REALITY 用于非 VLESS 等阻止保存；TLS 证书路径缺失、SS 密钥为空等作为需确认警告。
- `server_key` 为密码输入，只写；服务端只返回 `server_key_set`，编辑时默认“保持服务端现有密钥不变”（不提交该字段）；`flow/cipher` 用空字符串清空（后端 `*string` 中 null 表示不修改）。
- 高级编辑器 JSON 解析失败时禁止保存并切到高级页提示，避免静默丢参数。
- 保存提示明确：配置变更默认重启该独立核心实例，连接会短暂中断，生效以任务回执为准。

## 3. 测试

`npm run test`（Vitest，9 个文件 / 65 个用例）：

| 文件 | 覆盖 |
| --- | --- |
| `src/api/http.spec.ts` | `data` 解包；写请求带 `X-CSRF-Token` 而 GET 不带；公共端点（登录）不带；`Idempotency-Key`；错误信封 → `ApiError(code/message/details/retryable/request_id)`；HTML 错误页不回显；网络失败 → `NETWORK_ERROR`；204；401 触发全局登出而登录失败不触发；列表 `items/next_cursor` 与结构错误 |
| `src/api/endpoints.spec.ts` | 端点路径/方法与后端一致；`/me/usage` 的 `{usage, adjustments}` 信封归一化；恢复模式退出携带 `confirm=verified` 与 CSRF；`PUT /settings` 携带 `version` 与 `template_defaults`；批量预览提交显式成员集合；登录为公共端点不带 CSRF |
| `src/lib/nodeDraft.spec.ts` | 结构化读取；`applyStreamView` 只写已知路径、保留 `sockopt`/REALITY、显式清理；`applyProtocolView`/`applyClientView` 保留未知键；`buildNodeWrite` 完整提交三对象并剥离 `clients`；`omitServerKey`；端口/必填/REALITY/VLESS/SS2022/fallbacks 校验与警告 |
| `src/lib/batch.spec.ts` | 预览前置条件；预览后需确认；集合/参数变化 → 需重新预览；成员顺序无关指纹；预览过期拒绝；提交体 `{preview_id, confirm:true}`；幂等键唯一；`PREVIEW_REQUIRED` 判定；摘要不宣称“已生效” |
| `src/lib/subscriptionDraft.spec.ts` | 不限量 → `quota_bytes=null`；GiB→字节；`anchor_day` 1–31；无效时区；默认有效期空 → null；允许新绑定与允许访问独立；暂停访问硬撤权警告；回填 |
| `src/lib/memberDraft.spec.ts` | 创建密码 ≥10；编辑留空不提交 password；覆盖关闭 → null、启用需数值；用户名规则；有效期本地→UTC；无订阅警告；额度来源（含列表未提供 → unknown） |
| `src/lib/format.spec.ts` | 1024 基数字节、GiB 往返、不限量/未提供区分、业务时区展示、无效时区不猜测、datetime-local 往返 |
| `src/lib/json.spec.ts` | `setPath` 保留未知键/删除键、`getPath` 安全、JSON 解析与中文错误、`unknownKeys` |
| `src/components/ErrorState.spec.ts` | 服务端消息按纯文本渲染（`<img onerror>` 不成为标签）、错误码/请求号展示、可重试才显示重试按钮且点击发出事件、中文提示 |

安全渲染：全仓库无 `v-html` 指令（`Grep 'v-html='` 为 0），错误/审计/事件 JSON 一律 `<pre>{{ }}</pre>`。

## 4. 验证命令（实际运行）

环境：Node v24.22.0 / npm 11.19.1，先 `. .\scripts\Use-DevEnvironment.ps1`（npm 缓存 `E:\SDK\Caches\proxypanel\npm`）。

| 命令 | 结果 |
| --- | --- |
| `npm run typecheck`（vue-tsc --noEmit，strict） | 通过，exit 0 |
| `npm run test`（vitest run） | 通过：9 文件 / 65 用例，exit 0 |
| `npm run build`（vue-tsc + vite build） | 通过，exit 0；产物 `web/dist`，入口 125.9 kB（gzip 48.4 kB），二维码独立 chunk 26.5 kB |
| 静态托管冒烟 | `vite preview --host 127.0.0.1 --port 4173`：`/` = 200，`/admin/overview`（SPA 回退）= 200 |

说明：`npm install` 时 npm 11 的 allowScripts 策略跳过了 `esbuild` postinstall 警告，但 esbuild 平台二进制来自 optional dependency，`vite build` 实际成功（未影响构建）。

## 5. 未完成 / 未验证项

1. **未做真实联调**：`internal/panel` 代码已存在，但本次未启动 PostgreSQL/Redis/面板进程，未验证真实登录、CSRF、订阅输出与 Agent 回执；页面在后端不可达时只显示明确错误。
2. **成员端 M5 使用说明**未实现：需依赖 D1 客户端/协议能力矩阵（product §13.2 未确认），不做猜测性说明。
3. **批量列表页**未实现：契约只有 `GET /batches/{id}`，无列表端点；批量入口在成员页/订阅编辑页，结果页按 ID 直达。
4. **批量重试**只走 `POST /tasks/{id}/retry`（契约）；api-proposal 的 `POST /batches/{id}/retry` 未在冻结契约中，且当前后端未挂载批量端点（§7.3）。
5. **恢复模式状态**无法读取（只有 enter/exit），页面不显示当前是否处于恢复模式；AC-OBS 需要的常驻横幅缺后端字段。
6. **成员端每节点凭据明文**未提供：契约 `/me/**` 无 `GET /me/credentials`（api-proposal 有），成员页不展示 UUID/密码，只管理订阅令牌与轮换。
7. **服务器重启/核心实例启停**未实现为按钮：契约无 `POST /core-instances/{id}/restart`（仅 api-proposal），运行时页只读展示。
8. **订阅绑定历史**未展示：契约无 `GET /members/{id}/binding?include_history`（api-proposal 有）。
9. 响应式：320/768/1440 走 CSS 断点（侧栏抽屉、表格横向滚动、卡片网格），未做真实浏览器截图验收（AC-OBS-06 未执行）。
10. **登录后管理员进成员端**：管理员访问 `/me/*` 使用自身权限（后端按自身范围校验），未额外模拟他人视图。

## 6. 接口疑问（契约层，仍需主负责人确认）

1. `agents` / `core-instances` / `events` / `tasks` / `audit-logs`（契约 61–62、71–76 行）未列字段；本工程已按 `internal/panel/domain/types.go` 实际字段对齐（§7），建议把实际字段回写契约。
2. `settings` 的 “template defaults”（77 行）字段名已确认是 `template_defaults`；`PUT` 还需 `version`（契约未写），见 §7.2。
3. DELETE 语义：契约只写“PATCH 传 version”。前端 `DELETE` 在 JSON body 传 `{version}`；后端当前忽略版本，请确认最终语义（`If-Match` 或无需版本）。
4. 影响预览 `update_subscription` / `update_node_group`（81 行）的 `values` 形状仅文字“id/version/patch”。前端提交 `values={id, version, patch}` 且 `member_ids: []`；后端端点尚未挂载，需一并确认。
5. `PREVIEW_REQUIRED` 未出现在 api-proposal 错误码表（契约 81 行提及）。前端识别 `code==='PREVIEW_REQUIRED'` 或 `details.preview_required===true`；后端 `domain.CodePreviewRequired` 已存在，建议补入错误码表。
6. 成员输出派生值（53 行）未说明是否出现在**列表**响应；实际 `ListMembers` 不含派生值，列表页降级显示“未提供”。
7. 契约字段 `allow_new_bindings` / `access_enabled`（49 行）与 product §6.3 的 `allow_new_binding` / `access_paused` 命名不一致；前端与后端均使用契约命名。
8. `/batches` 提交响应：契约 `data:{id,state,results}`，api-proposal 用 `batch_id/status/dispatch`；前端按契约取 `id`。
9. `/sub` 支持 `v2rayn`，但模板格式只有 `xray/mihomo/singbox`（51 行）。v2rayN 是否允许无模板、模板 `content` 结构为何，需要冻结。
10. `me/status`、`me/usage`、`me/subscription`（85 行）只给出概要；实际形状见 §7.2，建议冻结并写回契约（尤其 `/me/usage` 的 `{usage, adjustments}` 信封与列表契约不一致）。
11. `Node` 管理员 GET 是否返回 `server_key`/REALITY 私钥：已确认只返回 `server_key_set`（`domain.Node.ServerKey` 为 `json:"-"`），前端据此处理。
12. 注册码路径：契约 `/servers/{id}/registration-codes`，api-proposal 为 `/servers/{id}/agents/registration-codes`；后端实现与契约一致。
13. `/usage` 的 `from/to` 时间格式与语义（采样区间还是接收时间）未定义，且后端当前忽略 `from/to`（§7.3）。
14. `/members/{id}/quota-adjustments` 的 `bytes` 有符号（65 行）无上下限与“调整后低于已用量是否允许”说明；前端要求非零 + 必填原因，超额撤权交服务端判定。
15. `templates` 的 `PATCH/DELETE /templates/{id}`：后端已提供，前端按契约实现；确认无额外归档约束即可。
16. 无 CSRF 端点仅登录/邀请兑换/Agent（契约 24 行）。后端 logout 也要求 CSRF，前端携带；登录/兑换不携带。

## 7. 与后端实现（`internal/panel`）的核对结果

核对方式：只读比对 `internal/panel/api/api.go` 路由表、`internal/panel/domain/types.go` 结构体 json tag 与各 handler 的请求结构；未修改后端任何文件。

### 7.1 已确认一致

- 路由：auth / me / admin 全部端点（含 `/sub/{token}`、`/servers/{id}/registration-codes`、`/members/{id}/subscription-token/rotations`、`/members/{id}/credentials/rotations`、`/events/{id}/ack`、`/tasks/{id}/retry`、`/recovery-mode/enter|exit`、`/templates` CRUD）与 `src/api/endpoints.ts` 完全一致。
- 登录/会话响应 `{user, csrf_token}`；写接口统一要求 `X-CSRF-Token`（登录/兑换/Agent 除外）。
- 节点读取只给 `server_key_set`，不返回明文 `server_key`；前端默认“保持现有密钥”，并显示“服务端已保存：是/否”。
- 成员创建/更新字段名、额度调整 `{bytes, reason}`、凭据轮换 `{scope, node_id}`、订阅字段名（`allow_new_bindings`、`access_enabled`、`quota_bytes`、`anchor_day`、`default_valid_days`、`template_id`）均一致。

### 7.2 发现不一致并在前端修正

| 项 | 后端实际 | 前端处理 |
| --- | --- | --- |
| `PUT /settings` | 必须回传 `version`（int64） | 保存体带 `version`；页面显示当前版本 |
| `POST /recovery-mode/exit` | 必须 `{"confirm":"verified"}` | 端点固定发送该确认 |
| `/admin/usage` | 平铺 `period_start/period_end/estimated/boundary_split/correction/direction/algorithm_version`，无 `nic_counters` | 兼容平铺与 `period` 对象；网卡缺字段时明确“服务端未返回网卡计数” |
| `/me/status` | `{member, access:{proxy_access, access_reasons, access_chain, effective_quota_bytes, used_bytes, quota_source, allowed_node_ids, algorithm_version}, nodes_allowed}` | 状态/额度页读取 `member`/`access` 并展示判定链 |
| `/me/usage` | `items[0] = {usage:[...], adjustments:[...]}` | 端点层归一化为 `{items, adjustments}`；成员用量页展示调整记录 |
| `/me/nodes` | `id/name/protocol/host/port/network/security/flow`，无 `enabled` | 不再臆测启用状态，改为展示传输/安全 |
| `/events` | `kind/severity/subject_type/subject_id/message/state/acked_at` | 按实际字段渲染与确认 |
| `/tasks` | `state/agent_id/last_error/attempts/max_attempts/updated_at` | 按 `state` 过滤/重试，错误取 `last_error` |
| `/audit-logs` | `actor_username/detail/ip/request_id` | 审计页改用 `actor_username/detail` |
| `/core-instances` | `state/node_name/last_error/config_revision` | 运行时页改用对应字段 |
| `/invitations` | `state/max_uses/used_count` | 按 `state` 显示状态与使用次数 |
| 节点 `flow`/`cipher` | PATCH 为 `*string`，`null`=不修改 | 提交空字符串以支持清空 |
| 成员列表派生值 | `ListMembers` 只返回存储字段（不含 `used_bytes/effective_quota_bytes/access_reasons`） | 缺失显示“未提供/列表未提供”，`formatQuota(undefined)` = 未提供，不误显示为“不限量” |

### 7.3 仍需后端/契约处理（前端按契约实现，当前后端会失败或缺失）

1. **批量接口未挂载**：`/api/v1/admin/batches/preview`、`/batches`、`/batches/{id}` 在 `api.go` 无路由（`domain.Batch/BatchItem` 已存在）。前端批量预览/提交与批量结果页会得到 404；契约要求这三个端点，需补实现。
2. **订阅 PATCH 无法“改为不限量/清空模板/清空默认有效期”**：`quota_bytes *int64`、`default_valid_days *int`、`template_id *string` 的 JSON `null` 在 Go 中等于“未提供”，无法与“清除”区分。前端按契约发送 `null`，需后端改为显式字段（如 `clear_quota`/`quota_mode`）或用 `json.RawMessage` 判断。
3. **成员 `expires_at` 无法清空**：PATCH 中 `null` 表示不修改；前端“有效期留空=跟随订阅默认”的语义对已有成员不会生效，需要后端支持显式清空。
4. **`/admin/usage` 的 `from/to` 未实现**：handler 只读 `member_id`、`node_id`；页面已提示“结果可能包含全部周期”。
5. **恢复模式无可读状态**：仅 enter/exit 返回 `ControlState`；没有 GET，页面无法常驻横幅（AC-OBS 相关验收无法在前端完成）。
6. **成员列表派生值缺失**（见 7.2 末行）：建议 `ListMembers` 也计算派生值，否则列表长期降级。
7. **DELETE 忽略 version**：DELETE handler 直接按 ID 删除/归档，不校验请求体版本。前端仍发送 `{version}`（无害），请确认是否改为 `If-Match` 或明确“无需版本”。
8. **`/me/usage` 信封形状**（`items:[{usage, adjustments}]`）与列表契约 `items:[对象]` 不一致，建议改为 `data:{items, adjustments}` 或平铺列表。