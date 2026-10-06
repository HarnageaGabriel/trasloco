package httpx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

type requestFlagKey uint8

const (
	idempotentKey requestFlagKey = iota
	idempotencyGuardKey
	contentCreationKey
	targetScopeKey
	costKey
	cacheKey
	anonymousKey
)

// IdempotencyGuard must confirm whether a POST/PATCH attempt may be repeated.
// It can inspect a response or network error but must not consume the body.
type IdempotencyGuard func(*http.Request, *http.Response, error) bool

// MarkIdempotent marks PUT/DELETE as safe to retry.
func MarkIdempotent(request *http.Request) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), idempotentKey, true))
}

// WithIdempotencyGuard permits guarded retries for POST/PATCH.
func WithIdempotencyGuard(request *http.Request, guard IdempotencyGuard) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), idempotencyGuardKey, guard))
}

// MarkContentCreation applies GitHub's content-creation sub-limit.
func MarkContentCreation(request *http.Request) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), contentCreationKey, true))
}

func contentCreationFrom(ctx context.Context) bool {
	value, _ := ctx.Value(contentCreationKey).(bool)
	return value
}

// RetryOptions bounds retry amplification.
type RetryOptions struct {
	Clock        Clock
	Observer     Observer
	Policy       RateLimitPolicy
	MaxAttempts  int
	MaxTotalWait time.Duration
	BaseWait     time.Duration
	BlockedUntil func(*http.Request) time.Time
	Jitter       func(time.Duration) time.Duration
}

type retryTransport struct {
	next         http.RoundTripper
	clock        Clock
	observer     Observer
	policy       RateLimitPolicy
	maxAttempts  int
	maxTotalWait time.Duration
	baseWait     time.Duration
	blockedUntil func(*http.Request) time.Time
	jitter       func(time.Duration) time.Duration
}

func newRetryTransport(next http.RoundTripper, opts RetryOptions) http.RoundTripper {
	if opts.Clock == nil {
		opts.Clock = RealClock{}
	}
	if opts.Observer == nil {
		opts.Observer = nopObserver{}
	}
	if opts.Policy == nil {
		opts.Policy = GenericPolicy{}
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 4
	}
	if opts.MaxTotalWait <= 0 {
		opts.MaxTotalWait = 3 * time.Minute
	}
	if opts.BaseWait <= 0 {
		opts.BaseWait = 250 * time.Millisecond
	}
	if opts.Jitter == nil {
		//nolint:gosec // Retry jitter is scheduling randomness, not security material.
		opts.Jitter = func(ceiling time.Duration) time.Duration { return time.Duration(rand.Int64N(int64(ceiling) + 1)) }
	}
	return &retryTransport{
		next: next, clock: opts.Clock, observer: opts.Observer, policy: opts.Policy,
		maxAttempts: opts.MaxAttempts, maxTotalWait: opts.MaxTotalWait, baseWait: opts.BaseWait,
		blockedUntil: opts.BlockedUntil, jitter: opts.Jitter,
	}
}

func (transport *retryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if !retryableMethod(request) {
		return transport.next.RoundTrip(request)
	}
	var totalWait time.Duration
	for attempt := 1; ; attempt++ {
		attemptRequest, err := cloneForAttempt(request, attempt)
		if err != nil {
			return nil, err
		}
		response, roundTripErr := transport.next.RoundTrip(attemptRequest)
		shouldRetry := transport.shouldRetry(attemptRequest, response, roundTripErr)
		if attempt >= transport.maxAttempts || !shouldRetry {
			return response, roundTripErr
		}
		wait := transport.retryWait(response, attempt)
		if transport.blockedUntil != nil {
			wait = max(wait, transport.blockedUntil(request).Sub(transport.clock.Now()))
		}
		if totalWait+wait > transport.maxTotalWait {
			return response, roundTripErr
		}
		if response != nil {
			drainAndClose(response.Body)
		}
		transport.observer.OnRetry(RetryObservation{
			Method: request.Method, URL: sanitizedURL(request), Attempt: attempt,
			Wait: wait, StatusCode: statusCode(response), Err: safeError(roundTripErr),
		})
		if err := transport.clock.Sleep(request.Context(), wait); err != nil {
			return nil, err
		}
		totalWait += wait
	}
}

func retryableMethod(request *http.Request) bool {
	switch request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	case http.MethodPut, http.MethodDelete:
		marked, _ := request.Context().Value(idempotentKey).(bool)
		return marked
	case http.MethodPost, http.MethodPatch:
		guard, _ := request.Context().Value(idempotencyGuardKey).(IdempotencyGuard)
		return guard != nil
	default:
		return false
	}
}

func (transport *retryTransport) shouldRetry(request *http.Request, response *http.Response, err error) bool {
	candidate := false
	if err != nil {
		candidate = !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
	} else if response != nil {
		candidate = response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || (response.StatusCode >= 500 && response.StatusCode <= 599)
		if response.StatusCode == http.StatusForbidden {
			budget := transport.policy.Parse(response, transport.clock.Now())
			candidate = response.Header.Get("Retry-After") != "" || budget.Remaining == 0 || (transport.policy.Name() == "github" && secondaryLimit(response))
		}
	}
	if !candidate {
		return false
	}
	if guard, _ := request.Context().Value(idempotencyGuardKey).(IdempotencyGuard); guard != nil {
		return guard(request, response, err)
	}
	return true
}

func (transport *retryTransport) retryWait(response *http.Response, attempt int) time.Duration {
	ceiling := transport.baseWait * time.Duration(1<<min(attempt-1, 10))
	wait := min(ceiling, max(0, transport.jitter(ceiling)))
	if response == nil {
		return wait
	}
	now := transport.clock.Now()
	budget := transport.policy.Parse(response, now)
	wait = max(wait, budget.RetryAfter, budget.Delay)
	if budget.Remaining == 0 {
		wait = max(wait, budget.Reset.Sub(now))
	}
	if response.StatusCode == http.StatusForbidden && transport.policy.Name() == "github" && budget.Remaining != 0 && budget.RetryAfter == 0 {
		wait = max(time.Minute, wait)
	}
	return wait
}

// secondaryLimit inspects at most 8 KiB and restores the exact response stream,
// including a read error. The original body still owns Close.
func secondaryLimit(response *http.Response) bool {
	if response.Body == nil {
		return false
	}
	body := response.Body
	prefix, err := io.ReadAll(io.LimitReader(body, 8<<10))
	reader := io.MultiReader(bytes.NewReader(prefix), body)
	if err != nil {
		reader = io.MultiReader(bytes.NewReader(prefix), errorReader{err}, body)
	}
	response.Body = &restoredBody{Reader: reader, Closer: body}
	return strings.Contains(strings.ToLower(string(prefix)), "secondary rate limit")
}

type restoredBody struct {
	io.Reader
	io.Closer
}
type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }

func cloneForAttempt(request *http.Request, attempt int) (*http.Request, error) {
	cloned := request.Clone(request.Context())
	if attempt == 1 || request.Body == nil {
		cloned.Body = request.Body
		return cloned, nil
	}
	if request.GetBody == nil {
		return nil, fmt.Errorf("retry %s request: body cannot be replayed", request.Method)
	}
	body, err := request.GetBody()
	if err != nil {
		return nil, fmt.Errorf("retry %s request body: %w", request.Method, err)
	}
	cloned.Body = body
	return cloned, nil
}

func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.CopyN(io.Discard, body, 32<<10)
	_ = body.Close()
}

func statusCode(response *http.Response) int {
	if response == nil {
		return 0
	}
	return response.StatusCode
}
