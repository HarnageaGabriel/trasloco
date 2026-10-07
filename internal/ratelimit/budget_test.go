package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/clock"
)

// Source: docs/adr/0007-http-auth-retries-limits.md. GraphQL secondary ceiling:
// https://docs.github.com/en/graphql/overview/rate-limits-and-query-limits-for-the-graphql-api
func TestGitHubWindows(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cost    Cost
		count   int
		minimum time.Duration
	}{
		{"REST points", Cost{Points: 5}, 181, time.Minute},
		{"GraphQL secondary", Cost{Points: 100, GraphQL: true, SecondaryPoints: 5}, 401, time.Minute},
		{"minute writes", Cost{Points: 1, ContentCreation: true}, 81, time.Minute},
		{"hour writes", Cost{Points: 1, ContentCreation: true}, 501, time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newTestClock()
			start := clock.Now()
			limiter := NewLimiter(LimiterOptions{Clock: clock, Policy: GitHubPolicy{}})
			for range tc.count {
				if err := limiter.Acquire(t.Context(), "key", tc.cost); err != nil {
					t.Fatal(err)
				}
				limiter.Release("key", nil)
			}
			if elapsed := clock.Now().Sub(start); elapsed < tc.minimum {
				t.Fatalf("elapsed %v < %v", elapsed, tc.minimum)
			}
		})
	}
}

func TestPrimaryCostAndProactivePacing(t *testing.T) {
	for _, graphql := range []bool{false, true} {
		clock := newTestClock()
		limiter := NewLimiter(LimiterOptions{Clock: clock, Policy: GitHubPolicy{}})
		resource := "core"
		if graphql {
			resource = "graphql"
		}
		response := &http.Response{StatusCode: 200, Header: make(http.Header)}
		response.Header.Set("X-RateLimit-Limit", "100")
		response.Header.Set("X-RateLimit-Remaining", "2")
		response.Header.Set("X-RateLimit-Reset", strconv.FormatInt(clock.Now().Add(time.Minute).Unix(), 10))
		response.Header.Set("X-RateLimit-Resource", resource)
		limiter.Release("key", response)
		start := clock.Now()
		cost := Cost{Points: 1}
		if graphql {
			cost = Cost{Points: 3, GraphQL: true}
		}
		if err := limiter.Acquire(t.Context(), "key", cost); err != nil {
			t.Fatal(err)
		}
		minimum := 30 * time.Second
		if graphql {
			minimum = time.Minute
		}
		if elapsed := clock.Now().Sub(start); elapsed < minimum {
			t.Fatalf("elapsed %v", elapsed)
		}
		limiter.Release("key", nil)
	}
}

func TestPacerAdjustedInPlaceAndServerBlock(t *testing.T) {
	clock := newTestClock()
	limiter := NewLimiter(LimiterOptions{Clock: clock, Policy: AzureDevOpsPolicy{}})
	key := Key("host", "credential")
	response := &http.Response{StatusCode: 200, Header: make(http.Header)}
	response.Header.Set("X-RateLimit-Remaining", "10")
	response.Header.Set("X-RateLimit-Reset", strconv.FormatInt(clock.Now().Add(time.Minute).Unix(), 10))
	limiter.Release(key, response)
	pacer := limiter.states[key].pacer
	response.Header.Set("X-RateLimit-Remaining", "9")
	response.Header.Set("Retry-After", "20")
	limiter.Release(key, response)
	if pacer != limiter.states[key].pacer {
		t.Fatal("pacer replaced")
	}
	if !limiter.BlockedUntil(key).Equal(clock.Now().Add(20 * time.Second)) {
		t.Fatal("successful Retry-After ignored")
	}
	if err := limiter.Acquire(t.Context(), key, Cost{}); err != nil {
		t.Fatal(err)
	}
	limiter.Release(key, &http.Response{StatusCode: 200, Header: make(http.Header)})
}

func TestCancellationAndConcurrency(t *testing.T) {
	clock := newTestClock()
	limiter := NewLimiter(LimiterOptions{Clock: clock, MaxConcurrency: 1})
	if err := limiter.Acquire(t.Context(), "key", Cost{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- limiter.Acquire(ctx, "key", Cost{}) }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	limiter.Release("key", nil)
	if err := limiter.Acquire(t.Context(), "key", Cost{}); err != nil {
		t.Fatal(err)
	}
	limiter.Release("key", nil)
	github := NewLimiter(LimiterOptions{Clock: clock, Policy: GitHubPolicy{}})
	if err := github.Acquire(t.Context(), "key", Cost{Points: 901}); err == nil {
		t.Fatal("oversized cost")
	}
	if err := github.Acquire(t.Context(), "key", Cost{GraphQL: true}); err != nil {
		t.Fatal(err)
	}
	github.Release("key", nil)
}

func TestPolicyAliasesAndMalformedHeaders(t *testing.T) {
	for _, tc := range []struct{ alias, name string }{{"github", "github"}, {"ghes", "github"}, {"ado", "azuredevops"}, {"gitlab", "gitlab"}, {"bitbucket-cloud", "bitbucket"}, {"unknown", "generic"}} {
		policy := PolicyFor(tc.alias)
		if policy.Name() != tc.name || policy.Cost(http.MethodGet, false) != 1 {
			t.Fatal(tc)
		}
		response := &http.Response{StatusCode: 429, Header: make(http.Header)}
		response.Header.Set("Retry-After", "invalid")
		response.Header.Set("X-RateLimit-Delay", "invalid")
		response.Header.Set("X-RateLimit-Remaining", "bad")
		if budget := policy.Parse(response, time.Unix(0, 0)); budget.Remaining != -1 {
			t.Fatal(budget)
		}
	}
	now := time.Unix(1700000000, 0)
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{{"", 0}, {"-1", 0}, {"bad", 0}, {now.Add(time.Minute).UTC().Format(http.TimeFormat), time.Minute}, {"1.5", 1500 * time.Millisecond}} {
		if got := parseRetryAfter(tc.value, now); got != tc.want {
			t.Fatalf("%s: %v", tc.value, got)
		}
	}
	realClock := clock.Real{}
	if realClock.Now().IsZero() {
		t.Fatal("clock")
	}
	if err := realClock.Sleep(t.Context(), time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	if err := realClock.Sleep(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := realClock.Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	_ = NewLimiter(LimiterOptions{})
}
