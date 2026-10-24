//go:build measurement_candidate

// Independent disputed accounting policy evidence; baseline reset assertions remain unchanged.
package state

import (
	"context"
	"testing"
	"time"
)

func TestReviewOutOfOrderFinalAndPeriodicSamplesOverbill(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	// Newer final snapshot is committed before the older periodic snapshot.
	for i, v := range []int64{100, 200, 150} {
		if _, err := s.Measure(ctx, "node-review", "gen-review", now.Add(time.Duration(i)*time.Second), []CounterItem{{MemberID: "member-review", Counter: counterUp, Value: v}}); err != nil {
			t.Fatal(err)
		}
	}
	batches, err := s.PendingBatches(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, b := range batches {
		for _, smp := range b.Samples {
			total += smp.UploadBytes
		}
	}
	if total != 200 {
		t.Fatalf("stale same-generation sample must not bill again: total=%d want=200", total)
	}
	t.Logf("same generation cumulative max=200, billed total=%d", total)
}
