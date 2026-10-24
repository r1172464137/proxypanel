package supervisor

import (
	"context"
	"sync"
)

// ctxMutex is a mutex whose acquisition can be given up, and that does NOT spend
// a goroutine per cancelled waiter.
//
// The agent has two places where an unbounded wait would be a safety problem:
//
//   - the apply gate: an apply that reconciles several instances holds it for as
//     long as its technical validation takes, while a delivered user expiry must
//     still be enforced;
//   - the sampling gate: the final-sample hook runs immediately before a core is
//     stopped, so waiting indefinitely for a periodic sampling cycle would delay
//     exactly the stop that enforces a revocation.
//
// Implementation: a capacity-1 channel used as a semaphore. A blocked caller
// parks directly in the channel's sender queue, so cancelling it costs nothing -
// there is no helper goroutine to wait for the lock and no second goroutine to
// release it later. That matters because the expiry loop retries every second:
// with a per-request goroutine a long apply would accumulate two goroutines per
// cancelled expiry attempt for as long as it runs.
//
// The zero value is ready to use. Ordering is the runtime's channel queue (FIFO
// for blocked senders), and a cancellation that races a grant is resolved in one
// direction only: the caller that observes ctx.Err() releases the token again and
// reports the cancellation, so a cancelled caller never silently holds the lock.
type ctxMutex struct {
	once sync.Once
	ch   chan struct{}
}

// init creates the semaphore once. It is safe to call concurrently.
func (m *ctxMutex) init() {
	m.once.Do(func() { m.ch = make(chan struct{}, 1) })
}

// lock blocks until the mutex is free, like a plain mutex.
func (m *ctxMutex) lock() {
	m.init()
	m.ch <- struct{}{}
}

// lockContext acquires the mutex or reports ctx.Err() when ctx ends first. It
// never starts a goroutine and never loses the token.
func (m *ctxMutex) lockContext(ctx context.Context) error {
	m.init()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case m.ch <- struct{}{}:
		// Both cases can be ready at the same instant. Resolve that race
		// deterministically: a caller whose context already ended must not keep
		// the lock it just won.
		if err := ctx.Err(); err != nil {
			<-m.ch
			return err
		}
		return nil
	case <-ctx.Done():
		// Nothing was acquired, so there is nothing to release.
		return ctx.Err()
	}
}

// unlock releases the mutex.
func (m *ctxMutex) unlock() {
	m.init()
	<-m.ch
}
