// Package limit implements the login rate limiter. Redis is the primary
// counter; when Redis is unavailable the limiter degrades to a bounded local
// counter and never silently allows unlimited attempts.
package limit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// MaxLocalKeys bounds the local fallback table; overflowing it denies requests
// (fail closed) instead of growing without limit.
const MaxLocalKeys = 4096

type bucket struct {
	count   int
	expires time.Time
}

// Limiter is safe for concurrent use.
type Limiter struct {
	rdb   *redis.Client
	clock func() time.Time

	mu    sync.Mutex
	local map[string]bucket

	degraded bool
	lastErr  string
}

// New builds a limiter from a redis URL. An empty URL means "no Redis": the
// limiter still enforces limits locally and reports the degraded state.
func New(redisURL string) (*Limiter, error) {
	l := &Limiter{clock: func() time.Time { return time.Now().UTC() }, local: map[string]bucket{}}
	if strings.TrimSpace(redisURL) == "" {
		return l, nil
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("redis URL: %w", err)
	}
	opts.MaxRetries = 1
	opts.DialTimeout = 2 * time.Second
	opts.ReadTimeout = 2 * time.Second
	opts.WriteTimeout = 2 * time.Second
	l.rdb = redis.NewClient(opts)
	return l, nil
}

// Close releases the Redis client.
func (l *Limiter) Close() error {
	if l.rdb != nil {
		return l.rdb.Close()
	}
	return nil
}

// Allow reports whether the key may proceed. A Redis failure falls back to the
// local counter; if even the fallback cannot decide, access is denied.
func (l *Limiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	if limit <= 0 {
		return true, nil
	}
	if window <= 0 {
		window = time.Minute
	}
	if l.rdb != nil {
		allowed, err := l.allowRedis(ctx, key, limit, window)
		if err == nil {
			return allowed, nil
		}
		l.setDegraded(err)
		return l.allowLocal(key, limit, window)
	}
	l.setDegraded(errors.New("redis not configured"))
	return l.allowLocal(key, limit, window)
}

func (l *Limiter) allowRedis(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	pipe := l.rdb.Pipeline()
	incr := pipe.Incr(ctx, "pp:rl:"+key)
	pipe.Expire(ctx, "pp:rl:"+key, window)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, err
	}
	count := incr.Val()
	l.clearDegraded()
	return count <= int64(limit), nil
}

func (l *Limiter) allowLocal(key string, limit int, window time.Duration) (bool, error) {
	now := l.clock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.local) >= MaxLocalKeys {
		for k, b := range l.local {
			if !b.expires.After(now) {
				delete(l.local, k)
			}
		}
		if len(l.local) >= MaxLocalKeys {
			return false, errors.New("rate limiter local table is full")
		}
	}
	b, ok := l.local[key]
	if !ok || !b.expires.After(now) {
		b = bucket{count: 0, expires: now.Add(window)}
	}
	b.count++
	l.local[key] = b
	return b.count <= limit, nil
}

func (l *Limiter) setDegraded(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.degraded = true
	if err != nil {
		l.lastErr = err.Error()
	}
}

func (l *Limiter) clearDegraded() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.degraded = false
	l.lastErr = ""
}

// Status reports the current Redis state for /readyz: ok, unavailable or
// disabled (no Redis configured). It always pings so a recovered Redis is
// reported truthfully.
func (l *Limiter) Status(ctx context.Context) string {
	if l.rdb == nil {
		return "disabled"
	}
	pingCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := l.rdb.Ping(pingCtx).Err(); err != nil {
		l.setDegraded(err)
		return "unavailable"
	}
	l.clearDegraded()
	return "ok"
}

// RejectKey is the client visible marker used by tests and metrics.
func RejectKey(key string) string { return "rate_limited:" + key }
