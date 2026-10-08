package httpx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	responsecache "github.com/HarnageaGabriel/trasloco/internal/cache"
	"github.com/HarnageaGabriel/trasloco/internal/credpool"
	"github.com/HarnageaGabriel/trasloco/internal/ratelimit"
)

// Source: ADR0007 and docs/spec/credentials.md. These are adversarial synthetic
// responses, not provider captures.
func TestRetrySignals(t *testing.T) {
	for _, tc := range []struct {
		name, body, remaining, retry string
		status, attempts             int
	}{
		{"permission", "denied", "20", "", 403, 1},
		{"permission without budget", "denied", "", "", 403, 1},
		{"primary", "denied", "0", "", 403, 2},
		{"secondary", `{"message":"You have exceeded a secondary rate limit"}`, "42", "", 403, 2},
		{"retry header", "denied", "42", "0", 403, 2},
		{"all 5xx", "failed", "", "", 501, 2},
		{"timeout", "failed", "", "", 408, 2},
		{"429", "failed", "", "1", 429, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				response := testResponse(r, tc.status)
				response.Body = io.NopCloser(strings.NewReader(tc.body))
				response.Header.Set("X-RateLimit-Remaining", tc.remaining)
				response.Header.Set("Retry-After", tc.retry)
				return response, nil
			})
			client := NewClient(Options{Base: base, Clock: newTestClock(), Platform: "github", MaxAttempts: 2})
			r, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com", nil)
			response, err := client.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || string(body) != tc.body || calls != tc.attempts {
				t.Fatalf("calls=%d body=%q err=%v", calls, body, err)
			}
		})
	}
}

func TestRetryWaitAndAttemptBudget(t *testing.T) {
	clock := newTestClock()
	limiter := NewLimiter(LimiterOptions{Clock: clock, Policy: GitHubPolicy{}})
	calls := 0
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		response := testResponse(r, 503)
		response.Header.Set("Retry-After", "0.1")
		return response, nil
	})
	client := NewClient(Options{Base: base, Clock: clock, Platform: "github", Limiter: limiter, MaxAttempts: 3, Jitter: func(d time.Duration) time.Duration { return d }})
	r, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com", nil)
	r = WithCost(r, ratelimit.Cost{Points: 450})
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if calls != 3 || clock.Now().Sub(time.Unix(1700000000, 0)) < time.Minute {
		t.Fatalf("attempt reservations missing: calls=%d waits=%v", calls, clock.sleeps)
	}
	retry := newRetryTransport(base, RetryOptions{Clock: clock, Jitter: func(d time.Duration) time.Duration { return d }, BlockedUntil: func(*http.Request) time.Time { return clock.Now().Add(2 * time.Second) }, MaxTotalWait: time.Second})
	response, err = retry.RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if calls != 4 {
		t.Fatal("wait budget exceeded")
	}
}

func TestRetryMethodGuardsAndBodies(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete, http.MethodPost, http.MethodPatch, "CUSTOM"} {
		t.Run(method, func(t *testing.T) {
			r, _ := http.NewRequestWithContext(t.Context(), method, "https://example.com", strings.NewReader("body"))
			want := method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
			if retryableMethod(r) != want {
				t.Fatal("unsafe default")
			}
			if method == http.MethodPut || method == http.MethodDelete {
				r = MarkIdempotent(r)
				want = true
			}
			if method == http.MethodPost || method == http.MethodPatch {
				r = WithIdempotencyGuard(r, func(*http.Request, *http.Response, error) bool { return true })
				want = true
			}
			calls := 0
			base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				body, _ := io.ReadAll(req.Body)
				_ = req.Body.Close()
				if string(body) != "body" {
					t.Error("body not replayed")
				}
				return testResponse(req, 500), nil
			})
			retry := newRetryTransport(base, RetryOptions{Clock: newTestClock(), MaxAttempts: 2})
			response, err := retry.RoundTrip(r)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			expected := 1
			if want {
				expected = 2
			}
			if calls != expected {
				t.Fatal(calls)
			}
		})
	}
	r, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.com", io.NopCloser(strings.NewReader("body")))
	if _, err := cloneForAttempt(r, 2); err == nil {
		t.Fatal("nonreplayable body")
	}
	r.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("body error") }
	if _, err := cloneForAttempt(r, 2); err == nil {
		t.Fatal("body error ignored")
	}
	retry := newRetryTransport(nil, RetryOptions{}).(*retryTransport)
	r = WithIdempotencyGuard(r, func(*http.Request, *http.Response, error) bool { return false })
	if retry.shouldRetry(r, nil, errors.New("network")) {
		t.Fatal("guard ignored")
	}
	if retry.shouldRetry(r, nil, context.DeadlineExceeded) {
		t.Fatal("deadline retry")
	}
	response := testResponse(r, 500)
	defer func() { _ = response.Body.Close() }()
	response.Header.Set("Retry-After", "1")
	retry.jitter = func(time.Duration) time.Duration { return 2 * time.Second }
	retry.baseWait = 4 * time.Second
	if got := retry.retryWait(response, 1); got != 2*time.Second {
		t.Fatal(got)
	}
}

func testPool(t *testing.T, host string) *credpool.Pool {
	t.Helper()
	pool, err := credpool.New(map[string][]credpool.Credential{host: {{ID: "credential", Actor: "verified-actor", TargetScope: "org", ScopeID: "read", Source: credpool.StaticToken("secret")}}})
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestRedirectsAndPresignedAreAnonymous(t *testing.T) {
	var seen []string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization")+r.Header.Get("Cookie")+r.Header.Get("Private-Token"))
		_, _ = io.WriteString(w, "ok")
	}))
	defer target.Close()
	originServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing initial credential")
		}
		http.Redirect(w, r, target.URL+"/signed?sig=signature", http.StatusFound)
	}))
	defer originServer.Close()
	r, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, originServer.URL, nil)
	pool := testPool(t, r.URL.Host)
	targetRequest, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, target.URL, nil)
	if err := pool.Add(targetRequest.URL.Host, credpool.Credential{ID: "target", Actor: "target-actor", TargetScope: "org", Source: credpool.StaticToken("target-secret")}); err != nil {
		t.Fatal(err)
	}
	client := NewClient(Options{Credentials: pool, TargetScope: "org", Clock: newTestClock()})
	r.Header.Set("Cookie", "secret-cookie")
	r.Header.Set("Private-Token", "secret-header")
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	targetRequest.URL.RawQuery = "X-Amz-Signature=value"
	targetRequest.Header.Set("Authorization", "explicit-secret")
	response, err = client.Do(targetRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(seen) != 2 || seen[0] != "" || seen[1] != "" {
		t.Fatal("credential leak", seen)
	}
	back := r.Clone(r.Context())
	cross := Anonymous(targetRequest)
	if err := CheckRedirect(back, []*http.Request{r, cross}); err != nil || !anonymousFrom(back) {
		t.Fatal("redirect back restored credentials")
	}
	if err := CheckRedirect(back, make([]*http.Request, 10)); err == nil {
		t.Fatal("unbounded redirects")
	}
	if origin(r.URL) == origin(targetRequest.URL) {
		t.Fatal("ports ignored")
	}
}

func TestAuthScopeFailureAndAnonymous(t *testing.T) {
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("auth")
		}
		return testResponse(r, 200), nil
	})
	client := NewClient(Options{Base: base, Credentials: testPool(t, "example.com"), Clock: newTestClock()})
	r, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com", nil)
	response, err := client.Do(r)
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("missing scope accepted")
	}
	response, err = client.Do(WithTargetScope(r, "org"))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if safeError(errors.New("secret")).Error() != "HTTP request failed" || !errors.Is(safeError(context.Canceled), context.Canceled) || !errors.Is(safeError(context.DeadlineExceeded), context.DeadlineExceeded) {
		t.Fatal("unsafe observer error")
	}
	r.URL.RawQuery = "token=secret"
	r.URL.User = nil
	if strings.Contains(sanitizedURL(r), "secret") {
		t.Fatal("query in observation")
	}
}

func TestCacheOptInValidatorsCapAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name             string
		optin, validator bool
		size             int
		hit              bool
	}{
		{"default stream", false, true, 10, false}, {"no validator", true, false, 10, false}, {"valid", true, true, 10, true}, {"oversized", true, true, 65, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := responsecache.Open(responsecache.Options{Dir: filepath.Join(t.TempDir(), "cache"), MaxBytes: 4096, Clock: newTestClock()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			conditional := false
			base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				response := testResponse(r, 200)
				if tc.validator {
					response.Header.Set("ETag", `"v1"`)
				}
				response.Body = io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), tc.size)))
				response.ContentLength = -1
				if r.Header.Get("If-None-Match") != "" {
					conditional = true
					response.StatusCode = 304
				}
				return response, nil
			})
			client := NewClient(Options{Base: base, Clock: newTestClock(), Cache: store, MaxCacheBody: 64})
			r, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com/items", nil)
			if tc.optin {
				r = WithCache(r)
			}
			for range 2 {
				response, err := client.Do(r)
				if err != nil {
					t.Fatal(err)
				}
				body, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if readErr != nil || len(body) != tc.size || response.StatusCode != 200 {
					t.Fatal("cache changed response")
				}
			}
			if conditional != tc.hit {
				t.Fatalf("conditional=%v", conditional)
			}
		})
	}
	dir := filepath.Join(t.TempDir(), "cache")
	store, err := responsecache.Open(responsecache.Options{Dir: dir, MaxBytes: 4096, Clock: newTestClock()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// Make only this test-owned cache root unwritable by replacing it with a file.
	if err := os.WriteFile(dir, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	errorsSeen := 0
	client := NewClient(Options{Cache: store, CacheError: func(error) { errorsSeen++ }, Clock: newTestClock(), Base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response := testResponse(r, 200)
		response.Header.Set("ETag", `"v"`)
		return response, nil
	})})
	r, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com", nil)
	response, err := client.Do(WithCache(r))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || string(body) != "body" || errorsSeen == 0 {
		t.Fatalf("cache failure became fatal: %q %v %d", body, err, errorsSeen)
	}
}

func TestPeekPreservesErrorsAndCachePartialReads(t *testing.T) {
	r, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com", nil)
	sentinel := errors.New("read failed")
	response := testResponse(r, 403)
	response.Body = io.NopCloser(io.MultiReader(strings.NewReader("prefix"), errorReader{sentinel}))
	if secondaryLimit(response) {
		t.Fatal("false signal")
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(data) != "prefix" || !errors.Is(err, sentinel) {
		t.Fatalf("peek lost data/error: %q %v", data, err)
	}
	if secondaryLimit(&http.Response{}) {
		t.Fatal("nil body")
	}
	saved := false
	body := &cacheBody{ReadCloser: io.NopCloser(errorReader{sentinel}), cap: 64, save: func([]byte) { saved = true }}
	_, _ = body.Read(make([]byte, 8))
	_ = body.Close()
	if saved {
		t.Fatal("cached failed body")
	}
	drainAndClose(nil)
}
