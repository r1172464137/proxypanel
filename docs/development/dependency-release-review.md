# 安全依赖与镜像发布前置审查

## 输入与边界

统一候选基线 `7feee36`；隔离 worktree 分支 `pi-web-agent-7d9d86de-44cf-4405-99fe-96b6302a3aad`。保留 Go 1.27.3 与现有栈，新增应用框架 0；不更改业务 Go、SQL、web、旧迁移或运行中服务。

重新解析 `/tmp/proxypanel-review-deploy-3435` 导出的依赖与 workflow，而非直接复制。原候选/当前输入文件 SHA256、扫描器构建信息、基线 SHA 保存在主工作区私有 `.ai/evidence/security/dependency-review/inputs.txt`；验证源码逐文件指纹在 `validated-inputs.json`，最终提交与文件指纹在 `final-receipt.json`。证据不依赖此 worktree 留存。

## 扫描解释与最小升级

使用 govulncheck v1.9.0 对 `./...` 扫描，完整 JSON 流分别保存在 `baseline.json`、`after.json`，stderr/exit 独立保留。**JSON 模式进程退出 0 不代表无漏洞。** 按每个 OSV 的最高证据层级互斥分类（trace 首帧含 function 为可达、仅含 package 为导入包、否则仅模块），结果：

| 输入 | 可达 OSV | 仅导入包 OSV | 仅模块 OSV | finding 记录总数 |
| --- | ---: | ---: | ---: | ---: |
| 基线 | 3 | 2 | 21 | 36 |
| 修复后 | 0 | 0 | 1 | 1 |

明细在 `scan-summary.json`。基线可达为 GO-2026-5004（pgx）、GO-2026-5970 与 GO-2026-6629（text）；包级为 GO-2026-4771/4772（pgx）。

- pgx/v5：5.7.6 → **5.9.2**，覆盖扫描要求的最高最低修复版本。
- x/crypto：0.42.0 → **0.56.0**，覆盖 GO-2026-6354/6355 等已知可修复告警。
- x/text：0.29.0 → **0.41.0**，修复最新可达告警，也是 crypto 的模块要求。
- x/sys：0.36.0 → **0.47.0**。曾尝试告警最低修复版 0.44.0，被 crypto 0.56.0 的模块约束拒绝；失败保存在 `go-get.log`。
- x/sync：0.17.0 → **0.22.0**，由最终模块图要求，并非任意升级。模块图在 `module-graph.txt`。

版本经 `go get` 解析、Go 校验和机制及 `go mod verify` 校验，并运行 `go mod tidy`。最终 go.mod 与来源候选一致，但来自当前输入的独立审查；go.sum 按当前模块图整理。API 兼容性证据为全 Go 构建、双架构命令编译及受影响路径选择测试，不表示穷尽上游所有语义变化。

残留 **GO-2026-5932**：`golang.org/x/crypto/openpgp` 停止维护、设计不安全，无修复版；本项目不导入该包，仅模块命中（原 OSV 见 `residual-osv.json`）。未删除依赖或屏蔽告警。文本默认 symbol 级门禁退出 0 仅表示无已知可达漏洞，不能宣传为零漏洞。

## 实际验证

- `go build ./...`、`go mod verify`：通过。
- 与 workflow 相同参数的 CGO_ENABLED=0、linux amd64/arm64、panel/agent 四个交叉编译：通过。
- `go test -json -count=1 ./internal/panel/secrets`：通过。
- `uv run --no-project python scripts/backend-test.py --json store/identity-tx protocol/offline-review protocol/handshake-review`：通过；分别核对 9、30、6 个登记顶层测试，未跳过。发现/执行原始日志及输入指纹已复制至主证据目录 `runner/`。
- PostgreSQL 使用既有专用开发测试实例配置（未输出凭据），选择中的 `newRegStore` 为每个测试建立随机 `ppreg_` 自有 schema 并清理，只操作自有 schema。
- 真实 Xray 使用 `/home/compile/Work/bin/xray`；握手测试只创建临时配置、回环端口和测试拥有的子进程，不连接或停止生产核心。
- actionlint v1.7.12 校验 `.github/workflows/images.yml`：通过。通过 Go 官方模块校验机制在普通用户缓存解析工具，不修改系统 Python 或全局代理。
- Node 22.23.2 下 `npm ci --include=dev`：通过；随后 `npm audit --audit-level=high` **失败**，现有锁文件的 zod 与 @vee-validate/zod 共 2 条 high（GHSA-mrhx-c7xc-m3w5，当前依赖树无可用修复解）。串联命令因此未运行 typecheck/test/build；不能把该 web 门禁报告为通过。证据 `web-gate.log`。

## 发布门禁与未完成项

Workflow 的镜像任务依赖 Go 和 web 门禁；使用真实 `go.mod` / uv 登记入口、Node 22.23.2 + `npm ci` 锁定安装；Go 默认 symbol 文本门禁与完整 JSON artifact 并存，未屏蔽模块告警。web high 审计失败会阻止发布，需另行授权修复 web 依赖，不得降低阈值。

任何登录/推送之前构建本地 amd64 镜像，调用仓库已有 `scripts/verify-panel-image.sh` 检验非 root（65532）、无镜像内 nginx、真实 PG、SPA/API-only 与宿主 nginx HTTPS 登录。既有 Dockerfile 的 Node 22 digest 与当前非 root/宿主 nginx 架构保持不变。

**未实际执行 GitHub workflow 或 Docker 构建/镜像冒烟，未 push、部署或启动代理服务。** 静态检查不等于容器成功。现有镜像验证脚本及发布后冒烟仍有 `curl | grep -q` / pipefail 的潜在 SIGPIPE 假阴性，不能通过忽略失败放行；若 CI 实际遇到，应在独立授权范围修复测试脚本。amd64 本地冒烟不等于 arm64 运行验收；跨架构发布仍需 CI 与目标架构验证。没有反复执行全仓测试，报告仅覆盖上述构建和最小选择。
