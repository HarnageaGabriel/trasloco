package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestPOSTIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	base := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return testResponse(request, http.StatusServiceUnavailable), nil
	})
	retry := newRetryTransport(base, RetryOptions{Clock: newTestClock(), MaxAttempts: 4})
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.com/write", strings.NewReader("x"))
	response, err := retry.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if got := calls.Load(); got != 1 {
		t.Fatalf("POST attempts = %d, want 1", got)
	}
}

func TestGETRetries(t *testing.T) {
	var calls atomic.Int32
	base := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return testResponse(request, http.StatusServiceUnavailable), nil
		}
		return testResponse(request, http.StatusOK), nil
	})
	retry := newRetryTransport(base, RetryOptions{Clock: newTestClock(), MaxAttempts: 3})
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com/items", nil)
	response, err := retry.RoundTrip(request)
	if err != nil || response.StatusCode != http.StatusOK || calls.Load() != 2 {
		t.Fatalf("RoundTrip() = status %v, err %v, calls %d", statusCode(response), err, calls.Load())
	}
	_ = response.Body.Close()
}

type cancelClock struct{}

func (cancelClock) Now() time.Time { return time.Unix(0, 0) }
func (cancelClock) Sleep(ctx context.Context, _ time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestRetryContextCancellation(t *testing.T) {
	base := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	})
	retry := newRetryTransport(base, RetryOptions{Clock: cancelClock{}, MaxAttempts: 3})
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com", nil)
	done := make(chan error, 1)
	go func() {
		response, err := retry.RoundTrip(request)
		if response != nil {
			_ = response.Body.Close()
		}
		done <- err
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("RoundTrip() error = %v, want context.Canceled", err)
	}
}

func testResponse(request *http.Request, status int) *http.Response {
	return &http.Response{
		StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("body")), Request: request,
	}
}
