package ratelimit

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// LimiterOptions configures adaptive per-host, per-credential scheduling.
type LimiterOptions struct {
	Clock          Clock
	OnThrottle     func(ThrottleObservation)
	Policy         Policy
	MinConcurrency int
	MaxConcurrency int
	Jitter         func(time.Duration) time.Duration
}

// Limiter applies server budgets and AIMD concurrency bounds.
type Limiter struct {
	mu         sync.Mutex
	states     map[string]*limitState
	clock      Clock
	onThrottle func(ThrottleObservation)
	policy     Policy
	min        int
	max        int
}

type limitState struct {
	active       int
	concurrency  int
	blockedUntil time.Time
	pacer        *rate.Limiter
	points       [2][]pointCharge
	primary      [2]Budget
	notify       chan struct{}
	minuteWrites []time.Time
	hourWrites   []time.Time
}

// NewLimiter constructs an adaptive limiter.
func NewLimiter(opts LimiterOptions) *Limiter {
	if opts.Clock == nil {
		opts.Clock = RealClock{}
	}
	if opts.OnThrottle == nil {
		opts.OnThrottle = func(ThrottleObservation) {}
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
		states: make(map[string]*limitState), clock: opts.Clock, onThrottle: opts.OnThrottle,
		policy: opts.Policy, min: opts.MinConcurrency, max: opts.MaxConcurrency,
	}
	return limiter
}

func (l *Limiter) state(key string) *limitState {
	state := l.states[key]
	if state == nil {
		state = &limitState{
			concurrency: l.max,
			pacer:       rate.NewLimiter(rate.Inf, 1),
			notify:      make(chan struct{}),
			primary:     [2]Budget{{Remaining: -1}, {Remaining: -1}},
		}
		l.states[key] = state
	}
	return state
}

// Acquire reserves one attempt; the caller must Release even on network failure.
func (l *Limiter) Acquire(ctx context.Context, key string, cost Cost) error {
	if cost.Points <= 0 {
		cost.Points = 1
	}
	secondary := cost.Points
	resource, ceiling := 0, int64(900)
	if cost.GraphQL {
		resource, ceiling = 1, 2000
		secondary = cost.SecondaryPoints
		if secondary <= 0 {
			secondary = 1
		}
	}
	if l.policy.Name() == "github" && secondary > ceiling {
		return fmt.Errorf("request exceeds secondary points budget")
	}
	contentCreation := cost.ContentCreation
	paced := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
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
		waitUntil := state.blockedUntil
		if l.policy.Name() == "github" {
			charges := state.points[resource]
			first := 0
			for first < len(charges) && !charges[first].at.Add(time.Minute).After(now) {
				first++
			}
			charges = charges[first:]
			state.points[resource] = charges
			used := int64(0)
			for _, charge := range charges {
				used += charge.points
			}
			for _, charge := range charges {
				if used+secondary <= ceiling {
					break
				}
				waitUntil = later(waitUntil, charge.at.Add(time.Minute))
				used -= charge.points
			}
			primary := state.primary[resource]
			primaryCost := int64(1)
			if cost.GraphQL {
				primaryCost = cost.Points
			}
			if primary.Remaining >= 0 && primary.Remaining < primaryCost && primary.Reset.After(now) {
				waitUntil = later(waitUntil, primary.Reset)
			}
		}
		if state.active < state.concurrency && !now.Before(waitUntil) {
			if !paced {
				reservation := state.pacer.ReserveN(now, 1)
				if !reservation.OK() {
					l.mu.Unlock()
					return fmt.Errorf("rate limiter cannot reserve request")
				}
				if delay := reservation.DelayFrom(now); delay > 0 {
					l.mu.Unlock()
					if err := l.clock.Sleep(ctx, delay); err != nil {
						reservation.CancelAt(l.clock.Now())
						return err
					}
					paced = true
					continue
				}
			}
			state.active++
			if l.policy.Name() == "github" {
				state.points[resource] = append(state.points[resource], pointCharge{at: now, points: secondary})
				primary := &state.primary[resource]
				primaryCost := int64(1)
				if cost.GraphQL {
					primaryCost = cost.Points
				}
				if primary.Remaining >= 0 {
					primary.Remaining = max(0, primary.Remaining-primaryCost)
				}
			}
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
			if err := l.clock.Sleep(ctx, wait); err != nil {
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

// Release records every response and frees an attempt slot.
func (l *Limiter) Release(key string, response *http.Response) Budget {
	now := l.clock.Now()
	budget := Budget{Remaining: -1}
	if response != nil {
		budget = l.policy.Parse(response, now)
	}
	l.update(key, budget, response, now, true)
	return budget
}

func (l *Limiter) update(key string, budget Budget, response *http.Response, now time.Time, release bool) {
	l.mu.Lock()
	state := l.state(key)
	if release && state.active > 0 {
		state.active--
	}
	resource := 0
	if budget.Resource == "graphql" {
		resource = 1
	}
	if budget.Remaining >= 0 {
		previous := state.primary[resource]
		if previous.Reset.Equal(budget.Reset) && previous.Remaining >= 0 {
			budget.Remaining = min(budget.Remaining, previous.Remaining)
		}
		state.primary[resource] = budget
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
			state.pacer.SetLimitAt(now, rate.Every(max(spacing, time.Nanosecond)))
			// Account for the request whose response established this pace so
			// the next request waits for one replenished token.
			if state.pacer.TokensAt(now) >= 1 {
				_ = state.pacer.ReserveN(now, 1)
			}
		} else {
			state.pacer.SetLimitAt(now, rate.Inf)
		}
	} else if !throttled {
		state.pacer.SetLimitAt(now, rate.Inf)
		state.pacer.SetBurstAt(now, 1)
	}
	close(state.notify)
	state.notify = make(chan struct{})
	concurrency := state.concurrency
	l.mu.Unlock()
	if throttled {
		host, credentialID := splitLimitKey(key)
		l.onThrottle(ThrottleObservation{
			Host: host, CredentialID: credentialID, Budget: budget, Concurrency: concurrency,
		})
	}
}

// Concurrency returns the current AIMD bound for tests and diagnostics.
func (l *Limiter) Concurrency(host, credentialID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state(Key(host, credentialID)).concurrency
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

// Key separates pacing by provider origin and verified actor.
func Key(host, credentialID string) string { return host + "\x00" + credentialID }

func splitLimitKey(key string) (string, string) {
	for i := range len(key) {
		if key[i] == 0 {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// Cost carries a conservative primary cost and secondary request points.
// For GraphQL, Points is the query primary cost; SecondaryPoints is 1 for a
// query and 5 for a mutation. REST Points defaults to the policy method cost.
type Cost struct {
	Points          int64
	SecondaryPoints int64
	GraphQL         bool
	ContentCreation bool
}

type pointCharge struct {
	at     time.Time
	points int64
}

// ThrottleObservation describes server pressure without authentication data.
type ThrottleObservation struct {
	Host         string
	CredentialID string
	Budget       Budget
	Concurrency  int
}

// BlockedUntil returns the server's earliest permitted next attempt.
func (l *Limiter) BlockedUntil(key string) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state(key).blockedUntil
}
