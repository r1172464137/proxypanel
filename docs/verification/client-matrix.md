# 执行记录：A15 实际客户端矩阵（Docker + 固定 Windows 核心）

本记录对应目标 A15：「固定并记录 sing-box、Mihomo、v2rayN 及所用核心版本，对既有契约声明支持的
VLESS、VMess、Trojan、Shadowsocks（含 2022）组合形成明确矩阵；支持项有实际导入/配置加载和经 SSH
隧道连接的结果，不支持项显式拒绝，不能静默丢参数或换传输」。

两条入口：

- `tests/e2e/client_matrix_test.go` → `TestDockerClientMatrix`（仅 `PANEL_DOCKER_TEST=1` 时运行），
  由父在真实 Docker + SSH 隧道环境执行（`scripts/Invoke-DockerAcceptance.ps1 -Run TestDockerClientMatrix`）。
  该行已于 2026-10-21 由父在真实 Docker + SSH 隧道环境执行：30/40 真实格通过、10 个 v2rayN 格 `not-run`（见下方“Docker 矩阵实测结果”）；未执行的格子仍标未验收。
- `internal/panel/clientconfig/realcore_test.go`（构建标签 `corematrix`）→ 本轮新增的**本地真核心矩阵**，
  在固定版本的官方核心上做真实小流量验证，不需要 Docker/隧道。本文的「通过」必须按入口标明：**本地真核心矩阵**（下一节）与 **Docker 矩阵**（「Docker 矩阵实测结果」，由父在真实 Docker + SSH 隧道执行）是两条独立证据链，不能互相替代，也不能合并计数。

## 固定环境

| 项 | 值 |
|---|---|
| Xray | `E:\SDK\Xray\v26.3.27\xray.exe`（`XRAY_BIN`，26.3.27） |
| sing-box | `E:\SDK\sing-box\1.14.3\sing-box-1.14.3-windows-amd64\sing-box.exe`（`SING_BOX_BIN`，1.14.3） |
| Mihomo | `E:\SDK\mihomo\1.19.32\mihomo-windows-amd64.exe`（`MIHOMO_BIN`，1.19.32） |
| v2rayN | **7.25.6 已启动并做只读观测**（窗口 329882 / pid 15128 / observation 8：`snapshot_actionable=false`、elements 0）；**实际 GUI 导入从未执行**，本轮未导入、未重启；旧 ForcedClear 无 prelaunch baseline，故不声称整体系统代理未变 |
| 目标（Docker 行） | `http://target:18080/`，完整 payload 69,632 字节 |
| 隧道（Docker 行） | 父的 SSH 转发覆盖 20101–20105、20201–20205；测试只连 `127.0.0.1:<端口>` |
| 目标（本地核心行） | 本机 `httptest` 服务，`http://127.0.0.1:<临时端口>/`，同一 69,632 字节 payload |

核心启动时校验二进制存在且版本字符串包含固定版本，不匹配即失败，不以其他版本冒充。

## 本地真核心矩阵（本轮实际执行，通过）

命令：

```
. .\scripts\Use-DevEnvironment.ps1
go test -tags corematrix -count=1 -timeout 600s ./internal/panel/clientconfig -run TestLocalCore -v
```

构造：服务端文档由**真实生成器** `internal/panel/xrayconf.Build` 产出（含面板注入的 `clients`），
客户端 outbound/proxy 取自**真实导出器** `clientconfig.Render`，只替换本机入口（socks/mixed）与
强制路由；先做 `xray run -test -c` / `sing-box check -c` / `mihomo -t -f`，通过后启动并经 SOCKS5
取目标，断言完整 payload。TLS 用一次性自签 ECDSA P-256 证书（CN/SAN `matrix-trojan.test`），
pin 由公开证书 DER 派生。

| 组合 | Xray 26.3.27 | sing-box 1.14.3 | Mihomo 1.19.32 |
|---|---|---|---|
| vless / tcp / none | 200 + 69,632 B | 200 + 69,632 B | 200 + 69,632 B |
| vmess / tcp / none | 200 + 69,632 B | 200 + 69,632 B | 200 + 69,632 B |
| trojan / tcp / tls（自签 + pin） | 200 + 69,632 B | 200 + 69,632 B | 200 + 69,632 B |
| trojan / tcp / none（明文） | 200 + 69,632 B | 200 + 69,632 B | **显式拒绝**（`UnsupportedError`，字段 `stream_settings.security`） |
| shadowsocks / aes-128-gcm（clients 数组） | 200 + 69,632 B | 200 + 69,632 B | 200 + 69,632 B |
| shadowsocks / 2022-blake3-aes-128-gcm | 200 + 69,632 B | 200 + 69,632 B | 200 + 69,632 B |

负向断言（同一入口 `TestLocalCoreCredentialGuards`，均通过）：

| 检查 | 结果 |
|---|---|
| Xray 客户端把 pin 换成另一证书哈希 | 连接失败（证书校验确实生效，不是被忽略） |
| Shadowsocks 2022 客户端只用用户 PSK（修复前形态） | 被核心拒绝，取不到 payload |
| Shadowsocks 2022 客户端用 `serverPSK:userPSK`（修复后导出） | 200 + 69,632 B |

`allowInsecure` 相关：Xray 26.3.27 明确拒绝该字段（`The feature "allowInsecure" has been removed
and migrated to "pinnedPeerCertSha256"`）；`pinnedPeerCertSha256` 为**单个十六进制字符串**，
`pinnedPeerCertificateChainSha256` 才是 base64 数组。导出器输出 hex pin，本地真核心加载通过。

## Docker 矩阵构造（真实 API，幂等，父执行时使用）

前置：Agent 容器由 Docker 生命周期创建（`TestDockerAcceptance` 或 `control.sh agent1/agent2`），
`TestDockerClientMatrix` 只执行 `start agent1`/`start agent2`。

| Key | 协议/传输/安全 | Agent1 | Agent2 |
|---|---|---|---|
| vless-1/2 | vless/tcp/none | 20101（既有 fixture 节点） | 20201（既有 fixture 节点） |
| vmess-1/2 | vmess/tcp/none | 20102 | 20202 |
| trojan-tls-1/2 | trojan/tcp/tls | 20103 | 20203 |
| ss-1/2 | shadowsocks/tcp/none，aes-128-gcm | 20104 | 20204 |
| ss2022-1/2 | shadowsocks/tcp/none，2022-blake3-aes-128-gcm | 20105 | 20205 |

- 用真实 API 创建节点/组/订阅/会员：组 `client-matrix-group`、订阅 `client-matrix-subscription`、
  会员 `client-matrix-member`；不修改 `docker-alice`、`docker-group`、`docker-sub`。
- Shadowsocks 2022 的服务器 PSK 由 `crypto/rand` 现场生成并通过 `server_key` 交给面板（面板加密保存），
  不写日志；修复后它作为客户端凭据的一部分出现在该会员订阅里（SIP022 多用户要求）。
- 幂等：ID 保存到受保护目录的 `client-fixture.json`（`$PANEL_DOCKER_ROOT/.local/docker-acceptance/`），
  同时按名称对已有对象做核对/PATCH；重复运行不重复建资源。
- Trojan TLS 证书：节点 `stream_settings.tlsSettings.certificates` 内联一次性自签证书
  （CN/SAN `matrix-trojan.test`，ECDSA P-256），客户端 `client_settings.server_name` +
  `allow_insecure: true`；面板从该公开证书派生 pin，导出为 `pinnedPeerCertSha256`。
  Agent 只限制 `certificateFile`/`keyFile` 路径（`internal/agent/xray/build.go`
  `validateCertificatePaths`），内联证书无需为 Agent 挂载文件；私钥只作为节点配置交给面板，不落日志。
- 全量快照：创建完成后 `d.applied()` 等两个 Agent 的 `applied_revision=desired_revision`，再做端口预检。
- 每个协议：按端口从四份真实导出中选单个 outbound/proxy，断言参数未丢失（uuid、alterId=0/security=auto、
  trojan 密码与 TLS SNI + pin、SS method、SS2022 `serverPSK:userPSK`、flow 为空），跨三格式比较凭据一致；
  只改本机入口与强制路由，先做核心配置校验再启动，经 SOCKS5 取 `http://target:18080/` 全量 payload。
  v2rayN 行记录 `not-run`，只校验分享链接与端口对应。

## 本轮修复的四个缺陷（原「已确认缺陷」）

1. **Xray 导出不再输出已被移除的 `allowInsecure`，改为证书 pin。**
   - `internal/panel/protocol/endpoint.go`：`Endpoint.PeerCertSha256` / `PeerCertPinned`；
     `EndpointFor` 从 `tlsSettings.certificates` 内联证书派生叶证书 SHA-256（hex），或读取管理员
     显式设置的 `client_settings.pinned_peer_cert_sha256`（hex/base64 均接受，统一规范为 hex）。
   - `internal/panel/clientconfig/clientconfig.go`：TLS 出口写 `pinnedPeerCertSha256`；`allow_insecure`
     为真却没有可 pin 的证书时返回 `UnsupportedError`（字段 `client_settings.allow_insecure`），
     不再静默降级。显式 pin 在 sing-box / Mihomo / v2rayN 无法表达时同样显式拒绝（字段
     `client_settings.pinned_peer_cert_sha256`），避免忽略管理员参数。
   - 语义：pin 即「该节点自签证书」的官方映射，不再接受任意证书。
2. **Shadowsocks 2022 客户端凭据改为 `serverPSK:userPSK`。**
   - `Endpoint.ServerPSK` 仅在 `2022-*` 方法上由节点 `server_key` 填充（其他协议/方法绝不暴露该
     秘密，REALITY 私钥仍然不会出现在任何导出中）。
   - `clientconfig.shadowsocksPassword` 供 xray / sing-box / Mihomo / v2rayN 四个格式共用；缺少
     server PSK 时返回 `UnsupportedError`（字段 `server_key`），不再输出核心必然拒绝的裸用户 PSK。
3. **旧版 Shadowsocks 的 `clients` 数组补 `method`。**
   - `internal/panel/xrayconf/xrayconf.go`：非 2022 方法为每个 client 写 `method`；2022 多用户
     必须保持空 `method`（Xray 报 `shadowsocks 2022 (multi-user): users must have empty method`）。
     根因是 Xray 26.3.27 从每个 client 读取 cipher，缺省即 `unsupported cipher method:`（空方法名）。
   - `internal/panel/protocol/protocol.go`：`none` 从支持列表移除——核心只接受单用户 inbounds 的
     `none`，而面板模型永远注入 `clients` 数组。
4. **Mihomo 明文 Trojan 显式拒绝。**
   - `internal/panel/clientconfig/clients.go`：`security != tls` 的 trojan 对 Mihomo 返回
     `UnsupportedError`（字段 `stream_settings.security`）；Mihomo 无论是否输出 `tls` 都会发起
     TLS 握手，修复前导出的代理必然不可用。Xray 与 sing-box 仍支持明文 Trojan（本地真核心已验证）。
   - v2rayN 分享链接对明文 Trojan 显式写 `security=none`（否则 v2rayN 默认按 TLS 导入），
     `allow_insecure` 映射为 `allowInsecure=1`，不静默丢参数。

单测回归（`go test ./internal/panel/...` 全绿）：

- `internal/panel/clientconfig/clientconfig_test.go`：`TestXrayTLSReplacesAllowInsecureWithPin`、
  `TestXrayAllowInsecureWithoutPinIsExplicit`、`TestExplicitCertificatePinRejectedWhereUnsupported`、
  `TestMihomoPlaintextTrojanIsExplicit`、`TestShadowsocks2022CredentialsCarryServerPSK`、
  `TestShadowsocks2022WithoutServerPSKIsExplicit`、`TestLegacyShadowsocksKeepsMemberSecret`。
- `internal/panel/protocol/protocol_test.go`：`TestEndpointForTLSDerivesCertificatePin`、
  `TestEndpointForExplicitCertificatePin`、`TestNormalizeCertSHA256`、
  `TestEndpointForShadowsocks2022CarriesServerPSK`、`TestValidateShadowsocksRejectsMultiUserUnsupportedMethod`。
- `internal/panel/xrayconf/xrayconf_test.go`：`TestBuildShadowsocksClientsCarryMethod`。

## Docker 矩阵的构造与执行入口（父执行；结果见「Docker 矩阵实测结果」）

`TestDockerClientMatrix` 已按修复更新断言：Xray TLS 出口必须带 64 位 hex
`pinnedPeerCertSha256` 且不再出现 `allowInsecure`；Shadowsocks 2022 的三格式凭据必须包含
`serverPSK:userPSK` 分隔符。矩阵分组不含明文 Trojan 节点（Mihomo 会对整份导出 422，
本地核心 + 单测已覆盖该显式拒绝），其余 10 个节点（VLESS/VMess/Trojan+TLS/SS/SS2022 × 2 Agent）
由父经 SSH 隧道执行。

**Docker 重建要求**：本轮改动全部在 `internal/panel`（panel 二进制），Agent 二进制未改，但验收
镜像同时打包 panel/agent/fixture，旧镜像里的 `/app/panel` 不含修复，必须先重建：

1. `scripts/Initialize-DockerAcceptance.ps1`：重新交叉编译 linux panel/agent/fixture、上传
   `context.tar`、`docker build -t proxypanel-acceptance:20261021 context`（脚本末尾会执行
   `control.sh up`）。
2. `control.sh up` 只创建不存在的容器，**不会**替换已有容器。需要在宿主上显式重建受影响的容器：
   `cd /opt/proxypanel-acceptance-20261021 && sh control.sh remove panel && sh control.sh remove agent1 &&
   sh control.sh remove agent2 && sh control.sh remove target && sh control.sh up`。
   PostgreSQL/Redis 容器与卷不动，既有测试数据保留。

## Docker 矩阵实测结果（2026-10-21，父执行，PASS 30/40）

父在真实 Docker + SSH 隧道环境执行 `TestDockerClientMatrix`（固定 `XRAY_BIN` / `SING_BOX_BIN` / `MIHOMO_BIN`，串行），结果 `docker-client-matrix-3.log` **PASS 6.57s**：

| 节点 | 端口 | 协议 | Xray 26.3.27 | sing-box 1.14.3 | Mihomo 1.19.32 | v2rayN 7.25.6 |
| --- | --- | --- | --- | --- | --- | --- |
| docker-vless-1 | 20101 | vless/tcp/none | 69,632B ok | ok | ok | not-run |
| client-matrix-vmess-1 | 20102 | vmess/tcp/none | ok | ok | ok | not-run |
| client-matrix-trojan-tls-1 | 20103 | trojan/tcp/tls | ok | ok | ok | not-run |
| client-matrix-ss-1 | 20104 | shadowsocks aes-128-gcm | ok | ok | ok | not-run |
| client-matrix-ss2022-1 | 20105 | SS2022 blake3-aes-128-gcm | ok | ok | ok | not-run |
| docker-vless-2 | 20201 | vless/tcp/none | ok | ok | ok | not-run |
| client-matrix-vmess-2 | 20202 | vmess/tcp/none | ok | ok | ok | not-run |
| client-matrix-trojan-tls-2 | 20203 | trojan/tcp/tls | ok | ok | ok | not-run |
| client-matrix-ss-2 | 20204 | shadowsocks aes-128-gcm | ok | ok | ok | not-run |
| client-matrix-ss2022-2 | 20205 | SS2022 blake3-aes-128-gcm | ok | ok | ok | not-run |

- 每个“ok”= 真实导出 → 固定版本核心配置校验 → 经 SSH 隧道以 SOCKS5 取 `http://target:18080/` **完整 69,632B** 载荷；三个客户端的凭据参数同时比对。
- 共 **30 格真实通过**；v2rayN 只校验分享链接，**10 格 `not-run`**。因此**不是 40/40**，也不能写成“全部客户端支持”。日志中的“rows with a real defect or blocker: 0 of 40”只表示没有未处理的缺陷/阻塞行，不等于全部真实执行。
- 覆盖传输仅 TCP：VLESS none、VMess none、Trojan TLS、legacy SS(aes-128-gcm)、SS2022(blake3-aes-128-gcm)。WS/gRPC/REALITY 等未构造，不得外推；公网直连未验收。
- 版本固定：核心启动前校验版本字符串，不匹配即失败（Xray 26.3.27 / sing-box 1.14.3 / Mihomo 1.19.32），不以其他版本冒充。
- 历史：round 1（`docker-client-matrix-1.log`）在 `export "xray"` 返回 422，根因是生产 cert PEM 行数组的 pin 派生缺陷，DS 7978 修复并部署（`/app/panel` `0595390…`，镜像 `4e96ae…`）；round 2（`-2.log`）是测试侧把“preflight 仅含 blocked 的缺项”读反，DS 1337 修正判断与正反回归；round 3 为上述 30 格通过。
- 未验收：v2rayN 实际 GUI 导入（7.25.6 无官方非 GUI 接口，只读观测 observation 8 仍无 actionable snapshot / elements 0）、公网直连、未列出的传输与安全组合。矩阵节点/成员按设计保留在 fixture 中，未做破坏性清理。

## 秘密与未覆盖

- 不打印 UUID、密码、订阅 token、Shadowsocks 2022 PSK；`client-fixture.json` 只保存资源 ID。
  本地真核心测试只把凭据写进临时目录的一次性配置并在测试结束删除。
- Shadowsocks 2022 的 **server PSK 现在属于客户端凭据**（SIP022 多用户要求），只出现在该节点的
  授权会员订阅中；除 `2022-*` 的 Shadowsocks 外任何协议都不会携带节点 `server_key`。
- VLESS+REALITY 不在 Docker 矩阵重复（无允许的外部 REALITY 目标），已有 `TestXrayVLESSRealityVision`
  与 `internal/panel/clientconfig` 单测覆盖；公网直连整体仍未验收。
- v2rayN 的真实 GUI 导入未运行，列为未验收；分享链接的 scheme/端口/`security=none`/`allowInsecure=1`
  已校验。

## 本轮执行的命令与结果

| 命令 | 结果 |
|---|---|
| `go build ./...` | 通过 |
| `go vet ./...` | 通过 |
| `go test ./internal/panel/...` | 全部通过 |
| `go test -tags corematrix ./internal/panel/clientconfig -run TestLocalCore -v` | 通过（6 组合 × 3 客户端 + 3 项负向断言） |
| `CGO_ENABLED=1 go test -race ./internal/panel/clientconfig ./internal/panel/protocol ./internal/panel/xrayconf`（PATH 加 w64devkit 2.10.1） | 通过，无 DATA RACE |
| `TestDockerClientMatrix` | 父已在 Docker/隧道环境执行：**PASS 6.57s**，30 格真实通过 + 10 个 v2rayN `not-run`（`docker-client-matrix-3.log`） |
