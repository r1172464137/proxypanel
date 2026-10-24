# web-demo-accept · product-readiness / final（独立只读终验）

## 结论

**REJECTED**（`task_completion.status=failed`）。并非环境阻塞，也不否定已完成的大量真实证据：A/B/C/D/E/F 六个物理 run 的**技术链路**在自己声明的范围内成立，产品候选字节与声明的证据源一致。拒绝的唯一原因是**故事级必要场景的断言缺失**：最终 18 故事矩阵把 4 条故事的关键场景记录成“未断言/未覆盖”，其中一个还被显式记录为 `false` 却随文件 PASS 一起计入 23/23。按 `acceptance-contract`（必要门禁未验证 → REJECTED）与 `guides/web-demo/quality.md`（逐故事核对关键断言，不以占比豁免），这属于 P1，不是 P2。

- 候选/待验对象：主 `e68f048`（`web-demo` 终验对象）；生产字节与 `fea3187` **完全相同**。
- 独立审计：`.ai/quality/web-demo-accept-product-readiness-final-20261024-122828-audit.json`
- 模式：独立只读。未改源码/测试/断言/设计/state，未派代理、未 push/deploy，未启动 PG/core/browser，未访问 203/8445/8443/15432，只读现有证据并新增本报告与 audit。
- 上游状态：backend R3 `ACCEPTED_WITH_IMPROVEMENTS`（`backend-accept-product-readiness-backend-r3-20261024-122207-3fedab77.md`），frontend R1 `ACCEPTED_WITH_IMPROVEMENTS`；本报告不复制其结论，只复核与本阶段相关的输入有效性。

## 1. 候选与证据源独立核对（不只信 summary）

| 项 | 结果 | 依据 |
|---|---|---|
| 生产字节未漂移 | **成立** | `git diff fea3187..e68f048` 非 `.ai` 部分只有 `internal/panel/api/abc_readiness_test.go`、`tests/e2e/webdemo_upgrade_test.go`；`git diff --name-only fea3187..HEAD -- ':!tests' ':!web/tests' ':!*_test.go' ':!.ai'` 为空 |
| 生产差异仅为 router + readiness（上一轮起） | **成立** | `f38aff4` 的 `web/src/router/index.ts` blob `4f825dd0…` 在 `f38aff4`/`d28e3df`/`HEAD` 三处一致；`a76909b..f40ee37` 生产差异为空；`d28e3df..f40ee37` 仅 `readiness.go`、`docs/operations/backup-recovery.md`（DEC018 语义，backend R3 已复核） |
| A run 与当前测试输入同源 | **成立（test-only 之外）** | `A/full-pass…/manifest.txt` `candidate=fea3187`，`runner_sha` 首项 `029892a8…` = 当前 `web/tests/product-readiness-e2e.mjs` sha256；`page-errors.json=[]`，`procs-before.txt`/`procs-after.txt` 逐字节相同 |
| A 无 runner receipt | **已确认并纳入边界** | 目录内无 `receipt.json`/`test.log`；`main-verification.json` 由主从 same run 的 `report.json`/`results.json`/`us-matrix.json` 生成，我用这三份原件的 story 明细独立重算，未把 summary 当权威 |
| B 输入与当前一致 | **成立** | `B/main-20261024T121009Z-133312/receipt.json` `base_commit=fea3187`，`webdemo_dual_test.go=984f93dc…`、`run_webdemo_dual.py=081db040…` 与当前文件逐一相符 |
| C 生产输入与当前一致 | **成立** | `C/r3-final-green…/runner-receipt.json` `base_commit=a76909b`（`a76909b..f40ee37` 生产差异为空），`webdemo_restore_test.go=1ccec20f…` 与当前一致 |
| E 输入与当前一致 | **成立** | `vision_test.go=cbadb276…`、`run_webdemo_clients.py=85f919c5…`、manifest `7d88419a…` 与当前一致；`env_scrubbed` 记录 `PANEL_TEST_/PANEL_BROWSER_/PANEL_DOCKER_*` 未继承 |
| D 输入与当前一致 | **不成立（test-only）** | D run `finished_at=20:19:39+0800`，早于 `f40ee37`（20:22:03+0800）；见 P2-1 |
| F 覆盖当前 router | **成立** | `F/host-main-20261024T101214Z-fe65bc/manifest.json` `source_sha=d28e3df`（干净），且 `f38aff4` 是 `d28e3df` 祖先、router blob 相同 |

## 2. 必要门禁

| # | 门禁 | 结果 | 证据 |
|---|---|---|---|
| G1 | 候选/生产字节闭包，故事 run 与当前源码可对位 | passed | `git diff`；A `manifest.txt`、`report.json`、`results.json`、`us-matrix.json` |
| G2 | 整文件执行真实、无跳过/零用例、页面无未捕获错误 | passed | A `results.json` 23 条、`report.json.browser`（Chromium 156，exit 0）、`page-errors.json=[]`、`manifest.txt browser_exit=0` |
| G3 | 18 个 US ID 全部出现在最终矩阵且非 SKIP | passed | A `us-matrix.json.us` 18 键全 PASS；`results.json` 18 条 `kind=us` |
| G4 | US-PR-001/007/008/009/011/013/014/016/017/018 的关键成功与异常断言 | passed | A `results.json` 对应 detail（69632B/SHA 一致、台账 69834、暂停 403+新连接/既有流被断、旧凭据仍拒、恢复后 token 保持、旧链接 403、恢复模式写阻断与 400 退出确认、成员越权 403 且 HTML 无完整 token、390px/暗色无横向溢出、51 成员分页与跨页选择范围） |
| G5 | **US-PR-005 “方案可交付”页面断言** | **failed** | A `results.json` US-PR-005 `planSummaryVisible=false`，测试对该字段**无断言**（`web/tests/product-readiness-e2e.mjs:736-746` 只断言 DELETE 409） |
| G6 | **US-PR-003 “接入成功后页面展示真实连接和同步结果”** | **unverified** | A 同文件两处 `/admin/servers` 均在 Agent 启动前（`:463`、`:467`）；注册与 applied 只由 `/admin/agents` + `core_instances` 证明（`:601-626`），页面徽章回归未断言；`ServersView.vue:94-109` 有 `在线/离线/未接入/状态未知` 分支但无运行断言 |
| G7 | **US-PR-002 “部分配置已存在，进度来自实际配置、已有对象可复用”** | **unverified** | A 只覆盖全新环境 `已确认完成 3/6`（`results.json` US-PR-002）；无部分配置/不重复创建场景 |
| G8 | **US-PR-006 “无服务邀请可登录查看待开通说明”** | **unverified** | A 只覆盖“带服务邀请成功”“已用邀请 400 + 0 账号”（`results.json` US-PR-006）；无服务邀请路径未断言 |
| G9 | 关键异常（双 Agent/离线/到期/UDP/部分成功/旧回执/回滚/崩溃）为断言而非探针 | passed | B `test.log` 原文：TCP 66444+SOCKS5 UDP 19B 双节点、expiry 0.9s 拒、坏文档仅 node1 且 last-known-good、node2 离线时 batch `pending` 与 `timeout: no agent receipt`、撤权 1.90s 断流+拒新连接、旧凭据恢复后仍拒、`REVISION_STALE` 具体错误、stale ACK 不完成任务、pidfd 杀 core 后回到合法 revision 且旧凭据仍拒、成员访问 batch detail 403；`dual-state.json` 结论与 `agents[].desired/applied` 一致 |
| G10 | 备份恢复与“恢复不复活旧权限”，阴性具体非任意错误 | passed | C `runner-receipt.json`/`receipt.json`：`pg_dump -Fc` 87032B、`pg_restore` exit0、20/20 断言、source 与 target 独立集群、revocation `active_credentials_after=0`、generation 轮换、ACK 已删除批次 49546B 记为不可恢复缺口；`test.log` `--- PASS` |
| G11 | 跨版本 Agent/core 升级与退回真实进程证据 | passed（证据新鲜度见 P2-1） | D `receipt.json` status pass/exit0、`state/demo-upgrade-receipt.json` pass、5 个 core 身份（旧/新/回滚/旧 Agent）+5 次 65536B 实传 SHA 相同、retained batch exact-once、`postgres_graceful`、`leftover_processes=[]`、`lock_released=true`、`cleanup.test_binary_removed/work_dir_removed=true`；`REPORT.md` 全部数字与 receipt 同源 |
| G12 | 声明支持范围的客户端矩阵 | passed | E `receipt.json` vision 3 观测=3 期望、matrix 21=21、auxiliary 4、`problems=[]`、`raw_exit=0`、`recovery.all_exited=true`、`tempdir_removed=true`；`vision.jsonl` 56 行、`matrix.jsonl` 171 行 |
| G13 | 平台/HTTPS 双托管与 Agent 用户级 unit（按声明范围） | passed | F host `main-verification.json`（2 hosting、真实 host nginx ELF + 严格私 CA 无 bypass、API-only 与 SPA 两模式、`same_db_key_binary_session_survives`、负例 curl60 issuer/SAN + lost-listener）、F Agent `main-verification.json`（同二进制两 PID、心跳恢复、AF_UNIX 正控 + AF_INET EAFNOSUPPORT、installer exit1/97 tripwire、unit inactive） |
| G14 | 隔离/锁/清理/受保护对象 | passed（ENV 边界如实） | A `protected_before/checked=7`、`protected_lost=0`、`owned_process_leftovers=0`、procs 快照相同；B 锁取得+释放、`protected_original_intact=true`、`own_process_leftovers=[]`、`foreign_listeners_unchanged=true`，并如实记录 `host_core_inventory_churn`；D `protection_clean=false` 仅因外部 `1747724` 消失且 `changed_identity=[]`、29/29 listener 未丢（未冒 host 全 clean）；C 记录 `ENV_UNATTRIBUTED_EXTERNAL_PROCESS_CHANGE` 而非放宽 |
| G15 | 证据不含 secret/raw token/private key | passed | 引用范围内 B 日志 `value redacted`、A `redact()` 输出、US-PR-017 `fullTokenInHTML=false`、D/C/E 无凭据文本；本报告不复制任何凭据值 |
| G16 | backend readiness/store/API 在当前字节下有效 | passed | R3 报告 + `backend-test/20261024T113542Z-7676d17a`、`20261024T122047Z-8d27c817`（`inputs_stable=true`，输入逐项 SHA 0 missing/mismatch） |

## 3. 问题

### P1（阻断本轮通过）

| # | 位置 | 证据 | 影响 | 最小修复 |
|---|---|---|---|---|
| P1-1 | `web/tests/product-readiness-e2e.mjs:736-746`（US-PR-005） | A `results.json` US-PR-005 `planSummaryVisible:false`，该字段无 `assert` | 故事“能看懂成员将获得哪些节点、多少额度、何时到期/重置”未被验证；一个 false 观测随 PASS 计入 23/23，等于把“页面可能未就绪”当成功 | 在 `goto(/admin/subscriptions/:id)` 后等待 `subscription-form` 与“成员将获得什么”区块（或加 `data-testid`），断言节点组名、1 GiB、周期/重置说明，并 `assert.equal(planSummaryVisible, true)`；整文件重跑 |
| P1-2 | 同文件 `:463-474` 与 `:601-626`（US-PR-003） | 仅 Agent 启动前断言 `未接入`；启动后无 `/admin/servers` 页面断言 | 故事“页面展示真实连接和同步结果，下一步可直接配置节点”只由 API/DB 证明；页面徽章映射（`ServersView.vue:94-109`）无运行证据 | Agent applied 后重新打开 `/admin/servers`，断言 `已连接/在线` + 最后心跳 + 下一步节点入口；并保留 `未接入/离线/状态未知` 三态区分断言 |
| P1-3 | 同文件 US-PR-002 块（`:441-457`） | `results.json` 仅 `3/6` 全新环境 | “部分配置已存在时进度来自实际配置、已有对象可复用、不重复创建”未验证 | 增加“先建 1-2 个对象再进工作台”的场景，断言进度数与对象复用（计数不增） |
| P1-4 | 同文件 US-PR-006 块（`:494-510`） | 仅带方案邀请 + 已用邀请 400/0 账号 | “无服务邀请可登录查看待开通说明”未验证 | 用无方案邀请完成注册，断言可登录且显示待开通说明、无虚假授权 |

四条同属一个测试文件，修复后**整文件重跑**即可一次覆盖；不需要改生产源码（现有页面代码看起来支持这些断言，失败时按 TEST/FRONTEND 归因，不得改弱断言）。

### P2（不阻断结论，但需登记）

1. **D 证据早于最后 test-only 提交**：D run 20:19:39+0800，`f40ee37`（20:22:03+0800）改了 `tests/e2e/webdemo_upgrade_test.go`（pid 记录搜索根由 `state/xray` 扩为整个 state 树 + 等待；probe 移到台账不变式冻结之后；批次数改为“有台账行的批次”）。当前树该文件 sha `07f9df9c…`，D 执行的是旧测试二进制 `02baeec5…`。产品字节未变，旧断言在批次数上更严、5 身份/5 探测均实际通过，因此**不据此判失败**；但“当前测试代码已执行”不成立。
2. C `restore-evidence.json` 仍是启动占位 `status:"fail"`（权威值取 `receipt.json`/`runner-receipt.json`/`test.log`），已知 P2。
3. E `receipt.json.lease` 记录 `acquired:false` 而 `acquired_at/released_at` 均有值——`describe()` 在 `release()` 之后调用导致字段自相矛盾；`own_pid`/`released:true` 与 `bounded_wait_s` 可还原事实，属报告字段表达问题。
4. US-PR-015 第二场景（加载/空/错三态可区分）不在 A run；仅由前端单测（`web/src/views/admin/MembersView.spec.ts` 空状态、`web/src/views/me/MeUsageView.spec.ts` 503/游标错误）在同字节前端上覆盖，需在报告中显式标为“跨证据”。
5. US-PR-010 `responseLoss` 明细写成“由 `tests/e2e` data-plane runner 覆盖”但未给本轮 run 路径；实际重复提交由 `idempotentReplay:true` + backend store 证据承担，建议补路径避免悬空引用。
6. A `main-verification.json` 无 runner receipt（主从同 run 原件生成）；我已回核三份原件与 manifest 指纹，建议后续保留 runner 级 receipt 以便第三方复算。

## 4. 明确 boundary（不是 N/A，也不是通过）

- **公有 CA / 公网 DNS**：故事只要求“按部署说明的域名、证书”完成 HTTPS；F 用真实 host nginx + 严格私有 CA（无 `ignore_https_errors`、无 NSS bypass）证明两种托管与负例。公有 PKI/公网 DNS 未被承诺，记为 boundary，不计失败。
- **Windows/mobile GUI、读屏器（Orca/NVDA/VoiceOver）**：无设备/AT 环境，`F/visual-accessibility-status.json` 明确 NOT RUN；headless AX 不代表等价通过。仅 `US-PR-015` 的窄屏/键盘/暗色由 A 运行证据覆盖。
- **系统级 Agent 安装/完整硬化**：需 root + 可弃 PID1，F 用用户级 transient unit 证明真实 Agent 生命周期，系统级 `not_run`。
- **WAL/PITR/离机备份**：C `not_run`，因此**不得**据此宣称生产 RPO/RTO；C 的 49,546B ACK-已删除缺口按 `docs/operations/backup-recovery.md` 第 5 步如实记录、不双计。
- **外部 AgentLab core 子进程 churn**：C/D 如实记录外部 identity 变化（D `protection_clean=false`、C `host_identity_unchanged=false`），B run 的原进程/listener 完整；不冒“宿主全 clean”。
- **8445 部署**：宿主 8445 仍是早期 706 预览，不等于当前 final；最终部署由主后置，本次未访问、不作为验收输入。
- **支持范围**：E 只声明 xray/sing-box/mihomo 的 21 本地格 + 3 Vision + 4 辅助守卫；不复制历史 54 格口径。

## 5. 未验证与承接

| 场景 | 承接 | 位置 |
|---|---|---|
| US-PR-005 方案可交付页面断言、US-PR-003 接入后页面同步、US-PR-002 部分配置、US-PR-006 无服务邀请 | web-demo dev/test（只改测试并整文件重跑） | `web/tests/product-readiness-e2e.mjs` |
| 上述重跑后的独立只读终验 | 新 web-demo-accept 报告 | 本目录，新时间戳 |
| D 在当前树（`07f9df9c…`）重跑一次 | web-demo D（可选；若要求“当前测试代码已执行”） | `tests/e2e/run_webdemo_upgrade.py` |
| 8445 最终部署与热更核对 | 主/delivery，验收放行后 | 部署阶段 |

## 6. 产物与清理

仅新增本报告与同名 audit（均在 `.ai/quality/`）。未创建运行资源、未启停任何服务、未改 state、未产生测试派生物。上一轮与其它阶段报告原样保留。
