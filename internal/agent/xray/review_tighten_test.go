package xray

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"proxypanel/internal/contracts"
)

// authorizationDeadline is the contract bound: a delivered revocation must be
// enforced well inside 60 seconds. The tests below assert a much tighter bound so
// a regression is caught long before the contract is violated.
const authorizationDeadline = 10 * time.Second

// blockedValidationRunner models `xray -test` being serialized process wide and
// one run stalling: only ONE test can be in flight, and the first one blocks until
// the test releases it. That is exactly the condition under which the sequential
// Apply let a slow validation of node A delay the revocation of node B.
type blockedValidationRunner struct {
	*fakeRunner

	// blockedID is the instance whose validation blocks.
	blockedID string
	// hold is closed by the test to let the blocked validation continue.
	hold     chan struct{}
	holdOnce sync.Once

	inFlight  atomic.Int32
	testCalls atomic.Int32
	// order records when each instance's validation actually started.
	mu    sync.Mutex
	order []string
	// blockedTestEnd is the wall clock time (unix nano) at which the blocked
	// validation returned, i.e. the moment the "slow node" was released.
	blockedTestEnd atomic.Int64
}

func newBlockedValidationRunner() *blockedValidationRunner {
	return &blockedValidationRunner{
		fakeRunner: newFakeRunner(),
		hold:       make(chan struct{}),
	}
}

func (r *blockedValidationRunner) releaseBlocked() { r.holdOnce.Do(func() { close(r.hold) }) }

func (r *blockedValidationRunner) Test(ctx context.Context, configPath string) error {
	r.inFlight.Add(1)
	defer r.inFlight.Add(-1)
	r.testCalls.Add(1)

	// The runner is shared by both instances; the blocked one is identified by the
	// config that the manager wrote for it (the test sets blockedID after the first
	// apply), so the simplest deterministic model is: the FIRST test of the round
	// blocks when blocking is enabled.
	r.mu.Lock()
	block := r.blockedID != ""
	id := r.blockedID
	r.order = append(r.order, id)
	r.mu.Unlock()
	if block {
		select {
		case <-r.hold:
		case <-ctx.Done():
		}
		// Record when the slow validation of node A really ended, whatever the
		// reason (release or budget exhaustion).
		r.blockedTestEnd.CompareAndSwap(0, time.Now().UnixNano())
	}
	return nil
}

func (r *blockedValidationRunner) Start(ctx context.Context, spec StartSpec) (Proc, error) {
	return r.fakeRunner.Start(ctx, spec)
}

// targetForNode builds a target for the given instance id.
func targetForNode(t *testing.T, id string, emails ...string) Target {
	t.Helper()
	return targetForID(t, id, id, emails...)
}

// newTwoNodeBlockedManager builds a manager with two running instances.
func newTwoNodeBlockedManager(t *testing.T) (*Manager, *blockedValidationRunner) {
	t.Helper()
	runner := newBlockedValidationRunner()
	mgr := newManagerWithRunner(t, runner)
	return mgr, runner
}

// TestReviewSlowValidationOfAnotherNodeDoesNotDelayRevocation is the decisive
// C03 regression: node A's `xray -test` blocks for the whole validation budget
// while node B loses one of its two users. B's old core must be stopped inside the
// authorization deadline, i.e. BEFORE A's validation finishes - not after it.
func TestReviewSlowValidationOfAnotherNodeDoesNotDelayRevocation(t *testing.T) {
	mgr, runner := newTwoNodeBlockedManager(t)
	ctx := context.Background()

	a := targetForNode(t, "node-a", "a1@example.test")
	b := targetForNode(t, "node-b", "b1@example.test", "b2@example.test")
	if results := mgr.Apply(ctx, []Target{a, b}, ApplyOptions{}); len(results) != 2 {
		t.Fatalf("initial apply: %+v", results)
	}
	startedB := mgr.lookup("node-b")
	if startedB == nil || !startedB.view().running {
		t.Fatal("node-b is not running after the initial apply")
	}

	// A technical change on node A whose validation blocks; node B revokes b2.
	mgr.SetTimeouts(Timeouts{
		Readiness: 5 * time.Second,
		Validate:  3 * time.Second, // the blocked validation budget
		Stop:      5 * time.Second,
		Rollback:  5 * time.Second,
	})
	runner.mu.Lock()
	runner.blockedID = "node-a"
	runner.mu.Unlock()
	t.Cleanup(runner.releaseBlocked)

	aChanged := targetForNode(t, "node-a", "a1@example.test")
	aChanged = targetWithPort(t, aChanged, 8443)
	bRevoked := targetForNode(t, "node-b", "b1@example.test")

	done := make(chan []Result, 1)
	go func() {
		done <- mgr.Apply(ctx, []Target{aChanged, bRevoked}, ApplyOptions{PerInstanceTimeout: 20 * time.Second})
	}()

	// node B must lose its second user while node A is still validating.
	deadline := time.Now().Add(authorizationDeadline)
	var revokedAt time.Time
	for {
		view := mgr.lookup("node-b").view()
		if view.users <= 1 && !hasEmail(view.emailToMember, "b2@example.test") {
			revokedAt = time.Now()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("node-b kept the revoked user for %s while another node's validation was running: %+v",
				authorizationDeadline, view.emailToMember)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Ordering assertion: the revocation must be enforced BEFORE node A's slow
	// validation ends (with a sequential reconcile it can only happen after).
	if end := runner.blockedTestEnd.Load(); end != 0 && revokedAt.UnixNano() > end {
		t.Fatalf("node-b was revoked only after the slow validation of node-a finished (revoked=%s validation ended=%s)",
			revokedAt.Format(time.RFC3339Nano), time.Unix(0, end).Format(time.RFC3339Nano))
	}
	select {
	case <-done:
		t.Fatal("the apply finished before the blocked validation was released: the assertion is not deterministic")
	default:
	}
	runner.releaseBlocked()
	results := <-done
	if len(results) != 2 {
		t.Fatalf("unexpected results: %+v", results)
	}
	// node B must end up applied with exactly the narrowed authorization.
	for _, res := range results {
		if res.InstanceID != "node-b" {
			continue
		}
		if res.State != StateApplied {
			t.Fatalf("node-b was not re-applied with the narrowed authorization: %+v", res)
		}
		if res.AppliedUserCount != 1 {
			t.Fatalf("node-b applied %d user(s), want 1", res.AppliedUserCount)
		}
	}
	if hasEmail(mgr.lookup("node-b").view().emailToMember, "b2@example.test") {
		t.Fatal("node-b still authorizes the revoked user")
	}
}

// TestReviewRemovedNodeIsStoppedBeforeOtherValidation is the deletion variant:
// a node that leaves the snapshot must be stopped before another node's slow
// validation runs.
func TestReviewRemovedNodeIsStoppedBeforeOtherValidation(t *testing.T) {
	mgr, runner := newTwoNodeBlockedManager(t)
	ctx := context.Background()
	a := targetForNode(t, "node-a", "a1@example.test")
	b := targetForNode(t, "node-b", "b1@example.test")
	if results := mgr.Apply(ctx, []Target{a, b}, ApplyOptions{}); len(results) != 2 {
		t.Fatalf("initial apply: %+v", results)
	}
	mgr.SetTimeouts(Timeouts{Readiness: 5 * time.Second, Validate: 3 * time.Second, Stop: 5 * time.Second, Rollback: 5 * time.Second})
	runner.mu.Lock()
	runner.blockedID = "node-a"
	runner.mu.Unlock()
	t.Cleanup(runner.releaseBlocked)

	aChanged := targetWithPort(t, targetForNode(t, "node-a", "a1@example.test"), 8443)
	done := make(chan []Result, 1)
	go func() { done <- mgr.Apply(ctx, []Target{aChanged}, ApplyOptions{PerInstanceTimeout: 20 * time.Second}) }()

	deadline := time.Now().Add(authorizationDeadline)
	for {
		if mgr.lookup("node-b") == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the removed node kept running while another node's validation was blocked")
		}
		time.Sleep(10 * time.Millisecond)
	}
	runner.releaseBlocked()
	results := <-done
	for _, res := range results {
		if res.InstanceID == "node-b" {
			if res.State != StateStopped || !res.OldConnectionsTerminated {
				t.Fatalf("the removed node was not stopped: %+v", res)
			}
		}
	}
}

// TestReviewRepeatedValidationTimeoutsStillRevoke pins the "single node keeps
// timing out" case: repeated timeouts must never keep a revoked user authorized.
func TestReviewRepeatedValidationTimeoutsStillRevoke(t *testing.T) {
	mgr, runner := newValidateProbeManager(t)
	ctx := context.Background()
	first := validateTarget(t, "node-1", 443, "a@example.test", "b@example.test")
	if res := mgr.Apply(ctx, []Target{first}, ApplyOptions{}); res[0].State != StateApplied {
		t.Fatalf("initial apply: %+v", res[0])
	}
	mgr.SetTimeouts(Timeouts{Readiness: 10 * time.Second, Validate: 10 * time.Millisecond, Stop: 5 * time.Second, Rollback: 5 * time.Second})
	runner.setTestFn(func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() })
	narrowed := validateTarget(t, "node-1", 443, "a@example.test")
	for i := 0; i < 3; i++ {
		results := mgr.Apply(ctx, []Target{narrowed}, ApplyOptions{PerInstanceTimeout: 20 * time.Second})
		for _, res := range results {
			if res.AppliedUserCount > 1 {
				t.Fatalf("attempt %d kept the revoked user: %+v", i, res)
			}
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
}

// TestReviewMultipleRevocationsStopAllTightenedNodes covers several revocations in
// one round: every tightened node must be stopped, none may be skipped.
func TestReviewMultipleRevocationsStopAllTightenedNodes(t *testing.T) {
	runner := newBlockedValidationRunner()
	mgr := newManagerWithRunner(t, runner)
	ctx := context.Background()
	targets := []Target{
		targetForNode(t, "node-1", "u1@example.test", "x@example.test"),
		targetForNode(t, "node-2", "u2@example.test", "x@example.test"),
		targetForNode(t, "node-3", "u3@example.test"),
	}
	if results := mgr.Apply(ctx, targets, ApplyOptions{}); len(results) != 3 {
		t.Fatalf("initial apply: %+v", results)
	}
	// Two nodes lose a user at once; the third changes technically.
	mgr.SetTimeouts(Timeouts{Readiness: 5 * time.Second, Validate: 3 * time.Second, Stop: 5 * time.Second, Rollback: 5 * time.Second})
	runner.mu.Lock()
	runner.blockedID = "node-3"
	runner.mu.Unlock()
	t.Cleanup(runner.releaseBlocked)
	next := []Target{
		targetForNode(t, "node-1", "u1@example.test"),
		targetForNode(t, "node-2", "u2@example.test"),
		targetWithPort(t, targetForNode(t, "node-3", "u3@example.test"), 8443),
	}
	done := make(chan []Result, 1)
	go func() { done <- mgr.Apply(ctx, next, ApplyOptions{PerInstanceTimeout: 20 * time.Second}) }()
	deadline := time.Now().Add(authorizationDeadline)
	for {
		ok := true
		for _, id := range []string{"node-1", "node-2"} {
			view := mgr.lookup(id).view()
			if hasEmail(view.emailToMember, "x@example.test") {
				ok = false
			}
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not every tightened node was stopped before the blocked validation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	runner.releaseBlocked()
	results := <-done
	if len(results) != 3 {
		t.Fatalf("unexpected results: %+v", results)
	}
	for _, id := range []string{"node-1", "node-2"} {
		if hasEmail(mgr.lookup(id).view().emailToMember, "x@example.test") {
			t.Fatalf("%s still authorizes the revoked user after the round", id)
		}
	}
}

// TestReviewTightenedNodeStopFailureIsNotRetriedOnTopOfSurvivor covers the Stop
// failure interaction: if the narrowed core of a node that STAYS in the snapshot
// cannot be stopped, no replacement core may be started for it and the failure
// must be reported.
func TestReviewTightenedNodeStopFailureIsNotRetriedOnTopOfSurvivor(t *testing.T) {
	mgr, runner := newStubbornManager(t)
	ctx := context.Background()
	target := targetFor(t, "alice@example.test", "bob@example.test")
	if res := mgr.Apply(ctx, []Target{target}, ApplyOptions{}); res[0].State != StateApplied {
		t.Fatalf("initial apply: %+v", res[0])
	}
	startsBefore := runner.startedCount()
	narrowed := targetFor(t, "alice@example.test")
	results := mgr.Apply(ctx, []Target{narrowed}, ApplyOptions{})
	if len(results) != 1 {
		t.Fatalf("unexpected results: %+v", results)
	}
	res := results[0]
	if res.State != StateFailed {
		t.Fatalf("a tightened core that survived the stop must be reported as failed: %+v", res)
	}
	if res.OldConnectionsTerminated {
		t.Fatal("the failed stop claimed the old connections were terminated")
	}
	if got := runner.startedCount(); got != startsBefore {
		t.Fatalf("a replacement core was started on top of the survivor: %d -> %d", startsBefore, got)
	}
	if mgr.lookup(narrowed.InstanceID) == nil {
		t.Fatal("the surviving core lost its tracking record")
	}
}

// helper predicates/utilities

func hasEmail(m map[string]string, email string) bool {
	_, ok := m[email]
	return ok
}

// newManagerWithRunner builds a manager over an arbitrary Runner.
func newManagerWithRunner(t *testing.T, runner Runner) *Manager {
	t.Helper()
	mgr, err := NewManager(t.TempDir(), filepath.Join(t.TempDir(), "xray.exe"), runner, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	var mu sync.Mutex
	port := 23000
	mgr.SetPortAllocator(func(map[int]bool) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		port++
		return port, nil
	})
	return mgr
}

// targetForID builds a target for a specific instance id.
func targetForID(t *testing.T, id, nodeID string, emails ...string) Target {
	t.Helper()
	users := make([]contracts.AuthorizedUser, 0, len(emails))
	for i, e := range emails {
		users = append(users, contracts.AuthorizedUser{MemberID: fmt.Sprintf("%s-m%d", id, i+1), Email: e})
	}
	inst := contracts.Instance{ID: id, NodeID: nodeID, Config: json.RawMessage(baseDocument(443, emails...)), Users: users}
	plan, err := Prepare(inst, Options{CertRoot: t.TempDir(), Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("Prepare %s: %v", id, err)
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

// targetWithPort changes the inbound port of a target, i.e. its technical config
// hash, without touching the authorization.
func targetWithPort(t *testing.T, base Target, port int) Target {
	t.Helper()
	users := base.Users
	inst := contracts.Instance{ID: base.InstanceID, NodeID: base.NodeID, Config: json.RawMessage(baseDocument(port, emailsOf(users)...)), Users: users}
	plan, err := Prepare(inst, Options{CertRoot: t.TempDir(), Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("Prepare %s: %v", base.InstanceID, err)
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

func emailsOf(users []contracts.AuthorizedUser) []string {
	out := make([]string, 0, len(users))
	for _, u := range users {
		out = append(out, u.Email)
	}
	return out
}
