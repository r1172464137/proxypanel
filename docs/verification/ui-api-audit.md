# 前后端联调结果与待修复列表

测试拓扑：面板（203 Docker，nginx + Go，真实 PostgreSQL/Redis）+ Agent（WSL，
proxypanel-agent 0.1.1 + Xray 26.3.27，5 个节点）+ 真实 TLS。无网络拦截、无
伪造响应。原始数据在 `/home/compile/Work/evidence/{audit,ui,ui-full}/`。

## 测试覆盖

| 步骤 | 范围 | 结果 |
|---|---|---|
| 1. 路由对账 | 后端 75 条路由 vs 前端 52 个引用 | 前端引用的路径**全部**有后端路由；后端 8 条无前端引用均为 Agent 协议/运维端点，正常 |
| 2. API 直连 | 49 项（认证/权限/404/CSRF/校验/订阅/登出） | 45 通过；4 项为测试预期错误（admin 无订阅故 403 正确） |
| 3. 页面联调 | 17 个管理员页 + 5 个成员端页 + 侧栏链路 | 3 个页面真空、1 个页面崩溃、2 个文案缺陷 |
| 4. 界面质量 | 1440px / 390px 溢出与控制台 | 无横向溢出、无 pageerror |
| 5. 跨端 | 建链路 → Agent 注册 → 5 节点实例 → 面板可见 | 通过（`revision 11 applied (5 instance result(s))`） |

---

## 待修复列表

### P0-1 · `/admin/nodes/new` 完全不可用（新建节点白屏）

- **现象**：访问"新建节点"页面，主内容区 0 字符，页面空白。
- **控制台错误**：
  ```
  DataCloneError: Failed to execute 'structuredClone' on 'Window':
  #<Object> could not be cloned.
      at cloneJson (assets/json-*.js)
      at streamView/nodeDraft
  ```
- **根因**：`web/src/lib/json.ts:10` 的 `cloneJson` 优先用 `structuredClone`，
  但调用方传入的是 Vue 的 `reactive()` Proxy（`NodeEditorView.vue:44`
  `const draft = reactive<NodeDraft>(...)`）。`structuredClone` 不能克隆
  Proxy，直接抛 `DataCloneError`，组件初始化即失败。
- **影响**：**节点完全无法通过 UI 创建**，只能走 API。这是核心功能缺失。
- **建议修法**：`cloneJson` 改为优先 `JSON.parse(JSON.stringify(value))`
  （对纯 JSON 数据语义等价且一定可用），或调用前先 `toRaw()`。前者更稳，
  不依赖调用方记得解包；`structuredClone` 支持 `Map/Date` 等类型，但本模块
  的契约就是 JSON，不需要那些。
- **回归测试**：`web/src/lib/json.spec.ts` 加一例，传入 `reactive({a:{b:1}})`
  断言不抛错且结果与原对象相等。

### P0-2 · `/admin/templates` 页面不存在，但设置页有入口

- **现象**：HTTP 200、主内容 0 字符、无标题。侧栏虽无链接，但
  `/admin/settings` 的"模板默认设置"区块有"新建模板"按钮指向该路径。
- **根因**：`web/src/router/index.ts` **没有** `/admin/templates` 路由；
  后端有完整 CRUD（`GET/POST /api/v1/admin/templates`、`PATCH/DELETE {id}`），
  前端 `endpoints.ts` 也有 `templates.list/create/update/remove`，但**没有页面**。
  请求落到 `/:pathMatch(.*)*` → `NotFoundView`。
- **影响**：模板功能后端完整、前端无 UI；用户点"新建模板"进死页。
- **建议修法**：新增 `web/src/views/admin/TemplatesView.vue`（列表 + 编辑），
  并在 `router/index.ts` 注册，侧栏补一项。
- **回归测试**：路由可达 + 空状态渲染 + 创建→列表可见。

### P0-3 · `/admin/batches` 页面不存在（批量任务无列表页）

- **现象**：同上，空白。
- **根因**：路由只有 `/admin/batches/:id`（`BatchDetailView`），**没有列表页**；
  后端 `GET /api/v1/admin/batches` 存在，`endpoints.ts` 有 `batches.list`，
  但没有页面消费它。
- **影响**：做完批量操作后无法查看历史批次，只能靠任务页间接看。
- **建议修法**：新增 `BatchListView.vue` + 路由 + 侧栏入口；
  或从"任务与同步"页加一个"批次历史"跳转。
- **回归测试**：提交一个批量操作后，列表能看到该批次。

### P0-4 · 三个空白页均无任何提示，`NotFoundView` 只有"返回首页"

- **现象**：上述三个路径进入后只有一句话和"返回首页"链接。
- **影响**：用户无法区分"功能不存在"与"页面坏了"。而且"返回首页"→`/`
  →重定向 `/admin/overview`，观感像死循环。
- **建议修法**：`NotFoundView` 明确显示请求的路径 + "该功能尚未实现/地址有误"，
  并给出返回上一页。
  P0-2/P0-3 修好后此页只用于真实 404。

### P1-1 · `/admin/subscriptions/new` 表单出现字面量 `"null"`

- **现象**：页面正文含 `null`，同时伴随 `error-banner: 阻止保存 / 订阅名称必填`。
- **根因**：新建订阅时初始值是 `null`（很可能是某字段的默认值或模板占位），
  被直接渲染；校验器因此提前报"名称必填"。
- **影响**：新建订阅页一打开就报错，用户不知道只需改名称。
- **建议修法**：定位该字段（`SubscriptionEditorView.vue` 的初始 draft），
  改为空字符串或不渲染；校验提示改为"请输入订阅名称"而非一进页面就红。

### P1-2 · 管理员不绑订阅时，`/me/subscription` 仍展示链接与二维码

- **现象**：admin 无订阅绑定，`/sub/{token}` 返回
  `403 no proxy access: no_binding`，但 `/me/subscription` 页正常显示
  订阅链接、格式选择器、二维码。
- **影响**：用户复制链接去用，得到 403；文档说明与实际行为不符。
- **说明**：这是**后端正确、前端没表达后果**。API 直连测试里我一开始也误判
  为缺陷，实际是正确拒绝。
- **建议修法**：`/me/subscription` 根据 `access_reasons` 显示提示
  "当前无代理权限（未绑定订阅），链接不可用"，并禁用"打开/下载"。

### P1-3 · 成员端 5 个页面中 4 个无侧栏入口

- **现象**：路由有 `/me/usage`、`/me/credentials`、`/me/status`、`/me/security`，
  但侧栏只有"我的订阅视图 → /me/subscription"。
- **影响**：用量、凭据、状态、账号安全四个页面只能手输 URL 到达。
- **建议修法**：成员身份下侧栏列出这 5 项；管理员身份可保留现状（管理员看的是
  自己的视图，容易混淆）。

### P2-1 · `/admin/members` 表格操作按钮无间距

- **现象**：innerText 为 `编辑停用凭据订阅链接删除`，视觉上无分隔。
- **影响**：误点风险（"删除"紧邻"编辑"）。
- **建议修法**：操作列用 `display:flex; gap:…` 或加分隔符；删除按钮做视觉区分。

### P2-2 · `/admin/servers` 表格同样问题

- `注册码编辑归档` 三个按钮粘连。

### P2-3 · `/admin/usage` 用文案承认后端筛选未实现

- **原话**："时间范围按契约传给服务端（from/to）；若后端尚未实现该筛选，
  结果会包含全部周期，请以周期列核对。"
- **影响**：把未实现的功能写在界面上免责。
- **建议**：要么实现服务端 `from/to` 过滤，要么去掉起止时间控件。

### P2-4 · 审计日志的"操作者"列为空

- **现象**：`2026-10-22 15:31 / auth.login / (空) / member mbr_...`
- **说明**：登录事件的 actor 本就没有（未认证），但列头是"操作者"，
  空值容易被当成数据缺失。建议显示"匿名"或"—"。

---

## 已验证正常（不作为缺陷）

- 末登录跳转、登录后回 `next`、登出后 401
- member 访问 admin 路由 403、admin 访问 `/me/*` 允许
- CSRF：缺 token / 错 token 均 403
- 未知对象：7 类资源全部 404，错误体带 `code`/`request_id`
- 参数校验：空 body / 非法批量操作 400
- 列表响应 `{items, next_cursor}` 与前端 `requestPage` 契约一致
- 批量操作强制 `Idempotency-Key` 与 preview 流程
- 1440px / 390px 无页面级横向溢出
- 订阅输出：admin 无订阅时 `/sub/` 403 且原因明确（正确行为）
- 跨端：Agent 注册 → 5 个 core 实例 applied → 面板 `/admin/core-instances` 可见

---

## 建议修复顺序

1. **P0-1**（新建节点崩溃）—— 核心功能，一行 JS 改动即可
2. **P0-2 / P0-3**（两个缺失页面）—— 后端已就绪，只缺前端
3. **P1-1**（新建订阅页初始报错）
4. **P1-2 / P1-3**（成员端体验）
5. **P0-4 / P2-***（文案与视觉）
