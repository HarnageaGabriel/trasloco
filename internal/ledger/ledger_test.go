package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestResourceLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})

	run := Run{ID: "run-1", Command: "apply", Units: []Unit{{Key: "unit-1", Kind: "pipeline"}}}
	if err := store.BeginRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	previous := json.RawMessage(`{"name":"before"}`)
	resource := Resource{RunID: run.ID, UnitKey: "unit-1", Key: "repo-1", PreviousValue: previous, TargetID: "target-1", ETag: "etag-1"}
	if err := store.UpsertResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, run.ID, resource.Key, StateApplying, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, run.ID, resource.Key, StateApplied, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, run.ID, resource.Key, StateFailed, &Failure{Reason: "late"}); err == nil {
		t.Fatal("illegal transition accepted")
	}
	gotPrevious, err := store.PreviousValue(ctx, run.ID, resource.Key)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotPrevious) != string(previous) {
		t.Fatalf("PreviousValue() = %s, want %s", gotPrevious, previous)
	}
	if err := store.MarkRolledBack(ctx, run.ID, resource.Key); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := store.ListByState(ctx, run.ID, StateRolledBack)
	if err != nil {
		t.Fatal(err)
	}
	if len(rolledBack) != 1 || rolledBack[0].Key != resource.Key {
		t.Fatalf("ListByState() = %#v", rolledBack)
	}
}

func TestRetryableFailureCanResume(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.BeginRun(ctx, Run{ID: "run", Command: "apply"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertResource(ctx, Resource{RunID: "run", Key: "resource"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, "run", "resource", StateApplying, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, "run", "resource", StateFailed, &Failure{Retryable: true, Reason: "temporary"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, "run", "resource", StateApplying, nil); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentReaders(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.BeginRun(ctx, Run{ID: "run", Command: "plan"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertResource(ctx, Resource{RunID: "run", Key: "resource"}); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := store.ListByState(ctx, "run", StatePlanned); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
}

func TestCrashMidBatchIsAtomic(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRun(ctx, Run{ID: "run", Command: "apply"}); err != nil {
		t.Fatal(err)
	}
	store.beforeCommit = func(tx *sql.Tx) { _ = tx.Rollback() }

	errorsCh := make(chan error, 2)
	for _, key := range []string{"a", "b"} {
		go func() { errorsCh <- store.UpsertResource(ctx, Resource{RunID: "run", Key: key}) }()
	}
	for range 2 {
		if err := <-errorsCh; err == nil {
			t.Fatal("simulated crash unexpectedly committed")
		}
	}
	store.beforeCommit = nil
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	})
	resources, err := reopened.ListByState(ctx, "run", StatePlanned)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 0 {
		t.Fatalf("partial batch survived: %#v", resources)
	}
	header := make([]byte, 16)
	file, err := os.Open(filepath.Join(dir, databaseName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := file.Read(header); err != nil {
		t.Fatal(err)
	}
	if string(header) != "SQLite format 3\x00" {
		t.Fatalf("not SQLite: %q", header)
	}
}
