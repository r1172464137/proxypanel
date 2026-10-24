# backend-accept / product-readiness / backend / revision 3（复验）

## 结论

**ACCEPTED_WITH_IMPROVEMENTS；task_completion.status=success。** 上一报告的唯一 P1 已关闭，未发现新的 P0/P1；仅保留上一轮 3 个 P2。本轮只放行 backend 阶段，**完整 web-demo 仍未完成，不因本结论自动放行**。

- 复验对象：主 `fea3187`（`3709328` 之后仅 web-demo 测试资产）。`3709328` 的生产改动字节未变：`internal/panel/store/readiness.go` = `68b13c31…`；`store/readiness_test.go`、`upgrade_open_test.go`、`scripts/backend-test-manifest.json`、backend 设计/决策/`docs/operations/backup-recovery.md` 相对 `3709328` 的 diff 为空（0 行）。
- 唯一源码/测试改动：`internal/panel/api/abc_readiness_test.go`（`7fb82fb9…` → `b7683f84…`，`assertSource("unknown")` → `"ready"` + 注释），仅测试资产层。
- 新权威证据：`.ai/evidence/backend-test/20261024T122047Z-8d27c817/`（api/abc-readiness）。store/delivery 继续复用 `.ai/evidence/backend-test/20261024T113542Z-7676d17a/`；C 恢复证据 `.ai/evidence/web-demo/C/r3-final-green-20261024T115143Z-b7506f/` 未变。
- 上一轮报告与 audit 原样保留（仍为 REJECTED 的历史结论），本报告是独立新结论。
- 独立审计：`.ai/quality/backend-accept-product-readiness-backend-r3-20261024-122207-3fedab77-audit.json`。

## 范围与边界

最小增量：只读核对改动字节、新 run 的 result/原 JSONL/退出码/输入稳定性、P1 是否真关闭，以及旧 store/delivery 证据在新输入下是否仍有效。未改源码/测试/设计/state，未重跑任何选择，未启停 PG/Agent/Xray、未连接 15432、未操作 203/Agentlab/8443/8445，未派代理。只新增本报告与 audit；`change_scope` 九键全 false。

## 必要门禁

| name | result | evidence | 说明 |
|---|---|---|---|
| 生产/文档字节未漂移 | passed | `git diff 3709328 HEAD`（store/cmd/migrations/scripts/docs/design 为空）；本轮 audit；sha256 `readiness.go=68b13c31…` | `3709328..fea3187` 除 `.ai/` 与 web-demo 测试资产外无改动；本轮补丁只在 `internal/panel/api/abc_readiness_test.go`，未触及生产语义。 |
| P1 关闭：API 层断言与 DEC018 对齐 | passed | `internal/panel/api/abc_readiness_test.go:49–58`；`internal/panel/store/readiness.go:381`；`scripts/backend-test-manifest.json`（`api/abc-readiness`） | 现在 `registerAgent` 后断言 `assertSource("ready")`，与 schema-current/0 pending/无材料路径一致；紧随其后的 checksum 篡改仍断言 `blocked`，`recovery-mode/enter` 后仍 `blocked`，保留“内部阻断未被放宽”对照。不是删断言，是修正过期期望。 |
| 新证据原 JSONL/退出/输入 | passed | `.ai/evidence/backend-test/20261024T122047Z-8d27c817/{result.json,0-tests.jsonl,0-discovery.log}` | 独立重解析：`api/abc-readiness` passed，`run` 2 / `pass` 事件 3，`fail` 0、`skip` 0，expected⊆ran 无 missing，执行命令 `-run ^(TestABCReadinessMountedDuringRecovery\|TestUpgradeReadinessUsesConfiguredStoreNotRequestPath)$ ./internal/panel/api`，exit 0；日志原文 `--- PASS` 两条 + 包级 `ok`。`inputs_stable=true`，252 个输入对当前主路径逐项 SHA256 0 mismatch/0 missing（含修正后的 `b7683f84…`）。 |
| 旧 store/delivery 证据仍有效 | passed | `.ai/evidence/backend-test/20261024T113542Z-7676d17a/result.json` + 原 JSONL | 248 输入中唯一与当前字节不符的是 `internal/panel/api/abc_readiness_test.go`，属 `./internal/panel/api`，不在该 run 的 `./internal/panel/store`、`./cmd/panel` 选择内；其余输入 0 mismatch。61+39 实际 case/subcase、0 fail/skip/missing 结论不受影响，无需重跑。 |
| C 恢复链证据未变 | passed | `.ai/evidence/web-demo/C/r3-final-green-20261024T115143Z-b7506f/{runner-receipt.json,receipt.json,test.log,main-verification.json}`；三份 e2e 资产 sha256 | 20/20 断言、0 failed/blocked，`test.log` PASS，`main-verification` status=verified、`temporary_directory_removed=true`、`production_changes=false`；e2e 源码与 manifest 哈希仍与本轮一致。host identity=false 仍归因外部 Xray 子进程自换 PID（ENV，不阻断产品结论）。 |
| DEC018 安全边界保持 | passed | `readiness.go:381–393,541–560,690–700`；`upgrade_evidence_stop.go:88`；上轮已审执行证据 | `requireExternalEvidence = len(Pending)!=0 \|\| baseline!=""` 未变；pending+Agent 仍 `AGENT_EVIDENCE_MISSING`，空库+pending 显式坏材料仍 `EVIDENCE_INVALID`，current+内部异常仍 typed `READINESS_BLOCKED`，旧 proof 仍因指纹绑定不能授权新迁移。 |
| 阶段承接边界 | passed | R3 handoff/restore-closure；C `not_run` | backend 放行；web-demo A/B/D/E、18 故事、平台矩阵、203/8443 部署、WAL/PITR 与旧二进制回退仍承接。 |
| Rust/Clippy、OpenAPI/SDK 生成 | not_applicable | DEC009；Go + 手写 TS | 无该生成链。 |

## 遗留 P2（沿用，不阻断）

1. `.ai/evidence/web-demo/C/r3-final-green-20261024T115143Z-b7506f/restore-evidence.json` 仍是启动占位 `status:"fail"`（本轮复核仍为 fail）；权威结论取 `receipt.json`/`runner-receipt.json`/`test.log`。位置：`tests/e2e/webdemo_restore_test.go:710–716,748`。
2. “完成一次迁移后再出现新迁移文件、沿用旧材料必须拒绝”仍只有 wrong-hash 等间接覆盖。位置：`internal/panel/store/upgrade_open_test.go:204–216`。
3. 恢复副本进入协调模式前的隔离窗口仍依赖操作流程。位置：`docs/operations/backup-recovery.md:19–21`。

补充说明（非问题）：新证据只覆盖 `./internal/panel/api`，其一次性私有 PG 角色“执行后删除”未在 result.json 中留下角色生命周期记录，本轮无法从证据独立复核该清理动作；但证据目录不含密码/角色凭据文本，且未发现共享环境被改动的迹象（唯一输入漂移是上述测试文件）。

## 未验证及后续承接

1. 遗留 P2-1/P2-2/P2-3 的文档与回归补强（owner=backend dev/test/docs）。
2. 完整 web-demo A/B/D/E 与 18 故事、平台/客户端矩阵、真实 203/8443 部署（owner=web-demo/delivery）。
3. 外部 AgentLab core（625047 的 Xray 子进程自换 PID）在最终物理运行中的隔离/豁免协调（owner=web-demo/delivery）。
4. WAL/PITR/离机备份与旧发布二进制停写回退（C 标 not_run，不得据此宣称生产 RPO/RTO）。

## 产物与清理

仅新增本报告与 audit（均在 `.ai/quality/`），未覆盖上轮 REJECTED 报告。未创建运行资源、未启停任何服务、未改 state，无本轮待清理派生物。若后端生产字节或 store/delivery 输入再次变化，需新建独立验收，不沿用本结论。
