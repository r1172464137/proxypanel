# 持久验收证据索引（脱敏副本）

本目录保存 2026-10-21 Docker／SSH 隧道验收的关键**脱敏**证据副本，来源为父会话 scratch。目的：把只存在于临时目录的关键日志固化为项目内可核查的附件，并明确每条证据的**执行性质、版本与数据缺口**。

## 使用规则

- 只收录**已脱敏**的关键日志/摘要：不含订阅 token、UUID、密码、Shadowsocks PSK、私钥、`.local` 内容、数据库夹具或原始配置。节点/成员/批次等**ID、回环端口和 SHA256 不是秘密**，保留原样以便对照。
- `*.log` 一律改存为 `*.txt`，避免被通用 `*.log` 忽略规则吞掉；JSON 摘要保持 `.json`，只读观测说明保持 `.md`。
- 文件名与来源日志同名（扩展名除外），便于回溯；来源 scratch 目录不是永久交付物，**完整历史日志不随项目分发**，需要时按文件名在会话 scratch 中查找。
- `SHA256SUMS` 覆盖本目录除自身与 `README.md` 外的全部文件，UTF-8 无 BOM、LF 行尾；可用 `E:\SDK\Git\usr\bin\sha256sum.exe --check --strict SHA256SUMS` 复核。
- 任何一份证据都**不能单独**证明整体验收完成；结论以 [final-acceptance.md](../final-acceptance.md) 的逐条状态为准。

## A13 Docker 生命周期（真实执行）

| 证据文件 | 执行性质 | 版本/环境 | 能证明什么 | 数据差距 |
| --- | --- | --- | --- | --- |
| `docker-core-switch-3.txt` | 真实执行（隔离 Docker + SSH 隧道） | Agent 0.1.1 `a28ca865…`；core 26.3.27 `8255dd93…` → staged 26.3.23 `05205489…` → 26.3.27；geo 资产 19,701,033 / 10,454,877 B | 同一新 Agent 下核心三段切换与回退，每步精确版本/新心跳/`applied=desired=176/176`/双节点真实代理/旧凭据拒；结束归档节点并释放 20105；PASS 41.81s | 只覆盖 26.3.27 与 26.3.23 两个官方版本；非宿主 systemd/主机重启 |
| `docker-agent-upgrade-2.txt` | 真实执行（注入 PG 停机） | 镜像 Agent 0.1.0 → staged 0.1.1 → 0.1.0 | Agent 版本升级/回退、同 identity/代次/状态卷、4 批 420,558B 逐批一次入账；PASS 93.21s | 注入停库为受控故障；当时面板为早期二进制 |
| `docker-lifecycle-assets-stage.txt` | 真实执行（远端 `stage` 只读校验） | 四行 `SHA256SUMS`；staged agent/core + 两个 geo 文件 | 上传件在容器内校验通过并打印实测版本；镜像 0.1.0 / core 26.3.27 基线 | 只证明上传件一致，不代替切换用例 |
| `docker-current-agents-deploy.txt` | 真实执行（镜像构建 + 仅重建两个自有 Agent） | image `sha256:912aed…`；panel `4911fc3d…`；agent `a28ca865…`；xray `8255dd93…` | 两源 Agent 正式 0.1.1、状态卷保留、旧 baseline tag 保留 | 该镜像是后续被 cert-pin 面板镜像取代的中间产物 |
| `docker-panel-cert-pin-deploy.txt` | 真实执行（镜像构建 + 重建自有面板） | image `sha256:4e96ae…`；`/app/panel` `0595390…`；agent/xray 未改 | 生产 cert PEM 行数组 pin 修复后的面板已部署并回读 SHA | 只证明面板二进制，不重跑全部 Docker 用例 |

## A07 配置安全与主链（真实执行）

| 证据文件 | 执行性质 | 版本/环境 | 能证明什么 | 数据差距 |
| --- | --- | --- | --- | --- |
| `docker-config-safety-2.txt` | 真实执行（真实核心拒绝 + 回滚） | Agent 0.1.1；Xray 26.3.27 | 撤权 1.054s；无效文档被真实核心拒绝；同 Agent 另一节点不受影响；技术回滚不复活撤权；有效配置恢复；PASS 19.87s | 单节点组、单服务器；不覆盖所有无效配置类别 |
| `docker-e2e-current-agent.txt` | 真实执行（主链） | 当前 0.1.1 Agent + 真实 TLS 面板 + 两 Agent | 在线 TCP/UDP 撤权 1.830s；自然离线到期 0.733s/0.938s/1.596s（PG 服务端时钟）；重启不复活撤权；崩溃自动重启；卸载/重建保留卷身份；PASS 57.29s | 单一成员/节点组合；不证明生产规模 |

## A14 隔离恢复（真实执行，非生产 SLA）

| 证据文件 | 执行性质 | 版本/环境 | 能证明什么 | 数据差距 |
| --- | --- | --- | --- | --- |
| `docker-recovery-2.txt` | 真实执行（独立还原目标 + 恢复门禁） | 独立 restore/PG；恢复面板 18444；Agent1 停止态副本 | `TestDockerRecovery` PASS 120.64s 与汇总行 | 日志只有汇总；逐项断言见测试源码与 JSON |
| `recovery-evidence-summary.json` | 父从已脱敏证据白名单提取 | 同上 | 备份 46,722,583B/214 批；重放 70,093B/1 批；ACK 已删不可恢复 70,093B/1 批；124 基线/60 代次；RPO 7.467s；备份年龄 75.5166–76.1898s；还原 4.9013s；RTO 31.8674s；恢复后账本 46,792,676B | 无 WAL/PITR；未量化未采样核心尾数；不得宣称零损失 |
| `docker-post-recovery-state.txt` | 真实只读状态快照（演练后即时） | 同一宿主 | 恢复容器停止、源六容器运行、原七容器与基线一致 | 中间核对，已被最终对照取代但仍保留 |

## A15 客户端矩阵（真实执行；v2rayN 只读阻塞）

| 证据文件 | 执行性质 | 版本/环境 | 能证明什么 | 数据差距 |
| --- | --- | --- | --- | --- |
| `docker-client-matrix-3.txt` | 真实执行（真实导出 + 固定 Windows 核心 + 隧道） | Xray 26.3.27 / sing-box 1.14.3 / Mihomo 1.19.32；两 Agent；5 协议 10 节点 | 30 格真实配置加载 + 69,632B 载荷通过；v2rayN 10 格 `not-run`；PASS 6.57s | **不是 40/40**；只覆盖 TCP/none 与 Trojan TLS、SS/SS2022；未覆盖 WS/gRPC/REALITY 等传输；v2rayN 未导入 |
| `docker-client-matrix-1.txt` | 历史失败（已修复） | 同上 | 首轮在 `export "xray"` 返回 422 时失败，未进入矩阵 | 仅作修复前证据 |
| `docker-client-matrix-2.txt` | 历史失败（已修复） | 同上 | 第二轮把“仅含 blocked 的 preflight 缺项”读反，30/40 被判 blocked | 测试判断缺陷，非生产回归 |
| `v2rayN-final-readonly-blocker.md` | 只读观测（无操作） | v2rayN 7.25.6，PID 15128，窗口 329882 | observation 8 无 actionable snapshot、elements 0；未导入/未重启/未改系统代理 | 无实际 GUI 导入；旧 ForcedClear 无 prelaunch baseline，不能称整体系统代理未变 |

## 环境、原容器与资源归属（真实只读）

| 证据文件 | 执行性质 | 版本/环境 | 能证明什么 | 数据差距 |
| --- | --- | --- | --- | --- |
| `docker-original-final-comparison.txt` | 只读对比（终态） | 宿主 Docker；UTC 2026-10-21T05:59:37Z | 原 7 容器（5 业务 + 2 隔离测试库）full ID/running/StartedAt/RestartCount **7/7 完全一致** | 只证明观察到的容器层面未变，不能抵消 WSL 边界事件，也不覆盖未观察的宿主副作用 |
| `docker-owned-final-state.txt` | 只读状态清单 | 同一宿主 | 9 个自有容器（6 源运行 + 3 恢复停止）、6 个命名卷、1 个 bridge、current/old-baseline 两 tag、回环端口、无 lifecycle/临时容量/helper 容器；backup 364K、recovery 320K 保留 | 不声称已删除全部临时资源；不覆盖全局原业务零影响 |

## 本地工程检查（本地真实执行，无远端）

| 证据文件 | 执行性质 | 版本/环境 | 能证明什么 | 数据差距 |
| --- | --- | --- | --- | --- |
| `final-local-gotest-all.txt` + `final-local-govet.txt` | 本地真实执行 | Go 1.27.3；`PANEL_BASH=E:\SDK\Git\bin\bash.exe` | `go test ./...` 与 `go vet ./...` 退出 0 | 39 个环境门控 SKIP（PG/Redis/真实核心/Docker/浏览器）不计入集成成功 |
| `final-local-skip-audit.txt` | 本地只读扫描 | 同上 | 明确列出 SKIP/构建标签来源 | 仅审计用途 |
| `final-local-agent-race.txt` + `final-local-agent-xrayreal-race.txt` | 本地真实执行 | w64devkit 2.10.1 / GCC 16.2.0；Xray 26.3.27 | Agent 七包与真实核心 Supervisor `-race` 全 PASS | 不覆盖生产规模并发 |
| `final-local-corematrix-localcore.txt` + `final-cert-pin-corematrix.txt` | 本地真实核心执行 | Xray/sing-box/Mihomo 固定版本 | 6 组合 × 3 客户端与 2 项凭据守卫；cert-pin 修复后复跑 | 本地回环，不等于远端客户端矩阵 |
| `final-cert-pin-race.txt` + `final-cert-pin-govet.txt` | 本地真实执行 | 同上 | cert-pin 修复影响的 4 个包 `-race` PASS；vet 0 诊断 | 只覆盖受影响包 |
| `final-web-test.txt` / `final-web-typecheck.txt` / `final-web-build.txt` / `final-web-audit.txt` | 本地真实执行 | Node 24.22.0 / npm 11.19.1 / Vitest 5.0.5 / Vite 6.4.4 | 71/71 测试（10 文件）、typecheck 0、build 185 模块、audit 0 漏洞 | 未重跑浏览器（前端源码未变，沿用既有真实浏览器证据） |
| `browser-enhanced-1.txt` | 真实浏览器执行（注入 3 延迟/3 个 503） | 先前构建；前端源码未变 | 12 管理页 + 5 成员移动页 + 真实 404/空状态、28 次布局检查、无未处理页面错误；59.71s | 注入场景明确标注；非公网 |
| `acceptance-pg-fixed.txt` / `panel-real-pg-race.txt` / `retry-task-real-pg-race.txt` | 真实 PostgreSQL（本地隔离测试库） | 真实 PG/Redis | 邀请/会话/单管理员/双管理员并发/继承覆盖/订阅周期/节点组传播；面板全包 `-race`（API 417.504s、0 SKIP、0 DATA RACE）；RetryTask 三回归 55.38s | 使用项目隔离测试库，不是生产数据 |
| `docker-fault-2.txt` | 真实执行 + 受控故障注入 | 16MiB tmpfs；真实 PG/Redis | PG 故障 210,279B 仅入账一次、Redis 故障/重建、ENOSPC 停代理后恢复；PASS 37.96s | 容量受限文件系统为测试构造 |
| `docker-ack-1.txt` | 真实执行 + 受控 ACK 丢弃 | 回环 TLS 代理 | 12,288B 只入账一次、重复返回 duplicate、异载荷 409；PASS 2.20s | 注入计数样本，不是真实核心流量 |
| `docker-batch-retry-1.txt` | 真实执行（停自有 Agent） | 面板 RetryTask 修复 | 双节点部分失败可见、同任务重试收敛、旧凭据仍拒；PASS 72.98s | 受控停止自有容器 |

## 发布产物（本地构建 + 两轮校验）

| 证据文件 | 执行性质 | 版本/环境 | 能证明什么 | 数据差距 |
| --- | --- | --- | --- | --- |
| `release-current-summary.json` / `.md` | 本地真实执行汇总 | Go 1.27.3；sha256sum 8.32；node 24.22.0 | 三平台各 72 文件 / 71 条 SHA256；两轮 SHA256SUMS 完全一致；Windows agent `0.1.1` | `build/` 中确切 Linux amd64/arm64 发布二进制只交叉构建 + 清单/ELF 校验，未在目标平台执行（Linux amd64 平台功能在 Docker 内由 staged 构建验收，哈希不同；arm64 未运行）；Windows 发布件仅 `agent -version` |
| `release-current-verify-1/2.txt` | 本地真实执行 | 同上 | `Test-Release.ps1` 两轮均通过清单/资源/架构校验 | 不等同发布者签名 |
| `release-current-compare-rounds.txt` | 本地真实执行 | 同上 | 三平台两轮清单逐字节一致 | — |
| `release-current-meta-round1.json` | 本地采集 | 同上 | 第一轮文件数、清单 SHA、panel/agent SHA 与字节数 | — |
| `release-current-sha256-windows-amd64-1.txt` 等三份 | 本地真实执行 | GNU coreutils 8.32 | 每平台 `sha256sum --check --strict` 全部 OK | 第二轮的逐平台结果同源，未逐份收录 |

## 说明

- 本索引只列**已收录**的 45 个文件；更完整的历史日志（如 `docker-e2e-1…9`、`docker-core-switch-1/2`、反复修复的中间运行）保留在会话 scratch，仅作过程追溯，不随项目分发。
- 所有“PASS”都是相应入口在所列版本下的真实结果，不等于其他版本、其他平台或生产规模同样通过。
- 统一测试边界：事件后的测试只用 `PANEL_BASH=E:\SDK\Git\bin\bash.exe`，禁止 WSL 探测/启动/停止；秘密（`.local`、私钥、SSH/root 凭据）不输出、不打包。一次 DS 脚本自测确实启动过 WSL，见 [execution-boundary-incident.md](../execution-boundary-incident.md)。
