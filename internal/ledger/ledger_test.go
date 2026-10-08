package ledger

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/clock"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

const (
	testMigration = "migration-1"
	testRun       = "run-1"
)

func openTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
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
	return store, ctx
}

func beginTestRun(ctx context.Context, t *testing.T, store *Store) {
	t.Helper()
	if err := store.BeginRun(ctx, Run{ID: testRun, MigrationID: testMigration, Command: "apply", Units: []Unit{{Key: "unit-1", Kind: "pipeline"}}}); err != nil {
		t.Fatal(err)
	}
}

func collectResources(seq func(func(Resource, error) bool)) ([]Resource, error) {
	var values []Resource
	for value, err := range seq {
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func TestMigrationRunUnitAndResourceLifecycle(t *testing.T) {
	store, ctx := openTestStore(t)
	unitUpdatedAt := time.Date(2026, time.October, 9, 12, 34, 56, 789, time.FixedZone("test", 2*60*60))
	if err := store.BeginRun(ctx, Run{
		ID:          testRun,
		MigrationID: testMigration,
		Command:     "apply",
		Units:       []Unit{{Key: "unit-1", Kind: "pipeline", UpdatedAt: unitUpdatedAt}},
	}); err != nil {
		t.Fatal(err)
	}
	var storedUnitUpdatedAt int64
	if err := store.db.QueryRowContext(ctx, `SELECT updated_at FROM units WHERE migration_id=? AND unit_key=?`, testMigration, "unit-1").Scan(&storedUnitUpdatedAt); err != nil {
		t.Fatal(err)
	}
	if storedUnitUpdatedAt != unitUpdatedAt.UnixNano() {
		t.Fatalf("unit updated_at = %d, want %d", storedUnitUpdatedAt, unitUpdatedAt.UnixNano())
	}
	if err := store.TransitionUnit(ctx, testRun, "unit-1", UnitRunning); err != nil {
		t.Fatal(err)
	}
	previous := json.RawMessage(`{"name":"before"}`)
	resource := Resource{UnitKey: "unit-1", Key: "repo-1", PreviousValue: previous, TargetID: "target-1", ETag: "etag-1"}
	if err := store.UpsertResource(ctx, testRun, resource); err != nil {
		t.Fatal(err)
	}
	for _, state := range []State{StateApplying, StateApplied, StateRolledBack} {
		if err := store.Transition(ctx, testRun, resource.Key, state, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.TransitionUnit(ctx, testRun, "unit-1", UnitDone); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, testRun, RunSucceeded); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, testRun, RunSucceeded); err == nil {
		t.Fatal("finished run accepted twice")
	}
	gotPrevious, err := store.PreviousValue(ctx, testRun, resource.Key)
	if err != nil || string(gotPrevious) != string(previous) {
		t.Fatalf("PreviousValue() = %s, %v", gotPrevious, err)
	}
	rolledBack, err := collectResources(store.ListByState(ctx, testRun, StateRolledBack))
	if err != nil || len(rolledBack) != 1 || rolledBack[0].MigrationID != testMigration {
		t.Fatalf("ListByState() = %#v, %v", rolledBack, err)
	}

	const resumeRun = "run-2"
	if err := store.BeginRun(ctx, Run{ID: resumeRun, MigrationID: testMigration, Command: "resume"}); err != nil {
		t.Fatal(err)
	}
	resumed, err := collectResources(store.ListByState(ctx, resumeRun, StateRolledBack))
	if err != nil || len(resumed) != 1 {
		t.Fatalf("migration state not shared by attempts: %#v, %v", resumed, err)
	}
	if err := store.PutIDMapping(ctx, resumeRun, IDMapping{Kind: "repository", OldID: "old", NewID: "new"}); err != nil {
		t.Fatal(err)
	}
	mapping, err := store.LookupIDMapping(ctx, testRun, "repository", "old")
	if err != nil || mapping.NewID != "new" || mapping.MigrationID != testMigration {
		t.Fatalf("LookupIDMapping() = %#v, %v", mapping, err)
	}
	var mappings []IDMapping
	for value, err := range store.ListIDMappings(ctx, resumeRun) {
		if err != nil {
			t.Fatal(err)
		}
		mappings = append(mappings, value)
	}
	if len(mappings) != 1 || mappings[0].OldID != "old" {
		t.Fatalf("ListIDMappings() = %#v", mappings)
	}
}

func TestIntentOutcomeAndReconciliation(t *testing.T) {
	store, ctx := openTestStore(t)
	beginTestRun(ctx, t, store)
	if err := store.UpsertResource(ctx, testRun, Resource{Key: "resource"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, testRun, "resource", StateApplying, nil); err != nil {
		t.Fatal(err)
	}
	id, err := store.RecordIntent(ctx, Intent{RunID: testRun, ResourceKey: "resource", Operation: "create", Fingerprint: "sha256:abc"})
	if err != nil || id <= 0 {
		t.Fatalf("RecordIntent() = %d, %v", id, err)
	}
	pending := 0
	for intent, err := range store.ListPendingIntents(ctx, testRun) {
		if err != nil {
			t.Fatal(err)
		}
		pending++
		if intent.ID != id || intent.MigrationID != testMigration || intent.CreatedAt.IsZero() {
			t.Fatalf("pending intent = %#v", intent)
		}
	}
	if pending != 1 {
		t.Fatalf("pending intents = %d", pending)
	}
	err = store.RequireNoPendingIntents(ctx, testRun)
	var finding *diag.Error
	if !errors.As(err, &finding) || finding.Finding.Code != "TRL-APPLY-0008" {
		t.Fatalf("RequireNoPendingIntents() = %v", err)
	}
	if err := store.Transition(ctx, testRun, "resource", StateFailed, &Failure{Retryable: true, Reason: "unknown mutation result"}); err != nil {
		t.Fatal(err)
	}
	err = store.Transition(ctx, testRun, "resource", StateApplying, nil)
	if !errors.As(err, &finding) || finding.Finding.Code != "TRL-APPLY-0008" {
		t.Fatalf("retry with pending intent = %v", err)
	}
	_, err = store.RecordIntent(ctx, Intent{RunID: testRun, ResourceKey: "resource", Operation: "create", Fingerprint: "sha256:def"})
	if !errors.As(err, &finding) || finding.Finding.Code != "TRL-APPLY-0008" {
		t.Fatalf("second pending intent = %v", err)
	}
	if err := store.RecordOutcome(ctx, id, Outcome{Status: OutcomeSucceeded, TargetID: "target"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordOutcome(ctx, id, Outcome{Status: OutcomeSucceeded}); err == nil || !strings.Contains(err.Error(), "outcome already recorded") {
		t.Fatalf("duplicate outcome = %v", err)
	}
	if err := store.RequireNoPendingIntents(ctx, testRun); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, testRun, "resource", StateApplying, nil); err != nil {
		t.Fatal(err)
	}
}

func TestStreamingKeysetPaginationAndBatchTransitions(t *testing.T) {
	store, ctx := openTestStore(t)
	beginTestRun(ctx, t, store)
	resources := make([]Resource, pageSize+89)
	transitions := make([]ResourceTransition, len(resources))
	for i := range resources {
		key := fmt.Sprintf("resource-%04d", i)
		resources[i] = Resource{Key: key}
		transitions[i] = ResourceTransition{ResourceKey: key, To: StateApplying}
	}
	if err := store.UpsertResources(ctx, testRun, resources); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionResources(ctx, testRun, transitions); err != nil {
		t.Fatal(err)
	}
	count, last := 0, ""
	for resource, err := range store.ListByState(ctx, testRun, StateApplying) {
		if err != nil {
			t.Fatal(err)
		}
		if resource.Key <= last {
			t.Fatalf("non-keyset order %q after %q", resource.Key, last)
		}
		last = resource.Key
		count++
	}
	if count != len(resources) {
		t.Fatalf("stream count = %d, want %d", count, len(resources))
	}
	count = 0
	for range store.ListByState(ctx, testRun, StateApplying) {
		count++
		break
	}
	if count != 1 {
		t.Fatal("stream did not support early stop")
	}
}

func TestValidationDiagnosticsAndRetry(t *testing.T) {
	redactor := redact.New()
	redactor.Add("ledger-secret-value")
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir(), WithRedactor(redactor))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	beginTestRun(ctx, t, store)
	cases := []Resource{
		{},
		{Key: "bad-state", State: StateApplied},
		{Key: "bad-json", PreviousValue: []byte("{")},
		{Key: "flagged", PreviousValue: json.RawMessage(`{"safe":true}`), PreviousValueSecret: true},
		{Key: "registered", PreviousValue: json.RawMessage(`{"token":"ledger-secret-value"}`)},
	}
	for _, resource := range cases {
		if err := store.UpsertResource(ctx, testRun, resource); err == nil {
			t.Fatalf("invalid resource accepted: %#v", resource)
		}
	}
	if err := store.UpsertResource(ctx, testRun, Resource{Key: "retry"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, testRun, "retry", StateApplying, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, testRun, "retry", StateFailed, &Failure{Retryable: true, Reason: "temporary"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, testRun, "retry", StateApplying, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, testRun, "retry", StateFailed, nil); err == nil {
		t.Fatal("failure without reason accepted")
	}
	if err := store.TransitionUnit(ctx, testRun, "unit-1", UnitDone); err == nil {
		t.Fatal("illegal unit transition accepted")
	}
}

func TestProcessLockAndStaleRecovery(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, dir); err == nil {
		t.Fatal("second process lock acquired")
	} else {
		var finding *diag.Error
		if !errors.As(err, &finding) || finding.Finding.Code != "TRL-APPLY-0005" {
			t.Fatalf("second process lock error = %v", err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, lockName), []byte("2147483647\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessLockReleasedWhenHolderExits(t *testing.T) {
	if os.Getenv("TRASLOCO_LEDGER_LOCK_HELPER") == "1" {
		store, err := Open(context.Background(), os.Getenv("TRASLOCO_LEDGER_LOCK_DIR"))
		if err != nil {
			_, _ = fmt.Fprintln(os.Stdout, err)
			os.Exit(2)
		}
		_, _ = fmt.Fprintln(os.Stdout, "ready")
		_, _ = bufio.NewReader(os.Stdin).ReadByte()
		_ = store // Deliberately exit without Close; the kernel must release the lock.
		os.Exit(0)
	}

	dir := t.TempDir()
	commandCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, os.Args[0], "-test.run=^TestProcessLockReleasedWhenHolderExits$") // #nosec G204 -- executes the current test binary.
	command.Env = append(os.Environ(), "TRASLOCO_LEDGER_LOCK_HELPER=1", "TRASLOCO_LEDGER_LOCK_DIR="+dir)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("helper readiness = %q, %v", line, err)
	}
	if _, err := Open(context.Background(), dir); err == nil {
		t.Fatal("separate process lock acquired")
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestContextCancellationAfterEnqueueCommits(t *testing.T) {
	store, ctx := openTestStore(t)
	beginTestRun(ctx, t, store)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	store.beforeCommit = func(*sql.Tx) {
		once.Do(func() { close(entered) })
		<-release
	}
	writeCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- store.UpsertResource(writeCtx, testRun, Resource{Key: "durable"}) }()
	<-entered
	cancel()
	select {
	case err := <-done:
		t.Fatalf("enqueued write returned before commit: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	store.beforeCommit = nil
	values, err := collectResources(store.ListByState(ctx, testRun, StatePlanned))
	if err != nil || len(values) != 1 || values[0].Key != "durable" {
		t.Fatalf("committed resource = %#v, %v", values, err)
	}
}

func TestSchemaIdentityMigrationNamesAndIntegerTimestamps(t *testing.T) {
	if got, err := migrationVersions([]string{"001_init.sql", "002_more.sql"}); err != nil || len(got) != 2 {
		t.Fatalf("migrationVersions() = %v, %v", got, err)
	}
	for _, names := range [][]string{{"bad.sql"}, {"001_init.sql", "003_gap.sql"}} {
		if _, err := migrationVersions(names); err == nil {
			t.Fatalf("invalid migrations accepted: %v", names)
		}
	}
	store, ctx := openTestStore(t)
	beginTestRun(ctx, t, store)
	var startedType, appliedType string
	if err := store.db.QueryRowContext(ctx, `SELECT typeof(started_at) FROM runs WHERE id=?`, testRun).Scan(&startedType); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT typeof(applied_at) FROM schema_migrations WHERE version=1`).Scan(&appliedType); err != nil {
		t.Fatal(err)
	}
	if startedType != "integer" || appliedType != "integer" {
		t.Fatalf("timestamp types = %s, %s", startedType, appliedType)
	}
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, databaseName)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL); INSERT INTO schema_migrations VALUES(1,'old')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Open(ctx, dir)
	var finding *diag.Error
	if !errors.As(err, &finding) || finding.Finding.Code != "TRL-APPLY-0010" {
		t.Fatalf("old schema error = %v", err)
	}
}

func TestErrorsOptionsAndClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open canceled error = %v", err)
	}
	if _, err := Open(context.Background(), "  "); err == nil {
		t.Fatal("blank directory accepted")
	}
	for _, option := range []Option{nil, WithClock(nil), WithRedactor(nil)} {
		if _, err := Open(context.Background(), t.TempDir(), option); err == nil {
			t.Fatalf("invalid option accepted: %#v", option)
		}
	}
	for _, options := range [][]Option{{WithClock(clock.Real{})}} {
		opened, err := Open(context.Background(), t.TempDir(), options...)
		if err != nil {
			t.Fatal(err)
		}
		if err := opened.Close(); err != nil {
			t.Fatal(err)
		}
	}
	store, runCtx := openTestStore(t)
	if err := store.BeginRun(runCtx, Run{}); err == nil {
		t.Fatal("empty run accepted")
	}
	if err := store.BeginRun(runCtx, Run{ID: "bad", MigrationID: "m", Command: "c", Units: []Unit{{}}}); err == nil {
		t.Fatal("empty unit accepted")
	}
	beginTestRun(runCtx, t, store)
	if err := store.RecordOutcome(runCtx, 0, Outcome{Status: OutcomeSucceeded}); err == nil {
		t.Fatal("invalid intent ID accepted")
	}
	if _, err := store.RecordIntent(runCtx, Intent{}); err == nil {
		t.Fatal("empty intent accepted")
	}
	if err := store.RecordOutcome(runCtx, 1, Outcome{Status: "unknown"}); err == nil {
		t.Fatal("invalid outcome accepted")
	}
	if err := store.FinishRun(runCtx, testRun, RunRunning); err == nil {
		t.Fatal("running accepted as terminal status")
	}
	if err := store.FinishRun(runCtx, testRun, "unknown"); err == nil {
		t.Fatal("unknown terminal status accepted")
	}
	if err := store.PutIDMapping(runCtx, testRun, IDMapping{}); err == nil {
		t.Fatal("empty mapping accepted")
	}
	if _, err := store.LookupIDMapping(runCtx, testRun, "missing", "missing"); err == nil {
		t.Fatal("missing mapping accepted")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRun(runCtx, Run{ID: "closed", MigrationID: "m", Command: "c"}); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("closed write error = %v", err)
	}
}

func TestStateMachineTables(t *testing.T) {
	unitCases := []struct {
		from UnitState
		to   UnitState
		want bool
	}{
		{UnitPlanned, UnitRunning, true},
		{UnitPlanned, UnitSkipped, true},
		{UnitRunning, UnitDone, true},
		{UnitRunning, UnitFailed, true},
		{UnitFailed, UnitRunning, true},
		{UnitDone, UnitRunning, false},
	}
	for _, tc := range unitCases {
		if got := legalUnitTransition(tc.from, tc.to); got != tc.want {
			t.Errorf("legalUnitTransition(%s, %s) = %t", tc.from, tc.to, got)
		}
	}
	if retryable, reason, err := transitionDetails(StateSkipped, &Failure{Reason: "manual"}); err != nil || retryable || reason != "manual" {
		t.Fatalf("skipped details = %t, %q, %v", retryable, reason, err)
	}
	if _, _, err := transitionDetails(StateSkipped, &Failure{}); err == nil {
		t.Fatal("skipped transition without reason accepted")
	}
	if _, _, err := transitionDetails(StateApplied, &Failure{Reason: "extra"}); err == nil {
		t.Fatal("applied transition accepted failure details")
	}
	err := diagnosticError("TRL-APPLY-0004", errors.New("cause"), map[string]any{"kind": "test"})
	var finding *diag.Error
	if !errors.As(err, &finding) {
		t.Fatal("diagnostic error missing finding")
	}
}

func TestAtomicWriterBatchRollback(t *testing.T) {
	store, ctx := openTestStore(t)
	beginTestRun(ctx, t, store)
	store.beforeCommit = func(tx *sql.Tx) { _ = tx.Rollback() }
	errorsCh := make(chan error, 2)
	ready := make(chan struct{})
	for _, key := range []string{"a", "b"} {
		go func() {
			<-ready
			errorsCh <- store.UpsertResource(ctx, testRun, Resource{Key: key})
		}()
	}
	close(ready)
	for range 2 {
		if err := <-errorsCh; err == nil {
			t.Fatal("rolled-back batch reported success")
		}
	}
	store.beforeCommit = nil
	values, err := collectResources(store.ListByState(ctx, testRun, StatePlanned))
	if err != nil || len(values) != 0 {
		t.Fatalf("partial batch survived: %#v, %v", values, err)
	}
}

func TestPaginatedIntentAndMappingStreams(t *testing.T) {
	store, ctx := openTestStore(t)
	beginTestRun(ctx, t, store)
	if err := store.UpsertResource(ctx, testRun, Resource{Key: "resource"}); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < pageSize+1; index++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO intents(migration_id,resource_key,run_id,operation,fingerprint,created_at) VALUES(?,?,?,?,?,?)`, testMigration, "resource", testRun, "update", fmt.Sprintf("f-%04d", index), int64(index+1)); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO id_map(migration_id,kind,old_id,new_id,recorded_at) VALUES(?,?,?,?,?)`, testMigration, "kind", fmt.Sprintf("old-%04d", index), fmt.Sprintf("new-%04d", index), int64(index+1)); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	intents := 0
	for _, err := range store.ListPendingIntents(ctx, testRun) {
		if err != nil {
			t.Fatal(err)
		}
		intents++
	}
	mappings := 0
	for _, err := range store.ListIDMappings(ctx, testRun) {
		if err != nil {
			t.Fatal(err)
		}
		mappings++
	}
	if intents != pageSize+1 || mappings != pageSize+1 {
		t.Fatalf("paginated counts = %d, %d", intents, mappings)
	}
}

func TestReadAndInputErrors(t *testing.T) {
	store, ctx := openTestStore(t)
	beginTestRun(ctx, t, store)
	if err := store.BeginRun(ctx, Run{ID: "bad-status", MigrationID: "m", Command: "c", Status: RunFailed}); err == nil {
		t.Fatal("terminal initial run accepted")
	}
	if err := store.UpsertResources(ctx, testRun, nil); err == nil {
		t.Fatal("empty resource batch accepted")
	}
	if err := store.TransitionResources(ctx, testRun, nil); err == nil {
		t.Fatal("empty transition batch accepted")
	}
	if err := store.TransitionResources(ctx, testRun, []ResourceTransition{{To: StateApplying}}); err == nil {
		t.Fatal("empty resource transition accepted")
	}
	if err := store.Transition(ctx, testRun, "missing", StateApplying, nil); err == nil {
		t.Fatal("missing resource transition accepted")
	}
	if err := store.TransitionUnit(ctx, testRun, "missing", UnitRunning); err == nil {
		t.Fatal("missing unit transition accepted")
	}
	if _, err := store.PreviousValue(ctx, testRun, "missing"); err == nil {
		t.Fatal("missing previous value accepted")
	}
	if _, err := store.RecordIntent(ctx, Intent{RunID: testRun, ResourceKey: "missing", Operation: "create", Fingerprint: "f"}); err == nil {
		t.Fatal("intent for missing resource accepted")
	}
	if _, err := collectResources(store.ListByState(ctx, "missing", StatePlanned)); err == nil {
		t.Fatal("missing-run stream succeeded")
	}
	if err := store.RequireNoPendingIntents(ctx, "missing"); err == nil {
		t.Fatal("pending check for missing run succeeded")
	}
	for _, err := range store.ListIDMappings(ctx, "missing") {
		if err == nil {
			t.Fatal("mapping stream for missing run succeeded")
		}
		break
	}
	if err := (*processLock)(nil).release(); err != nil {
		t.Fatal(err)
	}
}

func TestAppliedMigrationVersionFailures(t *testing.T) {
	for _, version := range []int{2, 3} {
		dir := t.TempDir()
		store, err := Open(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, databaseName)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(context.Background(), `INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`, version, int64(1)); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(context.Background(), dir); err == nil {
			t.Fatalf("applied migration version %d accepted", version)
		}
	}
}
