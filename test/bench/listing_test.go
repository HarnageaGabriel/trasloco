package bench_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	responsecache "github.com/HarnageaGabriel/trasloco/internal/cache"
	"github.com/HarnageaGabriel/trasloco/internal/credpool"
	"github.com/HarnageaGabriel/trasloco/internal/httpx"
	"github.com/HarnageaGabriel/trasloco/test/fake"
)

func BenchmarkList5000Repos(benchmark *testing.B) {
	clock := fake.NewClock()
	server := fake.NewServer(fake.Config{
		Seed: 99, Clock: clock, GitHubRepos: 5000, PageSize: 100,
		GitHubPrimaryLimit: 5000, GitHubSecondaryPoints: 20,
	})
	defer server.Close()
	parsed, _ := url.Parse(server.URL())
	pool, err := credpool.New(map[string][]credpool.Credential{
		parsed.Host: {{ID: "benchmark", ScopeID: "repo-read", Source: credpool.StaticToken("benchmark-token")}},
	})
	if err != nil {
		benchmark.Fatal(err)
	}
	store, err := responsecache.Open(responsecache.Options{
		Dir: filepath.Join(benchmark.TempDir(), "cache"), MaxBytes: 64 << 20, Clock: clock,
	})
	if err != nil {
		benchmark.Fatal(err)
	}
	observer := &benchmarkObserver{}
	client := httpx.NewClient(httpx.Options{
		Platform: "github", Credentials: pool, Cache: store, Clock: clock, Observer: observer,
		MaxConcurrency: 16, Jitter: func(duration time.Duration) time.Duration { return duration },
	})
	defer client.CloseIdleConnections()
	endpoint := server.URL() + "/orgs/acme/repos?per_page=100"

	benchmark.ResetTimer()
	for range benchmark.N {
		if got := list(benchmark, client, endpoint); got != 5000 {
			benchmark.Fatalf("first listing = %d, want 5000", got)
		}
		if got := list(benchmark, client, endpoint); got != 5000 {
			benchmark.Fatalf("revalidated listing = %d, want 5000", got)
		}
	}
	benchmark.StopTimer()
	stats := server.Snapshot()
	benchmark.ReportMetric(float64(stats.Requests)/float64(benchmark.N), "requests/op")
	benchmark.ReportMetric(float64(stats.NotModified)/float64(max(1, stats.Requests)), "304-ratio")
	benchmark.ReportMetric(float64(stats.GitHubThrottle)/float64(benchmark.N), "throttles/op")
}

func list(benchmark *testing.B, client *http.Client, endpoint string) int {
	benchmark.Helper()
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	total := 0
	//nolint:bodyclose // Each yielded response is closed in the loop.
	for response, err := range httpx.LinkPages(request.Context(), client, request) {
		if err != nil {
			benchmark.Fatal(err)
		}
		var values []json.RawMessage
		if err := json.NewDecoder(response.Body).Decode(&values); err != nil {
			_ = response.Body.Close()
			benchmark.Fatal(err)
		}
		_ = response.Body.Close()
		total += len(values)
	}
	return total
}

type benchmarkObserver struct{ throttles atomic.Int64 }

func (*benchmarkObserver) OnRequest(httpx.RequestObservation) {}
func (observer *benchmarkObserver) OnThrottle(httpx.ThrottleObservation) {
	observer.throttles.Add(1)
}
func (*benchmarkObserver) OnRetry(httpx.RetryObservation)       {}
func (*benchmarkObserver) OnCacheHit(httpx.CacheHitObservation) {}
