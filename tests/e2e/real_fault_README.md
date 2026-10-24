# Owned physical-fault acceptance

Run from the repository (Go 1.27.3, PostgreSQL 17 tools, redis-server, Linux
user/mount namespaces required):

```sh
python3 tests/e2e/run_real_fault.py
# Optional: --evidence-dir /absolute/new/private/directory
```

Uses the installed tool defaults from `run_data_plane.py`; overrides: `GO_BIN`,
`PG_BIN`, `PG_LIB`, `REDIS_BIN`. No Docker, shared PG/Redis, production Agent,
existing fixture, root SSH or system service is required or changed.

The runner builds **one** test binary, fingerprints all Go/migration sources,
records binary/tool hashes, and executes exactly these manifest selections:

- `e2e/real-fault-postgres`: real API setup/registration/snapshot; real SQLite
  measurement/outbox/uploader/control client; physical stop of a fresh private
  initdb cluster. Positive upload first; failed upload must have zero ACKs and
  preserve the exact serialized batch/ID. Before resend after restart, SQL must
  show no usage rows/receipt for that batch and unchanged prior credit. Recovery
  must commit exactly the delta, empty the queue, and replay as duplicate without
  changing the ledger. **503 DB_UNAVAILABLE remains an independent assertion**;
  a 500 classification failure does not prevent collecting durability/recovery
  evidence, but still makes the test and runner FAIL.
- `e2e/real-fault-redis`: owned redis-server at a random loopback port, physically
  terminated/restarted. Actual current contract is **200 degraded readyz plus
  bounded local authentication admission**, not blanket 503: ten invalid-account
  attempts get 401 UNAUTHENTICATED, the eleventh 429 RATE_LIMITED. Healthy readyz
  and valid admin login are checked before/after the outage.
- `e2e/real-fault-enospc`: `unshare -Urnm`, new private 8 MiB tmpfs, real writes
  until syscall.ENOSPC, followed by SQLite FULL(13) during sampling. Existing
  queue/payload/ID, SQL baseline, sample_clock, seq and applied watermark must
  survive FULL, opening at capacity, and reopening after removing only the owned
  filler. Retried counter 250 must produce delta 150, not lose or double count.

Only the physical fault is under test here: counter inputs are deterministic
measurements passed to the production transactional state API. This does not
claim a second full real-core traffic, supervisor stop, client matrix, migration,
or deployment acceptance. API objects and ownership are real, not mocks.

The generic `scripts/backend-test.py` registers anchored exact test names and
requires the `owned-fault` capability. Without the owning fixture it reports
not_run, not skip/PASS. Use the dedicated runner to provision/control resources;
never manually set this capability against shared services. Manifest discovery:
`python3 scripts/backend-test.py --check-manifest --json e2e/real-fault`.

Evidence defaults to the main checkout's private
`.ai/evidence/backend-test/real-fault-r1/<unique-run>/` (0700 directories, 0600
files). `receipt.json` includes exact commands, per-scenario PASS/FAIL/blocked,
0-skip verification, source stability and original process identities. A missing
namespace is BLOCKED. Nonzero/timeout alone is never evidence of correct business
handling. Cleanup targets only the owned PG data directory, owned Redis child
(or matching private command directory after timeout), and the private mount.
No broad kill/prune, shared-filesystem fill, Redis flush or old-stack mutation.
