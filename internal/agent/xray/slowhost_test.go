package xray

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// This file is the "slow host" regression suite: it proves the two lifecycle
// defects that were only ever covered by unit-level assertions really heal on a
// machine where the management API answers late and where a failed apply leaves a
// core process behind.
//
// Every budget is injected through Manager.SetTimeouts (which goes through
// Timeouts.withDefaults), so no assertion depends on wall clock gambling: the
// longest test waits ~1.2s and the whole file stays well below 5s. The orphan
// cases use a real child process (the test binary in sleep mode) that really holds
// the inbound port, so the pid record, the ownership verification and the port
// release are exercised for real.
//
// Covered:
//
//   - a management API that keeps refusing the probe and only answers later must
//     end in applied (not failed) while the readiness budget lasts;
//   - an exhausted readiness budget must fail the instance in bounded time
//     instead of hanging, and must not leave the started core as an orphan;
//   - the rollback_applied wedge must heal on the next regular apply, with no
//     pkill / agent restart / operator action;
//   - an orphan core (live process, instance already failed) must be reclaimed by
//     the next attempt so its inbound port is released.

// survivorProc models a core that ignores the stop request and keeps its inbound
// port: the real orphan seen after a failed apply. pid is a real live process
// (see startPortHoldingHelper) so the durable pid record and the ownership
// verification are exercised for real instead of being faked.
type survivorProc struct {
	runner *scriptedRunner
	pid    int
	exited chan struct{}
	mu     sync.Mutex
	stops  int
}

func (p *survivorProc) PID() int                { return p.pid }
func (p *survivorProc) Exited() <-chan struct{} { return p.exited }
func (p *survivorProc) ExitError() error        { return nil }

func (p *survivorProc) Output() []string {
	return []string{"[Info] core: Xray 26.3.27 started"}
}

// Stop records the attempt but leaves the process alive, exactly like a core
// that ignores SIGTERM on a slow host.
func (p *survivorProc) Stop(ctx context.Context) error {
	p.mu.Lock()
	p.stops++
	p.mu.Unlock()
	p.runner.recordEvent(fmt.Sprintf("stop:%d", p.pid))
	return nil
}

func (p *survivorProc) stopCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stops
}

// freeListenAddress returns an address that was just free on the loopback
// interface.
func freeListenAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(APIListen, "0"))
	if err != nil {
		t.Fatalf("pick a free port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close the probe listener: %v", err)
	}
	return addr
}

// portIsBusy reports whether the address cannot be bound, i.e. a process still
// holds it.
func portIsBusy(t *testing.T, addr string) bool {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}

// startPortHoldingHelper starts the sleeping test helper with the listening
// socket of addr inherited, so the child process really holds that inbound port
// and it stays busy for as long as the child lives.
func startPortHoldingHelper(t *testing.T, cfgPath, addr string) *exec.Cmd {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("bind the inbound port %s: %v", addr, err)
	}
	tcpLn := ln.(*net.TCPListener)
	f, err := tcpLn.File()
	if err != nil {
		tcpLn.Close()
		t.Fatalf("dup the listening socket: %v", err)
	}
	cmd := exec.Command(testBinaryPath(t), cfgPath)
	cmd.Env = append(os.Environ(), procRoleEnv+"=sleep")
	// fd 3 in the child is the listening socket.
	cmd.ExtraFiles = []*os.File{f}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		f.Close()
		tcpLn.Close()
		t.Fatalf("start the port holding helper: %v", err)
	}
	// Only the child keeps the socket open from now on.
	f.Close()
	tcpLn.Close()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if !portIsBusy(t, addr) {
		t.Fatalf("the helper process does not hold %s", addr)
	}
	return cmd
}

// waitForExit waits (bounded) for a helper process to disappear.
func waitForExit(t *testing.T, cmd *exec.Cmd, within time.Duration) bool {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(within):
		return false
	}
}

// --- slow management API ------------------------------------------------------

// TestSlowHostManagementAPIThatAnswersLateIsApplied is the production scenario:
// the core logged "started" but its stats service needs more than a moment before
// it answers. Every probe fails until the recovery point and the instance must
// still reach applied within the configured readiness budget (before the fix the
// 5s hard coded budget failed it).
func TestSlowHostManagementAPIThatAnswersLateIsApplied(t *testing.T) {
	mgr, runner := newScriptedManager(t, filepath.Join(t.TempDir(), "xray.exe"), t.TempDir())
	// 2s is well above the 0.9s recovery point but small enough for a fast test.
	mgr.SetTimeouts(Timeouts{
		Readiness: 2 * time.Second,
		Validate:  2 * time.Second,
		Stop:      time.Second,
		Rollback:  2 * time.Second,
	})
	started := time.Now()
	recoverAt := 900 * time.Millisecond
	runner.setStatsFn(func(ctx context.Context, server string) ([]StatCounter, error) {
		if time.Since(started) < recoverAt {
			return nil, fmt.Errorf("dial tcp %s: connect: connection refused", server)
		}
		return nil, nil
	})
	begin := time.Now()
	results := mgr.Apply(context.Background(), []Target{targetFor(t, "alice@example.test")}, ApplyOptions{})
	elapsed := time.Since(begin)
	if len(results) != 1 || results[0].State != StateApplied {
		t.Fatalf("a management API that answers late must still be applied, got %+v", results)
	}
	if elapsed < recoverAt {
		t.Fatalf("the instance was declared applied after %s, before the API could have answered (%s)", elapsed, recoverAt)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("the readiness wait took %s, longer than the 2s budget", elapsed)
	}
	if calls := runner.statsCount(); calls < 3 {
		t.Fatalf("the management API was probed %d time(s); a slow host needs repeated polling", calls)
	}
	if got := mgr.Snapshot(); len(got) != 1 || got[0].State != StateApplied {
		t.Fatalf("snapshot reports %+v, want applied", got)
	}
	if _, err := os.Stat(filepath.Join(mgr.Root(), "instances", "node-1", pidFileName)); err != nil {
		t.Fatalf("the applied instance has no pid record: %v", err)
	}
}

// TestSlowHostReadinessBudgetBelowTheLegacyConstantIsEnforced pins the budget to
// the injected value: a core that needs 2s to answer must FAIL with a 1s budget
// even though the legacy hard coded 5s constant (and the 30s default) would have
// applied it. The API stays unready for both start attempts (startInstance retries
// once with a fresh port), so the retry cannot mask the shortened budget.
func TestSlowHostReadinessBudgetBelowTheLegacyConstantIsEnforced(t *testing.T) {
	mgr, runner := newScriptedManager(t, filepath.Join(t.TempDir(), "xray.exe"), t.TempDir())
	budget := 600 * time.Millisecond
	mgr.SetTimeouts(Timeouts{
		Readiness: budget,
		Validate:  2 * time.Second,
		Stop:      time.Second,
		Rollback:  2 * time.Second,
	})
	var mu sync.Mutex
	attemptStart := time.Now()
	runner.setStartFn(func(spec StartSpec) (Proc, error) {
		mu.Lock()
		attemptStart = time.Now()
		mu.Unlock()
		return &scriptedProc{runner: runner, pid: 760000 + runner.statsCount(), exited: make(chan struct{})}, nil
	})
	runner.setStatsFn(func(ctx context.Context, server string) ([]StatCounter, error) {
		mu.Lock()
		age := time.Since(attemptStart)
		mu.Unlock()
		if age < 1300*time.Millisecond {
			return nil, fmt.Errorf("dial tcp %s: connect: connection refused", server)
		}
		return nil, nil
	})
	begin := time.Now()
	results := mgr.Apply(context.Background(), []Target{targetFor(t, "alice@example.test")}, ApplyOptions{})
	elapsed := time.Since(begin)
	if len(results) != 1 || results[0].State != StateFailed {
		t.Fatalf("a 1s readiness budget must not stretch to the legacy 5s constant, got %+v", results)
	}
	if !containsAll(results[0].Error, "within 600ms") {
		t.Fatalf("the error must report the injected 600ms budget: %q", results[0].Error)
	}
	// Two start attempts at most, so the wait is bounded by roughly 2 x budget.
	if elapsed > 6*budget {
		t.Fatalf("two bounded attempts took %s for a %s budget", elapsed, budget)
	}
}

// TestSlowHostReadinessBudgetExhaustionFailsInBoundedTime proves the other half
// of D1: when the API never answers the instance fails within (a small multiple
// of) the configured budget instead of hanging forever, and the started cores are
// stopped instead of being left behind.
func TestSlowHostReadinessBudgetExhaustionFailsInBoundedTime(t *testing.T) {
	mgr, runner := newScriptedManager(t, filepath.Join(t.TempDir(), "xray.exe"), t.TempDir())
	budget := 500 * time.Millisecond
	mgr.SetTimeouts(Timeouts{
		Readiness: budget,
		Validate:  2 * time.Second,
		Stop:      time.Second,
		Rollback:  time.Second,
	})
	runner.setStatsFn(func(ctx context.Context, server string) ([]StatCounter, error) {
		return nil, fmt.Errorf("dial tcp %s: connect: connection refused", server)
	})
	begin := time.Now()
	// The per instance budget is deliberately generous: the readiness budget must
	// be what ends the wait, not the outer deadline.
	results := mgr.Apply(context.Background(), []Target{targetFor(t, "alice@example.test")},
		ApplyOptions{PerInstanceTimeout: 30 * time.Second})
	elapsed := time.Since(begin)
	if len(results) != 1 || results[0].State != StateFailed {
		t.Fatalf("an API that never answers must fail the instance, got %+v", results)
	}
	if !containsAll(results[0].Error, "did not answer within", budget.String()) {
		t.Fatalf("the error must name the configured budget %s: %q", budget, results[0].Error)
	}
	// startInstance retries once with a fresh port, so two readiness budgets are
	// the upper bound; anything near the 30s outer budget means the wait hung.
	if elapsed < budget {
		t.Fatalf("gave up after %s, before the %s readiness budget", elapsed, budget)
	}
	if elapsed > 6*budget {
		t.Fatalf("the wait took %s for a %s budget: the readiness wait is not bounded", elapsed, budget)
	}
	if calls := runner.statsCount(); calls < 2 {
		t.Fatalf("probed %d time(s); the wait must poll until the budget is used up", calls)
	}
	if got := mgr.Snapshot(); len(got) != 1 || got[0].State != StateFailed {
		t.Fatalf("snapshot reports %+v, want failed", got)
	}
	// The cores that failed readiness must be stopped, not leaked as orphans.
	starts, stops := 0, 0
	for _, e := range runner.eventList() {
		switch {
		case strings.HasPrefix(e, "start:"):
			starts++
		case strings.HasPrefix(e, "stop:"):
			stops++
		}
	}
	if stops < starts || starts == 0 {
		t.Fatalf("%d start(s) but only %d stop(s): a core failed readiness and was leaked: %v",
			starts, stops, runner.eventList())
	}
	if _, err := os.Stat(filepath.Join(mgr.Root(), "instances", "node-1", pidFileName)); !os.IsNotExist(err) {
		t.Fatalf("a stopped core left its pid record behind: %v", err)
	}
}

// --- D2: the wedge heals without external intervention ------------------------

// TestRollbackAppliedWedgeHealsOnTheNextApply reproduces the real wedge: a new
// technical config is rejected, the last known good config is applied instead and
// the instance stays in rollback_applied. The next regular apply (a new snapshot
// or the supervisor retry) must leave that state on its own - no pkill, no agent
// restart.
func TestRollbackAppliedWedgeHealsOnTheNextApply(t *testing.T) {
	shortStopVerify(t)
	mgr, runner := newScriptedManager(t, filepath.Join(t.TempDir(), "xray.exe"), t.TempDir())
	if _, ok := applyOneHelper(t, mgr, targetFor(t, "alice@example.test", "bob@example.test")); !ok {
		t.Fatal("initial apply failed")
	}
	// Only the new technical config is rejected; the last known good one is fine,
	// which is what produces rollback_applied.
	runner.setTestFn(func(ctx context.Context, configPath string) error {
		if filepath.Base(configPath) == "config.rollback.json" {
			return nil
		}
		return errors.New("xray -test failed: unknown field at inbounds[1]")
	})
	wedged := mgr.Apply(context.Background(), []Target{targetFor(t, "alice@example.test")}, ApplyOptions{})
	if len(wedged) != 1 || wedged[0].State != StateRollback {
		t.Fatalf("expected the rollback_applied wedge, got %+v", wedged)
	}
	if got := mgr.Snapshot(); len(got) != 1 || got[0].State != StateRollback {
		t.Fatalf("snapshot must report the wedge state, got %+v", got)
	}
	wedgedGeneration := wedged[0].CoreGeneration
	if wedgedGeneration == "" {
		t.Fatal("the rollback core has no generation")
	}
	// Nothing external happens here: the core binary is fixed (testFn cleared) and
	// the very next apply must heal the instance.
	runner.setTestFn(nil)
	healed, ok := applyOneHelper(t, mgr, targetFor(t, "alice@example.test", "carol@example.test"))
	if !ok || healed.State != StateApplied {
		t.Fatalf("the wedged instance did not heal on the next apply: %+v", healed)
	}
	if healed.AppliedUserCount != 2 {
		t.Fatalf("healed instance applies %d user(s), want 2", healed.AppliedUserCount)
	}
	if healed.CoreGeneration == wedgedGeneration {
		t.Fatal("the healed instance still reports the rollback core generation")
	}
	if healed.OldConnectionsTerminated != true {
		t.Fatal("the healed instance must report the terminated old connections")
	}
	if got := mgr.Snapshot(); len(got) != 1 || got[0].State != StateApplied {
		t.Fatalf("snapshot still reports the wedge after healing: %+v", got)
	}
}

// TestFailedInstanceIsRetriedOnTheNextApply proves a failed instance is not
// sticky: the identical target is re-attempted (the failure clears the committed
// hash) and reaches applied without any operator action.
func TestFailedInstanceIsRetriedOnTheNextApply(t *testing.T) {
	shortStopVerify(t)
	mgr, runner := newScriptedManager(t, filepath.Join(t.TempDir(), "xray.exe"), t.TempDir())
	target := targetFor(t, "alice@example.test", "bob@example.test")
	if _, ok := applyOneHelper(t, mgr, target); !ok {
		t.Fatal("initial apply failed")
	}
	// Every configuration is rejected: no verified rollback either, so the
	// instance ends up failed.
	runner.setTestFn(func(ctx context.Context, configPath string) error {
		return errors.New("xray -test failed: signal: killed")
	})
	failed := mgr.Apply(context.Background(), []Target{targetFor(t, "alice@example.test", "carol@example.test")}, ApplyOptions{})
	if len(failed) != 1 || failed[0].State != StateFailed {
		t.Fatalf("expected failed, got %+v", failed)
	}
	if got := mgr.lookup("node-1").view().state; got != StateFailed {
		t.Fatalf("tracked state = %q, want %q", got, StateFailed)
	}
	// The failure must drop the committed hash, otherwise an identical retry
	// would be reported unchanged and the instance could never recover.
	mgr.lookup("node-1").mu.Lock()
	committedHash := mgr.lookup("node-1").hash
	mgr.lookup("node-1").mu.Unlock()
	if committedHash != "" {
		t.Fatalf("a failed instance kept the committed hash %q; an identical retry would be skipped", committedHash)
	}
	// Same target, one regular apply, no intervention.
	runner.setTestFn(nil)
	healed := mgr.Apply(context.Background(), []Target{target}, ApplyOptions{})
	if len(healed) != 1 || healed[0].State != StateApplied {
		t.Fatalf("the failed instance was not retried and healed: %+v", healed)
	}
	if healed[0].AppliedUserCount != 2 {
		t.Fatalf("healed instance applies %d user(s), want 2", healed[0].AppliedUserCount)
	}
}

// --- D2: the orphan core and its inbound port ---------------------------------

// TestOrphanCoreIsReclaimedSoItsPortIsReleased is the end-to-end orphan case: a
// failed apply leaves a core process alive (it ignored the stop), the instance is
// already marked failed, and its inbound port is still busy. The next attempt must
// reclaim the orphan (verified ownership, not a blind pkill) and succeed, so the
// port is released without an operator killing anything.
func TestOrphanCoreIsReclaimedSoItsPortIsReleased(t *testing.T) {
	shortStopVerify(t)
	exe := testBinaryPath(t)
	root := t.TempDir()
	mgr, runner := newScriptedManager(t, exe, root)
	dir, err := mgr.instanceDir("node-1")
	if err != nil {
		t.Fatalf("instanceDir: %v", err)
	}
	addr := freeListenAddress(t)
	var (
		helper       *exec.Cmd
		survivor     *survivorProc
		startCalls   int
		startCallsMu sync.Mutex
	)
	runner.setStartFn(func(spec StartSpec) (Proc, error) {
		startCallsMu.Lock()
		startCalls++
		first := startCalls == 1
		startCallsMu.Unlock()
		if !first {
			// Later starts get an ordinary, stoppable core.
			return &scriptedProc{runner: runner, pid: 780000 + startCalls, exited: make(chan struct{})}, nil
		}
		helper = startPortHoldingHelper(t, spec.ConfigPath, addr)
		survivor = &survivorProc{runner: runner, pid: helper.Process.Pid, exited: make(chan struct{})}
		return survivor, nil
	})
	if _, ok := applyOneHelper(t, mgr, targetFor(t, "alice@example.test")); !ok {
		t.Fatal("initial apply failed")
	}
	// The running core holds the inbound port and has a verified pid record.
	if !portIsBusy(t, addr) {
		t.Fatalf("the managed core does not hold %s", addr)
	}
	rec, err := readPidFile(dir)
	if err != nil {
		t.Fatalf("readPidFile: %v", err)
	}
	if rec.PID != helper.Process.Pid || rec.Created == "" || rec.Executable == "" {
		t.Fatalf("the pid record does not describe the running core: %+v", rec)
	}
	// A rejected technical config: the core survives the stop, the rollback is
	// rejected too, so the instance is marked failed with an orphan core left.
	runner.setTestFn(func(ctx context.Context, configPath string) error {
		return errors.New("xray -test failed: signal: killed")
	})
	results := mgr.Apply(context.Background(), []Target{targetFor(t, "alice@example.test", "carol@example.test")}, ApplyOptions{})
	if len(results) != 1 || results[0].State != StateFailed {
		t.Fatalf("expected failed with a surviving core, got %+v", results)
	}
	if survivor.stopCount() == 0 {
		t.Fatal("the surviving core was never asked to stop")
	}
	// The failed apply must reclaim the orphan it could not stop gracefully.
	if !waitForExit(t, helper, 20*time.Second) {
		t.Fatal("the orphan core was not reclaimed; it keeps the inbound port busy and every retry fails")
	}
	if portIsBusy(t, addr) {
		t.Fatalf("the inbound port %s is still busy after the orphan was reclaimed", addr)
	}
	if _, err := os.Stat(filepath.Join(dir, pidFileName)); !os.IsNotExist(err) {
		t.Fatalf("the reclaimed core kept its pid record: %v", err)
	}
	if got := mgr.Snapshot(); len(got) != 1 || got[0].State != StateFailed {
		t.Fatalf("snapshot reports %+v, want failed", got)
	}
	// Self healing: one regular apply, no pkill and no restart.
	runner.setTestFn(nil)
	healed := mgr.Apply(context.Background(), []Target{targetFor(t, "alice@example.test", "carol@example.test")}, ApplyOptions{})
	if len(healed) != 1 || healed[0].State != StateApplied {
		t.Fatalf("the instance did not heal after the orphan reclaim: %+v", healed)
	}
}

// TestRestartReclaimsAnOrphanLeftByAPreviousAgent replays what an operator sees
// after a crash or a failed apply on an older agent: the instance record says
// failed, a verified pid record points at a live core that still holds the
// inbound port, and no manager owns that pid. The next apply of a fresh manager
// must reclaim it and start successfully.
func TestRestartReclaimsAnOrphanLeftByAPreviousAgent(t *testing.T) {
	shortStopVerify(t)
	exe := testBinaryPath(t)
	root := t.TempDir()
	addr := freeListenAddress(t)
	logger := log.New(io.Discard, "", 0)

	first, firstRunner := newScriptedManager(t, exe, root)
	dir, err := first.instanceDir("node-1")
	if err != nil {
		t.Fatalf("instanceDir: %v", err)
	}
	var helper *exec.Cmd
	firstRunner.setStartFn(func(spec StartSpec) (Proc, error) {
		helper = startPortHoldingHelper(t, spec.ConfigPath, addr)
		return &survivorProc{runner: firstRunner, pid: helper.Process.Pid, exited: make(chan struct{})}, nil
	})
	if _, ok := applyOneHelper(t, first, targetFor(t, "alice@example.test")); !ok {
		t.Fatal("initial apply failed")
	}
	if !portIsBusy(t, addr) {
		t.Fatalf("the orphan does not hold %s", addr)
	}
	// The agent dies here: the instance is never stopped and no manager owns the
	// pid any more. A fresh manager over the same state directory takes over.
	secondRunner := newScriptedRunner()
	second, err := NewManager(root, exe, secondRunner, logger)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	port := 26000
	second.SetPortAllocator(func(reserved map[int]bool) (int, error) {
		port++
		return port, nil
	})
	if second.isOwned(helper.Process.Pid) {
		t.Fatal("the fresh manager must not consider the orphan its own process")
	}
	healed, ok := applyOneHelper(t, second, targetFor(t, "alice@example.test", "bob@example.test"))
	if !ok || healed.State != StateApplied {
		t.Fatalf("the fresh manager did not reclaim and apply the orphaned instance: %+v", healed)
	}
	if !waitForExit(t, helper, 20*time.Second) {
		t.Fatal("the orphaned core of the previous agent survived the next attempt")
	}
	if portIsBusy(t, addr) {
		t.Fatalf("the orphan still holds the inbound port %s", addr)
	}
	rec, err := readPidFile(dir)
	if err != nil {
		t.Fatalf("the healed instance has no pid record: %v", err)
	}
	if rec.PID == helper.Process.Pid {
		t.Fatalf("the pid record still points at the reclaimed orphan: %+v", rec)
	}
	if rec.InstanceID != "node-1" || rec.CoreGeneration != healed.CoreGeneration {
		t.Fatalf("the new pid record does not describe the healed core: %+v", rec)
	}
	if got := second.Snapshot(); len(got) != 1 || got[0].State != StateApplied {
		t.Fatalf("snapshot reports %+v, want applied", got)
	}
}

// containsAll reports whether s contains every fragment.
func containsAll(s string, fragments ...string) bool {
	for _, f := range fragments {
		if f == "" {
			continue
		}
		if !strings.Contains(s, f) {
			return false
		}
	}
	return true
}
