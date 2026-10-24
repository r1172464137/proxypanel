# D + protocol 实际整合证据

## 分支与范围

- 隔离 worktree：`/home/compile/Work/proxypanel-worktrees/pi-web-agent-b4de1e18-782e-4ae6-885c-cb38bfbc6b08`。
- 开始时主分支 `product-readiness` 最新为 `8e5a3053bd14411b32d1bfdba5d0511244174c43`。提交前主线新增 5 个 web-only 提交，已将隔离分支无冲突 rebase 到读取时最新 `a8e7bcf3a7b30defa4151c72c87a2f9d0d29dc57`；未修改主工作区。统一测试证据在 rebase 前生成，rebase 后再次核对 runner 输入指纹完全相同（runner 明确排除 web）；没有声称已验收这些 web 变更。
- 先读候选 diff、协议两份外部 handoff、D `LIFECYCLE_HANDOFF.md`，按序采用：`05f75cd -> 02f9d2e`、`88fe0f3 -> 52efea2`、`f728779 -> 15db1f0`、`b8085f2 -> dabfde4`。未重复采用等价 `afdc340`。rebase 后上述四个落地提交分别为 `d7cd733`、`fdb7607`、`d7024ff`、`74e9417`。
- manifest 冲突按 id 并集合并；机器校验 base 原有 10 条内容完全保留，整合后 19 条。新注册 `integration/protocol-agent`，不覆盖别路选择。
- 不改 panel store/api/web/SQL/HTTP 请求结构；没有主工作区写入、PG、真实 lab、push 或代理操作。

## 本轮接口验证与修正

1. `protocol_integration_real_test.go` 的同一连续链路，分别覆盖历史顶层 TLS 文件对、内联 PEM 字符串：Sanitize/Validate -> 历史未重存行的 xrayconf.Build -> Agent Prepare/Apply -> 固定真实核心启动 -> 校验证书信任和 ServerName 的 TLS/VLESS 精确 echo -> 技术替换 -> 不同 CoreGeneration 的独立真实计量 -> 空快照撤权 -> 精准进程退出、旧 TLS 连接中断、revision 3 applied 回执及 intent 消费、实例记录删除。每种形式两代均计入 28 upload bytes（Xray 实际统计值，不是人为写入计数）。
2. `sampling_order_integration_test.go` 在周期 StatsQuery 阻塞期间并发最终采样；第二次读取前验证第一次的 100 已持久化，最终读 200 后复读不重复计量，新 generation 10 独立计入，合计 210。结合原有 `TestCommitSnapshotSerializesWithMeasure` 验证 SQLite commit/measure 原子串行，原有 generation/reset 断言保持。
3. 具体修正：周期采样原来无条件等待 sample gate，即使 context 已取消仍等待；改为 `lockContext(ctx)`，新增取消回归测试。并修正 D 注释中的“Measure 使用高水位”错误陈述：生产仍使用 reset/rebase；最终采样 gate 超时是显式可观测的计量损失，不应宣称自动补全。
4. D 原有 intent 优先恢复、原子回执/水位、phase-zero 收紧、拒停保留 tombstone、expiry fail-closed、只读 export 无秘密、旧 schema intent unknown 等目标在本轮同一统一运行中复验，非拼接旧报告。能力/count 单独不是停止证明。

## 本轮统一成绩（仅注册目标，不是全仓验收）

证据根：`.ai/evidence/backend-test/20261024T012436Z-7691366b/`；`result.json`: `status=passed`, `inputs_stable=true`。

| selection | 顶层 | 子事件 | 合计 | skip |
|---|---:|---:|---:|---:|
| protocol/handshake-review | 6 | 7 | 13 | 0 |
| protocol/offline-review | 30 | 34 | 64 | 0 |
| integration/protocol-agent | 3 | 2 | 5 | 0 |
| agent-state/full | 16 | 5 | 21 | 0 |
| agent-supervisor/full | 63 | 2 | 65 | 0 |
| agent-xray/full | 78 | 46 | 124 | 0 |
| agent-reporting/full | 17 | 2 | 19 | 0 |
| agent-supervisor/real-lifecycle | 6 | 2 | 8 | 0 |
| **总计** | **219** | **100** | **319** | **0** |

复现（Go `/home/compile/sdk/go1.27.3/go/bin` 在 PATH 前）：

```sh
GOFLAGS=-tags=xrayreal XRAY_BIN=/home/compile/Work/bin/xray \
 python3 scripts/backend-test.py protocol/handshake-review protocol/offline-review \
 integration/protocol-agent agent-state/full agent-supervisor/full agent-xray/full \
 agent-reporting/full agent-supervisor/real-lifecycle
```

故障目标串行执行；runner `-p=1`，没有并发重用同一真实目标。辅助证据 `.ai/evidence/d-protocol-integration/`：

- `race.log`：`go test -race -count=1 -timeout=180s ./internal/agent/supervisor ./internal/agent/xray -run 'TestIntegration|TestLifecycle|TestReview|TestRevocationStopsOldCore|TestRollbackIsValidatedBefore'` 通过（26.616s / 7.401s）。
- `vet.log`：`go vet ./internal/agent/... ./cmd/agent ./internal/panel/protocol ./internal/panel/clientconfig ./internal/panel/xrayconf` 通过；Agent build 通过，临时输出二进制已删除；diff check 通过。
- `accounting-dispute.jsonl`：单独 opt-in 争议复现，**预期失败且实际失败**，不计入上表。

## 独立保留的计量风险

`go test -json -count=1 -tags=measurement_candidate ./internal/agent/state -run 'TestReviewOutOfOrderFinalAndPeriodicSamplesOverbill|TestMeasureComputesDeltasAndRebasesCounterReset'`：候选断言 `total=350 want=200` 失败，稳定 reset 断言通过。本轮未改任何 state 生产计量算法或删除 reset 断言。采样 gate 防止正常周期/最终读取和提交乱序，但不能判断外部同 generation 的计数下降究竟是真 reset 还是旧观察。该策略决策仍未解决；绝不能将统一通过理解为候选 200 策略已采用。最终采样超时仍可能丢失尾流量，D 的累计错误/降级指标保留。

## 隔离与清理

唯一真实核心：`/home/compile/Work/bin/xray`，SHA256 `8255dd939c34cf966cc91517b6324dd3c8d0bcf49ffac8beca049a38c46845ed`。全部自有 TempDir、动态 loopback 端口、manager 所有权 PID/exit 事件；新增测试使用共享严格 cleanup 断言。统一运行自有 TMPDIR `/tmp/d-protocol-integration-*` residual 为空并成功 rmdir。`cores-before.txt` 与 `cores-after.txt` 完全相同，`cores-diff.txt` 为空；未停止既有核心。详细真实新测试见统一报告 `2-tests.jsonl`，每个子例记录 generation、usage 和 cleanup 确认。

## 具体未完路径 / 不作扩大结论

- REALITY：本轮重新执行了原来的独立 `Sanitize -> Build -> Xray client` loopback payload 握手；**未新增 REALITY -> Agent Apply -> generation usage -> 撤权回执的一体化场景**。下一测试应在本文件 TLS 连续链路旁构建自有 HTTP2 camouflage 与 Render Xray client，并仍由 Supervisor 管理服务端，而非把既有独立握手当作已覆盖 Agent。
- 没有扩展实际 sing-box/Mihomo、gRPC 网络握手、issue-only CA 动态签发、多证书 SNI/单 pin 正确性矩阵；没有 panel/PG/API 或真实部署验证。这些不是本轮通过的接口范围。
- 未做断电级持久性证明；SQLite 故障注入/重开与 D crash/recovery 测试通过不等同于电源故障验证。
