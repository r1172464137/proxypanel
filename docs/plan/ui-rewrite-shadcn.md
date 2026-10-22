# 前端重写计划书：shadcn-vue + Tailwind v4 + 前后端分离

> 决策已定：一次重写完整，shadcn 默认风格（Neutral），Tailwind 可接受，测试由我负责改到全绿。

---

## 一、技术栈变更

### 现状

| 项 | 现在 |
|---|---|
| 框架 | Vue 3.5.44 + TypeScript 5.9 + Vite 6.4 |
| 样式 | **纯 CSS**：`tokens.css` + `base.css`，共 1,664 行 |
| UI 组件 | **15 个手写组件**，共 1,058 行 |
| 状态 | `reactive()` 模块（无 Pinia） |
| 路由 | vue-router 4.5 |
| 测试 | Vitest + jsdom + Playwright |

### 变更后

| 项 | 变更 |
|---|---|
| Tailwind CSS | **v4**（`tailwindcss` + `@tailwindcss/vite` 插件，不是 v3 的 postcss） |
| shadcn-vue | **2.8.2**，组件以源码形式复制进 `src/components/ui/` |
| reka-ui | **2.11**，shadcn-vue 的可访问性底座（Radix 的 Vue 实现） |
| Data Table | `@tanstack/vue-table`（shadcn 的 Data Table 文档基于它） |
| Form 校验 | `vee-validate` + `zod`（shadcn-vue 官方推荐的 Form 组合） |
| Toast | `vue-sonner`（shadcn 的 Sonner 组件） |
| 图标 | `lucide-vue-next`（shadcn 默认图标库，替换现有 20 个手写 SVG） |
| 日期 | `@internationalized/date` + `reka-ui` Calendar（shadcn Date Picker 依赖） |

**不变**：Vue、TypeScript、Vite、vue-router、`reactive()` 状态管理（不引入 Pinia）、后端全部代码。

---

## 二、边界：保留 / 重写 / 删除

### 完全保留（约 3,100 行，一行不改）

| 目录 | 内容 | 行数 | 为什么保留 |
|---|---|---|---|
| `src/lib/` | accessReasons、nodeDraft、nodeTemplates、batch、format、json、paged、async、errors、clipboard、memberDraft、subscriptionDraft、toast、icons | **2,000** | 与框架无关的纯逻辑。**nodeDraft 438 行、nodeTemplates 214 行是核心资产**，包含全部 Xray 参数推导与密钥生成 |
| `src/api/` | http.ts、endpoints.ts、types.ts | **1,080** | 后端契约。**573 行的 types.ts 是接口定义，绝对不能动** |
| `src/stores/` | session、theme | 约 60 | 逻辑简单，仅改 theme 的持久化 key |

### 重写（约 7,200 行）

| 目录 | 内容 | 行数 |
|---|---|---|
| `src/views/` | 27 个视图（19 管理 + 4 认证 + 5 成员） | **6,166** |
| `src/components/` | 15 个通用组件 | **1,058** |
| `src/styles/` | 换为 Tailwind + shadcn CSS 变量 | 1,664 → 约 200 |

### 删除

- `src/styles/tokens.css`、`base.css`（内容迁到 shadcn 的 CSS 变量 + Tailwind）
- `src/lib/icons.ts` 里的手写 SVG 路径 → 换 `lucide-vue-next`（`AppIcon.vue` 变为薄封装）
- `ToastHost.vue` → `Sonner`

---

## 三、组件映射表

现有 15 个自写组件 → shadcn-vue 对应

| 现有组件 | 行数 | 替换为 |
|---|---|---|
| `AppModal.vue` | 118 | `Dialog` / `Sheet`（移动端用 Sheet） |
| `ConfirmDialog.vue` | 38 | `AlertDialog` |
| `ToastHost.vue` | 63 | `Sonner` |
| `StatusBadge.vue` | 99 | `Badge` + 语义色映射（保留 reasons 中文映射逻辑） |
| `EmptyState.vue` | 20 | `Empty` |
| `LoadingBlock.vue` | 9 | `Skeleton` |
| `ErrorState.vue` | 48 | `Alert`（variant=destructive）+ `Button`（重试） |
| `CopyField.vue` | 101 | `Input`（readonly）+ `Button`（复制）+ `Tooltip` |
| `JsonEditor.vue` | 68 | `Textarea` + `Form` 校验 |
| `ImpactPreviewDialog.vue` | 166 | `Dialog` + `Table` + `Alert` |
| `PageHeader.vue` | 16 | 保留为布局组件（内部用 Tailwind） |
| `AppShell.vue` | 183 | `Sidebar`（shadcn 有专用组件）+ `Sheet`（移动端抽屉） |
| `QrPanel.vue` | 63 | **保留**（shadcn 无二维码）+ 用 `Card` 包一层 |
| `AppIcon.vue` | 49 | 薄封装 `lucide-vue-next` |
| `ThemeToggle.vue` | 17 | `DropdownMenu` + `lucide` 图标 |

---

## 四、视觉设计系统

采用 **shadcn 默认 Neutral** 基色，不保留原来的"像素硬边"风格。

### 变量（`src/assets/index.css`）

| 变量 | 浅色 | 深色 |
|---|---|---|
| `--background` | `oklch(1 0 0)` | `oklch(0.145 0 0)` |
| `--foreground` | `oklch(0.145 0 0)` | `oklch(0.985 0 0)` |
| `--primary` | `oklch(0.205 0 0)` | `oklch(0.985 0 0)` |
| `--muted` / `--muted-foreground` | Neutral 标准值 | 同 |
| `--destructive` | `oklch(0.577 0.245 27.325)` | 同 |
| `--radius` | `0.625rem`（10px） | 同 |
| `--accent-brand` | **`#d97757`（保留珊瑚橙，仅用于图表与强调）** | 同 |

**唯一保留的品牌元素**：珊瑚橙作为 `--chart-1` / 强调色，不用于主按钮。
主按钮用 shadcn 默认的黑/白，这是"默认风格"的要求。

### 字体

```css
--font-sans: Inter, -apple-system, "PingFang SC", "Microsoft YaHei", sans-serif;
--font-mono: "JetBrains Mono", ui-monospace, monospace;
```

Inter 用 `@fontsource-variable/inter`（自托管，不依赖 CDN）。

### 布局

| 项 | 值 |
|---|---|
| 侧栏宽度 | 16rem（256px），可折叠 |
| 内容最大宽 | 无限制（表格页需要宽） |
| 间距节奏 | Tailwind 默认（4px 基准） |
| 表格 | 容器内滚动，`min-w` 按列数设定 |

---

## 五、前后端分离方案（本轮新增功能）

### 设计目标

**默认行为完全不变**（同源部署照旧可用），只有显式配置才启用跨域。这样现有部署不受影响，新部署可以选择分离。

### 5.1 前端改动

**① API 基地址可配置**（`src/api/endpoints.ts`）

```ts
// 默认空字符串 = 同源，与现在行为一致
const BASE = (import.meta.env.VITE_API_BASE ?? '').replace(/\/$/, '')
const ADMIN = `${BASE}/api/v1/admin`
const ME = `${BASE}/api/v1/me`
```

**② 订阅链接与二维码也是同一套基地址**

`MeSubscriptionView` / `MeCredentialsView` 里的链接来自后端 `PANEL_PUBLIC_URL`，前端不需要拼。

**③ fetch 凭据策略可配置**（`src/api/http.ts`）

```ts
// VITE_API_BASE 为空时同源，用 same-origin；配置了跨域则用 include
credentials: BASE ? 'include' : 'same-origin',
```

**④ 构建期注入**（新增 `.env.example`）

```bash
# 留空 = 同源部署（默认）
# 跨域部署时填后端地址，例如 https://api.example.com
VITE_API_BASE=
```

`vite.config.ts` 里 `define` 不需要额外配置，Vite 原生支持 `import.meta.env.VITE_*`（需以 `VITE_` 前缀）。

**⑤ 跨域时的开发代理**

开发环境（`npm run dev`）保持现有 `/api` 代理即可，不需要 CORS。

### 5.2 后端改动

**① CORS 中间件**（新增 `internal/panel/api/cors.go`，约 60 行）

```go
// PANEL_ALLOWED_ORIGINS 为逗号分隔的白名单；为空表示不启用 CORS（同源部署）。
func corsMiddleware(next http.Handler, allowed []string) http.Handler
```

要点：
- `Access-Control-Allow-Origin` 回显**匹配到的那个 Origin**，绝不用 `*`（配 Credentials 时浏览器会拒绝）
- `Access-Control-Allow-Credentials: true`
- `Access-Control-Allow-Headers: Content-Type, X-CSRF-Token, Idempotency-Key`
- `Access-Control-Allow-Methods: GET, POST, PUT, PATCH, DELETE, OPTIONS`
- `Access-Control-Max-Age: 600`
- `Vary: Origin`（避免 CDN 缓存串味）
- `OPTIONS` 直接返回 204，不进业务逻辑
- **白名单为空时完全跳过**，零开销

**② Cookie 策略可配置**（`handlers.go` 两处）

```go
// PANEL_COOKIE_SAMESITE: lax（默认，同站）| none（跨站必须）+ 强制 Secure
SameSite: cfg.CookieSameSite,
Secure:   !cfg.Dev,   // SameSite=None 时 Secure 必须为 true，启动时校验
Domain:   cfg.CookieDomain,  // 可选，同主域子域间共享
```

启动时校验：`SameSite=None` 且 `Secure=false` → 直接报错退出（浏览器会静默丢弃这种 Cookie，不校验就会变成"能登录但立刻掉线"的诡异问题）。

**③ 环境变量**

| 变量 | 默认 | 说明 |
|---|---|---|
| `PANEL_ALLOWED_ORIGINS` | 空 | 逗号分隔，如 `https://panel.example.com` |
| `PANEL_COOKIE_SAMESITE` | `lax` | 跨站部署设 `none` |
| `PANEL_COOKIE_DOMAIN` | 空 | 同主域共享 Cookie 时设 `.example.com` |
| `PANEL_PUBLIC_URL` | 必填 | 已存在，用于生成订阅链接 |

**④ 兼容性**

- 全部为新增可选配置，默认值保持现状
- 现有 `docker-compose.bundle.yml` 不加新变量也能跑
- 现有测试不受影响（新增 2 个 CORS 单测）

### 5.3 两种部署形态

**形态 1 · 同源（默认，不变）**

```
浏览器 → https://panel.example.com
           └── 单容器：nginx(:8443 静态+反代) + 面板(:8080)
```
环境变量：不需要任何新变量。

**形态 2 · 前后端分离**

```
浏览器 → https://panel.example.com   （前端，静态托管）
         https://api.example.com      （后端，只跑面板）

前端构建：VITE_API_BASE=https://api.example.com
后端环境：PANEL_ALLOWED_ORIGINS=https://panel.example.com
         PANEL_COOKIE_SAMESITE=none       ← 完全不同域名时
         （若同主域子域，保持 lax 即可）
         PANEL_PUBLIC_URL=https://panel.example.com
```

**Safari 风险声明**：完全不同域名时，Safari 默认拦截第三方 Cookie，登录可能失败。这是浏览器策略，代码无法绕过。**同主域子域不受此影响**（same-site）。

### 5.4 交付物

- `docs/deployment-split.md` 更新为与代码一致的两形态教程
- `.env.example`（前端）与 `deploy/.env.bundle.example`（后端）补全新变量
- 后端 CORS 单测 + 前端跨域模式单测

---

## 六、执行阶段

虽然一次性重写，但内部按阶段推进，每阶段可编译、可测试。

### 阶段 0 · 基础设施（半天）

- 装依赖：`tailwindcss @tailwindcss/vite reka-ui lucide-vue-next vue-sonner @tanstack/vue-table vee-validate zod @fontsource-variable/inter class-variance-authority clsx tailwind-merge`
- `shadcn-vue init`（base color: Neutral）
- 写 `src/assets/index.css`（shadcn 变量 + Tailwind 导入）
- 配 `tsconfig` 的 `@/*` 别名（已存在，确认）
- **验收**：`npm run build` 通过，现有页面样式崩但能渲染

### 阶段 1 · 组件层（1–2 天）

按依赖顺序添加 shadcn 组件并改写 15 个自写组件：

```
button input label textarea select checkbox switch radio-group
card badge alert dialog sheet alert-dialog dropdown-menu tooltip
table pagination tabs skeleton empty spinner separator
form sonner
```

- `AppShell` → `Sidebar` + `Sheet`（响应式）
- **验收**：15 个组件全部有 shadcn 版本，`lib/` 未被改动

### 阶段 2 · 认证与骨架（半天）

`LoginView`、`RedeemView`、`NotFoundView`、`AppShell`、`ThemeToggle`

- **验收**：能登录、能切主题、404 页正常

### 阶段 3 · 管理端列表页（1 天）

`OverviewView`、`ServersView`、`NodesView`、`NodeGroupsView`、`SubscriptionsView`、`MembersView`、`InvitationsView`、`TemplatesView`、`BatchListView`

统一用 `Data Table`（TanStack Table）+ `Pagination`。

- **验收**：所有列表可排序/分页，空状态/加载态/错误态齐全

### 阶段 4 · 管理端表单页（1–2 天）

`NodeEditorView`（710 行，最大）、`SubscriptionEditorView`、`MemberDetailView`、`ServerRuntimeView`

节点编辑器用 `Form` + `Tabs`：
- Tab 1「模板」→ 5 个 `Card` 可选
- Tab 2「基本」→ 名称/服务器/端口
- Tab 3「协议」→ 按协议条件渲染
- Tab 4「传输与安全」→ 按 security 条件渲染
- Tab 5「高级 JSON」→ 默认折叠

**保留全部逻辑**：`nodeDraft.ts`、`nodeTemplates.ts` 一行不动。

- **验收**：能建出 5 种节点，字段级校验就地显示

### 阶段 5 · 管理端运维页（1 天）

`UsageView`、`EventsView`、`TasksView`、`AuditView`、`SettingsView`、`BatchDetailView`

- `SettingsView` 用 `Form` + `Accordion` 分区
- **验收**：全部页面无控制台错误

### 阶段 6 · 成员端（半天）

`MeSubscriptionView`、`MeUsageView`、`MeCredentialsView`、`MeStatusView`、`MeSecurityView`

- **验收**：5 页齐全，无代理权限时的提示保留

### 阶段 7 · 前后端分离（半天）

- 后端 CORS 中间件 + Cookie 可配置 + 启动校验
- 前端 `VITE_API_BASE` + `credentials` 可配置
- `.env.example` 两份
- 部署文档更新

- **验收**：同源模式回归通过；跨域模式用真实域名实测登录闭环

### 阶段 8 · 测试与验收（1 天）

- 保留 9 个 `lib/*.spec.ts`（71 用例）+ 2 个 `api/*.spec.ts`（20 用例）
- 重写 3 个 `components/*.spec.ts`（13 用例）+ 5 个 `views/*.spec.ts`（22 用例）
- 新增：CORS 单测、跨域配置单测、shadcn 组件交互测试
- 真实浏览器验收：1440 / 390、明暗、0 溢出、0 控制台错误
- 完成真实链路联调（复用 `scripts/integration-test.py`，64 项）

---

## 七、测试策略

| 类别 | 处理 |
|---|---|
| `lib/*.spec.ts`（9 文件 71 用例） | **零改动**，纯逻辑不依赖 UI |
| `api/*.spec.ts`（2 文件 20 用例） | 小改：`credentials` 断言从 `same-origin` 改为动态 |
| `components/*.spec.ts`（3 文件 13 用例） | 重写：断言从自造 class 改为 `role`/`aria`/文本 |
| `views/*.spec.ts`（5 文件 22 用例） | 重写：改用 `getByRole` / `getByText`，不用 class 选择器 |
| Playwright 实测 | 复用现有 `tests/*.mjs`，选择器改为语义化 |

**原则**：新测试一律用 `getByRole` / `getByLabelText` / `getByText`，**不再断言 CSS 类名**。这样以后再换 UI 库测试不会全废。

**数量目标**：最终 ≥ 130 个用例（现 128，新增 CORS/跨域/组件交互后应更多）。

---

## 八、风险与应对

| 风险 | 概率 | 应对 |
|---|---|---|
| Tailwind v4 与现有 `@` 别名冲突 | 低 | 先跑空壳验证 |
| `Data Table` 与现有 `usePagedList` 分页语义不符 | 中 | 保留 `lib/paged.ts`，TanStack Table 只做排序/渲染，分页仍由后端 cursor 驱动 |
| shadcn `Form` 与现有 `nodeDraft` 校验重复 | 中 | `vee-validate` 只做展示层校验，**提交前仍跑 `validateNodeDraft`**（业务规则单一来源） |
| `reka-ui` 的 Select 在测试环境行为差异 | 中 | 测试用 `jsdom` 时优先 `Native Select`；真实浏览器用 `Select` |
| 深色主题变量遗漏 | 中 | 用 shadcn 官方 `.dark` 变量块整套复制，不手写 |
| 移动端侧栏（Sheet）焦点陷阱 | 低 | 直接使用 shadcn `Sheet` 的默认实现 |
| 测试大面积失败掩盖真实回归 | 高 | 每阶段结束跑一次全量，失败必须归因（UI 变更 vs 逻辑回归） |

### 回滚

重写期间保留分支 `ui-shadcn`。若最终效果不可接受，`git checkout main` 回到当前像素风版本，损失仅为时间。

---

## 九、验收标准

### 功能（不退化）

- [ ] 27 个页面全部可访问，标题正确
- [ ] 5 种节点模板都能建出来并保存
- [ ] 密钥生成（UUID / base64 / X25519 / shortId）全部可用
- [ ] 字段级校验就地显示
- [ ] 批量预览 → 提交 → 幂等重放
- [ ] 四格式订阅导出（含 422 显式拒绝）
- [ ] 成员端 5 页 + 无权限提示
- [ ] 主题切换、移动端抽屉

### 质量

- [ ] 1440 / 390 无横向溢出
- [ ] 0 控制台错误、0 未捕获异常
- [ ] 键盘可达：Tab 顺序、focus-visible、Esc 关弹窗
- [ ] `npm run typecheck` 通过
- [ ] `npm test` ≥ 130 用例全绿
- [ ] `npm run build` 产物 ≤ 400 KB（gzip ≤ 130 KB）

### 前后端分离

- [ ] 同源模式（不设任何新变量）行为与现在一致
- [ ] 跨域模式：真实两个域名登录闭环通过
- [ ] `SameSite=none` 但 `Secure=false` 时启动报错
- [ ] CORS 白名单不匹配时不返回任何 CORS 头
- [ ] `OPTIONS` 预检返回 204

### 部署

- [ ] 单容器形态可用（现有 compose）
- [ ] 分离形态可用（前端静态 + 后端 API）
- [ ] 文档两形态教程与实际代码一致

---

## 十、工作量估算

| 阶段 | 内容 | 估时 |
|---|---|---|
| 0 | 基础设施 | 0.5 天 |
| 1 | 组件层 | 1.5 天 |
| 2 | 认证与骨架 | 0.5 天 |
| 3 | 列表页 | 1 天 |
| 4 | 表单页 | 2 天 |
| 5 | 运维页 | 1 天 |
| 6 | 成员端 | 0.5 天 |
| 7 | 前后端分离 | 0.5 天 |
| 8 | 测试与验收 | 1 天 |
| **合计** | | **约 8.5 天** |

---

## 需要你最后确认一件事

**珊瑚橙 `#d97757` 要不要保留？**

- **A（我建议）**：主按钮用 shadcn 默认黑/白，珊瑚橙只作为图表色和强调色。这样最接近"shadcn 默认风格"。
- **B**：把 shadcn 的 `--primary` 直接设为珊瑚橙，主按钮是橙色。风格更统一但不算"默认"。

默认按 **A** 执行，除非你说 B。

确认后我从阶段 0 开始。
