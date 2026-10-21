# 集成验收记录

状态：**主体实现、唯一集中审查的定点修复、Windows 真实联调和三平台发布构建校验已完成；不是完整生产验收通过声明。** 本记录取代早期模块报告中的“数据库未注入”“最终回归进行中”等阶段状态。历史暂停记录见 [交接记录](pause-handoff-20261020.md)。

## 环境与边界

- Windows / PowerShell 7，Go 1.27.3、Node 24.22.0、官方 Xray 26.3.27。
- PostgreSQL 17.11 / Redis 7：仅隔离测试服务，经本地 SSH 隧道 `127.0.0.1:15432` / `16379`，专属账号/库；未在数据库宿主运行 Agent/Xray。
- 浏览器：Playwright Chromium 156.0.8078.4（build 1248），缓存位于 `E:\SDK\Caches\proxypanel\playwright`。
- 未启动 WSL；未进行 Linux 或公网部署。源码未自动 Git commit / push。

## 已实际完成的检查

| 检查 | 入口 | 结果 |
|---|---|---|
| 基础 Go | `go test ./... -count=1 -timeout 120s`、`go vet ./...` | 通过；独立基础命令无 DB 环境时会跳过可选 DB 测试，不能以此代替下一项 |
| 完整隔离集成 | `PANEL_BROWSER_TEST=1`，移除 `PANEL_BROWSER_ONLY`，`scripts/Invoke-IntegrationTests.ps1` | API 21.632s、store 39.040s、maintenance 23.956s、e2e 43.464s，全部通过；该轮无 FAIL/SKIP |
| API | `TestPanelFlow` | 登录/CSRF、资源、批量预览/原子提交、过期预览、幂等重放/冲突、注册、用量去重/异载荷拒绝、回执/空授权快照、恢复写门禁/退出双确认 |
| 快照 generation | TestPanelFlow 快照断言 | 相同客户端 revision+generation 返回 204；不同 generation 或缺状态返回完整 200 |
| 权威 store 回归 | `authoritative_regression_test.go` | 业务/修订原子发布、新节点关联、跨服务器传播、周期推进/额度恢复/新周期入账、205 成员及 205 节点无截断；大列表用例 3.93s |
| 恢复事务 | `TestRecoveryGenerationTransaction` | 注入 Agent UPDATE 失败，门禁/代次均不部分提交；正常进入生成新代次并统一 Agent 登记值，2.79s |
| 维护任务 | 真实 PG maintenance 集成 | 告警去重/恢复、单执行者、保留清理、持久 outbox 失败重试通过；通知接收端为本地模拟，不是外部 Telegram |
| 完整真实链路 | `TestPlatformRealCore` | PG/Redis→API→Agent→真实 Xray→导出 Xray 客户端配置；69,632 字节持久入账；真实 UDP 回显 25 字节；批量撤权后已有 TCP 与 UDP 关联均在 **1.953s 内关闭**，新连接失败、订阅 403 |
| REALITY+Vision | `TestXrayVLESSRealityVision` | 载荷 81,920 字节、下行计数 82,330；停核心后新连接失败；TLS decoy 为本地自签名，不是公网证书验收 |
| 真实浏览器 | 同轮 `TestPlatformRealCore` | 无 mock；12 管理员页、5 成员页（390px 移动视口）、界面建服务器、成员访问管理员 API/路由被拒；无 pageerror |
| Agent 独立检查 | Sol 测试任务 | 92 个顶层测试、6 个真实核心顶层测试、vet 通过；空快照撤权 99ms、本地到期撤权 1.047s；未运行 race |
| 前端 | typecheck、test、build | 65 测试通过，类型与生产构建通过 |
| npm 依赖 | Vitest 升至 5.0.5 后 `npm audit` | 当轮 0 漏洞；不是未来安全状态承诺 |
| 发布 | `Build-Release.ps1`、`Test-Release.ps1` | Windows amd64、Linux amd64/arm64 构建及重复构建通过；每包 66 文件、65 条 SHA256，资源内容/文件清单/二进制架构吻合 |
| 清单可移植性 | Windows Git Bash `sha256sum --check --strict SHA256SUMS` | 三平台清单全部通过；修复 CRLF→LF；旧 CRLF 被校验器正确拒绝；不是 Linux 运行测试 |
| 安装脚本 | Git Bash `bash -n deploy/manage-linux.sh` | 语法检查通过，未执行安装 |

真实代理测试仅覆盖本机受控 VLESS 节点。为避免私网直连造成假成功，测试将导出配置的私网直连规则替换为强制代理路由并使用随机 SOCKS 端口；协议和凭据仍来自真实订阅。UDP 为经 VLESS 连接承载的真实 UDP 回显，不代表所有 UDP/QUIC 协议矩阵。

## 集中审查与联调问题闭环

唯一 Sol 集中实现审查已完成；未重复启动广泛 review。八项发现均已定点修复并完成相关回归：

1. 到期重建与快照交错可复活授权：序列化应用、复核身份/修订，旧权限不能覆盖新撤权。
2. 部分到期误停其他节点：按全部已提交目标重建，未变化实例保留 PID/generation/hash。
3. 普通业务写入与修订发布不原子：同事务发布变更和任务。
4. 跨月周期不推进：读/用量/批量路径推进，面板每 15 秒 tick；当前周期保持，待生效规则按边界应用。
5. 新节点未关联已注册 Agent：建立关联并按 server 传播；迁移节点通知新旧服务器。
6. 内部列表被 200 条上限截断：内部完整分页，公开分页保留上限；快照批量加载证据，避免逐成员重复查询。
7. 将 64 MiB 业务流量误作载荷上限：改为实际 JSON 1 MiB 限制；3 GiB 计量通过，int64 溢出拒绝。
8. HTTPUpgrade 投影缺失/静默降级：Xray/sing-box/v2rayN 保留 path/host；Mihomo 明确拒绝不支持组合。

真实联调还修复了注册 SQL 字段歧义、服务端错误判断快照 204、PATCH null/omitted 三态、开发模式公网绑定、恢复退出确认不足、Agent 锁死、维护清理 SQL 参数类型及告警 adopt 统计。

最终数据库回归修正了两个测试问题（已关闭周期夹具起点错误、Agent 对象引用比较），并修复快照重复查询性能问题；没有通过削弱断言掩盖失败。恢复代次改成密码学随机正整数（≤2^53-1），与门禁同事务更新；随机化不能替代逐节点核对。

发布检查实际发现 Windows CRLF 清单不被 `sha256sum` 正确读取，现强制 UTF-8 无 BOM、LF，并加入拒绝旧格式的回归。重复构建无嵌套或旧文件残留，文件哈希不变（清单换行格式除外）。

超时遗留 `ppe2e_7c514eeb47452c5f`、`ppreg_98c759e74907` 均在核对夹具后按精确名称清理；成功测试使用自身清理逻辑。未执行无范围 DROP、FLUSHALL、Docker prune 或按名称杀全部 Xray。`.local`、密钥、依赖及 build 的 Git 忽略已核对；发布清单不含这些路径，打包配置为占位示例。

## A01–A16 验收证据映射

此表是证据覆盖说明，不把部分自动化覆盖等同于整项生产签收。完整要求以主计划为准。

| 项 | 当前证据 | 尚不能由本轮证明的部分 |
|---|---|---|
| A01 身份与越权 | auth/CSRF、成员 API/路由拒绝、批量管理员保护相关自动化 | 全部并发账户生命周期场景的生产验收 |
| A02 继承/覆盖 | access 单测、预览失效与空授权 PG 用例 | 全组合人工验收 |
| A03 批量任务 | PG 原子提交、幂等/冲突、任务/回执流程 | 多实体节点同时部分失败的实际演练 |
| A04 周期与额度 | 周期单测、PG 自动推进/待生效规则/补报与额度检查 | 跨期长时间离线联合演练 |
| A05 组与导出 | 权限/导出器测试、205 节点权威快照、实际 Xray 导出流量 | 所有模板与客户端组合 |
| A06 硬撤权 | 本机 TCP/UDP 1.953s、空快照真实核心测试 | 公网多节点规模与全部传输 |
| A07 配置与回退 | Agent 校验/乱序/到期交错回归、未受影响节点保留 | 目标 Linux 升级回滚实机 |
| A08 可靠上报 | PG 去重/异载荷拒绝、SQLite 队列/基线测试、实际入账 | 生产持续故障压力 |
| A09 重启/跨期账本 | Agent 状态/计量测试、PG 周期/补报回归 | 长时间跨期与主机故障联合场景 |
| A10 离线到期 | 已下发到期本地撤权 1.047s、旧权限不复活确定性回归 | 公网断网/重启完整联合演练 |
| A11 依赖与容量故障 | 队列/失败路径及 limiter 自动化 | 真实磁盘耗尽、DB/Redis 生产故障演练 |
| A12 可观测与 UI | 真 PG 告警/outbox、真实桌面及移动浏览器 | 外部 Telegram 真送达、全部断点/错误状态视觉验收 |
| A13 Linux 运维 | 三平台构建、包校验、安装脚本语法 | 安装/启停/重启/升级/卸载实机：未授权、未执行 |
| A14 灾备 | 恢复门禁/随机代次事务、Agent 显式协调 | 真备份恢复、RPO/RTO 和缺口核对：未执行 |
| A15 客户端 | 导出器测试、本地 Xray/REALITY+Vision 真流量 | sing-box/Mihomo/v2rayN 应用与完整公网矩阵：未执行 |
| A16 环境约束 | SDK/env 备份、忽略规则、隔离资源及受限清理记录 | 环境回退未实际执行；不以此改动现有工具 |

## 剩余验收边界

- 未获准 Linux/公网节点；不得通过 WSL 或既有数据库宿主绕过限制。
- 实际客户端矩阵、外部 Telegram、真灾备 RPO/RTO、多节点负载及真实磁盘故障尚未验证。
- `go test -race` 未执行：当前 CGO=0，无可用 gcc/clang；确定性并发用例不能等同 race detector。
- Linux 包为交叉编译产物，发布文件哈希校验不证明目标系统运行正确。

## 证据索引

本会话 scratch：`E:\Agent\.pi-desktop\data\scratch\de63322a-9a13-46d0-a66c-b9f9ba07a08c`（会话删除后可能清理；长期结论保存在本文件）。

- `integration-final-2.log`：最终真实集成，无 FAIL/SKIP；`integration-final.log` 为已修复的首轮失败历史。
- `go-final.log`；`go-agent-test-corrected.log`；`go-xrayreal-test-corrected.log`；`web-*.log`：Go、独立 Agent 与前端。
- `release-final.log`、`release-repeat.log`、`release-lf-build.log`：首次、重复及清单修复后构建。
- `release-verification-final.log`、`sha256sum-<platform>.log`：最终文件/哈希/架构校验；实际清单位于各 `build/<platform>/SHA256SUMS`。
