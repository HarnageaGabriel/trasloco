package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type requestFlagKey uint8

const (
	idempotentKey requestFlagKey = iota
	idempotencyGuardKey
	contentCreationKey
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
	OnThrottle   func(*http.Request, *http.Response)
}

type retryTransport struct {
	next         http.RoundTripper
	clock        Clock
	observer     Observer
	policy       RateLimitPolicy
	maxAttempts  int
	maxTotalWait time.Duration
	baseWait     time.Duration
	onThrottle   func(*http.Request, *http.Response)
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
	return &retryTransport{
		next: next, clock: opts.Clock, observer: opts.Observer, policy: opts.Policy,
		maxAttempts: opts.MaxAttempts, maxTotalWait: opts.MaxTotalWait, baseWait: opts.BaseWait,
		onThrottle: opts.OnThrottle,
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
		if response != nil && transport.onThrottle != nil {
			budget := transport.policy.Parse(response, transport.clock.Now())
			if budget.Throttled || budget.NearLimit || budget.RetryAfter > 0 || budget.Delay > 0 || budget.Remaining == 0 {
				transport.onThrottle(attemptRequest, response)
			}
		}
		wait := transport.retryWait(response, attempt)
		if totalWait+wait > transport.maxTotalWait {
			return response, roundTripErr
		}
		if response != nil {
			drainAndClose(response.Body)
		}
		transport.observer.OnRetry(RetryObservation{
			Method: request.Method, URL: sanitizedURL(request), Attempt: attempt,
			Wait: wait, StatusCode: statusCode(response), Err: roundTripErr,
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
		switch response.StatusCode {
		case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			candidate = true
		case http.StatusForbidden:
			if transport.policy.Name() == "github" {
				budget := transport.policy.Parse(response, transport.clock.Now())
				candidate = budget.RetryAfter > 0 || budget.Remaining >= 0
			}
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
	wait := transport.baseWait * time.Duration(1<<min(attempt-1, 10))
	if response == nil {
		return wait
	}
	budget := transport.policy.Parse(response, transport.clock.Now())
	if budget.RetryAfter > 0 {
		return budget.RetryAfter
	}
	if budget.Delay > 0 {
		return budget.Delay
	}
	if budget.Remaining == 0 && budget.Reset.After(transport.clock.Now()) {
		return budget.Reset.Sub(transport.clock.Now())
	}
	if response.StatusCode == http.StatusForbidden && transport.policy.Name() == "github" {
		return max(time.Minute, wait)
	}
	return wait
}

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
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}

func statusCode(response *http.Response) int {
	if response == nil {
		return 0
	}
	return response.StatusCode
}
