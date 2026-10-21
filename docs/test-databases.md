# 隔离测试数据库

## 部署信息

- Linux 主机：`192.168.5.203`，SSH `root` / `22`，Ubuntu 24.04 x86_64。
- Compose 项目：`proxypanel-test`。
- 项目目录：`/opt/proxypanel-test`（仅 root 可访问）。
- 仅部署 PostgreSQL 和 Redis，没有部署面板或 Agent。
- 未改动原有 axonhub 数据库、原有容器配置及数据卷；未安装软件或更新镜像。

| 项目 | PostgreSQL | Redis |
| --- | --- | --- |
| 容器 | `proxypanel-test-postgres` | `proxypanel-test-redis` |
| 服务器监听 | `127.0.0.1:15432` | `127.0.0.1:16379` |
| 应用账号 | `proxypanel`（非超级用户，不可创建数据库或角色） | `default`，需要密码 |
| 数据库 | `proxypanel` | 该独立实例内的 DB 0 |
| 数据卷 | `proxypanel-test-pgdata` | `proxypanel-test-redisdata` |
| 内存上限 | 512 MiB | 192 MiB（Redis maxmemory 96 MiB） |
| CPU 上限 | 0.75 CPU | 0.25 CPU |

网络：`proxypanel-test-network`，独立 bridge，不连接原有应用网络。主机发布端口只绑定回环地址。它不是虚拟机级别的隔离，也不隔离共享主机的磁盘 IO、网络及 root 权限。Docker 管理者可以访问容器和秘密文件。

镜像使用现有镜像的固定摘要，`pull_policy: never`：

- PostgreSQL 17.11：`postgres@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24`
- Redis 7 系列：`redis@sha256:858f009f9709ce576febc734aa78b8f6d624b82571f9ddb6bda4377c833b3499`

Redis 开启 AOF，`appendfsync everysec`；达到 maxmemory 后采用 `noeviction`。资源上限用于限制对原应用的影响，不保证共享主机零性能影响。测试过程中需关注磁盘与内存。

## 凭据

随机生成的独立密码只保存于服务器专属目录，未写入仓库或聊天：

- 应用数据库密码：`/opt/proxypanel-test/secrets/pg-app-password`
- PostgreSQL 管理密码：`/opt/proxypanel-test/secrets/pg-admin-password`
- Redis 密码：`/opt/proxypanel-test/secrets/redis-password`
- Redis 运行配置：`/opt/proxypanel-test/secrets/redis.conf`

父目录仅 root 可访问；挂载进容器的文件允许服务用户读取。应用必须使用 `proxypanel`，不可使用 PostgreSQL 超级用户。

密码文件用于初始化或运行配置，不能将“修改密码文件”当作完成数据库密码轮换。轮换 PostgreSQL 密码需要同步执行 ALTER ROLE；Redis 密码与运行配置也必须同步更新。

用户提供的 `私钥.txt` 未读取或打印内容，原文件未修改。它的 Windows ACL 过宽，OpenSSH 拒绝直接使用；本次连接使用会话临时目录下仅当前用户可访问的副本，直接传给 `ssh -i`。该副本和主机记录随会话清理，不属于持久连接配置。

首次连接采用 TOFU（首次接受主机密钥），后续连接严格校验会话 known_hosts；没有通过第二渠道核实主机指纹。

## 本机连接

数据库不直接对局域网开放。先建立 SSH 隧道，再让本地应用连接 `127.0.0.1:15432/16379`。

在 PowerShell 中，使用权限受限的私钥文件和已核实的 known_hosts：

```powershell
ssh -N -i <权限受限的私钥路径> -p 22 `
  -o StrictHostKeyChecking=yes `
  -o UserKnownHostsFile=<主机记录文件路径> `
  -o ExitOnForwardFailure=yes `
  -o ServerAliveInterval=30 -o ServerAliveCountMax=3 `
  -L 127.0.0.1:15432:127.0.0.1:15432 `
  -L 127.0.0.1:16379:127.0.0.1:16379 `
  root@192.168.5.203
```

占位符必须替换，不可原样执行。不要为方便而关闭主机密钥校验。此命令前台运行，关闭后隧道断开。本次临时验证隧道已经关闭，没有留下本机后台 SSH 进程。

连接配置由后续本地开发环境加载秘密文件，不能把真实密码提交到 Git。测试数据库实例本身未配置 TLS；跨主机链路必须走 SSH，不可改为明文公网或局域网直连。

## 服务管理（在 Linux 执行）

```sh
cd /opt/proxypanel-test
docker compose ps
docker compose stop
# 需要使用时启动已有实例：
docker compose up -d --wait
```

- 管理命令必须在这个目录执行，或者明确指定本项目 Compose 文件和项目名。
- 不运行全局 `docker system prune`、`docker volume prune`。
- 不使用原 axonhub 的 Compose 文件、网络、账号或数据卷。
- `docker compose down` 会移除本项目容器和网络但保留数据卷；不要加 `-v`，除非用户明确同意删除测试数据。
- 默认 `unless-stopped`：随 Docker 启动恢复，显式 stop 后保持停止。

## 已执行验证

1. PostgreSQL 容器健康检查通过。
2. 应用账号经 TCP 密码认证成功；确认非超级用户、无创建角色/数据库权限。
3. PostgreSQL 创建测试表、写入和读取通过，事务回滚，没有保留测试表。
4. Redis 无凭据请求返回 NOAUTH；带认证 PING、写入、读取、删除均通过。
5. 通过主机发布端口 `15432/16379` 完成认证连接验证。
6. Windows 临时 SSH 隧道：PostgreSQL 协议协商响应正常，Redis 返回预期的未认证拒绝。未在 Windows 中安装数据库客户端，尚未在本机执行完整认证 SQL；服务端发布端口认证已验证。
7. 原有五个容器的 ID、StartedAt、RestartCount 与部署前完全一致，原数据库仍显示 healthy。
8. 检查了新容器的专属数据卷和 CPU/内存限制。

没有读取原有数据库内容，未做原应用业务级回归或性能测试；不能据此保证零性能影响。尚未执行主机重启恢复、备份恢复和应用集成测试。

## 本次部署处理过的问题

当前 Docker 环境下，`internal: true` 网络未发布主机端口。已仅重建本项目网络和容器，改为独立普通 bridge，保留数据卷，并验证端口仅监听主机回环地址。没有调整原网络或防火墙配置。

## 后续应用阶段补充（2026-10-20）

以上“尚未执行”描述是最初部署时点记录。后续已使用 Windows Go pgx/redis 客户端，通过同一隔离隧道完成 PG 完整认证、临时表读写回滚和 Redis 随机过期键读写/删除；`scripts/Invoke-DatabaseCheck.ps1` 可重复执行。

已增加受限且被 Git 忽略的 `.local/database-ssh-key`、known_hosts 与测试应用凭据文件；原 `私钥.txt` 仍未读取、打印或修改。`scripts/Start-DatabaseTunnel.ps1`/`Use-TestDatabases.ps1` 仅加载这些应用凭据，不接触原库。

面板真实 API 与面板→Agent→Xray 计量/撤权集成测试已在随机专属 schema 中通过，详见 `docs/verification/integration.md`。应用测试进程/核心均在 Windows 本机，远端仍仅运行测试数据库。测试通过与否、后续审查修复回归状态以最新集成记录为准。Linux 主机重启、备份恢复及原业务性能回归仍未执行。
