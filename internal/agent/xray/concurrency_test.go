package xray

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blockingRunner is a Runner whose lifecycle calls can be held for a while so
// lock contention windows are deterministic instead of timing dependent.
type blockingRunner struct {
	mu         sync.Mutex
	testDelay  time.Duration
	startDelay time.Duration
	statsDelay time.Duration
	starts     int
	stops      int
	stats      []StatCounter
	version    string
	nextPID    int
}

func (r *blockingRunner) setDelays(testDelay, startDelay, statsDelay time.Duration) {
	r.mu.Lock()
	r.testDelay, r.startDelay, r.statsDelay = testDelay, startDelay, statsDelay
	r.mu.Unlock()
}

func (r *blockingRunner) Test(ctx context.Context, configPath string) error {
	r.mu.Lock()
	delay := r.testDelay
	r.mu.Unlock()
	// Delays are applied without holding the runner lock, like a real process
	// spawn: only manager locks are exercised.
	time.Sleep(delay)
	return nil
}

func (r *blockingRunner) Start(ctx context.Context, spec StartSpec) (Proc, error) {
	r.mu.Lock()
	delay := r.startDelay
	r.mu.Unlock()
	time.Sleep(delay)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
	r.nextPID++
	// A pid that does not exist keeps the leftover process logic inert.
	return &blockingProc{runner: r, pid: 900000 + r.nextPID, exited: make(chan struct{})}, nil
}

func (r *blockingRunner) StatsQuery(ctx context.Context, server string) ([]StatCounter, error) {
	r.mu.Lock()
	delay, stats := r.statsDelay, r.stats
	r.mu.Unlock()
	time.Sleep(delay)
	return stats, nil
}

func (r *blockingRunner) Version(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.version == "" {
		return "26.3.27", nil
	}
	return r.version, nil
}

func (r *blockingRunner) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts, r.stops
}

type blockingProc struct {
	runner *blockingRunner
	pid    int
	exited chan struct{}
	once   sync.Once
}

func (p *blockingProc) PID() int                { return p.pid }
func (p *blockingProc) Exited() <-chan struct{} { return p.exited }
func (p *blockingProc) ExitError() error        { return nil }
func (p *blockingProc) Output() []string        { return nil }
func (p *blockingProc) Stop(ctx context.Context) error {
	p.once.Do(func() {
		close(p.exited)
		p.runner.mu.Lock()
		p.runner.stops++
		p.runner.mu.Unlock()
	})
	return nil
}

func newConcurrencyManager(t *testing.T) (*Manager, *blockingRunner) {
	t.Helper()
	runner := &blockingRunner{}
	mgr, err := NewManager(t.TempDir(), filepath.Join(t.TempDir(), "xray.exe"), runner, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	port := 23000
	var portMu sync.Mutex
	mgr.SetPortAllocator(func(map[int]bool) (int, error) {
		portMu.Lock()
		defer portMu.Unlock()
		port++
		return port, nil
	})
	return mgr, runner
}

func waitGroupDone(wg *sync.WaitGroup, budget time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(budget):
		return false
	}
}

// TestConcurrentLifecycleAndReadersDoNotDeadlock reproduces the production
// deadlock report: an apply holding the instance lock while taking the manager
// lock (reportStarted/owned) against readers holding the manager lock while
// taking the instance lock (QueryStats) plus expiry and shutdown.
func TestConcurrentLifecycleAndReadersDoNotDeadlock(t *testing.T) {
	mgr, runner := newConcurrencyManager(t)
	runner.setDelays(2*time.Millisecond, 2*time.Millisecond, time.Millisecond)
	one := targetFor(t, "alice@example.test")
	two := targetFor(t, "alice@example.test", "bob@example.test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	loop := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				fn()
			}
		}()
	}
	var flips atomic.Int64
	loop(func() { // snapshot loop: alternates the authorized set to force restarts
		target := one
		if flips.Add(1)%2 == 0 {
			target = two
		}
		mgr.Apply(ctx, []Target{target}, ApplyOptions{PerInstanceTimeout: 10 * time.Second})
	})
	loop(func() { _ = mgr.QueryStats(ctx) })                                    // stats loop
	loop(func() { _ = mgr.Snapshot() })                                         // heartbeat loop
	loop(func() { _ = mgr.NextExpiry(); _ = mgr.ExpiredInstances(time.Now()) }) // expiry loop
	loop(func() { _, _ = mgr.Stop(ctx, "node-1", "concurrency test") })

	time.Sleep(700 * time.Millisecond)
	cancel()
	if !waitGroupDone(&wg, 20*time.Second) {
		t.Fatal("apply/query/snapshot/expiry/stop did not finish: lock cycle suspected")
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer stopCancel()
	stopped := make(chan struct{})
	go func() {
		mgr.StopAll(stopCtx)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(15 * time.Second):
		t.Fatal("StopAll did not finish after concurrent churn")
	}
}

// TestReadersAreNotBlockedBySlowApply pins the bounded time property: heartbeat,
// stats and expiry reads must not wait for a config test or start in progress.
func TestReadersAreNotBlockedBySlowApply(t *testing.T) {
	mgr, runner := newConcurrencyManager(t)
	base := targetFor(t, "alice@example.test")
	changed := targetFor(t, "alice@example.test", "bob@example.test")
	if results := mgr.Apply(context.Background(), []Target{base}, ApplyOptions{}); results[0].State != StateApplied {
		t.Fatalf("initial apply: %+v", results[0])
	}
	runner.setDelays(500*time.Millisecond, 500*time.Millisecond, 0)
	applyDone := make(chan struct{})
	go func() {
		defer close(applyDone)
		mgr.Apply(context.Background(), []Target{changed}, ApplyOptions{})
	}()
	time.Sleep(120 * time.Millisecond) // the apply now holds the instance lock

	measure := func(name string, limit time.Duration, fn func()) {
		started := time.Now()
		fn()
		if elapsed := time.Since(started); elapsed > limit {
			t.Fatalf("%s blocked for %s during an apply (limit %s)", name, elapsed, limit)
		}
	}
	measure("Snapshot", 250*time.Millisecond, func() { _ = mgr.Snapshot() })
	measure("NextExpiry", 100*time.Millisecond, func() { _ = mgr.NextExpiry() })
	measure("ExpiredInstances", 100*time.Millisecond, func() { _ = mgr.ExpiredInstances(time.Now()) })
	measure("QueryStats", 300*time.Millisecond, func() { _ = mgr.QueryStats(context.Background()) })

	select {
	case <-applyDone:
	case <-time.After(20 * time.Second):
		t.Fatal("the slow apply never finished")
	}
}

// TestStopAllBoundedWhileApplyInFlight proves shutdown is bounded while an apply
// holds the instance lock.
func TestStopAllBoundedWhileApplyInFlight(t *testing.T) {
	mgr, runner := newConcurrencyManager(t)
	base := targetFor(t, "alice@example.test")
	changed := targetFor(t, "alice@example.test", "bob@example.test")
	if results := mgr.Apply(context.Background(), []Target{base}, ApplyOptions{}); results[0].State != StateApplied {
		t.Fatalf("initial apply: %+v", results[0])
	}
	runner.setDelays(300*time.Millisecond, 300*time.Millisecond, 0)
	applyDone := make(chan struct{})
	go func() {
		defer close(applyDone)
		mgr.Apply(context.Background(), []Target{changed}, ApplyOptions{})
	}()
	time.Sleep(80 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	started := time.Now()
	stopped := make(chan struct{})
	go func() {
		mgr.StopAll(ctx)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(15 * time.Second):
		t.Fatal("StopAll blocked while an apply was in flight")
	}
	if elapsed := time.Since(started); elapsed > 12*time.Second {
		t.Fatalf("StopAll took %s", elapsed)
	}
	select {
	case <-applyDone:
	case <-time.After(20 * time.Second):
		t.Fatal("the apply never returned")
	}
	if got := mgr.Snapshot(); len(got) != 0 {
		t.Fatalf("instances still tracked after StopAll: %+v", got)
	}
	if _, stops := runner.counts(); stops == 0 {
		t.Fatal("no core was stopped")
	}
}
