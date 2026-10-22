package reporting

import (
	"context"
	"errors"
	"io"
	"log"
	"sort"
	"strings"
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

// scriptedSource replays one statsquery response per call, so a test can make a
// node fail for a while and then answer.
type scriptedSource struct {
	rounds [][]xray.InstanceStats
	calls  int
}

func (s *scriptedSource) QueryStats(ctx context.Context) []xray.InstanceStats {
	idx := s.calls
	if idx >= len(s.rounds) {
		idx = len(s.rounds) - 1
	}
	s.calls++
	return s.rounds[idx]
}

func sampledNode(nodeID string, uplink int64) xray.InstanceStats {
	return xray.InstanceStats{
		InstanceID:     "inst-" + nodeID,
		NodeID:         nodeID,
		CoreGeneration: "gen-1",
		EmailToMember:  map[string]string{"alice@example.test": "member-alice"},
		Counters: []xray.StatCounter{
			{Name: "user>>>alice@example.test>>>traffic>>>uplink", Value: uplink},
			{Name: "user>>>alice@example.test>>>traffic>>>downlink", Value: uplink * 2},
		},
	}
}

func failedNode(nodeID string, err error) xray.InstanceStats {
	return xray.InstanceStats{
		InstanceID:     "inst-" + nodeID,
		NodeID:         nodeID,
		CoreGeneration: "gen-1",
		Err:            err,
	}
}

func newTestSampler(t *testing.T, store *state.Store, source StatsSource, now time.Time) *Sampler {
	t.Helper()
	sampler := NewSampler(store, source, log.New(io.Discard, "", 0))
	sampler.SetNow(func() time.Time { return now })
	return sampler
}

// TestSamplerRetriesTransientStatsQueryFailure covers the real log lines
// "xray api statsquery failed: signal: killed" and "context deadline exceeded":
// one failed query used to drop the whole sampling period.
func TestSamplerRetriesTransientStatsQueryFailure(t *testing.T) {
	store := newStore(t)
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	killed := errors.New("xray api statsquery failed: signal: killed")
	source := &scriptedSource{rounds: [][]xray.InstanceStats{
		{failedNode("node-1", killed)},
		{failedNode("node-1", killed)},
		{sampledNode("node-1", 100)},
	}}
	sampler := newTestSampler(t, store, source, now)
	var waits []time.Duration
	sampler.SetSleep(func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	})
	res, err := sampler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if source.calls != 3 {
		t.Fatalf("statsquery calls = %d, want 3 (first try plus two retries)", source.calls)
	}
	if res.Retries != 2 {
		t.Fatalf("retries = %d, want 2", res.Retries)
	}
	if len(res.SamplingErrors) != 0 || len(res.InstanceErrs) != 0 {
		t.Fatalf("a recovered node must not be reported as failed: %+v", res)
	}
	if res.SampleRows != 1 || len(res.BatchIDs) != 1 {
		t.Fatalf("the recovered sample was not queued: %+v", res)
	}
	if len(waits) != 2 || waits[0] <= 0 || waits[1] <= waits[0] {
		t.Fatalf("the backoff must grow between retries: %v", waits)
	}
	if got := sampler.LastSamplingErrors(); len(got) != 0 {
		t.Fatalf("last sampling errors = %v", got)
	}
	pending, err := store.PendingBatches(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || len(pending[0].Samples) != 1 {
		t.Fatalf("unexpected queue: %+v", pending)
	}
	if sample := pending[0].Samples[0]; sample.UploadBytes != 100 || sample.DownloadBytes != 200 {
		t.Fatalf("unexpected sample: %+v", sample)
	}
}

// TestSamplerRetryBooksEachCycleOnce proves the retries cannot duplicate usage:
// a cycle is measured once with the counters of its successful attempt, and a
// later cycle only books the delta.
func TestSamplerRetryBooksEachCycleOnce(t *testing.T) {
	store := newStore(t)
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	deadline := errors.New("rpc error: code = DeadlineExceeded desc = context deadline exceeded")
	source := &scriptedSource{rounds: [][]xray.InstanceStats{
		{failedNode("node-1", deadline)},
		{failedNode("node-1", deadline)},
		{sampledNode("node-1", 180)},
		{sampledNode("node-1", 250)},
		{sampledNode("node-1", 250)},
	}}
	sampler := newTestSampler(t, store, source, now)
	sampler.SetSleep(func(context.Context, time.Duration) error { return nil })

	first, err := sampler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("first cycle: %v", err)
	}
	if first.SampleRows != 1 {
		t.Fatalf("first cycle sample rows = %d, want 1", first.SampleRows)
	}
	second, err := sampler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	if second.SampleRows != 1 || second.Retries != 0 {
		t.Fatalf("second cycle = %+v, want one row and no retry", second)
	}
	// A third cycle with unchanged counters must book nothing.
	third, err := sampler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("third cycle: %v", err)
	}
	if third.SampleRows != 0 || len(third.BatchIDs) != 0 {
		t.Fatalf("unchanged counters were booked again: %+v", third)
	}

	pending, err := store.PendingBatches(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]contracts.UsageSample, 0, 2)
	for _, b := range pending {
		samples = append(samples, b.Samples...)
	}
	if len(samples) != 2 {
		t.Fatalf("queued samples = %d (%+v), want exactly 2 (one per changing cycle)", len(samples), samples)
	}
	booked := []int64{samples[0].UploadBytes, samples[1].UploadBytes}
	sort.Slice(booked, func(i, j int) bool { return booked[i] < booked[j] })
	if booked[0] != 70 || booked[1] != 180 {
		t.Fatalf("booked usage = %v, want the deltas 70 and 180 once each", booked)
	}
}

// TestSamplerContinuesAfterOneNodeFails is requirement (b): a node that keeps
// failing must not stop the other nodes, and the failure must stay visible.
func TestSamplerContinuesAfterOneNodeFails(t *testing.T) {
	store := newStore(t)
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	down := errors.New("failed to dial 127.0.0.1:20525")
	round := []xray.InstanceStats{failedNode("node-broken", down), sampledNode("node-healthy", 42)}
	source := &scriptedSource{rounds: [][]xray.InstanceStats{round, round, round}}
	sampler := newTestSampler(t, store, source, now)
	sampler.SetSleep(func(context.Context, time.Duration) error { return nil })

	res, err := sampler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Instances != 2 {
		t.Fatalf("instances = %d, want 2", res.Instances)
	}
	if res.SampleRows != 1 || len(res.BatchIDs) != 1 {
		t.Fatalf("the healthy node was not sampled: %+v", res)
	}
	if len(res.SamplingErrors) != 1 {
		t.Fatalf("sampling errors = %v, want one entry for the broken node", res.SamplingErrors)
	}
	entry := res.SamplingErrors[0]
	for _, want := range []string{"sampling_error", "node=node-broken", "instance=inst-node-broken", "attempts=3", "failed to dial"} {
		if !strings.Contains(entry, want) {
			t.Fatalf("sampling error %q does not contain %q", entry, want)
		}
	}
	if len(res.InstanceErrs) != 1 || res.InstanceErrs[0] != entry {
		t.Fatalf("InstanceErrs = %v, want the same entry as SamplingErrors", res.InstanceErrs)
	}
	if got := sampler.LastSamplingErrors(); len(got) != 1 || got[0] != entry {
		t.Fatalf("LastSamplingErrors = %v", got)
	}
	pending, err := store.PendingBatches(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || len(pending[0].Samples) != 1 || pending[0].Samples[0].UploadBytes != 42 {
		t.Fatalf("unexpected queue: %+v", pending)
	}
}

// TestSamplerPersistFailureDoesNotStopOtherNodes covers the second half of (b):
// failing to persist one node must not abort the rest of the cycle either.
func TestSamplerPersistFailureDoesNotStopOtherNodes(t *testing.T) {
	store := newStore(t)
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	source := &scriptedSource{rounds: [][]xray.InstanceStats{{
		sampledNode("node-1", 10),
		sampledNode("node-2", 20),
	}}}
	sampler := newTestSampler(t, store, source, now)
	sampler.SetSleep(func(context.Context, time.Duration) error { return nil })
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	res, err := sampler.RunOnce(context.Background())
	if err == nil {
		t.Fatal("a persistence failure must be returned to the caller")
	}
	if len(res.SamplingErrors) != 2 {
		t.Fatalf("sampling errors = %v, want one entry per node (the cycle must not stop at the first)", res.SamplingErrors)
	}
	for _, entry := range res.SamplingErrors {
		if !strings.Contains(entry, "task=persist") {
			t.Fatalf("unexpected entry: %q", entry)
		}
	}
}

// TestSamplerRetriesAreBounded keeps the retry budget finite: a permanently dead
// node must not make the sampler hammer the API forever.
func TestSamplerRetriesAreBounded(t *testing.T) {
	store := newStore(t)
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	down := []xray.InstanceStats{failedNode("node-down", errors.New("context deadline exceeded"))}
	source := &scriptedSource{rounds: [][]xray.InstanceStats{down}}
	sampler := newTestSampler(t, store, source, now)
	sampler.SetRetryPolicy(2, 0)
	sampler.SetSleep(func(context.Context, time.Duration) error { return nil })
	res, err := sampler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if source.calls != 2 {
		t.Fatalf("statsquery calls = %d, want the configured 2 attempts", source.calls)
	}
	if len(res.SamplingErrors) != 1 {
		t.Fatalf("sampling errors = %v", res.SamplingErrors)
	}
}
