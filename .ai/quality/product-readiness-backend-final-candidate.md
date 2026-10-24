# product-readiness：统一后端候选验证与验收交接

## 范围与结论边界

- 冻结候选：`6d1345c54b5fc5b3ea0033275338da8ff8362820`，分支 `product-readiness`。
- 当前请求阶段仍为 **backend**。开发与集中测试技术闭环已完成，**正式独立 backend-accept 尚须放行**；不是 feature 完成报告，不自动进入 frontend/web-demo。
- 用户授权并行准备的前端与真实浏览器资产已合入同候选；相关运行证据可以作为跨端准备材料，但不能据此把未请求的正式 phase 标成完成。
- 本次统一复验覆盖已登记 50 组，不冒充所有历史测试/平台/部署环境。未执行声明见 `.ai/evidence/final-6d1345c/unexecuted-declarations.json`。

## 同候选技术证据

| 门禁 | 当前结果 | 原始入口 |
|---|---|---|
| 后端集中回归 | 50 组；542 个不重复顶层测试 PASS；0 SKIP、0 missing；输入指纹稳定 | `.ai/evidence/backend-test/final-6d1345c/result.json`；实际 runner 报告 `.ai/evidence/backend-test/20261024T032530Z-ca404f10/result.json` |
| 构建与工具合同 | 项目 Python runner 合同、模块校验、Go 全仓构建及 Linux amd64/arm64 Panel+Agent 4 产物编译通过 | `.ai/evidence/backend-test/final-6d1345c/` |
| Go 漏洞扫描 | 固定 govulncheck v1.9.0；可达/已导入包告警 0；保留未导入 OpenPGP `GO-2026-5932` 模块级告警 | 同目录 `govulncheck.json`、`govulncheck.stderr`；不把 JSON 进程退出 0 当无漏洞 |
| 前端准备回归 | 219 单测；25 真实 Chromium 主路径；0 失败；npm 锁审计 0 | `.ai/evidence/final-6d1345c/frontend-data/result.json`；`.ai/evidence/browser/product-readiness/2026-10-24T03-30-58-223Z/` |
| 真实数据面 | Panel→Agent→官方客户端→完整回源→真实采样/账本；ACK 丢失同批重发不重复；暂停新旧连接拒绝；新导出恢复、旧凭据仍拒绝 | `.ai/evidence/data-plane/product-readiness/20261024T113127-f5d6a1b0/`；receipt 绑定同 HEAD，临时 PG/schema/core 已清理，原核心身份及 hash 未变 |
| 双 HTTPS 跨版升级 | 两种托管×旧版/升级 4 场景；`7feee36` 五迁移→当前六迁移；旧会话刷新可用，旧迁移 checksum/applied_at 不变；显式空 WEB_ROOT 有效 | `.ai/evidence/deployment/dual-hosting/final-6d1345c-attempt2/verified-summary.json`、`upgrade-proof.json`、`artifact-associations.json`、原始 migration/browser/runtime 记录 |
| 产物与清理关联 | 4 组产物关联匹配；升级目标 binary SHA256 还与后端最终编译产物一致；自有资源清零，原容器启动身份/网络与原镜像未变 | 同部署目录；核验脚本 `.ai/evidence/final-6d1345c/verify-deploy-proof.py` |

### 核心运行版本

Xray 26.3.27、sing-box 1.14.3、Mihomo 1.19.33（本次显式 `MIHOMO_BIN_VERSION=1.19.33`；不冒充默认 1.19.32）。官方二进制未修改，版本及 SHA256 在最终原始日志。

本地矩阵为 21 格：20 supported 格均完整内容及 SHA256 回源通过，1 格 Mihomo/Trojan/TCP/none 明确按 `stream_settings.security` 拒绝。不是 54 格全客户端矩阵。

## 已闭合的关键回归

- 永久 batch receipt 无历史表 FK；保留期清理后同请求回放不重推授权，异请求冲突；v1/v2 不猜旧调用参数。
- task ID/revision 与 item 原子绑定；旧/未来/其他 Agent 或 kind 回执不能完成新任务；未知旧 revision 留诊断并阻止重试。
- `Open` 的升级最终验证留在本次迁移/advisory lock 内；短命 receipt 仅绑定本次已提交文件，最终锁下验证当前摘要、材料和语义；无 pending 或后续启动不永久接受旧 baseline。
- 成员正常访问的 `ok` 不再渲染成“暂不可用”；恢复期间已有令牌只读可用，无令牌时仅导入区维护提示，成员不探测管理员恢复接口。
- 本人当前 Web 会话删除返回 `400 VALIDATION_FAILED` 且不撤销；logout 与其他本人会话救援仍可用；他人/不存在会话仍 404。
- 真实核心测试 discovery/execution 同时携带显式 build tags；不存在的测试、0 用例、SKIP 不计 PASS。
- SS2022 user-only 负例需要本次新服务端身份拒绝诊断与零回源计数；wrong-pin 需要明确 pin 诊断；不接受超时、任意非零、EOF/启动失败作为拒绝。合法凭据前后完整回源。

## 失败记录未覆盖或改写

- 原全 34 回归失败、P1 同次 Open 回归、旧夹具/错误契约预期及 SS2022 超时均保留在各自原报告。
- 最终 HTTPS 第一次调用因证据目录不在允许树内，被护栏在远端创建前拒绝。
- 第二次 HTTPS helper 实际退出 0、四场景成功，但外层汇总器误期待 helper 不会自动生成的 `upgrade-proof.json`。未重跑产品场景；从本次原始 SQL/运行参数/browser/fingerprint 严格重建核验，由独立原始证据核验监控通过。两次调用失败说明保存在最终部署 summary。
- 未削弱目录护栏、业务拒绝断言、授权/凭据安全语义或依赖审计阈值。

## 未覆盖与正式承接

- 11 个历史 `TestDocker*` 套件、`TestPlatformRealCore`、独立 `TestXrayVLESSRealityVision` 未运行；完整声明名已逐项列入 unexecuted-declarations。真实数据链/REALITY 握手等现有替代证据不等于这些套件运行成功。由独立 accept 判断本阶段必要性，必要但缺证据则拒绝，不自动转“不适用”。
- `measurement_candidate` 实验标签用例未运行；非 Linux 的 ACL runtime 未执行，已有交叉编译记录不计运行 PASS。
- arm64 只有编译，未真机执行；真实宿主 nginx、公共 DNS/CA 未验收。
- HTTPS 宿主层由隔离 nginx 容器模拟；升级 fixture 无 Agent，不能证明 Agent 停止材料。Agent 升级 proof 由本阶段真实 PG/readiness/Open 测试另行验证。
- 完整管理员/成员用户故事、全部客户端/平台、真实安装/systemd 与部署现场最终验收由已启用的后续 frontend/web-demo 按主设计及计划承接；不得在后端放行后声称全部业务验收完成。

## 执行与复用

- Go/Rust 适配：本项目为既有 Go/Vue，DEC-009 不改栈；使用项目 `uv run scripts/backend-test.py -- [选择]`、Go 构建，Cargo/Clippy 不适用，不创建 Rust 项目或替代框架。
- 全 50 升级原因：最终候选包含 Store/迁移/授权/Agent/协议/API/依赖跨模块改动，需要统一阶段门禁；各已知故障此前已定向红绿，不拿全量代替失败归因。
- 当前源码已提交且无 tracked WIP；同 HEAD 的必要原始输出、源码/迁移/锁/fixture 指纹与真实加载产物可核对。accept 优先独立核查有效证据，不为格式重跑已清理的场景。
- 允许的后续工作：正式只读 backend-accept；必要缺口由结论回交 dev/test。不得凭本报告代出验收结论，不改其他 phase 状态。
