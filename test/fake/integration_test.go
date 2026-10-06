package fake_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	responsecache "github.com/HarnageaGabriel/trasloco/internal/cache"
	"github.com/HarnageaGabriel/trasloco/internal/credpool"
	"github.com/HarnageaGabriel/trasloco/internal/httpx"
	"github.com/HarnageaGabriel/trasloco/test/fake"
)

func TestFleetListingAndRevalidation(t *testing.T) {
	baseline := runtime.NumGoroutine()
	clock := fake.NewClock()
	server := fake.NewServer(fake.Config{
		Seed: 42, Clock: clock, GitHubRepos: 2000, AzureProjects: 3, AzurePipelines: 3000,
		PageSize: 100, GitHubPrimaryLimit: 5000, GitHubSecondaryPoints: 10,
		AzureTSTULimit: 12, AzureDelayRemaining: 1, AzureDelay: 301 * time.Second,
	})
	defer server.Close()
	host, _ := url.Parse(server.URL())
	pool, err := credpool.New(map[string][]credpool.Credential{
		host.Host: {{ID: "fleet-1", Actor: "fleet-actor", TargetScope: "org", ScopeID: "read-org", Source: credpool.StaticToken("test-token")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := responsecache.Open(responsecache.Options{
		Dir: filepath.Join(t.TempDir(), "responses"), MaxBytes: 64 << 20, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	observer := &countingObserver{}
	githubClient := httpx.NewClient(httpx.Options{
		Platform: "github", Credentials: pool, TargetScope: "org", Cache: store, Clock: clock, Observer: observer,
		MaxConcurrency: 16, Jitter: noJitter,
	})
	azureClient := httpx.NewClient(httpx.Options{
		Platform: "azuredevops", Credentials: pool, TargetScope: "org", Cache: store, Clock: clock, Observer: observer,
		MaxConcurrency: 16, Jitter: noJitter,
	})

	if got := listGitHub(t, githubClient, server.URL()+"/orgs/acme/repos?per_page=100"); got != 2000 {
		t.Fatalf("GitHub repos = %d, want 2000", got)
	}
	if got := listAzure(t, azureClient, server.URL()+"/acme/core/_apis/pipelines"); got != 3000 {
		t.Fatalf("ADO pipelines = %d, want 3000", got)
	}
	first := server.Snapshot()
	if first.AzureThrottle != 0 {
		t.Fatalf("first pass unhandled ADO throttles = %d, want 0", first.AzureThrottle)
	}
	if first.AzureDelays == 0 {
		t.Fatal("ADO server never emitted a successful proactive delay")
	}
	if !containsWaitAtLeast(clock.Sleeps(), 301*time.Second) {
		t.Fatal("ADO successful delay did not slow the client before the next request")
	}
	if observer.throttles() == 0 {
		t.Fatal("client did not observe throttle signals")
	}

	if got := listGitHub(t, githubClient, server.URL()+"/orgs/acme/repos?per_page=100"); got != 2000 {
		t.Fatalf("cached GitHub repos = %d, want 2000", got)
	}
	if got := listAzure(t, azureClient, server.URL()+"/acme/core/_apis/pipelines"); got != 3000 {
		t.Fatalf("cached ADO pipelines = %d, want 3000", got)
	}
	second := server.Snapshot()
	deltaRequests := second.Requests - first.Requests
	deltaNon304 := second.Non304 - first.Non304
	if deltaRequests == 0 || float64(deltaNon304)/float64(deltaRequests) >= 0.10 {
		t.Fatalf("second pass non-304 ratio = %d/%d, want < 10%%", deltaNon304, deltaRequests)
	}
	if observer.cacheHits() < 50 {
		t.Fatalf("cache hits = %d, want at least 50", observer.cacheHits())
	}

	githubClient.CloseIdleConnections()
	azureClient.CloseIdleConnections()
	runtime.GC()
	if after := runtime.NumGoroutine(); after > baseline+10 {
		t.Fatalf("goroutines before/after = %d/%d, tolerance 10", baseline, after)
	}
}

func listGitHub(t *testing.T, client *http.Client, endpoint string) int {
	t.Helper()
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	request = httpx.WithCache(request)
	total := 0
	//nolint:bodyclose // Each yielded response is closed in the loop.
	for response, err := range httpx.LinkPages(request.Context(), client, request) {
		if err != nil {
			t.Fatal(err)
		}
		var values []json.RawMessage
		if err := json.NewDecoder(response.Body).Decode(&values); err != nil {
			_ = response.Body.Close()
			t.Fatal(err)
		}
		_ = response.Body.Close()
		total += len(values)
	}
	return total
}

func listAzure(t *testing.T, client *http.Client, endpoint string) int {
	t.Helper()
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	request = httpx.WithCache(request)
	total := 0
	//nolint:bodyclose // Each yielded response is closed in the loop.
	for response, err := range httpx.AzureDevOpsPages(request.Context(), client, request) {
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Value []json.RawMessage `json:"value"`
		}
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			_ = response.Body.Close()
			t.Fatal(err)
		}
		_ = response.Body.Close()
		total += len(envelope.Value)
	}
	return total
}

type countingObserver struct {
	mu       sync.Mutex
	throttle int
	cacheHit int
	requests int
	retries  int
}

func (observer *countingObserver) OnRequest(httpx.RequestObservation) {
	observer.mu.Lock()
	observer.requests++
	observer.mu.Unlock()
}

func (observer *countingObserver) OnThrottle(httpx.ThrottleObservation) {
	observer.mu.Lock()
	observer.throttle++
	observer.mu.Unlock()
}

func (observer *countingObserver) OnRetry(httpx.RetryObservation) {
	observer.mu.Lock()
	observer.retries++
	observer.mu.Unlock()
}

func (observer *countingObserver) OnCacheHit(httpx.CacheHitObservation) {
	observer.mu.Lock()
	observer.cacheHit++
	observer.mu.Unlock()
}

func (observer *countingObserver) throttles() int {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return observer.throttle
}

func (observer *countingObserver) cacheHits() int {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return observer.cacheHit
}

func noJitter(duration time.Duration) time.Duration { return duration }

func containsWaitAtLeast(waits []time.Duration, minimum time.Duration) bool {
	for _, wait := range waits {
		if wait >= minimum {
			return true
		}
	}
	return false
}
