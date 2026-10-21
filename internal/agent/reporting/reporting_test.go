package reporting

import (
	"context"
	"errors"
	"io"
	"log"
	"testing"
	"time"

	"proxypanel/internal/agent/state"
	"proxypanel/internal/agent/xray"
	"proxypanel/internal/contracts"
)

func newStore(t *testing.T) *state.Store {
	t.Helper()
	s, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

type fakeSource struct {
	stats []xray.InstanceStats
}

func (f *fakeSource) QueryStats(ctx context.Context) []xray.InstanceStats { return f.stats }

func TestSamplerMapsCountersToMembers(t *testing.T) {
	store := newStore(t)
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{stats: []xray.InstanceStats{{
		InstanceID:     "node-1",
		NodeID:         "node-1",
		CoreGeneration: "gen-1",
		EmailToMember:  map[string]string{"alice@example.test": "member-alice"},
		Counters: []xray.StatCounter{
			{Name: "user>>>alice@example.test>>>traffic>>>uplink", Value: 100},
			{Name: "user>>>alice@example.test>>>traffic>>>downlink", Value: 250},
			{Name: "user>>>ghost@example.test>>>traffic>>>uplink", Value: 999},
			{Name: "inbound>>>vless-in>>>traffic>>>uplink", Value: 5000},
		},
	}}}
	sampler := NewSampler(store, source, log.New(io.Discard, "", 0))
	sampler.SetNow(func() time.Time { return now })
	res, err := sampler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(res.BatchIDs) != 1 || res.SampleRows != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Unmapped != 2 {
		t.Fatalf("unmapped counters = %d, want 2", res.Unmapped)
	}
	pending, err := store.PendingBatches(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || len(pending[0].Samples) != 1 {
		t.Fatalf("unexpected queue: %+v", pending)
	}
	sample := pending[0].Samples[0]
	if sample.MemberID != "member-alice" || sample.UploadBytes != 100 || sample.DownloadBytes != 250 {
		t.Fatalf("unexpected sample: %+v", sample)
	}

	// A second identical round must not duplicate usage.
	again, err := sampler.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(again.BatchIDs) != 0 {
		t.Fatalf("duplicate usage was queued: %+v", again)
	}
}

func TestMemberForCounter(t *testing.T) {
	mapping := map[string]string{"a@b.test": "m1"}
	if member, ok := MemberForCounter("user>>>a@b.test>>>traffic>>>uplink", mapping); !ok || member != "m1" {
		t.Fatalf("unexpected mapping: %q %v", member, ok)
	}
	for _, name := range []string{
		"user>>>a@b.test>>>traffic>>>total",
		"inbound>>>x>>>traffic>>>uplink",
		"user>>>unknown@b.test>>>traffic>>>uplink",
		"garbage",
	} {
		if _, ok := MemberForCounter(name, mapping); ok {
			t.Fatalf("%q must not be attributed", name)
		}
	}
}

type fakeUsage struct {
	calls  int
	errs   []error
	status []string
	last   contracts.UsageBatch
}

func (f *fakeUsage) Usage(ctx context.Context, batch contracts.UsageBatch) (contracts.UsageAck, error) {
	f.calls++
	f.last = batch
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return contracts.UsageAck{}, err
		}
	}
	status := "accepted"
	if len(f.status) > 0 {
		status = f.status[0]
		f.status = f.status[1:]
	}
	return contracts.UsageAck{BatchID: batch.BatchID, Status: status}, nil
}

func seedBatch(t *testing.T, store *state.Store) string {
	t.Helper()
	res, err := store.Measure(context.Background(), "node-1", "gen-1", time.Now().UTC(), []state.CounterItem{
		{MemberID: "m1", Counter: "user>>>a@b.test>>>traffic>>>uplink", Value: 10},
	})
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	if res.BatchID == "" {
		t.Fatal("no batch created")
	}
	return res.BatchID
}

func TestUploaderKeepsBatchOnFailure(t *testing.T) {
	store := newStore(t)
	batchID := seedBatch(t, store)
	client := &fakeUsage{errs: []error{errors.New("panel returned HTTP 503")}}
	uploader := NewUploader(store, client, nil, log.New(io.Discard, "", 0))
	res, err := uploader.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected an upload error")
	}
	if res.Failed != 1 || res.Acked != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	pending, _ := store.PendingBatches(context.Background(), 10)
	if len(pending) != 1 || pending[0].BatchID != batchID || pending[0].Attempts != 1 {
		t.Fatalf("batch must be retained after a failure: %+v", pending)
	}
}

func TestUploaderDeletesOnlyAfterDurableAck(t *testing.T) {
	for _, status := range []string{"accepted", "duplicate"} {
		t.Run(status, func(t *testing.T) {
			store := newStore(t)
			seedBatch(t, store)
			client := &fakeUsage{status: []string{status}}
			uploader := NewUploader(store, client, nil, log.New(io.Discard, "", 0))
			res, err := uploader.RunOnce(context.Background())
			if err != nil {
				t.Fatalf("RunOnce: %v", err)
			}
			if res.Acked != 1 {
				t.Fatalf("unexpected result: %+v", res)
			}
			if pending, _ := store.PendingBatches(context.Background(), 10); len(pending) != 0 {
				t.Fatalf("acknowledged batch not deleted: %+v", pending)
			}
		})
	}
}

func TestUploaderKeepsBatchOnNonDurableStatus(t *testing.T) {
	store := newStore(t)
	seedBatch(t, store)
	client := &fakeUsage{status: []string{"queued"}}
	uploader := NewUploader(store, client, nil, log.New(io.Discard, "", 0))
	res, err := uploader.RunOnce(context.Background())
	if err == nil {
		t.Fatal("a non-durable status must be an error")
	}
	if res.Failed != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if pending, _ := store.PendingBatches(context.Background(), 10); len(pending) != 1 {
		t.Fatalf("batch must be retained: %+v", pending)
	}
}

type fakeReports struct {
	errs []error
	sent []contracts.ApplyReport
}

func (f *fakeReports) Applied(ctx context.Context, report contracts.ApplyReport) error {
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return err
		}
	}
	f.sent = append(f.sent, report)
	return nil
}

func TestUploadReportsDeletesOnlyOnSuccess(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	payload := []byte(`{"control_generation":"gen-1","revision":2,"state":"applied","applied_at":"2026-03-01T00:00:00Z","results":[]}`)
	if err := store.SaveReport(ctx, "gen-1:r2:applied", state.ReportKindApplied, payload); err != nil {
		t.Fatal(err)
	}
	client := &fakeReports{errs: []error{errors.New("timeout")}}
	uploader := NewUploader(store, &fakeUsage{}, client, log.New(io.Discard, "", 0))
	if _, err := uploader.UploadReports(ctx); err == nil {
		t.Fatal("expected a delivery error")
	}
	if pending, _ := store.PendingReports(ctx, 10); len(pending) != 1 {
		t.Fatalf("report must be retained: %+v", pending)
	}
	if _, err := uploader.UploadReports(ctx); err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	if pending, _ := store.PendingReports(ctx, 10); len(pending) != 0 {
		t.Fatalf("report not deleted after delivery: %+v", pending)
	}
	if len(client.sent) != 1 || client.sent[0].Revision != 2 {
		t.Fatalf("unexpected delivered reports: %+v", client.sent)
	}
}
