# product-readiness / frontend

## 目标与完成条件

本次用户“按计划继续”明确承接已完成backend之后的frontend phase。复用已有Vue候选，补齐前端设计的任务导航、管理员/成员状态与安全交互、查询和操作范围、可访问性及最小局部回归；不重写框架，不新增后端接口，不自动进入web-demo。

完成需要：来源门禁通过；API使用已验收后端契约；dev/test运行验证有效；独立只读frontend-accept放行。已有222 Vitest/25浏览器只是准备证据，逐场景核对有效性，不直接推定本阶段完成。独立验收R0/R1发现的问题不得用测试数量掩盖。

## Source Trace

- `.ai/design/product-readiness.md`
- `.ai/design/product-readiness/frontend.md`（主要体验/状态/文件边界）
- `.ai/design/product-readiness/backend.md` §4（唯一API源）
- `.ai/design/product-readiness/backend-contract.json`、`frontend-dependencies.json`（已核对源SHA，派生缓存不是第二源）
- `.ai/prd/core/product-readiness.md`；`.ai/user-stories/core/product-readiness.md`
- `.ai/decision-log/product-readiness.md`；必要技术预研
- `.ai/quality/backend-accept-product-readiness-backend-r2-20261024-051141-3dc57dc3.md`（ACCEPTED与下游承接）
- 基线 `c8af262`，工作分支product-readiness；后端phase已completed，不因前端记录重开。

## Decision Trace

| Decision ID | Applied / Not Applicable / Superseded | Artifact Location | Notes |
|---|---|---|---|
| DEC-product-readiness-001 | Applied | dev/test/accept | 私有面板，无交易/多租户扩展 |
| DEC-product-readiness-002 | Applied | dev/accept | 任务旅程与真实状态，不限于换词 |
| DEC-product-readiness-003 | Applied | dev/test | admin/member，本人数据边界 |
| DEC-product-readiness-005 | Applied | test/accept及下游web-demo | 当前界面行为有证据；完整真实网络旅程仍由web-demo负责 |
| DEC-product-readiness-006 | Applied | dev/test/accept | 同候选/锁/fixture，复用有效证据不拼版本 |
| DEC-product-readiness-007 | Applied | test及web-demo承接 | 宿主nginx、两托管不反转 |
| DEC-product-readiness-008 | Applied | dev/test | 服务方案与个人订阅链接不混称 |
| DEC-product-readiness-009 | Applied | 全阶段 | Go/Vue既有栈，无新框架/Query库/OpenAPI生成器 |
| DEC-product-readiness-014 | Applied | dev/test/accept | 最新用户授权最多6个DS V4 Flash并行实际干活；主大体复核与关键验证，正式accept独立只读串行 |
| DEC-product-readiness-013 | Applied | dev/test/accept | checksum真/假/null互斥，未知不能当已知 |

## Task

| task | goal | agent spec | related documents | deliverable | validation |
|---|---|---|---|---|---|
| dev | 完成已有候选的UI/状态/契约差距闭环 | `/home/compile/code/t-tools/agents/frontend-dev.md` | 上述来源；`/home/compile/code/t-tools/guides/frontend/index.md`、`development.md`、`ui-decisions.md`、`testing.md`、`validation.md`、`testid-standards.md`；runtime-boundaries、code-comment-contract | 最小复用/扩展的组件、路由、API与安全状态；无后端漂移 | 受影响局部测试、typecheck/build，必要真实UI核验 |
| test | 维护有增量价值的状态/并发/解包/无损边界资产并集中执行 | `/home/compile/code/t-tools/agents/frontend-test.md` | 当前前端设计§8、阶段计划；frontend testing/validation；verification-evidence-contract | Expected Test Manifest与有效执行证据，不以整页mock替代故事 | 发现/执行预期用例、0必要skip；JSON/state/payload等业务断言 |
| accept | 独立核查本阶段设计/实现/消费者/运行证据并承接web-demo | `/home/compile/code/t-tools/agents/frontend-accept.md` | 当前设计、后端唯一契约、dev/test handoff；acceptance-contract、verification-evidence-contract、frontend quality | 新只读独立报告与有效结构化结论 | 必要门禁全通过，无P0/P1；不冒称完整feature结束 |

## 当前实现分工（用户并发授权，非新task层级）

主会话负责 `api/endpoints.ts`、`api/types.ts`、`api/http.ts`、router、session、async/polling、通用错误/视觉触控边界及集成；不与独立路重复修改。

- A 管理员工作台/设置/运行：AppShell、RecoveryModeBanner、SetupChecklist、Overview/Setup/Runtime/Settings/Servers/ServerRuntime与recovery/readiness共享查询；不改router/API/shared hooks。
- B 查询/批量责任闭环：Members/MemberDetail、BatchList/Detail、ImpactPreviewDialog、batch/pending/selectionScope/paged，Tasks/Events/Usage过滤与分页；不改全局API/router。
- C 成员使用/安全：Me各页、MeConnect、auth两页、memberService/meUsage/clientImport与必要成员展示组件、CopyField；不改router/API/session/http。
- D 资源/方案表单：NodeEditor、nodeDraft/nodeTemplates、SubscriptionEditor/subscriptionDraft、NodeGroups、Invitations、Subscriptions/Templates反馈；不改共享预览组件/API/router。

基于c8af262隔离commit，互不覆盖owned文件；主审diff后合入。共同约束：无新依赖框架，无backend/SQL/部署变更，无修改原stack；无私钥/完整token截图日志，私有证据仅.ai/evidence；最多5同时任务，不派子代理；源码显式stage，不把私有原始证据提交。各路只测相关高价值边界，整阶段集中一次统一门禁。

## 最小验证责任表

| 场景 | 入口/关键断言 | 当前承接 | 状态 |
|---|---|---|---|
| 导航/首次设置/跨页恢复 | router/AppShell/真实setup：新增页可达、旧别名与锚点有效、成员不查admin、重登不继承旧维护状态 | dev/test；完整浏览器故事web-demo | 核对中 |
| 批量/跨页范围/不确定结果 | q/筛选使preview失效；跨页保留ID且计数真实；CURSOR_INVALID回首清范围；响应丢失复用原key/payload，结果记录持续可查 | dev/test及web-demo | 核对中 |
| 成员/用量/凭据 | 已知/未知/零/不限量逐字段；format不可用无链接；copy不旋转；两reset范围与影响；错误密码不误登出；已有token维护只读/无token本区提示 | dev/test；浏览器准备证据按输入复用，新增缺口web-demo | 核对中 |
| 节点/方案/邀请 | 原生未知键/多证书深等、脱敏空值保持、显式成对换钥、下一周期只读；方案影响预览与历史退役；邀请来源真实 | dev/test；实际客户端连接web-demo | 核对中 |
| loading/empty/error/403/unknown及窄屏键盘 | 现有通用组件与新主路径：持续状态、可重试、不把错误当空；390px动作可达、语义label、焦点归还、触控目标 | dev UI核验/test局部边界，完整18故事web-demo | 核对中 |
| 样式/类型/构建/回归 | `cd web && npm run typecheck`、`npm run build`、`npm test -- <相关spec>`；最终当前候选全前端Vitest可用既有入口 | 主统一执行，独立accept复核 | 待执行 |

## 资源与停止条件

project scripts实际为web/typecheck、build、test(Vitest)，Vue测试沿用@vue/test-utils；不安装React/MSW/Zod/Query样板。浏览器真实证据使用设计批准的现有Playwright mjs与自有环境，不冒Chrome DevTools MCP；如后续动作确需该MCP且不可用则显式阻塞该动作。不得操作192.168.5.203原栈/原Agent、旧.devdb或业务资料；backend专用PG已关闭，仅有必要时验证owner后原目录重启，不reset、不动旧库。

当前用户选择方向明确，needs_user_answer=0。遇后端契约或产品语义新冲突先定位来源并报告，不在前端猜字段/权限；发现输入变化只重开受影响task。完成本frontend phase后停止，剩余web-demo需明确启动。

## 首批实际交付批次

四个Sol medium隔离实现基线c8af262：A `01a16c82-0c10-75a9-8640-ad403c753632`；B `01a16c82-0c01-75a9-8640-ad3f3c12791d`；C `01a16c82-0bfb-75a9-8640-ad3d5a5b4c4f`；D `01a16c82-0c14-75a9-8640-ad42a7553f55`。无第五任务，正式accept待整合后唯一串行派发。主已读取实际router/AppShell/endpoints/async/paged，确认Setup目前redirect、MeConnect与legacyaliases未注册、后台polling不暂停、公共error hints漏新码等真实差距。
主负责共享async资源reset与visibility polling，先红后绿小测试证明旧账号晚回复不可复活、隐藏暂停/无并发/scope清理；已告知A不要平行造polling，返回recovery reset接线点。源码WIP未冻结，不把已有222/25自动累加为新通过数。

## 最新加速授权与当前整合结果

用户最新明确允许6并发，后续worker用已配置wk/workbuddy/deepseek-v4.1-flash，主只负责协调/合并后大体复核及关键门禁；DEC014替代012。先前Sol实现A/B/C/D/A-workbench均已结束，不重做。当前已合候选全285 Vitest：283PASS/2FAIL/0skip；两个失败均SubscriptionsView旧按钮定位trigger undefined，必须保持retired true/false持久反馈断言，仅校准真实语义选择器。类型检查通过，build因set-e未执行。
后续DS闭环：选择器定向修补；复用现有owned fixture的真实前端UI探测（路由/状态/390px/键盘/触控）；共享session晚响应身份竞态核对/修复。最多6不是必须凑满；不得碰后端、旧业务环境或改弱断言，不安装新框架。主复核DS完整diff、执行证据与清理，不能把其自报结果直接当阶段通过。
