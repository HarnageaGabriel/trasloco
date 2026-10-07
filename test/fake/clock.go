// Package fake provides deterministic GitHub and Azure DevOps HTTP servers.
package fake

import (
	"context"
	"sync"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/clock"
)

// Clock advances instead of blocking, and is safe for client and server use.
type Clock struct {
	clock.Real
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

// NewClock returns a clock at a stable epoch.
func NewClock() *Clock {
	return &Clock{now: time.Unix(1_700_000_000, 0).UTC()}
}

// Now implements httpx.Clock.
func (clock *Clock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

// Sleep implements httpx.Clock without wall-clock delay.
func (clock *Clock) Sleep(ctx context.Context, duration time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if duration <= 0 {
		return nil
	}
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.sleeps = append(clock.sleeps, duration)
	clock.mu.Unlock()
	return ctx.Err()
}

// Advance moves time explicitly.
func (clock *Clock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

// Sleeps returns recorded waits.
func (clock *Clock) Sleeps() []time.Duration {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return append([]time.Duration(nil), clock.sleeps...)
}
