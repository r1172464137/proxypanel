package supervisor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"proxypanel/internal/agent/state"
	"proxypanel/internal/agent/xray"
)

const maxRecoveryAttempts = 5

// stableRecoveryWindow is how long a recovered core must keep running before the
// bounded recovery budget of that target is reset. It is measured on the
// supervisor clock, so tests can drive it without sleeping.
const stableRecoveryWindow = 5 * time.Minute

// defaultExpiryLockBudget bounds how long the local expiry pass waits for an
// in-flight apply before it stops the expired instances directly.
const defaultExpiryLockBudget = 5 * time.Second

func (s *Supervisor) expiryLockBudgetValue() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expiryLockBudget > 0 {
		return s.expiryLockBudget
	}
	return defaultExpiryLockBudget
}

// SetExpiryLockBudget overrides how long the local expiry pass waits for the
// apply serialization gate (tests and startup wiring only).
func (s *Supervisor) SetExpiryLockBudget(d time.Duration) {
	s.mu.Lock()
	s.expiryLockBudget = d
	s.mu.Unlock()
}

func (s *Supervisor) protectionError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closing:
		return fmt.Errorf("agent shutting down")
	case s.queueFull:
		return fmt.Errorf("local upload queue over capacity")
	case s.persistenceErr != "":
		return fmt.Errorf("local persistence unavailable: %s", s.persistenceErr)
	}
	return nil
}

// CheckStart reports whether the agent may start a new core process right now.
// The manager calls it immediately before every Start, so a protection state
// that engaged in the middle of an apply is a real start gate and not only an
// asynchronous stop that runs after a core was already started.
func (s *Supervisor) CheckStart() error { return s.protectionError() }

// Recovery requires a successful WRITE and a fresh capacity check. Never clear
// a disk failure just because reads (or an HTTP upload) happened to succeed.
func (s *Supervisor) refreshProtection(ctx context.Context) error {
	depth, err := s.store.QueueDepth(ctx)
	if err != nil {
		s.setPersistenceError(err)
		return err
	}
	s.setQueueOverCapacity(depth.OverCapacity())
	if depth.OverCapacity() {
		return s.protectionError()
	}
	if err = s.store.ProbeWritable(ctx); err != nil {
		s.setPersistenceError(err)
		return err
	}
	s.mu.Lock()
	s.persistenceErr = ""
	s.mu.Unlock()
	return s.protectionError()
}

// healthOnce reconciles committed desired state without requiring a new panel
// revision. It never widens authorization from an interrupted apply: intent is
// replayed before any old record can be used. Per-target retries are capped,
// exponentially delayed, and reset only by a changed target or a stable core.
func (s *Supervisor) healthOnce(ctx context.Context) error {
	if err := s.refreshProtection(ctx); err != nil {
		return err
	}
	intent, err := s.store.ApplyIntent(ctx)
	if err != nil {
		return err
	}
	if intent != nil {
		return s.applySnapshot(ctx, intent)
	}
	s.applyMu.lock()
	defer s.applyMu.unlock()
	if err := s.protectionError(); err != nil {
		return err
	}
	reapErr := s.mgr.ReapUntracked(ctx)
	records, err := s.store.Instances(ctx)
	if err != nil {
		s.setPersistenceError(err)
		return err
	}
	now := s.now()
	var targets []xray.Target
	skip := map[string]bool{}
	attempted := map[string]bool{}
	for _, rec := range records {
		if rec.State == xray.StateStopping {
			res, tracked := s.mgr.Stop(ctx, rec.NodeID, "pending deletion")
			if (!tracked && reapErr == nil) || (tracked && res.State == xray.StateStopped) {
				if err := s.store.RemoveInstance(ctx, rec.NodeID); err != nil {
					s.setPersistenceError(err)
					return err
				}
			}
			continue
		}
		target, terr := s.targetFromRecord(rec, now)
		if terr != nil {
			s.mgr.Stop(ctx, rec.NodeID, "unrecoverable committed record")
			continue
		}
		targets = append(targets, target)
		if !s.mgr.NeedsRecovery(rec.NodeID) {
			skip[rec.NodeID] = true
			if since := s.mgr.RunningSince(rec.NodeID); !since.IsZero() && now.Sub(since) >= stableRecoveryWindow {
				delete(s.recovery, rec.NodeID)
			}
			continue
		}
		f := s.recovery[rec.NodeID]
		if f == nil || f.hash != target.Hash {
			f = &failure{hash: target.Hash}
			s.recovery[rec.NodeID] = f
		}
		if f.count >= maxRecoveryAttempts || now.Before(f.next) {
			skip[rec.NodeID] = true
			continue
		}
		f.count++
		delay := failureBackoffBase * time.Duration(1<<uint(f.count-1))
		if delay > failureBackoffMax {
			delay = failureBackoffMax
		}
		f.next = now.Add(delay)
		attempted[rec.NodeID] = true
	}
	if len(attempted) == 0 {
		return reapErr
	}
	byID := map[string]xray.Target{}
	for _, t := range targets {
		byID[t.InstanceID] = t
	}
	results := s.mgr.Apply(ctx, targets, xray.ApplyOptions{Skip: skip, PerInstanceTimeout: s.instanceBudget(), BeforeStop: s.sampleHook()})
	for _, res := range results {
		if !attempted[res.InstanceID] {
			continue
		}
		if err := s.persistResult(ctx, res, byID[res.InstanceID], now); err != nil {
			return err
		}
	}
	return reapErr
}

// stopExpiredWithoutLock is the fail-closed fallback of the local expiry loop: the
// apply serialization gate was not available inside the expiry deadline, so the
// expired cores are stopped directly instead of staying authorized until an
// unrelated technical apply finishes.
//
// It never rewrites the instance records: an apply is currently allowed to own
// them. The stopped state is therefore recorded on the next expiry pass (the
// manager is not running those instances any more, so its next rebuild is a fresh
// start) and an interrupted record still describes the pre-stop authorization -
// which is why the cores are stopped rather than merely marked.
func (s *Supervisor) stopExpiredWithoutLock(ctx context.Context, expired []string, lockErr error) error {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	var stopped []string
	results := make([]xray.Result, len(expired))
	var wg sync.WaitGroup
	for i, id := range expired {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			results[i], _ = s.mgr.StopExpired(stopCtx, id, s.now())
		}(i, id)
	}
	wg.Wait()
	for i, id := range expired {
		res := results[i]
		if res.State == xray.StateFailed {
			s.log.Printf("agent: ALERT: expired instance %s could not be stopped: %s", id, res.Error)
			continue
		}
		stopped = append(stopped, id)
	}
	if len(stopped) > 0 {
		s.log.Printf("agent: ALERT: stopped locally expired instance(s) %v without waiting for the apply gate: %v", stopped, lockErr)
	}
	// The next pass sees no expired instance any more, but the records still hold
	// the pre-stop authorization. Persist the narrowed record now: the write is a
	// single statement per instance and cannot widen anything.
	records, err := s.store.Instances(stopCtx)
	if err != nil {
		s.setPersistenceError(err)
		return err
	}
	now := s.now()
	for _, rec := range records {
		if !containsID(stopped, rec.NodeID) {
			continue
		}
		filtered, nextExpiry := filterExpiredUsers(rec.Users, now)
		// A concurrently committed extension/new authorization may already have
		// replaced the expired record. Never mark that healthy replacement down.
		if len(filtered) == len(rec.Users) {
			continue
		}
		narrowed, err := s.store.NarrowExpiredInstance(stopCtx, state.InstanceRecord{
			NodeID:           rec.NodeID,
			Users:            filtered,
			UsersBefore:      rec.Users,
			AppliedUserCount: len(filtered),
			NextExpiry:       nextExpiry,
			State:            xray.StateFailed,
			Error:            "local expiry reached; core stopped while the apply gate was busy",
			UpdatedAt:        now,
		})
		if err != nil {
			s.setPersistenceError(err)
			return err
		}
		if !narrowed {
			s.log.Printf("agent: instance %s: the record changed while the expiry fallback stopped it; keeping the newer record", rec.NodeID)
		}
	}
	return fmt.Errorf("local expiry could not serialize with the apply gate: %w", lockErr)
}

func containsID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
