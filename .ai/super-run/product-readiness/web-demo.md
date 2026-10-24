# product-readiness / web-demo

## 目标与边界

用户在frontend验收放行并看到203独立8445后明确要求“继续按计划推进，加快速度”，本次进入最后web-demo阶段。目标是同一可定位候选的完整18故事成功/关键异常与真实数据链、兼容/多节点/恢复维护证据闭合；不是再做一轮样式开发或重复300局部测试。

**禁止将用户正在看的8445或旧8443当破坏性fixture**。两栈的容器/卷/数据/Agent均保护；测试只用新owner临时资源、随机schema/端口。阶段通过后再按用户授权更新8445，不自动覆盖旧8443。不虚构公网/全平台支持，无能力的必要门禁先报具体阻塞而非假PASS。

## Source Trace

- 主设计 `.ai/design/product-readiness.md` §6、frontend.md §8.3/8.4、backend.md §10与对应业务/升级契约。
- `.ai/prd/core/product-readiness.md`、`.ai/user-stories/core/product-readiness.md`、`.ai/decision-log/product-readiness.md`。
- backend R2 ACCEPTED、frontend ACCEPTED_WITH_IMPROVEMENTS及输出形状补正报告；各阶段候选、原始JSONL/运行数据、清理与SourceSHA，只复用适用部分，不简单拼数字。
- 项目真实入口：`web/tests/*.mjs` + owned env fixture、`tests/e2e/run_data_plane.py`、`run_real_fault.py`、`scripts/backend-test.py -- [filter]`，Go/Vue现有栈；不创建默认React/TypeScript Playwright Test工程或新测试框架。
- 插件：agents/web-demo-dev.md、web-demo-diagnose.md，guides/web-demo/index.md/e2e-testing.md/selector-strategy.md及必要诊断细页，protocols/verification-evidence-contract.md/acceptance-contract.md/diagnostic-report-contract.md。

## Decision Trace

| Decision ID | Applied / Not Applicable / Superseded | Artifact Location | Notes |
|---|---|---|---|
| DEC-product-readiness-001 | Applied | dev/accept | 私有面板，无交易扩展 |
| DEC-product-readiness-002 | Applied | dev/accept | 完整产品旅程，不仅换词 |
| DEC-product-readiness-003 | Applied | dev/accept | admin/member与本人边界 |
| DEC-product-readiness-005 | Applied | 全阶段 | 真实UI/Agent/官方client/traffic/ledger，不拿进程或API200当链路 |
| DEC-product-readiness-006 | Applied | 全阶段 | 候选与相关输入/产物指纹，同版本分片不跨版本拼结果 |
| DEC-product-readiness-007 | Applied | 部署验证 | Go-only panel、外部TLS、两托管，不把preview网关冒host配置 |
| DEC-product-readiness-008 | Applied | UI故事 | 服务方案/订阅链接/两reset术语和影响 |
| DEC-product-readiness-009 | Applied | 实现/验证 | 无新应用框架，复用Go/Vue/mjs |
| DEC-product-readiness-013 | Applied | unknown用例 | checksum真/假/null与门禁同源 |
| DEC-product-readiness-014 | Applied | 并发分工 | 最多6个DS V4 Flash独立闭环，主复查合并；正式accept独立只读串行 |
| DEC-product-readiness-015 | Applied | 交付 | 每阶段用户可看效果 |
| DEC-product-readiness-016 | Applied | 环境保护 | 203隔离8445，旧栈不动 |
| DEC-product-readiness-017 | Applied | 当前预览 | 已授权待验收预览运行，不用预览替代正式验收 |

## Task

| task | goal | agent spec | related documents | deliverable | validation |
|---|---|---|---|---|---|
| dev | 真实故事/维护/兼容场景与失败诊断修复闭环 | `/home/compile/code/t-tools/agents/web-demo-dev.md` | 上述来源；`agents/web-demo-diagnose.md`；`guides/web-demo/index.md`,`e2e-testing.md`,`selector-strategy.md`,`common-failures.md`,`frontend-sync-checklist.md`；verification/diagnostic/requirement contracts | 可复跑项目mjs/Go owned场景、US→断言→实际证据矩阵、遗留11Docker逐项等价关系 | 关键持久业务结果、真实流量SHA与账本、失败/未跑/不支持独立，必要场景不SKIP |
| accept | 独立只读最终故事与证据闭合判定 | `/home/compile/code/t-tools/agents/web-demo-accept.md` | 当前源/计划/全部相关handoff；`acceptance-contract.md`,`verification-evidence-contract.md`,`guides/web-demo/quality.md` | 新独立报告与结构化结论 | 所有必要故事与关键异常有效整文件证据，无P0/P1；不以平均通过率豁免 |

## 并行实现闭环与所有权

A：两角色故事UI与UI→真实Node/client/ledger主链，负责web/tests/product-readiness-e2e.mjs及必要独立POM/helpers、owned运行适配；禁止继续SQL造最终usage冒流量。必须从匿名根入口验证管理员默认落点，不能把“我的服务”改称工作台；当前8445 smoke观察到root/login next=me，作为US001新验证点先定位，必要小修router/Login与最小回归交主明确影响。

B：双Agent、offline/UDP/部分完成/坏节点/实际rollback与重启，专用tests/e2e webdemo_dual_*，只新owned运行环境，不动A共享runner。

C：真实pg_dump/restore、历史/迟报守恒、恢复不复活撤销授权，专用webdemo_restore_*。故障不可施加共用15432或preview。

D：旧Agent/SQLite与当前Agent、官方核心版本切换/回退实际维护，专用webdemo_upgrade_*；不猜旧队列为空，不改原batchID/payload，旧binary从明确source/tag构建，记录校验。

E：声明支持客户端/平台与Vision，复用21本地格及真实TLS/REALITY证据，补缺失；专用兼容场景/报告，不重跑无关物理故障。建立历史DockerClientMatrix实际支持/拒绝/未运行关系，绝不报告旧54全过。

F：真实部署/两托管TLS/安装systemd能力与安全隔离验证，专用webdemo_platform_*；先查可用隔离能力，不能借已有宿主root改原systemd/nginx或使用hostcgroup写入。公有DNS/CA/平台外部前置按实际支持声明判定，无授权能力要具体报告，不伪容器当宿主。

## 执行资源与速度边界

- 6路并行编写/最小编译，不并发修改共享fixture/同源file。所有新helper名/ENV变量有组前缀；只显式stage源码，不Git私有evidence。
- **真实local Xray/PG/Redis进程与故障执行**若复用全host identity snapshot，必须同一 `flock /home/compile/Work/.locks/proxypanel-webdemo-local-runtime.lock` 保护，锁在before snapshot之前取得、自己cleanup后释放；不要整个authoring阶段占锁。避免其它任务正常结束被误判污染原进程。
- remote203新测试资源若做全container snapshot，使用单独remote-runtime锁并先协调；原preview/旧栈任何mutator禁止。所有新增测试端口限127/random，除已授权8445外不额外公开。
- 本机product-readiness专用PG15432仅只读/新schema用途，可复用owner，不用于stop/restore/ENOSPC；故障全部新私有PG/Redis/namespace。根PG继承到本阶段，最后owner验证后统一关闭。
- 未改变的源码/fixture/锁/环境有效证据复用。必要失败先按TEST/DATA/FRONTEND/BACKEND/AUTH/ENV归因，生产修复不归测试角色：主切dev规范或明确委派DS owned代码，不能改弱断言。
- 基线先冻结为706f0ba实际代码（后续.ai交接commit不算业务变化）。最终整合只重开受影响验证，不每路独立跑全300/542。

## 调度检查点

六路基线均为e92c6ba（非.ai业务源码仍706f0ba），各worktree独立；实际执行中的报告未被主采信为PASS，必须合并复查/相关复验。

| 组 | inspectable session | 边界 |
|---|---|---|
| A | 01a16d01-efee-75a9-8640-ad6722136dbb | UI故事/真实主链；已知根入口观察待定位 |
| B | 01a16d01-effa-75a9-8640-ad6d8bece8be | 双Agent/offline/UDP/partial |
| C | 01a16d01-eff7-75a9-8640-ad6ba6c3ca18 | 真实备份恢复/历史守恒 |
| D | 01a16d01-efe5-75a9-8640-ad63001064fa | old Agent/SQLite与core跨版 |
| E | 01a16d01-eff4-75a9-8640-ad69ff316078 | Vision与实际支持矩阵 |
| F | 01a16d01-efeb-75a9-8640-ad64709b3e75 | 部署/安装安全能力与验证 |

## 完成/停止

每US001..018在最终矩阵列出成功、关键异常、证据与未验证，不以17技术probe代替18完整故事。上游旧11Docker套件逐个实际运行或逐断言等价关系；触碰旧fixture的helper不执行并说明真正目标如何覆盖。发现缺设备/权限等外部条件先明确解除条件，不能无限空转。只在正式accept放行且全部active phase与必要验收证据闭合后宣称整个feature完成；否则列实际未验证和责任。
