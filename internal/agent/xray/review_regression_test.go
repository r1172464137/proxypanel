package xray

import (
	"context"
	"testing"
	"time"
)

func TestReviewRevocationTimeoutKeepsRevokedUser(t *testing.T) {
	m, r := newValidateProbeManager(t)
	first := validateTarget(t, "node-1", 443, "a@example.test", "b@example.test")
	m.Apply(context.Background(), []Target{first}, ApplyOptions{})
	m.SetTimeouts(Timeouts{Validate: 10 * time.Millisecond})
	r.setTestFn(func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() })
	narrowed := validateTarget(t, "node-1", 443, "a@example.test")
	m.Apply(context.Background(), []Target{narrowed}, ApplyOptions{})
	if r.stopCount() != 1 {
		t.Fatalf("revocation must stop old authorization before slow validation; stops=%d", r.stopCount())
	}
	for _, s := range m.Snapshot() {
		if s.AppliedUserCount > 1 {
			t.Fatalf("revoked user remains: %+v", s)
		}
	}
}

func TestReviewExitedCoreRemainsApplied(t *testing.T) {
	m, r := newTestManager(t)
	target := targetFor(t, "a@example.test")
	m.Apply(context.Background(), []Target{target}, ApplyOptions{})
	inst := m.lookup(target.InstanceID)
	p := inst.proc.(*fakeProc)
	p.once.Do(func() { close(p.exited) })
	if m.Snapshot()[0].State == StateApplied {
		t.Fatal("exited core still advertised as applied")
	}
	res := m.Apply(context.Background(), []Target{target}, ApplyOptions{})[0]
	if res.State != StateApplied || r.startedCount() != 2 {
		t.Fatalf("dead unchanged core was not recovered: %+v starts=%d", res, r.startedCount())
	}
}
