package xray

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubbornProc is a Proc that refuses to die: Stop returns an error and the exit
// channel never closes. It models an OS level stop failure without touching any
// real process.
type stubbornProc struct {
	pid    int
	exited chan struct{}
	once   sync.Once
	// stops counts Stop calls.
	stops atomic.Int32
}

func (p *stubbornProc) PID() int                { return p.pid }
func (p *stubbornProc) Exited() <-chan struct{} { return p.exited }
func (p *stubbornProc) ExitError() error        { return nil }
func (p *stubbornProc) Output() []string        { return []string{"injected survivor"} }
func (p *stubbornProc) Stop(ctx context.Context) error {
	p.stops.Add(1)
	return errors.New("injected stop failure")
}

// stubbornRunner starts stubbornProc processes and reports every pid as alive
// through the caller installed process table.
type stubbornRunner struct {
	*fakeRunner
	mu     sync.Mutex
	surviv map[int]bool
}

func newStubbornRunner() *stubbornRunner {
	return &stubbornRunner{fakeRunner: newFakeRunner(), surviv: map[int]bool{}}
}

func (r *stubbornRunner) Start(ctx context.Context, spec StartSpec) (Proc, error) {
	proc, err := r.fakeRunner.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	fake := proc.(*fakeProc)
	r.mu.Lock()
	r.surviv[fake.pid] = true
	r.mu.Unlock()
	// A survivor keeps its exit channel open for the whole test: no Stop ever
	// closes it.
	return &stubbornProc{pid: fake.pid, exited: fake.exited}, nil
}

// newStubbornManager builds a manager whose pid inspection reports the stub pids
// as alive. Ownership can never be proven for them (no recorded creation
// identity), so no kill is ever attempted.
func newStubbornManager(t *testing.T) (*Manager, *stubbornRunner) {
	t.Helper()
	runner := newStubbornRunner()
	mgr, err := NewManager(t.TempDir(), filepath.Join(t.TempDir(), "xray.exe"), runner, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	port := 22000
	mgr.SetPortAllocator(func(map[int]bool) (int, error) {
		port++
		return port, nil
	})
	mgr.SetInspectProcess(func(pid int) (ProcessIdentity, bool, error) {
		runner.mu.Lock()
		defer runner.mu.Unlock()
		if runner.surviv[pid] {
			return ProcessIdentity{PID: pid}, true, nil
		}
		return ProcessIdentity{}, false, nil
	})
	return mgr, runner
}

// TestReviewStopNotConfirmedKeepsTracking pins C09: a stop that could not be
// confirmed must keep the tracking record, must keep the pid file for the next
// ownership check, and must never claim the old connections were terminated.
func TestReviewStopNotConfirmedKeepsTracking(t *testing.T) {
	mgr, _ := newStubbornManager(t)
	ctx := context.Background()
	target := targetFor(t, "alice@example.test")
	if res := mgr.Apply(ctx, []Target{target}, ApplyOptions{}); res[0].State != StateApplied {
		t.Fatalf("initial apply: %+v", res[0])
	}
	dir := filepath.Join(mgr.Root(), "instances", target.InstanceID)
	if _, err := os.Stat(filepath.Join(dir, pidFileName)); err != nil {
		t.Fatalf("the pid record must exist after the start: %v", err)
	}

	res, tracked := mgr.Stop(ctx, target.InstanceID, "node removed")
	if !tracked {
		t.Fatal("the instance disappeared from the manager before the stop was confirmed")
	}
	if res.State != StateFailed {
		t.Fatalf("an unconfirmed stop must be reported as failed, got %q", res.State)
	}
	if res.OldConnectionsTerminated {
		t.Fatal("an unconfirmed stop claimed the old connections were terminated")
	}
	if mgr.lookup(target.InstanceID) == nil {
		t.Fatal("the manager dropped the tracking record of a surviving core")
	}
	if _, err := os.Stat(filepath.Join(dir, pidFileName)); err != nil {
		t.Fatalf("the pid record of a surviving core was deleted: %v", err)
	}
	for _, inst := range mgr.Snapshot() {
		if inst.InstanceID == target.InstanceID && inst.State == StateStopped {
			t.Fatalf("the surviving core is advertised as stopped: %+v", inst)
		}
	}
}

// TestReviewSurvivingCoreBlocksReplacement pins the second half of C09: a new
// configuration must not be started on top of a core that is still running.
func TestReviewSurvivingCoreBlocksReplacement(t *testing.T) {
	mgr, runner := newStubbornManager(t)
	ctx := context.Background()
	first := targetFor(t, "alice@example.test")
	if res := mgr.Apply(ctx, []Target{first}, ApplyOptions{}); res[0].State != StateApplied {
		t.Fatalf("initial apply: %+v", res[0])
	}
	startsAfterFirst := runner.startedCount()
	second := targetFor(t, "alice@example.test", "bob@example.test")
	res := mgr.Apply(ctx, []Target{second}, ApplyOptions{})
	if len(res) != 1 {
		t.Fatalf("unexpected results: %+v", res)
	}
	if res[0].State != StateFailed {
		t.Fatalf("a replacement must fail while the old core survives, got %+v", res[0])
	}
	if res[0].OldConnectionsTerminated {
		t.Fatal("a failed replacement claimed the old connections were terminated")
	}
	if got := runner.startedCount(); got != startsAfterFirst {
		t.Fatalf("a new core was started while the old one survives: %d -> %d", startsAfterFirst, got)
	}
	if mgr.lookup(first.InstanceID) == nil {
		t.Fatal("the surviving core lost its tracking record")
	}
}

// TestReviewStartGuardBlocksStart pins C05 at the manager boundary: the last
// moment gate runs immediately before every Start and a veto must never result in
// a started process.
func TestReviewStartGuardBlocksStart(t *testing.T) {
	mgr, runner := newTestManager(t)
	target := targetFor(t, "alice@example.test")
	mgr.SetStartGuard(func() error { return errors.New("local persistence unavailable") })
	results := mgr.Apply(context.Background(), []Target{target}, ApplyOptions{})
	if len(results) != 1 || results[0].State != StateFailed {
		t.Fatalf("a vetoed start must be reported as failed: %+v", results)
	}
	if !strings.Contains(results[0].Error, "safety gate") {
		t.Fatalf("the failure must name the gate: %q", results[0].Error)
	}
	if runner.startedCount() != 0 {
		t.Fatalf("%d core process(es) were started although the gate vetoed the start", runner.startedCount())
	}

	// A cleared gate lets the very same target start.
	mgr.SetStartGuard(nil)
	results = mgr.Apply(context.Background(), []Target{target}, ApplyOptions{})
	if results[0].State != StateApplied || runner.startedCount() != 1 {
		t.Fatalf("the gate did not clear: %+v starts=%d", results[0], runner.startedCount())
	}
}

// TestReviewReapUntrackedKeepsUnverifiedRecord pins the removal cleanup path: a
// crash can leave only a pid file, and a record that cannot be proven ours must
// be kept (and reported) instead of being deleted.
func TestReviewReapUntrackedKeepsUnverifiedRecord(t *testing.T) {
	mgr, runner := newStubbornManager(t)
	ctx := context.Background()
	target := targetFor(t, "alice@example.test")
	if res := mgr.Apply(ctx, []Target{target}, ApplyOptions{}); res[0].State != StateApplied {
		t.Fatalf("apply: %+v", res[0])
	}
	// Simulate a crash: the manager forgets the instance (map and owned pid set)
	// but the pid record on disk stays.
	mgr.mu.Lock()
	delete(mgr.instances, target.InstanceID)
	for pid := range mgr.owned {
		delete(mgr.owned, pid)
	}
	mgr.mu.Unlock()
	_ = runner

	err := mgr.ReapUntracked(ctx)
	if err == nil {
		t.Fatal("an unverifiable leftover record must be reported, not silently dropped")
	}
	if !errors.Is(err, ErrUnverifiedProcess) {
		t.Fatalf("unexpected error: %v", err)
	}
	dir := filepath.Join(mgr.Root(), "instances", target.InstanceID)
	if _, serr := os.Stat(filepath.Join(dir, pidFileName)); serr != nil {
		t.Fatalf("the unverified pid record was deleted: %v", serr)
	}
}

// TestReviewExpiredInstanceStopIsFailClosed pins C03 at the manager boundary: a
// narrowing authorization is enforced even when the new config can not be
// validated, and the deferred result never reports the old connections as
// terminated-or-alive ambiguously.
func TestReviewExpiredInstanceStopIsFailClosed(t *testing.T) {
	mgr, runner := newValidateProbeManager(t)
	ctx := context.Background()
	first := validateTarget(t, "node-1", 443, "a@example.test", "b@example.test")
	if res := mgr.Apply(ctx, []Target{first}, ApplyOptions{}); res[0].State != StateApplied {
		t.Fatalf("initial apply: %+v", res[0])
	}
	mgr.SetTimeouts(Timeouts{Readiness: 10 * time.Second, Validate: 10 * time.Millisecond, Stop: 5 * time.Second, Rollback: 5 * time.Second})
	runner.setTestFn(func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() })

	// Repeated timeouts must not keep the revoked user authorized.
	narrowed := validateTarget(t, "node-1", 443, "a@example.test")
	for i := 0; i < 3; i++ {
		res := mgr.Apply(ctx, []Target{narrowed}, ApplyOptions{PerInstanceTimeout: 30 * time.Second})
		if len(res) != 1 {
			t.Fatalf("unexpected result count: %+v", res)
		}
		if res[0].State == StateApplied || res[0].State == StateUnchanged {
			t.Fatalf("attempt %d claimed the narrowed authorization was applied: %+v", i, res[0])
		}
		for _, inst := range mgr.Snapshot() {
			if inst.State == StateApplied && inst.AppliedUserCount > 1 {
				t.Fatalf("attempt %d kept the revoked user authorized: %+v", i, inst)
			}
		}
	}
	if runner.stopCount() < 1 {
		t.Fatal("the revoked authorization was never stopped")
	}
	for _, inst := range mgr.Snapshot() {
		if inst.State == StateApplied {
			t.Fatalf("a core is still advertised as applied after the revocation: %+v", inst)
		}
	}
}

// TestReviewFinalSampleRunsBeforeEveryStop pins C08 at the manager boundary: the
// sample hook runs on the shutdown path and on the rejected-configuration path,
// and it runs exactly once per stopped core.
func TestReviewFinalSampleRunsBeforeEveryStop(t *testing.T) {
	mgr, runner := newTestManager(t)
	ctx := context.Background()
	var mu sync.Mutex
	stopped := 0
	hook := func(hctx context.Context, nodeID, gen, api string, emails map[string]string) {
		mu.Lock()
		stopped++
		mu.Unlock()
	}
	mgr.SetSampleHook(hook)
	target := targetFor(t, "alice@example.test")
	if res := mgr.Apply(ctx, []Target{target}, ApplyOptions{}); res[0].State != StateApplied {
		t.Fatalf("apply: %+v", res[0])
	}
	mgr.StopAll(ctx)
	mu.Lock()
	count := stopped
	mu.Unlock()
	if count != 1 {
		t.Fatalf("the shutdown stop ran the final sample %d time(s), want 1", count)
	}
	if runner.startedCount() != 1 {
		t.Fatalf("unexpected starts: %d", runner.startedCount())
	}

	// The rejected-configuration path must sample too.
	mgr2, _ := newTestManager(t)
	mgr2.SetSampleHook(hook)
	if res := mgr2.Apply(ctx, []Target{target}, ApplyOptions{}); res[0].State != StateApplied {
		t.Fatalf("second apply: %+v", res[0])
	}
	if _, ok := mgr2.Stop(ctx, target.InstanceID, "configuration rejected: bad"); !ok {
		t.Fatal("the instance was not tracked")
	}
	mu.Lock()
	count = stopped
	mu.Unlock()
	if count != 2 {
		t.Fatalf("the rejected stop ran the final sample %d time(s) in total, want 2", count)
	}
}
