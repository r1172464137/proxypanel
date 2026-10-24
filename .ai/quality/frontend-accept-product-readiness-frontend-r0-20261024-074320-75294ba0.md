# frontend-accept · product-readiness / frontend · R0

- **阶段**：frontend（backend 已独立 ACCEPTED，web-demo 未正式开始）
- **结论**：**ACCEPTED_WITH_IMPROVEMENTS**（本阶段必要门禁全部通过；无 P0/P1，存在 P2 改进项）
- **候选**：`706f0ba8abe0d9193c12436509264670040f2852`（基线 `c8af262`）
- **验收模式**：独立只读；不实现、不改源码/测试/断言/设计、不写 `.state.json`、不派代理、不 push、不部署
- **本轮唯一写入**：本报告 + `.ai/quality/frontend-accept-product-readiness-frontend-r0-20261024-074320-75294ba0-audit.json`

## 1. 范围与来源

输入按以下来源独立读取（主绝对路径）：

| 来源 | 位置 | 用途 |
|---|---|---|
| 本轮交接 | `.ai/quality/product-readiness-frontend-candidate.md` | 责任表、证据索引、主对 DS 自述的复查记录 |
| phase 计划 | `.ai/super-run/product-readiness/frontend.md` | 最小验证责任表、DEC014/DEC015/DEC016、停止条件 |
| 设计主文档 | `.ai/design/product-readiness.md` §2.5/§4.3/§5.1 | 覆盖矩阵、唯一 API 导航 |
| 前端分端设计 | `.ai/design/product-readiness/frontend.md` §3/§4.1–4.12/§8.3 | 每角色体验、状态语言、错误与恢复、Demo 承接 |
| 唯一契约 | `.ai/design/product-readiness/backend.md` §4（含 §4.2/§4.5/§4.6 字段索引） | 核对实际调用方法/路径/参数/响应/错误码/认证 |
| 公共协议 | `acceptance-contract.md`、`verification-evidence-contract.md`、`frontend/quality.md` | 门禁、分级、结论、证据复用条件 |
| 原始证据 | `.ai/evidence/frontend-phase/frozen-706f0ba/20261024T071857Z-1046d1af/`（含 `probe/2026-10-24T07-20-06-410Z/`、`main-touch-review.json`） | 直接复用并按条件核查适用性 |

技术栈事实按现状覆盖模板默认：Vue 3.5 + Vue Router + 手写 typed API（`api/{http,endpoints,types}.ts`）+ 现有 `useResource/useAction/usePolling`、`usePagedList`；无 React/MSW/生成客户端/新查询库。公共 guide 为默认规范，已按项目真实 Go/Vue 适配，未引入新框架。

## 2. 候选与证据适用性（独立核对，非照单全收）

- 当前验收工作树 HEAD = `706f0ba8…`，工作树干净；主路径 HEAD 为 `1b0e7e4`，其相对 `706f0ba` 的改动**仅** 4 个 `.ai/` 文件（decision-log、本交接、state、phase 计划），`git diff 706f0ba..HEAD -- . ':(exclude).ai'` 为空 → 非 `.ai` 源码/测试/fixture/锁/配置与本候选一致，证据输入适用。
- 证据清单内指纹与磁盘现状逐一比对一致：`probe=c31fe1b2…`、`fixtures=45db9096…`、`launcher=f027d08e…`、`dbHelper=c7f6e59d…`、`package-lock=1242c082…`（sha256sum 实测相同）→ 证据 3 条件（可复核、输入未变、无矛盾）满足，未重跑。
- `vitest.json` 由我独立解析：`numTotalTests=300`、`passed=300`、`failed=0`、`pending/todo=0`、`testResults` 文件 48 个，逐 assertion 统计全部 `passed`。worker 的 107 是 suite 计数，不是文件数，未沿用该说法。
- `summary.json`：typecheck exit 0、build exit 0、`npm audit --package-lock-only` 各级 total=0（prod 100/dev 217/total 316）。`build.log` 尾部为真实 Vite 构建产物。
- `source-tree-diff.txt`：`trees_identical=true`、`git diff lines before=0 after=0`、HEAD 记录 `706f0ba`；`cleanup.json`：`schema_dropped=true`；`probe/report.json`：`notRun` 明列 HTTPS/静态托管/Agent 数据面/18 故事/截图/screen-reader。**没有把「已通过」摘要当作行为证据。**
- 触控争议独立复核：读 `web/tests/frontend-phase-probe.mjs`（当前 sha 与证据 `probe` 指纹一致）第 225–252 行 `assertMobilePage` 对每个传入 testid 执行 `scrollIntoViewIfNeeded` → `toBeVisible` → 横向不越界 → `box.height>=44`；admin 场景确实传入 `overview-tasks-link`、`member-username-${memberId}`，两个 testid 在 `OverviewView.vue:202`、`MembersView.vue:477` 真实存在，渲染分支为真实 API 数据的列表项。`navLinkHeight` 是关闭抽屉下可为 null 的可选量，与被断言的 key targets 不是同一组。结论：worker 备注误读，断言真实执行，不升级为 P1，也不因此忽略该项。

## 3. 独立行为/API/安全核对（按用户关心的边界逐项）

以下为我在源码与契约层面独立核对的结果（每条均落到实际文件），配合上述运行证据：

### 3.1 导航、设置、兼容入口
- `/admin/setup` 是真实页面（`SetupView.vue` 读 `adminApi.overview()`，含 loading/error/retry），不再是 redirect；`MeConnectView` 独立注册 `/me/connect`；`/me/credential`、`/admin/invite`、`/admin/usage-detail` 为显式 redirect，`/me` → `/me/subscription`；`/me/subscription#link|#qrcode` 在 `beforeEach` 中带 `query`/`hash` 迁移到 `/me/connect`（`router/index.ts:203`）。
- 角色守卫：`meta.role='admin'` 时非 admin → `me-subscription?denied=`；守卫只对 `next` 使用 `safeLoginNext`（要求以 `/` 开头、拒绝 `//`、反斜杠与控制字符），不开放外部跳转；管理员访问 `/me/*` 不被强制重定向。
- setup 计数：`SetupChecklist.vue` 只信 `completed_count`（要求 0–6 整数），缺失/`unknown`/未知状态一律计入 `unknownCount` 且明确「N 步状态未知，未计入已完成」，`complete` 需 6 且各步 completed 且无 blocked/unknown；未报 `setup` 时显示「进度暂不可用」，不显示 0/6。probe observation 印证真实 API 返回 `completedCount=3` + `unknownKeys=[traffic_observed]` 时页面为「已确认完成 3/6 步」。
- 工作台范围语言：`OverviewView.vue` 失败任务 `state=failed&limit=20`、agents `limit=20`，正文明确「首批最多 20 项；下列条数不是全部故障总数」，并有 `next_cursor` 时提示还有更多；修订落后仅在 `desired/applied` 均为非负安全整数且 `desired>applied` 时判落后，未知单列并说明「未算作落后或已应用」。
- 事件直达白名单：`lib/workbench.ts` 的 `target()` 只接受 `batch|node|server` 且 id 必须匹配 `^[a-zA-Z0-9_-]+$`，其余回落到 `/admin/events`；**不插值 API 路径**，不生成任意跳转。

### 3.2 恢复核对与成员可见性
- `lib/recoveryMode.ts` 为唯一共享查询：`identity()` 只认 `role==='admin'`，非 admin 时 `useResource` 直接抛 AbortError 且 `reload()` 返回，**成员不探测 `/admin/recovery-mode`**（`recoveryMode.spec.ts` 用例 1 断言 `get` 未被调用）；`epoch` 在登出/身份切换/`clearRecoveryMode()` 时递增，晚到的管理员响应被丢弃；轮询 `usePolling` 隐藏暂停、可见立即补一次、`inFlight` 不可并发、`consumers` 归零即 `stop()`；回调异常 `stop()` 并暴露 `error` 供显式重试。
- 横幅只在 `AppShell.vue:256` 对 admin 渲染，成员页面收到写操作 409 时按本区域提示（`MeSubscriptionView`/`MeConnectView` 对 `RECOVERY_MODE` 单独分支）。
- 成员已有 token 只读、无 token 409 只降级导入区：`MeSubscriptionView` 中状态/额度卡片与导入卡片是独立资源与独立错误分支，`RECOVERY_MODE` 只替换导入区，状态与用量保留。

### 3.3 查询、跨页选择、批量与不确定结果
- `MembersView.vue` 真实把 `q`/`subscription_id`/`include_archived` 传给 `adminApi.members.list`（`endpoints.ts:210`，契约 §4.6 `listMembers` 请求字段一致）；`NodesView` 传 `q`（契约 `listNodes` 含 server_id/q）；`EventsView` 用 `state`/`q`。
- 选择只存 ID（`selected: Set<string>`），`selectionScope` 摘要只含 query/已访问游标/选中 ID/`revision`，不缓存成员对象；`otherPageCount` 给出「含其它页 M 名」。
- 筛选/刷新/游标失效清范围：`watch([subscriptionFilter, includeArchived], search)`、`search()` 清 `selected`；`filter` 一变即 `list.invalidate()`；`usePagedList.invalidate()` 同步推进 `request`/`revision` 并清空 items/history/`visitedCursors`；`CURSOR_INVALID` 时 `invalidate()` + 明确文案且不自动重试（`paged.spec.ts` 两条用例覆盖「invalidate 不产生旧结果」「过期游标回首页且不循环」）。
- 晚到响应不能恢复旧范围：`load()` 内 `current !== request` 直接 return；`ImpactPreviewDialog` 用 `generation` 丢弃旧 preview，且 scope 变化立即 `stale=true`。
- 不确定提交：`ImpactPreviewDialog.submit()` 先 `savePending(record)`（写入后回读校验）再发请求，失败时除 `PREVIEW_STALE`/`PREVIEW_CONCLUSION_CHANGED` 外**保留 pending 记录与同一 key/payload**；`batchPending.ts` 拒绝跨账号/损坏记录、同 scope 不同 key 抛错；`MembersView` 有「核对未确认批量提交」入口（`recoverPending`）。
- 批次详情解包：`batchDetailFromEnvelope` 取 `envelope.batch ?? envelope`、`results ?? batch.results`、`summary`，缺 `id` 或 results 非数组时抛 `MALFORMED_RESPONSE`（不渲染空成功）；`{...batch}` 保留 `created_at/state/operation`；`frontendContract.spec.ts` 覆盖真实包络、非法包络、扁平包络。
- 部分完成三类计数：`batchResultCounts()` 把 `succeeded|applied` 计已更新，`failed|timeout|superseded|conflict|skipped` 计失败，其余计等待；详情页与列表页共用同一函数。

### 3.4 节点、方案、邀请
- `nodeDraft.ts`：`applyStreamView/applyProtocolView/applyClientView` 都是「读旧值→仅写变化字段」的 diff 写法，其它键（sockopt/wsSettings 等）原样保留；`unknownKeys()` 在编辑页展示未知键；多证书只改第一条的填写字段并保留其余条目（`certificates.slice(1)`）；`splitShortIds` 保留 REALITY 空 shortId；`server_key` 不回显，`draftFromNode` 置空并只保留 `server_key_set`，`buildNodeWrite({omitServerKey})` 在保持原密钥时不提交；提交前删除 `protocol_settings.clients`。
- 换钥：必须显式勾选 `rotateReality`，`realityRotationError` 对「未勾选却改私钥/公钥」「勾选但缺一对或公钥等于旧值」都返回错误；`keyChanged && !keyImpactConfirmed` 阻止保存并给出影响说明；版本冲突（409 `CONFLICT` 或 `field_errors.version`）时 `versionConflict=true`，草稿保留，提供「重新载入最新版本（替换草稿）」。
- 服务方案：编辑页固定展示「成员将获得什么」「当前保存的周期规则」「拟保存的周期时区…仅下一周期生效」；`pending_period_rule` 以只读 `<pre>` 展示；影响面变更走 `PREVIEW_REQUIRED` → `ImpactPreviewDialog`（`isPreviewRequired`）；删除按钮文案按 `bound_members` 区分，响应 `retired=true` 显示「方案已退役，历史保留」，后端的 `subscription still has N bound member(s)` 被 `errorText` 原样展示在确认框内。
- 模板 409：`TemplatesView` 对 `CONFLICT` + `details.reason==='template_in_use'` 显示「仍被 N 个未归档方案引用，请先解除引用后再删除」，与 `store/resources.go:280` 的 `template_in_use/reference_count` 一致。
- 邀请：`max_uses` 默认 1；创建后一次性展示码与链接，`closeCreate()` 清 `created`（secret 不驻留），列表不显示明文码；文案明确「仅显示一次」。

### 3.5 成员使用、账号与凭据
- `lib/memberService.ts` 只信服务端判定：`data_complete===false` 或未知/未来 `proxy_access` → `unknown`；`access===true` 且 `proxy_access` 缺省或 `enabled` 才 `enabled`；`access===false` 且非 enabled 才 `denied`；`total` 用 `!== undefined` 区分「显式 null=不限量」与「缺失=未知」，`unlimited` 仅 `total===null`；`access` 存在时不回退到 member 的旧 `access_reasons`（避免 stale 原因），并过滤 `ok`。`MeSubscriptionView.spec.ts` 覆盖 0 额度/0 已用/期限、显式不限量 vs 缺失、以及 5 组判定与 4 类原因文案。
- 链接不乐观生成：`formatAvailability()` 要求格式在 `formats` 且 `format_status` 有对应条目且 `available===true`；`memberSubscriptionUrl()` 无 url 或不可用即返回 `''`；仅 `http/https`、无 userinfo。对应设计「格式缺明确 available 不产链接」。
- 无未验证一键 import：`MEMBER_CLIENTS` 只给文字步骤，页面文案明确「手动导入指引不代表兼容认证，不提供未经验证的一键唤起」。
- copy 不旋转：`CopyField` 仅调用 `copyText`，失败时揭示并聚焦/选中文本、提示手动复制，不触发任何轮换接口。
- 下载/自检不说连接已验证：`retrieve()` 只 `fetch` 配置并校验非 HTML/非空，消息为「配置已取回；未验证真实连接」「…未验证真实连接」。
- MeUsage：`unpackMeUsage` 保留 `items={usage,adjustments}` 对象形状，非法/不完整抛错而非空账本；`MeUsageView` 的 `adjustments` 只在 `page===0` 展示；`CURSOR_INVALID` 时回首页并清游标历史，不自动重试；`loadMeUsage` 走 `VITE_API_BASE` + `me/usage`，与契约 §4.2 一致。
- node/session 真游标：`allPages()` 循环 `next_cursor` 并对重复游标抛错，`meApi.nodes/sessions` 传 `cursor`（`frontendContract.spec.ts` 覆盖），不静默截断在 50。
- 两种 reset 区分：`MeConnectView` 的「重置订阅链接」只调 `rotateSubscriptionToken`，成功后立刻把本地 `subscription.data` 置空再重读；`MeCredentialsView` 的「重置连接凭据」默认 `single_node`，需选节点；`all_nodes` 必须额外勾选确认；两者确认文案与影响各自说明，且都注明「提交成功≠已断开现有连接 / 同步结果为准」。
- 密码 401：`MeSecurityView.changePassword()` 401 时先 `authApi.session()` 只读核对：会话仍有效 → 就地「当前密码不正确…现有会话未被退出」；会话确已 401 → `clearSession()` 并跳登录；其他错误单独提示。
- 会话 epoch：`stores/session.ts` 的 `identityEpoch` 在 `clearSession/markAnonymous/login` 推进；`ensureSession` 每个请求带 `epoch`，回写前比对；`finally` 只在 `inflight === entry` 时释放（旧请求不清理新 singleflight）。`session.spec.ts` 6 个用例覆盖：旧读取不复活身份/CSRF、新登录不被旧响应覆盖、clear 后新单飞不被旧 finally 释放、同身份 force 合并、401 vs 网络错误、废弃请求失败不覆写匿名态。

### 3.6 通用状态、可访问性与主题
- `StatusBadge`：已知状态映射中文，未知/未来状态回落 `TONES[key] ?? 'muted'` 并显示原码，不显示「正常」；`applied_stale/rollback/partial/unconfirmed/not_connected` 全部有明确中文且非成功色调。
- `lib/readiness.ts`：只翻译已知检查 id，未知 → 「未知检查项 / 请查看诊断详情」，原始 id/summary/evidence 仍在技术诊断折叠区；`RuntimeView` 的 `checksum_ok` 严格三态（true/false/「无法核验（不是成功…）」），`status` 三分支（ready/blocked/unknown,unknown 文案为「不应继续升级」），读取失败走 `ErrorState` + 重试，不与「可升级」混用。
- 网关非契约正文：`api/http.ts normalizeError()` 仅在 `payload.error` 为对象时透传 message/code/details，否则统一 `statusFallback(status)`；注释与实现都明确「HTML/纯文本/任意 JSON 一律不回显」，避免代理/网关错误页泄露潜在 credentials。
- 390px/触控/键盘/主题：probe 在真实 Chromium 156 + coarse pointer、390×844 下跑 member 3 页与 admin 6 页，断言无文档级横向溢出、每个 key action 横向在视口内且 `height>=44`；`observations` 记录两个角色「<44px 交互项」列表为空数组；亮/暗切换断言 `documentElement` class 与 computed 背景变化；Tab→抽屉 nav、Enter 导航、Esc 关闭并把焦点还给汉堡按钮；全程无 pageerror。`AppShell` 的 sheet `onCloseAutoFocus` 里 `preventDefault()` 后显式 `menuButton.focus()`，与断言一致。

## 4. 门禁

| # | 门禁 | 结果 | 证据 |
|---|---|---|---|
| G1 | 受版本控制的输入与证据一致（非 `.ai` 源码/测试/fixture/锁/配置 = 候选） | passed | 工作树 HEAD `706f0ba`；`git diff 706f0ba..HEAD -- . ':(exclude).ai'` 空；`source-tree-diff.txt`（trees_identical）；五个指纹 sha256sum 实测一致 |
| G2 | 类型检查实际通过 | passed | `summary.json.typecheck.exit=0`；`typecheck.log` |
| G3 | 全部前端单测实际执行、无失败/跳过 | passed | `vitest.json` 我独立解析 300/300 pass、0 fail、0 pending/todo、48 testResults |
| G4 | 生产构建实际成功 | passed | `summary.json.build.exit=0`；`build.log`（真实 Vite 产物与耗时） |
| G5 | 依赖审计无已知漏洞 | passed | `audit.json` metadata：info/low/moderate/high/critical/total 全 0（--package-lock-only） |
| G6 | 设计/行为状态（setup 计数、未知≠0、工作台 20 项范围、恢复只 admin、成员不探测 admin、两类 token） | passed | `SetupChecklist.vue`/`OverviewView.vue`/`lib/recoveryMode.ts`+`recoveryMode.spec.ts`/`MeSubscriptionView.vue`；probe 场景 4、8、9、10 PASS 与其 observation |
| G7 | 查询/跨页选择/预览失效/不确定提交/批次包络/三类计数 | passed | `MembersView.vue`、`lib/paged.ts`+`paged.spec.ts`、`lib/selectionScope.ts`+spec、`ImpactPreviewDialog.vue`、`batchDetailFromEnvelope`+`frontendContract.spec.ts`、`lib/batch.ts`；probe 场景 12 PASS |
| G8 | 节点无损/脱敏/成对换钥/影响确认/版本冲突保草稿；方案 pending 只读+影响预览+退役；模板 409；邀请一次性 secret 清理 | passed | `lib/nodeDraft.ts`+`nodeDraft.spec.ts`、`NodeEditorView.vue`、`SubscriptionEditorView.vue`、`SubscriptionsView.vue`、`TemplatesView.vue`、`InvitationsView.vue`；probe 场景 6「真实 API 仅改名回读深等」PASS |
| G9 | 成员逐字段状态/无链接降级/无未验证 import/copy 不旋转/下载不称已验证/用量对象与游标/两种 reset/密码 401/session epoch | passed | `lib/memberService.ts`+`MeSubscriptionView.spec.ts`、`lib/clientImport.ts`、`ClientImportGuide.vue`、`CopyField.vue`、`lib/meUsage.ts`+spec、`MeUsageView.vue`、`MeConnectView.vue`、`MeCredentialsView.vue`、`MeSecurityView.vue`、`stores/session.ts`+`session.spec.ts`；probe 场景 8、11 PASS |
| G10 | 状态语言不假正常、readiness 三态、非契约正文不回显、390px/键盘/主题真实证据 | passed | `StatusBadge.vue`、`lib/readiness.ts`+spec、`api/http.ts normalizeError`、`RuntimeView.vue`；probe 场景 13–16 PASS + `assertMobilePage` 实现核对 + `main-touch-review.json` |
| G11 | API 方法/路径/参数/响应/错误码/认证与 backend §4 一致（前端仅消费） | passed | `endpoints.ts` 逐条对照 §4.5/§4.6（members q/include_archived/subscription_id、nodes q/server_id、events state/q、tasks state/batch_id/agent_id、me/usage 对象形状、batches 包络、recovery exit 三字段、PATCH 带 version、DELETE 语义）；`frontendContract.spec.ts`/`http.spec.ts` |
| G12 | 无新增框架/生成物漂移，派生物与清理 | passed | `package.json`/锁与证据指纹一致；无 OpenAPI 生成物改动；probe `cleanup.json schema_dropped=true`、`cleanups` 记录 Panel/browser 关闭；本验收未产生源码派生物 |

> 未重跑 300/17：按 verification-evidence-contract 复用条件 1–3 全部满足，且用户/项目门禁未要求独立重跑；本轮独立执行的是证据解析、指纹比对、`git diff` 范围核对与源码/契约逐点复核（不产生副作用的只读动作）。

## 5. 问题

无 P0、无 P1。以下为 P2（不影响本轮验收目标，交后续维护）：

| 级别 | 位置 | 证据 | 影响 | 建议 |
|---|---|---|---|---|
| P2 | `web/src/views/me/MeSubscriptionView.vue:49`、`web/src/views/me/MeUsageView.vue:87` | 模板硬编码「数据更新时间：暂未提供」；`MeStatus`/`MeUsage` 响应中未消费时间字段 | 设计 §4.10 要求展示数据更新时间；当前是诚实的占位（不编造时间），不影响状态正确性 | 待契约提供成员侧采样/账本时间字段后接入；否则保持现有诚实文案 |
| P2 | `web/src/views/admin/BatchDetailView.vue` 的 `summary` 徽标 | 该处按原始 state 码渲染 `{{ status }}：{{ count }}`（如 `succeeded：1`），同页其他状态都走 `StatusBadge` 中文 | 仅列表页局部一致性/可读性；不影响三类计数与业务判断 | 复用 `StatusBadge` 或已有中文映射，不改数据口径 |
| P2 | `web/src/components/ImpactPreviewDialog.vue:57` | `scope` 包含 `reason.value`，预览后修改「变更原因」也会触发 `scopeChanged` 要求重新预览 | 比服务端 `PREVIEW_STALE` 更保守，只增加一次只读预览，无错误提交风险 | 若确认 reason 不参与服务端结论，可从 scope 指纹中排除；需与后端 digest 口径一致后再改 |

## 6. 未验证 / 下游承接（本阶段通过 ≠ 业务验收完成）

| 场景 | 承接 | 位置 |
|---|---|---|
| 18 个完整用户故事端到端（管理员/成员全旅程）、`scripts/web-demo-test-runner.py` 整文件终验 | web-demo | 主设计 §6；frontend §8.3/§8.4 |
| UI → 真实 Agent → 官方客户端 → 流量账本整链；真实 core 应用/回滚/部分完成 | web-demo | frontend §4.8；backend §10 |
| 双 Agent、离线/UDP、部分完成、旧 Agent/SQLite 与 core 跨版本 | web-demo | backend §6.10/§10 |
| 两种托管（Panel 托管 + 宿主 nginx 静态）下的登录/刷新、HTTPS/TLS、实际 host nginx、DNS、公有 CA | web-demo / 部署承接 | frontend §4.1、§8.4；DEC007 |
| 真实数据库备份/恢复、`getUpgradeReadiness` 在真实历史库上的 blocked/unknown 分支、升级回退 | web-demo | backend §9.9；frontend §4.8 |
| 屏幕阅读器审计、截图视觉回归（本轮只有键盘/角色/label 与 computed 主题证据） | web-demo | probe `notRun` 明示 |
| 全部支持平台/客户端与 Vision | web-demo | 用户范围；主文档 §1.3 |
| 203 独立 8445 预览部署与用户可见检查 | 主会话（ACCEPTED 后按 DEC015/DEC016） | 本验收**不执行部署**，不以 stage 镜像当页面通过 |

本前端阶段可放行：必要门禁通过、无 P0/P1，前端实现与 706f0ba 证据输入适用。**feature 未完成**，`product-readiness` 仍需 web-demo 收口上述场景；本报告不构成 web-demo 或部署验收。

## 7. 变更范围与清理

- 本轮只写本报告与同名 `-audit.json` 审计附件；源码、测试、断言、设计、fixture、锁、迁移、部署与 `.state.json` 均未改动。
- 未启动/停止任何服务、未操作 203/8443/旧 PG/Redis/Agent、未使用旧 `.devdb`、未 install 新工具。
- 复用证据的私有 raw 输出保持在 `.ai/evidence`，未复制进本报告。
