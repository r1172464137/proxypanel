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

## 命令

- `npm run typecheck` — vue-tsc 严格类型检查
- `npm run test` — Vitest（API 封装/错误/批量状态/关键表单/安全渲染）
- `npm run build` — 类型检查 + 生产构建

## 结构

- `src/api/http.ts` 统一信封解析、CSRF、Idempotency-Key、错误规范化
- `src/api/endpoints.ts` 按 `docs/design/implementation-contract.md` 冻结端点
- `src/lib/*` 纯函数：格式化、原生 JSON 保留式编辑、节点/订阅/成员表单、批量状态机
- `src/views/admin/*`、`src/views/me/*` 页面；`src/components/*` 通用部件
- 表单校验在 `src/lib/*Draft.ts`，字段错误 key 与后端 `field_errors` 对齐
- 所有页面只展示真实 API 结果；无后端能力时展示明确错误/空态，不伪造成功。