# 构建、部署与维护

> 当前验证平台为 Windows。Linux amd64/arm64 构建、systemd/nginx 示例与安装脚本不代表 Linux 运行验收。不得在现有 `192.168.5.203` 数据库宿主上执行本手册的应用/Agent 安装；该主机上实际执行的是**本项目独立 Docker 容器验收**（A13 容器生命周期、A14 隔离还原），它不是宿主 systemd 或主机重启验收。

## 1. 构建与开发检查

工作区 PowerShell 7：

```powershell
. .\scripts\Use-DevEnvironment.ps1
$env:PANEL_BASH='E:\SDK\Git\bin\bash.exe'   # 只用 Git Bash；禁止 WSL/System32 bash 启动器
go test ./... -count=1 -timeout 120s
go vet ./...
go test -tags xrayreal ./internal/agent/xray ./internal/agent/supervisor -run TestReal -count=1 -timeout 120s
.\scripts\Invoke-IntegrationTests.ps1
$env:PANEL_BROWSER_TEST='1'
.\scripts\Invoke-IntegrationTests.ps1 -Run TestPlatformRealCore
Remove-Item Env:PANEL_BROWSER_TEST
.\scripts\Build-Release.ps1
.\scripts\Test-Release.ps1
```

首次安装前端：在 `web/` 执行 `npm ci`、`npx playwright install chromium`。脚本将缓存放在 `E:\SDK\Caches\proxypanel`。浏览器测试要求已执行 `npm run build`。数据库脚本创建 SSH 隧道、加载受限 `.local` 凭据、只连接项目 `15432/16379` 端口，并在同一生命周期关闭隧道；不要把凭据输出到终端。测试创建随机 schema 并清理，不在应用正式 schema 上运行夹具。

`build/<platform>/` 包含面板、Agent、迁移、网页、配置/服务示例及 SHA256SUMS。不打包真实密钥、数据库凭据、SSH 私钥或 Agent 状态。发布时记录 Git commit（本任务未自动提交）与构建命令。核心必须另行从官方发行下载、核对官方 SHA256；当前 Agent 精确接受 **Xray 26.3.27** 与 **26.3.23**（闭合白名单，两者都在隔离 Docker 中做过真实切换与回退验证），不会自动下载或升级。

三平台构建与两轮重复构建已在 Windows 完成并通过：每平台 **72 个文件 / 71 条 SHA256**（三平台合计 216 文件 / 213 条），两轮 `SHA256SUMS` 逐字节一致，Windows agent `-version` 为 `proxypanel-agent 0.1.1`。`Test-Release.ps1` 核对清单完整性、逐文件哈希、打包资源与工作区一致性及 PE/ELF 架构；仅供可信工作区生成的发布目录使用，不是来源签名认证。清单强制 UTF-8 无 BOM、LF，已通过 Windows Git Bash 的 `sha256sum --check --strict`。重复构建只替换带 `.proxypanel-build` 标记的固定目标目录，未标记目录拒绝覆盖。`build/` 中**确切的** Linux amd64/arm64 发布二进制只做交叉构建、清单与 ELF 校验，**未在目标平台运行**（Linux amd64 平台的功能验收由 Docker 内 staged 构建完成，哈希与发布目录不同；arm64 架构整体未运行），二进制可运行性仍须在获准主机实测。Windows 发布件的运行证据仅 `agent -version`，Windows 核心/浏览器功能证据来自本地验收，不由发布件承担。

## 2. Windows 本地启动

1. 使用独立数据库及 `DATABASE_URL`、`REDIS_URL` 环境变量；不要复用他人数据库。
2. 生成并安全保存随机 32 字节 master key，设置 `PANEL_MASTER_KEY`。它用于加密凭据，丢失后数据库中的秘密无法恢复；切勿每次启动重新生成。
3. 设置 `PANEL_DEV=true`、`PANEL_LISTEN=127.0.0.1:8080`、`PANEL_PUBLIC_URL=http://127.0.0.1:5173`，首次启动提供 `PANEL_ADMIN_USER`、`PANEL_ADMIN_PASSWORD`。用户表非空不会再次 bootstrap；首次启动后移除密码变量。
4. `go run ./cmd/panel`。另一个终端在 `web/` 执行 `npm run dev -- --host 127.0.0.1`；Vite 代理 `/api` 和 `/sub` 到 8080。
5. 建服务器、节点、组、订阅、成员；服务器页面生成一次性注册码。
6. 将 `configs/agent.example.json` 复制到受限 `.local`，改为回环面板 URL、`allow_loopback_http=true`、Windows 绝对 Xray/状态路径和注册码。`go run ./cmd/agent -config <path> -check` 检查后运行（不带 `-check`）。
7. 注册成功后可清除配置中的注册码；token 已在 Agent SQLite 内持久保存。不要删除状态目录来“修复”上报或恢复问题。

这是开发方式，不是 Windows 服务安装器。状态目录 ACL 限当前账户/SYSTEM；运行账户必须拥有该目录。不要在命令行或日志中传 subscription token、代理 UUID 或私钥。

## 3. Linux 正式安装（尚未执行验收）

要求独立获准主机、systemd、可信 DNS/TLS、正确时钟及架构匹配的构建。PostgreSQL 17 / Redis 7 使用专用低权限账号、独立库/命名空间、网络白名单。跨主机数据库连接启用服务端证书校验；示例 `sslmode=disable` 仅适用同机回环，不是公网建议。

1. 传输可信构建到临时目录，执行 `sha256sum --check --strict SHA256SUMS`。
2. 根据角色运行 `bash deploy/manage-linux.sh install panel <release-dir> <unique-release-name>` 或 `... install agent ...`。脚本只创建本项目账户/目录/服务，不启动、不重启运行中的服务，不安装数据库、不修改防火墙或他人服务。已有非托管 `/opt/proxypanel` 会拒绝。
3. 面板复制环境示例到 `/etc/proxypanel/panel.env`，root:root `0600`。systemd 由 root 读取 EnvironmentFile 后以 `proxypanel` 运行。
4. Agent 配置使用 root:proxypanel-agent `0640`；状态 `/var/lib/proxypanel-agent` 为该账户 `0700`。官方核心置于 `/opt/proxypanel/xray/26.3.27/xray`，root 持有、Agent 只读/执行。证书文件需该账户可读，但不得可写。不要给 Agent 任意 root 权限。
5. 面板后端只监听 `127.0.0.1:8080`，`PANEL_DEV=false`、`PANEL_TRUST_REVERSE_PROXY=true`。按 `deploy/nginx.conf.example` 配置 HTTPS、静态 SPA、同源 API。先 `nginx -t` 再 reload。不要把 Agent StatsService 端口暴露外网。限流使用连接来源 IP，不盲信客户端自带 X-Forwarded-For。
6. 显式执行 `systemctl enable --now proxypanel-panel` / `proxypanel-agent`。查看脱敏 journal；面板 `/healthz` 是进程健康，`/readyz` 才检查数据库/Redis。Agent 在线不等于核心已应用；核对 applied revision、实例结果、用量和真实客户端。
7. 首次代理流量、TCP/UDP 撤权、离线到期、主机重启、服务恢复必须重新实测，并填写验收记录。

直接 HTTPS 可设置 `PANEL_TLS_CERT` 和 `PANEL_TLS_KEY`；面板自身仅提供 API，不提供生产 SPA 静态服务，仍需静态服务器。开发 HTTP 强制回环，不能用于正式公网。

## 4. 升级和技术回滚

- 先备份 PostgreSQL、master key 和 Agent SQLite；阅读迁移，迁移采用 checksum+advisory lock，不能修改已应用 SQL 文件。
- 安装新不可变 release 名；运行中的服务只暂存新包，不会自动切换。维护窗口停止相应服务后，把 `current` 指向经过校验的新 `releases/<name>`，再启动。面板与 Agent 同机共用 current 时应统一版本并协调两个服务。
- 核心按版本目录安装，不覆盖现有二进制。Agent 精确版本白名单需随代码升级并重新验证协议矩阵。禁止任意 URL 自动升级。
- 技术回滚先停止服务，将 current 指回兼容的旧二进制；**不得通过恢复旧配置/旧 SQLite/旧数据库来重新放行被撤销用户**。数据库迁移不保证向下兼容；如需恢复数据库，必须走 `docs/recovery.md`。
- 每节点一个独立核心；成员撤权/凭据变化可能重启该节点实例并中断同节点其他连接。只有配置无变化的实例才能保持运行，不承诺透明热更新。
- **容器级版本切换与技术回滚已实测**（A13）：在隔离 Docker 栈内 Agent `0.1.0→0.1.1→0.1.0`、核心 `26.3.27→26.3.23→26.3.27` 及技术回滚均通过，Agent 身份/控制代次/状态卷/账本/队列保持不变，已撤权凭据不复活（`docs/verification/docker-lifecycle.md`）。这只证明容器路径；宿主 systemd 的安装、升级与回滚仍未验收。

## 5. 卸载和保留

`bash deploy/manage-linux.sh uninstall agent` 或 `... uninstall panel` 只停止并移除本服务定义，保留配置、发布包、核心、状态和所有数据库，不删除账户、不执行 Docker prune。

只有明确选择 `uninstall agent --purge-agent-data` 才删除 `/var/lib/proxypanel-agent`（含独立 token、计量基线和待上传队列，会产生不可恢复的数据缺口）。应先确认队列清空并备份。面板数据库删除没有一键脚本，避免误删其他业务。

## 6. 边界

- 未获准 Linux/公网节点，未执行宿主 systemd 安装、主机重启或自动恢复；**容器级**升级/回滚与隔离备份恢复演练已在自有 Docker 栈实际执行（见 §4 与 [docs/verification/docker-recovery.md](verification/docker-recovery.md)），但不等于宿主服务管理验收。
- 尚不能凭本机连接时延承诺多节点生产 SLA；必须在目标规模测量。
- `go test -race` 需要可用 C 工具链；已在 SDK 的官方 w64devkit 2.10.1 下执行 Agent 全包、真实核心 Supervisor 与面板真实 PG 回归（0 DATA RACE），但这不替代目标生产规模下的并发验证。
- 外部 Telegram 送达必须配置真实 bot/chat 后验证，不应把本地 HTTP 模拟测试当作真实送达。
- 客户端矩阵（A15）**未完全满足**：真实 Docker 运行 `docker-client-matrix-3.log` **PASS 6.57s**，5 协议 × 2 节点 × 3 客户端 = **30 格真实配置加载 + 69,632B 载荷**；v2rayN **10 格 `not-run`**（7.25.6 无官方非 GUI 导入接口、只读观测不可操作，实际 GUI 导入从未执行）。**不能写成 40/40**；WS/gRPC/REALITY 等传输与公网直连未验收。
- 十二项验收标准没有全部通过：`#10` 未完全满足、`#12` 因一次真实的 WSL 启动而非完全符合；本手册中的边界与“已实测”表述必须按本节限定，不能扩大为整体完成（逐条状态见 [docs/verification/final-acceptance.md](verification/final-acceptance.md)）。
