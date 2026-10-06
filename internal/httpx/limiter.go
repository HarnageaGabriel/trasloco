package httpx

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// LimiterOptions configures adaptive per-host, per-credential scheduling.
type LimiterOptions struct {
	Clock          Clock
	Observer       Observer
	Policy         RateLimitPolicy
	MinConcurrency int
	MaxConcurrency int
	Jitter         func(time.Duration) time.Duration
}

// Limiter applies server budgets and AIMD concurrency bounds.
type Limiter struct {
	mu       sync.Mutex
	states   map[string]*limitState
	clock    Clock
	observer Observer
	policy   RateLimitPolicy
	min      int
	max      int
	jitter   func(time.Duration) time.Duration
	sequence atomic.Uint64
}

type limitState struct {
	active       int
	concurrency  int
	blockedUntil time.Time
	nextAllowed  time.Time
	notify       chan struct{}
	minuteWrites []time.Time
	hourWrites   []time.Time
}

// NewLimiter constructs an adaptive limiter.
func NewLimiter(opts LimiterOptions) *Limiter {
	if opts.Clock == nil {
		opts.Clock = RealClock{}
	}
	if opts.Observer == nil {
		opts.Observer = nopObserver{}
	}
	if opts.Policy == nil {
		opts.Policy = GenericPolicy{}
	}
	if opts.MinConcurrency <= 0 {
		opts.MinConcurrency = 1
	}
	if opts.MaxConcurrency < opts.MinConcurrency {
		opts.MaxConcurrency = max(8, opts.MinConcurrency)
	}
	limiter := &Limiter{
		states: make(map[string]*limitState), clock: opts.Clock, observer: opts.Observer,
		policy: opts.Policy, min: opts.MinConcurrency, max: opts.MaxConcurrency, jitter: opts.Jitter,
	}
	if limiter.jitter == nil {
		limiter.jitter = limiter.defaultJitter
	}
	return limiter
}

func (l *Limiter) state(key string) *limitState {
	state := l.states[key]
	if state == nil {
		state = &limitState{concurrency: l.max, notify: make(chan struct{})}
		l.states[key] = state
	}
	return state
}

func (l *Limiter) acquire(ctx context.Context, key string, contentCreation bool) error {
	for {
		now := l.clock.Now()
		l.mu.Lock()
		state := l.state(key)
		if contentCreation && l.policy.Name() == "github" {
			state.pruneWrites(now)
			if len(state.minuteWrites) >= 80 {
				state.blockedUntil = later(state.blockedUntil, state.minuteWrites[0].Add(time.Minute))
			}
			if len(state.hourWrites) >= 500 {
				state.blockedUntil = later(state.blockedUntil, state.hourWrites[0].Add(time.Hour))
			}
		}
		waitUntil := later(state.blockedUntil, state.nextAllowed)
		if state.active < state.concurrency && !now.Before(waitUntil) {
			state.active++
			if contentCreation && l.policy.Name() == "github" {
				state.minuteWrites = append(state.minuteWrites, now)
				state.hourWrites = append(state.hourWrites, now)
			}
			l.mu.Unlock()
			return nil
		}
		notify := state.notify
		wait := waitUntil.Sub(now)
		l.mu.Unlock()
		if wait > 0 {
			if err := l.clock.Sleep(ctx, l.jitter(wait)); err != nil {
				return err
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-notify:
		}
	}
}

func (l *Limiter) release(key string, response *http.Response) Budget {
	now := l.clock.Now()
	budget := Budget{Remaining: -1}
	if response != nil {
		budget = l.policy.Parse(response, now)
	}
	l.update(key, budget, response, now, true)
	return budget
}

func (l *Limiter) observe(key string, response *http.Response) {
	if response == nil {
		return
	}
	now := l.clock.Now()
	budget := l.policy.Parse(response, now)
	l.update(key, budget, response, now, false)
}

func (l *Limiter) update(key string, budget Budget, response *http.Response, now time.Time, release bool) {
	l.mu.Lock()
	state := l.state(key)
	if release && state.active > 0 {
		state.active--
	}
	throttled := budget.Throttled || budget.NearLimit || budget.RetryAfter > 0 || budget.Delay > 0 ||
		(budget.Remaining == 0 && budget.Reset.After(now))
	if throttled {
		state.concurrency = max(l.min, state.concurrency/2)
		wait := max(budget.RetryAfter, budget.Delay)
		if budget.Remaining == 0 && budget.Reset.After(now) {
			wait = max(wait, budget.Reset.Sub(now))
		}
		state.blockedUntil = later(state.blockedUntil, now.Add(wait))
	} else if response != nil && response.StatusCode >= 200 && response.StatusCode < 400 {
		state.concurrency = min(l.max, state.concurrency+1)
	}
	if budget.Remaining > 0 && budget.Reset.After(now) {
		spacing := budget.Reset.Sub(now) / time.Duration(budget.Remaining)
		// Only pace when the server is signaling pressure. Full GitHub budgets
		// should retain burst capacity; ADO TSTU headers are proactive signals.
		if l.policy.Name() == "azuredevops" || budget.NearLimit {
			state.nextAllowed = later(state.nextAllowed, now.Add(spacing))
		}
	}
	close(state.notify)
	state.notify = make(chan struct{})
	concurrency := state.concurrency
	l.mu.Unlock()
	if throttled {
		host, credentialID := splitLimitKey(key)
		l.observer.OnThrottle(ThrottleObservation{
			Host: host, CredentialID: credentialID, Budget: budget, Concurrency: concurrency,
		})
	}
}

// Concurrency returns the current AIMD bound for tests and diagnostics.
func (l *Limiter) Concurrency(host, credentialID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state(limitKey(host, credentialID)).concurrency
}

func (l *Limiter) defaultJitter(duration time.Duration) time.Duration {
	if duration <= 0 {
		return 0
	}
	// Deterministic per-instance sequence: 100%..110%, avoiding early wakeups
	// and global RNG.
	step := int64(l.sequence.Add(1) % 11)
	return duration + time.Duration(int64(duration)*step/100)
}

func (state *limitState) pruneWrites(now time.Time) {
	state.minuteWrites = pruneBefore(state.minuteWrites, now.Add(-time.Minute))
	state.hourWrites = pruneBefore(state.hourWrites, now.Add(-time.Hour))
}

func pruneBefore(values []time.Time, cutoff time.Time) []time.Time {
	first := 0
	for first < len(values) && !values[first].After(cutoff) {
		first++
	}
	return values[first:]
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func limitKey(host, credentialID string) string { return host + "\x00" + credentialID }

func splitLimitKey(key string) (string, string) {
	for i := range len(key) {
		if key[i] == 0 {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

type limiterTransport struct {
	next    http.RoundTripper
	limiter *Limiter
}

func (transport *limiterTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	credential := credentialFrom(request.Context())
	key := limitKey(request.URL.Hostname(), credential.ID)
	contentCreation := contentCreationFrom(request.Context())
	if err := transport.limiter.acquire(request.Context(), key, contentCreation); err != nil {
		return nil, err
	}
	response, err := transport.next.RoundTrip(request)
	budget := transport.limiter.release(key, response)
	if credential.pool != nil {
		credential.pool.Release(request.URL.Hostname(), credential.ID, budget)
	}
	return response, err
}
