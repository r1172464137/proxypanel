package supervisor

// Regression for the sampling visibility gap.
//
// The retry work for the statsquery failure ("xray api statsquery failed: signal:
// killed") made the sampler survive transient failures, and it collected the
// instances it still could not read into SampleResult.SamplingErrors. Nothing in
// production ever read that field: the supervisor printed it to a log line and
// the heartbeat carried no sampling signal at all. A node whose counter queries
// were failing therefore looked exactly like a node with no traffic - the panel
// showed zero usage and zero errors, which is the same silent-loss symptom the
// retry work was meant to remove.
//
// These tests pin the wiring: a degraded cycle must appear in the heartbeat
// metrics, and a healthy cycle must clear it.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"proxypanel/internal/agent/xray"
	"proxypanel/internal/contracts"
)

// TestSamplingErrorIsVisibleInHeartbeatMetrics proves a cycle that cannot read an
// instance publishes the failure through the heartbeat metrics, not just the log.
func TestSamplingErrorIsVisibleInHeartbeatMetrics(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{
		nodeInstance([]contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test"}}, "alice@example.test"),
	}, 1)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Make the stats query fail for every instance, the way a killed or hung
	// statsquery looks to the sampler.
	env.runner.setStatsError(errors.New("xray api statsquery failed: signal: killed"))

	if err := env.sup.sampleOnce(ctx); err != nil {
		t.Fatalf("sampleOnce must not fail the whole cycle for one unreadable instance: %v", err)
	}

	env.sup.mu.Lock()
	errs := env.sup.metrics["sampling_errors"]
	retries := env.sup.metrics["sampling_retries"]
	env.sup.mu.Unlock()

	if errs < 1 {
		t.Fatalf("sampling_errors metric = %v, want at least 1 after a failing statsquery", errs)
	}
	if retries < 1 {
		t.Fatalf("sampling_retries metric = %v, want at least 1 (the sampler retries before giving up)", retries)
	}

	last := env.sup.LastSamplingErrors()
	if len(last) == 0 {
		t.Fatal("LastSamplingErrors must expose the degraded instances")
	}
	// The message must identify the problem without leaking member identifiers.
	for _, line := range last {
		if !strings.Contains(line, "sampling_error") {
			t.Fatalf("unexpected sampling error format: %q", line)
		}
		if strings.Contains(line, "alice@example.test") || strings.Contains(line, "member-alice") {
			t.Fatalf("sampling error leaks a member identifier: %q", line)
		}
	}
}

// TestSamplingMetricsClearAfterRecovery proves the signal is not sticky: once the
// counters can be read again the heartbeat must stop reporting an error, otherwise
// an operator would see a permanent alert for a transient condition.
func TestSamplingMetricsClearAfterRecovery(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{
		nodeInstance([]contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test"}}, "alice@example.test"),
	}, 1)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	env.runner.setStatsError(errors.New("statsquery failed"))
	if err := env.sup.sampleOnce(ctx); err != nil {
		t.Fatalf("sampleOnce (failing): %v", err)
	}
	env.sup.mu.Lock()
	before := env.sup.metrics["sampling_errors"]
	env.sup.mu.Unlock()
	if before < 1 {
		t.Fatalf("expected a sampling error to be reported first, got %v", before)
	}

	// Recover: real counters with a mapping, so the instance samples cleanly.
	env.runner.setStatsError(nil)
	env.runner.statsAny = []xray.StatCounter{
		{Name: "user>>>alice@example.test>>>traffic>>>uplink", Value: 10},
		{Name: "user>>>alice@example.test>>>traffic>>>downlink", Value: 20},
	}
	if err := env.sup.sampleOnce(ctx); err != nil {
		t.Fatalf("sampleOnce (recovered): %v", err)
	}

	env.sup.mu.Lock()
	after := env.sup.metrics["sampling_errors"]
	retries := env.sup.metrics["sampling_retries"]
	env.sup.mu.Unlock()
	if after != 0 {
		t.Fatalf("sampling_errors must return to 0 after recovery, got %v", after)
	}
	if retries != 0 {
		t.Fatalf("sampling_retries must return to 0 after recovery, got %v", retries)
	}
	if last := env.sup.LastSamplingErrors(); len(last) != 0 {
		t.Fatalf("LastSamplingErrors must be cleared after recovery, got %v", last)
	}
}

// TestSamplingMetricsReachTheHeartbeatPayload closes the loop: the metric has to
// be transmitted, not merely stored on the struct.
func TestSamplingMetricsReachTheHeartbeatPayload(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	env.panel.setSnapshot("gen-1", 1, []contracts.Instance{
		nodeInstance([]contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test"}}, "alice@example.test"),
	}, 1)
	if err := env.sup.syncOnce(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	env.runner.setStatsError(errors.New("statsquery failed"))
	if err := env.sup.sampleOnce(ctx); err != nil {
		t.Fatalf("sampleOnce: %v", err)
	}
	if err := env.sup.heartbeatOnce(ctx); err != nil {
		t.Fatalf("heartbeatOnce: %v", err)
	}

	env.panel.mu.Lock()
	defer env.panel.mu.Unlock()
	var found bool
	for _, hb := range env.panel.heartbeats {
		if v, ok := hb.Metrics["sampling_errors"]; ok && v >= 1 {
			found = true
			if _, hasRetries := hb.Metrics["sampling_retries"]; !hasRetries {
				t.Fatal("the heartbeat must also carry sampling_retries")
			}
		}
	}
	if !found {
		t.Fatalf("no heartbeat carried sampling_errors; heartbeats: %d", len(env.panel.heartbeats))
	}
}
