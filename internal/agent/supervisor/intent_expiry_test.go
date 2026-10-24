package supervisor

import (
	"context"
	"testing"
	"time"

	"proxypanel/internal/contracts"
)

// An expiry cycle is not allowed to bypass the write-ahead authorization fence,
// even if a crash/failing commit left older, wider instance records behind.
func TestLifecycleExpiryNeverRestoresRecordsBehindPendingIntent(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	expiry := e.sup.now().Add(time.Minute)
	alice := contracts.AuthorizedUser{MemberID: "alice", Email: "alice@example.test", ExpiresAt: &expiry}
	bob := contracts.AuthorizedUser{MemberID: "bob", Email: "bob@example.test"}
	e.panel.setSnapshot("gen-1", 1, []contracts.Instance{nodeInstance([]contracts.AuthorizedUser{alice, bob}, alice.Email, bob.Email)}, 2)
	if err := e.sup.syncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// Revision 2 revoked Bob, but its instance transaction has not committed.
	snap := contracts.Snapshot{SchemaVersion: contracts.SchemaVersion, ControlGeneration: "gen-1", Revision: 2, Instances: []contracts.Instance{nodeInstance([]contracts.AuthorizedUser{alice}, alice.Email)}, ExpectedUserCount: 1}
	if err := e.store.SaveApplyIntent(ctx, snap); err != nil {
		t.Fatal(err)
	}
	e.advance(2 * time.Minute)
	if err := e.sup.expiryOnce(ctx); err == nil {
		t.Fatal("unresolved intent must remain visible")
	}
	starts, stops := e.runner.counts()
	if starts != 1 || stops < 1 {
		t.Fatalf("expiry rebuilt old authorization: starts=%d stops=%d", starts, stops)
	}
	if pending, err := e.store.ApplyIntent(ctx); err != nil || pending == nil || pending.Revision != 2 {
		t.Fatal("expiry consumed or lost intent")
	}
}
