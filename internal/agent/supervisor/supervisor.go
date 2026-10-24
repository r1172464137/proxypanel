// Package supervisor runs the agent: registration, snapshot reconciliation,
// sampling, reliable reporting, heartbeats and local expiry enforcement.
package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"proxypanel/internal/agent/config"
	"proxypanel/internal/agent/control"
	"proxypanel/internal/agent/reporting"
	"proxypanel/internal/agent/state"
	"proxypanel/internal/agent/telemetry"
	"proxypanel/internal/agent/xray"
	"proxypanel/internal/contracts"
)

// Report states sent to the panel.
const (
	applyStateApplied = "applied"
	applyStateFailed  = "failed"
	// applyStateCoordination tells the panel that the agent refuses to apply the
	// snapshot because its control generation is unknown: a local reconcile
	// action on the node is required first.
	applyStateCoordination = "coordination_required"
)

// finalSampleGateBudget bounds how long a stop waits for the sampling gate before
// it gives up on the final sample. A revocation must never be delayed by a slow
// periodic sampling round.
const finalSampleGateBudget = 2 * time.Second

const (
	// applyBudgetFloor is the minimum apply budget of one snapshot. The real
	// budget is derived from the xray lifecycle budgets times the number of
	// instances (see Supervisor.snapshotBudget): a snapshot never gets less time
	// than one instance needs, so a single slow core can not consume the budget
	// of the next instance and make its `xray -test` fail with a killed process.
	applyBudgetFloor = 55 * time.Second
)

// Options are the dependencies of a Supervisor. Tests replace the panel client
// and the core runner.
type Options struct {
	Config  *config.Config
	Logger  *log.Logger
	Client  *control.Client
	Runner  xray.Runner
	Now     func() time.Time
	Version string
}

// failure is the per-instance retry backoff after a REAL apply failure.
type failure struct {
	count   int
	next    time.Time
	lastErr string
	// hash is the target hash that actually failed. The backoff only suppresses
	// retries of that same target: a new revision (a revocation, a narrowed
	// authorization, a fixed technical config) is attempted immediately, so a
	// stale backoff can never delay the 60 second revocation requirement.
	hash string
}

const (
	// failureBackoffBase and failureBackoffMax bound the retry backoff after a
	// real apply failure. Unchanged, still failing targets are retried at least
	// every failureBackoffMax so a transient fault cannot park an instance.
	failureBackoffBase = 5 * time.Second
	failureBackoffMax  = 30 * time.Second
)

// Supervisor is the agent runtime.
type Supervisor struct {
	cfg     *config.Config
	log     *log.Logger
	now     func() time.Time
	version string

	store    *state.Store
	lock     io.Closer
	client   *control.Client
	runner   xray.Runner
	mgr      *xray.Manager
	sampler  *reporting.Sampler
	uploader *reporting.Uploader

	startedAt time.Time

	mu             sync.Mutex
	xrayVersion    string
	lastSyncAt     time.Time
	lastSyncErr    string
	lastApplyState string
	queueFull      bool
	persistenceErr string
	instancesDown  bool
	stopInFlight   bool
	failures       map[string]*failure
	metrics        map[string]float64
	// lastSamplingErrors holds the sampling failures of the most recent cycle so
	// they can be surfaced (heartbeat metrics plus the LastSamplingErrors
	// accessor) instead of only being logged once.
	lastSamplingErrors []string
	// coordinationGeneration is the control generation the agent refused to
	// apply, and coordinationReportKey throttles the panel report to once per
	// offered snapshot.
	coordinationGeneration string
	coordinationReportKey  string
	// applyMu serializes snapshot application with local expiry rebuilds: both
	// rewrite the persisted instance records and the applied watermark, so they
	// must never interleave (a stale expiry target set could re-authorize a
	// member that a newer snapshot already revoked).
	applyMu  *ctxMutex
	sampleMu *ctxMutex // orders periodic read+commit against final read+commit
	closing  bool
	recovery map[string]*failure
	// expiryLockBudget bounds how long the local expiry pass waits for a snapshot
	// apply. Past that bound the affected instances are stopped directly (a
	// fail-closed stop that cannot be delayed by unrelated technical validation);
	// the record update is retried by the next pass.
	expiryLockBudget time.Duration
	// expiryAfterReadHook is a test seam that runs inside the serialized section
	// right after the instance records were read. nil in production.
	expiryAfterReadHook func()
	// timeouts are the lifecycle budgets of one instance (readiness, config
	// validation, stop, rollback). The apply budgets are derived from them, so a
	// slow machine is tuned in exactly one place.
	timeouts xray.Timeouts
}

// New builds a supervisor and locks the state directory.
func New(opts Options) (*Supervisor, error) {
	if opts.Config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if err := opts.Config.Validate(); err != nil {
		return nil, err
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.New(os.Stderr, "agent ", log.LstdFlags|log.LUTC)
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	version := opts.Version
	if version == "" {
		version = config.AgentVersion
	}
	store, err := state.Open(opts.Config.StateDir)
	if err != nil {
		return nil, err
	}
	lock, err := state.AcquireLock(opts.Config.StateDir)
	if err != nil {
		store.Close()
		return nil, err
	}
	client := opts.Client
	if client == nil {
		client, err = control.New(opts.Config.PanelURL, version)
		if err != nil {
			lock.Close()
			store.Close()
			return nil, err
		}
	}
	runner := opts.Runner
	if runner == nil {
		runner = xray.NewCLIRunner(opts.Config.XrayPath)
	}
	mgr, err := xray.NewManager(opts.Config.StateDir, opts.Config.XrayPath, runner, logger)
	if err != nil {
		lock.Close()
		store.Close()
		return nil, err
	}
	s := &Supervisor{
		cfg:       opts.Config,
		log:       logger,
		now:       now,
		version:   version,
		store:     store,
		lock:      lock,
		client:    client,
		runner:    runner,
		mgr:       mgr,
		sampler:   reporting.NewSampler(store, mgr, logger),
		failures:  map[string]*failure{},
		recovery:  map[string]*failure{},
		applyMu:   &ctxMutex{},
		sampleMu:  &ctxMutex{},
		metrics:   map[string]float64{},
		startedAt: now(),
		timeouts:  xray.DefaultTimeouts(),
	}
	mgr.SetSecrets(func() []string {
		values := []string{client.Token(), opts.Config.RegistrationCode}
		return values
	})
	// One clock for the whole agent: the recovery budget, the expiry triggers and
	// the sampling order are all derived from it.
	mgr.SetNow(now)
	mgr.SetStartGuard(s.CheckStart)
	s.sampler.SetNow(now)
	s.uploader = reporting.NewUploader(store, client, client, logger)
	mgr.SetSampleHook(s.sampleHook())
	return s, nil
}

// Store exposes the local store (tests).
func (s *Supervisor) Store() *state.Store { return s.store }

// Manager exposes the instance manager (tests).
func (s *Supervisor) Manager() *xray.Manager { return s.mgr }

// SetXrayTimeouts overrides the instance lifecycle budgets (startup wiring and
// tests only). It must run before Run/Apply.
func (s *Supervisor) SetXrayTimeouts(t xray.Timeouts) {
	s.mu.Lock()
	s.timeouts = t
	s.mu.Unlock()
	s.mgr.SetTimeouts(t)
}

// instanceBudget is the upper bound of one instance reconcile.
func (s *Supervisor) instanceBudget() time.Duration {
	s.mu.Lock()
	t := s.timeouts
	s.mu.Unlock()
	return t.InstanceBudget()
}

// snapshotBudget is the upper bound of one snapshot apply that reconciles
// instanceCount instances sequentially. It is never smaller than the floor, so a
// snapshot with a single instance keeps the documented 60 second envelope.
func (s *Supervisor) snapshotBudget(instanceCount int) time.Duration {
	s.mu.Lock()
	t := s.timeouts
	s.mu.Unlock()
	budget := t.SnapshotBudget(instanceCount)
	if budget < applyBudgetFloor {
		budget = applyBudgetFloor
	}
	return budget
}

// Client exposes the panel client (tests).
func (s *Supervisor) Client() *control.Client { return s.client }

// Close stops every managed core process, then releases the state directory
// lock and the local database.
func (s *Supervisor) Close() error {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	s.applyMu.lock()
	var stopErrs []error
	if s.mgr != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		results := s.mgr.StopAll(ctx)
		cancel()
		if len(results) > 0 {
			s.log.Printf("agent: stopped %d instance(s) on shutdown", len(results))
		}
		if serr := shutdownError(results); serr != nil {
			stopErrs = append(stopErrs, serr)
		}
		// The records of a stop that was not confirmed must not claim the core
		// is gone; record them before the database is closed.
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		if err := s.persistStoppedInstancesLocked(ctx, results); err != nil {
			stopErrs = append(stopErrs, err)
		}
		cancel()
	}
	s.applyMu.unlock()
	var errs []error
	errs = append(errs, stopErrs...)
	if s.lock != nil {
		errs = append(errs, s.lock.Close())
	}
	if s.store != nil {
		errs = append(errs, s.store.Close())
	}
	return errors.Join(errs...)
}

// Run starts every loop and blocks until ctx is cancelled.
func (s *Supervisor) Run(ctx context.Context) error {
	s.log.Printf("agent: starting version=%s panel=%s state_dir=%s", s.version, s.client.BaseURL(), s.cfg.StateDir)
	if err := s.checkCoreBinary(ctx); err != nil {
		// An unknown core version is refused instead of being assumed
		// compatible; nothing is started in that case.
		return fmt.Errorf("core compatibility check failed: %w", err)
	}
	if err := s.restore(ctx); err != nil {
		s.log.Printf("agent: restore local state: %v", err)
	}
	// Publish the sanitized state report immediately, so an operator can always
	// read what this Agent has durably committed without stopping it.
	s.writeStatus()
	var wg sync.WaitGroup
	start := func(name string, interval time.Duration, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.loop(ctx, name, interval, fn)
		}()
	}
	start("snapshot", s.cfg.PollInterval(), s.syncOnce)
	start("stats", s.cfg.StatsInterval(), s.sampleOnce)
	start("upload", s.cfg.PollInterval(), s.uploadOnce)
	start("heartbeat", s.cfg.HeartbeatInterval(), s.heartbeatOnce)
	start("expiry", time.Second, s.expiryOnce)
	start("health", time.Second, s.healthOnce)
	<-ctx.Done()
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.applyMu.lock()
	// Never leave an untracked core process behind on a normal shutdown.
	results := s.mgr.StopAll(stopCtx)
	s.applyMu.unlock()
	// A shutdown can interrupt a local expiry rebuild (or a snapshot apply) that
	// was already narrowing the authorization. Persisting the stopped records
	// under the same serialization lock, and applying the same expiry filter,
	// guarantees a cancelled rebuild never leaves a WIDER authorization on disk
	// than the local clock already proved.
	persistErr := s.persistStoppedInstances(stopCtx, results)
	s.writeStatus()
	s.log.Printf("agent: stopped %d instance(s)", len(results))
	wg.Wait()
	return errors.Join(shutdownError(results), persistErr)
}

// shutdownError reports the instances whose stop could not be confirmed during
// shutdown. The runtime must not announce a clean shutdown when a core may still
// be serving: the caller (cmd/agent) exits non zero and an operator sees it.
func shutdownError(results []xray.Result) error {
	var errs []error
	for _, r := range results {
		if r.State == xray.StateFailed {
			errs = append(errs, fmt.Errorf("instance %s did not stop cleanly: %s", r.InstanceID, r.Error))
		}
	}
	return errors.Join(errs...)
}

// persistStoppedInstances records the controlled-shutdown state of every stopped
// instance. Already expired users are dropped with the same rule the snapshot
// and local expiry paths use, so the persisted authorization is never wider
// than what this node enforces, even if the shutdown cancelled the rebuild.
func (s *Supervisor) persistStoppedInstances(ctx context.Context, results []xray.Result) error {
	s.applyMu.lock()
	defer s.applyMu.unlock()
	return s.persistStoppedInstancesLocked(ctx, results)
}

// persistStoppedInstancesLocked is persistStoppedInstances for a caller that
// already holds the serialization gate.
func (s *Supervisor) persistStoppedInstancesLocked(ctx context.Context, results []xray.Result) error {
	now := s.now()
	var errs []error
	for _, r := range results {
		rec, ok, err := s.storedInstance(ctx, r.InstanceID)
		if err != nil {
			errs = append(errs, fmt.Errorf("read record of %s: %w", r.InstanceID, err))
			continue
		}
		if !ok {
			continue
		}
		rec.Users, rec.NextExpiry = filterExpiredUsers(rec.Users, now)
		rec.AppliedUserCount = len(rec.Users)
		rec.UpdatedAt = now
		if r.State == xray.StateFailed {
			// The stop was not confirmed: the core may still hold its listeners.
			// Keep the recovery record (config, hash, core generation) and never
			// claim it is stopped, so the next start can prove ownership and
			// reclaim it instead of leaking a serving process.
			rec.State = xray.StateFailed
			rec.Error = r.Error
		} else {
			rec.State = xray.StateStopped
			rec.CoreGeneration = ""
			rec.ConfigHash = ""
			rec.Error = ""
		}
		if err := s.store.RecordInstance(ctx, rec); err != nil {
			errs = append(errs, fmt.Errorf("record stopped instance %s: %w", r.InstanceID, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Supervisor) loop(ctx context.Context, name string, interval time.Duration, fn func(context.Context) error) {
	backoff := control.NewBackoff(time.Second, 60*time.Second)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		started := s.now()
		err := fn(ctx)
		if ctx.Err() != nil {
			return
		}
		delay := interval
		if err != nil {
			delay = backoff.Next()
			s.log.Printf("agent: %s cycle failed: %v", name, err)
		} else {
			backoff.Reset()
		}
		elapsed := s.now().Sub(started)
		wait := delay - elapsed
		if wait < 100*time.Millisecond {
			wait = 100 * time.Millisecond
		}
		timer.Reset(wait)
	}
}

func (s *Supervisor) checkCoreBinary(ctx context.Context) error {
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := s.runner.Version(vctx)
	if err != nil {
		return fmt.Errorf("cannot query the core version: %w", err)
	}
	if _, err := xray.CheckCoreVersion(raw); err != nil {
		return err
	}
	s.mu.Lock()
	s.xrayVersion = raw
	s.mu.Unlock()
	s.log.Printf("agent: core version %s (verified compatible)", raw)
	return nil
}

// restore starts the instances that were running before the agent restarted,
// with already expired users filtered out, before the first network round trip.
func (s *Supervisor) restore(ctx context.Context) error {
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
	// Startup restore rewrites the same records the snapshot/expiry paths own.
	s.applyMu.lock()
	defer s.applyMu.unlock()
	records, err := s.store.Instances(ctx)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}
	if err := s.mgr.ReapUntracked(ctx); err != nil {
		return err
	}
	now := s.now()
	targets := make([]xray.Target, 0, len(records))
	for _, rec := range records {
		if rec.State == xray.StateStopping {
			continue
		}
		target, err := s.targetFromRecord(rec, now)
		if err != nil {
			s.log.Printf("agent: cannot restore instance %s: %v", rec.NodeID, err)
			_ = s.store.RecordInstance(ctx, state.InstanceRecord{
				NodeID:    rec.NodeID,
				State:     xray.StateFailed,
				Error:     err.Error(),
				Config:    rec.Config,
				Users:     rec.Users,
				UpdatedAt: now,
			})
			continue
		}
		s.mgr.SeedLastGood(rec.NodeID, target.Document)
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		return nil
	}
	applyCtx, cancel := context.WithTimeout(ctx, s.snapshotBudget(len(targets)))
	defer cancel()
	results := s.mgr.Apply(applyCtx, targets, xray.ApplyOptions{
		Force: true, PerInstanceTimeout: s.instanceBudget(), BeforeStop: s.sampleHook(),
	})
	byID := map[string]xray.Target{}
	for _, t := range targets {
		byID[t.InstanceID] = t
	}
	for _, res := range results {
		target := byID[res.InstanceID]
		if res.State == xray.StateApplied || res.State == xray.StateUnchanged {
			if err := s.persistResult(ctx, res, target, now); err != nil {
				return err
			}
			s.log.Printf("agent: restored instance %s with %d authorized user(s)", res.InstanceID, res.AppliedUserCount)
			continue
		}
		s.log.Printf("agent: restore of instance %s failed: %s", res.InstanceID, res.Error)
		var rawConfig []byte
		if target.Document != nil {
			if raw, err := json.Marshal(target.Document); err == nil {
				rawConfig = raw
			}
		}
		_ = s.store.RecordInstance(ctx, state.InstanceRecord{
			NodeID:    res.InstanceID,
			State:     res.State,
			Error:     res.Error,
			Config:    rawConfig,
			Users:     target.Users,
			UpdatedAt: now,
		})
	}
	return nil
}

func (s *Supervisor) targetFromRecord(rec state.InstanceRecord, now time.Time) (xray.Target, error) {
	if len(rec.Config) == 0 {
		return xray.Target{}, fmt.Errorf("stored instance has no configuration")
	}
	plan, err := xray.Prepare(contracts.Instance{
		ID: rec.NodeID, NodeID: rec.NodeID, Config: json.RawMessage(rec.Config), Users: rec.Users,
	}, xray.Options{CertRoot: s.mgr.CertRoot(), Now: now, MaxInstances: xray.DefaultMaxInstances})
	if err != nil {
		return xray.Target{}, err
	}
	return xray.Target{
		InstanceID:    plan.InstanceID,
		NodeID:        plan.NodeID,
		Document:      plan.Document,
		Hash:          plan.Hash,
		Authorization: plan.Authorization,
		EmailToMember: plan.EmailToMember,
		Users:         plan.EffectiveUsers,
		NextExpiry:    plan.NextExpiry,
	}, nil
}

func (s *Supervisor) ensureIdentity(ctx context.Context) error {
	id, err := s.store.Identity(ctx)
	if err != nil {
		return err
	}
	if id.Token == "" && s.cfg.Token != "" {
		id.AgentID = s.cfg.AgentID
		id.Token = s.cfg.Token
		if err := s.store.SaveIdentity(ctx, id); err != nil {
			return err
		}
	}
	if id.Token != "" {
		s.client.SetToken(id.Token)
		s.client.SetAgentID(id.AgentID)
		return nil
	}
	if s.cfg.RegistrationCode == "" {
		return fmt.Errorf("agent has no token in %s and no registration_code is configured", s.cfg.StateDir)
	}
	host, _ := os.Hostname()
	resp, err := s.client.Register(ctx, contracts.RegistrationRequest{
		Code: s.cfg.RegistrationCode, Name: host, Version: s.version,
	})
	if err != nil {
		return fmt.Errorf("register agent: %w", err)
	}
	if resp.AgentID == "" || resp.Token == "" {
		return fmt.Errorf("registration response is incomplete")
	}
	id.AgentID = resp.AgentID
	id.Token = resp.Token
	if id.ControlGeneration == "" {
		id.ControlGeneration = resp.ControlGeneration
	}
	if err := s.store.SaveIdentity(ctx, id); err != nil {
		return err
	}
	s.client.SetToken(resp.Token)
	s.client.SetAgentID(resp.AgentID)
	s.log.Printf("agent: registered as %s; remove registration_code from the config file", resp.AgentID)
	return nil
}

func (s *Supervisor) syncOnce(ctx context.Context) error {
	intent, ierr := s.store.ApplyIntent(ctx)
	if ierr != nil {
		return ierr
	}
	if intent != nil {
		if err := s.applySnapshot(ctx, intent); err != nil {
			s.log.Printf("agent: pending intent remains unresolved: %v", err)
		}
	}
	if err := s.ensureIdentity(ctx); err != nil {
		s.noteSyncError(err)
		return err
	}
	id, err := s.store.Identity(ctx)
	if err != nil {
		return err
	}
	snapshot, err := s.client.Snapshot(ctx, id.AppliedRevision, id.ControlGeneration)
	if err != nil {
		s.noteSyncError(err)
		return err
	}
	s.noteSyncSuccess()
	if snapshot == nil {
		return nil
	}
	return s.applySnapshot(ctx, snapshot)
}

// applySnapshot applies one snapshot. The whole decide-and-commit sequence runs
// under applyMu: a local expiry rebuild (expiryOnce) rewrites the same instance
// records and the same applied watermark, so a stale expiry target set could
// otherwise re-authorize a member that this snapshot just revoked - and because
// the revision would already be committed, the panel would answer 204 forever.
func (s *Supervisor) applySnapshot(ctx context.Context, snapshot *contracts.Snapshot) error {
	needUpload, err := s.applySnapshotSerialized(ctx, snapshot)
	if needUpload {
		// Delivery is a network call: do it after the lock is released so an
		// unreachable panel cannot block a local expiry rebuild.
		if _, uerr := s.uploader.UploadReports(ctx); uerr != nil {
			s.log.Printf("agent: apply report not delivered yet: %v", uerr)
		}
	}
	return err
}

// applySnapshotSerialized performs the serialized decide-and-commit sequence.
// needUpload reports that a report was persisted and should be delivered.
func (s *Supervisor) applySnapshotSerialized(ctx context.Context, snapshot *contracts.Snapshot) (bool, error) {
	s.applyMu.lock()
	defer s.applyMu.unlock()
	// Re-read the watermark under the serialization lock: the value used to build
	// the request is only a hint, because an earlier snapshot or a local expiry
	// rebuild may have rewritten the records since.
	id, err := s.store.Identity(ctx)
	if err != nil {
		return false, err
	}
	if err := xray.ValidateSnapshot(*snapshot, xray.DefaultMaxInstances); err != nil {
		s.mu.Lock()
		s.lastApplyState = xray.StateFailed
		s.mu.Unlock()
		return false, fmt.Errorf("snapshot rejected: %w", err)
	}
	generationChanged := id.ControlGeneration != "" && snapshot.ControlGeneration != id.ControlGeneration
	force := false
	if generationChanged {
		// A new (or restored) control generation is never trusted blindly: the
		// old epoch could be a backup that still authorizes revoked members.
		accepted, aerr := s.store.AcceptedGeneration(ctx)
		if aerr != nil {
			return false, aerr
		}
		if accepted != snapshot.ControlGeneration {
			return s.enterCoordination(ctx, snapshot, id, accepted)
		}
		s.log.Printf("agent: applying the explicitly accepted control generation %q (previous %q)", snapshot.ControlGeneration, id.ControlGeneration)
		force = true
	}
	if !force && snapshot.Revision <= id.AppliedRevision {
		s.log.Printf("agent: ignoring snapshot revision %d (already applied %d)", snapshot.Revision, id.AppliedRevision)
		return false, nil
	}
	if err := s.protectionError(); err != nil {
		return false, err
	}
	// IssuedAt is transport metadata and changes when the panel reoffers the
	// same revision. Preserve the fenced issue time; state still rejects any
	// changed authorization or technical payload at that revision.
	pending, err := s.store.ApplyIntent(ctx)
	if err != nil {
		return false, err
	}
	if pending != nil && pending.ControlGeneration == snapshot.ControlGeneration && pending.Revision == snapshot.Revision {
		copy := *snapshot
		copy.IssuedAt = pending.IssuedAt
		snapshot = &copy
	}
	if err := s.store.SaveApplyIntent(ctx, *snapshot); err != nil {
		s.setPersistenceError(err)
		return false, err
	}
	if err := s.mgr.ReapUntracked(ctx); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	stored, err := s.store.Instances(ctx)
	if err != nil {
		s.setPersistenceError(err)
		return false, err
	}
	previous := map[string]state.InstanceRecord{}
	for _, rec := range stored {
		previous[rec.NodeID] = rec
	}
	applyCtx, cancel := context.WithTimeout(ctx, s.snapshotBudget(len(snapshot.Instances)))
	defer cancel()

	now := s.now()
	var targets []xray.Target
	var rejected []xray.Result
	for _, inst := range snapshot.Instances {
		plan, perr := xray.Prepare(inst, xray.Options{CertRoot: s.mgr.CertRoot(), Now: now, MaxInstances: xray.DefaultMaxInstances})
		if perr != nil {
			s.log.Printf("agent: instance %s rejected: %v", inst.ID, perr)
			res, ok := s.mgr.Stop(ctx, inst.ID, "configuration rejected: "+perr.Error())
			if !ok {
				res = xray.Result{InstanceID: inst.ID, NodeID: inst.NodeID, State: xray.StateFailed, Error: perr.Error()}
			} else {
				res.Error = "configuration rejected: " + perr.Error()
				res.State = xray.StateFailed // stopping safely is not applying successfully
			}
			rejected = append(rejected, res)
			// The result loop below records this attempt exactly once. No target
			// hash exists for a rejected configuration, so it can never suppress a
			// future retry of a corrected one.
			continue
		}
		targets = append(targets, xray.Target{
			InstanceID:    plan.InstanceID,
			NodeID:        plan.NodeID,
			Document:      plan.Document,
			Hash:          plan.Hash,
			Authorization: plan.Authorization,
			EmailToMember: plan.EmailToMember,
			Users:         plan.EffectiveUsers,
			NextExpiry:    plan.NextExpiry,
		})
	}
	skip := s.skipSetFor(targets)
	results := s.mgr.Apply(applyCtx, targets, xray.ApplyOptions{
		Force:              force,
		Skip:               skip,
		PerInstanceTimeout: s.instanceBudget(),
		BeforeStop:         s.sampleHook(),
	})
	results = append(results, rejected...)

	byID := map[string]xray.Target{}
	for _, t := range targets {
		byID[t.InstanceID] = t
	}
	committed := map[string]state.InstanceRecord{}
	wanted := map[string]bool{}
	for _, inst := range snapshot.Instances {
		wanted[inst.ID] = true
	}
	ok := true
	for _, res := range results {
		switch res.State {
		case xray.StateApplied, xray.StateUnchanged:
			rec, rerr := s.resultRecord(res, byID[res.InstanceID], now)
			if rerr != nil {
				return false, rerr
			}
			committed[rec.NodeID] = rec
			s.clearFailure(res.InstanceID)
		case xray.StateRollback:
			// The authorization is current but the new technical config could not
			// be used: report it as a failure and keep retrying with backoff.
			rec, rerr := s.resultRecord(res, byID[res.InstanceID], now)
			if rerr != nil {
				return false, rerr
			}
			committed[rec.NodeID] = rec
			s.recordFailure(res.InstanceID, byID[res.InstanceID].Hash, errors.New(res.Error))
			ok = false
		case xray.StateStopped:
			s.clearFailure(res.InstanceID)
		case xray.StateDeferred:
			if rec, exists := previous[res.InstanceID]; exists {
				committed[rec.NodeID] = rec
			} else {
				rec, rerr := s.resultRecord(res, byID[res.InstanceID], now)
				if rerr != nil {
					return false, rerr
				}
				committed[rec.NodeID] = rec
			}
			// This poll did not attempt anything: the instance is inside the retry
			// backoff of the target that already failed. It is NOT a new failure, so
			// the deadline and the failure count are left untouched (otherwise each
			// poll would re-arm the backoff and the instance could never recover),
			// and the committed instance record keeps its last applied state. The
			// watermark is not advanced either: this revision was not applied.
			ok = false
		default:
			rec := state.InstanceRecord{NodeID: res.InstanceID, State: res.State, Error: res.Error, UpdatedAt: now}
			if target, exists := byID[res.InstanceID]; exists {
				var rerr error
				rec, rerr = s.resultRecord(res, target, now)
				if rerr != nil {
					return false, rerr
				}
			}
			if !res.OldConnectionsTerminated {
				if prev, exists := previous[res.InstanceID]; exists {
					rec = prev
					rec.State, rec.Error, rec.UpdatedAt = xray.StateFailed, res.Error, now
				}
			}
			if !wanted[res.InstanceID] {
				// The instance was removed from the snapshot but its core could not
				// be proven gone. Never claim it is stopped: keep the last known
				// record (config, users, applied count) and turn it into a
				// tombstone that health keeps retrying to clean up. Dropping the
				// record - or writing an empty one - would lose the only recovery
				// pointer to a core that may still serve the old connections.
				if prev, exists := previous[res.InstanceID]; exists {
					rec = prev
					rec.UpdatedAt = now
				}
				rec.State = xray.StateStopping
				rec.Error = res.Error
			}
			committed[rec.NodeID] = rec
			s.recordFailure(res.InstanceID, byID[res.InstanceID].Hash, errors.New(res.Error))
			ok = false
		}
	}

	report := contracts.ApplyReport{
		ControlGeneration: snapshot.ControlGeneration,
		Revision:          snapshot.Revision,
		State:             applyStateApplied,
		AppliedAt:         s.now(),
		Results:           contractResults(results),
	}
	if !ok {
		report.State = applyStateFailed
	}
	s.log.Printf("agent: snapshot revision %d generation %s: %s (%d instance result(s))", snapshot.Revision, snapshot.ControlGeneration, report.State, len(results))
	records := make([]state.InstanceRecord, 0, len(committed))
	for _, rec := range committed {
		records = append(records, rec)
	}
	// The reported apply state is published only after the ONE transaction that
	// owns the instance set, the watermark and the outbox committed. A failed
	// commit therefore never leaves "applied" in the local status while the
	// durable records still describe the previous revision.
	if err := s.store.CommitSnapshot(ctx, records, report, ok); err != nil {
		s.mu.Lock()
		s.lastApplyState = applyStateFailed
		s.mu.Unlock()
		s.setPersistenceError(err)
		return false, err
	}
	s.mu.Lock()
	s.lastApplyState = report.State
	s.mu.Unlock()
	if ok {
		s.mu.Lock()
		s.coordinationGeneration = ""
		s.mu.Unlock()
	}
	// Refresh the sanitized report after the instance records and reachability
	// changes of this apply (including removed records and failed records).
	s.writeStatus()
	return true, nil
}

// enterCoordination refuses to apply a snapshot whose control generation the
// agent has never accepted. The running instances keep the last known (never
// wider) authorization: locally applied revocations and expiry filtering stay in
// force, so a restored backup cannot re-authorize a revoked member. The panel is
// told exactly once per offered snapshot and an operator has to run the local
// reconcile command with this generation to proceed.
func (s *Supervisor) enterCoordination(ctx context.Context, snapshot *contracts.Snapshot, id state.Identity, accepted string) (bool, error) {
	message := fmt.Sprintf(
		"control generation %q is not accepted on this node (locally applied %q); the agent keeps its last known authorization and refuses to apply this snapshot until an operator runs: agent -config <file> -accept-generation %s",
		snapshot.ControlGeneration, id.ControlGeneration, snapshot.ControlGeneration)
	s.mu.Lock()
	s.lastApplyState = applyStateCoordination
	s.coordinationGeneration = snapshot.ControlGeneration
	reportKey := snapshot.ControlGeneration + "/" + strconv.FormatInt(snapshot.Revision, 10)
	alreadyReported := s.coordinationReportKey == reportKey
	s.coordinationReportKey = reportKey
	s.mu.Unlock()

	if accepted != "" {
		message = fmt.Sprintf("%s (a different generation %q was accepted locally)", message, accepted)
	}
	if err := s.store.SetPendingGeneration(ctx, snapshot.ControlGeneration); err != nil {
		return false, err
	}
	s.log.Printf("agent: ALERT: %s", message)
	unresolved := fmt.Errorf("control generation %q requires local reconciliation", snapshot.ControlGeneration)
	if alreadyReported {
		return false, unresolved
	}
	results := s.mgr.Snapshot()
	for i := range results {
		results[i].Error = message
		if results[i].State == "" {
			results[i].State = xray.StateUnchanged
		}
	}
	report := contracts.ApplyReport{
		ControlGeneration: snapshot.ControlGeneration,
		Revision:          snapshot.Revision,
		State:             applyStateCoordination,
		AppliedAt:         s.now(),
		Results:           results,
	}
	if err := s.enqueueReport(ctx, report); err != nil {
		return false, err
	}
	// Delivery happens outside applyMu (see applySnapshot).
	return true, unresolved
}

func contractResults(results []xray.Result) []contracts.InstanceResult {
	out := make([]contracts.InstanceResult, 0, len(results))
	for _, r := range results {
		out = append(out, r.ContractResult())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InstanceID < out[j].InstanceID })
	return out
}

func (s *Supervisor) persistResult(ctx context.Context, res xray.Result, target xray.Target, now time.Time) error {
	rec, err := s.resultRecord(res, target, now)
	if err == nil {
		err = s.store.RecordInstance(ctx, rec)
	}
	if err != nil {
		s.setPersistenceError(err)
		return err
	}
	s.writeStatus()
	return nil
}

func (s *Supervisor) resultRecord(res xray.Result, target xray.Target, now time.Time) (state.InstanceRecord, error) {
	users := res.Users
	if users == nil {
		users = target.Users
	}
	doc := res.Document
	if doc == nil {
		doc = target.Document
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return state.InstanceRecord{}, err
	}
	rec := state.InstanceRecord{
		NodeID:           res.InstanceID,
		ConfigHash:       res.Hash,
		CoreGeneration:   res.CoreGeneration,
		State:            res.State,
		Error:            res.Error,
		AppliedUserCount: res.AppliedUserCount,
		Config:           raw,
		Users:            users,
		NextExpiry:       res.NextExpiry,
		UpdatedAt:        now,
	}
	if rec.ConfigHash == "" {
		if hash, err := xray.HashDocument(doc); err == nil {
			rec.ConfigHash = hash
		}
	}
	return rec, nil
}

func (s *Supervisor) enqueueReport(ctx context.Context, report contracts.ApplyReport) error {
	raw, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode apply report: %w", err)
	}
	id := fmt.Sprintf("%s:r%d:%s", report.ControlGeneration, report.Revision, report.State)
	if err := s.store.SaveReport(ctx, id, state.ReportKindApplied, raw); err != nil {
		return fmt.Errorf("persist apply report: %w", err)
	}
	return nil
}

// sampleHook is the final accounting of every stop path.
//
// It runs while the manager holds the instance lifecycle lock, i.e. exactly
// between "this core is about to die" and "start the replacement", so it shares
// the sampling gate with the periodic cycle. The wait for that gate is bounded by
// the hook context: a revocation must never wait for a slow periodic sampling
// round. Losing the race is safe because Measure keeps a per generation
// high-water mark, and the periodic round either already collected this core or
// will collect the (already replaced) one on its next run.
func (s *Supervisor) sampleHook() xray.SampleHook {
	return func(ctx context.Context, nodeID, coreGeneration, apiServer string, emailToMember map[string]string) {
		gateCtx, gateCancel := context.WithTimeout(ctx, finalSampleGateBudget)
		err := s.sampleMu.lockContext(gateCtx)
		gateCancel()
		if err != nil {
			s.noteFinalSamplingError(nodeID, "sampling gate unavailable")
			s.log.Printf("agent: instance %s: final sample skipped, the sampling gate is busy: %v", nodeID, err)
			return
		}
		defer s.sampleMu.unlock()
		qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		counters, queryErr := s.runner.StatsQuery(qctx, apiServer)
		cancel()
		err = queryErr
		if err != nil {
			s.noteFinalSamplingError(nodeID, "final stats query failed")
			s.log.Printf("agent: instance %s: final stats query failed: %v", nodeID, err)
			return
		}
		items, _ := reporting.CounterItems(counters, emailToMember)
		if len(items) == 0 {
			return
		}
		mctx, mcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer mcancel()
		if _, err := s.store.Measure(mctx, nodeID, coreGeneration, s.now(), items); err != nil {
			s.noteFinalSamplingError(nodeID, "final sample persistence failed")
			s.log.Printf("agent: CRITICAL: cannot persist the final sample of instance %s: %v", nodeID, err)
			s.setPersistenceError(err)
		}
	}
}

func (s *Supervisor) sampleOnce(ctx context.Context) error {
	if s.queueOverCapacity() {
		return fmt.Errorf("local upload queue exceeds its capacity; sampling is paused until it drains")
	}
	s.sampleMu.lock()
	res, err := s.sampler.RunOnce(ctx)
	s.sampleMu.unlock()
	if err != nil {
		s.setPersistenceError(err)
		s.log.Printf("agent: sampling failed: %v", err)
		return err
	}
	for _, e := range res.InstanceErrs {
		s.log.Printf("agent: sampling: %s", e)
	}
	if len(res.BatchIDs) > 0 {
		s.log.Printf("agent: queued %d new sample row(s) in %d batch(es)", res.SampleRows, len(res.BatchIDs))
	}
	// Publish the sampling outcome so a degraded sampling loop is visible instead
	// of only appearing in a log line that scrolls away. A cycle that needed
	// retries, or that gave up on an instance, is an operator-visible condition:
	// without it "sampling quietly returns nothing" - the exact shape of the
	// original statsquery failure - looks identical to "no traffic".
	s.mu.Lock()
	s.metrics["sampling_errors"] = float64(len(res.SamplingErrors))
	s.metrics["sampling_retries"] = float64(res.Retries)
	s.metrics["sampling_instances"] = float64(res.Instances)
	s.metrics["sampling_counters"] = float64(res.Counters)
	s.lastSamplingErrors = append([]string(nil), res.SamplingErrors...)
	s.mu.Unlock()
	depth, derr := s.store.QueueDepth(ctx)
	if derr != nil {
		s.setPersistenceError(derr)
		return derr
	}
	s.mu.Lock()
	s.metrics["queue_batches"] = float64(depth.Batches)
	s.metrics["queue_samples"] = float64(depth.Samples)
	s.mu.Unlock()
	if depth.OverCapacity() {
		s.log.Printf("agent: ALERT: local upload queue holds %d batches / %d samples (limits %d / %d); stopping instances to protect the device",
			depth.Batches, depth.Samples, state.MaxQueuedBatches, state.MaxQueuedSamples)
		s.setQueueOverCapacity(true)
	}
	return nil
}

// LastSamplingErrors returns the persistent sampling failures reported by the most
// recent cycle (empty when every instance was sampled). It is the read accessor
// for the heartbeat metrics published in sampleOnce.
func (s *Supervisor) LastSamplingErrors() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lastSamplingErrors...)
}

func (s *Supervisor) uploadOnce(ctx context.Context) error {
	res, err := s.uploader.RunOnce(ctx)
	if res.Acked > 0 || res.Failed > 0 {
		s.log.Printf("agent: usage upload: %d acknowledged, %d retained (queue %d)", res.Acked, res.Failed, len(res.Retained))
	}
	sentReports, rerr := s.uploader.UploadReports(ctx)
	if sentReports > 0 {
		s.log.Printf("agent: delivered %d pending report(s)", sentReports)
	}
	if err == nil && rerr != nil {
		err = rerr
	}
	depth, derr := s.store.QueueDepth(ctx)
	if derr == nil && !depth.OverCapacity() && s.queueOverCapacity() {
		s.setQueueOverCapacity(false)
	}
	if derr == nil {
		s.mu.Lock()
		s.metrics["queue_batches"] = float64(depth.Batches)
		s.metrics["queue_samples"] = float64(depth.Samples)
		s.mu.Unlock()
	}
	return err
}

func (s *Supervisor) heartbeatOnce(ctx context.Context) error {
	if !s.client.HasToken() {
		return nil
	}
	id, err := s.store.Identity(ctx)
	if err != nil {
		return err
	}
	depth, _ := s.store.QueueDepth(ctx)
	reports, _ := s.store.PendingReports(ctx, 1000)
	s.mu.Lock()
	metrics := map[string]float64{}
	for k, v := range s.metrics {
		metrics[k] = v
	}
	s.mu.Unlock()
	for k, v := range telemetry.Collect(s.cfg.StateDir) {
		metrics[k] = v
	}
	instances := s.mgr.Snapshot()
	metrics["health_degraded"] = 0
	metrics["stop_unconfirmed"] = float64(s.mgr.UnconfirmedStops())
	if s.protectionError() != nil || metrics["final_sampling_errors"] > 0 || metrics["sampling_errors"] > 0 {
		metrics["health_degraded"] = 1
	}
	for _, inst := range instances {
		if inst.State == xray.StateFailed || inst.State == xray.StateStopping {
			metrics["health_degraded"] = 1
		}
	}
	running, failed := 0, 0
	for _, inst := range instances {
		switch inst.State {
		case xray.StateApplied, xray.StateRollback:
			running++
		case xray.StateFailed:
			failed++
		}
	}
	metrics["instances_running"] = float64(running)
	metrics["instances_failed"] = float64(failed)
	metrics["instances_total"] = float64(len(instances))
	metrics["queue_batches"] = float64(depth.Batches)
	metrics["queue_samples"] = float64(depth.Samples)
	metrics["pending_reports"] = float64(len(reports))
	metrics["apply_failures"] = float64(s.failureCount())
	if s.queueOverCapacity() {
		metrics["queue_over_capacity"] = 1
	}
	s.mu.Lock()
	coordinationPending := s.coordinationGeneration != ""
	s.mu.Unlock()
	if coordinationPending {
		metrics["coordination_required"] = 1
	}
	s.mu.Lock()
	lastSync := s.lastSyncAt
	syncErr := s.lastSyncErr
	s.mu.Unlock()
	if !lastSync.IsZero() {
		metrics["last_sync_age_seconds"] = float64(int64(s.now().Sub(lastSync).Seconds()))
	}
	if syncErr != "" {
		metrics["last_sync_failed"] = 1
	}
	s.mu.Lock()
	version := s.xrayVersion
	s.mu.Unlock()
	if version == "" {
		if err := s.checkCoreBinary(ctx); err == nil {
			s.mu.Lock()
			version = s.xrayVersion
			s.mu.Unlock()
		}
	}
	hb := contracts.Heartbeat{
		Version:       s.version,
		XrayVersion:   version,
		Revision:      id.AppliedRevision,
		SentAt:        s.now(),
		UptimeSeconds: int64(s.now().Sub(s.startedAt).Seconds()),
		QueueDepth:    depth.Batches,
		Metrics:       metrics,
		Instances:     instances,
	}
	return s.client.Heartbeat(ctx, hb)
}

// expiryOnce enforces already delivered expiry times locally, even when the
// panel is unreachable.
//
// The record read, the rebuild and the persist step run under applyMu so they
// cannot interleave with a snapshot apply: the target set must reflect exactly the
// records the newest committed snapshot wrote. Without that, an expiry pass that
// read its records before a snapshot committed a revocation could re-apply the old
// authorization afterwards and - because the revision was already advanced - the
// revoked member would stay authorized forever (the panel answers 204).
func (s *Supervisor) expiryOnce(ctx context.Context) error {
	expiredNow := s.mgr.ExpiredInstances(s.now())
	if len(expiredNow) == 0 {
		return nil
	}
	// A delivered expiry must not be held back by an unrelated technical apply:
	// bounded wait, then a direct fail-closed stop of the expired instances.
	lockCtx, cancel := context.WithTimeout(ctx, s.expiryLockBudgetValue())
	err := s.applyMu.lockContext(lockCtx)
	cancel()
	if err != nil {
		return s.stopExpiredWithoutLock(ctx, expiredNow, err)
	}
	defer s.applyMu.unlock()

	// A failed/interrupted snapshot owns authorization until reconciled. Old
	// committed records must never be used to rebuild a wider authorization in
	// the meantime; expiry may only stop, not restore from those records.
	intent, err := s.store.ApplyIntent(ctx)
	if err != nil {
		return err
	}
	if intent != nil {
		return s.stopExpiredWithoutLock(ctx, expiredNow, fmt.Errorf("pending apply intent requires reconciliation"))
	}
	records, err := s.store.Instances(ctx)
	if err != nil {
		s.setPersistenceError(err)
		s.mgr.StopAll(ctx)
		return err
	}
	if hook := s.expiryAfterReadHook; hook != nil {
		hook()
	}
	if len(records) == 0 {
		s.mgr.StopAll(ctx)
		return fmt.Errorf("no committed records for running expired instances")
	}
	now := s.now()
	expired := s.mgr.ExpiredInstances(now)
	if len(expired) == 0 {
		// A snapshot applied while we waited for the lock already moved the
		// expiries (or rebuilt the instances).
		return nil
	}
	// Build the target set from every committed record, not just the expired
	// instances: Apply reconciles the whole instance set, so a partial list would
	// stop the other nodes. Instances without an expired user keep an identical
	// hash and are therefore left running untouched.
	targets := make([]xray.Target, 0, len(records))
	for _, rec := range records {
		target, terr := s.targetFromRecord(rec, now)
		if terr != nil {
			// A bad record cannot hold other nodes' expiry hostage. Stop this
			// instance too; omitting it from the full target set remains fail-closed.
			s.mgr.Stop(ctx, rec.NodeID, "local expiry cannot plan committed record")
			continue
		}
		targets = append(targets, target)
	}
	s.log.Printf("agent: local expiry reached for %v; rebuilding with the committed authorization of all %d instance(s)", expired, len(targets))
	applyCtx, cancel := context.WithTimeout(ctx, s.snapshotBudget(len(targets)))
	defer cancel()
	// Force stays false: expiry only drops the expired clients, so the affected
	// instances get a new hash while every other instance keeps its hash and is
	// neither restarted nor stopped.
	results := s.mgr.Apply(applyCtx, targets, xray.ApplyOptions{
		Skip:               s.skipSetFor(targets),
		PerInstanceTimeout: s.instanceBudget(),
		BeforeStop:         s.sampleHook(),
	})
	byID := map[string]xray.Target{}
	for _, t := range targets {
		byID[t.InstanceID] = t
	}
	for _, res := range results {
		switch res.State {
		case xray.StateUnchanged:
			// Persist the refreshed expiry metadata so the local trigger advances.
			if err := s.persistResult(ctx, res, byID[res.InstanceID], now); err != nil {
				return err
			}
			s.clearFailure(res.InstanceID)
		case xray.StateApplied, xray.StateRollback:
			if err := s.persistResult(ctx, res, byID[res.InstanceID], now); err != nil {
				return err
			}
			s.log.Printf("agent: instance %s rebuilt locally with %d authorized user(s)", res.InstanceID, res.AppliedUserCount)
		case xray.StateDeferred:
			s.log.Printf("agent: local expiry rebuild of %s deferred by retry backoff: %s", res.InstanceID, res.Error)
		case xray.StateStopped:
			// The manager tracked an instance without a committed record; the
			// snapshot path owns that lifecycle.
			s.log.Printf("agent: local expiry: instance %s was stopped because it has no committed record", res.InstanceID)
			_ = s.store.RemoveInstance(ctx, res.InstanceID)
		default:
			// The rebuild failed (rejected configuration, failed start, or a
			// cancelled apply during shutdown). The authorization this node can
			// already prove is the expiry-filtered one, so persist that: an
			// interrupted or failed rebuild must never leave a WIDER
			// authorization on disk than the local clock enforces. persistResult
			// takes the target's filtered users and clears next_expiry when none
			// remain. The write must not inherit the cancelled apply context.
			persistCtx, persistCancel := context.WithTimeout(context.Background(), 5*time.Second)
			perr := s.persistResult(persistCtx, res, byID[res.InstanceID], now)
			persistCancel()
			if perr != nil {
				return perr
			}
			s.log.Printf("agent: ALERT: local expiry rebuild of %s failed: %s", res.InstanceID, res.Error)
			s.recordFailure(res.InstanceID, byID[res.InstanceID].Hash, errors.New(res.Error))
		}
	}
	s.writeStatus()
	return nil
}

func (s *Supervisor) setPersistenceError(err error) {
	s.mu.Lock()
	changed := s.persistenceErr == ""
	s.persistenceErr = err.Error()
	s.mu.Unlock()
	if !changed {
		return
	}
	// Protects against unbounded local growth and silent data loss: stop the
	// affected instances instead of dropping samples. This must not run on the
	// caller stack: the final-sample hook is invoked while the manager holds the
	// instance lifecycle lock, so a synchronous StopAll would dead-lock.
	s.stopAllAsync(fmt.Sprintf("local persistence failed (%v)", err))
}

func (s *Supervisor) setQueueOverCapacity(over bool) {
	s.mu.Lock()
	was := s.queueFull
	s.queueFull = over
	s.mu.Unlock()
	if over && !was {
		s.stopAllAsync("the local upload queue is over capacity")
	}
}

// stopAllAsync stops every managed instance outside the caller's stack. The
// sample hook runs while the manager holds an instance lifecycle lock, so the
// stop has to happen on another goroutine; only one stop is in flight at a time.
func (s *Supervisor) stopAllAsync(reason string) {
	s.mu.Lock()
	if s.stopInFlight {
		s.mu.Unlock()
		return
	}
	s.stopInFlight = true
	s.mu.Unlock()
	go func() {
		s.applyMu.lock()
		defer s.applyMu.unlock()
		defer func() { s.mu.Lock(); s.stopInFlight = false; s.mu.Unlock() }()
		if s.protectionError() == nil {
			// The condition cleared while this goroutine waited: never kill the
			// cores that a newer apply just started.
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		results := s.mgr.StopAll(ctx)
		cancel()
		if len(results) > 0 {
			s.log.Printf("agent: ALERT: %s; stopped %d instance(s)", reason, len(results))
		}
		// The records must reflect the stop; the stop context is already spent.
		persistCtx, persistCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := s.persistStoppedInstancesLocked(persistCtx, results); err != nil {
			s.log.Printf("agent: cannot record the protection stop: %v", err)
		}
		persistCancel()
	}()
}

func (s *Supervisor) queueOverCapacity() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queueFull
}

func (s *Supervisor) noteSyncError(err error) {
	s.mu.Lock()
	s.lastSyncErr = err.Error()
	s.mu.Unlock()
}

func (s *Supervisor) noteSyncSuccess() {
	s.mu.Lock()
	s.lastSyncAt = s.now()
	s.lastSyncErr = ""
	s.mu.Unlock()
}

// recordFailure records one REAL apply failure and (re)arms the bounded retry
// backoff for that exact target. It must only be called for an attempt that was
// actually made: a deferred (skipped) poll is not a new failure.
func (s *Supervisor) recordFailure(id, targetHash string, err error) {
	if id == "" || err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.failures[id]
	if f == nil {
		f = &failure{}
		s.failures[id] = f
	}
	f.count++
	f.lastErr = err.Error()
	f.hash = targetHash
	delay := failureBackoffBase
	for i := 1; i < f.count && delay < failureBackoffMax; i++ {
		delay *= 2
	}
	if delay > failureBackoffMax {
		delay = failureBackoffMax
	}
	f.next = s.now().Add(delay)
}

func (s *Supervisor) clearFailure(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failures, id)
}

// skipSetFor reports the instances whose CURRENT target already failed and is
// still inside its retry backoff. A different target hash - a new revision, a
// narrowed authorization (revocation or expiry) or a corrected technical config
// - is never skipped, so an old backoff can never hold a tightening back.
func (s *Supervisor) skipSetFor(targets []xray.Target) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	now := s.now()
	for _, t := range targets {
		f := s.failures[t.InstanceID]
		if f == nil || t.Hash == "" || f.hash != t.Hash {
			continue
		}
		if now.Before(f.next) {
			out[t.InstanceID] = true
		}
	}
	return out
}

func (s *Supervisor) failureCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.failures)
}

func (s *Supervisor) storedInstance(ctx context.Context, nodeID string) (state.InstanceRecord, bool, error) {
	records, err := s.store.Instances(ctx)
	if err != nil {
		return state.InstanceRecord{}, false, err
	}
	for _, rec := range records {
		if rec.NodeID == nodeID {
			return rec, true, nil
		}
	}
	return state.InstanceRecord{}, false, nil
}

// ManagementServer renders the local API address for logging/tests.
func ManagementServer(port int) string {
	return net.JoinHostPort(xray.APIListen, strconv.Itoa(port))
}
