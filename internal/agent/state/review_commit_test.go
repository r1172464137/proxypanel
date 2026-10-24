package state

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"proxypanel/internal/contracts"
)

func commitTarget(rev int64) contracts.Snapshot {
	return contracts.Snapshot{SchemaVersion: contracts.SchemaVersion, ControlGeneration: "g1", Revision: rev, Instances: []contracts.Instance{{NodeID: "n1", Config: json.RawMessage(`{"inbounds":[]}`), Users: []contracts.AuthorizedUser{{MemberID: "m1", Email: "a@test"}}}}}
}
func commitReport(rev int64, state string) contracts.ApplyReport {
	return contracts.ApplyReport{ControlGeneration: "g1", Revision: rev, State: state, AppliedAt: time.Now().UTC()}
}
func commitRecords() []InstanceRecord {
	return []InstanceRecord{{NodeID: "n1", CoreGeneration: "core1", State: "applied", Config: []byte(`{"inbounds":[]}`), Users: commitTarget(1).Instances[0].Users, AppliedUserCount: 1}}
}

func TestCommitSnapshotAtomicRollbackAndRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.RecordInstance(ctx, InstanceRecord{NodeID: "old", State: "applied"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveApplyIntent(ctx, commitTarget(2)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAcceptedGeneration(ctx, "g1"); err != nil {
		t.Fatal(err)
	}
	// Fail the final statement, after all proposed watermark/history changes.
	if _, err := s.db.Exec(`CREATE TRIGGER fail_report BEFORE INSERT ON reports BEGIN SELECT RAISE(ABORT,'injected report failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitSnapshot(ctx, commitRecords(), commitReport(2, "applied"), true); err == nil {
		t.Fatal("expected rollback")
	}
	rows, err := s.Instances(ctx)
	if err != nil || len(rows) != 1 || rows[0].NodeID != "old" {
		t.Fatalf("partial instances: %+v %v", rows, err)
	}
	id, err := s.Identity(ctx)
	if err != nil || id.AppliedRevision != 0 || id.ControlGeneration != "" {
		t.Fatalf("advanced: %+v %v", id, err)
	}
	for _, key := range []string{MetaAppliedSnapshot, MetaGenerationHistory} {
		if raw, err := s.Meta(ctx, key); err != nil || raw != "" {
			t.Fatalf("partial %s: %q %v", key, raw, err)
		}
	}
	if accepted, err := s.AcceptedGeneration(ctx); err != nil || accepted != "g1" {
		t.Fatalf("acceptance lost: %q %v", accepted, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_report`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := s.ApplyIntent(ctx)
	if err != nil || intent == nil || intent.Revision != 2 || len(intent.Instances[0].Users) != 1 {
		t.Fatalf("restart intent: %+v %v", intent, err)
	}
	if err := s.CommitSnapshot(ctx, commitRecords(), commitReport(2, "applied"), true); err != nil {
		t.Fatal(err)
	}
	id, err = s.Identity(ctx)
	if err != nil || id.ControlGeneration != "g1" || id.AppliedRevision != 2 {
		t.Fatalf("watermark: %+v %v", id, err)
	}
	snap, err := s.AppliedSnapshot(ctx)
	if err != nil || snap == nil || snap.Revision != 2 || len(snap.Instances[0].Users) != 1 {
		t.Fatalf("authorization: %+v %v", snap, err)
	}
	if intent, err := s.ApplyIntent(ctx); err != nil || intent != nil {
		t.Fatalf("intent not consumed: %+v %v", intent, err)
	}
	history, err := s.GenerationHistory(ctx)
	if err != nil || len(history) != 1 || history[0] != "g1" {
		t.Fatalf("history: %v %v", history, err)
	}
	if accepted, err := s.AcceptedGeneration(ctx); err != nil || accepted != "" {
		t.Fatalf("acceptance not consumed: %q %v", accepted, err)
	}
	rows, err = s.Instances(ctx)
	if err != nil || len(rows) != 1 || rows[0].NodeID != "n1" || len(rows[0].Users) != 1 {
		t.Fatalf("instances: %+v %v", rows, err)
	}
	reports, err := s.PendingReports(ctx, 10)
	if err != nil || len(reports) != 1 || reports[0].ID != "g1:r2:applied" {
		t.Fatalf("reports: %+v %v", reports, err)
	}
	var report contracts.ApplyReport
	if err := json.Unmarshal(reports[0].Payload, &report); err != nil || report.Revision != 2 {
		t.Fatalf("payload: %+v %v", report, err)
	}
}

func TestCommitSnapshotFailedApplyRetainsIntent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.SaveApplyIntent(ctx, commitTarget(1)); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitSnapshot(ctx, commitRecords(), commitReport(1, "applied"), true); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveApplyIntent(ctx, commitTarget(2)); err != nil {
		t.Fatal(err)
	}
	rec := commitRecords()
	rec[0].State = "failed"
	if err := s.CommitSnapshot(ctx, rec, commitReport(2, "failed"), false); err != nil {
		t.Fatal(err)
	}
	id, _ := s.Identity(ctx)
	snap, err := s.AppliedSnapshot(ctx)
	if err != nil || id.AppliedRevision != 1 || snap == nil || snap.Revision != 1 {
		t.Fatalf("failure advanced: %+v %+v %v", id, snap, err)
	}
	intent, err := s.ApplyIntent(ctx)
	if err != nil || intent == nil || intent.Revision != 2 {
		t.Fatalf("lost intent: %+v %v", intent, err)
	}
	reports, err := s.PendingReports(ctx, 10)
	if err != nil || len(reports) != 2 {
		t.Fatalf("failed report lost: %+v %v", reports, err)
	}
}

func TestCommitSnapshotRejectsStaleIntent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, rev := range []int64{2, 3} {
		if err := s.SaveApplyIntent(ctx, commitTarget(rev)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CommitSnapshot(ctx, commitRecords(), commitReport(2, "applied"), true); !errors.Is(err, ErrApplyConflict) {
		t.Fatalf("stale commit: %v", err)
	}
	if err := s.SaveApplyIntent(ctx, commitTarget(2)); !errors.Is(err, ErrApplyConflict) {
		t.Fatalf("stale intent: %v", err)
	}
	changed := commitTarget(3)
	changed.Instances = nil
	if err := s.SaveApplyIntent(ctx, changed); !errors.Is(err, ErrApplyConflict) {
		t.Fatalf("same revision changed authorization: %v", err)
	}
	if err := s.CommitSnapshot(ctx, commitRecords(), commitReport(3, "applied"), true); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveApplyIntent(ctx, commitTarget(2)); !errors.Is(err, ErrApplyConflict) {
		t.Fatalf("watermark rollback: %v", err)
	}
	next := commitTarget(1)
	next.ControlGeneration = "g2"
	if err := s.SaveApplyIntent(ctx, next); !errors.Is(err, ErrApplyConflict) {
		t.Fatalf("unaccepted generation: %v", err)
	}
	if err := s.SetAcceptedGeneration(ctx, "g2"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveApplyIntent(ctx, next); err != nil {
		t.Fatal(err)
	}
	report := commitReport(1, "applied")
	report.ControlGeneration = "g2"
	if err := s.CommitSnapshot(ctx, nil, report, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAcceptedGeneration(ctx, "g1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveApplyIntent(ctx, commitTarget(99)); !errors.Is(err, ErrApplyConflict) {
		t.Fatalf("retired generation replay: %v", err)
	}
}

func TestCommitSnapshotSerializesWithMeasure(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.SaveApplyIntent(ctx, commitTarget(1)); err != nil {
		t.Fatal(err)
	}
	// Hold the sole connection: both production operations must wait, then
	// complete without partial state or altering the existing batch's payload.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	start := make(chan struct{})
	done := make(chan error, 2)
	go func() { <-start; done <- s.CommitSnapshot(ctx, commitRecords(), commitReport(1, "applied"), true) }()
	go func() {
		<-start
		_, err := s.Measure(ctx, "n1", "core1", time.Now(), []CounterItem{{MemberID: "m1", Counter: counterUp, Value: 100}})
		done <- err
	}()
	close(start)
	select {
	case err := <-done:
		t.Fatalf("operation bypassed transaction: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("deadlock")
		}
	}
	before, err := s.PendingBatches(ctx, 10)
	if err != nil || len(before) != 1 {
		t.Fatalf("queue: %+v %v", before, err)
	}
	if err := s.SaveApplyIntent(ctx, commitTarget(2)); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitSnapshot(ctx, nil, commitReport(2, "applied"), true); err != nil {
		t.Fatal(err)
	}
	after, err := s.PendingBatches(ctx, 10)
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if err != nil || string(a) != string(b) || after[0].Samples[0].UploadBytes != 100 {
		t.Fatalf("queue changed: %s -> %s %v", a, b, err)
	}
}
