# Backend Test Run Blocked: Test Semantics Need Decision

## Failure

- Command: `uv run scripts/backend-test.py --json -- store/database-baseline`
- Test: `TestSubscriptionRuleEditSchedulesImmediatelyForNextBoundary`
- Report: `.ai/evidence/backend-test/20261023T133529Z-a6275451/result.json`
- 51个预期顶层用例全部选中，0遗漏/跳过，1失败。实际新区间2026-11-01UTC至2026-11-14T15:00Z；旧测试用生产`period.Containing(15, Tokyo, oldEnd)`生成期望，错误地把新起点设为2026-10-14T15:00Z。

## Evidence

- `docs/design/product.md`§8.3：从旧end衔接，新end为新规则下一个严格边界。
- `.ai/prd/core/product-readiness.md`§4.4：当前周期与下一周期规则分开，不无声重叠账期。
- `.ai/design/product-readiness/backend.md`§6.4同一不变量；CP03已先红后绿验证该算法。
- 该场景已验证当前周期/已用量保留、pending立即登记、首次旧边界消费、旧周期100字节守恒与同边界重复tick=0。失败仅在自证式期望生成，不应删掉其他断言。

## Diagnosis

停止runner自动修语义。该错误与只读连接构造无关，是旧测试期望与既有明确规则冲突；生产结果符合既定衔接规则，不回退生产算法。

## Orchestration Decision

主会话编排者依据已确认规则，交回backend-test角色校准这一现有测试资产；不请求重新选择产品规则，也不授权该角色改生产代码。仅将期望改为输入oldEnd起点及独立UTC算术的Tokyo15日终点，不调用生产周期函数生成oracle，保留全部输入和其他断言。authoring只做编译；随后切回执行编排用统一入口复测。

本动作是当前dev失败依赖的局部校准，不越过尚未完成dev去宣称整体test任务已开始或完成；test/accept状态仍pending，后续正式场景资产和验收范围不削减。
