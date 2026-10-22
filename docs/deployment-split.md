# 前后端分开部署教程

## 0. 先理解为什么不能"真·跨域"

当前代码有四个约束，导致前端和后端**必须同源**：

| 约束 | 位置 | 后果 |
|---|---|---|
| API 路径写死为相对路径 `/api/v1/admin` | `web/src/api/endpoints.ts:51` | 请求总是打向当前页面的 origin |
| `credentials: 'same-origin'` | `web/src/api/http.ts:159` | 跨域请求不带 Cookie |
| 后端没有任何 CORS 头 | `internal/panel/api/` 全文 | 浏览器会拒绝跨域响应 |
| 会话 Cookie `SameSite=Strict` | `internal/panel/api/handlers.go:54` | 跨站请求完全不发送 Cookie |

"分开部署"因此只能理解为：**静态文件存放在别处，但统一由同一个入口对外**。

---

## 方案 A · 静态分离 + 统一入口（推荐，零代码改动）

### 架构

```
浏览器
  │  https://panel.example.com
  ▼
统一入口（nginx / CDN / 云负载均衡）
  ├── /                 → 静态文件（对象存储 / CDN / 独立 nginx）
  ├── /assets/          → 静态文件（长缓存）
  ├── /api/             → 面板 127.0.0.1:8080
  ├── /sub/             → 面板（订阅输出，不记日志）
  └── /healthz,/readyz  → 面板
```

浏览器只看到一个域名，所以同源、Cookie、CSRF 全部正常。

### 步骤 1 · 构建前端

```bash
cd web
npm ci
npm run build          # 产物：web/dist/
```

产物约 58 个文件、190 KB（gzip 73 KB）。

### 步骤 2 · 把 dist 放到任意静态位置

三种都行：

| 方式 | 说明 |
|---|---|
| 对象存储（S3 / OSS / COS） | 上传 `dist/*`，开启静态网站托管 |
| 独立 nginx / Caddy | 拷到 `/usr/share/panel-web` |
| 另一台机器 / CDN 回源 | 同上 |

**关键**：SPA 必须把未知路径回落到 `index.html`，否则刷新 `/admin/members` 会 404：

```nginx
location / {
    try_files $uri $uri/ /index.html;
}
```

对象存储对应配置是「错误文档 → index.html，状态码 200」。

### 步骤 3 · 部署纯 API 面板

现有 `deploy/Dockerfile.panel` 是 **nginx + 面板二合一**，分开部署时不需要内置 nginx。
另建一个纯 API 镜像：

```dockerfile
FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && update-ca-certificates
WORKDIR /app
ARG TARGETARCH
COPY bin/${TARGETARCH}/panel /app/panel
COPY migrations /app/migrations
RUN chmod 0755 /app/panel
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app/panel"]
```

环境变量：

```bash
PANEL_LISTEN=127.0.0.1:8080          # 由统一入口反代；容器网络内可用 0.0.0.0:8080
PANEL_TRUST_REVERSE_PROXY=true       # 必须；此时面板强制要求回环监听
PANEL_PUBLIC_URL=https://panel.example.com   # 用户浏览器实际访问的地址
PANEL_MASTER_KEY=<32 字节 base64>    # 生成一次后固定保存
DATABASE_URL=postgres://...
REDIS_URL=redis://...
PANEL_MIGRATIONS_DIR=/app/migrations
```

**`PANEL_PUBLIC_URL` 很重要**：订阅链接由它拼出来（`handlers.go:233`），填错会导致成员拿到打不开的订阅地址。

### 步骤 4 · 统一入口配置

```nginx
upstream panel_api {
    server 127.0.0.1:8080;          # 或 docker compose 的服务名，如 panel:8080
    keepalive 16;
}

server {
    listen 443 ssl;
    server_name panel.example.com;

    ssl_certificate     /etc/ssl/panel/fullchain.pem;
    ssl_certificate_key /etc/ssl/panel/privkey.pem;

    root /usr/share/panel-web;      # 步骤 2 的 dist
    index index.html;
    client_max_body_size 4m;
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
        # 限流按连接来源 IP，不能盲信客户端自带的 X-Forwarded-For
        proxy_set_header X-Forwarded-For $remote_addr;
        add_header Cache-Control no-store always;
        proxy_read_timeout 120s;
    }

    location = /healthz { proxy_pass http://panel_api/healthz; }
    location = /readyz  { proxy_pass http://panel_api/readyz; }

    # 带指纹的构建产物可长缓存
    location /assets/ {
        try_files $uri =404;
        expires 1y;
        add_header Cache-Control "public, immutable";
    }

    # SPA 路由回落
    location / {
        try_files $uri $uri/ /index.html;
        add_header Cache-Control no-store always;
    }

    location ~ /\. { deny all; }
}
```

### 步骤 5 · 验证

```bash
# 静态
curl -I https://panel.example.com/
curl -I https://panel.example.com/assets/index-xxxx.js

# API（401 说明到达面板鉴权层，不是 nginx 404）
curl -o /dev/null -w '%{http_code}\n' https://panel.example.com/api/v1/admin/members
curl -o /dev/null -w '%{http_code}\n' https://panel.example.com/healthz

# SPA 刷新
curl -o /dev/null -w '%{http_code}\n' https://panel.example.com/admin/members   # 应 200

# 登录闭环
curl -X POST https://panel.example.com/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"..."}' -c /tmp/ck -w '\n%{http_code}\n'
```

### 步骤 6 · Agent 仍然连同一个域名

Agent 用的 `panel_url` 填 `https://panel.example.com` 即可，它会走统一入口的 `/api/`。
不需要额外开端口，也不需要改 Agent 代码。

---

## 方案 B · 真·跨域（前端 a.com，后端 b.com）

如果确实要两个域名，**必须改代码**，共四处：

### B1 · 后端加 CORS 中间件

`internal/panel/api/` 新增（并在 `Handler()` 里包一层）：

```go
func corsMiddleware(next http.Handler, allowOrigin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 注意：带 Credentials 时 Allow-Origin 不能是 "*"，浏览器会拒绝
		if allowOrigin != "" && r.Header.Get("Origin") == allowOrigin {
			w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PATCH,PUT,DELETE,OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-CSRF-Token, Idempotency-Key")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

环境变量：`PANEL_ALLOW_ORIGIN=https://app.example.com`

### B2 · Cookie 改 SameSite=None

`handlers.go:52-55` 与 `handlers.go:136`：

```go
SameSite: http.SameSiteNoneMode,   // 必须与 Secure 同时使用
Secure:   true,                    // 跨域 Cookie 强制要求 HTTPS
```

### B3 · 前端改 API 基地址 + 带凭据

```ts
// web/src/api/endpoints.ts
const BASE = import.meta.env.VITE_API_BASE ?? ''
const ADMIN = `${BASE}/api/v1/admin`
const ME = `${BASE}/api/v1/me`
```

```ts
// web/src/api/http.ts
credentials: 'include',            // 原来是 'same-origin'
```

构建时注入：`VITE_API_BASE=https://api.example.com npm run build`

### B4 · 后端信任代理头

跨域后客户端 IP 只能从前置代理取：

```bash
PANEL_TRUST_REVERSE_PROXY=true
```

并确保前置代理用 `$remote_addr` 覆盖 `X-Forwarded-For`（否则限流可被伪造绕过）。

### 改完之后仍然存在的问题

| 问题 | 说明 |
|---|---|
| CSRF 保护变弱 | `SameSite=None` 后必须依赖 `X-CSRF-Token` 双提交，不能只靠 Cookie 策略 |
| 第三方 Cookie 拦截 | Safari / 部分浏览器默认拦第三方 Cookie，跨站场景登录可能直接失败 |
| 部署复杂度 | 两个域名都要证书、都要配 CORS 白名单 |

**除非有强需求，不建议方案 B。** 方案 A 能达到同样的"静态独立"收益，且不改代码。

---

## 方案 C · 只想给静态资源加速（CDN 回源）

不改代码，只在统一入口前加 CDN：

- CDN 源站 = 方案 A 的统一入口
- 只缓存 `/` 与 `/assets/`
- **必须**把 `/api/`、`/sub/`、`/healthz`、`/readyz` 配成「不缓存、直接回源」
- Cookie 域名保持 `panel.example.com`，不加 `Domain` 属性即可

---

## 三种方案对比

| | 方案 A | 方案 B | 方案 C |
|---|---|---|---|
| 改代码 | 不需要 | 需要 4 处 | 不需要 |
| 前端可放 CDN | 可以 | 可以 | 可以（且强制走 CDN） |
| 面板只跑 API | 是 | 是 | 取决于源站 |
| 登录可靠性 | 稳（同源） | 受第三方 Cookie 策略影响 | 稳（同源） |
| 证书数量 | 1 | 2 | 1 |
| 推荐度 | ★★★ | ★（仅在必须分域名时） | ★★（在 A 之上叠加） |

---

## 常见坑

1. **忘了 SPA 回落** → 刷新 `/admin/members` 404。必须 `try_files $uri $uri/ /index.html`。
2. **`PANEL_PUBLIC_URL` 填成内网地址** → 成员拿到的订阅链接打不开。
3. **`PANEL_LISTEN` 设成 `0.0.0.0` 又开 `PANEL_TRUST_REVERSE_PROXY`** → 面板启动直接报错（代码强制要求回环，防止绕过代理直接暴露信任头）。
4. **`/sub/` 被 CDN 或日志记录** → 订阅令牌（等同密码）进日志。
5. **`X-Forwarded-For` 直接透传客户端值** → 登录限流可被伪造头绕过。
6. **只缓存了 `index.html`** → 发布后用户拿到旧页面。`index.html` 必须 `no-store`，带哈希的 `/assets/` 才能长缓存。
