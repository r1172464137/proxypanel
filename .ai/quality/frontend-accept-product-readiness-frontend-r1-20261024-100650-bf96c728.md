# frontend-accept · product-readiness / frontend · R1（匿名根入口按角色落点）

- **结论**：**ACCEPTED_WITH_IMPROVEMENTS**（`status=success`，无 P0/P1）
- **阶段**：frontend，`validation_revision=1`（R0 为 revision 0）
- **代码输入**：主 `7b1830d50ef48836c9b908a51885dfea702a9d9c`（验收隔离 worktree 同 HEAD、`git status` 干净）
- **本轮生产变化**：`web/src/router/index.ts`（+5/-1）+ `web/src/router/index.spec.ts`（+24），即 A `e41fce0` 经主审消费为 `f38aff4`
- **验收模式**：独立只读；不改源码/测试/断言/设计/状态，不派代理、不 push、不部署，未访问 203:8445/8443、Agentlab/15432 或任何运行中服务，未读写任何 `.state.json`
- **本阶段通过 ≠ 业务验收完成**：18 条完整故事与真实浏览器根路径仍由 web-demo 承接（见 §5）

## 1. 输入与范围核对（独立重算，不复述上游摘要）

| 项 | 核对结果 | 证据 |
|---|---|---|
| 范围 | `git diff --stat 706f0ba..HEAD -- web` = 仅 2 个 router 文件（+29/-1）；`web/src` 其它 0 行 | `git diff 706f0ba..HEAD` |
| 生产源与 A 一致 | `git show e41fce0:web/src/router/index.ts` 与 `index.spec.ts` 的 blob SHA-256 分别等于当前 `fdb5f986…`、`ff9f15f2…` | `sha256sum`、`git show` |
| 上游合并纯净 | `git diff --stat 00ed7da..HEAD` 的非 `.ai` 部分仅为 `scripts/webdemo-host-nginx-verify.sh`、`tests/e2e/webdemo_host_nginx_test.go`（他组 Go/host-nginx 资产，不影响前端根输入）；`web/` 下 0 行差异 | `git diff 00ed7da..HEAD -- . ':(exclude).ai'` |
| source.json 适用性 | 313 个 `web/**` 指纹在当前主仓**逐个重算全部一致（0 missing / 0 mismatch）**，`source=00ed7da…` | `.ai/evidence/frontend-phase/root-route-r1/20261024T094736Z-4b66c0c1/source.json` |
| 依赖/锁未变 | `web/package.json`、`web/package-lock.json` 指纹与 R0 审计输入完全相同 → R0 `npm audit` 各级 total=0 可复用，未重装、未重跑 audit | `source.json`、R0 `audit.json` |
| 隔离环境 | 隔离 worktree `web/node_modules` 不存在，未安装依赖、未产生新派生物（主仓 `web/dist` 为 R1 构建产物，`web/.gitignore` 已忽略） | `ls`、`git check-ignore` |

## 2. 独立核查断言与行为适用性（不是只信 summary）

1. **生产逻辑**（`web/src/router/index.ts:8`）：`{ path: '/', redirect: () => (sessionState.user ? homeRoute() : '/login') }`。
   - 匿名 `/` → `/login`，**不带 `next`**（不再预算成成员页）。
   - 已登录 `/` → `homeRoute()`（admin `/admin/overview`，member `/me/subscription`）不变。
2. **登录后角色落点闭环**（静态复核，非仅看测试名）：
   - `router.beforeEach` 先对 `unknown/loading` 做 `ensureSession()`（`index.ts:199-202`），再判 public；**已登录访问 `/login` 会被 `homeRoute(user)` 改写**（`index.ts:210-212`）。因此「会话已存在但首帧未 bootstrap 时 `/`→`/login`」也会被守卫送回角色首页，不产生"已登录被拦在登录页"的回退。
   - `LoginView.vue:36` 用 `safeLoginNext(route.query.next, homeRoute())`；根入口无 `next` → 走 `homeRoute()` → 角色默认。`safeLoginNext` 的站内/跨站拒绝规则已有既有断言（`lib/clientImport.spec.ts:32-33`）。
   - 显式私有 deep link 的 `next` 保持：`index.ts:214-216` 与既有用例（`router/index.spec.ts` "retains the actual private destination…" 9/9 passed）未变。
   - 权限不降：admin role 守卫、member→`/me/subscription?denied=` 原断言保留并通过。
3. **新增 2 条用例的断言质量**（读原始 `vitest.json` 的 `assertionResults`，非 summary 转述）：两条均 `passed`；断言匿名根落 `/login` 且 `next === undefined`、登录后 admin→`/admin/overview`、member→`/me/subscription`（含中文页名语义由 A 用例与页面标题承担）。缺陷见 §4 P2-1（login 提交路径/homeRoute 真实现被 mock，属表达与覆盖改进，不改判）。
4. **执行证据**：`vitest.json` 独立解析 `numTotalTests=302 / numPassed=302 / failed=0 / pending=0 / todo=0`，`testResults=48` 与仓库内 `*.spec.ts` 实际数量 48 一致（零用例/漏文件门禁通过）；`startTime` 2026-10-24T09:47:37Z 与 run id 吻合；`typecheck.rawexit=0`、`build.rawexit=0`、`vitest.rawexit=0`；`build.log` 2726 modules、`✓ built`，`summary.json.dist` 81 个产物指纹与主仓 `web/dist` **逐个重算全部一致**。
5. **A 旧 checkpoint 的适用性（本轮明确不采信为根路径证据）**：
   - `.ai/evidence/web-demo/A/20261024T084629Z/results.json` 整体 14 PASS / 7 FAIL（US-PR-006/007/009/010 + 3 chain），确实不是全绿；US-PR-001 条目自报 `anonymous root → login`、`landed=/admin/overview`、`anonymousNext=null`。
   - 但**source/资产绑定不可证明**：错误栈指向已删除的 worktree `pi-web-agent-506cb7de…`；`results.json` 行号（54/94/251/255/391）与已提交 `e41fce0` 的同一文件（blob `5b706445…`）不匹配，当前主仓为 `7f352476…`；`build.log` mtime 17:20 晚于 `results.json` 16:47，且其产物名（`LoginView-BuG0mU1_`、`index-CGArvJdj.css`、`OverviewView-C8rOgTAz`）与同源 R1 构建（`LoginView-meKexqaQ`、`index-D12cOk1q.css`、`OverviewView-aHyQZXzr`）不同、`index.css` 体积 70.08 vs 70.07。
   - 结论：按 `verification-evidence-contract` 复用条件 1–2 不成立 → 不把 US001 PASS 当本轮根行为证据，本轮亦不擅自复跑真实浏览器（见 §5 承接）。US-PR-007/009 等红项为测试/fixture 待重跑，未据此推断产品无缺陷。
6. **旧 R0 证据复用边界**：R0 的 300 单测/17 probe/audit 仅用于**未变化的输入与行为**；本轮 302 = 300 + 2 且 `git diff` 证明其它前端源未变，故不重复 542 后端/物理服务/17 probe，也不把旧 300/17 说成"新根路径已跑过"。

## 3. 必要门禁

| # | 门禁 | 结果 | 证据 |
|---|---|---|---|
| G1 | 范围与输入一致（仅 2 router 文件；source.json 313/313 重算一致；worktree=HEAD） | passed | `git diff`、`source.json` |
| G2 | 类型检查实际通过 | passed | `…/20261024T094736Z-4b66c0c1/typecheck.log`、`typecheck.rawexit` |
| G3 | 受影响 + 全量前端单测实际执行、无失败/跳过/零用例 | passed | `vitest.json`、`vitest.log`、`vitest.rawexit` |
| G4 | 生产构建实际成功且产物与源码绑定 | passed | `build.log`、`summary.json(dist)`、主仓 `web/dist` 81/81 |
| G5 | 依赖审计（输入未变，复用 R0） | passed | R0 `audit.json`（total=0）、`source.json` 锁指纹 |
| G6 | 根入口行为：匿名→`/login` 无 `next`；admin/member 角色默认落点；显式 deep link `next` 保留 | passed | `web/src/router/index.ts`、`web/src/router/index.spec.ts`（9/9 passed）、`vitest.json` |
| G7 | 权限/安全边界不降（admin 守卫、member denied、`safeLoginNext` 站外拒绝） | passed | `router/index.ts:199-225`、`lib/clientImport.ts:43-45`、既有 spec 全绿 |
| G8 | 会话/API/其它前端未被改动（本次不新增 API 契约核对） | passed | `source.json`（session/App/main/endpoints 指纹与 R0 相同）、`git diff` |
| G9 | 无新增框架/依赖/生成物；派生物范围可控 | passed | `package.json`/锁未变；仅构建 `web/dist`（已 gitignore，未纳入版本控制） |

## 4. 问题

无 P0、无 P1。3 条旧 P2 **保留**（未擅自抹掉，位置在当前源码仍存在），另新增 2 条 P2。

| 级别 | 位置 | 证据 | 影响 | 建议 |
|---|---|---|---|---|
| P2（旧） | `web/src/views/me/MeSubscriptionView.vue:49`、`web/src/views/me/MeUsageView.vue:87` | 源码现文仍为"数据更新时间：暂未提供" | 设计 §4.10 期望数据更新时间；当前是诚实占位，未显示错误值 | 契约提供采样/帐本时间后再接真值 |
| P2（旧） | `web/src/views/admin/BatchDetailView.vue:95-98` | 汇总徽章直接渲染 `status` 原码（如 `succeeded`） | 与页面其它中文 `StatusBadge` 不一致，仅可读性 | 复用 `StatusBadge`/既有标签映射，不改计数语义 |
| P2（旧） | `web/src/components/ImpactPreviewDialog.vue:57` | `reason` 参与 `scope` 摘要 | 预览后改备注需再预览一次（比服务端 PREVIEW_STALE 更保守，无越权提交） | 若 reason 不属服务端结论摘要，确认后排除 |
| P2（新） | `web/src/router/index.spec.ts:49-71` | 用例 mock 了 `homeRoute`，且"登录后默认"这类通过 `router.push('/login')` 走守卫而非 `LoginView.submit → router.replace(safeLoginNext(...))`；`/` 重定向的 `sessionState.user` 真分支（已登录访问 `/`）无直接断言 | 断言语义仍成立但与真实登录提交路径隔了一层；角色默认落点的真实现未被这两条用例覆盖 | 后续补一条不 mock `homeRoute`、直接驱动登录提交（或断言 `homeRoute()` 真实现）的用例；不改变现结论 |
| P2（新） | `.ai/evidence/web-demo/A/20261024T084629Z/`（证据卫生） | §2.5：worktree 已删、行号与已提交 blob 不符、`build.log` mtime 晚于 results、同源 dist 指纹不同 | 该 run 的 US-PR-001 PASS 无法与受审字节绑定，误引用会形成"假已验根路径" | 证据目录补 `source/asset SHA + 运行顺序` 或明确标注"不可复用"；根路径行为以 web-demo A2 新 run 为准 |

## 5. 未验证与承接（不计为通过）

| 场景 | 承接 | 位置 |
|---|---|---|
| 真实浏览器：匿名 `/` → 登录 → admin 落"工作台"、member 落"我的服务"、显式 deep link 保留（本轮 A 旧 run 不可绑定，未冒验证） | web-demo（A2 `01a16d44-806d-75a9-8640-ad74f45f3d91`） | `.ai/super-run/product-readiness/web-demo.md`、frontend §4.1/§8.3 US-PR-001 |
| 已登录用户首帧访问 `/`（会话未 bootstrap）与刷新保持 | web-demo（US001 最终整文件） | frontend §4.1；`index.ts:199-212` |
| 18 条完整故事整链（UI→真实 Agent→官方 client→ledger、双 Agent/离线 UDP/旧 Agent/SQLite、备份恢复/升级回滚） | web-demo | R0 报告 §5 原承接表 |
| 两种托管（面板托管 + 宿主 nginx 静态）/HTTPS/TLS/DNS/CA | web-demo / deployment | frontend §4.1/§8.4、DEC007 |
| 读屏器审计、截图视觉回归、全平台客户端与 Vision | web-demo | R0 probe `.notRun` |
| 203 独立 8445 预览与用户可见确认 | main session（授权后） | `.ai/super-run/product-readiness/frontend.md` |

## 6. 派生物与清理

- 本轮唯一写入：本报告 + 同目录 `-audit.json` 附件。未改源码/测试/设计/状态，未安装依赖，未建库、未启停服务，未产生需清理的运行资源。
- 主仓 `web/dist` 为 R1 构建派生物（已被 `web/.gitignore` 忽略），本轮只读核验其 81 个指纹，未改动。
- 未外泄任何完整 token/password/私钥；本报告仅记录路径与 SHA-256。

## 7. 放行口径

本阶段（frontend，revision 1）可放行；feature 未完成，业务验收待 web-demo 按 §5 收口。3 条旧 P2 与 2 条新 P2 不阻断本轮，也不代表已修复。
