package supervisor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"proxypanel/internal/agent/state"
	"proxypanel/internal/agent/xray"
	"proxypanel/internal/contracts"
)

func TestIntegrationSamplingReadCommitOrder(t *testing.T) {
	e := newTestEnv(t)
	u := []contracts.AuthorizedUser{{MemberID: "m1", Email: "a@example.test"}}
	reviewApply(t, e, 1, u, "a@example.test")
	ctx := context.Background()
	records, err := e.store.Instances(ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("records: %v %v", records, err)
	}
	gen := records[0].CoreGeneration
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	e.runner.mu.Lock()
	e.runner.statsQueryHook = func(ctx context.Context, _ string) ([]xray.StatCounter, error) {
		call := calls.Add(1)
		value := int64(100)
		if call == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		} else {
			// The second READ may start only after the first READ's transaction has
			// committed, not merely after its statsquery returned.
			batches, err := e.store.PendingBatches(ctx, 10)
			if err != nil || len(batches) != 1 || batches[0].Samples[0].UploadBytes != 100 {
				t.Errorf("second read overtook first commit: %v %v", batches, err)
			}
			value = 200
		}
		return []xray.StatCounter{{Name: "user>>>a@example.test>>>traffic>>>uplink", Value: value}}, nil
	}
	e.runner.mu.Unlock()
	periodic := make(chan error, 1)
	go func() { periodic <- e.sup.sampleOnce(ctx) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("periodic query not reached")
	}
	final := make(chan struct{})
	go func() {
		defer close(final)
		e.sup.sampleHook()(ctx, "node-1", gen, "owned-fake-api", map[string]string{"a@example.test": "m1"})
	}()
	select {
	case <-final:
		t.Fatal("final sample bypassed in-flight periodic read")
	case <-time.After(30 * time.Millisecond):
	}
	if calls.Load() != 1 {
		t.Fatal("overlapping reads")
	}
	close(release)
	if err := <-periodic; err != nil {
		t.Fatal(err)
	}
	select {
	case <-final:
	case <-time.After(3 * time.Second):
		t.Fatal("final sample deadlocked")
	}
	// Re-reading the final cumulative value is idempotent. A NEW generation,
	// however, starts at zero and must account its full first observation.
	counter := "user>>>a@example.test>>>traffic>>>uplink"
	for _, sample := range []struct {
		generation string
		value      int64
	}{{gen, 200}, {"replacement-core", 10}} {
		if _, err := e.store.Measure(ctx, "node-1", sample.generation, e.now, []state.CounterItem{{MemberID: "m1", Counter: counter, Value: sample.value}}); err != nil {
			t.Fatal(err)
		}
	}
	batches, err := e.store.PendingBatches(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, b := range batches {
		for _, s := range b.Samples {
			total += s.UploadBytes
		}
	}
	if total != 210 {
		t.Fatalf("serialized cumulative + new generation total=%d want=210", total)
	}
	e.runner.mu.Lock()
	e.runner.statsQueryHook = nil
	e.runner.mu.Unlock()
}

func TestIntegrationCancelledPeriodicSamplingGate(t *testing.T) {
	e := newTestEnv(t)
	e.sup.sampleMu.lock()
	defer e.sup.sampleMu.unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- e.sup.sampleOnce(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled periodic sample stuck behind final sample")
	}
}
