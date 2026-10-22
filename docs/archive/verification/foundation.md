# 执行记录：开发基础与核心能力

此记录仅覆盖实际完成的基础检查，不代表应用整体交付。实现契约见 `docs/archive/implementation-contract.md`。

## 新确认的范围

- 用户：VLESS 参数重点完整支持，其他协议支持常用组合。
- 用户：客户端为 sing-box、Mihomo/Clash 系、v2rayN。
- 负责人：按固定官方 Xray 26.3.27 验证参数，原生高级设置保留结构；客户端不兼容显式拒绝，不静默删参数。常用其他协议按 VMess/Trojan/Shadowsocks（含2022）编排。
- 用户：暂不使用已安装 Ubuntu WSL2，先完成 Windows 验证。原远程主机仍仅承载专属数据库。

## 已执行检查

| 检查 | 结果与证据 |
|---|---|
| Go安装 | 官方go1.27.3 windows-amd64 zip SHA256一致，`go version`成功 |
| Git复用 | 原E:\SDK\Git安装可用，2.51.0.windows.1 |
| 用户环境变量 | 备份后增补PATH及Go缓存；新子PowerShell正确解析；不改机器级变量 |
| Xray安装 | 官方26.3.27 windows-64 zip摘要一致，`xray version`成功 |
| Go基础包 | `go test ./internal/contracts ./cmd/dbcheck`通过编译（两包尚无单元测试） |
| 私钥排除 | `git check-ignore 私钥.txt .local/test-databases.json`确认均忽略；未提交文件 |
| Windows数据库认证 | `scripts/Invoke-DatabaseCheck.ps1`通过；非超级用户连接，临时表读写回滚，Redis认证及随机键读写删除 |
| 隧道生命周期 | 同一自动化调用中建立、检查、finally关闭；不依赖跨调用后台进程存活 |
| 重复脚本调用 | ACL已安全时不重复设置所有者，隧道重建与数据库检查通过 |
| 真实VLESS链路 | `go test -v ./tests/e2e -run TestXrayVLESSRealityVision -count=1 -timeout 60s`通过 |

真实链路使用本地TLS1.3目标、Xray VLESS+REALITY+Vision服务端、Xray客户端SOCKS入口和本地HTTP目标。实际payload 81,920字节，StatsService下行82,330字节；计数包含HTTP等开销。核心终止后请求失败。测试出现本地自签名TLS目标的握手诊断日志，但代理请求和计数断言均通过；不将该测试当作公网证书/目标兼容证明。

## 本次解决的基础问题

- 工具运行环境最初找不到Git，实际安装已存在，补PATH而非重装。
- 自动化命令结束后后台SSH不再存在，改用同一命令生命周期的隧道+测试+清理。
- 重复Set-Acl触发SeSecurityPrivilege，改为先验证已保护DACL，仅不合格时通过访问权限段收紧；原私钥不修改。

## 尚未由这些检查证明

- 面板-Agent整体链路、权限、幂等计量、配额、旧连接硬撤权。
- 所有VLESS传输参数、Shadowsocks2022等其他协议及三类客户端互通。
- Linux systemd、部署/恢复、主机重启、公网可用性。
- 性能、原服务业务级零影响、生产安全审查。
