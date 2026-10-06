// Package httpx provides retrying, cache-aware HTTP transport.
package httpx

import "github.com/HarnageaGabriel/trasloco/internal/ratelimit"

// Clock is the shared pacing clock.
type Clock = ratelimit.Clock

// RealClock uses the process wall clock.
type RealClock = ratelimit.RealClock
