package supervisor

import (
	"context"
	"database/sql"
	"encoding/json"
	"proxypanel/internal/agent/xray"
	"proxypanel/internal/contracts"
	"testing"
	"time"
)

func reviewApply(t *testing.T, e *testEnv, rev int64, users []contracts.AuthorizedUser, emails ...string) {
	t.Helper()
	err := e.sup.applySnapshot(context.Background(), &contracts.Snapshot{SchemaVersion: contracts.SchemaVersion, ControlGeneration: "gen-review", Revision: rev, IssuedAt: e.now, ExpectedUserCount: len(users), Instances: []contracts.Instance{nodeInstance(users, emails...)}})
	if err != nil {
		t.Fatal(err)
	}
}
func TestReviewQueueProtectionBypassedAndNoResume(t *testing.T) {
	e := newTestEnv(t)
	u := []contracts.AuthorizedUser{{MemberID: "m1", Email: "a@example.test"}}
	reviewApply(t, e, 1, u, "a@example.test")
	e.sup.setQueueOverCapacity(true)
	if !waitForStops(e.runner, 1, time.Second) {
		t.Fatal("stop missing")
	}
	err := e.sup.applySnapshot(context.Background(), &contracts.Snapshot{SchemaVersion: contracts.SchemaVersion, ControlGeneration: "gen-review", Revision: 2, IssuedAt: e.now, ExpectedUserCount: 1, Instances: []contracts.Instance{nodeInstance(u, "a@example.test")}})
	if err == nil || len(e.sup.mgr.Snapshot()) != 0 {
		t.Fatal("new revision bypassed overload startup gate")
	}
}
func TestReviewInstanceWriteFailureStillAdvancesAndRestoresRevokedUser(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	a := contracts.AuthorizedUser{MemberID: "m1", Email: "a@example.test"}
	b := contracts.AuthorizedUser{MemberID: "m2", Email: "b@example.test"}
	reviewApply(t, e, 1, []contracts.AuthorizedUser{a, b}, a.Email, b.Email)
	db, err := sql.Open("sqlite", e.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TRIGGER review_fault BEFORE UPDATE ON instances BEGIN SELECT RAISE(ABORT,'injected instance write failure'); END`); err != nil {
		t.Fatal(err)
	}
	err = e.sup.applySnapshot(ctx, &contracts.Snapshot{SchemaVersion: contracts.SchemaVersion, ControlGeneration: "gen-review", Revision: 2, IssuedAt: e.now, ExpectedUserCount: 1, Instances: []contracts.Instance{nodeInstance([]contracts.AuthorizedUser{a}, a.Email)}})
	if err == nil {
		t.Fatal("instance write failure swallowed")
	}
	id, _ := e.store.Identity(ctx)
	if id.AppliedRevision != 1 {
		t.Fatalf("failed transaction advanced watermark: %+v", id)
	}
	if _, err = db.Exec(`DROP TRIGGER review_fault`); err != nil {
		t.Fatal(err)
	}
	e.sup.mgr.StopAll(ctx)
	if err = e.sup.restore(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countClients(e.runner.lastStartedFor("node-1")); got > 1 {
		t.Fatalf("revoked user resurrected: restored %d clients", got)
	}
}
func TestReviewRejectedExistingInstanceReportedApplied(t *testing.T) {
	e := newTestEnv(t)
	u := []contracts.AuthorizedUser{{MemberID: "m1", Email: "a@example.test"}}
	reviewApply(t, e, 1, u, "a@example.test")
	inst := nodeInstance(u, "a@example.test")
	var doc map[string]any
	json.Unmarshal(inst.Config, &doc)
	doc["api"] = map[string]any{"tag": "forbidden"}
	inst.Config, _ = json.Marshal(doc)
	err := e.sup.applySnapshot(context.Background(), &contracts.Snapshot{SchemaVersion: contracts.SchemaVersion, ControlGeneration: "gen-review", Revision: 2, IssuedAt: e.now, ExpectedUserCount: 1, Instances: []contracts.Instance{inst}})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := e.store.Identity(context.Background())
	if id.AppliedRevision != 1 || e.sup.lastApplyState != applyStateFailed || len(e.sup.mgr.Snapshot()) != 0 {
		t.Fatalf("unexpected result rev=%d state=%s", id.AppliedRevision, e.sup.lastApplyState)
	}

}
func TestReviewFailedOtherNodeBlocksOfflineExpiry(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	expiry := e.now.Add(time.Second)
	good := nodeInstance([]contracts.AuthorizedUser{{MemberID: "m1", Email: "a@example.test", ExpiresAt: &expiry}}, "a@example.test")
	bad := contracts.Instance{ID: "node-bad", NodeID: "node-bad", Config: json.RawMessage(`{"inbounds":[],"api":{}}`)}
	err := e.sup.applySnapshot(ctx, &contracts.Snapshot{SchemaVersion: contracts.SchemaVersion, ControlGeneration: "gen-review", Revision: 1, IssuedAt: e.now, ExpectedUserCount: 1, Instances: []contracts.Instance{good, bad}})
	if err != nil {
		t.Fatal(err)
	}
	e.advance(2 * time.Second)
	for i := 0; i < 3; i++ {
		_ = e.sup.expiryOnce(ctx)
	}
	if len(e.sup.mgr.ExpiredInstances(e.sup.now())) != 0 || stopCount(e.runner) == 0 {
		t.Fatalf("expired authorization retained: expired=%v stops=%d", e.sup.mgr.ExpiredInstances(e.sup.now()), stopCount(e.runner))
	}
}
func TestReviewStopAllOmitsFinalUsage(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	reviewApply(t, e, 1, []contracts.AuthorizedUser{{MemberID: "m1", Email: "a@example.test"}}, "a@example.test")
	e.runner.mu.Lock()
	e.runner.statsAny = []xray.StatCounter{{Name: "user>>>a@example.test>>>traffic>>>uplink", Value: 123}}
	e.runner.mu.Unlock()
	e.sup.mgr.StopAll(ctx)
	d, err := e.store.QueueDepth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.Batches != 1 {
		t.Fatalf("final 123 bytes lost: depth=%+v", d)
	}
}
