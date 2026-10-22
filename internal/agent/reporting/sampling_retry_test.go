package reporting

// Regression tests for the sampling retry policy.
//
// The failure that motivated it on a real machine was:
//
//	sampling: ... xray api statsquery failed: signal: killed
//	sampling: ... DeadlineExceeded
//	sampling: ... failed to dial 127.0.0.1:20525
//
// A single transient statsquery failure used to lose the whole node's traffic for
// that cycle. The sampler now retries with a bounded backoff, isolates failures
// per instance, and surfaces a still-failing instance instead of swallowing it.
//
// The invariant that makes retrying safe is that a cycle measures each instance
// exactly once no matter how many attempts it took: those assertions below are as
// important as the retry ones, because a retry that booked the same cumulative
// counter twice would double-bill a member.

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"proxypanel/internal/agent/state"
	"proxypanel/internal/agent/xray"
)

// flakySource answers per attempt: each entry is the whole QueryStats reply for
// that round. The last entry is repeated if more rounds are requested.
type flakySource struct {
	mu     sync.Mutex
	rounds [][]xray.InstanceStats
	calls  int
}

func (f *flakySource) QueryStats(ctx context.Context) []xray.InstanceStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	idx := f.calls - 1
	if idx >= len(f.rounds) {
		idx = len(f.rounds) - 1
	}
	return f.rounds[idx]
}

func (f *flakySource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// recordingSleep captures the requested backoff without waiting for it.
type recordingSleep struct {
	mu     sync.Mutex
	delays []time.Duration
	err    error
}

func (r *recordingSleep) sleep(ctx context.Context, d time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delays = append(r.delays, d)
	return r.err
}

func (r *recordingSleep) recorded() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.delays...)
}

func aliceStats(instanceID, nodeID string, up, down int64) xray.InstanceStats {
	return xray.InstanceStats{
		InstanceID:     instanceID,
		NodeID:         nodeID,
		CoreGeneration: "gen-1",
		EmailToMember:  map[string]string{"alice@example.test": "member-alice"},
		Counters: []xray.StatCounter{
			{Name: "user>>>alice@example.test>>>traffic>>>uplink", Value: up},
			{Name: "user>>>alice@example.test>>>traffic>>>downlink", Value: down},
		},
	}
}

func failingStats(instanceID, nodeID string, err error) xray.InstanceStats {
	return xray.InstanceStats{InstanceID: instanceID, NodeID: nodeID, Err: err}
}

func newRecordingSampler(t *testing.T, source StatsSource) (*Sampler, *recordingSleep, *state.Store) {
	t.Helper()
	store := newStore(t)
	sampler := NewSampler(store, source, log.New(io.Discard, "", 0))
	sampler.SetNow(func() time.Time { return time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC) })
	sleeper := &recordingSleep{}
	sampler.SetSleep(sleeper.sleep)
	return sampler, sleeper, store
}

// The second attempt succeeding must book the traffic, report the retry, and not
// leave a stale sampling_error behind.
func TestSamplerRetriesAndSucceeds(t *testing.T) {
	source := &flakySource{rounds: [][]xray.InstanceStats{
		{failingStats("i1", "n1", errors.New("statsquery failed: signal: killed"))},
		{aliceStats("i1", "n1", 100, 200)},
	}}
	sampler, sleeper, _ := newRecordingSampler(t, source)

	res, err := sampler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Retries != 1 {
		t.Fatalf("Retries = %d, want 1", res.Retries)
	}
	if len(res.SamplingErrors) != 0 {
		t.Fatalf("a recovered instance must not be reported as a sampling error: %v", res.SamplingErrors)
	}
	if len(sampler.LastSamplingErrors()) != 0 {
		t.Fatalf("LastSamplingErrors must be cleared after recovery, got %v", sampler.LastSamplingErrors())
	}
	if res.SampleRows != 1 {
		t.Fatalf("SampleRows = %d, want 1 (the recovered attempt must be booked exactly once)", res.SampleRows)
	}
	if got := sleeper.recorded(); len(got) != 1 || got[0] != DefaultSampleBackoff {
		t.Fatalf("backoff delays = %v, want [%v]", got, DefaultSampleBackoff)
	}
	// Recovery must stop the retry loop: a third round would be wasted work.
	if calls := source.callCount(); calls != 2 {
		t.Fatalf("statsquery rounds = %d, want 2 (retry must stop as soon as every instance resolved)", calls)
	}
}

// An instance that fails every round must be reported, and the error must be
// readable without leaking member identifiers or URLs.
func TestSamplerPersistentFailureIsSurfaced(t *testing.T) {
	source := &flakySource{rounds: [][]xray.InstanceStats{
		{failingStats("i1", "n1", errors.New("dial tcp 127.0.0.1:20525: connect: connection refused"))},
	}}
	sampler, sleeper, _ := newRecordingSampler(t, source)

	res, err := sampler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(res.SamplingErrors) != 1 {
		t.Fatalf("SamplingErrors = %v, want exactly 1 entry", res.SamplingErrors)
	}
	line := res.SamplingErrors[0]
	for _, want := range []string{"sampling_error", "node=n1", "instance=i1", "attempts=3", "connection refused"} {
		if !strings.Contains(line, want) {
			t.Fatalf("sampling error %q is missing %q", line, want)
		}
	}
	for _, leak := range []string{"alice@example.test", "member-alice"} {
		if strings.Contains(line, leak) {
			t.Fatalf("sampling error leaks %q: %s", leak, line)
		}
	}
	// The queryable twin must agree, and must be a copy rather than a live slice.
	last := sampler.LastSamplingErrors()
	if len(last) != 1 || last[0] != line {
		t.Fatalf("LastSamplingErrors = %v, want the same single entry", last)
	}
	last[0] = "mutated by caller"
	if again := sampler.LastSamplingErrors(); again[0] != line {
		t.Fatal("LastSamplingErrors must return a copy; a caller mutated the sampler state")
	}
	if calls := source.callCount(); calls != DefaultSampleAttempts {
		t.Fatalf("statsquery rounds = %d, want %d", calls, DefaultSampleAttempts)
	}
	if got := sleeper.recorded(); len(got) != DefaultSampleAttempts-1 {
		t.Fatalf("sleep calls = %d, want %d", len(got), DefaultSampleAttempts-1)
	}
}

// A failing instance must not disturb a healthy one, and the healthy one must be
// measured exactly once despite the retries happening around it.
func TestSamplerInstanceFailureIsIsolated(t *testing.T) {
	source := &flakySource{rounds: [][]xray.InstanceStats{
		{
			failingStats("i-bad", "n-bad", errors.New("statsquery failed: signal: killed")),
			aliceStats("i-good", "n-good", 10, 20),
		},
	}}
	sampler, _, store := newRecordingSampler(t, source)

	res, err := sampler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(res.SamplingErrors) != 1 || !strings.Contains(res.SamplingErrors[0], "instance=i-bad") {
		t.Fatalf("the failing instance must be reported alone: %v", res.SamplingErrors)
	}
	if res.SampleRows != 1 {
		t.Fatalf("SampleRows = %d, want 1 (only the healthy instance books)", res.SampleRows)
	}
	if strings.Contains(strings.Join(res.SamplingErrors, " "), "i-good") {
		t.Fatal("the healthy instance must not appear in the sampling errors")
	}

	// Three retry rounds saw the same cumulative counters; the measurement must
	// still be booked once. A second cycle with unchanged counters books nothing.
	again, err := sampler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if again.SampleRows != 0 {
		t.Fatalf("an unchanged counter must book nothing on the next cycle, booked %d", again.SampleRows)
	}
	_ = store
}

// The backoff doubles and is capped, so a long outage cannot turn into a hot loop
// or an unbounded wait.
func TestSamplerBackoffDoublesAndCaps(t *testing.T) {
	source := &flakySource{rounds: [][]xray.InstanceStats{
		{failingStats("i1", "n1", errors.New("boom"))},
	}}
	store := newStore(t)
	sampler := NewSampler(store, source, log.New(io.Discard, "", 0))
	sampler.SetNow(func() time.Time { return time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC) })
	sleeper := &recordingSleep{}
	sampler.SetSleep(sleeper.sleep)
	sampler.SetRetryPolicy(8, DefaultSampleBackoff)

	if _, err := sampler.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	got := sleeper.recorded()
	want := []time.Duration{
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		10 * time.Second, // capped
		10 * time.Second,
	}
	if len(got) != len(want) {
		t.Fatalf("backoff delays = %v, want %d entries", got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("backoff[%d] = %v, want %v (full: %v)", i, got[i], want[i], got)
		}
	}
	if maxSampleBackoff != 10*time.Second {
		t.Fatalf("maxSampleBackoff = %v, want 10s", maxSampleBackoff)
	}
}

// A cancelled cycle must stop retrying instead of sleeping through the remaining
// rounds, and it must still report the instances it could not read.
func TestSamplerCancellationStopsRetrying(t *testing.T) {
	source := &flakySource{rounds: [][]xray.InstanceStats{
		{failingStats("i1", "n1", errors.New("boom"))},
	}}
	store := newStore(t)
	sampler := NewSampler(store, source, log.New(io.Discard, "", 0))
	sampler.SetNow(func() time.Time { return time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC) })
	sleeper := &recordingSleep{err: context.Canceled}
	sampler.SetSleep(sleeper.sleep)

	res, err := sampler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if source.callCount() != 1 {
		t.Fatalf("statsquery rounds = %d, want 1 (a cancelled wait must not trigger another round)", source.callCount())
	}
	if len(res.SamplingErrors) != 1 {
		t.Fatalf("the unread instance must still be reported: %v", res.SamplingErrors)
	}
	// A candidate only attempted once here, so the reported attempt count is 1.
	if !strings.Contains(res.SamplingErrors[0], "attempts=1") {
		t.Fatalf("attempts must reflect the rounds actually run: %s", res.SamplingErrors[0])
	}
}

// Attempts=1 must disable retrying entirely (an operator escape hatch).
func TestSamplerSingleAttemptDisablesRetries(t *testing.T) {
	source := &flakySource{rounds: [][]xray.InstanceStats{
		{failingStats("i1", "n1", errors.New("boom"))},
	}}
	sampler, sleeper, _ := newRecordingSampler(t, source)
	sampler.SetRetryPolicy(1, DefaultSampleBackoff)

	res, err := sampler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if source.callCount() != 1 || res.Retries != 0 || len(sleeper.recorded()) != 0 {
		t.Fatalf("attempts=1 must not retry: calls=%d retries=%d sleeps=%v",
			source.callCount(), res.Retries, sleeper.recorded())
	}
	if len(res.SamplingErrors) != 1 || !strings.Contains(res.SamplingErrors[0], "attempts=1") {
		t.Fatalf("SamplingErrors = %v", res.SamplingErrors)
	}
}
