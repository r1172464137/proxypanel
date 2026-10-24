# frontend-accept · product-readiness / frontend · R0 · 结构化输出 schema 补正

- **性质**：**仅结构化输出补正（schema correction）**，不是新一轮验收
- **原正式报告**：`.ai/quality/frontend-accept-product-readiness-frontend-r0-20261024-074320-75294ba0.md`
- **原审计附件**：`.ai/quality/frontend-accept-product-readiness-frontend-r0-20261024-074320-75294ba0-audit.json`
- **被补正的问题**：原报告返回值中的 `task_completion.change_scope` 使用了 `backend_source` / `frontend_source` / `tests` / `state_file` 等按层自造键，不符合 `protocols/agent-task-output-contract.md` 的固定九键（`backend` / `frontend` / `extension` / `miniapp` / `flutter` / `shopify` / `web_demo` / `extension_demo` / `flutter_demo`）。`tests` 与 `state_file` 属错层键（应为 `tests_to_run` 与报告正文的清理说明），不能塞进 `change_scope`。
- **结论**：**ACCEPTED_WITH_IMPROVEMENTS**（与原报告一致，未改变）
- **候选**：`706f0ba8abe0d9193c12436509264670040f2852`（基线 `c8af262`）
- **验收模式**：独立只读；不实现、不改源码/测试/断言/设计、不写 `.state.json`、不派代理、不 push、不部署、不访问任何部署端口或运行中的服务

## 1. 补正依据（本轮读取）

| 来源 | 用途 |
|---|---|
| `/home/compile/code/t-tools/protocols/acceptance-contract.md` | 验收专属必填字段（`acceptance_result`/`report_path`/`gates`/`findings`/`pending_verification`）与 `change_scope` 语义（只表示实际改动，只写报告/日志时九项全 false） |
| `/home/compile/code/t-tools/protocols/agent-task-output-contract.md` | `task_completion` envelope 与 `change_scope` 固定九键、`tests_to_run` 字段结构 |
| `/home/compile/code/t-tools/agents/frontend-accept.md` | 角色边界：只读验收、按公共协议写独立报告并返回验收字段 |
| 原正式报告 + 原 audit 附件 | 本轮结论、门禁、P2 问题、待承接场景的唯一事实来源 |

## 2. 补正内容（仅字段形状，不含业务判断）

1. `task_completion.change_scope` 改为固定九键，**全部 `false`**：本轮只新增报告与补正说明，未改动 backend / frontend / extension / miniapp / flutter / shopify / web_demo / extension_demo / flutter_demo 任一层源码或受版本控制的生成物。
2. 删除错层键：原 `tests` 键所表达的「未重跑 300/17、复用证据」已由报告 §4 与 `verification-evidence-contract` 复用条件记录；`state_file` 所表达的「未读写 `.state.json`」已由报告 §7 与本节记录。二者均不进 `change_scope`。
3. `tests_to_run` 返回空数组 `[]`：本轮为 schema 补正，未产生任何需补测的代码/测试改动，无可靠补测指令可给（符合 output-contract「无法给出可靠补测时在 summary 说明」）。
4. `gates`、`findings`、`pending_verification` 均**沿用原报告**：门禁结果不变（G1–G12 全部 passed），问题仍为原 3 条 P2，下游承接不变。

## 3. 不变项声明（防止补正被误读为重新验收）

- **未重跑** 300 个单测 / 17 个 probe 场景；未重新执行 typecheck、build、audit；未新增或删除任何门禁。
- **未重新验收**，**未新增问题**，**未改变原结论**，**未修复任何问题**。3 条 P2 保持原样待后续维护。
- **未改动源码/测试/fixture/锁/配置/设计**；未安装依赖；未启动或停止任何服务。
- 代码候选 `706f0ba` 与主当前 `1b0e7e4` 的差异仍仅为 `.ai/` 元数据（decision-log、交接、state、phase 计划），非 `.ai` 源码/测试路径 diff 为空；该指纹核对结论来自原报告的独立核对，本轮未重做。
- 203 独立 8445 预览部署属主会话后续 DEC017 新用户授权，与本格式补正无关；本轮**不访问部署端口、不访问任何服务**。
- **未读写 `.state.json`**；本轮唯一写入为本文件。

## 4. 门禁（沿用原报告，未变更）

| # | 门禁 | 结果 | 证据 |
|---|---|---|---|
| G1 | 受版本控制的输入与证据一致（非 `.ai` 源码/测试/fixture/锁/配置 = 候选） | passed | 原报告 §2/§4；`.ai/evidence/frontend-phase/frozen-706f0ba/20261024T071857Z-1046d1af/source-tree-diff.txt` |
| G2 | 类型检查实际通过 | passed | `summary.json.typecheck.exit=0`、`typecheck.log` |
| G3 | 全部前端单测实际执行、无失败/跳过 | passed | `vitest.json`（300/300 pass、0 fail、0 pending/todo、48 testResults） |
| G4 | 生产构建实际成功 | passed | `summary.json.build.exit=0`、`build.log` |
| G5 | 依赖审计无已知漏洞 | passed | `audit.json`（各级 total=0） |
| G6 | 设计/行为状态（setup 计数、未知≠0、工作台 20 项范围、恢复只 admin、成员不探测 admin、两类 token） | passed | 原报告 §3.1/§3.2；`SetupChecklist.vue`、`OverviewView.vue`、`lib/recoveryMode.ts`+spec、`MeSubscriptionView.vue`；probe 场景 4/8/9/10 |
| G7 | 查询/跨页选择/预览失效/不确定提交/批次包络/三类计数 | passed | 原报告 §3.3；`MembersView.vue`、`lib/paged.ts`+spec、`lib/selectionScope.ts`+spec、`ImpactPreviewDialog.vue`、`frontendContract.spec.ts`、`lib/batch.ts`；probe 场景 12 |
| G8 | 节点无损/脱敏/成对换钥/影响确认/版本冲突保草稿；方案 pending 只读+影响预览+退役；模板 409；邀请一次性 secret 清理 | passed | 原报告 §3.4；`lib/nodeDraft.ts`+spec、`NodeEditorView.vue`、`SubscriptionEditorView.vue`、`SubscriptionsView.vue`、`TemplatesView.vue`、`InvitationsView.vue`；probe 场景 6 |
| G9 | 成员逐字段状态/无链接降级/无未验证 import/copy 不旋转/下载不称已验证/用量对象与游标/两种 reset/密码 401/session epoch | passed | 原报告 §3.5；`lib/memberService.ts`+`MeSubscriptionView.spec.ts`、`lib/clientImport.ts`、`ClientImportGuide.vue`、`CopyField.vue`、`lib/meUsage.ts`+spec、`MeUsageView.vue`、`MeConnectView.vue`、`MeCredentialsView.vue`、`MeSecurityView.vue`、`stores/session.ts`+`session.spec.ts`；probe 场景 8/11 |
| G10 | 状态语言不假正常、readiness 三态、非契约正文不回显、390px/键盘/主题真实证据 | passed | 原报告 §3.6；`StatusBadge.vue`、`lib/readiness.ts`+spec、`api/http.ts normalizeError`、`RuntimeView.vue`；probe 场景 13–16、`assertMobilePage` 实现核对、`main-touch-review.json` |
| G11 | API 方法/路径/参数/响应/错误码/认证与 backend §4 一致（前端仅消费） | passed | 原报告 §3；`endpoints.ts` 逐条对照 §4.5/§4.6；`frontendContract.spec.ts`、`http.spec.ts` |
| G12 | 无新增框架/生成物漂移，派生物与清理 | passed | `package.json`/锁与证据指纹一致；无 OpenAPI 生成物改动；probe `cleanup.json schema_dropped=true`；本轮未产生源码派生物 |

> 未重跑说明沿用原报告：按 `verification-evidence-contract` 复用条件 1–3 全部满足。本轮仅做字段形状补正，未新增任何验证动作。

## 5. 问题（沿用原报告，无新增，无升级）

无 P0、无 P1。以下 3 条 P2 保持原样：

| 级别 | 位置 | 证据 | 影响 | 建议 |
|---|---|---|---|---|
| P2 | `web/src/views/me/MeSubscriptionView.vue:49`、`web/src/views/me/MeUsageView.vue:87` | 模板硬编码「数据更新时间：暂未提供」；`MeStatus`/`MeUsage` 响应未消费时间字段 | 设计 §4.10 要求展示数据更新时间；当前为诚实占位（不编造时间），不影响状态正确性 | 待契约提供成员侧采样/账本时间字段后接入；否则保持现有诚实文案 |
| P2 | `web/src/views/admin/BatchDetailView.vue` 的 `summary` 徽标 | 按原始 state 码渲染 `{{ status }}：{{ count }}`（如 `succeeded：1`），同页其他状态走 `StatusBadge` 中文 | 仅列表页局部一致性/可读性；不影响三类计数与业务判断 | 复用 `StatusBadge` 或已有中文映射，不改数据口径 |
| P2 | `web/src/components/ImpactPreviewDialog.vue:57` | `scope` 包含 `reason.value`，预览后修改「变更原因」也会触发 `scopeChanged` 要求重新预览 | 比服务端 `PREVIEW_STALE` 更保守，只增加一次只读预览，无错误提交风险 | 若确认 reason 不参与服务端结论，可从 scope 指纹中排除；需与后端 digest 口径一致后再改 |

## 6. 未验证 / 下游承接（沿用原报告，未屏蔽）

| 场景 | 承接 | 位置 |
|---|---|---|
| 18 个完整用户故事端到端（管理员/成员全旅程）、`scripts/web-demo-test-runner.py` 整文件终验 | web-demo | 主设计 §6；frontend §8.3/§8.4 |
| UI → 真实 Agent → 官方客户端 → 流量账本整链；真实 core 应用/回滚/部分完成 | web-demo | frontend §4.8；backend §10 |
| 双 Agent、离线/UDP、部分完成、旧 Agent/SQLite 与 core 跨版本 | web-demo | backend §6.10/§10 |
| 两种托管（Panel 托管 + 宿主 nginx 静态）下的登录/刷新、HTTPS/TLS、实际 host nginx、DNS、公有 CA | web-demo / 部署承接 | frontend §4.1、§8.4；DEC007 |
| 真实数据库备份/恢复、`getUpgradeReadiness` 在真实历史库上的 blocked/unknown 分支、升级回退 | web-demo | backend §9.9；frontend §4.8 |
| 屏幕阅读器审计、截图视觉回归（本轮仅有键盘/角色/label 与 computed 主题证据） | web-demo | probe `notRun` 明示 |
| 全部支持平台/客户端与 Vision | web-demo | 用户范围；主文档 §1.3 |
| 203 独立 8445 预览部署与用户可见检查 | 主会话（ACCEPTED 后按 DEC015/DEC016/DEC017 授权） | 本验收不执行部署，不以 stage 镜像当页面通过 |

本前端阶段可放行：必要门禁通过、无 P0/P1。**feature 未完成**，`product-readiness` 仍需 web-demo 收口上述场景；本补充报告不构成 web-demo 或部署验收。

## 7. 变更范围与清理

- 本轮唯一写入：本文件。未创建其它报告、日志或派生物。
- `change_scope` 九项全 `false`（无源码/测试/生成物改动）；`tests_to_run` 为空数组（无代码改动需补测）。
- 未读写 `.state.json`；未运行环境命令、未安装库、未启动/停止服务、未访问 203/8443/8445 或任何部署端口与运行中服务。
