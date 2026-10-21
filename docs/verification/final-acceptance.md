# 最终验收状态（12 条逐项 + A01–A16 索引）

基线：[已批准目标](../../.pi/goal/完成计划剩余任务与-docker-ssh-隧道验收-20261021-1045.md)（2026-10-21 范围调整：A13 = 本项目独立 Docker 容器生命周期；全部跨机访问只经 SSH 隧道；不声称宿主 systemd/主机重启、公网直连或外部 Telegram 实际送达）。

本文件是交付的状态与证据索引，不是“全部通过”的声明。**逐条看状态：`#10` 未完全满足，`#12` 存在明确的执行边界不符合项；其余条目在本次调整后的范围内有真实证据。** 任何条目都不能被其他条目的通过抵消。

## 1. 结论

- `#1`–`#9`、`#11`：**满足**（证据与限制见下）。
- `#10`（A15 客户端矩阵）：**未完全满足**——真实 30 格通过，但 v2rayN 的实际 GUI 导入从未执行；40 格报表中 10 格是 `not-run`，不是成功。
- `#12`（A16 与交付边界）：**非完全符合**——一次 DS 脚本自测确实启动了 WSL（[execution-boundary-incident.md](execution-boundary-incident.md)）；后续的 shell 选择器修复与原始容器 7/7 一致**不能抵消**该事件。
- 因此整体验收**未全部完成**；不宣称无条件完成、零数据损失或原业务零影响。外部 Telegram 送达、公网直连、宿主 systemd、主机重启明确排除。

## 2. 统一版本与环境

| 项 | 版本 / 值 |
| --- | --- |
| 面板（当前验收镜像内二进制） | `/app/panel` SHA256 `059539071ba945bed6d7222607cd2d237b4bef1ab9b3f16c4412794bc9575bda` |
| 验收镜像 | `proxypanel-acceptance:20261021` = `sha256:4e96ae4a51f01949ab7a7e747d84e5229b4d6589332e3032d185480c564eb76d`；旧来源 tag `…-agent010-baseline` = `sha256:326d0b9f…e4acca` 保留 |
| Agent | `proxypanel-agent 0.1.1`，SHA256 `a28ca86562686f4b66c856e69253cdc6202461f53f1c2cd1c38358f76fabd66a`（两源 Agent 均为该件，状态卷未重建） |
| 核心 | Xray **26.3.27** SHA256 `8255dd939c34cf966cc91517b6324dd3c8d0bcf49ffac8beca049a38c46845ed`；可切换的第二官方版本 **26.3.23** SHA256 `05205489259d6c74795d98b4f01fcd11f987fc6ff7f65fd2991e9a7164518c98`（含同 zip 的 `geoip.dat` 19,701,033B / `geosite.dat` 10,454,877B） |
| 客户端核心 | Xray 26.3.27（`E:\SDK\Xray\v26.3.27\xray.exe`）、sing-box 1.14.3、Mihomo 1.19.32；v2rayN 7.25.6 仅只读观测 |
| 工具链 | Go 1.27.3 windows/amd64、w64devkit 2.10.1 / GCC 16.2.0、Node 24.22.0、npm 11.19.1、Vitest 5.0.5、Vite 6.4.4、sha256sum (coreutils) 8.32 |
| 测试边界 | **事件后**的收尾批次显式使用 `PANEL_BASH=E:\SDK\Git\bin\bash.exe`，shell 选择器同时拒绝 System32 启动器；此前一次 DS 脚本自测经 PATH 真实启动了 WSL（[execution-boundary-incident.md](execution-boundary-incident.md)），因此不声称“全部批次边界合规”或“本机环境未变”。Docker 用例经受保护 SSH 隧道、串行执行 |

## 3. 12 条验收标准逐项状态

| # | 标准（摘要） | 状态 | 关键证据 | 限制 / 说明 |
| --- | --- | --- | --- | --- |
| 1 | 状态与回归证据可信；A01–A16 有入口/版本/结果/证据，区分真实、注入、跳过与范围调整 | **满足** | 本文件 §4；[evidence/](evidence/README.md) 的 45 份脱敏证据与索引；`final-local-skip-audit.txt` | 39 个环境门控 SKIP 明确不计入集成成功；完整历史日志只留在 scratch，非永久附件 |
| 2 | A01–A03 身份、授权、批量正确 | **满足** | 真实 PG `TestAcceptance*`（邀请/会话撤销/最后管理员/双管理员并发/继承覆盖/订阅切换）；`TestDockerBatchPartialFailure` 72.98s | 隔离测试库，不是生产数据；批量部分失败用停止自有 Agent 制造 |
| 3 | A04–A05 订阅、周期与输出正确 | **满足** | `TestAcceptanceSwitchSubscriptionKeepsTokenUsageAndCycle`、`…SubscriptionRuleEditKeepsRunningCycle`、`…BoundarySplitAndQuotaChange`、`…NodeGroupPropagationAndDedupe`；导出器去重/权限单测与真实核心矩阵 | 周期用受控时钟/边界构造，不是自然跨月长期运行 |
| 4 | A06–A07 真实核心撤权与配置安全 | **满足** | 主链在线 TCP/UDP 撤权 **1.830s**；A07 `TestDockerConfigSafety` 19.87s（无效文档拒绝、另一节点不受影响、回滚不复活撤权、有效配置恢复） | 「单节点组 / 单服务器」只限定 A07 同 server 共享实例的验证；A06 主 e2e 是双 Agent。切换会中断同节点其他连接（不承诺零停机） |
| 5 | A08–A10 账本、重启与离线到期 | **满足** | ACK 丢弃 12,288B 只入账一次/重复 duplicate/异载荷 409；升级 4 批 420,558B 逐批一次；PG 故障 210,279B 一次入账；离线自然到期 0.733s/0.938s/1.596s；重启与回滚不复活撤权 | 注入式故障与受控样本，明确标注；非自然跨月 |
| 6 | A11 受控故障行为 | **满足** | `TestDockerFaultAcceptance` 37.96s（PG/Redis/16MiB tmpfs ENOSPC 停代理后恢复）；数据库不可用不返回计量成功 ACK | 故障注入限隔离组件与容量受限文件系统；未耗尽宿主磁盘/内存 |
| 7 | A12 可观测与界面可用 | **满足** | 真实浏览器 59.71s（12 管理页 + 5 成员移动页、真实 404/空状态、28 次布局检查、无未处理页面错误）；前端 71/71 测试 + typecheck/build/audit；告警去重/恢复与通知重试单测 | 3 个延迟与 3 个 503 为明确注入；**外部 Telegram 实际送达未验收**（按本目标仅记录实现与模拟结果） |
| 8 | A13 Docker 生命周期实测 | **满足（容器口径）** | Agent 升级/回退 93.21s；核心 26.3.27→26.3.23→26.3.27→镜像 0.1.0 的 41.81s；主链崩溃自动重启与卸载/重建保留卷身份 | 仅容器与自有网络/卷；**不代表宿主 systemd 或主机重启**；未执行的架构（arm64）只标注可构建：`build/` 中确切的 Linux arm64 发布二进制只做交叉构建、未在目标平台运行 |
| 9 | A14 隔离恢复演练 | **满足（有实测缺口）** | `TestDockerRecovery` 120.64s：独立目标还原、新随机 generation、显式 `accept-generation`、账本精确一次、旧授权不复活、源栈未改；备份 **46,722,583B / 214 批**、重放 **70,093B / 1 批**、RPO **7.467s**、备份年龄 **75.5166–76.1898s**、还原 4.9013s、RTO **31.8674s** | **ACK 已删 70,093B / 1 batch 不可恢复**；无 WAL/PITR、未量化未采样核心尾数；不覆盖现有数据库，不声称零数据损失 |
| 10 | A15 实际客户端矩阵 | **未完全满足** | `docker-client-matrix-3.log` **PASS 6.57s**：5 协议（VLESS/VMess/Trojan TLS/legacy SS/SS2022）× 2 节点 × Xray 26.3.27/sing-box 1.14.3/Mihomo 1.19.32 = **30 格真实配置加载 + 69,632B 载荷**；不支持的组合由 `UnsupportedError` 显式拒绝（本地真实核心与单测验证） | **v2rayN 实际 GUI 导入未验收**（10 格 `not-run`，只读观测仍无 actionable snapshot）；**不是 40/40**；未覆盖 WS/gRPC/REALITY 等传输；公网直连未验收 |
| 11 | 工程检查与产物可复现 | **满足** | `go test ./...`、`go vet ./...`、Agent 七包 `-race`、真实 Xray Supervisor `-race`、cert-pin 受影响 4 包 `-race` 全 PASS；前端 71/71 + typecheck/build/audit 0；发布两轮 `Build-Release.ps1`/`Test-Release.ps1`/`sha256sum --check --strict` 通过，三平台各 **72 文件 / 71 条**，两轮清单逐字节一致 | `build/` 中**确切的** Linux amd64/arm64 发布二进制只交叉构建 + 清单/ELF 校验、未在目标平台运行（Linux amd64 平台功能由 Docker 内 staged 构建验收，哈希与发布目录不同；arm64 架构整体未运行）；Windows 发布件运行证据仅 `agent -version`；39 个 SKIP 不计入集成成功；发布校验不等同发布者签名 |
| 12 | A16 与交付边界可核查 | **非完全符合（执行边界）** | 秘密未入库/未打包（`.gitignore` + 发布清单校验）；自有资源清单与保留记录；原始 7 容器终态 **7/7 完全一致**（`docker-original-final-comparison.txt`，UTC `2026-10-21T05:59:37Z`） | **一次 DS 脚本自测真实启动了 WSL**：违反“不启动 WSL”边界，未再探测/关闭；原容器一致**不能抵消**。不把容器层对照扩大为原业务零影响；不声称删除全部临时资源 |

## 4. A01–A16 测试入口 / 版本 / 结果索引

> 入口名与结果一一对应；“注入”表示受控故障或模拟时钟/样本，“本地”表示无远端。计划原文 A13 为 Linux/systemd，本目标已调整为 Docker 容器生命周期，下表按**调整后**口径标注。

| 编号 | 目标 | 测试入口（命令族） | 版本 / 环境 | 结果 | 证据 |
| --- | --- | --- | --- | --- | --- |
| A01 | F01、安全 | `go test -race ./internal/panel/api -run 'TestAcceptance'`（真实 PG/Redis） | 当前面板源码；隔离测试库 | PASS（邀请一次兑换 6.26s、会话撤销/范围 37.31s、最后管理员 15.69s、双管理员并发 151.04s） | `panel-real-pg-race.txt`、`acceptance-pg-fixed.txt` |
| A02 | F02、F03 | 同上（继承/覆盖、订阅切换、规则边界）+ `TestDockerBatchPartialFailure` | 真实 PG；Docker 双 Agent | PASS（23–36s 级真实 PG；Docker 72.98s） | `panel-real-pg-race.txt`、`docker-batch-retry-1.txt` |
| A03 | F03、任务 | `TestDockerBatchPartialFailure` + `go test -race ./internal/panel/store -run TestRetryTask` | 面板 RetryTask 修复；真实 PG | PASS（部分失败可见、同任务重试收敛、旧凭据仍拒；PG 回归 55.38s） | `docker-batch-retry-1.txt`、`retry-task-real-pg-race.txt` |
| A04 | F02、F08 | `TestAcceptanceSwitchSubscriptionKeepsTokenUsageAndCycle`、`…SubscriptionRuleEditKeepsRunningCycle`、`…BoundarySplitAndQuotaChange` | 真实 PG/Redis；受控边界 | PASS（26.46s / 23.70s / 31.14s） | `panel-real-pg-race.txt` |
| A05 | F04、F07 | `TestAcceptanceNodeGroupPropagationAndDedupe`、导出器去重/权限单测、`TestLocalCoreCredentialGuards` | 真实 PG；Xray/sing-box/Mihomo | PASS（31.61s；6 组合 × 3 客户端 + 2 守卫） | `panel-real-pg-race.txt`、`final-cert-pin-corematrix.txt` |
| A06 | F01、F05 | `TestDockerAcceptance`（在线撤权）+ `TestDockerConfigSafety`（撤权部分） | Agent 0.1.1；Xray 26.3.27；隧道 | PASS（TCP/UDP 1.830s、20B UDP 回显；A07 撤权 1.054s） | `docker-e2e-current-agent.txt`、`docker-config-safety-2.txt` |
| A07 | F05、F06 | `TestDockerConfigSafety` + cert-pin 单测/`TestLocalCore` | 同上；cert PEM 行数组 pin 修复 | PASS（19.87s；无效文档拒绝、回滚不复活、有效恢复；pin 负例保留） | `docker-config-safety-2.txt`、`final-cert-pin-corematrix.txt`、`final-cert-pin-race.txt` |
| A08 | F09 | `TestDockerACKLossAcceptance` + `TestDockerAgentVersionUpgrade` 队列段 + `TestDockerFaultAcceptance` PG 段 | 注入 ACK 丢弃；注入停库；真实 PG | PASS（12,288B 一次；4 批 420,558B 一次；210,279B 一次） | `docker-ack-1.txt`、`docker-agent-upgrade-2.txt`、`docker-fault-2.txt` |
| A09 | F08、F09 | Agent 升级/回退、崩溃重启、真实核心 Supervisor `-race` | Agent 0.1.1；Xray 26.3.27 | PASS（同 identity/代次/卷；无负数双计；`-race` 24.811s） | `docker-agent-upgrade-2.txt`、`docker-e2e-current-agent.txt`、`final-local-agent-xrayreal-race.txt` |
| A10 | F08、F10 | 主链离线到期 + 重启/回滚不复活 + A14 旧授权拒绝 | PG 服务端时钟；Agent 0.1.1 | PASS（0.733s/0.938s/1.596s；重启后仍拒；恢复后旧授权拒） | `docker-e2e-current-agent.txt`、`docker-recovery-2.txt` |
| A11 | F09、F10 | `TestDockerFaultAcceptance` | 16MiB tmpfs；真实 PG/Redis | PASS（37.96s；ENOSPC 停代理后由新心跳与真实流量恢复） | `docker-fault-2.txt` |
| A12 | F11、F12 | `TestPlatformRealCore`（浏览器）+ 前端测试/typecheck/build/audit + maintenance 单测 | 固定构建；注入 3 延迟/3 个 503 | PASS（59.71s；71/71、0 漏洞；告警去重/重试单测） | `browser-enhanced-1.txt`、`final-web-*.txt`、`panel-real-pg-race.txt` |
| A13 | F06、部署（**调整为容器生命周期**） | `TestDockerAgentVersionUpgrade`、`TestDockerCoreVersionSwitch`、`TestDockerAcceptance` 崩溃/卸载段 | Agent 0.1.0↔0.1.1；core 26.3.27↔26.3.23 | PASS（93.21s、41.81s、57.29s） | `docker-agent-upgrade-2.txt`、`docker-core-switch-3.txt`、`docker-e2e-current-agent.txt` |
| A14 | 恢复 | `TestDockerRecovery`（`PANEL_DOCKER_RECOVERY_TEST=1`） | 独立 restore 25433 + 恢复面板 18444 | PASS（120.64s；RPO 7.467s、RTO 31.8674s、70,093B 不可恢复） | `docker-recovery-2.txt`、`recovery-evidence-summary.json`、[docker-recovery.md](docker-recovery.md) |
| A15 | 客户端 | `TestDockerClientMatrix` + `go test -tags corematrix ./internal/panel/clientconfig -run TestLocalCore` | Xray 26.3.27/sing-box 1.14.3/Mihomo 1.19.32；v2rayN 7.25.6 只读 | **部分通过**：Docker 30 格真实 PASS（6.57s）；本地 8 个测试子项（17 次真实连接 + 1 次 Mihomo 明文 Trojan 显式拒绝）PASS；v2rayN 未运行 | `docker-client-matrix-3.txt`、`final-cert-pin-corematrix.txt`、`v2rayN-final-readonly-blocker.md`、[client-matrix.md](client-matrix.md) |
| A16 | 环境限制 | 宿主只读终态对照 + 自有资源清单 + 发布清单秘密校验 + 边界记录 | 同一宿主；UTC `2026-10-21T05:59:37Z` | **部分符合**：原始 7 容器 7/7 一致、秘密未打包、资源清单完整；**WSL 启动一次不符合** | `docker-original-final-comparison.txt`、`docker-owned-final-state.txt`、[execution-boundary-incident.md](execution-boundary-incident.md) |

## 5. 资源、清理与保留记录

- 自有容器共 **9 个**：源栈 6 个（postgres/redis/panel/agent1/agent2/target）运行中；restore / recovery-panel / recovery-agent **已停止**（卷保留）。无 lifecycle、临时容量或 helper 容器残留。
- 自有卷 **6 个**（agent1、agent2、pg、redis、restore、recovery-agent）；网络 `ppaccept-20261021-net` 1 个；镜像保留 current `4e96ae…` 与 old baseline `326d0b…` 两个 tag。
- 远端受保护证据 `backup/` 364K、`recovery/` 320K 保留：第一轮备份先另存为 `backup/acceptance-first-attempt.dump`，Agent 状态副本 `backup/recovery-first-agent1.db` 一并保留；第二轮 control backup 重新生成 `acceptance.dump`（同名备份文件被重写，不是源数据库）。还原只在独立 restore 容器/库执行，不覆盖源 DB、不清空 Agent 原 volume；因此不声称“全部备份从未被覆盖”。
- 宿主可用约 **2,648 MiB / 25 GiB**；原始 7 容器（5 业务 + 2 隔离测试库）full ID/StartedAt/RestartCount 终态 7/7 与基线一致；**不声称原业务零影响**，也不声称已删除全部临时资源。

## 6. 明确排除项（未验收）

- 外部 Telegram 实际送达（仅实现与模拟测试）。
- 公网直连（全部跨机访问只经 SSH 隧道）。
- 宿主 systemd 安装/服务启停、主机重启与自动恢复（A13 是容器口径）。
- A15 中的 v2rayN 实际 GUI 导入，以及 WS/gRPC/REALITY 等未构造的传输矩阵。
- `build/` 中**确切** Linux amd64/arm64 发布二进制的目标平台运行验收（只交叉构建 + 清单/ELF 校验；Linux amd64 平台功能由 Docker 内 staged 构建验收，arm64 架构整体未运行）。
- 生产 RPO/RTO/SLA 与零数据损失（A14 只给出隔离演练实测值与不可恢复缺口）。

## 7. 证据入口

- 全部脱敏证据与逐条数据差距：[docs/verification/evidence/README.md](evidence/README.md)（含 `SHA256SUMS`）。
- Docker 生命周期与 A02/A07/A13 细节：[docker-lifecycle.md](docker-lifecycle.md)。
- 进度与历史失败：[docker-acceptance-progress.md](docker-acceptance-progress.md)。
- A14 细节与缺口：[docker-recovery.md](docker-recovery.md)、[docs/recovery.md](../recovery.md)。
- A15 矩阵与未覆盖项：[client-matrix.md](client-matrix.md)。
- 执行边界事件：[execution-boundary-incident.md](execution-boundary-incident.md)。
