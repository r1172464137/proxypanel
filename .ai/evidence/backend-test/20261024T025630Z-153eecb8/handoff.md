# Remaining ordinary backend coverage — 3b86336

## Scope and execution

- Base: `3b86336`; isolated worktree `pi-web-agent-34b8aaf2-7c62-4cbf-a9d6-767e7d95e8f4`.
- Only code/config change: append 11 `remaining/...` selections to `scripts/backend-test-manifest.json`. Each package is independent; every run expression is anchored to the inventoried remaining names. No runner, production, fixture, assertion, store, readiness or web changes.
- Command: source `/home/compile/Work/env.sh` and `/home/compile/Work/.devdb-product-readiness/test.env`, then `uv run --no-project python scripts/backend-test.py remaining/`.
- Unified evidence: `result.json`, per-selection discovery logs and raw Go JSONL in this directory. Input fingerprint stable. Discovery succeeded for all 145 expected top-level tests; 140 PASS, 5 FAIL, 0 SKIP, 0 missing. Counts exclude subtests. This is not a repository-wide pass.

| Selection | Top-level PASS | FAIL | SKIP |
|---|---:|---:|---:|
| cmd-panel | 6 | 0 | 0 |
| agent-config | 6 | 0 | 0 |
| agent-control | 10 | 0 | 0 |
| agent-telemetry | 1 | 0 | 0 |
| panel-access | 12 | 0 | 0 |
| panel-usagecalc | 12 | 0 | 0 |
| panel-protocol | 22 | 0 | 0 |
| panel-api | 25 | 5 | 0 |
| panel-maintenance | 21 | 0 | 0 |
| panel-clientconfig-render | 24 | 0 | 0 |
| agent-state-unix-permissions | 1 | 0 | 0 |

## PostgreSQL isolation

Before execution inspected `internal/panel/api/integration_test.go:newEnv/testDSN`, `internal/panel/maintenance/maintenance_integration_test.go:openTestStore/testDSN`, and `internal/panel/store/store.go:openDatabase`. Helpers restrict the injected test DSN to `127.0.0.1:15432/proxypanel`, create cryptographically random owned `pptest_*` / `ppmt_*` schemas, pass `SearchPath: schema`, and drop only that schema in cleanup. Store sets connection `search_path` to that schema alone (no public fallback). Environment sourced from the designated isolated cluster; no credentials printed. No public/lab schema operations were issued by this task. Preliminary concern about missing migrations was not borne out by execution: all PG cases reached their assertions, and maintenance passed; no migration/helper fix was attempted.

## API failures retained, no safety weakening

Raw failure details: `7-tests.jsonl`.

1. `TestAcceptanceSwitchSubscriptionKeepsTokenUsageAndCycle`, `acceptance_test.go:970`: HTTP 422 `LEDGER_UNATTRIBUTABLE` / complete sample interval cannot be proven from recorded history. Fixture creates authorization now but submits a sample starting two minutes earlier (`:963`). No historical authorization was invented to make it pass.
2. `TestAcceptanceSubscriptionRuleEditKeepsRunningCycle`, `acceptance_test.go:1087`: same 422; sample starts two minutes before freshly created authorization (`:1082`).
3. `TestAcceptanceBoundarySplitAndQuotaChange`, `acceptance_test.go:1209`: same 422; historical boundary-straddling sample predates freshly created authorization (`:1204`). Reached real PG/HTTP; did not hit its calendar skip.
4. `TestPanelFlow`: subtest `batch_preview_and_submit`, `integration_test.go:380`: HTTP 409 `IDEMPOTENCY_KEY_REUSED`. Initial submit includes `rules_version` and `member_set_digest` (`:357-360`); claimed replay omits both (`:378-380`), changing request content. Subtest `agent_registration_and_usage`, `:457`: HTTP 422 `LEDGER_UNATTRIBUTABLE`. Preserve both independent failures; not classified as a missing-PG environment failure.
5. `TestRecoveryModeBlocksMemberSelfServiceWrites`, `write_guard_test.go:104`: member DELETE `/api/v1/me/sessions/some-id` returned HTTP 404 `NOT_FOUND` instead of required 409 `RECOVERY_MODE`. Fixture deliberately references a nonexistent session (`:96`), so this establishes the error-order mismatch, not proof an existing session was deleted. Needs owner review of recovery guard semantics/fixture; no production or security assertion change made.

These failures are reported for coordination with existing PG/API fixture owners. This task does not touch their files and does not rerun green selections.

## Blocked / excluded, not PASS and not default SKIP

- `TestLocalCoreClientMatrix`, `TestLocalCoreCredentialGuards`: source requires existing `corematrix` build tag and pinned executable Xray 26.3.27, sing-box 1.14.3, Mihomo 1.19.32. Sourced environment has executable `XRAY_BIN=/home/compile/Work/bin/xray`, but no `SING_BOX_BIN` or `MIHOMO_BIN`; versions were not launched/verified. Blocked pending capabilities and core/snapshot coordination. Not registered as runnable or executed without the tag. No client matrix launched.
- `TestReviewReservedTags`: mixed structural checks and nested real sing-box checks. Kept out of the ordinary render selection rather than accepting nested default skips; real portion blocked by absent SING_BOX_BIN. The top-level case remains unexecuted by this task, including its structural subtests.
- `TestStateDirACLIsRestricted`: Windows-only; Windows/amd64 cross-compile succeeded with `go test -c`, via uv Python invocation. See `windows-acl-compile.json` (command, platform, binary SHA-256) and compile log. Binary was not executed; no Linux ACL PASS claim. Unix permissions test is a separate genuine PASS.
- `TestReviewOutOfOrderFinalAndPeriodicSamplesOverbill`: `measurement_candidate`-tagged state candidate outside this ordinary selection, remains unexecuted here.
- Supervisor real 9 already green in `20261024T025047Z-65b325b4`; not repeated. DataPlane, Docker, real Xray/client matrices, e2e and frontend/404 work not launched. No tagged core request was needed for these ordinary selections; any future core-tagged execution must wait for coordination.

## Assets

Complete evidence directory is copied verbatim to main worktree `.ai/evidence/backend-test/20261024T025630Z-153eecb8/`, including input fingerprints and failures. `coverage-summary.json` provides top-level accounting and independent blockers. Only handoff/summary and manifest need to be cherry-picked; raw logs are preserved on disk in main evidence. No push or delegated agents.
