# backend CP03：规则切换后的连续周期

状态：纯推进计算checkpoint通过；不是周期历史/账本/迁移验收。

## 规则与旧oracle纠正

既有`docs/design/product.md`§8.3与本轮PRD/设计要求：新周期从旧`end`开始，第一段结束由新规则下一个严格边界确定。不能直接拿新规则Containing(now)作为第一段起点。

旧测试`TestComputeAdvanceAppliesTimezoneEditAtFirstBoundaryExactly`的输入旧周期截至2026-04-01UTC，但原期望新起点为2026-03-14T15:00Z，实际与旧周期重叠。修前先将起点期望纠正为固定字面量2026-04-01UTC，保留输入及其他断言；新增五个锚日/短月/时区/精确边界子例，明确不重叠、包含now与不修改输入。这是有独立规则依据的oracle修正，不是删用例或放宽预期。

## 实现

- 共享`period.Transition`保留第一段oldEnd，越过第一段后直接按新规则定位当前周期，不用任意月数上限静默截断。
- `store.computeAdvance`调用共享函数，不加第二个同义store包装。现有读取缺周期不擅自新建、当前周期不改、错误时区拒绝的测试保留。
- 详细设计的内部函数草图同步实际共享落点，HTTP契约不变；派生文档指纹更新，设计来源门禁通过。

## 证据

- 修前：`.ai/evidence/backend-test/20261023T130722Z-a5656c26/result.json`，纠正oracle后的原用例与新增不重叠用例真实失败。
- 修后：`.ai/evidence/backend-test/20261023T131056Z-ab0de766/result.json`。
- 入口：`uv run scripts/backend-test.py --json -- store/cycle period/calendar api/static e2e/standalone-agent`。
- 39个当前注册顶层用例通过，0遗漏、0跳过；修前/修后store测试、manifest与runner内容相同。
- 全仓Go构建和diff检查通过；原始构建日志`.ai/evidence/backend-test/cycle-native-build.log`。

## 未完成

尚未登记跳过的历史周期或修正`refreshMemberPeriod`/历史用量路径，未执行数据库/旧队列/升级或实际数据面。这些仍由后续同phase实现与真实集成测试关闭；当前分支不部署为成品。
