# 前后端联调完整测试计划

目标：在真实链路（面板容器 + 真实 PostgreSQL/Redis + 真实 nginx + 真实 Agent）上，
把后端 75 个 API 与前端页面逐一对账，找出「后端有、前端没入口」「前端调了、
后端没有」「调了但报错」「能调但结果不对」四类问题。

不做任何网络拦截或伪造响应。注入故障只用于验证错误态渲染，且用
`INJECTED_` 前缀的假错误码与真实错误区分开。

---

## 0. 环境基线

| 项 | 值 |
|---|---|
| 面板 | `https://192.168.5.203:8443`（compose：panel + postgres + redis） |
| Agent | WSL 内 `proxypanel-agent 0.1.1` + Xray 26.3.27，5 个节点 |
| 浏览器 | Playwright Chromium，1440×900 与 390×844 |
| 账号 | `admin`（管理员）、另建一个 `member` 用于权限对账 |

**前置检查**（每轮都跑，任一失败则中止）：
- [ ] `/healthz` 200、`/readyz` 200
- [ ] 容器三个服务 healthy
- [ ] Agent 心跳新鲜、`applied_revision == desired_revision`
- [ ] 5 个节点端口在监听

---

## 1. 路由对账（静态）

**目的**：找出后端有但前端没入口、或前端调了但后端没有的接口。

- [ ] 1.1 提取后端全部 `"METHOD /path"` 注册（已有 75 条）
- [ ] 1.2 提取前端 `endpoints.ts` 全部调用（已有 30 条唯一路径）
- [ ] 1.3 提取前端路由表全部 `path`
- [ ] 1.4 输出三张差异表：
  - 后端 API 无前端调用 → 功能未接线
  - 前端调用无后端路由 → 必然 404
  - 前端路由表 vs 侧栏链接 → 死链

---

## 2. API 层逐个直连测试（curl，绕过 UI）

**目的**：先证明后端本身正确，UI 问题才可归因。

对 75 个路由逐条测，每条记录：状态码、响应体、是否幂等、是否泄露敏感字段。

- [ ] 2.1 **认证类**（login/logout/session/redeem）
  - 正确凭据 200；错误凭据 401；缺 CSRF 403；重复登录后的旧会话
- [ ] 2.2 **权限类**：未登录 401、member 访问 admin 路径 403
- [ ] 2.3 **CRUD 全生命周期**（servers / nodes / node-groups / subscriptions /
      members / templates / invitations）
      每条路径做 create → list → get → patch → delete，并验证列表可见性
- [ ] 2.4 **批量操作**（batches/preview → batches）
      预览必填、`Idempotency-Key` 必填、重复提交幂等
- [ ] 2.5 **运维类**（overview / tasks / events / audit-logs / usage /
      core-instances / agents / settings / recovery-mode）
- [ ] 2.6 **成员端**（me/*）
- [ ] 2.7 **Agent 协议**（register/snapshot/applied/usage/heartbeat）
- [ ] 2.8 **订阅输出** `/sub/{token}?format=`
      xray / singbox / mihomo / v2rayn 四个格式；无效 token 403；
      不兼容组合 422 且指明字段
- [ ] 2.9 **敏感字段审计**：任何响应里不得出现
      明文密码、`token_enc`、`server_key`、`master_key`、私钥

---

## 3. 页面逐个联调（Playwright，真实 UI 操作）

对每个页面记录：HTTP、标题、主内容、控制台错误、未捕获异常、失败请求、
横向溢出、页面级错误横幅。

### 3.1 已确认有问题的页面（重点复核）
- [ ] `/admin/templates` —— 空
- [ ] `/admin/batches` —— 空
- [ ] `/admin/nodes/new` —— 空（新建节点不可用）

### 3.2 管理员页面全覆盖
- [ ] `/admin/overview`
- [ ] `/admin/servers` → `/admin/servers/:id/runtime`
- [ ] `/admin/nodes` → `/admin/nodes/new`、`/admin/nodes/:id`
- [ ] `/admin/node-groups`
- [ ] `/admin/subscriptions` → `/admin/subscriptions/new`、`/admin/subscriptions/:id`
- [ ] `/admin/members` → `/admin/members/:id`
- [ ] `/admin/invitations`
- [ ] `/admin/usage`
- [ ] `/admin/events`
- [ ] `/admin/tasks`
- [ ] `/admin/audit`
- [ ] `/admin/settings`
- [ ] `/admin/batches/:id`

### 3.3 成员端页面
- [ ] `/me/subscription`、`/me/usage`、`/me/credentials`、`/me/status`、`/me/security`
- [ ] 侧栏是否有入口（已知只有 subscription 有）

### 3.4 认证流程
- [ ] 未登录访问受保护页 → 跳 `/login`
- [ ] 登录后回到 `next`
- [ ] member 登录 → 访问 `/admin/*` → 跳 `/me/subscription?denied=`
- [ ] 登出 → 会话失效

### 3.5 交互操作（不只是打开页面）
对每个表单做**真实提交**，验证列表/详情随之变化：
- [ ] 新建服务器 → 出现在列表 → 生成注册码
- [ ] 新建节点（VLESS tcp）→ 出现在列表 → Agent 侧 core 起来
- [ ] 新建节点组 → 挂节点 → 新建订阅 → 绑成员
- [ ] 成员页：编辑 / 停用 / 凭据 / 订阅链接 / 删除
- [ ] 批量操作：勾选 → 预览 → 确认 → 结果
- [ ] 设置页：改保留期 → 保存 → 重载仍生效
- [ ] 改密码 → 旧密码失效

### 3.6 状态覆盖
- [ ] 空状态（无数据的列表）
- [ ] 加载态（骨架屏）
- [ ] 错误态（真实 4xx/5xx 与注入的 503）
- [ ] 分页（多于一页时）

---

## 4. 界面质量检查

- [ ] 4.1 1440px：无页面级横向溢出（宽表格应在容器内滚动）
- [ ] 4.2 390px：同上；侧栏抽屉可开合
- [ ] 4.3 深浅色主题切换后无不可读文本
- [ ] 4.4 表格操作列按钮有可见间距，不粘连
- [ ] 4.5 页面出现 `undefined` / `null` / `NaN` / `[object Object]` 视为缺陷
- [ ] 4.6 键盘可达：Tab 顺序、focus-visible、Esc 关弹窗

---

## 5. 跨端一致性（面板 ↔ Agent）

- [ ] 5.1 面板改节点 → Agent 重启 core → 真实流量可用
- [ ] 5.2 面板停用成员 → 新连接被拒、长连接在 60s 内断开
- [ ] 5.3 面板显示的 `used_bytes` 与账本、与真实流量对得上
- [ ] 5.4 三种客户端（xray / sing-box / mihomo）五协议矩阵
- [ ] 5.5 Agent 离线 → 面板任务显示待下发 → 恢复后回执

---

## 6. 产出

- [ ] 6.1 `docs/archive/verification/ui-api-audit.md`：三张差异表 + 每条 API 的结果
- [ ] 6.2 `docs/archive/verification/ui-inspection.md`：逐页记录 + 截图索引
- [ ] 6.3 **待修复列表**：按 P0/P1/P2 分级，每条含
      现象 / 复现步骤 / 根因（文件:行）/ 影响面 / 建议修法
- [ ] 6.4 修复后回归：同一套脚本重跑，差异为零

---

## 执行顺序

1. 第 1 步（静态对账）——最快，能立刻暴露死链与未接线功能
2. 第 2 步（API 直连）——把后端问题与 UI 问题分开
3. 第 3 步（页面联调）——依赖第 2 步的结论
4. 第 4 步（质量检查）——可在第 3 步中顺带完成
5. 第 5 步（跨端）——需要 Agent 在线，放在最后避免其他测试干扰
6. 第 6 步（产出）

每一步都把原始输出落盘到 `/home/compile/Work/evidence/`，报告只引用文件。
