# 快速开始

这篇带你把面板跑起来、接上一个节点、并打一次真实流量确认它真的在工作。面向第一次拿到仓库、想先看一眼效果的人；完整的部署参数见 [deployment/docker.md](../deployment/docker.md)。

## 前置条件

| 需要 | 说明 |
|---|---|
| Docker Engine 24+ / Compose v2 | `docker compose version` 有输出即可，单机就够 |
| 2 vCPU / 2 GiB 内存 / 10 GiB 磁盘 | 面板 + PostgreSQL + Redis + Agent + 一个 Xray 实例同机 |
| 一个能访问的地址 | 局域网 IP 或域名。自签证书就够用，浏览器会提示证书不受信任 |
| 官方 Xray 二进制 | 只有「验证真实流量」那一步需要，下载与校验命令见下文 |

下面所有命令都在仓库根目录执行。

## 步骤 1 · 生成密钥与证书

```bash
git clone https://github.com/r1172464137/proxypanel.git
cd proxypanel

# 一体化 compose：PostgreSQL + Redis + 面板（镜像里带 nginx，对外只有一个 8443）
cp deploy/docker-compose.bundle.yml docker-compose.yml
cp deploy/.env.bundle.example .env
mkdir -p certs
```

生成三个密钥和一个自签证书。`PANEL_HOST` 换成你实际访问用的 IP 或域名：**证书的 SAN 必须包含它**，否则浏览器报错、Agent 也连不上。

```bash
PANEL_HOST=192.168.1.20          # 改成你的 IP 或域名

PG_PASSWORD="$(openssl rand -hex 32)"
REDIS_PASSWORD="$(openssl rand -hex 32)"
PANEL_MASTER_KEY="$(openssl rand -base64 32)"
ADMIN_PASSWORD="$(openssl rand -base64 12)"      # 一次性管理员密码，下面会打印出来

# IP 用 IP:，域名把 IP: 换成 DNS:
openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
  -keyout certs/privkey.pem -out certs/fullchain.pem \
  -subj "/CN=$PANEL_HOST" -addext "subjectAltName=IP:$PANEL_HOST"

cat > .env <<EOF
PG_PASSWORD=$PG_PASSWORD
REDIS_PASSWORD=$REDIS_PASSWORD
PANEL_MASTER_KEY=$PANEL_MASTER_KEY
PANEL_PUBLIC_URL=https://$PANEL_HOST:8443
PANEL_BIND_IP=$PANEL_HOST
PANEL_TAG=latest
PANEL_ADMIN_USER=admin
PANEL_ADMIN_PASSWORD=$ADMIN_PASSWORD
EOF
chmod 600 .env
echo "一次性管理员密码：$ADMIN_PASSWORD"   # 登进去后到「账号安全」里自己改掉
```

`PANEL_BIND_IP` 决定面板发布到宿主的哪个地址：默认模板里写的是本项目测试机的内网 IP，**必须改成你自己的**；填 `127.0.0.1` 就只能本机访问（可配合 SSH 隧道），填 `0.0.0.0` 则同网段/公网（有公网 IP 时）都能连。

**`PANEL_MASTER_KEY` 必须固定保存。** 它加密数据库里的代理凭据（每个「成员 × 节点」的密码 / UUID）。这个值丢了，数据库还在、成员还能登录，但所有代理凭据都解不开——只能全部轮换，成员要重新导入订阅。把它和 `.env` 一起抄进密码管理器。

## 步骤 2 · 起容器

```bash
docker compose up -d
docker compose ps
docker compose logs -f panel
```

等到 `panel` 日志出现下面两行，说明面板进程活着、nginx 已经开始对外服务：

```text
entrypoint: panel is answering /healthz
entrypoint: starting nginx on 0.0.0.0:8443
```

另开一个终端验证探针（自签证书要加 `-k`）：

```bash
curl -k -s "https://$PANEL_HOST:8443/healthz"; echo
# {"data":{"status":"ok"},"request_id":""}
curl -k -s "https://$PANEL_HOST:8443/readyz"; echo
# {"data":{"status":"ok","postgres":"ok","redis":"ok"},"request_id":""}
```

`/healthz` 只说明进程活着；`/readyz` 才真的去 ping PostgreSQL 和 Redis。

## 步骤 3 · 用一次性管理员密码登录

浏览器打开 `https://$PANEL_HOST:8443`，用户名 `admin`，密码是步骤 1 最后打印出来的那一串。

这套引导只在**成员表为空**时生效（密码至少 10 位，太短面板会直接启动失败）。登录成功后先到 **我的账户 → 账号安全** 把密码改成自己的，再把这两行从 `.env` 删掉，让配置生效：

```bash
sed -i '/^PANEL_ADMIN_/d' .env
docker compose up -d
```

不删也不会重复创建管理员，但密码留在 `.env` 里没有任何好处。

## 接下来 · 建到能出订阅

最短路径是「服务器 → 节点 → 节点组 → 订阅 → 成员 → Agent」。顺序有依赖：订阅引用节点组，节点组引用节点，成员绑定订阅。

### 1. 建服务器

左侧 **资源 → 服务器** → **新建服务器**，填名称、地址（IP 或域名）、地区，**保存**。

### 2. 建节点

**资源 → 节点** → **新建节点**，先点卡片里的 **Shadowsocks 2022** 模板（这个模板不需要域名和证书，适合先跑通）。然后核对这几项：

- **所属服务器**：选第 1 步那台
- **对外地址**：成员客户端实际连接的地址，填 `$PANEL_HOST`
- **监听端口**：模板默认 `8388`（节点在这台机器上真正监听的端口）
- **对外端口**：留空表示与监听端口相同
- **监听地址**：`0.0.0.0`
- **启用该节点**：勾上

**保存节点**。节点技术配置一旦变化会重启该节点的核心实例，已连接的会话会断——这里只有你自己，无所谓。

### 3. 建节点组

**资源 → 节点组** → **新建节点组**，填名称，在 **包含节点** 里勾上刚建的节点，**保存**。

节点组是订阅的授权单位。改成员会直接影响已有成员，所以服务端要求先看影响预览再提交。

### 4. 建订阅

**资源 → 订阅** → **新建订阅**：

- **名称**、**授权节点组**（勾上第 3 步的组）
- **每人配额（GiB）**：默认 100，可以先改小
- **周期起始日**、**时区**：默认 `1` / `Asia/Shanghai`
- **默认有效期（天）**：留空也行，绑成员时手动填到期时间

**保存订阅**。

### 5. 建成员

**成员 → 新建成员**：

- **用户名**、**初始密码**（至少 10 位）
- **角色**：`成员`
- **订阅**：选第 4 步的订阅（保存时会给这个成员按授权节点生成凭据）
- **到期时间**：留空则跟随订阅；想省事就直接选一个未来时间

**保存**。之后在成员行点 **订阅链接** 就能拿到该成员的订阅 URL。

### 6. 接上 Agent

Agent 跑在节点机上，负责把面板下发的配置变成真实的 Xray 进程。这里用容器跑，和面板同机最省事。

先在面板里拿注册码：**资源 → 服务器** → 目标服务器那一行的 **注册码** → 弹窗 **一次性 Agent 注册码** 里复制。注册码**只能用一次，15 分钟内有效**，关掉弹窗不保证还能再看。

然后在节点机上准备官方 Xray 并核对官方摘要：

```bash
mkdir -p /opt/proxypanel/xray /var/lib/proxypanel-agent /etc/proxypanel
cd /tmp
curl -fsSLo xray.zip https://github.com/XTLS/Xray-core/releases/download/v26.3.27/Xray-linux-64.zip
curl -fsSLo xray.zip.dgst https://github.com/XTLS/Xray-core/releases/download/v26.3.27/Xray-linux-64.zip.dgst
expected="$(sed -n 's/.*SHA2-256=[[:space:]]*\([0-9a-fA-F]\{64\}\).*/\1/p' xray.zip.dgst | head -n1)"
echo "$expected  xray.zip" | sha256sum -c -
unzip -q -o xray.zip -d /opt/proxypanel/xray/26.3.27
/opt/proxypanel/xray/26.3.27/xray version | head -1   # 应该是 26.3.27
```

（arm64 主机把两个文件名换成 `Xray-linux-arm64-v8a.zip`。）

写 Agent 配置。`xray_path` 与 `state_dir` 要按**容器内**路径写，因为下面用容器跑 Agent：

```bash
cat > /etc/proxypanel/agent.json <<'EOF'
{
  "panel_url": "https://REPLACE_WITH_PANEL_HOST:8443",
  "registration_code": "REPLACE_WITH_ONE_TIME_CODE",
  "xray_path": "/opt/xray/26.3.27/xray",
  "state_dir": "/var/lib/proxypanel-agent",
  "poll_interval_seconds": 5,
  "stats_interval_seconds": 5,
  "heartbeat_interval_seconds": 15,
  "allow_loopback_http": false
}
EOF
chmod 600 /etc/proxypanel/agent.json
chown 65532:65532 /var/lib/proxypanel-agent    # Agent 镜像以 65532 运行

# 面板用的是自签证书；把这张证书作为 Agent 的信任根
# （仓库根目录执行；如果 Agent 在另一台机器上，把面板的 fullchain.pem 拷过去）
cp certs/fullchain.pem /etc/proxypanel/panel-ca.crt
```

启动 Agent（`-p` 的宿主端口要与节点配置里的「监听端口」一致）：

```bash
docker run -d --name proxypanel-agent --restart unless-stopped \
  -e SSL_CERT_FILE=/usr/local/share/ca-certificates/panel-ca.crt \
  -v /etc/proxypanel/agent.json:/etc/proxypanel/agent.json:ro \
  -v /var/lib/proxypanel-agent:/var/lib/proxypanel-agent \
  -v /opt/proxypanel/xray:/opt/xray:ro \
  -v /etc/proxypanel/panel-ca.crt:/usr/local/share/ca-certificates/panel-ca.crt:ro \
  -p 8388:8388 \
  ghcr.io/r1172464137/proxypanel-agent:latest

docker logs -f proxypanel-agent
```

镜像由仓库里的 `images` workflow 推到 `ghcr.io/<owner>/proxypanel-*`。如果你的 fork 没有开 Packages、或包是私有的，先 `docker login ghcr.io`，或者按 `deploy/Dockerfile.agent` 自己构建（构建上下文需要 `bin/<arch>/agent`）。

注册成功后 Agent 会把 token 写进状态目录，`agent.json` 里的 `registration_code` 可以删掉。**不要删状态目录**：里面还有 Agent 身份、计量基线和尚未上传的用量队列。

回到面板核对三件事：

1. **资源 → 服务器** 列表里那台机器的状态列显示 **在线**（心跳在 60 秒内）
2. 点服务器名进入 **Agent 与核心实例** 页：**核心实例** 表里出现这个节点、实例运行中，`已应用` 修订号追上了 `期望`
3. **运行 → 任务与同步** 里没有失败项

## 确认真的通了

上面前两项只证明「面板以为它好了」。真实流量要自己打一次。

先取该成员的订阅链接：**成员 → 订阅链接**。用 `xray` 格式拉一份客户端配置（这份配置自带 `127.0.0.1:10808` 的 socks 入站）：

```bash
SUB_URL='https://REPLACE_WITH_PANEL_HOST:8443/sub/REPLACE_WITH_MEMBER_TOKEN'
curl -k -fsS "$SUB_URL?format=xray" -o /tmp/pp-client.json

# 用同一个官方核心当客户端（不要和 Agent 抢端口，这里另起一个进程）
/opt/proxypanel/xray/26.3.27/xray run -c /tmp/pp-client.json &
sleep 2

# 走代理访问，看出口 IP 是不是节点自己的
curl -s --socks5-hostname 127.0.0.1:10808 https://www.cloudflare.com/cdn-cgi/trace | grep '^ip='
```

拿到节点的出口 IP 就说明：面板授权 → Agent 下发 → Xray 实例 → 成员凭据 → 真实出网，这条链整条是通的。测试完 `kill %1`。

然后去 **运行 → 用量** 看这个成员的用量：Agent 每 5 秒采样一次并上报，十几秒内应该出现字节数。数字出现了，计量链路也就是通的。

## 常见卡点

**`PANEL_MASTER_KEY` 丢了会怎样？** 面板照常启动、照常登录，但所有代理凭据解不开，表现为成员订阅里没有可用节点 / 连不上。恢复方式只有轮换凭据（成员页 **凭据** 或用 **轮换凭据**），成员需要重新导入订阅。密码不受影响（存的是哈希）。所以 master key 与数据库必须一起备份。

**Agent 在线 ≠ 节点可用。** 「在线」只是心跳在 60 秒内。节点能不能用要看 **运行详情** 里的核心实例是否运行、`已应用` 是否追上 `期望`。Agent 拉不到快照、Xray 起不来、端口被占用，都会表现为「在线但连不上」。

**端口没映射。** 节点配置里的「监听端口」是**容器内**端口，必须靠 `docker run -p 宿主:容器` 暴露出去；「对外端口」是写给成员看的，面板不代管端口转发。上面面板容器只发布了 8443，节点端口 8388 是在 Agent 容器上单独映射的。

**容器起来就退出。** 按顺序看日志：

- `cannot read TLS certificate at /certs/fullchain.pem` → `certs/` 没放证书，或 compose 不在仓库根目录（`./certs` 是相对 compose 文件解析的）
- `panel exited during startup` → 面板自身配置错。最常见的是 `PANEL_ADMIN_PASSWORD` 短于 10 位，或 `PANEL_LISTEN` 与 `PANEL_TRUST_REVERSE_PROXY` 冲突（镜像内已固定为回环 + 信任反代，不要覆盖）
- `PANEL_MASTER_KEY must decode to 32 bytes` → 生成方式不对，用 `openssl rand -base64 32`

**订阅链接打不开。** `PANEL_PUBLIC_URL` 决定订阅链接里的主机名，必须与浏览器实际访问的地址完全一致（协议、主机、端口）。填成内网地址却从外网导入，打不开是正常的。

---

下一步：真正部署时看 [deployment/docker.md](../deployment/docker.md)（环境变量、卷、升级、备份）与 [deployment/production.md](../deployment/production.md)（TLS、权限、不能暴露的端口）。想把面板和节点拆到不同机器上，看 [deployment/split-panel-agent.md](../deployment/split-panel-agent.md)。
