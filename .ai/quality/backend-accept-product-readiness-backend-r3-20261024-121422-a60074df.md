# backend-accept / product-readiness / backend / revision 3

## 结论

**REJECTED；task_completion.status=failed。** 本轮生产行为（DEC018 收窄外部材料门禁）本身未发现 P0/P1 逻辑缺陷，权威 store 证据与真实恢复链证据均独立核对通过；但本轮变更**打破了一个既有已注册后端测试**，且该测试在变更后从未被执行，构成受影响测试门禁失败。

- 验收对象：主候选 `3709328`（`/home/compile/Work/proxypanel-worktrees/pi-web-agent-976ab1df-ea71-4385-aaac-ac7448e721c4`）。`/home/compile/Work/proxypanel` 在其后仅增加 web-demo 文件（`c681219`）；本报告涉及的 store/api/manifest/文档文件两端字节一致。
- 唯一生产改动：`internal/panel/store/readiness.go`（sha256 `68b13c31…`），文档 `.ai/design/product-readiness/backend.md`、`.ai/decision-log/product-readiness.md` DEC018、`docs/operations/backup-recovery.md`；测试 `readiness_test.go`、`upgrade_open_test.go`、`scripts/backend-test-manifest.json`。
- 证据（相对主仓 `/home/compile/Work/proxypanel`）：`.ai/evidence/backend-test/20261024T113542Z-7676d17a/`、`.ai/evidence/web-demo/C/r3-final-green-20261024T115143Z-b7506f/`。
- 独立审计：`.ai/quality/backend-accept-product-readiness-backend-r3-20261024-121422-a60074df-audit.json`。旧 R2 ACCEPTED 报告保留原样；本轮按生产变更重新验收受影响门禁。

## 范围、来源与边界

输入：`.ai/quality/product-readiness-backend-r3-handoff.md`、`product-readiness-backend-r3-restore-closure.md`、只读诊断 `webdemo-restore-startup-readiness-20261024T1200Z.md`、主设计/PRD/stories/decision、C 证据与 store 证据。依据 acceptance-contract、agent-task-output-contract、verification-evidence-contract、core/backend quality、backend-accept 与 backend quality。

只读 Git、源码、断言、JSON/JSONL 与证据字段；未改业务/测试/设计/state，未派代理、未 push/deploy，未操作 203/Agentlab/15432，未读取或输出 token/key/rawstate（`grep` 证据目录未发现 `Bearer`）。只新增本报告与 audit，`change_scope` 九键全 false。

## 必要门禁

| name | result | evidence | 说明 |
|---|---|---|---|
| 候选与运行输入身份、证据可复用性 | passed | 本轮 audit；`.ai/evidence/backend-test/20261024T113542Z-7676d17a/result.json` inputs | 248 个记录输入对当前主路径逐项 SHA256 0 mismatch、0 missing（含 `readiness.go`、`readiness_test.go`、`upgrade_open_test.go`、manifest、`abc_readiness_test.go`），`inputs_stable=true`；C runner `assets` 11 个与 tools 5 个 SHA256 全部匹配当前字节。 |
| 编译/构建 | passed | 同上 `upgrade/delivery` execution；C `runner-receipt.json` `test_binary_sha256`、build 日志 | `upgrade/delivery` 通过要求真实 `UPGRADE_TEST_PANEL_BINARY`（真实候选二进制）；C 侧真实构建 test/agent 二进制并有 SHA256 与 exit0。 |
| 权威 store 证据（独立重解析原 JSONL） | passed | 证据目录 `0-tests.jsonl`、`1-tests.jsonl`、`result.json` | 独立统计：0 组 61 个 unique case/subcase（run 61 / pass 62）、1 组 39 个（run 39 / pass 41），`fail/skip` 均为 0，expected⊆ran 无 missing，两组 status=passed；真实 panel 二进制 + 私有 PG socket/psql。早期四次 env failure 保留、不计产品红。 |
| 生产改动正确性与设计/决策一致性（DEC018） | passed | `readiness.go:365–400,415–430,520–560,690–700`；`store.go:200–300`；backend §5.3/§6.4.4；DEC018 | 单一快照内先算迁移状态再决定 `requireExternalEvidence := len(r.Pending)!=0 || baseline!=""`；空库有 pending 且显式损坏材料仍 `EVIDENCE_INVALID` blocked；pending 时逐 Agent 证据门禁保留；schema-current 0 pending 跳过外部材料但迁移 checksum/账本/周期/归属/owner/orphan/batch-hash 与 typed `READINESS_BLOCKED` 全部仍在（`TASK_ORPHAN` 例外路径有断言）。 |
| 旧 proof 不被复用为下一迁移授权 | passed | `readiness.go:541–546`、`upgrade_evidence_stop.go:88`、`store.go:250–258`；`upgrade_open_test.go` 全组；`upgrade_delivery_test.go:80–92` | stop review 绑定 `PostgreSQLSHA256`/fingerprint+generation/revision；后续新迁移的 baseline=="" 分支要求 review 绑定**当前**指纹，旧材料指纹不符即 `AGENT_STOP_REVIEW_INVALID`（该分支由 wrong-hash 用例实际执行）。后续 schema-current `Open` 不再读取 proof，是 DEC018 的目标行为而非“旧 proof 通行”。 |
| pending+Agent 仍拒绝、current+Agent 正向 | passed | `readiness_test.go:164–190`、`upgrade_open_test.go:221–250`；`result.json` ran | `TestUpgradeReadinessPendingAgentRequiresEvidence` 断言 pending 仍 `unknown/AGENT_EVIDENCE_MISSING`；`TestUpgradeReadinessLegacyAndUnknown` 断言 current+Agent ready 且无该码；`TestUpgradeOpenSchemaCurrentAgentWithoutTransitionEvidence` 先正向 Open 成功，再插入 `TASK_ORPHAN` 断言 `READINESS_BLOCKED` 且 `domain.CodeReadinessBlocked` typed。 |
| 公共 API/诊断投影未被误放宽 | passed | `internal/panel/api/readiness.go`；`readiness.go:117–170`；`cmd/panel/check.go`；`web/src/lib/readiness.ts`、`RuntimeView.vue` | HTTP/CLI 仍调用同一 `CheckUpgradeReadiness`；内部 blocked/unknown 仍进 `checks/blockers`，`schema.checksum_ok` 三态不变；投影只按内部结果生成，不推断通过。 |
| 受影响测试通过（含既有已注册选择） | **failed** | `internal/panel/api/abc_readiness_test.go:49–57`；`scripts/backend-test-manifest.json:797–813`；`result.json`（仅选 store 两组）；`r1-integrated-targeted/result.json`（其 api 执行日志指向变更前 `20261024T043929Z`） | 见 P1：该测试在 `registerAgent` 后仍断言 `assertSource("unknown")`，而变更后同一路径（schema-current、0 pending、无证据文件）返回 `ready`；变更后没有任何一次执行包含 `api/abc-readiness`。 |
| 真实恢复链证据（C） | passed | `.ai/evidence/web-demo/C/r3-final-green-20261024T115143Z-b7506f/{runner-receipt.json,receipt.json,test.log,main-verification.json,restore-evidence.json}` | 20/20 assertion passed、0 failed/blocked；两套新 PG17 initdb、真实 `pg_dump -Fc`/`pg_restore`（87032B、restore exit0）、真实 Agent/Xray 流量、queue/ledger 精确重放、ACK 已删缺口 49546B/1 batch 保持记录、旧凭据三次真实拒绝、退出保留 generation、接受后仅 1 个有效成员且新导出真实连通。`test.log` 为 `--- PASS (29.68s)`；`main-verification.json` status=verified、runner=pass。 |
| C 断言修正是契约对齐而非放弱 | passed | C test 断言与 `backend.md:205,249–251,542–551`、`snapshot.go:27`、`api.go:382–402`、`ops.go:882–891` | 订阅 token 恢复期只读 200（无 token 才 409）、snapshot 409、退出不二次轮换、coordination receipt 移到 verified exit 之后、单有效成员=1、异 payload 用语义字段变化+`usagecalc.PayloadSHA256` 校验——全部与既有生产/设计一致；未为绿改断言或删安全检查。 |
| 数据面/宿主清理与隔离 | passed | 同 C `runner-receipt.json`；本轮 `ps` 只读核对 | `temporary_directory_removed=true`、`skipped=false`、`timeout_recovered_owned_core_pids=[]`、`own_children_finished=[]`；共享 15432 六个 PG pid 与 starttime 前后一致、625047 AgentLab 父进程未被信号；15432/8443/8445 未作为 mutator。`/tmp` 私有目录已删。 |
| host identity 归因 | passed（ENV，不阻断产品结论） | C `existing_object_delta`、`isolation_diagnosis`、`status_caveat` | 唯一变化为外部 625047 的 Xray 子进程 1732329→1733099（约每 2s 自换 PID），runner 未 signalled、未写 host-clean；属 ENV/实验室外部 core 不稳，不影响 20 项产品断言，但须在最终物理运行协调中单独处理。 |
| 文档隔离入口可执行 | passed（含 P2） | `docs/operations/backup-recovery.md:19–23`；`cmd/panel/main.go:79–129`；`backend.md:502,604`、DEC018 | 文档步骤 2/3 现在可执行：schema-current 恢复库可正常 `Open`，随后立即 `POST /admin/recovery-mode/enter` 冻结并轮换 generation；`PANEL_LISTEN` 默认回环。P2-3 记录“进入前窗口靠流程隔离”的残余风险。 |
| 阶段承接边界 | passed | R3 handoff/restore-closure；主设计 §6；C `not_run` | 本报告只覆盖 backend store/API/恢复链；完整 web-demo（A/B/D/E 及 18 故事）、Windows 平台、真实 203/8443 部署、WAL/PITR 与旧二进制回退仍承接，不因此自动拒绝 backend，也不标 feature 完成。 |
| Rust/Clippy、OpenAPI/SDK 生成 | not_applicable | DEC009；项目 Go + 手写 TS；`docs/reference/api.md` | 无该生成链，不新增工具或生成物。 |

## Findings

### P1 既有已注册 API 测试仍断言旧语义，变更后将失败

- `severity`: P1
- `location`: `internal/panel/api/abc_readiness_test.go:49–57`（`assertSource("unknown")`）；选择注册处 `scripts/backend-test-manifest.json:797–813`（`api/abc-readiness`，expected 含 `TestABCReadinessMountedDuringRecovery`）。
- `evidence`: 本轮 audit（静态路径推导）；`.ai/evidence/backend-test/20261024T113542Z-7676d17a/result.json`（只选 `store/upgrade-preflight`、`upgrade/delivery`，未含该测试）；`.ai/evidence/backend-test/r1-integrated-targeted/result.json`（其 `api/abc-readiness` 执行日志指向变更前 run `20261024T043929Z-cfa40b3`）。
- `impact`: 纯静态不可驳：该测试先 `store.Open` 到 schema-current（6 个迁移全部 applied、0 pending、构造中无 `PANEL_UPGRADE_EVIDENCE_FILE`），再 `registerAgent`，随后调用公共 `GET /api/v1/admin/upgrade-readiness`。按 `readiness.go:381` `requireExternalEvidence=false`，不会产生 `AGENT_EVIDENCE_MISSING`，同一结果的 `status` 为 `ready`；而测试硬要求 `unknown`，`assertSource` 会 `t.Fatalf("diagnostic status: %+v")`。即“受影响测试通过”门禁为红，且本轮权威 run 的 selection 范围刚好绕过了它；`store` 层同语义断言已改、API 层未改，属遗漏而非设计分歧。
- `suggested_fix`（不代实现）：把该断言改为断言 current+Agent 仍 `ready` 且不含 `AGENT_EVIDENCE_MISSING`（同时保留其后的 checksum 篡改仍 `blocked` 断言，作为“API 投影未整体放宽”的正/负对照；如需，可在 API 层补 pending-unknown 定向用例或明确复用 store 用例）。修复后重跑 `api/abc-readiness` 与 `store/upgrade-preflight`、`upgrade/delivery` 三个选择，再重新验收。

### P2-1 “权威”恢复证据里含启动时的占位 `status=fail`

- `location`: `tests/e2e/webdemo_restore_test.go:710–716,748`；证据 `.ai/evidence/web-demo/C/r3-final-green-20261024T115143Z-b7506f/restore-evidence.json`。
- `evidence`: 该文件仅 5 键、`status:"fail"`，是测试开始即写入的占位（之后未重写）；同一 run 的 `receipt.json` status=pass、`runner-receipt.json` status=pass、`test.log` `--- PASS`。
- `impact`: handoff 把该文件列为权威输入，读者可能误读为产品失败；不影响已核对断言与结论。
- `suggested_fix`: 交接/报告只引用 `receipt.json`+`runner-receipt.json`+`test.log`，或让占位文件在收尾时按最终状态重写；不改断言。

### P2-2 “旧 proof 不能授权**下一次新迁移**”缺少显式回归

- `location`: `internal/panel/store/upgrade_open_test.go:204–216`（旧的“later Open 必须拒绝”断言已替换为“必须成功”）；`upgrade_evidence_stop.go:88`。
- `evidence`: 现执行选择只覆盖 wrong-hash（`upgrade_delivery_test.go:82–84`）、迁移中材料变更/过期 fence（`upgrade_open_test.go` 子用例）；没有“完成一次迁移后再出现新迁移文件、沿用旧材料 → 必须拒绝”的用例。
- `impact`: 机制由同一 `PostgreSQLSHA256` 比较分支保证，属覆盖缺口，非当前行为缺陷；但这是本轮安全边界的核心承诺。
- `suggested_fix`: 迁移完成后追加一个受控 0007 迁移文件，用旧材料再 `Open`，断言 `READINESS_BLOCKED` 且 `AGENT_STOP_REVIEW_INVALID`。

### P2-3 恢复副本进入协调模式前的窗口仍靠操作流程隔离

- `location`: `docs/operations/backup-recovery.md:19–21`；`cmd/panel/main.go:79–129`。
- `evidence`: schema-current 恢复库可直接 `Open` 并监听（默认回环），`enterRecoveryMode` 需 HTTP 登录后才冻结；期间若监听被改为非回环或已有 Agent 主动连入，快照仍会按旧备份授权返回（测试按设计先阻断外部通信并立即进入）。
- `impact`: 新引入的“可启动”能力把隔离责任前移到操作者；设计已写明“未核对不得接 Agent/开放业务”，C 也验证了 isolate+立即 enter，故不阻断，但值得在发布说明中强化。
- `suggested_fix`: 文档加一条启动前自检（仅回环/防火墙/Agent 停止），或后续提供不提供业务服务的恢复入口；不要求本轮实现。

## 未验证及后续承接（不是本条 backend 结论的延期项）

1. **P1 修复后的定向重跑**：`api/abc-readiness`、`store/upgrade-preflight`、`upgrade/delivery`（owner=backend dev/test）。位置：上述三选择 + P1 location。
2. **完整 web-demo**：A/B/D/E 及 18 故事、平台/客户端矩阵、真实 203/8443 部署（owner=web-demo/delivery）。位置：主设计 §6、backend 设计 §10、C `not_run`。
3. **外部 ENV core churn**：625047 的 Xray 子进程周期性自换 PID 使宿主身份基线不可用于“host clean”结论；最终物理运行需隔离或显式豁免（owner=web-demo/delivery）。
4. **恢复演练未覆盖项**：WAL/PITR/离机备份、旧发布二进制停写回退（C 已 `not_run`，不得据此宣称生产 RPO/RTO）。

## 产物、清理与恢复

仅新增本报告与 audit（均在 `.ai/quality/`）。未创建运行资源、未启停 PG/Agent/Xray、未改 state；无本轮待清理派生物。`change_scope` 九键全 false。P1 未修复前本结论保持 REJECTED；修复后应新建独立验收报告，不沿用本报告。
