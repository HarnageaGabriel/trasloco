// Package clock owns wall-clock access and cancellable timing.
package clock

import (
	"context"
	"time"
)

// Clock is the shared injectable time source.
type Clock interface {
	Now() time.Time
	Sleep(context.Context, time.Duration) error
	NewTimer(time.Duration) Timer
}

// Timer is a stoppable notification created by a Clock.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// Real implements Clock using the system clock.
type Real struct{}

// Now returns the current wall time.
func (Real) Now() time.Time { return time.Now() }

// NewTimer returns a timer which fires once after d.
func (Real) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ *time.Timer }

func (t realTimer) C() <-chan time.Time { return t.Timer.C }

// Sleep waits for d or returns the original context error.
func (c Real) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := c.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C():
		return nil
	}
}
