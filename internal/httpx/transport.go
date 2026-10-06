package httpx

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	responsecache "github.com/HarnageaGabriel/trasloco/internal/cache"
	"github.com/HarnageaGabriel/trasloco/internal/credpool"
)

type credentialContextKey struct{}

type requestCredential struct {
	ID      string
	ScopeID string
	pool    *credpool.Pool
}

func credentialFrom(ctx context.Context) requestCredential {
	credential, _ := ctx.Value(credentialContextKey{}).(requestCredential)
	return credential
}

// Options configures a complete HTTP transport chain.
type Options struct {
	Platform       string
	Credentials    *credpool.Pool
	Cache          *responsecache.Store
	Clock          Clock
	Observer       Observer
	Policy         RateLimitPolicy
	Base           http.RoundTripper
	AuthScheme     string
	MinConcurrency int
	MaxConcurrency int
	MaxAttempts    int
	MaxTotalWait   time.Duration
	Jitter         func(time.Duration) time.Duration
}

// NewClient builds an http.Client using the adaptive transport chain.
func NewClient(opts Options) *http.Client {
	return &http.Client{Transport: NewTransport(opts)}
}

// NewTransport builds auth -> limiter -> retry -> cache -> base transport.
func NewTransport(opts Options) http.RoundTripper {
	if opts.Clock == nil {
		opts.Clock = RealClock{}
	}
	if opts.Observer == nil {
		opts.Observer = nopObserver{}
	}
	if opts.Policy == nil {
		opts.Policy = PolicyFor(opts.Platform)
	}
	if opts.MaxConcurrency <= 0 {
		opts.MaxConcurrency = 16
	}
	base := opts.Base
	if base == nil {
		base = newHostTransport(opts.MaxConcurrency)
	}
	base = drainingTransport{next: base}
	chain := base
	if opts.Cache != nil {
		chain = &cacheTransport{next: chain, store: opts.Cache, observer: opts.Observer}
	}
	limiter := NewLimiter(LimiterOptions{
		Clock: opts.Clock, Observer: opts.Observer, Policy: opts.Policy,
		MinConcurrency: opts.MinConcurrency, MaxConcurrency: opts.MaxConcurrency, Jitter: opts.Jitter,
	})
	chain = newRetryTransport(chain, RetryOptions{
		Clock: opts.Clock, Observer: opts.Observer, Policy: opts.Policy,
		MaxAttempts: opts.MaxAttempts, MaxTotalWait: opts.MaxTotalWait,
		OnThrottle: func(request *http.Request, response *http.Response) {
			credential := credentialFrom(request.Context())
			limiter.observe(limitKey(request.URL.Hostname(), credential.ID), response)
		},
	})
	chain = &limiterTransport{next: chain, limiter: limiter}
	chain = &observingTransport{next: chain, clock: opts.Clock, observer: opts.Observer}
	return &authTransport{
		next: chain, pool: opts.Credentials, policy: opts.Policy, authScheme: opts.AuthScheme,
	}
}

type authTransport struct {
	next       http.RoundTripper
	pool       *credpool.Pool
	policy     RateLimitPolicy
	authScheme string
}

func (transport *authTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport.pool == nil {
		return transport.next.RoundTrip(request)
	}
	cost := transport.policy.Cost(request.Method, contentCreationFrom(request.Context()))
	credential, err := transport.pool.Acquire(request.Context(), request.URL.Hostname(), cost)
	if err != nil {
		return nil, err
	}
	token, err := credential.Source.Token(request.Context())
	if err != nil {
		return nil, err
	}
	cloned := request.Clone(context.WithValue(request.Context(), credentialContextKey{}, requestCredential{
		ID: credential.ID, ScopeID: credential.ScopeID, pool: transport.pool,
	}))
	cloned.Header = request.Header.Clone()
	scheme := transport.authScheme
	if scheme == "" {
		scheme = "Bearer"
	}
	cloned.Header.Set("Authorization", scheme+" "+token)
	return transport.next.RoundTrip(cloned)
}

type observingTransport struct {
	next     http.RoundTripper
	clock    Clock
	observer Observer
}

func (transport *observingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	started := transport.clock.Now()
	response, err := transport.next.RoundTrip(request)
	credential := credentialFrom(request.Context())
	transport.observer.OnRequest(RequestObservation{
		Method: request.Method, URL: sanitizedURL(request), CredentialID: credential.ID,
		StatusCode: statusCode(response), Duration: transport.clock.Now().Sub(started), Err: err,
	})
	return response, err
}

func sanitizedURL(request *http.Request) string {
	clean := url.URL{Scheme: request.URL.Scheme, Host: request.URL.Host, Path: request.URL.EscapedPath()}
	return clean.String()
}

type cacheTransport struct {
	next     http.RoundTripper
	store    *responsecache.Store
	observer Observer
}

func (transport *cacheTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		return transport.next.RoundTrip(request)
	}
	credential := credentialFrom(request.Context())
	key := responsecache.Key(request, credential.ScopeID)
	cached, found, _ := transport.store.Get(key)
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	if found {
		responsecache.Revalidate(cloned, cached.Entry)
	}
	response, err := transport.next.RoundTrip(cloned)
	if err != nil || response == nil {
		return response, err
	}
	if response.StatusCode == http.StatusNotModified && found {
		drainAndClose(response.Body)
		header := cached.Entry.Header.Clone()
		for name, values := range response.Header {
			header[name] = append([]string(nil), values...)
		}
		transport.observer.OnCacheHit(CacheHitObservation{
			Method: request.Method, URL: sanitizedURL(request), ETag: cached.Entry.ETag,
		})
		return &http.Response{
			Status: cachedStatus(cached.Entry.StatusCode), StatusCode: cached.Entry.StatusCode,
			Header: header, Body: io.NopCloser(bytes.NewReader(cached.Body)),
			ContentLength: int64(len(cached.Body)), Request: request,
		}, nil
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if _, cacheErr := transport.store.Put(key, response.StatusCode, response.Header, body); cacheErr != nil {
			return nil, cacheErr
		}
		response.Body = io.NopCloser(bytes.NewReader(body))
		response.ContentLength = int64(len(body))
	}
	return response, nil
}

func cachedStatus(code int) string {
	if code == 0 {
		code = http.StatusOK
	}
	return strconv.Itoa(code) + " " + http.StatusText(code)
}

type drainingTransport struct{ next http.RoundTripper }

func (transport drainingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.next.RoundTrip(request)
	if response != nil && response.Body != nil {
		response.Body = &drainingBody{ReadCloser: response.Body}
	}
	return response, err
}

type drainingBody struct{ io.ReadCloser }

func (body *drainingBody) Close() error {
	_, _ = io.Copy(io.Discard, body.ReadCloser)
	return body.ReadCloser.Close()
}

type hostTransport struct {
	mu             sync.Mutex
	maxConcurrency int
	byHost         map[string]*http.Transport
}

func newHostTransport(maxConcurrency int) *hostTransport {
	return &hostTransport{maxConcurrency: maxConcurrency, byHost: make(map[string]*http.Transport)}
}

func (transport *hostTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	key := request.URL.Scheme + "://" + request.URL.Host
	transport.mu.Lock()
	base := transport.byHost[key]
	if base == nil {
		base = http.DefaultTransport.(*http.Transport).Clone()
		base.ForceAttemptHTTP2 = true
		base.MaxIdleConnsPerHost = max(transport.maxConcurrency, 2)
		base.MaxIdleConns = max(base.MaxIdleConns, transport.maxConcurrency*4)
		transport.byHost[key] = base
	}
	transport.mu.Unlock()
	return base.RoundTrip(request)
}
