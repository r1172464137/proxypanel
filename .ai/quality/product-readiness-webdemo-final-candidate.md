# product-readiness web-demo final candidate

Application/test candidate: `fea31871586534927f6bee2cb32754587e141ba8` plus subsequent test-only D/API corrections; production bytes remain the DEC018 readiness fix + frontend root landing.

## Current authoritative runs

- A complete stories: `.ai/evidence/web-demo/A/full-pass-20261024T120815Z-f095c2/main-verification.json` — 23/23 PASS, all US-PR-001..018, 0 skip/fail, synthetic usage 0, 69,632B payload SHA, 7 protected processes intact, own leftovers 0.
- B dual Agent: `.ai/evidence/web-demo/B/main-20261024T121009Z-133312/receipt.json` — pass/exit0; real TCP+SOCKS5 UDP, offline expiry/revocation, partial tasks/stale receipt/crash rollback; canonical lease acquired+released, protected originals/listeners intact, owner cleanup complete.
- C backup/recovery: `.ai/evidence/web-demo/C/r3-final-green-20261024T115143Z-b7506f/` — runner/test pass, 20/20 assertions, real dump/restore/queue/ledger/generation/acceptance/new traffic. External AgentLab Xray child churn recorded; runner did not signal it and own resources cleaned.
- D cross-version: `.ai/evidence/web-demo/D/main-final-pass-20261024T121918Z-b2af77/` — status pass/exit0; known git-archive old Agent, new Agent, 5 managed-core process identities and 5 real positive probes across old/new/core rollback/software rollback; retained batch exact once, source closed, PG graceful, own cleanup complete. External AgentLab Xray child churn recorded separately, no listener loss.
- E clients: `.ai/evidence/web-demo/E/main-20261024T115431Z-4d728d/receipt.json` — pass; 3 Vision, 21 main matrix, 4 auxiliary guards, exact expected test/cell sets, lock/cleanup complete.
- F platform: real host nginx two ownership modes and strict trusted private CA `.ai/evidence/web-demo/F/host-main-20261024T101214Z-fe65bc/main-verification.json`; real Agent user-unit restart `.ai/evidence/web-demo/F/main-dd40fac-retry-20261024T093138Z-a8941e/main-verification.json`.
- backend R3 store/delivery `.ai/evidence/backend-test/20261024T113542Z-7676d17a/result.json`; API follow-up `.ai/evidence/backend-test/20261024T122047Z-8d27c817/result.json`.

## Honest residual boundaries

No claim for public CA/DNS, Windows/mobile GUI, screen-reader device, production WAL/PITR, or full system-level Agent unit install/hardening. These are environment/product-boundary not-run items, not hidden passes. Existing internal preview uses private CA/self-signed scope. External long-running AgentLab core is independently unstable and changes child PID; final evidence protects its parent Agent/shared PG and reports rather than attributes that churn.

Formal backend R3 re-accept and final web-demo accept remain required before feature completion; deploy 8445 only after these reports are consumed.
