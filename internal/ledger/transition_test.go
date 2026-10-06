package ledger

import (
	"context"
	"testing"
)

func TestTransitionDetailsAndErrors(t *testing.T) {
	if _, _, err := transitionDetails(StateFailed, nil); err == nil {
		t.Fatal("failed without reason accepted")
	}
	if _, _, err := transitionDetails(StateSkipped, &Failure{}); err == nil {
		t.Fatal("skipped without reason accepted")
	}
	if _, _, err := transitionDetails(StateApplied, &Failure{Reason: "x"}); err == nil {
		t.Fatal("failure details on applied accepted")
	}
	if r, reason, err := transitionDetails(StateSkipped, &Failure{Reason: "manual"}); err != nil || r || reason != "manual" {
		t.Fatal("skipped details wrong")
	}
	if r, reason, err := transitionDetails(StateFailed, &Failure{Reason: "x", Retryable: true}); err != nil || !r || reason != "x" {
		t.Fatal("failed details wrong")
	}
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Transition(ctx, "", "", StateApplying, nil); err == nil {
		t.Fatal("empty ids accepted")
	}
	if err := store.Transition(ctx, "nope", "nope", StateApplying, nil); err == nil {
		t.Fatal("missing resource accepted")
	}
}

func TestValidationAndLifecycleErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := Open(ctx, "  "); err == nil {
		t.Fatal("blank dir accepted")
	}
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRun(ctx, Run{}); err == nil {
		t.Fatal("empty run accepted")
	}
	if err := store.BeginRun(ctx, Run{ID: "r", Command: "c", Units: []Unit{{}}}); err == nil {
		t.Fatal("empty unit accepted")
	}
	if err := store.BeginRun(ctx, Run{ID: "r2", Command: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRun(ctx, Run{ID: "r2", Command: "c"}); err == nil {
		t.Fatal("duplicate run accepted")
	}
	for _, res := range []Resource{
		{},
		{RunID: "r2", Key: "k", State: StateApplied},
		{RunID: "r2", Key: "k", PreviousValue: []byte("{")},
	} {
		if err := store.UpsertResource(ctx, res); err == nil {
			t.Fatalf("invalid resource accepted: %#v", res)
		}
	}
	if err := store.UpsertResource(ctx, Resource{RunID: "r2", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertResource(ctx, Resource{RunID: "r2", Key: "k", TargetID: "t", ETag: "e"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, "r2", "k", StateApplying, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, "r2", "k", StateFailed, &Failure{Reason: "x", Retryable: true}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ListByState(ctx, "r2", StateFailed); err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := store.PreviousValue(ctx, "r2", "missing"); err == nil {
		t.Fatal("missing resource accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.BeginRun(cancelled, Run{ID: "r3", Command: "c"}); err == nil {
		t.Fatal("cancelled context accepted")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRun(ctx, Run{ID: "r4", Command: "c"}); err == nil {
		t.Fatal("closed store accepted write")
	}
}
