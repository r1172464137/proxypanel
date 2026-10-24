# Proxypanel product-readiness 前端技术设计

**生成时间**: 2026-02-18（本轮设计产出，未执行）
**状态**: Draft
**主文档**: `.ai/design/product-readiness.md`（本轮由主会话汇总）
**代码基线**: `/home/compile/Work/proxypanel` `product-readiness` @ `3435f1d`（`/mnt/e/Work/proxypanel` 旧副本禁用）
**契约源**: [`.ai/design/product-readiness/backend.md`](./backend.md) §4（现状核对经 `.ai/design/product-readiness/backend-contract.json`，该文件是派生 handoff 缓存，不是第二设计源）

> 本文档是前端分端设计，以用户体验流为主，页面/组件结构与状态分工只做承载体验所需的最小技术映射。目标、范围与跨端决策以 PRD、用户故事、决策账本与主文档为准。API 契约只引用后端分端设计，不复制字段表。
>
> 技术栈事实（覆盖模板默认的 React/TanStack/Zustand）：本项目为 **Vue 3.5 + Vue Router 4.6 + TypeScript 5.9 + Vite 6 + Tailwind 4 + Reka UI 2.11 + TanStack Vue Table 9**，服务端数据层是既有 `web/src/lib/async.ts` 的 `useResource`/`useAction`/`usePolling` 与 `web/src/lib/paged.ts` 的 `usePagedList`，会话状态在 `web/src/stores/session.ts`，API 封装在 `web/src/api/{http,endpoints,types}.ts`（手写、非生成）。`DEC-product-readiness-009` 要求**新增应用框架数量为 0**：本轮不引入 React、Zustand、TanStack Query 或任何新查询/状态库，只用现有 Vue 能力实现同样的"服务端数据 / UI 草稿"分工。模板中的 TanStack Query / Zustand 章节在本文中按此事实落地，不视为依赖要求。

---

## 1. 目标与范围（前端）

- 承接主文档目标的前端边界：把管理员"接入资源 → 配置服务 → 开通成员 → 跟踪生效 → 处理异常"和成员"加入 → 查看状态 → 导入客户端 → 查看用量 → 保护账号"两条旅程，落成**成熟控制台的默认工作台 + 明确导航 + 真实首次设置 + 真实状态语言**；保留全部高级原生配置入口，且危险影响、不支持与未知状态不得隐瞒。
- 统一表达（`DEC-008`）：UI 用"服务方案"表示授权规则，用"订阅链接"表示成员个人地址；改名只发生在界面，不改路由路径与兼容实体名。
- 不包含：支付/订单/余额/商城类界面与文案；假的在线人数、假的连接已验证、假客服入口、虚构 SLA；新视觉主题系统；多语言；Chrome 扩展/Flutter/小程序界面；后端 DTO 的重定义。
- 前端不自行推导授权：`used_bytes`/`effective_quota_bytes`/`proxy_access`/`allowed_node_ids`/`access_reasons` 一律取服务端判定结果。

## 2. 需求来源与决策追踪

- 需求来源：[PRD](../../prd/core/product-readiness.md) §4–§7、[用户故事](../../user-stories/core/product-readiness.md) US-PR-001…018、[技术预研](../../tech-research/product-readiness.md) §2.2/§5.2、[决策账本](../../decision-log/product-readiness.md)。
- 关键 UX 期望：入口可在导航一步到达；每步有可持续查看的反馈（不靠一闪而过的 toast）；保存≠生效；未知≠零；危险动作名称即影响；失败有就地恢复路径与"核对同一次操作"的路径。

| Decision ID | 状态 | 设计落点 | 说明 |
|---|---|---|---|
| `DEC-product-readiness-001` | Applied | §4、§5.1 全站 | 无支付/交易页与词；"服务方案"不产出付费语义 |
| `DEC-product-readiness-002` | Applied | §4 全节、§9 | 文案与流程一起改：导航重组、首次设置、状态机、异常闭环 |
| `DEC-product-readiness-003` | Applied | §4.1–4.8（管理员）、§4.9–4.11（成员）、§5 | 仅 admin/member 两角色；沿用 `router.beforeEach` 现有角色守卫 |
| `DEC-product-readiness-014` | Applied | §8、§10 | 前端设计与实现分批交付，子代理自述不作验收证据；本设计未执行任何验证 |
| `DEC-product-readiness-005` | Applied | §8 测试矩阵、§10 | 真实浏览器 E2E 承接 US，不用组件 mock 代替完整链路 |
| `DEC-product-readiness-006` | Applied | §8.4、§10 | 前端证据必须指向同一候选版本；旧 `web/tests` 日志不冒充新结果 |
| `DEC-product-readiness-007` | Applied | §4.1（US-PR-001/016）、§8.4 | 面板托管与宿主 nginx 静态托管两种前端归属都要能登录与刷新 |
| `DEC-product-readiness-008` | Applied | §4.4、§4.9、§5 | "服务方案" / "订阅链接" / "重置订阅链接" / "重置连接凭据" 术语分离 |
| `DEC-product-readiness-009` | Applied | §3、§6.2、§7 | 复用 Vue/Router/现有组件体系；无新增框架、无新 Query/状态库 |

## 3. 现有实现分析（前端）

> 只描述现状（`3435f1d`，源码核对）。

- 路由与页面：`web/src/router/index.ts` 定义 29 条路由（`/admin/*` 21 条 + `/me/*` 5 条 + `/login`/`/redeem` 2 条 + 404 1 条），`/` 由 `homeRoute()` 分流（admin → `/admin/overview`，member → `/me/subscription`）。`meta.role='admin'` + `beforeEach` 已实现"成员访问管理页 → `/me/subscription?denied=`"。
- 导航：`web/src/components/AppShell.vue` 内联 `adminNav`/`memberNav`（标题：概览/资源/成员/运行/系统/我的账户），桌面侧栏 + Reka Sheet 抽屉，已有焦点归还、Esc 处理与 `aria-label`。
- 服务端数据层：`useResource`（loading/error/loaded/reload + AbortController）、`useAction`（pending/error/data）、`usePolling`（定时器，卸载停止）、`usePagedList`（`limit+next_cursor`，内存 cursor 历史）。全站 20+ 视图直接用这三者，没有集中缓存层。
- 会话：`web/src/stores/session.ts` 用 `reactive` 保存 `user`/`csrf_token`，`ensureSession()` 单飞；`api/http.ts` 负责信封解包、`X-CSRF-Token`、`Idempotency-Key`、401 全局登出。
- 既有反馈与结构组件：`PageHeader`、`DataTable`（三态 + 行操作）、`EmptyState`、`ErrorState`（`role=alert`）、`LoadingBlock`、`StatusBadge`、`CopyField`、`QrPanel`、`AppModal`、`ConfirmDialog`、`ImpactPreviewDialog`（预览→确认→提交→结果，含 `newIdempotencyKey`）、`JsonEditor`、`ToastHost` + `lib/toast.ts`。
- 领域纯函数：`lib/accessReasons.ts`（`access_reasons` → 用户语言）、`lib/accessSteps.ts`、`lib/batch.ts`（操作元数据、`previewSummary`、`submitRequest`、幂等 key）、`lib/batchConsequences.ts`、`lib/nodeDraft.ts`（结构化 ↔ Xray 原生，`unknownKeys` 保留未知键、`clients` 提交前移除、`server_key` 不返回只给 `server_key_set`）、`lib/subscriptionDraft.ts`、`lib/memberDraft.ts`、`lib/nodeTemplates.ts`、`lib/errors.ts`（码 → 中文 hint）、`lib/format.ts`（字节/配额/时间/`shortId`）。
- `data-testid`：**当前 `web/src` 内为 0 处**，现有 `web/tests/*.mjs` 依赖文本、`role` 与 `banner--danger` 等结构选择器。
- 已核对的现状缺陷（本设计要修的消费侧问题，不是契约破坏）：
  1. `meApi.usage()`（`web/src/api/endpoints.ts`）用 `Array.isArray(payload.items)` 判断，而服务端 `items={usage,adjustments}` 是**对象**，导致成员用量页落到空列表（后端契约 §4.2/§4.3 明确保留对象形状）。
  2. `adminApi.members.list()` / `nodes.list()` 只透传 `limit/cursor`，未传 `q`/`subscription_id`/`include_archived`/`server_id`；`UsageView` 仍读已不存在的 `nic_counters`。
  3. 会员/订阅删除语义变化未在前端体现：`deleteSubscription` 返回 `retired=true`，`SubscriptionsView.vue` 的确认框仍写“将删除订阅”并把按钮叫“删除”；`deleteTemplate` 新增 409 `template_in_use`，而 `TemplatesView.vue` 的成功 toast 仍写“删除会写入审计记录；引用该模板的设置需自行修正”（与新契约“被在用订阅引用时阻止删除”相反，会误导管理员）。
  4. 恢复核对入口埋在 `/admin/settings` 表单区，跨页不可见；`getUpgradeReadiness` 无任何消费方。
  5. `OverviewView` 标题为"一屏看关键数字。这些是面板的即时统计……"的自证式说明，无待办、无首次设置进度。
  6. `MeSubscriptionView` 同时承担状态、导入、链接、二维码、重置，是成员首页认知过载源；`/me/status` 与它重复展示授权链。
  7. 组件层普遍无 `data-testid`，也无持续可见的"同步/结果"承载组件（结果依赖 toast，`lib/toast.ts` 的 `toastRulesSaved` 文案仍是自证式）。

## 4. 用户体验流

> 用户视角描述，不展开技术实现。两条旅程的语言统一为：发生了什么 → 影响什么 → 下一步做什么。

### 4.1 首次部署与启动（US-PR-001，`DEC-007`）

- 入口：部署说明给出的 HTTPS 地址；首次打开是登录页（无"演示登录"、无明文兜底提示）。
- 操作路径（面板托管前端）：访问域名 → 输入账号密码 → 进入工作台。操作路径（宿主 nginx 托管静态前端）：同一个域名下访问静态站点 → 登录 → 工作台；两种方式界面完全一致。
- 系统反馈：登录成功后直接进入工作台；刷新任意页面仍停留在当前页并保持登录（不退回登录页、不 404）。若页面资源与后端版本不匹配（例如静态资源 404），显示"界面资源缺失/版本不一致，请刷新或联系部署人员"，而不是空白页。
- 默认值：登录后落地工作台（管理员）或成员服务首页（成员）；`?next=` 保留原目标页。
- 错误状态与恢复：账号密码错误统一提示"账号或密码不正确"（不区分用户不存在）；来源被拒（403）提示"当前访问地址未被允许，请使用面板配置的地址"；限流（429）提示剩余等待时间并可重试；连接不上后端提示"面板服务不可达"并给出重试按钮，不做"公开明文访问"建议。

### 4.2 首次设置路径（US-PR-002，P1）

- 入口：管理员登录后若工作台显示"尚未交付可用的服务"，顶部出现"继续完成设置"卡片；导航"概览 → 工作台"内常驻"设置进度"区块；相应用例页也有"下一步"入口。
- 操作路径：工作台 → 查看六步进度（接入服务器 / 创建节点 / 组织节点组 / 建立服务方案 / 开通成员 / 观察到真实流量）→ 点未完成步骤进入对应页面 → 完成后返回工作台，进度自动按真实配置刷新。
- 系统反馈：每一步显示"已完成 / 未开始 / 被阻断（原因）"三态，来源是服务端 `setup` 字段；已存在的对象显示为已完成并可直接进入复用（例如已有 1 个服务器时"接入服务器"显示已完成并列出该服务器）。全部完成时卡片收起为一行摘要，不反复打扰。
- 默认值：首次进入不弹模态遮挡工作台，进度以内联区块呈现（用户可直接忽略去用其他功能）。
- 错误状态与恢复：服务端返回"阻断原因"时原样用用户语言解释（如"服务方案尚未关联节点组，成员将无法连接"）并给出修复入口；读取失败时显示"暂时无法获取设置进度"+ 重试，**不显示 0/6 冒充进度**；已有部分配置时不要求重做，也不把示例数据标记为完成。

### 4.3 接入服务器并判断真实状态（US-PR-003）

- 入口：导航"资源 → 服务器" → 服务器详情；工作台待办"有服务器尚未接入"也直达同一详情。
- 操作路径：新建服务器（名称、地址、区域、备注、可选到期与成本）→ 保存 → 在详情点"生成接入注册码"→ 把注册码与安装命令交给部署人员 → 等待"管理程序"上线 → 进入该服务器的运行页查看管理程序与节点状态。
- 系统反馈：保存后显示"已保存服务器。下一步：生成接入注册码"（服务器 ≠ 已接入）；生成注册码后显示一次性码 + 复制按钮 + 过期时间 + "复制注册码不等于接入成功"的说明；接入轮询期间显示"等待管理程序接入（最近检查时间）"；接入后显示真实心跳时间、版本、应用修订号与在线状态。
- 默认值：新服务器默认启用域内可用；到期/成本字段可留空（未填不作"永久"宣称，只显示"未设置"）。
- 错误状态与恢复：注册码失效/已用/过期显示"该注册码不可用，请重新生成"，且不影响服务器档案；恢复核对期间生成注册码被阻止并说明原因；服务器未接入 / 离线 / 状态未知**三种分开表述**（未接入=从未上线；离线=曾经上线但当前无心跳；未知=读取失败），绝不在任一状态下显示"节点可用"；离线时给出"查看运行页诊断"与"重新注册"两个入口。

### 4.4 节点创建、无损编辑与安全换钥（US-PR-004）

- 入口：导航"资源 → 节点" → 新建节点 / 点行进入编辑；服务器详情"下一步：创建节点"。
- 操作路径：选择服务器 → 填名称、协议、地址、端口（对外端口可留空表示同端口）→ 按需要展开"传输与安全"（ws/grpc/httpupgrade/xhttp、tls/reality、REALITY 参数）→ 需要用原始 JSON 时打开"高级原生配置"→ 保存。之后只改名称再保存。
- 系统反馈：保存成功显示"已保存，正在同步"，并就地出现可持续查看的同步状态条（对象、最近状态、"查看任务"链接），**不出现"节点已生效"**；只改名称时明确提示"证书、传输参数与密钥保持不变"；高级原生配置面板显示"未识别的键将原样保存（共 N 处）"，并在切换结构化/原生视图时保留双向内容；多证书场景显示"现有证书条目 N 条，编辑只修改你填写的字段"。
- 默认值：协议 `vless`、端口 `443`、监听 `0.0.0.0`、启用；REALITY 私钥/公钥字段默认显示为"已设置（不显示明文）"，输入框留空＝保持不变；`server_key` 同理（`server_key_set` 只读展示）。
- 错误状态与恢复：字段级错误就地展示（例如"REALITY 缺少目标域名"、"该协议不支持此流控"）；服务端 422（协议不可表达）显示"该组合无法生成客户端配置"，并列出不支持的字段，**不提供"忽略并保存"**。换钥：必须点开"更换 REALITY 密钥"复选框，展示影响（"该节点的客户端配置将变化，使用旧密钥的客户端需要重新导入；已有连接会重连"）+ 配对一致性校验（私钥与公钥必须成对，缺失即禁止保存）；保存后仍只宣称"已保存，正在同步"，不宣称"连接已验证"。编辑冲突（409 `version`）提示"他人已修改该节点"，提供"重新载入最新版本"与"对比我的改动"，不静默覆盖。

### 4.5 建立可复用服务方案（US-PR-005）

- 入口：导航"成员与服务 → 服务方案"（路径仍 `/admin/subscriptions`）→ 新建/编辑；设置页也可作为默认方案的辅助入口。
- 操作路径：填名称与描述 → 选择节点组（可多选）→ 设定每人额度（或"不限量"）→ 设定时区、锚定日、默认有效天数 → 设定是否允许新绑定/是否允许访问 → 可选客户端模板 → 保存。
- 系统反馈：编辑页固定展示"成员将获得什么"摘要句（节点组列表、额度、到期与重置节奏、生效时点），并区分"当前周期规则"与"下一周期规则（`pending_period_rule`）"；改到影响已有成员的内容时，保存前必须经过影响预览对话框（影响人数、受影响节点与 Agent、断线/下周期生效等后果），确认后才提交。
- 默认值：时区 `Asia/Shanghai`、锚定日 1、允许新绑定开、访问开；新建时不预勾任何节点组，避免静默扩大授权。
- 错误状态与恢复：`PREVIEW_REQUIRED` → 直接打开影响预览；`PREVIEW_STALE`/`PREVIEW_CONCLUSION_CHANGED` → 提示"影响已变化"，重新生成预览后再次确认；绑定成员数 > 0 时删除按钮改为"退役方案"，文案说明"保留历史用量与绑定记录，成员不再获得新绑定；已被绑定的成员需先处理"；`retired=true` 的响应显示"方案已退役（历史保留）"，并刷新列表状态。

### 4.6 创建成员与邀请开通（US-PR-006）

- 入口：导航"成员与服务 → 成员" → 新建成员 / 批量导入入口；"邀请"页创建邀请；成员详情内也有"再次邀请"。
- 操作路径：直接开通：填用户名、密码（一次性展示）→ 选择服务方案 → 可选个人到期/额度覆盖 → 保存。邀请开通：在邀请页选择"是否分配服务方案"→ 创建 → 一次性展示邀请码与链接 → 复制给对方；对方在 `/redeem` 用邀请码注册。
- 系统反馈：创建成功显示成员状态摘要（账号状态、服务方案、当前周期、到期、是否已获得节点），并明确"账号已创建；节点同步在后台进行"；邀请创建后显示"已分配服务方案 X / 未分配方案（加入后待开通）"、有效期、剩余次数、"仅显示一次，请现在保存"；邀请列表不显示明文码（现状即如此），只显示状态与使用次数。
- 默认值：邀请"分配方案"默认为当前筛选的方案（若无则空并显式要求选择或选择"仅建账号"）；`max_uses` 默认 1；密码生成器可用。
- 错误状态与恢复：邀请失效/已撤销/已用尽/过期在 `/redeem` 统一提示"邀请不可用，请联系邀请你的管理员"，**不泄露**邀请状态细节与他人信息；无方案邀请注册成功后显示"账号已创建，服务尚未开通，请联系为你开通服务的管理员"；用户名冲突就地提示"该用户名已被占用"；恢复核对期间注册被阻止并说明维护状态；最后一名管理员被撤销（`LAST_ADMIN`）时提示"必须保留至少一名管理员"，不提交。

### 4.7 批量变更：预览 → 提交 → 核对 → 重试（US-PR-010、US-PR-018）

- 入口：成员列表勾选后底部操作条"批量操作（已选 N）"；筛选/搜索并跨页勾选时，操作条持续显示"已选 N 名（含其它页 M 名）"。
- 操作路径：勾选目标（可用搜索 `q`、按方案筛选、含归档开关）→ 选择操作与参数 → 填写变更原因（`reason`）→ 生成预览 → 在预览里核对每人的"变更前/变更后"、后果（断线/下周期生效/失去节点）、被阻断对象及原因 → 输入确认 → 提交 → 进入"变更记录详情"持续查看逐对象结果。
- 系统反馈：预览显示成员数、受影响节点/管理程序数、结论（可提交/需修正），并标明 10 分钟有效期与"成员集合摘要"；提交后显示批次状态（排队/执行中/部分完成/完成/失败）与逐对象行状态；完成不是"一片绿"——部分完成时分别列出"已更新 / 等待 / 失败"计数与对象；**始终**提供"查看本次变更记录"入口，而不是只弹一次成功提示。
- 默认值：操作默认"分配 / 切换服务方案"以外的保守项？实际为：未选择操作时不预选，必须先选；确认框默认不勾选。
- 错误状态与恢复：
  - 提交后网络中断/响应丢失 → 界面进入"结果未确认：正在核对提交结果"，**自动回到同一 preview 的状态查询**，用同一幂等键重试；若回放返回 `idempotent_replay=true`，显示"这是之前那次提交的结果"，不新建变更。
  - `PREVIEW_STALE`（成员集合或规则变化）→ 停止提交，提示"目标范围已变化"，要求重新预览；不拿旧选择范围冒充新结果。
  - 他人并发修改（409）→ 逐对象列出冲突项，提供"仅对未冲突对象重新预览"。
  - 列表/搜索结果变化：任何筛选、搜索、分页、归档开关变动都会让"预览用的成员集合"与当前显示不一致，此时预览按钮要求重新生成，并在确认框内固定展示"本次目标：N 名，摘要 xxxx"。
  - 游标失效（`CURSOR_INVALID`）→ 提示"列表已过期"，回到第一页并清空选择范围，不再提交。

### 4.8 运行监控、异常定位、维护与恢复核对（US-PR-011/012/014/016）

- 入口：导航"运行监控"分组下的"运行状态"（新页 `/admin/runtime`）、"用量"、"事件与告警"、"任务与同步"；工作台"需要处理"列表直达；服务器详情"运行"页按服务器过滤。
- 操作路径（异常）：工作台"需要处理"卡片 → 进入事件/任务/运行对象 → 查看受影响对象、当前影响、最近证据 → 展开"技术详情"看原始码 → 执行处理（重试任务 / 确认事件 / 修复节点配置后重新同步）。
- 系统反馈：运行状态页分区展示四类不同事实——① 管理程序在线与最近心跳；② 核心实例进程与端口；③ 应用修订（applied/desired）与是否落后；④ 恢复核对状态。每区都有"最后更新时间"；落后修订显示"节点尚未应用最新配置"。**没有真实连接验证时不出现"连接已验证"字样**，只用"管理程序在线 / 配置已应用 / 未验证连接"。
- 默认值：事件列表默认只看未确认；运行页默认展示全部服务器汇总。
- 错误状态与恢复：重试任务成功只表示"已重新排队"（显示原任务号、新尝试次数与新修订），不表示节点已好；`REVISION_STALE` 类旧回执在界面标注"旧回执，不计入本次成功"；坏节点失败时显示"该节点未更新，请查看其他节点的实际状态"；仅在各节点证据确实支持时说明其维持运行，不作无证据的“其他节点不受影响”保证。回滚运行显示"正在使用上一份可运行配置（目标变更未完成）"，与"当前目标已生效"分开着色与措辞。
- 恢复核对：进入核对后，全站顶部出现**持久横幅**（非 toast）："恢复核对进行中：普通授权变更、注册、邀请与连接信息重置暂不可用；账号救援仍可用。"横幅在所有管理页可见，并链接到 `/admin/runtime` 的"恢复核对"分页；相关按钮在点击时就地说明"恢复核对中暂不可用"，不解锁也不伪造成功。退出核对需要勾选"已核对各节点应用修订"、"已核对用量账本"并输入确认，退出后展示"未通过核对的节点清单（`unverified_agents`）与提示（`advisories`）"，明确"退出核对 ≠ 节点已全部恢复"。
- 升级预检：`/admin/runtime` → "升级与恢复"分页展示 `getUpgradeReadiness`：状态（可升级 / 已阻断 / 未知）、检查时间、候选版本、迁移已应用/待应用与校验和结果、逐项检查（阻断/警告 × 通过/失败/未知 + 摘要 + 受影响数量 + 证据）、阻断码清单；`blocked` 时明确"先处理下列阻断项再升级"，并提供"仅报告、系统不会自动修改历史数据"的说明；`unknown` 与读取失败（500）分开显示，读取失败给重试，不显示"可升级"。

### 4.9 成员首页与客户端导入（US-PR-007）

- 入口：成员登录后落地 `/me/subscription`（保留原路径，标题改为"我的服务"）；导航"我的"分组第一项。
- 操作路径：首页自上而下：① 一句话状态（"服务可用 / 本周期额度已用完 / 服务已到期 / 服务已暂停 / 尚未开通服务 / 数据暂时无法获取"）；② 额度与时间（已用 / 总额 / 剩余、有效期、下次重置时间与更新时间）；③ 我的节点（名称 + 协议，用于选择客户端）；④ 按客户端导入：选客户端（平台 + 客户端名）→ 复制订阅链接 / 扫码 / 下载配置 → 按步骤导入 → 连接 → 回到页面点"我已连接，检查一下"；⑤ 用量摘要与"查看用量明细"；⑥ 账号入口。
- 系统反馈：每一步都有明确结果——复制成功显示"已复制订阅链接"（失败显示"复制失败，请手动选择文本"并选中输入框）；扫码展示二维码与"二维码仅供本人使用"；下载触发同名 `.json`/`.yaml` 文件下载；"我已连接，检查一下"的回答必须诚实：只做**可达性与状态**核对（订阅链接能否取回配置、成员授权是否仍有效），结果为"配置已取回 / 取回失败（原因）/ 未验证真实连接"，**永远不显示"连接已验证"**。
- 默认值：客户端默认选中平台匹配且格式可用的第一项；订阅格式默认取 `formats` 第一项；导入方式默认"复制链接"。
- 错误状态与恢复：`format_status` 指明某格式不可用（`nodes_empty` / `protocol_unsupported:{field}` / `node_incomplete:{field}`）时，该格式的卡片置灰并给出原因与替代（例如"当前节点使用本格式无法表达的参数，请改用 X 客户端"），**不提供无效链接**；无权限/未开通时导入区整体替换为"尚未开通服务"说明 + "联系为你开通服务的管理员"，并保留账号与用量查看入口；数据不完整（`data_complete=false`）时显示"部分服务资料需核对"并附原因；后端仍返回的已知用量、额度与时间继续按真实数值展示并带核对标记，不能一律改为“—”或 0。只有对应字段确实未知或读取失败时才用“—”并说明原因，遵循 backend.md 的历史守恒与逐字段口径。
- 明确不做：假客服入口、工单系统、假在线人数；未配置联系渠道时统一用"联系为你开通服务的管理员"。

维护降级：`getMeSubscription`因缺令牌而409 `RECOVERY_MODE`时，首页保留`getMeStatus`/用量的独立成功结果，仅导入区显示维护说明与稍后重试；已有令牌只读获取仍可用。恢复横幅只在有管理员恢复状态证据的管理端全局显示；成员不调用管理员接口，收到维护拒绝的动作在本区域提示。没有维护证据时不编造全局状态。

### 4.10 成员用量、期限与不可用原因（US-PR-008）

- 入口：`/me/subscription` 用量摘要"查看明细"→ `/me/usage`；导航"我的用量"；状态异常时首页原因卡"查看原因"→ `/me/status`。
- 操作路径：用量页查看本周期用量构成（上行/下行/合计、估算与修正标记、边界拆分说明）→ 查看额度调整记录（仅第一页展示全量）→ 翻页查看更多明细行。
- 系统反馈：单位一律带"GB/字节"，标注"数据更新时间"；不限量显示"不限量"而不是 0/∞ 混用；未知显示"—"并解释；零用量显示"本周期暂无记录"。到期/重置时间同时显示时区；接近额度时提示"剩余 X，预计本周期结束前用完"（只基于真实速率计算，不预测也不虚构）。
- 默认值：按当前周期（服务端 `period_start`/`period_end`）展示；不默认跨周期合计。
- 错误状态与恢复：超额 → 原因"本周期额度已用完" + 恢复方式"下一周期重置时间 X，或联系管理员调整额度"；到期 → "服务已于 X 到期，请联系管理员续期"；暂停 → "管理员已暂停该服务"；无方案 → "尚未分配服务方案"；不展示内部错误码与权限判定原始数据（技术码收在"诊断详情"折叠区，仅 `/me/status` 提供）。列表读取失败显示错误横幅 + 重试，不显示空表。

### 4.11 账号、会话与连接信息（US-PR-009）

- 入口：导航"我的账户 → 账号安全 / 连接信息"（`/me/security`、`/me/credentials`）；首页状态卡"账号与连接"次级入口。
- 操作路径：改密：输入当前密码与两次新密码 → 提交 → 看到"密码已更新，已退出其他 N 个设备会话"；会话：查看设备/时间列表 → 撤销某个非当前会话；连接信息：“重置订阅链接”（旧链接立刻失效）/“重置连接凭据”（改变实际连接凭据，需选择范围或指定节点）两个动作分开，各自确认框写明影响。
- 系统反馈：动作按钮名即结果（“重置订阅链接”、“重置连接凭据”）；成功后持续展示新链接/说明与"旧链接已失效"，不以 toast 作为唯一反馈；当前会话保持登录，其他会话按说明处理。
- 默认值：连接凭据重置默认"仅针对指定节点"（`scope` 选择），全量重置需要额外勾选确认。
- 错误状态与恢复：当前密码错误就地提示，且**不退出**任何会话；网络中断/"结果未确认"时不显示"已完成"，提供"重新核对（读取当前订阅信息）"入口；恢复核对期间两个重置动作被阻止并解释例外（账号救援仍可用）。

改密认证核对：该动作的401先保留表单并用既有`getSession`只读核对；会话仍有效时就地提示当前密码不正确，不清登录态；会话确已失效时清理会话并进入登录。不能靠解析英文message猜原因，也不能因单动作抑制全局跳转而永远掩盖过期会话。该分支使用既有契约，无新接口。

### 4.12 全局状态、可访问性与移动端（US-PR-015）

- 三态区分：加载（>300ms 才显示骨架/`LoadingBlock`）、空态（说明 + 下一步真实动作）、错误（`ErrorState` + 重试）。三者文案不同、`data-testid` 不同，不用空表格冒充加载失败。
- 权限受限：成员访问管理页时路由重定向到成员首页并显示"该页面仅管理员可用"（保留现有 `?denied=` 机制）；管理页内的数据级 403 显示"当前账号没有权限，此操作需要管理员"并保留浏览能力。
- 键盘与语义：所有操作走原生 `<button>`/`<a>`（现状已遵守）；图标按钮有 `aria-label`；表单控件与 label 关联，placeholder 不作 label；抽屉关闭焦点回到汉堡按钮（现状已实现）。
- 状态不只靠颜色：状态词 + 图标（`StatusBadge` 已有 `status` 文案），新增状态词见 §9.1。
- 移动端：`@media` 断点沿用 Tailwind 现有 `lg`；宽表在 `.table-wrap` 内横向滚动，文档级无横向溢出；关键动作（保存、生成预览、复制链接）在 390px 下可达且不被遮挡。
- 危险动作：名称表达实际效果（"归档服务器"/"退役服务方案"/"重置订阅链接"/"重置连接凭据"）；确认框列出对象与影响；不可撤销的换钥额外二次勾选。

## 5. 页面与组件设计（最小技术映射）

### 5.1 页面/路由清单

导航分组（`AppShell.vue`）：**工作台** → **成员与服务**（成员、服务方案、邀请、批量变更记录）→ **资源**（服务器、节点、节点组、客户端模板）→ **运行监控**（运行状态、用量、事件与告警、任务与同步、审计）→ **系统**（设置、账号安全）→ **我的账户**（成员侧五个入口，管理员也可见）。路径除标注外全部保持现状，旧链接可用。

| 路由/页面 | 承载的体验流 | 用途 |
|---|---|---|
| `/`（redirect → `homeRoute()`） | §4.1 | 保持现有分流 |
| `/login`、`/redeem` | §4.1、§4.6 | 登录、邀请注册（文案重写，无新增路由） |
| `/admin/overview` | §4.2、§4.8 | **工作台**：需要处理 + 设置进度 + 交付进度统计 |
| `/admin/setup`（**新增**） | §4.2 | 首次设置完整路径（六步可展开、可返回修改） |
| `/admin/servers`、`/admin/servers/:id/runtime` | §4.3、§4.8 | 服务器列表/详情；运行页保留（加"返回运行总览"链接） |
| `/admin/nodes`、`/admin/nodes/new`、`/admin/nodes/:id` | §4.4 | 节点列表（服务端搜索/过滤/分页）、结构化 + 原生编辑、换钥 |
| `/admin/node-groups` | §4.4、§4.5 | 节点组（影响预览沿用） |
| `/admin/subscriptions`、`/admin/subscriptions/new`、`/admin/subscriptions/:id` | §4.5 | **服务方案**列表/编辑（路径不变，仅界面术语） |
| `/admin/members`、`/admin/members/:id` | §4.6、§4.7 | 成员列表（搜索/筛选/跨页选择范围）、成员详情 |
| `/admin/invitations` | §4.6 | 邀请（分配方案、一次性码、撤销） |
| `/admin/templates` | §4.5 | 客户端模板（高级；修正删除提示，说明被在用服务方案引用时会被阻止） |
| `/admin/batches`、`/admin/batches/:id` | §4.7 | **变更记录**列表/详情（结果持续可见、幂等回放标注） |
| `/admin/runtime`（**新增**） | §4.8 | 运行状态 / 恢复核对 / 升级与恢复 三个分段（tab） |
| `/admin/usage` | §4.8、§4.10 | 用量查询（游标分页、数据完整性提示） |
| `/admin/events`、`/admin/tasks` | §4.8 | 事件与告警、任务与同步（状态、重试、旧回执标注） |
| `/admin/audit` | §4.8 | 审计（保持，仅统一术语） |
| `/admin/settings` | §4.8 | 面板设置（恢复核对区改为指向 `/admin/runtime` 的链接 + 状态摘要） |
| `/me`（**新增 redirect → `/me/subscription`**） | §4.9 | 成员首页短链接，兼容手工输入的 `/me` |
| `/me/subscription` | §4.9 | **我的服务**（首页：状态 + 额度 + 节点 + 客户端导入 + 用量摘要） |
| `/me/connect`（**新增**） | §4.9 | 订阅链接、二维码、格式可用性、重置订阅链接（从首页下沉的次级页面） |
| `/me/usage` | §4.10 | 用量明细与调整记录（修正对象解包） |
| `/me/status` | §4.10 | 服务状态与原因、数据完整性、诊断详情（折叠） |
| `/me/credentials` | §4.11 | 连接信息（重置订阅链接 / 重置连接凭据分开） |
| `/me/security` | §4.11 | 账号安全（改密、会话撤销） |
| `/:pathMatch(.*)*` | — | 404（保持） |

兼容跳转（保证旧链接可用，不产生死链）：`/admin/batches/*` 标题改"变更记录"但路径不变；`/admin/subscriptions/*` 标题改"服务方案"但路径不变；`/admin/servers/:id/runtime` 保留并在页内提供回 `/admin/runtime`；`/me/credential`→`/me/credentials`、`/admin/invite`→`/admin/invitations`、`/admin/usage-detail`→`/admin/usage`（历史外链兜底，均为 `redirect` 记录，不新增视图）。`/me/subscription` 上原有链接/二维码/重置区迁移到 `/me/connect`，但保留 `#link`/`#qrcode` 锚点重定向到 `/me/connect`，避免旧收藏失效。

### 5.2 组件清单与层级

容器（持有 `useResource`/`usePagedList`/`useAction`，负责状态编排）：

- `views/admin/OverviewView.vue`（**扩展**）——组合待办、设置进度、统计。
  - `components/SetupChecklist.vue`（**新建**）——纯展示：六步状态/原因/入口链接。
  - `components/ServiceStatusCard.vue`（**新建**）——纯展示：一句话状态 + 额度 + 时间 + 原因。
  - `components/QuotaMeter.vue`（**新建**）——纯展示：已用/总额/剩余/不限量/未知三态（工作台、成员首页、成员用量 3 处复用）。
- `views/admin/SetupView.vue`（**新建**）——`useResource(getAdminOverview)` 读 `setup`，逐步引导到既有页面。
- `views/admin/MembersView.vue`（**扩展**）——`usePagedList` + 搜索/筛选/跨页选择范围（`lib/selectionScope.ts`）。
  - `components/DataTable.vue`（**扩展**）——分页、空/错/加载三态、`data-testid` 透传。
  - `components/ImpactPreviewDialog.vue`（**扩展**）——幂等键生命周期、结果未确认态、部分完成、回结果记录。
- `views/admin/MemberDetailView.vue`（**扩展**）——状态原因、`data_complete`、两个重置动作。
- `views/admin/NodeEditorView.vue`（**扩展**）——结构化/原生双视图、REALITY 脱敏与换钥（`lib/nodeDraft.ts` 扩展）。
- `views/admin/NodesView.vue`（**扩展**）——服务端 `q`/`server_id` 搜索与真实分页。
- `views/admin/SubscriptionsView.vue`、`SubscriptionEditorView.vue`（**扩展**）——术语、"成员将获得什么"摘要、`pending_period_rule`、退役语义。
- `views/admin/RuntimeView.vue`（**新建**）——三个分段；持有 `listAgents`/`listCoreInstances`/`getRecoveryMode`/`getUpgradeReadiness`。
  - `components/UpgradeReadinessPanel.vue`（**新建**）——纯展示：状态、检查清单、阻断码。
  - `components/RecoveryModeBanner.vue`（**新建**）——持久横幅，挂载于 `AppShell` 内容区顶部。
- `components/AppShell.vue`（**扩展**）——导航分组、恢复横幅插槽、`nav-*` testid。
- `views/me/MeSubscriptionView.vue`（**重构**）——首页四段式。
  - `components/ClientImportGuide.vue`（**新建**）——按客户端分步导入 + 复制/扫码/下载 + "检查一下"诚实结论。
- `views/me/MeConnectView.vue`（**新建**）——链接、二维码、格式可用性、重置订阅链接。
- `views/me/MeUsageView.vue`、`MeStatusView.vue`、`MeCredentialsView.vue`、`MeSecurityView.vue`（**扩展**）——见 §4.10/4.11。
- 共享纯函数（无组件）：`lib/meUsage.ts`（**新建**，对象形状解包）、`lib/clientImport.ts`（**新建**，客户端↔格式映射与可用性判定）、`lib/readiness.ts`（**新建**，检查码/阻断码 → 用户语言）、`lib/selectionScope.ts`（**新建**，选择范围与摘要）、`lib/recoveryMode.ts`（**新建**，跨页共享的恢复态读取 + 变更失效）。

选型依据（复用阶梯，`ui-decisions.md`）：
- **复用**：`DataTable`、`ErrorState`、`EmptyState`、`LoadingBlock`、`StatusBadge`、`CopyField`、`QrPanel`、`AppModal`、`ConfirmDialog`、`ImpactPreviewDialog`、`JsonEditor`、`useResource`/`useAction`/`usePolling`/`usePagedList`、`lib/*Draft.ts` 系列、`lib/errors.ts`、`lib/accessReasons.ts`、`lib/batch.ts`、`lib/batchConsequences.ts`。它们是现有模式，替换会扩大回归面。
- **扩展**：`DataTable`（补分页与三态 testid）、`ImpactPreviewDialog`（补幂等/未确认/部分完成）、`AppShell`（导航分组 + 横幅插槽）、`StatusBadge`（补状态词）、`lib/nodeDraft.ts`（REALITY 脱敏/换钥）、`lib/errors.ts`（新码 hint）。均保持现有 props 语义向后兼容（新增属性可选）。
- **新建**（无法复用/扩展现有组件的理由）：
  - `SetupChecklist.vue`：现有 `EmptyState`/卡片无法表达"多步状态 + 阻断原因 + 分步入"；`DataTable` 语义不匹配表单式引导。
  - `ServiceStatusCard.vue`：成员首页与 `/me/status` 需要同一段"一句话状态 + 额度 + 时间 + 原因"展示，现有 `StatusBadge` 只承载单个状态词。
  - `QuotaMeter.vue`：额度三态（数值/不限量/未知）在 3 处重复出现，现有 `formatQuota` 是纯文本，无进度与未知态表达。
  - `ClientImportGuide.vue`：按客户端分步导入 + 三种交付方式 + 诚实结论是本节核心体验，现有 `CopyField`/`QrPanel` 只提供原子能力。
  - `UpgradeReadinessPanel.vue`：检查清单（severity × state × evidence）无现成组件，用 `DataTable` 会丢层级。
  - `RecoveryModeBanner.vue`：需常驻、非模态、跨页，`toast.ts` 是自动消失提示，`Alert` 无跨页状态与生命周期管理。
- 选项控件：客户端选择（<10 项）用原生 `<select>`/Radiogroup（Reka）；成员/节点筛选与搜索用现有输入 + 原生 select；不引入 combobox。
- 反馈时机：保存类操作按钮内 loading（100ms 内禁用 + 文案变化），>300ms 显示进行中；同步/批量等长任务进入**可持续查看**的面板或详情页，toast 只作辅助。

### 5.3 页面结构 / 线框说明

```text
工作台 /admin/overview
├── PageHeader（标题"工作台"，刷新 / 去设置）
├── SetupChecklist（未交付时显示；已完成则收为一行）
├── 需要处理（事件 / 失败任务 / 落后修订 / 数据不完整；每条 → 详情）
├── 交付进度（成员/服务器/节点/服务方案 + 在线管理程序 + 待同步 + 本周期用量）
└── 最近事件（时间 + 影响 + 入口；空态："当前没有需要处理的事项"）

管理员运行 /admin/runtime
├── 分段：运行状态 | 恢复核对 | 升级与恢复
├── 运行状态：管理程序表（在线/心跳/版本/应用修订/落后）→ 核心实例表 → 服务器运行页
├── 恢复核对：当前状态 + 进入核对（影响说明）+ 退出核对（两项勾选 + 确认 + 未核对节点清单）
└── 升级与恢复：UpgradeReadinessPanel（状态、迁移、检查项、阻断码、只读声明）

成员首页 /me/subscription
├── ServiceStatusCard（一句话状态 + QuotaMeter + 有效期/重置/更新时间）
├── 原因与下一步（不可用时；含"联系为你开通服务的管理员"）
├── 我的节点（名称 + 协议，提示"客户端里选择同名节点"）
├── ClientImportGuide（客户端选择 → 复制/扫码/下载 → 步骤 → 检查一下）
├── 用量摘要（→ /me/usage）+ 账号入口（→ /me/security、/me/connect）
└── 数据不完整/超时：卡片级"暂时无法获取…" + 重试（不显示 0）
```

布局与响应式边界：导航在 `lg` 以下收进现有 Sheet；表格统一 `.table-wrap` 横向滚动；成员首页与工作台在窄屏单列，主操作（复制链接、生成预览、保存）保持在首屏可见；不做内容等比缩放。

## 6. 状态与数据流设计

### 6.1 API 依赖（只引用契约源）

契约源：`.ai/design/product-readiness/backend.md` §4（已核对的 operation/method/path 与字段口径见下表；字段定义一律回该文件，本文不复制）。列表口径 `items`/`next_cursor`；对象/动作口径为 `data` 内业务字段；`getMeUsage` 的 `items` 是 `{usage,adjustments}` **对象**。

| Operation ID | 方法 | 路径 | 使用的请求字段 | 使用的响应字段 | 用途 |
|---|---|---|---|---|---|
| login | POST | `/api/v1/auth/login` | username, password | user, csrf_token | 登录 |
| getSession | GET | `/api/v1/auth/session` | — | user, csrf_token | 会话恢复与 CSRF |
| logout | POST | `/api/v1/auth/logout` | — | revoked | 退出 |
| redeemInvitation | POST | `/api/v1/auth/redeem` | code, username, password | id, username, role, active, proxy_enabled, subscription_id, period_start, period_end, expires_at | 邀请注册与开通结果 |
| getSubscriptionOutput | GET | `/sub/{token}` | format, base64 | — | 复制/下载/二维码目标（浏览器直接拉取，非 JSON） |
| getMeStatus | GET | `/api/v1/me/status` | — | member, access, nodes_allowed, data_complete, data_issues | 成员首页/状态页判定 |
| listMeNodes | GET | `/api/v1/me/nodes` | — | items, next_cursor | 成员可用节点（导入区） |
| getMeUsage | GET | `/api/v1/me/usage` | — | items, next_cursor | 成员用量与额度调整（对象解包） |
| getMeSubscription | GET | `/api/v1/me/subscription` | — | url, token, formats, format_status, expires_at, period_end, rotation_impact | 链接、格式可用性、重置影响 |
| rotateMeSubscriptionToken | POST | `/api/v1/me/subscription/token/rotations` | — | token, url | 重置订阅链接 |
| rotateMeCredentials | POST | `/api/v1/me/credentials/rotations` | scope, node_id | rotated, scope | 重置连接凭据 |
| changeMePassword | POST | `/api/v1/me/password` | current_password, new_password | updated, sessions_revoked | 改密与其它会话撤销 |
| listMeSessions | GET | `/api/v1/me/sessions` | — | items, next_cursor | 会话列表 |
| deleteMeSession | DELETE | `/api/v1/me/sessions/{sessionId}` | — | revoked | 撤销指定会话 |
| getRecoveryMode | GET | `/api/v1/admin/recovery-mode` | — | control_generation, revision_counter, recovery_mode, recovery_entered_at, updated_at | 全局恢复横幅 |
| enterRecoveryMode | POST | `/api/v1/admin/recovery-mode/enter` | — | control_generation, recovery_mode, recovery_entered_at, updated_at | 进入核对 |
| exitRecoveryMode | POST | `/api/v1/admin/recovery-mode/exit` | confirm, agent_revisions_checked, ledger_checked | recovery_mode, updated_at, unverified_agents, advisories | 退出核对与未核对清单 |
| getAdminOverview | GET | `/api/v1/admin/overview` | — | counts, usage_bytes, agents_online, pending_tasks, recent_events, setup | 工作台与设置进度 |
| getUpgradeReadiness | GET | `/api/v1/admin/upgrade-readiness` | — | status, checked_at, candidate_version, schema, checks, blockers | 升级预检面板 |
| listAgents | GET | `/api/v1/admin/agents` | limit, cursor | items, next_cursor | 运行状态：管理程序 |
| listCoreInstances | GET | `/api/v1/admin/core-instances` | limit, cursor | items, next_cursor | 运行状态：核心实例 |
| listServers | GET | `/api/v1/admin/servers` | limit, cursor | items, next_cursor | 服务器列表/选择器 |
| createServer | POST | `/api/v1/admin/servers` | name, address, region, notes, expires_at, cost_minor, currency | id, name, address, region, expires_at, archived, version | 新建服务器 |
| getServer | GET | `/api/v1/admin/servers/{serverId}` | — | id, name, address, region, notes, expires_at, archived, version, updated_at | 服务器详情 |
| updateServer | PATCH | `/api/v1/admin/servers/{serverId}` | version, name, address, region, notes, expires_at, cost_minor, currency | id, version, updated_at | 编辑服务器 |
| deleteServer | DELETE | `/api/v1/admin/servers/{serverId}` | — | id, archived, version | 归档服务器 |
| createRegistrationCode | POST | `/api/v1/admin/servers/{serverId}/registration-codes` | — | id, code, expires_at | 一次性接入注册码 |
| listNodes | GET | `/api/v1/admin/nodes` | server_id, q, limit, cursor | items, next_cursor | 节点列表（搜索/过滤/分页） |
| createNode | POST | `/api/v1/admin/nodes` | name, server_id, enabled, host, port, external_port, listen, protocol, flow, cipher, server_key, stream_settings, protocol_settings, client_settings, sort_order | id, version, server_key_set, enabled, protocol | 新建节点 |
| getNode | GET | `/api/v1/admin/nodes/{nodeId}` | — | id, name, server_id, enabled, host, port, external_port, listen, protocol, flow, cipher, stream_settings, protocol_settings, client_settings, sort_order, archived, version, server_key_set, updated_at | 节点编辑回读（密钥只回 set 标志） |
| updateNode | PATCH | `/api/v1/admin/nodes/{nodeId}` | version, name, server_id, enabled, host, port, external_port, listen, protocol, flow, cipher, server_key, stream_settings, protocol_settings, client_settings, sort_order | id, version, server_key_set, stream_settings, protocol_settings, client_settings, updated_at | 无损编辑与换钥 |
| deleteNode | DELETE | `/api/v1/admin/nodes/{nodeId}` | — | id, archived, version | 归档节点 |
| listNodeGroups | GET | `/api/v1/admin/node-groups` | limit, cursor | items, next_cursor | 节点组列表 |
| createNodeGroup | POST | `/api/v1/admin/node-groups` | name, description, node_ids | id, version, node_ids | 新建节点组 |
| getNodeGroup | GET | `/api/v1/admin/node-groups/{groupId}` | — | id, name, description, node_ids, version | 节点组详情 |
| updateNodeGroup | PATCH | `/api/v1/admin/node-groups/{groupId}` | version, name, description, node_ids | id, version, node_ids, updated_at | 编辑（影响预览） |
| deleteNodeGroup | DELETE | `/api/v1/admin/node-groups/{groupId}` | — | deleted, id | 删除节点组 |
| listSubscriptions | GET | `/api/v1/admin/subscriptions` | limit, cursor | items, next_cursor | 服务方案列表 |
| createSubscription | POST | `/api/v1/admin/subscriptions` | name, description, node_group_ids, quota_bytes, timezone, anchor_day, default_valid_days, allow_new_bindings, access_enabled, template_id, archived | id, name, node_group_ids, quota_bytes, timezone, anchor_day, default_valid_days, allow_new_bindings, access_enabled, archived, template_id, pending_period_rule, bound_members, version | 新建服务方案 |
| getSubscription | GET | `/api/v1/admin/subscriptions/{subscriptionId}` | — | id, name, description, node_group_ids, quota_bytes, timezone, anchor_day, default_valid_days, allow_new_bindings, access_enabled, archived, template_id, pending_period_rule, bound_members, version | 方案详情（"成员将获得什么"） |
| updateSubscription | PATCH | `/api/v1/admin/subscriptions/{subscriptionId}` | version, name, description, node_group_ids, quota_bytes, timezone, anchor_day, default_valid_days, allow_new_bindings, access_enabled, template_id, archived | id, version, pending_period_rule, bound_members, archived | 编辑与影响预览 |
| deleteSubscription | DELETE | `/api/v1/admin/subscriptions/{subscriptionId}` | — | id, retired | 退役语义与历史保留 |
| listMembers | GET | `/api/v1/admin/members` | subscription_id, include_archived, q, limit, cursor | items, next_cursor | 成员列表（搜索/筛选/分页） |
| createMember | POST | `/api/v1/admin/members` | username, password, role, active, proxy_enabled, subscription_id, expires_at, quota_override_bytes, quota_override_enabled | id, username, role, active, proxy_enabled, archived, subscription_id, expires_at, over_quota, period_start, period_end, period_timezone, period_anchor_day, used_bytes, effective_quota_bytes, access_reasons, proxy_access, allowed_node_ids, data_complete, data_issues, version | 直接开通成员 |
| getMember | GET | `/api/v1/admin/members/{memberId}` | — | member, access | 成员详情（解包 `{member,access}`） |
| updateMember | PATCH | `/api/v1/admin/members/{memberId}` | version, username, password, role, active, proxy_enabled, subscription_id, expires_at, quota_override_bytes, quota_override_enabled | id, version, proxy_access, over_quota, access_reasons, used_bytes, effective_quota_bytes, allowed_node_ids, data_complete, data_issues | 编辑/停用/绑定 |
| deleteMember | DELETE | `/api/v1/admin/members/{memberId}` | — | id, archived, version | 归档成员 |
| rotateMemberSubscriptionToken | POST | `/api/v1/admin/members/{memberId}/subscription-token/rotations` | — | token, url | 管理员重置成员订阅链接 |
| rotateMemberCredentials | POST | `/api/v1/admin/members/{memberId}/credentials/rotations` | scope, node_id | rotated, scope | 管理员重置连接凭据 |
| createMemberQuotaAdjustment | POST | `/api/v1/admin/members/{memberId}/quota-adjustments` | bytes, reason | id, member_id, bytes, reason, period_start, created_at | 审计化额度调整 |
| listTemplates | GET | `/api/v1/admin/templates` | limit, cursor | items, next_cursor | 客户端模板列表 |
| createTemplate | POST | `/api/v1/admin/templates` | name, format, content, description | id, name, format, content, description, version | 新建模板 |
| getTemplate | GET | `/api/v1/admin/templates/{templateId}` | — | id, name, format, content, description, version | 模板详情 |
| updateTemplate | PATCH | `/api/v1/admin/templates/{templateId}` | version, name, format, content, description | id, version, updated_at | 编辑模板 |
| deleteTemplate | DELETE | `/api/v1/admin/templates/{templateId}` | — | deleted, id | 删除（新增 409 引用阻止） |
| listInvitations | GET | `/api/v1/admin/invitations` | limit, cursor | items, next_cursor | 邀请列表（无明文码） |
| createInvitation | POST | `/api/v1/admin/invitations` | subscription_id, expires_at, max_uses | id, code, expires_at | 创建邀请（是否分配方案） |
| revokeInvitation | POST | `/api/v1/admin/invitations/{invitationId}/revoke` | — | revoked, id | 撤销邀请 |
| getAdminUsage | GET | `/api/v1/admin/usage` | member_id, node_id, from, to, limit, cursor | items, next_cursor | 用量查询与分页 |
| previewBatch | POST | `/api/v1/admin/batches/preview` | operation, member_ids, values, reason | id, operation, version, rules_version, member_set_digest, member_count, affected_count, affected_nodes, affected_agents, items, exceptions, warnings, conclusion, expires_at | 影响预览 |
| submitBatch | POST | `/api/v1/admin/batches` | preview_id, rules_version, member_set_digest, confirm | id, preview_id, operation, state, results, summary, member_count, idempotent_replay, created_at | 提交与回放识别 |
| listBatches | GET | `/api/v1/admin/batches` | limit, cursor | items, next_cursor | 变更记录列表 |
| getBatch | GET | `/api/v1/admin/batches/{batchId}` | — | batch, results, summary, id, state, operation | 结果核对 |
| listEvents | GET | `/api/v1/admin/events` | state, q, limit, cursor | items, next_cursor | 事件与告警 |
| ackEvent | POST | `/api/v1/admin/events/{eventId}/ack` | — | acked, id | 确认事件 |
| listTasks | GET | `/api/v1/admin/tasks` | state, batch_id, agent_id, limit, cursor | items, next_cursor | 任务与同步 |
| retryTask | POST | `/api/v1/admin/tasks/{taskId}/retry` | — | id, kind, agent_id, node_id, state, attempts, max_attempts, last_error, available_at | 重试（重新排队，非已修好） |
| listAuditLogs | GET | `/api/v1/admin/audit-logs` | action, actor, limit, cursor | items, next_cursor | 审计 |
| getSettings | GET | `/api/v1/admin/settings` | — | timezone, retention_days, telegram_enabled, telegram_chat_id, telegram_token_set, template_defaults, version, updated_at | 面板设置 |
| updateSettings | PUT | `/api/v1/admin/settings` | version, timezone, retention_days, telegram_enabled, telegram_chat_id, telegram_token, template_defaults | version, updated_at, telegram_token_set | 保存设置 |

不消费：`getHealth`、`getReady`（`callers=internal`，部署探针）与 5 个 Agent 端点（`callers=agent`）。前端不新增接口，不伪造缺失能力；`getUpgradeReadiness` 是本轮唯一新增端点的消费方。

### 6.3 由契约直接决定的界面边界（不新增接口、不编造能力）

| 契约事实 | 前端界面后果 |
|---|---|
| `updateSubscription` 请求无 `pending_period_rule` 字段，仅响应可读 | “下一周期规则”只做**只读展示**并标注“下一周期生效”；不提供“取消下一周期规则”按钮，文案只给“如需改变，请在生效前重新保存方案”——不假装按钮能撤销 |
| `getMeUsage.adjustments` 仅在首页给全、后续页为空 | 成员用量页把额度调整记录渲染为一次性区块，不提供分页，也不在翻页后重复或提示“更多” |
| 无连接探测端点；`getSubscriptionOutput` 返回非 JSON 配置文本 | “检查一下”只做可达性/可取回判定，结论限定“配置已取回 / 取回失败 / 未验证真实连接”，全站不出现“连接已验证” |
| `server_key`、`telegram_token` 永不回读（只回 `*_set`） | 编辑界面只显示“已设置/未设置”，无明文回显、无“查看”按钮；换钥只能写入 |
| `data_issues` / `blockers` / `format_status.reason` 是稳定码集合，未来可能增码 | 所有码 → 文案映射带未知码兜底（“未知检查项，请查看诊断详情”），不抛错、不显示空文案 |
| 单对象/动作用 `data`，列表用 `items`/`next_cursor`；`getMember` 返回 `{member,access}`、`getBatch` 返回 `{batch,results,...}` | 解包集中在 `api/endpoints.ts` 与 `lib/meUsage.ts`，视图层只消费已解包结构，避免每页各写一套判空逻辑 |

### 6.2 状态分工（服务端数据 / UI 草稿，按本项目 Vue 事实落地）

模板要求的"TanStack Query 独占服务端数据、Zustand 只承载客户端 UI 状态"在本项目对应为：

- **服务端数据（等价 server-state）**：只存在于 `useResource`/`usePagedList`/`useAction` 的 `shallowRef` 中，由视图或模块级单例持有，不复制进任何 `reactive` 全局 store。
  - 视图内资源：每个页面直接 `useResource(loader)` 并在 `onMounted` 触发；刷新用 `reload()`；mutate 成功后**显式失效**（重新 `reload()` 受影响资源），不做隐式缓存穿透。
  - 跨页共享的只读服务端状态（仅此一处）：`lib/recoveryMode.ts` 暴露**模块级单例** `useRecoveryMode()`（内部一个 `useResource(getRecoveryMode)` + `enter/exit` 后置失效 + 30s 轮询 `usePolling`）。它是 Vue 下"共享查询缓存"的等价物，明确不是 UI 状态容器；它只被 `RecoveryModeBanner` 与 `/admin/runtime` 读取。
  - 列表：`usePagedList` 保留 cursor 历史；新增"游标失效/筛选变化"时必须 `refresh()` 回第一页并清空选择。
  - 轮询：接入中的服务器、运行中的批次、同步中的任务使用 `usePolling`，`onBeforeUnmount` 停止；轮询不为失败重试兜底。
- **UI 状态（等价 client-state）**：只保存用户输入与交互状态，不进服务端数据。
  - 组件局部 `ref`/`reactive`：表单草稿（`lib/*Draft.ts` 纯函数产出的草稿对象）、抽屉开关（`AppShell` `drawerOpen`）、预览对话框开关、当前 tab、当前选择格式/客户端。
  - 跨页/跨组件 UI 状态：主题（`stores/theme.ts` 现状）、侧栏开关（现状组件内）、`lib/clipboard.ts` 复制反馈——保持现状，不新增全局 store。
  - 选中态只存 ID：`MembersView` 的 `selected: Set<string>` 现状即 ID 引用；新增 `lib/selectionScope.ts` 额外保存**范围快照**（`q`/`subscription_id`/`include_archived`/已访问游标）与 `scopeDigest`，用于判断"当前显示范围是否仍等于预览范围"。**不缓存成员对象**，渲染所需数据仍取 `list.items`。
  - 批量提交的幂等键：`preview_id` + `Idempotency-Key` 由 `lib/batch.ts` 生成后保存在视图内 `ref`，在"结果未确认"期间复用同一键重试，核对完成后清除。
- **订阅范围**：视图用 `computed` 从资源派生最小所需字段（如 `data.value?.access?.proxy_access`），避免整对象透传到深层组件；`QuotaMeter`/`ServiceStatusCard`/`SetupChecklist` 只接收扁平标量与数组（浅比较），不接收整个 API 响应。

## 7. 性能考虑（仅当前必需）

- 无当前必需的性能项（无大数据量渲染、无重型图表、无实时流）。沿用现状：路由级 `() => import()` 已全部懒加载。
- 仅两条与体验相关的最小约束：① 成员首页并发 3 个只读请求（status/nodes/subscription），保持并行而非串行；② 轮询间隔沿用现状量级（接入/同步状态 3–5s），页面隐藏时（`document.hidden`）暂停轮询，避免后台空转。
- 不引入虚拟滚动、不预取、不做手动 memo 优化，直到有真实数据显示需要。

## 8. 前端测试与 Demo 策略

### 8.1 前端局部测试（Vitest，仅覆盖 Demo 难稳定观察的纯规则）

新增/扩展 `*.spec.ts`（与现有 `web/src/lib/*.spec.ts`、`web/src/views/**/*.spec.ts` 布局一致）：

- `lib/meUsage.spec.ts`（新建）：`items` 为 `{usage,adjustments}` 对象时正确解包；`usage` 为空数组时不返回 `adjustments` 丢失；`items` 为数组（历史响应）时不崩；`next_cursor` 透传。
- `lib/clientImport.spec.ts`（新建）：`format_status.available=false` 的格式不产生可用链接与一键导入；`nodes_empty` / `protocol_unsupported:{field}` / `node_incomplete:{field}` 分别映射到不同原因文案；无节点时不显示任何"一键导入"。
- `lib/selectionScope.spec.ts`（新建）：筛选条件变化后 `scopeDigest` 变化；跨页勾选保留；`refresh`/游标失效清空范围；"含其它页 N 名"计数正确。
- `lib/readiness.spec.ts`（新建）：`status=blocked` 与 `unknown` 文案不同；每个阻断码有用户语言；未知码回落到"未知检查项"而不抛错。
- `lib/errors.spec.ts`（**新建**，当前无）：`RECOVERY_MODE`/`READINESS_BLOCKED`/`LEDGER_UNATTRIBUTABLE`/`CURSOR_INVALID`/`PREVIEW_STALE`/`SUBSCRIPTION_NOT_BINDABLE` 的 hint 存在且不泄露内部字段。
- `lib/nodeDraft.spec.ts`（扩展）：只改名称时 `stream_settings` 未知键与多证书条目字节级不变；REALITY 私钥为空表示保持不变；换钥只填私钥不填公钥时校验失败。
- `views/admin/BatchListView.spec.ts`（扩展）：`idempotent_replay=true` 显示"之前那次提交的结果"；`state=partial` 显示三类计数。
- 不以组件快照或 mock 全站代替完整链路；组件单测只保留上述可失败规则。

### 8.2 `data-testid` 规划（遵循 testid 规范，kebab-case、`{entity}-{action}-{type}`）

现状 `web/src` 内 0 处 testid，因此本轮为新/改主路径补 P0–P3：

- 导航（P2）：`nav-workbench`、`nav-members`、`nav-subscriptions`、`nav-servers`、`nav-nodes`、`nav-runtime`、`nav-usage`、`nav-events`、`nav-tasks`、`nav-settings`、`nav-me-service`、`nav-me-connect`、`nav-me-security`。
- 工作台/设置（P1/P3）：`workbench-pending-list`、`workbench-setup-checklist`、`setup-step-{key}`、`setup-progress-label`。
- 服务器/节点（P0/P1）：`server-create-button`、`server-registration-code-button`、`registration-code-value`、`server-agent-state`、`node-q-input`、`node-server-select`、`node-form`、`node-reality-rotate-checkbox`、`node-save-button`、`node-sync-panel`。
- 服务方案/成员/邀请（P0/P1）：`subscription-form`、`subscription-impact-preview-button`、`subscription-retire-button`、`member-q-input`、`member-subscription-select`、`member-table`、`member-select-all-checkbox`、`member-selection-scope`、`batch-preview-button`、`batch-confirm-input`、`batch-submit-button`、`batch-result-panel`、`invitation-plan-select`、`invitation-code-value`、`redeem-form`、`redeem-result-message`。
- 运行/恢复/升级（P1/P3）：`runtime-agent-table`、`runtime-instance-table`、`recovery-mode-banner`、`recovery-enter-button`、`recovery-exit-checkbox-agent`、`recovery-exit-checkbox-ledger`、`recovery-exit-confirm-input`、`upgrade-readiness-status`、`upgrade-readiness-check-list`、`upgrade-blocker-list`。
- 成员首页（P0/P1）：`me-service-status-card`、`me-quota-meter`、`me-unavailable-reason`、`client-import-select`、`client-import-copy-button`、`subscription-link-value`、`client-import-download-link`、`client-import-check-button`、`client-import-result`、`me-usage-table`、`me-usage-unknown`。
- 通用态（P3）：`loading-spinner`、`empty-state`、`error-message`、`retry-button`。
- 约束：同一页面唯一；不加在装饰图标/分隔线上；表格行用稳定的 `data-testid="member-row-{id}"` 便于断言（ID 来自服务端）。

对 Playwright Demo 主路径的影响：现有 `web/tests/*.mjs` 依赖文本与 `banner--danger[role=alert]`；本轮改为"testid 优先 + 语义文本兜底"，旧选择器在页面保留（`role=alert` 不变），因此旧脚本不会因新增 testid 而失效。

### 8.3 US → 入口 → 断言 → 阶段承接（**全部未执行**）

| US | 入口 | 关键断言（用户可见结果） | 阶段 |
|---|---|---|---|
| US-PR-001 | `/login` → `/admin/overview`；两种托管各一次 | 登录后 URL 为工作台；刷新 `/admin/members` 仍在 `/admin/members` 且显示表格（非登录页/404） | backend / web-demo |
| US-PR-002 | `/admin/overview` → `setup-step-*` | 新库：六步未完成 + "继续完成设置"；已有服务器/方案时对应步骤为已完成并可点击进入原对象；点击后返回进度变化 | frontend / web-demo |
| US-PR-003 | `/admin/servers` → 详情 | 保存后出现"下一步：生成接入注册码"；注册码可复制且带过期时间；未接入/离线/未知三种文案互不相同；接入后显示真实心跳时间 | backend / frontend / web-demo |
| US-PR-004 | `/admin/nodes/:id` | 只改名称后逐项核对连接语义、未知字段与多证书内容不丢；现代合法形状深等，历史形状只按规范映射迁移并由独立真实握手验证，不能用JSON字符串顺序或实现自带归一化函数自证；`server_key_set` 保持；换钥未成对时保存被阻止并就地报错；保存后状态为"已保存，正在同步"且不含"连接已验证" | backend / frontend / web-demo |
| US-PR-005 | `/admin/subscriptions/:id` | 摘要句包含节点组/额度/到期/重置；改动影响项保存前强制出预览；仍有绑定成员时拒绝删除并说明先迁移/解除绑定；无绑定但有历史时退役成功且历史保留；完全未使用时明确实际删除含义 | backend / frontend / web-demo |
| US-PR-006 | `/admin/invitations`、`/redeem` | 创建时显示"已分配服务方案 X"或"未分配（加入后待开通）"；带方案邀请注册后成员详情显示方案与周期；失效邀请注册显示统一"邀请不可用"且不含邀请状态细节 | backend / frontend / web-demo |
| US-PR-007 | `/me/subscription` | 首页首屏含状态、额度、到期、重置、客户端导入；复制实际成功才提示"已复制订阅链接"且令牌不变；只有明确重置动作才使旧链接失效；`format_status` 不可用格式无链接；"检查一下"结果为"配置已取回/取回失败/未验证真实连接"三者之一 | frontend / web-demo |
| US-PR-008 | `/me/subscription`、`/me/usage`、`/me/status` | 用量页有真实行（非空表）；不限量/未知/零三态文案不同；超额/到期/暂停/无方案各自显示原因与下一步 | backend / frontend / web-demo |
| US-PR-009 | `/me/security`、`/me/connect`、`/me/credentials` | 改密后显示"已退出其他 N 个会话"且当前会话仍在；两个重置动作按钮名与确认文案不同；"重置订阅链接"后旧链接返回不可用 | backend / frontend / web-demo |
| US-PR-010 | `/admin/members` → 批量 | 预览含成员数与前后值；提交后跳/链到 `/admin/batches/:id` 且逐对象结果可见；重复用同 key 提交显示"之前那次提交的结果"（`idempotent_replay`）；模拟响应丢失后重试不产生第二条记录 | backend / frontend / web-demo |
| US-PR-011 | `/admin/members/:id` 暂停/恢复 + `/admin/tasks` | 暂停后成员详情 `proxy_access` 变化且任务记录可追踪；恢复后权限与节点回归；离线节点显示"未完成"而非"已完成" | backend / web-demo |
| US-PR-012 | `/admin/overview` 待办 → 详情 | 每条待办可进入受影响对象；修复后该条消失；重试保持同一任务号、增加尝试次数且明确"已重新排队" | backend / frontend / web-demo |
| US-PR-013 | `/admin/usage`、`/me/usage` | 跨周期数据按区间显示；`data_complete=false` 时显示"暂时无法获取完整状态"而非 0 | backend / web-demo |
| US-PR-014 | `/admin/runtime` 恢复分段 | 进入核对后全站出现横幅；普通写操作就地说明"恢复核对中暂不可用"；退出后显示未核对节点清单；退出≠"节点已恢复" | backend / frontend / web-demo |
| US-PR-015 | 390px + 键盘 | 主操作首屏可达、无文档级横向溢出、焦点顺序合理、状态有文本 | frontend / web-demo |
| US-PR-016 | `/admin/runtime` 升级分段 | `blocked` 显示阻断码与处理方向；`unknown`/读取失败与"可升级"互斥；两种托管升级后均可刷新与登录 | backend / web-demo |
| US-PR-017 | 成员账号访问 `/admin/*`；管理页数据 403 | 成员被重定向到 `/me/subscription?denied=...` 并显示"仅管理员可用"；未授权/默认可见页面与公开验收产物不泄露完整token或私钥；明确授权动作可读取本人订阅链接，不把合法本人输出误判泄漏；服务端秘密不回显 | backend / frontend / web-demo |
| US-PR-018 | `/admin/members?q=` | 搜索命中跨页可达；"已选 N（含其它页 M）"与实际一致；筛选变化后提交被要求重新预览；游标失效回到第一页并清空范围 | backend / frontend / web-demo |

### 8.4 Demo/Web 资产与技能 runner 适配

- 复用现有布局：`web/tests/`（`e2e-ui-full.mjs` 全路由矩阵、`ui-integration.mjs` 逐页联调与关键提交、`platform-browser.mjs` 桌面/移动关键流程、`host-nginx-smoke.mjs` 宿主 nginx 托管烟测、`dump-ui.mjs`/`inspect-ui.mjs` 文本证据）。**不新建平行测试系统、不新建 `demo/e2e`**。
- 新增（`web-demo` 归属）：`web/tests/product-readiness-e2e.mjs`——按 §8.3 表格逐条实现的真实浏览器脚本，覆盖两条旅程主路径 + 关键异常（无效邀请、格式不可用、恢复核对横幅、预览过期、响应丢失后按同键重试、升级预检 blocked），断言落在持久可见元素（`data-testid` + 页面文本），**不以 toast 作为唯一断言**。
- 修改（`web-demo` 归属）：`web/tests/e2e-ui-full.mjs`（路由清单加 `/admin/setup`、`/admin/runtime`、`/me/connect`、`/me`；选择器优先 testid，保留旧 `role=alert` 断言）、`web/tests/ui-integration.mjs`（页面清单同上 + 新增提交用例：节点只改名保存、成员搜索、批量预览）。
- 技能 runner 按项目事实适配（说明，不在本设计执行）：`t-tools` 的 Web Demo runner 默认以 `uv run scripts/web-demo-test-runner.py demo/e2e/<file>.ts` 启动 Playwright Test 工程；本项目不是 Playwright Test 工程（`web/package.json` 只锁定 `@playwright/test` 作为库，脚本是 `node tests/*.mjs` + `PANEL_BASE`/`PANEL_ADMIN_PASSWORD` 环境变量的独立脚本，且需要真实面板与数据库）。因此 runner 侧以项目入口为准：`cd web && npm run test`（Vitest 局部规则）、`node tests/<file>.mjs`（真实浏览器，依赖已部署候选版本），并由主会话在任务阶段记录"未执行/执行失败/通过"三种状态；**不声称已验证**，不把旧 `web/tests` 日志当本轮证据（`DEC-006`）。工具预检（Docker 远端、Go 工具链、Chrome DevTools MCP 缺失）按技术预研 §6.2 在对应阶段前置，不在前端设计内解决。

## 9. 详细设计（前端最小实现映射）

### 9.1 页面状态模型

统一用"数据资源状态 + 动作状态"两层，避免每页自造：

| 状态 | 来源 | 默认呈现 | 明确不宣称 |
|---|---|---|---|
| 加载中 | `loading && !data` | `LoadingBlock`（>300ms 才显示）+ `loading-spinner` | — |
| 已加载·空 | `loaded && items.length===0` | `EmptyState` + 真实下一步（"尚未创建服务器。创建后生成接入注册码。"） | 不解释为"健康/正常" |
| 错误 | `error` | `ErrorState` + 重试；按 `errorHint` 补下一步 | 不显示 0 值或空表代替 |
| 数据不完整 | `data_complete===false` 或 `data_issues.length>0` | 就地提示"暂时无法获取完整状态"+ 原因码折叠 + 重试 | 不显示 0 用量、不显示"不限量" |
| 提交中 | `action.pending` | 按钮内 loading、禁用重复提交、保持表单值 | 不清空草稿、不跳页 |
| 已保存待同步 | 保存成功后 | 持久状态条："已保存，正在同步"+ 对象 + "查看任务" | 不说"已生效"、"已连接" |
| 部分完成 | 批次/结果 `partial` | 三类计数（已更新/等待/失败）+ 逐对象行 | 不用整体绿色成功覆盖 |
| 结果未确认 | 提交中断/网络不确定 | "正在核对提交结果"+ 复用同一幂等键的"重新核对" | 不判失败、不诱导新建变更 |
| 回滚运行 | 运行/任务上下文 | "正在使用上一份可运行配置，目标变更未完成" | 不与"目标已生效"混称 |
| 权限受限 | 路由守卫 / 403 | 重定向 + "该页面仅管理员可用" / 就地"需要管理员权限" | 不隐藏入口后仍留下可点击动作 |
| 恢复核对中 | `recovery_mode=true` | 全站持久横幅 + 相关动作就地禁用说明 | 不解锁、不伪造成功 |

状态词统一到 `StatusBadge`：`active/suspended/archived`、`proxy_access` 现有值、`pending_sync`（待同步）、`partial`（部分完成）、`failed`（同步失败）、`rollback`（回滚运行）、`unconfirmed`（结果未确认）、`unknown`（数据未知）、`not_connected`（未接入）、`offline`（离线）、`online`（管理程序在线）、`applied_stale`（配置未应用）。**不新增**`connection_verified` 之类无证据状态。

反馈时机（`ui-decisions.md`）：点击 100ms 内禁用按钮并换文案；300ms 显示 loading；>3s 的长任务（批量提交、恢复退出、升级预检）转入可持续查看面板并给"查看进度"链接，不使用 toast 作为唯一结果。

### 9.2 关键事件与副作用

| 事件 | 副作用序列 | 失效/核对 |
|---|---|---|
| 登录 | `login` → `setCsrfToken` → `sessionState.ready` → `router.push(next ?? homeRoute())` | 无 |
| 退出 | `logout` → `clearSession` → 跳 `/login` | 清 `recoveryMode` 模块缓存 |
| 保存节点 | 校验草稿（`nodeDraft`）→ `updateNode` → 关闭/保留编辑页 → 显示"已保存，正在同步"状态条 | `reload()` 节点详情 + 该服务器的运行/任务列表（若可见） |
| 换钥 | 勾选换钥 → 校验私钥/公钥成对 → 影响确认 → `updateNode`（含 `server_key`） | 同上；提示"旧客户端需重新导入" |
| 创建注册码 | `createRegistrationCode` → 一次性展示 + 复制 | `reload()` 服务器详情 |
| 保存服务方案（影响面） | `updateSubscription` → 若 409 `PREVIEW_REQUIRED` → 打开 `ImpactPreviewDialog` → `previewBatch` → `submitBatch`（幂等键）→ 跳变更记录详情 | `reload()` 方案 + 成员列表缓存视图 |
| 退役方案 | `deleteSubscription` → 若 `retired` 显示"已退役（历史保留）"→ 刷新列表 | `reload()` 列表 |
| 批量提交 | `previewBatch`（保存 digest/version/expires_at）→ 用户确认 → `submitBatch`（同键复用）→ `router.push('/admin/batches/:id')` → `usePolling(getBatch)` 直到终态 | 终态后 `reload()` 成员列表（保持筛选与选择范围不变） |
| 列表筛选/搜索/翻页 | 更新查询状态 → `usePagedList.refresh()` 或 `next()` → 未提交的预览作废（`preview=null`），选择范围摘要更新 | 清空依赖旧范围的预览 |
| 重置订阅链接（成员/管理员） | 确认影响 → `rotateMeSubscriptionToken`/`rotateMemberSubscriptionToken` → 就地展示新链接 + "旧链接已失效" | `reload()` 订阅资源 |
| 重置连接凭据 | 选择范围（`scope`/`node_id`）→ 确认（全量需二次勾选）→ `rotateMeCredentials`/`rotateMemberCredentials` → 显示"已重置，影响范围 X" | `reload()` 成员详情/本人状态 |
| 进入/退出恢复核对 | `enterRecoveryMode`/`exitRecoveryMode` → 失效 `recoveryMode` 缓存 → 显示未核对节点清单 | 全站横幅状态更新 |
| 轮询停止条件 | 批次终态、服务器已接入、组件卸载、`document.hidden` | — |

### 9.3 公共边界（新增或修改的公开面）

- `web/src/api/endpoints.ts`（修改，签名兼容）：`meApi.usage()` 返回 `{usage, adjustments, next_cursor}`（对象解包，修正现状缺陷）；`adminApi.members.list(query)` 增加 `q`/`subscription_id`/`include_archived`；`adminApi.nodes.list(query)` 增加 `q`/`server_id`；`adminApi.usage.query` 去掉 `nic_counters`、使用真实 `next_cursor`；`adminApi.batches.preview` 增加 `reason`；`adminApi.upgradeReadiness.get()` 新增；`meApi.subscription()` 类型扩展 `format_status`/`expires_at`/`period_end`/`rotation_impact`。模板删除保持 `TemplatesView.vue` 现有内联调用（只补 409 分支），不为此新增封装。
- `web/src/api/types.ts`（修改）：新增 `FormatStatus`、`OverviewSetup`/`SetupStep`、`UpgradeReadiness`/`ReadinessCheck`、`MeUsageEnvelope`；`MemberRecord` 增加 `data_complete`/`data_issues`；`SubscriptionRecord` 增加 `retired`（仅删除响应）。命名保持后端 JSON 的 `snake_case`（现状即如此，**不做 camelCase 转换层**，避免双口径）。
- 新增纯函数模块（`lib/`）：`meUsage.ts`（`unpackMeUsage(payload)`）、`clientImport.ts`（`usableFormats(format_status)`、`clientOptions()`、`importSteps(client)`）、`readiness.ts`（`describeReadinessStatus`、`describeCheck`、`describeBlocker`）、`selectionScope.ts`（`createScope`、`scopeDigest`、`isStale`）、`recoveryMode.ts`（`useRecoveryMode()` 单例）。
- 修改纯函数模块：`errors.ts`（新码 hint）、`toast.ts`（去自证文案，long-lived 状态交给面板）、`accessReasons.ts`（补无方案/暂停/到期/超额/节点禁用/数据未知的用户语言）、`nodeDraft.ts`（`realityKeyState`、`applyRealityRotation`、`preservedKeys` 摘要）、`subscriptionDraft.ts`（`pending_period_rule` 只读视图模型）、`paged.ts`（`invalidate()` 语义：失效并回第一页，返回是否失效）。
- 无新增全局 store；`stores/session.ts` 与 `stores/theme.ts` 不变（仅 `logout` 后清 `recoveryMode` 缓存）。

## 10. 风险与验证动作（前端范围）

| 风险项 | 等级 | 缓解或验证动作 | 负责人 | 完成条件 |
|---|---|---|---|---|
| `meApi.usage` 对象解包错误导致成员用量仍为空 | P0 | 先落 `lib/meUsage.ts` + spec，再改视图；真实浏览器验证行数与接口一致 | frontend-dev | 用量页在真实账号下显示行，且未知态不显示 0 |
| 无 `data-testid` 导致 E2E 依赖文本，改文案即碎 | P0 | 按 §8.2 一次性补齐主路径 testid，旧选择器保留 | frontend-dev | `product-readiness-e2e.mjs` 只用 testid/role/稳定文本，跑通 1 次 |
| "已保存"被误读为"已生效/已连接" | P0 | 状态词表 + `StatusBadge` 新增 `pending_sync`；审查全站搜索"已生效/已验证"字样 | frontend-dev + 主会话复核 | 全站无"连接已验证"类无证据表述 |
| REALITY/证书经界面编辑后被静默丢弃 | P0 | `nodeDraft` 未知键保留 + spec 断言字节一致；换钥成对校验 | frontend-dev | 只改名保存后回读 `stream_settings` 无差异 |
| 跨页选择范围/预览范围不一致导致误改他人 | P0 | `selectionScope` digest + 预览作废规则 + 后端 `PREVIEW_STALE` 兜底 | frontend-dev | 筛选变化后必须重新预览；旧范围无法提交 |
| 提交响应丢失被当成失败并重复创建变更 | P0 | 幂等键复用 + "结果未确认"状态 + `idempotent_replay` 展示 | frontend-dev | 同键重试只产生一条变更记录 |
| 恢复核对期间界面误导为成功 | P0 | 持久横幅 + 就地禁用说明；`RECOVERY_MODE` hint | frontend-dev | 横幅在全部管理页可见；写操作给出原因 |
| 升级预检 `unknown`/500 被显示为"可升级" | P0 | 三态互斥渲染 + `readiness` spec | frontend-dev | `blocked/unknown/ready` 各自文案无法互相冒充 |
| 两种前端托管下刷新 404 或资源不匹配 | P1 | 保留 history 模式 + 静态托管 fallback 文档；`host-nginx-smoke.mjs` 覆盖 | 主会话 + 部署 | 两种托管均能刷新任意路由 |
| 移动端宽表溢出遮挡主操作 | P1 | `.table-wrap` 滚动 + 390px 断言（`platform-browser.mjs`） | frontend-dev | 文档无横向溢出，主操作可达 |
| 旧链接/书签因导航改名失效 | P1 | 路径不变 + 兼容 redirect + `/me/subscription#link` 锚点迁移 | frontend-dev | 旧路径清单逐条可打开 |
| testid 与语义重复维护成本 | P2 | 只在主路径加，装饰元素不加；命名遵循规范 | frontend-dev | 无重复 testid、无装饰 testid |

## 11. 文件影响范围（前端文件）

> 只列前端；全量汇总以主文档 §8 为准。所有 MODIFY 路径均已存在（源码核对）；所有 CREATE 均在已存在父目录内，命名沿用现有 `views/`、`components/`、`lib/`、`tests/` 约定，无需新建目录。

| 文件 | 操作 | 归属 | 说明 |
|---|---|---|---|
| `web/src/router/index.ts` | MODIFY | frontend | 新增 `/admin/setup`、`/admin/runtime`、`/me/connect`、`/me` redirect；兼容 redirect（`/admin/invite`、`/me/credential`、`/admin/usage-detail`）；标题改"工作台/服务方案/变更记录"；meta 增加 `navSection` |
| `web/src/components/AppShell.vue` | MODIFY | frontend | 导航五分组重排 + `nav-*` testid；挂载 `RecoveryModeBanner` |
| `web/src/components/StatusBadge.vue` | MODIFY | frontend | 新增状态词（待同步/部分完成/回滚运行/结果未确认/未知/未接入/配置未应用） |
| `web/src/components/DataTable.vue` | MODIFY | frontend | 分页控件、三态 testid、行 ID 透传 |
| `web/src/components/ImpactPreviewDialog.vue` | MODIFY | frontend | 幂等键生命周期、"结果未确认"、"部分完成"、回结果记录链接 |
| `web/src/components/CopyField.vue` | MODIFY | frontend | 复制失败反馈与手动选中兜底 |
| `web/src/components/ConfirmDialog.vue` | MODIFY | frontend | 支持影响列表 + 二次勾选（换钥/全量重置） |
| `web/src/components/SetupChecklist.vue` | CREATE | frontend | 六步设置进度（父目录 `web/src/components/` 已存在） |
| `web/src/components/ServiceStatusCard.vue` | CREATE | frontend | 成员/管理员共用状态摘要 |
| `web/src/components/QuotaMeter.vue` | CREATE | frontend | 额度三态（数值/不限量/未知） |
| `web/src/components/ClientImportGuide.vue` | CREATE | frontend | 按客户端导入 + 诚实核对结论 |
| `web/src/components/RecoveryModeBanner.vue` | CREATE | frontend | 全站恢复核对持久横幅 |
| `web/src/components/UpgradeReadinessPanel.vue` | CREATE | frontend | 升级预检三态与检查清单 |
| `web/src/api/endpoints.ts` | MODIFY | frontend | 见 §9.3（含 `meApi.usage` 修正、新查询参数、`upgradeReadiness`；模板删除沿用现有内联调用，不新增封装） |
| `web/src/api/types.ts` | MODIFY | frontend | 新 DTO 镜像与派生字段（snake_case 一致） |
| `web/src/lib/meUsage.ts` | CREATE | frontend | `items={usage,adjustments}` 解包 |
| `web/src/lib/clientImport.ts` | CREATE | frontend | 客户端↔格式映射、可用性判定、导入步骤 |
| `web/src/lib/readiness.ts` | CREATE | frontend | 预检状态/检查码/阻断码 → 用户语言 |
| `web/src/lib/selectionScope.ts` | CREATE | frontend | 选择范围快照与 digest |
| `web/src/lib/recoveryMode.ts` | CREATE | frontend | 跨页共享恢复态（模块级单例资源，非 UI store） |
| `web/src/lib/errors.ts` | MODIFY | frontend | 新错误码 hint（`RECOVERY_MODE`/`READINESS_BLOCKED`/`LEDGER_UNATTRIBUTABLE`/`CURSOR_INVALID`/`SUBSCRIPTION_NOT_BINDABLE`） |
| `web/src/lib/accessReasons.ts` | MODIFY | frontend | 补无方案/暂停/到期/超额/节点禁用/数据未知的用户语言 |
| `web/src/lib/nodeDraft.ts` | MODIFY | frontend | REALITY 脱敏编辑、换钥成对校验、保留键摘要、多证书保留 |
| `web/src/lib/subscriptionDraft.ts` | MODIFY | frontend | `pending_period_rule` 只读视图模型与"成员将获得什么"摘要输入 |
| `web/src/lib/batch.ts` | MODIFY | frontend | `reason` 字段、幂等键生命周期、`idempotent_replay` 判定 |
| `web/src/lib/paged.ts` | MODIFY | frontend | `invalidate()`（失效即回第一页并通知调用方清空选择） |
| `web/src/lib/toast.ts` | MODIFY | frontend | 移除自证式长文案；明确"toast 仅辅助" |
| `web/src/views/admin/OverviewView.vue` | MODIFY | frontend | 工作台：待办 + 设置进度 + 交付统计（替换自证说明） |
| `web/src/views/admin/SetupView.vue` | CREATE | frontend | 首次设置路径（`/admin/setup`） |
| `web/src/views/admin/RuntimeView.vue` | CREATE | frontend | 运行状态/恢复核对/升级与恢复 三分段 |
| `web/src/views/admin/ServersView.vue` | MODIFY | frontend | 接入流程、注册码一次性提示、未接入/离线/未知三态 |
| `web/src/views/admin/ServerRuntimeView.vue` | MODIFY | frontend | 四类事实分区、应用修订落后、回 `/admin/runtime` |
| `web/src/views/admin/NodesView.vue` | MODIFY | frontend | 服务端搜索/服务器过滤/真实分页 |
| `web/src/views/admin/NodeEditorView.vue` | MODIFY | frontend | 结构化/原生双视图无损、REALITY 脱敏与换钥、同步状态面板 |
| `web/src/views/admin/NodeGroupsView.vue` | MODIFY | frontend | 影响预览文案（断线/下周期）与错误恢复 |
| `web/src/views/admin/SubscriptionsView.vue` | MODIFY | frontend | "服务方案"术语、退役语义与 `retired` 展示 |
| `web/src/views/admin/SubscriptionEditorView.vue` | MODIFY | frontend | 摘要句、`pending_period_rule`、影响预览强制 |
| `web/src/views/admin/MembersView.vue` | MODIFY | frontend | 搜索/筛选/跨页选择范围、批量入口与范围摘要 |
| `web/src/views/admin/MemberDetailView.vue` | MODIFY | frontend | 状态原因、`data_complete`、两个重置动作区分、额度调整 |
| `web/src/views/admin/InvitationsView.vue` | MODIFY | frontend | 是否分配方案、一次性码提示、撤销与失效状态 |
| `web/src/views/admin/TemplatesView.vue` | MODIFY | frontend | 修正删除提示（引用阻止 409 `template_in_use` 与引用数展示）；不再声称“引用方需自行修正” |
| `web/src/views/admin/BatchListView.vue` | MODIFY | frontend | "变更记录"术语、状态列、结果入口 |
| `web/src/views/admin/BatchDetailView.vue` | MODIFY | frontend | 部分完成三类计数、幂等回放标注、轮询终态 |
| `web/src/views/admin/UsageView.vue` | MODIFY | frontend | 游标分页、去 `nic_counters`、数据完整性提示 |
| `web/src/views/admin/EventsView.vue` | MODIFY | frontend | 待处理优先、确认后持续可见、技术详情折叠 |
| `web/src/views/admin/TasksView.vue` | MODIFY | frontend | 重试=重新排队、旧回执标注、按批次/Agent 过滤 |
| `web/src/views/admin/AuditView.vue` | MODIFY | frontend | 术语统一（同步任务/变更记录） |
| `web/src/views/admin/SettingsView.vue` | MODIFY | frontend | 恢复核对区改为状态摘要 + 链接到 `/admin/runtime`；token 只显"已设置" |
| `web/src/views/auth/LoginView.vue` | MODIFY | frontend | 统一错误文案、限流等待、`next` 保留 |
| `web/src/views/auth/RedeemView.vue` | MODIFY | frontend | 邀请确认语、无方案"待开通"结果、失效邀请统一提示 |
| `web/src/views/me/MeSubscriptionView.vue` | MODIFY | frontend | 重构为成员首页（状态 + 额度 + 节点 + 导入 + 用量摘要），链接/二维码下沉 |
| `web/src/views/me/MeConnectView.vue` | CREATE | frontend | 订阅链接/二维码/格式可用性/重置订阅链接 |
| `web/src/views/me/MeUsageView.vue` | MODIFY | frontend | 对象解包修正、不限量/未知/零三态、调整记录仅首页给全 |
| `web/src/views/me/MeStatusView.vue` | MODIFY | frontend | 原因卡、数据完整性、诊断详情折叠（内部码不铺首页） |
| `web/src/views/me/MeCredentialsView.vue` | MODIFY | frontend | 重置订阅链接 / 重置连接凭据分开 + 影响说明 |
| `web/src/views/me/MeSecurityView.vue` | MODIFY | frontend | 改密就地反馈（错误不踢会话）、会话撤销 |
| `web/src/lib/meUsage.spec.ts` | CREATE | frontend | 对象解包边界 |
| `web/src/lib/clientImport.spec.ts` | CREATE | frontend | 格式不可用/无节点不产链接 |
| `web/src/lib/selectionScope.spec.ts` | CREATE | frontend | 范围变化/跨页/失效清空 |
| `web/src/lib/readiness.spec.ts` | CREATE | frontend | 三态互斥与阻断码文案 |
| `web/src/lib/errors.spec.ts` | CREATE | frontend | 新码 hint 存在且不泄露 |
| `web/src/lib/nodeDraft.spec.ts` | MODIFY | frontend | 无损保存/换钥成对/多证书保留 |
| `web/src/views/admin/BatchListView.spec.ts` | MODIFY | frontend | 幂等回放与部分完成文案 |
| `web/src/views/admin/MembersView.spec.ts` | MODIFY | frontend | 选择范围摘要与筛选变化后禁用提交 |
| `web/tests/product-readiness-e2e.mjs` | CREATE | web-demo | §8.3 主路径真实浏览器脚本（复用 `web/tests` 布局与 `PANEL_*` 环境变量） |
| `web/tests/e2e-ui-full.mjs` | MODIFY | web-demo | 路由清单加新页、选择器改 testid 优先 |
| `web/tests/ui-integration.mjs` | MODIFY | web-demo | 页面清单与新增提交用例 |
| `web/tests/platform-browser.mjs` | MODIFY | web-demo | 成员首页导入、恢复横幅、390px 主操作 |
| `web/README.md` | MODIFY | frontend | 按最终实现事实同步项目说明，不冒称已完成运行验收 |
| `web/src/api/http.ts` | MODIFY | frontend | 复用前轮已复核的HTTP/配置/反馈修复，与本轮任务体验一致 |
| `web/src/api/reviewRegression.spec.ts` | CREATE | frontend | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `web/src/components/AppShell.spec.ts` | MODIFY | frontend | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `web/src/components/ErrorState.vue` | MODIFY | frontend | 复用前轮已复核的HTTP/配置/反馈修复，与本轮任务体验一致 |
| `web/src/components/ImpactPreviewDialog.spec.ts` | MODIFY | frontend | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `web/src/components/LoadingBlock.vue` | MODIFY | frontend | 复用前轮已复核的HTTP/配置/反馈修复，与本轮任务体验一致 |
| `web/src/lib/nodeDraft.review.spec.ts` | CREATE | frontend | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `web/src/lib/nodeTemplates.review.spec.ts` | CREATE | frontend | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `web/src/lib/nodeTemplates.ts` | MODIFY | frontend | 复用前轮已复核的HTTP/配置/反馈修复，与本轮任务体验一致 |
| `web/src/views/admin/NodeEditorView.review.spec.ts` | CREATE | frontend | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `web/tests/browserAssertions.mjs` | CREATE | frontend | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `web/tests/dump-ui.mjs` | MODIFY | frontend | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `web/tests/exitCode.mjs` | CREATE | frontend | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `web/tests/fixture-browser-check.mjs` | CREATE | frontend | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `web/tests/fixture-exitcode-check.mjs` | CREATE | frontend | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `web/tests/inspect-ui.mjs` | MODIFY | frontend | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |
| `web/tests/spaFixture.mjs` | CREATE | frontend | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 |

主审补齐说明：以上实物候选路径用于明确复用/核验边界，不表示批准原实现或旧报告。复用能力落在一个实现处，不能平行保留同义helper；测试须证明增量价值，不复制错误期望。候选压缩测试 `internal/panel/store/review_fixes_test.go` 不自动采用，其有效场景应由已列oracle/场景测试覆盖并在Handoff交代。外部候选SQL不自动采用，追加迁移按本设计单独审查。

| `web/tests/host-nginx-smoke.mjs` | MODIFY | web-demo | 两种托管的真实HTTPS/浏览器深链与会话，不把旧烟测结果当新版本证据 |


## D2 补充：readiness 校验和未知表示

| Decision ID | Applied / Not Applicable / Superseded | Artifact Location | Notes |
|---|---|---|---|
| DEC-product-readiness-013 | Applied | backend §4.2；API/CLI/消费者镜像；阶段验收 | checksum_ok 为 boolean/null：true已核验成功、false已知不符、null无法核验；不改变status/checks/blockers门禁，不伪造事实。 |
