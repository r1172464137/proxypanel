# web — Vue 3 + TypeScript + Vite 前端

管理员端与成员端共享的单一工程，产物由 Go 面板在 `/` 静态托管（同源 Cookie + CSRF）。

## 开发

```powershell
. .\scripts\Use-DevEnvironment.ps1   # 设置 npm 缓存到 E:\SDK
cd web
npm install
npm run dev                          # http://127.0.0.1:5173
```

Vite 将 `/api` 与 `/sub` 代理到 `http://127.0.0.1:8080`（可用 `PANEL_DEV_TARGET` 覆盖）。
生产由 Go 面板静态托管 `web/dist`；前端不使用绝对后端地址。

## 命令

- `npm run typecheck` — vue-tsc 严格类型检查
- `npm run test` — Vitest（API 封装/错误/批量状态/关键表单/安全渲染）
- `npm run build` — 类型检查 + 生产构建

## 结构

- `src/api/http.ts` 统一信封解析、CSRF、Idempotency-Key、错误规范化
- `src/api/endpoints.ts` 按 `docs/specs/implementation-contract.md` 冻结端点
- `src/lib/*` 纯函数：格式化、原生 JSON 保留式编辑、节点/订阅/成员表单、批量状态机
- `src/views/admin/*`、`src/views/me/*` 页面；`src/components/*` 通用部件
- 所有页面只展示真实 API 结果；无后端能力时展示明确错误/空态，不伪造成功。