# Docker 生命周期验收（A02 / A07 / A13）— 真实改动、版本 SHA 与实际结果

目标基线：已批准目标（A13 = 容器生命周期；不声称宿主机 systemd / 主机重启）。

本文件区分**已实际执行**的检查与仍需父 Agent 执行的命令，未执行项不写“通过”。§1–§15 保留各次失败定位的原文，§0 与 §16 是最新真实结果（含明确未验收项）。

## 0. 当前状态（2026-10-21 收尾轮真实结果；取代 §1–§15 的“待父执行”表述）

> §1–§15 是各次失败定位时的**原始记录**，未删除、未改写当时的结论；其中“尚未真跑”“远端仍未升级”等表述**已被本节与 §16 取代**。口径：A13 = 本项目独立 Docker **容器生命周期**，**不是**宿主机 systemd 或主机重启验收。

| 项目 | 最新真实结果 | 证据 |
| --- | --- | --- |
| A13 `TestDockerAgentVersionUpgrade` | **PASS 93.21s**：镜像 0.1.0 → staged 0.1.1 → 回 0.1.0，同 identity / generation / state 卷，4 批 **420,558B** 逐批一次入账，已撤权凭据始终拒绝，归档 cleanup 释放 20105 | `docker-agent-upgrade-2.log`（`-1` 是脚本命名缺陷导致的旧失败，已修） |
| A13 `TestDockerCoreVersionSwitch` | **PASS 41.81s**：同一 staged Agent 0.1.1 下 core **26.3.27 → 26.3.23 → 26.3.27**，最后回到镜像 Agent 0.1.0；每步同一观察里版本精确、心跳更新、`applied=desired=176/176`；账本不回退、旧凭据仍拒、同一轮双节点真实代理成功；结束归档节点并确认 20105 无 LISTEN | `docker-core-switch-3.log`（`-1`/`-2` 是测试时序与 staged 缺 geo 资产的旧失败，已修） |
| A13 镜像内 Agent | 两源 Agent 正式 **0.1.1**：当前 image `sha256:4e96ae4a…c564eb76d`（panel `0595390…`，含 cert-pin 修复；此前的 `sha256:912aed…` / panel `4911fc3d…` 已被取代），`/app/agent` `a28ca865…bd66a`、`/app/xray` `8255dd93…845ed`（26.3.27 + `/app` geo 资产未动）；两 Agent 状态卷原地保留；旧 tag `…-agent010-baseline`（`sha256:326d0b…e4acca`）保留 | `docker-current-agents-deploy.log`、`docker-panel-cert-pin-deploy.log` |
| A07 `TestDockerConfigSafety`（0.1.1） | **PASS 19.87s**：无效配置被真实核心拒绝、同 Agent 另一节点不受影响、技术回滚未复活撤权、有效配置恢复后两节点服务且账本完整；撤权 1.054s | `docker-config-safety-2.log`（`-1` 是 Deferred 退避缺陷旧失败，已修） |
| A02 `TestDockerBatchPartialFailure` | **PASS 72.98s**：在线撤权 0.342s、部分失败可见、同一 `task_id` 重试收敛、旧凭据仍拒 | `docker-batch-retry-1.log` |
| 主链 `TestDockerAcceptance`（当前 0.1.1 Agent） | **PASS 57.29s**：在线 TCP/UDP 撤权 **1.830s**；自然离线到期新连接 **0.733s** 拒、旧 UDP **0.938s**、旧 TCP **1.596s** 终止；Agent 重启不复活撤权；崩溃自动重启；卸载/重建保留卷身份不变 | `docker-e2e-current-agent.log` |
| A14 隔离恢复演练 | **PASS 120.64s**：独立目标还原、新随机 generation、显式 `accept-generation`、账本精确一次、旧授权不复活、源栈未改；实测 RPO 窗口 7.467s、备份年龄 75.52–76.19s、还原 4.9013s、演练 RTO 31.8674s，ACK 已删 **70,093B/1 batch 不可恢复** | [docker-recovery.md](docker-recovery.md)、`docker-recovery-2.log`、scratch `recovery-evidence-summary.json` |
| 本地最终工程检查 | `go test ./...`、`go vet ./...`、Agent 全包 `-race`、真实 Xray `-race`、clientconfig 本地真实核心矩阵（6 组合 + 2 守卫）全部 PASS；**39 个环境门控 SKIP 不算集成成功** | `final-local-*.log` |

仍明确**未验收**（不得写成通过）：十二项验收标准并非全部通过——`#10` A15 只有 **30/40** 真实格通过（v2rayN 10 格 `not-run`，实际 GUI 导入未执行），`#12` 存在 WSL 执行边界不符合项；发布虽已两轮通过（每平台 72 文件 / 71 条 SHA256），但 `build/` 中**确切的** Linux amd64/arm64 发布二进制只做交叉构建与清单/ELF 校验、未在目标平台运行（Linux amd64 平台功能已由 Docker 内 staged 构建真实验收，哈希与发布目录不同；arm64 架构整体未运行）。外部 Telegram、公网直连、宿主 systemd、主机重启仍排除。执行边界：一次 DS 脚本自测真实启动了 WSL（[execution-boundary-incident.md](execution-boundary-incident.md)），属明确的非符合项，**不能被后续保护或原容器一致抵消**；此后的测试固定 `PANEL_BASH=E:\SDK\Git\bin\bash.exe`。远端操作只由授权操作者通过受保护 SSH 在归属本项目的资源上执行，不接触原业务容器/服务、不输出秘密；本轮远端验收已结束，不再进行远端操作。

## 1. 本轮改动（产品版本/配置已另由父完成，此处不扩范围）

| 文件 | 改动 |
| --- | --- |
| `tests/e2e/docker_lifecycle_test.go`（新增） | A02/A07/A13 三个 Docker 用例 + 离线解析用例 |
| `deploy/docker-lifecycle.sh`（新增） | 固定动作版本切换脚本（同 state 卷内 v2/core 互斥、归属/网络/挂载校验、不打印状态库内容） |
| `scripts/Prepare-DockerLifecycle.ps1`（新增） | 本地准备上传件与来源记录（**不 SSH、不 Docker**） |
| `docs/archive/verification/docker-lifecycle.md`（本文件） | 证据与执行入口 |

产品侧（`internal/agent/config/config.go` 的 0.1.1、`internal/agent/xray/version.go` 的白名单 26.3.23 及对应单测）已在上一轮完成，本轮未再改动。

## 2. 版本与 SHA256

### 2.1 真实切换起点 = 镜像内二进制（不是 `build/` 旧产物）

镜像 `proxypanel-acceptance:20261021` 内自带 agent 0.1.0，**已包含早前 expiry 修复**；它才是升级前基线。父在远端执行 `sh <root>/lifecycle.sh baseline` 会实测并打印：

```
image agent:  proxypanel-agent 0.1.0 sha256=<64 位>
image core:   Xray 26.3.27 (...) sha256=<64 位>
staged agent: proxypanel-agent 0.1.1 sha256=<64 位>
staged core:  Xray 26.3.23 (...) sha256=<64 位>
```

测试 `TestDockerAgentVersionUpgrade` / `TestDockerCoreVersionSwitch` 解析该输出，断言镜像 agent 版本 0.1.0、staged agent 0.1.1，且两者 **SHA 不同**（同版本重建会直接失败）。

父在远端实测（2026-10-21）：**镜像 agent 0.1.0 SHA256 `90ee1d0c…`**（不是 `build/` 里的旧产物），staged agent `a28ca865…`（最新件；父远端当时实测的旧件 `7eaebb96…` 已作废，见 §12.4）、staged core `05205489…`（26.3.23）容器内实测通过——即“升级前”基线是镜像内二进制，`build/linux-amd64/agent`（`1ffe5bb4…936fe`）与 `build/windows-amd64/agent.exe`（`7e7ce4c6…4916`）**只作来源记录**。

### 2.2 上传件（本轮实测）

上传集是**一个整体**（五个文件）：staged core 会在**自己可执行文件所在目录**（容器内 `/s`）解析 `geoip.dat`/`geosite.dat`，缺资产会在原 Agent 已停止后起不来（§15 的实测根因）。

| 产物 | SHA256 | 实测版本 |
| --- | --- | --- |
| `agent-new`（linux/amd64，`-trimpath -buildvcs=false`） | `a28ca86562686f4b66c856e69253cdc6202461f53f1c2cd1c38358f76fabd66a` | 同源 Windows 构建 `-version` → `proxypanel-agent 0.1.1`；连续两次构建同 SHA。**最新件**（早先的 `7eaebb96…` 作废，远端仍需换成此件，§12.4） |
| `xray-core`（官方 linux/amd64 **26.3.23**） | `05205489259d6c74795d98b4f01fcd11f987fc6ff7f65fd2991e9a7164518c98` | 容器内 `xray version` → `Xray 26.3.23 … cb7bfeb` |
| `geoip.dat`（官方 26.3.23 `Xray-linux-64.zip` 条目） | `4bdb1c1cf01ec988789085a9acf5522b5f3355906e3eaa6a90ea60db5b583711`（19701033 字节） | 无独立版本号；来源 = 已按官方 `.dgst` 校验过的同一 zip（§15.2） |
| `geosite.dat`（同一官方 zip 条目） | `2804834a4139af1194ffb7badfdbf4d12f76723be27575c0b95f67b702a2c276`（10454877 字节） | 同上 |
| `SHA256SUMS` | 覆盖**四行**（agent-new / xray-core / geoip.dat / geosite.dat），LF 行尾，容器内 `sha256sum -c` 校验 | — |

官方摘要：`Xray-linux-64.zip` = `8985f9dac52714194cd86368e6361f234b9e26017603b24d58d83041197c5db5`（与 `.dgst` 的 `SHA2-256=` 一致）；Windows 侧解出 `E:\SDK\Xray\v26.3.23\xray.exe` = `12f1ccde23bc52ffc6f7c1ddda8ea8963c97fcb1be401de85068860035af693d`。下载/解压限 `E:\SDK\Xray\v26.3.23[-linux-amd64]`。发布选择：26.3.27 为最近稳定版，v26.3.23（2026-03-23，GitHub 标记 prerelease）是与它最近的另一发布，已确认存在；未追 26.10.17。

## 3. 26.3.23 加入闭合白名单前的真实核心检查（上一轮完成，证据仍有效）

`XRAY_BIN=E:\SDK\Xray\v26.3.23\xray.exe`，`go test -tags xrayreal ./internal/agent/xray ./internal/agent/supervisor` 全 PASS（生成配置 `xray run -test`、每用户统计、撤权终止既有连接、本地到期重建、完整 Agent 循环）；26.3.27 同套回归 PASS。白名单未更新时完整 Agent 会被 `checkCoreBinary` 拒绝（闭合清单按设计生效），加入后同一用例 PASS。

证据：`$env:PI_SCRATCH_DIR\lifecycle-realcore-26.3.23.log`、`lifecycle-realcore-26.3.27.log`、`lifecycle-version-unit.log`、`lifecycle-vet.log`、`lifecycle-upload\manifest.txt`。

## 4. 三个用例的最终口径

### A02 `TestDockerBatchPartialFailure`

1. 两个受管节点真实可代理后才提交；**任何注入前**先登记恢复：`start agent2` / `start agent1`（`t.Cleanup`，LIFO 顺序保证先恢复容器、再恢复成员授权）。
2. 停自有 `agent2`（真实故障一侧，不是模拟错误码）→ 提交撤权批量：可达节点 60s 内真实拒绝旧凭据，另一端派发项未完成（`succeeded>=1` 且存在未完成项）。
3. 未触达项被面板清扫为 `timeout` 且 `tasks.last_error` 非空，**不改写为成功**。
4. 重启 agent2 → 两个 Agent 达到 `applied_revision=desired_revision`；agent2 节点用旧凭据真实连接被拒；`credentials` 无 `active`、`members` 不再 active/proxy_enabled。
5. **重试的是同一个失败任务**：`POST /api/v1/admin/tasks/{id}/retry` → 断言 `state=pending`、`attempts>=1`；随后断言原批次里该 `task_id` 的项与任务本身都收敛为 `succeeded`，原批次无 pending/dispatched/failed/timeout，且恢复后的节点仍拒绝旧凭据（**复用撤权前建立的客户端与旧凭据**，不重新登录/导出）、账本不回退。（不再用“重新授权 + 新建 batch”冒充重试。）

> **生产缺陷已修复（按父授权）**：原 `Store.RetryTask` 只把 `tasks` 置回 `pending`，既不重开同一 `task_id` 的 `batch_items`，也不发布新 revision，因此重试后任务停在 `pending`、原批次永远显示 `timeout`。现实现（`internal/panel/store/ops.go`）在**同一事务**内完成：`control_state` 行锁 → 发布新 revision 到该 task 所属 agent（只改 `desired_revision`，不触碰 `applied_revision`）→ 重开任务（`attempts+1`，受 `attempts < max_attempts` 守卫）→ 重开该任务的 `batch_items`（`timeout|failed` → `pending`，并**盖上本次新 revision**，避免被 `SweepTimeouts` 立刻判为 superseded）→ 已完成批次回到 `applied`。业务动作不重复应用（成员/凭据/批次/账本行数均不变）；`succeeded` 为终态不再重开；已排队与 `attempts` 用尽均返回明确 409。新 revision 的授权来自实时状态，因此不会复活已撤权凭据。

#### 4.1 `RetryTask` 定点回归（真实 PostgreSQL，父执行）

新文件 `internal/panel/store/retry_task_regression_test.go`，复用既有 `newRegStore` harness（每个用例独立 schema，结束后 DROP），无 SSH、不读 `.local`：

- `TestRetryTaskReopensDispatchAndPublishesCurrentRevision`：真实 `CreatePreview`+`SubmitBatch` 产生一次业务动作 → `SweepTimeouts` 把派发扫成 timeout/批次 completed → `RetryTask` 断言：同一 `task_id` 回到 `pending`、`attempts` 恰好 +1、`control_state` 只前进一步且 `agents.desired_revision` 同步、**`applied_revision` 不动**、原 `batch_item` 重开为 `pending` 且 revision == 新 revision（再跑 `SweepTimeouts` 不被 superseded/不重新 timeout）、批次回 `applied`、成员版本/激活态/凭据/批次数/账本行数全部不变；随后 `BuildSnapshot` 的 revision 等于新 revision 且**不含已撤权成员**（`ExpectedUserCount=0`），再用 `RecordApplyReport` 模拟真实 Agent 回执 → 任务与 item 均 `succeeded`。
- `TestRetryTaskRefusals`：未知 id → `NOT_FOUND`；已 `succeeded` 终态 → `CONFLICT` 且不回退；已排队（`pending`）二次重试 → `CONFLICT` 且**不再推进 revision**；`attempts = max_attempts` → `CONFLICT` 且状态与 revision 不变。
- `TestRetryTaskIsAtomicOnFailure`：在测试 schema 建 `plpgsql` 触发器让 `batch_items` 的 UPDATE 抛错（事务失败注入），断言整笔回滚：任务仍 `timeout`、`attempts`/`last_error` 不变、item 仍 `timeout` 且旧 revision、`control_state.revision_counter` 与 `agents.desired_revision` 不变、批次仍 `completed`；删除触发器后同一 `task_id` 重试成功。

运行（父，需既有隔离 PG；`-race` 需 `CGO_ENABLED=1` 且 `E:\SDK\w64devkit\2.10.1\w64devkit\bin` 在 PATH）：

```powershell
. .\scripts\Use-DevEnvironment.ps1
# 连接串沿用既有隔离测试库（与 ./internal/panel/... 存量回归相同的 PANEL_TEST_DATABASE_URL）
go test -race -count=1 -v ./internal/panel/store -run 'TestRetryTask'
```

另外：`RetryTask` 属于**面板服务端**代码，A02 的 Docker 用例要看到修复效果，必须先按既有流程重建验收镜像（`scripts/Initialize-DockerAcceptance.ps1` 重新编译 panel/agent/fixture 并 `docker build` 出 `proxypanel-acceptance:20261021`），再重跑 §6.2 的 `TestDockerBatchPartialFailure`；否则仍在测旧面板二进制。

### A07 `TestDockerConfigSafety`

1. 额外节点（名称唯一，形如 `docker-lifecycle-<unixnano>`；首轮曾用 `docker-lifecycle-2`，归档后名称仍占唯一索引故不再复用）（server1/同一 Agent，127.0.0.1:20105）+ 独立组/订阅/成员（`docker-lifecycle-active`、`docker-lifecycle-revoked`），不进入共享 `docker-group`。
2. **注入前**登记清理：先把节点恢复为有效 `protocol_settings`，再 `lcQuiesce`（停用两个成员 + **归档**节点 + 等两个 Agent 收敛 + 容器内断言 `agent1:20105` 无 LISTEN；口径见 §5 与 §12.2）。
3. 通过面板下发真实核心会拒绝的文档：`protocol_settings.fallbacks="not-an-array"`（面板校验透传；真实 26.3.27 报 `cannot unmarshal string into … VLessInboundFallback`）。
4. 断言 `desired>applied`（无效文档从未成为已应用修订）、容器日志出现 `applied last known good technical config with current authorization`、`mount-check agent1` 显示状态卷挂载。
5. 同 Agent 的另一节点连续 3 秒真实流量正常；受影响节点随后由上一份有效文档恢复服务；被撤权成员旧凭据仍被拒、`credentials` 无 `active`。
6. 恢复有效文档 → 两个 Agent 收敛、账本不回退、两节点均恢复。

### A13 `TestDockerAgentVersionUpgrade`

1. `stage`（容器内 `sha256sum -c` + 实测版本）与 `baseline`；断言镜像 0.1.0 / staged 0.1.1 且 SHA 不同。
2. 先真实撤权一个成员并保留其旧凭据。
3. **队列持久性（逐批 + 屏障）**：`lcWaitEmpty`（连续两次空快照，间隔 > 采样周期 5s）先排空 → 记录基线：该成员账本字节、该 Agent 的 `usage_batches` 行数、PostgreSQL `now()` 作为**记账窗口起点** → 停自有 PostgreSQL（注入，登记恢复）→ 用**停库前已导出**的两节点客户端各打 3 次真实请求（每次探针都必须成功；被拒即判失败，否则根本没有字节可记）→ `lcWaitStable` 取**切换前稳态快照**（连续两次 `batch_id/样本数/字节/成员字节/SHA` 全等；`attempts` 递增不算变化）→ 在 pending 期间执行 `agent-upgrade` → `lcWaitStable` 取**切换后最终快照**（含切换自身产生的批次）→ 断言切换前每个批次仍在且各字段不变（`lcCarried` 为空）→ 恢复 PostgreSQL、等 readyz、再 `lcWaitEmpty` 排空。
4. 逐批断言（**不**放宽为“账本单调”）：最终快照里的每个 `batch_id` 必须在 `usage_batches(agent_id,batch_id)` 恰好 1 行；其 `payload_sha256` 必须**等于**从 Agent SQLite 行（`samples ORDER BY seq`，毫秒→UTC）按上传器同一规则重建、再用 `usagecalc.PayloadSHA256` 算出的值（不是只查 `len(SHA)==64`）；`sample_count`、`usage_entries` 字节总量、该成员字节三者也必须完全相等；行数 0（队列已排空）判“丢失”，行数 >1 判“重复入账”。此外：窗口内 `usage_batches` 收到的 `batch_id` 集合必须**恰好等于**最终快照集合（面板在停库期间不可达，故不可能有集合外批次），该 Agent 的批次总行数 == 基线 + 快照数，且该成员账本增量**恰好等于**快照内成员字节之和。
5. 版本断言：`agents.version` 0.1.1、`agents.id`/`control_generation` 不变、**同一次观察**里 core 精确为 26.3.27 且 `last_heartbeat_at` 新于动作前快照（§14）、`applied=desired`、`mount-check agent1v2` 指向 `ppaccept-20261021-agent1:/state`、已撤权仍拒、两节点在**同一轮探测**里都服务。
6. `agent-rollback` 回镜像 0.1.0 并重复不变量；`cleanup` + `lcQuiesce` 收尾。

### A13 `TestDockerCoreVersionSwitch`

**同一个新版 Agent 0.1.1** 下切换核心，容器互斥、最终回到镜像旧版：

1. `agent-upgrade` → `agent1v2` = staged agent 0.1.1 + **镜像核心 26.3.27**；断言 `version=0.1.1`、`xray_version` **精确**为 26.3.27。
2. `core-switch` → 停 `agent1v2`（不删除，作为回退目标）→ `agent1core` = **同一 staged agent 0.1.1** + staged 核心 26.3.23；断言 `version` 仍为 0.1.1、`xray_version` **精确**为 26.3.23、`mount-check agent1core` 指向同一状态卷。
3. `core-rollback` → 删 `agent1core`、重启 `agent1v2`；断言 `xray_version` 回到 26.3.27、`version` 仍 0.1.1。
4. `cleanup` → 删两个生命周期容器、重启镜像 agent1；断言 `version=0.1.0`、核心 26.3.27。
5. 每一步都重跑：身份/控制代次不变、**同一次观察**里 Agent 版本与 core 版本精确匹配、`last_heartbeat_at` 严格新于动作前快照、`applied=desired`、outbox 排空（有界等待）、账本不回退、已撤权仍拒、两节点在**同一轮探测**里都成功（切换不承诺零停机，见 §14）。

脚本层的互斥与归属：`assert_exclusive` 保证同一时刻只有一个容器持有状态卷与 20101-20105 端口；`owned_*` 校验 root marker + 容器/卷/**网络**/镜像 label；`mount_check` 校验 `NetworkMode` 为 `ppaccept-20261021-net`、状态卷真的挂在 `/state`、无 `docker.sock`、宿主 bind 仅在 `$OWNED 根` 之下。

## 5. 端口与污染控制（20105 与客户端矩阵）

- 生命周期节点固定 127.0.0.1:20105（Agent 容器已发布该回环端口）。
- **只有归档才释放端口**：`DELETE /api/v1/admin/nodes/{id}` 归档节点（保留 usage 历史、审计与账本行），节点随即离开 `BuildSnapshot`，Agent 移除该实例、20105 的 LISTEN 套接字消失。
- **仅停用（`enabled=false`）不释放端口**：`BuildSnapshot` 对所有非 archived 节点都生成实例，`enabled=false` 只是让该实例没有 clients，空用户的核心依然监听。实测证据见 §12.1。
- 因此两个生命周期用例结束时执行 `lcQuiesce`：两个成员 `active=false` + 归档节点 + 等两个 Agent 收敛 + 容器内内核表断言 `agent1:20105` 无 LISTEN（§10）。
- 再次运行时 `lcPrepare` 不再“重新启用”旧节点（PATCH 不能反归档，旧名字仍占唯一索引）：`lifecycleFixture` 用**唯一名称**新建节点，经 `/batches/preview`+`/batches` 把新节点挂回原组（组被订阅引用时直接 `PATCH /node-groups` 会被 409 拒绝），PATCH 两个成员的 `active/proxy_enabled/expires_at`，并更新 `lifecycle-fixture.json`；组、订阅与两个固定用户名成员全部复用。
- 未修改客户端矩阵文件，也未改动现有转发端口。

脚本层的互斥与归属：`assert_exclusive` 保证同一时刻只有一个容器持有状态卷与 20101-20105 端口；`owned_*` 校验 root marker + 容器/卷/网络/镜像 label；`mount_check` 校验 `NetworkMode` 为 `ppaccept-20261021-net`、状态卷真的挂在 `/state`、无 `docker.sock`、宿主 bind 只允许在 `$root`（`/opt/proxypanel-acceptance-20261021`）之下。

## 6. 待父执行（本会话无 SSH/Docker 权限）

### 6.1 上传（父持有 SSH 通道）

```powershell
$root='E:\Work\proxypanel'; $remote='/opt/proxypanel-acceptance-20261021'
$args=@('-i',"$root\.local\database-ssh-key",'-o','BatchMode=yes','-o','StrictHostKeyChecking=yes',
        '-o',"UserKnownHostsFile=$root\.local\database-known-hosts",'-o','ConnectTimeout=10')
ssh @args root@192.168.5.203 "set -eu; d=$remote; [ \"\$(cat \$d/.proxypanel-acceptance)\" = 20261021 ]; mkdir -p \$d/lifecycle; chmod 700 \$d/lifecycle"
scp @args "$root\deploy\docker-lifecycle.sh" "root@192.168.5.203:$remote/lifecycle.sh"
scp @args (Join-Path $env:PI_SCRATCH_DIR 'lifecycle-upload\agent-new')   "root@192.168.5.203:$remote/lifecycle/agent-new"
scp @args (Join-Path $env:PI_SCRATCH_DIR 'lifecycle-upload\xray-core')   "root@192.168.5.203:$remote/lifecycle/xray-core"
scp @args (Join-Path $env:PI_SCRATCH_DIR 'lifecycle-upload\SHA256SUMS')  "root@192.168.5.203:$remote/lifecycle/SHA256SUMS"
ssh @args root@192.168.5.203 "sh $remote/lifecycle.sh stage && sh $remote/lifecycle.sh baseline && sh $remote/lifecycle.sh state-summary"
```

`stage` 会在受限一次性容器里 `chmod 0755`、`sha256sum -c` 校验并打印容器内实测版本；`baseline` 打印镜像与暂存件的版本+SHA；`state-summary` 只打印状态库**大小与 SHA256**（库内含 Agent token/凭据，故默认绝不打印内容）。

### 6.2 运行（串行，不能与其他 Docker 用例并发）

```powershell
. .\scripts\Use-DevEnvironment.ps1
$env:PANEL_DOCKER_TEST='1'; $env:PANEL_DOCKER_ROOT='E:\Work\proxypanel'
go test -v ./tests/e2e -run 'TestDockerBatchPartialFailure' -count=1 -timeout 20m   # A02（含 60s 清扫）
go test -v ./tests/e2e -run 'TestDockerConfigSafety'        -count=1 -timeout 20m   # A07
go test -v ./tests/e2e -run 'TestDockerAgentVersionUpgrade' -count=1 -timeout 25m   # A13 agent（含 PG 停机注入）
go test -v ./tests/e2e -run 'TestDockerCoreVersionSwitch'   -count=1 -timeout 25m   # A13 core（0.1.1 下 26.3.27→26.3.23→26.3.27）
go test -v ./tests/e2e -run 'TestLifecycleParsing'          -count=1                # 离线解析
```

SSH 隧道沿用 `scripts/Invoke-DockerAcceptance.ps1` 的等价端口转发。

## 7. 实测 / SKIP 一览

| 项目 | 状态 |
| --- | --- |
| 镜像内 agent/core 版本+SHA（`baseline` 动作） | 已实测：镜像 agent 0.1.0 `90ee1d0c…`、core 26.3.27 `8255dd93…`；staged agent 0.1.1 `a28ca865…`、staged core 26.3.23 `05205489…`（`docker-lifecycle-assets-stage.log`）。收尾轮后镜像内 agent 已更新为 0.1.1（§16.2） |
| 旧 `build/` 产物来源记录（SHA + Windows 实跑 0.1.0） | 已实测 |
| 26.3.23 官方下载 + `.dgst` 校验 + 真实核心配置/统计/撤权/完整 Agent 循环；26.3.27 同套回归 | 已实测（PASS） |
| 新 agent 构建可复现（两次同 SHA） | 已实测 |
| `go build ./...`、`go vet ./...`、`gofmt`、`go test ./tests/e2e -run TestLifecycleParsing`、`bash -n deploy/docker-lifecycle.sh` | 已实测（PASS） |
| A02/A07/A13 三个 Docker 用例 | **已真实通过**（0.1.1）：A02 72.98s、A07 19.87s、A13 agent 93.21s、A13 core 41.81s，见 §0 与 §16 |
| 生产 `RetryTask` 收敛缺陷 | **已修复并真实回归**：三个定点用例在真实 PG 上 `-race` **PASS 55.38s**（`retry-task-real-pg-race.log`），A02 双节点同任务重试 **72.98s PASS**（`docker-batch-retry-1.log`） |
| `go test -race ./internal/panel/...`（离线，PG 用例按设计跳过） | 已实测（PASS） |
| 宿主机 systemd / 主机重启 / 公网直连 / Telegram 实际送达 | 明确未验收 |

## 8. 剩余缺口

- 三个 Docker 用例依赖：既有 `ppaccept-20261021-*` 栈（镜像带 `io.proxypanel.acceptance=20261021` 标签）、`.local/docker-acceptance/{test.json,agent2.json}`、`control.sh` 已上传。
- `clientconfig` 真实核心矩阵仍按他人契约固定 26.3.27（`realcore_test.go:67`，tag `corematrix`），本轮未在 26.3.23 复跑，也未放宽客户端导出契约。
- 队列持久性只在 `agent-upgrade` 用例做 PG 停机注入（一次足够，A08 另有专项）；core 用例不重复注入了，以免与矩阵/恢复用例争用时间。
- `RetryTask` 修复属面板服务端：A02 的 Docker 用例必须先重建 `proxypanel-acceptance:20261021` 镜像（新 panel 二进制）再执行，否则仍在测旧行为。

## 9. A07 实跑失败定位（2026-10-21，`docker-config-safety-1.log` FAIL 317.67s）

实测结果：撤权 0.295s、无效文档被真实核心拒绝、同 Agent 另一节点不受影响、技术回滚未复活撤权——**面板侧证据全部成立**；失败在两处 `waitFor`：line 709「valid doc 后 converge 180s 超时」、line 657「cleanup 等节点停用 120s 超时」。诊断日志显示 agent1 持续 `snapshot revision 84 generation 1 failed (2 instance results)`，容器 `oom=false restart=0`。

### 9.1 面板文档确认已清（不是 `fallbacks` 残留）

- `POST/PATCH /api/v1/admin/nodes/{id}`：`internal/panel/api/admin.go:329-345` 把整个 `protocol_settings` 对象**整体替换**（`if in.ProtocolSettings != nil { n.ProtocolSettings = in.ProtocolSettings }`），随后 `protocol.Sanitize` 只删服务端 secret，不保留旧键。
- 落库：`internal/panel/store/resources.go:535-540` 写 `protocol_settings=$13`（整列覆盖，非 jsonb merge）。
- 反序列化：`internal/panel/domain/types.go:24-38` 把 body 解成 `map[string]json.RawMessage`（整对象，不是浅合并）。
- 本轮测试侧进一步加了硬断言：恢复后读取节点并断言 `protocol_settings.fallbacks` **不存在**（`lcAssertNodeDocumentValid`），失败信息只打印 JSON 类型与键名（不含配置/凭据）。

结论：restore 后数据库里的文档是干净的；`revision 84` 的文档本身合法，Agent 却一直失败——根因在 Agent 侧。

### 9.2 Agent 侧缺陷（本轮已由产品侧修复；我未改 `internal/agent`）

> **状态更新（2026-10-21）**：该退避缺陷已按下面的建议在 `internal/agent/supervisor` 源码修复，父的 `-race` 回归已通过（§12.4）。定位证据与建议保留为历史记录。

`internal/agent/supervisor/supervisor.go`：

- `:592-601` 把 `xray.StateDeferred`（“上一次失败、正在退避等待”）落进 `default:` 分支，于是**调用 `recordFailure` 再加一次失败**并 `ok=false`；
- `:1101-1119 recordFailure` 每次 `f.count++`，`shift=min(count,6)`，`next=now+5<<shift`（10s/20s/40s/80s/160s/**320s**）；
- `:1127-1138 skipSet` 把仍在退避的实例作为 `Skip` 传给 Manager，Manager 返回 `StateDeferred` → 又回到 `:592`。

即：**每 1s 的轮询把退避自己再加长一次**，几次心跳内就把单实例退避顶到 320s 并持续续期 → 一次真实的 rollback（无效文档）之后，即使文档已修好、desired 已推进，实例也永远落在退避里，`applied_revision` 不再推进，批次/节点收敛与 60s 撤权 SLA 都无法保证。

**最小修复建议**（供该文件所有者）：

1. `:592` 前加一个分支，把 deferral 与真实失败分开：
   ```go
   case xray.StateDeferred:
       // 已在退避中：延迟本身不是新失败，否则轮询会把退避无限续期
       ok = false // 仍未应用新授权，绝不能谎报 applied
   ```
   即：**不调用 `recordFailure`**，其他保持（`ok=false` 依然保证不虚报 applied）。
2. `:1114-1118` 把上限压到 SLA 之内（例如 `if shift > 2 { shift = 2 }` → 最长 20s），保证一次失败后能在 60s 撤权窗口内真正重试；并可考虑在成功应用后 `clearFailure`（已存在）。

修复前，A07 用例不再“静默超时”：`lcConverge` 会在超时后打印安全状态、重启自有 Agent 容器（不动 SQLite 状态卷）清空内存退避；若重启后才收敛，用例**明确判失败**并指出上述 `supervisor.go:592/1102` 缺陷。

### 9.3 父可执行的安全 SQL（只输出指针/类型/错误，不含 token/凭据）

```sql
-- 1) 节点文档：只看 enabled 与 fallbacks 是否还在、JSON 类型
select id, name, enabled, version,
       jsonb_typeof(protocol_settings->'fallbacks') as fallbacks_type,
       (protocol_settings ? 'fallbacks')           as has_fallbacks,
       jsonb_typeof(protocol_settings->'decryption') as decryption_type
from nodes where name like 'docker-%' order by name;

-- 2) Agent 指针与心跳（不取 instances 内容：其中含完整 Xray 文档）
select id, version, xray_version, desired_revision, applied_revision,
       coalesce(round(extract(epoch from (now()-last_heartbeat_at)))::int,-1) as heartbeat_age_s,
       jsonb_array_length(coalesce(instances,'[]'::jsonb)) as instance_count
from agents where archived=false order by id;

-- 3) 面板侧实例状态（core_instances 只有状态/错误，没有配置）
select node_id, state, config_revision, generation, left(coalesce(last_error,''),160) as last_error
from core_instances order by node_id;

-- 4) 最近回执：只取 state 与首条错误文本（payload 里含 results/文档，故不整列输出）
--    注意：agent_receipts 没有 created_at（migrations/0001_init.sql:415），时间列是 received_at / applied_at
select agent_id, revision, state,
       left(coalesce(payload->'results'->0->>'error',''),200) as first_error
from agent_receipts order by received_at desc limit 5;

-- 5) 任务/批次项
select id, agent_id, state, attempts, max_attempts, left(last_error,160)
from tasks where batch_id<>'' order by updated_at desc limit 10;
select id, agent_id, state, revision, left(error,160)
from batch_items order by updated_at desc limit 10;
```

### 9.4 两个入口（父可直接用，无需删 Agent 卷）

```powershell
. .\scripts\Use-DevEnvironment.ps1
$env:PANEL_DOCKER_TEST='1'; $env:PANEL_DOCKER_ROOT='E:\Work\proxypanel'
# 只读定位：打印安全事实（节点 archived/enabled/fallbacks 类型、desired/applied、实例状态、回执错误、容器与状态卷摘要）
go test -v ./tests/e2e -run 'TestDockerLifecycleDiagnose' -count=1 -timeout 10m
# 从当前失败 fixture 恢复：停用两个成员 + 归档节点 + 重启自有 Agent 清退避，并验证 20105 已释放
go test -v ./tests/e2e -run 'TestDockerLifecycleRecover' -count=1 -timeout 15m
```

`TestDockerLifecycleRecover` 不删除任何卷：它只改面板状态（成员 `active=false`、`DELETE /api/v1/admin/nodes/{id}` **归档**节点）、`restart agent1/agent2`（Docker 容器重启，SQLite 状态卷原地保留），最后用**容器内内核表**确认 `agent1` 在 20105 上已无 LISTEN（见 §10）。**归档是唯一能释放端口的动作**：仅 `enabled=false` 时核心仍在监听（§12.1）；归档后 A13 的 `lcPrepare` 会自动用新节点重建 fixture（§12.2）。`TestDockerLifecycleDiagnose` 完全只读：fixture 文件不存在时只报告路径并停止，不会创建任何节点/成员。

### 9.5 脚本缺陷修复（`deploy/docker-lifecycle.sh`）

原 `core-switch` / `core-rollback` 用 `docker container inspect --format '{{index .Config.Labels \"$label\"}}'`——模板在单引号里，`$label` 不展开且模板含反斜杠，检查必然失败、两个动作必被拒绝。现在改为先判存在再调用已证实有效的 `owned_lifecycle`，与其余动作一致；`bash -n` 通过，需父重新上传 `deploy/docker-lifecycle.sh → $remote/lifecycle.sh`。

### 9.6 本轮测试侧同时加固

- `patchNode` 的 settings 每次用新 map（`lcValidProtocolSettings()`），避免 map 复用带旧 `version`；
- 恢复/停用一律同时下发 `protocol_settings`（即便上一步失败也会被清理），并在 `lcPrepare` 后硬断言文档干净；
- `lcQuiesce` 不再 `Fatal`：先 PATCH → 30s 收敛 → 必要时重启自有 Agent（+90s）→ 仍不收敛才 `Errorf` 并打印安全状态，避免 cleanup 掩盖主失败，同时尽量释放 20105（§12.2 起改为归档式释放：仅停用节点不会让核心停止监听）；
- `lcLogSafeState` 只查 `core_instances`/`tasks`/`batch_items`/`agent_receipts` 的错误文本与指针，**不再打印 `agents.instances` 原文**（其中含完整 Xray 文档）。

## 10. 端口“已释放”判据改为容器内 LISTEN 探测（2026-10-21 二次修正）

### 10.1 为什么原来的 `net.DialTimeout(127.0.0.1:20105)` 必假红

验收隧道由父在本机 `ssh -L 127.0.0.1:20105:127.0.0.1:20105` 建立。**本地监听端由 ssh 客户端持有**：远端没有任何服务时，本地 TCP 握手仍会成功（随后才被关闭/拒绝数据）。因此 `DialTimeout` 恒成功 → `TestDockerLifecycleRecover` 结尾必然误判“端口仍被占用”。宿主侧的任何 TCP 探测（connect/nc）都有同样问题。

### 10.2 新脚本动作 `listeners N P`（唯一可信端口判据）

`deploy/docker-lifecycle.sh` 新增只读动作：

```
sh <remote>/lifecycle.sh listeners agent1 20105
listeners container=agent1 port=20105 count=0
```

- **容器白名单**：`agent1 | agent2 | agent1v2 | agent1core`；**端口白名单**：`20101..20105`。其它取值直接拒绝（exit 66/67）。
- **归属校验**：容器必须存在（68）、带 `io.proxypanel.acceptance=20261021` 标签（`owned_lifecycle`）、且处于 running（69，否则网络命名空间不可读）。
- **判据来自内核表**：在容器内读 `/proc/net/tcp` 与 `/proc/net/tcp6`，只统计 `local_address` 以 `:HEX` 结尾且 `st == 0A`（LISTEN）的套接字；`printf '%04X'` 由宿主机脚本换算（20105 → `4E89`）。**不看**远端地址、不看 ESTABLISHED，因此隧道/对端连接不会造成假阳性。
- **输出固定一行** `listeners container=<name> port=<port> count=<N>`；异常输出（空、非数字、重复行、容器/端口不符）一律拒绝（70），不会被误读成“无监听”。
- **读不到内核表必须失败**：容器内先数可读表，`/proc/net/tcp` 与 `/proc/net/tcp6` **都**不可读 → 容器内 exit 1，宿主机映射为 **exit 71**；`awk` 非零退出、或 awk 输出非数字/空，同样容器内 exit 1 → 外层 71。绝不再把“表不可读”静默当成 `count=0`（旧的 `[ -r "$f" ] || continue` 正是这个缺陷）。

### 10.3 用例侧的确定性解析与判据

- `parseListenerCount(raw, container, port)` 严格解析上述单行，缺行/重复/非数字/端口容器不符全部报错；`lcListenerCount` 通过 `tryLifecycle` 调用（不 Fatal，供 cleanup 使用）。
- `lcWaitNoListener(t, d, "agent1", 20105, 60s)` 有界轮询直到 `count==0`，返回最后一次实测计数用于报错。
- `TestDockerLifecycleRecover`：收敛（`applied==desired`）与 `count==0` 为**硬判据**；`core_instances.state='running'` 只作为附带事实记录（面板回执可能滞后，不再据此判红）。
- `lcQuiesce`（cleanup）：同样以 `count==0` 为硬判据（`Errorf`，不中断后续 cleanup）：先 PATCH → 30s 收敛 → 必要时重启自有 Agent（+90s）→ 再探测（§12.2 起改为 `DELETE` 归档节点：停用不释放端口）；`core_instances` 只在“无监听但面板仍报 running”时记一条 note，避免用面板簿记掩盖真实端口占用。
- `TestDockerLifecycleDiagnose` 变为**完全只读**：`loadLifecycleFixture()` 只读 `lifecycle-fixture.json`，缺失时打印路径并停止，不创建节点/组/订阅/成员；同时打印 `agent1:20105`、`agent2:20105` 的 LISTEN 计数（安全事实）。

### 10.4 离线验证（本会话实跑，无 Docker/无 SSH）

| 检查 | 结果 |
| --- | --- |
| `printf '%04X' 20105` → `4E89` | PASS |
| 用**脚本文本中原样的 awk 行**跑合成内核表：含 1 条 LISTEN + 1 条 ESTABLISHED → `1` | PASS |
| 仅 ESTABLISHED（对端地址也是 `:4E89`）→ `0`（无假阳性） | PASS |
| `tcp` 无监听 + `tcp6` 有监听 → `1`（两表求和） | PASS |
| 一张表可读 + 一张表缺失 → 出数（不因缺表失败） | PASS |
| **两张表都不可读 → 容器内 exit 1 → 外层 71**（旧行为是静默 `count=0`） | PASS（本轮修正） |
| `go test ./tests/e2e -run TestLifecycleParsing`（新增 listener 解析 8 例拒绝 + 2 例接受） | PASS |
| `gofmt -l`、`go vet ./tests/e2e`、`bash -n deploy/docker-lifecycle.sh` | PASS |
| 所有 Docker 用例（含 Diagnose/Recover）无环境时 SKIP | PASS |

### 10.5 父后续执行顺序（最新版见 §12.6）

1. **只读定位**：`TestDockerLifecycleDiagnose`（不创建、不修改任何对象）。
2. **释放 20105**：`TestDockerLifecycleRecover`（停用成员 + **归档**节点 + 收敛 + 容器内 `count=0`，幂等，可重复跑）。
3. **A13 agent**：`TestDockerAgentVersionUpgrade`（前置：远端 staged `agent-new` = 0.1.1 `a28ca86…`，镜像 agent 仍 0.1.0）。
4. **A13 core**：`TestDockerCoreVersionSwitch`（同一 0.1.1 下 26.3.27→26.3.23→26.3.27）。
5. **A07**：`TestDockerConfigSafety`（Deferred 缺陷已在源码修复，应 180s 内自然收敛）。
6. **A14**：按灾备演练脚本继续。

旧顺序里的“先重建镜像再跑 A02”已完成：A02 于 2026-10-21 真实通过（§12.3）。

### 10.6 需父上传/同步

- 重新上传 `deploy/docker-lifecycle.sh → /opt/proxypanel-acceptance-20261021/lifecycle.sh`（`listeners` 动作；**本轮再加固**：任一内核表都不可读 → exit 71，不再静默 `count=0`；`core-switch`/`core-rollback` 的 label 检查已修）。
- 其余文件（`tests/e2e/docker_lifecycle_test.go`、本文件）随工作区生效，无需上传。
- 本轮（§12：归档式释放 + fixture 可重复运行）**没有再改** `deploy/docker-lifecycle.sh`：父的 Recover 日志里已出现 `listeners container=agent1 port=20105 count=` 输出，说明远端脚本已含 `listeners` 动作，无需再次上传。

## 11. 本轮三处测试/脚本定点修正（2026-10-21 三次修正）

### 11.1 A02 重试后的撤权复核（`TestDockerBatchPartialFailure`）

- 旧代码在 `POST /api/v1/admin/tasks/{id}/retry` 之后调用 `d.proxyFor("docker-alice", 20201)` 重新登录并导出订阅来复核“已撤权仍被拒”。该成员此时 `active=false`，登录/导出必然 403，用例会以无关原因红。
- 现在**复用撤权前建立的 `node2` 客户端与旧凭据**（同一份已导出 outbound、同一 TCP 目标），只断言 `if node2.ok() { t.Fatalf(...) }`：投递成功即“retry 复活了撤权”，失败即“旧凭据仍被拒”。不再新授权，也不再依赖面板登录。

### 11.2 `lcQueueDurability` 的顺序修正

- 旧代码在 `d.remote("stop postgres")` **之后**才 `proxyFor(...)`：登录与订阅导出都依赖 PostgreSQL，停库后必然失败；且 `node1.ok()/node2.ok()` 的返回值被丢弃，探针失败也照样往下走（可能 0 字节却“通过”）。
- 现在两个节点的客户端由调用方在停库**之前**构建并传入（`lcQueueDurability(t, d, f, switchAction, node1, node2)`），停库后**不再**抓任何面板配置；3 次探针逐个断言成功，被拒立即失败。

### 11.3 队列总账：逐批 id/hash/bytes + 屏障 + 窗口集合

- 新增纯函数 `lcBatchIDs/lcSameBatch/lcSameBatches/lcCarried`（供稳态屏障与断言复用）与 `lcWaitEmpty/lcWaitStable`（连续两次快照，间隔 > 5s 采样周期）。
- 新增 `lcBatchesSince(agentID, since)`：读窗口内 `usage_batches.batch_id` 列表，用于“集合外批次”判据。
- `parseAgentQueue` 不再只看 `len(SHA)==64`：按 `samples ORDER BY seq`（毫秒→UTC，与 `internal/agent/state` 一致）重建 `contracts.UsageBatch`，用**与面板同一个** `usagecalc.PayloadSHA256` 算摘要，再与 PG 存的 `payload_sha256` 逐批比较；`batches.sample_count` 与样本行数不符直接报错。
- 断言链：切换前稳态快照 ⊆ 切换后最终快照（字段不变）→ 排空后逐批 `Rows==1`、`SHA/Samples/Bytes/MemberBytes` 全等 → 窗口内收到的 `batch_id` 集合 == 最终快照集合 → 该 Agent 批次总行数 == 基线 + 快照数 → 该成员账本增量 == 快照内成员字节之和。**没有**退化成“账本单调不减”，也没有“缺失批次视为仍在队列”的旁路。

### 11.4 计量来源（如实说明，无第二来源）

- 字节数只有**一个**来源：Xray 的 per-user 统计计数器 `user>>>email>>>traffic>>>uplink|downlink`，由 Agent 每 5s（`stats_interval_seconds`）采样，按 `(node_id, core_generation, counter)` 与 `baseline` 表做差分；负增量（同代次内核心重启）按“从零重算”处理。
- 每次 `Measure` 把**同一节点同一核心代次**下所有 `delta > 0` 的成员写成**一个批次**，每个成员一行 `samples`（`upload`/`download` 两列），批次字节 = 各成员样本 `upload+download` 之和；面板侧 `usage_entries` 由 `usagecalc.Allocate` 按账期切分，**总和不变**。
- 因此 `lcQueueDurability` 比较的 `Bytes/MemberBytes` 是“样本字节之和”，**不是**从 payload 大小或 HTTP 流量推算出来的，也没有第二个计量来源可对账（这是既有实现的客观限制，不虚构）。

### 11.5 本轮离线验证（无 Docker / 无 SSH）

| 检查 | 结果 |
| --- | --- |
| `gofmt -l tests/e2e`（空）、`go vet ./tests/e2e`、`go build ./...` | PASS |
| `go test ./tests/e2e -run 'TestLifecycleParsing|TestAgentQueueReconstruction' -count=1 -v` | PASS |
| `TestAgentQueueReconstruction`：乱序 `seq` 仍按 seq 重建、SHA == 上传器口径、反转顺序 SHA 不同、`memberID` 过滤（`MemberBytes=300` vs `0`）、header 行数不符报错、库文件缺失报错、`lcSameBatches` 忽略 `attempts` 但不忽略字节/SHA、`lcCarried` 报出消失或改写的批次 | PASS |
| `deploy/docker-lifecycle.sh` 内层探测（**取文件中原样 awk 行**，仅把表列表替换为测试注入）：两表都不可读 → exit 1；一表可读一表缺失 → `1`；仅 ESTABLISHED → `0`；tcp6 LISTEN → `1`；awk 非零退出 → exit 1；awk 输出非数字 → exit 1 | PASS |
| `bash -n deploy/docker-lifecycle.sh` | PASS |
| `go test ./tests/e2e -run 'TestDockerBatchPartialFailure|TestDockerConfigSafety|TestDockerAgentVersionUpgrade|TestDockerCoreVersionSwitch|TestDockerLifecycleDiagnose|TestDockerLifecycleRecover'`（无环境） | 全部 SKIP（预期） |

### 11.6 未实测（需父在本机 Docker 上执行）

- `lcQueueDurability` 全链（停库 → 真实流量 → 稳态屏障 → `agent-upgrade` → 恢复 → 逐批 SHA/字节对账）**只在离线层面验证了读取器与断言逻辑**；真实停机时序（采样 5s、上传退避、切换耗时）下的稳态屏障与“窗口内无集合外批次”仍待父实跑确认。
- `listeners` 加固后的**真实容器**行为（`exit 71` 分支）无法在本会话触发；父已有的真实探针证据（`20105=1`、`20101=1`）只覆盖正常分支。

## 12. 本轮（三次修正之后）定点修正：**归档才释放** + fixture 可重复运行（2026-10-21）

### 12.1 实测缺陷：`enabled=false` 不释放 20105（不是产品 bug）

父在真实栈上执行 `TestDockerLifecycleRecover`，日志 `docker-lifecycle-recover-2.log`：重启后两个 Agent 都已 `applied=desired`（91/91 收敛），但 `node.enabled=false` 之后容器内探测仍是 `20105 LISTEN=1`。

只读取证（未改产品语义，DS explorer 427 亦确认这不是产品缺陷）：

- `internal/panel/store/snapshot*.go` 的 `BuildSnapshot` 对**所有非 archived 节点**生成实例；`enabled=false` 只是让该节点的 clients 为空。
- 空用户的核心**依然允许监听**（这是核心的正常行为），`supervisor` 的 manager 只停止“快照里已不存在的实例”。
- 因此“停用节点”永远无法释放端口，**只有归档（`DELETE /api/v1/admin/nodes/{id}` → `Store.DeleteNode` → `archived=true`）** 会让节点离开快照、移除实例、关闭监听。

### 12.2 用例侧修正（`tests/e2e/docker_lifecycle_test.go`）

- `lcArchiveNode(t, d, nodeID)`：先读 `nodes.archived`；已归档则**什么都不做**（幂等，不重复推进 revision），未归档才 `DELETE /api/v1/admin/nodes/{id}`，随后回读断言 `archived=true`、`enabled=false`。归档走 `Store.DeleteNode`→`UpdateNode`（`internal/panel/store/resources.go:569-576`），节点行、usage 历史、审计与账本全部保留。
- `lcQuiesce`（cleanup）：两个成员 `active=false` → `lcArchiveNode` → 30s 收敛（必要时重启自有 Agent 清退避）→ 回读断言已归档 → 容器内 `listeners agent1 20105` 必须 `count=0`（硬判据）。重复运行不会二次归档、不新增 fixture 对象。
- `TestDockerLifecycleRecover`：同样改为“成员停用 + 归档”，`archived=true` 与容器内 `count=0` 都是硬判据（面板侧的 `core_instances.state` 只作 note：面板不会把被归档节点的实例标成 stopped）；**幂等**：已有归档节点不再 PATCH、不再二次归档。
- `lifecycleFixture` 支持重复运行：fixture 记录的节点已归档（或已不存在）时，用**唯一名称**（`docker-lifecycle-<unixnano>`）新建节点（名称唯一索引 `nodes_name_key` 覆盖归档行，`migrations/0001_init.sql:64`），再通过真实 `/api/v1/admin/batches/preview` + `/batches` 执行 `update_node_group`，把组关系恢复成 `[共享 20101 节点, 新节点]`（组被订阅引用时直接 `PATCH /node-groups` 会 409 `PREVIEW_REQUIRED`，见 `internal/panel/api/admin2.go:69-79`），写回 `lifecycle-fixture.json`；组、订阅与两个固定用户名成员全部复用，`proxyFor` 硬编码的用户名不受影响。
- `lcPrepare`：节点未归档才 `PATCH enabled=true` + 有效文档；`applied()` 之后用 `lcWaitListening('agent1', 20105)` 有界等待容器内**真的出现监听**（最多 90s，探针错误也计入预算并在超时时随 `lastErr` 报出），避免客户端探针抢新节点 core 的启动窗口。**不**再等 `core_instances.state='running'`：该列由 Agent 回执写入 `applied/unchanged/rollback_applied/deferred`（`internal/panel/store/ops.go:350-370` 只在回执里置 running），并不代表存活进程，等它只会必然超时（`docker-lifecycle-recover-2.log`）。监听只证明核心已起，鉴权仍必须由真实 `proxy.ok()` 往返证明。
- `TestDockerLifecycleDiagnose` 仍**完全只读**：只多打印节点 `archived` 标志。
- `lcLogSafeState`：`agent_receipts` 没有 `created_at`（`migrations/0001_init.sql:415` 只有 `received_at`/`applied_at`），查询改为 `order by received_at desc`；仍只取 `state` 与截断后的错误文本，**不输出 payload/配置/token**。

### 12.3 A02 真实通过（父执行）

`TestDockerBatchPartialFailure`：72.98s 通过，证据 `docker-batch-retry-1.log`——在线撤权 0.342s、同一 `task_id` retry 成功、原批次无 pending/failed/timeout。生产 `RetryTask` 修复与三个定点回归见 §4.1（离线全绿，真实 PG 由父执行）。

### 12.4 状态更新

- Agent 退避缺陷（§9.2）：**源码已修复**，父 `-race` 回归通过；本文件不再要求等待。
- 最新 staged Agent 0.1.1 SHA256 = `a28ca86562686f4b66c856e69253cdc6202461f53f1c2cd1c38358f76fabd66a`（旧的 `7eaebb96…` 作废）；staged core 26.3.23 仍是 `05205489…`。
- **远端仍未升级**：镜像内 Agent 仍为 0.1.0（`90ee1d0c…`），因此 A13（`TestDockerAgentVersionUpgrade` / `TestDockerCoreVersionSwitch`）尚未真实执行；执行前必须让父确认远端 staged `agent-new` 换成上面这个 SHA。
- **（已被 §16 取代）** 收尾轮已把镜像内两个 Agent 正式提升为 0.1.1（`a28ca865…`，`docker-current-agents-deploy.log`），A13 两个用例均真实整项通过；上面“远端仍未升级”只是当时状态。

### 12.5 本轮离线验证（无 Docker / 无 SSH）

| 检查 | 结果 |
| --- | --- |
| `gofmt -l tests/e2e`（空）、`go vet ./tests/e2e`、`go build ./...` | PASS |
| `go test ./tests/e2e -run 'TestLifecycleParsing|TestAgentQueueReconstruction' -count=1 -v` | PASS |
| 所有 Docker 用例（含 Diagnose/Recover）无环境时 SKIP | PASS（预期） |

### 12.6 父的执行入口（按顺序，串行、不能与其他 Docker 用例并发）

```powershell
. .\scripts\Use-DevEnvironment.ps1
$env:PANEL_DOCKER_TEST='1'; $env:PANEL_DOCKER_ROOT='E:\Work\proxypanel'
# 1) 只读定位（不创建、不修改任何对象）
go test -v ./tests/e2e -run 'TestDockerLifecycleDiagnose' -count=1 -timeout 10m
# 2) 释放 20105：成员停用 + 归档节点 + 收敛 + 容器内 count=0（幂等，可重复跑）
go test -v ./tests/e2e -run 'TestDockerLifecycleRecover'   -count=1 -timeout 15m
# 3) A13（前置：远端 staged agent 为 0.1.1 a28ca86…，镜像 agent 仍 0.1.0）
go test -v ./tests/e2e -run 'TestDockerAgentVersionUpgrade' -count=1 -timeout 25m
go test -v ./tests/e2e -run 'TestDockerCoreVersionSwitch'   -count=1 -timeout 25m
```

需上传的文件：**无**。本轮未修改 `deploy/docker-lifecycle.sh`（`listeners` 版本已在远端，父的 Recover 日志里已有 `listeners container=agent1 port=20105 count=` 输出）；`tests/e2e/docker_lifecycle_test.go` 与本文件随工作区生效。**（已被 §13 取代：A13 首次真跑暴露出脚本自身的缺陷，必须重传 `deploy/docker-lifecycle.sh`，见 §13.5。）**

附带效果：`TestDockerLifecycleRecover` 归档旧 fixture 节点后，A13 的两个用例会在 `lcPrepare` 里自动重建一个新节点（唯一名称、复用原组/订阅/成员），无需父手工清理面板。

## 13. A13 首次真跑失败的定点修复：脚本 helper 覆盖了调用方参数（2026-10-21）

### 13.1 实测证据与根因（源码级，不是猜测）

父第一次真跑 A13 的**前半段是真实通过的**（`$env:PI_SCRATCH_DIR/docker-agent-upgrade-1.log`）：

- 镜像 0.1.0 → 暂存 0.1.1 **真实替换并运行**，Agent 身份/队列未变（`sameAgent1 116/116`）；
- 2 个批次共 `420558` 字节，逐批 id / `payload_sha256` / `memberbytes` / 集合 / 精确 delta 全部 PASS；
- 紧接着 `mount-check agent1v2` 失败：`exit 52 Refusing: the lifecycle container is missing: ppaccept-20261021-agent1v2`，而实际被创建的是 `ppaccept-20261021-agent1core`。

根因在 `deploy/docker-lifecycle.sh`（POSIX `sh`，函数**没有**局部作用域）：

| 位置 | 缺陷 |
| --- | --- |
| `assert_exclusive` | 用**全局**循环变量 `name`：`for name in "$prefix-agent1" "$upgraded" "$coretest"` |
| `run_lifecycle_agent` | 先 `name=$1`，再 `assert_exclusive "$name"`；返回时 `name` 已被覆盖成循环最后一个值 `$coretest`（`ppaccept-20261021-agent1core`） |
| 结果 | `docker run -d --name "$name"` 创建了 **agent1core**；函数内的 `mount_check "$name"` 检查的也是 agent1core（能通过），错误因此延后到调用方的 `mount-check agent1v2`（exit 52）才暴露 |

`core-switch` 只是**碰巧**正确：它的目标正好是循环最后一个名字 `$coretest`，同一个缺陷在它身上不可见——所以两个动作都必须被回归锁住。

### 13.2 修复（只改 `deploy/docker-lifecycle.sh`）

- 每个 helper 改用**自有前缀**变量，绝不写 action 的 `name` / `target` / `port`：`require_stage`→`rs_file`、`drop_lifecycle`→`dl_name`、`assert_exclusive`→`ex_target`/`ex_name`、`mount_check`→`mc_name`/`mc_binds`、`run_lifecycle_agent`→`lla_name`/`lla_agent`/`lla_config`。文件头把这条规则写成注释，避免下一个 helper 再犯。
- `run_lifecycle_agent` 在 `docker run -d` 之后**回读** `{{.Name}}`，必须等于 `/<请求名>`，否则 `exit 72 Refusing: the started container is not the one the action asked for`：这类缺陷从此在原地报错，不再延后一个动作。
- 所有权检查（root marker、label、network、volume、binds、limits）与退出码 40–71 **全部不变**，唯一新增退出码是 72。

### 13.3 无 Docker 回归（持久、可重复、不需要 SSH）

`deploy/docker-lifecycle-selftest.sh`：把 stub `docker` 放到 PATH 首位，用临时目录顶替 `/opt/proxypanel-acceptance-20261021`（只重定向脚本里那一行 `root=`，其余原样执行），不碰 Docker、SSH 或 `.local/`：

| 场景 | 断言 |
| --- | --- |
| `agent-upgrade` | 只起一个容器且必须是 `ppaccept-20261021-agent1v2`；`agent1core` 从未创建；state 卷 / `/s:ro` / `--entrypoint /s/agent-new` / 自有网络 / 192m / 0.5CPU / 128PID 全部沿用；`mount-check agent1v2` 通过、`mount-check agent1core` 仍 `exit 52` |
| `core-switch` | 只起 `ppaccept-20261021-agent1core`；`agent1v2` 是"停止不删除"的回退目标（`stops.log` 有、`rms.log` 无）；派生配置含 `"xray_path": "/s/xray-core"` |
| 排他拒绝 | 模拟 `docker stop` 未生效（stub 返回 0 但容器仍 running）时，`agent-upgrade` 必须 `exit 51`，且**在创建任何容器之前** |
| 前置缺失 | 没有 `agent1v2` 时 `core-switch` 仍 `exit 60` |

运行入口（Windows 用 Git bash，**不要**用 WSL 的 `C:\Windows\System32\bash.exe`）：

```powershell
& 'E:\SDK\Git\bin\bash.exe' deploy/docker-lifecycle-selftest.sh   # 期望：38 项 ok，末行 all checks passed
```

Go 入口 `TestLifecycleScriptStub`（`tests/e2e/docker_lifecycle_test.go`）**不依赖** `PANEL_DOCKER_TEST`：找到 POSIX shell 就跑，找不到（或只找到 WSL 启动器）就**显式 SKIP**；`PANEL_BASH` 可指定 shell：

```powershell
$env:PANEL_BASH='E:\SDK\Git\bin\bash.exe'
go test ./tests/e2e -run 'TestLifecycleScriptStub' -count=1 -v
```

### 13.4 本轮离线验证（无 Docker / 无 SSH）

| 检查 | 结果 |
| --- | --- |
| `bash -n deploy/docker-lifecycle.sh`（修复后） | PASS |
| `E:\SDK\Git\bin\bash.exe deploy/docker-lifecycle-selftest.sh` | PASS（38 项 ok，末行 `all checks passed`） |
| 同一 selftest 对**修复前变体**（helper 变量改回 `name`） | 16 项 FAIL，其中 `mount-check agent1v2 ... (got 52: Refusing: ppaccept-20261021-agent1v2 is missing)`，与父的真实失败同形 → 回归确实咬得住，不是自说自话 |
| `gofmt -l tests/e2e`（空）、`go vet ./tests/e2e`、`go build ./...` | PASS |
| `go test ./tests/e2e -run 'TestLifecycleScriptStub|TestLifecycleParsing|TestAgentQueueReconstruction' -count=1` | PASS（`PANEL_BASH` 指向 Git bash） |

### 13.5 需父上传 / 需父实跑

**必须重传脚本本身**（修的就是远端正在执行的那个文件），staged 二进制不用换（§15 起上传集扩为五个文件，见 §15.4）：

```powershell
$root='E:\Work\proxypanel'; $remote='/opt/proxypanel-acceptance-20261021'
$args=@('-i',"$root\.local\database-ssh-key",'-o','BatchMode=yes','-o','StrictHostKeyChecking=yes',
        '-o',"UserKnownHostsFile=$root\.local\database-known-hosts",'-o','ConnectTimeout=10')
scp @args "$root\deploy\docker-lifecycle.sh" "root@192.168.5.203:$remote/lifecycle.sh"
ssh @args root@192.168.5.203 "sh $remote/lifecycle.sh baseline"   # 确认仍是镜像 0.1.0 / staged 0.1.1 + core 26.3.27 / 26.3.23
```

然后重跑 A13（串行，不能与其他 Docker 用例并发；**前半段已通过不等于整例通过**）：

```powershell
. .\scripts\Use-DevEnvironment.ps1
$env:PANEL_DOCKER_TEST='1'; $env:PANEL_DOCKER_ROOT='E:\Work\proxypanel'
go test -v ./tests/e2e -run 'TestDockerAgentVersionUpgrade' -count=1 -timeout 25m
go test -v ./tests/e2e -run 'TestDockerCoreVersionSwitch'   -count=1 -timeout 25m
```

`cleanup` 幂等：无论上一个用例停在 `agent-upgrade`、`core-switch` 还是 `core-rollback`，父都可以先 `sh $remote/lifecycle.sh cleanup` 回到镜像 0.1.0，再重跑。

### 13.6 状态更新（2026-10-21，本节修复之后）

- §13 的脚本修复已在真实栈上生效：父重传后 `TestDockerAgentVersionUpgrade` **整项 PASS 93.21s**（`$env:PI_SCRATCH_DIR/docker-agent-upgrade-2.log`：0.1.0→0.1.1→0.1.0、同 identity/gen/state 卷、4 批 420558 字节逐批一次入账、归档 cleanup 通过）。
- 同一次真跑里 `TestDockerCoreVersionSwitch` 在**测试侧时序**上失败（FAIL 22.61s，`docker-core-switch-1.log`，根因见 §14），与脚本无关；修正后的用例尚未在真实 Docker 上重跑。
- 新增的 `exit 72` 分支在真实栈上仍未被触发（stub 只覆盖正常分支与 51/52/60）。

## 14. A13 第二次真跑失败的定点修复：切换步骤接受了旧心跳（2026-10-21）

### 14.1 实测证据与根因（测试侧，不是产品/脚本缺陷）

- `docker-agent-upgrade-2.log`：`TestDockerAgentVersionUpgrade` **整项 PASS 93.21s**（`0.1.0 → 0.1.1 → 0.1.0`，同一个 state 卷；4 个批次共 `420558` 字节逐批一次入账；归档 cleanup 通过）。
- 紧接着 `docker-core-switch-1.log`：`TestDockerCoreVersionSwitch` **FAIL 22.61s**，唯一一行阶段输出是
  `core 26.3.27 under the new agent: agent=agt_f3r2... version=0.1.0 core="26.3.27" applied=146/146`，
  随后 `the second node of the same Agent lost service during core 26.3.27 under the new agent`；日志里**没有** `core-switch` 动作输出，cleanup 已把镜像 0.1.0 拉回并归档 20105。

| 位置 | 缺陷 |
| --- | --- |
| 旧 `lcVersionLadder` | 一个步骤只比较**一个**字段（`strings.Contains(value, expected)`）；`xray_version` 步骤传 `26.3.27` |
| 第 1 步 | `agent-upgrade` 保持镜像核心，所以**旧 0.1.0 心跳里的 core 已经是 26.3.27** → 等待立刻返回；`version=0.1.0`、`applied=146/146` 全是旧进程的观察 |
| 探针 | 旧代码先 `waitFor(node2.ok())`、再单独 `if !node1.ok()`：Agent 容器正在被替换，20105 的核心已起、20101 的还没起，单次瞬时探测把"切换中"判成失败 |

结论：不是产品 bug，也不是 `deploy/docker-lifecycle.sh` 的 bug；是用例把**不变的字段字符串**当成了切换屏障。

### 14.2 修复（只改 `tests/e2e/docker_lifecycle_test.go`）

- 新增 `lcExpect{AgentVersion, CoreVersion, After}` 与纯函数 `lcPhaseReached(row, want) error`：**同一次观察**必须同时满足 Agent 版本精确匹配、core 版本精确匹配、`last_heartbeat_at` **严格新于**动作前快照、`applied==desired>0`。`strings.Contains` 判据删除（`126.3.27` 不再能冒充 `26.3.27`）。
- 快照语义：每一步都在**动作前立刻**读一次 `agents` 行；`lcQueueDurability` 返回它在 `d.lifecycle(switchAction)` 之前读到的行，`agent-rollback` / `core-switch` / `core-rollback` / `cleanup` 各自重新读，不再复用最早 baseline。
- 心跳字段可信：`version`、`xray_version`、`last_heartbeat_at` 由 `internal/panel/store/ops.go:252-254` 的**同一条 UPDATE** 写入，因此一次行读取要么整体早于切换、要么整体来自新进程。
- 探测改为 `lcWaitBothServe`（纯判据 `lcBothServe`）：**同一轮探测**里两个节点都必须成功，最多等 180s；单节点成功不再放行。
- 保持不变：identity 与 `control_generation` 不变、撤权凭据始终拒绝、账本单调不减、outbox 迁移与逐批 SHA/字节/精确 delta 断言原样。

### 14.3 无 Docker 回归

`TestLifecycleSwitchGate`（纯函数，不需要 Docker/SSH/`PANEL_DOCKER_TEST`）：11 个"不得放行"的负例（等于快照的心跳、更旧心跳、旧 0.1.0、旧 core 26.3.23、`126.3.27`、无心跳、未收敛、`applied=0`、缺动作前快照、缺预期 Agent 版本、缺预期 core）+ 4 种双节点组合（"只有第一个/只有第二个节点成功"必须拒绝并指出缺的是哪个端口）。

### 14.4 本轮离线验证（无 Docker / 无 SSH）

| 检查 | 结果 |
| --- | --- |
| `gofmt -l tests/e2e` | 空 |
| `go vet ./tests/e2e` | PASS |
| `go build ./...` | PASS |
| `go test ./... -count=1` | PASS |
| `go test ./tests/e2e -run 'TestLifecycleSwitchGate|TestLifecycleParsing|TestLifecycleScriptStub|TestAgentQueueReconstruction' -count=1`（`PANEL_BASH`=Git bash） | PASS |

### 14.5 父的执行入口（本轮**无需上传任何文件**）

只改了工作区的测试文件，远端 `lifecycle.sh` 与 staged 二进制都不用重传。串行执行（不能与其他 Docker 用例并发）：

```powershell
. .\scripts\Use-DevEnvironment.ps1
$env:PANEL_DOCKER_TEST='1'; $env:PANEL_DOCKER_ROOT='E:\Work\proxypanel'
go test -v ./tests/e2e -run 'TestDockerCoreVersionSwitch' -count=1 -timeout 25m
```

`TestDockerAgentVersionUpgrade` 已真实 PASS（93.21s），可作回归再跑，但不是本轮阻塞项。若上一次跑停在中间，先 `sh $remote/lifecycle.sh cleanup`（幂等）再跑。

> 该“无需上传”只对 §14 的**测试文件**修正成立；§15 之后 staged 上传集本身必须换成五个文件（含两个 geo 资产）并重传 `deploy/docker-lifecycle.sh`。

### 14.6 仍未验证（如实）
> **本节列出的“尚未执行”已于收尾轮真实完成**：修正后的 `TestDockerCoreVersionSwitch` 在真实 Docker 上 **PASS 41.81s**（`docker-core-switch-3.log`，见 §16.1），含 SQLite 身份/队列/账本与已撤权凭据的逐步断言。原文保留，作为该次失败定位的历史。

- 修正后的 `TestDockerCoreVersionSwitch` **尚未在真实 Docker 上执行**；本轮只证明判据在离线负例下咬得住、代码可编译、全仓测试通过。
- 步骤 1 的屏障依赖新 Agent 的心跳（`heartbeat_interval_seconds: 2`）；若真实栈上新 Agent 心跳长期不来，用例会在 240s 超时并打印 `lcLogSafeState`（其中含 `heartbeat_age`），是失败而非跳过。
- 双节点同轮探测最多等 180s：Agent 容器替换期间允许短暂不可达（切换不承诺零停机），但**不允许**单节点成功就放行。
- `core-switch` / `core-rollback` 之后的 SQLite 身份/队列/账本与已撤权权限仍需真实重跑确认（断言保持原样，但未执行）。

## 15. A13 第三次真跑失败的定点修复：staged core 缺自身 geo 资产（2026-10-21）

### 15.1 实测证据与根因（上传集/脚本，不是核心兼容性）

- `docker-core-switch-2.log`：`TestDockerCoreVersionSwitch` 第二次真实执行 **FAIL 208.65s**。新 Agent `0.1.1` + staged core `26.3.23` 的心跳/版本/收敛都对，但**两个节点都无流量**。
- 父用受保护动作单独复现（`docker-core-23-diagnostic.log`，随后自动 cleanup），最后明确错误：
  `failed to load GeoIP: private: open /s/geoip.dat: no such file or directory`，且 `listeners container=agent1core port=20101 count=0`。
- 根因：Xray 在**可执行文件所在目录**解析 geo 资产；`core-switch` 的 `xray_path=/s/xray-core`，而上一轮上传集只有 `agent-new` / `xray-core` / `SHA256SUMS`，`/s` 下没有 `geoip.dat` / `geosite.dat`。这不是 26.3.23 不兼容（§3 的白名单前真实核心检查仍然有效），也不放宽 routing 规避，更不改官方 binary 或产品代码。

### 15.2 修复（三处，无 Agent 源码变更；`stageAgentSHA` 仍为 `a28ca865…`）

1. `scripts/Prepare-DockerLifecycle.ps1`：把 `E:\SDK\Xray\v26.3.23-linux-amd64` 中由**已按官方 `.dgst` 校验**的 `Xray-linux-64.zip` 解出的 `geoip.dat` / `geosite.dat` 复制进 `lifecycle-upload`；复制前用 zip 中央目录核对**条目长度**，别的发布的残留文件混不进来。`SHA256SUMS` 变四行（agent-new / xray-core / geoip.dat / geosite.dat，LF），manifest 同时列出两者 SHA256 与字节数。
2. `deploy/docker-lifecycle.sh`：`require_stage` 要求四个文件**存在且非空**，并要求 `SHA256SUMS` **逐条列出**它们（宿主机 `grep`，旧的三行 manifest 直接 exit 50）；新增 `verify_stage`（只读挂载 `/s`、无网络、cap-drop 的一次性容器里 `sha256sum -c SHA256SUMS`，不一致 exit 73）。`stage` 动作先置权限（geo 0644）再 `verify_stage`；`core-switch` 在 `docker stop agent1v2` **之前**先 `verify_stage` —— 缺件/坏件时正在服务的 Agent 不会被停。
3. `deploy/docker-lifecycle-selftest.sh`：持久 stub 回归从 4 个场景扩到 7 个（新增：旧三行 manifest 在停 agent1 前 exit 50；缺 `geoip.dat` 在停 agent1v2 前 exit 50；哈希不符在停 agent1v2 前 exit 73 且不创建容器）。`run_lifecycle_agent` 的挂载/入口断言保持原样。

镜像 `image core 26.3.27` 继续用 `/app` 内它自己的官方资产：`E:\SDK\Xray\v26.3.27-linux-amd64` 的 geo 文件字节数（19768301 / 10491954）与 26.3.23（19701033 / 10454877）**不同**，两套资产互不替代。

### 15.3 本轮离线验证（无 Docker / 无 SSH / 未下载任何其他版本）

| 检查 | 结果 |
| --- | --- |
| `bash -n deploy/docker-lifecycle.sh`、`bash -n deploy/docker-lifecycle-selftest.sh`（显式 `E:\SDK\Git\bin\bash.exe`） | 0 |
| `sh deploy/docker-lifecycle-selftest.sh` | **all checks passed**（55 项 ok / 0 FAIL，含上述 3 个新拒绝场景） |
| `.\scripts\Prepare-DockerLifecycle.ps1 -SkipDownload -SkipBuild` | 退出 0；`official geo assets (26.3.23): geoip.dat=19701033 geosite.dat=10454877`；`lifecycle-upload` 现为 5 个上传件 + manifest |
| `sha256sum -c SHA256SUMS`（对 upload 目录，本地 Git bash） | 四行全 `OK` |
| `gofmt -l tests/e2e` / `go vet ./tests/e2e` / `go build ./...` / `go test ./... -count=1` | 空 / PASS / PASS / PASS |
| `go test ./tests/e2e -run 'TestLifecycleScriptStub\|TestLifecycleSwitchGate\|TestLifecycleParsing\|TestAgentQueueReconstruction' -count=1`（`PANEL_BASH=E:\SDK\Git\bin\bash.exe`） | PASS（`TestLifecycleScriptStub` 3.57s） |

### 15.4 父的上传 / 执行（本轮必做：五个文件 + 脚本）

```powershell
. .\scripts\Use-DevEnvironment.ps1
# 1) 重新生成上传集：不下载、不重建，沿用已校验的官方 zip 与既有 agent-new（a28ca865…）
.\scripts\Prepare-DockerLifecycle.ps1 -SkipDownload -SkipBuild
# 2) 上传脚本 + 完整文件组（五个）
$root='E:\Work\proxypanel'; $remote='/opt/proxypanel-acceptance-20261021'
$u=Join-Path $env:PI_SCRATCH_DIR 'lifecycle-upload'
$args=@('-i',"$root\.local\database-ssh-key",'-o','BatchMode=yes','-o','StrictHostKeyChecking=yes',
        '-o',"UserKnownHostsFile=$root\.local\database-known-hosts",'-o','ConnectTimeout=10')
scp @args "$root\deploy\docker-lifecycle.sh" "root@192.168.5.203:$remote/lifecycle.sh"
scp @args (Join-Path $u 'agent-new') (Join-Path $u 'xray-core') (Join-Path $u 'geoip.dat') `
    (Join-Path $u 'geosite.dat') (Join-Path $u 'SHA256SUMS') "root@192.168.5.203:$remote/lifecycle/"
# 3) 先 stage（校验四行 SHA256SUMS 并置权限），再串行跑切核用例
ssh @args root@192.168.5.203 "sh $remote/lifecycle.sh stage"
$env:PANEL_DOCKER_TEST='1'; $env:PANEL_DOCKER_ROOT='E:\Work\proxypanel'
go test -v ./tests/e2e -run 'TestDockerCoreVersionSwitch' -count=1 -timeout 25m
```

若上一次停在中间，先 `sh $remote/lifecycle.sh cleanup`（幂等）再跑；A13 不能与其他 Docker 用例并发。

### 15.5 仍未验证（如实）
> **本节“尚未重跑”已由收尾轮真实结果取代**：补齐官方 geo 资产并重传五个上传件后，`TestDockerCoreVersionSwitch` **PASS 41.81s**（`docker-core-switch-3.log`，staged 四件容器内校验 OK，见 §16.1）。原文保留，作为“缺 geo 资产”根因的历史记录。

- 修复后的 `TestDockerCoreVersionSwitch` **尚未在真实 Docker 上重跑**；本轮只证明：上传集完整且哈希自洽、脚本对缺件/坏件在停止原 Agent 前拒绝、stub 回归与离线测试通过。
- 官方 `.dgst` 只覆盖 `Xray-linux-64.zip`，不提供 `geoip.dat` / `geosite.dat` 的逐文件摘要；来源证据 = 官方 zip 的 SHA2-256 与 `.dgst` 一致 + 解出文件与 zip 条目长度一致。未改官方 binary，未下载其他版本。
- 新 `core-switch` 多一次一次性容器开销（约 30MB 哈希，秒级）；这是“先验证后停机”的代价，不改产品行为。
- `core-switch` / `core-rollback` 之后的 SQLite 身份/队列/账本与已撤权权限、两节点真实连接，仍需父真实重跑确认（断言保持原样，但未执行）。

## 16. 收尾轮真实结果明细（2026-10-21，§1–§15 的最终口径）

执行方式：本机 `PANEL_BASH=E:\SDK\Git\bin\bash.exe`、`PANEL_DOCKER_TEST=1`、`PANEL_DOCKER_ROOT=E:\Work\proxypanel`；`scripts/Invoke-DockerAcceptance.ps1 -Run '^<ExactTest>$'` 在**同一次工具调用**里建立并关闭受保护的 SSH 隧道（只做回环端口转发），串行执行，不与其它 Docker 用例并发。证据只来自本项目脱敏日志，收尾轮未再发起新的远端排查。

### 16.1 A13 core 切换（`docker-core-switch-3.log`，PASS 41.81s）

| 阶段 | 同一次 `agents` 行读取 |
| --- | --- |
| 撤权 | 20105 上真实撤权 **0.810s** |
| `agent-upgrade` 后 | `version=0.1.1 core="26.3.27" applied=176/176 heartbeat=…T05:40:09.613Z` |
| `core-switch` 后 | `version=0.1.1 core="26.3.23" applied=176/176 heartbeat=…T05:40:17.256Z` |
| `core-rollback` 后 | `version=0.1.1 core="26.3.27" applied=176/176 heartbeat=…T05:40:22.186Z` |
| `cleanup` 后 | `version=0.1.0 core="26.3.27" applied=176/176 heartbeat=…T05:40:28.428Z` |
| 收尾 | 节点经 `DELETE /api/v1/admin/nodes/{id}` 归档（历史/审计/账本保留），`agent1` 在 20105 上 **无 LISTEN** |

每个阶段都由 `lcVersionLadder`（`tests/e2e/docker_lifecycle_test.go:1563`）判定：Agent 身份与 `control_generation` 不变、版本/core **精确**匹配、心跳严格新于动作前快照、`applied=desired>0`、outbox 有界排空、账本不回退、**已撤权凭据仍被拒**、**同一轮探测里两个节点都真实代理成功**（最多 180s；切换不承诺零停机）。staged 件：agent `a28ca865…`、core 26.3.23 `05205489…`、`geoip.dat` 19,701,033B、`geosite.dat` 10,454,877B（四行 `SHA256SUMS` 在 `stage` 时于容器内校验）。

### 16.2 镜像内 Agent 正式更新为 0.1.1（`docker-current-agents-deploy.log`）

- 新 image：`sha256:912aed513e608b833bf566850373e6c133f716f458e1861db7d949ebc39e32ed`（沿用 tag `proxypanel-acceptance:20261021`）。
- **后续更新**：该镜像随后被 cert-pin 修复镜像 `sha256:4e96ae4a…c564eb76d`（`/app/panel` `0595390…`）取代，Agent 0.1.1 与 core 26.3.27 未改；证据 `docker-panel-cert-pin-deploy.log`。
- 容器内实测：`proxypanel-agent 0.1.1`、`/app/panel` `4911fc3dcaec949fee193cf8f3c1ab0e32caaae3197cfcd481be68280fc70a12`、`/app/agent` `a28ca86562686f4b66c856e69253cdc6202461f53f1c2cd1c38358f76fabd66a`、`/app/xray` `8255dd939c34cf966cc91517b6324dd3c8d0bcf49ffac8beca049a38c46845ed`（**26.3.27 与 `/app` 自带 geo 资产未动**）。
- 只重建自有 `agent1`/`agent2` 两个容器并回读 `running=true`、版本与 SHA；**状态卷未删除**；`panel`/`postgres`/`redis`/`target` 未动。
- 旧 tag `proxypanel-acceptance:20261021-agent010-baseline`（`sha256:326d0b9f0f5c3c020807cebc1295c87f822aca95cbb3b1066d52a96613e4acca`，内置 Agent 0.1.0 `90ee1d0c…`）**保留**作来源记录，未删除。

### 16.3 当前 0.1.1 镜像下的回归

- A07 `TestDockerConfigSafety` **PASS 19.87s**（`docker-config-safety-2.log`）：撤权 1.054s → 无效文档由真实核心拒绝（`desired>applied`，从未成为已应用修订）→ 同 Agent 另一节点持续服务 → 技术回滚**未**恢复撤权 → 恢复有效文档后两节点服务、账本不回退。§9 的 Deferred 退避缺陷至此**实际闭环**，不再是“源码已修、远端未验”。
- 主链 `TestDockerAcceptance` **PASS 57.29s**（`docker-e2e-current-agent.log`）：真实 TLS 面板 + 两 Agent + 隧道 Xray TCP/UDP 与真实入账；在线 TCP 与 UDP 会话在撤权后 **1.830s** 内终止；面板停离线后按 **PostgreSQL 服务端时钟**自然到期：新连接 **0.733s** 被拒（`after_expiry`）、旧 UDP **0.938s**、旧 TCP **1.596s** 终止，均 < 60s；Agent 重启不复活撤权；容器崩溃自动重启恢复身份与转发；卸载/重建保留命名卷后两个 Agent 身份不变。末尾状态：六容器仍为 192 MiB / 0.5 CPU / 128 PID、`privileged=false`、端口只绑 `127.0.0.1`。
- 0.1.1 之前的 A02 `TestDockerBatchPartialFailure` **PASS 72.98s**（`docker-batch-retry-1.log`，见 §12.3）；面板 RetryTask 三个真实 PG `-race` 回归 **PASS 55.38s**（`retry-task-real-pg-race.log`）。
- 本地最终工程检查（`final-local-env.log`、`final-local-gotest-all.log`、`final-local-govet.log`、`final-local-agent-race.log`、`final-local-agent-xrayreal-race.log`、`final-local-corematrix-localcore.log`）：`go test ./...`、`go vet ./...`、Agent 全包 `-race`、真实 Xray Supervisor `-race`（24.811s）、`clientconfig` 本地真实核心矩阵（6 个协议组合 + 2 个凭据守卫）全部 **PASS**；`final-local-skip-audit.log` 记录 **39 个环境门控 SKIP**（PG/Redis/真实核心/Docker/浏览器），**不算集成成功**。

### 16.4 本轮明确未验收（不得写作通过）

- **十二项验收标准没有全部通过**。A15 客户端矩阵的最终真实运行 `docker-client-matrix-3.log` **PASS 6.57s**：5 协议 × 2 节点 × 3 客户端 = **30 格真实通过**，v2rayN **10 格 `not-run`**（实际 GUI 导入未执行），因此**不是 40/40**；历史 `docker-client-matrix-1.log`（`export "xray"` 422）与 `-2.log`（测试读取缺项反转）均已修复。未覆盖 WS/gRPC/REALITY 等传输。逐条状态见 [final-acceptance.md](final-acceptance.md)、矩阵细节见 [client-matrix.md](client-matrix.md)。
- v2rayN 7.25.6 仍无官方非 GUI 导入接口，只读观测持续为 `snapshot_actionable=false` / 空 `snapshot_id`，**实际 GUI 导入从未执行**。
- 发布产物已重建并通过两轮校验（每平台 **72 文件 / 71 条 SHA256**，两轮清单逐字节一致）；`build/` 中**确切的** Linux amd64/arm64 发布二进制只做交叉构建、清单与 ELF 校验，未在目标平台运行（Linux amd64 平台功能由 Docker 内 staged 构建验收，哈希与发布目录不同；arm64 架构整体未运行），校验不等同发布者签名；完整数据见 [final-acceptance.md](final-acceptance.md)。
- 外部 Telegram 实际送达、公网直连、宿主 systemd、主机重启：仍然未验收。A13 是**容器生命周期**，不等于宿主机服务管理或主机重启。
- 执行边界：一次 DS 脚本自测真实启动了 WSL（[execution-boundary-incident.md](execution-boundary-incident.md)），属**明确的执行边界不符合项**；后续改用 `PANEL_BASH=E:\SDK\Git\bin\bash.exe`、shell 选择器拒绝 System32 启动器以及原容器一致性证据，都**不能抵消**该事件。此后的测试只允许该绝对路径 shell，禁止 WSL 探测/启动/停止。远端操作只由授权操作者通过受保护 SSH 在归属本项目的资源上执行，不接触原业务容器/服务、不输出秘密；本轮远端验收已结束，不再进行远端操作。
