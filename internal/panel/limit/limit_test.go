package limit

import (
	"context"
	"testing"
	"time"
)

func TestLocalFallbackEnforcesLimit(t *testing.T) {
	l, err := New("")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer l.Close()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		ok, err := l.Allow(ctx, "login:1.2.3.4:alice", 3, time.Minute)
		if err != nil || !ok {
			t.Fatalf("attempt %d should be allowed: ok=%v err=%v", i+1, ok, err)
		}
	}
	ok, err := l.Allow(ctx, "login:1.2.3.4:alice", 3, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("fourth attempt must be rate limited")
	}
}

func TestLocalWindowExpires(t *testing.T) {
	l, err := New("")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer l.Close()
	now := time.Now().UTC()
	l.clock = func() time.Time { return now }
	ctx := context.Background()
	if ok, _ := l.Allow(ctx, "k", 1, time.Minute); !ok {
		t.Fatal("first attempt must pass")
	}
	if ok, _ := l.Allow(ctx, "k", 1, time.Minute); ok {
		t.Fatal("second attempt in the same window must fail")
	}
	now = now.Add(61 * time.Second)
	if ok, _ := l.Allow(ctx, "k", 1, time.Minute); !ok {
		t.Fatal("window must have expired")
	}
}

func TestLocalOverflowRemainsBoundedAndEnforcesLimit(t *testing.T) {
	l, err := New("")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer l.Close()
	ctx := context.Background()
	for i := 0; i < MaxLocalKeys; i++ {
		if ok, err := l.Allow(ctx, "key"+itoa(i), 5, time.Hour); !ok || err != nil {
			t.Fatalf("fill %d: ok=%v err=%v", i, ok, err)
		}
	}
	for i := 0; i < 6; i++ {
		ok, err := l.Allow(ctx, "overflow", 5, time.Hour)
		if err != nil || ok != (i < 5) {
			t.Fatalf("overflow attempt %d: allowed=%v err=%v", i+1, ok, err)
		}
	}
	if len(l.local) != MaxLocalKeys {
		t.Fatal("overflow grew the exact-key table")
	}
}

func TestStatusReportsUnavailableRedisAndDegrades(t *testing.T) {
	// Port 1 on loopback is never a Redis server: the limiter must fall back
	// locally instead of allowing unlimited attempts.
	l, err := New("redis://127.0.0.1:1/0")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer l.Close()
	ctx := context.Background()
	if got := l.Status(ctx); got != "unavailable" {
		t.Fatalf("status = %q, want unavailable", got)
	}
	allowed := 0
	for i := 0; i < 5; i++ {
		ok, err := l.Allow(ctx, "login:test", 2, time.Minute)
		if err != nil {
			t.Fatalf("allow must degrade, not fail: %v", err)
		}
		if ok {
			allowed++
		}
	}
	if allowed != 2 {
		t.Fatalf("local fallback allowed %d attempts, want 2", allowed)
	}
}

func TestStatusDisabledWithoutRedis(t *testing.T) {
	l, _ := New("")
	defer l.Close()
	if got := l.Status(context.Background()); got != "disabled" {
		t.Fatalf("status = %q, want disabled", got)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
