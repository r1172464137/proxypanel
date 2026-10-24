# backend CP02：业务时区日历边界

状态：算法checkpoint验证完成；数据库历史/升级及backend整体尚未完成。

## 实现

`period.go`按业务时区的本地年月定位Containing/PrevBoundary，而非从UTC月份向过去搜索。午夜解析遍历ZoneBounds常量偏移区间，实际午夜优先、重复取较早；不存在则取跳变后的首个有效时刻，覆盖Tehran缺失午夜与Apia整日跳过。拒绝宿主相关的Local时区名，不自动将旧资料猜为UTC。

没有直接采用候选的固定区间二分猜测，没有改写数据库或账本。

## 独立期望与证据

- 日期期望固定为UTC字面量，不用生产边界函数生成自己的期望。
- 另用Python标准库zoneinfo确认：Tehran在2021-03-21T20:30Z由23:59:59+03:30进入次日01:00+04:30；Apia在2011-12-30T10:00Z由29日23:59:59-10:00进入31日00:00+14:00。
- 修前四个新增顶层测试真实失败：`.ai/evidence/backend-test/20261023T125606Z-e47a38ed/result.json`。
- 修后报告：`.ai/evidence/backend-test/20261023T130047Z-4c22f610/result.json`。
- 入口：`uv run scripts/backend-test.py --json -- period/calendar api/static e2e/standalone-agent`。
- 周期包16个顶层用例通过；与CP01合计当前注册30个顶层用例通过，0跳过/遗漏。原有分配守恒、短月、Havana跳变与重复午夜测试也通过。
- 修前/修后测试、runner和manifest的SHA256相同；全仓Go构建及diff检查通过。

## 未完成

周期推进规则切换、历史区间持久化、迟报归属守恒、数据库升级、旧Agent队列与真实流量均未由本结果证明。不会把纯函数通过推断成这些场景完成。所有phase状态仍按整体任务门禁维护。
