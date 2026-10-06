// Package credpool selects credentials independently for each platform host.
//
// Credentials are keyed by actor and target scope. Registering multiple tokens
// for the same actor, host, and target scope is rejected: credential pooling
// must not multiply one actor's platform rate-limit throughput.
package credpool

import (
	"context"
	"errors"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

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
	hosts := make([]string, 0, len(credentials))
	for host := range credentials {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	for _, host := range hosts {
		entries := credentials[host]
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
	credential.Actor = strings.TrimSpace(credential.Actor)
	credential.TargetScope = strings.TrimSpace(credential.TargetScope)
	if credential.Actor == "" || credential.TargetScope == "" {
		return errors.New("verified actor and target scope are required")
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

// ActorResolver is implemented by platform preflight adapters. It must return a
// verified, non-secret provider identity; configured labels are not identities.
type ActorResolver interface {
	ResolveActor(ctx context.Context, host, targetScope string, source TokenSource) (string, error)
}

// Acquire selects only within host and target scope. Cost is validated here;
// reservations and pacing belong exclusively to ratelimit.
func (p *Pool) Acquire(ctx context.Context, host, scope string, cost int64) (Credential, error) {
	if err := ctx.Err(); err != nil {
		return Credential{}, err
	}
	if cost < 0 {
		return Credential{}, errors.New("credential cost must not be negative")
	}
	host, scope = normalizeHost(host), strings.TrimSpace(scope)
	p.mu.Lock()
	defer p.mu.Unlock()
	hp := p.hosts[host]
	if hp != nil && scope != "" {
		for offset := range len(hp.credentials) {
			i := (hp.next + offset) % len(hp.credentials)
			if hp.credentials[i].TargetScope == scope {
				hp.next = (i + 1) % len(hp.credentials)
				return hp.credentials[i], nil
			}
		}
	}
	return Credential{}, errors.New("no credentials for requested host and target scope")
}

func normalizeHost(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if strings.Contains(raw, "://") {
		if parsed, err := url.Parse(raw); err == nil {
			raw = parsed.Host
		}
	}
	host, port, err := net.SplitHostPort(raw)
	if err == nil {
		return net.JoinHostPort(strings.TrimSuffix(host, "."), port)
	}
	return strings.TrimSuffix(raw, ".")
}
