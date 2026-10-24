# web-demo-accept · product-readiness / final（独立只读重验 R1）

## 结论

**ACCEPTED_WITH_IMPROVEMENTS**（`task_completion.status=success`）。上一轮 4 个 P1 **全部关闭**，未发现新的 P0/P1；仅登记 P2 与既有 boundary。上一份 REJECTED 报告原样保留，本报告是独立新结论。

- 待验候选：主 `ee57982c4139b24fd2047e4f3ff5b48d9f0600ab`（`a177779` 仅新增上一份报告与 audit，未改字节）。
- 生产字节自 `fea3187` 起未变：`git diff --name-only fea3187..ee57982 -- ':!tests' ':!web/tests' ':!*_test.go' ':!.ai'` 为空；`e68f048..ee57982` 仅 `web/tests/product-readiness-e2e.mjs`（+56/−6）。
- 权威新证据：`.ai/evidence/web-demo/A/full-contract-pass-20261024T123512Z-91d928/{report.json,results.json,us-matrix.json,manifest.txt}`。
- 独立审计：`.ai/quality/web-demo-accept-product-readiness-final-r1-20261024-210146-audit.json`
- 模式：独立只读。未改源码/测试/断言/设计/state，未派代理、未 push/deploy，未启动 PG/core/browser、未访问 203/8445/8443/15432，只读现有证据并新增本报告与 audit。

## 1. 证据源独立核对（回原件，不只信 summary）

| 项 | 结果 | 依据 |
|---|---|---|
| A run 与当前候选同源 | **成立** | `manifest.txt` `candidate=ee57982…`、`worktree_sha=d61dfc62…`；我用 `git status --porcelain`（前一轮仅两份未跟踪报告）独立重算该 sha256 = `d61dfc62…` |
| A run 与当前 runner 字节绑定 | **成立** | `runner_sha` 首项 `6ad2a652…` = 当前 `web/tests/product-readiness-e2e.mjs`；其余 `820bbf59…`(`webdemo-pom.mjs`)、`0f943260…`(`webdemo-env.sh`)、`08f1a52a…`(`webdemo_ui_fixture_test.go`) 与主树逐一相同；`fixture_binary=02baeec5…` 与该次 `go test -c` 一致 |
| A 结果不是 summary 转述 | **成立** | `results.json` 23 条（5 chain + 18 us）逐个 `status=PASS`，无 `SKIP`/零用例，`report.json.haltedAt=null`、`syntheticUsageRows=0`、`pageErrors=[]`、`browser_exit=0`、`owned_process_leftovers=0`、`protected_lost=0`、`procs-before` 与 `procs-after` 逐字节相同 |
| `us-matrix.json` 与 `results.json` 一致 | **成立** | 18 个 US 键全 `PASS`，且与 `results.json` 的 18 条 `kind=us` 一一对应（我按 ID 对表，非计数比对） |
| 上一轮 P1 的两个失败 run 未被冒充 | **成立** | `A/full-contract-20261024T123301Z-7191aa` 仍保留 `US-PR-005/006 FAIL`（原文 断言）与 `candidate=6c4db07`；本轮只采信 `…123512Z…` |
| B/C/D/E/F 输入未漂移 | **成立（D 的 `base_commit` 字段不精确，见 P2-1）** | B `base_commit=fea3187` + `webdemo_dual_test.go=984f93dc…`/`run_webdemo_dual.py=081db040…` 与当前一致；C `runner-receipt.base_commit=a76909b`（`a76909b..HEAD` 生产差异为 0）、`webdemo_restore_test.go=1ccec20f…` 一致；E `webdemo_clients_vision_test.go=cbadb276…`、`run_webdemo_clients.py=85f919c5…`、manifest `7d88419a…` 一致；F host `source_sha=d28e3df`（router blob `4f825dd0…` 在 `f38aff4`/`d28e3df`/`HEAD` 三处相同）、F agent `source_sha=44f35aa` + `test_file_sha256=1c4db6cc…` 与当前一致 |
| 后端/前端上游结论未被改写 | **成立** | backend R3 复验 `backend-accept-…-122207-3fedab77.md` = ACCEPTED_WITH_IMPROVEMENTS（输入 `20261024T122047Z-8d27c817`、store `20261024T113542Z-7676d17a`）；frontend R1 = ACCEPTED_WITH_IMPROVEMENTS。本报告只复核与本阶段相关输入有效性，不复制其结论 |

## 2. 上一轮 4 个 P1 的逐项关闭核对

| P1 | 上轮要求 | 本轮源码（`web/tests/product-readiness-e2e.mjs`） | 本轮运行原件 | 判定 |
|---|---|---|---|---|
| P1-1 US-PR-005「方案可交付」 | 页面等待加载并断言节点组/额度/周期，`planSummaryVisible` 不得以 false 随 PASS 计入 | `:781-787` `goto(/admin/subscriptions/:id)` → `subscription-form` `waitFor(visible)` → `#sub-name` `waitFor(visible)` → `assert.match(/节点组：wdemo group/)`、`/每人额度：1 GiB/`、`/周期|UTC|锚定日/` | `results.json` US-PR-005 `planSummaryVisible:true`，且上述三条断言先于取详情执行（失败即整条 FAIL，见 `…123301Z…` 的同点真实红） | **关闭** |
| P1-2 US-PR-003「接入后页面同步」 | Agent applied 后回 `/admin/servers` 断言在线 + 最后心跳；复制码不得宣称已连接 | `:651-670` 真实 Agent applied（`applied_revision>0` 且 `core_instances.state∈{applied,running}`、`generation/revision/agent_id` 非空）后 `servers.open()` → `server-agent-state` 过滤 `在线` `waitFor(visible)` → `assert.match(/在线/)` → 写回 `afterAgent{pageState,realHeartbeatVisible,appliedCoreState}`；`record('us','US-PR-003')` 在 `:481-482` 仍断言复制码后徽章必须匹配 `未接入|离线|未知` | `results.json` US-PR-003 `before/agentStateBefore=未接入`、`afterAgent={pageState:在线, realHeartbeatVisible:true, appliedCoreState:running}`；`requests` 显示 `/admin/servers` 在 `12:35:44.950Z`、agent applied `12:35:34.95+08:00=12:35:34.95Z` **之后**；chain `real-agent-official-core.serverPageAfterAgent` 原文含 `在线 | 刚刚上报心跳` | **关闭**（三态区分的剩余覆盖见 P2-2） |
| P1-3 US-PR-002「部分配置已存在」 | 先建对象再进工作台，进度来自实际配置、已有对象复用不重复创建 | `:429-437` chain 先取全新态 `assert.equal(initialSetupCompleted,0)`；`:453-461` `assert.ok(done>=3 && done>initialSetupCompleted)`、`assert.match(/6 步/)`、`serverRows.filter(id===server.id).length===1`、`planRows.filter(id===subscription.id).length===1` | `results.json` US-PR-002 `initialCompletedCount:0`、`progressText:已确认完成 3/6 步`、`completedCount:3`、`reusedServerId/reusedPlanId` 为 UI 创建的那一对；chain `ui-create-resources.initialSetupCompleted:0` 与 US-002 同 run 连续 | **关闭** |
| P1-4 US-PR-006「无服务邀请」 | 无方案邀请注册后可登录 + 待开通说明 + 无虚假授权；失效邀请仍拒 | `:519-541` UI 生成无方案邀请（`selectOption('')`）→ 断言 POST 201 → 真实 `/redeem` → `waitForURL('**/me/subscription?welcome=1')` → 轮询 `redeem-result-message` 直到匹配 `/尚未分配服务方案|待开通/` 并 `assert.match` → 用该成员会话读 `/me/status` 断言 `member.subscription_id===null`；失效邀请分支仍为 `400 + 0 账号` | `results.json` US-PR-006 `withoutPlan={subscriptionId:null, pendingCopy:"账号已创建。尚未分配服务方案，请联系邀请你的管理员开通。"}`、`unusableInvitationStatus:400`、`accountsCreated:0`；`requests` 有 `POST /auth/redeem 201` 与 `POST /auth/redeem 400` 各一条 | **关闭** |

补充核对：`ee57982` 相对 `6c4db07` 只做了两处**等待/就绪**修正（无方案欢迎文案改为有界轮询 15s；方案页加 `#sub-name` 就绪等待），未弱化任何断言，也未把断言改成可选——这与 `…123301Z…` 的两条真实 FAIL 原文对应，属于正当 TEST 侧修复。

## 3. 必要门禁

| # | name | result | evidence |
|---|---|---|---|
| G1 | 候选/生产字节闭包，故事 run 与当前源码可对位 | passed | `git diff fea3187..ee57982`、`A/full-contract-pass…91d928/manifest.txt` |
| G2 | 整文件真实执行、无跳过/零用例/页面未捕获错误 | passed | `results.json`（23 条全 PASS）、`report.json`（`haltedAt=null`、`browser.exit`、`syntheticUsageRows=0`）、`page-errors.json=[]`、`manifest.txt browser_exit=0` |
| G3 | 18 个 US ID 全在最终矩阵且非 SKIP | passed | `us-matrix.json`、`results.json` |
| G4 | US-PR-001/007/008/009/011/013/014/016/017/018 关键成功/异常断言 | passed | `results.json` 对应 detail（匿名根→`/admin/overview` 且 reload/direct-login 保持、成员页仍「我的服务」；69632B/SHA 一致与 PG 69834；暂停 403 + 新连接 1ms 拒 + 既有流被断；恢复后 token 保持且旧凭据仍拒；旧链接 403；恢复模式写阻断 409 与 400 退出确认；成员越权 403 且 `fullTokenInHTML=false`；390px/键盘/暗色无横向溢出；51 成员分页与「已选 2 名（含其它页 1 名）」） |
| G5 | US-PR-005 方案可交付页面断言（上轮 P1-1） | passed | `results.json` US-PR-005；`product-readiness-e2e.mjs:781-787` |
| G6 | US-PR-003 接入后页面真实连接/同步（上轮 P1-2） | passed | `results.json` US-PR-003 `afterAgent`；`report.json.requests` 的 `/admin/servers` 时序；`product-readiness-e2e.mjs:651-670` |
| G7 | US-PR-002 部分配置/复用（上轮 P1-3） | passed | `results.json` US-PR-002 + chain `ui-create-resources`；`:429-461` |
| G8 | US-PR-006 无服务邀请（上轮 P1-4） | passed | `results.json` US-PR-006；`report.json.requests` redeem 201/400；`:519-541` |
| G9 | 关键异常为断言而非探针（B：双 Agent/UDP/离线/到期/部分成功/旧回执/回滚/崩溃） | passed | `B/main-20261024T121009Z-133312/{receipt.json,test.log}`：`--- PASS: TestWebDemoDualAgent (36.18s)`、exit 0、`protected_original_intact=true`、`own_process_leftovers=[]`、`foreign_listeners_unchanged=true`、锁 acquired+released、`xray_hash_unchanged=true`；`host_core_inventory_churn` 如实记录 |
| G10 | 备份恢复与「恢复不复活旧权限」 | passed | `C/r3-final-green-20261024T115143Z-b7506f/receipt.json`：`assertion_count=20/passed=20/failed=0/blocked=0`、`pg_dump -Fc` 87032B、source/target 独立集群、`ack_deleted_gap{batches:1,bytes:49546}` 如实记为不可恢复、`not_run=[WAL/PITR、旧版二进制停写回滚、203/8443/8445 变更]` |
| G11 | 跨版本 Agent/core 升级与回退真实进程证据 | passed（receipt 标签见 P2-1，已证执行当前测试内容） | `D/main-final-pass-20261024T121918Z-b2af77/{receipt.json,test.log}`：`status=pass/exit_code=0`、5 个 managed core 身份行、5 次 `real transfer … 65536 bytes` 同 sha、retained batch `credited exactly once`、`revoked export=403`、`postgres_graceful=true`、`leftover_processes=[]`、`lock_released=true`、`listeners_lost=[]`、`changed_identity=[]` |
| G12 | 声明支持范围客户端矩阵 | passed | `E/main-20261024T115431Z-4d728d/receipt.json`：`status=pass`、vision `raw_exit=0/expected 2 tests/3 cells/observed 3`、matrix `raw_exit=0/21 of 21`、`problems=[]`、`recovery.all_exited=true`、`tempdir_removed=true` |
| G13 | 平台/HTTPS 双托管与 Agent 用户级 unit（按声明范围） | passed | `F/host-main-20261024T101214Z-fe65bc/main-verification.json`（2 hostings、真实 host nginx ELF + `ignore_https_errors=false` + 临时 NSS 严格私有 CA、API-only/SPA 两模式、`same_db_key_binary_session_survives`、负例 curl60 issuer/SAN + lost-listener、`system_level_install=not_run`、`public_ca_or_dns=not_run`）；`F/main-dd40fac-retry…/main-verification.json`（同二进制两次 PID、心跳恢复、`EAFNOSUPPORT`+AF_UNIX 正控、installer exit1/97 tripwire、unit inactive、`stage_F_complete=false`） |
| G14 | 隔离/锁/清理/受保护对象 | passed（ENV 边界如实） | A `protected_before/checked=7`、`protected_lost=0`、`owned_process_leftovers=0`、procs 快照相同；B 见 G9；C 见 G10；D `foreign_vanished` 仅外部 `1747724` 且 `changed_identity=[]`；E `recovery.all_exited` |
| G15 | 证据不含 secret/raw token/private key | passed | A `redact()` 输出、US-PR-017 `fullTokenInHTML=false`、`requests` 中订阅路径 `[REDACTED]`；B/C/D/E 无凭据文本；本报告未复制任何凭据值 |
| G16 | backend readiness/store/API 在当前字节下仍有效 | passed | backend R3 复验报告 + `backend-test/20261024T122047Z-8d27c817`、`20261024T113542Z-7676d17a`；`internal/panel/store/readiness.go` 相对 `d28e3df` 仅 readiness 语义（DEC018），backend R3 已独立复核 |

## 4. 问题

### P0/P1

无。

### P2（不阻断结论，需登记）

1. **D 的 `base_commit` 标签与实际执行的测试内容不一致（本轮纠正上一轮 P2-1）**：D run `base_commit=fea3187`、`finished_at=20:19:39+0800`，而 `tests/e2e/webdemo_upgrade_test.go` 的运行内容其实是 `f40ee37` 的内容（20:22:03 才提交、文件 mtime 20:18:59，即运行时是未提交的工作区改动）。三项独立证据：① `receipt.source_files[...]=07f9df9c…` = 当前内容（`fea3187` 为 `97489762…`）；② D `test.log` 的行号是 `564/593/597/670`，这些行号**只**存在于 `f40ee37` 之后的文件版式（`fea3187` 中对应日志在 `567/592/549/663`），而更早的 `D/main-green…`、`D/main-pass2…` 日志确实是 `549`；③ `test_binary_sha256=02baeec5…` 与 A run（`ee57982`，`tests/e2e` 内容与 `f40ee37` 之后相同）的 `fixture_binary` **逐字节相同**，不同源码集不可能产出同一 `go test -c` 二进制。**结论：D 实际执行的就是当前测试代码**，上一轮「D 执行旧测试二进制」的判断应撤回；残留问题只是 receipt 用 `git rev-parse HEAD` 记录基线，未标注工作区已被后续提交的测试改动覆盖。属可复核性瑕疵，不需重跑，建议后续 receipt 增记 `source_dirty`/内容指纹。
2. **US-PR-003 异常分支只覆盖一种状态**：`product-readiness-e2e.mjs:482` 用 `/未接入|离线|未知/` 松匹配，运行中只出现「未接入」；`ServersView.vue:92-109` 的 `离线`/`状态未知` 分支与「重新注册/查看诊断」入口无运行断言（`server-agent-state` 三态区分、runtime 页入口仅由源码与 `ServerRuntimeView` 存在性推断）。建议后续对离线/未知各补一条可注入状态的断言。
3. **US-PR-005 第二场景（影响已有成员）不在 A run**：方案已绑定成员后修改权限内容应「先显示影响及生效时间」，A run 只断言了 DELETE 409 与退役对话框文案。该行为目前由 `web/src/components/ImpactPreviewDialog.spec.ts`（影响成员/受影响节点）与 `web/tests/e2e-ui-full.mjs:1141-1152`（节点组 `PREVIEW_REQUIRED` 后弹影响预览）跨证据承担，且服务端 `PREVIEW_REQUIRED` 为强制契约；属覆盖位置分散，不是无证据。
4. **US-PR-015 第二场景跨证据**：「加载/空/错三态可区分」不在 A run，仅由前端单测（`MembersView.spec.ts` 空态、`MeUsageView.spec.ts` 503/游标错误）在同字节前端上覆盖；A run 覆盖的是 390px/键盘/暗色。需在报告中显式标为跨证据（上一轮已登记，状态未变）。
5. **US-PR-010 `responseLoss` 为悬空引用**：detail 写「covered by tests/e2e data-plane runner」但未给本轮 run 路径；实际由 `idempotentReplay:true` + backend store 证据承担。建议补具体路径。
6. **C `restore-evidence.json` 仍是启动占位 `status:"fail"`**（权威值取 `receipt.json`/`runner-receipt.json`/`test.log`），上一轮已登记，未变。
7. **E `receipt.lease` 字段自相矛盾**：`acquired:false` 但 `acquired_at/released_at` 有值、`own_pid` 有值（`describe()` 在 `release()` 之后调用）；`bounded_wait_s=300` 与 `released:true` 可还原事实，属报告字段表达问题。
8. **A 无 runner 级 receipt**：目录内没有 `receipt.json`/`test.log`，只有 `manifest.txt` + 三份原件；我已按原件重算 `worktree_sha`、`runner_sha` 与 `procs` 快照来替代，建议后续保留 runner receipt 便于第三方复算。

## 5. 明确 boundary（不是 N/A，也不是通过）

- **公有 CA / 公网 DNS**：故事只要求按部署说明的域名/证书完成 HTTPS；F 用真实 host nginx + 严格私有 CA（`ignore_https_errors=false`、临时 NSS 信任、无 bypass）证明两种托管与负例。公有 PKI/公网 DNS 未被承诺，记为 boundary。
- **Windows/mobile GUI、读屏器**：无设备/AT 环境，`F/visual-accessibility-status.json` NOT RUN；headless AX 不等价通过。
- **系统级 Agent 安装/完整硬化**：需 root + 可弃 PID1；F 用用户级 transient unit 证明真实生命周期，系统级 `not_run`（`stage_F_complete=false` 如实保留）。
- **WAL/PITR/离机备份**：C `not_run`，因此**不得**据此宣称生产 RPO/RTO；C 的 49,546B ACK-已删除缺口按 `docs/operations/backup-recovery.md` 第 5 步如实记录、不双计。
- **外部 AgentLab core 子进程 churn**：D `foreign_vanished` 记录外部 `1747724` 消失、`changed_identity=[]`；B 的 `host_core_inventory_churn` 如实记录；未冒「宿主全 clean」。
- **8445 部署**：宿主 8445 仍是早期预览，不等于当前 final；最终部署由主后置，本次未访问、不作为验收输入。
- **支持范围**：E 只声明 xray/sing-box/mihomo 的 21 本地格 + 3 Vision + 4 辅助守卫，不复制历史 54 格口径。

## 6. 未验证与承接

| 场景 | 承接 | 位置 |
|---|---|---|
| D receipt 补记 `source_dirty`/内容指纹（P2-1，非必需） | web-demo D（仅文档性改进；已证当前测试代码被执行，无需重跑） | `tests/e2e/run_webdemo_upgrade.py` |
| US-PR-003 离线/状态未知分支与诊断入口断言（P2-2） | web-demo dev/test（补断言并整文件重跑） | `web/tests/product-readiness-e2e.mjs` |
| US-PR-005 影响已有成员预览归档进 A run（P2-3） | web-demo A（可选） | 同上 |
| 8445 最终部署与热更核对 | 主/delivery（本结论放行后） | 部署阶段 |

## 7. 产物与清理

仅新增本报告与同名 audit（均在 `.ai/quality/`）。未创建运行资源、未启停任何服务、未改 state、未产生测试派生物；上一轮 REJECTED 报告与所有其它阶段报告原样保留。
