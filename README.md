# Proxypanel

私人朋友合租使用的代理管理平台：**Go 面板 + PostgreSQL + Redis + Vue 3/TypeScript + 自研 Go Agent + 独立官方 Xray 进程**。没有支付、订单、返利等商业系统。

## 能力

- 管理员/成员角色、邀请、Cookie 会话与 CSRF、成员独立订阅令牌和 member×node 代理凭据。
- 可复用订阅规则、节点组、连续继承与成员覆盖、批量预览/确认/幂等提交。
- Agent 完整快照、配置校验、逐节点核心实例、硬撤权、离线到期、可靠计量队列和幂等账本。
- 管理员运维页面、成员订阅/用量/凭据/账号页面；Xray、sing-box、Mihomo、v2rayN 输出。
- 告警状态/恢复、持久通知 outbox、受限保留清理、恢复协调与本地 generation 明确接受。

**状态：主体实现、集中审查定点修复、Windows 本地真实链路/浏览器回归、Docker 生命周期与隔离恢复演练、三平台发布两轮校验已完成；尚未达到完整生产验收标准。** A15 客户端矩阵真实 30 格通过但 v2rayN GUI 未验收（`#10` 未完全满足）；执行边界存在一次真实的 WSL 启动（`#12` 非完全符合）；Linux/systemd、公网直连、真实 Telegram 送达与生产 RPO/RTO 仍未验收。逐条状态见 [最终验收状态与 A01–A16 索引](docs/verification/final-acceptance.md)，证据见 [持久证据索引](docs/verification/evidence/README.md)。

## 开发与检查

Windows PowerShell 7，工具与缓存按本项目约定放在 `E:\SDK`：

```powershell
$savedPanelBash = $env:PANEL_BASH                    # 保存原值，结束时恢复
. .\scripts\Use-DevEnvironment.ps1
$env:PANEL_BASH = 'E:\SDK\Git\bin\bash.exe'          # 只用 Git Bash；禁止 WSL / System32 bash 启动器
go test ./... -count=1 -timeout 120s
go vet ./...
Push-Location web
npm ci
npm run typecheck
npm run test
npm run build
Pop-Location
.\scripts\Invoke-IntegrationTests.ps1
if ($null -eq $savedPanelBash) { Remove-Item Env:PANEL_BASH -ErrorAction SilentlyContinue } else { $env:PANEL_BASH = $savedPanelBash }
```

真实数据库测试要求本地受限连接配置，不能使用任意数据库；隧道和测试在同一脚本生命周期内结束。首次浏览器验收在 `web` 执行 `npx playwright install chromium`，再设 `$env:PANEL_BROWSER_TEST='1'` 运行集成脚本。缺少可选数据库环境时 Go 用例会 **SKIP**，不是“已通过数据库测试”。

启动面板、Vite、Agent 的具体配置见 [部署与开发启动](docs/deployment.md)。不提供默认管理员密码，不自动提交真实凭据。Agent 核心固定 **Xray 26.3.27**，并接受 **26.3.23**（闭合白名单，两者均已在隔离 Docker 中完成真实切换与回退），升级须显式更改兼容白名单并回归。

## 发布产物

已生成 `build/windows-amd64/`、`build/linux-amd64/`、`build/linux-arm64/`，包含 panel/agent、前端、迁移、配置与部署示例及 `SHA256SUMS`；每平台 **72 个文件 / 71 条 SHA256**，两轮构建的清单逐字节一致。`build/` 中**确切的** Linux amd64/arm64 发布二进制只做交叉构建、清单与 ELF 架构校验，未在目标平台运行（arm64 架构整体未运行；**Linux amd64 平台的功能验收由 Docker 内 staged 构建完成，其哈希与发布目录不同**）。Windows 发布件的运行证据仅 `agent -version`；Windows 上的真实核心与浏览器功能证据来自本地验收，不由发布件承担。核心 Xray 须另行从官方获取。

```powershell
.\scripts\Build-Release.ps1
.\scripts\Test-Release.ps1
```

发布校验检查完整文件清单、SHA256、源码资源一致性和 PE/ELF 架构。重复构建已验证；输出仅覆盖脚本标记的目录。哈希清单使用 UTF-8 无 BOM、LF，可供 `sha256sum --check --strict SHA256SUMS` 校验。哈希校验不等同于发布者签名或目标系统运行验收。

## 文档

- [冻结实现契约](docs/specs/implementation-contract.md)：接口、授权、快照与计量语义。
- [主执行计划](.pi/plan/合租代理管理平台完整计划-订阅管理-go-面板与自研节点端-20261020-2138.md)：完整范围与 A01–A16 验收。
- [环境与工具](docs/development.md)、[隔离测试数据库](docs/test-databases.md)。
- [部署/升级/卸载](docs/deployment.md)、[备份恢复](docs/recovery.md)。
- [历史集成记录](docs/verification/integration.md)、[基础真实核心](docs/verification/foundation.md)。
- [Agent 实现与测试](docs/verification/agent-implementation.md)、[后端实现](docs/verification/backend-implementation.md)、[前端实现](docs/verification/frontend-implementation.md)、[运维后台](docs/verification/maintenance.md)。
- [最终验收状态与 A01–A16 索引](docs/verification/final-acceptance.md)、[持久脱敏证据索引](docs/verification/evidence/README.md)。
- [Docker 生命周期（A13）](docs/verification/docker-lifecycle.md)、[Docker 隔离恢复（A14）](docs/verification/docker-recovery.md)、[客户端矩阵（A15）](docs/verification/client-matrix.md)。

## 安全与操作边界

- PostgreSQL 为权威，Redis 故障不允许绕过鉴权或丢账；Agent 不直接连接 PG/Redis。
- 新控制 generation 不自动信任；恢复旧备份须先隔离并核对授权/账本，不得靠清空 Agent 状态目录绕过协调。
- 节点配置/成员撤权可能重启该节点核心并中断同节点其他会话；不是零中断热更新承诺。
- 原始 SSH 私钥、`.local`、环境秘密、依赖和构建目录被 Git 忽略；不要把日志、订阅链接或二维码当作可公开测试附件。
- `192.168.5.203` 的授权范围为**本项目独立 Docker 应用测试**（自有容器/网络/卷/镜像 tag，CPU/内存受限，端口只绑回环，跨机只经受保护 SSH 隧道访问）。禁止改动、重启或接管该主机原有业务容器/服务，也不把它当作 Linux/systemd、宿主重启或公网节点验收环境。
- 本机测试固定使用 Git Bash（`PANEL_BASH=E:\SDK\Git\bin\bash.exe`），禁止 WSL 探测/启动/停止；一次脚本自测曾真实启动 WSL，属执行边界不符合项，见 [执行边界事件](docs/verification/execution-boundary-incident.md)。
