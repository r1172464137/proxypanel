package xray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"proxypanel/internal/contracts"
)

// This file is the regression suite for P0-4: concurrent `xray -test` runs
// block each other inside the core's own StatsService.
//
// Measured on the real binary: one `xray -test` takes ~26ms, five concurrent runs
// take ~116s each (all exit 0). Removing the api/stats/policy sections made the
// concurrent run fast again, which is why `xray -test` is now serialized per
// agent. The same congestion produced the historical "xray -test failed: signal:
// killed" and "core management API did not answer within 5s" reports.
//
// Covered here:
//
//   - five instances that validate concurrently really run one test at a time
//     (peak concurrency == 1) and the five tests are not skipped (total time
//     ~= 5 x testDelay, test calls == 5);
//   - a wait for the serialized slot is bounded by the validation budget, so a
//     queued instance gives up instead of testing after it was reported late;
//   - a validation timeout is not a verdict about the document: the instance is
//     not marked failed, the running core keeps serving and the next poll
//     retries, while a real rejection still rolls back.

// validateProbeRunner is a Runner that records how many Test calls run at the
// same time (peak) and can hold a call open, so serialization is asserted
// instead of assumed.
type validateProbeRunner struct {
	mu     sync.Mutex
	testFn func(ctx context.Context, configPath string) error

	inFlight atomic.Int32
	peak     atomic.Int32
	calls    atomic.Int32
	starts   atomic.Int32
	stops    atomic.Int32
	nextPID  atomic.Int32
}

func (r *validateProbeRunner) setTestFn(fn func(ctx context.Context, configPath string) error) {
	r.mu.Lock()
	r.testFn = fn
	r.mu.Unlock()
}

func (r *validateProbeRunner) Test(ctx context.Context, configPath string) error {
	running := r.inFlight.Add(1)
	defer r.inFlight.Add(-1)
	for {
		peak := r.peak.Load()
		if running <= peak || r.peak.CompareAndSwap(peak, running) {
			break
		}
	}
	r.calls.Add(1)
	r.mu.Lock()
	fn := r.testFn
	r.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, configPath)
}

func (r *validateProbeRunner) Start(ctx context.Context, spec StartSpec) (Proc, error) {
	r.starts.Add(1)
	return &validateProbeProc{
		runner: r,
		pid:    800000 + int(r.nextPID.Add(1)),
		exited: make(chan struct{}),
	}, nil
}

func (r *validateProbeRunner) StatsQuery(ctx context.Context, server string) ([]StatCounter, error) {
	return nil, nil
}

func (r *validateProbeRunner) Version(ctx context.Context) (string, error) { return "26.3.27", nil }

func (r *validateProbeRunner) peakConcurrency() int { return int(r.peak.Load()) }
func (r *validateProbeRunner) testCalls() int       { return int(r.calls.Load()) }
func (r *validateProbeRunner) startCount() int      { return int(r.starts.Load()) }
func (r *validateProbeRunner) stopCount() int       { return int(r.stops.Load()) }

type validateProbeProc struct {
	runner *validateProbeRunner
	pid    int
	exited chan struct{}
	once   sync.Once
}

func (p *validateProbeProc) PID() int                { return p.pid }
func (p *validateProbeProc) Exited() <-chan struct{} { return p.exited }
func (p *validateProbeProc) ExitError() error        { return nil }
func (p *validateProbeProc) Output() []string        { return nil }
func (p *validateProbeProc) Stop(ctx context.Context) error {
	p.once.Do(func() {
		close(p.exited)
		p.runner.stops.Add(1)
	})
	return nil
}

// sleepTest returns a Test function that occupies the test slot for d (or less
// when the budget ends first), like a real `xray -test` run.
func sleepTest(d time.Duration) func(ctx context.Context, configPath string) error {
	return func(ctx context.Context, configPath string) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func newValidateProbeManager(t *testing.T) (*Manager, *validateProbeRunner) {
	t.Helper()
	runner := &validateProbeRunner{}
	mgr, err := NewManager(t.TempDir(), filepath.Join(t.TempDir(), "xray.exe"), runner, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	var portMu sync.Mutex
	port := 25000
	mgr.SetPortAllocator(func(map[int]bool) (int, error) {
		portMu.Lock()
		defer portMu.Unlock()
		port++
		return port, nil
	})
	return mgr, runner
}

// validateTarget builds a valid target for the given instance id.
func validateTarget(t *testing.T, id string, port int, emails ...string) Target {
	t.Helper()
	users := make([]contracts.AuthorizedUser, 0, len(emails))
	for i, e := range emails {
		users = append(users, contracts.AuthorizedUser{MemberID: fmt.Sprintf("%s-m%d", id, i+1), Email: e})
	}
	inst := contracts.Instance{
		ID: id, NodeID: id,
		Config: json.RawMessage(baseDocument(port, emails...)),
		Users:  users,
	}
	plan, err := Prepare(inst, Options{CertRoot: t.TempDir(), Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("Prepare(%s): %v", id, err)
	}
	return Target{
		InstanceID:    plan.InstanceID,
		NodeID:        plan.NodeID,
		Document:      plan.Document,
		Hash:          plan.Hash,
		Authorization: plan.Authorization,
		EmailToMember: plan.EmailToMember,
		Users:         plan.EffectiveUsers,
		NextExpiry:    plan.NextExpiry,
	}
}

// TestConcurrentConfigValidationIsSerialized is the P0-4 regression: five
// instances validating at the same time must run exactly one `xray -test` at a
// time, and every one of the five must really run.
//
// The instances are applied through applyOne because Apply reconciles the whole
// instance set: five overlapping Apply calls would stop each other's instances,
// which is not the production trigger. In production the concurrent triggers are
// the snapshot loop, the local expiry rebuild loop and rollback validation, and
// they touch different instances - exactly what this test models.
func TestConcurrentConfigValidationIsSerialized(t *testing.T) {
	const instanceCount = 5
	const testDelay = 60 * time.Millisecond
	mgr, runner := newValidateProbeManager(t)
	mgr.SetTimeouts(Timeouts{
		Readiness: 10 * time.Second,
		Validate:  20 * time.Second,
		Stop:      5 * time.Second,
		Rollback:  20 * time.Second,
	})
	runner.setTestFn(sleepTest(testDelay))

	targets := make([]Target, instanceCount)
	for i := range targets {
		id := fmt.Sprintf("node-%d", i+1)
		targets[i] = validateTarget(t, id, 443+i, "alice@example.test")
	}
	results := make([]Result, instanceCount)
	var wg sync.WaitGroup
	begin := time.Now()
	for i := range targets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = mgr.applyOne(context.Background(), targets[i], ApplyOptions{
				PerInstanceTimeout: 60 * time.Second,
			})
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(begin)

	for i, res := range results {
		if res.State != StateApplied {
			t.Fatalf("instance %d: state = %q (%s), want %q", i, res.State, res.Error, StateApplied)
		}
	}

	// Serialization: never more than one `xray -test` in flight.
	if peak := runner.peakConcurrency(); peak != 1 {
		t.Fatalf("peak concurrent `xray -test` runs = %d, want 1 (serialization is broken); "+
			"concurrent runs block each other inside the core StatsService (~116s instead of 26ms)", peak)
	}
	// No fake pass: all five validations really ran (one test per instance, no
	// rollback and no retry), so the serial time is the sum of five tests.
	if calls := runner.testCalls(); calls != instanceCount {
		t.Fatalf("`xray -test` ran %d time(s), want %d", calls, instanceCount)
	}
	// Serial cost: ~5 x testDelay. Running them in parallel would finish in about
	// one testDelay; running them not at all in about zero.
	minSerial := time.Duration(instanceCount-1) * testDelay
	maxSerial := time.Duration(instanceCount)*testDelay + 5*time.Second
	if elapsed < minSerial {
		t.Fatalf("five serialized validations took %s, want at least %s (5 x %s): "+
			"the tests appear to have run in parallel", elapsed, minSerial, testDelay)
	}
	if elapsed > maxSerial {
		t.Fatalf("five serialized validations took %s, want at most %s", elapsed, maxSerial)
	}
	if starts := runner.startCount(); starts != instanceCount {
		t.Fatalf("%d core(s) started, want %d", starts, instanceCount)
	}
	t.Logf("five serialized validations: peak=%d, calls=%d, elapsed=%s (5 x %s = %s)",
		runner.peakConcurrency(), runner.testCalls(), elapsed.Round(time.Millisecond),
		testDelay, time.Duration(instanceCount)*testDelay)
}

// TestValidationWaitIsBoundedByItsOwnBudget proves the wait for the serialized
// slot inherits the validation budget: a queued instance must not start a test
// after its budget is gone, otherwise it would report a result nobody waits for
// and keep the slot busy for the next instance.
func TestValidationWaitIsBoundedByItsOwnBudget(t *testing.T) {
	mgr, runner := newValidateProbeManager(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseSlot := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseSlot)
	runner.setTestFn(func(ctx context.Context, configPath string) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	holderDone := make(chan error, 1)
	go func() {
		holderDone <- mgr.validateWithBudget(context.Background(), "holder.json", 20*time.Second)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first validation never entered the runner")
	}

	const budget = 150 * time.Millisecond
	begin := time.Now()
	err := mgr.validateWithBudget(context.Background(), "queued.json", budget)
	elapsed := time.Since(begin)
	if !errors.Is(err, ErrValidationTimeout) {
		t.Fatalf("a queued validation must report ErrValidationTimeout, got %v", err)
	}
	if elapsed < budget/2 {
		t.Fatalf("the queued validation gave up after %s, before its %s budget", elapsed, budget)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the queued validation waited %s for a %s budget: the wait is not bounded", elapsed, budget)
	}
	if calls := runner.testCalls(); calls != 1 {
		t.Fatalf("`xray -test` ran %d time(s); a timed out waiter must not spawn a test process", calls)
	}

	releaseSlot()
	select {
	case herr := <-holderDone:
		if herr != nil {
			t.Fatalf("the slot holder failed: %v", herr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the slot holder never finished")
	}
	if calls := runner.testCalls(); calls != 1 {
		t.Fatalf("`xray -test` ran %d time(s) in total, want 1", calls)
	}
}

// TestValidationTimeoutDoesNotFailTheInstance proves a validation timeout is not
// treated as "the configuration is invalid": the healthy core keeps serving, the
// instance keeps its applied state and the next poll retries. A real rejection is
// still a rejection and still rolls back.
func TestValidationTimeoutDoesNotFailTheInstance(t *testing.T) {
	mgr, runner := newValidateProbeManager(t)
	first := validateTarget(t, "node-1", 443, "alice@example.test")
	if results := mgr.Apply(context.Background(), []Target{first}, ApplyOptions{}); results[0].State != StateApplied {
		t.Fatalf("initial apply: %+v", results[0])
	}
	if starts := runner.startCount(); starts != 1 {
		t.Fatalf("initial apply started %d core(s), want 1", starts)
	}

	// Hold the serialized test slot exactly like another instance's in-flight
	// `xray -test` would.
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseSlot := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseSlot)
	held := make(chan struct{})
	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		if err := mgr.validateMu.lock(context.Background()); err != nil {
			t.Errorf("acquire the validation slot: %v", err)
			return
		}
		close(held)
		<-release
		mgr.validateMu.unlock()
	}()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("the validation slot was never held")
	}

	mgr.SetTimeouts(Timeouts{
		Readiness: 10 * time.Second,
		Validate:  150 * time.Millisecond,
		Stop:      5 * time.Second,
		Rollback:  10 * time.Second,
	})
	changed := validateTarget(t, "node-1", 443, "alice@example.test", "bob@example.test")
	begin := time.Now()
	results := mgr.Apply(context.Background(), []Target{changed}, ApplyOptions{PerInstanceTimeout: 30 * time.Second})
	elapsed := time.Since(begin)
	if len(results) != 1 {
		t.Fatalf("unexpected result count: %+v", results)
	}
	if results[0].State != StateDeferred {
		t.Fatalf("a validation timeout must defer the instance, got %+v (want state %q)", results[0], StateDeferred)
	}
	if !strings.Contains(results[0].Error, ErrValidationTimeout.Error()) {
		t.Fatalf("the deferred result must name the timeout, got %q", results[0].Error)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("the deferred validation took %s", elapsed)
	}
	// The committed state is untouched: still the applied target with one user,
	// not failed and not rolled back.
	if results[0].AppliedUserCount != 1 {
		t.Fatalf("a deferred validation must keep the applied user count, got %d", results[0].AppliedUserCount)
	}
	if snapshot := mgr.Snapshot(); len(snapshot) != 1 || snapshot[0].State != StateApplied {
		t.Fatalf("the instance must keep serving the old configuration, snapshot = %+v", snapshot)
	}
	if starts := runner.startCount(); starts != 1 {
		t.Fatalf("%d core start(s): a validation timeout must not restart the core", starts)
	}
	if stops := runner.stopCount(); stops != 0 {
		t.Fatalf("%d core stop(s): a validation timeout must not stop the healthy core", stops)
	}

	releaseSlot()
	select {
	case <-holderDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the slot holder never finished")
	}

	// Contrast: a real rejection is still a rejection and rolls back to the last
	// known good document. Only the timeout is special.
	runner.setTestFn(func(ctx context.Context, configPath string) error {
		if filepath.Base(configPath) == "config.rollback.json" {
			return nil
		}
		return errors.New("xray -test failed: unknown field at inbounds[1]")
	})
	rejected := validateTarget(t, "node-1", 443, "alice@example.test", "bob@example.test", "carol@example.test")
	results = mgr.Apply(context.Background(), []Target{rejected}, ApplyOptions{PerInstanceTimeout: 30 * time.Second})
	if len(results) != 1 || results[0].State != StateRollback {
		t.Fatalf("a rejected configuration must still roll back, got %+v", results)
	}
}
