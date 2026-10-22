package xray

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"proxypanel/internal/panel/protocol"
)

// The tests in this file are the regression suite for the three P0 defects seen
// on a real host (nested virtualization):
//
//  1. the management API readiness budget was a hard coded 5s, so a core that
//     logged "started" but answered slowly was killed and the instance failed;
//  2. a failed apply left the instance dead with an orphan core and a rollback
//     that inherited the exhausted budget ("xray -test failed: context deadline
//     exceeded");
//  3. the panel accepted a transport (QUIC) the frozen core rejects.

// scriptedProc is a Proc whose stop outcome is scripted, so a surviving core (a
// process that ignores the stop and keeps the inbound port) can be modelled.
type scriptedProc struct {
	runner   *scriptedRunner
	pid      int
	exited   chan struct{}
	once     sync.Once
	survives bool
}

func (p *scriptedProc) PID() int                { return p.pid }
func (p *scriptedProc) Exited() <-chan struct{} { return p.exited }
func (p *scriptedProc) ExitError() error        { return nil }

func (p *scriptedProc) Output() []string {
	return []string{"[Warning] core: Xray 26.3.27 started"}
}

func (p *scriptedProc) Stop(ctx context.Context) error {
	p.runner.recordEvent(fmt.Sprintf("stop:%d", p.pid))
	if p.survives {
		// The stop request was delivered but the process did not die.
		return nil
	}
	p.once.Do(func() { close(p.exited) })
	return nil
}

// scriptedRunner is a Runner whose steps are scripted per test and whose event
// log keeps the real interleaving of validate / stop / start.
type scriptedRunner struct {
	mu         sync.Mutex
	events     []string
	statsCalls int
	nextPID    int
	testFn     func(ctx context.Context, configPath string) error
	statsFn    func(ctx context.Context, server string) ([]StatCounter, error)
	startFn    func(spec StartSpec) (Proc, error)
}

func newScriptedRunner() *scriptedRunner { return &scriptedRunner{nextPID: 700000} }

func (r *scriptedRunner) recordEvent(event string) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *scriptedRunner) Test(ctx context.Context, configPath string) error {
	r.recordEvent("test:" + filepath.Base(configPath))
	r.mu.Lock()
	fn := r.testFn
	r.mu.Unlock()
	if fn != nil {
		return fn(ctx, configPath)
	}
	return nil
}

func (r *scriptedRunner) Start(ctx context.Context, spec StartSpec) (Proc, error) {
	r.recordEvent("start:" + filepath.Base(spec.ConfigPath))
	r.mu.Lock()
	fn := r.startFn
	r.nextPID++
	pid := r.nextPID
	r.mu.Unlock()
	if fn != nil {
		return fn(spec)
	}
	return &scriptedProc{runner: r, pid: pid, exited: make(chan struct{})}, nil
}

func (r *scriptedRunner) StatsQuery(ctx context.Context, server string) ([]StatCounter, error) {
	r.mu.Lock()
	r.statsCalls++
	fn := r.statsFn
	r.mu.Unlock()
	if fn != nil {
		return fn(ctx, server)
	}
	return nil, nil
}

func (r *scriptedRunner) Version(ctx context.Context) (string, error) { return "26.3.27", nil }

func (r *scriptedRunner) eventList() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	copy(out, r.events)
	return out
}

func (r *scriptedRunner) statsCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.statsCalls
}

func (r *scriptedRunner) setTestFn(fn func(ctx context.Context, configPath string) error) {
	r.mu.Lock()
	r.testFn = fn
	r.mu.Unlock()
}

func (r *scriptedRunner) setStatsFn(fn func(ctx context.Context, server string) ([]StatCounter, error)) {
	r.mu.Lock()
	r.statsFn = fn
	r.mu.Unlock()
}

func (r *scriptedRunner) setStartFn(fn func(spec StartSpec) (Proc, error)) {
	r.mu.Lock()
	r.startFn = fn
	r.mu.Unlock()
}

func newScriptedManager(t *testing.T, xrayPath, root string) (*Manager, *scriptedRunner) {
	t.Helper()
	runner := newScriptedRunner()
	mgr, err := NewManager(root, xrayPath, runner, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	port := 24000
	mgr.SetPortAllocator(func(reserved map[int]bool) (int, error) {
		port++
		return port, nil
	})
	return mgr, runner
}

// shortStopVerify keeps the survivor detection window short in tests.
func shortStopVerify(t *testing.T) {
	t.Helper()
	previous := stopVerifyWindow
	stopVerifyWindow = 200 * time.Millisecond
	t.Cleanup(func() { stopVerifyWindow = previous })
}

// indexOfEvent returns the position of the first event matching prefix/contains.
func indexOfEvent(events []string, match func(string) bool) int {
	for i, e := range events {
		if match(e) {
			return i
		}
	}
	return -1
}

// --- defect 1: the readiness budget -----------------------------------------

// TestReadinessBudgetDefaultsToAtLeast30Seconds pins the fixed production
// failure: "core management API did not answer within 5s" while the core had
// already logged "started". Before the fix readinessTimeout was 5s.
func TestReadinessBudgetDefaultsToAtLeast30Seconds(t *testing.T) {
	t.Setenv(EnvReadinessTimeout, "")
	defaults := DefaultTimeouts()
	if defaults.Readiness < 30*time.Second {
		t.Fatalf("default readiness budget = %s, want at least 30s", defaults.Readiness)
	}
	if defaults.Validate <= 0 || defaults.Stop <= 0 || defaults.Rollback <= 0 {
		t.Fatalf("default timeouts incomplete: %+v", defaults)
	}
	mgr, _ := newScriptedManager(t, filepath.Join(t.TempDir(), "xray.exe"), t.TempDir())
	if got := mgr.Timeouts().Readiness; got < 30*time.Second {
		t.Fatalf("manager readiness budget = %s, want at least 30s", got)
	}
}

// TestTimeoutOverridesFromTheEnvironment proves the budgets are configurable on a
// slow host without a rebuild, and that garbage is ignored.
func TestTimeoutOverridesFromTheEnvironment(t *testing.T) {
	t.Setenv(EnvReadinessTimeout, "45s")
	if got := DefaultTimeouts().Readiness; got != 45*time.Second {
		t.Fatalf("readiness override = %s, want 45s", got)
	}
	t.Setenv(EnvReadinessTimeout, "not-a-duration")
	if got := DefaultTimeouts().Readiness; got != DefaultReadinessTimeout {
		t.Fatalf("invalid override must fall back to %s, got %s", DefaultReadinessTimeout, got)
	}
}

// TestReadinessToleratesACoreThatNeedsSixSeconds reproduces the production log:
// the core is up, its management API simply answers late. Before the fix (5s
// budget) the start failed and the instance ended up failed.
func TestReadinessToleratesACoreThatNeedsSixSeconds(t *testing.T) {
	t.Setenv(EnvReadinessTimeout, "")
	mgr, runner := newScriptedManager(t, filepath.Join(t.TempDir(), "xray.exe"), t.TempDir())
	started := time.Now()
	runner.setStatsFn(func(ctx context.Context, server string) ([]StatCounter, error) {
		if time.Since(started) < 6*time.Second {
			return nil, errors.New("dial tcp: connection refused")
		}
		return nil, nil
	})
	results := mgr.Apply(context.Background(), []Target{targetFor(t, "alice@example.test")}, ApplyOptions{})
	if len(results) != 1 || results[0].State != StateApplied {
		t.Fatalf("a slow but healthy core must be applied, got %+v", results)
	}
	if calls := runner.statsCount(); calls < 3 {
		t.Fatalf("the management API was probed %d time(s); want repeated polling", calls)
	}
}

// TestReadinessUsesTheConfiguredBudgetAndKeepsPolling proves the wait ends at the
// configured budget, keeps polling until then, and reports that budget.
func TestReadinessUsesTheConfiguredBudgetAndKeepsPolling(t *testing.T) {
	mgr, runner := newScriptedManager(t, filepath.Join(t.TempDir(), "xray.exe"), t.TempDir())
	mgr.SetTimeouts(Timeouts{Readiness: 1500 * time.Millisecond})
	runner.setStatsFn(func(ctx context.Context, server string) ([]StatCounter, error) {
		return nil, errors.New("dial tcp: connection refused")
	})
	started := time.Now()
	results := mgr.Apply(context.Background(), []Target{targetFor(t, "alice@example.test")}, ApplyOptions{})
	elapsed := time.Since(started)
	if len(results) != 1 || results[0].State != StateFailed {
		t.Fatalf("an unready core must fail the apply, got %+v", results)
	}
	if !strings.Contains(results[0].Error, "within 1.5s") {
		t.Fatalf("error must report the configured budget: %q", results[0].Error)
	}
	if calls := runner.statsCount(); calls < 3 {
		t.Fatalf("probed %d time(s) in 1.5s; want polling every 500ms", calls)
	}
	if elapsed < 1500*time.Millisecond {
		t.Fatalf("gave up after %s, before the configured budget", elapsed)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("the readiness budget was not honoured: waited %s", elapsed)
	}
}

// TestReadinessBudgetIsIndependentOfTheStopBudget proves the two budgets do not
// influence each other: a tiny stop budget must not cut a long start short.
func TestReadinessBudgetIsIndependentOfTheStopBudget(t *testing.T) {
	mgr, runner := newScriptedManager(t, filepath.Join(t.TempDir(), "xray.exe"), t.TempDir())
	mgr.SetTimeouts(Timeouts{
		Readiness: 3 * time.Second,
		Stop:      200 * time.Millisecond,
		Validate:  5 * time.Second,
		Rollback:  5 * time.Second,
	})
	if got := mgr.Timeouts(); got.Readiness != 3*time.Second || got.Stop != 200*time.Millisecond {
		t.Fatalf("the budgets must stay separate: %+v", got)
	}
	started := time.Now()
	runner.setStatsFn(func(ctx context.Context, server string) ([]StatCounter, error) {
		if time.Since(started) < 1500*time.Millisecond {
			return nil, errors.New("dial tcp: connection refused")
		}
		return nil, nil
	})
	results := mgr.Apply(context.Background(), []Target{targetFor(t, "alice@example.test")}, ApplyOptions{})
	if len(results) != 1 || results[0].State != StateApplied {
		t.Fatalf("the stop budget cut the readiness wait short: %+v", results)
	}
}

// --- defect 2: rollback budget, ordering and orphan reclaim ------------------

// TestRollbackValidationGetsItsOwnBudgetAfterTheInstanceBudgetIsExhausted
// reproduces "rollback config rejected: xray -test failed: context deadline
// exceeded": the rollback inherited the exhausted per instance budget, so the
// instance ended up failed with 0 applied users even though the last known good
// configuration was perfectly usable.
func TestRollbackValidationGetsItsOwnBudgetAfterTheInstanceBudgetIsExhausted(t *testing.T) {
	mgr, runner := newScriptedManager(t, filepath.Join(t.TempDir(), "xray.exe"), t.TempDir())
	if _, ok := applyOneHelper(t, mgr, targetFor(t, "alice@example.test", "bob@example.test")); !ok {
		t.Fatal("initial apply failed")
	}
	var rollbackCtxErr error
	runner.setTestFn(func(ctx context.Context, configPath string) error {
		if filepath.Base(configPath) == "config.rollback.json" {
			rollbackCtxErr = ctx.Err()
			if rollbackCtxErr != nil {
				return fmt.Errorf("xray -test failed: %w", rollbackCtxErr)
			}
			return nil
		}
		// Validating the new configuration overshoots the per instance budget,
		// exactly like a killed `xray -test` on a slow host.
		time.Sleep(400 * time.Millisecond)
		return errors.New("xray -test failed: unknown field")
	})
	revoked := targetFor(t, "alice@example.test")
	results := mgr.Apply(context.Background(), []Target{revoked}, ApplyOptions{PerInstanceTimeout: 250 * time.Millisecond})
	if len(results) != 1 || results[0].State != StateRollback {
		t.Fatalf("the rollback must succeed on its own budget, got %+v", results)
	}
	if rollbackCtxErr != nil {
		t.Fatalf("the rollback validation inherited an expired budget: %v", rollbackCtxErr)
	}
	if results[0].AppliedUserCount != 1 {
		t.Fatalf("rollback applied %d user(s), want only the still authorized one", results[0].AppliedUserCount)
	}
	if len(results[0].Users) != 1 || results[0].Users[0].Email != "alice@example.test" {
		t.Fatalf("the rollback revived a revoked user: %+v", results[0].Users)
	}
}

// TestRollbackIsValidatedBeforeTheOldCoreIsStopped proves requirement (a): the
// old core keeps serving until a replacement configuration was validated. Before
// the fix the core was stopped first, so a rejected rollback left the instance
// with nothing running.
func TestRollbackIsValidatedBeforeTheOldCoreIsStopped(t *testing.T) {
	shortStopVerify(t)
	mgr, runner := newScriptedManager(t, filepath.Join(t.TempDir(), "xray.exe"), t.TempDir())
	if _, ok := applyOneHelper(t, mgr, targetFor(t, "alice@example.test", "bob@example.test")); !ok {
		t.Fatal("initial apply failed")
	}
	runner.setTestFn(func(ctx context.Context, configPath string) error {
		return errors.New("xray -test failed: unknown field")
	})
	revoked := targetFor(t, "alice@example.test")
	results := mgr.Apply(context.Background(), []Target{revoked}, ApplyOptions{})
	if len(results) != 1 || results[0].State != StateFailed {
		t.Fatalf("both configurations are rejected, want failed, got %+v", results)
	}
	// The revocation contract is unchanged: no invented applied count.
	if results[0].AppliedUserCount != 0 {
		t.Fatalf("a failed apply must not report applied users: %+v", results[0])
	}
	events := runner.eventList()
	rollbackIdx := indexOfEvent(events, func(e string) bool { return e == "test:config.rollback.json" })
	stopIdx := indexOfEvent(events, func(e string) bool { return strings.HasPrefix(e, "stop:") })
	if rollbackIdx < 0 || stopIdx < 0 {
		t.Fatalf("missing lifecycle events: %v", events)
	}
	if rollbackIdx > stopIdx {
		t.Fatalf("the old core was stopped before the replacement was validated: %v", events)
	}
}

// TestSurvivingCoreIsReclaimedSoTheNextAttemptCanHeal covers requirement (c): the
// orphan core that survived the stop must be reclaimed, otherwise it keeps the
// inbound port busy and every later retry fails until an operator kills it.
func TestSurvivingCoreIsReclaimedSoTheNextAttemptCanHeal(t *testing.T) {
	shortStopVerify(t)
	exe := testBinaryPath(t)
	root := t.TempDir()
	mgr, runner := newScriptedManager(t, exe, root)
	dir, err := mgr.instanceDir("node-1")
	if err != nil {
		t.Fatalf("instanceDir: %v", err)
	}
	pidPath := filepath.Join(dir, pidFileName)

	var helper *exec.Cmd
	runner.setStartFn(func(spec StartSpec) (Proc, error) {
		helper = startHelperProcess(t, spec.ConfigPath)
		// The started core ignores the stop request: a survivor.
		return &scriptedProc{runner: runner, pid: helper.Process.Pid, exited: make(chan struct{}), survives: true}, nil
	})

	if _, ok := applyOneHelper(t, mgr, targetFor(t, "alice@example.test")); !ok {
		t.Fatal("initial apply failed")
	}
	if _, err := os.Stat(pidPath); err != nil {
		t.Fatalf("the running core has no pid record: %v", err)
	}
	// A failing configuration leaves the previous core alive after the stop.
	runner.setTestFn(func(ctx context.Context, configPath string) error {
		return errors.New("xray -test failed: signal: killed")
	})
	results := mgr.Apply(context.Background(), []Target{targetFor(t, "alice@example.test", "carol@example.test")}, ApplyOptions{})
	if len(results) != 1 || results[0].State != StateFailed {
		t.Fatalf("unexpected results: %+v", results)
	}
	if results[0].AppliedUserCount != 0 {
		t.Fatalf("a failed apply must not invent applied users: %+v", results[0])
	}
	done := make(chan error, 1)
	go func() {
		_, werr := helper.Process.Wait()
		done <- werr
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the surviving core was not reclaimed; the next retry would fail on a busy inbound port")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("the reclaimed pid record was kept: %v", err)
	}
	// The instance heals on the next attempt instead of requiring manual cleanup.
	runner.setTestFn(nil)
	healed := mgr.Apply(context.Background(), []Target{targetFor(t, "alice@example.test", "carol@example.test")}, ApplyOptions{})
	if len(healed) != 1 || healed[0].State != StateApplied {
		t.Fatalf("the instance did not heal after the reclaim: %+v", healed)
	}
}

// --- defect 3: transport whitelist consistency ------------------------------

// TestAllowedTransportsMatchThePanelWhitelist enforces one shared whitelist. A
// value the panel accepts but the core rejects makes every later snapshot fail
// until the node is archived (the real QUIC incident).
func TestAllowedTransportsMatchThePanelWhitelist(t *testing.T) {
	// The panel accepts a few legacy aliases and normalises them before a node is
	// stored (raw->tcp, mkcp->kcp, splithttp->xhttp), so the panel set is the
	// canonical value space, not the alias space. The invariant that matters is:
	// every transport the panel can hand the agent must be one the core loads.
	panelNetworks := protocol.SupportedNetworks()
	if len(panelNetworks) == 0 {
		t.Fatal("panel transport whitelist is empty")
	}
	for _, name := range panelNetworks {
		if _, ok := allowedNetworks[name]; !ok {
			t.Fatalf("the panel can emit transport %q but the agent whitelist %v rejects it: "+
				"such a node would make every snapshot fail until it is archived",
				name, sortedKeys(allowedNetworks))
		}
	}
	// The agent also lists the legacy aliases (raw, mkcp, splithttp) because an
	// already stored node keeps its literal network string. Every one of them must
	// still be expressible by the panel (as an alias or as the canonical name),
	// otherwise the core would accept a transport no admin can produce.
	for name := range allowedNetworks {
		if _, ok := protocol.NormalizeNetwork(name); !ok {
			t.Fatalf("the agent accepts transport %q which the panel cannot express", name)
		}
	}
	agentSecurities := make([]string, 0, len(allowedSecurities))
	for name := range allowedSecurities {
		agentSecurities = append(agentSecurities, name)
	}
	sort.Strings(agentSecurities)
	panelSecurities := protocol.SupportedSecurities()
	if strings.Join(agentSecurities, ",") != strings.Join(panelSecurities, ",") {
		t.Fatalf("security whitelist drift:\n agent: %v\n panel: %v", agentSecurities, panelSecurities)
	}
	// The frozen core removed these transports; nothing may re-add them silently.
	for _, removed := range []string{"quic", "http", "h2"} {
		if _, ok := allowedNetworks[removed]; ok {
			t.Fatalf("transport %q is not supported by the frozen core but is still allowed", removed)
		}
		if _, ok := protocol.NormalizeNetwork(removed); ok {
			t.Fatalf("the panel still accepts the removed transport %q", removed)
		}
	}
}

// sortedKeys returns the keys of a string set in stable order for messages.
func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- timeout budgets: configurable, never zero, never a panic ----------------

// TestReadinessTimeoutEnvOverridesEveryBudget proves
// PROXYPANEL_XRAY_READINESS_TIMEOUT really replaces the built-in readiness budget
// (the operator knob for a slow host) and that it reaches the manager, the
// derived per instance budget and the readiness loop. Every override is scoped to
// the test with t.Setenv, so the process environment is never polluted.
func TestReadinessTimeoutEnvOverridesEveryBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want time.Duration
	}{
		{name: "seconds", raw: "45s", want: 45 * time.Second},
		{name: "minutes", raw: "2m", want: 2 * time.Minute},
		{name: "milliseconds", raw: "1500ms", want: 1500 * time.Millisecond},
		{name: "surrounding spaces", raw: "  17s\t", want: 17 * time.Second},
		{name: "compound", raw: "1m30s", want: 90 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvReadinessTimeout, tc.raw)
			if got := DefaultTimeouts().Readiness; got != tc.want {
				t.Fatalf("DefaultTimeouts().Readiness = %s, want %s for %q", got, tc.want, tc.raw)
			}
			// The override must be what a freshly constructed manager uses, and it
			// must flow into the derived budgets instead of being recomputed from a
			// constant.
			mgr, _ := newScriptedManager(t, filepath.Join(t.TempDir(), "xray.exe"), t.TempDir())
			if got := mgr.Timeouts().Readiness; got != tc.want {
				t.Fatalf("manager readiness budget = %s, want %s", got, tc.want)
			}
			if got := mgr.Timeouts().InstanceBudget(); got < tc.want {
				t.Fatalf("the instance budget %s does not cover the readiness budget %s", got, tc.want)
			}
			// A budget with one unset field keeps the default for that field only.
			partial := Timeouts{Readiness: tc.want}.withDefaults()
			if partial.Readiness != tc.want {
				t.Fatalf("withDefaults changed the explicit readiness budget: %s", partial.Readiness)
			}
			if partial.Validate != DefaultValidationTimeout || partial.Stop != DefaultStopTimeout {
				t.Fatalf("withDefaults did not keep the other defaults: %+v", partial)
			}
		})
	}
}

// TestInvalidReadinessTimeoutFallsBackToTheDefault proves garbage or a non
// positive value never panics and never collapses the budget to zero: a zero
// readiness budget would fail every instance instantly.
func TestInvalidReadinessTimeoutFallsBackToTheDefault(t *testing.T) {
	for _, raw := range []string{
		"not-a-duration",
		"45",           // missing unit
		"45 s",         // inner space
		"-1s",          // negative
		"-45s",         // negative duration
		"0s",           // explicitly zero
		"0",            // zero without unit
		"1e9",          // unit-less float
		"99999999999h", // overflow
		"null",
		" ",
		"",
	} {
		t.Run("raw="+raw, func(t *testing.T) {
			t.Setenv(EnvReadinessTimeout, raw)
			got := DefaultTimeouts()
			if got.Readiness != DefaultReadinessTimeout {
				t.Fatalf("readiness budget for %q = %s, want the default %s", raw, got.Readiness, DefaultReadinessTimeout)
			}
			// Every budget must stay usable, so withDefaults can never produce zero.
			for name, d := range map[string]time.Duration{
				"readiness": got.Readiness,
				"validate":  got.Validate,
				"stop":      got.Stop,
				"rollback":  got.Rollback,
			} {
				if d <= 0 {
					t.Fatalf("%s budget for %q is %s, want a positive default", name, raw, d)
				}
			}
			mgr, _ := newScriptedManager(t, filepath.Join(t.TempDir(), "xray.exe"), t.TempDir())
			if mgr.Timeouts().Readiness != DefaultReadinessTimeout {
				t.Fatalf("manager readiness budget = %s, want the default", mgr.Timeouts().Readiness)
			}
			// Explicitly unset fields are filled from the same defaults.
			filled := Timeouts{}.withDefaults()
			if filled.Readiness <= 0 || filled.Validate <= 0 || filled.Stop <= 0 || filled.Rollback <= 0 {
				t.Fatalf("withDefaults left a zero budget: %+v", filled)
			}
		})
	}
}

// TestReadinessBudgetOverrideIsActuallyUsedByTheWait proves the environment
// override is not just parsed but honoured by the readiness loop: a very short
// override fails the instance quickly and reports that exact budget, while the
// default would have kept polling.
func TestReadinessBudgetOverrideIsActuallyUsedByTheWait(t *testing.T) {
	// 300ms is below the 500ms poll interval, so the first probe failure already
	// exhausts the budget: a budget that was silently replaced by 30s would make
	// this test wait for seconds instead of failing fast.
	t.Setenv(EnvReadinessTimeout, "300ms")
	mgr, runner := newScriptedManager(t, filepath.Join(t.TempDir(), "xray.exe"), t.TempDir())
	if got := mgr.Timeouts().Readiness; got != 300*time.Millisecond {
		t.Fatalf("manager readiness budget = %s, want the 300ms override", got)
	}
	runner.setStatsFn(func(ctx context.Context, server string) ([]StatCounter, error) {
		return nil, errors.New("dial tcp: connection refused")
	})
	begin := time.Now()
	results := mgr.Apply(context.Background(), []Target{targetFor(t, "alice@example.test")}, ApplyOptions{})
	elapsed := time.Since(begin)
	if len(results) != 1 || results[0].State != StateFailed {
		t.Fatalf("an unready core must fail, got %+v", results)
	}
	if !strings.Contains(results[0].Error, "within 300ms") {
		t.Fatalf("the error must report the overridden budget, got %q", results[0].Error)
	}
	// Two start attempts (the second one with a fresh port) are the only retries.
	if elapsed > 4*time.Second {
		t.Fatalf("the 300ms override was ignored: the apply took %s", elapsed)
	}
}

// TestEveryTimeoutEnvOverrideIsHonoured keeps the four knobs independent: each
// variable only moves its own budget, so tuning one cannot silently disable
// another.
func TestEveryTimeoutEnvOverrideIsHonoured(t *testing.T) {
	t.Setenv(EnvReadinessTimeout, "11s")
	t.Setenv(EnvValidationTimeout, "12s")
	t.Setenv(EnvStopTimeout, "13s")
	t.Setenv(EnvRollbackTimeout, "14s")
	got := DefaultTimeouts()
	if got.Readiness != 11*time.Second || got.Validate != 12*time.Second ||
		got.Stop != 13*time.Second || got.Rollback != 14*time.Second {
		t.Fatalf("the four overrides were not applied independently: %+v", got)
	}
	// An unset variable (in this process the other three are set) must keep its
	// own default, which proves there is no shared value behind the knobs.
	t.Setenv(EnvReadinessTimeout, "")
	got = DefaultTimeouts()
	if got.Readiness != DefaultReadinessTimeout {
		t.Fatalf("clearing the readiness override = %s, want the default", got.Readiness)
	}
	if got.Validate != 12*time.Second || got.Stop != 13*time.Second || got.Rollback != 14*time.Second {
		t.Fatalf("clearing one override moved another budget: %+v", got)
	}
}
