package limit

import (
	"context"
	"testing"
	"time"
)

func TestSecurityFullTableDoesNotDisableUnrelatedKeys(t *testing.T) {
	l, _ := New("")
	defer l.Close()
	ctx := context.Background()
	for i := 0; i < MaxLocalKeys; i++ {
		if ok, err := l.Allow(ctx, "fill:"+itoa(i), 5, time.Minute); !ok || err != nil {
			t.Fatalf("fill %d", i)
		}
	}
	for _, key := range []string{"fill:0", "unrelated-legitimate-source"} {
		if ok, err := l.Allow(ctx, key, 5, time.Minute); !ok || err != nil {
			t.Errorf("full table denied unrelated/previously admitted key: %q", key)
		}
	}
	if len(l.local) > MaxLocalKeys {
		t.Fatal("unbounded fallback state")
	}
}

// A full exact table must not turn one new key into a full scan of every other
// entry: a capacity miss may only reclaim a bounded number of expired keys.
func TestSecurityFullTableScanIsBoundedPerRequest(t *testing.T) {
	l, _ := New("")
	defer l.Close()
	now := time.Now().UTC()
	l.clock = func() time.Time { return now }
	ctx := context.Background()
	for i := 0; i < MaxLocalKeys; i++ {
		if ok, err := l.Allow(ctx, "fill:"+itoa(i), 5, time.Minute); !ok || err != nil {
			t.Fatalf("fill %d", i)
		}
	}
	// Every entry is now expired: a single miss must not be allowed to walk
	// the whole map (that is the CPU amplification this bound prevents).
	now = now.Add(2 * time.Minute)
	if _, err := l.Allow(ctx, "new:0", 5, time.Minute); err != nil {
		t.Fatal(err)
	}
	// The production sweep is a small fixed budget; anything close to the table
	// size means the miss walked the whole map again.
	const maxPerMiss = 512
	if n := MaxLocalKeys - len(l.local); n == 0 || n > maxPerMiss {
		t.Fatalf("one capacity miss reclaimed %d of %d expired entries (bound %d)", n, MaxLocalKeys, maxPerMiss)
	}
	// Repeated misses still converge: the table drains as later requests arrive.
	for i := 1; i <= MaxLocalKeys/16; i++ {
		if _, err := l.Allow(ctx, "new:"+itoa(i), 5, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if len(l.local) >= MaxLocalKeys {
		t.Fatal("expired entries were never reclaimed")
	}
}

// Overflow counters must still enforce the per-key limit (shared hashed buckets
// may reject earlier, never later) and must recover once the window passed.
func TestSecurityOverflowStillEnforcesLimitAndExpires(t *testing.T) {
	l, _ := New("")
	defer l.Close()
	now := time.Now().UTC()
	l.clock = func() time.Time { return now }
	ctx := context.Background()
	for i := 0; i < MaxLocalKeys; i++ {
		if ok, err := l.Allow(ctx, "fill:"+itoa(i), 5, time.Minute); !ok || err != nil {
			t.Fatalf("fill %d", i)
		}
	}
	allowed := 0
	for i := 0; i < 10; i++ {
		if ok, err := l.Allow(ctx, "overflow-key", 3, time.Minute); err == nil && ok {
			allowed++
		}
	}
	if allowed == 0 || allowed > 3 {
		t.Fatalf("overflow allowed %d attempts, want 1..3 (limit 3)", allowed)
	}
	if ok, err := l.Allow(ctx, "overflow-key", 3, time.Minute); err != nil || ok {
		t.Fatalf("overflow kept allowing past its limit: ok=%v err=%v", ok, err)
	}
	now = now.Add(2 * time.Minute)
	if ok, err := l.Allow(ctx, "overflow-key", 3, time.Minute); err != nil || !ok {
		t.Fatalf("overflow window did not expire: ok=%v err=%v", ok, err)
	}
}

// Existing keys keep exact accounting even while the table is full and the
// overflow path is active.
func TestSecurityExactBucketsSurviveOverflowPressure(t *testing.T) {
	l, _ := New("")
	defer l.Close()
	ctx := context.Background()
	for i := 0; i < MaxLocalKeys; i++ {
		if ok, err := l.Allow(ctx, "fill:"+itoa(i), 5, time.Minute); !ok || err != nil {
			t.Fatalf("fill %d", i)
		}
	}
	// fill:0 already used one of its five attempts.
	for i := 0; i < 4; i++ {
		if ok, err := l.Allow(ctx, "fill:0", 5, time.Minute); !ok || err != nil {
			t.Fatalf("existing key attempt %d: ok=%v err=%v", i+2, ok, err)
		}
	}
	if ok, _ := l.Allow(ctx, "fill:0", 5, time.Minute); ok {
		t.Fatal("existing key exceeded its own limit under overflow pressure")
	}
}
