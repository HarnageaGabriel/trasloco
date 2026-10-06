package httpx

import (
	"context"
	"sync"
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
