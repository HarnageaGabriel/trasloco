package httpx

import (
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/ratelimit"
)

// Observer receives sanitized transport events. URL contains scheme, host and
// path only; authorization and query parameters are never exposed.
type Observer interface {
	OnRequest(RequestObservation)
	OnThrottle(ThrottleObservation)
	OnRetry(RetryObservation)
	OnCacheHit(CacheHitObservation)
}

// RequestObservation describes one logical request.
type RequestObservation struct {
	Method       string
	URL          string
	CredentialID string
	StatusCode   int
	Duration     time.Duration
	Err          error
}

// ThrottleObservation describes a limiter adjustment or enforced delay.
type ThrottleObservation = ratelimit.ThrottleObservation

// RetryObservation describes a scheduled retry.
type RetryObservation struct {
	Method     string
	URL        string
	Attempt    int
	Wait       time.Duration
	StatusCode int
	Err        error
}

// CacheHitObservation describes a 304 revalidation served from CAS.
type CacheHitObservation struct {
	Method string
	URL    string
	ETag   string
}

type nopObserver struct{}

func (nopObserver) OnRequest(RequestObservation)   {}
func (nopObserver) OnThrottle(ThrottleObservation) {}
func (nopObserver) OnRetry(RetryObservation)       {}
func (nopObserver) OnCacheHit(CacheHitObservation) {}
