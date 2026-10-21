# Docker／SSH 隧道验收进度（2026-10-21 最终快照）

当前基线：[已批准目标](../../.pi/goal/完成计划剩余任务与-docker-ssh-隧道验收-20261021-1045.md)。它把 `192.168.5.203` 限定为**本项目独立 Docker 应用测试**，A13 = 容器生命周期；无公网开放、无 WSL、无宿主重启、无 Telegram 实际送达。

**这是进度快照，不是完成声明。** 逐条结论见 [final-acceptance.md](final-acceptance.md)：`#10`（A15 客户端矩阵）未完全满足（v2rayN 实际 GUI 导入未执行），`#12`（A16/交付边界）因一次真实的 WSL 启动而**非完全符合**；其余条目在调整后的范围内有真实证据。

## 1. 最新真实结果

| 项目 | 结果 | 证据 |
| --- | --- | --- |
| A13 `TestDockerAgentVersionUpgrade` | **PASS 93.21s**：镜像 0.1.0 → staged 0.1.1 → 回 0.1.0，同 identity/generation/state 卷，4 批 **420,558B** 逐批一次入账，撤权凭据始终拒绝，归档 cleanup 释放 20105 | `docker-agent-upgrade-2.log` |
| A13 `TestDockerCoreVersionSwitch` | **PASS 41.81s**：同一 staged Agent 0.1.1 下 core **26.3.27 → 26.3.23 → 26.3.27**，最后回镜像 Agent 0.1.0；每步版本精确、心跳更新、`applied=desired=176/176`、账本不回退、旧凭据仍拒、同一轮双节点真实代理成功；结束归档节点、20105 无 LISTEN | `docker-core-switch-3.log` |
| 面板 cert-pin 修复部署 | 生产 cert PEM 行数组 pin 派生修复后的面板已部署：`/app/panel` `059539071ba945bed6d7222607cd2d237b4bef1ab9b3f16c4412794bc9575bda`，镜像 `sha256:4e96ae4a51f01949ab7a7e747d84e5229b4d6589332e3032d185480c564eb76d`；Agent 0.1.1 `a28ca865…` 与 core 26.3.27 `8255dd93…` 未改 | `docker-panel-cert-pin-deploy.log` |
| A07 `TestDockerConfigSafety`（0.1.1） | **PASS 19.87s**：无效配置被真实核心拒绝、同 Agent 另一节点不受影响、技术回滚未复活撤权、有效配置恢复后两节点服务且账本完整；撤权 1.054s。Deferred 退避缺陷**实际闭环** | `docker-config-safety-2.log` |
| 主链 `TestDockerAcceptance`（当前 0.1.1） | **PASS 57.29s**：在线 TCP/UDP 撤权 **1.830s**；面板停离线后自然到期新连接 **0.733s** 拒、旧 UDP **0.938s**、旧 TCP **1.596s** 终止；Agent 重启不复活撤权；崩溃自动重启；卸载/重建保留卷身份不变 | `docker-e2e-current-agent.log` |
| A02 `TestDockerBatchPartialFailure` | **PASS 72.98s**：在线撤权 0.342s、部分失败可见、同一 `task_id` 重试收敛、旧凭据仍拒 | `docker-batch-retry-1.log` |
| A14 `TestDockerRecovery` | **PASS 120.64s**：独立目标还原、新随机 generation、显式 `accept-generation`、账本精确一次、旧授权不复活、源栈未改；RPO 窗口 **7.467s**、备份年龄 **75.5166–76.1898s**、还原 **4.9013s**、演练 RTO **31.8674s**、ACK 已删 **70,093B/1 batch 不可恢复** | `docker-recovery-2.log`、[docker-recovery.md](docker-recovery.md) |
| A15 `TestDockerClientMatrix`（round 3） | **PASS 6.57s**：VLESS / VMess / Trojan TLS / legacy SS(aes-128-gcm) / SS2022(blake3-aes-128-gcm) × 2 节点 × Xray 26.3.27 / sing-box 1.14.3 / Mihomo 1.19.32 = **30 格真实配置加载 + 69,632B 载荷**；v2rayN **10 格 `not-run`**（GUI 未导入）。**不是 40/40，也不是“全部客户端支持”** | `docker-client-matrix-3.log` |
| 宿主终态对照 | **PASS 7/7**：5 个原业务容器 + 2 个原隔离测试库的 full ID / running / StartedAt / RestartCount 完全一致（UTC `2026-10-21T05:59:37Z`） | `docker-original-final-comparison.txt` |
| 自有资源终态 | 9 个自有容器（6 源运行 + restore/recovery-panel/recovery-agent 停止）、6 个命名卷、1 个 bridge、current + old-baseline 两个 tag、源端口仅回环；无 lifecycle / 临时容量 / helper 容器；`backup/` 364K、`recovery/` 320K 保留 | `docker-owned-final-state.txt` |
| 三平台发布 | 两轮 `Build-Release.ps1` + `Test-Release.ps1` + `sha256sum --check --strict` 通过；每平台 **72 文件 / 71 条 SHA256**，两轮清单逐字节一致；Windows agent `0.1.1` | `release-current-summary.json/.md`、`release-current-verify-1/2.log`、`release-current-compare-rounds.log` |
| 本地最终工程检查 | `go test ./...`、`go vet ./...`、Agent 全包 `-race`、真实 Xray `-race`、cert-pin 受影响 4 包 `-race`、clientconfig 真实核心矩阵全部 PASS；前端 71/71、typecheck/build/audit 0；**39 个环境门控 SKIP 不算集成成功** | `final-local-*.log`、`final-cert-pin-*.log`、`final-web-*.log` |

## 2. 当前镜像 / 版本现状

- 当前验收镜像 `proxypanel-acceptance:20261021` = `sha256:4e96ae4a51f01949ab7a7e747d84e5229b4d6589332e3032d185480c564eb76d`；`/app/panel` `0595390…`、`/app/agent` `a28ca865…`、`/app/xray` `8255dd93…`（26.3.27 + `/app` 自带 geo 资产）。此前 `sha256:912aed…`（panel `4911fc3d…`）是 cert-pin 修复前的中间镜像，已被取代。
- 旧 tag `proxypanel-acceptance:20261021-agent010-baseline`（`sha256:326d0b9f…e4acca`，内置 Agent 0.1.0 `90ee1d0c…`）**保留**作来源记录；Agent 状态卷未删除。
- staged 生命周期上传件：agent `a28ca865…`、core 26.3.23 `05205489…`、官方 geo 资产 `geoip.dat` 19,701,033B / `geosite.dat` 10,454,877B（四行 `SHA256SUMS` 在容器内校验）。
- Agent 0.1.1 含 shutdown 持久化与 Deferred 退避修复；核心白名单同时接受 26.3.27 与 26.3.23（真实切换已验证）。

## 3. 仍明确未验收 / 限制

1. **A15 客户端矩阵**：真实 30 格通过，但 v2rayN 的 10 格是 `not-run`，**实际 GUI 导入从未执行**（observation 8 仍无 actionable snapshot / elements 0）；因此 40 格不等于 40/40。未覆盖 WS/gRPC/REALITY 等传输；公网直连未验收。历史 `docker-client-matrix-1.log`（`export "xray"` 422）与 `-2.log`（测试把仅有 blocked 的缺项读反）均已定位修复，不代表最终状态。
2. **v2rayN**：7.25.6 无官方非 GUI 导入接口，只读观测持续不可操作；不得写成已导入。旧 ForcedClear 无 prelaunch baseline，不能声称整体系统代理未变。
3. **执行边界**：一次 DS 脚本自测真实启动了 WSL（[execution-boundary-incident.md](execution-boundary-incident.md)），属明确不符合项；后续保护与原容器 7/7 一致**不能抵消**。最终报告必须单独列出。
4. **A14 缺口**：无 WAL/PITR、无生产 RPO/RTO/SLA、ACK 已删 70,093B/1 batch 不可恢复、未量化未采样核心尾数；不得宣称零数据损失或连续无缺口。
5. **发布产物**：`build/` 中**确切的** Linux amd64/arm64 发布二进制只做交叉构建 + 清单/ELF 校验，未在目标平台运行（Linux amd64 平台功能由 Docker 内 staged 构建验收，哈希与发布目录不同；arm64 架构整体未运行）；发布校验不等同发布者签名；Windows 发布件运行证据仅 `agent -version`，Windows 核心/浏览器功能证据来自本地验收。
6. **外部 Telegram 实际送达、公网直连、宿主 systemd、主机重启**：仍排除。A13 是容器生命周期，不是宿主机服务管理或主机重启验收。
7. **39 个环境门控 SKIP**（PG/Redis/真实核心/Docker/浏览器）不是集成成功，也不能折算成“全部通过”。
8. **十二项验收标准没有全部通过**：`#10` 未完全满足、`#12` 非完全符合；本文件不是完成声明。

## 4. 历史失败与修复（保留，不删除）

- 停电后文件、三平台发布产物 SHA256/架构复核通过；VPS amd64、约 3.2 GiB 可用内存、29 GiB 可用磁盘，原五容器与两测试数据库状态基线已记录。
- 独立 Docker 栈 `ppaccept-20261021-*`：专属网络/卷/标签，192 MiB / 0.5 CPU / 128 PID，无 privileged、host 网络、`docker.sock`；所有发布端口绑 `127.0.0.1`，只经 SSH 隧道访问。
- 早期主链 `TestDockerAcceptance`：55.83s PASS（在线撤权 1.602s、离线自然到期 1.132s/1.086s/1.995s）；`docker-e2e-1…9` 是已定位/修复的中间失败。
- 到期重建被 shutdown 中断会保留旧 users 的持久状态缺口已修，且不是授权复活；真实核心重启仍过滤过期用户。
- `TestDockerFaultAcceptance` 修正旧心跳竞态后 **37.96s PASS**（PG 故障队列一次入账、Redis 故障鉴权/限流与重建、16MiB tmpfs ENOSPC 停代理后恢复）；`TestDockerACKLossAcceptance` **2.20s PASS**（ACK 丢弃后重复返回 duplicate、异载荷 409），使用注入计数样本而非真实核心流量。
- 面板全包真实 PG/Redis `-race` 通过（API 417.504s，0 SKIP、0 DATA RACE）；RetryTask 三个真实 PG `-race` 回归 **55.38s PASS**。
- 前端 71/71 测试、typecheck、build、npm audit 0；增强真实浏览器验收 **59.71s PASS**（12 管理页、5 成员移动页、真实 404/空状态、注入的 3 延迟/3 个 503）。
- A13 历史失败（均已修复，原文见 [docker-lifecycle.md](docker-lifecycle.md) §13–§15）：脚本 helper 覆盖调用方参数导致容器命名错误（`docker-agent-upgrade-1.log`）；切换屏障接受旧心跳（`docker-core-switch-1.log`）；staged core 缺 `/s/geoip.dat`（`docker-core-switch-2.log`，父诊断 `docker-core-23-diagnostic.log`）。
- A07 历史失败：`docker-config-safety-1.log` 的 Deferred 退避自我续期缺陷已修（5/10/20/30s 封顶、只对同目标 hash 退避），新撤权/到期/合法文档不受旧退避阻挡；`docker-config-safety-2.log` 是修复后的真实通过。
- A15 历史失败：round 1 在 `export "xray"` 返回 422（生产 cert PEM 行数组 pin 派生缺陷，DS 7978 修复并部署）；round 2 测试侧把“preflight 仅含 blocked 的缺项”读反（DS 1337 修正判断与正反回归）；round 3 为 30 格真实通过。
- 首次恢复演练失败证据保留：远端 `backup/acceptance-first-attempt.dump`、`backup/recovery-first-agent1.db` 未删除；`docker-recovery-1.log` 是失败入口，不是通过记录。
- v2rayN 只读观测（窗口 329882 / pid 15128 / observation 8）返回 `snapshot_id` 空、`snapshot_actionable=false`、elements 0；没有导入、重启或修改系统代理，实际客户端结果仍未验证。
- 生命周期节点清理语义：`enabled=false` 不释放端口（契约允许空授权核心监听），只有归档 `DELETE /api/v1/admin/nodes/{id}` 会让实例离开快照并释放 20105；`docker-lifecycle-recover-3.log` **5.80s PASS**。

## 5. 证据与文件索引

- 持久脱敏证据（45 份，源日志名/执行性质/版本/数据差距 + `SHA256SUMS`）：[evidence/README.md](evidence/README.md)。
- 逐条验收状态与 A01–A16 索引：[final-acceptance.md](final-acceptance.md)。
- 脚本：`scripts/Initialize-DockerAcceptance.ps1`、`Invoke-DockerAcceptance.ps1`、`Prepare-DockerLifecycle.ps1`、`deploy/Dockerfile.acceptance`、`docker-acceptance.sh`、`docker-faults.sh`、`docker-lifecycle.sh`、`docker-lifecycle-selftest.sh`、`docker-recovery.sh`。
- 测试：`tests/e2e/docker_test.go`、`docker_lifecycle_test.go`、`docker_fault_test.go`、`docker_recovery_test.go`、`client_matrix_test.go`；`internal/panel/api/acceptance_test.go`、`internal/panel/store/retry_task_regression_test.go`。
- 终态与发布：`docker-original-final-comparison.txt`、`docker-owned-final-state.txt`、`release-current-summary.json/.md`、`release-current-verify-1/2.log`、`release-current-compare-rounds.log`、`release-current-meta-round1.json`。
- 真凭据只在 `.local/docker-acceptance` 与远端专属测试目录；不输出、不提交、不打包，不读取原始 `私钥.txt`。

## 6. 安全与执行边界

- 一次 DS 脚本自测真实启动了 WSL：见 [execution-boundary-incident.md](execution-boundary-incident.md)。这是**明确的执行边界不符合项**，后续保护、shell 选择器修复或原容器一致都**不能抵消**；最终十二项报告必须单独引用。
- 事件后的测试固定 `PANEL_BASH=E:\SDK\Git\bin\bash.exe`，禁止 WSL 探测/启动/停止；秘密（`.local`、私钥、SSH/root 凭据）不输出、不提交。远端操作只由授权操作者通过受保护 SSH 在归属本项目的资源上执行，不接触原业务容器/服务；本轮远端验收已结束，不再进行远端操作，后续只用已提供的脱敏证据。
- 远端只允许本项目自有命名资源；不重启宿主/Docker daemon/原服务，不全局 prune、不 `FLUSHALL`、不无范围 DROP。

剩余工作按批准目标继续执行；不以文档更新替代验收，也不把公网、Telegram、宿主 systemd/重启、未覆盖传输或 v2rayN 写成通过。
