# A14 隔离恢复演练实测（独立目标还原，非生产灾备 SLA）

目标基线：[已批准目标](../../.pi/goal/完成计划剩余任务与-docker-ssh-隧道验收-20261021-1045.md)。A14 要求“从项目测试数据的一致备份还原到独立目标，报告实测恢复耗时、可恢复水位和不可恢复缺口，不覆盖现有数据库或声称零数据损失”。

本文件只记录**已实际发生**的证据。状态：`TestDockerRecovery` **PASS 120.64s**（`docker-recovery-2.log`，2026-10-21）。它**不是**生产 RPO/RTO 承诺，也**不是**零数据损失证明；十二项验收标准并未因此全部通过。

## 1. 演练对象与隔离边界

| 项 | 实际配置 |
| --- | --- |
| 源栈 | 本项目自有 `ppaccept-20261021-*`：postgres / redis / panel / agent1 / agent2 / target，专属网络 `ppaccept-20261021-net`、卷、标签，192 MiB / 0.5 CPU / 128 PID，无 privileged、无 host 网络、无 `docker.sock` |
| 还原目标 | **独立**容器 `ppaccept-20261021-restore`（自有卷，回环 `127.0.0.1:25433`）与 `recovery-panel`（`127.0.0.1:18444`）、`recovery-agent`；不动源 PostgreSQL（`25432`） |
| 参与协调的节点 | **只有 Agent1**：使用其**停止态完整副本**（含 SQLite 主库 + WAL，非运行中直接复制），恢复后**显式**接受新 generation |
| Agent2 | **原状态卷保留**，既未指向还原目标，也未被声称“已协调”；不参与本次恢复 |
| 传输/触发 | 父在本机经受保护 SSH 隧道（回环转发）执行；`PANEL_DOCKER_RECOVERY_TEST=1` 一次性显式开启，串行运行，不与其它 Docker 用例并发 |

未使用 WSL、未读 `.local` 私钥/凭据内容、未运行任何其他远端测试；证据只来自本项目的脱敏日志与父提取的白名单字段。

## 2. 实测数据（`TestDockerRecovery` 输出 + 脱敏证据）

| 指标 | 实测值 |
| --- | --- |
| 备份账本水位 | **46,722,583 B / 214 batches**（`pg_dump --format=custom`） |
| 备份时间 | 开始 `2026-10-21T05:44:42.3362281Z`，完成 `05:44:43.0094611Z`；采样水位 `05:44:39.355Z` |
| 可重放队列 | **70,093 B / 1 个离线批次**（保留在 Agent 停止态副本中，恢复后重放） |
| ACK 已删、不可恢复缺口 | **70,093 B / 1 batch**（Agent 已 ACK 并删除，备份中没有；**故意不从仍存活源导入**来伪造零缺口） |
| 计数器基线 | **124 baselines / 60 generations**（`baseline` 表按 core generation 计数，跨本地到期仍保留） |
| RPO（ACK 采样窗口） | **7.467 s** = 源端最后已确认采样水位 `05:44:46.822Z` − 备份采样水位 `05:44:39.355Z` |
| 备份年龄（还原时刻） | **75.5166 s ～ 76.1898 s**（dump 开始/结束括号给出的上下界） |
| 还原耗时 | **4.9013 s**（`pg_restore` 恢复到独立库） |
| 演练 RTO | **31.8674 s**（从发起还原到“恢复后 Agent 重启并验证完成”） |
| 恢复后账本 | **46,792,676 B**（= 备份账本 + 重放的 70,093 B；不代表无缺口，见 §4） |
| 重放采样水位 | `05:44:55.898Z`，与备份水位**不连续**；不能用它宣称“连续无缺口” |

## 3. 用例断言（`tests/e2e/docker_recovery_test.go`）

只有下列断言全部成立，`TestDockerRecovery` 才 PASS；日志只打印一行摘要，逐项断言在源码中：

- **恢复门禁**：独立还原库里原子进入 `recovery_mode`，控制代次更新为**新的随机正整数 generation**，且与源栈、Agent1 副本、Agent2 本地状态中的旧 generation **都不相同**（:327、:349-358）。
- **拒绝未显式接受的新代次**：停止态副本以旧 generation 启动后，面板收到 `coordination_required`、`PendingGeneration` 为新代次；在显式接受之前，Agent 不应用新授权（:432-481）。
- **显式协调**：执行 `accept-generation <exact>` 后，`agents` 表的 `applied/desired/control_generation` 与副本一致，`Pending/AcceptedGeneration` 清空，**不通过清空状态目录绕过门禁**（:484-504）。
- **账本精确一次**：重放批次在恢复库中按 `batch_id` 恰好一行，样本按 `seq` 重建后 SHA/字节/成员字节与 Agent 副本一致；不重复入账（:810 起）。
- **已撤销授权不复活**：恢复后快照 `ExpectedUserCount=0`，真实代理拒绝旧凭据（:424 及真实探针）。
- **重启**：恢复面板/Agent 重启后 generation/revision 保持，队列与账本不回退。
- **源状态未改**：演练结束后读取源栈 `control_state.control_generation` 与 `recovery_mode`，与演练前一致；源库未被覆盖（:522）。
- 结束时还原目标与恢复面板/Agent 停止，源六容器继续运行。

## 4. 缺口与 RPO/RTO 的准确含义（不得扩大）

- 备份是**静默快照式** `pg_dump`：真实 RPO 由 dump 开始/结束括号决定，实测备份年龄 **75.52–76.19 s**；ACK 采样窗口 **7.467 s** 是**已确认但未进备份**的账本缺口，属于可量化的记账损失。
- **ACK 已删除的 70,093 B / 1 batch 无法从 Agent 队列再造**：需要更新的 PG 备份、WAL/PITR 或独立持久记录；本演练**未**从存活源导入该数据。
- **未量化未采样的核心尾数**：核心计数器中尚未被采样/上报的部分没有独立来源，故不做“零丢失”或“全部可恢复”表述。
- **没有 WAL 归档/PITR**，没有异地加密备份，也没有生产存储/带宽下的 RPO 验证。
- RTO **31.87 s** 只是本次小数据量、同宿主、单节点副本的**演练值**，不是生产故障恢复 SLA；不含 DNS/TLS、审批、人工核对授权的时间预算。
- 恢复后账本字节变大只说明“队列被重放”，**不能**用来掩盖 §2 的 7.467 s 缺口与 70,093 B 不可恢复缺口。

## 5. 当前状态与保留资源（中间核对 + 终态对照）

`docker-post-recovery-state.txt` 是演练后立即取的**中间核对快照**：

- `recovery-panel` / `recovery-agent` / `restore` 均**已停止**（`restore` 仍占回环 `25433`，但已 exited）。
- 源栈六容器（postgres / redis / panel / agent1 / agent2 / target）**运行中**，`restarts=0`，资源上限不变。
- 宿主原有七个容器（5 个业务容器 + 2 个项目测试数据库）的 **ID / StartedAt / RestartCount 与 `docker-baseline.txt` 一致**（含 `axonhub-axonhub-1` 早于基线的 `restarts=1`）。
- 父已在最终交付前完成**结束状态对照**：`docker-original-final-comparison.txt` **PASS 7/7**（5 个原业务容器 + 2 个原隔离测试库，full ID/running/StartedAt/RestartCount，UTC `2026-10-21T05:59:37Z`）；自有资源终态见 `docker-owned-final-state.txt`（9 个自有容器、6 个命名卷、1 个 bridge、两个镜像 tag）。仍**不**把容器层一致扩大为原业务零影响，也不抵消 WSL 边界事件。

保留的证据与本项目自有资源：

- 第一次失败证据：远端 `backup/acceptance-first-attempt.dump`、`backup/recovery-first-agent1.db` **保留**，未删除、未被后续成功覆盖。
- 当前还原目标卷、恢复面板/Agent 卷与脱敏证据目录**保留**，供复核；源卷与 `backup/` 未被触碰。
- 脱敏白名单副本：scratch `recovery-evidence-summary.json`（仅字节数/水位/计时/状态字符串，无 token、凭据或配置原文）。`.local/docker-acceptance/recovery-evidence.json` 属私有，不得打印或提交。

## 6. 与操作手册的关系

本演练验证了 [docs/recovery.md](../../docs/recovery.md) 的操作顺序在**隔离 Docker 环境**中可执行：先进入恢复模式并生成新代次 → 对独立目标还原 → 拒绝未接受的新代次 → 显式 `accept-generation` → 重放队列并核对账本 → 重启验证 → 源栈状态未变。

它**没有**验证生产环境的备份策略、WAL/PITR、跨机还原、全部节点同时故障或更大数据量；这些仍列在 `docs/recovery.md` 的“当前不能宣称完成的内容”中。

逐条验收状态与 A01–A16 索引见 [final-acceptance.md](final-acceptance.md)。

## 7. 明确不宣称

- 不宣称零数据损失、连续无缺口、生产级 RPO/RTO 或灾备 SLA。
- 不宣称 Agent2 已协调、已还原或已接受新代次。
- 不宣称宿主 systemd、主机重启、公网直连或外部 Telegram 送达通过。
- 不把本文件当作十二项验收标准全部通过的证据。
