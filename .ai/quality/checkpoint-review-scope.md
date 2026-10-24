# CP01–04 专项审阅输入（非最终验收）

## 冻结范围

Git显式区间：`3435f1d..fe4bf8e`，在隔离worktree读取fe4bf8e版本。不要把主工作区未提交的新Agent证据DTO纳入此轮。

命令：`git diff --no-ext-diff 3435f1d fe4bf8e -- <目标文件>`；完整函数取`git show fe4bf8e:<path>`或当前隔离worktree文件。主分支本地状态不可修改。

相关文件：
- `deploy/deploy-agent.sh`
- `internal/panel/api/static.go`、`static_open_unix.go`、`static_open_other.go`、`static_boundary_test.go`
- `internal/panel/period/period.go`、`period_test.go`
- `internal/panel/store/access.go`、`store.go`、`period_advance_test.go`、`authoritative_regression_test.go`
- `scripts/backend-test.py`、`backend-test-manifest.json`
- `tests/e2e/deploy_agent_test.go`

## 已确认边界

- 月度周期按显式IANA/UTC民用日历；缺失午夜取首个有效时刻、重复取较早，拒绝宿主Local；半开区间与严格Prev/Next。
- 规则切换第一段从旧end开始，不能退入旧周期；未来完整历史表尚未实现。
- 只读检查构造不得迁移、无需/不加载密钥；普通Open继续自动迁移。只读连接不承诺对可执行任意SQL的恶意内部代码提供沙箱。
- 静态Root边界、根内相对链接保留、FIFO不阻塞；未知API不能SPA假200。
- 独立安装单元必须匹配其安装位置、状态和显式CA。测试只stub特权副作用，不是实际宿主安装验收。
- 新增/修正的测试oracle有冻结规则依据，不能回退生产行为去迎合旧的重叠期望。检查是否真有独立期望，不接受自证。
- runner只注册当前增量范围；记录expected/ran/missing/skip和内容指纹；不能把不同选择重复计数或缺环境记通过。

## 已有证据及非目标

实际已验证：81个不重复顶层用例（store51+period16+static7+installer7）0跳过；Go构建；只读PG开发探针；Windows API包仅编译，未执行。阶段未验收。

周期历史持久化、迟报账本、只读升级材料、Agent完整故障/撤权、协议兼容矩阵、前端与完整E2E均明确未完成。不能仅因这些尚未实现就报“本次引入P0”；但变更函数或其调用路径若存在具体错误仍应传递候选。

禁止：修改代码/测试/文档/状态，运行共享PG/服务/故障测试，读取.keys/.secrets或私有test.env，递归派代理。可以只读源码及git，独立Python纯计算；不得将未执行推断写成测试通过。

每任务最多20次工具调用/12分钟，超限报告未完成范围。具体问题要给输入/状态/错误结果和代码位置；没有可命名触发路径不凑数。结论是候选而非CONFIRMED，后续逐项独立验证。
