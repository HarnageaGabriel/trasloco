package httpx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/ratelimit"

	responsecache "github.com/HarnageaGabriel/trasloco/internal/cache"
	"github.com/HarnageaGabriel/trasloco/internal/credpool"
)

type credentialContextKey struct{}

type requestCredential struct {
	ID      string
	ScopeID string
}

func credentialFrom(ctx context.Context) requestCredential {
	credential, _ := ctx.Value(credentialContextKey{}).(requestCredential)
	return credential
}

// Options configures a complete HTTP transport chain.
type Options struct {
	TargetScope    string
	Limiter        *ratelimit.Limiter
	MaxCacheBody   int64
	CacheError     func(error)
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
	return &http.Client{Transport: NewTransport(opts), CheckRedirect: CheckRedirect}
}

// NewTransport builds auth -> observation -> cache -> retry -> limiter -> base transport.
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
	limiter := opts.Limiter
	if limiter == nil {
		limiter = NewLimiter(LimiterOptions{
			Clock: opts.Clock, OnThrottle: opts.Observer.OnThrottle, Policy: opts.Policy,
			MinConcurrency: opts.MinConcurrency, MaxConcurrency: opts.MaxConcurrency,
		})
	}
	var chain http.RoundTripper = &limiterTransport{next: base, limiter: limiter, policy: opts.Policy}
	chain = newRetryTransport(chain, RetryOptions{
		Clock: opts.Clock, Observer: opts.Observer, Policy: opts.Policy, Jitter: opts.Jitter,
		MaxAttempts: opts.MaxAttempts, MaxTotalWait: opts.MaxTotalWait,
		BlockedUntil: func(request *http.Request) time.Time {
			return limiter.BlockedUntil(limitKey(request.URL.Host, credentialFrom(request.Context()).ID))
		},
	})
	if opts.Cache != nil {
		if opts.MaxCacheBody <= 0 {
			opts.MaxCacheBody = 1 << 20
		}
		chain = &cacheTransport{next: chain, store: opts.Cache, observer: opts.Observer, maxBody: opts.MaxCacheBody, onError: opts.CacheError}
	}
	chain = &observingTransport{next: chain, clock: opts.Clock, observer: opts.Observer}
	return &authTransport{
		next: chain, pool: opts.Credentials, policy: opts.Policy, authScheme: opts.AuthScheme, targetScope: opts.TargetScope,
	}
}

type authTransport struct {
	next        http.RoundTripper
	pool        *credpool.Pool
	policy      RateLimitPolicy
	authScheme  string
	targetScope string
}

func (transport *authTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if anonymousFrom(request) || presigned(request.URL) {
		return transport.next.RoundTrip(Anonymous(request))
	}
	if transport.pool == nil {
		return transport.next.RoundTrip(request)
	}
	cost := requestCost(request, transport.policy)
	scope, _ := request.Context().Value(targetScopeKey).(string)
	if scope == "" {
		scope = transport.targetScope
	}
	credential, err := transport.pool.Acquire(request.Context(), request.URL.Host, scope, cost.Points)
	if err != nil {
		return nil, err
	}
	token, err := credential.Source.Token(request.Context())
	if err != nil {
		return nil, err
	}
	cloned := request.Clone(context.WithValue(request.Context(), credentialContextKey{}, requestCredential{
		ID: pacingIdentity(credential.Actor), ScopeID: credential.ScopeID,
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
		StatusCode: statusCode(response), Duration: transport.clock.Now().Sub(started), Err: safeError(err),
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
	maxBody  int64
	onError  func(error)
}

func (transport *cacheTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	enabled, _ := request.Context().Value(cacheKey).(bool)
	if !enabled || anonymousFrom(request) || request.Method != http.MethodGet {
		return transport.next.RoundTrip(request)
	}
	credential := credentialFrom(request.Context())
	key := responsecache.Key(request, credential.ScopeID)
	cached, found, cacheErr := transport.store.Get(key)
	if cacheErr != nil {
		transport.cacheError("cache lookup failed")
	}
	found = found && int64(len(cached.Body)) <= transport.maxBody && (cached.Entry.ETag != "" || cached.Entry.Header.Get("Last-Modified") != "")
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
	if response.StatusCode == http.StatusOK && response.Body != nil &&
		(response.Header.Get("ETag") != "" || response.Header.Get("Last-Modified") != "") &&
		response.ContentLength <= transport.maxBody {
		header := response.Header.Clone()
		response.Body = &cacheBody{ReadCloser: response.Body, cap: transport.maxBody, save: func(body []byte) {
			if _, putErr := transport.store.Put(key, response.StatusCode, header, body); putErr != nil {
				transport.cacheError("cache write failed")
			}
		}}
	}

	return response, nil
}

func cachedStatus(code int) string {
	if code == 0 {
		code = http.StatusOK
	}
	return strconv.Itoa(code) + " " + http.StatusText(code)
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

// cacheBody buffers only as the caller reads, stops at the cap, and writes only
// a complete successful body. A partial read or close never populates the cache.
type cacheBody struct {
	io.ReadCloser
	cap  int64
	data []byte
	done bool
	save func([]byte)
}

func (body *cacheBody) Read(p []byte) (int, error) {
	n, err := body.ReadCloser.Read(p)
	if !body.done {
		if int64(len(body.data))+int64(n) > body.cap || (err != nil && err != io.EOF) {
			body.done = true
			body.data = nil
		} else {
			body.data = append(body.data, p[:n]...)
			if err == io.EOF {
				body.done = true
				body.save(body.data)
				body.data = nil
			}
		}
	}
	return n, err
}

func (transport *cacheTransport) cacheError(message string) {
	if transport.onError != nil {
		transport.onError(errors.New(message))
	}
}

func safeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New("HTTP request failed")
}

// WithTargetScope selects the preflight-verified credential target.
func WithTargetScope(request *http.Request, scope string) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), targetScopeKey, scope))
}

// WithCost overrides provider request cost (including GraphQL query points).
func WithCost(request *http.Request, cost ratelimit.Cost) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), costKey, cost))
}

// WithCache opts a request into bounded validator-based revalidation.
func WithCache(request *http.Request) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), cacheKey, true))
}

func requestCost(request *http.Request, policy RateLimitPolicy) ratelimit.Cost {
	cost, _ := request.Context().Value(costKey).(ratelimit.Cost)
	if cost.Points <= 0 {
		cost.Points = policy.Cost(request.Method, false)
	}
	cost.ContentCreation = cost.ContentCreation || contentCreationFrom(request.Context())
	return cost
}

// Anonymous prevents credential selection and removes caller-provided secrets.
// Only representation headers are retained; URL signatures are left intact.
func Anonymous(request *http.Request) *http.Request {
	cloned := request.Clone(context.WithValue(request.Context(), anonymousKey, true))
	cloned.URL.User = nil
	cloned.Header = make(http.Header)
	for _, key := range []string{"Accept", "Accept-Encoding", "User-Agent", "Range"} {
		if values := request.Header.Values(key); len(values) > 0 {
			cloned.Header[key] = append([]string(nil), values...)
		}
	}
	return cloned
}

func anonymousFrom(request *http.Request) bool {
	value, _ := request.Context().Value(anonymousKey).(bool)
	return value
}

func presigned(value *url.URL) bool {
	for key := range value.Query() {
		switch strings.ToLower(key) {
		case "x-amz-signature", "x-goog-signature", "signature", "sig":
			return true
		}
	}
	return false
}

func origin(value *url.URL) string {
	port := value.Port()
	if port == "" {
		if strings.EqualFold(value.Scheme, "https") {
			port = "443"
		} else {
			port = "80"
		}
	}
	return strings.ToLower(value.Scheme) + "://" + strings.ToLower(strings.TrimSuffix(value.Hostname(), ".")) + ":" + port
}

// CheckRedirect keeps cross-origin and pre-signed redirects anonymous for the
// entire remaining chain, including a redirect back to the initial origin.
func CheckRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	for _, previous := range via {
		if anonymousFrom(previous) || origin(previous.URL) != origin(request.URL) || presigned(previous.URL) {
			*request = *Anonymous(request)
			return nil
		}
	}
	if presigned(request.URL) {
		*request = *Anonymous(request)
	}
	return nil
}

func pacingIdentity(actor string) string {
	sum := sha256.Sum256([]byte(actor))
	return hex.EncodeToString(sum[:])
}
