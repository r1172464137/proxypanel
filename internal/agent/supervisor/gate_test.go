package supervisor

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestReviewCtxMutexCancelledWaitersDoNotAccumulate is the regression for the
// goroutine leak of the previous implementation: a blocked apply gate held for a
// long time while the expiry loop retried once per second used to leave one
// blocked lock goroutine plus one pending release goroutine per cancelled attempt.
func TestReviewCtxMutexCancelledWaitersDoNotAccumulate(t *testing.T) {
	var g ctxMutex
	g.lock() // the "slow apply" holds the gate for the whole test
	before := runtime.NumGoroutine()

	const waiters = 500
	var wg sync.WaitGroup
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Millisecond)
			defer cancel()
			if err := g.lockContext(ctx); err == nil {
				// Must never happen: the gate is held.
				g.unlock()
				t.Error("a waiter acquired a held gate")
			}
		}()
	}
	wg.Wait()

	// The cancelled waiters are gone: nothing may be left over that waits for the
	// gate to be released.
	deadline := time.Now().Add(5 * time.Second)
	for {
		after := runtime.NumGoroutine()
		if after <= before+2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutine(s) leaked by %d cancelled waiters (%d -> %d)", after-before, waiters, before, after)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The gate is still usable and exactly one owner can hold it.
	g.unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := g.lockContext(ctx); err != nil {
		t.Fatalf("the gate is not usable after %d cancelled waiters: %v", waiters, err)
	}
	g.unlock()
}

// TestReviewCtxMutexNoPerRequestGoroutine asserts the implementation property
// directly: a blocked-forever waiter costs exactly one goroutine (itself) and no
// additional helper goroutine, and cancelling it releases that one.
func TestReviewCtxMutexNoPerRequestGoroutine(t *testing.T) {
	var g ctxMutex
	g.lock()
	base := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	acquired := make(chan struct{})
	var result error
	go func() {
		result = g.lockContext(ctx)
		close(acquired)
	}()
	time.Sleep(50 * time.Millisecond) // the waiter is now parked in the channel queue
	during := runtime.NumGoroutine()
	if during > base+1 {
		t.Fatalf("a single blocked waiter added %d goroutine(s), want at most 1", during-base)
	}
	cancel()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled waiter never returned")
	}
	if result == nil {
		t.Fatal("the cancelled waiter reported success")
	}
	g.unlock()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > base && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > base {
		t.Fatalf("%d goroutine(s) leaked after the cancellation", got-base)
	}
}

// TestReviewCtxMutexCancellationNeverLosesTheToken covers the race between a grant
// and a cancellation: under heavy contention with short budgets the gate must
// never be lost (which would dead-lock every later caller) and never be held by a
// cancelled caller (which would break the mutual exclusion invariant).
func TestReviewCtxMutexCancellationNeverLosesTheToken(t *testing.T) {
	var g ctxMutex
	var inside atomic.Int32
	var violations atomic.Int32
	var acquisitions atomic.Int32
	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(i%3)*time.Microsecond)
				err := g.lockContext(ctx)
				cancel()
				if err != nil {
					continue
				}
				if n := inside.Add(1); n != 1 {
					violations.Add(1)
				}
				acquisitions.Add(1)
				time.Sleep(time.Duration(i%2) * 10 * time.Microsecond)
				inside.Add(-1)
				g.unlock()
			}
		}(i)
	}
	time.Sleep(300 * time.Millisecond)
	close(stop)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the gate dead-locked: a token was lost by a cancelled acquire")
	}
	if v := violations.Load(); v != 0 {
		t.Fatalf("%d mutual exclusion violation(s)", v)
	}
	if a := acquisitions.Load(); a == 0 {
		t.Fatal("no caller ever acquired the gate")
	}
	// After all that contention a fresh acquire must still succeed.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := g.lockContext(ctx); err != nil {
		t.Fatalf("the gate is unusable after the contention test: %v", err)
	}
	g.unlock()
}

// TestReviewCtxMutexBlocksKindErrorsAndFairness documents the two ordering
// properties the agent relies on: lock() blocks while the gate is held, and
// blocked callers are granted in arrival order (channel FIFO), so a steady stream
// of cancellable waiters can not starve a plain lock() caller forever.
func TestReviewCtxMutexBlocksKindErrorsAndFairness(t *testing.T) {
	var g ctxMutex
	g.lock()

	// lock() must block.
	plain := make(chan struct{})
	go func() {
		g.lock()
		close(plain)
		// Hold it briefly, then hand it on: a plain lock() caller must not starve
		// the queued cancellable waiter forever.
		time.Sleep(20 * time.Millisecond)
		g.unlock()
	}()
	select {
	case <-plain:
		t.Fatal("lock() did not block on a held gate")
	case <-time.After(100 * time.Millisecond):
	}

	// An already cancelled context must fail immediately without touching the
	// gate (the gate stays held for the real owner).
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.lockContext(cancelled); err == nil {
		t.Fatal("lockContext succeeded with an already cancelled context")
	}

	// A live waiter with a generous budget must be granted once the gate frees.
	live, liveCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer liveCancel()
	granted := make(chan error, 1)
	go func() { granted <- g.lockContext(live) }()
	time.Sleep(50 * time.Millisecond)

	g.unlock()
	// The blocked plain lock() caller and the lockContext caller are both queued;
	// both must complete without a dead-lock.
	select {
	case <-plain:
	case <-time.After(5 * time.Second):
		t.Fatal("the blocked lock() caller never got the gate: ordering broke")
	}
	select {
	case err := <-granted:
		if err == nil {
			g.unlock()
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the blocked lockContext caller never returned")
	}
}

// TestReviewCtxMutexZeroValueIsUsable pins the zero value contract used by the
// struct fields (no constructor call anywhere).
func TestReviewCtxMutexZeroValueIsUsable(t *testing.T) {
	m := &ctxMutex{}
	m.lock()
	m.unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.lockContext(ctx); err != nil {
		t.Fatalf("zero value lockContext: %v", err)
	}
	m.unlock()
}
