# 三平台可复现发布汇总 (local, 2 rounds)

Generated: 10/21/2026 13:59:53

## 命令（每轮）
- `scripts/Build-Release.ps1   (rounds 1 and 2, default targets windows-amd64/linux-amd64/linux-arm64)`
- `scripts/Test-Release.ps1    (rounds 1 and 2)`
- `E:\SDK\Git\usr\bin\sha256sum.exe --check --strict SHA256SUMS   (per platform, rounds 1 and 2)`

## 工具版本
- sha256sum: sha256sum (GNU coreutils) 8.32 (E:\SDK\Git\usr\bin\sha256sum.exe)
- go version go1.27.3 windows/amd64; node v24.22.0; npm 11.19.1

## 当前最终产物

| platform | output path | files | manifest entries | manifest SHA256 | panel SHA256 | agent SHA256 |
|---|---|---|---|---|---|---|
| windows-amd64 | `E:\Work\proxypanel\build\windows-amd64` | 72 | 71 | `4c317d0a60f9d01c1c5449bc4dd89d06974f5b237ae7d58db776f25eda1afd8b` | `8a49359854d38add8b6f50745fe16e3d9fc12a2d8dc541fb3f11a09999175fec` | `d78748f355c5f14d5560a1339dd9edf941635e0789d8a6eecf2ccdfd7812328a` |
| linux-amd64 | `E:\Work\proxypanel\build\linux-amd64` | 72 | 71 | `a611e9276bb7b942ad28797fb2eb7c5f742985e56bccd4551d427383d5e4c762` | `5f0b3ced051735072f40eeb80e2d627b8e27df57766fbb4052a55ecb52cc8ef6` | `fd5e41f7ea48044ef0e08d5ffe83b137098ab2684d62528272c83c632f35e500` |
| linux-arm64 | `E:\Work\proxypanel\build\linux-arm64` | 72 | 71 | `9efff679b8afb2db9fa8b1270a7243018af1c9a93947bf58a47bc29688dbf0a6` | `e649d7df6021c6cb7a0156ce59c06bf185430c01e1cd01797eb3f3ece17246fd` | `82dd963ef54a40d34f36c2da9d796bcd285b4a42a51912b587b73ae0fa1ab8bc` |

总计: 3 平台 x 72 文件 = 216 文件; 清单条目 71/平台 共 213。

Windows agent -version: `proxypanel-agent 0.1.1`

## 两轮一致性
- 方法: byte-for-byte comparison of each SHA256SUMS against the round-1 baseline copy
- windows-amd64 identical: True; linux-amd64 identical: True; linux-arm64 identical: True
- 两轮完全一致: **True**

## 限制
- linux-amd64 and linux-arm64 are cross-built from Windows and were not executed on those platforms; only archives/manifest integrity, source-asset equality and binary architecture were verified.

## 日志
- `release-current-build-1.log`
- `release-current-verify-1.log`
- `release-current-sha256-windows-amd64-1.log`
- `release-current-sha256-linux-amd64-1.log`
- `release-current-sha256-linux-arm64-1.log`
- `release-current-build-2.log`
- `release-current-verify-2.log`
- `release-current-sha256-windows-amd64-2.log`
- `release-current-sha256-linux-amd64-2.log`
- `release-current-sha256-linux-arm64-2.log`
- `release-current-compare-rounds.log`
- `release-current-meta-round1.json`
- `release-current-summary.json` / `release-current-summary.md`

