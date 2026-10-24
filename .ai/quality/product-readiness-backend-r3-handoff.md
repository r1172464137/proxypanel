# backend R3 handoff：升级过渡材料不再永久阻断 schema-current 启动

## 决策与实现

`DEC-product-readiness-018`：Agent停止态外部材料仅在本次有待迁移文件，或同次迁移最终核对持有短命baseline时强制。schema-current、0 pending 的普通/隔离恢复目标启动不消费陈旧升级材料，但仍在迁移锁内执行全部数据库内部checksum、账本、周期/归属、任务孤儿、批次hash门禁；内部blocked/unknown继续拒绝。

实现位于`internal/panel/store/readiness.go`：先从同一数据库快照计算迁移状态，仅`len(Pending)>0 || baseline!=""`时加载外部材料并核对每个Agent。空库有pending时，显式配置的损坏材料仍是operator error；空库未配置且无Agent保持ready。当前库有Agent、0 pending时不会产生`AGENT_EVIDENCE_MISSING`，陈旧/缺失升级材料不影响重启。

测试同步：
- schema-current+Agent+无/陈旧材料可以`Open`；随后插入`TASK_ORPHAN`仍以typed READINESS_BLOCKED拒绝。
- 旧prefix+Agent+pending+无材料仍为`AGENT_EVIDENCE_MISSING/unknown`。
- 同次受控迁移仍验证baseline/fence/材料变化；迁移完成后的第二次Open不再重复消费旧proof。
- runner manifest纳入新测试。

规范同步：backend设计§5.3/6.4.4、决策账本DEC018、`docs/operations/backup-recovery.md`。恢复副本仍必须在隔离网络启动并立即进入recovery mode，轮换generation后完成授权/账本核对；本修复不允许公开旧备份、不放开Agent snapshot/订阅/普通写，也不把PG记录当本地队列为空。

## 实际验证

权威结果：`.ai/evidence/backend-test/20261024T113542Z-7676d17a/result.json`。

- `store/upgrade-preflight`：passed，61个实际case/subcase，0 skip/fail/missing。
- `upgrade/delivery`：passed，39个实际case/subcase，0 skip/fail/missing。
- 真实候选panel二进制、私有PostgreSQL Unix socket、真实psql和运行库；输入前后指纹一致。
- 此前四次未执行/环境失败记录保留：缺隔离标志、过期TCP密码、缺psql PATH、缺libpq。它们不冒业务失败；最终权威run已替代。

## 尚需

本handoff只闭合store级门禁。下一步必须用C真实pg_dump/restore链验证：schema-current恢复库可启动→隔离进入协调→旧凭据不复活→核对/新generation→队列精确一次及ACK已删缺口保持记录。随后独立backend R3验收；未完成前不恢复整个feature完成状态。
