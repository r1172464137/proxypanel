package supervisor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"proxypanel/internal/agent/state"
	"proxypanel/internal/agent/xray"
	"proxypanel/internal/contracts"
)

// C05: an engaged protection state must be a real start gate. A new revision
// that arrives while the overload/disk protection is active must never start a
// core: the old implementation only started and stopped asynchronously, which is
// visible here as a successful apply.
func TestReviewQueueProtectionIsStartGate(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	u := []contracts.AuthorizedUser{{MemberID: "m1", Email: "a@example.test"}}
	reviewApply(t, e, 1, u, "a@example.test")
	e.sup.setQueueOverCapacity(true)
	if !waitForStops(e.runner, 1, 5*time.Second) {
		t.Fatal("the protection did not stop the running instance")
	}
	startsBefore, _ := e.runner.counts()
	err := e.sup.applySnapshot(ctx, &contracts.Snapshot{
		SchemaVersion: contracts.SchemaVersion, ControlGeneration: "gen-review", Revision: 2,
		IssuedAt: e.now, ExpectedUserCount: 1, Instances: []contracts.Instance{nodeInstance(u, "a@example.test")},
	})
	if err == nil {
		t.Fatal("a snapshot applied while the upload queue is over capacity must be refused")
	}
	if starts, _ := e.runner.counts(); starts != startsBefore {
		t.Fatalf("protection bypassed: %d core start(s) after the protection engaged", starts-startsBefore)
	}
	if len(e.sup.mgr.Snapshot()) != 0 {
		t.Fatalf("a core is running while the queue protection is engaged: %+v", e.sup.mgr.Snapshot())
	}
	id, _ := e.store.Identity(ctx)
	if id.AppliedRevision != 1 {
		t.Fatalf("a refused snapshot advanced the watermark: %+v", id)
	}

	// Recovery must not require a new revision: clearing the condition has to let
	// health rebuild the committed desired state by itself.
	e.sup.setQueueOverCapacity(false)
	if err := e.sup.healthOnce(ctx); err != nil {
		t.Fatalf("health after recovery: %v", err)
	}
	if starts, _ := e.runner.counts(); starts != startsBefore+1 {
		t.Fatalf("health did not resume the committed instance: starts=%d", starts-startsBefore)
	}
}

// C05: a persistence failure that happens DURING an apply (final sample hook)
// must not be bypassed by the start that follows it in the same apply.
func TestReviewPersistenceFailureBlocksMidApplyStart(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	u := []contracts.AuthorizedUser{{MemberID: "m1", Email: "a@example.test"}}
	reviewApply(t, e, 1, u, "a@example.test")
	startsBefore, _ := e.runner.counts()

	// The final sample hook of the running instance fails to persist. That sets
	// the persistence protection while the apply still holds the gate.
	e.sup.sampleMu.lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.sup.setPersistenceError(errors.New("injected disk failure"))
	}()
	e.sup.sampleMu.unlock()
	<-done
	if err := e.sup.protectionError(); err == nil {
		t.Fatal("the injected persistence failure did not engage the protection")
	}

	newUser := contracts.AuthorizedUser{MemberID: "m2", Email: "b@example.test"}
	err := e.sup.applySnapshot(ctx, &contracts.Snapshot{
		SchemaVersion: contracts.SchemaVersion, ControlGeneration: "gen-review", Revision: 2,
		IssuedAt: e.now, ExpectedUserCount: 2,
		Instances: []contracts.Instance{nodeInstance(append(u, newUser), "a@example.test", "b@example.test")},
	})
	if err == nil {
		t.Fatal("a snapshot applied under an active persistence protection")
	}
	id, _ := e.store.Identity(ctx)
	if id.AppliedRevision != 1 {
		t.Fatalf("a refused snapshot advanced the watermark: %+v", id)
	}
	// The async protection goroutine may stop the previous core; what matters is
	// that the apply did not START a new one.
	if starts, _ := e.runner.counts(); starts != startsBefore {
		t.Fatalf("the start gate was bypassed: %d new start(s)", starts-startsBefore)
	}
}

// C06: a core that crashed after it became ready must be reported as failed and
// must be rebuilt by the health loop WITHOUT a new panel revision (the panel
// answers 204 forever).
func TestReviewCrashedCoreRecoversWithoutNewRevision(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	u := []contracts.AuthorizedUser{{MemberID: "m1", Email: "a@example.test"}}
	reviewApply(t, e, 1, u, "a@example.test")
	if starts, _ := e.runner.counts(); starts != 1 {
		t.Fatalf("starts=%d", starts)
	}
	inst := e.sup.mgr.Snapshot()
	if len(inst) != 1 || inst[0].State != xray.StateApplied {
		t.Fatalf("the instance is not applied before the crash: %+v", inst)
	}
	// Simulate a crash: the process exit channel closes without any stop call.
	e.runner.mu.Lock()
	e.runner.crashOnStart = true // the next started "core" is born dead
	e.runner.mu.Unlock()
	e.crashInstance(t, "node-1")

	if got := e.sup.mgr.Snapshot(); len(got) != 1 || got[0].State != xray.StateFailed {
		t.Fatalf("a crashed core must not be advertised as applied: %+v", got)
	}
	// First health pass: recovery attempt 1 starts a core that dies immediately.
	if err := e.sup.healthOnce(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}
	if got := e.sup.mgr.Snapshot(); len(got) != 1 || got[0].State != xray.StateFailed {
		t.Fatalf("the dead recovery core is not reported as failed: %+v", got)
	}
	// The panel keeps answering 204 (same generation, same revision): only the
	// health loop can heal this. Advance past the recovery backoff.
	e.runner.mu.Lock()
	e.runner.crashOnStart = false
	e.runner.mu.Unlock()
	e.advance(2 * time.Minute)
	if err := e.sup.healthOnce(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}
	// Every failed attempt can consume two core starts (the second attempt on a
	// fresh management API port), so the count is bounded, not exact.
	if starts, _ := e.runner.counts(); starts < 3 {
		t.Fatalf("health did not restart the crashed core (starts=%d)", starts)
	}
	if got := e.sup.mgr.Snapshot(); len(got) != 1 || got[0].State != xray.StateApplied {
		t.Fatalf("the recovered instance is not applied: %+v", got)
	}
	// The heartbeat reports the healed state.
	if err := e.sup.heartbeatOnce(ctx); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
}

// installFakeProcessTable points the manager's pid inspection at the stub pid
// range and reports those pids as alive. The identity deliberately has no
// recorded creation identity, so the ownership check refuses to kill anything:
// the seam only replaces the /proc lookup, never a real OS process.
func installFakeProcessTable(t *testing.T, e *testEnv) {
	t.Helper()
	e.sup.mgr.SetInspectProcess(func(pid int) (xray.ProcessIdentity, bool, error) {
		if pid >= 900000 {
			return xray.ProcessIdentity{PID: pid}, true, nil
		}
		return xray.ProcessIdentity{}, false, nil
	})
}

// crashInstance closes the exit channel of the running fake core, exactly like a
// real process that died (no Stop call, no pid removal).
func (e *testEnv) crashInstance(t *testing.T, id string) {
	t.Helper()
	e.runner.mu.Lock()
	defer e.runner.mu.Unlock()
	if e.runner.procs == nil {
		t.Fatal("the stub runner did not record any process")
	}
	proc, ok := e.runner.procs[id]
	if !ok {
		t.Fatalf("no process recorded for %s", id)
	}
	proc.once.Do(func() { close(proc.exited) })
}

// C06: the bounded recovery budget must stop an endless restart loop and must be
// reset by a stable core, not by the wall clock.
func TestReviewRecoveryBudgetIsBoundedAndResetsWhenStable(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	u := []contracts.AuthorizedUser{{MemberID: "m1", Email: "a@example.test"}}
	reviewApply(t, e, 1, u, "a@example.test")

	// Every start is born dead: the instance can never become healthy.
	e.runner.mu.Lock()
	e.runner.crashOnStart = true
	e.runner.mu.Unlock()
	e.crashInstance(t, "node-1")

	// One failed recovery attempt may consume several core starts (retry on a
	// fresh API port plus a rollback attempt), so the invariant is the recovery
	// ATTEMPT budget, not the raw start count.
	for i := 0; i < 40; i++ {
		// Advance past the exponential backoff of the recovery budget.
		e.advance(2 * time.Minute)
		if err := e.sup.healthOnce(ctx); err != nil {
			t.Fatalf("health: %v", err)
		}
		if f := e.sup.recovery["node-1"]; f != nil && f.count > maxRecoveryAttempts {
			t.Fatalf("health made %d recovery attempts, budget is %d", f.count, maxRecoveryAttempts)
		}
	}
	f := e.sup.recovery["node-1"]
	if f == nil || f.count != maxRecoveryAttempts {
		t.Fatalf("the recovery budget was never exhausted: %+v", f)
	}
	startsAfterBudget, _ := e.runner.counts()
	// The budget is exhausted: no further start may happen.
	e.advance(24 * time.Hour)
	if err := e.sup.healthOnce(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}
	if starts, _ := e.runner.counts(); starts != startsAfterBudget {
		t.Fatalf("the exhausted recovery budget restarted the core: %d -> %d starts", startsAfterBudget, starts)
	}

	// A stable core resets the budget: the next crash is recovered again.
	e.runner.mu.Lock()
	e.runner.crashOnStart = false
	e.runner.mu.Unlock()
	delete(e.sup.recovery, "node-1")
	if err := e.sup.healthOnce(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}
	if got := e.sup.mgr.Snapshot(); len(got) != 1 || got[0].State != xray.StateApplied {
		t.Fatalf("the instance did not recover once the core stopped crashing: %+v", got)
	}
	e.advance(6 * time.Minute) // the core is stable for the whole window
	if err := e.sup.healthOnce(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}
	e.crashInstance(t, "node-1")
	if err := e.sup.healthOnce(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}
	if got := e.sup.mgr.Snapshot(); len(got) != 1 || got[0].State != xray.StateApplied {
		t.Fatalf("a stable core did not reset the recovery budget: %+v", got)
	}
}

// C07 + C02: a rejected configuration must not report applied, and a failing
// commit must not leave "applied" in the runtime status either.
func TestReviewCommitFailureReportsFailedState(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	u := []contracts.AuthorizedUser{{MemberID: "m1", Email: "a@example.test"}}
	reviewApply(t, e, 1, u, "a@example.test")

	// Fail exactly the durable commit (the instance upsert), not an earlier read:
	// the apply has already started the new core when the transaction fails.
	db, err := sql.Open("sqlite", e.store.Path())
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER review_commit_fault BEFORE UPDATE ON instances
		BEGIN SELECT RAISE(ABORT,'injected commit failure'); END`); err != nil {
		t.Fatalf("install trigger: %v", err)
	}
	err = e.sup.applySnapshot(ctx, &contracts.Snapshot{
		SchemaVersion: contracts.SchemaVersion, ControlGeneration: "gen-review", Revision: 2,
		IssuedAt: e.now, ExpectedUserCount: 2,
		Instances: []contracts.Instance{nodeInstance([]contracts.AuthorizedUser{u[0], {MemberID: "m2", Email: "b@example.test"}}, "a@example.test", "b@example.test")},
	})
	if err == nil {
		t.Fatal("a snapshot whose durable commit failed was reported as applied")
	}
	if e.sup.lastApplyState == applyStateApplied {
		t.Fatal("the local apply state claims applied although the durable commit failed")
	}
	if e.sup.lastApplyState != applyStateFailed {
		t.Fatalf("the local apply state = %q, want %q", e.sup.lastApplyState, applyStateFailed)
	}
	// The durable records and the watermark still describe revision 1.
	if _, derr := db.Exec(`DROP TRIGGER review_commit_fault`); derr != nil {
		t.Fatalf("drop trigger: %v", derr)
	}
	id, ierr := e.store.Identity(ctx)
	if ierr != nil {
		t.Fatalf("identity: %v", ierr)
	}
	if id.AppliedRevision != 1 {
		t.Fatalf("the failed commit advanced the watermark: %+v", id)
	}
	records, rerr := e.store.Instances(ctx)
	if rerr != nil || len(records) != 1 || len(records[0].Users) != 1 {
		t.Fatalf("the failed commit rewrote the durable authorization: %+v err=%v", records, rerr)
	}
}

// C09: deleting a node whose core can not be proven stopped must keep the
// recovery record, must not claim the connections were terminated and must not
// leave the node without any cleanup path.
func TestReviewUnstoppableRemovedNodeKeepsTombstone(t *testing.T) {
	e := newTestEnv(t)
	installFakeProcessTable(t, e)
	ctx := context.Background()
	u := []contracts.AuthorizedUser{{MemberID: "m1", Email: "a@example.test"}}
	reviewApply(t, e, 1, u, "a@example.test")
	e.runner.setStopFailure(true)

	// An empty snapshot removes the node: the stop can not be confirmed. The
	// apply report must say failed (the panel contract carries the outcome, the
	// call itself reports only transport/commit errors).
	if err := e.sup.applySnapshot(ctx, &contracts.Snapshot{
		SchemaVersion: contracts.SchemaVersion, ControlGeneration: "gen-review", Revision: 2,
		IssuedAt: e.now, ExpectedUserCount: 0,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if e.sup.lastApplyState != applyStateFailed {
		t.Fatalf("an unconfirmed removal was reported as %q", e.sup.lastApplyState)
	}
	reports := e.panel.appliedReports()
	if len(reports) == 0 || reports[len(reports)-1].State != applyStateFailed {
		t.Fatalf("the panel was told the removal succeeded: %+v", reports)
	}
	for _, r := range reports {
		if r.Revision != 2 {
			continue
		}
		for _, res := range r.Results {
			if res.InstanceID == "node-1" && res.OldConnectionsTerminated {
				t.Fatal("an unconfirmed stop claimed the old connections were terminated")
			}
		}
	}
	records, rerr := e.store.Instances(ctx)
	if rerr != nil {
		t.Fatalf("records: %v", rerr)
	}
	if len(records) != 1 || records[0].State != xray.StateStopping {
		t.Fatalf("the removal did not keep a cleanup tombstone: %+v", records)
	}
	if len(records[0].Config) == 0 || len(records[0].Users) == 0 {
		t.Fatalf("the tombstone lost the recovery data (config/users): %+v", records[0])
	}
	id, _ := e.store.Identity(ctx)
	if id.AppliedRevision != 1 {
		t.Fatalf("the failed removal advanced the watermark: %+v", id)
	}
	// Health must keep trying and must NOT rebuild a proxy for the tombstone.
	if err := e.sup.healthOnce(ctx); err != nil {
		t.Logf("health while the core is unstoppable: %v", err)
	}
	starts, _ := e.runner.counts()
	if starts != 1 {
		t.Fatalf("health restarted a removed node: starts=%d", starts)
	}
	// Once the core can be stopped, health finishes the cleanup.
	e.runner.setStopFailure(false)
	if err := e.sup.healthOnce(ctx); err != nil {
		t.Fatalf("health after the core became stoppable: %v", err)
	}
	records, _ = e.store.Instances(ctx)
	if len(records) != 0 {
		t.Fatalf("the tombstone was not cleaned up after the confirmed stop: %+v", records)
	}
}

// C08: shutdown must keep the tail usage of every stopped core.
func TestReviewShutdownKeepsTailUsage(t *testing.T) {
	e := newTestEnv(t)
	e.panel.setSnapshot("gen-1", 1, []contracts.Instance{
		nodeInstance([]contracts.AuthorizedUser{{MemberID: "m1", Email: "a@example.test"}}, "a@example.test"),
	}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.sup.Run(ctx) }()
	waitAppliedRevision(t, e, 1, 20*time.Second)
	e.runner.mu.Lock()
	e.runner.statsAny = []xray.StatCounter{{Name: "user>>>a@example.test>>>traffic>>>uplink", Value: 123}}
	e.runner.mu.Unlock()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not stop")
	}
	depth, err := e.store.QueueDepth(context.Background())
	if err != nil {
		t.Fatalf("queue depth: %v", err)
	}
	if depth.Batches != 1 {
		t.Fatalf("the shutdown lost the tail sample: depth=%+v", depth)
	}
}

// C01 + C03: a delivered local expiry must not wait for an unrelated technical
// apply. The apply gate is held by a long snapshot; the expiry pass has its own
// deadline and stops the expired core directly.
func TestReviewExpiryDeadlineStopsWithoutApplyGate(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	expiry := e.now.Add(time.Minute)
	u := []contracts.AuthorizedUser{{MemberID: "m1", Email: "a@example.test", ExpiresAt: &expiry}}
	reviewApply(t, e, 1, u, "a@example.test")
	e.advance(2 * time.Minute)

	installFakeProcessTable(t, e)
	e.sup.SetExpiryLockBudget(50 * time.Millisecond)
	// Hold the gate exactly like a slow multi-instance apply would.
	e.sup.applyMu.lock()
	stopsBefore := stopCount(e.runner)
	errCh := make(chan error, 1)
	go func() { errCh <- e.sup.expiryOnce(ctx) }()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("the expiry pass reported success although it could not serialize")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the expiry pass waited for the apply gate instead of enforcing its deadline")
	}
	e.sup.applyMu.unlock()
	if stopCount(e.runner) != stopsBefore+1 {
		t.Fatalf("the expired core was not stopped at the deadline: %d -> %d", stopsBefore, stopCount(e.runner))
	}
	// The narrowed authorization must be durable, so a restart can not revive it.
	records, rerr := e.store.Instances(ctx)
	if rerr != nil || len(records) != 1 {
		t.Fatalf("records: %+v err=%v", records, rerr)
	}
	if len(records[0].Users) != 0 || !json.Valid(records[0].Config) {
		t.Fatalf("the expired user is still persisted: %+v", records[0])
	}
}

// C09 + C08: an unconfirmed stop during shutdown must neither be recorded as
// stopped nor swallowed. The run reports it, and the durable record keeps the
// recovery data so the next agent start can prove ownership and reclaim it.
func TestReviewShutdownReportsUnconfirmedStop(t *testing.T) {
	e := newTestEnv(t)
	installFakeProcessTable(t, e)
	ctx := context.Background()
	u := []contracts.AuthorizedUser{{MemberID: "m1", Email: "a@example.test"}}
	reviewApply(t, e, 1, u, "a@example.test")
	e.runner.setStopFailure(true)

	err := e.sup.Close()
	if err == nil {
		t.Fatal("an unconfirmed shutdown stop was reported as a clean shutdown")
	}
	if !strings.Contains(err.Error(), "did not stop cleanly") {
		t.Fatalf("the shutdown error does not name the surviving instance: %v", err)
	}
	// Close() also closes the store, so the durable state is read back through a
	// fresh handle: this is exactly what the next agent start does.
	reopened, oerr := state.Open(e.sup.cfg.StateDir)
	if oerr != nil {
		t.Fatalf("reopen state: %v", oerr)
	}
	defer reopened.Close()
	records, rerr := reopened.Instances(ctx)
	if rerr != nil {
		t.Fatalf("records: %v", rerr)
	}
	if len(records) != 1 {
		t.Fatalf("the recovery record of the surviving core was dropped: %+v", records)
	}
	rec := records[0]
	if rec.State == xray.StateStopped {
		t.Fatalf("a core that survived the stop is recorded as stopped: %+v", rec)
	}
	if len(rec.Config) == 0 || rec.CoreGeneration == "" {
		t.Fatalf("the recovery data of the surviving core was cleared: %+v", rec)
	}
}

// C09: a snapshot removal whose stop fails must not delete the only recovery
// record, and the tombstone must keep the exact configuration and authorization
// that the surviving core is running.
func TestReviewRemovalTombstoneKeepsConfig(t *testing.T) {
	e := newTestEnv(t)
	installFakeProcessTable(t, e)
	ctx := context.Background()
	u := []contracts.AuthorizedUser{{MemberID: "m1", Email: "a@example.test"}}
	reviewApply(t, e, 1, u, "a@example.test")
	before, err := e.store.Instances(ctx)
	if err != nil || len(before) != 1 {
		t.Fatalf("records: %+v err=%v", before, err)
	}
	e.runner.setStopFailure(true)
	if err := e.sup.applySnapshot(ctx, &contracts.Snapshot{
		SchemaVersion: contracts.SchemaVersion, ControlGeneration: "gen-review", Revision: 2,
		IssuedAt: e.now, ExpectedUserCount: 0,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	after, rerr := e.store.Instances(ctx)
	if rerr != nil || len(after) != 1 {
		t.Fatalf("records: %+v err=%v", after, rerr)
	}
	if after[0].ConfigHash != before[0].ConfigHash || after[0].CoreGeneration != before[0].CoreGeneration {
		t.Fatalf("the tombstone lost the identity of the surviving core:\n before=%+v\n after =%+v", before[0], after[0])
	}
	if len(after[0].Users) != 1 || after[0].Users[0].Email != "a@example.test" {
		t.Fatalf("the tombstone lost the authorization of the surviving core: %+v", after[0].Users)
	}
}

// C04 + C03: the final sample of a stop must not be able to delay the stop
// itself. A periodic sampling round that is stuck (slow statsquery) must not hold
// a revocation back; the sample is skipped and the accounting stays correct
// because Measure keeps a per generation high-water mark.
func TestReviewSlowSamplingDoesNotDelayRevocation(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	expiry := e.now.Add(time.Minute)
	u := []contracts.AuthorizedUser{{MemberID: "m1", Email: "a@example.test", ExpiresAt: &expiry}}
	reviewApply(t, e, 1, u, "a@example.test")
	e.advance(2 * time.Minute)

	// A periodic sampling round holds the sampling gate for longer than the final
	// sample budget: exactly the contention the budget protects against.
	e.sup.sampleMu.lock()
	release := make(chan struct{})
	go func() {
		<-release
		e.sup.sampleMu.unlock()
	}()
	defer close(release)

	began := time.Now()
	done := make(chan error, 1)
	go func() { done <- e.sup.expiryOnce(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expiryOnce: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the expiry rebuild waited for the sampling gate")
	}
	if elapsed := time.Since(began); elapsed > 15*time.Second {
		t.Fatalf("the expiry rebuild took %s while the sampling gate was busy", elapsed)
	}
	if stopCount(e.runner) == 0 {
		t.Fatal("the expired core was not stopped")
	}
	if cfg := e.runner.lastStartedFor("node-1"); cfg != "" && strings.Contains(cfg, "a@example.test") {
		t.Fatalf("the expired user is still authorized: %s", cfg)
	}
}

// node1WithPort rewrites the inbound port of a test instance so its document hash
// changes (a technical change, not an authorization change).
func node1WithPort(t *testing.T, inst contracts.Instance, port int) contracts.Instance {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(inst.Config, &doc); err != nil {
		t.Fatalf("decode instance config: %v", err)
	}
	inbounds, ok := doc["inbounds"].([]any)
	if !ok || len(inbounds) == 0 {
		t.Fatal("the test instance has no inbound")
	}
	inbound := inbounds[0].(map[string]any)
	inbound["port"] = port
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode instance config: %v", err)
	}
	inst.Config = raw
	return inst
}

// C05: a protection state that engages IN THE MIDDLE of a multi-instance apply
// must stop the rest of that apply from starting cores. This is the case the
// asynchronous protection stop can not cover: the apply already holds the gate,
// so only a start gate can refuse the next start.
func TestReviewMidApplyProtectionIsStartGate(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	alice := contracts.AuthorizedUser{MemberID: "member-alice", Email: "alice@example.test"}
	node1 := nodeInstanceNamed("node-1", []contracts.AuthorizedUser{alice}, "alice@example.test")
	node1Changed := node1WithPort(t, nodeInstanceNamed("node-1", []contracts.AuthorizedUser{alice}, "alice@example.test"), 8443)
	node2 := nodeInstanceNamed("node-2", []contracts.AuthorizedUser{alice}, "alice@example.test")
	// rev1 starts node-1 only; node-2 is added in rev2 so the apply really starts
	// a second core after the first instance was reconciled.
	if err := e.sup.applySnapshot(ctx, &contracts.Snapshot{
		SchemaVersion: contracts.SchemaVersion, ControlGeneration: "gen-review", Revision: 1,
		IssuedAt: e.now, ExpectedUserCount: 1, Instances: []contracts.Instance{node1},
	}); err != nil {
		t.Fatalf("rev1: %v", err)
	}
	startsBefore, _ := e.runner.counts()

	// The final sample of the stopped node-1 core can not be persisted: this is
	// exactly how a disk failure appears in the middle of an apply (node-1 gets a
	// new hash, so it is stopped and restarted, and its final sample is written
	// before the replacement core starts).
	db, err := sql.Open("sqlite", e.store.Path())
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER review_sample_fault BEFORE INSERT ON baseline
		BEGIN SELECT RAISE(ABORT,'injected disk failure'); END`); err != nil {
		t.Fatalf("install trigger: %v", err)
	}
	e.runner.mu.Lock()
	e.runner.statsAny = []xray.StatCounter{{Name: "user>>>alice@example.test>>>traffic>>>uplink", Value: 42}}
	e.runner.mu.Unlock()

	// node-2 must not be started: its start happens after node-1's failed sample.
	_ = e.sup.applySnapshot(ctx, &contracts.Snapshot{
		SchemaVersion: contracts.SchemaVersion, ControlGeneration: "gen-review", Revision: 2,
		IssuedAt: e.now, ExpectedUserCount: 2, Instances: []contracts.Instance{node1Changed, node2},
	})
	if err := e.sup.protectionError(); err == nil {
		t.Fatal("the injected persistence failure never engaged the protection")
	}
	starts, _ := e.runner.counts()
	if starts > startsBefore+1 {
		t.Fatalf("the apply kept starting cores after the persistence failure: %d -> %d starts", startsBefore, starts)
	}
	for _, inst := range e.sup.mgr.Snapshot() {
		if inst.InstanceID == "node-2" {
			t.Fatalf("node-2 was started although the protection engaged during the apply: %+v", inst)
		}
	}
}

// C03 at the supervisor level: a snapshot that changes node-a technically (its
// validation blocks) and revokes a user on node-b must stop node-b's old core
// inside the authorization deadline - before node-a's validation finishes.
//
// This is the multi-node case that a per-instance `authorizationTightened` check
// inside a sequential reconcile can not satisfy: node-b's turn only comes after
// node-a's slow `xray -test`.
func TestReviewSlowPeerValidationDoesNotDelayPeerRevocation(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	alice := contracts.AuthorizedUser{MemberID: "member-alice", Email: "alice@example.test"}
	bob := contracts.AuthorizedUser{MemberID: "member-bob", Email: "bob@example.test"}
	carol := contracts.AuthorizedUser{MemberID: "member-carol", Email: "carol@example.test"}
	node1 := nodeInstanceNamed("node-1", []contracts.AuthorizedUser{alice}, "alice@example.test")
	node2 := nodeInstanceNamed("node-2", []contracts.AuthorizedUser{bob, carol}, "bob@example.test", "carol@example.test")

	if err := e.sup.applySnapshot(ctx, &contracts.Snapshot{
		SchemaVersion: contracts.SchemaVersion, ControlGeneration: "gen-review", Revision: 1,
		IssuedAt: e.now, ExpectedUserCount: 3, Instances: []contracts.Instance{node1, node2},
	}); err != nil {
		t.Fatalf("rev1: %v", err)
	}
	if got := e.runner.lastStartedFor("node-2"); !strings.Contains(got, "carol@example.test") {
		t.Fatalf("node-2 was not started with both users: %q", got)
	}

	// node-1 gets a technical change whose validation blocks; node-2 loses carol.
	e.runner.mu.Lock()
	e.runner.testDelay = 3 * time.Second
	e.runner.mu.Unlock()
	node1Changed := node1WithPort(t, node1, 8443)
	node2Narrowed := nodeInstanceNamed("node-2", []contracts.AuthorizedUser{bob}, "bob@example.test")

	done := make(chan error, 1)
	go func() {
		done <- e.sup.applySnapshot(ctx, &contracts.Snapshot{
			SchemaVersion: contracts.SchemaVersion, ControlGeneration: "gen-review", Revision: 2,
			IssuedAt: e.now, ExpectedUserCount: 2, Instances: []contracts.Instance{node1Changed, node2Narrowed},
		})
	}()

	// node-2's OLD core must be stopped (revoked users' connections terminated)
	// inside the authorization deadline and BEFORE node-1's validation ends.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if !e.runner.stoppedAt("node-2").IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("node-2's old core was not stopped within %s while node-1 validated", 10*time.Second)
		}
		time.Sleep(5 * time.Millisecond)
	}
	stoppedAt := e.runner.stoppedAt("node-2")
	if err := <-done; err != nil {
		t.Fatalf("apply: %v", err)
	}
	// The apply has returned, so the slow validation of node-1 has definitely
	// ended. Only now can the two timestamps be compared meaningfully.
	end := e.runner.testEndAt.Load()
	if end == 0 {
		t.Fatal("the slow validation of node-1 never completed")
	}
	if stoppedAt.UnixNano() > end {
		t.Fatalf("node-2 was revoked only after the slow validation of node-1 finished (stopped=%s validation ended=%s)",
			stoppedAt.Format(time.RFC3339Nano), time.Unix(0, end).Format(time.RFC3339Nano))
	}
	users := mustRecordUsers(t, e)
	if hasUser(users["node-2"], "carol@example.test") {
		t.Fatalf("the revoked user is still committed for node-2: %+v", users["node-2"])
	}
	if !hasUser(users["node-2"], "bob@example.test") || !hasUser(users["node-1"], "alice@example.test") {
		t.Fatalf("unrelated authorization changed: %+v", users)
	}
}
