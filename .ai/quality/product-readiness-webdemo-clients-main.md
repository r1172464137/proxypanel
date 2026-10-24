# web-demo E：统一候选客户端/Vision验证

主分支合入E修正后实际运行`uv run tests/e2e/run_webdemo_clients.py`，权威收据位于`.ai/evidence/web-demo/E/main-20261024T115431Z-4d728d/receipt.json`。

结果status=pass：Vision 3个精确client cell、主矩阵21格、辅助credential guard 4格，problems均空；raw Go JSON/exit按expected test/cell精确集合判定，不以任一PASS或25混21。官方Xray26.3.27、sing-box1.14.3、Mihomo1.19.33保持pin；完整69,632B/SHA、真实stats、停止具体拒绝和恢复对照。runner自身取得canonical运行锁并在cleanup后释放，私有tempdir删除。

边界不变：这是官方core级真实配置/流量，不是Windows GUI、手机或公网REALITY实机声明；历史“54格”无可核对定义，不使用。正式web-demo整体验收仍待A/B/D与完整故事。
