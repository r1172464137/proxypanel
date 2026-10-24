# backend-accept / product-readiness / backend / revision 1

## 结论

**REJECTED；task_completion.status=failed。本轮 backend 尚不能正式放行，不能据此进入“已验收 backend”的 frontend/web-demo。**

候选唯一为 `269e4e25ec4693c184cc7ebf493595ec1a97136a`。本轮独立核实 R0 F1、F2、F4 已闭合，F3 原来的长期文档遗漏已修复；定向回归唯一环境失败已有效补验闭环，必要三项真实物理故障已有足够原始证据。拒绝原因**不是这些场景仍失败**，而是 R1 readiness 公共字段类型与唯一规范性设计源存在未收口冲突（R1-C1）。nullable 的技术动机合理、已知消费者同步且没有发现放宽安全门禁，但 accept 无权把规范中明确的 boolean 和“字段变更先改本文件”自行解释成可豁免的草图。

此结论不继承 R0 的拒绝，也不因为实现者称其“技术细化”而预设通过。需要主会话/设计责任人明确裁决该契约细化并完成规范性来源收口；**不要求为得到通过而把 unknown 改成 false/成功，不接管代码或设计修复**。

独立核验附件：`.ai/quality/backend-accept-product-readiness-backend-r1-20261024-independent-audit.json`。下列路径均相对 `/home/compile/Work/proxypanel`，除明确标注外。

## 范围、输入及只读边界

- 主仓和隔离工作树 HEAD 均为 269e4e2；对候选执行主仓非 `.ai` tracked diff 为空。未读写任何 `.state.json`，不以计划/状态元数据 WIP 使业务证据失效。
- 先读主路径 R0 报告 `backend-accept-product-readiness-backend-20261024-034457.md` 与其 audit 的运行/指纹部分，再核查 R1 handoff 和原始日志。复用其未改变范围的审查事实，不沿用其结论。
- 公共依据：acceptance、verification-evidence、agent-task-output、requirement-source、runtime-boundaries；backend quality/development、core quality。项目实际约束依据 `go.mod`、DEC-009、长期 implementation-contract 和本轮分端设计。未发现根/一级 AGENTS.md。Go/Vue 覆盖 Rust 模板；不创建 Rust/Cargo/OpenAPI/客户端生成链。
- 需求/设计来源：`.ai/prd/core/product-readiness.md` §4–§8、`.ai/user-stories/core/product-readiness.md`（重点005/008/010/013/016/017/018）、`.ai/decision-log/product-readiness.md`；`.ai/design/product-readiness.md`、分端 `backend.md` §4/§10 和 frontend §4.8/消费及下游边界；`.ai/super-run/product-readiness/backend.md` 验证责任表和 R1 记录；`.ai/quality/product-readiness-backend-r1-handoff.md`。没有以实现修改这些来源。
- 聚焦 6d1345c→269e4e2 的 DELETE、readiness、分页、错误分类、升级 helper/文档、消费者及相应测试，不重审所有历史或无关文件。

## 门禁

| name | result | evidence | reason / 独立判断 |
|---|---|---|---|
| 候选及相关输入身份 | passed | R1 targeted/result.json、helper-library-green.json；本报告 audit | 两份 runner 均243文件，独立逐项SHA256对主路径0 mismatch，inputs_stable=true；业务非.ai tracked diff为空。不是仅比较HEAD。 |
| F1/F2 DELETE事务、并发及公共响应 | passed | internal/panel/store/resources.go；internal/panel/api/admin2.go；两层delete_contract_test.go；20261024T043929Z-cfa40b23/0-tests.jsonl | 9根PASS，0fail/skip/missing；覆盖归档/物理删/幂等/引用409/合法对照及精确行快照守恒，锁序与引用写并发有真实PG阻塞观察。 |
| 定向回归及失败闭环 | passed | backend-test/r1-integrated-targeted/result.json、helper-library-green.json；对应两run原始JSONL；audit | 12选择原结果仍failed。3个选择唯一失败根均TestUpgradeDeliveryHelper，原文为psql缺libpq.so.5、exit127；其余根无失败/跳过。243输入完全相同，补环境后的upgrade/delivery 8根PASS、0missing/skip；不把原报告改绿，不强制重跑542。 |
| 分页、恢复诊断及错误分类行为 | passed | admin_usage_page.go、pagination.go、admin_pages.go、operations_pages.go；admin_usage_page_test.go、readiness_contract_test.go、readiness_test.go、db_errors_test.go；上述R1 JSONL | admin usage独立SQL oracle核全结果/聚合，真实seek、身份/端点/规范化filter绑定；CURSOR_INVALID仅游标；HTTP/CLI同源诊断和typed unsafe拒绝已验证。此行为门禁不替代下面的规范性类型门禁。 |
| 长期文档原R0漂移修复 | passed | docs/design/implementation-contract.md；docs/reference/api.md §9–11；R1 diff | 原恒null分页、session救援、usage对象、DELETE及错误/诊断字段现已同步实际。R0 F3原缺项闭合；R1新增类型差异另列，不能合并掩盖。 |
| 公共字段与唯一设计契约一致 | failed | .ai/design/product-readiness/backend.md:258,271–279,308–314；internal/panel/store/readiness.go:46–51,136–164；web/src/api/types.ts:7–14；docs/reference/api.md §11；R1 handoff | schema.checksum_ok规范是boolean，运行/TS/长期文档为boolean|null；规范还明确字段变化先改唯一设计源。未见决策账本或规范性设计对nullable的接受记录。详见R1-C1；unknown安全语义正确不能自行消除类型冲突。 |
| F4受控升级、只读材料及停写回退 | passed | deploy/manage-linux.sh、upgrade-panel.sh；store/upgrade_delivery_test.go、upgrade_open_test.go；helper绿色run/0-tests.jsonl；docs/deployment/{production,docker}.md | install仅stage；真实候选CLI与PG，缺/错材料不切current/写DB，ready才切且不start；rollback-check只读核对实际前缀、不启动旧版。仅systemctl为stub，不冒真实宿主安装证据。 |
| 必要物理故障与恢复 | passed | backend-test/real-fault-r1/20261024T044830Z-578ec0f2/{receipt.json,TestRealFaultPostgresOutage.log,TestRealFaultRedisOutage.log,TestRealFaultENOSPC.log}；tests/e2e/real_fault_test.go | 3PASS/0SKIP；核读真实注入/断言和原日志，239源码及5工具指纹0 mismatch，source_stable/owner清理成立。不是用mock/unit替代物理故障。 |
| 构建、启动健康、数据面和隔离清理 | passed | r1-integrated-targeted/panel、native-build.log；data-plane/product-readiness/20261024T124820-3ab371f2/{receipt.json,test.log}；deployment/dual-hosting/r1-final-269e4e2原始目录 | 实际panel ELF的Go buildInfo指向269且vcs.modified=false；真实Panel/Agent/Xray链、PG账本/ACK及撤权恢复；启动/readyz和owned清理有效。没有运行新服务。 |
| 双托管跨版升级及实际加载关联 | passed | deployment/dual-hosting/r1-final-269e4e2/{result.json,refs.json,build-artifacts.json,artifact-associations.json}；四browser report、原始schema_migrations/runtime/fingerprints；audit | 两模式7feee36五迁移→269六迁移，原5行checksum/applied_at逐行保留；4浏览器报告passed且无errors，资产hash在实际加载指纹，旧会话刷新/HTML边界/SecureHttpOnly/logout有效；原对象快照相同、owned-after为空。边界是隔离nginx模拟host TLS、无Agent。 |
| 受影响消费者及依赖证据复用 | passed | web-typecheck.log、web-delete-tests.log；r1-final-269e4e2/frontend-data-fault/vitest.json；Go锁文件及R0 govulncheck.json | typecheck日志/主执行记录与同候选消费者测试一致；DELETE两文件9测，完整Vitest222/222、0pending/fail。未引新依赖/imported OpenPGP路径，复用原扫描0 reachable/imported，保留GO-2026-5932模块级残留，不称零漏洞。 |
| Rust及OpenAPI/SDK生成 | not_applicable | DEC-009；backend设计§4.3；go.mod | 原生Go路由/手写TS，无现有生成链；不创建生成器，不运行Cargo/Clippy/自动修复。 |

## F1–F4闭环及直接回归风险

### F1：闭合

`DeleteSubscription` 通过inWritableTx先control锁再订阅FOR UPDATE；当前未归档绑定先409（包括已归档方案）；已归档无绑定幂等，不推进version/updated_at；存在member_bindings历史仅归档并保留绑定、ledger、成员与group链接；无历史才物理删，解绑归档成员沿用既定语义。handler仅DELETE原快照附加retired，GET无此字段，不要求version。

测试不是只数行：store八并发DELETE返回同快照、control原值不变、历史/ledger/group/member完整JSON守恒；并发引用写占control时DELETE确实等待，NOWAIT验证未倒序锁对象。HTTP测试比对完整前后快照、物理删后404、拒绝无副作用、已归档幂等、GET无retired。原归档物理删断言按已批准§4.3校正，没有放宽新目标。

### F2：闭合

模板同control→对象锁后统计未归档引用，409 CONFLICT/details.reason=template_in_use/reference_count；引用不变；解除引用和仅归档引用均有成功对照。FK清空只作用允许的归档引用。最小Vue反馈/DELETE类型和9个局部测试有效，不据此认为frontend全阶段完成。

### F3：原缺项闭合，新增类型冲突尚待收口

两长期docs已覆盖真正keyset、usage对象/第一页adjustments、恢复救援、当前session400/logout、引用与退役、readiness及错误码。admin usage混合排序seek在聚合前按完整身份过滤；续页不切割aggregate；>200行、归一化时间、limit变化、跨管理员/端点/filters失配、精确最后满页、删空续页均有PG/API断言。

readiness公共字段由同内部核对结果MarshalJSON投影；legacy checks/迁移数组同源，blockers从失败/未知结果产生；HTTP失败诊断仍200 unknown、无驱动原文/任意路径接口。candidate_version来自debug.ReadBuildInfo；本次实际模块版本为`v0.0.0-20261024043718-269e4e25ec46`，不是伪造release。旧pending_batches=0兼容占位绝不视为Agent远端已排空。unknown仍阻断unsafe Open/迁移；enterRecovery救援未新增阻止。

**独立技术判断：** null表达“未核验”优于伪造true，也能区分已知false。TS `boolean|null`和RuntimeView三分支严格比较已适配；有真实unknown schema/fallback测试。但这证明实现内部一致，不证明其已符合唯一规范源。按源文档明确的字段变更规则，仍须由主会话/设计责任人决定是否接受这一细化，accept不静默代裁。见下方唯一未解决finding。

### F4：闭合（限定本backend技术范围）

生产helper实际调用候选CLI check --preflight --json --evidence，检查exit和status；保持同材料配置给Open锁内重验，缺/错材料不写DB、不切current。合法停止fixture通过后只激活不启动；rollback-check读schema_migrations逐文件checksum前缀，不虚构旧版readonly能力，也不把前缀匹配等同业务兼容。文档明确停新增流量→最终计量→旧Uploader/Panel保留至ACK排空→Agent/core停机取证→Panel及写者停写；材料readonly目录/权限、相同服务端路径、失败文件与已提交前缀、真正DB恢复分开。

helper原exit127为SDK动态库环境问题，不是有效业务拒绝。独立解析两run原始输出后确认8根补验真正执行；源码/断言输入相同。没有借预算豁免失败，也未重新操作已清理远端。

## 必要物理故障证据

- **PG fast stop**：真实pg_ctl -m fast，端口拒绝；HTTP503 DB_UNAVAILABLE、无ACK；原BatchID/payload hash不变，队列保留；重启重发前SQL失败批次为[0,0,0,100]、原提交批次[1,100,1,100]不变；恢复后唯一账本[1,150,1,250]，再次发送duplicate不增长、ACK后队列清空。dbErr单测补class08和57P01/02/03的retryable503及驱动文本不外泄，同时保留约束/事务错误分类。
- **Redis真实stop**：readyz200 degraded/unavailable；前10次错误登录401，第11次429 RATE_LIMITED；重启readyz ok、合法登录成功。不是 blanket503，也不是无限失去限流。
- **私有8MiB tmpfs真实ENOSPC**：填充写返回syscall.ENOSPC，Measure要求SQLite FULL13；原队列、baseline=100、clock、seq=0、revision=7不变；满盘Open有正确保留路径；释放并重开后delta=150、总计250，未丢/重记。源码核实私有mount guard及namespace命令，不在host/tmp填盘。
- receipt确认原进程身份不变、自有PG清理退出0/absent、host_mount_absent、临时目录移除。当前验收没有创建测试资源，不触碰现有PG/Redis/Agent或旧栈。

## R0证据复用与15个NOT_RUN声明逐项复核

R0原run仍是50选择/542唯一根的历史有效执行，并非在269再次执行。独立重解析原JSONL并比对原234输入：212相同、22变化。**不把22变化当无影响**：对应store/API/readiness/CLI/runner已由R1相关回归替换覆盖；Agent、contracts、migration、Go依赖锁、协议/核心实现和测试未变。R0相关审查的授权/周期/幂等/task/Agent/协议事实保留，跨模块store风险另有当前104通过根及helper补验、真实数据面和物理故障承接。原50的PASS不作为本轮全仓PASS数字。

| 未执行声明 | R1必要性/适用性判断 | 本轮状态及承接 |
|---|---|---|
| TestReviewOutOfOrderFinalAndPeriodicSamplesOverbill | measurement_candidate的高水位200是未批准替代策略，生产采样串行化与reset输入未变；不擅改计量需求 | 原函数NOT_RUN；当前实验策略N/A，未来策略主会话裁决 |
| TestStateDirACLIsRestricted (Windows) | Linux Unix权限证据仍适用，编译不证明Windows ACL | NOT_RUN；平台/发布及web-demo承接Windows实机 |
| TestDockerClientMatrix | 三官方core锁/实现未变，原local支持/不支持矩阵事实可复用，不等于54格/全客户端 | NOT_RUN；web-demo完整支持矩阵 |
| TestDockerACKLossAcceptance | 当前真实Agent sampler/outbox/PG同批ACK丢失重放和字节守恒再次通过 | 旧包装NOT_RUN；backend核心断言已覆盖，容器场景web-demo |
| TestDockerFaultAcceptance | PG/Redis/ENOSPC必要本阶段断言此次已有真实隔离等价证据 | 旧包装NOT_RUN；本backend必要缺口已闭合，不伪称旧套件运行 |
| TestDockerBatchPartialFailure | task/revision层可复用并有当前store回归，两Agent一台离线用户链不等价 | NOT_RUN；web-demo两节点部分完成 |
| TestDockerConfigSafety | 当前授权不回退/撤权不复活已有Agent证据未变，新数据面再次核新旧凭据 | NOT_RUN；容器挂载/跨节点存活/最终UI由web-demo承接 |
| TestDockerAgentVersionUpgrade | HTTPS fixture无Agent，材料验证不能代替真实旧Agent/SQLite升级 | NOT_RUN；US-PR-016 web-demo/部署 |
| TestDockerCoreVersionSwitch | 当前官方core运行不证明切换回退 | NOT_RUN；web-demo/部署 |
| TestDockerLifecycleDiagnose | 旧特定fixture诊断helper不是独立本轮必要运行门禁，无fixture空跑无意义 | NOT_RUN；工具本轮N/A，异常用户流程web-demo |
| TestDockerLifecycleRecover | helper会重启旧两Agent，本轮未污染其fixture，不允许为补计数操作 | NOT_RUN；该helper本轮N/A，本轮owner清理由receipt证明 |
| TestDockerRecovery | API恢复冻结不等于pg_dump恢复/旧Agent副本联动 | NOT_RUN；US-PR-014/016 web-demo真实恢复 |
| TestDockerAcceptance | 单候选已验证TCP不代表双Agent/UDP/离线撤权全集 | NOT_RUN；web-demo完整网络/故事 |
| TestPlatformRealCore | 特定旧PG/Redis环境与部分新数据链重叠，不是整函数通过 | NOT_RUN；余断言web-demo新安全fixture；禁旧.devdb |
| TestXrayVLESSRealityVision | REALITY握手/配置不等于独立Vision有效流量 | NOT_RUN；US-PR-004/007矩阵，支持声明前必须验证 |

## Findings（仅未解决项）

### R1-C1 / P1：readiness校验和字段类型未与唯一规范性设计源收口

- **location**：`.ai/design/product-readiness/backend.md:258,271–279,308–314`；`internal/panel/store/readiness.go:46–51,136–164`；`docs/reference/api.md` §11；`web/src/api/types.ts:7–14`。
- **evidence**：唯一契约源明确`schema.checksum_ok:boolean`，并明确字段变化先改本文件、TS只是消费镜像；实现`*bool`且JSON不omitempty，unknown会实际返回null，TS/长期docs声明boolean|null。决策账本无相关接受条款；handoff只声明技术细化、要求accept独立判断，不是规范性设计变更。store/API unknown测试及原JSONL证明此差异真实可观察，不是注释差异。
- **impact**：当前规范仍要求非空boolean，实际输出含null；依规范建立的类型/契约检查不再成立。已知Vue消费者适配降低运行风险，但不能替所有按唯一设计源工作的消费者或设计责任人接受公共类型变化。必要契约门禁失败，故本阶段不能放行。**未发现null被当成功或绕过安全门禁；不把此项描述为数据丢失/授权漏洞。**
- **suggested_fix / owner**：交回主会话及backend设计/API责任人，明确决定是否接受nullable作为unknown技术细化，并使唯一规范性来源、相关派生契约及实际消费者一致；若不接受，则由dev提出保留unknown语义且符合已定类型的方案，不能由accept任选。不是要求把源需求机械反改为实现。已验证源码/断言未变化时可复用本轮行为证据；若只做规范性收口，最小复核规范/派生指纹/消费类型即可，不必重复物理故障或542。若改wire行为，backend-test补最小readiness HTTP/CLI/消费者运行证据后独立复验。

## 未验证及承接

**本阶段不可延期豁免：** R1-C1公共契约冲突由主会话/设计/API责任人解决，位置backend §4.2–4.5、本报告finding；不能把它移到frontend修复后才放行。

**后续frontend/web-demo正式阶段（尚未正式启动/完成）：**

1. frontend：完整18故事的真实页面消费、加载/空/错/unknown/恢复状态、移动/键盘/主题、DELETE及nullable最终契约消费，定位frontend设计§4/§8.3、主设计§6。
2. web-demo：真实UI→Agent→客户端→流量→ledger两条完整旅程，所有实际声明支持的平台/客户端及Vision、双Agent离线/UDP/部分完成，定位主设计§6、backend§10、phase验证责任表。
3. web-demo/部署：真实数据库备份恢复、旧Agent/SQLite及核心跨版、真实systemd安装/宿主nginx/DNS/公网CA；双HTTPS隔离nginx证据不能替代真实宿主及Agent停止证明。US-PR-001/014/016。

222单测、25浏览器、数据链及双HTTPS都是候选技术证据，不是全部业务验收完成。最终承接阶段不得带必要未验证项交付。

## 执行、派生物及恢复

- 仅执行只读Git diff/buildInfo查询、源码/设计/原始日志审查、JSONL/SQL/hash独立解析。没有补跑有效测试，没有停止任何服务、建立schema/namespace/容器或派代理，没有push。
- 唯一新文件是本报告和独立audit，均在主`.ai/quality`；未生成/修改受版本控制的API客户端、业务、测试、部署、迁移、索引。change_scope九项全false。
- audit记录输入hash检查、原失败/补验逐选择结果、物理故障/数据面指纹、双托管迁移/浏览器及清理核验；未复制原始运行凭据到报告/附件。
- 无本轮运行资源待清理。主仓已有业务源码仍应保持269；R0报告和失败原证据保持原样。
- 恢复：调用方先处理R1-C1，由设计/API责任人裁决并提供明确收口输入；必要行为变化才重开对应验证。下一轮accept新建报告，复用本轮有效原始证据，不覆盖本轮结论，不以备份分支或准备资产代替阶段完成。
