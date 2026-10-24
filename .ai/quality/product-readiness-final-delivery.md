# product-readiness 最终交付

## 结论

`product-readiness` 已完成 backend、frontend、web-demo 三个阶段并经独立验收放行：

- backend R3：`ACCEPTED_WITH_IMPROVEMENTS`，启动/恢复材料门禁liveness修复、store/API回归与真实pg_dump/restore链通过。
- frontend R1：`ACCEPTED_WITH_IMPROVEMENTS`，302/302、typecheck/build通过，匿名根入口按角色落点。
- web-demo final R1：`ACCEPTED_WITH_IMPROVEMENTS`，上一轮4个P1关闭，无P0/P1。

正式报告：
- `.ai/quality/backend-accept-product-readiness-backend-r3-20261024-122207-3fedab77.md`
- `.ai/quality/frontend-accept-product-readiness-frontend-r1-20261024-100650-bf96c728.md`
- `.ai/quality/web-demo-accept-product-readiness-final-r1-20261024-210146.md`

## 决定性实证

- A：18个故事全部必要分支 + 5个链路场景，23/23 PASS、0 skip/fail；真实UI→Agent→官方Xray→69,632B SHA→PG账本→暂停/恢复；`.ai/evidence/web-demo/A/full-contract-pass-20261024T123512Z-91d928/`。
- B：双Agent、真SOCKS5 UDP、离线到期/撤权、部分完成、旧回执、崩溃回滚，pass；`.ai/evidence/web-demo/B/main-20261024T121009Z-133312/`。
- C：真实pg_dump/pg_restore、恢复门禁、队列/账本、代际接受、旧授权不复活、新合法流量，20/20；`.ai/evidence/web-demo/C/r3-final-green-20261024T115143Z-b7506f/`。
- D：已知历史commit构建旧Agent、旧SQLite、新Agent、Xray 26.3.23↔26.3.27、软件回退；5个真实core身份+5次实传，pass；`.ai/evidence/web-demo/D/main-final-pass-20261024T121918Z-b2af77/`。
- E：Vision 3格、主矩阵21格、辅助守卫4格，pass；`.ai/evidence/web-demo/E/main-20261024T115431Z-4d728d/`。
- F：真实host nginx两托管/API-only/严格私CA浏览器链，真实Agent用户级unit重启；对应F main evidence。

## 203最终预览

- URL：<https://192.168.5.203:8445>
- 部署应用revision：`e68f04811b12d946b5bacb36cadc2068076f4ba3`；其后到交付HEAD的生产路径diff为空。
- 镜像：`sha256:baad83f2f23bf0fed369106359cf97aa478ddb3847421174edd43cd18c2c160d`
- panel binary SHA256：`513bf8a8a2bf16b4f01b85d8c3059616a4bb2dac96aee0fb3ced189473056735`
- `/healthz`、`/readyz`、深链均200，PG/Redis ok；浏览器真实登录、刷新、Secure HttpOnly session、登出、非HTML 404/403均通过。
- 只重建独立preview panel；preview PG/Redis/gateway身份不变，原8443三容器身份不变，6条迁移保持，旧镜像与compose备份保留可回退。
- 权威部署结果：`.ai/evidence/final-deploy-8445/result.json`；浏览器：`.ai/evidence/final-deploy-8445/browser-20261024T130920Z/report.json`。

## 已知改进项／非承诺边界

验收剩余均为P2证据可维护性或跨证据覆盖：D receipt dirty标签、A部分异常分支/预览/状态三态的证据组织、C占位文件、E lease字段命名、A缺runner receipt等；不阻断当前交付。

未声明为实测支持：公有CA/DNS、Windows/mobile GUI、读屏器、完整system-level Agent unit安装、生产WAL/PITR/离机备份。内部预览使用现有私有证书范围；官方core级客户端能力与真实流量已按矩阵记录。外部AgentLab的Xray子进程自身持续换PID被证据如实标记，未归因于测试、也未冒充全宿主core身份稳定。
