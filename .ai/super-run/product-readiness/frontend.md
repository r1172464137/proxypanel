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
| DEC-product-readiness-015 | Applied | 验收后交付 | 每阶段通过后部署203供用户看，同候选与升级安全前置不豁免 |
| DEC-product-readiness-016 | Applied | 203阶段预览 | 用户选8445新独立库/服务，旧8443/Agent不动，不冒旧数据升级验证 |

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

当前5个DS V4 Flash有界实际任务（不填第6名额）：旧删除选择器 `01a16ca9-5cfc-75a9-8640-ad4b882624ab`；真实UI probe/owned fixture `01a16ca9-5cf4-75a9-8640-ad47d0ef4b25`；session身份epoch竞态 `01a16ca9-5cf9-75a9-8640-ad482629742b`；README事实/有限查漏 `01a16cab-4764-75a9-8640-ad4d3ccad268`；StatusBadge/readiness中文语义 `01a16cad-b239-75a9-8640-ad4e8c96ed43`。均新建模型wk/workbuddy/deepseek-v4.1-flash，不resume旧Sol改模型。实现代码主已集中至18df098（285中283PASS，2旧选择器待修），主只做diff/真实API形状与危险scope大体复核、整合与关键验证。全体不得改backend/原环境，UI任务独占本轮fixture，其他不启动服务。

DS首项9a63a6f已主审仅2行选择器差异，retired true/false/API参数/list刷新断言完整；生产testid与ConfirmDialog公开保留footer钩子对应，未改生产，13局部用例/typecheck绿。主合为d8da4d0。未直接宣称原285全绿，待其余DS输出后集中复验。其余4DS（UI probe、session epoch、README/查漏、状态语言）仍在独立owned范围，主不重复实现。前端候选/DS授权已备份远端0617377，工作分支非main。

DS README 1f8a042主核查：Runtime hash实际computed读取/写入route.hash，兼容aliases与链接存在，命令为实际Vue工程；已合。其三风险主判定：Templates inline为设计明确例外，不因统一封装愿望新增包装；generic verified尚无连接语义消费者，不直接当P1；Runtime缺中央get封装与MeUsage失效cursor恢复是实点。新DS单点闭环 `01a16cb3-c807-75a9-8640-ad50931888ca` owns endpoints/BatchList/MeUsage/spec，不碰另一路Runtime状态语言；主最终只换Runtime loader。仍最多4活跃DS，不凑6。DS status语言完成后需保留原summary/evidence技术诊断，不更改来源门禁。

## 阶段交付新增授权 DEC015

用户最新要求每阶段完成后部署203查看效果。当前frontend通过独立验收后，同候选前后端一起部署并报告入口/版本/健康及可见页面；不把未验收WIP标为阶段交付。先由DS只读盘点203现有容器/版本/迁移/nginx与备份条件，无任何原服务变更。新部署授权不豁免材料预检、备份、未知阻断，不自动清库/重建Agent或暂停代理流量；如果现有环境无法无损升级，向用户明确现有数据升级维护与隔离预览两个方案后再执行。阶段状态仍仅dev/test/accept，不另造工作流phase；部署是本次用户授权的验收后交付动作，不自动进入web-demo。

## 冻结与集中验证

全部DS实现已回收并主审：会话dc5→2e21505，UI probe9c86→b95d550，状态语言8ae→cffe087，usage/API95a→531e655，实测进度/触控e049→77a7732；主仅做Runtime typed get最后接线706f0ba。此前285中2旧选择器已修d8da4d0，未弱化retired断言。最后统一验证DS会话 `01a16cc6-af84-75a9-8640-ad56fc1f27cc`，候选706f0ba，只运行typecheck/完整Vitest/build/npm锁audit及17真实UIprobe，不允许改码/断言；源码指纹/清理必须核验。原18df分支局部结果只作来源，不直接累加最终数。
主已核验owner/project/datadir/exeSHA后重启当前专用PG，monitor55 PID1547858/127.0.0.1:15432，evi主frontend-phase/main/owned-pg-restart.json；验证worker只随机schema，不停根PG，阶段结束主按owner精确停。
203新增部署前置只读DS会话 `01a16cc4-3e8b-75a9-8640-ad55fe721ce1`，受DEC015授权但当前只读，不能stop/cp/restart现有stack。frontend正式accept通过后才部署同候选；现有数据升级若缺stop-proof需维护说明/选择，不自动停流或丢队列。当前仅这两个DS任务活跃，未安排第3个正式accept直到必要验证完成。

用户已结构化选择DEC016：203独立HTTPS8445预览，新数据库与新服务；原8443/Agent/业务PGRedis不动。DS只读库存安全字段已主核对并从其误置的private主.ai路径复制到主.ai/evidence/deploy-stage203（原件保留）。不采纳其“仅心跳中断窗口=stop-proof”误解，也不把未发现备份路径写成从未备份。隔离新预览不复制旧授权/计量、不要求操作旧Agent；正式旧数据迁移验证仍独立承接。可验收前构建/暂存新owner发布包，但必须前端accept放行后才启动8445；独立TLS网关不在Go面板内运行，且不得改现有OpenResty/8443。

## 当前候选技术门禁完成，正式frontend accept待结论

候选706f0ba，DS集中验证300 tests/48 files/0fail/0skip，typecheck/build/audit0，17 real UI技术场景全过；主直接解析JSON、probeSHA、cleanup/source-tree一致记录核验，非只信自述。DS的触控备注误把closed drawer的可选navLinkHeight当两个key targets；主用相同probeSHA、显式key IDs和PASS场景确认二者>=44真实断言执行，main-touch-review.json保留原备注与纠正，无重复无关测试。
源码/test/fixture/lock/config当前与706一致，仅.ai元数据WIP；所有实现worker已结束。dev/test现在completed，accept in_progress，revision0。交接.ai/quality/product-readiness-frontend-candidate.md；正式accept必须独立只读、DS模型、不能代放行。验收前203只允许镜像/模板stage：DS预备会话01a16cd3-6388-75a9-8640-ad59d0619b74，禁止compose up/port8445绑定/原服务变更；用户明确选择独立预览，只有ACCEPTED后主授权start。整个feature仍待web-demo。

正式frontend-accept只读DS会话 `01a16cd8-15ad-75a9-8640-ad5bcd81fa00`，revision0，代码706f0ba；只消费有效源码/原始300/17技术证据并独立判定，不代放行。当前另一DS `01a16cd3-6388-75a9-8640-ad59d0619b74` 仅构建203新8445候选image/模板stage，用户DEC016允许隔离新库，禁止启动直到frontend ACCEPTED。无其它活跃工作/源码写入；主当前非.ai源与706一致。
