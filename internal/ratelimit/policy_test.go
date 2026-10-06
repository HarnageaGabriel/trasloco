package ratelimit

import (
	"net/http"
	"testing"
	"time"
)

func TestRateLimitPolicies(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name      string
		policy    Policy
		status    int
		header    http.Header
		remaining int64
		retry     time.Duration
		delay     time.Duration
		near      bool
		throttle  bool
	}{
		{
			name: "github primary", policy: GitHubPolicy{}, status: 403,
			header: http.Header{
				"X-Ratelimit-Limit": {"5000"}, "X-Ratelimit-Remaining": {"0"},
				"X-Ratelimit-Reset": {"1700000060"},
			},
			remaining: 0, throttle: true, near: true,
		},
		{
			name: "github secondary retry", policy: GitHubPolicy{}, status: 403,
			header:    http.Header{"Retry-After": {"12"}, "X-Ratelimit-Remaining": {"42"}},
			remaining: 42, retry: 12 * time.Second, throttle: true,
		},
		{
			name: "azure successful delay", policy: AzureDevOpsPolicy{}, status: 200,
			header: http.Header{
				"Retry-After": {"2"}, "X-Ratelimit-Delay": {"1.5"},
				"X-Ratelimit-Remaining": {"7"}, "X-Ratelimit-Cost": {"3"},
			},
			remaining: 7, retry: 2 * time.Second, delay: 1500 * time.Millisecond, throttle: true,
		},
		{
			name: "gitlab", policy: GitLabPolicy{}, status: 429,
			header:    http.Header{"Ratelimit-Remaining": {"0"}, "Retry-After": {"4"}},
			remaining: 0, retry: 4 * time.Second, throttle: true,
		},
		{
			name: "bitbucket near", policy: BitbucketPolicy{}, status: 200,
			header:    http.Header{"X-Ratelimit-Nearlimit": {"true"}},
			remaining: -1, near: true, throttle: true,
		},
		{
			name: "ghes disabled", policy: GitHubPolicy{}, status: 200,
			header: http.Header{}, remaining: -1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.policy.Parse(&http.Response{StatusCode: test.status, Header: test.header}, now)
			if got.Remaining != test.remaining || got.RetryAfter != test.retry || got.Delay != test.delay ||
				got.NearLimit != test.near || got.Throttled != test.throttle {
				t.Fatalf("Parse() = %+v", got)
			}
		})
	}
}

func TestGitHubCost(t *testing.T) {
	policy := GitHubPolicy{}
	if got := policy.Cost(http.MethodGet, false); got != 1 {
		t.Fatalf("GET cost = %d, want 1", got)
	}
	if got := policy.Cost(http.MethodPost, true); got != 5 {
		t.Fatalf("POST cost = %d, want 5", got)
	}
}
