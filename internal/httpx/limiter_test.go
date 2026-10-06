package httpx

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

type testClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func newTestClock() *testClock { return &testClock{now: time.Unix(1_700_000_000, 0)} }

func (clock *testClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *testClock) Sleep(ctx context.Context, duration time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	clock.mu.Lock()
	clock.sleeps = append(clock.sleeps, duration)
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
	return ctx.Err()
}

func TestLimiterAIMD(t *testing.T) {
	clock := newTestClock()
	limiter := NewLimiter(LimiterOptions{
		Clock: clock, Policy: GenericPolicy{}, MinConcurrency: 1, MaxConcurrency: 8,
		Jitter: func(duration time.Duration) time.Duration { return duration },
	})
	key := limitKey("example.com", "one")
	if err := limiter.acquire(context.Background(), key, false); err != nil {
		t.Fatal(err)
	}
	limiter.release(key, &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"1"}}})
	if got := limiter.Concurrency("example.com", "one"); got != 4 {
		t.Fatalf("throttled concurrency = %d, want 4", got)
	}
	if err := limiter.acquire(context.Background(), key, false); err != nil {
		t.Fatal(err)
	}
	limiter.release(key, &http.Response{StatusCode: 200, Header: make(http.Header)})
	if got := limiter.Concurrency("example.com", "one"); got != 5 {
		t.Fatalf("successful concurrency = %d, want 5", got)
	}
}
