# 单进程面板镜像验证

验证对象：`deploy/Dockerfile.panel`（无 nginx，非 root Go 进程）、可选 SPA 托管、宿主 nginx 模板。以下为本次实际执行结果，不沿用旧镜像验收数字。

## 实测结果

| 检查 | 结果 |
|---|---|
| linux/amd64 面板镜像 | 真实 Docker 构建成功，运行 UID=65532，不存在 nginx 可执行文件 |
| PostgreSQL | 独立临时实例，面板真实迁移与登录，不使用正式数据库 |
| 模式 A：Go 托管 SPA | 首页、深层路由、真实 JS 资源、缺失资源 404、HTML no-store 均通过 |
| 模式 B：API-only + nginx 静态目录 | 面板根路径 404；nginx 提供镜像导出的同版 dist，API 反代正常 |
| nginx 配置模板 | A/B 两种形态均经过真实 `nginx -t` |
| HTTPS 会话 | A/B 均真实登录、回传 Secure Cookie、成功读取认证会话 |
| 真实 Chromium | A/B 均登录跳转、访问节点页、刷新深层路由、保持会话；无意外 console/pageerror |
| Compose 插值 | 两份 compose 均验证变量未定义时启用 SPA、显式空值时 API-only |
| Go | build、vet、19 个包全量测试通过，数据库相关用例连接专用测试库 |
| 前端 | typecheck、28 文件 / 178 单测、生产构建通过（测试环境 `NODE_ENV=development`） |

测试结束后临时容器、网络、临时数据库及代理均清理。没有替换现有运行中的面板，
没有安装或修改宿主机 nginx。

## 如何复现

构建过程见 [Docker 部署](../deployment/docker.md#镜像与构建)，然后：

```bash
bash scripts/verify-panel-image.sh proxypanel-panel:local
```

需要 Docker、curl、openssl、Python 3；会使用 `postgres:17-alpine` 和 `nginx:alpine`。
脚本创建独立资源并用 trap 清理，不访问正式数据库。

nginx 模板的验证使用**隔离 nginx 容器模拟外置反向代理**：只替换 upstream、证书与静态文件路径，
不改变 location 逻辑。这不等于已经在某台生产宿主机安装和 reload nginx。
临时自签证书仅用于测试，不作为生产关闭 TLS 校验的示例。

部署后也可以针对真实 HTTPS 入口跑浏览器烟测：

```bash
cd web
PANEL_BASE=https://panel.example.com PANEL_ADMIN_PASSWORD='你的密码' \
  node tests/host-nginx-smoke.mjs
```

仅当测试使用临时自签证书时允许设置 `PANEL_SMOKE_SELF_SIGNED=1`；真实部署应信任签发 CA。
镜像验证脚本的 `PANEL_BROWSER_HOOK` 可在临时 HTTPS 站点仍运行时调用浏览器测试，
通过环境变量提供 `PANEL_BASE`、`PANEL_SMOKE_MODE`、`PANEL_SMOKE_PORT` 与临时账号密码。

## 范围限制

- 未验证生产宿主 nginx reload、真实域名证书申请和公网入口。
- 未在本轮重新跑 Agent/Xray 数据面完整矩阵；历史结果见 [测试基线](testing.md#已验证的结果参考基线)。
- 本次镜像构建/运行验证为 amd64；另已交叉编译出静态 arm64 ELF，但未在 arm64 主机运行，多架构镜像发布仍由 CI 执行。不能把交叉编译成功当作目标平台运行证明。
- GitHub Actions / GHCR 发布结果必须另外查看 workflow 状态。
