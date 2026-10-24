# 只读诊断：C 恢复案例被 READINESS_BLOCKED 阻断（启动/恢复可用性 vs 安全门禁）

- 诊断者：只读子诊断（不实现、不改生产/测试/设计/状态/权限、不跑 PG/core/部署、不派代理）
- 主 root `/home/compile/Work/proxypanel`，HEAD `d28e3df`（任务基线 `032f31c`；`032f31c..d28e3df` 仅 `.ai/super-run/...` 与 `scripts/webdemo-host-nginx-verify.sh`，无 store/cmd/docs/operations 改动，结论同样适用）
- C 候选 `2461596`（base `e92c6ba`，3 个自有文件），主尚未合并；证据 `C/{final,run1..run7}` 只读引用
- 本报告不是正式 accept，不把 C 的 `blocked` 当绿，也不为绿绕门禁

## 1. 唯一主分类（结论先行）

**BACKEND（真实启动/恢复可用性缺陷，而非“门禁太严”）：严重性 High；confidence high（对代码路径）/ medium-high（对“设计本意是每次启动都要重新取证”这一读法）。**

`store.Open` 把“升级过渡必须持有的**外部 Agent 停止材料**”无差别地施加到**每一次启动**，包括：

1. 迁移前缀已完整、checksum 干净、**0 个待迁移**、库内已有注册 Agent 的库（正是 pg_dump/restore 后的形态）：无 `PANEL_UPGRADE_EVIDENCE_FILE` → 每次 `Open` 都以 `AGENT_EVIDENCE_MISSING/unknown` 判 `unknown` → `READINESS_BLOCKED` → `cmd/panel` 直接 `log.Fatalf`，进程不监听。
2. 即使**带着**合法材料完成过一次升级，同一份材料在迁移提交后即失效（指纹/fence 绑定），下一次重启仍被拒（`AGENT_STOP_REVIEW_INVALID`）→ 每次重启都需要再做一轮“停流+停 Agent+管理员核对”的维护动作。仓库现有单测把这一行为当作正确并显式断言（见 §3）。
3. 由此，`docs/operations/backup-recovery.md` 的**灾备恢复**第 3 步（起隔离面板 → 管理员登录 → `POST /admin/recovery-mode/enter`）在“库内有 Agent”的情况下**没有任何合法可达路径**：面板必须先起来，而 `Open` 要求灾备场景拿不到的升级材料；`enterRecoveryMode` 只有 HTTP 入口，`cmd/panel` 只有 `check` 子命令。

安全门禁本身的**内容**（拒绝 without evidence、UNKNOWN 不当 ready、旧 proof 不永久通行、PG 不能推 Agent 队列 0、checksum/ledger/归属检查）**没有被证伪，也不应放松**。缺陷在**适用范围与缺少合法入口**，属 liveness/流程可达性，因此后续修复必须走设计裁决（主 D2），不能由实现者“加个开关”。

次级（不构成主分类，但交接必须写清）：
- **B（DOC/D2 缺口）**：规范本身没有定义“恢复协调入口不得被启动门禁挡住”的落地形态（设计只写“保留只读诊断入口”，未给 CLI/只读监听），`backup-recovery.md` 的步骤顺序与 `Open` 的实际前置条件直接矛盾。此项需要 D2 补规范，不能只改文档文字。
- **A（TEST/DATA 遗漏，部分成立、不充分）**：C 未尝试产品当前**唯一**已实现的材料路径（`PANEL_UPGRADE_EVIDENCE_FILE` + 它自己已经拿到的 stopped-state 副本 + `-inspect-state` 导出）。但这条路径要求把**灾备恢复**伪装成**计划内升级**（`postgresql_sha256`/fence/实例核对全部按升级语义填写），本身会把“恢复”错当“升级”、并绕开 `enterRecoveryMode` 才是灾备的正确顺序；所以“A 只是测试少做一步”不成立，最多是**未证伪的补充分支**。

## 2. 关键代码/文档证据（source:line + 原 raw）

启动路径（无 recovery/0-pending 例外）：
- `internal/panel/store/store.go:56-72`：`Open` → `s.migrate(ctx, dir, true)`（postflight 恒为 true）。
- `internal/panel/store/store.go:200-211`：advisory lock 内先 `checkUpgradeReadinessQ(...,preflight=true)`；`if proof.Status != "ready" { return readinessBlocked("upgrade preflight %s: %v", ...) }`。
- `internal/panel/store/store.go:287-292`：`if postflight { ... return s.checkPostMigrationReadiness(ctx, conn, receipt) }`（即使 `applied==false` 也做最终 READINESS）。
- `internal/panel/store/readiness.go:240-241`：`post-migration readiness %s` → `readinessBlocked`。
- `internal/panel/store/readiness.go:533-548`：`for _, a := range agents { ... if input == nil { r.add("AGENT_EVIDENCE_MISSING","unknown",1,"Drain with the old uploader or provide a consistent stopped-state export and external stop evidence."); continue } }` —— **对 `agents` 全表逐行**，无 `archived` 过滤、无 `pending migrations == 0` 条件、无 `recovery_mode` 例外。
- `internal/panel/store/readiness.go:159-165` 注释自称 `entering recovery is deliberately not gated here`，但该函数只有在 HTTP 服务已监听后才可达（`internal/panel/api/admin2.go:1002-1010`），与 `Open` 的顺序矛盾。
- `cmd/panel/main.go:102-111`：`Open` 失败 → `log.Fatalf("database: %v", err)`；`cmd/panel/main.go:70-73` 只有 `check` 子命令；`cmd/panel/check.go:19-42` 用 `OpenReadOnly`，仅诊断。**不存在** recovery/只读监听/离线进入恢复的 CLI。

材料与“旧 proof 不永久”绑定：
- `internal/panel/store/upgrade_evidence_stop.go:88`：`v.PostgreSQLSHA256 != r.DatabaseSHA256`（材料绑定**一次** PG 指纹）。
- `internal/panel/store/upgrade_evidence_stop.go:103-105`：`!v.Fence.ValidUntil.After(now)` 拒绝 + `v.ReviewedAt.After(now)` 等时序。
- `internal/panel/store/readiness.go:529-532`：`stopProof := *r; if baseline != "" { stopProof.DatabaseSHA256 = baseline }`；`baseline` 只在**本次真有文件提交**时构造（`store.go:288-291`），因此 0-pending 启动时材料必须绑定**当前**（迁移后）指纹。
- `internal/panel/store/upgrade_open_test.go:190-194` 正对照（材料就绪、0-pending → Open 成功）；`internal/panel/store/upgrade_open_test.go:209-216` 原 raw：
  `if err == nil || !strings.Contains(err.Error(), "AGENT_STOP_REVIEW_INVALID") { t.Fatalf("old proof must not authorize a later Open: %v", err) }` / `t.Logf("later Open correctly refused: %v", err)` —— **后续一次 Open 被拒被当作通过条件**。
- `internal/panel/store/upgrade_open_test.go:53-140` 的 `expired-fence` 子用例：fence 过期 → 最终验证拒绝 `AGENT_STOP_REVIEW_INVALID`，即 **fence 一到期，重启即不可用**。

现有单测覆盖面（回答“是否只覆盖 noAgent 空库”）：
- `internal/panel/store/readiness_test.go:128-162`：空库 → ready → migrate → ready；**无 Agent**（即“空库/无旧 Agent 免材料”的唯一正向覆盖）。
- `internal/panel/store/readiness_test.go:164-182`：`upgradeSeedAgent` 后只断言 `Status=="unknown"` 且含 `AGENT_EVIDENCE_MISSING` —— 断言的是**预检诊断**，从未断言“该库仍应可被合法启动”。
- `internal/panel/store/readiness_test.go:41-60` 的 `upgradeTestStore(t, old=true)` 会 `Migrate` 到全量目录=**schema-current**，但没有任何“schema-current + Agent + 无材料 → Open 成功/失败”的用例；结论：**0-pending / current-agent 的合法 `Open` 路径没有任何正向单测，只有被当作“正确拒绝”的反向断言**。

规范 authority 与现状的张力：
- `.ai/design/product-readiness/backend.md:502`：`blocked/unknown 停止对外服务并保留只读诊断入口`。
- 同文件 `:604`：`未配置且存在需证明旧队列时为unknown/blocked … 空库无旧Agent时无需材料`（条件是“需证明旧队列”，不是“库里有 Agent”）。
- 同文件 `:859`：`正常启动仍走 Open(ctx, Options) … 新库无旧数据可不提供材料`。
- 同文件 `:545`：`enterRecoveryMode 不受 getUpgradeReadiness 阻断…readiness 只阻止不安全升级/业务启动`；但未规定**业务启动被阻止时如何合法进入恢复**。
- `docs/operations/backup-recovery.md:19,21`（步骤 1/3）：先阻断通信、再“启动隔离恢复面板 → 登录 → `POST /api/v1/admin/recovery-mode/enter`”；与 `Open` 的前置条件直接冲突。
- `docs/deployment/production.md:326`、`docs/deployment/docker.md:243`、`docs/development/upgrade-stop-review-handoff.md:54`：`Open 还会在锁内重验 DB/材料；预检不是永久许可` / `旧材料不是后续任意升级或运行状态的永久许可证`。
  → “材料不得永久通行”是 authority；“**每次普通重启**都要求重新停流取证”**不是** authority 明文，且与 `backup-recovery.md` 的恢复/重启流程、与 `:604` 的“需证明旧队列”条件不一致。这是 C（范围）而非 A（材料齐不齐）的判断依据。

C 原始证据（只读，未复跑）：
- `.ai/evidence/web-demo/C/SUMMARY.md:41-60`：`READINESS_BLOCKED: upgrade preflight unknown: [AGENT_EVIDENCE_MISSING unknown 1 …]`；`steps 3–4 … currently unreachable after a plain restore, in cmd/panel and in the test harness alike (same store.Open path)`。
- `.ai/evidence/web-demo/C/final/test.log`：`webdemo_restore_test.go:1205: blocked at the restore readiness gate: [AGENT_EVIDENCE_MISSING]`；`--- PASS`（作为 blocked 收尾，不是绿）。
- `.ai/evidence/web-demo/C/final/receipt.json` `blocked_diagnosis`：`"gate":"READINESS_BLOCKED","stage":"store.Open of the restored database (post-migration readiness, before the panel can listen)"`，`"checks":["AGENT_EVIDENCE_MISSING/unknown/1"]`，`assertions {count:9,passed:8,blocked:1,failed:0}`。
- `.ai/evidence/web-demo/C/run6-fail-readiness-preflight/test.log`：`open owned restore store failed: READINESS_BLOCKED: upgrade preflight unknown: [{AGENT_EVIDENCE_MISSING unknown 1 Drain with the old uploader …}]`（早期即已复现同一门禁，说明不是最后一次改动引入）。
- `.ai/evidence/web-demo/C/final/receipt.json`：`ack_deleted_gap {batches:1, bytes:49546, disposition:"unrecoverable from the Agent queue … deliberately never re-imported"}`；`stopped_queue {batches:3, samples:3, bytes:148638, source:"Agent SQLite copy + shipped -inspect-state"}`；`source_revocation {credentials_revoked:1, active_credentials_after:0}`（撤回发生在 **backup 之后**，故**备份/恢复库内该凭据仍是 active**——这正是“恢复必须先进入协调、不得推旧授权”的原因，修复不得放开普通服务）。

## 3. 判断

1. C 的**事实陈述正确**：一个可还原、迁移前缀完整、checksum 干净的恢复库，因库内存在注册 Agent 而无合法材料/无合法入口，永远起不来；文档第 3-4 步不可达。这与 `readiness.go:546` 的实现范围一致，不是 C 的调用方式错误。
2. C 的**归因不完整**：真正的问题不是“恢复也需要安全证明”（这条本身合理），而是 (a) 该外部材料门禁被无差别施加于**非迁移启动**；(b) 材料绑一次指纹 + 有 fence 有效期，导致**每次重启**都要重新停流取证；(c) 业务启动被拒后**缺少规范允许的恢复协调入口**（`panel check` 只是诊断，不能冻结授权/轮换 generation）。三条中 (a)(b) 是 BACKEND liveness，(c) 是规范缺口。
3. **不成立的替代读法**：“旧 proof 不能永久通行”被用来解释“第二次 Open 必须失败”。这是对 authority 的过度延伸：authority 说的是材料不能作为**后续升级/运行状态**的许可证（不得因为曾经 ready 就跳过下一次迁移前的重验），并未要求**0 待迁移的普通启动**也重建一份“停流+停止 Agent+管理员核对”材料。单测把这个过度延伸固化成了期望值（`upgrade_open_test.go:209-216`），属于**测试把 bug 锁死**，修时必须同步由设计裁决改期望，不能悄悄改测试。
4. 安全边界**未发现被削弱**：`recovery_mode` 仍然是唯一冻结入口（`internal/panel/store/write_guard.go:23-34`、`ops.go:836-862`），`EnterRecoveryMode` 仍原子轮换随机 generation 并同步 `agents.control_generation`；`agent snapshot` 在恢复态仍 409。因此修复方向应当是“让协调入口可达 + 收窄材料门禁的适用范围”，**不是**删检查。
5. 但 `readiness.go:533` 对 `agents` **全表**（含 `archived`、含已完成迁移的 `node_server_history` 已登记归属）一律要求材料，这一点**没有 authority 支持**：`:604` 的触发条件是“存在需证明旧队列”，不是“存在 Agent 行”。这也是判断为 BACKEND 而非“用户用错流程”的关键。

## 4. 必要最小下一步

顺序不可颠倒；**不要**先合并 C，也不要为让 C 变绿而动门禁。

1. **主 D2 裁决（阻塞项，需 1 次设计决策）**：明确“升级过渡证明”的**适用边界**，二选一（可并存）：
   - D2-a（收窄）：外部 Agent 材料只在 **本次确有待应用迁移** 或 **存在未登记归属/未证明队列** 时强制；迁移前缀完整+0 pending+无未归属批次时，启动仍需通过 checksum/语义/账本/孤儿等**不依赖外部材料**的检查，材料缺失不再把 0-pending 启动判 `unknown`。
   - D2-b（补入口）：新增**不提供业务服务**的恢复协调入口（如 `panel recovery enter` 复用 `store.EnterRecoveryMode`，或在 `migrate` 后、监听前附加“仅 admin 恢复端点+只读诊断”模式），使 `backup-recovery.md` 第 3 步可达；该入口必须先原子冻结授权并轮换 generation，绝不提供 snapshot/订阅/普通写。
   任一方向都必须写清“材料的 fence/指纹绑定是否继续用于**迁移**（是）”与“是否用于**重启**（否）”。
2. **C 侧（不阻塞 D2，但需主判定）**：C 的材料路径分支标为 `not_run` 的补充项——不是要求 C 去伪造升级材料，而是等 D2 落地后，用**同一条真实链路**复测：restore → 合法进入协调 → 核对 → 退出 → 接受 generation；`ACK 删除缺口 49546B/1 batch` 仍必须保持“记录缺口、不回灌、不重写原 payload”，`LEDGER_UNATTRIBUTABLE` 仍必须对未登记区间返回 422 且不 ACK。
3. **是否需要用户 D1**：仅当产品愿意接受“生产环境每次面板重启都需一轮停流+停止 Agent+管理员取证停机维护”作为**有意代价**时，才需要用户 D1 确认；否则主 D2 即可收窄（因为只改“何时要求材料”，不改“材料内容/校验强度”，不放开任何普通服务）。建议不要把这条推给用户，默认按 D2-a/D2-b 走。
4. 若只允许 ≤2 个安全修复方向，取：
   - **F1（收窄范围）**：把 `AGENT_EVIDENCE_MISSING/AGENT_STOP_*` 的强制点从“每次 `Open` 的 postflight”改为“本次 `migrate` 确有待应用文件时（或检测到未登记归属/未证明队列时）”；`0-pending` 启动保留全部 `MIGRATION_CHECKSUM_*`、`TASK_ORPHAN`、`ITEM_TASK_ORPHAN`、`BATCH_CLOCK_INVALID`、`LEDGER_PERIOD_UNPROVEN`、`MEMBER_*`、`PERIOD_OVERLAP`、`BATCH_HASH_MISMATCH`、`LOCAL_BATCH_DUPLICATE` 等检查与 `unknown`→拒绝语义。
   - **F2（补合法入口）**：实现不监听业务的恢复协调入口（复用 `EnterRecoveryMode`，先冻结再轮换），仅本地管理员可用，供灾备第 3 步；**不**提供“跳过 readiness 的普通启动”。
   **禁止**：`skipReadinessOnRestore` 之类的 env/参数开关；SQL 手改 `control_state`/`schema_migrations`“标 ready”；把 `AGENT_EVIDENCE_MISSING`/`UNKNOWN`/`checksum_ok=null` 降级为 warning；用旧 proof 充当永久通行证；为让 C 通过而重写/回灌 ACK 已删 payload、改历史 batch ID、或把 49546B 缺口算成零；在 `recovery_mode` 下放开 snapshot/订阅/普通写。

## 5. 最小复测建议（只读建议，未执行）

1. store 单测（私有 PG schema，最小集，不需要真实 core）：
   - `schema-current + agents(1) + 无材料 + 0 pending` → 期望按 D2 裁决为**可 `Open`**；同一库 `+1 待迁移 + 无材料` → 仍必须 `READINESS_BLOCKED(AGENT_EVIDENCE_MISSING)`。
   - `一次真实迁移后、同一材料、第二次 Open` → 期望按 D2 裁决（若 F1 生效则成功；否则需 D2 明确“重启需重新取证”并同步 `upgrade_open_test.go:209-216` 的期望，不得保留“被拒=通过”的模糊断言）。
   - `checksum mismatch / TASK_ORPHAN / BATCH_HASH_MISMATCH` 在 0-pending 启动时仍拒绝（证明 F1 未削弱语义门禁）。
2. 恢复链路（等 D2/F1/F2 后，由 C 或其继任者按原 isolation/lease 跑一次，不新增共享资源）：restore → 合法进入协调（新入口或收窄后的启动）→ `recovery_mode=true` 下 `agent snapshot` 仍 409、普通写仍 `RECOVERY_MODE` → 旧凭据不出现在任何快照 → 退出需 `confirm=verified`+两项核对 → Agent `-accept-generation` → 重放 3 batch/148638B 恰一次、`LEDGER_UNATTRIBUTABLE` 未登记区间 422 且不 ACK、49546B 缺口如实记录不回灌。
3. 若时间不足以跑 2，最低限度：只做 1 的第一条（能区分“门禁正确拒绝”与“门禁挡死合法启动”），并在交接里明确 C 的 8 pass 仍有效、1 blocked 不可当绿。

## 6. 明确的剩余风险 / confidence

- confidence：代码路径与“0-pending+agents+无材料必然被拒”= **high**（store.go:56/204-211/287-292 + readiness.go:533-548 直读，且 C run6 已实测）。
- “设计本意不要求每次重启重新取证”= **medium-high**（`:604` 条件是“需证明旧队列”，`backup-recovery.md` 的恢复/重启流程与 `production.md:326` 的“预检不是永久通行证”都只约束升级过渡；但 authority 未逐字写“重启免材料”，故最终以主 D2 为准）。
- “C 是否本可用材料路径得到更多断言”= **medium**：技术上可行（它已有 `-inspect-state` 导出），但语义上等于把灾备伪装成升级，且会绕过 `enterRecoveryMode` 的正确顺序，故不建议把它当作 C 的必修项，只作 D2 落地后的复测补充。
- 未执行/未覆盖：未跑 PG/core/部署、未跑任何 `go test`、未复跑 C、未审 542 项 backend 已验项；C 的 `run2`（proxy）、`run1`（audit）等早期失败与本次分类无关，未纳入。
