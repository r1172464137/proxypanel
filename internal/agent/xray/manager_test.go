package xray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"proxypanel/internal/contracts"
)

type fakeProc struct {
	runner *fakeRunner
	pid    int
	exited chan struct{}
	once   sync.Once
}

func (p *fakeProc) PID() int                { return p.pid }
func (p *fakeProc) Exited() <-chan struct{} { return p.exited }
func (p *fakeProc) ExitError() error        { return nil }
func (p *fakeProc) Output() []string        { return nil }
func (p *fakeProc) Stop(ctx context.Context) error {
	p.once.Do(func() {
		close(p.exited)
		p.runner.record("stop", p.pid)
	})
	return nil
}

type fakeRunner struct {
	mu        sync.Mutex
	events    []string
	configs   map[string]string
	testErrs  []error
	startErrs []error
	started   []int
	stats     []StatCounter
	nextPID   int
	writeErr  error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{configs: map[string]string{}, nextPID: 100}
}

func (f *fakeRunner) record(kind string, pid int) {
	f.events = append(f.events, fmt.Sprintf("%s:%d", kind, pid))
}

func (f *fakeRunner) Test(ctx context.Context, configPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "test:"+filepath.Base(configPath))
	if raw, err := os.ReadFile(configPath); err == nil {
		f.configs[filepath.Base(configPath)] = string(raw)
	}
	if len(f.testErrs) > 0 {
		err := f.testErrs[0]
		f.testErrs = f.testErrs[1:]
		return err
	}
	return nil
}

func (f *fakeRunner) Start(ctx context.Context, spec StartSpec) (Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.startErrs) > 0 {
		err := f.startErrs[0]
		f.startErrs = f.startErrs[1:]
		return nil, err
	}
	f.nextPID++
	pid := f.nextPID
	f.events = append(f.events, "start:"+filepath.Base(spec.ConfigPath))
	if raw, err := os.ReadFile(spec.ConfigPath); err == nil {
		f.configs[filepath.Base(spec.ConfigPath)] = string(raw)
	}
	f.started = append(f.started, pid)
	return &fakeProc{runner: f, pid: pid, exited: make(chan struct{})}, nil
}

func (f *fakeRunner) StatsQuery(ctx context.Context, server string) ([]StatCounter, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stats, nil
}

func (f *fakeRunner) Version(ctx context.Context) (string, error) { return "26.3.27", nil }

func (f *fakeRunner) eventList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.events))
	copy(out, f.events)
	return out
}

func (f *fakeRunner) startedConfigs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.events {
		if strings.HasPrefix(e, "start:") {
			if raw, ok := f.configs[strings.TrimPrefix(e, "start:")]; ok {
				out = append(out, raw)
			}
		}
	}
	return out
}

func (f *fakeRunner) startedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.started)
}

func newTestManager(t *testing.T) (*Manager, *fakeRunner) {
	t.Helper()
	runner := newFakeRunner()
	mgr, err := NewManager(t.TempDir(), filepath.Join(t.TempDir(), "xray.exe"), runner, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	port := 21000
	mgr.SetPortAllocator(func(reserved map[int]bool) (int, error) {
		port++
		return port, nil
	})
	return mgr, runner
}

func targetFor(t *testing.T, emails ...string) Target {
	t.Helper()
	users := make([]contracts.AuthorizedUser, 0, len(emails))
	for i, e := range emails {
		users = append(users, contracts.AuthorizedUser{MemberID: fmt.Sprintf("m%d", i+1), Email: e})
	}
	inst := contracts.Instance{ID: "node-1", NodeID: "node-1", Config: json.RawMessage(baseDocument(443, emails...)), Users: users}
	plan, err := Prepare(inst, Options{CertRoot: t.TempDir(), Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
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

func TestApplyStartsAndSkipsUnchanged(t *testing.T) {
	mgr, runner := newTestManager(t)
	ctx := context.Background()
	target := targetFor(t, "alice@example.test")
	results := mgr.Apply(ctx, []Target{target}, ApplyOptions{})
	if len(results) != 1 || results[0].State != StateApplied {
		t.Fatalf("unexpected results: %+v", results)
	}
	if !results[0].OldConnectionsTerminated {
		t.Fatal("a fresh start must report terminated old connections")
	}
	if runner.startedCount() != 1 {
		t.Fatalf("started %d times", runner.startedCount())
	}
	results = mgr.Apply(ctx, []Target{target}, ApplyOptions{})
	if len(results) != 1 || results[0].State != StateUnchanged {
		t.Fatalf("unchanged instance must not restart: %+v", results)
	}
	if runner.startedCount() != 1 {
		t.Fatalf("unchanged instance restarted: %d", runner.startedCount())
	}
}

// Future expiry changes do not alter the effective Xray document or its hash.
// The unchanged path must still commit all users, even if NextExpiry is equal.
func TestApplyUnchangedRefreshesUserExpiries(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	early, middle, late := now.Add(time.Minute), now.Add(2*time.Minute), now.Add(3*time.Minute)
	for _, tc := range []struct {
		name         string
		old, updated *time.Time
		otherExpiry  *time.Time
	}{
		{name: "add", updated: &middle},
		{name: "shorten", old: &late, updated: &middle},
		{name: "extend", old: &early, updated: &middle},
		{name: "remove", old: &early},
		{name: "non-earliest", old: &late, updated: &middle, otherExpiry: &early},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, runner := newTestManager(t)
			makeTarget := func(expiry *time.Time) Target {
				plan, err := Prepare(contracts.Instance{
					ID: "node-1", NodeID: "node-1",
					Config: json.RawMessage(baseDocument(443, "alice@example.test", "bob@example.test")),
					Users: []contracts.AuthorizedUser{
						{MemberID: "m1", Email: "alice@example.test", ExpiresAt: expiry},
						{MemberID: "m2", Email: "bob@example.test", ExpiresAt: tc.otherExpiry},
					},
				}, Options{CertRoot: mgr.CertRoot(), Now: now})
				if err != nil {
					t.Fatal(err)
				}
				return Target{InstanceID: plan.InstanceID, NodeID: plan.NodeID, Document: plan.Document, Hash: plan.Hash,
					Authorization: plan.Authorization, EmailToMember: plan.EmailToMember, Users: plan.EffectiveUsers, NextExpiry: plan.NextExpiry}
			}
			initial, updated := makeTarget(tc.old), makeTarget(tc.updated)
			first, ok := applyOneHelper(t, mgr, initial)
			if !ok {
				t.Fatal("initial apply failed")
			}
			if initial.Hash != updated.Hash {
				t.Fatal("future expiry alone must not change the effective config hash")
			}
			results := mgr.Apply(context.Background(), []Target{updated}, ApplyOptions{})
			if len(results) != 1 || results[0].State != StateUnchanged {
				t.Fatalf("expected unchanged: %+v", results)
			}
			res := results[0]
			if !reflect.DeepEqual(res.Users, updated.Users) {
				t.Errorf("unchanged result retained stale user expiries: got %+v, want %+v", res.Users, updated.Users)
			}
			view := mgr.lookup(updated.InstanceID).view()
			if !reflect.DeepEqual(view.usersList, updated.Users) {
				t.Errorf("published users retained stale expiries: got %+v, want %+v", view.usersList, updated.Users)
			}
			if !res.NextExpiry.Equal(updated.NextExpiry) || !mgr.NextExpiry().Equal(updated.NextExpiry) {
				t.Fatalf("next expiry = %v / %v, want %v", res.NextExpiry, mgr.NextExpiry(), updated.NextExpiry)
			}
			if runner.startedCount() != 1 || res.CoreGeneration != first.CoreGeneration || res.OldConnectionsTerminated {
				t.Fatalf("metadata-only change restarted the core: %+v", res)
			}
		})
	}
}

func TestApplyRestartsChangedInstanceAfterValidation(t *testing.T) {
	mgr, runner := newTestManager(t)
	ctx := context.Background()
	if _, ok := applyOneHelper(t, mgr, targetFor(t, "alice@example.test")); !ok {
		t.Fatal("initial apply failed")
	}
	runner.mu.Lock()
	runner.events = nil
	runner.mu.Unlock()
	changed := targetFor(t, "alice@example.test", "bob@example.test")
	results := mgr.Apply(ctx, []Target{changed}, ApplyOptions{})
	if results[0].State != StateApplied {
		t.Fatalf("expected applied, got %+v", results[0])
	}
	events := runner.eventList()
	// Validate first, then stop, then start.
	var testIdx, stopIdx, startIdx = -1, -1, -1
	for i, e := range events {
		switch {
		case strings.HasPrefix(e, "test:") && testIdx < 0:
			testIdx = i
		case strings.HasPrefix(e, "stop:") && stopIdx < 0:
			stopIdx = i
		case strings.HasPrefix(e, "start:") && startIdx < 0:
			startIdx = i
		}
	}
	if testIdx < 0 || stopIdx < 0 || startIdx < 0 {
		t.Fatalf("missing lifecycle events: %v", events)
	}
	if !(testIdx < stopIdx && stopIdx < startIdx) {
		t.Fatalf("expected test -> stop -> start, got %v", events)
	}
}

func TestApplyStopsInstancesRemovedFromSnapshot(t *testing.T) {
	mgr, runner := newTestManager(t)
	ctx := context.Background()
	if _, ok := applyOneHelper(t, mgr, targetFor(t, "alice@example.test")); !ok {
		t.Fatal("initial apply failed")
	}
	results := mgr.Apply(ctx, nil, ApplyOptions{})
	if len(results) != 1 || results[0].State != StateStopped {
		t.Fatalf("empty snapshot must stop everything: %+v", results)
	}
	if !results[0].OldConnectionsTerminated {
		t.Fatal("stopping must report terminated connections")
	}
	if got := mgr.Snapshot(); len(got) != 0 {
		t.Fatalf("instance still tracked: %+v", got)
	}
	events := strings.Join(runner.eventList(), ",")
	if !strings.Contains(events, "stop:") {
		t.Fatalf("no stop event: %v", events)
	}
}

func TestApplyRollbackKeepsOnlyCurrentAuthorization(t *testing.T) {
	mgr, runner := newTestManager(t)
	ctx := context.Background()
	if _, ok := applyOneHelper(t, mgr, targetFor(t, "alice@example.test", "bob@example.test")); !ok {
		t.Fatal("initial apply failed")
	}
	// The new document is rejected by the core; the rollback must drop bob.
	runner.mu.Lock()
	runner.testErrs = []error{errors.New("xray -test: unknown field")}
	runner.mu.Unlock()
	revoked := targetFor(t, "alice@example.test")
	results := mgr.Apply(ctx, []Target{revoked}, ApplyOptions{})
	if results[0].State != StateRollback {
		t.Fatalf("expected rollback_applied, got %+v", results[0])
	}
	if results[0].AppliedUserCount != 1 {
		t.Fatalf("rollback applied %d users, want 1", results[0].AppliedUserCount)
	}
	if !strings.Contains(results[0].Error, "config validation failed") {
		t.Fatalf("rollback must report the original failure: %+v", results[0])
	}
	configs := runner.startedConfigs()
	if len(configs) == 0 {
		t.Fatal("rollback did not start a process")
	}
	last := configs[len(configs)-1]
	var doc map[string]any
	if err := json.Unmarshal([]byte(last), &doc); err != nil {
		t.Fatalf("decode rollback config: %v", err)
	}
	inbounds := doc["inbounds"].([]any)
	clients := inbounds[0].(map[string]any)["settings"].(map[string]any)["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("rollback config still authorizes %d clients: %s", len(clients), last)
	}
	email := clients[0].(map[string]any)["email"].(string)
	if email != "alice@example.test" {
		t.Fatalf("rollback kept the wrong user: %s", email)
	}
}

func TestApplyFailureStopsOldProcessWhenRollbackImpossible(t *testing.T) {
	mgr, runner := newTestManager(t)
	ctx := context.Background()
	if _, ok := applyOneHelper(t, mgr, targetFor(t, "alice@example.test")); !ok {
		t.Fatal("initial apply failed")
	}
	runner.mu.Lock()
	runner.testErrs = []error{errors.New("rejected"), errors.New("rejected again")}
	runner.mu.Unlock()
	changed := targetFor(t, "alice@example.test", "carol@example.test")
	results := mgr.Apply(ctx, []Target{changed}, ApplyOptions{})
	if results[0].State != StateFailed {
		t.Fatalf("expected failed, got %+v", results[0])
	}
	events := strings.Join(runner.eventList(), ",")
	if !strings.Contains(events, "stop:") {
		t.Fatalf("the old process must be stopped: %v", events)
	}
	if got := mgr.Snapshot(); len(got) != 1 || got[0].State != StateFailed {
		t.Fatalf("instance state = %+v", got)
	}
}

func TestApplyForceRestartsUnchangedInstance(t *testing.T) {
	mgr, runner := newTestManager(t)
	ctx := context.Background()
	target := targetFor(t, "alice@example.test")
	if _, ok := applyOneHelper(t, mgr, target); !ok {
		t.Fatal("initial apply failed")
	}
	results := mgr.Apply(ctx, []Target{target}, ApplyOptions{Force: true})
	if results[0].State != StateApplied || runner.startedCount() != 2 {
		t.Fatalf("force apply did not restart: %+v (starts=%d)", results[0], runner.startedCount())
	}
}

func TestApplySkipReturnsDeferred(t *testing.T) {
	mgr, runner := newTestManager(t)
	results := mgr.Apply(context.Background(), []Target{targetFor(t, "alice@example.test")}, ApplyOptions{Skip: map[string]bool{"node-1": true}})
	if len(results) != 1 || results[0].State != StateDeferred {
		t.Fatalf("unexpected results: %+v", results)
	}
	if runner.startedCount() != 0 {
		t.Fatal("a deferred instance must not be started")
	}
}

func TestApplyRejectsUnsafeInstanceID(t *testing.T) {
	mgr, _ := newTestManager(t)
	results := mgr.Apply(context.Background(), []Target{{InstanceID: "../escape", NodeID: "../escape"}}, ApplyOptions{})
	if len(results) != 1 || results[0].State != StateFailed {
		t.Fatalf("unsafe id must fail: %+v", results)
	}
	if _, err := os.Stat(filepath.Join(mgr.Root(), "escape")); err == nil {
		t.Fatal("path traversal created a directory outside the instance root")
	}
}

func TestQueryStatsReturnsEmailMapping(t *testing.T) {
	mgr, runner := newTestManager(t)
	ctx := context.Background()
	target := targetFor(t, "alice@example.test")
	if _, ok := applyOneHelper(t, mgr, target); !ok {
		t.Fatal("initial apply failed")
	}
	runner.mu.Lock()
	runner.stats = []StatCounter{{Name: "user>>>alice@example.test>>>traffic>>>uplink", Value: 42}}
	runner.mu.Unlock()
	stats := mgr.QueryStats(ctx)
	if len(stats) != 1 {
		t.Fatalf("expected 1 instance stat, got %d", len(stats))
	}
	if stats[0].EmailToMember["alice@example.test"] != "m1" {
		t.Fatalf("email mapping missing: %+v", stats[0])
	}
	if len(stats[0].Counters) != 1 || stats[0].Counters[0].Value != 42 {
		t.Fatalf("unexpected counters: %+v", stats[0].Counters)
	}
	if stats[0].CoreGeneration == "" {
		t.Fatal("core generation missing")
	}
}

func TestNextExpiryAndExpiredInstances(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	now := time.Now().UTC()
	expiry := now.Add(2 * time.Second)
	inst := contracts.Instance{
		ID: "node-1", NodeID: "node-1", Config: json.RawMessage(baseDocument(443, "alice@example.test")),
		Users: []contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test", ExpiresAt: &expiry}},
	}
	plan, err := Prepare(inst, Options{CertRoot: t.TempDir(), Now: now})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	target := Target{InstanceID: "node-1", NodeID: "node-1", Document: plan.Document, Hash: plan.Hash,
		Authorization: plan.Authorization, EmailToMember: plan.EmailToMember, Users: plan.EffectiveUsers, NextExpiry: plan.NextExpiry}
	if results := mgr.Apply(ctx, []Target{target}, ApplyOptions{}); results[0].State != StateApplied {
		t.Fatalf("apply failed: %+v", results[0])
	}
	if got := mgr.NextExpiry(); !got.Equal(expiry) {
		t.Fatalf("NextExpiry = %v, want %v", got, expiry)
	}
	if got := mgr.ExpiredInstances(now); len(got) != 0 {
		t.Fatalf("nothing should be expired yet: %v", got)
	}
	if got := mgr.ExpiredInstances(expiry.Add(time.Second)); len(got) != 1 || got[0] != "node-1" {
		t.Fatalf("expired instance not reported: %v", got)
	}
}

func applyOneHelper(t *testing.T, mgr *Manager, target Target) (Result, bool) {
	t.Helper()
	results := mgr.Apply(context.Background(), []Target{target}, ApplyOptions{})
	if len(results) != 1 {
		return Result{}, false
	}
	switch results[0].State {
	case StateApplied, StateUnchanged:
		return results[0], true
	}
	return results[0], false
}
