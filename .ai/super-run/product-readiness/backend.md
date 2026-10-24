# product-readiness / backend

## 目标与范围

以Go/Vue既有栈形成可信后端候选：安全、身份与恢复门禁、周期/账本/任务、Agent执行与计量、协议投影、产品所需查询及升级预检。主会话执行dev/test，accept串行派发只读验收。不改现有部署，不运行前端或web-demo阶段。

完成必须同时满足：设计来源门禁通过；本阶段必要行为测试通过；统一候选构建/依赖/迁移可定位；独立accept放行。当前仅开始dev，任何checkpoint通过不等于本phase完成。

## Source Trace

- `.ai/design/product-readiness.md`
- `.ai/design/product-readiness/backend.md`
- `.ai/design/product-readiness/frontend.md`（消费方边界，不实施其资产）
- `.ai/prd/core/product-readiness.md`
- `.ai/user-stories/core/product-readiness.md`
- `.ai/decision-log/product-readiness.md`
- `.ai/tech-research/product-readiness.md`
- `.ai/quality/product-readiness-design-reader.md`
- `/home/compile/Work/evidence/full-review-3435f1d/product-readiness-input-manifest.json`（输入来源，不是测试通过证明）
- 基线3435f1d，分支product-readiness；当前Linux工作区`/home/compile/Work/proxypanel`。

插件Rust/Cargo模板不适用；开发编译用真实Go工具链。后端验证统一交付项目本地`uv run scripts/backend-test.py -- [选择]`，不回退插件Cargo脚本。先准备工具与无数据库副作用的入口安全checkpoint；实际数据链整合仍按存储→身份/门禁→Agent→协议→产品读模型推进。独立安装/静态护栏先行不改变迁移或授权依赖。

## Decision Trace

| Decision ID | Status | Task/Item Location | Notes |
|---|---|---|---|
| DEC-product-readiness-001 | Applied | dev/test/accept | 私有面板，无交易扩展 |
| DEC-product-readiness-002 | Applied | dev/accept | 支撑完整交付，非只改词 |
| DEC-product-readiness-003 | Applied | dev/test | 仅既有管理员/成员 |
| DEC-product-readiness-012 | Applied | accept | 用户新授权：Sol专项审阅/DeepSeek清点，总并发≤5；正式accept仍独立串行 |
| DEC-product-readiness-005 | Applied | test/accept及web-demo承接 | 后端行为先验，完整用户链路未完成不能报完成 |
| DEC-product-readiness-006 | Applied | dev/test/accept | 每批内容指纹；候选不盲合；最终统一回归 |
| DEC-product-readiness-007 | Applied | dev/test | 宿主nginx与双托管不反转 |
| DEC-product-readiness-008 | Applied | dev | 不按UI术语改底层兼容实体 |
| DEC-product-readiness-009 | Applied | dev/test | 保持Go，依赖升级独立验 |

## Task

| task | goal | agent spec | related documents | deliverable | validation |
|---|---|---|---|---|---|
| dev | 统一后端实现与必要高价值局部回归 | `/home/compile/code/t-tools/agents/backend-dev.md` | 上述设计/需求/决策/预研；`/home/compile/code/t-tools/guides/core/quality.md`、`guides/backend/index.md`、`guides/backend/development.md`、`guides/backend/testing.md`、`guides/backend/validation.md`、`protocols/runtime-boundaries.md`、`protocols/code-comment-contract.md` | 按真实路径选择并复核候选；不复制旧SQL策略/重复门禁；项目Go入口与增量清单 | 每checkpoint编译、必要定向回归与内容指纹；最后全阶段回归 |
| test | 独立场景资产与集中执行闭环 | `/home/compile/code/t-tools/agents/backend-test.md` | 后端/主设计与所有上游handoff；`/home/compile/code/t-tools/protocols/backend-test-execution.md`；`guides/backend/testing.md` | 隔离PG/核心/升级故障场景及Expected Test Manifest | 真正选中且执行预期用例，修前红/修后绿，必要场景不可SKIP |
| accept | 独立只读核查本phase并交接用户链路 | `/home/compile/code/t-tools/agents/backend-accept.md` | 本计划、设计与证据；`/home/compile/code/t-tools/protocols/acceptance-contract.md`、`protocols/verification-evidence-contract.md`、`guides/backend/quality.md` | 当前revision的独立验收报告 | 不以自述或静态检查代替必要行为验证 |

上述相对的插件资源均相对`/home/compile/code/t-tools/`，执行当前角色时读取对应全文；不预读后续全部角色。

## 验证责任与交接

| 场景/验收目标 | 入口与关键断言 | 承接 | 结果与证据 |
|---|---|---|---|
| 静态文件边界 | api/static：根外符号链接拒绝、根内相对链接可用、FIFO不阻塞、未知API不变SPA | dev | CP01通过：`.ai/quality/product-readiness-backend-cp01.md` |
| 独立Agent安装 | e2e/standalone-agent：真实脚本函数+临时文件+特权桩；二进制/状态/CA匹配、危险目录拒绝、保留配置不改单元/CA | dev | CP01局部回归通过；不是实机systemd安装验收 |
| 登录/邀请/恢复/会话并发 | api/store场景，普通写冻结与身份救援例外、事务原子性 | test/accept | 未执行 |
| 周期/迟报/移机/旧回执/幂等 | 真实隔离PG及独立oracle，字节/周期/授权守恒 | test/accept | 未执行 |
| Agent停机/故障/收紧/计量 | 真实固定核心、临时状态，最终采样与已知到期有界生效 | test/accept | 未执行 |
| 只读升级与一致证据 | 空库/旧库/不足资料/校验和、输入不改、失败文件回滚 | test/accept | 未执行 |
| 实际配置能力 | TLS/REALITY/投影回归与握手、无损编辑，既有断言不得弱化 | dev/test | 未执行 |
| 统一Go回归与依赖 | 统一候选快照、全范围Go回归/扫描，跳过单列 | test/accept | 未执行 |
| 浏览器两条用户旅程、两种托管与完整客户端矩阵 | 主设计§6和frontend§8.3，真实UI→节点→流量→账本 | 后续web-demo | 尚未实施，不纳入本phase通过数字 |

## 恢复与停止条件

- 状态唯一源`.ai/super-run/product-readiness/.state.json`；dev/test/accept责任不交给实现子代理。
- 每次恢复核对来源、WIP和证据；代码变化只重开相关验证。未提交树不能只用HEAD当内容指纹。
- 不触碰既有面板/Agent/数据库。故障注入先证明隔离资源归属；缺必要工具先准备，确实无权限才blocked。
- 无法证明旧队列/历史、规则冲突、权限缺口不能用猜测绕过；相关项保持失败/阻断。
- 当前清单是增量注册范围，不冒充整个仓库测试覆盖；完整门禁由test补齐并明确执行。

## 进度

- 设计完整性门禁passed；当前dev已开始改动实际代码，尚未通过本phase验收。
- checkpoint入口已交付：项目Go runner与两项增量注册。官方uv wheel经SHA256校验后装入用户目录；未修改系统Python。首次默认sh和缺ensurepip两次工具准备失败未执行测试，不计测试结果。
- 修前证据：`.ai/evidence/backend-test/20261023T123544Z-5cf2861b/result.json`。四项关键已知缺陷确实失败，无遗漏/跳过预期测试。危险目录负例已避免将命令不存在127当有效拒绝。
- CP01修后14个预期顶层用例通过，0跳过；测试/runner与修前指纹一致。全仓Go构建、脚本语法与Windows API交叉编译通过（不代表Windows实机运行）。汇总见`.ai/quality/product-readiness-backend-cp01.md`。
- CP01本地可回退提交`2a5d1cf`，没有推送远端。
- CP02周期边界已通过16个顶层用例；与CP01合计当前注册30个顶层用例0跳过，全仓Go构建通过。修前四类真实失败、修后断言未改，见`.ai/quality/product-readiness-backend-cp02.md`。
- CP02本地提交`3549fea`，没有推送远端。
- CP03核验computeAdvance规则切换：旧测试`TestComputeAdvanceAppliesTimezoneEditAtFirstBoundaryExactly`把2026-03-15JST当2026-04-01UTC结束后的新起点，实际重叠当前周期。依据既有`docs/design/product.md`§8.3、PRD§4.4与后端设计§6.4，修前先纠正此错误期望为2026-04-01UTC并保留全部输入/断言/用例，另加五种衔接子例及不重叠/纯函数不改输入约束。不是弱化断言；待红证据后再修生产代码。
- CP03修前证据：`.ai/evidence/backend-test/20261023T130722Z-a5656c26/result.json`；纠正期望后的原测试与新增不重叠测试均真实失败。当前生产修改仅让computeAdvance直接使用共享period.Transition，第一段从oldEnd开始，后续按新日历定位，避免无意义的store同义包装；详细设计私有函数草图已同步实际落点，HTTP契约未变、派生指纹已刷新，设计来源门禁通过。
- CP03修后39个当前注册顶层用例通过（含前两批），0遗漏/跳过，store测试/manifest/runner相对修前不变，全仓Go构建通过。证据见`.ai/quality/product-readiness-backend-cp03.md`。
- CP03本地提交`7a754f9`；仍未推送/部署。
- 当前准备独占本地PG17测试实例：仅复用`/home/compile/sdk/pkg/usr/lib/postgresql/17/bin`二进制，使用全新`/home/compile/Work/.devdb-product-readiness`，符合既有fixture仅允许的127.0.0.1:15432/proxypanel；启动前检查空闲，保持fsync=on。不得调用旧`/home/compile/Work/scripts/devdb.sh`的reset/bootstrap/stop，不触碰其`.devdb`或Redis。凭据仅保存在新私有目录test.env（0600），运行证据只存非秘密owner/端口/路径。
- 独占PG已就绪：`.ai/evidence/backend-test/owned-postgres-environment.json`；仅新目录，fsync=on。阶段完成后仅按owner凭据停本实例，不影响旧`.devdb`。
- CP04当前实现OpenReadOnly，共用连接池构造但不自动Migrate、不加载密钥；连接默认read_only、Migrate/inTx拒绝、secret helper明确拒绝，普通Open保持迁移。真实PG开发探针在`.ai/evidence/backend-test/readonly-probe/main.go`：独占随机schema、两条池连接、SQLSTATE25006拒写、表数0→0、普通Open正对照、finally清理。它是开发者验证，不冒充新增业务场景测试/完整预检；正式场景归后续test。
- CP04真实PG探针通过：只读表数0→0，两条连接强制只读、写被25006拒绝，普通Open正常迁移32表，探针schema清理成功；39定向回归及Go构建通过，详见`.ai/evidence/backend-test/readonly-checkpoint.json`。
- 随后存储包51用例0遗漏/跳过但一条旧场景oracle失败。编排诊断/决定见`.ai/quality/product-readiness-cp04-test-semantics.md`：交backend-test角色仅校准该资产，输入与其他断言保留，独立UTC期望取代调用生产Containing自证。作者编译已通过，未执行测试；现在切回执行编排复测。整体test任务仍pending，不拿局部校准充当其完成。
- CP04收口通过：存储51 + 周期16 + 静态7 + 安装器7 = 81个不重复顶层用例，0跳过/遗漏，全仓Go构建通过。与store/cycle9个重叠未重复计数；汇总`.ai/quality/product-readiness-backend-cp04.md`。不是全仓/E2E通过。
- 预检材料、历史表/迁移、refreshMemberPeriod和迟报路径仍未完成；当前纯推进计算与只读构造不宣称账本守恒或升级完成，也不部署该分支。
- 本checkpoint为FS/安装函数局部回归，不是数据库业务或真实systemd安装场景。后续存储、身份、Agent、协议及产品接口仍待实现；test/accept均未开始。

## 最新调度授权

用户已明确允许Sol审阅与DS机械工作，采用DEC-product-readiness-012。当前使用主实现+独立并行只读专项审阅，覆盖原super-run“除accept不派子代理”的限制；这是用户授权的编排调整，不冒充原入口的纯串行执行。

当前最多五个任务（按用户最新指令，禁止并发第6个）：
- Sol A / 周期推进：`01a16910-3fb3-75a9-8640-accfc457d64a`，codex/gpt-6.1-sol，只读。
- Sol C / 入口与只读调用链：`01a16910-3fa8-75a9-8640-accb70c87227`已返回3候选，不当作确认；集合`.ai/quality/checkpoint-review-candidates.json`。
- DSV4F / Agent状态源码清点：`01a16910-3faf-75a9-8640-accc63f8f0e1`已完成；主会话检查其保留branch的HEAD确为fe4bf8e。只采纳源码事实，不把其未验证项目当PASS。
- Sol / 升级证据链执行缺口：`01a16916-5f67-75a9-8640-acd25df324bc`，codex/gpt-6.1-sol，只读建议。
- DSV4F / 存储候选完整取证：`01a16916-5f1b-75a9-8640-acd1a560bf91`，仅可写私有证据目录，不改源码/索引/迁移。
- 两个任务结束释放名额后，新开单候选独立验证：CPREV-002=`01a1691b-5cd5-75a9-8640-acd724e35bc8`，CPREV-003=`01a1691b-5ccd-75a9-8640-acd4b5c27f23`。CPREV-001待名额；当前仍不超过5。核验前不改受审文件，不把专项审阅报成完整t-review或阶段验收。

正式accept仍单独串行，不让finder结论代替验收。本轮只审已提交3435f1d..fe4bf8e；主工作区未提交的新Agent离线证据DTO不在其中。专项审阅只采用A/C角度，DS另做状态结构盘点，**不是完整t-review**；正式完整审查留阶段收尾，不谎称A/B/C/D/E/Gaps已全覆盖。

## 最新提速实施批次（DEC-product-readiness-012）

用户明确要求子代理实际干活。主会话仍负责账本/迁移/合并，独立模块在隔离分支实现，正式accept不被实现者自述替代。原单phase/只读调度限制已被最新授权覆盖；前端分支可并行准备，但不计入backend完成或跨端验收。

当前5任务：
- Sol协议/客户端实现：01a16b3d-d407-75a9-8640-acdd16a08d2d，仅protocol/clientconfig/xrayconf+限定选择清单，不能改store/api/go.mod。
- Sol成员端实现：01a16b3d-d415-75a9-8640-ace2202995ac，仅成员views/lib/附加types，不改管理端/全局http/后端。
- DSV4F指纹小修：01a16b3d-d410-75a9-8640-ace073ef9b4a，仅runner脚本；主会话暂停同文件修改。
- Sol单候选CPREV-004：01a16b3d-d3fe-75a9-8640-acda2ae1f9a1，只读核验短周期即时用量。
- Sol单候选CPREV-005：01a16b3d-d40b-75a9-8640-acdf124d4b3f，只读核验跨月回拨。

后续实际进展：
- DS指纹修复75e3964已独立核对并cherry-pick为1f7efa8；主通过统一uv复验，service内容/删除均影响指纹。
- CPREV-003入口oracle修复已验证并提交daf3c7d；CPREV-005回拨边界先红后绿、周期17根通过，提交1492b12；CPREV-004 PG复现used0/overfalse后修正当前周期入账，52个store根测试通过，提交d7391a9。证据汇总`.ai/quality/checkpoint-review-fixes.md`。
- 协议实现返回afdc340e（30offline根绿，partial），未合入；新Sol任务01a16b50-5bbe-75a9-8640-ace6e171c361只收口临时官方核心握手。
- 成员UI返回e013e158（29通过1失败，partial），未合入；原Sol01a16b3d-d415-75a9-8640-ace2202995ac已resume修错误路径，允许必要最小共享ErrorState/useResource修改，不弱化断言。
- Sol 01a16b50-5bb4-75a9-8640-ace4dcb895fb在隔离分支实现Agent SQLite intent/原子提交（仅state包）。
- Sol 01a16b57-5099-75a9-8640-ace8f46c096b在隔离分支实现纯文件LoadUpgradeEvidence校验（不得碰DB/迁移）。
- Sol 01a16b5b-bf30-75a9-8640-acea84e0683c在隔离分支实现不可变周期历史helpers+schema-only0005，未允许主直接采用或部署；主后续负责启动前证据门禁及事务接线。

当前上述5任务上限（若提前结束先取结果，不盲补任务）。主必须复核隔离commit后再合入，尤其manifest不整文件覆盖。全部使用medium/low推理级别与有界交付，不开第6个。源码子代理必须在自身分支commit，主复核后合入，不push/不部署。日常只测受影响模块；完整候选及真实撤权/故障/迁移门禁不削减。固定等待优先改为条件等待，不用缩超时或SKIP假提速。

## 五个Sol大闭环实现（用户再次明确要求）

共同基线7c5aa67，写入所有权见`.ai/quality/implementation-coordination.md`。主不重复实现这些模块，只协调接口、审diff、合分支和运行统一候选门禁。

| 路 | session | 内容 |
|---|---|---|
| A | 01a16b66-fed9-75a9-8640-acf4b304eddb | 材料→真实预检→CLI/API→任何DDL前门禁→0005同事务导入 |
| B | 01a16b66-fed5-75a9-8640-acf3b2e80931 | 周期/执行授权历史→业务写者→计量/迟报/额度→移机回执 |
| C | 01a16b66-fed2-75a9-8640-acf19006dbba | 邀请开通→密码/会话CAS→限流→恢复事务门禁 |
| D | 01a16b66-fec9-75a9-8640-aced05510ab4 | SQLite intent接supervisor→收紧/到期→停机计量/崩溃恢复 |
| E | 01a16b66-fecf-75a9-8640-acef1cb2cdda | 已有成员分支继续完整管理员/成员Vue旅程 |

全部codex/gpt-6.1-sol:medium，40–45分钟有界交付，不能派子代理，不push/部署。五个均是实际实现任务，不是finder。已有候选3fead75/62167da3/0ee3983/e013e158/493bd7f供负责路复用，不再次重写。协议afdc340+88fe0f3由主待复核合入，不能把其36个根测试跨版本加到主候选通过数字。

主当前源码已提交离线导出7c5aa67；材料不等于ready。独占PG仍运行，原业务环境未动。正式test/accept仍未完成，独立实现者自报结果只作输入。

## C整合结果

7bbb19c已审限定生产diff后合为8839858，主补本人会话删除路由的恢复救援例外（保留身份/CSRF），并清除临时分路叙事注释。主统一uv复验store/identity-tx、api/identity-http、limit/identity-limit共30根0跳过/遗漏，证据20261024T010726Z-779bccb8。其他域写事务门禁和B周期登记仍待共同整合，不把此结果当全后端完成。
A已交b4a2d75及依赖a9921ab/b3b8a6c，旧Agent执行证明仍unknown，未合入；已向D协调artifact能力，不能假ready。

## E候选与补闭环

E提交链5f0be4c/d9fd467/49181bc/958b487已返回，前端typecheck、14文件81Vitest和build通过，但未浏览器/E2E，未合入。批量pending key跨刷新缺失与成员列表分页字段冲突明确留存。

主已将唯一backend规范索引listMeNodes/getMeUsage/listMeSessions请求字段补为limit/cursor（与已有分页语义一致、响应不变），派：
- Sol 01a16b7c-1dab-75a9-8640-acfabef322fb：只补前端跨刷新幂等核对，基于E已有提交，不能重写已完成旅程。
- Sol 01a16b7c-1d9e-75a9-8640-acf852a3eb0d：三个me列表真实keyset，仅本人数据/原usage对象信封，定向PG >200/隔离用例。

A/B/C实际整合由01a16b78-daac-75a9-8640-acf70bf89116继续；D仍负责执行证据能力接线。新模块必须各自commit，不把partial或多个分支成绩拼作主候选通过。剩余名额不无意义填满。

## D候选与执行整合

D已返回f728779+b8085f2，真实核心子链报237测试事件（不是237个唯一顶层用例），0skip；撤连接50ms、到期1.036s均是该分支条件下观测，不宣传生产SLA。handoff internal/agent/supervisor/LIFECYCLE_HANDOFF.md，未合入主。
计数100→200→150的基线reset语义350与候选高水位200存在独立冲突；D保留既有稳定断言/语义，未冒PASS。此项未批准策略变化，不把候选200擅自当冻结需求。
主已向ABC整合者传D artifact能力边界。另派Sol 01a16b7f-4696-75a9-8640-acfcf02f8d66实际整合协议05f75cd/88fe0f3与D，处理真实调用冲突和临时核心验证，不修改PG/store/web。其余在途：ABC整合、E幂等恢复、成员分页；总不超过5。各分支成果不能拼总通过数字。

## 当前统一主候选

已合入前端E全链+pending恢复（主105测试/typecheck/build绿）、协议/D整合3a41be5（主219预期根0skip，临时目录清理，20261024T013655Z-81677d09）、ABC整合依赖c803e41/56d0fb3/c7849ec/45cc141/032030b（主解决manifest按id并集冲突）、me分页865f1b3。主ABC+me8选择44槽0skip和Panel build绿，20261024T014710Z-e9b9e2d8。不得把不同范围/版本简单加数。
停止证明旧/新intent零计数仍不能单独放行，A已知AGENT_STOP_UNVERIFIED阻断保留。后台读模型（管理列表cursor/q、setup、format_status、data_complete）仍欠实现，不把前端显示unknown当完成功能。下一并行补此域，并解决可核对的人工停止材料语义，再统一真实Web/数据面与升级演练。

## 后端读模型最后补齐与升级解除

已统一主候选通过ABC/me8选择44槽（20261024T014710Z-e9b9e2d8），Panel build绿；主目前含协议/D、ABC、E与成员分页，不表示最终全仓/浏览器/E2E已过。
并行实现：
- F1核心六列表q/keyset：01a16b9c-0782-75a9-8640-ad036ee9b65f；不改F2/F3域。
- F2运维七列表：01a16b9c-0786-75a9-8640-ad05b6ef2c79；不争写公共pagination.go。
- F3 setup/data_complete/format_status产品读状态：01a16b9c-0778-75a9-8640-acfe63e71481；domain只改对应附加DTO。
- G合法停止证明/升级解除：01a16b9c-077e-75a9-8640-ad011233d6fb；不能hash即ready或零即执行完成，不能伪造旧intent，缺信任边界返回最小裁决。
上述任务均实际代码、medium，最多5；主不重复实现。后续真实Web和数据面需在最终统一树运行，不能拼分支结果。

## 主读模型/升级路径与最后验收批次

主已合F3产品状态90ba1e9，真实PG/status+me+readiness5槽0skip，证据20261024T020124Z-6f797a84。已合停止核对14a5095：真实管理员核对结构材料不是机器独立观测，主stop合法导入/反例/回滚及PG CLI4根0skip，20261024T020716Z-49173c96；旧未知/矛盾仍阻断，不再将所有Agent永久unknown。
F1六核心列表8d9dfc5，主admin/me HTTP/PG0skip，20261024T021238Z-b0b11253；F2七运维列表1c4ae5a，主三路由7feee36已接并复验0skip，20261024T021900Z-6e660d4e。至此基本读模型齐备，真实浏览器尚未跑。

当前最后五个并行任务（全部Sol，最多5）：
- H 01a16bb9-6b9e-75a9-8640-ad0a53b02748：剩余永久幂等/tasks.revision/0006持久化实现与真实PG场景。
- I 01a16bb9-6b98-75a9-8640-ad08b1775eaf：新隔离Panel/PG的真实浏览器开通与成员主路径，交付可重复mjs资产。
- J 01a16bb9-6ba1-75a9-8640-ad0dce5511ec：安全依赖扫描分类与最小升级/CI，不改业务Go。
- K 01a16bbb-a2a8-75a9-8640-ad11961193a2：真实Panel→Agent→官方客户端→69632字节→账本→暂停/恢复，临时自有资源。
- M 01a16bbb-a2a4-75a9-8640-ad0ef75fab19：两种HTTPS托管真实浏览器；远端只新label隔离对象，禁止覆盖/重启原栈。

各任务必须保留失败/重测和代码/产物指纹，证据复制主私有目录；不拼旧分支数字。本轮4个功能与3组验收尚不全部关闭，dev/test/accept不自动改完成。旧业务进程/数据库/原Docker栈不动。

## 最后一轮安全更新

Go依赖候选ddb72d9已审最小版本后cherry为069adee；主go mod verify/build、store/identity-tx和protocol/handshake-review无skip通过，20261024T023351Z-c03c7af3。原始OSV扫描保留：基线reachable3/imported2/module21，修后仅未导入无修复OpenPGP GO-2026-5932模块级1；JSON退出0不说明无漏洞。workflow仅actionlint、未执行GitHub/Docker。
web锁文件两条high仍阻发布，另派Sol 01a16bbf-4a52-75a9-8640-ad12b119051d仅web依赖最小修复与表单回归，禁止audit降级/force瞎升。
此前H/I/J/K/M中J已完成释放名额，现H持久化、I浏览器、K真实数据面、M双HTTPS、J-web五路，禁止再开第6个。当前多数模块已合统一树；test/accept整体尚未完成，不能把各路成绩拼最终成功。

## 统一候选253526a，最后实现与可重复验收资产已合

H永久幂等/tasks.revision/0006候选94f5cfe→主253526a，原候选5选择真实PG绿、0skip；当前独立只读Sol high审查01a16bc7-5a1e-75a9-8640-ad15526349ad聚焦逐文件proof基线与幂等/revision，待结论，不能宣布accept。
J-web78964d3→主e5652fd仅移除未使用zod/适配器，主git grep独立确认无调用，npm锁审计0各级告警、typecheck/5表单文件/build通过；安装树未重装以免打断浏览器，隔离clean npm ci已验证。
M双HTTPS脚本4d1861b→主c1c907f，K真实数据链195cd01→主37e4456，脚本语法已查。两者已有真实绿证据基于7feee36，不能当253526a结果；最终统一候选应重跑。M是独立nginx容器模拟宿主TLS与同候选替换，NOT旧版本跨版升级/真实宿主配置；K是DEV loopback数据链，NOT全HTTPS/54格客户端矩阵。
K真实流量69632字节文件SHA匹配、累计计量70096字节、usage ACK丢失同批重发PG不增长、暂停新旧连接1.084秒内拒绝、恢复新导出可用且旧凭据仍拒绝。曾错误期待旧凭据复活的失败与修后均保留，不改生产语义。
现仅I浏览器主路径和独立H审查在跑，余名额保留实际缺口。开始在统一树跑全部34已登记后端选择（不是宣称全仓所有未登记用例）；此时冻结受测输入，后到候选暂缓合入。

## 全登记回归失败及真实修复队列

统一253526a全34，输入稳定但FAILED，原报告20261024T024355Z-a4eaf3ba。不得报告34绿。4个失败选择：store/database-baseline中TestUsageBatchBooksIntoAdvancedCycle因授权区间历史不充分被LEDGER_UNATTRIBUTABLE拒；api/abc-readiness旧断言看到新增0006；两个真实core选择发现缺失，因为源文件带xrayreal而runner未传tag。已加显式manifest build_tags、发现/执行同tag与独立Python合同测试，绝不删预期测试。
独立H审查证实P1：0005 ready→Open应用0006→post check重新比新摘要与旧proof失败（迁移6已提交，不是回滚泄漏）。修复Sol 01a16bcd-6505-75a9-8640-ad18ce7402fb，限定同次短命proof/最终锁下digest+evidence+语义，后续启动不永久复用。
浏览器22PASS/2FAIL，旧候选：正常access首页矛盾提示、维护缺提示且下载仍可见。资产8350230由修复Sol 01a16bcb-abb9-75a9-8640-ad17b25ca410在新候选加永久单测并重跑24。
另Sol 01a16bcf-3609-75a9-8640-ad1b77645d67查用量/API两旧用例，先区分fixture与生产bug，不放宽未知历史、守恒与路径拒绝。现3个Sol在跑，主负责真实tag入口与后续统一验收。
静态覆盖盘点有555顶层声明，已登记选择不涵盖所有；有些run=^Test确实执行更多，不能用expected列表简单减法声称漏跑总数。仍须最终全包/环境需求单独核对，不能以34选择替代全仓。

## 成员状态修复与权威维护契约核对

浏览器资产8350230→主b635502，正常access与ok被错误当拒绝原因的修复621ef93→主3b86336。旧候选真实browser23/24；未获最终全绿。现有full Vitest216PASS/1FAIL（NotFound首页链接，干净base可复现），须处理不冒通过。
**纠正“BUG2”分类**：backend.md549/frontend.md143权威设计已有令牌只读获取仍允许，无令牌因维护不能生成才导入区提示409；成员无管理员恢复状态不编全局维护。原mjs要求所有成员全局横幅/禁下载是错误预期，不得修改后端策略让它绿。Sol01a16bd3-96e3-75a9-8640-ad1d7328d651仅补已token/无token双场景并查404单测，保留原错断言证据。没有新增成员维护API契约。
runner构建标签修复b593fab：Python合同+真实protocol/Agent lifecycle两选择实际绿0skip，20261024T025047Z-65b325b4；不等于之前全34已绿。
补覆盖Sol01a16bd5-06fa-75a9-8640-ad1fce6d49de负责剩余普通API/PG与纯逻辑，参考actual ran而非只expected的静态盘点；不与主/其他代理重复真实core或Docker。当前4个Sol活跃：P1迁移、两旧fixture、维护/404、剩余覆盖。

## 主已合收尾修复；补覆盖仍有明确失败

Open P1 4b9709b→主042cc35：final在同次内部migrate/advisory lock内，只在确有文件提交时用栈内receipt，final先control锁再表锁，READ COMMITTED锁后重算digest/evidence/语义；无pending及下次Open不接受旧baseline。已审delta，原race33绿，待主统一复验。两旧fixture290fa9e→主358b769；正常member UI此前主已合；维护/404903f940→主fa1c746，原分支全Vitest219、真实browser25绿（尚非最终树重跑）。
补覆盖f5fa4d2→主222ce7d，145根140PASS/5FAIL/0SKIP/0missing，追加11个remaining选择；自动带入四份技术证据文件（不含秘密），未push。API5失败：3 acceptance历史区间422、PanelFlow异payload409+历史422、本人会话救援旧409预期与权威backend.md540允许本人撤销冲突。Sol01a16bdb-d030-75a9-8640-ad20b9a0f326仅修这三API测试文件，保留普通写409/真实本人会话救援与越权。
前路错误报缺sing-box/Mihomo：主find发现/home/compile/Work/bin三官方客户端均存在，需正确env路径/版本核对而非外部阻塞。Sol01a16bdc-980c-75a9-8640-ad23dc07e8fe补真实matrix/credentialguards/reserved-tags及xrayreal，主DataPlane等该任务结束避免既有core快照并发假阴性。
Sol01a16bde-8f2e-75a9-8640-ad243dc7c8dc补DUAL_BASE_REF=7feee36的真实0005→当前0006双HTTPS升级（无Agent旧库，Agent proof由独立PG回归），远端仅新owner对象/原栈禁止触碰。
现3个Sol活跃，未有全量成功声明。用户明确希望频繁主动简短进度，已存harness-only preference，不等追问。

## 主候选构建与真实前端复验通过；矩阵负例待补证

222ce7d主构建：module verify/native build、linux amd64/arm64 panel+agent四产物编译绿，.ai/evidence/release-build/20261024T030638Z-4370e70a；arm64 runtime NOT_RUN。
222ce7d主前端：219 Vitest、25真实browser、锁审计0、schema清理verified，.ai/evidence/browser/main-candidate/20261024T030752Z-2c2da362/result.json；是主候选而非沿用旧分支。loopbackHTTP，HTTPS别路复验。
矩阵e85fd75已合主（仅tests/manifest）：20 supported完整69632字节内容+SHA、MihomoTrojanTCPnone1声明拒绝、sing-box保留tag6与Xray3真实场景绿；wrongpin绿但通用负例helper仍需强诊断审查，SS2022 user-only仅timeout→FAIL。不能说matrix整体绿。三binary未修改：Xray26.3.27、sing-box1.14.3、Mihomo1.19.33显式pin（不是默认1.19.32）。
SS2022严格oracles Sol01a16be2-223f-75a9-8640-ad2603860bb8，要求明确server/client认证诊断及合法前后成功对照，不认timeout/任意非零/基础设施EOF。主DataPlane等其所有core退出。
离线E2E最后补验Sol01a16be3-a568-75a9-8640-ad29a619fd27，仅读取实际offline函数后注册ACK/queue/observation/parser等；不启动Docker/Platform/DataPlane/core。
当前四个Sol：API旧fixture01a16bdb-d030-75a9-8640-ad20b9a0f326、SS2022严格负例、跨版双HTTPS01a16bde-8f2e-75a9-8640-ad243dc7c8dc、离线E2E辅助。未有总体成功声明，不再新扩任务；收回结果后冻结统一树执行最终已登记回归和DataPlane。

## backend dev/test技术闭环与正式accept交接

冻结候选6d1345c。主最终50选择/542不重复Go根绿0skip/0missing、源码指纹稳定，native build和Linux双架构4产物编译、govuln0 reachable/imported（保留未导入OpenPGP模块级1）。同候选补充：219前端单测、25真实浏览器、真实Panel-Agent-client-traffic-ledger/ACK去重/撤权恢复及双HTTPS旧5→新6四场景均绿，清理可核验。HTTPS两次wrapper错误分别是目录护栏与误期待人工派生summary；原始helper已0且4场景/产物/迁移/资源记录独立重建验证，未改产品或降低门禁。
主技术汇总.ai/evidence/final-6d1345c/summary.json，独立验收handoff.ai/quality/product-readiness-backend-final-candidate.md，完整未执行声明15项另列（含11历史Docker套件、Platform、独立Vision、实验tag、非LinuxACL），不冒全仓/全平台运行成功。accept须独立判断本阶段必要性：必要但缺证据就拒绝，不能由主把它们一概不适用。
设计来源门禁再次passed，三文档集合与state一致。已据本次有效资产及集中执行闭环将dev/test记completed，**backend phase仍in_progress，正式accept in_progress**，不自动完成其他phase。正式验收只读串行Sol，无其他活跃实现子代理。用户授权并行准备的frontend/web-demo证据不等于这些正式阶段完成。

正式只读backend-accept已串行派发：Sol high session `01a16c02-d320-75a9-8640-ad2a5f0bfa57`，validation_revision=0，唯一冻结源码6d1345c。报告仅允许主.ai/quality，禁止读写state/改源码测试/原部署；允许复核原始证据及安全隔离最小补验。不存在其他活跃实现子代理。主不得代出结论或把技术汇总当验收；返回后先核验报告字段、实际文件与必要门禁一致性，再更新state。当前仅backend请求，正式frontend/web-demo仍未启动；此前用户授权准备资产仅作handoff。

## R0正式验收拒绝，validation_revision=1

独立accept report .ai/quality/backend-accept-product-readiness-backend-20261024-034457.md，有效REJECTED；其独立确认234文件指纹、50选择542根、真实数据链/HTTPS原始证据通过，不代替缺失公共契约。已重开dev in_progress/test pending/accept pending，保留R0全部成功与拒绝，不回退成未做。
必须闭合：F1方案DELETE（未归档当前绑定409；member_bindings历史→归档retired=true并守恒；无历史物理删；已归档幂等）、F2模板未归档引用409 CONFLICT/details.reason=template_in_use+count且引用不变、F3长期工程/API文档同步、F4真实可执行check/evidence/挂载/失败前缀与停写技术回退；另补真实PG outage/Redis outage/物理ENOSPC最小同候选故障证据。不得删历史绕FK、撤恢复安全门禁、用旧测试旧语义覆盖明确设计，或以542绿覆盖故障缺证据。
调度限4个互不重叠实现闭环：DELETE生产/真实PG/API/最小消费者；长期API文档；升级helper与deployment文档及隔离fixture；真实故障资产。所有现有环境/stack不得触碰，仅新owner临时资源；源码commit仅显式owned文件、私有evidence不得入Git。后续只测受影响范围，原输入未变证据可保留，独立accept R1再消费，不例行重跑全部542。

R1四个有界Sol medium实现会话（用户五上限内）：DELETE契约 `01a16c0d-b5b1-75a9-8640-ad32a6993343`；长期API文档 `01a16c0d-b5a9-75a9-8640-ad2e4e546c65`；升级交付helper/docs `01a16c0d-b5a2-75a9-8640-ad2d2896c466`；真实故障资产 `01a16c0d-b5ad-75a9-8640-ad3041c61e6e`。4路均基线6d1345c、隔离commit，无子派发/push/原部署操作。资源协调：203新owner Docker由故障路使用；升级路优先本地私有PG+特权stubs，如需要remote先回报，避免全容器快照把另一测试正常清理误判原环境变更。主不重复实现其owned文件，回收后审契约/证据并合入，再定向与统一受影响回归、正式accept R1。

## R1合入检查点（用户要求先备份GitHub）

主已合：DELETE b48dc24、游标/管理用量4dcb1f5、升级交付baa5690、故障资产bf22bc5、readiness46c7f81、长期契约f78c13f；PG故障分类修复12cc263。三真实物理故障在主bf22bc5已3/3 PASS（.ai/evidence/backend-test/real-fault-r1/20261024T122351Z-83eb224f），readiness合入后尚需同候选回归。全部实现子代理已结束，不再开新功能任务。

主当前整合补充：长期文档已按实际retired/reference_count、CURSOR_INVALID、usage keyset、readiness同源投影更新；新增DB错误与upgrade/delivery统一runner选择；故障目录命名改UTC。仍是WIP，未有R1正式accept放行。readiness checksum使用boolean|null区分不可核验与已知不匹配，需在R1验收显式核对技术细化与上层unknown契约，不以nullable绕过任何门禁。

下一步：先按用户要求备份当前分支到origin GitHub（不force、不推main、私有原始证据/凭据不提交），再构建真实UPGRADE_TEST_PANEL_BINARY并用项目uv runner执行受影响store/API/upgrade选择与consumer；物理故障3项须使用owned launcher，不能在缺PP_FAULT_OWNER的普通全选中伪造通过。补完后冻结R1候选，正式只读backend-accept revision1串行再验。R0拒绝及所有旧绿证据保留，不把备份commit称为验收完成。
