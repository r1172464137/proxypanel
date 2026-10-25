# 订阅输出格式

成员拿到的是一个订阅链接：

```
{PANEL_PUBLIC_URL}/sub/{token}?format=<fmt>
```

`format` 取值（大小写不敏感，含别名）：

| format | 别名 | Content-Type | 适用客户端 |
|---|---|---|---|
| `xray`（默认） | `xray-json` | `application/json` | Xray / v2rayA |
| `mihomo` | `clash`、`clash-meta`、`meta` | `text/yaml` | Mihomo(Clash.Meta) |
| `singbox` | `sing-box`、`sing_box` | `application/json` | sing-box（成员界面不展示） |
| `v2rayn` | `v2ray`、`v2rayng`、`links` | `text/plain` | v2rayN / v2rayNG（成员界面不展示） |

不带 `format` 时按 `User-Agent` 猜；猜不出就用 `xray`。
实现与归一化逻辑在 `internal/panel/clientconfig/clientconfig.go` 的 `NormalizeFormat`。

响应头只有 `Cache-Control: no-store`。**不发** `Subscription-Userinfo`
或 `Profile-Update-Interval` —— 客户端不会从订阅响应头拿到配额/到期，
成员要看用量得进面板 `/me/status`。

> 成员导入页只展示 **Mihomo（Clash）** 与 **Xray** 两种格式。`v2rayn` 和 `singbox` 仍由
> 服务端完整支持，`/sub/{token}?format=` 照常输出，已有链接不会失效 —— 隐藏是界面决定，
> 不是移除能力。

## 四种格式的形状差异

### xray（默认）

`outbounds[]`，按协议用不同子结构：VLESS/VMess/Trojan 走 `settings.vnext` 或
`settings.servers`，Shadowsocks 走 `settings.servers`：

```json
{ "tag": "proxy",
  "protocol": "vless",
  "settings": { "vnext": [ { "address": "hk01.example.com", "port": 8443,
    "users": [ { "id": "<uuid>", "flow": "xtls-rprx-vision", "encryption": "none" } ] } ] },
  "streamSettings": { "network": "ws", "security": "tls",
    "tlsSettings": { "serverName": "hk01.example.com" },
    "wsSettings": { "path": "/ws" } } }
```

末尾固定追加 `direct`（freedom）与 `block`（blackhole）两个出站，实际出站数比节点数多 2。

### sing-box

同样是 JSON，但用 `type` 而非 `protocol`，传输层拆成独立对象：

```json
{ "type": "vless", "tag": "proxy", "server": "hk01.example.com", "server_port": 8443,
  "uuid": "<uuid>", "flow": "xtls-rprx-vision",
  "tls": { "enabled": true, "server_name": "hk01.example.com" },
  "transport": { "type": "ws", "path": "/ws" } }
```

sing-box 的传输层类型名与 Xray 不完全一致（`grpc` → `service_name`、`httpupgrade`、
`xhttp` 等），由 `clients.go` 里的转换负责。

### mihomo

YAML，`proxies:` 列表 + `proxy-groups` + `rules`。字段名与上面两者都不同
（`cipher`、`alterId`、`ws-opts`、`servername` 等）。

### v2rayn

plain text，每行一条 URI：

```
vless://<uuid>@hk01.example.com:8443?encryption=none&security=tls&sni=hk01.example.com&type=ws&path=%2Fws#hk-vless
vmess://<base64-json>
trojan://<password>@...:443?...#tag
ss://<base64>@...:port#tag
```

这种格式信息密度最低，表达不了某些组合（见下）。

## 无法表达时会发生什么

不是所有节点都能被所有客户端表达。例如某客户端不支持 `xhttp` 传输，
或 v2rayN URI 无法携带 REALITY 的某些参数。

此时**整个响应返回 `422 PROTOCOL_NOT_SUPPORTED`**，而不是静默丢节点：

```json
{ "error": { "code": "PROTOCOL_NOT_SUPPORTED",
  "message": "sing-box: node \"hk-xhttp\" cannot represent transport (xhttp)",
  "retryable": false } }
```

判断在 `internal/panel/clientconfig/clients.go`，按「协议 → 传输 → 安全层」逐层检查。

## `external_port`：服务端监听与对外暴露分离

节点有两个端口字段：

| 字段 | 含义 |
|---|---|
| `port` | 节点机上 Xray **实际监听**的端口 |
| `external_port` | 成员客户端**实际连接**的端口；留空（NULL）表示与 `port` 相同 |

有了它就能表达「中转机 8443 → 节点机 20101」：

```
成员 ──▶ relay.example.com:8443 ──(你自己配的转发)──▶ node:20101 ──▶ Xray
                  ↑                                          ↑
         客户端配置写这个                              服务端监听这个
```

规则：

- 客户端配置里永远是 `host:external_port`（`protocol.EndpointFor` 决定）
- 服务端 inbound 永远是 `port`
- `external_port` 不能等于 `port`（相同就没分离的意义，API 返回 `400`，几乎总是误填）
- **端口转发不由面板代管**。nginx stream / iptables / haproxy 需要你自己在机器上配

这样设计的原因：转发状态如果进面板，就变成面板必须知道并维护的隐藏状态；
而它天然属于机器层配置，运维本来就要自己管。

## 已验收的范围

在 WSL2/Linux 的真实 Docker 栈上做过完整验证：

- **5 种协议**（VLESS / VMess / SS / SS2022 / Trojan 变体）× **真实客户端**，
  载荷 69,632 字节，SHA-256 与直连一致
- 已实测的客户端：**Xray**、**sing-box**、**Mihomo**，每种 5 个节点各一个实例
- 四种订阅格式全部生成成功并被对应客户端加载

**未验收**：v2rayN 的图形界面导入。它没有官方非 GUI 导入接口，只能人工拖拽操作，
所以只验证了「生成的 URI 能被解析」，没验证「GUI 里点导入能用」。

公网直连、真实手机客户端、WS/gRPC/REALITY 在真实客户端的完整组合也不在已验收范围内。

测试实现与用例见 `internal/panel/clientconfig/*_test.go`，跑法见
[开发环境](../development/setup.md)。
