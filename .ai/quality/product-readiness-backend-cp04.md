# backend CP04：检查连接与自动迁移分离

状态：当前checkpoint已验证；升级预检/历史导入/完整backend尚未完成。

## 实现

- OpenReadOnly不需要主密钥、不调用Migrate；正常Open保持密钥校验与自动迁移。
- 两者复用数据库连接池构造，读连接覆盖DSN中可能的read_only=off，默认只读。
- 检查连接Migrate/inTx明确ErrReadOnly，秘密解密/加密明确拒绝，避免nil密钥导致panic或意外解密。
- 只读连接不是对能执行任意SQL的内部代码的安全沙箱；检查逻辑不得接收用户SQL。

## 实际PG开发探针

- 环境：全新独占PG17，`.ai/evidence/backend-test/owned-postgres-environment.json`；fsync=on，未触碰旧.devdb/Redis。
- 探针源码与输入验证：`.ai/evidence/backend-test/readonly-probe/main.go`。
- `.ai/evidence/backend-test/readonly-checkpoint.json`含源码SHA256及原始结果。
- 无主密钥、故意不存在的迁移路径仍可连接；两条池连接均read_only=on、SELECT42成功。
- CREATE TABLE必须返回真正SQLSTATE25006，不以任意非零代替；检查schema表数0→0。
- Migrate在读迁移文件前返回ErrReadOnly；普通Open正对照正常迁移32表。
- 临时随机schema清理成功。该探针为开发者行为验证，不冒充完整业务场景或新增顶层测试计数。

## 现有测试回归与oracle校准

- 首次store包51个预期用例0遗漏/跳过但1失败：`.ai/evidence/backend-test/20261023T133529Z-a6275451/result.json`。
- 失败属于旧场景调用生产Containing生成重叠期望。依据与编排决定在`.ai/quality/product-readiness-cp04-test-semantics.md`。
- 临时切backend-test仅校准此资产，保留输入及所有其他断言，独立UTC算术替换自证式oracle；作者只编译，之后切回编排执行。
- 修后：`.ai/evidence/backend-test/20261023T134213Z-795a6633/result.json`。
- 命令：`uv run scripts/backend-test.py --json -- store/database-baseline period/calendar api/static e2e/standalone-agent`。
- 存储包51个顶层用例通过，加period16、static7、standalone7共81个不重复顶层用例通过，0跳过/遗漏。
- store/cycle的9个用例已包含在51中，没有重复累计；没有宣称这是全仓或完整E2E。
- Go全仓构建及diff检查通过。

## 待办

CheckUpgradeReadiness、材料解析与Agent只读导出、追加迁移、历史区间持久化与迟报、恢复门禁/身份/Agent/协议/产品接口和真正完整E2E仍待后续关闭。test/accept仍pending，不部署当前分支。
