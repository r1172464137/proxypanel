# 生产部署

这篇讲把面板放到公网之前必须做对的事：TLS 终止在哪、哪些端口绝对不能开、文件和账户权限长什么样、时钟与来源 IP 怎么处理、以及升级回滚的红线。面向要长期跑这套东西的运维者。容器参数见 [docker.md](docker.md)，面板与节点分机部署见 [split-panel-agent.md](split-panel-agent.md)。

## 拓扑：只有一个入口

```text
浏览器 / Agent ──https:443──> nginx（TLS 终止）──http──> 面板
                                                          │
                              systemd: 127.0.0.1:8080 ────┤
                              docker : 127.0.0.1:8081 ────┘（→ 容器 0.0.0.0:8080）
                                                          └──> PostgreSQL / Redis（仅回环或仅容器网络）

节点机：Agent ──> 官方 Xray 实例（监听节点端口，供成员连接）
```

本文推荐面板不直接公网暴露，TLS 由宿主 nginx 终止。
`PANEL_TRUST_REVERSE_PROXY=true` 允许面板在该拓扑下提供明文 HTTP，不再强制回环监听；
Docker 镜像使用 `0.0.0.0:8080`，**只发布到宿主 `127.0.0.1:8081`**。
镜像无 nginx 或入口脚本，只有一个 Go 进程，默认托管 SPA。

不要将来源 IP 校验当作网络隔离：`clientIP()` 在直连方为回环、私网或链路本地时
读取 XFF，并取最右侧合法 IP；这不是精确代理白名单，也不受上述传输开关控制。
必须限制宿主发布地址和容器网络成员，防止不可信对端绕过 TLS 或伪造来源。
Docker 转发可能改变对端地址，不能保证绕过 nginx 时一定被识别为公网来源。
nginx 应使用 `$remote_addr` 覆盖 `X-Forwarded-For`，同时传递 Host 与协议头；
当前代码不通过 `X-Forwarded-Proto` 决定 Cookie Secure，Secure 由 DEV 设置决定。

其他传输模式的真实约束：成对设置 `PANEL_TLS_CERT` / `PANEL_TLS_KEY` 时 Go 直接提供
TLS，允许任意监听地址，且优先于反代路径；但不适用于本文的 HTTP upstream。
**`PANEL_DEV=true` 无论是否同时配置 TLS 或反代，都必须回环监听**，因此不能用于
本文 Docker bridge 部署。不要为了明文浏览器访问启用 DEV。

## nginx：终止 TLS 与选择前端归属

[`deploy/nginx.conf.example`](../../deploy/nginx.conf.example) 当前面向 Docker：
上游是宿主 `127.0.0.1:8081`，默认全量反代给 Go 托管 SPA。镜像默认
`PANEL_WEB_ROOT=/usr/share/proxypanel/web`；显式设空才是 API-only，由宿主 nginx serve dist。
Compose 单连字符默认值保留显式空，详见 [Docker 静态托管](docker.md#方案-bapi-only宿主-nginx-托管-dist)。

下面是另一种 **systemd + API-only + 宿主静态托管** 示例：面板设置
`PANEL_LISTEN=127.0.0.1:8080`、`PANEL_TRUST_REVERSE_PROXY=true`、`PANEL_WEB_ROOT=`，
nginx 从 `/opt/proxypanel/current/web/dist` 读取前端。若希望裸进程也托管 SPA，
将 `PANEL_WEB_ROOT` 设为该目录并改用全量反代，不要混用两种 location。
模板和下列配置都需按域名、证书路径及 nginx 版本调整，再执行 `nginx -t`。

```nginx
upstream panel_api {
    server 127.0.0.1:8080;
    keepalive 16;
}

server {
    listen 443 ssl;
    server_name panel.example.com;

    ssl_certificate     /etc/proxypanel/tls/fullchain.pem;
    ssl_certificate_key /etc/proxypanel/tls/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;

    root /opt/proxypanel/current/web/dist;
    index index.html;
    client_max_body_size 2m;
    server_tokens off;

    add_header X-Content-Type-Options nosniff always;
    add_header Referrer-Policy no-referrer always;
    add_header X-Frame-Options DENY always;

    # 订阅令牌是 bearer 凭据：不记日志、不缓存
    location /sub/ {
        access_log off;
        proxy_pass http://panel_api;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        add_header Cache-Control no-store always;
    }

    location /api/ {
        proxy_pass http://panel_api;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        # 限流按连接来源 IP；必须覆盖而不是追加客户端自带的头
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_read_timeout 120s;
        add_header Cache-Control no-store always;
    }

    location = /healthz { proxy_pass http://panel_api/healthz; }
    location = /readyz  { proxy_pass http://panel_api/readyz; }

    location /assets/ {
        try_files $uri =404;
        expires 1y;
        add_header Cache-Control "public, immutable";
    }

    # SPA 路由回落：否则刷新 /admin/members 会 404
    location / {
        try_files $uri $uri/ /index.html;
        add_header Cache-Control no-store always;
    }

    location ~ /\. { deny all; }
}

server {
    listen 80;
    server_name panel.example.com;
    return 301 https://$host$request_uri;
}
```

三条容易漏的：

- `index.html` 必须 `no-store`，带哈希的 `/assets/` 才能长缓存；反过来会让用户一直看到旧页面；
- 忘了 `try_files ... /index.html`，前端路由一刷新就 404；
- `/sub/` 的 `access_log off` 不只是省日志：URL 里带的是等同密码的令牌。

启用前先校验再 reload：

```bash
nginx -t
systemctl reload nginx
```

### 容器部署的反代写法

容器里**没有** TLS，面板只提供明文 HTTP 8080，所以宿主 nginx 用 `http://` 反代，
不需要 `proxy_ssl_*` 那一套：

```nginx
upstream proxypanel_api {
    server 127.0.0.1:8081;      # compose 里 PANEL_PORT 的宿主端口
    keepalive 16;
}

location / {
    proxy_pass http://proxypanel_api;
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host              $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For   $remote_addr;
    proxy_read_timeout 60s;
}
```

完整版本（含订阅不记日志、探针穿透、前端两种归属）见
[`deploy/nginx.conf.example`](../../deploy/nginx.conf.example)。

回环 HTTP 只用于宿主反代和命令行探针，不是浏览器快速入口；SSH 转发明文端口
也不会提供 HTTPS。正式浏览器会话使用 Secure Cookie，应通过已配置好的 HTTPS 入口访问。
不要以启用 DEV 替代 TLS。

## 哪些端口绝对不能暴露

| 端口 | 是什么 | 应该 |
|---|---|---|
| 443 / 节点代理端口 | 对外开放的服务 | 公网可达 |
| 面板 `8080`（宿主 systemd）或宿主发布端口（bundle 默认 `${PANEL_PORT:-8081}`，example 固定 8081） | 面板 HTTP | **宿主只回环**；容器内部监听 `0.0.0.0:8080` 不直接公开 |
| PostgreSQL `5432` | 权威数据库 | 只回环或只容器网络。跨机连接要开服务端证书校验，`sslmode=disable` 仅限同机 |
| Redis `6379` | 登录限流 | 只回环或只容器网络 |
| Xray 管理 API | Agent 注入的 `StatsService`，监听 `127.0.0.1`，端口在 `20000–60000` 里随机 | 永远不要做端口转发。它是用量统计的唯一来源，也是本机提权面 |
| Agent 状态目录 | `/var/lib/proxypanel-agent` | 不是端口，但也别共享出去：里面有 token 和未上传队列 |

自查（在节点和面板所在机器上都要跑）：

```bash
ss -lntp | grep -E ':(5432|6379|8080|8081|2000[0-9]|[2-5][0-9]{4})\b'
# 宿主视角：已发布的数据库与面板 HTTP 端口只绑回环；bundle 数据库不发布
# 容器内 Go 的 0.0.0.0:8080 是预期值；Xray 管理 API 在其所在网络命名空间内只绑回环
```

`0.0.0.0:8388` 这类是节点真正的代理端口，公开是有意的——请确认它就是你想要公开的那一个。
Docker 发布可能由 NAT 而非宿主监听进程实现，还应检查 `docker compose ps` / 容器端口映射，
并从外部确认面板明文端口不可达，不能只靠 `ss` 判断。

## 权限与文件所有权

`deploy/manage-linux.sh` 是发布包的安装入口。它做的事很窄：建账户、放文件、装 unit，不装数据库、不改防火墙、不动别人的服务。

| 路径 | 属主 / 权限 | 说明 |
|---|---|---|
| `/opt/proxypanel` | root `0755` | 基目录，带 `.managed-by-proxypanel` 标记文件 |
| `/opt/proxypanel/releases/<name>` | root | 每个版本一个不可变目录，名字重复会拒绝安装 |
| `/opt/proxypanel/current` | root，符号链接 | 指向当前版本；必须是本脚本管的链接，否则拒绝 |
| `/etc/proxypanel` | root `0755` | 配置目录 |
| `/etc/proxypanel/panel.env` | root `0600` | 面板环境变量，systemd 以 root 读取后再降权运行 |
| `/etc/proxypanel/agent.json` | `root:proxypanel-agent` `0640` | Agent 配置；注册码与 token 在里面 |
| `/var/lib/proxypanel-agent` | `proxypanel-agent:proxypanel-agent` `0700` | Agent 状态目录 |
| `/opt/proxypanel/xray/<version>/xray` | `root:root` `0755` | 官方核心，Agent 只读执行，跟它同目录放 `geoip.dat` / `geosite.dat` |

账户：

- `proxypanel`（system、无 home、nologin）跑面板；
- `proxypanel-agent`（system、无 home、nologin）跑 Agent。

两个都不需要 root，也**不要**给 root。Agent 唯一的特权是 `CAP_NET_BIND_SERVICE`（为了监听 443 这类 <1024 端口），其余能力为空。

发布包的安装与卸载：

```bash
# 安装（只为 root，且只在明确授权的主机上）
sha256sum --check --strict SHA256SUMS
bash deploy/manage-linux.sh install panel /path/to/release proxypanel-0.1.1
bash deploy/manage-linux.sh install agent /path/to/release proxypanel-agent-0.1.1

# 卸载：只移除 unit，保留配置、发布包、数据库、核心与 Agent 状态
bash deploy/manage-linux.sh uninstall panel
# 只有明确要丢状态时才加 --purge-agent-data（会删未上传的用量队列）
bash deploy/manage-linux.sh uninstall agent --purge-agent-data
```

`deploy/deploy-agent.sh` 是另一条路：面向单台裸机节点，交互式安装官方 Xray + Agent + unit，会自己 `systemctl enable` 并 restart。它把 Agent 放到 `/opt/proxypanel/agent/agent`（unit 也指向那里），与发布包的 `/opt/proxypanel/current/agent` 不是同一套布局。两者选一个，不要混用在同一台机器上。

## systemd 两个 unit 的真实行为

`proxypanel-panel.service`：

- `User=proxypanel`、`EnvironmentFile=/etc/proxypanel/panel.env`、`WorkingDirectory=/opt/proxypanel/current`；
- `ProtectSystem=strict`、`ProtectHome=true`、`PrivateTmp=true`、`NoNewPrivileges=true`、`UMask=0077`；
- `Restart=on-failure`，`KillMode=control-group`（SIGTERM 后 30 秒整组收掉）。

面板不需要任何可写路径（日志进 journal），所以 `ProtectSystem=strict` 不会碍事。

`proxypanel-agent.service`：

- `StateDirectory=proxypanel-agent` + `StateDirectoryMode=0700`，systemd 负责创建 `/var/lib/proxypanel-agent`；
- `ReadWritePaths=/var/lib/proxypanel-agent`，只有这里可写；
- `AmbientCapabilities=CAP_NET_BIND_SERVICE` + `CapabilityBoundingSet=CAP_NET_BIND_SERVICE`；
- `TimeoutStopSec=75`：Agent 停机时要停掉所有核心实例，比面板慢得多。

`manage-linux.sh install` 的边界（照代码写的，别指望它做更多）：

- 不自动启动、不重启任何服务；
- 发现 `/opt/proxypanel` 存在但没有标记文件 → 直接拒绝（不接管别人的目录）；
- release 名已存在 → 拒绝（版本目录不可变）；
- `current` 是指向 `releases/` 之外的链接 → 拒绝；
- **服务正在运行时只把新版本放到 `releases/`，不改 `current`**，然后以退出码 0 提示你先停服务。

也就是说激活新版本要手动做这一步：

```bash
systemctl stop proxypanel-panel
ln -sfn /opt/proxypanel/releases/proxypanel-0.1.2 /opt/proxypanel/current
systemctl start proxypanel-panel
systemctl status proxypanel-panel --no-pager
```

首次启动（两个都是显式动作）：

```bash
# 先按配置示例准备好 /etc/proxypanel/panel.env（0600）与 agent.json（0640）
/opt/proxypanel/current/agent -config /etc/proxypanel/agent.json -check
systemctl enable --now proxypanel-panel
systemctl enable --now proxypanel-agent
journalctl -u proxypanel-panel -f      # 看 transport: 与 migration 日志
```

`-check` 只做静态校验：配置格式（`panel_url` 必须是 https，或回环 http + `allow_loopback_http=true`）、`xray_path` 存在且是普通文件，然后打印摘要退出。它不会连面板，也不验证 `state_dir` 能不能写（那是真正启动时 `supervisor.New` 的事）。

## 时钟同步

面板里所有时间都是 RFC3339 UTC 比较：注册码 15 分钟有效期、订阅到期、周期起止、会话 12 小时、用量批次窗口。节点和面板的时钟一歪，症状是「刚生成的注册码立刻过期」「到期时间显示对但行为不对」「周期在错误的时间点翻页」。

```bash
timedatectl                       # 看 System clock synchronized: yes
timedatectl set-timezone Asia/Shanghai   # 显示用；业务时区在订阅上单独配置
chronyc tracking                  # 或 ntpq -p；看 offset 是否在毫秒级
```

订阅的业务时区（`timezone`）是另一回事：它只决定「每月几号 0 点翻周期」，与主机时区无关。但主机时钟必须准。

## 来源 IP 与限流

登录限流是 **10 次/分钟，键 = 来源 IP + 用户名**（`login:<ip>:<username>`）。面板怎么确定「来源 IP」：

1. 直连方（`RemoteAddr`）是回环 / RFC1918 私网 / 链路本地 → 认为是我们自己的反代，读 `X-Forwarded-For`；
2. 否则忽略任何 `X-Forwarded-For`，直接用 TCP 对端地址；
3. 采信 XFF 时取**最右一项**（离面板最近的那一跳）。

由此得到两条部署纪律：

- nginx 必须用 `$remote_addr` **覆盖** `X-Forwarded-For`，不能 `$proxy_add_x_forwarded_for` 那样追加。`nginx.conf.example` 里写的就是覆盖；
- 必须限制面板的可达范围：systemd 监听宿主回环；Docker 内可监听所有接口，但只发布到宿主回环，且容器网络只容纳受信服务。私网地址会被当作可信代理，不可信同网段或同容器网络对端可能自带 XFF 绕过限流；来源校验不能代替隔离。

其他与来源相关的点：

- 会话里记了创建时的 IP 和 User-Agent，用于成员自己在「账号安全」里看会话；
- 订阅输出 `/sub/<token>` 不需要登录，本身就是 bearer 凭据。不要在 CDN、WAF 或访问日志里留下完整 URL，泄露后让成员轮换订阅链接（**成员 → 订阅链接**）；
- 真的被爆破时先看 `/readyz` 的 `redis`：Redis 不可用会退化成进程内限流，多副本时各算各的，更容易被打穿。

## 升级与回滚

前提：升级前**先备份并验证能读**（数据库 dump + master key），记录当前 `current` 指向的版本名，读完待应用的迁移 SQL。

```bash
# A. 服务在跑：脚本只把新版本放进 releases/，不动 current（前一节已有步骤）
bash deploy/manage-linux.sh install panel /path/to/new-release proxypanel-0.1.2
systemctl stop proxypanel-panel
ln -sfn /opt/proxypanel/releases/proxypanel-0.1.2 /opt/proxypanel/current
systemctl start proxypanel-panel

# B. 先停服务再装：脚本会自己切 current
systemctl stop proxypanel-panel
bash deploy/manage-linux.sh install panel /path/to/new-release proxypanel-0.1.3
systemctl start proxypanel-panel

journalctl -u proxypanel-panel -n 100 --no-pager     # 迁移日志 + transport 行
curl -s http://127.0.0.1:8080/readyz; echo           # systemd 宿主探针；Docker 宿主改用 8081
```

面板启动即跑迁移（advisory lock + checksum）。agent 与 panel 同机共用 `current` 时，两个服务要一起切、版本要配套。

技术回滚（二进制层面）：

```bash
systemctl stop proxypanel-panel
ln -sfn /opt/proxypanel/releases/proxypanel-0.1.1 /opt/proxypanel/current
systemctl start proxypanel-panel
```

但要清楚：**迁移不回退**。新版本的迁移已经把 schema 推进了，旧二进制可能起不来。这不是 bug 是取舍——所以升级窗口前必须有可验证的备份，并且知道恢复备份意味着什么。

Agent 的回滚有额外约束：恢复后的控制代次（generation）是一个新的随机值，Agent 不会自动接受，需要在每个节点上停掉 Agent、核对完授权与账本后用 `-accept-generation <新值>` 显式接受。把旧 Agent 换回来、或让它接受一个旧代次，同样需要显式接受且必须先核对；靠清空状态目录「让它重新开始」会造出账目缺口。

### 红线：不能靠恢复旧数据库撤销权限

恢复一份旧数据库，会把**恢复点之后**发生的停用、到期、撤权、密码/凭据轮换全部抹掉——被你踢掉的人会重新拿到可用凭据，而且节点上的核心可能还在按旧授权跑。所以：

- 回滚**只能**回滚二进制和配置，或从备份恢复到一个独立目标后按流程核对；
- 真要恢复数据库，必须走 [operations/backup-recovery.md](../operations/backup-recovery.md)：隔离还原 → 比对 master key 与迁移版本 → 进入恢复协调模式（面板与 Agent 的 generation 都会换新）→ 逐项核对停用/到期/撤权/轮换 → 勾选确认后退出 → 每个节点用 `-accept-generation <新的 generation>` 显式接受；
- 恢复期间普通写操作被拒绝，快照下发暂停，Agent 保留最后一次更严格的授权。这是设计上的「宁可少发，不可错发」。

## 首次上线检查清单

```bash
# 1. systemd 进程与就绪（未安装 Agent 时去掉后一个）
# Docker 则用 docker compose ps，以下宿主探针端口改为 8081
systemctl is-active proxypanel-panel proxypanel-agent
curl -s http://127.0.0.1:8080/healthz; echo
curl -s http://127.0.0.1:8080/readyz;  echo

# 2. TLS 与入口（私有 CA 用 --cacert，不以跳过校验作为验收）
curl -sI https://panel.example.com/ | head -1
curl -s -o /dev/null -w '%{http_code}\n' https://panel.example.com/assets/does-not-exist.js  # 404
curl -s -o /dev/null -w '%{http_code}\n' https://panel.example.com/admin/members            # 200（SPA 回落）

# 3. 认证闭环
curl -s -X POST https://panel.example.com/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"..."}' -c /tmp/ck -w '\n%{http_code}\n'

# 4. 端口暴露面
ss -lntp
```

然后在界面上确认：服务器页 Agent **在线**、点服务器名进入 **Agent 与核心实例** 看到核心实例运行且 `已应用` 追上 `期望`、**任务与同步**无失败、**用量**有数字。这些都不等于「代理真的可用」——最后一次真实流量必须自己打（见 [快速开始](../getting-started/quickstart.md#确认真的通了)）。

## 已验证与未验证

历史已验证：真实 Docker 栈上跑过面板 + PostgreSQL + Redis + 真实 Agent + 真实官方 Xray + 真实客户端流量，含撤权重连、计量与账本、容器内 Agent/核心版本切换与技术回滚、隔离环境备份还原演练。
本次无 nginx 的单进程镜像另做了 amd64 构建、两种托管模式的 HTTPS 与真实 Chromium 验证，见[部署烟测](../development/deployment-smoke.md)。nginx 在隔离容器中模拟外置代理，未安装/修改生产宿主 nginx；本轮未重新验证 Agent/Xray 数据面。

未验证：Linux 宿主 systemd 的安装、升级、回滚没有在目标生产机上验收；`manage-linux.sh` 与 `deploy-agent.sh` 的 unit 行为来自代码与脚本本身，不是真机执行记录。公网直连、宿主重启自动恢复、真实 Telegram 送达、多节点生产规模下的 RPO/RTO 也都需要你在自己的环境里实测。
