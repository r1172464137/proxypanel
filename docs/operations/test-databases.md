# 隔离测试数据库

跑 store 层和 API 集成测试需要一套**专用的** PostgreSQL + Redis ——
不能用应用自己的库，也不能用别人的库，因为测试会建随机 schema、写真实事务。

本文记录验证栈里那套隔离实例怎么连、怎么管、边界在哪。

## 部署信息

- 主机：`192.168.5.203`，Ubuntu 24.04 x86_64，SSH `root` / `22`
- Compose 项目：`proxypanel-test`，目录 `/opt/proxypanel-test`（仅 root 可访问）
- 只部署 PostgreSQL 和 Redis，**不部署**面板或 Agent

| 项目 | PostgreSQL | Redis |
|---|---|---|
| 容器 | `proxypanel-test-postgres` | `proxypanel-test-redis` |
| 监听 | `127.0.0.1:15432` | `127.0.0.1:16379` |
| 账号 | `proxypanel`（非超级用户，不能建库/建角色） | `default`，需密码 |
| 数据库 | `proxypanel` | 该实例的 DB 0 |
| 数据卷 | `proxypanel-test-pgdata` | `proxypanel-test-redisdata` |
| 资源上限 | 512 MiB / 0.75 CPU | 192 MiB（maxmemory 96 MiB）/ 0.25 CPU |

网络是独立 bridge `proxypanel-test-network`，不接原应用网络；宿主端口只绑回环。

镜像用固定摘要、`pull_policy: never`：

- `postgres@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24`（17.11）
- `redis@sha256:858f009f9709ce576febc734aa78b8f6d624b82571f9ddb6bda4377c833b3499`（7 系列）

Redis 开 AOF（`appendfsync everysec`），满 maxmemory 用 `noeviction`。

**这不是虚拟机级隔离。** 它限制的是 CPU/内存，不隔离共享磁盘 IO、网络和 root 权限；
有 Docker 权限的人能进容器、能读挂载的秘密文件。资源上限是为了不影响同机其它应用，
不保证零影响。

## 凭据

随机生成的密码只存在服务器上，不进仓库、不进聊天：

| 文件 | 内容 |
|---|---|
| `/opt/proxypanel-test/secrets/pg-app-password` | 应用数据库密码 |
| `/opt/proxypanel-test/secrets/pg-admin-password` | PostgreSQL 管理密码 |
| `/opt/proxypanel-test/secrets/redis-password` | Redis 密码 |
| `/opt/proxypanel-test/secrets/redis.conf` | Redis 运行配置 |

父目录只 root 可读。应用必须用 `proxypanel` 账号，不能拿 PostgreSQL 超级用户跑测试。

改密码文件**不等于**完成密码轮换：PostgreSQL 还要同步 `ALTER ROLE`，
Redis 还要同步运行配置。

## 从本机连接

数据库不对局域网开放，走 SSH 隧道：

```bash
ssh -N \
  -i ~/.ssh/proxypanel-test-key \
  -o StrictHostKeyChecking=yes \
  -o UserKnownHostsFile=~/.ssh/known_hosts_proxypanel \
  -o ExitOnForwardFailure=yes \
  -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
  -L 127.0.0.1:15432:127.0.0.1:15432 \
  -L 127.0.0.1:16379:127.0.0.1:16379 \
  root@192.168.5.203
```

前台运行，`Ctrl-C` 即断开。**不要**为了省事关掉主机密钥校验。

然后跑测试：

```bash
export PANEL_TEST_DATABASE_URL='postgres://proxypanel:<pg-app-password>@127.0.0.1:15432/proxypanel?sslmode=disable'
export REDIS_URL='redis://default:<redis-password>@127.0.0.1:16379/0'

go test ./internal/panel/store/ ./internal/panel/api/ -count=1
```

隧道没起来时测试会连接失败（不是 SKIP）—— SKIP 只发生在
`PANEL_TEST_DATABASE_URL` 完全没设的时候。

测试实例本身没配 TLS，跨主机链路必须走 SSH，不要改成明文公网/局域网直连。

私钥文件权限必须是 `0600`，否则 OpenSSH 直接拒绝使用：

```bash
chmod 600 ~/.ssh/proxypanel-test-key
```

主机密钥首次连接采用 TOFU；没有通过第二渠道核对过指纹，这是已知的薄弱点。

## 服务管理

管理命令在项目目录执行，或显式指定 compose 文件与项目名：

```bash
cd /opt/proxypanel-test
docker compose ps
docker compose stop
docker compose up -d --wait     # 需要用时启动
```

- 不跑全局 `docker system prune` / `docker volume prune`。
- 不碰同机其它项目的 compose 文件、网络、账号或数据卷。
- `docker compose down` 会删容器和网络但**保留数据卷**；不要加 `-v`，除非明确要删测试数据。
- 默认 `unless-stopped`：随 Docker 启动，显式 `stop` 后保持停止。

## 已执行的验证

- PostgreSQL 容器健康检查通过
- 应用账号经 TCP 密码认证成功；确认非超级用户、无建角色/建库权限
- 建表/写入/读取/事务回滚通过，未残留测试表
- Redis 无凭据请求返回 `NOAUTH`；带认证的 `PING`/写/读/删均通过
- 经宿主发布端口 `15432`/`16379` 完成认证连接
- 从本机经 SSH 隧道用 Go 的 pgx/redis 客户端完成 PG 完整认证、临时表读写回滚、
  Redis 随机过期键读写删除
- 同机其它容器的 ID、StartedAt、RestartCount 与部署前一致

**没做**：主机重启恢复、备份恢复演练、原业务性能回归、读取原数据库内容。
不能据此保证对同机其它业务零性能影响。

## 部署时遇到的问题

当前 Docker 版本下 `internal: true` 的网络不发布宿主端口。已只重建本项目的网络和容器，
改成独立普通 bridge，数据卷不变，并确认端口只监听宿主回环地址；未改动机器上其它项目的网络或防火墙。

## 与真实部署的区别

这套实例只给自动化测试用。真实部署用的是应用自己的 PostgreSQL/Redis
（见 [Docker 部署](../deployment/docker.md)），两者互不影响，
不要为了让测试跑起来去改生产库的权限或 schema。
