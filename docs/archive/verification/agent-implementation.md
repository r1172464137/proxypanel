# 自研 Go Agent 实现与验收记录

> 历史阶段报告注记（2026-10-21）：本文件是 Agent 单模块阶段的实现/测试记录，其中“Linux 仅编译未运行”“未 SSH / 未操作远程主机”“未启用 WSL”“Linux、完整 UDP/QUIC/公网矩阵及 race 仍未验证”等表述只对该阶段成立，**不改写为当时已通过**。后续独立 Docker 容器验收（A13 容器生命周期、A14 隔离恢复）与最终逐条状态以 [final-acceptance.md](final-acceptance.md)、[docker-lifecycle.md](docker-lifecycle.md) 为准；本文件下文“最终集成更新”所指的 [integration.md](integration.md) 只是历史集成记录。

> 最终集成更新：以 [统一验收记录](integration.md) 为当前状态。面板已支持 revision+control_generation 双比较并通过真实 PG 回归，原第 6 节第 9 项缺口已关闭。deploy 已提供 systemd/安装卸载示例，但未在 Linux 执行。最终真实全链路验证 TCP/UDP 关联撤权 1.953s；独立测试验证本地到期撤权 1.047s。下文单模块阶段范围不代表全项目最新覆盖；Linux、完整 UDP/QUIC/公网矩阵及 race 仍未验证。


状态：实现完成；单元测试、本机真实 Xray 受控测试、Windows/Linux 交叉编译均通过。Linux 仅编译未运行。
范围与文件所有权：`cmd/agent/`、`internal/agent/`（含模块内测试）、本文件。未修改根 `go.mod/go.sum`、
`internal/contracts/`、`internal/panel/`、`web/`、`scripts/`、`docs/archive/`。未读取 `私钥.txt`、未使用 `.local/` 凭据、
未 SSH、未操作远程主机、未启用 WSL。测试临时目录由 Go 的 `TMPDIR` 落在会话 scratch
（`E:\Agent\.pi-desktop\data\scratch\<session>\...`），真实核心只在本机 `E:\SDK\Xray\v26.3.27\xray.exe` 运行。

工具与依赖：Go 1.27.3，module `proxypanel`；直接依赖仅标准库 + 已锁定 `modernc.org/sqlite v1.39.1`（Agent 侧未新增第三方依赖）。
Windows 受限 ACL 使用模块图内已有、版本已锁定的 `golang.org/x/sys v0.36.0`（`secure_windows.go`）。
**需主负责人执行 `go mod tidy`**：`golang.org/x/sys v0.36.0` 会从 indirect 移到 direct require 块（版本不变，仅位置移动）。

## 1. 交付文件

```text
cmd/agent/main.go                       CLI：-config / -version / -check / -accept-generation
internal/agent/config/config.go         JSON 配置加载、严格校验、回环 HTTP 守卫
internal/agent/control/client.go        Agent HTTP 协议（data 信封、204、带 control_generation 的快照请求、退避）
internal/agent/xray/build.go            快照/实例校验、授权过滤、管理面注入、哈希、证书路径约束
internal/agent/xray/version.go          核心版本固定校验（仅接受已验证版本）
internal/agent/xray/runner.go           固定命令行 Runner（run -test / run -c / api statsquery / version）+ 日志脱敏
internal/agent/xray/process_{windows,unix}.go   进程创建与受控停止
internal/agent/xray/pidfile.go          遗留进程记录（pid + 创建身份 + 完整可执行路径 + 实例配置路径）
internal/agent/xray/procidentity_{windows,linux,other}.go  进程身份取证与终止
internal/agent/xray/manager.go          每节点独立核心子进程生命周期、回滚、采样查询、世代、遗留进程归属校验
internal/agent/state/store.go           SQLite：身份、实例记录、采样基线、待上报队列、回执 outbox、世代接受标记
internal/agent/state/lock_{windows,unix}.go     独占 state 目录锁
internal/agent/state/secure_{windows,unix}.go   受限 ACL / 0700 权限
internal/agent/reporting/*.go           计数器→成员增量、可靠上报、回执重发
internal/agent/telemetry/*.go           心跳主机指标（跨平台）
internal/agent/supervisor/supervisor.go 注册、快照同步、安全协调、采样、上报、心跳、本地到期循环
```

## 2. 配置与使用

配置为 JSON（未知字段直接报错）：

```json
{
  "panel_url": "https://panel.example.com",
  "registration_code": "once-only-code",
  "token": "",
  "agent_id": "",
  "xray_path": "E:\\SDK\\Xray\\v26.3.27\\xray.exe",
  "state_dir": "C:\\ProgramData\\proxypanel-agent",
  "poll_interval_seconds": 5,
  "stats_interval_seconds": 5,
  "heartbeat_interval_seconds": 15,
  "allow_loopback_http": false
}
```

- 间隔缺省 5/5/15 秒，允许 1–3600 秒；上报周期与轮询周期相同（契约未给单独字段）。
- `panel_url` 绝对 http(s) URL，禁 userinfo/query/fragment；`http` 仅在 `allow_loopback_http=true` 且
  `localhost`/`127.0.0.0/8`/`::1` 时允许。生产为 HTTPS 且不提供跳过校验开关。
- `xray_path` 必须是绝对路径常规文件，且核心版本必须属于已验证清单（当前 `26.3.27`），否则 Agent 拒绝启动。
- `state_dir` 自动解析为绝对路径；目录权限 Windows 用显式受限 DACL、Unix 用 0700，数据库文件 0600，并加独占锁。
- `token`/`agent_id` 可省略：首次用 `registration_code` 注册，token 落库（不写日志），注册后建议从配置移除注册码。

命令：

```powershell
. .\scripts\Use-DevEnvironment.ps1
go build ./cmd/agent
$env:GOOS='linux'; $env:GOARCH='amd64'; go build -o agent-linux-amd64 ./cmd/agent
.\agent.exe -version
.\agent.exe -config .\agent.json -check
.\agent.exe -config .\agent.json
# 面板恢复/世代变化后的显式本地协调（必须先停止 Agent）：
.\agent.exe -config .\agent.json -accept-generation <control_generation>
```

## 3. 行为实现摘要

### 3.1 控制面
- `POST /api/v1/agent/register`：一次性注册码 → `{agent_id,token,control_generation}`；token 持久化后不再注册。
- `GET /api/v1/agent/snapshot?revision=N&control_generation=<已应用世代>`：204 = 无变化；200 = 完整快照（`instances:[]` 也是完整撤销）。
  `control_generation` 为**可选附加参数**（契约只定义 `revision`），用于让面板在世代变化时返回 200 而不是 204；面板可忽略。
- `POST /api/v1/agent/applied`：逐实例结果 + 本地 outbox，收到 `accepted:true` 才删除。
- `POST /api/v1/agent/usage`：仅 `accepted`/`duplicate` 删除本地批次；其余（含 2xx 非持久确认）保留重试。
- `POST /api/v1/agent/heartbeat`：版本、核心版本、已应用修订、运行时长、队列深度、主机与运行指标、逐实例状态
  （协调模式额外带 `coordination_required=1`）。
- 非 2xx 一律失败并有界指数退避（1s→60s，±20% 抖动）；401/403 保留队列并持续告警。

### 3.2 快照校验与乱序防护
`schema_version` = `proxypanel.agent/v1`；`control_generation` 非空；`revision ≥ 1`；`instance.id == node_id` 且匹配
`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`（同时是目录名）；实例数 ≤ 64；`expected_user_count == Σlen(users)`；
用户 email/member_id 非空且实例内唯一；`issued_at` 非零。任一失败整体拒绝且不改动运行实例。
同世代 `revision <= 已应用修订` 直接忽略（不重启、不回执新版本）。

### 3.3 控制世代：安全协调，绝不自动信任（本次修复）
- 世代与本地已应用世代不同（包括回退到旧备份世代）时，Agent **不应用**该快照：
  保持当前已知（只会更严格）的授权继续运行，写入 `pending_generation`，日志告警，
  并向面板回执 `state=coordination_required`（同一世代+修订只回执一次，避免每 5 秒刷屏）。
- 为什么“继续运行”是安全的：本地授权只由已应用快照与本地到期过滤逐步收窄，
  已撤销成员不会被本地凭空放回；协调期间不再接受任何新授权，因此旧备份无法恢复已撤销用户。
- 只有管理员在节点上显式执行 `-accept-generation <该世代字符串>`（Agent 必须已停止，需持有 state 目录锁）
  才会记录 `accepted_generation`；下一次同步只有世代**完全等于**该值时才强制全量重建并推进世代，
  成功后清除接受标记与 `pending_generation`。默认路径永不接受新世代。
- 首次注册（本地世代为空）走正常路径：注册响应给出世代，首个快照正常应用。

### 3.4 每节点独立核心实例
- 独立实例 ID = `node_id`，目录 `state_dir/instances/<node_id>/`（config.json、config.rollback.json、xray.log、xray.pid）。
- 有效配置哈希不变则不重启；变化先 `xray run -test -c`，通过后才受控停止旧进程再启动；启动后轮询本机管理 API（≤5s）
  确认真的在服务，失败换新端口重试一次。
- 停止前用固定命令 `xray api statsquery --server=127.0.0.1:<随机端口>`（`-reset` 缺省 false，非清零）采集末次计数。
- 新快照为空：停止全部实例并逐实例回执 `stopped`；授权变严格时旧进程一定先终止，旧连接随进程结束。
- 校验/启动失败：终止旧进程 → 用“最近可用技术配置 + 当前授权”重建 → 仍失败则停止并回执 `failed`，
  不推进 applied revision，按实例级退避重试（回执 `deferred`）。回滚文档只含当前授权成员。

### 3.5 遗留进程回收：必须可证明归属（本次修复）
- 启动核心后把 `{pid, 进程创建身份, 完整可执行路径, 实例配置路径, 实例 ID, 核心世代}` 写入 `xray.pid`。
- 回收条件（缺一不可）：进程仍存在；创建身份与记录**完全一致**（防 PID 复用）；完整可执行路径与记录一致、
  且等于配置的核心路径；记录中的配置路径等于当前实例配置路径；平台能提供命令行时必须引用该配置路径。
- 任一条件无法证明（记录缺失/不可解析/无创建身份/身份不符/取不到进程信息）→ **不杀**，返回
  `ErrUnverifiedProcess`，实例回执 `failed` 并提示人工处理，绝不“只要叫 xray.exe 就杀”。
- 本管理器自己启动且仍在运行的进程（`owned` 集合）永不被当作遗留进程回收。
  （实测发现：缺此保护时本地到期重建会误杀自己的核心并丢失末次用量；已修复并有回归测试。）
- Windows：`OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION)` + `QueryFullProcessImageName`（完整路径）+ `GetProcessTimes`（创建时间）。
  Linux：`/proc/<pid>/{stat,exe,cmdline}`（boot 时间 + starttime 作为创建身份、readlink 得完整路径、cmdline 需含实例配置路径）。
  其它平台无法取证 → 一律拒绝回收。

### 3.6 配置安全边界
- 顶层键只允许 `log/inbounds/outbounds/routing/policy/stats`；`api`、`dns`、`fakedns`、`reverse` 等拒绝。
- 入站协议只允许 `vless/vmess/trojan/shadowsocks`；出站只允许 `freedom/blackhole`；禁 `balancerTag`；保留 tag `agent-api`。
- 入站必须声明 `settings.clients[]` 且每个 client 带 `email`（按成员授权与计量）；未授权/过期 client 在 Agent 侧移除；
  全部撤销时保留入站但 clients 为空（真实核心已验证可启动）。
- `streamSettings` 原样保留（不禁止合法 VLESS 传输参数），但 `network`/`security` 取值白名单校验；
  TLS 证书/私钥必须绝对路径且位于 `state_dir/certs/` 内，`..`/相对路径拒绝。
- `log.access` 强制 `none`，其它日志文件路径剥离（不让面板指定任意文件写入）。
- 注入：`api.tag=agent-api`（仅 StatsService）、`stats:{}`、`policy.system` 四项统计开关、
  `policy.levels.<level>.statsUserUplink/Downlink`（见 §5）、`127.0.0.1:<随机端口>` dokodemo-door 入站、指向该 tag 的 routing 规则。
- 不执行任何 shell；子进程参数固定；子进程 stdout/stderr 先脱敏（token/注册码）再写实例日志（单行截断、8MB 滚动）。

### 3.7 用量计量与可靠上报
- 计数器 `user>>>email>>>traffic>>>uplink|downlink` 按 email→member_id 映射（client email 等于 member_id 也支持）。
- 每次核心启动产生新 `core_generation`；基线按 (node, generation, counter) 隔离；计数器回退按 0 起算，绝不出负值。
- 采样基线与待上报批次同一 SQLite 事务；批次含 node/member/世代/起止时间/上下行。
- 队列上限 2000 批 / 200000 行；超限或本地持久化失败 → 停止受影响实例并告警，不静默丢数据。
- 只有 `accepted`/`duplicate` 删除批次；409、超时、非 2xx 保留并记录 attempts/last_error。
- 已下发到期由 Agent 本地执行（面板离线也执行）：到点重建受影响实例；重启时先按当前时间过滤过期用户再恢复运行。

### 3.8 state 目录保护
- Windows：`SetNamedSecurityInfo` + 显式 DACL + `PROTECTED_DACL_SECURITY_INFORMATION`，只授予当前账户与 `NT AUTHORITY\SYSTEM`，
  ACE 可继承，因此实例/证书/日志目录同样受限；不再依赖 0600（Windows 上文件模式不是访问控制）。
- Unix：目录 0700、数据库 0600。两者都在写入任何秘密前执行，失败则拒绝启动（fail closed）。

### 3.9 并发与锁顺序（本次修复的真实联调死锁）
- 全局锁顺序固定为 `instance.mu`（生命周期锁）→ `Manager.mu`（映射/owned 叶子锁）。
  `Manager.mu` 只保护实例映射与 owned pid 集合，**绝不在持有它时获取实例锁**，也不在持锁时做 I/O、写日志或调用回调。
- 读路径（`Snapshot`、`NextExpiry`、`ExpiredInstances`、`QueryStats`）不再获取实例锁，
  改读实例发布出的不可变 `instanceView`（`atomic.Pointer`）。因此配置校验/启动进行中不会阻塞心跳、统计、到期判断与停机。
- 已修复的具体缺陷：
  1. `QueryStats` 曾持有 `Manager.mu` 再获取实例锁，`SeedLastGood` 同样；而 apply 侧持有实例锁后再取 `Manager.mu`
     （`reportStarted` 的 owned、`sampleAndStop`/`stopInstance` 的 owned 与映射更新）→ ABBA 死锁，
     心跳 `Snapshot` 与停机 `StopAll` 也被一起卡住。
  2. 最终采样 hook 在 manager 持有实例生命周期锁时被调用，而 `setPersistenceError`/`setQueueOverCapacity`
     同步调用 `StopAll` → 对同一把不可重入锁自死锁。现改为 `stopAllAsync`（独立 goroutine、30s 有界上下文、单飞）。
  3. `stopInstance` 不再为未跟踪的 id 凭空创建实例（只做查找）。
- 语义未放宽：停止实例、撤权重建、队列/持久化失败停实例、遗留进程归属校验、世代协调全部保持原样；只是锁的范围与顺序被收紧。

### 3.10 快照应用与本地到期的串行化（本次修复）

- 新增 supervisor 级 `applyMu`：**快照应用**（含在锁内重新读取并校验 identity/revision、Manager 应用、
  逐实例持久化、`applied_revision`/世代持久提交、回执入 outbox）与**本地到期重建**（读记录 → 构造目标 → 应用 → 持久化）
  全程互斥。回执的网络投递放在解锁之后，避免面板不可达时卡住到期重建。
- 为什么必须串行：到期重建若在“读记录”之后、“应用”之前被新快照插入，新快照已撤销某成员并提交了 revision，
  到期重建随后用**旧目标**回写，就会把该成员重新授权；此时 revision 未再变化，面板一直返回 204，撤销被永久覆盖。
- 读取与校验都在锁内完成：`applySnapshot` 不再信任调用前读到的水位，进入串行区后重新 `Identity(ctx)` 并重新比较
  revision/世代；`expiryOnce` 的实例记录读取也在锁内，**过时目标不会在加锁后被盲目使用**。
- 到期重建改为**全量协调**：从已提交的**全部**实例记录构造目标（不再只传过期实例），`Force=false`。
  过期实例因移除过期 client 而 hash 变化→重建；其余实例 hash 不变→`unchanged`，**不重启、不停机、不重写配置**。
  （旧实现只传过期实例，`Manager.Apply` 会把不在目标列表里的其它节点全部停止。）
- 记录为空时不调用 `Manager.Apply`（避免把已跟踪实例全部停掉）；任一记录无法构造目标时整轮中止（避免“部分目标”导致误停）。
- `unchanged` 分支现在会在授权不变的条件下刷新 `nextExpiry` 元数据（先 `inst` 锁、再 `publish`），
  使本地到期触发点前进，不会对同一实例每秒重复触发。
- 锁安全：`applyMu` 只在 supervisor 顶层获取，内部仅再取 Manager 的实例锁与叶子锁，
  **不引入 `instance.mu → applyMu` 的反向顺序**；采样 hook 中的失败停机仍走 `stopAllAsync`（不等待、不取 applyMu）。
## 4. 测试与证据

命令均在 `. .\scripts\Use-DevEnvironment.ps1` 之后执行：

| 命令 | 结果 |
|---|---|
| `go test ./internal/agent/...` | 通过（config/control/reporting/state/supervisor/telemetry/xray） |
| `go build ./cmd/agent` | 通过 |
| `go vet ./internal/agent/... ./cmd/agent` | 通过 |
| `GOOS=linux GOARCH=amd64 go build ./cmd/agent` | 通过（仅编译） |
| `GOOS=linux GOARCH=arm64 go build ./cmd/agent` | 通过（仅编译） |
| `go test -race ./internal/agent/...` | 未执行：本机 `CGO_ENABLED=0` 且无 gcc（`-race requires cgo`） |
| `go test -tags xrayreal ./internal/agent/xray/ -run TestRealXray` | 通过（连续 3 次稳定） |
| `go test -tags xrayreal ./internal/agent/supervisor/ -run TestReal` | 通过（连续 2 次稳定，含撤权长连接与 Run 多 loop 测试） |
| `go test -count=3 ./internal/agent/xray/ ./internal/agent/supervisor/ -run '并发相关用例'` | 通过（并发用例连续 3 轮稳定） |

单元测试覆盖要点：
- 快照空列表：停止全部实例、回执 `stopped`、推进修订、删除本地实例记录。
- 世代/乱序（本次新增）：新世代默认拒绝（`coordination_required`，实例不停不重启、水位不变、只回执一次）；
  旧备份世代不会恢复已撤销成员；**同 revision 不同 generation 不被忽略**；显式接受后才全量重建并清除标记；同世代正常升级仍工作。
- 遗留进程（本次新增）：可证明归属的真实子进程被回收；外来进程/其它路径/身份缺失/PID 复用/其它实例配置/损坏记录全部拒绝且不杀；
  无法证明时 `Apply` 回执 `failed` 且不启动；自己拥有的运行中进程不被回收。
- 核心版本（本次新增）：`26.3.27` 接受；`26.3.26/25.9.11/1.8.24/27.0.1/空` 拒绝；不支持时 `Run` 直接失败且不启动实例。
- state 保护（本次新增）：Windows 断言 DACL 受保护且仅含当前账户 + SYSTEM（无 Everyone/Users/Authenticated Users）；Unix 断目录 0700、库 0600。
- 过期：应用时过滤过期用户（clients 变 0）；本地到期按 hash 重建受影响实例（其余实例 unchanged，不重启）并持久化撤销后的用户集，末次用量先入队。
- HTTP 失败/认证：非 2xx 保留状态与队列、修订不推进；401/403 不当作成功；回执仅 accepted 才删除。
- SQLite：基线+队列同事务、计数器回退重算、世代隔离、非持久状态保留。
- 防路径：实例 ID 越界（`../escape`、`a/b`、`.`）、证书路径穿越/相对/越出 `certs/` 全部拒绝。
- 回滚安全：新配置被核心拒绝时回滚只保留当前授权成员（真实检查启动配置里的 clients 数量与 email）；无法回滚则停止并回执失败。
- 独占 state 目录；`Run` 取消后停止全部核心并标记 stopped，不留不受管进程。
- **并发/锁顺序（本次新增）**：
  - `TestConcurrentLifecycleAndReadersDoNotDeadlock`：真实并行 apply（交替授权集合触发重启）+ `QueryStats` + `Snapshot`
    + `NextExpiry`/`ExpiredInstances` + `Stop` + 收尾 `StopAll`，要求 20s 内全部收敛。
  - `TestReadersAreNotBlockedBySlowApply`：apply 持有生命周期锁 500ms 期间，`Snapshot`<250ms、`NextExpiry`<100ms、
    `ExpiredInstances`<100ms、`QueryStats`<300ms（旧实现会阻塞整个 apply 时长）。
  - `TestStopAllBoundedWhileApplyInFlight`：apply 进行中调用 `StopAll`，12s 内返回且实例全部停止。
  - `TestRunConcurrentLoopsShutdownBounded`：`Run` 全 loop（快照/统计/上报/心跳/到期）并行 + 快照改动 + 本地到期重建，
    取消后 20s 内退出、无实例残留运行。
  - `TestPersistenceFailureDuringApplyDoesNotDeadlock`：apply 持锁期间制造本地持久化失败（关闭 store），
    过去会在 hook 内同步 `StopAll` 自死锁；现在 apply 与异步停止都在界内完成。
  - **回归有效性已验证**：把 `QueryStats`/`SeedLastGood` 临时改回“持 `Manager.mu` 再取实例锁”后，
    `TestConcurrentLifecycleAndReadersDoNotDeadlock` 在 20s 内无法收敛而失败；
    把 `setPersistenceError` 临时改回同步 `StopAll` 后，`TestPersistenceFailureDuringApplyDoesNotDeadlock` 40s 超时失败。
    恢复修复后两者均通过。
- **快照/到期串行化（本次新增）**：
  - `TestExpiryRebuildDoesNotResurrectUserRevokedBySnapshot`：用测试钩子把到期重建**停在“读完记录”之后**，
    此时并发推进快照撤销 bob：断言该快照**不能**在到期重建持有串行锁时提交（否则直接失败）、
    两轮完成后 bob 既不在提交记录里也不在 node-1 最后启动的配置里、revision=2 且随后 204 的周期不会复活 bob、
    node-2 未被误停。
  - `TestExpiryRebuildLeavesOtherNodesRunning`：两个节点，仅 node-1 的某用户到期；断言 node-1 精确重建一次
    （pid/世代/配置 hash 变化、alice 从配置与记录中消失），而 node-2 的 **pid、core_generation、config sha256 全部不变**、
    `starts=1/stops=0`、仍为 applied；并断言第二次到期扫描不再重复重建。
  - `TestRunSerializesExpiryAndSnapshot`：走真实 `Run` loop（快照 loop 与到期 loop 同时触发：推进时钟使 alice 到期 +
    同时下发撤销 bob 的 revision 2），多轮循环后 bob 不复活、node-2 的 pid/世代/配置 hash 不变、revision=2、取消后 20s 内退出。
  - **回归有效性已验证**：去掉 `applyMu` 的加锁（还原旧实现）后 `TestExpiryRebuildDoesNotResurrectUserRevokedBySnapshot`
    立即失败（“the snapshot apply committed while a local expiry rebuild held the serialization lock”）；
    把到期目标改回“只含过期实例 + Force”后 `TestExpiryRebuildLeavesOtherNodesRunning` 失败（node-2 的 pid 记录被停止删除）。
    恢复修复后两者均通过（并用 `-count=3` 复跑稳定）。

真实核心证据（`-tags xrayreal`，本机 Xray 26.3.27）：
1. `TestRealXrayInjectedConfig`：注入管理面的配置被 `xray run -test` 接受，进程启动后 `api statsquery` 可查询，可受控停止。
2. `TestRealXrayEmptyClients`：撤销最后一个用户（clients 为空）配置仍被核心接受。
3. `TestRealXrayPerUserUsage`：真实 VLESS 连接 + 本地 echo 目标后出现 `user>>>alice@example.test>>>traffic>>>uplink` 并可归因成员。
4. `TestRealCoreLifecycleAndUsage`：完整 Agent（真实 HTTP + 真实 SQLite + 真实核心）应用快照 → 真实流量 → 采样上报 → 撤销后重启为 0 用户 → 旧凭据新连接被拒。
5. `TestRealCoreRunConcurrentLoops`（本次新增）：真实核心 + 真 `Run` 全 loop；撤销→重授权→循环上报用量后取消，
   4ms 内完成关闭、无实例残留、该入站端口不再监听。
6. `TestRealCoreRevokeTerminatesEstablishedConnection`（长连接持续收发 ≥2 轮后撤权）：
   - 空快照（实例全部删除）：已建立连接在 **99ms/50ms 量级**结束（远低于 60s 预算），事后新连接被拒；
   - 本地到期（面板未参与）：已建立连接在 **约 1.05s** 内结束（含受控停止+重建），且**末次用量已入队**（780 字节/1 批）。
   两者都断言“连接被真正终止”，不是“仅拒绝新连接”。

手工验证：
- `agent.exe -accept-generation gen-demo`（Agent 停止）→ 打印已应用世代并写入接受标记；
  同一命令在 Agent 运行时失败：`stop the agent before accepting a control generation: ... already in use by another agent`。
- `icacls <state_dir>` → 仅 `PC-COMPILE\Compile` 与 `NT AUTHORITY\SYSTEM`（含继承 ACE），无 Users/Everyone。

## 5. 实现期间的关键发现（需面板侧配合）

1. **Xray 26.3.27 用户级统计需要 policy.levels**：仅在 `policy.system` 设置 `statsUserUplink/Downlink` 时，
   `api statsquery` 只返回 `inbound>>>...`，不产生 `user>>>email>>>traffic>>>...`。必须同时设置客户端实际使用 level
   （默认 0）的 `policy.levels["0"].statsUserUplink/Downlink=true`。Agent 已自动注入所有 client 引用到的 level，
   面板若自行生成完整配置不要覆盖这两级设置。
2. **快照请求现在附带 `control_generation`**：`GET /api/v1/agent/snapshot?revision=N&control_generation=<世代>`。
   面板应据此实现“世代与本面板当前世代不同 → 返回 200 完整快照”，否则 Agent 会收到 204 而无法感知世代变化；
   该参数为可选，未注册前不带。Agent 侧已做到：即使面板给了完整快照，新世代默认只进入协调状态，绝不自动应用。
3. Windows 无跨平台 SIGTERM：受控停止在 Windows 上是“先采样再强制结束进程”，旧连接必断开（撤权语义成立），
   但核心无优雅关闭钩子；Linux 使用 SIGTERM→超时后 SIGKILL。
4. `applied` 报告状态串未在契约枚举：Agent 发送 `applied` / `failed` / `coordination_required`；
   逐实例状态为 `applied`、`unchanged`、`stopped`、`rollback_applied`、`failed`、`deferred`。面板需按字符串兼容。

## 6. 未完成 / 明确限制

1. 服务托管未实现：无 systemd unit / Windows 服务注册、安装与卸载脚本（属 `deploy/` 与主负责人范围）；
   Agent 不支持二进制自升级（无下载、无自动回滚），升级需替换二进制并重启。
2. Linux 未实际运行：交叉编译通过，进程身份取证（/proc）、文件锁、指标采集未在 Linux 主机验证（未获 Linux 节点授权）。
3. `-race` 未执行（本机无 cgo/gcc），并发正确性依赖确定性延迟注入 + 有界超时用例，而不是数据竞争检测；
   若后续具备 cgo 环境，应补跑 `go test -race ./internal/agent/...`。
4. Windows 主机指标不含网卡字节（无零依赖计数器）；Linux 有 `net_rx/tx_bytes`、`load1`。
5. 遗留进程回收在其它 Unix（darwin 等）一律拒绝（无法取证），需要人工处理；这是有意的 fail-closed 行为。
6. TLS/REALITY 仅静态校验（路径与枚举），未做证书到期检查、自动签发或公网 REALITY 实测。
7. 核心版本校验为精确白名单（`26.3.27`）；升级核心后需同步更新 `SupportedCoreVersions` 并回归，不做“就近兼容”猜测。
8. 撤权时延已在真实核心上测到“秒级”，但仅本机单实例、单连接；未覆盖多节点、UDP/QUIC 长会话、真实运营商网络。
9. 面板侧尚未支持 `control_generation` 查询参数（主负责人后续接入）；在此之前若面板恢复旧备份且 revision 不增，
   Agent 会收到 204，无法感知世代变化（这是面板侧缺口，Agent 无法单方面修复）。
10. Agent JSON 配置未新增字段（证书目录固定 `state_dir/certs/`）；如需自定义需主负责人扩充配置契约。




