# product-readiness 阶段交付（2026-10-24，未完整放行）

## 当前交付

- 状态：**部分交付 / 可预览，非生产放行**。用户要求结束过长等待；本轮15分钟硬限内固定候选及缺口，不新增任务/范围，不为全绿继续排队。
- 应用候选源码：`0e1adbd17e1c4596029a203a8f0bbdf5c781286d`（生产变化仅匿名根入口角色默认落点；其余新内容为测试/交接资产）。
- 代码分支：`product-readiness`，阶段资料提交后推送备份。
- 独立预览：<https://192.168.5.203:8445/admin/overview>。2026-10-24T10:31Z只读复核readyz postgres/redis/status=ok、4容器healthy；原8443未动。预览仍为先前已部署706f0ba，不冒称刚滚动更新到候选，账号/密码/数据均未重置。
- Linux amd64阶段包（实际Ubuntu host执行验证的panel二进制+fresh前端+6迁移+README/manifest/SHA256SUMS）：
  `.ai/evidence/stage-delivery/20261024/product-readiness-stage-linux-amd64.tar.gz`
  大小11,515,330字节，SHA256 `39ff1e5772cf44b1c09de6053b98de327ab5d96f628f0a8a38659a731682fa5f`。
  不含数据库、Agent状态、密码、证书私钥、会话或订阅配置；只用于全新隔离库预览，不接已有业务库/灾备恢复。不是跨平台正式release。

## 已验证，分别限定范围

| 项目 | 有效结果 | 证据 |
|---|---|---|
| frontend根入口修复 | 302/302、0skip、typecheck/build0，独立R1 ACCEPTED_WITH_IMPROVEMENTS；5P2保留 | frontend-root-route-r1/20261024T094736Z-4b66c0c1；frontend-accept R1 100650-bf96c728及audit |
| 正常真实主链 | A2精确源码4文件SHA已对账；10/10尝试PASS，**13 SKIP**，不是18/18 | `.ai/evidence/web-demo/A/20261024T100250Z-chain/receipt.json`（代理误报了少一层proxypanel的路径，以本路径为准） |
| 主链内容 | UI创建/邀请兑换、首页导入、真实AgentSupervisor/官方Xray、69,632B SHA、同成员PG/状态/页面计量、暂停新/旧连接拒、恢复新导出可用旧凭据拒 | A2上述receipt；0 synthetic usage |
| 真实host nginx两模式 | 主候选fresh dist与R1完全匹配，真Go API-only直接404/API成功、同DB/key/binary旧session保持、私有CA严格curl/Node/NSS浏览器不绕验证、签名Ubuntu来源、前后host/监听不变 | `.ai/evidence/web-demo/F/host-main-20261024T101214Z-fe65bc/main-verification.json` |
| 真实Agent用户级unit生命周期 | 同exe+SHA两PID、心跳恢复、真实持锁、具体否定oracle、cleanup通过；3case0skip | `.ai/evidence/web-demo/F/main-dd40fac-retry-20261024T093138Z-a8941e/main-verification.json` |

A旧14PASS/7红checkpoint源/产物绑定被独立验收否决，已加NOT_REUSABLE标记，原始数据保留，不用于凑当前矩阵。

## 未完成 / 不得报通过

1. **真实后端启动/恢复可用性缺陷**：`store.Open`每次postflight对所有Agent要求升级停止态材料，无待迁移的普通/恢复启动同样被AGENT_EVIDENCE_MISSING挡住；面板不监听，文档“启动后进入恢复核对”不可达。C有8项真实备份/恢复前证据，恢复后流程1项blocked，不绕过unknown/checksum/停止证明，不回灌ACK已删的49,546B来假修。
   - 诊断：`.ai/diagnose/webdemo-restore-startup-readiness-20261024T1200Z.md`。
   - backend重开validation_revision=3，待主D2界定过渡材料适用边界并新增合法0-pending/真实Agent启动正对照；有待迁移缺材料、checksum异常、账本/归属异常仍拒绝。尚未实现修复，**不建议本候选用于已有Agent的正式重启、升级或灾备恢复**。
2. 完整18故事整文件终验尚未完成。本轮不再以调试run或合计占比凑全绿；A2尚有13条未跑，需固定版本有界执行后最终独立验收。
3. B/D原报告有P1证据缺口（锁旁路/拒绝分类/原身份保护、旧Agent来源/core真实切换/跨运行数字），旧报告不视为全面放行。B修正2446f5c仅编译/静态通过、未新实跑，未合并；D修正任务仅收最后阶段产物，不继续占锁跑新矩阵。
4. E修正b0b8f35已返回3Vision/21主格+4辅助/21selftests/race结果，但主整合审查未完成，分支原样保留，不合称最终统一候选全绿。
5. 系统级安装/完整unit硬化、公有CA/DNS、GUI实机/手机/读屏器未验证，不声称支持实测；额外平台/公有PKI不是凭空新增的强制产品范围。已选内部私有CA真实两托管有实证。

## 后续仅剩必要收口方向

优先修启动/恢复门禁的适用边界与合法协调入口，不能以skipReadiness/SQL标ready/复活旧proof绕过。随后只跑受影响Open/恢复对照及当前完整故事，消费B/D/E已有修正的最小有效证据，再独立最终web-demo验收。当前没有feature完成或可生产升级声明。
