package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
)

func TestABCRecoveryTelemetryAndAuthorizationFreeze(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()
	now := bTime("2026-01-05T12:00:00Z")
	st.Now = func() time.Time { return now }
	server := regServer(t, st, "abc")
	agent := regAgent(t, st, server.ID)
	node := regNode(t, st, server.ID, "abc", 18822)
	sub := regSubscription(t, st, "abc", nil, nil)
	m, err := st.CreateMember(ctx, &domain.Member{ID: ids.New(ids.Member), Username: "abc", PasswordHash: "fixture", Role: "member", Active: true, ProxyEnabled: true, SubscriptionID: &sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	regCredential(t, st, m.ID, node.ID)
	periods, err := st.ListMemberPeriods(ctx, m.ID, *m.PeriodStart, *m.PeriodEnd)
	if err != nil || len(periods) != 1 || periods[0].Source != "binding" {
		t.Fatalf("creation history: %+v %v", periods, err)
	}
	if _, err := st.EnterRecoveryMode(ctx); err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetControlState(ctx)
	// Attribution remains possible for an already recorded interval during recovery.
	batch := contracts.UsageBatch{BatchID: "abc-recovery", Samples: []contracts.UsageSample{{NodeID: node.ID, MemberID: m.ID, StartedAt: now.Add(time.Minute), EndedAt: now.Add(2 * time.Minute), UploadBytes: 23, DownloadBytes: 31}}}
	now = now.Add(time.Hour)
	if status, err := st.SubmitUsageBatch(ctx, agent, batch); err != nil || status != "accepted" {
		t.Fatalf("telemetry: %s %v", status, err)
	}
	if status, err := st.SubmitUsageBatch(ctx, agent, batch); err != nil || status != "duplicate" {
		t.Fatalf("duplicate: %s %v", status, err)
	}
	used, err := st.UsedBytes(ctx, m.ID, m.PeriodStart)
	if err != nil || used != 54 {
		t.Fatalf("byte conservation: %d %v", used, err)
	}
	if err := st.RecordHeartbeat(ctx, agent.ID, contracts.Heartbeat{}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if err := st.RecordApplyReport(ctx, agent.ID, contracts.ApplyReport{Revision: 0, State: "applied"}); err != nil {
		t.Fatalf("necessary receipt blocked: %v", err)
	}
	_, err = st.CreateQuotaAdjustment(ctx, m.ID, 1, "blocked", "fixture")
	bCode(t, err, domain.CodeRecoveryMode)
	// Recovery must be checked before waiting for B's execution advisory lock.
	hold, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback()
	if err := lockExecutionHistoryTx(ctx, hold); err != nil {
		t.Fatal(err)
	}
	blockedCtx, cancel := context.WithTimeout(ctx, time.Second)
	_, err = st.UpdateNode(blockedCtx, node.ID, 0, func(n *domain.Node) error { n.Enabled = false; return nil })
	cancel()
	bCode(t, err, domain.CodeRecoveryMode)
	if err := hold.Rollback(); err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateMember(ctx, &domain.Member{ID: ids.New(ids.Member), Username: "blocked", Role: "member"})
	bCode(t, err, domain.CodeRecoveryMode)
	now = bTime("2026-02-05T12:00:00Z")
	n, err := st.AdvancePeriods(ctx, now)
	bCode(t, err, domain.CodeRecoveryMode)
	if n != 0 {
		t.Fatal("recovery advanced periods")
	}
	if _, err := st.EvaluateAccess(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	afterMember, err := st.GetMember(ctx, m.ID)
	if err != nil || !afterMember.PeriodStart.Equal(*m.PeriodStart) {
		t.Fatalf("read advanced period: %+v %v", afterMember, err)
	}
	after, _ := st.GetControlState(ctx)
	if after.RevisionCounter != before.RevisionCounter {
		t.Fatal("telemetry/read changed authorization revision")
	}
	// New-period samples cannot manufacture history while recovery is active.
	batch.BatchID = "abc-unproven"
	batch.Samples[0].StartedAt, batch.Samples[0].EndedAt = now.Add(-time.Minute), now
	_, err = st.SubmitUsageBatch(ctx, agent, batch)
	bCode(t, err, "LEDGER_UNATTRIBUTABLE")
	var total int64
	if err := st.DB.QueryRow(`SELECT COALESCE(sum(up_bytes+down_bytes),0) FROM usage_entries WHERE member_id=$1`, m.ID).Scan(&total); err != nil || total != 54 {
		t.Fatalf("rejected batch changed ledger: %d %v", total, err)
	}
}

func TestABCBatchBindingUsesRecordedHistory(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()
	now := bTime("2026-01-05T12:00:00Z")
	st.Now = func() time.Time { return now }
	sub := regSubscription(t, st, "binding", nil, nil)
	m, err := st.CreateMember(ctx, &domain.Member{ID: ids.New(ids.Member), Username: "binding", Role: "member", PasswordHash: "fixture", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.inWritableTx(ctx, func(tx *sql.Tx) error { return st.applySubscriptionBinding(ctx, tx, m, sub, now) }); err != nil {
		t.Fatal(err)
	}
	firstStart, firstEnd := *m.PeriodStart, *m.PeriodEnd
	records, err := st.ListMemberPeriods(ctx, m.ID, firstStart, firstEnd)
	if err != nil || len(records) != 1 || records[0].Source != "binding" {
		t.Fatalf("batch history: %+v %v", records, err)
	}
	next := regSubscription(t, st, "replacement", nil, nil)
	if _, err := st.DB.Exec(`UPDATE subscriptions SET anchor_day=15 WHERE id=$1`, next.ID); err != nil {
		t.Fatal(err)
	}
	next.AnchorDay = 15
	updated, err := st.UpdateMember(ctx, m.ID, 0, func(cur *domain.Member) error { cur.SubscriptionID = &next.ID; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !updated.PeriodStart.Equal(firstStart) || !updated.PeriodEnd.Equal(firstEnd) || updated.PendingPeriodRule == nil || updated.PendingPeriodRule.AnchorDay != 15 {
		t.Fatalf("replacement reset running history: %+v", updated)
	}
}
