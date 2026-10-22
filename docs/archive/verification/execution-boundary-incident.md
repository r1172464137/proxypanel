# 执行边界事件：脚本自测误调用 WSL 启动器

## 结论

本轮批准目标明确禁止启动 WSL。DS 子任务的一次本地脚本自测通过 PATH 间接调用了 `C:\Windows\System32\bash.exe`，其输出表明 WSL 的 `/bin/bash` 实际运行后因传入 Windows 路径不存在而退出。这是一次违反执行边界的事件；**不得在最终报告中宣称本轮从未启动 WSL或所有执行边界均符合**。

父 Agent 在收到具体操作记录后已向用户说明。此事件不能被后续测试通过抵消。

## 已有证据（不重放）

来源：DS 会话 `58ab7eac-f37f-4d99-b844-d2432f1fafcd` 对其前序操作记录的确认；完成时间 `2026-10-21T05:10:29.031Z`。父未重新执行该命令或启动器。

当时子任务执行的命令为：

```powershell
cd E:\Work\proxypanel
. .\scripts\Use-DevEnvironment.ps1
Remove-Item Env:PANEL_BASH -ErrorAction SilentlyContinue
"bash on PATH: " + ((Get-Command bash -ErrorAction SilentlyContinue).Source)
go test ./tests/e2e -run 'TestLifecycleScriptStub' -count=1 -v
```

`Get-Command` 本身仅查询路径。真正的执行发生在当时的测试程序中：`exec.LookPath("bash")` 找到系统启动器，随后执行它。

已有输出：

```text
bash on PATH: C:\Windows\system32\bash.exe
--- FAIL: TestLifecycleScriptStub
the lifecycle stub regression failed (exit status 127):
/bin/bash: E:/Work/proxypanel/deploy/docker-lifecycle-selftest.sh: No such file or directory
```

这证明 bash 运行过；不能仅因脚本没有成功打开就声称 WSL 未启动。

## 已实施纠正

- `tests/e2e/docker_lifecycle_test.go` 的 shell 选择逻辑已拒绝 PATH 中的 Windows System32 启动器；缺少允许的 POSIX shell 时明确跳过，而非回落到 WSL。
- 后续本轮 shell 自测和相关 Go 测试显式使用 `PANEL_BASH=E:\SDK\Git\bin\bash.exe`；调用 shell 也只用该绝对路径。
- 已向后续 DS 任务重申：禁止调用任何 WSL 命令、系统 bash 启动器或为调查而再次启动；不能依赖 PATH 自动选择 shell。
- 没有为掩盖事件擅自执行 `wsl --shutdown`、发行版关闭、删除或其他补偿性环境修改。

## 影响范围与验收表述

观察到的是一次 shell 启动及路径错误退出。没有追加 WSL 探测，因此未独立核实发行版名称、当前运行状态或所有后台副作用；**不声称“环境完全未变”**。

远端原有容器是否保持 ID/StartedAt/RestartCount 仍需以独立的开始/结束状态对照证明；该远端对照不能证明本机 WSL 边界未被违反。

最终十二项验收须引用本事件，区分功能测试结果和执行边界不符合项。
