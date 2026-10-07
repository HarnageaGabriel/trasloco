package credpool

import (
	"context"
	"errors"
	"testing"
)

func TestAcquireScopeAndRoundRobin(t *testing.T) {
	pool, err := New(map[string][]Credential{
		"https://api.example.com": {
			{ID: "a", Actor: "actor-a", TargetScope: "org:acme", Source: StaticToken("one")},
			{ID: "b", Actor: "actor-b", TargetScope: "org:acme", Source: StaticToken("two")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := pool.Acquire(context.Background(), "api.example.com", "org:acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "a" {
		t.Fatalf("Acquire() ID = %q, want a", got.ID)
	}
	first, _ := pool.Acquire(context.Background(), "api.example.com", "org:acme", 0)
	second, _ := pool.Acquire(context.Background(), "api.example.com", "org:acme", 0)
	if first.ID == second.ID {
		t.Fatalf("matching credentials did not rotate: %q then %q", first.ID, second.ID)
	}
}

func TestRejectsDuplicateActorHostScope(t *testing.T) {
	_, err := New(map[string][]Credential{
		"https://api.example.com": {
			{ID: "a", Actor: "actor-1", TargetScope: "org:acme", Source: StaticToken("one")},
			{ID: "b", Actor: "actor-1", TargetScope: "org:acme", Source: StaticToken("two")},
		},
	})
	var registration *RegistrationError
	if !errors.As(err, &registration) {
		t.Fatalf("New() error = %v, want RegistrationError", err)
	}
	if registration.Finding.Code != "TRL-AUTH-0006" {
		t.Fatalf("diagnostic code = %q, want TRL-AUTH-0006", registration.Finding.Code)
	}
}

func TestRejectsDuplicateActorIgnoringCase(t *testing.T) {
	_, err := New(map[string][]Credential{
		"api.github.com": {
			{ID: "a", Actor: "Octocat", TargetScope: "org:acme", Source: StaticToken("one")},
			{ID: "b", Actor: "octocat", TargetScope: "org:acme", Source: StaticToken("two")},
		},
	})
	var registration *RegistrationError
	if !errors.As(err, &registration) || registration.Finding.Code != "TRL-AUTH-0006" {
		t.Fatalf("New() error = %v, want TRL-AUTH-0006", err)
	}
}

func TestAcquireCancellation(t *testing.T) {
	pool, _ := New(map[string][]Credential{"example.com": {{ID: "a", Actor: "actor", TargetScope: "org", Source: StaticToken("x")}}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pool.Acquire(ctx, "example.com", "org", 1); err == nil {
		t.Fatal("Acquire() unexpectedly succeeded")
	}
}

// Source: docs/spec/credentials.md CRD-001..006 and ADR0007.
func TestRegistrationAndSelectionBoundaries(t *testing.T) {
	valid := Credential{ID: "one", Actor: " actor ", TargetScope: " scope ", Source: StaticToken("secret")}
	for _, tc := range []struct {
		name, host string
		credential Credential
	}{
		{"no ID", "example.com", Credential{Actor: "a", TargetScope: "s", Source: StaticToken("x")}},
		{"no source", "example.com", Credential{ID: "x"}},
		{"no host", "", valid},
		{"no actor", "example.com", Credential{ID: "x", TargetScope: "s", Source: StaticToken("x")}},
		{"blank actor", "example.com", Credential{ID: "x", Actor: "  ", TargetScope: "s", Source: StaticToken("x")}},
		{"no scope", "example.com", Credential{ID: "x", Actor: "a", Source: StaticToken("x")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(map[string][]Credential{tc.host: {tc.credential}}); err == nil {
				t.Fatal("accepted invalid credential")
			}
		})
	}
	var pool Pool
	if err := pool.Add("https://EXAMPLE.com.:8443", valid); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"", "wrong"} {
		if _, err := pool.Acquire(t.Context(), "example.com:8443", scope, 1); err == nil {
			t.Fatal("scope bypass")
		}
	}
	for _, host := range []string{"absent", "example.com", "example.com:443"} {
		if _, err := pool.Acquire(t.Context(), host, "scope", 1); err == nil {
			t.Fatal("host bypass")
		}
	}
	if _, err := pool.Acquire(t.Context(), "example.com:8443", "scope", -1); err == nil {
		t.Fatal("negative cost")
	}
	got, err := pool.Acquire(t.Context(), "example.com:8443", "scope", 3)
	if err != nil || got.Actor != "actor" {
		t.Fatalf("selection: %+v %v", got, err)
	}
	token, err := got.Source.Token(t.Context())
	if err != nil || token != "secret" {
		t.Fatal("token source")
	}
	if err := pool.Add("example.com:8443", valid); err == nil {
		t.Fatal("duplicate ID")
	}
	valid.ID = "two"
	if err := pool.Add("example.com:8443", valid); err == nil {
		t.Fatal("duplicate actor")
	} else if err.Error() == "" {
		t.Fatal("missing diagnostic")
	}
	valid.TargetScope = "other"
	if err := pool.Add("example.com:8443", valid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Acquire(t.Context(), "example.com:8443", "other", 1); err != nil {
		t.Fatal(err)
	}
}
