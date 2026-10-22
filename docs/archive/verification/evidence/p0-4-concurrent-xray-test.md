# P0-4 `xray -test` 偶发 2 分钟停顿，导致实例楔死在 `rollback_applied`

日期：2026-10-22（VM 时钟 UTC）
环境：VirtualBox `proxypanel-lab`（Ubuntu 24.04.5，2 vCPU / 4GB），官方 Xray 26.3.27
（`/opt/proxypanel/xray/26.3.27/xray`），Agent 0.1.1。

## 更正说明

本文件此前版本把根因写成“并发 `xray -test` 在 StatsService 上互相阻塞”，
依据是一组 5 并发测量得到 116–118 秒。**该结论已被干净复测否定**：

- 预热后 5 个不同实例配置并发校验 **wall_ms = 154**（A 组）、153（C 组重复），全部 `rc=0`；
- 去掉 `api`/`stats`/`policy` 的 5 并发是 121ms，与带 api 的 154ms 属同一量级，
  差异不足以解释两个数量级；
- 因此“并发”与“StatsService”都不是触发条件。

下面只保留实测事实。

## 实测事实

脚本：`$env:PI_SCRATCH_DIR\p04.sh` … `p04h.sh`，原始输出 `p04-measure.log`、
`p04b.log`、`p04c.log`、`p04d.log`、`p04e.log`、`p04f.log`、`p04g.log`、`p04h.log`。

### 1. 同一个配置的顺序单跑，存在随机 ~120 秒停顿

`p04g.log`，同一份 `/tmp/p04/c1.json`，连续 12 次，每次 20s 硬超时上限：

```
run1  rc=137 ms=119090
run2  rc=0   ms=39
run3  rc=0   ms=4499
run4  rc=0   ms=34
run5  rc=0   ms=37
run6  rc=0   ms=35
run7  rc=0   ms=35
run8  rc=137 ms=119921
run9  rc=0   ms=35
run10 rc=137 ms=117711
run11 rc=0   ms=34
run12 rc=0   ms=32
```

`p04h.log` 同样现象：`run1 rc=137 ms=116830`，其后 7 次均 31–63ms。

注意即使在“顺序、单进程、无并发”条件下也会出现，且停顿时长高度集中在
**117–120 秒**，期间 `vmstat` 的 `wa`/`st` 均为 0、`id` 92–100，即**不是 CPU 饥饿、
不是磁盘 iowait、不是虚拟化窃取**。

### 2. 停顿与“并发”无关

- 5 个真实实例配置并发：154ms（`p04d.log` A 组），重复 153ms（C 组）；
- 2 个并发成对跑 6 轮：`pair1 42ms / pair2 116904ms / pair3 262ms /
  pair4 124528ms / pair5 95174ms / pair6 120163ms`（`p04g.log`）；
- 单进程顺序跑也会命中同样的 ~120s（上表）。

即：**并发既不是必要条件也不是充分条件**，命中率看起来与时间点相关。

### 3. 不是配置内容、不是沙箱

- 同一配置在停顿发生时被 `timeout -s KILL 20` 杀掉，杀不掉真实耗时；
- 停顿之后立刻重跑，同样的文件 30–70ms 通过；
- 以 `proxypanel-agent` 用户直接跑（无 systemd 沙箱）31ms 通过（`p04e.log` 第 2 段）；
- 网络侧在本机是正常的：`tcp 10.0.2.3:53` 8ms、`8.8.8.8:53` 6ms、
  `getent hosts example.com` 12ms（`p04h.log`）——所以在整段停顿期间，
  宿主机/VirtualBox NAT 的 DNS 与出网都没有整体劣化。
- 未取到停顿进程的 socket/栈样本（进程在被采样前就已结束或采样未命中），
  因此**停顿发生在 Xray 进程内部的哪一步，目前尚未定位**。

## 对产品的影响（这一条是确定且严重的）

Agent 的 `DefaultValidationTimeout = 30s`（`internal/agent/xray/manager.go`）。
一旦某次 `xray -test` 命中上述停顿，超时即由 `exec.CommandContext` 杀掉进程，
`runner.Test` 返回 `xray -test failed: signal: killed`。

真实日志（`journalctl -u proxypanel-agent`）：

```
Oct 22 02:50:01 agent: instance nd_mkrmoq3yvlpnkbvws32hfzqmiu:
  applied last known good technical config with current authorization:
  config validation failed: xray -test failed: signal: killed
Oct 22 02:50:44 agent: restore of instance nd_mkrmoq3yvlpnkbvws32hfzqmiu failed:
  config validation failed: xray -test failed: signal: killed
```

由此产生的连锁后果，均已实测：

1. **回滚路径同样被打断**：回滚也要先校验“上一份可用配置”，于是“回到已知可用状态”
   这一步也失败，实例停在 `rollback_applied` 且 `applied_users = 0`——
   该节点上**没有任何被授权的用户**，代理不可用；
2. **不会自愈**：重启 Agent 后 5 个实例同时进入恢复校验，再命中停顿，仍然复现。
   实测重启后 120 秒状态仍为
   `[applied, rollback_applied, applied, applied, applied]`，此后保持不变；
3. **快照反复失败**：`snapshot revision 269 generation 1: failed (6 instance result(s))`
   连续出现 9 次，需要人工介入；
4. 提高校验预算**不是修复**：临时把
   `PROXYPANEL_XRAY_VALIDATION_TIMEOUT` 调到 240s 后重启，仍然是
   `signal: killed`（02:45:55 开始，02:50:01 被杀，正好撞满 240s 预算）。

## 结论与修复方向（待统一清单确认）

事实层面：这不是“并发导致”的问题，而是**校验步骤对单次耗时波动没有任何韧性**，
一次偶发停顿就把实例推进不可恢复状态。

因此修复应针对“失败处理策略”，而不是继续找 Xray 内部原因：

1. **校验超时不得判定实例失败**：超时属于“暂时无法判断”，应退避重试
   （例如 3 次，间隔 1s/2s，总预算独立于单次预算），只有确定性失败
   （非零退出且输出明确报配置错误，如 `rc=23 Configuration OK` 之外）
   才触发回滚；
2. **回滚不得依赖一次校验**：回滚到 last-known-good 时，若该校验超时，
   应保留原状态并重试，绝不把 `applied_users` 清成 0；
3. **实例失败必须是可恢复态**：`rollback_applied` 需要周期性重试入口，
   而不是要求人工重启 Agent；
4. 顺带：把 `rollback_applied` 与 `applied_users = 0` 的组合视为不变量违反，
   在状态写入处直接拒绝。

上述第 1、3 条与已实现的 `DefaultValidationTimeout = 30s` 属于同一处代码，
是本次测试要交付的核心修复项之一。
