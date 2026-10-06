// Package credpool selects credentials independently for each platform host.
//
// Credentials are keyed by actor and target scope. Registering multiple tokens
// for the same actor, host, and target scope is rejected: credential pooling
// must not multiply one actor's platform rate-limit throughput.
package credpool

import (
	"context"
	"errors"
	"math"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

// Budget is the normalized rate-limit state observed for one credential.
// A negative Remaining value means the server did not report a limit.
type Budget struct {
	Limit      int64
	Remaining  int64
	Used       int64
	Reset      time.Time
	RetryAfter time.Duration
	Delay      time.Duration
	Cost       int64
	NearLimit  bool
	Throttled  bool
	Resource   string
}

// TokenSource returns current authentication material. Implementations may
// refresh it; callers must not retain the token beyond one request.
type TokenSource interface {
	Token(context.Context) (string, error)
}

// StaticToken is a TokenSource for non-refreshing personal access tokens.
type StaticToken string

// Token implements TokenSource.
func (s StaticToken) Token(context.Context) (string, error) { return string(s), nil }

// Credential identifies a token, the actor it authenticates, and its target
// scope. Actor and TargetScope must be stable non-secret identity keys. ScopeID,
// unlike the token, is safe to persist in cache keys.
type Credential struct {
	ID          string
	Actor       string
	TargetScope string
	ScopeID     string
	Source      TokenSource
	Budget      Budget
}

// RegistrationError reports a credential-policy violation with its stable
// diagnostic. Finding contains no token material.
type RegistrationError struct {
	Finding diag.Diagnostic
}

// Error implements error.
func (e *RegistrationError) Error() string { return e.Finding.Message }

// Pool holds credentials grouped by normalized host.
type Pool struct {
	mu    sync.Mutex
	hosts map[string]*hostPool
}

type hostPool struct {
	credentials []Credential
	next        int
}

// New constructs a pool. Map keys may be hostnames or absolute URLs.
func New(credentials map[string][]Credential) (*Pool, error) {
	p := &Pool{hosts: make(map[string]*hostPool, len(credentials))}
	for host, entries := range credentials {
		for _, credential := range entries {
			if err := p.Add(host, credential); err != nil {
				return nil, err
			}
		}
	}
	return p, nil
}

// Add registers a credential for host.
func (p *Pool) Add(host string, credential Credential) error {
	if credential.ID == "" || credential.Source == nil {
		return errors.New("credential ID and token source are required")
	}
	host = normalizeHost(host)
	if host == "" {
		return errors.New("credential host is required")
	}
	if credential.ScopeID == "" {
		credential.ScopeID = credential.ID
	}
	if credential.Actor == "" {
		credential.Actor = credential.ID
	}
	credential.Actor = strings.TrimSpace(credential.Actor)
	if credential.TargetScope == "" {
		credential.TargetScope = credential.ScopeID
	}
	credential.TargetScope = strings.TrimSpace(credential.TargetScope)
	if credential.Budget.Remaining == 0 && credential.Budget.Limit == 0 {
		credential.Budget.Remaining = math.MaxInt64
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hosts == nil {
		p.hosts = make(map[string]*hostPool)
	}
	hp := p.hosts[host]
	if hp == nil {
		hp = &hostPool{}
		p.hosts[host] = hp
	}
	for _, existing := range hp.credentials {
		if existing.ID == credential.ID {
			return errors.New("duplicate credential ID for host")
		}
		if existing.Actor == credential.Actor && existing.TargetScope == credential.TargetScope {
			finding, err := diag.New("TRL-AUTH-0006", diag.WithParams(map[string]any{
				"host": host, "targetScope": credential.TargetScope,
			}))
			if err != nil {
				return err
			}
			return &RegistrationError{Finding: finding}
		}
	}
	hp.credentials = append(hp.credentials, credential)
	return nil
}

// Acquire reserves cost from the credential with most reported capacity.
// Equal budgets rotate, preventing one token from receiving all requests.
func (p *Pool) Acquire(ctx context.Context, host string, cost int64) (Credential, error) {
	if err := ctx.Err(); err != nil {
		return Credential{}, err
	}
	host = normalizeHost(host)
	p.mu.Lock()
	defer p.mu.Unlock()
	hp := p.hosts[host]
	if hp == nil || len(hp.credentials) == 0 {
		return Credential{}, errors.New("no credentials for host " + host)
	}
	maxRemaining := int64(math.MinInt64)
	for i := range hp.credentials {
		if hp.credentials[i].Budget.Remaining > maxRemaining {
			maxRemaining = hp.credentials[i].Budget.Remaining
		}
	}
	chosen := -1
	for offset := range len(hp.credentials) {
		i := (hp.next + offset) % len(hp.credentials)
		if hp.credentials[i].Budget.Remaining == maxRemaining {
			chosen = i
			break
		}
	}
	hp.next = (chosen + 1) % len(hp.credentials)
	if cost > 0 && hp.credentials[chosen].Budget.Remaining != math.MaxInt64 {
		hp.credentials[chosen].Budget.Remaining = max(0, hp.credentials[chosen].Budget.Remaining-cost)
	}
	return hp.credentials[chosen], nil
}

// Release records server-observed state for a credential.
func (p *Pool) Release(host, credentialID string, observed Budget) {
	host = normalizeHost(host)
	p.mu.Lock()
	defer p.mu.Unlock()
	hp := p.hosts[host]
	if hp == nil {
		return
	}
	for i := range hp.credentials {
		if hp.credentials[i].ID == credentialID {
			if observed.Remaining >= 0 || observed.Limit > 0 || observed.Throttled {
				hp.credentials[i].Budget = observed
			}
			return
		}
	}
}

func normalizeHost(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if strings.Contains(raw, "://") {
		if parsed, err := url.Parse(raw); err == nil {
			raw = parsed.Host
		}
	}
	host, _, err := net.SplitHostPort(raw)
	if err == nil {
		return host
	}
	return strings.TrimSuffix(raw, ".")
}
