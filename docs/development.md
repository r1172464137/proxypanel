# Windows 开发环境

## 已安装与复用

| 工具 | 位置 | 状态 |
|---|---|---|
| Go 1.27.3 | `E:\SDK\Go\go1.27.3\go\bin\go.exe` | 官方zip下载，SHA256校验，实际运行通过 |
| Git 2.51.0.windows.1 | `E:\SDK\Git\cmd\git.exe` | 复用原安装，没有重装 |
| Node v24.22.0 / npm | `E:\SDK\Node\24` | 复用原安装 |
| Xray 26.3.27 | `E:\SDK\Xray\v26.3.27\xray.exe` | 官方release下载，SHA256校验，真实本地转发通过 |
| OpenSSH | Windows 已有组件 | 复用 |

官方制品 SHA256：

- Go windows-amd64 zip：`f26081f438319757f8a1b46d7d0b2c5ec7583d4038e485e0aa725cfde7885fff`
- Xray windows-64 zip：`d004c39288ce9ada487c6f398c7c545f7d749e44bdfdd59dbc9f865afba4e1ad`

仅用户级 PATH 追加 Go bin、Git cmd。设置用户 GOPATH/GOMODCACHE/GOCACHE 至 SDK 内的项目目录；没有设置 GOROOT 或改机器级 PATH。

修改前备份：`E:\SDK\ConfigBackups\proxypanel\user-env-before-20261020-223323.json`。回退时只恢复该备份包含的用户变量；若之后用户又修改过这些变量，应人工合并，不盲目覆盖新修改。回退环境变量不删除SDK。

已经在重新组合机器/用户 PATH 后启动子 PowerShell，验证 go/git 解析路径。已运行的桌面程序可能保留旧环境，开发命令统一先执行：

```powershell
. .\scripts\Use-DevEnvironment.ps1
```

该脚本只设置当前进程的工具路径、Go/npm/Playwright缓存、GOTOOLCHAIN=local、XRAY_BIN和临时目录。临时目录优先使用会话scratch，否则使用SDK项目缓存；不修改系统TEMP。

## 数据库

服务器和隔离资源详见 `test-databases.md`。应用密码保存在本地受限 `.local/test-databases.json`，不要打印、提交或发送给模型。

重复执行完整连接检查：

```powershell
.\scripts\Invoke-DatabaseCheck.ps1
```

该命令建立SSH隧道，在同一进程生命周期运行Go认证读写检查，最后关闭隧道。仅使用新测试实例15432/16379，临时表事务回滚，Redis随机前缀测试键有60秒过期并主动删除。

开发时在受控终端启动 `Start-DatabaseTunnel.ps1 -KnownHostsPath .\.local\database-known-hosts`，再在同一终端加载 `Use-TestDatabases.ps1` 启动应用。自动化宿主可能在命令结束时回收子进程，因此不要假定上一个工具调用启动的后台SSH会一直存在；自动化测试应在同一调用中启动隧道、启动服务、执行测试、清理进程。

私钥仅复制并作为ssh参数使用，原`私钥.txt`未改动。首次登记主机采用TOFU，尚无第二渠道指纹验证；后续严格使用已记录known_hosts。脚本不获取PG超级用户密码。

## 当前验证命令

```powershell
. .\scripts\Use-DevEnvironment.ps1
go test ./internal/contracts ./cmd/dbcheck
go test -v ./tests/e2e -run TestXrayVLESSRealityVision -count=1 -timeout 60s
```

真实核心测试仅使用本机代理客户端、代理服务端、TLS目标与HTTP目标，不访问公网或远程数据库主机。它不是完整面板-Agent联调，也不是三种目标客户端的互通验收。

## Linux 与公网边界

用户已明确暂不使用现有Ubuntu WSL2。没有启动、安装或修改该发行版。当前继续Windows测试；Linux/systemd、安装恢复、公网及真实手机客户端仍未验证，不能宣称通过。
