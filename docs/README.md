# 文档索引

proxypanel 的文档按**你要做什么**组织，不按时间堆。

## 想先跑起来

| 文档 | 内容 |
|---|---|
| [快速开始](getting-started/quickstart.md) | 十分钟起一套，走到真实流量打通 |

## 部署

| 文档 | 内容 |
|---|---|
| [Docker 部署](deployment/docker.md) | 单栈 vs 分体、环境变量全表、卷、升级、备份 |
| [面板与 Agent 分开部署](deployment/split-panel-agent.md) | 面板一台、节点另一批机器 |
| [生产部署](deployment/production.md) | TLS、反向代理、账户权限、不可暴露的端口、回滚红线 |

## 运维

| 文档 | 内容 |
|---|---|
| [备份与恢复](operations/backup-recovery.md) | pg_dump、master key、恢复协调模式、代际确认 |
| [维护与排障](operations/maintenance.md) | 保留清理删什么/不删什么、健康检查、Agent 指标、常见故障 |
| [测试数据库](operations/test-databases.md) | 隔离测试环境的连接方式与凭据管理 |

## 理解设计

| 文档 | 内容 |
|---|---|
| [架构](design/architecture.md) | 为什么 Agent 拉而不是面板推、revision/generation、硬撤权、计量幂等 |
| [数据模型](design/data-model.md) | 31 张表各管什么、软删 vs 硬删、乐观并发、凭据加密 |
| [产品规格](design/product.md) | 角色、能力边界、业务规则 |
| [实现契约](design/implementation-contract.md) | 冻结的接口与行为约定 |

## 参考

| 文档 | 内容 |
|---|---|
| [HTTP API](reference/api.md) | 认证、错误码全表、完整路由表、两段式批量、curl 流程 |
| [订阅格式](reference/subscription-formats.md) | 四种输出、形状差异、`external_port` 语义、已验收范围 |

## 开发

| 文档 | 内容 |
|---|---|
| [开发环境](development/setup.md) | WSL2/Linux 依赖、日常检查、真实数据库测试、代码地图 |
| [测试](development/testing.md) | 四层测试怎么跑、已验证基线、写测试的约定 |

## 历史记录

`docs/archive/` 下是**不再维护**的材料：验收快照、交接记录、事故复盘、旧计划书、
被取代的 API 提案。保留它们只为回答「当初为什么这么定」，**不要照着写代码** ——
里面的字段名、路由、状态大多已经过期。

| 目录 | 内容 |
|---|---|
| `archive/verification/` | 各阶段验收报告、Docker 生命周期/恢复演练、客户端矩阵、执行边界事故 |
| `archive/plan/` | UI 重写计划、组件契约、文案规范 |
| `archive/old-*-windows.md` | 之前 Windows 环境下的部署/开发说明（已被 WSL2/Linux 版本取代） |
| `archive/specs-acceptance.md` | 验收规格（场景清单，判定已并入测试文档） |

## 文档约定

- 事实以代码为准。文档与代码冲突时改文档，并在报告里指出冲突点。
- 「已验证」和「未验证」要分开写。没跑过的东西不说成跑过。
- 命令可复制即用，路径都是仓库内的真实路径。
- 中文正文，技术名词保留英文。
