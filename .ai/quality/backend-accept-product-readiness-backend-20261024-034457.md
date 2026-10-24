# backend-accept / product-readiness / backend / revision 0

## 结论

**REJECTED；task_completion.status=failed。当前 backend 阶段不能正式放行，不能以此报告进入已验收的 frontend/web-demo 状态。**

冻结候选 `6d1345c54b5fc5b3ea0033275338da8ff8362820`，基线 `3435f1d`。独立验收确认当前集中回归及部分真实链路证据有效，但发现两项明确未实现的公共 DELETE 契约，以及契约文档和升级交付路径缺口。542 个绿测不能抵消这些缺口。没有修改生产代码、测试、断言、迁移、索引或部署，没有派子代理，没有访问任务状态文件。

报告唯一标识：20261024-034457。机器审计附件：`/home/compile/Work/proxypanel/.ai/quality/backend-accept-product-readiness-backend-20261024-034457-audit.json`。附件是本次独立解析结果，不是上游摘要改名。

## 范围与来源

- 绝对项目根 `/home/compile/Work/proxypanel`；验收工作树 `/home/compile/Work/proxypanel-worktrees/pi-web-agent-8c7b8112-03cd-40b1-9034-20c5936f43a4`。两者 HEAD 均为冻结 commit，初始 tracked diff 为空。主项目的未追踪 `.ai` 是需求、设计及证据来源。
- 读取公共 acceptance、verification-evidence、requirement-source、agent-task-output、runtime-boundaries 合同，backend quality/development 与 core quality。
- 需求源：主路径 `.ai/prd/core/product-readiness.md`、`.ai/user-stories/core/product-readiness.md`、`.ai/decision-log/product-readiness.md`；设计源 `.ai/design/product-readiness.md`、`product-readiness/backend.md` 全文，frontend 分端的消费和后续边界；交接源 `.ai/quality/product-readiness-backend-final-candidate.md`、`.ai/super-run/product-readiness/backend.md`。
- 未发现 `docs/prd` / `docs/user-stories` 正式索引；不因草稿尚未发布拒绝。既有 `docs/design/implementation-contract.md` 是应同步的长期工程契约，不以“草稿”理由忽略其中的漂移。
- DEC-009：真实栈 Go 1.27.3/Vue，Rust、Cargo、Clippy 不适用。设计 backend §4.3 明确没有 OpenAPI/SDK 生成链，不新增生成器。
- 变更跨 store、SQL、身份与恢复、Agent、协议、API、依赖及部署脚本。DEC-012 的前端准备资产不等于其它 phase 正式通过。

## 门禁

证据路径均相对 `/home/compile/Work/proxypanel`，附件保存完整原始入口及逐选择核验结果。

| name | result | evidence | reason / 判断 |
|---|---|---|---|
| 冻结树与回归证据身份 | passed | `.ai/evidence/backend-test/20261024T032530Z-ca404f10/result.json`；本报告 audit 附件 | 对输入中 234 个文件逐个 SHA256，与当前主项目一致，0 mismatch；final/all-registered 与实际 runner 报告字节相同。 |
| 全登记后端回归运行真实性 | passed | 上述 runner 目录的 50 份 execution JSONL、discovery 记录；`.ai/evidence/backend-test/final-6d1345c/runner-contract.log` | 独立解析顶层事件，542 个 package+test 去重 PASS；无 fail/skip，预期根无缺失，执行/发现退出码均 0；显式 xrayreal/corematrix 标签存在。仅表示登记范围，不表示全仓全部声明。 |
| Go 构建与依赖证据 | passed | `.ai/evidence/backend-test/final-6d1345c/{module-verify.log,native-build.log,cross-build.log,panel-amd64,panel-arm64,agent-amd64,agent-arm64,govulncheck.json}` | module verify 原始输出通过；native/cross 日志为空不能单独证明成功，结合候选记录、实际 ELF 四产物及部署加载 SHA 关联复用。file 验证 x86-64/aarch64，arm64 不是运行通过。固定 govulncheck v1.9.0，唯一 finding 是无 package/function trace 的 GO-2026-5932 模块级项；不是零漏洞。 |
| 启动、健康与隔离清理 | passed | `.ai/evidence/data-plane/product-readiness/20261024T113127-f5d6a1b0/{receipt.json,test.log}`；`.ai/evidence/deployment/dual-hosting/final-6d1345c-attempt2/remote/evidence/*/{readyz.json,app.log}` | 真实 Panel/Agent/核心链及部署 readyz，数据面自有 schema/core/PG 清理；原核心进程身份前后一致、Xray hash 未变；部署 owned-after-cleanup 为空。 |
| 同候选 HTTPS 双托管升级 | passed | `.ai/evidence/deployment/dual-hosting/final-6d1345c-attempt2/{result.json,refs.json,artifact-associations.json}`；四个 browser report；`remote/evidence/*/{schema_migrations.txt,fingerprints.sha256}` | 独立读 raw SQL，spa/api 均 5→6，原五行（含 checksum/applied_at）逐行一致；四 browser report passed；目标 panel SHA 与最终 panel-amd64 均为 `f6f90e49b07d8226b34ebfe2f443a6ee255daa398f7263d494bc5ed8e87fcd46`。仅隔离 nginx 容器模拟宿主，不是实际 host/DNS/公有 CA，不是 Agent 停止证明。 |
| 升级证明/批量/Agent/协议重点回归 | passed | runner 原始日志及对应 `upgrade_open_test.go`、`batch_durability_test.go`、`review_commit_test.go`、`realcore_test.go` | 对关键断言进行定向审查，详见下文；此门禁仅覆盖已列举重点回归，不对所有契约背书。 |
| 公共 API 与业务契约符合性 | failed | `.ai/design/product-readiness/backend.md` §4.3/§10；`internal/panel/store/resources.go:270-281,1002-1053`；`internal/panel/api/admin2.go:316-335,740-747` | 模板引用 409 与有历史方案退役均未实现；见 F1/F2。 |
| 长期契约和升级交付入口同步 | failed | `docs/design/implementation-contract.md:29-30,80`；`docs/deployment/production.md:297-335`；`deploy/manage-linux.sh:38-44` | 仍称游标未实现、恢复期间删会话被冻；升级入口没有设计要求的 check/evidence 操作，回退示例直接重启旧版可写服务；见 F3/F4。 |
| 必要行为覆盖充分性 | unverified | `.ai/design/product-readiness/backend.md` §10；`internal/panel/store/subscription_delete_test.go`；unexecuted 声明表及下表 | 缺设计要求的两种 DELETE 成功/拒绝/守恒场景，既有删除测试仍约束旧物理删除语义。容器真实故障场景也没有完整等价覆盖证据，不能由全登记 PASS 推断通过。 |
| OpenAPI/API 客户端生成、Rust 工具 | not_applicable | backend 设计 §4.3；DEC-009；go.mod | 项目无此生成链且保留 Go；未执行任何生成或自动修复。 |

## 重点风险独立核查

### 有效证据，不要求无意义重跑

1. **Open 短命 proof**：审查 `store.go` migrate 与 `readiness.go:79-121`。preflight 在 advisory lock 内；逐文件事务先锁 control 再锁事实表，重核 DB/evidence；失败文件回滚，已提交前缀保留。只有本次实际提交文件才构造栈内 receipt；postflight 用 READ COMMITTED 在锁后读当前摘要与语义，检验无 pending。`TestUpgradeOpenControlledMigration` 同时有 no-pending 正对照、5/2 前缀到 6、后续 Open 拒绝旧 proof；`TestUpgradeOpenFinalProofRecheck` 覆盖 business-write/material/expired-fence，`TestUpgradeOpenPostMigrationSemantics` 不允许匹配摘要绕过语义错误。这些用例确在最终日志通过。
2. **永久幂等与 task**：核对 `batch_durability_test.go` 的 cleanup 后 receipt 仍在、同请求不改变成员版本和控制 revision、异请求冲突、原 task ID 重试新 revision、旧回执隔离、其他 Agent/kind、回填歧义、失败文件回滚。不是只比 200/计数。未把这组结果误当 DELETE 退役验证。
3. **周期/移机**：最终选择包含 closed-loop-b、period/usagecalc、身份与快照组。相关断言覆盖当前账本、历史切片、连续窗口并集/间隙、旧 terminal 回执不污染新实例；当前结果可复用。但真实备份恢复、跨宿主故障链不从这些局部测试推出。
4. **恢复/会话**：API 当前 logout、本人的 session DELETE 保留 CSRF/身份且不走普通 requireWritable；普通 credential/token rotations 仍冻结；设计明确当前会话 DELETE=400、不撤销，logout 可用；已有令牌读取与缺令牌 409 必须区别。旧工程契约反而仍写删除会话冻结，属于 F3，不修改正确实现迁就旧文档。
5. **Agent 原子性/收紧**：最终 state/supervisor/xray/reporting 与显式 xrayreal 组真实执行；`review_commit_test.go` 断言失败不推进水位、intent 保留、重启恢复与采样串行；真实 core crash、overload、既有 TCP 终止和本地到期有运行记录。真实数据面采样计量为 70096 字节，payload 为 69632 字节，二者不混为同一个数字。
6. **协议与负例**：native 保留测试逐键比对，无法投影明确 UnsupportedError。最终 credential guards 原始日志包含明确 wrong-pin 诊断、新连接 server identity `invalid request` 诊断、origin_counts `before=1 after=1 invalid=0 plaintext=0 other=0`，不是任意非零、EOF 或超时算拒绝。单独 race 目录属于较早来源，只作为补充；本次同冻结树全50再次实际执行这两例，拒绝 oracle 没有弱化。
7. **数据面 receipt**：对 source_files/fixture_assets/binaries 三组逐项重新 SHA256，0 mismatch；base_commit 为冻结候选；原核心身份集合前后一致；临时目录移除、PG exit=0。原始 test.log 记录真实 API 注册→snapshot→完整回源→采样→ACK 丢失同批重发 PG 不增长→既有 TCP 1.068s 终止→新连接拒绝→新导出恢复、旧凭据仍拒绝。仅 loopback DEV + 临时 TLS 控制跳，不是 HTTPS 全链生产验收。
8. **双 HTTPS 的失败历史**：复用的是最终 attempt2 的 raw exit=0/四报告/SQL/fingerprint，不以外层 wrapper 的错误当通过，也不抹掉第一次目录护栏拒绝及第二次误期待人工 summary 的失败记录。不重复运行已清理的远端产品场景，不触碰原部署。

### 15 个未执行声明：逐项判断

这些声明全部仍为 **NOT_RUN**，不是 PASS。后续承接依主设计 §6、backend §10 与 phase 计划“浏览器两条旅程/两种托管/完整客户端矩阵”行；其中当前 backend 必需的缺口不能仅移交以后就放行。

| 声明 | 当前 backend 必要性及证据关系 | 结果/责任 |
|---|---|---|
| TestReviewOutOfOrderFinalAndPeriodicSamplesOverbill (measurement_candidate) | 此标签在 100→200→150 上要求 total=200，是未批准的不同 reset 策略；设计没有授权改成高水位。当前生产路径采用采样串行化，已有 integration sampling order 及 stable reset 断言。不能将实验失败/未跑等同已确认生产重复收费。 | 当前实验策略 not_applicable；不是关闭所有计量风险，未来策略需主会话裁决。 |
| TestStateDirACLIsRestricted (Windows) | 当前 Linux 后端运行门禁已有 Unix 权限验证；Windows ACL 实机与 Windows 身份代码并非 Linux 运行证据。编译不算 ACL PASS。 | 非 Linux runtime 待 web-demo/发布平台责任人，未作全平台交付声明。 |
| TestDockerClientMatrix | 本阶段已验证 local 20 supported+1 unsupported，与官方核心版本绑定；Docker 54格/远端环境/全部客户端不等价。设计明确完整矩阵承接 web-demo。 | web-demo 必须执行安全的新 fixture 或逐格等价覆盖；不能因本阶段矩阵绿消项。 |
| TestDockerACKLossAcceptance | 核心 ACK 去重目标已有同候选真实数据面 sampler/outbox/PG 原始证据；旧 Docker 包装本身不必须重复。 | backend 核心断言可复用；容器包装后续 web-demo。 |
| TestDockerFaultAcceptance | PG outage 下未 durable commit 不 ACK、Redis outage 的真实降级、实际 ENOSPC 下保留队列是故障边界；mock/unit 和健康 happy path 不能自动等价。当前没有完整同候选真实故障关系表/运行证据。 | 当前 backend 故障覆盖 unverified，backend-test 补最小隔离等价证据；严禁直接操作旧栈。 |
| TestDockerBatchPartialFailure | task/revision 隔离有 PG 运行证据，跨两 Agent 一台离线的用户可见部分完成不是相同断言。 | backend task 层可复用；两宿主/页面完整场景由 web-demo 承接。 |
| TestDockerConfigSafety | 回滚保持当前授权、不复活被撤销用户已有 Manager/真实核心回归；容器挂载、跨节点存活、最终面板反馈没有整套同环境证明。 | backend 局部可复用；容器/用户链待 web-demo，不称旧套件通过。 |
| TestDockerAgentVersionUpgrade | 新面板/旧 Agent 请求字段兼容与真实旧 Agent 二进制/SQLite 升级并非一回事；HTTPS fixture 无 Agent。 | 真实 Agent 跨版升级必须在 US-PR-016 web-demo 闭合；PG 停止材料检查不替代它。 |
| TestDockerCoreVersionSwitch | 官方 Xray 26.3.27 的当前运行不证明核心版本切换与回退。 | web-demo/部署责任人承接实际版本切换；当前不扩大已验证核心版本声明。 |
| TestDockerLifecycleDiagnose | 辅助诊断读取既有特定 fixture；无 fixture 的返回不证明业务。不是独立本阶段必跑业务门禁。 | 工具执行 not_applicable；用户异常定位仍需 web-demo，不能用工具空跑算通过。 |
| TestDockerLifecycleRecover | 此 helper 会修改特定旧 fixture、重启既有两 Agent；不能为补计数越权执行。必要性在“本次是否污染该 fixture”，本次无此操作。 | 此恢复 helper not_applicable；新隔离资源清理由本次 receipts 证明。 |
| TestDockerRecovery | 真实 pg_dump/恢复/旧 Agent 状态副本与拒绝旧授权的联动，不由 recovery-mode API 或新空库升级证明。 | backend 事务冻结证据可复用；真实恢复演练在 US-PR-014/016 web-demo 必须关闭。 |
| TestDockerAcceptance | 包括双 Agent、UDP/TCP、重启与离线后撤权的整链；本次数据面仅证明单候选已列 TCP 路径。 | 按设计交 web-demo 完整故事/网络矩阵；不称当前整套通过。 |
| TestPlatformRealCore | 环境绑定本地专用 PG+Redis，部分真实链与当前 DataPlane 重叠，但不是整函数已跑。 | 核心 TCP/账本可复用；其余跨场景在 web-demo 对照完整断言，需安全隔离，禁止拿旧 .devdb 补跑。 |
| TestXrayVLESSRealityVision | 当前 REALITY 握手/配置校验不等于独立 Vision 流量案例。 | US-PR-004/007 客户端兼容矩阵交 web-demo，Vision 对外支持声明前必须跑对应有效案例。 |

当前阶段拒绝已有独立 API 缺陷作为充分依据；对上表未核完整的环境故障保持 unverified，不因“很多绿”或“没有 Docker”自动 N/A，也不要求运行会触碰旧 fixture 的历史套件。

## Findings（未解决）

### F1 / P1：有绑定历史的服务方案仍物理删除，未实现退役

- **location**：`internal/panel/store/resources.go:1002-1053`；`internal/panel/api/admin2.go:316-335`。
- **evidence**：backend 设计 §4.3 删除语义表与同事务判定顺序；`migrations/0001_init.sql:127-137` 的 member_bindings.subscription_id 无删除级联；`internal/panel/store/subscription_delete_test.go`。
- **事实**：实现只查当前未归档绑定成员，解绑归档成员后直接 `DELETE FROM subscriptions`；没有历史计数、archived 更新、retired 字段或已归档幂等分支。存在真实 member_bindings 历史时 FK 会拒绝，dbErr 将 23503 映射为校验错误；与设计要求的成功退役冲突。旧测试主要验证无历史物理删除，不能证明设计的新分支。
- **impact**：US-PR-005/010/016 的历史保留/退役工作流阻塞；消费者无法收到 retired=true。不是仅注释或命名问题。
- **suggested_fix / owner**：backend-dev 按已定设计事务实现有历史归档、无历史物理删、有现存绑定409、已归档幂等；backend-test 增加真实 PG/API 的历史/账本/关联守恒及响应断言，先红后绿。不要删历史绕 FK。

### F2 / P1：在用模板可被删除并静默清空方案引用

- **location**：`internal/panel/store/resources.go:270-281`；`internal/panel/api/admin2.go:740-747`。
- **evidence**：backend 设计 §4.3 要求未归档方案引用时409 CONFLICT/details.reason=template_in_use；`migrations/0001_init.sql:85` 为 ON DELETE SET NULL。
- **事实**：handler 不做引用检查，store 直接 DELETE；没有未归档引用计数或409。DB 成功后清掉方案 template_id，返回200 deleted。
- **impact**：正在交付成员的方案客户端模板被悄悄清空，破坏已确定公共契约和成员配置输出预期；不能等到 frontend 验收才修后端。
- **suggested_fix / owner**：backend-dev 在一致事务内阻断未归档引用，返回稳定 reason/引用数；backend-test 覆盖引用409且引用不变、解除引用后成功、仅归档引用可删、正常无引用删。不得把前端文案改回静默删除来迁就实现。

### F3 / P1：长期公共契约仍描述旧列表和恢复语义

- **location**：`docs/design/implementation-contract.md:29-30,80`，`docs/reference/api.md`。
- **evidence**：backend 设计 §4/§11 文档漂移门禁；当前 API cursor 与 session 救援路由；Git 差异未同步这两份契约文档。
- **事实**：冻结实现契约仍写 next_cursor 恒 null/分页未实现；恢复 enter 描述删会话被冻结；缺新 readiness/分页/对象形状与退役引用拒绝的完整说明。
- **impact**：真实消费方及运维将依错误公共契约实施；设计明确要求同批同步，不是无关文字美化。
- **suggested_fix / owner**：backend-dev 与文档责任人按唯一设计源同步长期接口说明，分清 me/usage 对象、本人 session 救援、当前session 400及 logout；F1/F2 实现后同步响应和调用者，验证 frontend types/封装。

### F4 / P1：升级交付入口没有可执行的材料预检步骤，回退示例允许旧版直接写

- **location**：`deploy/manage-linux.sh:38-44`；`docs/deployment/production.md:297-335`；`docs/deployment/docker.md:226-229`。
- **evidence**：backend 设计 §5.3/§6.4.4/§6.9/§12，当前 CLI check、PANEL_UPGRADE_EVIDENCE_FILE 和 Open 的阻断实现。
- **事实**：install 仅 stage/切 current 并提示 enable --now；文档升级路径直接 stop→切链→start，没有新候选 panel check --preflight --evidence 或只读材料挂载步骤。旧版技术回退示例 stop→旧链→start，没有设计要求的禁止旧版授权/账本写及已提交迁移前缀核对。Open 已内置阻断，所以此处不是声称会绕过数据库安全门禁，而是正常旧 Agent 环境按公开步骤会卡在 unknown/blocked，操作者缺解除路径；回退则给出不符合设计的可写路径。
- **impact**：US-PR-016 的可部署/可升级目标未交付完整；隔离无 Agent 的 HTTPS fixture 无法发现旧 Agent 材料操作缺失。
- **suggested_fix / owner**：backend-dev/deployment-doc 交付安全、可复现的 preflight/材料只读挂载/失败前缀/技术回退停写步骤；backend-test 用有 Agent 证明需求的隔离 fixture 验证阻断、材料通过及失败退出。不得越权改真实 host 或启动旧栈验证。

## 未验证与承接

**当前 backend 修复/复验，不可转下游豁免：** F1/F2 的两类 DELETE 与消费者契约；F3 公共文档同步；F4 升级入口/材料流程及回退停写；PG/Redis/真实磁盘故障的最小有效证据或逐断言等价关系。责任 backend-dev/backend-test，位置 backend 设计 §4.3/§5.3/§10、本报告 Findings 与上表。

**后续正式 frontend/web-demo 必须承接：** 18条完整管理员/成员故事及异常、移动/键盘/主题三态、全部实际声明支持的客户端/平台、Vision 流量、两 Agent 离线/UDP/部分完成、真实备份恢复、真实 Agent/核心跨版、systemd 安装、实际宿主 nginx/DNS/CA。位置主设计 §6、frontend §4/§8.3、backend §10、phase 计划验证责任表。准备好的219单测/25浏览器仅为候选技术证据，不标其它 phase 正式完成；最后承接阶段不得保留必要未验证项。

## 执行、派生物和恢复

- 本轮没有重复有效全50，没有重跑已清理数据链/远端部署；执行的是只读 Git/源码检查、JSONL/SQL/指纹独立解析、现有 ELF 检查。没有新增 PG schema、核心或容器，因此没有本轮测试资源待清理。
- 没有运行 OpenAPI、客户端生成、代码格式化/自动修复；生产/测试/迁移/索引均未修改。唯一新文件为本报告与 audit 附件，均位于主路径 `.ai/quality`。
- change_scope 九项全 false。独立报告完成不表示实现已修复。
- 恢复规则：调用方交回上述 dev/test；修复后冻结新候选，最小补跑受影响 DELETE/API、升级流程和相关消费者。原234文件指纹对应证据如被修复影响则重新验证相应范围，未变的有效链路可复用；不要为计数盲跑全量。新一轮独立 accept 写新报告，不覆盖本次拒绝或上游失败记录。
