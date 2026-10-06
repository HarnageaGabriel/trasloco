package httpx

import "github.com/HarnageaGabriel/trasloco/internal/ratelimit"

// Budget is owned by ratelimit.
type Budget = ratelimit.Budget

// RateLimitPolicy interprets provider rate-limit headers.
type RateLimitPolicy = ratelimit.Policy

// GitHubPolicy implements GitHub limits.
type GitHubPolicy = ratelimit.GitHubPolicy

// AzureDevOpsPolicy implements TSTU response signaling.
type AzureDevOpsPolicy = ratelimit.AzureDevOpsPolicy

// GitLabPolicy implements GitLab limits.
type GitLabPolicy = ratelimit.GitLabPolicy

// BitbucketPolicy implements Bitbucket limits.
type BitbucketPolicy = ratelimit.BitbucketPolicy

// GenericPolicy interprets conventional limit headers.
type GenericPolicy = ratelimit.GenericPolicy

// PolicyFor selects the provider policy.
func PolicyFor(platform string) RateLimitPolicy { return ratelimit.PolicyFor(platform) }
