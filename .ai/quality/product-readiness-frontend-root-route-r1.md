# frontend R1：匿名根入口按角色落点（增量验证通过，待独立验收）

## 来源与实际变化

部署后真实浏览器观察匿名访问`/`经登录后admin落`/me/subscription`，不能将“我的服务”冒称工作台。设计frontend §4.1/§8.3 US-PR-001要求管理员默认工作台。DS A的e41fce0包含修复；主只消费2个router文件至f38aff4，未合并其尚有红项的整套web-demo资产。

- `web/src/router/index.ts`：匿名根入口重定向`/login`不预填成员`next`；已有用户仍走`homeRoute`。显式私有deep link的`next`与原权限守卫不变。
- `web/src/router/index.spec.ts`：增加匿名根入口后admin/member两条默认落点回归，原拒绝/显式目的地/权限范围断言保留。
- 未改变API、session store、密码/令牌、依赖/锁文件、后端或部署，不触用户8445/旧8443数据。

## 验证范围

生产前端输入改变，因此frontend test/accept重新打开、validation_revision=1；当前请求phase仍web-demo，继续其独立闭环。旧R0 300单测/17probe与报告保留，未变化行为的相关证据可复用，但不拿R0直接宣称新入口已验。

本次快速302前端测试（旧300+新2）+typecheck+生产build；这些无真实core/PG进程，无需运行租约。依赖审计输入未变，复用R0 audit0，不重新全后端542或物理故障。

真实浏览器根路径将由A有界新fixture与最终US001共同核对；A旧e41fce0的US001 PASS若经source/资产指纹及原始结果复核可作为相同root行为的增量证据，不将其整体14PASS/7红报告当全阶段通过。未验证项保持显式，后续独立frontend-accept只读R1核对。

## 承接

- 主阶段计划：`.ai/super-run/product-readiness/web-demo.md`。
- frontend候选及R0证据：`.ai/quality/product-readiness-frontend-candidate.md`。
- F生命周期主验：`.ai/evidence/web-demo/F/main-dd40fac-retry-20261024T093138Z-a8941e/main-verification.json`，与本次router变化无关，适用性按Go/fixture/binary输入判断，不重复物理运行。
- 实际执行：monitor63，source`00ed7da3906a7f0aac6eb1b2400251524affbfa9`（生产frontend与f38aff4一致），`.ai/evidence/frontend-phase/root-route-r1/20261024T094736Z-4b66c0c1/`。302/302 pass、0failed/skip/todo、2新增root role case实际passed；typecheck/build退出均0，全部frontend输入前后SHA一致，dist逐文件SHA已记录。没有重跑后端/物理服务。
- 独立frontend R1验收尚待返回；真实完整18故事仍由web-demo承接，不把302 Vitest当整条业务验收或把A原14/7 checkpoint当全绿。
