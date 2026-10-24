package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"proxypanel/internal/agent/config"
	"proxypanel/internal/agent/control"
	"proxypanel/internal/agent/state"
	"proxypanel/internal/agent/xray"
	"proxypanel/internal/contracts"
)

// stubRunner is an in-memory Xray runner used to observe lifecycle decisions.
type stubRunner struct {
	mu        sync.Mutex
	starts    int
	stops     int
	configs   []string
	lastStart string
	// Per instance counters and last started config, so a test can prove that an
	// operation touched only the instance it was supposed to touch.
	startsByID    map[string]int
	stopsByID     map[string]int
	lastStartByID map[string]string
	nextPID       int
	testErr       error
	stats         map[string][]xray.StatCounter
	// statsErr, when set, makes every StatsQuery fail (sampling-retry tests).
	statsErr error
	// statsAny is returned for any server when stats has no entry for it.
	statsAny []xray.StatCounter
	version  string
	// testDelay and startDelay widen the lifecycle window so concurrency
	// regressions expose lock contention deterministically.
	testDelay  time.Duration
	testBegin  chan struct{}
	startDelay time.Duration
	// testEndAt is the wall clock time (unix nano) at which the FIRST slow
	// technical validation of a round returned (i.e. when the blocked node A was
	// released).
	testEndAt atomic.Int64
	// crashOnStart makes Start return the process with a CLOSED Exited channel,
	// i.e. a core that exits right after being reported as started.
	crashOnStart bool
	// startErr, when set, makes every Start fail (start gate tests).
	startErr error
	// stopAt records when each instance was stopped (unix nano), so a test can
	// prove that a revocation was enforced BEFORE a peer's slow validation ended.
	stopAt map[string]int64
	// stopFailure makes every stop report a survivor: the process keeps its
	// Exited channel open, i.e. the core could not be proven gone.
	stopFailure bool
	// procs records the started fake processes per instance id so a test can
	// close an exit channel without going through Stop (a crash).
	procs map[string]*stubProc
}

// stoppedAt returns when the first stop of one instance was requested.
func (r *stubRunner) stoppedAt(id string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.stopAt[id]; ok && v != 0 {
		return time.Unix(0, v)
	}
	return time.Time{}
}

func (r *stubRunner) setStopFailure(v bool) {
	r.mu.Lock()
	r.stopFailure = v
	r.mu.Unlock()
}

func (r *stubRunner) Test(ctx context.Context, configPath string) error {
	r.mu.Lock()
	delay, err := r.testDelay, r.testErr
	r.mu.Unlock()
	if r.testBegin != nil {
		select {
		case r.testBegin <- struct{}{}:
		default:
		}
	}
	// The delay is applied without holding the runner lock, like a real process
	// spawn, so only the manager locks are exercised.
	time.Sleep(delay)
	if delay > 0 {
		// Only the FIRST slow validation of the round is recorded: later ones (the
		// reconciled peer) would overwrite the moment the blocked node was
		// released.
		r.testEndAt.CompareAndSwap(0, time.Now().UnixNano())
	}
	return err
}

// setDelays updates the simulated lifecycle durations.
func (r *stubRunner) setDelays(testDelay, startDelay time.Duration) {
	r.mu.Lock()
	r.testDelay = testDelay
	r.startDelay = startDelay
	r.mu.Unlock()
}

func (r *stubRunner) Start(ctx context.Context, spec xray.StartSpec) (xray.Proc, error) {
	r.mu.Lock()
	delay := r.startDelay
	r.mu.Unlock()
	time.Sleep(delay)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
	if r.startsByID == nil {
		r.startsByID = map[string]int{}
	}
	if r.lastStartByID == nil {
		r.lastStartByID = map[string]string{}
	}
	r.startsByID[spec.InstanceID]++
	if raw, err := os.ReadFile(spec.ConfigPath); err == nil {
		r.configs = append(r.configs, string(raw))
		r.lastStart = string(raw)
		r.lastStartByID[spec.InstanceID] = string(raw)
	}
	if r.startErr != nil {
		return nil, r.startErr
	}
	r.nextPID++
	proc := &stubProc{runner: r, id: spec.InstanceID, pid: 900000 + r.nextPID, exited: make(chan struct{})}
	if r.crashOnStart {
		// Close through the Once so a later Stop can not close it twice.
		proc.once.Do(func() { close(proc.exited) })
	}
	if r.procs == nil {
		r.procs = map[string]*stubProc{}
	}
	r.procs[spec.InstanceID] = proc
	return proc, nil
}

// countsFor reports how often one instance was started and stopped.
func (r *stubRunner) countsFor(id string) (starts, stops int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.startsByID[id], r.stopsByID[id]
}

// lastStartedFor returns the configuration text most recently started for one
// instance.
func (r *stubRunner) lastStartedFor(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastStartByID[id]
}

func (r *stubRunner) StatsQuery(ctx context.Context, server string) ([]xray.StatCounter, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.statsErr != nil {
		return nil, r.statsErr
	}
	if counters, ok := r.stats[server]; ok {
		return counters, nil
	}
	return r.statsAny, nil
}

// setStatsError makes every StatsQuery fail, the way a killed or hung statsquery
// looks to the sampler. Pass nil to recover.
func (r *stubRunner) setStatsError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statsErr = err
}

func (r *stubRunner) Version(ctx context.Context) (string, error) {
	if r.version == "" {
		return "26.3.27", nil
	}
	return r.version, nil
}

func (r *stubRunner) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts, r.stops
}

func (r *stubRunner) clientCounts() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]int, 0, len(r.configs))
	for _, raw := range r.configs {
		out = append(out, countClients(raw))
	}
	return out
}

// stopCount returns how many core processes the stub runner saw stopped.
func stopCount(r *stubRunner) int {
	_, stops := r.counts()
	return stops
}

// waitForStops waits until at least want stops happened or the budget expires.
func waitForStops(r *stubRunner, want int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		if stopCount(r) >= want {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func countClients(raw string) int {
	var doc struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Settings struct {
				Clients []json.RawMessage `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return -1
	}
	for _, in := range doc.Inbounds {
		if in.Tag == xray.APITag {
			continue
		}
		return len(in.Settings.Clients)
	}
	return -1
}

type stubProc struct {
	runner *stubRunner
	id     string
	pid    int
	once   sync.Once
	// exited is closed by Stop, so the manager can prove that the process is
	// really gone instead of trusting the stop call alone.
	exited chan struct{}
}

func (p *stubProc) PID() int                { return p.pid }
func (p *stubProc) Exited() <-chan struct{} { return p.exited }
func (p *stubProc) ExitError() error        { return nil }
func (p *stubProc) Output() []string        { return nil }
func (p *stubProc) Stop(ctx context.Context) error {
	p.runner.mu.Lock()
	p.runner.stops++
	if p.runner.stopsByID == nil {
		p.runner.stopsByID = map[string]int{}
	}
	p.runner.stopsByID[p.id]++
	survivor := p.runner.stopFailure
	if p.runner.stopAt == nil {
		p.runner.stopAt = map[string]int64{}
	}
	if _, ok := p.runner.stopAt[p.id]; !ok {
		p.runner.stopAt[p.id] = time.Now().UnixNano()
	}
	p.runner.mu.Unlock()
	if survivor {
		// A stop that is not confirmed: the exit channel stays open.
		return errors.New("injected stop failure: core survived the stop budget")
	}
	p.once.Do(func() {
		close(p.exited)
	})
	return nil
}

// fakePanel implements the agent endpoints with the frozen envelopes.
type fakePanel struct {
	mu                   sync.Mutex
	generation           string
	revision             int64
	instances            []contracts.Instance
	expectedUsers        int
	snapshotFail         int
	snapshotCalls        int
	applied              []contracts.ApplyReport
	heartbeats           []contracts.Heartbeat
	usage                []contracts.UsageBatch
	registerCalls        int
	token                string
	registerErr          string
	registeredGeneration string
}

func (p *fakePanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if p.token != "" && r.Header.Get("Authorization") != "Bearer "+p.token {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"UNAUTHORIZED","message":"bad token"}}`))
		return
	}
	switch r.URL.Path {
	case control.PathRegister:
		p.registerCalls++
		if p.registerErr != "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":{"code":"INVALID_CODE","message":%q}}`, p.registerErr)
			return
		}
		p.token = "issued-token"
		p.registeredGeneration = p.generation
		_, _ = w.Write([]byte(`{"data":{"agent_id":"agent-1","token":"issued-token","control_generation":"` + p.generation + `"}}`))
	case control.PathSnapshot:
		p.snapshotCalls++
		if p.snapshotFail > 0 {
			p.snapshotFail--
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":"DB_DOWN","message":"database unavailable"}}`))
			return
		}
		rev := int64(0)
		if v := r.URL.Query().Get("revision"); v != "" {
			rev, _ = strconv.ParseInt(v, 10, 64)
		}
		// The agent sends the generation it has applied. A panel that changed or
		// restored its control generation must answer with a full snapshot; only
		// the same generation with the same or an older revision yields 204.
		// (Registered agents get the generation they were registered with.)
		served := r.URL.Query().Get("control_generation")
		if served == "" {
			served = p.registeredGeneration
			if served == "" {
				served = p.generation
			}
		}
		if rev >= p.revision && served == p.generation {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		snap := contracts.Snapshot{
			SchemaVersion:     contracts.SchemaVersion,
			ControlGeneration: p.generation,
			Revision:          p.revision,
			IssuedAt:          time.Now().UTC(),
			ExpectedUserCount: p.expectedUsers,
			Instances:         p.instances,
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": snap})
	case control.PathApplied:
		var report contracts.ApplyReport
		_ = json.NewDecoder(r.Body).Decode(&report)
		p.applied = append(p.applied, report)
		_, _ = w.Write([]byte(`{"data":{"accepted":true}}`))
	case control.PathUsage:
		var batch contracts.UsageBatch
		_ = json.NewDecoder(r.Body).Decode(&batch)
		p.usage = append(p.usage, batch)
		_, _ = fmt.Fprintf(w, `{"data":{"batch_id":%q,"status":"accepted"}}`, batch.BatchID)
	case control.PathHeartbeat:
		var hb contracts.Heartbeat
		_ = json.NewDecoder(r.Body).Decode(&hb)
		p.heartbeats = append(p.heartbeats, hb)
		_, _ = w.Write([]byte(`{"data":{"accepted":true}}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (p *fakePanel) setSnapshot(generation string, revision int64, instances []contracts.Instance, users int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.generation = generation
	p.revision = revision
	p.instances = instances
	p.expectedUsers = users
}

func (p *fakePanel) appliedReports() []contracts.ApplyReport {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]contracts.ApplyReport, len(p.applied))
	copy(out, p.applied)
	return out
}

func nodeInstance(users []contracts.AuthorizedUser, emails ...string) contracts.Instance {
	clients := make([]map[string]any, 0, len(emails))
	for i, e := range emails {
		clients = append(clients, map[string]any{"id": fmt.Sprintf("11111111-1111-1111-1111-00000000000%d", i), "email": e})
	}
	doc := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag": "vless-in", "listen": "127.0.0.1", "port": 443, "protocol": "vless",
			"settings":       map[string]any{"decryption": "none", "clients": clients},
			"streamSettings": map[string]any{"network": "tcp", "security": "none"},
		}},
		"outbounds": []any{map[string]any{"protocol": "freedom"}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return contracts.Instance{ID: "node-1", NodeID: "node-1", Config: raw, Users: users}
}

type testEnv struct {
	sup    *Supervisor
	panel  *fakePanel
	runner *stubRunner
	server *httptest.Server
	store  *state.Store
	now    time.Time
	shift  atomic.Int64
}

// advance moves the shared test clock forward without racing the Run loops.
func (e *testEnv) advance(d time.Duration) {
	e.shift.Add(int64(d))
}

// useShortIntervals makes the Run loops cycle quickly.
func (e *testEnv) useShortIntervals() {
	e.sup.cfg.PollIntervalSeconds = 1
	e.sup.cfg.StatsIntervalSeconds = 1
	e.sup.cfg.HeartbeatIntervalSeconds = 1
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	panel := &fakePanel{}
	server := httptest.NewServer(panel)
	t.Cleanup(server.Close)
	runner := &stubRunner{}
	dir := t.TempDir()
	cfg := &config.Config{
		PanelURL:          server.URL,
		Token:             "test-token",
		AgentID:           "agent-1",
		XrayPath:          filepath.Join(dir, "xray.exe"),
		StateDir:          filepath.Join(dir, "state"),
		AllowLoopbackHTTP: true,
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("config: %v", err)
	}
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	env := &testEnv{panel: panel, runner: runner, server: server, now: clock}
	now := func() time.Time { return env.now.Add(time.Duration(env.shift.Load())) }
	client, err := control.New(cfg.PanelURL, "test")
	if err != nil {
		t.Fatalf("control.New: %v", err)
	}
	sup, err := New(Options{
		Config:  cfg,
		Logger:  log.New(io.Discard, "", 0),
		Client:  client,
		Runner:  runner,
		Now:     now,
		Version: "test",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = sup.Close() })
	env.sup = sup
	env.store = sup.Store()
	return env
}

func TestSyncAppliesSnapshotAndAdvancesRevision(t *testing.T) {
	env := newTestEnv(t)
	future := env.now.Add(time.Hour)
	env.panel.setSnapshot("gen-1", 4, []contracts.Instance{
		nodeInstance([]contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test", ExpiresAt: &future}}, "alice@example.test"),
	}, 1)
	if err := env.sup.syncOnce(context.Background()); err != nil {
		t.Fatalf("syncOnce: %v", err)
	}
	identity, err := env.store.Identity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if identity.AppliedRevision != 4 || identity.ControlGeneration != "gen-1" {
		t.Fatalf("applied watermark = %+v", identity)
	}
	starts, _ := env.runner.counts()
	if starts != 1 {
		t.Fatalf("starts = %d", starts)
	}
	reports := env.panel.appliedReports()
	if len(reports) != 1 || reports[0].State != applyStateApplied || reports[0].Revision != 4 {
		t.Fatalf("unexpected reports: %+v", reports)
	}
	if len(reports[0].Results) != 1 || reports[0].Results[0].State != xray.StateApplied {
		t.Fatalf("unexpected results: %+v", reports[0].Results)
	}
	if !reports[0].Results[0].OldConnectionsTerminated {
		t.Fatal("apply must report terminated old connections")
	}
	if counts := env.runner.clientCounts(); len(counts) != 1 || counts[0] != 1 {
		t.Fatalf("unexpected client counts: %v", counts)
	}
}

func TestEmptySnapshotStopsAllInstances(t *testing.T) {
	env := newTestEnv(t)
	future := env.now.Add(time.Hour)
	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{
		nodeInstance([]contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test", ExpiresAt: &future}}, "alice@example.test"),
	}, 1)
	if err := env.sup.syncOnce(context.Background()); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	env.panel.setSnapshot("gen-1", 2, nil, 0)
	if err := env.sup.syncOnce(context.Background()); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if _, stops := env.runner.counts(); stops != 1 {
		t.Fatalf("stops = %d", stops)
	}
	identity, _ := env.store.Identity(context.Background())
	if identity.AppliedRevision != 2 {
		t.Fatalf("revision = %d, want 2", identity.AppliedRevision)
	}
	reports := env.panel.appliedReports()
	last := reports[len(reports)-1]
	if len(last.Results) != 1 || last.Results[0].State != xray.StateStopped {
		t.Fatalf("unexpected results: %+v", last.Results)
	}
	records, err := env.store.Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("instance records must be removed: %+v", records)
	}
}

func TestStaleRevisionIsIgnored(t *testing.T) {
	env := newTestEnv(t)
	env.panel.setSnapshot("gen-1", 5, []contracts.Instance{
		nodeInstance([]contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test"}}, "alice@example.test"),
	}, 1)
	if err := env.sup.syncOnce(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	appliedBefore := len(env.panel.appliedReports())
	if err := env.store.SetApplied(context.Background(), "gen-1", 9); err != nil {
		t.Fatal(err)
	}
	env.panel.setSnapshot("gen-1", 6, []contracts.Instance{}, 0)
	if err := env.sup.syncOnce(context.Background()); err != nil {
		t.Fatalf("stale sync: %v", err)
	}
	if len(env.panel.appliedReports()) != appliedBefore {
		t.Fatal("an older revision must not be applied")
	}
	identity, _ := env.store.Identity(context.Background())
	if identity.AppliedRevision != 9 {
		t.Fatalf("revision regressed to %d", identity.AppliedRevision)
	}
	if _, stops := env.runner.counts(); stops != 0 {
		t.Fatal("stale snapshot stopped a running instance")
	}
}

func TestPanelHTTPSFailureKeepsLocalState(t *testing.T) {
	env := newTestEnv(t)
	env.panel.setSnapshot("gen-1", 3, []contracts.Instance{
		nodeInstance([]contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test"}}, "alice@example.test"),
	}, 1)
	if err := env.sup.syncOnce(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	env.panel.mu.Lock()
	env.panel.snapshotFail = 1
	env.panel.mu.Unlock()
	if err := env.sup.syncOnce(context.Background()); err == nil {
		t.Fatal("a panel failure must be reported")
	}
	identity, _ := env.store.Identity(context.Background())
	if identity.AppliedRevision != 3 {
		t.Fatalf("revision changed during a failure: %+v", identity)
	}
	if _, stops := env.runner.counts(); stops != 0 {
		t.Fatal("a panel failure must not stop the running instance")
	}
	// A stale revision is ignored (no error, no apply).
	env.panel.setSnapshot("gen-1", 3, []contracts.Instance{}, 0)
	if err := env.sup.syncOnce(context.Background()); err != nil {
		t.Fatalf("stale sync: %v", err)
	}
	if _, stops := env.runner.counts(); stops != 0 {
		t.Fatal("stale snapshot stopped the running instance")
	}
}

func TestExpiredUsersAreFilteredDuringApply(t *testing.T) {
	env := newTestEnv(t)
	past := env.now.Add(-time.Minute)
	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{
		nodeInstance([]contracts.AuthorizedUser{
			{MemberID: "m1", Email: "alice@example.test", ExpiresAt: &past},
		}, "alice@example.test"),
	}, 1)
	if err := env.sup.syncOnce(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	counts := env.runner.clientCounts()
	if len(counts) != 1 || counts[0] != 0 {
		t.Fatalf("expired user must not be authorized: %v", counts)
	}
	reports := env.panel.appliedReports()
	if reports[0].Results[0].AppliedUserCount != 0 {
		t.Fatalf("applied user count = %d", reports[0].Results[0].AppliedUserCount)
	}
}

func TestLocalExpiryRebuildsAffectedInstance(t *testing.T) {
	env := newTestEnv(t)
	expiry := env.now.Add(30 * time.Second)
	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{
		nodeInstance([]contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test", ExpiresAt: &expiry}}, "alice@example.test"),
	}, 1)
	clock := env.now
	env.sup.now = func() time.Time { return clock }
	if err := env.sup.syncOnce(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if starts, _ := env.runner.counts(); starts != 1 {
		t.Fatalf("starts = %d", starts)
	}
	// Nothing to do before the expiry.
	if err := env.sup.expiryOnce(context.Background()); err != nil {
		t.Fatalf("expiryOnce: %v", err)
	}
	if starts, _ := env.runner.counts(); starts != 1 {
		t.Fatalf("unexpected restart before expiry: %d", starts)
	}
	// After the expiry the instance must be rebuilt without the user.
	clock = expiry.Add(time.Second)
	env.sup.sampler.SetNow(func() time.Time { return clock })
	if err := env.sup.expiryOnce(context.Background()); err != nil {
		t.Fatalf("expiryOnce: %v", err)
	}
	starts, stops := env.runner.counts()
	if starts != 2 || stops != 1 {
		t.Fatalf("expiry did not rebuild the instance: starts=%d stops=%d", starts, stops)
	}
	counts := env.runner.clientCounts()
	if counts[len(counts)-1] != 0 {
		t.Fatalf("expired user still present after local expiry: %v", counts)
	}
	// The rebuilt (revoked) state must be persisted for the next restart.
	records, err := env.store.Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || len(records[0].Users) != 0 {
		t.Fatalf("unexpected persisted users: %+v", records)
	}
}

// Reproduce an expires_at-only snapshot update, followed by a panel outage.
// The config hash stays identical, but the committed user expiry must not stay
// at the old value. The clock advance is deliberate; no wall-clock wait is used.
func TestLocalExpiryAfterMetadataOnlySnapshotUpdate(t *testing.T) {
	for _, hadExpiry := range []bool{false, true} {
		name := "add-expiry"
		if hadExpiry {
			name = "shorten-expiry"
		}
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t)
			ctx := context.Background()
			user := contracts.AuthorizedUser{MemberID: "m1", Email: "alice@example.test"}
			if hadExpiry {
				old := env.now.Add(time.Hour)
				user.ExpiresAt = &old
			}
			env.panel.setSnapshot("gen-1", 1, []contracts.Instance{nodeInstance([]contracts.AuthorizedUser{user}, user.Email)}, 1)
			if err := env.sup.syncOnce(ctx); err != nil {
				t.Fatal(err)
			}
			initial, err := env.store.Instances(ctx)
			if err != nil || len(initial) != 1 {
				t.Fatalf("initial records: %+v, err=%v", initial, err)
			}

			expiry := env.now.Add(12 * time.Second)
			user.ExpiresAt = &expiry
			env.panel.setSnapshot("gen-1", 2, []contracts.Instance{nodeInstance([]contracts.AuthorizedUser{user}, user.Email)}, 1)
			if err := env.sup.syncOnce(ctx); err != nil {
				t.Fatal(err)
			}
			updated, err := env.store.Instances(ctx)
			if err != nil || len(updated) != 1 {
				t.Fatalf("updated records: %+v, err=%v", updated, err)
			}
			rec := updated[0]
			if rec.ConfigHash != initial[0].ConfigHash || rec.CoreGeneration != initial[0].CoreGeneration {
				t.Fatal("an expiry-only update must not restart or reconfigure the core")
			}
			if !rec.NextExpiry.Equal(expiry) || len(rec.Users) != 1 || rec.Users[0].ExpiresAt == nil || !rec.Users[0].ExpiresAt.Equal(expiry) {
				t.Errorf("persisted expiry metadata is stale: next=%v users=%+v", rec.NextExpiry, rec.Users)
			}
			id, err := env.store.Identity(ctx)
			if err != nil || id.AppliedRevision != 2 {
				t.Fatalf("updated snapshot was not committed: %+v, err=%v", id, err)
			}
			env.server.Close()
			if err := env.sup.syncOnce(ctx); err == nil {
				t.Fatal("the panel must be unreachable")
			}
			if err := env.sup.expiryOnce(ctx); err != nil {
				t.Fatal(err)
			}
			if starts, stops := env.runner.counts(); starts != 1 || stops != 0 {
				t.Fatalf("the core was restarted before expiry: starts=%d stops=%d", starts, stops)
			}

			env.advance(13 * time.Second)
			if err := env.sup.expiryOnce(ctx); err != nil {
				t.Fatal(err)
			}
			if starts, stops := env.runner.counts(); starts != 2 || stops != 1 {
				t.Errorf("expiry did not rebuild the core: starts=%d stops=%d", starts, stops)
			}
			if countClients(env.runner.lastStartedFor("node-1")) != 0 {
				t.Error("the locally expired user is still authorized while the panel is offline")
			}
			records, err := env.store.Instances(ctx)
			if err != nil || len(records) != 1 || len(records[0].Users) != 0 || records[0].AppliedUserCount != 0 || !records[0].NextExpiry.IsZero() {
				t.Fatalf("expired authorization was not persisted: %+v, err=%v", records, err)
			}
			if err := env.sup.expiryOnce(ctx); err != nil {
				t.Fatal(err)
			}
			if starts, stops := env.runner.counts(); starts != 2 || stops != 1 {
				t.Fatal("local expiry unnecessarily rebuilt the core again")
			}
			// Restoring from the committed record must not revive the expired user.
			env.sup.mgr.StopAll(ctx)
			if err := env.sup.restore(ctx); err != nil {
				t.Fatal(err)
			}
			if countClients(env.runner.lastStartedFor("node-1")) != 0 {
				t.Fatal("restore revived the expired user")
			}
		})
	}
}

func TestRegistrationStoresTokenSecurely(t *testing.T) {
	panel := &fakePanel{}
	panel.setSnapshot("gen-1", 1, nil, 0)
	server := httptest.NewServer(panel)
	defer server.Close()
	dir := t.TempDir()
	cfg := &config.Config{
		PanelURL:          server.URL,
		RegistrationCode:  "one-time-code",
		XrayPath:          filepath.Join(dir, "xray.exe"),
		StateDir:          filepath.Join(dir, "state"),
		AllowLoopbackHTTP: true,
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("config: %v", err)
	}
	sup, err := New(Options{Config: cfg, Logger: log.New(io.Discard, "", 0), Runner: &stubRunner{}, Version: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sup.Close()
	if err := sup.syncOnce(context.Background()); err != nil {
		t.Fatalf("syncOnce: %v", err)
	}
	if panel.registerCalls != 1 {
		t.Fatalf("register calls = %d", panel.registerCalls)
	}
	identity, err := sup.Store().Identity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if identity.Token != "issued-token" || identity.AgentID != "agent-1" {
		t.Fatalf("identity not persisted: %+v", identity)
	}
	// A second cycle must reuse the stored token and not re-register.
	if err := sup.syncOnce(context.Background()); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if panel.registerCalls != 1 {
		t.Fatal("agent re-registered although a token exists")
	}
	if !sup.Client().HasToken() {
		t.Fatal("client has no token after registration")
	}
}

func TestStateDirectoryLockPreventsSecondAgent(t *testing.T) {
	env := newTestEnv(t)
	cfg := env.sup.cfg
	_, err := New(Options{Config: cfg, Logger: log.New(io.Discard, "", 0), Client: env.sup.client, Runner: &stubRunner{}})
	if err == nil {
		t.Fatal("a second agent must not open the same state directory")
	}
}

func TestHeartbeatReportsRuntimeState(t *testing.T) {
	env := newTestEnv(t)
	env.panel.setSnapshot("gen-1", 2, []contracts.Instance{
		nodeInstance([]contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test"}}, "alice@example.test"),
	}, 1)
	if err := env.sup.syncOnce(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if err := env.sup.heartbeatOnce(context.Background()); err != nil {
		t.Fatalf("heartbeatOnce: %v", err)
	}
	env.panel.mu.Lock()
	beats := append([]contracts.Heartbeat{}, env.panel.heartbeats...)
	env.panel.mu.Unlock()
	if len(beats) != 1 {
		t.Fatalf("heartbeats = %d", len(beats))
	}
	hb := beats[0]
	if hb.Version != "test" || hb.Revision != 2 || hb.QueueDepth != 0 {
		t.Fatalf("unexpected heartbeat: %+v", hb)
	}
	if hb.XrayVersion != "26.3.27" {
		t.Fatalf("core version missing: %q", hb.XrayVersion)
	}
	if len(hb.Instances) != 1 || hb.Instances[0].State != xray.StateApplied {
		t.Fatalf("instance states missing: %+v", hb.Instances)
	}
	if _, ok := hb.Metrics["instances_running"]; !ok {
		t.Fatalf("runtime metrics missing: %+v", hb.Metrics)
	}
}

func TestUsageUploadFlow(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{
		nodeInstance([]contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test"}}, "alice@example.test"),
	}, 1)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	stats := env.sup.mgr.QueryStats(ctx)
	if len(stats) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(stats))
	}
	if _, err := env.store.Measure(ctx, "node-1", stats[0].CoreGeneration, env.now, []state.CounterItem{
		{MemberID: "m1", Counter: "user>>>alice@example.test>>>traffic>>>uplink", Value: 1234},
	}); err != nil {
		t.Fatalf("Measure: %v", err)
	}
	if err := env.sup.uploadOnce(ctx); err != nil {
		t.Fatalf("uploadOnce: %v", err)
	}
	env.panel.mu.Lock()
	batches := append([]contracts.UsageBatch{}, env.panel.usage...)
	env.panel.mu.Unlock()
	if len(batches) != 1 || len(batches[0].Samples) != 1 {
		t.Fatalf("unexpected uploaded batches: %+v", batches)
	}
	if batches[0].Samples[0].UploadBytes != 1234 {
		t.Fatalf("unexpected sample: %+v", batches[0].Samples[0])
	}
	depth, err := env.store.QueueDepth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if depth.Batches != 0 {
		t.Fatalf("acknowledged batch not removed: %+v", depth)
	}
}

func TestQueueOverCapacityStopsInstances(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{
		nodeInstance([]contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test"}}, "alice@example.test"),
	}, 1)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	env.sup.setQueueOverCapacity(true)
	if !waitForStops(env.runner, 1, 5*time.Second) {
		t.Fatalf("over capacity must stop the affected instances: stops=%d", stopCount(env.runner))
	}
	if !env.sup.queueOverCapacity() {
		t.Fatal("capacity flag not set")
	}
	if err := env.sup.sampleOnce(ctx); err == nil {
		t.Fatal("sampling must be paused while the queue is over capacity")
	}
	env.sup.setQueueOverCapacity(false)
	if env.sup.queueOverCapacity() {
		t.Fatal("capacity flag not cleared")
	}
}

func TestPersistenceFailureStopsInstances(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{
		nodeInstance([]contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test"}}, "alice@example.test"),
	}, 1)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	env.sup.setPersistenceError(io.ErrUnexpectedEOF)
	// The stop is deliberately asynchronous (it must not run on the stack that
	// holds the instance lock), so wait for it within a bound.
	if !waitForStops(env.runner, 1, 5*time.Second) {
		t.Fatalf("a persistence failure must stop the affected instances: stops=%d", stopCount(env.runner))
	}
}

func TestRunShutsDownWithoutLeavingProcesses(t *testing.T) {
	env := newTestEnv(t)
	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{
		nodeInstance([]contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test"}}, "alice@example.test"),
	}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- env.sup.Run(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		identity, err := env.store.Identity(context.Background())
		if err == nil && identity.AppliedRevision == 1 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("agent did not apply the snapshot in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not stop in time")
	}
	if _, stops := env.runner.counts(); stops < 1 {
		t.Fatalf("shutdown must stop the core processes: stops=%d", stops)
	}
	records, err := env.store.Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].State != xray.StateStopped {
		t.Fatalf("shutdown state not persisted: %+v", records)
	}
}

func activeUser(env *testEnv, email string) contracts.AuthorizedUser {
	return contracts.AuthorizedUser{MemberID: "member-" + email, Email: email, ExpiresAt: ptrTime(env.now.Add(24 * time.Hour))}
}

func ptrTime(t time.Time) *time.Time { return &t }

func snapshotWith(users []contracts.AuthorizedUser, emails ...string) contracts.Instance {
	return nodeInstance(users, emails...)
}

// TestGenerationChangeRequiresLocalReconcile proves that a new control
// generation is never trusted automatically: the agent keeps its known
// authorization, reports coordination_required and only applies after an
// explicit local acceptance of exactly that generation.
func TestGenerationChangeRequiresLocalReconcile(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	alice := activeUser(env, "alice@example.test")
	env.panel.setSnapshot("gen-1", 8, []contracts.Instance{snapshotWith([]contracts.AuthorizedUser{alice}, "alice@example.test")}, 1)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if starts, stops := env.runner.counts(); starts != 1 || stops != 0 {
		t.Fatalf("unexpected lifecycle: starts=%d stops=%d", starts, stops)
	}

	env.panel.setSnapshot("gen-2", 1, []contracts.Instance{snapshotWith([]contracts.AuthorizedUser{alice}, "alice@example.test")}, 1)
	err := env.sup.syncOnce(ctx)
	if err == nil {
		t.Fatal("a new control generation must not be applied without a local reconcile action")
	}
	if !strings.Contains(err.Error(), "local reconciliation") {
		t.Fatalf("error should explain the required reconciliation: %v", err)
	}
	if starts, stops := env.runner.counts(); starts != 1 || stops != 0 {
		t.Fatalf("the instance must keep running with the known authorization: starts=%d stops=%d", starts, stops)
	}
	identity, _ := env.store.Identity(ctx)
	if identity.ControlGeneration != "gen-1" || identity.AppliedRevision != 8 {
		t.Fatalf("the applied watermark must not move: %+v", identity)
	}
	if pending, _ := env.store.PendingGeneration(ctx); pending != "gen-2" {
		t.Fatalf("pending generation = %q", pending)
	}
	reports := env.panel.appliedReports()
	last := reports[len(reports)-1]
	if last.State != applyStateCoordination || last.ControlGeneration != "gen-2" {
		t.Fatalf("unexpected coordination report: %+v", last)
	}
	// The same offered snapshot must not be reported again and again.
	if err := env.sup.syncOnce(ctx); err == nil {
		t.Fatal("coordination must stay in force until accepted")
	}
	if len(env.panel.appliedReports()) != len(reports) {
		t.Fatal("the coordination state was reported more than once for the same snapshot")
	}

	// An operator accepts exactly this generation on the node.
	if err := env.store.SetAcceptedGeneration(ctx, "gen-2"); err != nil {
		t.Fatal(err)
	}
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("the accepted generation must apply: %v", err)
	}
	if starts, stops := env.runner.counts(); starts != 2 || stops != 1 {
		t.Fatalf("accepted generation must rebuild the instance: starts=%d stops=%d", starts, stops)
	}
	identity, _ = env.store.Identity(ctx)
	if identity.ControlGeneration != "gen-2" || identity.AppliedRevision != 1 {
		t.Fatalf("generation was not adopted: %+v", identity)
	}
	if accepted, _ := env.store.AcceptedGeneration(ctx); accepted != "" {
		t.Fatalf("the acceptance marker must be cleared after use: %q", accepted)
	}
	if pending, _ := env.store.PendingGeneration(ctx); pending != "" {
		t.Fatalf("pending generation must be cleared: %q", pending)
	}
}

// TestOldBackupGenerationDoesNotRestoreRevokedUsers proves that a restored (old)
// control generation cannot re-authorize a member that was revoked afterwards.
func TestOldBackupGenerationDoesNotRestoreRevokedUsers(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	alice := activeUser(env, "alice@example.test")
	bob := activeUser(env, "bob@example.test")

	env.panel.setSnapshot("gen-1", 2, []contracts.Instance{snapshotWith([]contracts.AuthorizedUser{alice, bob}, "alice@example.test", "bob@example.test")}, 2)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("sync with two users: %v", err)
	}
	if counts := env.runner.clientCounts(); counts[len(counts)-1] != 2 {
		t.Fatalf("expected two authorized clients, got %v", counts)
	}
	// Revoke bob in the same generation.
	env.panel.setSnapshot("gen-1", 3, []contracts.Instance{snapshotWith([]contracts.AuthorizedUser{alice}, "alice@example.test")}, 1)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("revocation sync: %v", err)
	}
	if counts := env.runner.clientCounts(); counts[len(counts)-1] != 1 {
		t.Fatalf("bob was not revoked: %v", counts)
	}
	startsBefore, _ := env.runner.counts()

	// The panel is restored from an old backup: older generation, older revision,
	// bob is authorized again in that snapshot.
	env.panel.setSnapshot("gen-backup", 1, []contracts.Instance{snapshotWith([]contracts.AuthorizedUser{alice, bob}, "alice@example.test", "bob@example.test")}, 2)
	if err := env.sup.syncOnce(ctx); err == nil {
		t.Fatal("a restored control generation must not be applied automatically")
	}
	if starts, _ := env.runner.counts(); starts != startsBefore {
		t.Fatal("the instance must not be rebuilt for an unaccepted generation")
	}
	counts := env.runner.clientCounts()
	if counts[len(counts)-1] != 1 {
		t.Fatalf("the revoked member was restored by the backup generation: %v", counts)
	}
	records, err := env.store.Instances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || len(records[0].Users) != 1 || records[0].Users[0].Email != "alice@example.test" {
		t.Fatalf("the persisted authorization must stay revoked: %+v", records)
	}

	// Only the explicit local reconcile action may adopt the backup generation.
	if err := env.store.SetAcceptedGeneration(ctx, "gen-backup"); err != nil {
		t.Fatal(err)
	}
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("accepted backup generation: %v", err)
	}
	if counts := env.runner.clientCounts(); counts[len(counts)-1] != 2 {
		t.Fatalf("after explicit acceptance the snapshot must be applied: %v", counts)
	}
}

// TestSameRevisionDifferentGenerationIsNotIgnored proves that a changed control
// generation is detected even when the revision number is unchanged.
func TestSameRevisionDifferentGenerationIsNotIgnored(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	alice := activeUser(env, "alice@example.test")
	instance := snapshotWith([]contracts.AuthorizedUser{alice}, "alice@example.test")
	env.panel.setSnapshot("gen-1", 5, []contracts.Instance{instance}, 1)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	// Same generation and revision: the panel answers 204 and nothing happens.
	reportsBefore := len(env.panel.appliedReports())
	startsBefore, _ := env.runner.counts()
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("same generation/revision must be a no-op: %v", err)
	}
	if len(env.panel.appliedReports()) != reportsBefore {
		t.Fatal("an unchanged snapshot must not produce a report")
	}
	if starts, _ := env.runner.counts(); starts != startsBefore {
		t.Fatal("an unchanged snapshot must not restart anything")
	}
	// Different generation, same revision number: must be detected.
	env.panel.setSnapshot("gen-2", 5, []contracts.Instance{instance}, 1)
	if err := env.sup.syncOnce(ctx); err == nil {
		t.Fatal("the same revision in a new generation must not be ignored")
	}
	reports := env.panel.appliedReports()
	last := reports[len(reports)-1]
	if last.State != applyStateCoordination || last.ControlGeneration != "gen-2" || last.Revision != 5 {
		t.Fatalf("unexpected coordination report: %+v", last)
	}
	if starts, _ := env.runner.counts(); starts != startsBefore {
		t.Fatal("coordination must not restart the instance")
	}
}

// TestSameGenerationUpgradeApplies proves the normal upgrade path still works.
func TestSameGenerationUpgradeApplies(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	alice := activeUser(env, "alice@example.test")
	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{snapshotWith([]contracts.AuthorizedUser{alice}, "alice@example.test")}, 1)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("first revision: %v", err)
	}
	bob := activeUser(env, "bob@example.test")
	env.panel.setSnapshot("gen-1", 2, []contracts.Instance{snapshotWith([]contracts.AuthorizedUser{alice, bob}, "alice@example.test", "bob@example.test")}, 2)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("second revision: %v", err)
	}
	identity, _ := env.store.Identity(ctx)
	if identity.ControlGeneration != "gen-1" || identity.AppliedRevision != 2 {
		t.Fatalf("upgrade did not advance the watermark: %+v", identity)
	}
	if counts := env.runner.clientCounts(); counts[len(counts)-1] != 2 {
		t.Fatalf("upgrade did not authorize the new user: %v", counts)
	}
	reports := env.panel.appliedReports()
	if reports[len(reports)-1].State != applyStateApplied {
		t.Fatalf("unexpected report: %+v", reports[len(reports)-1])
	}
}

// TestUnsupportedCoreVersionPreventsStartup proves an unverified core version is
// refused instead of being assumed compatible.
func TestUnsupportedCoreVersionPreventsStartup(t *testing.T) {
	env := newTestEnv(t)
	env.runner.mu.Lock()
	env.runner.version = "25.9.11"
	env.runner.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := env.sup.Run(ctx)
	if err == nil {
		t.Fatal("an unsupported core version must stop the agent")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("error should explain the unsupported version: %v", err)
	}
	if starts, _ := env.runner.counts(); starts != 0 {
		t.Fatalf("nothing may be started with an unsupported core: starts=%d", starts)
	}
}

func waitAppliedRevision(t *testing.T, env *testEnv, revision int64, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		identity, err := env.store.Identity(context.Background())
		if err == nil && identity.AppliedRevision >= revision {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("revision %d was not applied in time (identity=%+v err=%v)", revision, identity, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRunConcurrentLoopsShutdownBounded runs the real loop set (snapshot, stats,
// upload, heartbeat, expiry) concurrently with instance churn and then shuts the
// agent down. It must finish in bounded time: the production hang happened when
// the snapshot loop held one lock while heartbeat/stats/shutdown held the other.
func TestRunConcurrentLoopsShutdownBounded(t *testing.T) {
	env := newTestEnv(t)
	env.useShortIntervals()
	env.runner.setDelays(15*time.Millisecond, 15*time.Millisecond)
	alice := activeUser(env, "alice@example.test")
	bob := activeUser(env, "bob@example.test")

	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{
		snapshotWith([]contracts.AuthorizedUser{alice}, "alice@example.test"),
	}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- env.sup.Run(ctx) }()

	waitAppliedRevision(t, env, 1, 20*time.Second)
	// Churn the snapshot while every other loop is running.
	for rev := int64(2); rev <= 3; rev++ {
		users := []contracts.AuthorizedUser{alice}
		emails := []string{"alice@example.test"}
		if rev%2 == 0 {
			users = append(users, bob)
			emails = append(emails, "bob@example.test")
		}
		env.panel.setSnapshot("gen-1", rev, []contracts.Instance{snapshotWith(users, emails...)}, len(users))
		waitAppliedRevision(t, env, rev, 20*time.Second)
	}
	// Force the local expiry loop to rebuild while the other loops run.
	env.advance(48 * time.Hour)
	time.Sleep(1500 * time.Millisecond)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not shut down within the bound: lock cycle suspected")
	}
	for _, inst := range env.sup.Manager().Snapshot() {
		if inst.State == xray.StateApplied {
			t.Fatalf("instance still running after shutdown: %+v", inst)
		}
	}
	if _, stops := env.runner.counts(); stops == 0 {
		t.Fatal("no core was stopped during the run")
	}
}

// TestPersistenceFailureDuringApplyDoesNotDeadlock covers the hook reentrancy
// path: the final usage sample is persisted while the manager holds the instance
// lifecycle lock, so a persistence failure must not stop the instances
// synchronously on that same stack.
func TestPersistenceFailureDuringApplyDoesNotDeadlock(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	alice := activeUser(env, "alice@example.test")
	bob := activeUser(env, "bob@example.test")
	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{
		snapshotWith([]contracts.AuthorizedUser{alice}, "alice@example.test"),
	}, 1)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	// Slow down the config test so the store can be broken while the apply holds
	// the instance lifecycle lock, and make the final-sample hook produce data so
	// it really tries to persist.
	env.runner.setDelays(200*time.Millisecond, 0)
	env.runner.mu.Lock()
	env.runner.statsAny = []xray.StatCounter{{Name: "user>>>alice@example.test>>>traffic>>>uplink", Value: 42}}
	env.runner.mu.Unlock()
	env.panel.setSnapshot("gen-1", 2, []contracts.Instance{
		snapshotWith([]contracts.AuthorizedUser{alice, bob}, "alice@example.test", "bob@example.test"),
	}, 2)

	applyDone := make(chan error, 1)
	go func() { applyDone <- env.sup.syncOnce(ctx) }()
	time.Sleep(60 * time.Millisecond)
	// Simulate a local persistence failure (disk/database error) from now on.
	if err := env.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	select {
	case <-applyDone:
	case <-time.After(25 * time.Second):
		t.Fatal("the apply dead-locked after a persistence failure inside the sample hook")
	}
	// The failure must still stop the affected instances, just not on the
	// applier stack.
	if !waitForStops(env.runner, 1, 15*time.Second) {
		t.Fatalf("the persistence failure did not stop the instances: stops=%d", stopCount(env.runner))
	}
}

// nodeInstanceNamed builds a snapshot instance for an explicit node id.
func nodeInstanceNamed(id string, users []contracts.AuthorizedUser, emails ...string) contracts.Instance {
	inst := nodeInstance(users, emails...)
	inst.ID = id
	inst.NodeID = id
	return inst
}

// instanceArtifacts are the externally observable identity of one running core:
// its pid, its core generation and the sha256 of its effective configuration.
type instanceArtifacts struct {
	PID            int    `json:"pid"`
	CoreGeneration string `json:"core_generation"`
	ConfigSHA256   string `json:"-"`
}

func readInstanceArtifacts(t *testing.T, env *testEnv, nodeID string) (instanceArtifacts, error) {
	t.Helper()
	dir := filepath.Join(env.sup.cfg.StateDir, "instances", nodeID)
	raw, err := os.ReadFile(filepath.Join(dir, "xray.pid"))
	if err != nil {
		return instanceArtifacts{}, fmt.Errorf("read pid record of %s: %w", nodeID, err)
	}
	var art instanceArtifacts
	if err := json.Unmarshal(raw, &art); err != nil {
		return instanceArtifacts{}, fmt.Errorf("decode pid record of %s: %w", nodeID, err)
	}
	cfg, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return instanceArtifacts{}, fmt.Errorf("read effective config of %s: %w", nodeID, err)
	}
	sum := sha256.Sum256(cfg)
	art.ConfigSHA256 = hex.EncodeToString(sum[:])
	if art.PID == 0 || art.CoreGeneration == "" {
		return art, fmt.Errorf("incomplete pid record of %s: %+v", nodeID, art)
	}
	return art, nil
}

func mustRecordUsers(t *testing.T, env *testEnv) map[string][]contracts.AuthorizedUser {
	t.Helper()
	records, err := env.store.Instances(context.Background())
	if err != nil {
		t.Fatalf("read instance records: %v", err)
	}
	out := map[string][]contracts.AuthorizedUser{}
	for _, rec := range records {
		out[rec.NodeID] = rec.Users
	}
	return out
}

func hasUser(users []contracts.AuthorizedUser, email string) bool {
	for _, u := range users {
		if u.Email == email {
			return true
		}
	}
	return false
}

// TestExpiryRebuildDoesNotResurrectUserRevokedBySnapshot pins the serialization
// between a local expiry rebuild and a snapshot apply. The expiry pass reads its
// records, then a snapshot revokes a member and commits the new revision. If the
// expiry rebuild were allowed to run with its stale target set afterwards it would
// re-apply the old authorization, and because the revision is already committed the
// panel would answer 204 forever: the revoked member would stay authorized.
func TestExpiryRebuildDoesNotResurrectUserRevokedBySnapshot(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	expiry := env.now.Add(time.Minute)
	alice := contracts.AuthorizedUser{MemberID: "member-alice", Email: "alice@example.test", ExpiresAt: &expiry}
	bob := contracts.AuthorizedUser{MemberID: "member-bob", Email: "bob@example.test", ExpiresAt: ptrTime(env.now.Add(24 * time.Hour))}
	carol := contracts.AuthorizedUser{MemberID: "member-carol", Email: "carol@example.test", ExpiresAt: ptrTime(env.now.Add(24 * time.Hour))}

	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{
		nodeInstanceNamed("node-1", []contracts.AuthorizedUser{alice, bob}, "alice@example.test", "bob@example.test"),
		nodeInstanceNamed("node-2", []contracts.AuthorizedUser{carol}, "carol@example.test"),
	}, 3)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	env.advance(2 * time.Minute) // alice expires locally

	readDone := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	env.sup.expiryAfterReadHook = func() {
		once.Do(func() {
			close(readDone)
			<-release
		})
	}
	expiryErr := make(chan error, 1)
	go func() { expiryErr <- env.sup.expiryOnce(ctx) }()
	select {
	case <-readDone:
	case <-time.After(15 * time.Second):
		t.Fatal("the expiry rebuild never read its records")
	}

	// Revoke bob (higher revision, same generation) while the expiry rebuild is
	// between its record read and its apply.
	env.panel.setSnapshot("gen-1", 2, []contracts.Instance{
		nodeInstanceNamed("node-1", []contracts.AuthorizedUser{alice}, "alice@example.test", "bob@example.test"),
		nodeInstanceNamed("node-2", []contracts.AuthorizedUser{carol}, "carol@example.test"),
	}, 2)
	snapshotErr := make(chan error, 1)
	go func() { snapshotErr <- env.sup.syncOnce(ctx) }()
	select {
	case err := <-snapshotErr:
		t.Fatalf("the snapshot apply committed while a local expiry rebuild held the serialization lock (err=%v)", err)
	case <-time.After(400 * time.Millisecond):
	}

	close(release)
	if err := <-expiryErr; err != nil {
		t.Fatalf("expiryOnce: %v", err)
	}
	if err := <-snapshotErr; err != nil {
		t.Fatalf("syncOnce: %v", err)
	}

	assertBobRevoked := func(stage string) {
		t.Helper()
		users := mustRecordUsers(t, env)
		if hasUser(users["node-1"], "bob@example.test") {
			t.Fatalf("%s: bob is authorized again in the committed records: %+v", stage, users["node-1"])
		}
		if cfg := env.runner.lastStartedFor("node-1"); strings.Contains(cfg, "bob@example.test") {
			t.Fatalf("%s: the last configuration started for node-1 still authorizes bob", stage)
		}
	}
	assertBobRevoked("after the serialized applies")
	identity, err := env.store.Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if identity.AppliedRevision != 2 {
		t.Fatalf("applied revision = %d, want 2", identity.AppliedRevision)
	}
	// The panel now answers 204 for revision 2: another cycle must not revive bob.
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("follow-up sync: %v", err)
	}
	assertBobRevoked("after the 204 follow-up")
	// node-2 was not part of the revocation and must still be running.
	if _, stops := env.runner.countsFor("node-2"); stops != 0 {
		t.Fatalf("node-2 was stopped by the revocation of bob: stops=%d", stops)
	}
}

// TestExpiryRebuildLeavesOtherNodesRunning proves that a local expiry rebuild only
// reconciles the expired instance: every other node must keep its pid, its core
// generation and its configuration, and must not be stopped.
func TestExpiryRebuildLeavesOtherNodesRunning(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	expiry := env.now.Add(time.Minute)
	alice := contracts.AuthorizedUser{MemberID: "member-alice", Email: "alice@example.test", ExpiresAt: &expiry}
	bob := contracts.AuthorizedUser{MemberID: "member-bob", Email: "bob@example.test", ExpiresAt: ptrTime(env.now.Add(24 * time.Hour))}
	carol := contracts.AuthorizedUser{MemberID: "member-carol", Email: "carol@example.test", ExpiresAt: ptrTime(env.now.Add(24 * time.Hour))}

	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{
		nodeInstanceNamed("node-1", []contracts.AuthorizedUser{alice, bob}, "alice@example.test", "bob@example.test"),
		nodeInstanceNamed("node-2", []contracts.AuthorizedUser{carol}, "carol@example.test"),
	}, 3)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	before1, err := readInstanceArtifacts(t, env, "node-1")
	if err != nil {
		t.Fatalf("node-1 artifacts: %v", err)
	}
	before2, err := readInstanceArtifacts(t, env, "node-2")
	if err != nil {
		t.Fatalf("node-2 artifacts: %v", err)
	}

	env.advance(2 * time.Minute) // alice expires locally
	if err := env.sup.expiryOnce(ctx); err != nil {
		t.Fatalf("expiryOnce: %v", err)
	}

	after1, err := readInstanceArtifacts(t, env, "node-1")
	if err != nil {
		t.Fatalf("node-1 artifacts after the rebuild: %v", err)
	}
	after2, err := readInstanceArtifacts(t, env, "node-2")
	if err != nil {
		t.Fatalf("node-2 artifacts after the rebuild: %v", err)
	}
	if after2 != before2 {
		t.Fatalf("node-2 was restarted or reconfigured by a local expiry rebuild of node-1:\n before=%+v\n after =%+v", before2, after2)
	}
	if starts, stops := env.runner.countsFor("node-2"); starts != 1 || stops != 0 {
		t.Fatalf("node-2 lifecycle changed: starts=%d stops=%d", starts, stops)
	}
	if after1 == before1 {
		t.Fatal("node-1 was not rebuilt although its user expired")
	}
	if starts, stops := env.runner.countsFor("node-1"); starts != 2 || stops != 1 {
		t.Fatalf("node-1 was not rebuilt exactly once: starts=%d stops=%d", starts, stops)
	}
	if cfg := env.runner.lastStartedFor("node-1"); strings.Contains(cfg, "alice@example.test") {
		t.Fatal("the expired user is still authorized after the local rebuild")
	}
	users := mustRecordUsers(t, env)
	if !hasUser(users["node-1"], "bob@example.test") || hasUser(users["node-1"], "alice@example.test") {
		t.Fatalf("node-1 authorization after expiry = %+v", users["node-1"])
	}
	if !hasUser(users["node-2"], "carol@example.test") {
		t.Fatalf("node-2 authorization was changed: %+v", users["node-2"])
	}
	for _, inst := range env.sup.Manager().Snapshot() {
		if inst.State != xray.StateApplied {
			t.Fatalf("instance %s is no longer applied after the expiry rebuild: %+v", inst.InstanceID, inst)
		}
	}
	// A second pass must be a no-op (the refreshed expiry moved into the future).
	if err := env.sup.expiryOnce(ctx); err != nil {
		t.Fatalf("second expiryOnce: %v", err)
	}
	if starts, _ := env.runner.countsFor("node-1"); starts != 2 {
		t.Fatalf("the expiry rebuild repeated although nothing expired again: starts=%d", starts)
	}
}

// TestRunSerializesExpiryAndSnapshot exercises the same race through the real Run
// loops (snapshot loop + expiry loop) instead of calling the steps directly.
func TestRunSerializesExpiryAndSnapshot(t *testing.T) {
	env := newTestEnv(t)
	env.useShortIntervals()
	expiry := env.now.Add(30 * time.Second)
	alice := contracts.AuthorizedUser{MemberID: "member-alice", Email: "alice@example.test", ExpiresAt: &expiry}
	bob := contracts.AuthorizedUser{MemberID: "member-bob", Email: "bob@example.test", ExpiresAt: ptrTime(env.now.Add(24 * time.Hour))}
	carol := contracts.AuthorizedUser{MemberID: "member-carol", Email: "carol@example.test", ExpiresAt: ptrTime(env.now.Add(24 * time.Hour))}

	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{
		nodeInstanceNamed("node-1", []contracts.AuthorizedUser{alice, bob}, "alice@example.test", "bob@example.test"),
		nodeInstanceNamed("node-2", []contracts.AuthorizedUser{carol}, "carol@example.test"),
	}, 3)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- env.sup.Run(ctx) }()
	waitAppliedRevision(t, env, 1, 20*time.Second)
	before2, err := readInstanceArtifacts(t, env, "node-2")
	if err != nil {
		t.Fatalf("node-2 artifacts: %v", err)
	}

	// Expire alice and revoke bob almost simultaneously so the expiry loop and the
	// snapshot loop really race.
	env.advance(2 * time.Minute)
	env.panel.setSnapshot("gen-1", 2, []contracts.Instance{
		nodeInstanceNamed("node-1", []contracts.AuthorizedUser{alice}, "alice@example.test", "bob@example.test"),
		nodeInstanceNamed("node-2", []contracts.AuthorizedUser{carol}, "carol@example.test"),
	}, 2)
	waitAppliedRevision(t, env, 2, 20*time.Second)
	// Let the expiry loop run several times after the revocation was committed.
	time.Sleep(2500 * time.Millisecond)

	users := mustRecordUsers(t, env)
	if hasUser(users["node-1"], "bob@example.test") {
		t.Fatalf("bob was revived by the expiry loop after the revocation was committed: %+v", users["node-1"])
	}
	if cfg := env.runner.lastStartedFor("node-1"); strings.Contains(cfg, "bob@example.test") {
		t.Fatal("the running configuration of node-1 authorizes bob again")
	}
	after2, err := readInstanceArtifacts(t, env, "node-2")
	if err != nil {
		t.Fatalf("node-2 artifacts after the race: %v", err)
	}
	if after2 != before2 {
		t.Fatalf("node-2 was restarted by an unrelated expiry rebuild:\n before=%+v\n after =%+v", before2, after2)
	}
	if starts, stops := env.runner.countsFor("node-2"); starts != 1 || stops != 0 {
		t.Fatalf("node-2 lifecycle changed during the race: starts=%d stops=%d", starts, stops)
	}
	identity, err := env.store.Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if identity.AppliedRevision != 2 {
		t.Fatalf("applied revision = %d, want 2", identity.AppliedRevision)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not shut down within the bound")
	}
}
