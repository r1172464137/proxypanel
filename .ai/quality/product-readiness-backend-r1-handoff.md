# product-readiness backend：R1 独立验收交接

## 本轮范围

- 验证源码：`269e4e25ec4693c184cc7ebf493595ec1a97136a`，已普通push至origin/product-readiness并通过ls-remote核对；不是发布/main更新。
- 仅当前backend阶段，validation_revision=1；R0报告有效REJECTED，未被改写。后续frontend/web-demo未正式完成。
- 本轮只闭合R0 F1–F4、文档核对暴露的已批准接口漂移及真实故障分类。原R0已通过且相关输入未变的证据可按公共协议复用，不要求再跑全部542根。

## 修复责任闭环

| 项 | 实现/行为 | 对应源码及证据 |
|---|---|---|
| F1 历史方案退役 | control锁→对象锁→未归档当前绑定409→已归档幂等→member_bindings历史归档并保留历史/ledger/组关联→无历史才物理删。DELETE不要求version；成功原快照附加retired布尔，不污染GET。 | resources.go、admin2.go、delete_contract_test.go；contract/delete及store/database-baseline当前执行 |
| F2 模板引用保护 | 未归档方案引用409 CONFLICT/details.reason=template_in_use/reference_count；拒绝无副作用，解除引用或只有归档引用允许物理删。 | 相同受控事务、真实PG并发/HTTP回归；SubscriptionsView/TemplatesView最小反馈与类型 |
| F3 长期契约 | implementation-contract.md、reference/api.md已按实际字段/行为同步；分页/恢复救援、usage对象、DELETE快照与引用计数、readiness及错误码说明不再停留旧实现。 | 相关docs源码、consumer类型检查与测试；不新增OpenAPI链 |
| F3 实现漂移补齐 | admin usage真实keyset，period_start DESC/member_id/node_id/period_end ASC，过滤/管理员scope绑定；核心/运维/me/usage游标统一CURSOR_INVALID，其他validation不改。readiness公共DTO来自同一内部核对结果，API/CLI共用，保留必要legacy别名，unsafe拒绝有READINESS_BLOCKED类型，诊断GET仍200，enterRecovery不受其阻止。 | admin_usage_page.go、分页helpers、readiness.go、CLI及新PG/API回归 |
| F4 升级交付 | install仅staging，候选真实CLI预检ready才激活且不启动；无/错材料不切指针，技术rollback-check只核对已提交前缀并保持stopped；补readonly材料/ACK排空/停止/相同路径配置及高风险DB恢复区别。 | manage-linux.sh、upgrade-panel.sh、两deployment文档、TestUpgradeDeliveryHelper；当前统一upgrade/delivery选择覆盖8根 |
| 必要物理故障 | PostgreSQL真实fast stop不ACK/队列ID与payload保持/无半提交/重发唯一入账；Redis真实中断有界本地降级与恢复；新私有namespace/tmpfs真正ENOSPC、SQLite FULL13，队列/baseline/clock/seq保持、释放后恢复。PG连接/关闭类SQLSTATE修正为可重试503 DB_UNAVAILABLE，不泄露驱动原文。 | dbErr及两新错误分类单测；real_fault_*资产与当前同候选运行 |

## 当前候选集中执行与故障闭环

1. `.ai/evidence/backend-test/r1-integrated-targeted/result.json`，实际run `.ai/evidence/backend-test/20261024T043929Z-cfa40b23/result.json`：12个相关选择，输入稳定；其中3个选择都仅同一个TestUpgradeDeliveryHelper失败，原因是psql动态库libpq.so.5未进入测试命令环境，exit127。不能把这次失败报告改成绿。
2. `.ai/evidence/backend-test/r1-integrated-targeted/helper-library-green.json`，实际run `.ai/evidence/backend-test/20261024T044330Z-2501669e/result.json`：仅补SDK LD_LIBRARY_PATH，源码/断言不变，upgrade/delivery全部8根通过、0skip/0missing。原失败case是唯一待闭环case，原其它case已通过；不重复无关Store/API全套。
3. 同目录web-typecheck.log、web-delete-tests.log、native-build.log：补跑之前因set-e未执行的消费者和构建，监控成功退出。主源码未改。
4. 新场景/错误分类的Expected Test Manifest均在项目scripts/backend-test-manifest.json。3个owned-fault选择必须通过其owner launcher，不能在没有PP_FAULT_OWNER的普通环境冒通过。

## 本轮最后行为验证

- `.ai/evidence/r1-final-269e4e2/frontend-data-fault/result.json`：当前候选222前端单测、25真实浏览器、npm锁审计0、真实Panel-Agent-官方client-traffic-ledger-ACK-撤权/新配置恢复、物理故障3项全通过；原进程未变化、资源清理通过。
- 浏览器：`.ai/evidence/browser/product-readiness/2026-10-24T04-47-50-005Z/`。
- 数据面：`.ai/evidence/data-plane/product-readiness/20261024T124820-3ab371f2/`。
- 物理故障：`.ai/evidence/backend-test/real-fault-r1/20261024T044830Z-578ec0f2/`。
- 双HTTPS跨版升级：`.ai/evidence/deployment/dual-hosting/r1-final-269e4e2/verified-summary.json` 已核验通过；同269e4e2候选，旧7feee36→当前，两模式各有旧版/升级后4份真实browser报告，5→6且原五行checksum/applied_at保持，相同数据库/master key，显式空WEB_ROOT有效，四产物关联匹配，自有资源清零、原对象未变。原始result/refs/browser/migration/fingerprint均保留；不依赖不存在的人工摘要。

## 明示技术细化/限制，需独立判断

- schema.checksum_ok使用boolean|null：实际已知成功true、已知不符false、无法核验null。这是为保留unknown语义而对设计boolean草图作的显式技术细化，长期API文档与TS消费者均已注明；未放宽任何预检/启动门禁。请独立核查该表示与需求/消费方一致性，不由主预设放行。
- candidate_version来自Go真实模块构建元数据；没有则(devel)/devel，不硬编码commit或虚构release。发布元数据注入另有边界，不把版本字符串当材料有效性证明。
- legacy队列零计数不证明远端Agent队列为空；所有新旧诊断字段由同一内部结果投影，未知仍阻止不安全升级，只有已配置服务端只读材料，无HTTP文件路径接口。
- HTTPS为隔离nginx模拟宿主，升级fixture无Agent；真实宿主systemd/公网CA/全部客户端/18故事由后续已启用frontend/web-demo正式承接，不在本backend放行后称全部业务验收通过。
- R0未执行声明及其独立逐项判断保留：实验measurement策略、非LinuxACL、11历史Docker包装、Platform及独立Vision。真实故障已补本阶段必要等价证据，不能改称旧套件已运行。
- Go依赖锁未变化；R0govulncheck零reachable/imported及唯一未导入OpenPGP模块级残留的事实不改成零漏洞。当前改动不引入新依赖；是否必要最小补查由accept独立判断。

## R1验收边界

正式backend-accept只读、串行，必须产出新的独立报告，复核R0问题是否真实闭合及新差异回归风险。允许复用有效原始证据，不默认重跑全542。无通过结论前backend phase不标completed，不修改原R0拒绝报告；若必要门禁失败/未验证或仍有P0/P1，按公共合同REJECTED交回责任人。
