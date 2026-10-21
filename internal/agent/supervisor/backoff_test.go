package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"proxypanel/internal/agent/state"
	"proxypanel/internal/agent/xray"
	"proxypanel/internal/contracts"
)

// failureEntry reads the per-instance retry backoff state.
func failureEntry(env *testEnv, id string) (failure, bool) {
	env.sup.mu.Lock()
	defer env.sup.mu.Unlock()
	f := env.sup.failures[id]
	if f == nil {
		return failure{}, false
	}
	return *f, true
}

// instanceRecords reads the committed instance records.
func instanceRecords(t *testing.T, env *testEnv) []state.InstanceRecord {
	t.Helper()
	records, err := env.store.Instances(context.Background())
	if err != nil {
		t.Fatalf("read instance records: %v", err)
	}
	return records
}

// changeLogLevel returns the same instance with a different technical document,
// so the plan hash changes without touching the authorization.
func changeLogLevel(t *testing.T, inst contracts.Instance, level string) contracts.Instance {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(inst.Config, &doc); err != nil {
		t.Fatalf("decode instance document: %v", err)
	}
	logDoc, _ := doc["log"].(map[string]any)
	if logDoc == nil {
		logDoc = map[string]any{}
	}
	logDoc["loglevel"] = level
	doc["log"] = logDoc
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode instance document: %v", err)
	}
	inst.Config = raw
	return inst
}

// TestRepeatedDeferredPollsDoNotGrowOrExtendBackoff pins the defect found in the
// Docker config-safety run: while the panel keeps offering a revision that is
// inside its retry backoff, every poll returned StateDeferred. That must not be
// counted as a new failure and must not move the retry deadline, otherwise each
// poll re-arms the backoff and the instance can never recover (the remote agent
// parked at revision 84 with a growing window).
func TestRepeatedDeferredPollsDoNotGrowOrExtendBackoff(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	user := contracts.AuthorizedUser{MemberID: "m1", Email: "alice@example.test"}
	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{nodeInstance([]contracts.AuthorizedUser{user}, user.Email)}, 1)
	env.runner.setTestError(errors.New("injected invalid technical config"))
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	first, ok := failureEntry(env, "node-1")
	if !ok || first.count != 1 || first.hash == "" || !first.next.After(env.sup.now()) {
		t.Fatalf("the real failure was not armed: %+v", first)
	}
	deadline := first.next
	before := instanceRecords(t, env)
	if len(before) != 1 || before[0].State != xray.StateFailed {
		t.Fatalf("the failed attempt was not recorded: %+v", before)
	}

	// The panel re-offers the same revision each poll until it is applied. Every
	// poll stays inside the armed window (the window must not be re-armed).
	polls := 0
	for env.sup.now().Add(time.Second).Before(deadline) {
		env.advance(time.Second)
		polls++
		if err := env.sup.syncOnce(ctx); err != nil {
			t.Fatalf("poll %d: %v", polls, err)
		}
		got, ok := failureEntry(env, "node-1")
		if !ok {
			t.Fatalf("poll %d dropped the retry record", polls)
		}
		if got.count != first.count {
			t.Fatalf("poll %d counted a deferred poll as a new failure: count=%d want %d", polls, got.count, first.count)
		}
		if !got.next.Equal(deadline) {
			t.Fatalf("poll %d moved the retry deadline: %v want %v", polls, got.next, deadline)
		}
	}
	if polls < 2 {
		t.Fatalf("the retry window is too small to exercise deferred polls: %v", deadline.Sub(env.sup.now()))
	}
	// Nothing was attempted and nothing was reported applied.
	if starts, _ := env.runner.counts(); starts != 0 {
		t.Fatalf("a deferred poll started a core: starts=%d", starts)
	}
	if id, err := env.store.Identity(ctx); err != nil || id.AppliedRevision != 0 {
		t.Fatalf("a deferred snapshot advanced the applied watermark: %+v err=%v", id, err)
	}
	for _, report := range env.panel.appliedReports() {
		if report.State == applyStateApplied {
			t.Fatalf("a deferred poll reported the revision as applied: %+v", report)
		}
	}
	// The committed record keeps the real failure instead of "deferred".
	after := instanceRecords(t, env)
	if len(after) != 1 || after[0].State != xray.StateFailed || after[0].Error != before[0].Error {
		t.Fatalf("a deferred poll rewrote the committed record: %+v", after)
	}
}

// TestRealFailureStillBacksOffAndRetriesAfterDeadline proves the bounded backoff
// is still real: the identical failing target is not retried inside the window,
// and it is retried once the window expires.
func TestRealFailureStillBacksOffAndRetriesAfterDeadline(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	user := contracts.AuthorizedUser{MemberID: "m1", Email: "alice@example.test"}
	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{nodeInstance([]contracts.AuthorizedUser{user}, user.Email)}, 1)
	env.runner.setTestError(errors.New("injected invalid technical config"))
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	armed, ok := failureEntry(env, "node-1")
	if !ok {
		t.Fatal("the failure was not recorded")
	}
	// The fault disappears, but the same target is still inside its backoff.
	env.runner.setTestError(nil)
	env.advance(2 * time.Second)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("poll inside the backoff: %v", err)
	}
	if starts, _ := env.runner.counts(); starts != 0 {
		t.Fatalf("the still backed-off target was retried early: starts=%d", starts)
	}
	// After the deadline the same target is retried and applied.
	env.advance(armed.next.Sub(env.sup.now()) + time.Second)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("sync after the backoff: %v", err)
	}
	if starts, _ := env.runner.counts(); starts != 1 {
		t.Fatalf("the target was not retried after its backoff: starts=%d", starts)
	}
	if id, err := env.store.Identity(ctx); err != nil || id.AppliedRevision != 1 {
		t.Fatalf("the retried snapshot was not applied: %+v err=%v", id, err)
	}
	if _, ok := failureEntry(env, "node-1"); ok {
		t.Fatal("a successful retry kept the failure record")
	}
}

// TestCorrectedConfigAppliesImmediatelyDespiteBackoff proves a different target
// (the operator fixed the technical document) is not held back by the backoff of
// the target that failed.
func TestCorrectedConfigAppliesImmediatelyDespiteBackoff(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	user := contracts.AuthorizedUser{MemberID: "m1", Email: "alice@example.test"}
	broken := nodeInstance([]contracts.AuthorizedUser{user}, user.Email)
	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{broken}, 1)
	env.runner.setTestError(errors.New("injected invalid technical config"))
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("failing sync: %v", err)
	}
	if _, ok := failureEntry(env, "node-1"); !ok {
		t.Fatal("the failure was not recorded")
	}
	// Same member, corrected document: a different hash must be attempted now,
	// without waiting for the backoff (the clock is not advanced here).
	env.runner.setTestError(nil)
	env.panel.setSnapshot("gen-1", 2, []contracts.Instance{changeLogLevel(t, broken, "info")}, 1)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("corrected sync: %v", err)
	}
	if starts, _ := env.runner.counts(); starts != 1 {
		t.Fatalf("the corrected configuration was not attempted: starts=%d", starts)
	}
	if id, err := env.store.Identity(ctx); err != nil || id.AppliedRevision != 2 {
		t.Fatalf("the corrected configuration was not applied: %+v err=%v", id, err)
	}
	if countClients(env.runner.lastStartedFor("node-1")) != 1 {
		t.Fatal("the corrected configuration did not authorize the member")
	}
	if _, ok := failureEntry(env, "node-1"); ok {
		t.Fatal("a successful corrected apply kept the failure record")
	}
}

// TestRevocationAppliesImmediatelyDespiteBackoff is the 60 second requirement:
// a narrowed authorization must never be queued behind the backoff of an older,
// different target.
func TestRevocationAppliesImmediatelyDespiteBackoff(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	alice := contracts.AuthorizedUser{MemberID: "m1", Email: "alice@example.test"}
	bob := contracts.AuthorizedUser{MemberID: "m2", Email: "bob@example.test"}
	carol := contracts.AuthorizedUser{MemberID: "m3", Email: "carol@example.test"}
	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{nodeInstance([]contracts.AuthorizedUser{alice, bob}, alice.Email, bob.Email)}, 2)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if starts, _ := env.runner.counts(); starts != 1 {
		t.Fatalf("initial apply did not start the core: starts=%d", starts)
	}
	// A failing technical change arms the backoff for that (different) target.
	env.runner.setTestError(errors.New("injected invalid technical config"))
	env.panel.setSnapshot("gen-1", 2, []contracts.Instance{nodeInstance(
		[]contracts.AuthorizedUser{alice, bob, carol}, alice.Email, bob.Email, carol.Email)}, 3)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("failing sync: %v", err)
	}
	armed, ok := failureEntry(env, "node-1")
	if !ok {
		t.Fatal("the failed target was not recorded")
	}
	// The operator revokes bob while the backoff is still armed: the narrowing
	// target is a new hash and must be applied immediately (the clock is not
	// advanced at all).
	env.runner.setTestError(nil)
	env.panel.setSnapshot("gen-1", 3, []contracts.Instance{nodeInstance([]contracts.AuthorizedUser{alice}, alice.Email)}, 1)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("revocation sync: %v", err)
	}
	if id, err := env.store.Identity(ctx); err != nil || id.AppliedRevision != 3 {
		t.Fatalf("the revocation was not applied immediately: %+v err=%v", id, err)
	}
	if got := countClients(env.runner.lastStartedFor("node-1")); got != 1 {
		t.Fatalf("the revoked member is still authorized: %d client(s)", got)
	}
	if records := instanceRecords(t, env); len(records) != 1 || len(records[0].Users) != 1 || records[0].Users[0].MemberID != "m1" {
		t.Fatalf("the narrowed authorization was not persisted: %+v", records)
	}
	if _, ok := failureEntry(env, "node-1"); ok {
		t.Fatalf("the revocation apply kept the old backoff: %+v", armed)
	}
}

// TestLocalExpiryNotBlockedByPreviousBackoff pins the same rule for the local
// expiry path: while the backoff of the current target is still armed, a user
// reaching its delivered expiry drops that user and must be rebuilt now.
func TestLocalExpiryNotBlockedByPreviousBackoff(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	expiry := env.now.Add(30 * time.Second)
	alice := contracts.AuthorizedUser{MemberID: "m1", Email: "alice@example.test", ExpiresAt: &expiry}
	bob := contracts.AuthorizedUser{MemberID: "m2", Email: "bob@example.test"}
	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{nodeInstance([]contracts.AuthorizedUser{alice, bob}, alice.Email, bob.Email)}, 2)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	records := instanceRecords(t, env)
	if len(records) != 1 || records[0].ConfigHash == "" {
		t.Fatalf("unexpected committed record: %+v", records)
	}
	// Arm the backoff exactly as a failed attempt of the CURRENT target does.
	env.sup.mu.Lock()
	env.sup.failures["node-1"] = &failure{
		count:   6,
		next:    env.now.Add(5 * time.Minute),
		lastErr: "injected earlier failure of the same target",
		hash:    records[0].ConfigHash,
	}
	env.sup.mu.Unlock()
	// The user expiry passes while the backoff is armed.
	env.advance(31 * time.Second)
	if err := env.sup.expiryOnce(ctx); err != nil {
		t.Fatalf("expiryOnce: %v", err)
	}
	if starts, _ := env.runner.counts(); starts != 2 {
		t.Fatalf("the expired authorization was not rebuilt: starts=%d", starts)
	}
	if got := countClients(env.runner.lastStartedFor("node-1")); got != 1 {
		t.Fatalf("the expired user is still authorized: %d client(s)", got)
	}
	if records := instanceRecords(t, env); len(records) != 1 || len(records[0].Users) != 1 || records[0].Users[0].MemberID != "m2" || !records[0].NextExpiry.IsZero() {
		t.Fatalf("the expired authorization was not persisted: %+v", records)
	}
}
