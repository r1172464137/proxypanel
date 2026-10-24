# web — Vue 3 + TypeScript + Vite 前端

管理员端与成员端共享的单一工程，构建产物为 `web/dist`。推荐前端与 API 同源，
使用会话 Cookie + CSRF；可以由 Go 面板托管 SPA，也可以由宿主 nginx 直接托管。

## 开发

```bash
cd web
npm ci
npm run dev          # http://127.0.0.1:5173
```

Vite 将 `/api` 与 `/sub` 代理到 `http://127.0.0.1:8080`（可用 `PANEL_DEV_TARGET` 覆盖）。
默认使用相对 API 路径；`VITE_API_BASE` 的跨域能力尚有认证路径未统一的缺口，
不要据此假定纯配置跨域登录可用，见 [分离部署](../docs/deployment/split-panel-agent.md#方案-b跨域的当前边界)。

## 生产托管

[`deploy/Dockerfile.panel`](../deploy/Dockerfile.panel) 无 nginx 或入口脚本，只有一个 Go 进程。
Node 构建阶段生成 dist 并复制到 `/usr/share/proxypanel/web`；镜像默认
`PANEL_WEB_ROOT=/usr/share/proxypanel/web`，Go 同时提供 API 与 SPA。
宿主 nginx 终止 TLS，通过宿主 `127.0.0.1:8081` 反代到容器 `0.0.0.0:8080`。

将 `PANEL_WEB_ROOT` **显式设空**则 Go API-only，由宿主 nginx serve dist；
裸进程未设置此变量时也是 API-only，不会自动发现 `web/dist`。
Compose 使用 `${PANEL_WEB_ROOT-/usr/share/proxypanel/web}`，未定义取默认，显式空关闭；
旧 compose 需先添加环境映射，仅往 `.env` 写变量不会自动传入容器。
切换与 nginx 路由见 [Docker 部署](../docs/deployment/docker.md#宿主机-nginx-与前端归属)。
静态独立发布需确保 dist 与 API 版本匹配、SPA 刷新回退、缺失 assets 404、HTML 不缓存。

新镜像的两种同源托管模式已通过隔离 HTTPS 与真实 Chromium 烟测，范围与复现步骤见[部署验证](../docs/development/deployment-smoke.md)。

## 入口与页面

路由定义在 `src/router/index.ts`，全部页面按角色懒加载：

- 管理员：`/admin/overview`（工作台，内联 `SetupChecklist` 六步进度）、`/admin/setup`（首次设置完整路径）、
  `/admin/runtime`（运行状态 / 恢复核对 / 升级与恢复三段，hash `#recovery`、`#upgrade` 可直达）。
- 成员：`/me` → `/me/subscription`（我的服务）、`/me/connect`（订阅链接、二维码、格式可用性与重置链接）、
  `/me/usage`、`/me/status`、`/me/credentials`、`/me/security`。
- 兼容书签：`/me/credential`→`/me/credentials`、`/admin/invite`→`/admin/invitations`、
  `/admin/usage-detail`→`/admin/usage` 均为 `redirect`；`/me/subscription#link`、`#qrcode` 会带 query/hash
  重定向到 `/me/connect`。旧路径不新增视图，也不改名既有路由路径。

## 状态与数据流

- `src/lib/async.ts`：`useResource`（loading/error/loaded/reload/reset）、`useAction`、`usePolling`。
  轮询只在页面可见时进行，同一时间只有一个在途请求；回调异常即停止，不隐藏重试。
- `src/lib/paged.ts`：游标分页（`limit` + `next_cursor`），`invalidate()` 回第一页并清空已访问游标；
  服务端返回 `CURSOR_INVALID` 时同样失效范围，不沿用旧选择。
- `src/lib/selectionScope.ts`、`src/lib/batchPending.ts`：跨页选择范围摘要与提交核对记录。
  响应丢失时用同一 `Idempotency-Key` / `preview_id` 重试；`idempotent_replay=true` 显示“这是之前那次提交的结果”，
  不新建变更。批次结果按逐对象计数（已更新 / 等待 / 失败或需处理）呈现，不用一次成功提示覆盖。
- `src/lib/recoveryMode.ts`：唯一的跨页共享服务端查询（模块级单例，30s 可见性轮询，登录身份变化即失效），
  供 `RecoveryModeBanner`、`/admin/runtime` 与设置页读取；进入/退出核对后显式失效并重读。
- 恢复核对中、格式不可用、用量未知、数据不完整、列表过期等状态都有独立文案与重试入口；
  `src/lib/errors.ts` 维护错误码 → 用户语言映射，未知码回落而不是抛错或留空。
- 所有页面只展示真实 API 结果；“已保存，正在同步”不等于已生效，也不宣称连接已验证。

## 命令

```bash
cd web
npm run typecheck    # vue-tsc --noEmit，零错误才继续
npm test             # Vitest（jsdom）：API 封装/错误/分页/批量状态/关键表单/安全渲染
npm run build        # 类型检查 + vite build 生产构建
```

局部回归可只跑相关文件，例如 `npm test -- src/lib/paged.spec.ts src/lib/batchPending.spec.ts`。

`web/tests/*.mjs` 是真实浏览器脚本，需要先有一个运行中的面板（含数据库与管理程序）并显式提供
`PANEL_BASE`、`PANEL_ADMIN_PASSWORD` 等环境变量；本仓库既不提交这些值，也不把它们写入日志或截图。
这类验证属于隔离的私有环境，与上面的本地单测/构建分开记录，详见[测试](../docs/development/testing.md)。
单测或本次局部通过不代表完整旅程已验收：两条旅程的真实浏览器证据仍由后续 web-demo 阶段负责。

## 结构

- `src/api/http.ts` 统一信封解析、CSRF、Idempotency-Key、错误规范化
- `src/api/endpoints.ts` 按 `docs/design/implementation-contract.md` 冻结端点
- `src/lib/*` 纯函数：格式化、原生 JSON 保留式编辑、节点/订阅/成员表单、批量状态机
- `src/views/admin/*`、`src/views/me/*` 页面；`src/components/*` 通用部件（含 `SetupChecklist`、
  `RecoveryModeBanner`、`ClientImportGuide`、`ImpactPreviewDialog`、`StatusBadge`、`CopyField`）
- 表单校验在 `src/lib/*Draft.ts`，字段错误 key 与后端 `field_errors` 对齐
- 前端为 Vue 3 + TypeScript 手写客户端（`api/http.ts`、`api/endpoints.ts`、`api/types.ts`），不是 OpenAPI 生成物；
  不引入新的应用框架或查询库
- 前端与 API 推荐同源：Go 托管 SPA 或宿主 nginx serve dist，两种模式的隔离 HTTPS 与 Chromium 烟测见部署验证。
  跨 origin 部署目前只有部分配置能力，认证 URL 一致性与浏览器实测尚未补齐，不作为已支持能力宣称。