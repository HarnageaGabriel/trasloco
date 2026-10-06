package credpool

import (
	"context"
	"testing"
)

func TestAcquireHighestBudgetAndRoundRobin(t *testing.T) {
	pool, err := New(map[string][]Credential{
		"https://api.example.com": {
			{ID: "a", Source: StaticToken("one"), Budget: Budget{Remaining: 4}},
			{ID: "b", Source: StaticToken("two"), Budget: Budget{Remaining: 8}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := pool.Acquire(context.Background(), "api.example.com", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "b" {
		t.Fatalf("Acquire() ID = %q, want b", got.ID)
	}
	pool.Release("api.example.com", "a", Budget{Remaining: 7})
	pool.Release("api.example.com", "b", Budget{Remaining: 7})
	first, _ := pool.Acquire(context.Background(), "api.example.com", 0)
	second, _ := pool.Acquire(context.Background(), "api.example.com", 0)
	if first.ID == second.ID {
		t.Fatalf("equal budgets did not rotate: %q then %q", first.ID, second.ID)
	}
}

func TestAcquireCancellation(t *testing.T) {
	pool, _ := New(map[string][]Credential{"example.com": {{ID: "a", Source: StaticToken("x")}}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pool.Acquire(ctx, "example.com", 1); err == nil {
		t.Fatal("Acquire() unexpectedly succeeded")
	}
}
