package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxypanel/internal/agent/state"
	"proxypanel/internal/contracts"
)

func TestLifecycleExpiryStopsSameInstanceDuringValidation(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	expiry := env.sup.now().Add(time.Minute)
	u := contracts.AuthorizedUser{MemberID: "m1", Email: "alice@example.test", ExpiresAt: &expiry}
	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{nodeInstance([]contracts.AuthorizedUser{u}, u.Email)}, 1)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// Own the exact instance lifecycle lock, as a slow technical validator does.
	// The public manager API is intentionally exercised through a blocking runner.
	env.runner.mu.Lock()
	env.runner.testDelay = 800 * time.Millisecond
	env.runner.testBegin = make(chan struct{}, 1)
	env.runner.mu.Unlock()
	env.panel.setSnapshot("gen-1", 2, []contracts.Instance{changeLogLevel(t, nodeInstance([]contracts.AuthorizedUser{u}, u.Email), "info")}, 1)
	done := make(chan error, 1)
	go func() { done <- env.sup.syncOnce(ctx) }()
	// Await the actual validation event, rather than sleeping for a guessed delay.
	select {
	case <-env.runner.testBegin:
	case <-time.After(2 * time.Second):
		t.Fatal("validation did not begin")
	}
	env.advance(2 * time.Minute)
	env.sup.SetExpiryLockBudget(10 * time.Millisecond)
	started := time.Now()
	if err := env.sup.expiryOnce(ctx); err == nil {
		t.Fatal("fallback must report busy apply gate")
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("expiry waited for same-instance technical validation")
	}
	_, stops := env.runner.counts()
	if stops == 0 {
		t.Fatal("expired core was not stopped")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	starts, _ := env.runner.counts()
	if starts != 1 {
		t.Fatalf("expired target was resurrected: starts=%d", starts)
	}
}

func TestLifecycleFinalSampleFailureIsVisibleAndQueueRetained(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	u := contracts.AuthorizedUser{MemberID: "m1", Email: "alice@example.test"}
	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{nodeInstance([]contracts.AuthorizedUser{u}, u.Email)}, 1)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.Measure(ctx, "node-1", "test-gen", env.sup.now(), []state.CounterItem{{MemberID: "m1", Counter: "user>>>alice@example.test>>>traffic>>>uplink", Value: 42}}); err != nil {
		t.Fatal(err)
	}
	before, err := env.store.QueueDepth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	env.runner.mu.Lock()
	env.runner.statsErr = errors.New("unavailable")
	env.runner.mu.Unlock()
	env.sup.Manager().StopAll(ctx)
	env.sup.mu.Lock()
	n := env.sup.metrics["final_sampling_errors"]
	env.sup.mu.Unlock()
	if n < 1 {
		t.Fatal("final accounting failure not visible")
	}
	after, err := env.store.QueueDepth(ctx)
	if err != nil || before != after {
		t.Fatalf("queue changed: %+v -> %+v err=%v", before, after, err)
	}
}

func TestLifecycleExporterRecognizesIntentWithoutSecrets(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveIdentity(ctx, state.Identity{AgentID: "agent", Token: "secret-token", ControlGeneration: "gen", AppliedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	snap := contracts.Snapshot{SchemaVersion: contracts.SchemaVersion, ControlGeneration: "gen", Revision: 2, Instances: []contracts.Instance{nodeInstance([]contracts.AuthorizedUser{{MemberID: "secret-member", Email: "secret-email"}}, "secret-email")}}
	if err := s.SaveApplyIntent(ctx, snap); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := state.ReadUpgradeEvidence(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !e.ApplyIntentTrackingSupported || e.PendingApplyIntentCount != 1 {
		t.Fatalf("intent omitted: %+v", e)
	}
	raw, _ := json.Marshal(e)
	for _, forbidden := range []string{"secret-token", "secret-email", "secret-member", `"config"`, `"users"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("authorization or secret leaked by exporter")
		}
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	if _, ok := obj["instances"]; ok {
		t.Fatal("instance secrets exported")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "agent.db"))
	if string(before) != string(after) {
		t.Fatal("inspection modified input")
	}
	s, err = state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CommitSnapshot(ctx, nil, contracts.ApplyReport{ControlGeneration: "gen", Revision: 2, State: "applied", AppliedAt: time.Now()}, true); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	e, err = state.ReadUpgradeEvidence(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !e.ApplyIntentTrackingSupported || e.PendingApplyIntentCount != 0 || e.AppliedRevision != 2 {
		t.Fatal("committed snapshot metadata not recognized")
	}
}
