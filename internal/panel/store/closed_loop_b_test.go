package store

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/period"
)

func bTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
func bRefresh(t *testing.T, st *Store, m *domain.Member) {
	t.Helper()
	if err := st.inTx(context.Background(), func(tx *sql.Tx) error { return st.refreshMemberPeriod(context.Background(), tx, m) }); err != nil {
		t.Fatal(err)
	}
}
func bCode(t *testing.T, err error, code string) {
	t.Helper()
	e, ok := err.(*domain.Error)
	if !ok || e.Code != code {
		t.Fatalf("got %v, want %s", err, code)
	}
}

func TestBPeriodFullChain(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()
	now := bTime("2026-01-05T12:00:00Z")
	st.Now = func() time.Time { return now }
	server := regServer(t, st, "a")
	agent := regAgent(t, st, server.ID)
	node := regNode(t, st, server.ID, "n", 18801)
	group, err := st.CreateNodeGroup(ctx, &domain.NodeGroup{ID: ids.New(ids.NodeGroup), Name: "g", NodeIDs: []string{node.ID}})
	if err != nil {
		t.Fatal(err)
	}
	quota := int64(100)
	sub := regSubscription(t, st, "sub", &quota, []string{group.ID})
	m := regMember(t, st, "member", sub, period.Containing(1, time.UTC, now))
	bRefresh(t, st, m)
	regCredential(t, st, m.ID, node.ID)
	batch := contracts.UsageBatch{BatchID: "over", Samples: []contracts.UsageSample{{NodeID: node.ID, MemberID: m.ID, StartedAt: now, EndedAt: now.Add(time.Minute), UploadBytes: 100}}}
	now = now.Add(time.Hour)
	if status, err := st.SubmitUsageBatch(ctx, agent, batch); err != nil || status != "accepted" {
		t.Fatalf("%s %v", status, err)
	}
	var active int
	if err := st.DB.QueryRow(`SELECT count(*) FROM credentials WHERE member_id=$1 AND state='active'`, m.ID).Scan(&active); err != nil || active != 0 {
		t.Fatalf("over quota credentials %d %v", active, err)
	}
	if _, err := st.CreateQuotaAdjustment(ctx, m.ID, 100, "restore", "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT count(*) FROM credentials WHERE member_id=$1 AND state='active'`, m.ID).Scan(&active); err != nil || active != 1 {
		t.Fatalf("restored credentials %d %v", active, err)
	}
	batch.BatchID = "over-again"
	if _, err := st.SubmitUsageBatch(ctx, agent, batch); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT count(*) FROM credentials WHERE member_id=$1 AND state='active'`, m.ID).Scan(&active); err != nil || active != 0 {
		t.Fatal("second quota exhaustion did not revoke")
	}
	// Replacement rule must not alter January. The first new interval is short.
	if _, err := st.DB.Exec(`UPDATE subscriptions SET anchor_day=2 WHERE id=$1`, sub.ID); err != nil {
		t.Fatal(err)
	}
	m, err = st.GetMember(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	bRefresh(t, st, m)
	now = bTime("2026-03-03T12:00:00Z")
	if _, err := st.AdvancePeriods(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT count(*) FROM credentials WHERE member_id=$1 AND state='active'`, m.ID).Scan(&active); err != nil || active != 1 {
		t.Fatal("rollover did not rebuild credentials")
	}
	records, err := st.ListMemberPeriods(ctx, m.ID, bTime("2026-01-01T00:00:00Z"), now)
	if err != nil || len(records) != 4 {
		t.Fatalf("intervals %#v %v", records, err)
	}
	for i := 1; i < len(records); i++ {
		if !records[i-1].PeriodEnd.Equal(records[i].PeriodStart) {
			t.Fatal("gap")
		}
	}
	if !records[1].PeriodStart.Equal(bTime("2026-02-01T00:00:00Z")) || !records[1].PeriodEnd.Equal(bTime("2026-02-02T00:00:00Z")) {
		t.Fatalf("short cycle: %#v", records[1])
	}
	// Late sample crosses the actual short-cycle boundary, not a reconstructed one.
	late := contracts.UsageBatch{BatchID: "late", Samples: []contracts.UsageSample{{NodeID: node.ID, MemberID: m.ID, StartedAt: bTime("2026-02-01T23:00:00Z"), EndedAt: bTime("2026-02-02T01:00:00Z"), UploadBytes: 101, DownloadBytes: 51}}}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	statuses := make(chan string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, err := st.SubmitUsageBatch(ctx, agent, late)
			errs <- err
			statuses <- status
		}()
	}
	wg.Wait()
	close(errs)
	close(statuses)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	accepted, duplicate := 0, 0
	for status := range statuses {
		if status == "accepted" {
			accepted++
		}
		if status == "duplicate" {
			duplicate++
		}
	}
	if accepted != 1 || duplicate != 1 {
		t.Fatalf("statuses %d/%d", accepted, duplicate)
	}
	var up, down, count int64
	if err := st.DB.QueryRow(`SELECT coalesce(sum(up_bytes),0),coalesce(sum(down_bytes),0),count(*) FROM usage_entries WHERE batch_id='late'`).Scan(&up, &down, &count); err != nil || up != 101 || down != 51 || count != 2 {
		t.Fatalf("conservation %d %d %d %v", up, down, count, err)
	}
	late.Samples[0].UploadBytes++
	_, err = st.SubmitUsageBatch(ctx, agent, late)
	bCode(t, err, domain.CodeBatchPayloadMismatch)
	// Equal timestamps are still distinct samples; split_part must not cause
	// silent ON CONFLICT byte loss within the same batch.
	twinSample := contracts.UsageSample{NodeID: node.ID, MemberID: m.ID, StartedAt: now.Add(-time.Minute), EndedAt: now, UploadBytes: 3, DownloadBytes: 4}
	if _, err := st.SubmitUsageBatch(ctx, agent, contracts.UsageBatch{BatchID: "twins", Samples: []contracts.UsageSample{twinSample, twinSample}}); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT sum(up_bytes),sum(down_bytes),count(*) FROM usage_entries WHERE batch_id='twins'`).Scan(&up, &down, &count); err != nil || up != 6 || down != 8 || count != 2 {
		t.Fatalf("same-range samples lost bytes: %d %d %d %v", up, down, count, err)
	}
	// First valid sample must roll back too if a later sample predates all periods.
	missing := late
	missing.BatchID = "missing"
	missing.Samples = append([]contracts.UsageSample{}, late.Samples...)
	bad := missing.Samples[0]
	bad.StartedAt = bTime("2025-12-31T23:00:00Z")
	bad.EndedAt = bTime("2026-01-01T01:00:00Z")
	missing.Samples = append(missing.Samples, bad)
	// Explicit owner evidence covers December, but no billing evidence does.
	if _, err := st.DB.Exec(`UPDATE node_server_history SET valid_from='2025-12-01' WHERE node_id=$1`, node.ID); err != nil {
		t.Fatal(err)
	}
	_, err = st.SubmitUsageBatch(ctx, agent, missing)
	bCode(t, err, "LEDGER_UNATTRIBUTABLE")
	if err := st.DB.QueryRow(`SELECT count(*) FROM usage_batches WHERE batch_id='missing'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected batch was ACKed")
	}
	if used, err := st.UsedBytes(ctx, m.ID, &records[0].PeriodStart); err != nil || used != 200 {
		t.Fatalf("old ledger changed: %d %v", used, err)
	}
}

func TestBMoveLateAndTerminalIsolation(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()
	now := bTime("2026-01-05T00:00:00Z")
	st.Now = func() time.Time { return now }
	a := regServer(t, st, "a")
	b := regServer(t, st, "b")
	aa := regAgent(t, st, a.ID)
	ba := regAgent(t, st, b.ID)
	n := regNode(t, st, a.ID, "n", 18802)
	sub := regSubscription(t, st, "s", nil, nil)
	m := regMember(t, st, "m", sub, period.Containing(1, time.UTC, now))
	bRefresh(t, st, m)
	regCredential(t, st, m.ID, n.ID)
	sample := contracts.UsageSample{NodeID: n.ID, MemberID: m.ID, StartedAt: now, EndedAt: now.Add(time.Minute), UploadBytes: 77}
	now = now.Add(time.Hour)
	if _, err := st.UpdateNode(ctx, n.ID, 0, func(n *domain.Node) error { n.ServerID = b.ID; return nil }); err != nil {
		t.Fatal(err)
	}
	aa, _ = st.GetAgent(ctx, aa.ID)
	release := aa.DesiredRevision
	// A remains authorized through confirmed termination, not the move click.
	if _, err := st.SubmitUsageBatch(ctx, aa, contracts.UsageBatch{BatchID: "late-a", Samples: []contracts.UsageSample{sample}}); err != nil {
		t.Fatal(err)
	}
	// A partial/failed report can prove release, but cannot touch B's core
	// state or claim snapshot-wide success.
	terminal := contracts.InstanceResult{InstanceID: n.ID, State: "stopped", OldConnectionsTerminated: true}
	var bState string
	if err := st.DB.QueryRow(`SELECT state FROM core_instances WHERE node_id=$1`, n.ID).Scan(&bState); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordApplyReport(ctx, aa.ID, contracts.ApplyReport{Revision: release, State: "failed", Results: []contracts.InstanceResult{terminal}}); err != nil {
		t.Fatal(err)
	}
	var bAfter string
	if err := st.DB.QueryRow(`SELECT state FROM core_instances WHERE node_id=$1`, n.ID).Scan(&bAfter); err != nil || bAfter != bState {
		t.Fatal("A terminal report polluted B")
	}
	if _, err := st.UpdateNode(ctx, n.ID, 0, func(n *domain.Node) error { n.ServerID = a.ID; return nil }); err != nil {
		t.Fatal(err)
	}
	var generation int64
	var state string
	if err := st.DB.QueryRow(`SELECT generation,state FROM core_instances WHERE node_id=$1`, n.ID).Scan(&generation, &state); err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetAgent(ctx, aa.ID)
	if err := st.RecordApplyReport(ctx, aa.ID, contracts.ApplyReport{Revision: release, State: "applied", Results: []contracts.InstanceResult{terminal}}); err != nil {
		t.Fatal(err)
	}
	after, _ := st.GetAgent(ctx, aa.ID)
	if before.AppliedRevision != after.AppliedRevision {
		t.Fatal("historical report advanced applied waterline")
	}
	var active int
	if err := st.DB.QueryRow(`SELECT count(*) FROM node_server_history WHERE node_id=$1 AND agent_id=$2 AND valid_to IS NULL AND released_revision IS NULL`, n.ID, aa.ID).Scan(&active); err != nil || active != 1 {
		t.Fatalf("new A window closed: %d %v", active, err)
	}
	var gotGen int64
	var gotState string
	if err := st.DB.QueryRow(`SELECT generation,state FROM core_instances WHERE node_id=$1`, n.ID).Scan(&gotGen, &gotState); err != nil || gotGen != generation || gotState != state {
		t.Fatal("historical report polluted current instance")
	}
	// Ordinary B results cannot touch the current A instance (SEC02).
	err := st.RecordHeartbeat(ctx, ba.ID, contracts.Heartbeat{Instances: []contracts.InstanceResult{{InstanceID: n.ID, State: "running"}}})
	bCode(t, err, domain.CodeForbidden)
	// An old ordinary A report cannot borrow current A metadata as proof.
	err = st.RecordHeartbeat(ctx, aa.ID, contracts.Heartbeat{Revision: release, Instances: []contracts.InstanceResult{{InstanceID: n.ID, State: "running"}}})
	bCode(t, err, domain.CodeRevisionStale)
	// Repeated terminal heartbeat is idempotent and still skips the new A state.
	if err := st.RecordHeartbeat(ctx, aa.ID, contracts.Heartbeat{Revision: release, Instances: []contracts.InstanceResult{terminal}}); err != nil {
		t.Fatal(err)
	}
}

func TestBInitialAndConcurrentAdvance(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()
	now := bTime("2026-01-05T00:00:00Z")
	st.Now = func() time.Time { return now }
	server := regServer(t, st, "registered")
	node := regNode(t, st, server.ID, "before-agent", 18804)
	now = now.Add(time.Hour)
	if _, err := st.CreateRegistrationCode(ctx, server.ID, "b-registration-hash", now.Add(time.Hour), "test"); err != nil {
		t.Fatal(err)
	}
	agent, _, err := st.RegisterAgent(ctx, "b-registration-hash", "registered", "test", "b-agent-token-hash", "prefix")
	if err != nil {
		t.Fatal(err)
	}
	var validFrom time.Time
	if err := st.DB.QueryRow(`SELECT valid_from FROM node_server_history WHERE node_id=$1 AND agent_id=$2`, node.ID, agent.ID).Scan(&validFrom); err != nil || !validFrom.Equal(now) {
		t.Fatalf("identity registration invented old history: %v %v", validFrom, err)
	}
	sub := regSubscription(t, st, "initial", nil, nil)
	m, err := st.CreateMember(ctx, &domain.Member{ID: ids.New(ids.Member), Username: "initial", PasswordHash: "test", Role: "member", Active: true, ProxyEnabled: true, SubscriptionID: &sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	bRefresh(t, st, m)
	rows, err := st.ListMemberPeriods(ctx, m.ID, bTime("2026-01-01T00:00:00Z"), bTime("2026-02-01T00:00:00Z"))
	if err != nil || len(rows) != 1 || rows[0].Source != "binding" {
		t.Fatalf("initial registration %v %v", rows, err)
	}
	now = bTime("2026-04-05T00:00:00Z")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := st.AdvancePeriods(ctx, now); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err = st.ListMemberPeriods(ctx, m.ID, bTime("2026-01-01T00:00:00Z"), now)
	if err != nil || len(rows) != 4 {
		t.Fatalf("concurrent history %v %v", rows, err)
	}
}

func TestBOwnerUnionAndGap(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()
	a := regServer(t, st, "a")
	aa := regAgent(t, st, a.ID)
	n := regNode(t, st, a.ID, "n", 18803)
	start := bTime("2026-01-01T00:00:00Z")
	mid := start.Add(time.Hour)
	end := mid.Add(time.Hour)
	if _, err := st.DB.Exec(`DELETE FROM node_server_history WHERE node_id=$1`, n.ID); err != nil {
		t.Fatal(err)
	}
	for i, w := range []period.Boundary{{Start: start, End: mid}, {Start: mid, End: end}} {
		if _, err := st.DB.Exec(`INSERT INTO node_server_history(id,node_id,server_id,agent_id,valid_from,valid_to,released_revision,release_confirmed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$6)`, ids.New("nsh"), n.ID, a.ID, aa.ID, w.Start, w.End, i+1); err != nil {
			t.Fatal(err)
		}
	}
	check := func() error {
		return st.inTx(ctx, func(tx *sql.Tx) error { return nodeAuthorizedForRangeTx(ctx, tx, n.ID, a.ID, aa.ID, start, end) })
	}
	if err := check(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`UPDATE node_server_history SET valid_from=valid_from+interval '1 second' WHERE node_id=$1 AND released_revision=2`, n.ID); err != nil {
		t.Fatal(err)
	}
	bCode(t, check(), "LEDGER_UNATTRIBUTABLE")
}
