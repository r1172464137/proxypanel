# 五路实现分工（共同基线7c5aa67）

用户最新要求：由多个GPT-6.1 Sol实际实现，主负责接口协调、整合、实测和最终把关；上限5。不是五路审阅。下列模块各自commit、最小定向验证，不push/不部署、不派代理。

| 路 | 目标 | 写入所有权 |
|---|---|---|
| A | 升级核对/材料/启动迁移门禁/检查CLI | store.go、readiness.go、upgrade_evidence.go、cmd/panel、api/readiness.go；不改api.go，给主注册片段；可采用62167da3、3fead75作为依赖 |
| B | 周期/执行授权历史与计量接线 | periods.go、access.go、usage.go、resources.go、ops.go及对应测试；0005仅采用3fead75原版，不自行与A竞争修改；不改members.go/store.go |
| C | 身份/会话/邀请与恢复事务门禁 | members.go、security.go、write_guard.go、limit包、api/handlers.go/admin.go/admin2.go及相应测试；不改resources.go/ops.go/store.go/api.go |
| D | Agent原子状态与执行生命周期整合 | internal/agent下state/supervisor/xray/reporting及cmd/agent；不改panel/HTTP contracts；对接已有离线导出 |
| E | Vue管理员与成员完整旅程 | web源码/组件/局部测试；先采用成员分支e013e158+493bd7f；不动后端/package-lock/真实部署 |

接口协调：
- C可提供requireWritableTx/inWritableTx，但B分支没有C时不得复制第二套实现；保留调用点，handoff给主最终统一接线。主允许少量注册与胶水，不把每域实现串回主会话。
- B在refreshMemberPeriod内登记创建/绑定区间，供现有members调用；C做原子邀请须调用此唯一helper，不另造周期规则。
- A负责任何DDL前预检与0005同事务材料导入；B schema-only候选不能独立发布。
- 材料JSON只证内容/引用自洽，不证明历史/停止。PG接受同hash但ACK丢失与本地pending要分开；不能改批次、删队列、重写既有ledger或自动暂停成员。
- 独占PG凭据仅使用指定test.env，绝不打印；仅随机自有schema，故障测试不与其他路并行修改同一实例。Agent/核心测试必须临时目录/动态loopback端口及精准PID清理。
- manifest只追加自身选择，不覆盖别人；整合由主合并数组。每日小范围验证，整候选收口再全量；未运行/缺环境不计PASS。
