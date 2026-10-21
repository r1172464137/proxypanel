package state

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"proxypanel/internal/contracts"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

const (
	counterUp   = "user>>>alice@example.test>>>traffic>>>uplink"
	counterDown = "user>>>alice@example.test>>>traffic>>>downlink"
)

func TestMeasureWritesBaselineAndBatchInOneTransaction(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	res, err := s.Measure(ctx, "node-1", "gen-1", now, []CounterItem{
		{MemberID: "m1", Counter: counterUp, Value: 100},
		{MemberID: "m1", Counter: counterDown, Value: 200},
	})
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	if res.BatchID == "" || res.SampleRows != 1 {
		t.Fatalf("unexpected measure result: %+v", res)
	}
	var baselines int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM baseline WHERE node_id = 'node-1' AND core_generation = 'gen-1'`).Scan(&baselines); err != nil {
		t.Fatal(err)
	}
	if baselines != 2 {
		t.Fatalf("expected 2 baselines, got %d", baselines)
	}
	pending, err := s.PendingBatches(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending batch, got %d", len(pending))
	}
	sample := pending[0].Samples[0]
	if sample.UploadBytes != 100 || sample.DownloadBytes != 200 {
		t.Fatalf("unexpected sample: %+v", sample)
	}
	if sample.MemberID != "m1" || sample.NodeID != "node-1" || sample.CoreGeneration != "gen-1" {
		t.Fatalf("unexpected sample identity: %+v", sample)
	}
	if !sample.StartedAt.Before(sample.EndedAt) {
		t.Fatalf("sample interval must be positive: %v -> %v", sample.StartedAt, sample.EndedAt)
	}

	// Same counters again: no new usage may be reported.
	again, err := s.Measure(ctx, "node-1", "gen-1", now.Add(5*time.Second), []CounterItem{
		{MemberID: "m1", Counter: counterUp, Value: 100},
		{MemberID: "m1", Counter: counterDown, Value: 200},
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.BatchID != "" {
		t.Fatalf("unchanged counters created a batch: %+v", again)
	}
}

func TestMeasureComputesDeltasAndRebasesCounterReset(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if _, err := s.Measure(ctx, "node-1", "gen-1", now, []CounterItem{
		{MemberID: "m1", Counter: counterUp, Value: 100},
		{MemberID: "m1", Counter: counterDown, Value: 200},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Measure(ctx, "node-1", "gen-1", now.Add(5*time.Second), []CounterItem{
		{MemberID: "m1", Counter: counterUp, Value: 260},
		{MemberID: "m1", Counter: counterDown, Value: 50}, // counter went backwards
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingBatches(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(pending))
	}
	sample := pending[1].Samples[0]
	if sample.UploadBytes != 160 {
		t.Fatalf("upload delta = %d, want 160", sample.UploadBytes)
	}
	if sample.DownloadBytes != 50 {
		t.Fatalf("download delta after reset = %d, want 50", sample.DownloadBytes)
	}
}

func TestMeasureSeparatesCoreGenerations(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if _, err := s.Measure(ctx, "node-1", "gen-1", now, []CounterItem{{MemberID: "m1", Counter: counterUp, Value: 100}}); err != nil {
		t.Fatal(err)
	}
	// A new core generation starts from zero counters: the baseline must not
	// leak across generations.
	res, err := s.Measure(ctx, "node-1", "gen-2", now.Add(time.Second), []CounterItem{{MemberID: "m1", Counter: counterUp, Value: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if res.BatchID == "" {
		t.Fatal("new core generation must produce its own sample")
	}
	pending, err := s.PendingBatches(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, b := range pending {
		for _, smp := range b.Samples {
			total += smp.UploadBytes
		}
	}
	if total != 110 {
		t.Fatalf("total upload = %d, want 110", total)
	}
}

func TestBatchLifecycleRequiresAcknowledgement(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	res, err := s.Measure(ctx, "node-1", "gen-1", time.Now().UTC(), []CounterItem{{MemberID: "m1", Counter: counterUp, Value: 5}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkBatchAttempt(ctx, res.BatchID, "HTTP 503"); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingBatches(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Attempts != 1 || pending[0].LastError != "HTTP 503" {
		t.Fatalf("attempt not recorded: %+v", pending)
	}
	if err := s.DeleteBatch(ctx, res.BatchID); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.PendingBatches(ctx, 10); len(pending) != 0 {
		t.Fatalf("batch not deleted: %+v", pending)
	}
	depth, err := s.QueueDepth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if depth.Batches != 0 || depth.Samples != 0 {
		t.Fatalf("queue not empty: %+v", depth)
	}
}

func TestQueueDepthCapacity(t *testing.T) {
	d := QueueDepth{Batches: MaxQueuedBatches + 1}
	if !d.OverCapacity() {
		t.Fatal("batch limit not enforced")
	}
	d = QueueDepth{Samples: MaxQueuedSamples + 1}
	if !d.OverCapacity() {
		t.Fatal("sample limit not enforced")
	}
	d = QueueDepth{Batches: 10, Samples: 100}
	if d.OverCapacity() {
		t.Fatal("small queue reported as over capacity")
	}
}

func TestReportsOutbox(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	payload, _ := json.Marshal(contracts.ApplyReport{Revision: 3, State: "applied"})
	if err := s.SaveReport(ctx, "gen-1:r3:applied", ReportKindApplied, payload); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveReport(ctx, "gen-1:r3:applied", ReportKindApplied, payload); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingReports(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("report outbox must be deduplicated, got %d", len(pending))
	}
	if err := s.MarkReportAttempt(ctx, pending[0].ID, "boom"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteReport(ctx, pending[0].ID); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.PendingReports(ctx, 10); len(pending) != 0 {
		t.Fatalf("report not deleted: %+v", pending)
	}
}

func TestInstanceRecords(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	expiry := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	rec := InstanceRecord{
		NodeID:           "node-1",
		ConfigHash:       "hash",
		CoreGeneration:   "gen-1",
		State:            "applied",
		AppliedUserCount: 2,
		Config:           []byte(`{"inbounds":[]}`),
		Users:            []contracts.AuthorizedUser{{MemberID: "m1", Email: "a@b.test", ExpiresAt: &expiry}},
		NextExpiry:       expiry,
	}
	if err := s.RecordInstance(ctx, rec); err != nil {
		t.Fatal(err)
	}
	records, err := s.Instances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || string(records[0].Config) != `{"inbounds":[]}` || len(records[0].Users) != 1 {
		t.Fatalf("unexpected records: %+v", records)
	}
	if !records[0].NextExpiry.Equal(expiry) {
		t.Fatalf("next expiry lost: %v", records[0].NextExpiry)
	}
	// A partial update must not lose the stored configuration.
	if err := s.RecordInstance(ctx, InstanceRecord{NodeID: "node-1", State: "failed", Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	records, _ = s.Instances(ctx)
	if len(records) != 1 || len(records[0].Config) == 0 {
		t.Fatalf("configuration lost on partial update: %+v", records)
	}
	if records[0].State != "failed" || records[0].Error != "boom" {
		t.Fatalf("state not updated: %+v", records[0])
	}
	if err := s.RemoveInstance(ctx, "node-1"); err != nil {
		t.Fatal(err)
	}
	if records, _ := s.Instances(ctx); len(records) != 0 {
		t.Fatalf("record not removed: %+v", records)
	}
}

func TestIdentityAndGenerationHistory(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id, err := s.Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id.Token != "" || id.AppliedRevision != 0 {
		t.Fatalf("unexpected initial identity: %+v", id)
	}
	if err := s.SaveIdentity(ctx, Identity{AgentID: "a1", Token: "secret", ControlGeneration: "gen-1", AppliedRevision: 5}); err != nil {
		t.Fatal(err)
	}
	id, err = s.Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id.Token != "secret" || id.AgentID != "a1" || id.AppliedRevision != 5 {
		t.Fatalf("identity not persisted: %+v", id)
	}
	if err := s.SetApplied(ctx, "gen-2", 9); err != nil {
		t.Fatal(err)
	}
	id, _ = s.Identity(ctx)
	if id.ControlGeneration != "gen-2" || id.AppliedRevision != 9 {
		t.Fatalf("applied watermark not persisted: %+v", id)
	}
	if err := s.RecordGeneration(ctx, "gen-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordGeneration(ctx, "gen-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordGeneration(ctx, "gen-2"); err != nil {
		t.Fatal(err)
	}
	history, err := s.GenerationHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0] != "gen-1" || history[1] != "gen-2" {
		t.Fatalf("unexpected history: %v", history)
	}
}
