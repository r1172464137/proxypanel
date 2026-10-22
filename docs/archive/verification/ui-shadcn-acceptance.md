# shadcn 重写验收报告

分支 `ui-shadcn`，从 `main` 的 `f7e766a` 切出。
拓扑：面板容器（nginx + Go，真实 PostgreSQL/Redis）+ WSL 真实 Agent（0.1.1）+ 真实 Xray 26.3.27（5 个节点）。

---

## 一、改动规模

| 类别 | 内容 |
|---|---|
| 提交数 | 8 个（s0 基础设施 → s7 前后端分离） |
| 重写文件 | 15 个通用组件 + 27 个视图 + 1 个新增 DataTable |
| 零改动 | `src/lib/`（14 文件 2000 行）、`src/api/`（3 文件 1080 行） |
| 技术栈 | Tailwind v4 + shadcn-vue 2.8.2 + reka-ui + lucide + sonner + vee-validate/zod + @tanstack/vue-table |
| 产物 | JS 301 KB（gzip 104.5 KB）、CSS 67 KB（gzip 12.4 KB） |

**接口未变**：15 个组件的 props/emits/slots 用脚本逐文件比对 HEAD，14 个完全一致，
1 个（StatusBadge）只是把内联字面量提成未导出的 `Tone` 别名（类型等价）。
因此 27 个视图的业务逻辑无需对应改动。

**业务逻辑零丢失**：27 个视图的 API 调用用脚本比对，无一处缺失；
4 个表单页的 `<script setup>` 逻辑与改动前逐行一致，仅新增 import。

---

## 二、第 1 层 · 单元与组件测试

```
Test Files  27 passed (27)
Tests       164 passed (164)
```

- 原 128 → 164（+36）
- 保留零改动：`lib/*.spec.ts` 71 用例
- 新增/重写：AppModal、ConfirmDialog、CopyField、JsonEditor、
  ImpactPreviewDialog、QrPanel、StatusBadge、AppIcon、AppShell、
  NotFoundView、MeSubscriptionView
- **原则**：全部断言用 `getByRole` / `getByLabelText` / `getByText`，
  不断言自造 CSS 类名，以后再换 UI 库测试不会全废

`npm run typecheck` 通过、`npm run build` 通过。

---

## 三、第 2 层 · 前后端联调（真实 API）

`scripts/integration-test.py`：

```
总计 64 项，通过 64，失败 0
```

覆盖：认证与会话（4）、资源 CRUD（20）、批量预览→提交→幂等重放（5）、
批量列表与详情一致性（4）、运维接口含 from/to 筛选（13）、成员端 5 接口
+ 四格式导出（11）、CSRF 与参数校验（5）、清理（4）。

---

## 四、第 3 层 · 真实浏览器 + 真实节点

### 4.1 真实 Web（Playwright + 真实面板容器）

| 检查 | 结果 |
|---|---|
| 21 个页面（1440×900） | **21/21**：HTTP 200、主内容非空、0 控制台错误、0 横向溢出 |
| 390×844 移动端 | **10/10** 无溢出 |
| 节点编辑器 5 个模板 | **5/5** 落对协议与端口 |
| REALITY 密钥生成 | WebCrypto 真生成 X25519：私钥 43 / 公钥 43 字符 |
| SS2022 密钥生成 | 密码 24 / 服务端密钥 24 字符 |
| 字段级校验 | 4 处 `aria-invalid` + 就地错误文案 |
| 高级 JSON 默认折叠 | 新建时 0 个 JSON tab，点「需要直接改 JSON？」后出现 |

### 4.2 三客户端 × 五协议真实流量

三客户端（Xray 26.3.27 / sing-box 1.14.3 / Mihomo 1.19.33）× 五节点
（SS-aead / SS2022 / VLESS-tcp / VMess / VLESS-ws-tls），
每个格子拉取 **69,632 字节**并与直连 SHA-256 **逐字节比对**。

```
xray      ss-aead       PASS    sing-box  ss-aead       PASS    mihomo  ss-aead       PASS
xray      ss2022        PASS    sing-box  ss2022        PASS    mihomo  ss2022        PASS
xray      vless-tcp     PASS    sing-box  vless-tcp     PASS    mihomo  vless-tcp     PASS
xray      vmess         PASS    sing-box  vmess         PASS    mihomo  vmess         PASS
xray      vless-ws-tls  PASS    sing-box  vless-ws-tls  PASS    mihomo  vless-ws-tls  PASS

矩阵: 15/15
```

**含 WS+TLS 三客户端全通** —— 这是 D5（ALPN h2 泄漏）修复的正面验证。

### 4.3 撤回时延

关闭成员的 `proxy_enabled`：

| 指标 | 实测 | 预算 |
|---|---|---|
| 新连接被拒 | **+2.06s** | 60s |
| 长连接断开 | **+3.26s** | 60s |

撤回后凭据**轮换**（服务端 `d8d7640b…` → 客户端旧值 `78c74109…` 失效），
重新拉取订阅并重建客户端后矩阵恢复 15/15 —— 符合「member-level revoke
轮换凭据」的设计。

### 4.4 账本一致性

20 次 69,632 字节请求（应用层 1,392,640 字节）：

| 指标 | 值 |
|---|---|
| 账本 delta 下行 | 1,397,260 |
| 比值 | **1.0033** |
| 条目数 | 5 |
| 判定 | PASS（覆盖应用层，0.33% 协议开销，无重复记账） |

> 首次测量得到 0.75 是因为采样窗口未追平，再等 90 秒后账本追平至 1.0033。
> 这属于采样延迟，不是丢账。

### 4.5 离线与队列回放

用 `docker pause` 暂停面板容器模拟不可达（可逆，不动配置）：

| 阶段 | 观测 |
|---|---|
| 离线期间服务 | **5/5** 请求仍成功 |
| 离线期间队列 | `batches=0` → `batches=1 samples=1` 增长 |
| Agent 日志 | `upload cycle failed … context deadline exceeded` |
| 恢复后 | **6 秒内排空**，`1 acknowledged, 0 retained (queue 0)` |

### 4.6 采样可见性（D4 修复验证）

面板 `agents.metrics` 实际收到：

```
sampling_errors    = 0
sampling_retries   = 0
sampling_instances = 5
sampling_counters  = 10
```

即 `sampling_error` 不再是只进日志，而是真的上报到面板。

---

## 五、前后端分离（阶段 7）

### 5.1 同源模式（默认，行为不变）

未配置任何新变量时，实测响应**一个 CORS 头都没有**，Cookie 仍是
`SameSite=Strict`。与改造前一致。

### 5.2 跨域模式

临时配置 `PANEL_ALLOWED_ORIGINS=https://panel.example.com` 与
`PANEL_COOKIE_SAMESITE=lax`：

| 场景 | 实测 |
|---|---|
| 白名单命中 | `Allow-Origin` 精确回显 + `Allow-Credentials: true` + `Allow-Headers` 含 `X-CSRF-Token`/`Idempotency-Key` + `Max-Age: 600` + `Vary: Origin` |
| 白名单未命中 | 只有 `Vary: Origin`，**无** `Allow-Origin` |
| `OPTIONS` 预检 | **204**，不进入业务逻辑与鉴权 |
| 登录响应 | `Set-Cookie: …; Secure; SameSite=Lax`（按配置生效） |

### 5.3 安全设计

- `Allow-Origin` 绝不使用 `*`（带 Credentials 时浏览器会拒绝整个响应）
- 白名单里写 `*` 会被显式忽略，避免「配了却不生效」的静默失败
- `SameSite=None` 且 `PANEL_DEV=true` 时**进程启动失败** —— 浏览器会静默丢弃
  这种组合的 Cookie，表现为「登录 200 但下一个请求 401」，极难排查
- 后端新增 13 个单测（CORS 9 + SameSite 4）

---

## 六、测试中发现并修正的问题

| 问题 | 性质 | 处理 |
|---|---|---|
| 两个 Agent 同时监听 20105/20106（旧的连 18443、新的连 8443），SO_REUSEPORT 导致连接随机分给旧进程，WS+TLS 报 pin 不匹配 | **我的测试环境问题**，不是产品缺陷 | 停掉旧 Agent，端口唯一后 15/15 |
| 首次账本比 0.75 | 采样延迟，非丢账 | 再等 90 秒追平至 1.0033 |
| lab-wstls 节点无内联证书导致 xray 格式 422 | **正确的显式拒绝** | 补内联证书后四格式全通 |

---

## 七、已知遗留（不在本轮范围）

| 项 | 说明 |
|---|---|
| D6 · 批量停用永久关闭 `proxy_enabled` | 后端缺陷，重写不涉及。已在 `docs/archive/verification/integration-findings.md` 登记 |
| `DELETE /subscriptions` 实为归档 | 设计选择，但 `DELETE` 语义不符，界面应写「归档」 |
| 视觉方向 | 采用 shadcn 默认 Neutral；珊瑚橙仅作 `--chart-1` 与强调色 |

---

## 八、合并条件

全部满足，可合并 `main`：

- [x] 三层测试全绿（164 单测 / 64 联调 / 21 页面 + 15 流量格）
- [x] 业务逻辑零丢失（脚本核对 API 调用与 script 逻辑）
- [x] 组件接口零变更
- [x] 前后端分离同源模式行为不变、跨域模式实测可用
- [x] 1440 / 390 无溢出、0 控制台错误
- [x] 文档更新（计划书、组件契约、部署教程、env 示例）
