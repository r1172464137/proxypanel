# 组件契约（阶段 1 必须严格遵守）

重写 15 个通用组件为 shadcn-vue 实现时，**对外接口一个字都不能改**。
27 个视图依赖这些接口；改了就要改 27 个页面，等于白干。

## 铁律

1. **props 名称、类型、默认值不变**
2. **emits 事件名与参数不变**
3. **slot 名称不变**
4. 允许内部实现、DOM 结构、样式完全重写
5. 允许新增**可选** props（必须有默认值），不允许删改已有 props
6. 不使用 `data-slot` 之类的属性做测试锚点（本项目装的 shadcn 版本没有）
7. 测试用 `getByRole` / `getByLabelText` / `getByText`，**不断言 CSS 类名**

## 依赖

```ts
import { cn } from '@/lib/utils'          // 类名合并（已存在）
import { Button } from '@/components/ui/button'
import { Card, CardHeader, CardContent, CardTitle } from '@/components/ui/card'
// 其余按需从 '@/components/ui/<name>' 引入
```

图标：`import { X } from 'lucide-vue-next'`
样式：Tailwind 工具类 + 主题变量（`bg-background`、`text-muted-foreground`、`border-border` 等）

---

## 1. AppIcon.vue（49 行）

```ts
props: { name: IconName; size?: number; strokeWidth?: number; label?: string }
```

`IconName` 定义在 `src/lib/icons.ts`（**不要改那个文件**），共 20 个语义名：
`overview server node groups subscription members invite usage events tasks audit settings key status shield bookmark menu close logout sun moon refresh copy check eye empty alert search plus info`

要求：
- 用 `lucide-vue-next` 做映射（在组件内建 `Record<IconName, Component>`）
- `label` 存在时渲染 `aria-label` 且 `role="img"`；否则 `aria-hidden="true"`
- 保留 `<svg class="app-icon">` 这个 class（现有测试和布局可能引用），但不要依赖它做断言

## 2. AppModal.vue（119 行）

```ts
props: { open: boolean; title: string; size?: 'sm' | 'md' | 'lg' }
emits: { close: [] }
slots: default（正文）、footer（底部按钮）
```

要求：
- 基于 shadcn `Dialog`
- `size` 映射：sm→`max-w-md`、md→`max-w-2xl`、lg→`max-w-4xl`
- 关闭（点遮罩 / Esc / 关闭按钮）都 emit `close`
- 打开时焦点移入，关闭后焦点归还

## 3. AppShell.vue（183 行）

```ts
props: 无
slots: default（页面内容）
```

要求：
- 用 shadcn `Sidebar`（桌面）+ `Sheet`（≤1024px 抽屉）
- 保留现有导航数据来源：内部分 `adminNav` / `memberNav`，按 `sessionState.user.role` 切换
- 保留「我的账户」区块的 5 项（`/me/subscription`、`/me/usage`、`/me/credentials`、`/me/status`、`/me/security`）
- 每个导航项必须有图标 + 文字
- 保留顶部栏：主题切换、退出登录
- 移动端汉堡按钮打开抽屉，Esc 关闭，点遮罩关闭
- **不要改路由守卫逻辑**（在 `router/index.ts`）

## 4. ConfirmDialog.vue（39 行）

```ts
props: { open: boolean; title: string; confirmLabel?: string; danger?: boolean; pending?: boolean; errorText?: string }
emits: { confirm: []; cancel: [] }
slots: default（说明文字）
```

要求：
- 基于 shadcn `AlertDialog`
- `danger` → 确认按钮 `variant="destructive"`
- `pending` → 确认按钮显示 spinner 且禁用
- `errorText` → 在正文下方渲染 `Alert variant="destructive"`

## 5. CopyField.vue（102 行）

```ts
props: { value: string; label?: string; mask?: boolean; warning?: string; revealable?: boolean }
slots: label（自定义标签）
```

要求：
- `Input readonly` + 复制按钮（`lucide` 的 `copy`/`check`）
- `mask` → 默认显示脱敏值（保留前 6 后 4，中间 `…`）；`revealable` 时给「显示/隐藏」按钮
- 复制成功后按钮图标切 `check` 2 秒
- `warning` → 下方 `text-xs text-muted-foreground`
- 用 `src/lib/clipboard.ts` 的现有函数，不要重写复制逻辑

## 6. EmptyState.vue（20 行）

```ts
props: { title: string; description?: string; icon?: IconName }
slots: default（行动按钮）
```

要求：
- 基于 shadcn `Empty`（若无则该版本用 `Card` 手写）
- 图标居中，标题 `font-medium`，说明 `text-sm text-muted-foreground`

## 7. ErrorState.vue（48 行）

```ts
props: { error: unknown; retryLabel?: string; showRetry?: boolean }
emits: { retry: [] }
```

要求：
- `error` 用 `src/lib/errors.ts` 的 `errorText()` 转文案（已有逻辑，直接调用）
- 渲染 `Alert variant="destructive" role="alert"`
- `showRetry`（默认 true）时给重试按钮，emit `retry`
- `retryLabel` 默认「重试」

## 8. ImpactPreviewDialog.vue（167 行）

```ts
props: { open: boolean; operation: BatchOperation; memberIds: string[]; values: Record<string, unknown>; title?: string; description?: string }
emits: { close: []; submitted: [batchId: string] }
```

要求：
- **业务逻辑一行不改**：仍调用 `endpoints.batches.preview` 与 `batches.submit`，仍用 `Idempotency-Key`
- 只换外观：`Dialog` + `Table`（预览项）+ `Alert`（阻断项）+ `Button`
- 保留：加载态、错误态、`submitted` 事件

## 9. JsonEditor.vue（69 行）

```ts
props: { modelValue: JsonObject; label?: string; hint?: string }
emits: { 'update:modelValue': [JsonObject]; 'update:error': [string] }
```

要求：
- `Textarea`（等宽字体，`min-h-40`）
- 用 `src/lib/json.ts` 的 `parseJsonObject` 校验（不改那个文件）
- 解析失败 → emit `update:error` 并显示 `text-destructive` 提示
- 合法 → emit `update:modelValue`
- `label` 用 `Label`，`hint` 用 `text-xs text-muted-foreground`

## 10. LoadingBlock.vue（10 行）

```ts
props: { rows?: number; label?: string }
```

要求：
- `rows`（默认 3）个 `Skeleton`
- `label` 用 `sr-only` 播报（`aria-live="polite"`）

## 11. PageHeader.vue（16 行）

```ts
props: { title: string; description?: string }
slots: default（右侧操作按钮）
```

要求：
- 标题 `text-2xl font-semibold tracking-tight`
- 说明 `text-sm text-muted-foreground`
- 右侧操作区 `flex items-center gap-2`
- 窄屏（≤640px）改为纵向堆叠

## 12. QrPanel.vue（64 行）

```ts
props: { value: string; caption?: string }
```

**不换实现**（shadcn 没有二维码）。只把外层容器改成 `Card`，内部仍用现有 `qrcode` 调用与 canvas 渲染。
保留 `title` / `aria-label`。

## 13. StatusBadge.vue（100 行）

```ts
props: { status?: string | boolean | null; label?: string; tone?: 'ok' | 'warn' | 'danger' | 'info' | 'muted' | 'accent' }
```

要求：
- 基于 shadcn `Badge`
- **保留现有的 status→文案/tone 映射逻辑**（enabled/failed/pending/timeout/superseded/applied/rollback_applied/stopped/paused/unknown 等）
- `tone` → Badge variant 映射：ok→默认绿系、warn→黄、danger→destructive、info→蓝、muted→secondary、accent→brand
- 用 `--chart-*` / 语义色，不要硬编码 hex

## 14. ThemeToggle.vue（17 行）

```ts
props: 无
```

要求：
- `DropdownMenu` 或单个 `Button`（保持现有交互即可）
- 用 `src/stores/theme.ts` 的现有 API
- 图标 `sun` / `moon`，`aria-label="切换主题"`

## 15. ToastHost.vue（63 行）

```ts
props: 无
```

要求：
- 换成 `vue-sonner` 的 `<Toaster />`
- **保留 `src/lib/toast.ts` 的现有 API**（`pushToast`），在 toast.ts 内部改为调用 sonner（允许改 toast.ts 的**实现**，但不能改它的导出签名）

---

## 验收（我会亲自验证，不看你的报告）

1. 逐个组件 `git diff` 确认 props/emits/slots 未变
2. `npm run typecheck` 通过
3. `npm run build` 通过
4. 现有 `components/*.spec.ts` 需要你同步改到通过（断言改成语义查询）
5. 用真实浏览器打开面板，确认导航可点、弹窗能开、徽章有颜色、复制能工作
