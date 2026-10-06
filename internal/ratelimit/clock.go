// Package ratelimit provides adaptive, retrying, cache-aware HTTP transport.
package ratelimit

import (
	"context"
	"time"
)

// Clock makes all transport waiting deterministic in tests.
type Clock interface {
	Now() time.Time
	Sleep(context.Context, time.Duration) error
}

// RealClock uses the process wall clock.
type RealClock struct{}

// Now returns the current time.
func (RealClock) Now() time.Time { return time.Now() }

// Sleep waits for duration or context cancellation.
func (RealClock) Sleep(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
