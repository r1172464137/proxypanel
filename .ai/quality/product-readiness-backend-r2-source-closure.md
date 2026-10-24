# backend R2：仅关闭 R1-C1 规范性来源不一致

## 裁决

主会话作为设计/API责任人按 Decision Continuity Contract 的 `agent_decision` 作 D2 裁决并记录 **DEC-product-readiness-013**：新增、未发布的 readiness 诊断字段 `schema.checksum_ok` 采用 `boolean|null`，分别表达已核验成功、已知不符、无法核验。

这是对设计 boolean 草图未覆盖“无法核验”状态的技术表示细化，不是伪造用户确认：Decided By 明确为 agent。不改变任何业务规则、权限、安全门禁、风险接受或既有 legacy 字段兼容承诺。R1已独立确认 null 的 unknown 语义与已知消费者适配正确，没有放宽或绕过门禁；所缺仅为规范源未同步。

## 本轮修改

- 唯一规范源 `.ai/design/product-readiness/backend.md` §4.2 字段类型与 Go shape 明确三态；未知时 null 不等于成功或已知不匹配，仍由同源 status/checks/blockers 阻止不安全升级。
- 主设计与 frontend 设计追加 DEC013 Applied 追踪。
- backend-contract.json、frontend-dependencies.json 的 source_sha256 精确匹配对应源文档；不另造第二契约源。
- Decision Log、super-run plan/state 记录D2所有权、依据、复核范围与validation_revision=2。
- API长期文档、Go `*bool` 投影、HTTP/CLI、TS `boolean|null` 与 UI消费者早已一致，本轮**没有修改运行代码、测试、fixture、依赖锁、配置或迁移**。

## 最小验证

`.ai/evidence/checksum-source-r2/verification.json` 保存：

1. 相对已验证/已push源码269e4e2，所有差异均位于.ai；无运行输入变化。
2. 两派生缓存 source hash 与当前唯一源完全匹配。
3. 唯一设计字段 boolean|null、实际Go *bool、TS boolean|null 一致。
4. 三份设计的 decision-closure 扫描通过。
5. `check-design.py --require-complete --json`：passed，0 findings，文档集合与阶段状态一致。

首次把补充表放到三级标题使结构解析器误当文件影响表，出现8个纯结构finding；改为独立二级章节后原工具passed，不更换工具或忽略校验。

## 证据复用与只读复核请求

- R1报告 `.ai/quality/backend-accept-product-readiness-backend-r1-20261024-050900-009359c1.md` 仍保留有效REJECTED；不改其结论。它已独立确认其余必要门禁通过，tests_to_run=[]，允许规范收口后复用运行证据。
- 运行候选仍为 `269e4e25ec4693c184cc7ebf493595ec1a97136a`，本文件所在提交只同步规范和工作流记录；应按相关输入判断复用，不谎称场景在新文档commit上重跑。
- R2独立accept仅复核R1-C1：裁决归属/权限、规范源/缓存/API/消费者一致、没有运行输入变化、原门禁仍有效。不得由主代出通过结论；若仍有必要冲突则真实REJECTED。
- 不再次生成物理故障、不全跑542、不扩大到尚未请求的frontend/web-demo，也不把阶段通过写成全部产品业务验收完成。
