# Real data-plane acceptance (VLESS/TCP)

```bash
uv run tests/e2e/run_data_plane.py
```

No Python dependencies. Defaults use the installed Go SDK, PostgreSQL 17 tools,
and `/home/compile/Work/bin/xray`; override with `GO_BIN`, `PG_BIN`, `PG_LIB`,
`XRAY_BIN`. The runner compiles only the e2e package and executes exactly
`TestDataPlaneProductReadiness`. Missing prerequisites are `not_run`/nonzero,
never SKIP-as-PASS. See `data_plane_manifest.json` for scope.

Creates a new temporary loopback PostgreSQL cluster, a random owned database
and schema, real Panel handler, real Agent supervisor with its normal CLI runner,
and official Xray server/client processes. Bootstrap admin is seeded through
Store; server, node, group, subscription, member and registration code are created
through authenticated Panel APIs. No synthetic statistics, SQL usage inserts or
mock access results. Redis is not needed (process-local limiter).

The Agent control hop uses a temporary trusted TLS certificate through the reused
`newAckProxy` fixture. Only the first **committed accepted usage response** is
dropped. A read-only observer hashes the real batch body and compares its PG
entry count and byte total at acceptance and duplicate retry. Thus the fault uses
real core statistics, sampler, durable outbox, authenticated upload and PG ledger.
Member `/api/v1/me/status` used bytes must exactly equal that ledger.

Initial and restored subscription exports are used as actual client configs.
Only a loopback SOCKS inbound and forced proxy routing are added: production
exports intentionally bypass private destinations, which would otherwise make
an all-loopback transfer a false positive. Each transfer requires HTTP 200,
69632 bytes and SHA256 `0a35dc5d9dbaf9e569e5d5d01461379213e16155b46661b930081308d61a8a64`.

Suspend via real `set_active` batch preview/confirmation, wait for the existing
stream to terminate and require a new connection to fail within a shared 60s
budget (no fixed sleep60). The subscription must return 403. Restore via the same
API flow and require a fresh actual export to transfer the identical file.
**Old credentials must remain denied:** batch credential reconciliation permanently
revokes removed authorizations; restoring creates new credentials. This is not a
production fix and does not silently resurrect old credentials.

## Failure evidence and results

The first two executions intentionally retain their failed restore result in main
worktree evidence. They incorrectly expected permanently revoked credentials to
become usable again. Both already passed real transfer, accounting, ACK loss and
revocation. Diagnosis: `store.reconcileCredentials` permanently revokes removed
credentials and creates replacements when access returns. The final scenario
explicitly tests old-credential rejection plus newly exported client restoration.
No production code was changed. Do not combine partial results across different
source fingerprints; each receipt fingerprints its own source, executable,
migrations, runner and fixture assets.

Evidence defaults to main worktree
`.ai/evidence/data-plane/product-readiness/<timestamp>-<random>/`, directories
0700 and files 0600. `receipt.json` records source/binary SHA256s, versions,
owned PG PID, cleanup and before/after existing core process identities. Actual
subscription bodies, credentials, bearer tokens and passwords are never saved.
The runner cleans its private cluster/directory in finally. Tests stop only their
own supervisor/client children and DROP only their random schema. On a Go timeout,
the runner additionally uses pidfds to terminate only Xray processes whose config
path is inside its unique temporary directory.

This is loopback **DEV Panel** evidence, not public HTTPS deployment, browser,
203 deployment, agent-lab or full 54-client-matrix acceptance. No shared service,
existing core, deployment or main source is modified; only evidence is copied to
main. The Xray binary is user-authorized and version/hash recorded; this runner
does not download it or independently attest an upstream release signature.
