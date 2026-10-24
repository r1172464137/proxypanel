# B/D定向审查更正：撤回D-1中的错误历史引用

本文件仅更正事实，不重新验收、不修改原始报告/日志，不执行运行场景或改变门禁结论。

## 主实际核对的原始Git事实

2026-10-24，主分别执行`git show <ref>:go.mod`：

| ref | golang.org/x/sys |
|---|---|
| ec7ee10 | v0.36.0 |
| 52bf44a | v0.36.0 |
| 069adee | v0.47.0 |

因此`.ai/quality/webdemo-BD-focused-review.md` D-1中“52bf44a用0.47.0”“全历史仅初始提交用0.36.0”“artifact不可能来自52bf44a”的推论**不成立，撤回**。buildinfo只给出依赖版本相容范围，不能凭它精确归属任一commit。独立复查报告也是待核验输入，不能覆盖Git原始事实。

仍成立的部分：`.proxypanel-build`不记commit，同目录SHA256SUMS只能校验副本完整性，不能证明源码来源。原artifact仍来源未定；不得借本更正将旧正向记录恢复为已知源码跨版本验收。

## D修正交接（不是通过）

新分支提交`9621309`，相对d28e3df只3个owned文件，主未合并：
- `tests/e2e/run_webdemo_upgrade.py`
- `tests/e2e/webdemo_upgrade_manifest.json`
- `tests/e2e/webdemo_upgrade_test.go`

作者改为默认从已知commit归档构建旧Agent、逐阶段受管core实际PID/exe/SHA及合法成员完整流量、同次生成报告、先pg_ctl优雅停机再残留回收与after/TCP+UDP保护核验。编译/vet/纯Python导入和旧源码构建检查已报完成；**未执行修正后的真实跨版场景**。这些新断言、positive probe、cleanup分支均仍未验证，主没有合并或计入已放行候选。

阶段包/应用候选/预览均未改变；启动恢复门禁缺陷、完整故事终验与正式web-demo验收仍未闭合。本更正不启动新任务，不增加运行范围。
