# CP01–04 专项审阅：增量结果

范围3435f1d..fe4bf8e。不是完整t-review，也不是正式阶段accept；仅记录已取得的独立结果，未完项目不能当干净结论。

## CPREV-003 — CONFIRMED

- Finder：`01a16910-3fa8-75a9-8640-accb70c87227`。
- 独立verifier：`01a1691b-5ccd-75a9-8640-acd4b5c27f23`，结果CONFIRMED。
- 位置：`tests/e2e/deploy_agent_test.go:222`。
- 触发：bash stdin携带--help，安装器顶层108–120行usage/exit0；测试只看成功与“用法”，不执行末尾652行BASH_SOURCE guard。
- 后果：新增回归不能检测其声称防护的set-u未绑定变量入口问题；不是当前生产guard仍有错误。
- 修复由主会话临时切backend-test只改该测试资产，保留真实guard，仅stub安装main调用；不用--help，并加入去掉fallback时必须出现明确BASH_SOURCE拒绝的对照。不得运行真实安装。
- Fix status：fixed。实际guard保持原样，仅stub末尾安装dispatch，不传--help；新增去掉fallback的负对照必须exit1且包含BASH_SOURCE错误，不能误把其他命令失败算覆盖。
- 先通过统一入口--check-manifest的编译/发现，再由执行编排运行e2e/standalone-agent；7个预期顶层用例通过，包含两个入口正/负子例，无实际特权安装。
- 证据：`.ai/evidence/backend-test/20261023T141957Z-9b9a22f0/result.json`。修复仅测试资产，没有改变安装器业务；原独立CONFIRMED结论保留。

## CPREV-002 — CONFIRMED

- 独立verifier：`01a1691b-5cd5-75a9-8640-acd724e35bc8`，结果CONFIRMED。
- 位置：`scripts/backend-test.py:91`。
- 安装器测试实际读取`.service`，但指纹后缀白名单不含它；该文件在执行后改变时，稳定性比较仍可为true且未保存其内容指纹。缺陷是验证证据完整性，不意味着修后安装器仍采用随附单元。
- Fix status：pending，先用独立临时Git仓库和真实fingerprint函数复现，再补范围并验证。当前运行已结束，不在执行中改runner。

## 其余候选

CPREV-001（重叠选择计数）尚待独立结果，不计入确认数。其他专项和机械盘点也不作为验收结论。
