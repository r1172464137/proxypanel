# 升级停止核对：主线同步 handoff

## Base / 范围

- 隔离分支 `pi-web-agent-e6509fd9-6e57-4c0c-9579-86f408f93c0a`，开始 base `96e76dd`（已含 ABC 与 D 的真实 intent 能力）。不修改主工作区。
- 主线需同步的新路径：`internal/panel/store/upgrade_evidence_stop.go`、`internal/panel/store/upgrade_evidence_stop_test.go`、本文。其余修改仅 readiness 停止核对、evidence 字段/验证/审计与 CLI 摘要；无 SQL、HTTP Agent、角色或远程 shell 变更。
- 依据：主工作区 `.ai/design/product-readiness/backend.md` §6.4.3、§6.4.4、§9.9，以及 Agent `LIFECYCLE_HANDOFF.md` / `PROTOCOL_INTEGRATION_HANDOFF.md`。

## 信任边界（不是机器独立证明）

设计明确授权**现有维护管理员**核对停流/最终计量/停止服务的外部事实，并通过受控本地文件提供核对材料。这里实现的是该授权下的结构化 **管理员核对**，不是新角色，不声称 panel 远程观察过进程，也不是数字签名认证管理员身份。能配置本机清单的维护管理员负责材料真实性、服务 fence 覆盖全部自动/人工重启路径、完整本机 inventory（含 orphan/untracked core）、无未解决最终采样错误及实际操作授权。

文件 hash 只绑定内容。管理员名字、完整性声明、零 reports/intents 都不能独自放行：必须同时通过身份/PG/状态指纹、逐实例进程身份与终止观察、owner 窗口、core generation、执行水位和时序交叉验证。若不能诚实提供这些事实，继续 unknown/blocked；不得编造材料。

旧附件不带新格式仍为 `AGENT_STOP_UNVERIFIED=unknown`；旧 schema 无 intent **不再独立构成永远 unknown**，完整外部管理员核对可替代其不存在的 journal。存在真实 pending intent/report 则仍 blocked，不能通过 attestation 覆盖。

## 可执行采集 / 核对路径

1. 管理员在已授权维护窗口停新增代理流量，执行并核实最终计量；保留旧 Uploader 与面板接收能力，完成回执确认。无法排空的 usage batch 必须保留原 ID/字节并补齐 period/owner 与 core generation 对应。最终采样失败不能声明成功。
2. 管理员建立重启 fence（所有 service/watchdog/container/人工重启路径），记录 service 身份、机制、开始时间及有效截止时间；在核对和迁移期间不得解除。精确记录 Agent 与每个 core 的 PID、boot ID、OS 启动身份、可执行文件 SHA256；核实退出或同一 OS 身份已不存在。不得仅凭 PID 数量或 `kill -0` 失败认定退出。
3. 停止 Agent，取得完整 checkpoint 的一致状态备份，执行现有只读接口：
   ```sh
   agent -inspect-state /controlled/stopped-backup > /controlled/materials/agent.json
   panel check --preflight --json
   ```
   第二条在未配置材料时用于取得 PG `database_sha256`，**非通过门禁**。备份指纹取真实 Agent export 的 `database_sha256`。PG 内容有变化必须重新核对。
4. 保存外部观察/操作记录为受控目录中的普通文件；管理员逐实例核对后填写 `UpgradeStopReview` JSON（完整字段定义在新 Go 文件中）。现有 manifest 的 agent 条目新增：
   ```json
   {
     "agent_id":"实际AgentID",
     "file":"agent.json", "sha256":"实际导出文件SHA256",
     "observed_agent_version":"PG中实际版本",
     "stopped_evidence_file":"stop-review.json",
     "stopped_evidence_sha256":"实际核对文件SHA256",
     "stop_review_format":"proxypanel.administrator-stop-review/v1"
   }
   ```
   `stop-review.json` 的 `format_version` 必须与此相同，`confirmation` 必须是 `administrator-reviewed`；管理员填写实际身份、PG/导出/状态三个指纹、PG 全局 generation/revision、Agent generation/applied/desired revision，全部完整性核对项、时间、restart_fence、agent_process、instances、records。`records` 为相对文件路径+SHA256；process 与 fence 的 `record_index` 指向其外部依据。`observation` 仅允许 `observed-exit` 或 `os-identity-absent`。不得把模板占位值当作观测。
5. 每个 PG core、该 server 的 node、每条 asserted owner window、每个排队 sample 的 core generation/时间窗口必须在 instances 中核对。历史执行可有独立 generation/window 记录；不能用当前归属猜历史。节点从未执行且无法提供此版本要求的进程证据时保守 blocked，不制造 PID。Agent 的 desired/applied 必须相等且 generation 是当前全局代际；未完成控制变更应先用旧服务核对完成。
6. 执行唯一核对接口：
   ```sh
   panel check --preflight --evidence /controlled/materials/manifest.json --json
   panel check --preflight --evidence /controlled/materials/manifest.json --json=false
   ```
   JSON `stop_verification` 与文本摘要明确显示“管理员核对 / administrator-reviewed … not independently observed by panel”。退出码 ready=0、blocked=2、unknown=3。服务端使用同一 `PANEL_UPGRADE_EVIDENCE_FILE`，不接受 HTTP 路径。
7. ready 后由既有 Migrate 在 table lock 后重新读取材料/PG 指纹，0005 DDL、period/owner 导入、含“管理员核对”来源的 audit、migration registry 同事务提交。材料变更/fence 过期/DB 变化/导入失败均不放行。不更改旧账本。

## 验证与局限

- 新测试使用私有临时 PG cluster（仅私有 Unix socket、随机端口）及每例随机 schema；不复用系统/共享服务。失败与成功都检查 migration registry / 新表。
- 正例：真实 `agent/state` 创建、关闭、checkpoint 后只读导出；两个精确自有 `/bin/sleep` PID 作为合成 workload 的 Agent/core 进程替身，真实读取 `/proc` 身份和 exe hash，精确终止并 Wait / OS absence 核对。**不是实际 Xray/代理流量端到端验收**；测试的最终计量工作负载为空，明确注明合成范围。
- 反例：未停声明、缺实例、错 PG/core generation/revision、错 server/version/state/export/record hash、错采集顺序、fence 失效、inventory 不全、PID 重复/缺启动身份、未核对执行、最终计量失败、未确认 reports 均 blocked 且不迁移；另有注入 audit 失败验证 ready 后的 0005 事务回滚。
- 原 ACK 丢失同 ID/hash 回归、旧任意附件仍 unknown、D 支持但无停止核对仍 unknown、pending intent/report 仍 blocked、文件越界/链接/严格 JSON 测试保留。
- 此材料绑定一次 PG 指纹；迁移提交后 DB 指纹改变，旧材料不是后续任意升级或运行状态的永久许可证。外部维护 fence 的真实性/持续有效性始终由管理员负责，工具不可能从 hash 证明远端服务停止。
- 本轮不访问 lab、不停止实际业务服务、不 push/部署。定向测试不等同全仓或真实流量验收。

### 最终本机定向证据

- Go 1.27.3；`go test -v -race ./internal/panel/store -run 'TestAdministratorStop|TestUpgradeReadiness|TestLoadUpgradeEvidence' -count=1`：**PASS，8.818s，14 个顶层 + 59 个子用例事件，无 skip**（其中新增停止失败对照 24 个）。日志 `/tmp/pp-stop-review-test.log`。首次执行因测试 SQL 字段误写失败，修正为实际 `nodes.host/port` 后复验通过；未掩盖失败。
- 最终专有 PG：`/tmp/pp-stop-review-pg-mCNrTP/data`，仅私有 Unix socket，随机端口；每例随机 schema 已清理，PG 由 trap 正常停止。无共享系统服务变更。
- `go test ./cmd/panel -count=1`：PASS（此条未注入 PG，原 CLI PG 用例按原约定 skip，不能宣称该用例实际验证）；`go vet ./internal/panel/store ./cmd/panel`：PASS；`git diff --check`：PASS。
- 新增正例在同一次运行实际确认 periods=1、owners=1、带管理员核对来源的 audit=1、schema_migrations=5；所有失败对照保留 registry=4 且没有 0005 新表。
