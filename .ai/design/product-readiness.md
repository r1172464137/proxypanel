# Proxypanel product-readiness 技术设计（主文档）

**状态：** 技术设计候选；总体生成状态以 `product-readiness/.state.json` 为准。completed_stacks只表示已产出设计，不表示实现或测试完成。  
**工作分支：** product-readiness；源码基线 `3435f1d`。  
**契约源：** [backend.md](product-readiness/backend.md) §4；前端仅消费，不另立契约。

## 1. 需求概述

### 1.1 方案描述
把已具备管理能力的私有面板改造成可部署、可交付、可长期使用的产品。先核验可信底座，再交付管理员与成员任务旅程，最后对单一候选版本验收。设计文档、候选补丁、构建或子代理自述都不等于运行验收。

### 1.2 目标
- 管理员能从接入资源走到真实成员交付，成员能从邀请走到实际客户端连接。
- 授权、周期、账本、同步、进程与页面的状态真实一致。
- 保留高级原生配置与既有兼容入口，不用营销词或大段工程免责声明代替交互。
- 每个故事具备成功与异常的验证责任，未运行和不支持独立记录。

### 1.3 范围
包含：Go面板、Agent、PostgreSQL/SQLite一致性、安全与协议候选整合；Vue界面和文案；部署/升级/恢复；真实Web故事演示和数据面验收。

不包含：支付、订单、返利、商城、多租户、细粒度新角色、客户端软件、通用远程shell、重写技术栈或全协议全平台承诺。

### 1.4 已确认假设与证据限制
不引入新的产品假设。旧候选保留在独立副本，但必须审查、重建完整交付、记录内容指纹后才采用。旧日志早于改动或版本/fixture无法对应时，相关结果失效。没有把当前设计标成已实现。

## 2. 需求来源

### 2.1 用户故事
候选来源：[18条用户故事](../user-stories/core/product-readiness.md)。P0为001、003–014、016–017，P1为002、015、018；均属于本轮范围。角色为管理员与成员，不新增权限身份。

### 2.2 PRD / 其他需求文档
当前候选为[产品计划PRD](../prd/core/product-readiness.md)。无同名已发布PRD/故事索引；现有docs/design/product.md是参考草案，不伪装成正式发布基线。本轮对界面、流程和验收表达作增量替换，不引入收费业务或自动放宽权限。

### 2.3 技术预研
[技术预研](../tech-research/product-readiness.md)已选定复用Go/Vue及现有组件，不新增应用框架。业务变化限于已确认的目标体验、安全正确性、查询和交付闭环；设计细节只活在对应分端文档。

### 2.4 决策追踪
| Decision ID | 状态 | 设计落点 | 说明 |
|---|---|---|---|
| DEC-product-readiness-001 | Applied | §1/§4/§6及分端决策表 | 沿用已确认产品边界或明确的D2选择，不扩大范围 |
| DEC-product-readiness-002 | Applied | §1/§4/§6及分端决策表 | 沿用已确认产品边界或明确的D2选择，不扩大范围 |
| DEC-product-readiness-003 | Applied | §1/§4/§6及分端决策表 | 沿用已确认产品边界或明确的D2选择，不扩大范围 |
| DEC-product-readiness-014 | Applied | §1/§4/§6及分端决策表 | 沿用已确认产品边界或明确的D2选择，不扩大范围 |
| DEC-product-readiness-005 | Applied | §1/§4/§6及分端决策表 | 沿用已确认产品边界或明确的D2选择，不扩大范围 |
| DEC-product-readiness-006 | Applied | §1/§4/§6及分端决策表 | 沿用已确认产品边界或明确的D2选择，不扩大范围 |
| DEC-product-readiness-007 | Applied | §1/§4/§6及分端决策表 | 沿用已确认产品边界或明确的D2选择，不扩大范围 |
| DEC-product-readiness-008 | Applied | §1/§4/§6及分端决策表 | 沿用已确认产品边界或明确的D2选择，不扩大范围 |
| DEC-product-readiness-009 | Applied | §1/§4/§6及分端决策表 | 沿用已确认产品边界或明确的D2选择，不扩大范围 |

决策账本：[product-readiness](../decision-log/product-readiness.md)。分端只列相关子集，本表覆盖全部9项决定。无未解决用户裁决。

### 2.5 设计覆盖矩阵
| Requirement / Story ID | 目标 | 设计落点 | 契约/组件 | 测试与验收 | 文件影响 |
|---|---|---|---|---|---|
| US-PR-001 | HTTPS与两种托管 | backend §6.9；frontend §4.1 | 部署与同源登录 | backend / web-demo；未执行 | §8对应真实模块/测试 |
| US-PR-002 | 首次设置 | backend §4.2；frontend §4.2 | getAdminOverview / setup | frontend / web-demo；未执行 | §8对应真实模块/测试 |
| US-PR-003 | 服务器接入 | backend §6.2；frontend §4.3 | 注册码、agents/core-instances | backend / web-demo；未执行 | §8对应真实模块/测试 |
| US-PR-004 | 节点配置 | backend §6.7；frontend §4.4 | 节点编辑、TLS/REALITY | backend / frontend / web-demo；未执行 | §8对应真实模块/测试 |
| US-PR-005 | 服务方案 | backend §6.4；frontend §4.5 | subscriptions / groups | backend / frontend / web-demo；未执行 | §8对应真实模块/测试 |
| US-PR-006 | 邀请开通 | backend §6.2；frontend §4.6 | redeemInvitation / createMember | backend / web-demo；未执行 | §8对应真实模块/测试 |
| US-PR-007 | 客户端导入 | backend §6.7；frontend §4.9 | 格式能力与个人链接 | frontend / web-demo；未执行 | §8对应真实模块/测试 |
| US-PR-008 | 用量与期限 | backend §6.4；frontend §4.10 | getMeStatus / getMeUsage | backend / web-demo；未执行 | §8对应真实模块/测试 |
| US-PR-009 | 账号保护 | backend §6.2/§7；frontend §4.11 | session/password/rotations | backend / web-demo；未执行 | §8对应真实模块/测试 |
| US-PR-010 | 批量闭环 | backend §6.5；frontend §4.7 | previewBatch / submitBatch / getBatch | backend / frontend / web-demo；未执行 | §8对应真实模块/测试 |
| US-PR-011 | 撤权与恢复 | backend §6.6；frontend §4.8 | 快照、进程、当前与历史授权 | backend / web-demo；未执行 | §8对应真实模块/测试 |
| US-PR-012 | 故障与重试 | backend §6.5/§6.6；frontend §4.8 | 回滚/停止确认/任务重试 | backend / web-demo；未执行 | §8对应真实模块/测试 |
| US-PR-013 | 历史计量 | backend §6.4；frontend §4.10 | 周期与执行授权历史 | backend / web-demo；未执行 | §8对应真实模块/测试 |
| US-PR-014 | 恢复核对 | backend §6.3；frontend §4.8 | recovery-mode与救援例外 | backend / web-demo；未执行 | §8对应真实模块/测试 |
| US-PR-015 | 多端与三态 | frontend §4.12 | 键盘、移动、亮暗和数据状态 | frontend / web-demo；未执行 | §8对应真实模块/测试 |
| US-PR-016 | 升级恢复 | backend §5.3/§6.9；frontend §4.8 | 只读预检、迁移、回退边界 | backend / web-demo；未执行 | §8对应真实模块/测试 |
| US-PR-017 | 权限秘密 | backend §7；frontend §4.11 | 角色、本机、本人与静态边界 | backend / frontend / web-demo；未执行 | §8对应真实模块/测试 |
| US-PR-018 | 真实查询 | backend §6.8；frontend §4.7 | keyset与明确选择集合 | backend / frontend / web-demo；未执行 | §8对应真实模块/测试 |

## 3. 现有实现分析

### 3.1 后端
单Go模块，cmd/panel与cmd/agent分别启动控制面/执行端；PG存业务、Redis限流、Agent SQLite存授权与队列。候选安全、存储、Agent、协议和部署修改尚未成为统一版本。细节见backend §3与§6.10，不从旧摘要推断通过。

### 3.2 前端
Vue3/Router、Tailwind/Reka、手写API封装与useResource现有模式；复用组件再扩展，不引入React、Zustand或新Query框架。默认页面围绕任务和结果，高级设置保留。详见frontend §3。

### 3.3 Flutter
不适用：不交付移动应用；手机验收是Web窄屏与受支持客户端导入旅程。

### 3.4 可复用点与受影响范围
复用PageHeader、DataTable、StatusBadge、CopyField、ImpactPreviewDialog及既有领域/配置代码。主审补齐候选真实路径，防止分端文件清单漏掉安全依赖、限流、静态护栏、证书投影与已有高价值回归。候选压缩/重复测试不是稳定验收oracle，不能照单全收。

## 4. 总体方案

### 4.1 总体设计与关键取舍
1. 在独立分支形成单候选树；不在main或既有部署直接覆盖未审查补丁。
2. 服务方案是授权规则，订阅链接是个人配置地址；只改UI表达，不重命名兼容实体。
3. 保存、同步、实际连接分别表达；未知不变零、回滚不变目标成功、结果未确认不诱导重复提交。
4. 恢复门禁同事务，密码救援例外明确；移机旧授权、最终采样与终止回执单独核验，不污染新机器状态。
5. 升级只读预检、原账本守恒；无法证明的旧队列/周期资料阻断升级，不丢批次、不自动补算/暂停成员。
6. 角色范围和旧链接保持；框架默认不覆盖本Go/Vue项目，测试脚本需项目适配。
7. 业务测试分层，优先最小可靠定向验证，阶段收尾才按跨模块门禁升级全量。

### 4.2 交付端范围
| 端 | 分端设计文档 | 状态 |
|---|---|---|
| backend | `.ai/design/product-readiness/backend.md` | 适用：面板/Agent/存储/部署 |
| frontend | `.ai/design/product-readiness/frontend.md` | 适用：Vue控制台与成员入口 |
| extension | 不创建 | 不适用：无扩展交付 |
| flutter | 不创建 | 不适用：无Flutter应用 |
| shopify | 不创建 | 不适用：无Shopify工程 |

按设计启用的执行phase为backend、frontend、web-demo。web-demo是Web真实故事演示，不是另一产品端；资产由该phase交付，不把Go或组件mock测试当完整故事验收。

### 4.3 跨端契约（API 契约摘要）
唯一设计源：[backend.md §4](product-readiness/backend.md)。下表只是操作导航，不复制字段/数据库细节。backend-contract.json与frontend-dependencies.json是带源文档指纹的派生校验缓存，不是第二契约源。

| Operation ID | 方法 | 路径 | 用途 | 调用方 |
|---|---|---|---|---|
| getHealth | GET | `/healthz` | 见 backend §4 | internal |
| getReady | GET | `/readyz` | 见 backend §4 | internal |
| login | POST | `/api/v1/auth/login` | 见 backend §4 | frontend |
| getSession | GET | `/api/v1/auth/session` | 见 backend §4 | frontend |
| logout | POST | `/api/v1/auth/logout` | 见 backend §4 | frontend |
| redeemInvitation | POST | `/api/v1/auth/redeem` | 见 backend §4 | frontend |
| getSubscriptionOutput | GET | `/sub/{token}` | 见 backend §4 | frontend |
| getMeStatus | GET | `/api/v1/me/status` | 见 backend §4 | frontend |
| listMeNodes | GET | `/api/v1/me/nodes` | 见 backend §4 | frontend |
| getMeUsage | GET | `/api/v1/me/usage` | 见 backend §4 | frontend |
| getMeSubscription | GET | `/api/v1/me/subscription` | 见 backend §4 | frontend |
| rotateMeSubscriptionToken | POST | `/api/v1/me/subscription/token/rotations` | 见 backend §4 | frontend |
| rotateMeCredentials | POST | `/api/v1/me/credentials/rotations` | 见 backend §4 | frontend |
| changeMePassword | POST | `/api/v1/me/password` | 见 backend §4 | frontend |
| listMeSessions | GET | `/api/v1/me/sessions` | 见 backend §4 | frontend |
| deleteMeSession | DELETE | `/api/v1/me/sessions/{sessionId}` | 见 backend §4 | frontend |
| registerAgent | POST | `/api/v1/agent/register` | 见 backend §4 | agent |
| getAgentSnapshot | GET | `/api/v1/agent/snapshot` | 见 backend §4 | agent |
| reportAgentApplied | POST | `/api/v1/agent/applied` | 见 backend §4 | agent |
| submitAgentUsage | POST | `/api/v1/agent/usage` | 见 backend §4 | agent |
| agentHeartbeat | POST | `/api/v1/agent/heartbeat` | 见 backend §4 | agent |
| getRecoveryMode | GET | `/api/v1/admin/recovery-mode` | 见 backend §4 | frontend |
| enterRecoveryMode | POST | `/api/v1/admin/recovery-mode/enter` | 见 backend §4 | frontend |
| exitRecoveryMode | POST | `/api/v1/admin/recovery-mode/exit` | 见 backend §4 | frontend |
| getAdminOverview | GET | `/api/v1/admin/overview` | 见 backend §4 | frontend |
| getUpgradeReadiness | GET | `/api/v1/admin/upgrade-readiness` | 见 backend §4 | frontend |
| listAgents | GET | `/api/v1/admin/agents` | 见 backend §4 | frontend |
| listCoreInstances | GET | `/api/v1/admin/core-instances` | 见 backend §4 | frontend |
| listServers | GET | `/api/v1/admin/servers` | 见 backend §4 | frontend |
| createServer | POST | `/api/v1/admin/servers` | 见 backend §4 | frontend |
| getServer | GET | `/api/v1/admin/servers/{serverId}` | 见 backend §4 | frontend |
| updateServer | PATCH | `/api/v1/admin/servers/{serverId}` | 见 backend §4 | frontend |
| deleteServer | DELETE | `/api/v1/admin/servers/{serverId}` | 见 backend §4 | frontend |
| createRegistrationCode | POST | `/api/v1/admin/servers/{serverId}/registration-codes` | 见 backend §4 | frontend |
| listNodes | GET | `/api/v1/admin/nodes` | 见 backend §4 | frontend |
| createNode | POST | `/api/v1/admin/nodes` | 见 backend §4 | frontend |
| getNode | GET | `/api/v1/admin/nodes/{nodeId}` | 见 backend §4 | frontend |
| updateNode | PATCH | `/api/v1/admin/nodes/{nodeId}` | 见 backend §4 | frontend |
| deleteNode | DELETE | `/api/v1/admin/nodes/{nodeId}` | 见 backend §4 | frontend |
| listNodeGroups | GET | `/api/v1/admin/node-groups` | 见 backend §4 | frontend |
| createNodeGroup | POST | `/api/v1/admin/node-groups` | 见 backend §4 | frontend |
| getNodeGroup | GET | `/api/v1/admin/node-groups/{groupId}` | 见 backend §4 | frontend |
| updateNodeGroup | PATCH | `/api/v1/admin/node-groups/{groupId}` | 见 backend §4 | frontend |
| deleteNodeGroup | DELETE | `/api/v1/admin/node-groups/{groupId}` | 见 backend §4 | frontend |
| listSubscriptions | GET | `/api/v1/admin/subscriptions` | 见 backend §4 | frontend |
| createSubscription | POST | `/api/v1/admin/subscriptions` | 见 backend §4 | frontend |
| getSubscription | GET | `/api/v1/admin/subscriptions/{subscriptionId}` | 见 backend §4 | frontend |
| updateSubscription | PATCH | `/api/v1/admin/subscriptions/{subscriptionId}` | 见 backend §4 | frontend |
| deleteSubscription | DELETE | `/api/v1/admin/subscriptions/{subscriptionId}` | 见 backend §4 | frontend |
| listMembers | GET | `/api/v1/admin/members` | 见 backend §4 | frontend |
| createMember | POST | `/api/v1/admin/members` | 见 backend §4 | frontend |
| getMember | GET | `/api/v1/admin/members/{memberId}` | 见 backend §4 | frontend |
| updateMember | PATCH | `/api/v1/admin/members/{memberId}` | 见 backend §4 | frontend |
| deleteMember | DELETE | `/api/v1/admin/members/{memberId}` | 见 backend §4 | frontend |
| rotateMemberSubscriptionToken | POST | `/api/v1/admin/members/{memberId}/subscription-token/rotations` | 见 backend §4 | frontend |
| rotateMemberCredentials | POST | `/api/v1/admin/members/{memberId}/credentials/rotations` | 见 backend §4 | frontend |
| createMemberQuotaAdjustment | POST | `/api/v1/admin/members/{memberId}/quota-adjustments` | 见 backend §4 | frontend |
| listTemplates | GET | `/api/v1/admin/templates` | 见 backend §4 | frontend |
| createTemplate | POST | `/api/v1/admin/templates` | 见 backend §4 | frontend |
| getTemplate | GET | `/api/v1/admin/templates/{templateId}` | 见 backend §4 | frontend |
| updateTemplate | PATCH | `/api/v1/admin/templates/{templateId}` | 见 backend §4 | frontend |
| deleteTemplate | DELETE | `/api/v1/admin/templates/{templateId}` | 见 backend §4 | frontend |
| listInvitations | GET | `/api/v1/admin/invitations` | 见 backend §4 | frontend |
| createInvitation | POST | `/api/v1/admin/invitations` | 见 backend §4 | frontend |
| revokeInvitation | POST | `/api/v1/admin/invitations/{invitationId}/revoke` | 见 backend §4 | frontend |
| getAdminUsage | GET | `/api/v1/admin/usage` | 见 backend §4 | frontend |
| previewBatch | POST | `/api/v1/admin/batches/preview` | 见 backend §4 | frontend |
| submitBatch | POST | `/api/v1/admin/batches` | 见 backend §4 | frontend |
| listBatches | GET | `/api/v1/admin/batches` | 见 backend §4 | frontend |
| getBatch | GET | `/api/v1/admin/batches/{batchId}` | 见 backend §4 | frontend |
| listEvents | GET | `/api/v1/admin/events` | 见 backend §4 | frontend |
| ackEvent | POST | `/api/v1/admin/events/{eventId}/ack` | 见 backend §4 | frontend |
| listTasks | GET | `/api/v1/admin/tasks` | 见 backend §4 | frontend |
| retryTask | POST | `/api/v1/admin/tasks/{taskId}/retry` | 见 backend §4 | frontend |
| listAuditLogs | GET | `/api/v1/admin/audit-logs` | 见 backend §4 | frontend |
| getSettings | GET | `/api/v1/admin/settings` | 见 backend §4 | frontend |
| updateSettings | PUT | `/api/v1/admin/settings` | 见 backend §4 | frontend |

## 5. 分端设计摘要

### 5.1 后端
- 身份/邀请/会话、恢复门禁与授权快照同一可信规则。
- 历史周期、批次幂等、回执和移机执行授权必须守恒且可核对。
- Agent收紧先行、持久化/采样有序、停止确认与崩溃恢复不能假成功。
- 静态目录与安装器安全候选复用，升级/回退边界明确。
详见[后端设计](product-readiness/backend.md)。

### 5.2 前端
- 管理员任务化导航、工作台与真实首次设置；成员首页优先状态/额度/导入。
- 实例/任务/回滚/结果未确认各有持久反馈，不隐藏真实危险影响。
- 复用Vue状态与组件，原生配置无损，真实分页与选择集合，错误恢复可操作。
- 主审明确：资料部分不完整不能遮掉已知用量；“其他节点未受影响”必须有实际证据，不能写保证。
详见[前端设计](product-readiness/frontend.md)。

### 5.3 Flutter
不适用，不生成文档。

### 5.4 Chrome 扩展
不适用，不生成文档。Shopify亦不适用。

## 6. 测试与验收策略

### 6.1 各端测试入口
| 场景/验收目标 | 入口与关键断言 | 承接phase/角色 | 当前结果 |
|---|---|---|---|
| 授权/邀请/会话/恢复竞态 | 项目scripts/backend-test.py选择真实Go包与-run；Expected Test Manifest非零且无漏项 | backend/dev-test-accept | 未执行 |
| 周期/迟报/移机/幂等/旧回执 | 隔离PG场景：原值守恒、跨边界、同批重发、终止不污染新实例 | backend/test-accept | 未执行 |
| Agent持久化/故障/已知到期 | 官方核心与临时状态：同实例慢校验、跨节点收紧、队列/磁盘失败、崩溃、停止未确认 | backend/test-accept | 未执行 |
| 配置与客户端能力 | 无损往返、合法CA usage/握手、REALITY成对换钥、完整传输投影 | backend + web-demo | 未执行 |
| 前端局部边界 | web中现有typecheck/build/vitest入口；只为Demo难稳定观察的规则新增局部用例 | frontend/dev-test-accept | 未执行 |
| 用户旅程与异常 | web/tests/product-readiness-e2e.mjs及现有脚本适配，持久结果而非toast唯一断言 | web-demo/dev-accept | 未执行 |
| 升级/恢复/宿主反代 | 新库/旧库/失败文件回滚与已应用前缀、两个托管、HTTPS会话与深链刷新 | backend + web-demo | 未执行 |
| 秘密/角色/静态资源 | 越权负例、失效会话、受限静态打开、日志/产物无真实凭据 | backend + frontend + web-demo | 未执行 |

Go项目不调用插件默认Cargo runner。先交付本项目脚本、确认uv/Go工具链和非破坏性测试库，再执行统一入口；不因为本机无Docker而伪造容器结果，实际用获授权隔离远端。必要MCP缺失时停止依赖该工具的步骤，不以其他证据冒充。

### 6.2 Demo / E2E 主验收路径
管理员部署与设置→真实服务器/节点→服务方案→邀请→成员浏览器导入→真实客户端流量→PG用量→暂停/恢复及异常处理。覆盖18故事与两种前端托管，桌面/手机、亮暗、键盘与关键三态均有对应断言。

真实矩阵允许分片，但必须同候选构建/锁文件/配置/fixture指纹，记录失败与重测关联；不支持、未运行和失败分开。旧11个Docker专项不能随便启用写死旧资源的测试，也不能消失：为每项建立真实执行或等价覆盖关系，必要场景缺失则最终拒绝。

### 6.3 回归风险点
- 候选报告早于源码、只有同HEAD却WIP不同、只看文件名相等。
- 旧面板严格JSON与新Agent字段、最终水位/回执/stop确认的不一致。
- 老队列不在PG可见；只看面板SQL无法证明旧Agent队列清空。
- 手工保存假证书路径只证明落库，不能证明握手；HTTP200或端口监听不等于服务实际可用。
- 修改周期规则不能重写过去；修前实现的错误期望不能作oracle。

## 7. 风险与验证动作
| 风险项 | 等级 | 缓解或验证动作 | 负责人 | 完成条件 |
|---|---|---|---|---|
| 旧候选/报告不匹配最终树 | P0 | 重导出并记录内容指纹，按不变量分批整合 | 主会话/backend | 每批定向验证与最后统一回归 |
| 历史/升级/旧队列不可证明 | P0 | 只读预检和一致证据，阻断而非自动补算/丢数据 | backend | 新旧库矩阵、原账本/授权守恒 |
| 移机末采样/终止与新实例混淆 | P0 | 旧执行授权窗口和释放revision；只更新正确身份的结果 | backend | A-B-A/并发/末采样等用例真实通过 |
| 慢校验/锁拖延撤权或过期 | P0 | 授权收紧通道与有界门等待；同/跨实例都测 | backend | 停新旧连接且无假成功 |
| 页面把保存、回滚、未知作健康 | P0 | 共用状态来源和逐字段证据，不承诺未验证连接 | frontend/web-demo | UI/API/真实进程与流量相互对应 |
| 验收环境缺工具/旧数据污染 | P1 | 预检、自有label资源、串行故障注入与finally复位 | 主会话/测试角色 | 可复现日志、清理核查 |
| 过度重写和重复helper | P1 | 复用真实落点；同义实现只留一份，无增量测试不复制 | 各dev/accept | 代码审查及路径残留检查 |

以上是有明确承接的验证动作，不是产品决策延期。没有未解决的用户裁决。

## 8. 文件影响范围（全量汇总）

本表汇总分端影响，也是后续任务拆分依据。候选复核行不意味着原样采用；压缩/重复测试需证明增量价值。原候选SQL0005–0009不自动采用，设计选择的追加文件单独审查。实现偏离文件落点或契约时先修设计与状态，再重新核查，不平行造第二套helper。

| 文件 | 操作 | 说明 | 来源分端 |
|---|---|---|---|
| `internal/panel/api/api.go` | MODIFY | 列表参数/cursor 装配；`getUpgradeReadiness` 路由；写路由统一恢复门禁顺序 | backend |
| `internal/panel/api/handlers.go` | MODIFY | login 走 `CreateSessionForPassword`；redeem 事务语义；me/status、me/subscription 新字段；me/usage 保留嵌套 + 真实游标 | backend |
| `internal/panel/api/admin.go` | MODIFY | 列表分页与 `q`；overview `setup`；节点无损 PATCH 保持 | backend |
| `internal/panel/api/admin2.go` | MODIFY | 成员/方案/模板/邀请列表分页检索；成员详情 `data_complete`；恢复端点核对项 | backend |
| `internal/panel/api/batches.go` | MODIFY | 列表分页；错误码/重试语义；幂等复用回放 | backend |
| `internal/panel/api/helpers.go` | MODIFY | 通用字段错误映射与 `requireVersion`；游标编解码只在 pagination.go，不重复实现 | backend |
| `internal/panel/api/readiness.go` | CREATE | `handleUpgradeReadiness` + 响应映射（命名依据：同目录按领域拆分 `batches.go`/`static.go`） | backend |
| `internal/panel/api/pagination.go` | CREATE | 游标编解码、limit 夹取、keyset 条件构造（同目录既有 `helpers.go` 的辅助风格） | backend |
| `internal/panel/api/security_regression_test.go` | CREATE | 越权、CSRF、限流、秘密不外泄负例（候选 patch 已有同名资产，需按本设计重建） | backend |
| `internal/panel/api/recovery_gate_test.go` | CREATE | 恢复冻结 + 改密例外 + 并发进入 | backend |
| `internal/panel/api/pagination_test.go` | CREATE | 分页/检索/cursor 非法/末页 | backend |
| `internal/panel/api/integration_test.go` | MODIFY | 既有集成断言跟进真实分页与新字段 | backend |
| `internal/panel/api/acceptance_test.go` | MODIFY | 端到端 API 场景（邀请、恢复、批量）更新断言 | backend |
| `internal/panel/store/store.go` | MODIFY | 新增 `inReadTx`；迁移器对新增文件的兼容；`querier` 复用 | backend |
| `internal/panel/store/access.go` | MODIFY | `loadEvidenceBatchQ`/`nodeGroupMapQ` 快照变体；`data_complete` 判定 | backend |
| `internal/panel/store/snapshot.go` | MODIFY | 单 MVCC 快照构建 + 周期推进重试 + revision 同源 | backend |
| `internal/panel/store/members.go` | MODIFY | 邀请兑换开通事务；`CreateSessionForPassword`；会话撤销矩阵；`UpdateMember` 门禁与区间记录；`listMembersPage` | backend |
| `internal/panel/store/periods.go` | CREATE | 复用候选真实落点承接 PeriodRecord、区间历史与重叠检查，按本设计审查改造，不另建重复 period_history.go | backend |
| `internal/panel/store/batch_idempotency.go` | CREATE | 持久幂等 tombstone 读写与回放（与 `batch_submit.go` 同域） | backend |
| `internal/panel/store/batch_submit.go` | MODIFY | 门禁顺序、幂等回放、任务 revision 绑定、`batch_items` 回填修复 | backend |
| `internal/panel/store/batch_preview.go` | MODIFY | 目标集合解析、conclusion 重算、分页相关过滤 | backend |
| `internal/panel/store/usage.go` | MODIFY | 归属校验（完整区间 `NodeAuthorizedForRange`、`LEDGER_UNATTRIBUTABLE`）、迟报/时钟、额度恢复重建凭据；不改既有账本行语义 | backend |
| `internal/panel/store/ops.go` | MODIFY | 回执 revision 语义、任务列表过滤、`retryTask` 新 revision、overview `setup`、恢复退出核对；`validateAgentInstancesTx` 接线（terminal-only 走历史窗口，其余保持当前 server 校验，SEC02 合并） | backend |
| `internal/panel/store/resources.go` | MODIFY | keyset 分页变体、`q` 过滤；节点移机时同事务关闭/开启 `node_server_history` 并发布 revision；DELETE 语义按 §4.3（订阅退役、模板引用 409） | backend |
| `internal/panel/store/readiness.go` | CREATE | `UpgradeReadiness` 查询与稳定检查码（与 `maintenance.go` 同目录的只读诊断模块） | backend |
| `internal/panel/store/write_guard.go` | CREATE | `requireWritableTx`/`controlModeTx`/`inWritableTx`/`passwordOnlyMemberChange`（单一门禁实现） | backend |
| `internal/panel/store/maintenance.go` | MODIFY | 保留期清理不删 `batch_idempotency`；告警类型补充 | backend |
| `internal/panel/store/snapshot_consistency_test.go` | CREATE | 快照 revision 与内容同源、并发撤权 | backend |
| `internal/panel/store/batch_unit_test.go` | MODIFY | 预览/提交/幂等断言跟进 tombstone 与 revision 绑定 | backend |
| `internal/panel/store/list_batches_test.go` | MODIFY | 列表分页与时间排序断言 | backend |
| `internal/panel/api/patch_test.go` | MODIFY | 版本冲突、`nullable` 清除、无损编辑断言跟进 | backend |
| `internal/panel/api/batches_test.go` | CREATE | 幂等回放、`PREVIEW_STALE`/`PREVIEW_CONCLUSION_CHANGED`、恢复冻结（同目录按领域拆分测试文件） | backend |
| `internal/panel/store/periods_test.go` | CREATE | 区间记录、推进不重叠、迟报归属 | backend |
| `internal/panel/store/readiness_test.go` | CREATE | 各类 blocker、空库/旧库/校验和 | backend |
| `internal/panel/store/write_guard_test.go` | CREATE | 门禁锁序、改密例外、组合变更拒绝 | backend |
| `internal/panel/period/period.go` | MODIFY | `Transition(oldEnd, anchor, loc, at)`；边界/DST 单测跟进 | backend |
| `internal/panel/period/period_test.go` | MODIFY | 追加 Transition 边界用例 | backend |
| `internal/panel/access/access.go` | MODIFY | 保持 `access/v1` 判定顺序；补充 `data_complete` 所需输入/原因码（不改已有原因码语义） | backend |
| `internal/panel/domain/errors.go` | MODIFY | 新增 3 个稳定码 | backend |
| `internal/panel/domain/types.go` | MODIFY | `Member.DataComplete/DataIssues`、`OverviewSetup`、`FormatStatus`、`UpgradeReadiness` 等 DTO | backend |
| `internal/contracts/agent.go` | MODIFY | 注释冻结“不新增 Agent 请求顶层字段”，登记 `Metrics` 保留键与 `stopping` 状态；结构体字段集合不变 | backend |
| `internal/contracts/agent_test.go` | CREATE | 新旧 Agent/面板 JSON 互操作：旧面板 `DisallowUnknownFields` 拒绝新顶层字段的负例、Metrics 保留键透传、缺键按 unknown | backend |
| `internal/agent/supervisor/supervisor.go` | MODIFY | 收紧优先 apply、停机确认汇总、健康上报 | backend |
| `internal/agent/supervisor/gate.go` | CREATE | 可取消 apply/sample gate（候选已实现，需重审后按本设计落地） | backend |
| `internal/agent/supervisor/status.go` | MODIFY | `status.json` 增加 `health`/未确认停机标记（仅状态码） | backend |
| `internal/agent/supervisor/gate_test.go` | CREATE | ctxMutex 取消不累积 goroutine、不丢 token | backend |
| `internal/agent/supervisor/review_crash_real_test.go` | CREATE | 真实核心崩溃/过载停机持久化（需真实核心，缺环境记未执行） | backend |
| `internal/agent/xray/manager.go` | MODIFY | 收紧类变更优先、Stop 确认、采样钩子覆盖所有 stop 路径 | backend |
| `internal/agent/xray/runner.go` | MODIFY | 进程退出确认与身份校验（pidfile 归属） | backend |
| `internal/agent/xray/review_tighten_test.go` | CREATE | 他节点慢校验不阻塞撤权、多节点撤权、停止失败不回退 | backend |
| `internal/agent/xray/review_stop_test.go` | CREATE | 未确认停机保留记录、幸存核心阻止替换、过期实例 fail-closed | backend |
| `internal/agent/xray/manager_test.go` | MODIFY | 适配收紧/停机语义 | backend |
| `internal/agent/xray/review_handoff_test.go` | CREATE | 同实例慢校验下本地到期仍撤权（有界等待/fail-closed）、跨节点收紧先执行 | backend |
| `internal/panel/store/handoff_regression_test.go` | CREATE | 移机末采样/全区间覆盖与未知间隙拒绝、terminal-only 回执不污染 B、A-B-A 晚到回执、无历史 403 | backend |
| `internal/agent/state/store.go` | MODIFY | `apply_intent`、`CommitSnapshot` 事务、`NarrowExpiredInstance` 不覆盖新提交 | backend |
| `internal/agent/state/review_commit_test.go` | CREATE | 全有或全无、失败不推进水位、计量串行化 | backend |
| `internal/agent/reporting/reporting.go` | MODIFY | 采样重试与最终采样失败语义、健康暴露 | backend |
| `internal/agent/reporting/sampling_retry_test.go` | MODIFY | 断言不重复计费与失败可见 | backend |
| `internal/panel/protocol/protocol.go` | MODIFY | REALITY 密钥成对校验、无损字段保留、错误字段定位 | backend |
| `internal/panel/protocol/reality_protocol_test.go` | MODIFY | 换钥/配对/TLS 材料负例 | backend |
| `internal/panel/clientconfig/clientconfig.go` | MODIFY | `format_status` 可用性计算、不可表达显式错误 | backend |
| `internal/panel/clientconfig/clientconfig_test.go` | MODIFY | 四种格式可用性与不可表达断言 | backend |
| `cmd/panel/main.go` | MODIFY | 增加 `check` 子命令调用只读预检（默认仍是启动服务）、构建版本注入、启动失败信息 | backend |
| `cmd/panel/check_test.go` | CREATE | 预检退出码 0/2/3 与 JSON 输出（同目录已有 `cookie_samesite_test.go`/`transport_test.go`，命名依据充分） | backend |
| `migrations/0005_period_and_owner_history.sql` | CREATE | `member_periods` + `node_server_history` + 索引 + 可证明当前区间/当前归属回填 | backend |
| `migrations/0006_batch_task_durability.sql` | CREATE | `batch_idempotency` + `tasks.revision` + 回填/索引/孤儿修复 | backend |
| `migrations/0007_usage_queried_indexes.sql` | CREATE | 仅新增 `usage_entries(member_id,period_start,node_id)` 与 `usage_batches(agent_id,received_at DESC)` 索引，不改数据 | backend |
| `scripts/backend-test.py` | CREATE | Go 定向选择、Expected Test Manifest 核验、真实退出码/日志证据、`-- [filter]`、`--list`/`--check-manifest`/`--json`；不调用 Cargo | backend |
| `scripts/backend-test-manifest.json` | CREATE | 选择→包/`-run`/期望用例/故事映射清单（父目录 `scripts` 已存在；与执行脚本同目录，命名沿用 `verify-panel-image.sh` 的 `名词-动词` 风格） | backend |
| `scripts/verify-panel-image.sh` | MODIFY | 双托管模式断言扩展到登录/刷新/未知 API；版本一致性 | backend |
| `scripts/run-remote-tests.sh` | MODIFY | 远端 Docker 门禁串行化与清理核对 | backend |
| `tests/e2e/upgrade_restore_test.go` | CREATE | 隔离库升级/预检阻断/恢复核对（需 Docker 远端） | backend |
| `tests/e2e/authorization_window_test.go` | CREATE | 在线/离线撤权 60 秒窗口与恢复（需真实核心） | backend |
| `tests/e2e/usage_late_report_test.go` | CREATE | 真实流量跨周期/迟报/重发（需真实核心） | backend |
| `tests/e2e/browser_test.go` | MODIFY | 浏览器入口断言跟进真实分页与新状态 | backend |
| `tests/e2e/docker_lifecycle_test.go` | MODIFY | 停机确认与清理断言 | backend |
| `deploy/manage-linux.sh` | MODIFY | 升级前 `panel check` 门禁、备份与回退说明 | backend |
| `deploy/nginx.conf.example` | MODIFY | 两种托管模式的 `/api`、`/sub`、SPA 深链与 `X-Request-Id` 透传 | backend |
| `.github/workflows/images.yml` | MODIFY | 发布前迁移/预检门禁、前端与后端版本一致性 | backend |
| `docs/design/implementation-contract.md` | MODIFY | 同步列表游标、新字段、`me/usage` 形状与新错误码 | backend |
| `docs/reference/api.md` | MODIFY | 端点目录与示例（不含秘密） | backend |
| `docs/deployment/production.md` | MODIFY | 备份/预检/升级/恢复/两种托管步骤 | backend |
| `docs/deployment/docker.md` | MODIFY | 镜像与升级顺序；技术回退与数据库恢复分别核对，不混同 | backend |
| `docs/development/testing.md` | MODIFY | 新增 `scripts/backend-test.py` 入口、Go 定向与全量门禁、uv 前置 | backend |
| `README.md` | MODIFY | 快速开始与验证入口同步（不写“已验证”） | backend |
| `deploy/deploy-agent.sh` | MODIFY | 复用独立安装器的可执行路径、服务单元、状态目录与权限修复 | backend |
| `go.mod` | MODIFY | 安全依赖版本与锁定，统一候选树重新扫描/回归 | backend |
| `go.sum` | MODIFY | 安全依赖版本与锁定，统一候选树重新扫描/回归 | backend |
| `internal/panel/api/static.go` | MODIFY | 复用静态目录/符号链接/特殊文件护栏，保持SPA与API边界 | backend |
| `internal/panel/api/static_boundary_test.go` | CREATE | 复用静态目录/符号链接/特殊文件护栏，保持SPA与API边界 | backend |
| `internal/panel/api/static_open_other.go` | CREATE | 复用静态目录/符号链接/特殊文件护栏，保持SPA与API边界 | backend |
| `internal/panel/api/static_open_unix.go` | CREATE | 复用静态目录/符号链接/特殊文件护栏，保持SPA与API边界 | backend |
| `tests/e2e/deploy_agent_test.go` | CREATE | 复核并复用 deploy 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/api/oracle_regression_test.go` | CREATE | 复核并复用 store 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/api/store_binding_regression_test.go` | CREATE | 复核并复用 store 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/maintenance/maintenance_integration_test.go` | MODIFY | 复核并复用 store 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/store/authoritative_regression_test.go` | MODIFY | 复核并复用 store 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/store/batch_node_group_unit_test.go` | MODIFY | 复核并复用 store 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/store/migration_upgrade_test.go` | CREATE | 复核并复用 store 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/store/oracle_regression_test.go` | CREATE | 复核并复用 store 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/store/period_advance_test.go` | MODIFY | 复核并复用 store 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/usagecalc/usagecalc.go` | MODIFY | 完整区间历史归属、跨周期和迟报守恒，保留跨来源拒绝 | backend |
| `internal/panel/api/auth_security.go` | CREATE | 登录来源、廉价拒绝与有界公平限流，保留合法认证策略 | backend |
| `internal/panel/api/rotation_scope_test.go` | MODIFY | 复核并复用 security 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/limit/limit.go` | MODIFY | 登录来源、廉价拒绝与有界公平限流，保留合法认证策略 | backend |
| `internal/panel/limit/limit_test.go` | MODIFY | 复核并复用 security 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/limit/security_test.go` | CREATE | 复核并复用 security 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/store/security.go` | CREATE | 邀请/会话/归属逻辑复用；恢复门禁仅保留write_guard.go的一份实现 | backend |
| `internal/panel/store/security_concurrency_test.go` | CREATE | 复核并复用 security 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/store/security_invitation_test.go` | CREATE | 复核并复用 security 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `docs/deployment/split-panel-agent.md` | MODIFY | 按最终实现事实同步项目说明，不冒称已完成运行验收 | backend |
| `internal/panel/clientconfig/clients.go` | MODIFY | 真实配置与客户端投影、TLS/REALITY归一化和兼容性，禁止静默丢参数 | backend |
| `internal/panel/clientconfig/realcore_test.go` | MODIFY | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/clientconfig/reality_alpn_test.go` | MODIFY | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/clientconfig/review_regression_test.go` | CREATE | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/clientconfig/tls_handshake_test.go` | CREATE | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/protocol/endpoint.go` | MODIFY | 真实配置与客户端投影、TLS/REALITY归一化和兼容性，禁止静默丢参数 | backend |
| `internal/panel/protocol/tls.go` | CREATE | 真实配置与客户端投影、TLS/REALITY归一化和兼容性，禁止静默丢参数 | backend |
| `internal/panel/protocol/tls_cert_test.go` | MODIFY | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/protocol/tls_review_test.go` | CREATE | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/panel/xrayconf/xrayconf.go` | MODIFY | 真实配置与客户端投影、TLS/REALITY归一化和兼容性，禁止静默丢参数 | backend |
| `internal/agent/state/commit.go` | CREATE | 复用持久化提交、故障状态与进程身份实现；按本设计补齐收紧/恢复/计量边界 | backend |
| `internal/agent/state/review_regression_test.go` | CREATE | 复核并复用 agent 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/agent/state/store_test.go` | MODIFY | 复核并复用 agent 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/agent/supervisor/health.go` | CREATE | 复用持久化提交、故障状态与进程身份实现；按本设计补齐收紧/恢复/计量边界 | backend |
| `internal/agent/supervisor/review_gate_test.go` | CREATE | 复核并复用 agent 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/agent/supervisor/review_regression_test.go` | CREATE | 复核并复用 agent 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/agent/supervisor/supervisor_test.go` | MODIFY | 复核并复用 agent 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/agent/xray/health.go` | CREATE | 复用持久化提交、故障状态与进程身份实现；按本设计补齐收紧/恢复/计量边界 | backend |
| `internal/agent/xray/procidentity_linux.go` | MODIFY | 复用持久化提交、故障状态与进程身份实现；按本设计补齐收紧/恢复/计量边界 | backend |
| `internal/agent/xray/procidentity_windows.go` | MODIFY | 复用持久化提交、故障状态与进程身份实现；按本设计补齐收紧/恢复/计量边界 | backend |
| `internal/agent/xray/regression_p0_test.go` | MODIFY | 复核并复用 agent 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `internal/agent/xray/review_regression_test.go` | CREATE | 复核并复用 agent 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | backend |
| `cmd/agent/main.go` | MODIFY | 增加只读inspect-state分支，早于注册/核心初始化，不启动进程 | backend |
| `internal/agent/state/upgrade_evidence.go` | CREATE | 读取一致状态备份并导出非秘密队列/身份/版本证据，拒绝未合并日志 | backend |
| `internal/agent/state/upgrade_evidence_test.go` | CREATE | 导出不改输入/无秘密、WAL与未知结构拒绝、队列非空不能报空 | backend |
| `cmd/agent/inspect_state_test.go` | CREATE | CLI不启动服务/不访问网络、stdout与退出码、错误输入 | backend |
| `internal/panel/store/upgrade_evidence.go` | CREATE | 单一材料加载/路径和摘要校验/DB身份绑定/事务登记实现，供预检与迁移复用 | backend |
| `internal/panel/store/upgrade_evidence_test.go` | CREATE | 材料缺失/越界/摘要变化/错身份/只读与导入失败回滚/原账本守恒 | backend |
| `web/src/router/index.ts` | MODIFY | 新增 `/admin/setup`、`/admin/runtime`、`/me/connect`、`/me` redirect；兼容 redirect（`/admin/invite`、`/me/credential`、`/admin/usage-detail`）；标题改"工作台/服务方案/变更记录"；meta 增加 `navSection` | frontend |
| `web/src/components/AppShell.vue` | MODIFY | 导航五分组重排 + `nav-*` testid；挂载 `RecoveryModeBanner` | frontend |
| `web/src/components/StatusBadge.vue` | MODIFY | 新增状态词（待同步/部分完成/回滚运行/结果未确认/未知/未接入/配置未应用） | frontend |
| `web/src/components/DataTable.vue` | MODIFY | 分页控件、三态 testid、行 ID 透传 | frontend |
| `web/src/components/ImpactPreviewDialog.vue` | MODIFY | 幂等键生命周期、"结果未确认"、"部分完成"、回结果记录链接 | frontend |
| `web/src/components/CopyField.vue` | MODIFY | 复制失败反馈与手动选中兜底 | frontend |
| `web/src/components/ConfirmDialog.vue` | MODIFY | 支持影响列表 + 二次勾选（换钥/全量重置） | frontend |
| `web/src/components/SetupChecklist.vue` | CREATE | 六步设置进度（父目录 `web/src/components/` 已存在） | frontend |
| `web/src/components/ServiceStatusCard.vue` | CREATE | 成员/管理员共用状态摘要 | frontend |
| `web/src/components/QuotaMeter.vue` | CREATE | 额度三态（数值/不限量/未知） | frontend |
| `web/src/components/ClientImportGuide.vue` | CREATE | 按客户端导入 + 诚实核对结论 | frontend |
| `web/src/components/RecoveryModeBanner.vue` | CREATE | 全站恢复核对持久横幅 | frontend |
| `web/src/components/UpgradeReadinessPanel.vue` | CREATE | 升级预检三态与检查清单 | frontend |
| `web/src/api/endpoints.ts` | MODIFY | 见 §9.3（含 `meApi.usage` 修正、新查询参数、`upgradeReadiness`；模板删除沿用现有内联调用，不新增封装） | frontend |
| `web/src/api/types.ts` | MODIFY | 新 DTO 镜像与派生字段（snake_case 一致） | frontend |
| `web/src/lib/meUsage.ts` | CREATE | `items={usage,adjustments}` 解包 | frontend |
| `web/src/lib/clientImport.ts` | CREATE | 客户端↔格式映射、可用性判定、导入步骤 | frontend |
| `web/src/lib/readiness.ts` | CREATE | 预检状态/检查码/阻断码 → 用户语言 | frontend |
| `web/src/lib/selectionScope.ts` | CREATE | 选择范围快照与 digest | frontend |
| `web/src/lib/recoveryMode.ts` | CREATE | 跨页共享恢复态（模块级单例资源，非 UI store） | frontend |
| `web/src/lib/errors.ts` | MODIFY | 新错误码 hint（`RECOVERY_MODE`/`READINESS_BLOCKED`/`LEDGER_UNATTRIBUTABLE`/`CURSOR_INVALID`/`SUBSCRIPTION_NOT_BINDABLE`） | frontend |
| `web/src/lib/accessReasons.ts` | MODIFY | 补无方案/暂停/到期/超额/节点禁用/数据未知的用户语言 | frontend |
| `web/src/lib/nodeDraft.ts` | MODIFY | REALITY 脱敏编辑、换钥成对校验、保留键摘要、多证书保留 | frontend |
| `web/src/lib/subscriptionDraft.ts` | MODIFY | `pending_period_rule` 只读视图模型与"成员将获得什么"摘要输入 | frontend |
| `web/src/lib/batch.ts` | MODIFY | `reason` 字段、幂等键生命周期、`idempotent_replay` 判定 | frontend |
| `web/src/lib/paged.ts` | MODIFY | `invalidate()`（失效即回第一页并通知调用方清空选择） | frontend |
| `web/src/lib/toast.ts` | MODIFY | 移除自证式长文案；明确"toast 仅辅助" | frontend |
| `web/src/views/admin/OverviewView.vue` | MODIFY | 工作台：待办 + 设置进度 + 交付统计（替换自证说明） | frontend |
| `web/src/views/admin/SetupView.vue` | CREATE | 首次设置路径（`/admin/setup`） | frontend |
| `web/src/views/admin/RuntimeView.vue` | CREATE | 运行状态/恢复核对/升级与恢复 三分段 | frontend |
| `web/src/views/admin/ServersView.vue` | MODIFY | 接入流程、注册码一次性提示、未接入/离线/未知三态 | frontend |
| `web/src/views/admin/ServerRuntimeView.vue` | MODIFY | 四类事实分区、应用修订落后、回 `/admin/runtime` | frontend |
| `web/src/views/admin/NodesView.vue` | MODIFY | 服务端搜索/服务器过滤/真实分页 | frontend |
| `web/src/views/admin/NodeEditorView.vue` | MODIFY | 结构化/原生双视图无损、REALITY 脱敏与换钥、同步状态面板 | frontend |
| `web/src/views/admin/NodeGroupsView.vue` | MODIFY | 影响预览文案（断线/下周期）与错误恢复 | frontend |
| `web/src/views/admin/SubscriptionsView.vue` | MODIFY | "服务方案"术语、退役语义与 `retired` 展示 | frontend |
| `web/src/views/admin/SubscriptionEditorView.vue` | MODIFY | 摘要句、`pending_period_rule`、影响预览强制 | frontend |
| `web/src/views/admin/MembersView.vue` | MODIFY | 搜索/筛选/跨页选择范围、批量入口与范围摘要 | frontend |
| `web/src/views/admin/MemberDetailView.vue` | MODIFY | 状态原因、`data_complete`、两个重置动作区分、额度调整 | frontend |
| `web/src/views/admin/InvitationsView.vue` | MODIFY | 是否分配方案、一次性码提示、撤销与失效状态 | frontend |
| `web/src/views/admin/TemplatesView.vue` | MODIFY | 修正删除提示（引用阻止 409 `template_in_use` 与引用数展示）；不再声称“引用方需自行修正” | frontend |
| `web/src/views/admin/BatchListView.vue` | MODIFY | "变更记录"术语、状态列、结果入口 | frontend |
| `web/src/views/admin/BatchDetailView.vue` | MODIFY | 部分完成三类计数、幂等回放标注、轮询终态 | frontend |
| `web/src/views/admin/UsageView.vue` | MODIFY | 游标分页、去 `nic_counters`、数据完整性提示 | frontend |
| `web/src/views/admin/EventsView.vue` | MODIFY | 待处理优先、确认后持续可见、技术详情折叠 | frontend |
| `web/src/views/admin/TasksView.vue` | MODIFY | 重试=重新排队、旧回执标注、按批次/Agent 过滤 | frontend |
| `web/src/views/admin/AuditView.vue` | MODIFY | 术语统一（同步任务/变更记录） | frontend |
| `web/src/views/admin/SettingsView.vue` | MODIFY | 恢复核对区改为状态摘要 + 链接到 `/admin/runtime`；token 只显"已设置" | frontend |
| `web/src/views/auth/LoginView.vue` | MODIFY | 统一错误文案、限流等待、`next` 保留 | frontend |
| `web/src/views/auth/RedeemView.vue` | MODIFY | 邀请确认语、无方案"待开通"结果、失效邀请统一提示 | frontend |
| `web/src/views/me/MeSubscriptionView.vue` | MODIFY | 重构为成员首页（状态 + 额度 + 节点 + 导入 + 用量摘要），链接/二维码下沉 | frontend |
| `web/src/views/me/MeConnectView.vue` | CREATE | 订阅链接/二维码/格式可用性/重置订阅链接 | frontend |
| `web/src/views/me/MeUsageView.vue` | MODIFY | 对象解包修正、不限量/未知/零三态、调整记录仅首页给全 | frontend |
| `web/src/views/me/MeStatusView.vue` | MODIFY | 原因卡、数据完整性、诊断详情折叠（内部码不铺首页） | frontend |
| `web/src/views/me/MeCredentialsView.vue` | MODIFY | 重置订阅链接 / 重置连接凭据分开 + 影响说明 | frontend |
| `web/src/views/me/MeSecurityView.vue` | MODIFY | 改密就地反馈（错误不踢会话）、会话撤销 | frontend |
| `web/src/lib/meUsage.spec.ts` | CREATE | 对象解包边界 | frontend |
| `web/src/lib/clientImport.spec.ts` | CREATE | 格式不可用/无节点不产链接 | frontend |
| `web/src/lib/selectionScope.spec.ts` | CREATE | 范围变化/跨页/失效清空 | frontend |
| `web/src/lib/readiness.spec.ts` | CREATE | 三态互斥与阻断码文案 | frontend |
| `web/src/lib/errors.spec.ts` | CREATE | 新码 hint 存在且不泄露 | frontend |
| `web/src/lib/nodeDraft.spec.ts` | MODIFY | 无损保存/换钥成对/多证书保留 | frontend |
| `web/src/views/admin/BatchListView.spec.ts` | MODIFY | 幂等回放与部分完成文案 | frontend |
| `web/src/views/admin/MembersView.spec.ts` | MODIFY | 选择范围摘要与筛选变化后禁用提交 | frontend |
| `web/tests/product-readiness-e2e.mjs` | CREATE | §8.3 主路径真实浏览器脚本（复用 `web/tests` 布局与 `PANEL_*` 环境变量） | web-demo |
| `web/tests/e2e-ui-full.mjs` | MODIFY | 路由清单加新页、选择器改 testid 优先 | web-demo |
| `web/tests/ui-integration.mjs` | MODIFY | 页面清单与新增提交用例 | web-demo |
| `web/tests/platform-browser.mjs` | MODIFY | 成员首页导入、恢复横幅、390px 主操作 | web-demo |
| `web/README.md` | MODIFY | 按最终实现事实同步项目说明，不冒称已完成运行验收 | frontend |
| `web/src/api/http.ts` | MODIFY | 复用前轮已复核的HTTP/配置/反馈修复，与本轮任务体验一致 | frontend |
| `web/src/api/reviewRegression.spec.ts` | CREATE | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | frontend |
| `web/src/components/AppShell.spec.ts` | MODIFY | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | frontend |
| `web/src/components/ErrorState.vue` | MODIFY | 复用前轮已复核的HTTP/配置/反馈修复，与本轮任务体验一致 | frontend |
| `web/src/components/ImpactPreviewDialog.spec.ts` | MODIFY | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | frontend |
| `web/src/components/LoadingBlock.vue` | MODIFY | 复用前轮已复核的HTTP/配置/反馈修复，与本轮任务体验一致 | frontend |
| `web/src/lib/nodeDraft.review.spec.ts` | CREATE | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | frontend |
| `web/src/lib/nodeTemplates.review.spec.ts` | CREATE | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | frontend |
| `web/src/lib/nodeTemplates.ts` | MODIFY | 复用前轮已复核的HTTP/配置/反馈修复，与本轮任务体验一致 | frontend |
| `web/src/views/admin/NodeEditorView.review.spec.ts` | CREATE | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | frontend |
| `web/tests/browserAssertions.mjs` | CREATE | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | frontend |
| `web/tests/dump-ui.mjs` | MODIFY | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | frontend |
| `web/tests/exitCode.mjs` | CREATE | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | frontend |
| `web/tests/fixture-browser-check.mjs` | CREATE | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | frontend |
| `web/tests/fixture-exitcode-check.mjs` | CREATE | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | frontend |
| `web/tests/inspect-ui.mjs` | MODIFY | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | frontend |
| `web/tests/spaFixture.mjs` | CREATE | 复核并复用 frontend 候选回归/测试工具；增量价值、修前失败和真实语义须核对 | frontend |
| `web/tests/host-nginx-smoke.mjs` | MODIFY | 两种托管的真实HTTPS/浏览器深链与会话，不把旧烟测结果当新版本证据 | web-demo |

## 9. 外部参考
无新增外部搜索。沿用PRD/技术预研中用户已准备的公开产品资料；本方案以当前仓库事实为准，不拿其他项目模板覆盖Go/Vue。

## 10. 下一步
完成独立无上下文读者核查、决策闭合与check-design结构检查后才将生成状态写complete。随后进入可恢复实施；本次设计不声称功能或真实测试已完成。


## D2 补充：readiness 校验和未知表示

| Decision ID | Applied / Not Applicable / Superseded | Artifact Location | Notes |
|---|---|---|---|
| DEC-product-readiness-013 | Applied | backend §4.2；API/CLI/消费者镜像；阶段验收 | checksum_ok 为 boolean/null：true已核验成功、false已知不符、null无法核验；不改变status/checks/blockers门禁，不伪造事实。 |
