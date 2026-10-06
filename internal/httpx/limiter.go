package httpx

import (
	"net/http"

	"github.com/HarnageaGabriel/trasloco/internal/ratelimit"
)

// Limiter owns per-actor scheduling in ratelimit.
type Limiter = ratelimit.Limiter

// LimiterOptions configures a shared limiter.
type LimiterOptions = ratelimit.LimiterOptions

// NewLimiter constructs a shared limiter.
func NewLimiter(opts LimiterOptions) *Limiter { return ratelimit.NewLimiter(opts) }

func limitKey(host, credentialID string) string { return ratelimit.Key(host, credentialID) }

type limiterTransport struct {
	next    http.RoundTripper
	limiter *Limiter
	policy  RateLimitPolicy
}

func (transport *limiterTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	credential := credentialFrom(request.Context())
	key := limitKey(request.URL.Host, credential.ID)
	if err := transport.limiter.Acquire(request.Context(), key, requestCost(request, transport.policy)); err != nil {
		return nil, err
	}
	response, err := transport.next.RoundTrip(request)
	transport.limiter.Release(key, response)
	return response, err
}
