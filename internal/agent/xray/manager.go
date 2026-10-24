package xray

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"proxypanel/internal/contracts"
)

// Instance states reported to the panel.
const (
	StateApplied   = "applied"
	StateUnchanged = "unchanged"
	StateRollback  = "rollback_applied"
	StateFailed    = "failed"
	StateStopped   = "stopped"
	StateDeferred  = "deferred"
	StateStopping  = "stopping"
)

// Default instance apply budget. The contract requires config validation plus
// stop/start to fit into 60 seconds per snapshot on a healthy machine; the
// individual step budgets below are upper bounds for a slow machine (nested
// virtualization) and are only reached when a step really stalls.
const (
	DefaultInstanceTimeout = 45 * time.Second
	MinAPIPort             = 20000
	MaxAPIPort             = 60000

	// DefaultReadinessTimeout is how long a started core may take before its
	// management API answers. Xray logs "started" long before the stats service
	// is reachable on a slow host, so the budget must not be measured in single
	// digit seconds.
	DefaultReadinessTimeout = 30 * time.Second
	// DefaultValidationTimeout is the budget of one `xray -test` run.
	DefaultValidationTimeout = 30 * time.Second
	// DefaultStopTimeout is the budget of one core stop. It is deliberately
	// independent of the readiness budget: a slow stop must never shorten a start
	// and a slow start must never shorten a stop.
	DefaultStopTimeout = 15 * time.Second
	// DefaultRollbackTimeout is the budget of the whole rollback attempt
	// (validate the last known good config, start it, wait for readiness). It is
	// more generous than the normal path on purpose: the failure that triggered
	// the rollback has usually already consumed the normal budget.
	DefaultRollbackTimeout = 60 * time.Second

	// readinessPollInterval is how often the management API is probed while the
	// core starts.
	readinessPollInterval = 500 * time.Millisecond
	// readinessProbeTimeout bounds one single probe.
	readinessProbeTimeout = 5 * time.Second
)

// Environment overrides so an operator can tune the lifecycle budgets on a slow
// machine without rebuilding the agent. Values use Go duration syntax ("45s",
// "2m"); invalid or non positive values are ignored.
const (
	EnvReadinessTimeout  = "PROXYPANEL_XRAY_READINESS_TIMEOUT"
	EnvValidationTimeout = "PROXYPANEL_XRAY_VALIDATION_TIMEOUT"
	EnvStopTimeout       = "PROXYPANEL_XRAY_STOP_TIMEOUT"
	EnvRollbackTimeout   = "PROXYPANEL_XRAY_ROLLBACK_TIMEOUT"
)

// Timeouts are the independent lifecycle budgets of one instance. They are
// separate values on purpose: one stalling step must never consume the budget
// of the next one.
type Timeouts struct {
	// Readiness is how long the management API may take to answer after a start.
	Readiness time.Duration
	// Validate is the budget of one `xray -test` run.
	Validate time.Duration
	// Stop is the budget of one core stop.
	Stop time.Duration
	// Rollback is the budget of the whole rollback attempt.
	Rollback time.Duration
}

// DefaultTimeouts returns the built-in budgets, overridden by the optional
// PROXYPANEL_XRAY_*_TIMEOUT environment variables.
func DefaultTimeouts() Timeouts {
	t := Timeouts{
		Readiness: DefaultReadinessTimeout,
		Validate:  DefaultValidationTimeout,
		Stop:      DefaultStopTimeout,
		Rollback:  DefaultRollbackTimeout,
	}
	t.Readiness = envDuration(EnvReadinessTimeout, t.Readiness)
	t.Validate = envDuration(EnvValidationTimeout, t.Validate)
	t.Stop = envDuration(EnvStopTimeout, t.Stop)
	t.Rollback = envDuration(EnvRollbackTimeout, t.Rollback)
	return t
}

// withDefaults fills every unset budget with its default.
func (t Timeouts) withDefaults() Timeouts {
	d := DefaultTimeouts()
	if t.Readiness <= 0 {
		t.Readiness = d.Readiness
	}
	if t.Validate <= 0 {
		t.Validate = d.Validate
	}
	if t.Stop <= 0 {
		t.Stop = d.Stop
	}
	if t.Rollback <= 0 {
		t.Rollback = d.Rollback
	}
	return t
}

// InstanceBudget is the upper bound of one complete instance apply: validate the
// new config, stop the old core and start the new one, plus slack for the
// bookkeeping around it. The rollback attempt has its own budget because it only
// runs after a failure.
func (t Timeouts) InstanceBudget() time.Duration {
	t = t.withDefaults()
	budget := t.Validate + t.Readiness + t.Stop
	if budget < DefaultInstanceTimeout {
		budget = DefaultInstanceTimeout
	}
	return budget + 15*time.Second
}

// SnapshotBudget is the upper bound of one snapshot apply that reconciles
// instanceCount instances sequentially.
func (t Timeouts) SnapshotBudget(instanceCount int) time.Duration {
	if instanceCount < 1 {
		instanceCount = 1
	}
	return time.Duration(instanceCount)*t.InstanceBudget() + 15*time.Second
}

// rollbackBudget is the budget of one whole rollback attempt (validate the last
// known good config, start it, wait for readiness).
func (t Timeouts) rollbackBudget() time.Duration {
	t = t.withDefaults()
	if t.Rollback > 0 {
		return t.Rollback
	}
	return t.Validate + t.Readiness
}

func envDuration(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// Target is an instance the agent has to run.
type Target struct {
	InstanceID    string
	NodeID        string
	Document      map[string]any
	Hash          string
	Authorization Authorization
	EmailToMember map[string]string
	Users         []contracts.AuthorizedUser
	NextExpiry    time.Time
}

// SampleHook is called right before a core process is stopped so the last
// counters can be collected before they disappear.
type SampleHook func(ctx context.Context, nodeID, coreGeneration, apiServer string, emailToMember map[string]string)

// ApplyOptions controls one reconciliation round.
type ApplyOptions struct {
	Force              bool
	Skip               map[string]bool
	PerInstanceTimeout time.Duration
	BeforeStop         SampleHook
}

// Result is the outcome for one instance.
type Result struct {
	InstanceID               string
	NodeID                   string
	State                    string
	Error                    string
	AppliedUserCount         int
	OldConnectionsTerminated bool
	CoreGeneration           string
	Hash                     string
	Document                 map[string]any
	Users                    []contracts.AuthorizedUser
	EmailToMember            map[string]string
	NextExpiry               time.Time
}

// ContractResult converts the result to the wire type.
func (r Result) ContractResult() contracts.InstanceResult {
	return contracts.InstanceResult{
		InstanceID:               r.InstanceID,
		State:                    r.State,
		Error:                    r.Error,
		AppliedUserCount:         r.AppliedUserCount,
		OldConnectionsTerminated: r.OldConnectionsTerminated,
	}
}

// InstanceStats carries the counters of one running instance.
type InstanceStats struct {
	InstanceID     string
	NodeID         string
	CoreGeneration string
	EmailToMember  map[string]string
	Counters       []StatCounter
	Err            error
}

// Lock order for this package:
//
//	instance.mu  ->  validateMu  ->  Manager.mu
//
// Manager.mu only guards the instance map and the owned pid set. It is a leaf
// lock: it is never held while acquiring an instance lock and never held across
// I/O, logging or a hook call. instance.mu serializes the start/stop decisions of
// one instance. Readers (Snapshot, NextExpiry, ExpiredInstances, QueryStats) do
// not take instance.mu at all: they read the immutable view that the lifecycle
// owner publishes, so a config test or start in progress can never stall the
// heartbeat, the stats loop or shutdown.
//
// validateMu serializes the `xray -test` runs of all instances (see
// validateMutex). It is taken while instance.mu is held and never while
// Manager.mu is held, and nothing else is acquired while it is held, so it can
// neither stall a reader nor close a lock cycle.
type instance struct {
	mu            sync.Mutex // lifecycle lock, see the lock order above
	id            string     // immutable after creation
	nodeID        string     // immutable after creation
	doc           map[string]any
	hash          string
	lastGood      map[string]any
	lastGoodHash  string
	coreGen       string
	emailToMember map[string]string
	usersList     []contracts.AuthorizedUser
	users         int
	nextExpiry    time.Time
	apiPort       int
	proc          Proc
	state         string
	err           string
	startedAt     time.Time
	pendingExpiry time.Time // lifecycle-lock owned deadline for every start/rollback
	// healthOn marks a published view as a live core whose Exited channel is
	// meaningful. It is false for a core that was already known to be dead, so
	// recovery never defers to an already closed channel and never declares a
	// dead core healthy again.
	// published holds the reader visible state and is replaced atomically by
	// publish, so readers never wait for a lifecycle operation.
	published atomic.Pointer[instanceView]
}

// instanceView is an immutable copy of the fields readers need.
type instanceView struct {
	proc           Proc
	healthOn       bool
	instanceID     string
	nodeID         string
	state          string
	err            string
	reportState    string
	running        bool
	users          int
	apiPort        int
	coreGeneration string
	emailToMember  map[string]string
	nextExpiry     time.Time
	hash           string
	document       map[string]any
	usersList      []contracts.AuthorizedUser
}

// publish refreshes the reader visible view. The caller must hold inst.mu.
func (inst *instance) publish() {
	view := &instanceView{
		instanceID:     inst.id,
		nodeID:         inst.nodeID,
		state:          inst.state,
		err:            inst.err,
		running:        inst.proc != nil,
		proc:           inst.proc,
		healthOn:       inst.proc != nil,
		users:          inst.users,
		apiPort:        inst.apiPort,
		coreGeneration: inst.coreGen,
		emailToMember:  copyMap(inst.emailToMember),
		nextExpiry:     inst.nextExpiry,
		hash:           inst.hash,
		document:       inst.doc,
		usersList:      copyUsers(inst.usersList),
	}
	switch {
	case inst.proc == nil:
		view.reportState = inst.state
	case inst.state == StateRollback:
		view.reportState = StateRollback
	default:
		view.reportState = StateApplied
	}
	inst.published.Store(view)
}

// view returns the last published reader visible state without blocking.
func (inst *instance) view() *instanceView {
	if v := inst.published.Load(); v != nil {
		if v.running && v.healthOn && processExited(v.proc) {
			dead := *v
			dead.running = false
			dead.reportState = StateFailed
			dead.state = StateFailed
			dead.err = "core exited unexpectedly"
			return &dead
		}
		return v
	}
	return &instanceView{state: StateStopped, reportState: StateStopped}
}

// Manager owns the per-instance Xray processes.
//
// Concurrency contract (see also the instance type): Manager.mu is a leaf lock
// for the instance map and the owned pid set. It is never held while acquiring an
// instance lock, so a start/stop in progress can never block Snapshot,
// QueryStats, ExpiredInstances, NextExpiry or StopAll, and no lock cycle exists
// between the two mutex levels.
type Manager struct {
	root       string
	xrayPath   string
	certRoot   string
	runner     Runner
	logger     *log.Logger
	now        func() time.Time
	portFn     func(reserved map[int]bool) (int, error)
	secrets    func() []string
	beforeStop SampleHook
	// startGuard is an optional last moment veto right before a core process is
	// started. The supervisor installs it so an overload/persistence protection
	// that engages while this apply already holds the serialization lock can not
	// be bypassed by starting a core first and stopping it asynchronously. nil
	// means "no veto".
	startGuard func() error
	// inspectProcess resolves a pid to its process identity. It is the
	// per-manager seam for the process ownership checks and lets tests prove the
	// "stop was not confirmed" path without touching a real OS process.
	inspectProcess func(pid int) (ProcessIdentity, bool, error)
	// timeouts holds the independent lifecycle budgets (readiness, validation,
	// stop, rollback). Set by NewManager from DefaultTimeouts and adjustable via
	// SetTimeouts; never zero.
	timeouts Timeouts
	// mu guards instances and owned only; leaf lock, never held across I/O.
	mu        sync.Mutex
	instances map[string]*instance
	owned     map[int]bool
	// validateMu serializes every `xray -test` of this manager. It is a mutex of
	// its own, not Manager.mu: Manager.mu is the leaf lock of the instance map
	// and must never be held across I/O, while this lock is held for the whole
	// test process. See validateConfig for why the test must not run in parallel.
	validateMu validateMutex
}

// validateMutex is the process wide serialization point for `xray -test`.
//
// It is not a plain sync.Mutex because waiting for it has to be bounded: a
// queued instance must be able to give up when its own validation budget is
// gone, instead of testing a configuration after it was already reported as out
// of time. The zero value is ready to use.
type validateMutex struct {
	init sync.Once
	slot chan struct{}
}

// lock acquires the mutex, or returns ctx.Err() when ctx ends first. It never
// waits past the caller's deadline, so a waiter stays inside a bounded
// lifecycle step.
func (m *validateMutex) lock(ctx context.Context) error {
	m.init.Do(func() { m.slot = make(chan struct{}, 1) })
	select {
	case m.slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// unlock releases the mutex. It must only be called after a successful lock.
func (m *validateMutex) unlock() { <-m.slot }

// NewManager creates the instance manager rooted at the agent state directory.
func NewManager(root, xrayPath string, runner Runner, logger *log.Logger) (*Manager, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("instance root is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve instance root: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(abs, "instances"), 0o700); err != nil {
		return nil, fmt.Errorf("create instance root: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(abs, "certs"), 0o700); err != nil {
		return nil, fmt.Errorf("create certificate root: %w", err)
	}
	if logger == nil {
		logger = log.New(os.Stderr, "agent ", log.LstdFlags|log.LUTC)
	}
	return &Manager{
		root:           abs,
		xrayPath:       xrayPath,
		certRoot:       filepath.Join(abs, "certs"),
		runner:         runner,
		logger:         logger,
		now:            time.Now,
		portFn:         allocatePort,
		timeouts:       DefaultTimeouts(),
		instances:      map[string]*instance{},
		owned:          map[int]bool{},
		inspectProcess: processIdentity,
	}, nil
}

// SetStartGuard installs a last moment veto that runs immediately before a core
// process is started. It must be called before Apply/Stop run (startup wiring).
//
// Why this exists: overload/disk protection is evaluated by the supervisor, but
// an apply can be in flight when the condition appears (a final sample hook that
// fails to persist, an upload queue that crossed its limit). Without a check at
// the last moment the apply would start a core first and only get stopped by the
// asynchronous protection goroutine, i.e. the safety state would not really be a
// start gate. A guard error turns the start into a failure, never into a start.
func (m *Manager) SetStartGuard(fn func() error) {
	m.startGuard = fn
}

// SetInspectProcess overrides the pid inspection used by the ownership checks
// (tests only). It must be called before Apply/Stop run.
func (m *Manager) SetInspectProcess(fn func(pid int) (ProcessIdentity, bool, error)) {
	if fn != nil {
		m.inspectProcess = fn
	}
}

// inspect is processIdentity through the per-manager seam.
func (m *Manager) inspect(pid int) (ProcessIdentity, bool, error) {
	fn := m.inspectProcess
	if fn == nil {
		fn = processIdentity
	}
	return fn(pid)
}

// checkStartGuard runs the installed start gate, if any.
func (m *Manager) checkStartGuard() error {
	if m.startGuard == nil {
		return nil
	}
	if err := m.startGuard(); err != nil {
		return fmt.Errorf("core start blocked by the agent safety gate: %w", err)
	}
	return nil
}

// SetNow overrides the clock. It must be called before Apply/Stop run (tests and
// startup wiring only).
func (m *Manager) SetNow(fn func() time.Time) {
	if fn != nil {
		m.now = fn
	}
}

// SetPortAllocator overrides the management API port allocator. It must be called
// before Apply/Stop run (tests and startup wiring only).
func (m *Manager) SetPortAllocator(fn func(reserved map[int]bool) (int, error)) {
	if fn != nil {
		m.portFn = fn
	}
}

// SetTimeouts overrides the lifecycle budgets. Unset (zero or negative) fields
// keep their default. It must be called before Apply/Stop run (tests and startup
// wiring only).
func (m *Manager) SetTimeouts(t Timeouts) {
	m.timeouts = t.withDefaults()
}

// Timeouts returns the effective lifecycle budgets.
func (m *Manager) Timeouts() Timeouts { return m.timeouts.withDefaults() }

// CertRoot is the only directory allowed to hold TLS material for instances.
func (m *Manager) CertRoot() string { return m.certRoot }

// Root is the instance root directory.
func (m *Manager) Root() string { return m.root }

// SeedLastGood records a previously applied document so a rejected future
// document can fall back to it while applying the current authorization.
//
// The map lock is released before the instance lock is taken so the two levels
// keep the documented order (instance.mu -> Manager.mu) and never nest the other
// way round.
func (m *Manager) SeedLastGood(nodeID string, doc map[string]any) {
	if doc == nil {
		return
	}
	inst := m.ensure(nodeID, nodeID)
	if inst == nil {
		return
	}
	inst.mu.Lock()
	defer inst.mu.Unlock()
	inst.lastGood = doc
	if hash, err := HashDocument(doc); err == nil {
		inst.lastGoodHash = hash
	}
	inst.publish()
}

// ensure returns the tracked instance, creating it when it is unknown. The map
// lock is released before returning.
func (m *Manager) ensure(id, nodeID string) *instance {
	if _, err := m.instanceDir(id); err != nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	inst := m.instances[id]
	if inst == nil {
		inst = &instance{id: id, nodeID: nodeID, state: StateStopped}
		inst.published.Store(&instanceView{instanceID: id, nodeID: nodeID, state: StateStopped, reportState: StateStopped})
		m.instances[id] = inst
	}
	return inst
}

// lookup returns the tracked instance or nil. The map lock is released before
// returning.
func (m *Manager) lookup(id string) *instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.instances[id]
}

// Apply reconciles every tracked instance with the target list: instances that
// disappeared from the snapshot are stopped, changed instances are tested then
// restarted, unchanged instances are left alone.
//
// The AUTHORIZATION tightening of the whole round happens FIRST (phase 0, see
// tightenAuthorization): every instance whose authorization is narrower than the
// running core, or that is not part of the snapshot any more, is stopped before
// any technical validation of any other instance can run.
//
// Why the ordering matters: `xray -test` is serialized process wide and can
// stall for a whole validation budget. Without phase 0, instance B's revocation
// would only be enforced when its turn in the sequential loop came, so a slow
// validation of instance A could keep B's revoked users connected far beyond the
// authorization deadline. Phase 0 is bounded per instance by the stop budget and
// runs concurrently, so it costs the slowest stop of the round - not the sum.
func (m *Manager) Apply(ctx context.Context, targets []Target, opts ApplyOptions) []Result {
	if opts.PerInstanceTimeout <= 0 {
		opts.PerInstanceTimeout = m.timeouts.withDefaults().InstanceBudget()
	}
	wanted := make(map[string]bool, len(targets))
	for _, t := range targets {
		wanted[t.InstanceID] = true
	}
	results := make([]Result, 0, len(targets))
	tightened := m.tightenAuthorization(ctx, targets, wanted, opts)
	for _, res := range tightened.removed {
		results = append(results, res)
	}
	for _, t := range targets {
		if opts.Skip[t.InstanceID] {
			results = append(results, m.deferred(t))
			continue
		}
		if res, ok := tightened.failed[t.InstanceID]; ok {
			// The narrowed core could not be stopped: report that, and do not
			// start a replacement on top of a surviving process.
			results = append(results, res)
			continue
		}
		results = append(results, m.applyOne(ctx, t, opts))
	}
	sort.Slice(results, func(i, j int) bool { return results[i].InstanceID < results[j].InstanceID })
	return results
}

// tightenResult carries the two different outcomes of phase 0.
type tightenResult struct {
	// removed holds the results of instances that left the snapshot: they have no
	// target, so phase 1 has nothing left to reconcile for them.
	removed []Result
	// failed holds the instances of THIS snapshot whose narrowed core could not be
	// stopped. They keep their tracking record and must not get a new core started
	// on top of the survivor.
	failed map[string]Result
}

// tightenAuthorization is phase 0 of Apply: it stops every core whose
// authorization must be narrowed (or that left the snapshot) BEFORE any
// technical validation runs.
//
// It reuses the normal stop path, so nothing is bypassed: the final sample hook
// runs, the stop must be confirmed, a survivor keeps its tracking record and the
// pid file, and the returned StateFailed carries OldConnectionsTerminated=false.
func (m *Manager) tightenAuthorization(ctx context.Context, targets []Target, wanted map[string]bool, opts ApplyOptions) tightenResult {
	out := tightenResult{failed: map[string]Result{}}
	type job struct {
		id     string
		reason string
		// removed marks a job for an instance that is not part of the snapshot.
		removed bool
	}
	var jobs []job
	for _, id := range m.runningIDs() {
		if wanted[id] {
			continue
		}
		jobs = append(jobs, job{id: id, reason: "instance is no longer part of the snapshot", removed: true})
	}
	seen := make(map[string]bool, len(jobs))
	for _, j := range jobs {
		seen[j.id] = true
	}
	for _, t := range targets {
		if seen[t.InstanceID] || opts.Skip[t.InstanceID] {
			continue
		}
		inst := m.lookup(t.InstanceID)
		if inst == nil || !needsStopForTightening(inst, t) {
			continue
		}
		jobs = append(jobs, job{id: t.InstanceID, reason: "authorization narrowed by the snapshot"})
	}
	if len(jobs) == 0 {
		return out
	}
	results := make([]Result, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			if j.removed {
				results[i] = m.stopInstance(ctx, j.id, opts.BeforeStop, j.reason)
				return
			}
			results[i] = m.stopForReconcile(ctx, j.id, opts.BeforeStop, j.reason)
		}(i, j)
	}
	wg.Wait()
	for i, j := range jobs {
		res := results[i]
		if j.removed {
			out.removed = append(out.removed, res)
			continue
		}
		// The core is now stopped (phase 1 reconciles it from scratch) or the stop
		// could not be confirmed (phase 1 must not start a replacement).
		if res.State == StateFailed {
			out.failed[j.id] = res
		}
	}
	return out
}

// stopForReconcile stops the core of an instance that STAYS in the snapshot.
//
// Unlike stopInstance it keeps the tracking record and the last known good
// document: the caller is about to reconcile the same instance in phase 1, and a
// technical rejection there must still be able to roll back to the last good
// document under the CURRENT authorization. The instance is marked stopped with
// an empty hash so phase 1 can never take the "unchanged" shortcut for a core that
// no longer exists.
func (m *Manager) stopForReconcile(ctx context.Context, id string, hook SampleHook, reason string) Result {
	inst := m.lookup(id)
	if inst == nil {
		return Result{InstanceID: id, NodeID: id, State: StateFailed, Error: "instance vanished before it could be stopped"}
	}
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if err := m.sampleAndStop(ctx, inst, hook); err != nil {
		return Result{InstanceID: id, NodeID: inst.nodeID, State: StateFailed, Error: err.Error(), OldConnectionsTerminated: false}
	}
	inst.hash = ""
	inst.err = reason
	inst.publish()
	return Result{
		InstanceID:               id,
		NodeID:                   inst.nodeID,
		State:                    StateStopped,
		Error:                    reason,
		OldConnectionsTerminated: true,
		CoreGeneration:           inst.coreGen,
	}
}

// needsStopForTightening reports whether the running core of inst authorizes
// something the target does not: a crashed core (it must be replaced anyway), a
// revoked/remapped email, or a member that is no longer part of the target.
//
// It does not take the instance lifecycle lock: it reads the published view, which
// is immutable. The actual decision is re-checked under the lock by applyOne, so a
// stale read can only cost one extra (idempotent) stop and never a missed one.
func needsStopForTightening(inst *instance, t Target) bool {
	view := inst.view()
	if !view.running {
		return false
	}
	if processExited(view.proc) {
		return true
	}
	for email, member := range view.emailToMember {
		if t.EmailToMember[email] != member {
			return true
		}
	}
	allowed := make(map[string]bool, len(t.EmailToMember))
	for _, member := range t.EmailToMember {
		allowed[member] = true
	}
	for _, member := range view.emailToMember {
		if !allowed[member] {
			return true
		}
	}
	return false
}

// StopAll stops every running instance; used on shutdown and when local
// persistence fails.
func (m *Manager) StopAll(ctx context.Context) []Result {
	results := make([]Result, 0, len(m.runningIDs()))
	for _, id := range m.runningIDs() {
		results = append(results, m.stopInstance(ctx, id, nil, "agent stopping"))
	}
	return results
}

// Stop stops one instance by node id.
func (m *Manager) Stop(ctx context.Context, id, reason string) (Result, bool) {
	if !m.isTracked(id) {
		return Result{}, false
	}
	return m.stopInstance(ctx, id, nil, reason), true
}

// Snapshot returns the current instance states for heartbeat reporting.
func (m *Manager) Snapshot() []contracts.InstanceResult {
	list := m.instanceList()
	out := make([]contracts.InstanceResult, 0, len(list))
	for _, inst := range list {
		view := inst.view()
		out = append(out, contracts.InstanceResult{
			InstanceID:       view.instanceID,
			State:            view.reportState,
			Error:            view.err,
			AppliedUserCount: view.users,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InstanceID < out[j].InstanceID })
	return out
}

// instanceList copies the tracked instances. The map lock is released before
// returning, so callers never hold Manager.mu while touching an instance.
func (m *Manager) instanceList() []*instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]*instance, 0, len(m.instances))
	for _, inst := range m.instances {
		list = append(list, inst)
	}
	return list
}

// NextExpiry reports the earliest local user expiry across running instances.
func (m *Manager) NextExpiry() time.Time {
	var next time.Time
	for _, inst := range m.instanceList() {
		view := inst.view()
		if !view.running || view.nextExpiry.IsZero() {
			continue
		}
		if next.IsZero() || view.nextExpiry.Before(next) {
			next = view.nextExpiry
		}
	}
	return next
}

// ExpiredInstances lists running instances that have at least one authorized
// user whose expiry has passed since the last apply.
func (m *Manager) ExpiredInstances(now time.Time) []string {
	var ids []string
	for _, inst := range m.instanceList() {
		view := inst.view()
		if !view.running || view.nextExpiry.IsZero() || view.nextExpiry.After(now) {
			continue
		}
		ids = append(ids, view.instanceID)
	}
	sort.Strings(ids)
	return ids
}

// QueryStats samples every running instance through the fixed statsquery CLI.
func (m *Manager) QueryStats(ctx context.Context) []InstanceStats {
	type entry struct {
		id, node, gen, server string
		emails                map[string]string
	}
	entries := make([]entry, 0)
	for _, inst := range m.instanceList() {
		view := inst.view()
		if !view.running || view.apiPort <= 0 {
			continue
		}
		entries = append(entries, entry{
			id:     view.instanceID,
			node:   view.nodeID,
			gen:    view.coreGeneration,
			server: net.JoinHostPort(APIListen, strconv.Itoa(view.apiPort)),
			emails: view.emailToMember,
		})
	}
	out := make([]InstanceStats, 0, len(entries))
	for _, e := range entries {
		qctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		counters, err := m.runner.StatsQuery(qctx, e.server)
		cancel()
		out = append(out, InstanceStats{
			InstanceID:     e.id,
			NodeID:         e.node,
			CoreGeneration: e.gen,
			EmailToMember:  e.emails,
			Counters:       counters,
			Err:            err,
		})
	}
	return out
}

// CoreVersion queries the configured Xray binary version.
func (m *Manager) CoreVersion(ctx context.Context) (string, error) {
	return m.runner.Version(ctx)
}

func (m *Manager) applyOne(ctx context.Context, t Target, opts ApplyOptions) Result {
	inst, err := m.getOrCreate(t.InstanceID, t.NodeID)
	if err != nil {
		return Result{InstanceID: t.InstanceID, NodeID: t.NodeID, State: StateFailed, Error: err.Error()}
	}
	inst.mu.Lock()
	defer inst.mu.Unlock()
	inst.pendingExpiry = t.NextExpiry
	if inst.proc != nil && (processExited(inst.proc) || authorizationTightened(inst, t)) {
		if err := m.sampleAndStop(ctx, inst, opts.BeforeStop); err != nil {
			return m.failLocked(inst, t, err)
		}
	}
	if !opts.Force && inst.proc != nil && inst.hash == t.Hash && inst.state != StateFailed {
		// Future user expiries are authorization metadata, not part of the Xray
		// document hash. Commit the full list even if the earliest expiry did not
		// move; otherwise persistResult would write stale expiries back to disk
		// and a local expiry rebuild could silently keep the user authorized.
		inst.usersList = copyUsers(t.Users)
		inst.nextExpiry = t.NextExpiry
		inst.publish()
		return Result{
			InstanceID:       inst.id,
			NodeID:           inst.nodeID,
			State:            StateUnchanged,
			AppliedUserCount: inst.users,
			CoreGeneration:   inst.coreGen,
			Hash:             inst.hash,
			Document:         inst.doc,
			EmailToMember:    copyMap(inst.emailToMember),
			Users:            copyUsers(inst.usersList),
			NextExpiry:       inst.nextExpiry,
		}
	}
	opCtx, cancel := context.WithTimeout(ctx, opts.PerInstanceTimeout)
	defer cancel()
	return m.replace(opCtx, inst, t, opts)
}

func (m *Manager) replace(ctx context.Context, inst *instance, t Target, opts ApplyOptions) Result {
	dir, err := m.instanceDir(inst.id)
	if err != nil {
		return m.failLocked(inst, t, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return m.failLocked(inst, t, fmt.Errorf("create instance directory: %w", err))
	}
	cfgPath := filepath.Join(dir, "config.json")
	if inst.proc == nil {
		// Only look for a leftover process when this manager does not own a
		// running one (otherwise the pid record describes our own core).
		if err := m.reapStale(dir); err != nil {
			// A process we cannot prove is ours must not be killed; report the
			// instance as failed and leave the unknown process untouched.
			m.sampleAndStop(ctx, inst, opts.BeforeStop)
			return m.failLocked(inst, t, err)
		}
	}
	port, err := m.portFn(m.reservedPorts(t.Document))
	if err != nil {
		return m.rollbackOrFail(ctx, inst, t, opts, fmt.Errorf("allocate management API port: %w", err))
	}
	config, err := BuildEffective(t.Document, port)
	if err != nil {
		return m.rollbackOrFail(ctx, inst, t, opts, fmt.Errorf("build effective config: %w", err))
	}
	if err := writeFileAtomic(cfgPath, config, 0o600); err != nil {
		return m.rollbackOrFail(ctx, inst, t, opts, fmt.Errorf("write instance config: %w", err))
	}
	// The new configuration is validated while the previous core is still
	// running, with a budget of its own: a rejected configuration must never take
	// a healthy instance down, and a stalled step must never make this validation
	// fail with a killed process.
	if err := m.validateConfig(ctx, cfgPath); err != nil {
		// A validation timeout is not a verdict about the document: the run may
		// never have started because another instance held the serialized test
		// slot. Treating that as "config is invalid" would stop a healthy core
		// and start a rollback for a document nobody rejected, so the previous
		// core keeps serving and the next poll retries the same target.
		if errors.Is(err, ErrValidationTimeout) {
			return m.deferValidation(inst, t, err)
		}
		return m.rollbackOrFail(ctx, inst, t, opts, fmt.Errorf("config validation failed: %w", err))
	}
	// Controlled stop: collect the last usage, then terminate the old process so
	// connections authorized by the previous snapshot cannot survive.
	if err := m.sampleAndStop(ctx, inst, opts.BeforeStop); err != nil {
		return m.failLocked(inst, t, err)
	}
	proc, gen, startErr := m.startInstance(ctx, inst, dir, cfgPath, t.Document, &port)
	if startErr != nil {
		return m.rollbackOrFail(ctx, inst, t, opts, fmt.Errorf("start instance: %w", startErr))
	}
	inst.proc = proc
	inst.coreGen = gen
	inst.apiPort = port
	inst.state = StateApplied
	inst.err = ""
	inst.doc = t.Document
	inst.hash = t.Hash
	inst.lastGood = t.Document
	inst.lastGoodHash = t.Hash
	inst.emailToMember = copyMap(t.EmailToMember)
	inst.usersList = copyUsers(t.Users)
	inst.users = len(t.EmailToMember)
	inst.nextExpiry = t.NextExpiry
	inst.startedAt = m.now()
	inst.publish()
	return Result{
		InstanceID:               inst.id,
		NodeID:                   inst.nodeID,
		State:                    StateApplied,
		AppliedUserCount:         inst.users,
		OldConnectionsTerminated: true,
		CoreGeneration:           gen,
		Hash:                     t.Hash,
		Document:                 t.Document,
		Users:                    copyUsers(t.Users),
		EmailToMember:            copyMap(t.EmailToMember),
		NextExpiry:               t.NextExpiry,
	}
}

// startInstance starts the core, retrying once with a fresh management API port
// when the first attempt failed (for example because a leftover process still
// holds the port).
func (m *Manager) startInstance(ctx context.Context, inst *instance, dir, cfgPath string, doc map[string]any, port *int) (Proc, string, error) {
	spec := StartSpec{
		InstanceID: inst.id,
		ConfigPath: cfgPath,
		LogPath:    filepath.Join(dir, "xray.log"),
		Secrets:    m.secretValues(),
	}
	proc, gen, err := m.startAndWait(ctx, inst, dir, cfgPath, spec, *port)
	if err == nil {
		return proc, gen, nil
	}
	m.logger.Printf("agent: instance %s: first start attempt failed: %v", inst.id, err)
	// A leftover process may still hold the port. When the failed attempt could
	// not be proven dead its pid record was kept, so this is what makes the retry
	// self healing instead of failing on a busy inbound port forever.
	if rerr := m.reapStale(dir); rerr != nil {
		return nil, "", rerr
	}
	nextPort, perr := m.portFn(m.reservedPorts(doc))
	if perr != nil {
		return nil, "", err
	}
	config, cerr := BuildEffective(doc, nextPort)
	if cerr != nil {
		return nil, "", err
	}
	if werr := writeFileAtomic(cfgPath, config, 0o600); werr != nil {
		return nil, "", err
	}
	// Fresh validation budget: the first attempt may already have consumed the
	// readiness budget of this instance. A validation timeout here is discarded
	// like any other validation error: the original start failure is the cause
	// the caller has to act on.
	if terr := m.validateConfig(ctx, cfgPath); terr != nil {
		return nil, "", err
	}
	proc, gen, err = m.startAndWait(ctx, inst, dir, cfgPath, spec, nextPort)
	if err != nil {
		return nil, "", err
	}
	*port = nextPort
	return proc, gen, nil
}

// startAndWait starts the core, records its identity and waits until its
// management API answers, so a successful start really means the instance is
// serving.
//
// The pid record is written before the readiness wait: a core that has to be
// stopped here must stay reclaimable by the next attempt instead of surviving as
// an orphan that holds the inbound port.
func (m *Manager) startAndWait(ctx context.Context, inst *instance, dir, cfgPath string, spec StartSpec, apiPort int) (Proc, string, error) {
	// Last moment gate: an overload/disk protection that engaged while this
	// apply was already running must not be bypassed by starting a core and
	// stopping it asynchronously afterwards.
	if err := m.checkStartGuard(); err != nil {
		return nil, "", err
	}
	if !inst.pendingExpiry.IsZero() && !m.now().Before(inst.pendingExpiry) {
		return nil, "", fmt.Errorf("authorization expired during validation; rebuild required")
	}
	proc, err := m.runner.Start(ctx, spec)
	if err != nil {
		return nil, "", err
	}
	gen := m.reportStarted(inst, dir, cfgPath, proc)
	if err := m.waitReady(ctx, proc, apiPort); err != nil {
		m.abandonStarted(inst, dir, proc)
		return nil, "", err
	}
	if !inst.pendingExpiry.IsZero() && !m.now().Before(inst.pendingExpiry) {
		m.abandonStarted(inst, dir, proc)
		return nil, "", fmt.Errorf("authorization expired during readiness; rebuild required")
	}
	return proc, gen, nil
}

// abandonStarted stops a core whose start failed and keeps its pid record when
// the process could not be proven dead, so the next attempt can reclaim it.
func (m *Manager) abandonStarted(inst *instance, dir string, proc Proc) {
	if m.stopProc(inst, proc) {
		removePidFile(dir)
		return
	}
	m.logger.Printf("agent: ALERT: instance %s: keeping %s for the surviving core pid %d so the next attempt can reclaim it", inst.id, pidFileName, proc.PID())
}

// waitReady polls the management API of a freshly started core until it answers
// or the readiness budget is exhausted.
//
// A single slow probe never ends the wait: on a slow host (nested
// virtualization) the core logs "started" long before the stats service answers,
// so the loop retries every readinessPollInterval until the configurable budget
// is used up. The budget is independent of the stop budget.
func (m *Manager) waitReady(ctx context.Context, proc Proc, apiPort int) error {
	if apiPort <= 0 {
		return nil
	}
	budget := m.timeouts.withDefaults().Readiness
	server := net.JoinHostPort(APIListen, strconv.Itoa(apiPort))
	deadline := m.now().Add(budget)
	var lastErr error
	for {
		select {
		case <-proc.Exited():
			return fmt.Errorf("core exited during startup: %v", proc.ExitError())
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		probe := readinessProbeTimeout
		if remaining := deadline.Sub(m.now()); remaining > 0 && remaining < probe {
			probe = remaining
		}
		qctx, cancel := context.WithTimeout(ctx, probe)
		_, err := m.runner.StatsQuery(qctx, server)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !m.now().Before(deadline) {
			if out := proc.Output(); len(out) > 0 {
				return fmt.Errorf("core management API did not answer within %s (last output: %s)", budget, out[len(out)-1])
			}
			return fmt.Errorf("core management API did not answer within %s: %v", budget, lastErr)
		}
		wait := readinessPollInterval
		if remaining := deadline.Sub(m.now()); remaining > 0 && remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-proc.Exited():
			timer.Stop()
			return fmt.Errorf("core exited during startup: %v", proc.ExitError())
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (m *Manager) reportStarted(inst *instance, dir, cfgPath string, proc Proc) string {
	gen := newCoreGeneration(inst.nodeID, m.now())
	rec := pidRecord{
		PID:            proc.PID(),
		ConfigPath:     cfgPath,
		InstanceID:     inst.id,
		CoreGeneration: gen,
		RecordedAt:     m.now().UTC().Unix(),
	}
	if live, found, err := m.inspect(proc.PID()); err != nil {
		// Without a provable identity a future leftover process must never be
		// killed automatically.
		m.logger.Printf("agent: instance %s: cannot record the core process identity (%v); leftover processes will need manual cleanup", inst.id, err)
	} else if found {
		rec.Created = live.Created
		rec.Executable = live.Executable
	}
	if werr := writePidFile(dir, rec); werr != nil {
		m.logger.Printf("agent: instance %s: cannot persist pid file: %v", inst.id, werr)
	}
	m.mu.Lock()
	m.owned[rec.PID] = true
	m.mu.Unlock()
	return gen
}

// rollbackOrFail handles a failed apply.
//
// The last known good configuration is built and validated while the previous
// core is still running: a rollback that cannot be validated must not take a
// healthy instance down. Only a verified replacement is started, and it never
// reuses the budget of the attempt that just failed.
func (m *Manager) rollbackOrFail(ctx context.Context, inst *instance, t Target, opts ApplyOptions, cause error) Result {
	plan, perr := m.prepareRollback(ctx, inst, t)
	if perr != nil {
		m.logger.Printf("agent: instance %s: no verified last known good config available: %v", inst.id, perr)
	} else {
		// The replacement is verified: stop the old core now (this also enforces
		// the current authorization) and start the rollback config immediately.
		if err := m.sampleAndStop(ctx, inst, opts.BeforeStop); err != nil {
			return m.failLocked(inst, t, err)
		}
		if res, ok := m.startRollback(ctx, inst, t, plan); ok {
			res.Error = cause.Error()
			m.logger.Printf("agent: instance %s: applied last known good technical config with current authorization: %v", inst.id, cause)
			return res
		}
	}
	// Nothing verified could be applied. The old core may still authorize users
	// that the current snapshot revoked, so it must not keep serving.
	m.sampleAndStop(ctx, inst, opts.BeforeStop)
	return m.failLocked(inst, t, cause)
}

// rollbackPlan is a rollback configuration that was built and validated while the
// previous core was still running.
type rollbackPlan struct {
	cfgPath       string
	doc           map[string]any
	port          int
	hash          string
	emailToMember map[string]string
}

// prepareRollback builds the last good technical document filtered by the current
// authorization, writes it and validates it with the independent rollback budget.
func (m *Manager) prepareRollback(ctx context.Context, inst *instance, t Target) (*rollbackPlan, error) {
	if inst.lastGood == nil {
		return nil, fmt.Errorf("no last known good configuration")
	}
	dir, err := m.instanceDir(inst.id)
	if err != nil {
		return nil, err
	}
	filtered, err := FilterDocument(inst.lastGood, t.Authorization)
	if err != nil {
		return nil, err
	}
	port, err := m.portFn(m.reservedPorts(filtered.Document))
	if err != nil {
		return nil, err
	}
	config, err := BuildEffective(filtered.Document, port)
	if err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(dir, "config.rollback.json")
	if err := writeFileAtomic(cfgPath, config, 0o600); err != nil {
		return nil, err
	}
	// A detached, more generous budget: the failure that triggered the rollback
	// has usually already consumed the validation and readiness budgets of this
	// instance, and reusing them would reject a config that is perfectly fine.
	// The test itself is serialized like every other `xray -test` of this agent.
	if terr := m.validateWithBudget(ctx, cfgPath, m.timeouts.withDefaults().rollbackBudget()); terr != nil {
		m.logger.Printf("agent: instance %s: rollback config rejected: %v", inst.id, terr)
		return nil, terr
	}
	hash, _ := HashDocument(filtered.Document)
	return &rollbackPlan{
		cfgPath:       cfgPath,
		doc:           filtered.Document,
		port:          port,
		hash:          hash,
		emailToMember: filtered.EmailToMember,
	}, nil
}

// startRollback starts a validated rollback configuration with the independent
// rollback budget.
func (m *Manager) startRollback(ctx context.Context, inst *instance, t Target, plan *rollbackPlan) (Result, bool) {
	dir, err := m.instanceDir(inst.id)
	if err != nil {
		return Result{}, false
	}
	sctx, cancel := stepContext(ctx, m.timeouts.withDefaults().rollbackBudget(), true)
	proc, gen, err := m.startInstance(sctx, inst, dir, plan.cfgPath, plan.doc, &plan.port)
	cancel()
	if err != nil {
		m.logger.Printf("agent: instance %s: rollback start failed: %v", inst.id, err)
		return Result{}, false
	}
	inst.proc = proc
	inst.coreGen = gen
	inst.apiPort = plan.port
	inst.state = StateRollback
	inst.err = ""
	inst.doc = plan.doc
	inst.hash = plan.hash
	inst.emailToMember = copyMap(plan.emailToMember)
	inst.usersList = authorizedSubset(t.Users, plan.emailToMember)
	inst.users = len(plan.emailToMember)
	inst.nextExpiry = t.NextExpiry
	inst.startedAt = m.now()
	inst.publish()
	return Result{
		InstanceID:               inst.id,
		NodeID:                   inst.nodeID,
		State:                    StateRollback,
		AppliedUserCount:         inst.users,
		OldConnectionsTerminated: true,
		CoreGeneration:           gen,
		Hash:                     plan.hash,
		Document:                 plan.doc,
		EmailToMember:            copyMap(plan.emailToMember),
		Users:                    authorizedSubset(t.Users, plan.emailToMember),
		NextExpiry:               t.NextExpiry,
	}, true
}

// failLocked marks the instance failed and reclaims any core process that
// survived the stop.
//
// A surviving core keeps the inbound port busy, so without this cleanup every
// later retry fails with the same error until an operator kills the process by
// hand. The reclaim is verified (creation identity, executable, instance
// directory), so it can never kill a foreign or reused pid.
func (m *Manager) failLocked(inst *instance, t Target, cause error) Result {
	inst.hash = ""
	inst.state = StateFailed
	inst.err = cause.Error()
	if inst.proc == nil {
		inst.users = 0
		inst.emailToMember = map[string]string{}
	}
	inst.publish()
	if dir, err := m.instanceDir(inst.id); err == nil && inst.proc == nil {
		if rerr := m.reapStale(dir); rerr != nil {
			m.logger.Printf("agent: instance %s: leftover core process could not be reclaimed: %v", inst.id, rerr)
		}
	}
	return Result{
		InstanceID:               inst.id,
		NodeID:                   t.NodeID,
		State:                    StateFailed,
		Error:                    cause.Error(),
		OldConnectionsTerminated: inst.proc == nil,
		Hash:                     "",
	}
}

func (m *Manager) deferred(t Target) Result {
	return Result{
		InstanceID: t.InstanceID,
		NodeID:     t.NodeID,
		State:      StateDeferred,
		Error:      "previous apply failed; waiting for retry backoff",
		Hash:       "",
	}
}

// deferValidation reports a `xray -test` that ran out of budget while waiting for
// the serialized test slot.
//
// This is not a verdict about the document and not a failure of the instance: the
// running core is left untouched, the state, the committed hash and the applied
// user count are unchanged, and the supervisor treats StateDeferred as "nothing
// was attempted this poll", so the same target is retried on the next poll without
// entering a rollback or a failed/backoff state.
func (m *Manager) deferValidation(inst *instance, t Target, cause error) Result {
	m.logger.Printf("agent: instance %s: config validation deferred, the serialized `xray -test` slot was not available in time: %v", inst.id, cause)
	view := inst.view()
	return Result{
		InstanceID:               inst.id,
		NodeID:                   inst.nodeID,
		State:                    StateDeferred,
		OldConnectionsTerminated: inst.proc == nil,
		Error:                    cause.Error(),
		AppliedUserCount:         view.users,
		CoreGeneration:           view.coreGeneration,
		Hash:                     view.hash,
		Document:                 view.document,
		EmailToMember:            copyMap(view.emailToMember),
		Users:                    copyUsers(view.usersList),
		NextExpiry:               view.nextExpiry,
	}
}

// sampleAndStop collects the final usage counters and stops the core of an
// instance.
//
// The stop uses its own budget (independent of the readiness budget) and the pid
// record is only removed once the process is provably gone. A survivor keeps its
// record so reapStale can prove ownership on the next attempt and reclaim it,
// which is what makes a failed apply self healing instead of terminal.
func (m *Manager) sampleAndStop(ctx context.Context, inst *instance, hook SampleHook) error {
	if inst.proc == nil {
		return nil
	}
	if hook == nil {
		hook = m.beforeStop
	}
	if hook != nil && inst.apiPort > 0 {
		hctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		hook(hctx, inst.nodeID, inst.coreGen, net.JoinHostPort(APIListen, strconv.Itoa(inst.apiPort)), copyMap(inst.emailToMember))
		cancel()
	}
	proc := inst.proc
	gone := m.stopProc(inst, proc)
	if !gone {
		inst.state = StateFailed
		inst.err = "core did not stop; cleanup remains pending"
		inst.publish()
		return errors.New(inst.err)
	}
	inst.proc = nil
	inst.state = StateStopped
	inst.users = 0
	inst.emailToMember = map[string]string{}
	inst.usersList = nil
	inst.nextExpiry = time.Time{}
	inst.apiPort = 0
	inst.publish()
	dir, derr := m.instanceDir(inst.id)
	if derr == nil {
		removePidFile(dir)
	}
	return nil
}

// stopProc terminates one core process with the stop budget and reports whether
// it is provably gone. The pid leaves the owned set first so reapStale can
// reclaim it when it survives.
func (m *Manager) stopProc(inst *instance, proc Proc) bool {
	if proc == nil {
		return true
	}
	m.mu.Lock()
	delete(m.owned, proc.PID())
	m.mu.Unlock()
	sctx, cancel := context.WithTimeout(context.Background(), m.timeouts.withDefaults().Stop)
	err := proc.Stop(sctx)
	cancel()
	if err != nil {
		m.logger.Printf("agent: instance %s: stop returned %v", inst.id, err)
	}
	if exitedWithin(proc, stopVerifyWindow) {
		return true
	}
	// Secondary confirmation for a Proc whose exit signal is unreliable: a pid
	// that no longer exists cannot be holding the inbound port.
	if _, found, ierr := m.inspect(proc.PID()); ierr == nil && !found {
		return true
	}
	// A verified fallback kill is useful even when a Proc stop implementation
	// failed. reapStale confirms exit before removing its recovery record.
	if dir, err := m.instanceDir(inst.id); err == nil {
		if err = m.reapStale(dir); err == nil {
			if _, found, ierr := m.inspect(proc.PID()); ierr == nil && !found {
				return true
			}
		}
	}
	if out := proc.Output(); len(out) > 0 {
		m.logger.Printf("agent: instance %s: last core output: %s", inst.id, out[len(out)-1])
	}
	m.logger.Printf("agent: instance %s: core pid %d is still alive after the %s stop budget", inst.id, proc.PID(), m.timeouts.withDefaults().Stop)
	return false
}

// stopVerifyWindow is how long the manager waits for a stopped core to disappear
// before it treats the process as a survivor.
var stopVerifyWindow = 2 * time.Second

// exitedWithin reports whether the process exited within the given window.
func exitedWithin(proc Proc, window time.Duration) bool {
	select {
	case <-proc.Exited():
		return true
	default:
	}
	timer := time.NewTimer(window)
	defer timer.Stop()
	select {
	case <-proc.Exited():
		return true
	case <-timer.C:
	}
	select {
	case <-proc.Exited():
		return true
	default:
		return false
	}
}

// validateConfig runs `xray -test` with a budget of its own.
//
// The validation is detached from the per instance deadline: on a slow host an
// earlier stalled step must never make it fail with a killed process (which is
// what turned a slow start into an unrecoverable failure). A shutdown still
// aborts it.
func (m *Manager) validateConfig(ctx context.Context, cfgPath string) error {
	return m.validateWithBudget(ctx, cfgPath, m.timeouts.withDefaults().Validate)
}

// ErrValidationTimeout reports that a `xray -test` run did not finish inside its
// budget, including the time it spent waiting for the serialized test slot.
//
// It is deliberately distinct from a rejected configuration: `xray -test` starts
// the core's StatsService, so concurrent runs block each other for minutes on a
// loaded host. A valid document that only queued too long must never be reported
// as invalid, must never trigger a rollback and must never mark the instance
// failed.
var ErrValidationTimeout = errors.New("config validation timed out")

// validateWithBudget runs one serialized `xray -test` with an explicit budget.
func (m *Manager) validateWithBudget(ctx context.Context, cfgPath string, budget time.Duration) error {
	vctx, cancel := stepContext(ctx, budget, true)
	err := m.runTestSerialized(vctx, cfgPath)
	// The budget state is sampled before cancel(), otherwise a real rejection
	// would be indistinguishable from a run that was cut short.
	expired := vctx.Err()
	cancel()
	if err != nil && errors.Is(expired, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrValidationTimeout, err)
	}
	return err
}

// runTestSerialized runs m.runner.Test while holding the manager wide validation
// slot, so two `xray -test` processes of this agent never run at the same time.
//
// Why this exists: `xray -test` instantiates the StatsService (api/stats/policy).
// Five concurrent runs against the same host took 116 seconds each instead of
// 26 milliseconds, which is what produced the historical "xray -test failed:
// signal: killed" and "core management API did not answer within 5s" reports.
// Waiting for the slot is part of the caller's budget: a run that cannot start in
// time returns a timeout instead of testing after it was reported as out of time.
func (m *Manager) runTestSerialized(ctx context.Context, cfgPath string) error {
	if err := m.validateMu.lock(ctx); err != nil {
		return fmt.Errorf("no free `xray -test` slot within the validation budget: %w", err)
	}
	defer m.validateMu.unlock()
	// Re-check the budget: lock can be granted at the very instant the deadline
	// fires, and select would otherwise pick the slot at random.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("no free `xray -test` slot within the validation budget: %w", err)
	}
	return m.runner.Test(ctx, cfgPath)
}

// stepContext returns a context with its own budget for one lifecycle step.
//
// When detachDeadline is set the parent deadline is ignored, so a step always
// gets its full budget even after an earlier step consumed the per instance
// budget. Cancellation of the parent (shutdown) is always propagated.
func stepContext(parent context.Context, budget time.Duration, detachDeadline bool) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), budget)
	stop := context.AfterFunc(parent, func() {
		if detachDeadline && errors.Is(parent.Err(), context.DeadlineExceeded) {
			return
		}
		cancel()
	})
	return ctx, func() {
		stop()
		cancel()
	}
}

func (m *Manager) stopInstance(ctx context.Context, id string, hook SampleHook, reason string) Result {
	if _, err := m.instanceDir(id); err != nil {
		return Result{InstanceID: id, State: StateFailed, Error: err.Error()}
	}
	// Look the instance up instead of creating it: stopping something that is not
	// tracked must not fabricate state.
	inst := m.lookup(id)
	if inst == nil {
		return Result{InstanceID: id, NodeID: id, State: StateStopped, Error: reason}
	}
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if err := m.sampleAndStop(ctx, inst, hook); err != nil {
		return Result{InstanceID: id, NodeID: inst.nodeID, State: StateFailed, Error: err.Error(), OldConnectionsTerminated: false}
	}
	inst.hash = ""
	inst.users = 0
	inst.emailToMember = map[string]string{}
	inst.nextExpiry = time.Time{}
	inst.err = reason
	inst.publish()
	m.mu.Lock()
	delete(m.instances, id)
	m.mu.Unlock()
	return Result{
		InstanceID:               id,
		NodeID:                   inst.nodeID,
		State:                    StateStopped,
		Error:                    reason,
		OldConnectionsTerminated: true,
	}
}

// getOrCreate returns the tracked instance, creating it when unknown.
func (m *Manager) getOrCreate(id, nodeID string) (*instance, error) {
	if _, err := m.instanceDir(id); err != nil {
		return nil, err
	}
	return m.ensure(id, nodeID), nil
}

func (m *Manager) isTracked(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.instances[id]
	return ok
}

func (m *Manager) runningIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.instances))
	for id := range m.instances {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// instanceDir returns the per-instance directory and refuses anything that is
// not a plain single path element inside the instance root.
func (m *Manager) instanceDir(id string) (string, error) {
	if !instanceIDPattern.MatchString(id) || id == "." || id == ".." {
		return "", fmt.Errorf("instance id %q is not a safe directory name", id)
	}
	base := filepath.Join(m.root, "instances")
	dir := filepath.Join(base, id)
	rel, err := filepath.Rel(base, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("instance directory %q escapes the instance root", dir)
	}
	return dir, nil
}

func (m *Manager) reservedPorts(doc map[string]any) map[int]bool {
	reserved := map[int]bool{}
	raw, ok := doc["inbounds"].([]any)
	if !ok {
		return reserved
	}
	for _, r := range raw {
		in, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if port, err := inboundPort(in); err == nil {
			reserved[port] = true
		}
	}
	return reserved
}

// reapStale stops a leftover core process recorded in the instance directory,
// but only when its identity can be proven: the recorded process creation
// identity, the full executable path and the instance directory of the recorded
// config must all match. Anything else returns ErrUnverifiedProcess and the
// process is left running, because a reused pid must never be killed.
//
// The record is matched against the instance directory rather than one exact
// config file: a core left behind by a failed rollback is started with
// config.rollback.json and must still be reclaimable.
func (m *Manager) reapStale(dir string) error {
	rec, err := readPidFile(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %s is unreadable (%v); check for a leftover core process manually", ErrUnverifiedProcess, filepath.Join(dir, pidFileName), err)
	}
	if rec.PID <= 0 {
		removePidFile(dir)
		return nil
	}
	if m.isOwned(rec.PID) {
		// This process belongs to the running instance of this manager.
		return nil
	}
	live, found, perr := m.inspect(rec.PID)
	if perr != nil {
		return fmt.Errorf("%w: cannot inspect pid %d (%v)", ErrUnverifiedProcess, rec.PID, perr)
	}
	if !found {
		// The recorded process is gone: nothing to reclaim.
		removePidFile(dir)
		return nil
	}
	if verr := m.verifyOwnership(rec, live, dir); verr != nil {
		return verr
	}
	if terr := terminateProcess(rec.PID); terr != nil {
		return fmt.Errorf("stop leftover core process %d: %w", rec.PID, terr)
	}
	deadline := time.Now().Add(stopVerifyWindow)
	for {
		_, found, err := m.inspect(rec.PID)
		if err == nil && !found {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("leftover core pid %d exit is not confirmed", rec.PID)
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.logger.Printf("agent: stopped leftover core process pid=%d in %s", rec.PID, dir)
	removePidFile(dir)
	return nil
}

func (m *Manager) verifyOwnership(rec pidRecord, live ProcessIdentity, dir string) error {
	if rec.Created == "" || live.Created == "" {
		return fmt.Errorf("%w: pid %d has no recorded process creation identity", ErrUnverifiedProcess, rec.PID)
	}
	if !creationIdentityMatches(rec.Created, live.Created) {
		return fmt.Errorf("%w: pid %d creation identity changed (recorded %s, live %s); the pid was reused", ErrUnverifiedProcess, rec.PID, rec.Created, live.Created)
	}
	if rec.Executable == "" || !samePath(rec.Executable, live.Executable) {
		return fmt.Errorf("%w: pid %d executable %q does not match the recorded %q", ErrUnverifiedProcess, rec.PID, live.Executable, rec.Executable)
	}
	if m.xrayPath != "" && !samePath(m.xrayPath, live.Executable) {
		return fmt.Errorf("%w: pid %d executable %q is not the configured core %q", ErrUnverifiedProcess, rec.PID, live.Executable, m.xrayPath)
	}
	if dir == "" || rec.ConfigPath == "" || !samePath(filepath.Dir(rec.ConfigPath), dir) {
		return fmt.Errorf("%w: pid %d is recorded for config %q, which is not inside %q", ErrUnverifiedProcess, rec.PID, rec.ConfigPath, dir)
	}
	if live.CommandLine != "" && !strings.Contains(live.CommandLine, rec.ConfigPath) {
		return fmt.Errorf("%w: pid %d command line does not reference %q", ErrUnverifiedProcess, rec.PID, rec.ConfigPath)
	}
	return nil
}

// creationIdentityMatches compares the recorded and live creation identity of a
// process.
//
// The identity is "<boot time>/<starttime>". starttime (proc/PID/stat field 22)
// is a monotonically increasing tick count since boot and never changes for a
// live process, so it is the part that actually proves "this pid was not
// reused". The boot time prefix only disambiguates two processes that happen to
// share a starttime across different boots.
//
// Requiring an exact match on the boot prefix is unsafe: /proc/stat's btime is
// derived from the current wall clock minus uptime and is therefore *not*
// stable when the clock is adjusted or the machine is suspended. Under WSL2 the
// observed btime moved by seconds to minutes within a single boot, which made
// every recorded identity look "reused" and permanently blocked reclaiming the
// leftover cores (the agent logged "creation identity changed … the pid was
// reused" for processes it had started itself, and the instance stayed failed
// with two cores fighting over the inbound port).
//
// So: the starttime component must match exactly, and the boot component is
// compared leniently - equal, either side unknown, or close enough that a clock
// adjustment (rather than a reboot) explains the difference. A genuine reboot
// changes btime far more than the tolerance, and a reused pid without a reboot
// cannot keep the same starttime.
func creationIdentityMatches(recorded, live string) bool {
	recBoot, recStart, recOK := splitCreationIdentity(recorded)
	liveBoot, liveStart, liveOK := splitCreationIdentity(live)
	if !recOK || !liveOK {
		// Unknown format: fall back to the strict comparison so an unparsable
		// identity is never silently trusted.
		return recorded == live
	}
	if recStart != liveStart {
		return false
	}
	if recBoot == liveBoot {
		return true
	}
	recSeconds, errA := strconv.ParseInt(recBoot, 10, 64)
	liveSeconds, errB := strconv.ParseInt(liveBoot, 10, 64)
	if errA != nil || errB != nil {
		// "?" is what bootTime() returns when /proc/stat is unreadable. Treat an
		// unknown boot time as "cannot contradict", since starttime already matched.
		return true
	}
	delta := recSeconds - liveSeconds
	if delta < 0 {
		delta = -delta
	}
	return delta <= creationIdentityBootTolerance
}

// creationIdentityBootTolerance bounds how far the boot-time component of a
// creation identity may drift before the identity is treated as a different
// boot. A real reboot shifts btime by at least the uptime of the previous boot
// (minutes to days), while clock adjustment and suspend/resume shift it by
// seconds. One hour is comfortably inside that gap.
const creationIdentityBootTolerance = 3600

func splitCreationIdentity(id string) (boot, start string, ok bool) {
	idx := strings.LastIndex(id, "/")
	if idx <= 0 || idx == len(id)-1 {
		return "", "", false
	}
	return id[:idx], id[idx+1:], true
}

// SetSecrets registers the values that must be redacted from core output. It must
// be called before Apply/Stop run (startup wiring only).
func (m *Manager) SetSecrets(fn func() []string) {
	if fn != nil {
		m.secrets = fn
	}
}

// isOwned reports whether the pid belongs to a core process this manager
// started and still tracks.
func (m *Manager) isOwned(pid int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.owned[pid]
}

func (m *Manager) secretValues() []string {
	if m.secrets == nil {
		return nil
	}
	return m.secrets()
}

func allocatePort(reserved map[int]bool) (int, error) {
	for attempt := 0; attempt < 64; attempt++ {
		var buf [2]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return 0, err
		}
		span := MaxAPIPort - MinAPIPort + 1
		port := MinAPIPort + int(buf[0])<<8 + int(buf[1])
		port = MinAPIPort + ((port-MinAPIPort)%span+span)%span
		if reserved[port] {
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(APIListen, strconv.Itoa(port)))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return port, nil
	}
	return 0, fmt.Errorf("no free management API port found")
}

func newCoreGeneration(nodeID string, now time.Time) string {
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("%s-%d-%s", nodeID, now.UTC().UnixNano(), hex.EncodeToString(buf[:]))
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil && !os.IsPermission(err) {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// authorizedSubset keeps only users that survived the authorization filter.
func authorizedSubset(users []contracts.AuthorizedUser, kept map[string]string) []contracts.AuthorizedUser {
	var out []contracts.AuthorizedUser
	for _, u := range users {
		for _, member := range kept {
			if member == u.MemberID {
				out = append(out, u)
				break
			}
		}
	}
	return out
}

func copyUsers(in []contracts.AuthorizedUser) []contracts.AuthorizedUser {
	if len(in) == 0 {
		return nil
	}
	out := make([]contracts.AuthorizedUser, len(in))
	copy(out, in)
	return out
}

func copyMap(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
