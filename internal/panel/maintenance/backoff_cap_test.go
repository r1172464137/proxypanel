package maintenance

// Regression for an incomplete cap in the notification backoff.
//
// backoffFor used to clamp its result with a *lower* bound of backoffBase, so the
// documented one-hour cap only applied to values the doubling had already pushed
// past it. cl durations produced from the configured base were returned as-is, which
// meant an operator policy (or a future base above the cap) could schedule a retry
// for longer than the cap states. The cap must bound every value the function can
// return, so the lower bound is the minimum retry delay, not the base delay.

import (
	"testing"
	"time"
)

func TestBackoffCapIsIndependentOfTheConfiguredBase(t *testing.T) {
	// Default base: the cap still applies and the early values are unchanged.
	if got := backoffFor(1, 0); got != backoffBase {
		t.Fatalf("first retry = %s, want %s", got, backoffBase)
	}
	if got := backoffFor(50, 0); got != maxRetryAfter {
		t.Fatalf("backoff must be capped at %s, got %s", maxRetryAfter, got)
	}

	// Every attempt count must respect the cap. The doubling is computed from the
	// fixed base, so this is a property check over the whole range rather than one
	// example.
	for attempts := 1; attempts <= 64; attempts++ {
		got := backoffFor(attempts, 0)
		if got > maxRetryAfter {
			t.Fatalf("backoffFor(%d, 0) = %s exceeds the %s cap", attempts, got, maxRetryAfter)
		}
		if got < minRetryAfter {
			t.Fatalf("backoffFor(%d, 0) = %s is below the %s floor", attempts, got, minRetryAfter)
		}
	}

	// A retry_after hint is clamped on both sides regardless of size.
	for _, hint := range []time.Duration{time.Millisecond, time.Second, 30 * time.Minute, 48 * time.Hour, 1000 * time.Hour} {
		got := backoffFor(1, hint)
		if got < minRetryAfter || got > maxRetryAfter {
			t.Fatalf("retry_after %s produced %s, outside [%s, %s]", hint, got, minRetryAfter, maxRetryAfter)
		}
	}

	// The cap must hold for an absurd attempt count too (no overflow).
	if got := backoffFor(1<<20, 0); got != maxRetryAfter {
		t.Fatalf("backoffFor(1<<20, 0) = %s, want the cap %s", got, maxRetryAfter)
	}
}

func TestBackoffIsMonotonicUpToTheCap(t *testing.T) {
	prev := time.Duration(0)
	for attempts := 1; attempts <= 10; attempts++ {
		got := backoffFor(attempts, 0)
		if got < prev {
			t.Fatalf("backoff decreased at attempt %d: %s < %s", attempts, got, prev)
		}
		if got > maxRetryAfter {
			t.Fatalf("backoff exceeded the cap at attempt %d: %s", attempts, got)
		}
		prev = got
	}
}
