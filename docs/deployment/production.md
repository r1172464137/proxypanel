# 生产部署

这篇讲把面板放到公网之前必须做对的事：TLS 终止在哪、哪些端口绝对不能开、文件和账户权限长什么样、时钟与来源 IP 怎么处理、以及升级回滚的红线。面向要长期跑这套东西的运维者。容器参数见 [docker.md](docker.md)，面板与节点分机部署见 [split-panel-agent.md](split-panel-agent.md)。

## 拓扑：只有一个入口

```text
浏览器 / Agent ──https:443──> nginx（TLS 终止 + 托管 SPA）──http:8080──> 面板（127.0.0.1）
                                                                  └──> PostgreSQL / Redis（仅回环）

节点机：Agent ──> 官方 Xray 实例（监听节点端口，供成员连接）
```

面板本身不做公网暴露：它只监听回环，TLS 由 nginx 终止，`PANEL_TRUST_REVERSE_PROXY=true` 让面板接受 nginx 写进来的 `X-Forwarded-For` / `X-Forwarded-Proto`。

代码里有个强制约束：**开了 `PANEL_TRUST_REVERSE_PROXY` 就不许监听非回环地址**，否则启动直接失败（`PANEL_TRUST_REVERSE_PROXY requires PANEL_LISTEN on a loopback address`）。这不是可选项——「信任反代头」的能力如果和公网端口同时存在，等于把限流键和审计里的来源 IP 交给客户端自己填。

容器部署时这个约束的形态不同：发布的 `proxypanel-panel` 镜像内 nginx 监听 `0.0.0.0:8443`，面板退回 `127.0.0.1:8080`，两者在同一个网络命名空间里，所以对外仍然只有一个端口，反代头只可能由本机 nginx 产生。

## nginx：终止 TLS 并提供前端

`deploy/nginx.conf.example` 是一个可以直接改用的完整 server 片段（systemd 部署形态：面板在 `127.0.0.1:8080`，静态文件在 `/opt/proxypanel/current/web/dist`）。下面这份在它基础上补齐了 upstream keepalive、`/api/` 超时和两个探针 location，语义与那份文件一致：

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

如果宿主 nginx 要放在 bundle 镜像前面（例如你想在宿主上做统一入口、限速或 WAF），镜像里已经有一层 TLS，反代要连 `https`：

```nginx
location / {
    proxy_pass https://127.0.0.1:8443;
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto https;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_ssl_server_name on;
    # 面板证书是自签时：把 CA 配进去，不要长期关校验
    proxy_ssl_verify off;
}
```

这种双层 TLS 只跑在回环上，能接受；但更简单的做法是只发布 `127.0.0.1:8443`，用 SSH 隧道访问，不额外加一层。

## 哪些端口绝对不能暴露

| 端口 | 是什么 | 应该 |
|---|---|---|
| 443 / 节点代理端口 | 对外开放的服务 | 公网可达 |
| 面板 `8080`（systemd）或 `8443`（容器） | 面板 HTTP | **只回环**，由本机 nginx 反代 |
| PostgreSQL `5432` | 权威数据库 | 只回环或只容器网络。跨机连接要开服务端证书校验，`sslmode=disable` 仅限同机 |
| Redis `6379` | 登录限流 | 只回环或只容器网络 |
| Xray 管理 API | Agent 注入的 `StatsService`，监听 `127.0.0.1`，端口在 `20000–60000` 里随机 | 永远不要做端口转发。它是用量统计的唯一来源，也是本机提权面 |
| Agent 状态目录 | `/var/lib/proxypanel-agent` | 不是端口，但也别共享出去：里面有 token 和未上传队列 |

自查（在节点和面板所在机器上都要跑）：

```bash
ss -lntp | grep -E ':(5432|6379|8080|8443|2000[0-9]|[2-5][0-9]{4})\b'
# 期望：5432/6379/8080/8443 只出现在 127.0.0.1；20000-60000 段的 Xray API 只出现在 127.0.0.1
```

`0.0.0.0:8388` 这类是节点真正的代理端口，公开是有意的——请确认它就是你想要公开的那一个。

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
- 面板必须只监听回环。因为私网地址被当作可信反代，一旦面板在局域网/公网明文可达，同网段任何人都能自带 `X-Forwarded-For` 绕过限流，也能直接跳过 nginx 访问面板。

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
curl -s http://127.0.0.1:8080/readyz; echo           # 未配反代时直接探环回
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
# 1. 进程与就绪（同一台机器上没装 Agent 时去掉后一个）
systemctl is-active proxypanel-panel proxypanel-agent
curl -s http://127.0.0.1:8080/healthz; echo
curl -s http://127.0.0.1:8080/readyz;  echo

# 2. TLS 与入口（不校验证书时用 -k，只为确认链路）
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

已验证：真实 Docker 栈上跑过面板 + PostgreSQL + Redis + 真实 Agent + 真实官方 Xray + 真实客户端流量，含撤权重连、计量与账本、容器内 Agent/核心版本切换与技术回滚、隔离环境备份还原演练。

未验证：Linux 宿主 systemd 的安装、升级、回滚没有在目标生产机上验收；`manage-linux.sh` 与 `deploy-agent.sh` 的 unit 行为来自代码与脚本本身，不是真机执行记录。公网直连、宿主重启自动恢复、真实 Telegram 送达、多节点生产规模下的 RPO/RTO 也都需要你在自己的环境里实测。
