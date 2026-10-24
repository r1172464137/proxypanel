# A+B+C 实际整合交接

## 隔离与提交

- Worktree: `pi-web-agent-d0fc1ca5-acd7-4419-94e8-294bfd549efe`，基线已有 `8839858` + `8e5a305`，未重复采用。
- A 链采用：`a9921ab → c803e41`、`b3b8a6c → 56d0fb3`、`b4a2d75 → c7849ec`。
- B 业务：`27fe9cd → 45cc141`。`3fead75` 与 A 链的 schema/helper 等价，未重复 cherry-pick。
- manifest 按 selection ID 合数组，保留 C identity/limit 条目；未整文件取任一侧。
- 未修改主工作区，未 push、部署或派代理；未改 web/protocol/Agent 或 0001–0004。D 仅读取提交与 handoff，未带入 Agent 代码。

## 已闭合业务

- `CreateMember`、邀请和 `UpdateMember` 绑定、`batch_submit.applySubscriptionBinding` 均由唯一 `refreshMemberPeriod` 登记周期；邀请原本已调用，予以保留。解绑先保存明确旧区间再清空当前周期，历史不删除。
- PATCH HTTP 层不再另算 containing/pending 周期。绑定更换保留明确运行区间，由 helper 安排后续规则，不重置已用额度。
- resources 普通授权事务、注册、revision/retry、quota adjustment、SubmitBatch、后台周期推进均使用 C 唯一 `inWritableTx`；门禁在对象/execution advisory 锁前。
- 必要用量不整体 requireWritable：先锁同一 control 行并读取恢复状态，再取 batch/member 锁。恢复中仅按真实已登记区间入账；不推进周期、不改额度判定、不生成凭据。无覆盖整批 422、不 ACK、不部分入账；重复批次仍按原 hash 幂等。
- 授权读路径恢复中不推进周期、不重建凭据。Agent 心跳和回执先取 control 行以保持锁序，但不拒绝必要遥测；身份救援保留 C 规则。
- B 两段 SEC02 保留，未叠加旧的仅当前 server 校验。A 任何 DDL 前预检、0005 与材料导入同事务保持原实现。

## D artifact v1 接线与明确剩余阻断

已读取 `b8085f2:internal/agent/supervisor/LIFECYCLE_HANDOFF.md`（state 依赖 `0ee3983` 在 D 链为 `f728779`）。D 仍导出 `proxypanel.agent-state-evidence/v1` / schema `1`；非空且合法的新 `apply_intent` / `applied_snapshot` keys 能真实建立 capability。

- A 不再把 schema1 + supported=true 一律判成伪造。supported=true/count>0 或 pending reports>0 为 `AGENT_EXECUTION_PENDING`，迁移明确拒绝，测试断言 0005 表未创建。
- 旧 supported=false/count=0 仍是 `AGENT_INTENT_UNPROVEN` unknown；不要求旧 Agent 提供当时不存在的 intent，也不补造 tracking。
- 新 supported=true/count=0 仅证明新实现的提交能力，**不证明停机**。目前 `stopped_evidence_file` 只经 hash/文件完整性校验，内容仍是未经独立验证的外部声明，故添加独立 `AGENT_STOP_UNVERIFIED` unknown，所有这类迁移仍被门禁拒绝。没有为整合绿强制 ready。
- 合法解除阻断尚缺一个审定的外部停止/逐项核对验证入口：须绑定 Agent/server 身份、服务禁止重启 fence、每个 core 的退出或 OS 不存在观测、停机与 immutable capture 的先后关系，以及逐实例对 PG generation/revision、归属窗口、待回执的核对。旧实现可用这些外部事实替代不存在的 intent；不能只再增加一个“已停止”布尔值或附件 hash。当前没有实现这个验证器，故不得宣称旧库可升级。
- period/owner/credential 的明确来源、缺口、冲突依旧单独核对；不从 CreatedAt 或当前归属猜历史，不自动暂停成员，不改 ledger。
- PG 已收到同 Agent/batch/hash 的本地 pending 仍计入 `pending_batches` 与 `duplicate_batches`，不要求重建该批归属；hash 不同仍 blocked。ACK 丢失回归保留这些断言。未知执行/停止事实不会被该 duplicate 快捷路径掩盖。

## 测试 fixture 调整依据

不是降低业务断言：

1. 原 regMember 可把“过去已创建成员”作为 fixture 插入；现在 CreateMember 会真实登记/推进。fixture 在给定周期设置创建时钟后恢复当前时钟，保留 AdvancePeriods 的 advanced=1 / 二次=0 断言。
2. period primitive 并发测试要从“无已登记区间”开始，改用无绑定成员，保留冲突、锁等待、幂等、相邻与不可变断言。
3. 短周期实时额度测试不再直接重写另一已登记成员的历史；另建真实 anchor15 成员，再安排 anchor31 过渡，仍断言 2000 字节精确归属、超额撤权和重放不增字节。
4. HTTP 邀请额度样本从真实 Agent 注册后开始，不再回溯注册前一秒；2048 字节、撤权、订阅403、snapshot移除断言均保留。
5. A 原 schema1 supported=true 必须 blocked 的断言由 D 已实现的 durable keys 明确推翻，替换为 capability 合法、stop 仍 unknown；另测 intent/report pending 必须 blocked、无 DDL。

## 实际验收与日志

只 source `/home/compile/Work/.devdb-product-readiness/test.env`，不打印凭据。使用 `/bin/bash` source `/home/compile/Work/env.sh` SDK、用户 bin 的 uv。PG 测试每例随机自有 schema，只清理自身 schema，无 DROP DATABASE。

最终执行（非全仓）：

```sh
uv run --no-project python scripts/backend-test.py --json \
  store/abc-integration api/abc-readiness store/upgrade-preflight cmd/upgrade-check \
  store/period-history-candidate store/closed-loop-b store/cycle store/short-cycle-live \
  store/identity-tx api/identity-http limit/identity-limit
go build ./cmd/panel
go vet ./internal/panel/store ./internal/panel/api ./cmd/panel
```

最终 11 selections 全 passed，149 个测试/子测试事件，0 skip，`inputs_stable=true`。Panel 实际二进制已编译，vet 与 diff-check 通过。新增组合测试还验证恢复期 quota/member/node 写被拒、门禁先于被占用的 execution advisory 锁、54 字节入账/duplicate/拒绝后守恒、心跳/回执可写、读与后台不推进周期、创建与 batch/更换绑定真实 history、管理员 readiness 在恢复期可读且匿名401。

私有 evidence（目录权限0700）：`/tmp/abc-integration-evidence.Qu5ZrY/`。包括 `acceptance.json`、`runner-acceptance/` 原始 discovery/test 日志、`acceptance-build.log`、`vet.log`、编译的 `panel`。早期失败记录保留，不能当最终 PASS。工作树忽略目录 `.ai/evidence/backend-test/` 同时保有 runner 原始结果；本文件是可读且随提交交接的摘要。

剩余未验：全仓、真实 Agent/core 流量与停机、规模吞吐、外部停止验证器，以及存量库完整升级放行。上述定向绿不构成部署许可。
