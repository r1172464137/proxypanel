package store

import (
	"testing"
	"time"

	"proxypanel/internal/panel/domain"
)

// The cycle rule is frozen: a running cycle is never recomputed, and a rule
// submitted while the cycle was running only takes effect at the next boundary
// (plan §F08, contract "直接改规则/换订阅均保留当前周期").
func TestComputeAdvanceKeepsRunningCycle(t *testing.T) {
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	pending := &domain.PeriodRule{AnchorDay: 5, Timezone: "UTC"}
	m := &domain.Member{
		PeriodStart: &start, PeriodEnd: &end, PeriodAnchorDay: 1, PeriodTimezone: "UTC",
		PendingPeriodRule: pending,
	}
	// The subscription was edited to a different rule: it must not move the
	// running cycle.
	sub := &domain.Subscription{AnchorDay: 10, Timezone: "UTC"}

	adv, err := computeAdvance(m, sub, now)
	if err != nil {
		t.Fatalf("computeAdvance: %v", err)
	}
	if adv.Advanced {
		t.Fatal("a running cycle must not be rolled forward")
	}
	if !adv.Start.Equal(start) || !adv.End.Equal(end) {
		t.Fatalf("running cycle changed: %s..%s", adv.Start, adv.End)
	}
	if adv.Pending == nil || adv.Pending.AnchorDay != 5 {
		t.Fatal("a pending rule must stay pending while the cycle is running")
	}
}

func TestComputeAdvanceRollsEndedCycleToContainingWindow(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	m := &domain.Member{PeriodStart: &start, PeriodEnd: &end, PeriodAnchorDay: 1, PeriodTimezone: "UTC"}

	adv, err := computeAdvance(m, &domain.Subscription{AnchorDay: 1, Timezone: "UTC"}, now)
	if err != nil {
		t.Fatalf("computeAdvance: %v", err)
	}
	if !adv.Advanced {
		t.Fatal("an ended cycle must be rolled forward")
	}
	if want := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC); !adv.Start.Equal(want) {
		t.Fatalf("new cycle start = %s, want %s", adv.Start, want)
	}
	if want := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC); !adv.End.Equal(want) {
		t.Fatalf("new cycle end = %s, want %s", adv.End, want)
	}
	if adv.RuleApplied || adv.Pending != nil {
		t.Fatal("no pending rule existed, nothing may be applied")
	}
}

func TestComputeAdvanceAppliesPendingRuleAtBoundary(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	m := &domain.Member{
		PeriodStart: &start, PeriodEnd: &end, PeriodAnchorDay: 1, PeriodTimezone: "UTC",
		PendingPeriodRule: &domain.PeriodRule{AnchorDay: 15, Timezone: "UTC"},
	}

	adv, err := computeAdvance(m, &domain.Subscription{AnchorDay: 1, Timezone: "UTC"}, now)
	if err != nil {
		t.Fatalf("computeAdvance: %v", err)
	}
	if !adv.Advanced || !adv.RuleApplied {
		t.Fatalf("pending rule must take effect at the boundary: %+v", adv)
	}
	if adv.Pending != nil {
		t.Fatal("the applied pending rule must be cleared")
	}
	if adv.Anchor != 15 {
		t.Fatalf("anchor = %d, want the pending 15", adv.Anchor)
	}
	// anchor 15 with now on the 20th means the cycle runs 15th..15th.
	if want := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC); !adv.Start.Equal(want) {
		t.Fatalf("new cycle start = %s, want %s", adv.Start, want)
	}
	if want := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC); !adv.End.Equal(want) {
		t.Fatalf("new cycle end = %s, want %s", adv.End, want)
	}
}

// A member without a persisted cycle is never handed a fresh one by a read or a
// background pass: that would silently reset the quota.
func TestComputeAdvanceLeavesMissingCycleAlone(t *testing.T) {
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	adv, err := computeAdvance(&domain.Member{}, &domain.Subscription{AnchorDay: 10, Timezone: "UTC"}, now)
	if err != nil {
		t.Fatalf("computeAdvance: %v", err)
	}
	if adv.Advanced {
		t.Fatal("a member without a cycle must not get one implicitly")
	}
	// A half persisted cycle (no end) is equally untouchable.
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	adv, err = computeAdvance(&domain.Member{PeriodStart: &start}, &domain.Subscription{AnchorDay: 1, Timezone: "UTC"}, now)
	if err != nil {
		t.Fatalf("computeAdvance: %v", err)
	}
	if adv.Advanced {
		t.Fatal("a member without a cycle end must not be advanced")
	}
}

func TestComputeAdvanceWithoutBindingOrRuleIsNoop(t *testing.T) {
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	adv, err := computeAdvance(&domain.Member{}, nil, now)
	if err != nil {
		t.Fatalf("computeAdvance: %v", err)
	}
	if adv.Advanced {
		t.Fatal("a member without a binding has no cycle to advance")
	}
}

// anchor_day 31 must clip through short months instead of failing.
func TestComputeAdvanceClipsShortMonths(t *testing.T) {
	start := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 2, 28, 1, 0, 0, 0, time.UTC)
	m := &domain.Member{PeriodStart: &start, PeriodEnd: &end, PeriodAnchorDay: 31, PeriodTimezone: "UTC"}

	adv, err := computeAdvance(m, nil, now)
	if err != nil {
		t.Fatalf("computeAdvance: %v", err)
	}
	if !adv.Advanced {
		t.Fatal("the cycle ended at the clipped boundary and must roll forward")
	}
	if want := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC); !adv.Start.Equal(want) {
		t.Fatalf("new cycle start = %s, want %s", adv.Start, want)
	}
	if want := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC); !adv.End.Equal(want) {
		t.Fatalf("new cycle end = %s, want %s", adv.End, want)
	}
}

func TestComputeAdvanceRejectsUnknownTimezone(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	m := &domain.Member{PeriodStart: &start, PeriodEnd: &end, PeriodAnchorDay: 1, PeriodTimezone: "Mars/Olympus"}
	if _, err := computeAdvance(m, nil, now); err == nil {
		t.Fatal("an unusable cycle timezone must be an error, never a silent guess")
	}
}

func TestComputeAdvanceNeverOverlapsPreviousCycle(t *testing.T) {
	parse := func(s string) time.Time {
		t.Helper()
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, tc := range []struct {
		name, oldStart, oldEnd, at, zone string
		oldAnchor, nextAnchor            int
		wantStart, wantEnd               string
	}{
		{"anchor-change", "2026-03-15T00:00:00Z", "2026-04-15T00:00:00Z", "2026-04-15T00:00:00Z", "UTC", 15, 31, "2026-04-15T00:00:00Z", "2026-04-30T00:00:00Z"},
		{"clipped-month", "2026-01-31T00:00:00Z", "2026-02-28T00:00:00Z", "2026-02-28T00:00:00Z", "UTC", 31, 1, "2026-02-28T00:00:00Z", "2026-03-01T00:00:00Z"},
		{"timezone-east", "2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z", "2026-11-01T00:00:00Z", "Asia/Shanghai", 1, 1, "2026-11-01T00:00:00Z", "2026-11-30T16:00:00Z"},
		{"timezone-west", "2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z", "2026-11-01T01:00:00Z", "America/New_York", 1, 1, "2026-11-01T00:00:00Z", "2026-11-01T04:00:00Z"},
		{"exact-short-cycle-end", "2026-03-15T00:00:00Z", "2026-04-15T00:00:00Z", "2026-04-30T00:00:00Z", "UTC", 15, 31, "2026-04-30T00:00:00Z", "2026-05-31T00:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, end, at := parse(tc.oldStart), parse(tc.oldEnd), parse(tc.at)
			m := &domain.Member{
				PeriodStart: &start, PeriodEnd: &end, PeriodAnchorDay: tc.oldAnchor, PeriodTimezone: "UTC",
				PendingPeriodRule: &domain.PeriodRule{AnchorDay: tc.nextAnchor, Timezone: tc.zone},
			}
			got, err := computeAdvance(m, nil, at)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Advanced || !got.RuleApplied || got.Pending != nil {
				t.Fatalf("pending rule not consumed at the old boundary: %+v", got)
			}
			if !got.Start.Equal(parse(tc.wantStart)) || !got.End.Equal(parse(tc.wantEnd)) {
				t.Fatalf("transition = [%s,%s), want [%s,%s)", got.Start, got.End, tc.wantStart, tc.wantEnd)
			}
			if got.Start.Before(parse(tc.oldEnd)) || at.Before(got.Start) || !at.Before(got.End) {
				t.Fatalf("transition overlaps history or does not contain now: %+v", got)
			}
			if !m.PeriodStart.Equal(parse(tc.oldStart)) || !m.PeriodEnd.Equal(parse(tc.oldEnd)) || m.PendingPeriodRule == nil {
				t.Fatal("pure cycle calculation mutated persisted input")
			}
		})
	}
}

func TestComputeAdvanceAppliesTimezoneEditAtFirstBoundaryExactly(t *testing.T) {
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	m := &domain.Member{
		PeriodStart: &start, PeriodEnd: &end, PeriodAnchorDay: 1, PeriodTimezone: "UTC",
		PendingPeriodRule: &domain.PeriodRule{AnchorDay: 15, Timezone: "Asia/Tokyo"},
	}
	sub := &domain.Subscription{AnchorDay: 15, Timezone: "Asia/Tokyo"}
	before, err := computeAdvance(m, sub, end.Add(-time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	if before.Advanced || before.RuleApplied || before.Timezone != "UTC" || before.Pending == nil ||
		!before.Start.Equal(start) || !before.End.Equal(end) {
		t.Fatalf("edit changed the current cycle before its boundary: %+v", before)
	}
	at, err := computeAdvance(m, sub, end)
	if err != nil {
		t.Fatal(err)
	}
	if !at.Advanced || !at.RuleApplied || at.Pending != nil || at.Anchor != 15 || at.Timezone != "Asia/Tokyo" {
		t.Fatalf("edit not applied at the first boundary: %+v", at)
	}
	wantStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC) // New rules cannot move the start into the old cycle.
	wantEnd := time.Date(2026, 4, 14, 15, 0, 0, 0, time.UTC)
	if !at.Start.Equal(wantStart) || !at.End.Equal(wantEnd) {
		t.Fatalf("new window = %s..%s, want %s..%s", at.Start, at.End, wantStart, wantEnd)
	}
}
