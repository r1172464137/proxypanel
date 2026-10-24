# R1 长期契约同步（F3）

工作树 `/home/compile/Work/proxypanel-worktrees/pi-web-agent-db6592b8-5aad-4831-8d20-4be615c3c693`，基线 `6d1345c54b5fc5b3ea0033275338da8ff8362820`。仅改两个长期文档，不改生产/测试/deploy/helper，不运行542全量或浏览器，不派代理、不push。报告位于主绝对路径，不stage。

读取 F3 拒绝报告与 backend 设计 §4.3、§11，并核对真实路由、分页解析、SQL、DTO、CLI。以下“核对”均为静态源码检查，不声称运行验证。

## 文档—代码对应表

| 文档落点 | 契约/决定 | 定位证据 |
|---|---|---|
| implementation-contract HTTP约定；api §2 | 对象 data + 空 request_id，列表 next_cursor，错误详细包络 | api/api.go writeData/writeList/writeError |
| implementation-contract HTTP约定；api §9.1 | 六核心列表 keyset，默认50夹取1..200，q仅nodes/members，未知/重复参数400；过滤绑定 | api/admin_pages.go adminPage/adminFilters/writeAdminPage；store/admin_pages.go adminReadPage |
| api §9.1 | nodes sort_order/lower(name)/id；members lower(username)/id；其它lower(name)/id；C排序 | store/admin_pages.go adminReadPage SQL，limit+1及Next |
| api §9.2 | 七运维列表keyset及各自过滤；管理员/端点/过滤绑定；expired_scope与invalid区分 | api/operations_pages.go operationsPage/operationsList；store/operations_pages.go operationsQuery/List*Page；api/api.go实际挂载handleOperations* |
| api §9.2 | agents/core按名字升序；invitations/batches/events/tasks/audit按created_at/id降序；事件q字面搜索 | store/operations_pages.go operationsQuery，各List*Page |
| implementation-contract 成员端；api §9.3 | me usage 是items对象，第一页adjustments全量，后页[]，usage真实游标 | api/handlers.go:309-342 handleMeUsage；store/me_pages.go ListMeUsagePage/ListMeAdjustments |
| api §9.3 | me nodes先授权再分页；sessions按created_at/id降序；本人/端点游标作用域 | api/pagination.go mePage/meNodePage；store/me_pages.go ListMeSessionsPage |
| implementation-contract 恢复/成员端；api §5.2 | login/logout/本人改密/撤销本人其他会话救援；当前session DELETE400，用logout | api/api.go Handler route wrappers；api/handlers.go handleMeSessionDelete/handleMePassword/handleLogout |
| 同上 | 有token只读可用，无token创建409且仅链接区降级 | api/handlers.go currentToken/handleMeSubscription；Store.EnsureSubscriptionToken 的恢复写门禁 |
| api §9.3 | known/unknown/unlimited/zero是解释状态，不能发明统一状态字段 | store/product_status.go ProductStatus（data_complete、proxy_access unknown）；domain Member及access Decision配额指针 |
| 两文DELETE小节；api §10 | 目标：有绑定409、有历史退役retired=true、无历史物理删、已归档幂等；模板未归档引用409+reason+引用数 | 批准backend §4.3；当前store/resources.go:270,1002及api/admin2.go:316,740尚无目标新分支，明确待验证 |
| api §11 | readiness真实DTO、200诊断unknown/blocked、服务器本地材料而非HTTP任意路径；CLI退出码 | api/readiness.go handleGetUpgradeReadiness；store/readiness.go:20-40；cmd/panel/check.go runCheck |
| api §7 | TLS示例改为原生tlsSettings.certificates[]数组，非旧顶层路径字段 | protocol/tls.go tlsCertEntry/normalizeTLS路径；其他请求字段与api create/patch路由一致 |
| api §4及两文DELETE说明 | 不要求DELETE version，保留兼容传参规则 | api/admin2.go handleDeleteSubscription/handleDeleteTemplate无requireVersion；helpers.go requireVersion仅PATCH/PUT |

## 提交主负责人的实际/设计差异（没有改源码，没有偷偷降目标）

1. **管理 usage 尚未分页**：api/admin2.go:789-801 handleAdminUsage始终writeList(items,nil)；usageWindow:805起不读cursor，limit默认200，非法/非正整数静默回落；store/usage.go:308-330 ListUsage仅period_start DESC且无唯一破平局。批准设计§4.2/§4.6要求真实cursor及统一分页错误。两文显式记录“目标待闭合”，不扩散为13个管理集合/本人usage都没分页。
2. **游标错误码**：设计§4.4提出CURSOR_INVALID，当前api/admin_pages.go、operations_pages.go、pagination.go实际均domain.Validation → VALIDATION_FAILED；运维scope mismatch另有field_errors.cursor=expired_scope，核心/本人为invalid。公共文档写实际，保留本报告给主裁决设计/实现统一。
3. **readiness DTO与设计不同**：批准§4.2/§4.3列checked_at/candidate_version/schema/checks{id,severity,state,summary,affected_count,evidence}/blockers；实际store/readiness.go:20-40为status/phase/checks{code,status,count,remedy}/applied_migrations/pending_migrations/pending_batches/duplicate_batches及可选摘要/stop_verification。api/readiness.go检查异常仍200 unknown。文档不捏造字段，明确批准目标与现状待主裁决。错误码READINESS_BLOCKED在当前domain常量中也不存在，不能凭设计新增HTTP响应声称。
4. **两DELETE仍未实现**：由并行代理负责，本轮以批准语义写并醒目标待验证；没有编造模板引用计数字段名、物理删除成功body或完整归档包装。主合入后必须核对retired字段与details引用计数字段最终位置/名字、条件顺序及历史守恒。
5. **状态四分法不是统一JSON enum**：生产仅有data_complete/data_issues、proxy_access等，不能造usage_state=known之类字段。文档将known/unknown/unlimited/zero明确为解释规则，数值null/0必须按证据完整性区别。

## 静态核验

- 相对Markdown文件链接存在；新链接均指向长期docs，不把.ai作为用户运行依赖；两文无Windows/PowerShell或.ai路径。
- api.md全部json fenced样例用Python json.loads解析通过：healthz、空列表、错误、version字段错误、me usage对象、readiness异常完整包络。me usage/readiness字段已逐一对照真实map/结构体。无真实凭据/完整token。
- 路由与代码名核对api.go、admin_pages.go、operations_pages.go、pagination.go、handlers.go、readiness.go及cmd/panel/check.go；TLS示例数组路径对照protocol/tls.go。不声称执行curl。
- git diff --check首次发现一处尾随空白，已修正，最终提交前重跑。
- 未跑Go测试/已有全542/浏览器；无新增数据库/服务资源，无清理事项。

合入后必须由主复核DELETE最终响应细节，并决定上述管理usage、CURSOR_INVALID、readiness设计/实现漂移如何闭合。文档提交不代表F1/F2运行验收或整个backend阶段通过。
