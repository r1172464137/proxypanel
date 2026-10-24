# product-readiness frontend：冻结候选与独立验收交接

## 范围与来源

- 运行/实现候选：`706f0ba8abe0d9193c12436509264670040f2852`。当前非.ai业务源码、测试、fixture、锁和配置与此提交一致；后续.ai状态/授权记录变化不伪称重新执行。
- 当前仅frontend phase，backend已独立ACCEPTED，web-demo未正式开始。用户DEC014允许最多6个DS V4 Flash隔离实际交付、主大体复查；正式accept依然独立只读串行。
- 规范：主设计、frontend.md及backend.md唯一API源；PRD/user-stories/decision-log；frontend阶段计划。Vue/现有hooks与手写API优先于React/生成客户端模板，新增框架/依赖数量0。

## 已交付的本阶段闭环

| 体验/行为 | 当前实现与关键边界 |
|---|---|
| 导航、设置、兼容入口 | 真SetupView而非redirect；MeConnect独立；原/me及credential/invite/usage-detail兼容；#link/#qrcode迁移保query/hash；路由角色守卫与next不开放外部跳转 |
| 工作台与真实状态 | setup逐步对实际API，合法已知completed_count保留，unknown/blocked单列，缺失不假0；失败任务/agents首20项清楚限定范围，不装全部；修订两值已知才判落后，事件只白名单链接 |
| 恢复与预检 | admin-only共享recovery查询、单轮询、身份epoch/失效；已有token readonly与无token局部提示区别，成员不探测admin；readiness ready/blocked/unknown/读取失败与checksum三态分开，中文映射+原诊断保留 |
| 查询、跨页与批量 | 真实服务端q/filter，ID选择不复制成员对象；翻页保留选择但preview失效，筛选/refresh/cursor失效清范围；preview晚响应隔离；不确定提交保留原key/payload持久核对；批次详情集中解包，部分完成三类计数 |
| 节点、方案与邀请 | 节点原生未知键/空数组/多证书无损；脱敏空值保持，成对显式换钥和影响确认；版本冲突保草稿；方案当前/拟保存/下一周期只读，影响保存预览；退役/物理删差异与模板409原因；邀请max_uses1、实际方案、关闭清一次性秘密 |
| 成员使用与账号 | 首页状态/已知额度/期限/节点/导入；MeConnect管理链接/二维码/reset；只有真实可用format才产链接，无未经验证一键import；copy不旋转且失败手选；仅声明配置取回不声明连接验证；me用量对象与adjustments首屏；nodes/sessions遍历真cursor；两reset默认node与全量确认；密码401只读核对；session旧回复不能复活旧身份或覆盖新CSRF |
| 通用状态与可达性 | 资源reset隔离晚回复；polling隐藏停、可见重读、不并发、scope释放，异常显式重试；非契约HTML/纯文本不显示可能含密正文；新错误码行动提示；部分/回滚/未确认不假已生效；触控目标与焦点有真实UI证据 |

私有实现落点按现有复用选择，不为凑同名文件造重复组件。部分原示意组件通过现有Card/纯派生方法承载；用户可观察行为/接口安全不能因此豁免。TemplatesView原内联调用是设计明确例外；Runtime与BatchList已中央typed封装。

## 冻结候选统一技术结果

证据根：`.ai/evidence/frontend-phase/frozen-706f0ba/20261024T071857Z-1046d1af/`。

- `vitest.json` 主独立JSON解析：300 tests PASS，0 fail，0 pending/skip；48实际testResults文件。worker说107/107是Vitest suite统计，不当文件数量。
- typecheck/build命令均0；npm audit metadata各级total=0；工具版本与各命令在summary.json和原始logs。
- 真实Chromium probe：`probe/2026-10-24T07-20-06-410Z/report.json`，候选706一致，17场景PASS、0FAIL、无pageerror；不是18完整故事。
- 真实Panel+当前构建+Go helper新随机schema，exclusive schema证明，no intercept/mock API。SYNTHETIC节点配置明确标，不冒实际core/Agent运行。
- 场景包括真实setup逐项/known+unknown、新旧路由/权限、admin全局恢复与两类token成员、退出恢复门禁、持久batch包络/计数、真实API仅改名配置回读深等、390px coarse两角色、亮暗computed变化、Tab/Enter/Esc与焦点归还。
- 源码/测试/fixture/锁/迁移前后sha列表完全相同（source-tree-diff.txt明示trees_identical=true），worktree无源码修改；真实浏览器/Panel/private temp已退出，schema_dropped=true，验证前后pr_browser schema数量0；主专用PG未停，阶段完成后主精确关闭。

### 主对DS报告的复查（不是照单全收）

1. 285早期全量中两失败是Subscriptions旧按钮文本定位；DS只改2行稳定钩子，保留retired true/false持久反馈/API参数/list刷新断言，13相邻case绿；主读diff核对生产testid和既有modal footer钩子后合入。
2. README风险：模板内联是已批准例外，不新造封装；generic verified未被页面用于连接语义，不直接判P1。真正的MeUsage cursor恢复与Runtime中央读取已定向修复。
3. 最终worker疑称“两plain links44断言未生效”，主查实际probe：overview-tasks-link、member-username-ID明确传入assertMobilePage，该函数逐项要求可见/横向不溢出/box.height>=44。当前实际加载probe SHA `c31fe1b2e29a710ad7801d910f2b189b2682f6ce8c8bc0678a16add40de33938`与源码一致，admin390场景PASS。worker混淆了可为空的关闭抽屉navLinkHeight守卫；admin返回摘要省略了每key测量但断言已执行。复核事实在main-touch-review.json，原worker备注未删，不重跑同一事实。
4. 合成秘密也保持私有，raw evidence未入Git；scope-report/数字不能单独代替以上原始输出。

## 未执行与后续正式承接

- 本次17技术场景不是全18用户故事、不是UI→真实Agent→官方client→ledger整链。后端已通过的底层证据可复用，但完整管理员/成员故事、全部支持平台与Vision、双Agent/离线UDP/部分完成、真实备份恢复/旧Agent SQLite/核心跨版与systemd/实际宿主TLS/DNS/公有CA仍由web-demo负责。
- 浏览器技术证据仅loopback DEV HTTP/Go托管；两种HTTPS托管的backend原证据不被改称本前端新场景已在两模式全验收。样式风险用真实viewport/coarse尺寸、computed主题和键盘焦点证据核对；没有屏幕阅读器/截图视觉回归认证。
- 用户已选阶段通过后203独立8445预览、新数据库，旧8443/PG/Redis/Agent不变。只读库存不是升级证明；缺停止/队列材料时不允许升级旧库。验收前仅可构建新owner镜像，不能启动预览；预览外置TLS网关不在Go面板内运行，不冒实际host nginx部署已验收。

## 收口请求

独立frontend-accept核对本阶段必要行为、API/状态/类型、证据输入与清理、后续承接。已有有效证据无需默认再跑300；发现必要缺口应REJECTED，不能以数字或已有backend放行抵消。主会话不代出验收结论，不自动进入web-demo。
