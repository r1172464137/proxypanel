# 专项审阅修复收口（非完整验收）

受审基线3435f1d..fe4bf8e；候选均经独立上下文判定，未把finder自述当确认。

| ID | 独立结论 | 处置 | 实际验证 |
|---|---|---|---|
| CPREV-001 | REFUTED | 不改代码；逐selection记录不等于把重叠数宣称唯一覆盖。统计能力可选，不当正确性缺陷 | verifier 01a16920-1069-75a9-8640-acd866e5d8da |
| CPREV-002 | CONFIRMED | DSV4F提交75e3964，主核对唯一一行后cherry-pick为1f7efa8；.service进入同一指纹 | 主重新跑真实fingerprint临时Git仓库内容/删除对照与统一uv入口，报告20261024T001602Z-5c442cb7 |
| CPREV-003 | CONFIRMED | stdin测试不再用提前exit的--help；保留真实guard，仅stub安装dispatch；负对照去掉fallback必须被检出 | 7个顶层安装用例及正/负入口子例，报告20261023T141957Z-9b9a22f0，后又由上述主入口复验 |
| CPREV-004 | CONFIRMED | 实时样本完整落在持久化当前周期时，按其真实起止入账，不倒推新锚日；不改历史账本 | PG修前2000字节/quota1000→used0/overfalse；修后计2000/overtrue/访问拒绝/重复不增加。定向20261024T002422Z-1358659c；store52根回归20261024T002424Z-bf1f91c9，0skip |
| CPREV-005 | CONFIRMED | 本地月份上下文包含相邻未来月，再按绝对时刻筛选，保留回拨前较早午夜 | 修前20261024T001329Z-60083e27；修后周期17根+安装7根，20261024T001602Z-5c442cb7，0skip |

所有路径相对`.ai/evidence/backend-test/`。当前样本路径修复不代表历史区间持久化/跨周期迟报/升级迁移已经完成。用户界面、协议握手和Agent原子提交在隔离分支推进，尚未合入或完整验收。
