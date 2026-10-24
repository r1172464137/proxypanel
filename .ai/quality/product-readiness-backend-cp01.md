# backend CP01：静态资源与独立安装器

状态：本checkpoint已运行验证；backend整体dev仍in_progress，test/accept尚未开始。不是完整产品验收。

## 实际改动

- `internal/panel/api/static.go`：通过os.Root打开文件，不允许根外符号链接；验证/响应均只接收普通文件。
- `static_open_unix.go` / `static_open_other.go`：Unix非阻塞打开防止FIFO卡住；保留非Unix编译分支。
- `deploy/deploy-agent.sh`：按独立二进制布局渲染服务单元，正确绑定状态目录及显式CA；拒绝不安全状态路径；保留配置时不覆盖单元/CA。
- 两个局部回归文件与项目Go选择runner/增量manifest。安装测试仅替换特权副作用，执行真实校验与单元渲染。
- 主审调整：拒绝测试必须验证exit=1及校验消息，不能将缺失函数127冒充正确拒绝；FIFO测试增加Unix构建约束；删除不成立的install原子替换注释。

## 证据

- 完整候选导出及输入指纹：`.ai/evidence/backend-test/first-batch-source/complete-deploy-candidate.json`、同目录patch。
- 实际仅采用四个生产路径，来源：同目录`receipt.json`，没有应用整个12路径候选。
- 修前同一组回归真实失败：`.ai/evidence/backend-test/20261023T123544Z-5cf2861b/result.json`。
- 修后：`.ai/evidence/backend-test/20261023T124451Z-932af4d4/result.json`。
- 运行入口：`uv run scripts/backend-test.py --json -- api/static e2e/standalone-agent`。
- 14个预期顶层用例全部实际运行并通过；包括子用例的run事件分别为25与31，不能把这两个数字当独立顶层用例数。0遗漏、0跳过。
- 回归文件、runner、manifest在修前与修后内容SHA256相同；没有靠修后改断言造绿。
- `go build ./...`、Bash语法、`git diff --check`通过；Windows amd64 API测试二进制仅交叉编译通过，未运行。
- 编译汇总：`.ai/evidence/backend-test/first-batch-build/result.json`，原始构建日志同目录。
- 工具：Go1.27.3、uv0.13.5（官方PyPI wheel摘要校验后安装用户目录，不修改系统Python）。

## 明确未验

未执行真实systemd安装/服务重启、宿主HTTPS双托管运行、依赖漏洞扫描、数据库/周期/身份整合、Agent与客户端数据面、前端新体验和完整E2E。首批manifest只声明增量注册范围，不能当作全仓测试覆盖。旧成绩不计入本轮。
