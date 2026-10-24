# 闭环 B 实现交接（基线 7c5aa67）

## 依赖与禁止独立发布

- 依赖 `3fead75` 原版 schema/helper（本分支 cherry-pick 为 `1250b60`）；未修改 0005。
- A 必须完成任何 DDL 前的预检，以及已验证材料与 0005 的同事务导入。当前分支的 schema-only 迁移不是可独立发布的升级路径。
- 历史缺失不从 Node.CreatedAt、当前归属或当前周期规则补猜；没有给旧 ledger 增加过滤标记，也没有更新/删除任何旧 usage_entries。

## 已实现接口/决策

- `refreshMemberPeriod(ctx, tx, member)` 是初始周期/绑定的唯一规则入口：初始化时登记 binding；已有周期保留旧区间与旧规则，只安排 pending 规则；到边界从旧 end 逐段登记。
- `advanceMemberPeriodTx` 登记旧的明确运行区间及每一个跳过区间；包括短过渡周期；当前周期采用实际登记的最后一段。`recordRunningPeriodTx` 只是不可变记录校验，不是另一套初始化规则。
- `recordedWindowsTx` 只取完整覆盖样本的真实半开区间，不调用 CandidateWindows。任意缺口/矛盾使整批 422 LEDGER_UNATTRIBUTABLE、回滚、不 ACK，并尝试记录独立告警事件。事件写入失败不会把拒绝变为成功。
- batch 级事务 advisory lock 保证同 Agent/batch 并发幂等；成员锁按 ID 排序；每个实际 ledger 切片具有批内递增 split_part，重复时间范围的两个样本也不会被 ON CONFLICT 静默吞字节。
- 额度方向改变时，在同事务按完整 access 决策撤销/生成真实加密凭据并发布 revision；审计调整前先推进周期。原 ledger 和旧额度归属不变。
- `node_server_history` 按实际 Agent 身份建窗口；创建、注册、移机接线。移机只填 released_revision，不关闭 valid_to；新 Agent 窗口同时打开。
- `nodeAuthorizedForRangeTx` 按同 Agent/server 的窗口并集完整覆盖；无该身份授权历史为 403，已知窗口存在缺口为 422。
- `validateExecutionResultsTx` 是本分支的 SEC02 统一实现：普通结果仍检查当前 core server；terminal-only 必须 stopped + OldConnectionsTerminated + 精确 released_revision，且不能是未来 revision。只关闭匹配旧窗口，跳过当前 core/实例摘要更新；terminal-only 不推进 applied 水位；旧普通回执不得借 A→B→A 的当前 metadata 写新实例。
- `lockExecutionHistoryTx` 是执行历史专用 advisory 锁，不是恢复门禁。创建/注册/移机/心跳/回执在行锁之前取得，避免身份登记与移机可见性竞态及反向 Agent/core 锁序。低频管理写与回执目前串行；大 Agent 规模吞吐未压测。

## 主/C 必须接的点（本分支没有 write_guard，未复制实现）

1. C/主将 `requireWritableTx`/`inWritableTx` 接到 B 的授权写事务入口，门禁在任何行锁、execution-history advisory 锁之前：周期推进/refresh 的外部调用者、SubmitUsageBatch、CreateQuotaAdjustment、CreateNode/UpdateNode、RegisterAgent，以及恢复期禁止的其他 resources/ops 写入口。只读授权路径不得在恢复模式隐式推进/重建凭据。
2. **基线 members.go 没有调用 refreshMemberPeriod。** C 需要在 CreateMember、邀请初始化及绑定事务中调用此唯一入口；不得只填 members 的 period 字段而漏掉登记。B 遵守所有权没有改 members.go。测试用 bRefresh 模拟调用者接线，不能把这一点算作已集成。
3. 基线 batch_submit.go 的直接绑定路径亦有自己的周期字段写入；主若保留此路径，应改为同事务调用 refreshMemberPeriod（由主协调该非 B 所有权文件）。未经接线的直接写周期路径不算已闭环。
4. 合并时保留本分支两段式 SEC02 检查，不再叠加“仅当前 server”的旧 validateAgentInstancesTx，否则移机 terminal-only 会再次被 403。
5. A 的预检负责证明存量显式运行区间。运行期对已持久化 start/end 的登记不是升级材料验证的替代。

## 实际测试（没有全仓运行）

统一使用 uv Python Go runner；仅 source 主指定 `/home/compile/Work/.devdb-product-readiness/test.env`，未打印凭据。真实 PG 测试每例随机自有 ppreg_ schema，清理仅自身 schema，没有 DROP DATABASE/真实用户数据操作。

最终定向选择：

- store/closed-loop-b：5 个顶层测试（4 个新增 B 测试 + 原 AdvancePeriodsTick），全部执行、零 skip。
- store/cycle：14 个测试/子测试，全部执行、零 skip。
- store/short-cycle-live：原 7c5 短周期实时额度回归通过。
- store/period-history-candidate：20 个测试/子测试，通过、零 skip。
- period/calendar：30 个测试/子测试，通过、零 skip，保留跨月回拨修复。

新增覆盖：初始 binding 与真实注册身份登记、不猜早于注册的历史、多段跳跃/短周期规则切换、跨周期迟报及 correction、字节守恒、重复时间样本、同批并发 accepted/duplicate、不同 payload 409、整批缺口 422 无 ACK、额度调整恢复与周期恢复真实凭据、移机晚到、部分失败终止证明、A→B→A 旧 terminal/普通回执隔离、SEC02 普通越权 403、连续授权并集/真实间隙。

未验证：整合 C/A 后的恢复门禁/启动迁移路径、全仓回归、真实 Agent 核心停机/流量、规模吞吐；不能据当前定向结果宣称这些已通过。
