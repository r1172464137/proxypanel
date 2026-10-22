# 分离部署：前端托管与节点 Agent

“分离”有两件独立的事：前端 dist 可由宿主 nginx 托管；Agent 应运行在真正承载节点的机器上。
面板仍是单个 Go 进程，不需要另造 API Dockerfile，也不需要在面板容器内运行 nginx。

**推荐同源 HTTPS**：无论谁托管静态文件，浏览器看到的前端与 `/api/` 都通过同一入口。
这样无需 CORS，使用默认 host-only、`SameSite=Strict`、Secure 会话 Cookie。
`SameSite` 判断 site 而不是 origin；跨 origin 不必然跨 site，但会引入额外的请求与 Cookie 配置。

## 方案 A：静态分离 + 统一入口

```text
浏览器 ──HTTPS:443──▶ 宿主 nginx（TLS 终止）
                      ├── /、/assets/        → 宿主 dist
                      └── /api/、/sub/、探针 → 宿主 127.0.0.1:8081
                                               └──容器 Go 0.0.0.0:8080（API-only）
节点机 Agent ──HTTPS──▶ 同一个域名的 /api/v1/agent/**
```

### 步骤 1 · 启动面板 API-only

使用现有 [`deploy/Dockerfile.panel`](../../deploy/Dockerfile.panel) 构建的镜像。
它没有 nginx 或入口脚本，默认从 `/usr/share/proxypanel/web` 托管 SPA；只要把
`PANEL_WEB_ROOT` 显式设为空，即可关闭静态托管，**无需另建 Dockerfile**。

Compose 应映射：

```yaml
PANEL_WEB_ROOT: "${PANEL_WEB_ROOT-/usr/share/proxypanel/web}"
```

在 `.env` 写 `PANEL_WEB_ROOT=`（单连字符默认值保留显式空），然后
`docker compose up -d panel` 重建容器。旧 compose 若未映射该变量，单改 `.env` 不生效，
需添加上述映射或直接设置 `PANEL_WEB_ROOT: ""`。
保持镜像的 `PANEL_LISTEN=0.0.0.0:8080`、`PANEL_TRUST_REVERSE_PROXY=true`，
只发布 `127.0.0.1:8081:8080`，并设置正确的 `PANEL_PUBLIC_URL=https://panel.example.com`。
数据库、密钥、初始管理员与镜像版本选择见 [Docker 部署](docker.md)。
不要启用 DEV，也不要给这个 HTTP upstream 配置 `PANEL_TLS_CERT` / `PANEL_TLS_KEY`。

若使用裸进程/systemd，则监听 `127.0.0.1:8080`、显式 `PANEL_WEB_ROOT=`，
nginx upstream 相应改为宿主 8080；反代模式本身已不强制回环，但宿主仍应限制可达面。

### 步骤 2 · 构建与同步前端

在源码根目录执行：

```bash
(cd web && npm ci && npm run build)
sudo mkdir -p /var/www/proxypanel
sudo rsync -a --delete web/dist/ /var/www/proxypanel/
```

前后端必须使用配套版本。`web/dist` 不是源码目录，nginx 用户应能读取全部构建产物。
静态文件也可放独立存储/CDN，但最终保持统一入口；不要把带凭据的 API 或订阅响应缓存进去。

### 步骤 3 · 配置宿主 nginx

参考 [`deploy/nginx.conf.example`](../../deploy/nginx.conf.example) 的 B 方案：

- upstream 为宿主 `127.0.0.1:8081`，协议是 **HTTP**，TLS 只在宿主终止。
- 用 B 的静态 `location /` **替换** A 的全量反代，而不是叠加；root 指向 `/var/www/proxypanel`。
- `/assets/` 使用 `try_files $uri =404`，带哈希资源可以长缓存。
- SPA 路由回落至 `index.html`，HTML 必须 `no-store`。
- `/api/` 反代到 Go，保留公共 `/sub/`（不记日志、不缓存）、`/healthz`、`/readyz`。
- 保留模板 A/B 共用的 `/api/`、`/sub/` 和探针 location，不重复定义相同路由。
- 覆盖 `X-Forwarded-For $remote_addr`，同时传递 Host 与协议头，不透传客户端伪造来源。
- 替换域名与证书路径，按宿主 nginx 版本调整语法，执行 `nginx -t` 后 reload。

Go 的来源 IP 校验不是网络隔离替代品：它信任回环、私网及链路本地 TCP 对端提供的 XFF，
并非精确代理白名单。面板明文端口只能给宿主反代与受信容器网络访问，不能开放给局域网或公网。

### 步骤 4 · 验证

下面是部署方需执行的检查，不是本次新镜像已验收记录：

```bash
curl -fsS https://panel.example.com/healthz; echo
curl -fsS https://panel.example.com/readyz; echo
curl -s -o /dev/null -w '%{http_code}\n' https://panel.example.com/admin/members  # SPA 200
curl -s -o /dev/null -w '%{http_code}\n' https://panel.example.com/assets/does-not-exist.js  # 404
curl -s -o /dev/null -w '%{http_code}\n' https://panel.example.com/api/v1/admin/members  # 未登录 401
```

私有 CA 使用 `--cacert`，不要用 `-k` 作为证书验收。
在浏览器完成登录、获取 session、CSRF 写请求、刷新子路由与退出；检查 Cookie。
`/readyz` 的 Redis 故障可能返回 200 + degraded，应检查响应体。

## Agent 部署在节点机

Agent 只通过面板 HTTPS 主动注册、轮询、上报，不直接连接 PostgreSQL/Redis。
面板所在机无需运行 Agent，除非该机本身也承载节点；bundle 不包含 Agent。
example 的 Agent 服务位于 `node` profile，默认不启动；它仍需配置状态目录、核心与 CA 挂载、
节点端口。核对后使用 `docker compose --profile node up -d` 启用，或单独部署 Agent。

1. 在面板创建该节点机的服务器档案、节点与授权，生成一次性注册码。
2. 在节点机准备官方白名单版本 Xray，核对 SHA256。Agent 镜像不内置核心，不自动下载。
3. `agent.json` 中 `panel_url` 填同一入口 `https://panel.example.com`，无需单开 Agent 管理端口。
4. 配置和核心路径按实际运行环境填写；容器配置文件必须允许 uid/gid 65532 读取，状态目录必须可写。
5. 私有 CA 挂进 Agent 容器并设 `SSL_CERT_FILE`，公共 CA 一般无需额外挂载。
6. 只发布节点代理端口，禁止发布 Xray Stats 管理端口。状态目录不可随意删除。
7. 注册后确认心跳、实例、修订收敛，再打真实客户端流量并核对用量。

完整命令见 [快速开始](../getting-started/quickstart.md#6-接上-agent)，
宿主 systemd 方式与权限布局见 [生产部署](production.md)。

## 方案 B：跨域的当前边界

代码已经有部分配置能力，但不能把它写成“只改几个环境变量即可可靠跨域登录”：

| 项目 | 当前实现 |
|---|---|
| API 基地址 | `web/src/api/endpoints.ts` 的资源路径使用构建变量 `VITE_API_BASE` |
| 携带凭据 | `web/src/api/http.ts` 配了 `VITE_API_BASE` 后使用 `credentials: include` |
| CORS | 后端配置 `PANEL_ALLOWED_ORIGINS` 精确 origin 白名单才启用，允许凭据及 CSRF/幂等头；不能用 `*` |
| Cookie | `PANEL_COOKIE_SAMESITE`、`PANEL_COOKIE_DOMAIN` 可配；`none` 要求 Secure，与 DEV 互斥 |
| **认证路径缺口** | 登录、session、退出、兑换仍为相对 `/api/v1/auth/**`，未使用资源 BASE |

因此纯静态前端域名 + 独立 API 域名的完整登录链路仍有实现缺口，尚不能给出可靠的
纯配置教程。不要重复粘贴 CORS 中间件或修改 Cookie 常量，相关能力已经存在；应先补齐
认证 URL 一致性并实测浏览器，再决定是否跨域。本文不修改代码。

即使补齐后也需要分别评估 origin 与 site、Cookie 作用域和浏览器第三方 Cookie 限制。
不同站点通常需要 `SameSite=None; Secure`，仍可能被第三方 Cookie 策略阻止。
Cookie 的 Domain 不能跨两个不同主域共享，扩大父域范围也不是默认必需项。
CSRF 仍靠会话令牌校验而不比对 Origin，不能用 CORS 代替 CSRF。
**没有强需求就保持方案 A 的同源入口。**

## 方案 C：只加速静态资源

可在统一入口上叠加 CDN，但仅缓存带内容哈希的 `/assets/` 等不可变资源。
`index.html` 不缓存；`/api/`、`/sub/`、`/healthz`、`/readyz` 全部直达源站，不记录订阅令牌。
CDN/WAF 会改变来源地址，当前 `clientIP()` 不提供可配置的 CDN 信任链解析，不能直接
把互联网提供的 XFF 透传给面板；新增代理层需要重新设计并验证边界。

## 验证边界

本次新镜像已完成 amd64 构建、两种同源托管模式的隔离 HTTPS 与 Chromium 验证，见[部署烟测](../development/deployment-smoke.md)。未在生产宿主配置 nginx，未验证真跨域浏览器链路，也未在本轮重跑节点数据面。首次上线仍需分别检查，不能只以 `/healthz` 成功宣布可用。
