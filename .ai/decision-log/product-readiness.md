# product-readiness Decision Log

本轮目标：将 proxypanel 改造成可部署、可交付、可长期使用的产品级私有面板。此文件是本轮工作流决策记录，不表示实现或验收已完成。

## Active Decisions

| ID | Level | Topic | Decision | Rationale | Decided By | Source | Affects | Reopen When | Supersedes |
|---|---|---|---|---|---|---|---|---|---|
| DEC-product-readiness-001 | D0 | scope.product-positioning | 产品级私有面板；不增加支付、订单、退款、返利或商城 | 用户在三种商业化范围中明确选择“产品级私有面板” | user | 本轮结构化范围问题的明确回答 | prd/design/task/test | 用户明确要求增加收费交易业务 | — |
| DEC-product-readiness-002 | D1 | outcome.complete-journeys | 同时改进文案、导航、开通与运维流程；以可部署、可交付、可长期使用为目标，不限于换词或换皮 | 用户要求修正文案与产品设计，并完成成品后的完整链路测试 | user | 本轮产品改造请求及范围选项说明 | prd/design/task/test | 用户明确缩减为仅文案修改 | — |
| DEC-product-readiness-003 | D1 | actors.admin-member | 保留管理员与成员两类使用角色，分别完成管理员交付和成员使用旅程 | 用户接受的范围选项明确包含管理员和成员旅程，不新增多租户或细粒度角色体系 | user | 本轮范围选项预览及明确选择 | prd/design/test | 用户要求新增角色或租户边界 | — |
| DEC-product-readiness-014 | D2 | execution.subagents | 上限6：后续实现、测试与专项检查交已配置DeepSeek V4 Flash；主会话负责分工、合并后大体复查与关键验证；正式accept仍独立只读串行 | 用户明确要求DS优先加速，并提醒其可能疏忽，故子代理自述不直接作为通过证据；不凑数重复工作 | user | 最新“允许6个子代理、让deepseekv4f干、干完主大体查”明确指令 | design/task/test/frontend | 用户改变模型、并发或复核要求 | DEC-product-readiness-012 |
| DEC-product-readiness-005 | D1 | acceptance.end-to-end | 实现完成后执行真实端到端链路，覆盖前后端、Agent、官方核心、真实客户端与账本；未运行/不支持/失败独立记录 | 用户明确要求完整链路测试，并提供现有环境 | user | 本轮任务原文；此前完整测试要求 | prd/design/task/test | 用户明确改变验证范围 | — |
| DEC-product-readiness-006 | D2 | delivery.evidence-gates | 将遗留补丁视为候选而非已交付；先核验整合安全/授权/计量基础，再在同一候选版本验证产品旅程，禁止跨版本拼接通过数字 | 多个独立副本及补丁尚未统一，存在缺失/过期交付和未收尾失败 | agent | 当前主仓库 3435f1d 与上一轮暂停状态；本轮事实调研 | design/task/test | 整合版本和完整验收已形成可靠证据 | — |
| DEC-product-readiness-007 | D1 | deployment.host-nginx | 面板容器不运行 nginx；TLS 由宿主 nginx 终止；同时支持面板托管前端及宿主托管静态前端 | 保留用户已明确选择的部署方式，本轮产品化不反转该架构 | user | 本会话此前 Dockerfile 与两种前端托管的明确选择 | prd/design/task/test | 用户明确要求改变部署架构 | — |
| DEC-product-readiness-008 | D2 | language.service-vs-link | UI 以“服务方案”表示授权规则，以“订阅链接”表示成员个人地址；不因改文案新增收费实体或自动重命名兼容契约 | 跨角色复用“订阅”造成误解，术语必须在设计、实现、测试中一致；这是表达选择，不改业务语义 | agent | 源码盘点、本轮文案改造授权 | prd/design/task/test | 实际使用验证发现词义仍混淆 | — |
| DEC-product-readiness-009 | D2 | architecture.reuse-stack | 保留 Go/Vue 与现有交互基础，新增应用框架数量为零；安全依赖升级独立验证 | 现有栈可承载目标，重建框架会扩大成本并增加回归风险 | agent | 本地依赖与可复用模块研究 | design/task/test | 出现可复现的既有栈能力阻塞且局部方案不能解决 | — |
| DEC-product-readiness-013 | D2 | readiness.checksum.unknown | 新增 readiness 字段 checksum_ok 规范为 boolean/null 三态：已核验成功 true、已知不符 false、无法核验 null；同源 status/checks/blockers 仍阻止不安全升级 | 主会话作为设计/API责任人裁决未发布诊断字段的表示细化，避免把无法核验伪称已知结果；已知消费者已处理null，不改变业务、权限、安全门禁或既有legacy兼容承诺。影响后端、前端与验收，故跨阶段入账 | agent | R1独立报告R1-C1；backend设计§4.2；API与CLI既有unknown语义 | design/task/test/frontend | 出现已发布消费者的固定非空类型约束，或用户明确要求两态表示 | — |
| DEC-product-readiness-015 | D2 | delivery.stage-preview | 每个阶段验收通过后部署到203供用户查看，报告入口/版本/验证结果；保持前后端同候选，部署前核对备份与只读升级门禁 | 用户希望及时看到阶段效果；新增部署授权不等于清库、丢队列或绕过迁移证明，需停流/破坏性恢复时先说明 | user | 最新“每完成一个阶段部署到203，我也想看看效果”明确建议 | task/test/delivery | 部署需要影响现有流量/资料且未具备安全前置，或用户改变预览环境 | — |
| DEC-product-readiness-016 | D2 | delivery.preview203-isolation | 阶段预览选择203独立HTTPS 8445与独立新库，现有8443/数据库/Agent不动；不把空库预览当旧数据升级验证 | 用户在“独立预览8445/升级现有8443”结构化选择中明确选择前者，避免本次查看效果影响旧环境 | user | 本轮203部署方式明确回答 | task/test/delivery | 用户明确改选原环境升级或要求复用旧数据 | — |

## Resolved Questions

| ID | Topic | Resolution | Decision ID | Source |
|---|---|---|---|---|
| Q-product-readiness-001 | scope.product-positioning | 选择产品级私有面板，不进入轻量收费或完整交易平台 | DEC-product-readiness-001 | 本轮结构化问题回答 |

## Deferred Questions

无。当前尚未采用任何需要用户另行决定的收费、角色、配额规则或兼容承诺。

## Superseded Decisions

DEC-product-readiness-012已由014替代；原五并发Sol实现规则不再用于后续调度，保留其历史授权：

| DEC-product-readiness-012 | D2 | execution.subagents | 上限5：Sol可在隔离分支实现独立模块并只读审阅；DSV4F机械取证/明确小修；主会话守账本迁移、安全语义和合并，按影响定向验证 | 用户明确要求加速并允许子代理实际干活，消除主会话串行实现瓶颈 | user | 最新“加快推进/缩短测试时长/开子代理干”指令 | design/task/test | 用户改变并发、模型或实现权限 | DEC-product-readiness-011 |

DEC-product-readiness-011已由012替代；保留5并发与模型选择，允许隔离实现、非仅只读。原决定：

| DEC-product-readiness-011 | D2 | execution.subagents | 主会话负责核心实现与整合；GPT-6.1 Sol只读专项审阅，DSV4.1 Flash做机械清点；同时不超过5个，结论仍需独立复核 | 用户在确认模型后明确再增加2个并发，最新上限为5 | user | 最新增加2个子代理的明确指令 | design/task/test | 用户改变模型/并发/职责限制 | DEC-product-readiness-010 |

DEC-product-readiness-010已由011替代，仅并发3→5；模型/证据要求不变。原决策：

| DEC-product-readiness-010 | D2 | execution.subagents | 主会话负责核心实现与整合；GPT-6.1 Sol只读专项审阅，DSV4.1 Flash做机械清点；同时不超过3个，结论仍需独立复核 | 用户明确要求按能力分工并选择GPT-6.1 Sol，原单一模型限制不再适用 | user | 最新并行审阅请求及模型选择回答 | design/task/test | 用户改变模型/并发/职责限制 | DEC-product-readiness-004 |

DEC-product-readiness-004已由DEC-product-readiness-010替代；仅模型与并行只读审阅安排更新，并发上限/主会话负责/证据要求保留。原决策：

| DEC-product-readiness-004 | D2 | execution.subagents | 子代理使用已配置的 DeepSeek Flash；同时运行不超过 3 个；子代理自述不作为独立验收证据 | 用户明确要求低成本分工、最多 3 个并发，并提醒不能盲信子代理结论 | user | 本轮任务原文；此前已确认对应模型 wk/workbuddy/deepseek-v4.1-flash | design/task/test | 用户改变并发或模型限制 | — |

本轮仅创建新候选需求，未覆盖或发布正式 PRD；旧文档与候选目标的差异必须显式标注。
