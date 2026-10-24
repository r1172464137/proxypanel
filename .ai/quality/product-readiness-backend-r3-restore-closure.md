# backend R3：真实恢复链闭合

候选以`3d5b5a4`的readiness修复为基础，随后合入C的私有恢复runner并修正其旧契约假设；生产实现未再变化。

## 权威执行

- store/升级门禁：`.ai/evidence/backend-test/20261024T113542Z-7676d17a/result.json`，两selection均passed，61+39实际case/subcase，0skip/fail/missing，输入稳定。
- 真实恢复：`.ai/evidence/web-demo/C/r3-final-green-20261024T115143Z-b7506f/`；runner/test receipt均pass，20/20 assertion passed、0fail/blocked，私有临时目录已删除。

恢复链实际覆盖：两套新PG17集群、真实pg_dump custom/pg_restore、真实Agent/Xray/流量/用量；schema-current恢复库可Open并在隔离入口进入recovery；普通授权写及snapshot被409阻止、必要遥测/身份救援可用；真实SQLite队列精确重放，ACK已删缺口不伪造；周期缺口用确定性owned fixture负例返回LEDGER_UNATTRIBUTABLE且不ACK；同ID同语义payload duplicate、异语义payload BATCH_PAYLOAD_MISMATCH；退出保持进入恢复时的随机generation，Agent先coordination_required，再显式accept；备份后撤销被审计重放，旧凭据真实拒绝，仍合法成员的新导出真实连通并入账。

修正的测试旧假设：红acted inspect不包含bearer token，认证探针改从0600停止态副本读取且不落证据；recovery期间已有订阅token允许只读200，但实际旧凭据必须拒绝；snapshot在恢复态409，coordination receipt应在verified exit后才出现；exit保持而不二次轮换fresh generation；唯一合法成员计数为1；幂等异载荷必须语义变化，JSON空白不算异payload；recorder ledger键含agent前缀。均与既有后端/前端设计和生产代码一致，未为绿改弱生产安全。

## 环境边界

本轮host identity收据为false：外部Agent PID625047的Xray子进程在运行窗口自行反复重启（before PID1732329→after1733099；随后每约2秒继续换PID），runner未向其发信号，私有owner进程已全部清理。产品20项断言可用，但不把此轮写成“全宿主原core身份不变”。原Agent父进程和共享PG未被改动；8443/8445/15432均未作为测试mutator。该外部实验室core不稳定需在最终物理运行协调时单独处理，不能归因于恢复实现或静默忽略。

## 待验收

backend R3生产修复和恢复链已具备独立验收输入；正式backend accept仍需只读判断。完整web-demo的A/B/D/E及18故事不是本报告结论。
