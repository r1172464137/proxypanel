# D: Agent execution lifecycle handoff

## Scope and dependencies

- Baseline: `7c5aa67`. State transaction dependency `0ee3983` adopted as `f728779` (manifest conflict resolved by retaining baseline entries and appending only its own selection).
- Candidate inspected: `/tmp/proxypanel-agent-fix-3435f1d-ZbU95Q`; supervisor/xray lifecycle portions adopted, then corrected against the verified state API. Candidate state transaction and counter-reset semantic changes were **not** copied over the verified state implementation.
- No panel, HTTP contracts, deployments, PostgreSQL, or live lab state modifications.

## Execution invariants

1. Save exact apply intent before reclaiming/stopping/starting any core. Commit complete instance set, report, applied authorization, generation history and watermark in `CommitSnapshot`, never `SetApplied`. Failed transactions retain intent; startup/health reconcile it before old records. Local expiry is also forbidden from rebuilding old records behind an unresolved intent.
2. Same-revision reoffers preserve the fenced `IssuedAt` (panel issue time is transport metadata); changed authorization/config at the same revision is still rejected by state.
3. Phase-zero concurrent authorization tightening/removals precede **all** technical validation. Technical-only replacement still validates before stopping a healthy core. The old rollback ordering assertion remains for technical-only replacement; independent `TestRevocationStopsOldCoreBeforeRollbackValidation` asserts the opposite ordering specifically for revocation.
4. Local expiry waits at most the configured apply-gate budget (default 5s), then concurrently stops exactly the immutable published expired processes without acquiring their technical lifecycle locks. Final sampling has a bounded gate wait. Last-moment checks before start and after readiness prevent an in-flight expired target (including rollback) from resurfacing. CAS record narrowing cannot overwrite a newly committed extension.
5. Stop success requires observed exit or verified OS absence. Survivors retain tracking/pid recovery records and block replacement. Shutdown errors reach `cmd/agent`'s nonzero exit, including errors from deferred `Close`.
6. Final-accounting failures are cumulative/visible (`final_sampling_errors`, `health_degraded`) and do not consume queue rows. `stop_unconfirmed` counts failed **live** processes, not confirmed crashes. Health writes/capacity checks gate restarts. Crash recovery has per-target backoff, five-attempt cap and a five-minute stability reset; it requires no new revision and filters expired users.
7. Offline evidence remains immutable/query-only. It recognizes and validates `apply_intent`, `applied_snapshot`, and the write probe; only counts/capability are exported, never configs/authorized-user lists/tokens. Schema v1 without new durable keys still reports intent support as unknown/false, not execution-complete.

## Reproducible acceptance

Use the installed Linux Go toolchain and fixed official Xray:

```sh
export PATH=/home/compile/sdk/go1.27.3/go/bin:$PATH
GOFLAGS=-tags=xrayreal XRAY_BIN=/home/compile/Work/bin/xray \
  uv run scripts/backend-test.py \
  agent-state/full agent-supervisor/full agent-xray/full \
  agent-reporting/full agent-supervisor/real-lifecycle

go test -race -count=1 -timeout=180s \
  ./internal/agent/supervisor ./internal/agent/xray \
  -run 'TestLifecycle|TestReview|TestRevocationStopsOldCore|TestRollbackIsValidatedBefore'

go vet ./internal/agent/state ./internal/agent/supervisor \
  ./internal/agent/xray ./internal/agent/reporting ./cmd/agent
```

The existing test runner does not inject build tags: `GOFLAGS=-tags=xrayreal` is required for discovery **and** execution of the real selection. Missing Xray or skipped tests must not be called PASS.

Official binary SHA256: `8255dd939c34cf966cc91517b6324dd3c8d0bcf49ffac8beca049a38c46845ed`.

## Independent accounting-policy dispute (not silently decided)

`measurement_candidate_test.go` is an explicitly opt-in, independent reproduction of the candidate's disputed same-generation high-water policy:

```sh
go test -count=1 -tags=measurement_candidate ./internal/agent/state \
  -run 'TestReviewOutOfOrderFinalAndPeriodicSamplesOverbill|TestMeasureComputesDeltasAndRebasesCounterReset'
```

Observed: existing reset assertion passes; candidate assertion fails with `total=350 want=200` for cumulative counters `100 -> 200 -> 150`. Baseline resets/bookkeeping semantics and their assertions are unchanged. Supervisor sampling/final-sampling read-and-commit are serialized so the normal execution chain cannot reorder those reads. Whether an externally decreased same-generation counter is a genuine reset or stale observation remains a **main-arbiter policy decision**, not a hidden production change or deleted assertion.

## Cleanup and isolation

Real tests use `t.TempDir`, OS-allocated dynamic loopback ports, manager-owned pid records, and precise single-PID SIGKILL for the crash case. Crash detection now waits for the actual published process exit event rather than guessing a fixed sleep. Shared real-test cleanup asserts `Close` success, every owned exit signal, and zero remaining owned pid records before removing its directory. No broad kill commands or business-core/lab operations are used. Existing live lab/client PIDs were only listed for observation, never signalled.

## A integration: exact offline artifact meaning

No new fields or HTTP version is needed. Existing `format_version` remains `proxypanel.agent-state-evidence/v1`; `state_schema_version` remains `"1"`.

| Recognized durable evidence | `apply_intent_tracking_supported` | `pending_apply_intent_count` | Consumer meaning |
|---|---:|---:|---|
| Legacy schema1, neither new nonempty key | false | 0 | **unknown**, not zero unfinished execution; do not turn old-Agent ready into permission for 0005 import |
| Valid nonempty `apply_intent` | true | 1 | Pending operation, must block execution-complete interpretation |
| Valid `applied_snapshot` matching generation/revision watermark, no intent | true | 0 | New atomic commit consumed the intent; **not** proof that Agent/core stopped, queue settled, or old execution history is complete |
| Both valid keys | true | 1 | The pending intent takes precedence over the older committed authorization |

Unknown keys/layout, malformed metadata, or a committed snapshot/watermark mismatch reject export rather than silently claim emptiness. Empty/missing keys do not establish capability. Existing `pending_batches` is a whole immutable usage-queue export; `pending_report_count` counts outbox reports and is not an execution-complete proof. A must still require its independent stop/ownership/history/queue prerequisites; these fields alone must never unlock migration/import.

Real tests: `TestUpgradeEvidencePreservesInputAndExportsWholeQueue` preserves the legacy unsupported/unknown assertion. `TestLifecycleExporterRecognizesIntentWithoutSecrets` exercises schema1 with pending intent, byte-for-byte read-only inspection and forbidden secret/config/users output, then atomically commits and verifies supported=true/count=0/revision=2 with the new applied-snapshot key.

## Final evidence

- Stable-input unified run: `.ai/evidence/backend-test/20261024T010903Z-fe391fff/result.json`, **passed**, `inputs_stable=true`.
- State: 21 test events; supervisor: 65; xray: 124; reporting: 19; real lifecycle: 8. Total **237**, **0 skipped**, all selections passed (includes subtests).
- Race: `/tmp/d-acceptance-race.log`: supervisor and xray selected lifecycle/security suites passed (26.644s / 7.359s).
- `go vet` passed for state/supervisor/xray/reporting/cmd/agent; Agent build passed; `git diff --check` clean.
- Official-core established-session revocation: **50ms**; local expiry termination: **1.036s** (unchanged **60s** assertions). Final expiry accounting retained **1092 upload bytes** in one pending batch. Concurrent-loop shutdown **15ms**.
- Real crash/no-new-revision recovery, overload protection and all owned-core cleanup assertions passed. Final process observation found no test-owned `/tmp/TestReal*` or `/tmp/TestLifecycle*` core configs still running. Existing lab/client cores were not signalled.
- Independent disputed accounting reproduction: `/tmp/d-measurement-policy-final.log`, deliberately **failed** candidate assertion (`350 != 200`) while the baseline reset assertion passed. This is the only undecided policy item; it is not included in the accepted production-semantic suite.

Earlier iterations are intentionally retained in ignored `.ai/evidence/backend-test/`, including failed/stale-input runs. They are not final acceptance. Results are local evidence (not deployment/production verification).
