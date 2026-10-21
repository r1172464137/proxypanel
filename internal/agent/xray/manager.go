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
)

// Default instance apply budget. The contract requires config validation plus
// stop/start to fit into 60 seconds per snapshot.
const (
	DefaultInstanceTimeout = 45 * time.Second
	MinAPIPort             = 20000
	MaxAPIPort             = 60000
	readinessTimeout       = 5 * time.Second
)

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
//	instance.mu  ->  Manager.mu
//
// Manager.mu only guards the instance map and the owned pid set. It is a leaf
// lock: it is never held while acquiring an instance lock and never held across
// I/O, logging or a hook call. instance.mu serializes the start/stop decisions of
// one instance. Readers (Snapshot, NextExpiry, ExpiredInstances, QueryStats) do
// not take instance.mu at all: they read the immutable view that the lifecycle
// owner publishes, so a config test or start in progress can never stall the
// heartbeat, the stats loop or shutdown.
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
	// published holds the reader visible state and is replaced atomically by
	// publish, so readers never wait for a lifecycle operation.
	published atomic.Pointer[instanceView]
}

// instanceView is an immutable copy of the fields readers need.
type instanceView struct {
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
	root     string
	xrayPath string
	certRoot string
	runner   Runner
	logger   *log.Logger
	now      func() time.Time
	portFn   func(reserved map[int]bool) (int, error)
	secrets  func() []string

	// mu guards instances and owned only; leaf lock, never held across I/O.
	mu        sync.Mutex
	instances map[string]*instance
	owned     map[int]bool
}

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
		root:      abs,
		xrayPath:  xrayPath,
		certRoot:  filepath.Join(abs, "certs"),
		runner:    runner,
		logger:    logger,
		now:       time.Now,
		portFn:    allocatePort,
		instances: map[string]*instance{},
		owned:     map[int]bool{},
	}, nil
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
func (m *Manager) Apply(ctx context.Context, targets []Target, opts ApplyOptions) []Result {
	if opts.PerInstanceTimeout <= 0 {
		opts.PerInstanceTimeout = DefaultInstanceTimeout
	}
	wanted := make(map[string]bool, len(targets))
	for _, t := range targets {
		wanted[t.InstanceID] = true
	}
	results := make([]Result, 0, len(targets))
	for _, id := range m.runningIDs() {
		if wanted[id] {
			continue
		}
		results = append(results, m.stopInstance(ctx, id, opts.BeforeStop, "instance is no longer part of the snapshot"))
	}
	for _, t := range targets {
		if opts.Skip[t.InstanceID] {
			results = append(results, m.deferred(t))
			continue
		}
		results = append(results, m.applyOne(ctx, t, opts))
	}
	sort.Slice(results, func(i, j int) bool { return results[i].InstanceID < results[j].InstanceID })
	return results
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
		if err := m.reapStale(dir, cfgPath); err != nil {
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
	if err := m.runner.Test(ctx, cfgPath); err != nil {
		return m.rollbackOrFail(ctx, inst, t, opts, fmt.Errorf("config validation failed: %w", err))
	}
	// Controlled stop: collect the last usage, then terminate the old process so
	// connections authorized by the previous snapshot cannot survive.
	m.sampleAndStop(ctx, inst, opts.BeforeStop)
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
// when the first attempt failed (for example because a stale process still holds
// the port).
func (m *Manager) startInstance(ctx context.Context, inst *instance, dir, cfgPath string, doc map[string]any, port *int) (Proc, string, error) {
	spec := StartSpec{
		InstanceID: inst.id,
		ConfigPath: cfgPath,
		LogPath:    filepath.Join(dir, "xray.log"),
		Secrets:    m.secretValues(),
	}
	proc, err := m.startAndWait(ctx, spec, *port)
	if err == nil {
		return proc, m.reportStarted(inst, dir, cfgPath, proc), nil
	}
	m.logger.Printf("agent: instance %s: first start attempt failed: %v", inst.id, err)
	// A leftover process may still hold the port.
	if rerr := m.reapStale(dir, cfgPath); rerr != nil {
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
	if terr := m.runner.Test(ctx, cfgPath); terr != nil {
		return nil, "", err
	}
	proc, err = m.startAndWait(ctx, spec, nextPort)
	if err != nil {
		return nil, "", err
	}
	*port = nextPort
	return proc, m.reportStarted(inst, dir, cfgPath, proc), nil
}

// startAndWait starts the core and waits until its management API answers, so a
// successful start really means the instance is serving.
func (m *Manager) startAndWait(ctx context.Context, spec StartSpec, apiPort int) (Proc, error) {
	proc, err := m.runner.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	if err := m.waitReady(ctx, proc, apiPort); err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = proc.Stop(stopCtx)
		cancel()
		return nil, err
	}
	return proc, nil
}

func (m *Manager) waitReady(ctx context.Context, proc Proc, apiPort int) error {
	if apiPort <= 0 {
		return nil
	}
	server := net.JoinHostPort(APIListen, strconv.Itoa(apiPort))
	deadline := m.now().Add(readinessTimeout)
	for {
		select {
		case <-proc.Exited():
			return fmt.Errorf("core exited during startup: %v", proc.ExitError())
		default:
		}
		qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := m.runner.StatsQuery(qctx, server)
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if m.now().After(deadline) {
			if out := proc.Output(); len(out) > 0 {
				return fmt.Errorf("core management API did not answer within %s (last output: %s)", readinessTimeout, out[len(out)-1])
			}
			return fmt.Errorf("core management API did not answer within %s: %v", readinessTimeout, err)
		}
		time.Sleep(150 * time.Millisecond)
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
	if live, found, err := processIdentity(proc.PID()); err != nil {
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

func (m *Manager) rollbackOrFail(ctx context.Context, inst *instance, t Target, opts ApplyOptions, cause error) Result {
	// The old process may still authorize users that the new snapshot revoked.
	m.sampleAndStop(ctx, inst, opts.BeforeStop)
	if inst.lastGood != nil {
		if res, ok := m.tryRollback(ctx, inst, t); ok {
			res.Error = cause.Error()
			m.logger.Printf("agent: instance %s: applied last known good technical config with current authorization: %v", inst.id, cause)
			return res
		}
	}
	return m.failLocked(inst, t, cause)
}

// tryRollback re-applies the last good technical document while keeping only
// clients that the current authorization still allows.
func (m *Manager) tryRollback(ctx context.Context, inst *instance, t Target) (Result, bool) {
	dir, err := m.instanceDir(inst.id)
	if err != nil {
		return Result{}, false
	}
	filtered, err := FilterDocument(inst.lastGood, t.Authorization)
	if err != nil {
		return Result{}, false
	}
	port, err := m.portFn(m.reservedPorts(filtered.Document))
	if err != nil {
		return Result{}, false
	}
	config, err := BuildEffective(filtered.Document, port)
	if err != nil {
		return Result{}, false
	}
	cfgPath := filepath.Join(dir, "config.rollback.json")
	if err := writeFileAtomic(cfgPath, config, 0o600); err != nil {
		return Result{}, false
	}
	if err := m.runner.Test(ctx, cfgPath); err != nil {
		m.logger.Printf("agent: instance %s: rollback config rejected: %v", inst.id, err)
		return Result{}, false
	}
	proc, gen, err := m.startInstance(ctx, inst, dir, cfgPath, filtered.Document, &port)
	if err != nil {
		m.logger.Printf("agent: instance %s: rollback start failed: %v", inst.id, err)
		return Result{}, false
	}
	hash, _ := HashDocument(filtered.Document)
	inst.proc = proc
	inst.coreGen = gen
	inst.apiPort = port
	inst.state = StateRollback
	inst.err = ""
	inst.doc = filtered.Document
	inst.hash = hash
	inst.emailToMember = copyMap(filtered.EmailToMember)
	inst.usersList = authorizedSubset(t.Users, filtered.EmailToMember)
	inst.users = len(filtered.EmailToMember)
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
		Hash:                     hash,
		Document:                 filtered.Document,
		EmailToMember:            copyMap(filtered.EmailToMember),
		Users:                    authorizedSubset(t.Users, filtered.EmailToMember),
		NextExpiry:               t.NextExpiry,
	}, true
}

func (m *Manager) failLocked(inst *instance, t Target, cause error) Result {
	inst.proc = nil
	inst.hash = ""
	inst.state = StateFailed
	inst.err = cause.Error()
	inst.users = 0
	inst.emailToMember = map[string]string{}
	inst.publish()
	return Result{
		InstanceID:               inst.id,
		NodeID:                   t.NodeID,
		State:                    StateFailed,
		Error:                    cause.Error(),
		OldConnectionsTerminated: true,
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

func (m *Manager) sampleAndStop(ctx context.Context, inst *instance, hook SampleHook) {
	if inst.proc == nil {
		return
	}
	if hook != nil && inst.apiPort > 0 {
		hctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		hook(hctx, inst.nodeID, inst.coreGen, net.JoinHostPort(APIListen, strconv.Itoa(inst.apiPort)), copyMap(inst.emailToMember))
		cancel()
	}
	proc := inst.proc
	inst.proc = nil
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := proc.Stop(sctx)
	cancel()
	m.mu.Lock()
	delete(m.owned, proc.PID())
	m.mu.Unlock()
	if err != nil {
		m.logger.Printf("agent: instance %s: stop returned %v", inst.id, err)
	}
	if out := proc.Output(); len(out) > 0 && err != nil {
		m.logger.Printf("agent: instance %s: last core output: %s", inst.id, out[len(out)-1])
	}
	inst.state = StateStopped
	inst.apiPort = 0
	inst.publish()
	if dir, derr := m.instanceDir(inst.id); derr == nil {
		removePidFile(dir)
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
	m.sampleAndStop(ctx, inst, hook)
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
// identity, the full executable path and the instance config path must all
// match. Anything else returns ErrUnverifiedProcess and the process is left
// running, because a reused pid must never be killed.
func (m *Manager) reapStale(dir, cfgPath string) error {
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
	live, found, perr := processIdentity(rec.PID)
	if perr != nil {
		return fmt.Errorf("%w: cannot inspect pid %d (%v)", ErrUnverifiedProcess, rec.PID, perr)
	}
	if !found {
		// The recorded process is gone: nothing to reclaim.
		removePidFile(dir)
		return nil
	}
	if verr := m.verifyOwnership(rec, live, cfgPath); verr != nil {
		return verr
	}
	if terr := terminateProcess(rec.PID); terr != nil {
		return fmt.Errorf("stop leftover core process %d: %w", rec.PID, terr)
	}
	m.logger.Printf("agent: stopped leftover core process pid=%d in %s", rec.PID, dir)
	removePidFile(dir)
	return nil
}

func (m *Manager) verifyOwnership(rec pidRecord, live ProcessIdentity, cfgPath string) error {
	if rec.Created == "" || live.Created == "" {
		return fmt.Errorf("%w: pid %d has no recorded process creation identity", ErrUnverifiedProcess, rec.PID)
	}
	if rec.Created != live.Created {
		return fmt.Errorf("%w: pid %d creation identity changed (recorded %s, live %s); the pid was reused", ErrUnverifiedProcess, rec.PID, rec.Created, live.Created)
	}
	if rec.Executable == "" || !samePath(rec.Executable, live.Executable) {
		return fmt.Errorf("%w: pid %d executable %q does not match the recorded %q", ErrUnverifiedProcess, rec.PID, live.Executable, rec.Executable)
	}
	if m.xrayPath != "" && !samePath(m.xrayPath, live.Executable) {
		return fmt.Errorf("%w: pid %d executable %q is not the configured core %q", ErrUnverifiedProcess, rec.PID, live.Executable, m.xrayPath)
	}
	if cfgPath == "" || rec.ConfigPath == "" || !samePath(rec.ConfigPath, cfgPath) {
		return fmt.Errorf("%w: pid %d is recorded for config %q, not %q", ErrUnverifiedProcess, rec.PID, rec.ConfigPath, cfgPath)
	}
	if live.CommandLine != "" && !strings.Contains(live.CommandLine, rec.ConfigPath) {
		return fmt.Errorf("%w: pid %d command line does not reference %q", ErrUnverifiedProcess, rec.PID, rec.ConfigPath)
	}
	return nil
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
