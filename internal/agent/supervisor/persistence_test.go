package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxypanel/internal/agent/state"
	"proxypanel/internal/agent/xray"
	"proxypanel/internal/contracts"
)

// setTestError injects a core validation failure into the stub runner.
func (r *stubRunner) setTestError(err error) {
	r.mu.Lock()
	r.testErr = err
	r.mu.Unlock()
}

// TestExpiryRebuildFailurePersistsFilteredAuthorization pins the guarantee the
// A14 copy depends on: when the local expiry rebuild cannot finish (rejected
// configuration, failed start or a cancelled apply), the Agent must still
// persist the authorization its own clock already narrowed. Leaving the stale,
// wider record behind would make the copied state look unexpired and would only
// be filtered again by luck at the next start.
func TestExpiryRebuildFailurePersistsFilteredAuthorization(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	expiry := env.now.Add(30 * time.Second)
	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{
		nodeInstance([]contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test", ExpiresAt: &expiry}}, "alice@example.test"),
	}, 1)
	clock := env.now
	env.sup.now = func() time.Time { return clock }
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if countClients(env.runner.lastStartedFor("node-1")) != 1 {
		t.Fatalf("the user was not authorized before expiry: %q", env.runner.lastStartedFor("node-1"))
	}

	// The rebuild cannot complete: every start is rejected.
	env.runner.setTestError(errors.New("injected rebuild failure"))
	clock = expiry.Add(time.Second)
	env.sup.sampler.SetNow(func() time.Time { return clock })
	if err := env.sup.expiryOnce(ctx); err != nil {
		t.Fatalf("expiryOnce: %v", err)
	}

	records, err := env.store.Instances(ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("records: %+v, err=%v", records, err)
	}
	rec := records[0]
	if len(rec.Users) != 0 {
		t.Fatalf("a failed expiry rebuild left the expired user persisted: %+v", rec.Users)
	}
	if !rec.NextExpiry.IsZero() {
		t.Fatalf("a failed expiry rebuild kept a user expiry trigger: %v", rec.NextExpiry)
	}
	if rec.AppliedUserCount != 0 {
		t.Fatalf("applied user count = %d, want 0", rec.AppliedUserCount)
	}
	if rec.State != xray.StateFailed || !strings.Contains(rec.Error, "injected rebuild failure") {
		t.Fatalf("the failed rebuild was not recorded: state=%q error=%q", rec.State, rec.Error)
	}
	// The published report must agree with the durable record.
	report := readStatusReport(t, env)
	if len(report.Instances) != 1 || report.Instances[0].Users != 0 || !report.Instances[0].ExpiryCleared {
		t.Fatalf("status report does not reflect the narrowed authorization: %+v", report)
	}

	// A restart of the committed record (the real recovery path) must not
	// resurrect the expired user even though the rebuild itself failed.
	env.runner.setTestError(nil)
	env.sup.mgr.StopAll(ctx)
	if err := env.sup.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := countClients(env.runner.lastStartedFor("node-1")); got != 0 {
		t.Fatalf("restore re-authorized the expired user: %d client(s)", got)
	}
}

// TestRestoreFiltersPersistedExpiredUser covers the exact state a shutdown can
// leave behind: a stopped instance record whose authorization still lists a user
// whose delivered expiry already passed. A restart must drop it - the record
// alone never revives access.
func TestRestoreFiltersPersistedExpiredUser(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	past := env.now.Add(-time.Minute)
	inst := nodeInstance([]contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test", ExpiresAt: &past}}, "alice@example.test")
	if err := env.store.RecordInstance(ctx, state.InstanceRecord{
		NodeID:     "node-1",
		State:      xray.StateStopped,
		Config:     inst.Config,
		Users:      inst.Users,
		NextExpiry: past,
		UpdatedAt:  env.now,
	}); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	if err := env.sup.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if starts, _ := env.runner.counts(); starts != 1 {
		t.Fatalf("restore did not start the instance: starts=%d", starts)
	}
	if got := countClients(env.runner.lastStartedFor("node-1")); got != 0 {
		t.Fatalf("restore authorized an already expired user: %d client(s)", got)
	}
	records, err := env.store.Instances(ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("records: %+v, err=%v", records, err)
	}
	if len(records[0].Users) != 0 || !records[0].NextExpiry.IsZero() {
		t.Fatalf("restore did not rewrite the filtered authorization: %+v", records[0])
	}
}

// TestShutdownDropsPersistedExpiredAuthorization covers an interrupted local
// expiry: the shutdown path rewrites the stopped records itself, so it must
// apply the same expiry filter instead of restoring the wider snapshot users.
func TestShutdownDropsPersistedExpiredAuthorization(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	past := env.now.Add(-time.Minute)
	future := env.now.Add(time.Hour)
	inst := nodeInstance([]contracts.AuthorizedUser{
		{MemberID: "m1", Email: "alice@example.test", ExpiresAt: &past},
		{MemberID: "m2", Email: "bob@example.test", ExpiresAt: &future},
	}, "alice@example.test", "bob@example.test")
	if err := env.store.RecordInstance(ctx, state.InstanceRecord{
		NodeID:           "node-1",
		State:            xray.StateApplied,
		CoreGeneration:   "node-1/live",
		ConfigHash:       "hash",
		Config:           inst.Config,
		Users:            inst.Users,
		AppliedUserCount: 2,
		NextExpiry:       past,
		UpdatedAt:        env.now,
	}); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	env.sup.persistStoppedInstances(ctx, []xray.Result{{InstanceID: "node-1", State: xray.StateStopped}})
	records, err := env.store.Instances(ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("records: %+v, err=%v", records, err)
	}
	rec := records[0]
	if rec.State != xray.StateStopped || rec.CoreGeneration != "" || rec.ConfigHash != "" {
		t.Fatalf("shutdown state was not recorded: %+v", rec)
	}
	if len(rec.Users) != 1 || rec.Users[0].MemberID != "m2" {
		t.Fatalf("shutdown did not drop only the expired user: %+v", rec.Users)
	}
	if rec.AppliedUserCount != 1 || !rec.NextExpiry.Equal(future) {
		t.Fatalf("shutdown kept stale counts: applied=%d next=%v", rec.AppliedUserCount, rec.NextExpiry)
	}
}

// TestStatusReportIsSanitized pins the read-only reporting channel the isolated
// acceptance uses instead of treating a failed network fetch as completion: it
// must publish durable state and must never carry a secret or an identifier.
func TestStatusReportIsSanitized(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	expiry := env.now.Add(time.Hour)
	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{
		nodeInstance([]contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test", ExpiresAt: &expiry}}, "alice@example.test"),
	}, 1)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	report := readStatusReport(t, env)
	if report.Schema != 1 || len(report.Instances) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	inst := report.Instances[0]
	if inst.Users != 1 || inst.AppliedUsers != 1 || inst.ExpiryCleared || inst.HasError {
		t.Fatalf("report does not describe the persisted record: %+v", inst)
	}
	raw, err := os.ReadFile(filepath.Join(env.store.Dir(), statusFileName))
	if err != nil {
		t.Fatalf("read status file: %v", err)
	}
	for _, secret := range []string{"test-token", "alice@example.test", "member-alice", "\"m1\"", "agent-1", env.server.URL, "11111111-1111-1111-1111"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("status report leaks %q: %s", secret, raw)
		}
	}
}

// readStatusReport decodes the published sanitized report of a test supervisor.
func readStatusReport(t *testing.T, env *testEnv) statusReport {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(env.store.Dir(), statusFileName))
	if err != nil {
		t.Fatalf("read status report: %v", err)
	}
	var report statusReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("decode status report: %v", err)
	}
	return report
}
