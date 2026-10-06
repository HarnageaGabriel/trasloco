package httpx

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/credpool"
)

// Budget is the common rate-limit representation shared with credpool.
type Budget = credpool.Budget

// RateLimitPolicy interprets platform-specific response headers and request
// costs. Missing limits must produce Remaining == -1.
type RateLimitPolicy interface {
	Name() string
	Parse(*http.Response, time.Time) Budget
	Cost(method string, contentCreation bool) int64
}

// PolicyFor returns the built-in policy named by platform.
func PolicyFor(platform string) RateLimitPolicy {
	switch strings.ToLower(platform) {
	case "github", "ghes":
		return GitHubPolicy{}
	case "azuredevops", "azure-devops", "ado":
		return AzureDevOpsPolicy{}
	case "gitlab":
		return GitLabPolicy{}
	case "bitbucket", "bitbucket-cloud", "bitbucket-dc":
		return BitbucketPolicy{}
	default:
		return GenericPolicy{}
	}
}

// GitHubPolicy implements GitHub primary and secondary limit semantics.
type GitHubPolicy struct{}

// Name implements RateLimitPolicy.
func (GitHubPolicy) Name() string { return "github" }

// Parse implements RateLimitPolicy.
func (GitHubPolicy) Parse(response *http.Response, now time.Time) Budget {
	header := response.Header
	budget := baseBudget(header, now, "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset")
	budget.Used = parseInt(header.Get("X-RateLimit-Used"), 0)
	budget.Resource = header.Get("X-RateLimit-Resource")
	budget.Throttled = response.StatusCode == http.StatusTooManyRequests ||
		(response.StatusCode == http.StatusForbidden && (budget.RetryAfter > 0 || budget.Remaining == 0))
	return budget
}

// Cost implements RateLimitPolicy.
func (GitHubPolicy) Cost(method string, _ bool) int64 {
	if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
		return 1
	}
	return 5
}

// AzureDevOpsPolicy implements TSTU response signaling, including successful
// responses carrying a delay.
type AzureDevOpsPolicy struct{}

// Name implements RateLimitPolicy.
func (AzureDevOpsPolicy) Name() string { return "azuredevops" }

// Parse implements RateLimitPolicy.
func (AzureDevOpsPolicy) Parse(response *http.Response, now time.Time) Budget {
	header := response.Header
	budget := baseBudget(header, now, "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset")
	budget.Delay = parseDurationSeconds(header.Get("X-RateLimit-Delay"))
	budget.Cost = parseInt(header.Get("X-RateLimit-Cost"), 0)
	budget.Resource = header.Get("X-RateLimit-Resource")
	budget.Throttled = response.StatusCode == http.StatusTooManyRequests || budget.RetryAfter > 0 || budget.Delay > 0
	return budget
}

// Cost implements RateLimitPolicy.
func (AzureDevOpsPolicy) Cost(string, bool) int64 { return 1 }

// GitLabPolicy interprets GitLab RateLimit headers.
type GitLabPolicy struct{}

// Name implements RateLimitPolicy.
func (GitLabPolicy) Name() string { return "gitlab" }

// Parse implements RateLimitPolicy.
func (GitLabPolicy) Parse(response *http.Response, now time.Time) Budget {
	budget := baseBudget(response.Header, now, "RateLimit-Limit", "RateLimit-Remaining", "RateLimit-Reset")
	budget.Throttled = response.StatusCode == http.StatusTooManyRequests
	return budget
}

// Cost implements RateLimitPolicy.
func (GitLabPolicy) Cost(string, bool) int64 { return 1 }

// BitbucketPolicy interprets Bitbucket Cloud limit and NearLimit signals.
type BitbucketPolicy struct{}

// Name implements RateLimitPolicy.
func (BitbucketPolicy) Name() string { return "bitbucket" }

// Parse implements RateLimitPolicy.
func (BitbucketPolicy) Parse(response *http.Response, now time.Time) Budget {
	budget := baseBudget(response.Header, now, "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset")
	budget.Resource = response.Header.Get("X-RateLimit-Resource")
	budget.NearLimit = strings.EqualFold(response.Header.Get("X-RateLimit-NearLimit"), "true")
	budget.Throttled = response.StatusCode == http.StatusTooManyRequests || budget.NearLimit
	return budget
}

// Cost implements RateLimitPolicy.
func (BitbucketPolicy) Cost(string, bool) int64 { return 1 }

// GenericPolicy recognizes Retry-After and conventional X-RateLimit headers.
type GenericPolicy struct{}

// Name implements RateLimitPolicy.
func (GenericPolicy) Name() string { return "generic" }

// Parse implements RateLimitPolicy.
func (GenericPolicy) Parse(response *http.Response, now time.Time) Budget {
	budget := baseBudget(response.Header, now, "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset")
	budget.Throttled = response.StatusCode == http.StatusTooManyRequests
	return budget
}

// Cost implements RateLimitPolicy.
func (GenericPolicy) Cost(string, bool) int64 { return 1 }

func baseBudget(header http.Header, now time.Time, limitName, remainingName, resetName string) Budget {
	budget := Budget{Remaining: -1}
	budget.Limit = parseInt(header.Get(limitName), 0)
	if raw := header.Get(remainingName); raw != "" {
		budget.Remaining = parseInt(raw, -1)
	}
	if seconds := parseInt(header.Get(resetName), 0); seconds > 0 {
		budget.Reset = time.Unix(seconds, 0)
	}
	budget.RetryAfter = parseRetryAfter(header.Get("Retry-After"), now)
	return budget
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
		return max(0, time.Duration(seconds*float64(time.Second)))
	}
	if parsed, err := http.ParseTime(value); err == nil {
		return max(0, parsed.Sub(now))
	}
	return 0
}

func parseDurationSeconds(value string) time.Duration {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0
	}
	return max(0, time.Duration(seconds*float64(time.Second)))
}

func parseInt(value string, fallback int64) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}
