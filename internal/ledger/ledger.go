package ledger

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	// Register the pure-Go SQLite database/sql driver.
	_ "modernc.org/sqlite"
)

const (
	databaseName = "ledger.db"
	batchSize    = 64
	batchDelay   = 5 * time.Millisecond
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type writeRequest struct {
	ctx   context.Context
	apply func(*sql.Tx) error
	done  chan error
	close bool
}

// Store is a crash-safe SQLite ledger with one batched writer and concurrent readers.
type Store struct {
	db         *sql.DB
	writes     chan writeRequest
	gate       sync.RWMutex
	closed     bool
	writerDone chan struct{}

	// beforeCommit exists for deterministic crash-safety tests.
	beforeCommit func(*sql.Tx)
}

// Open creates or opens <outputDir>/ledger.db, applies embedded migrations,
// enables WAL and starts the writer.
func Open(ctx context.Context, outputDir string) (*Store, error) {
	if strings.TrimSpace(outputDir) == "" {
		return nil, fmt.Errorf("output directory is required")
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return nil, fmt.Errorf("create output directory: %w", err)
	}
	path := filepath.Join(outputDir, databaseName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- path is the requested output directory plus a fixed filename.
	if err != nil {
		return nil, fmt.Errorf("create ledger: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close new ledger: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrPermission) {
		return nil, fmt.Errorf("set ledger permissions: %w", err)
	}

	pragma := url.QueryEscape("busy_timeout(5000)")
	foreignKeys := url.QueryEscape("foreign_keys(1)")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=" + pragma + "&_pragma=" + foreignKeys
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if err := applyMigrations(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &Store{db: db, writes: make(chan writeRequest, batchSize), writerDone: make(chan struct{})}
	go store.writer(context.WithoutCancel(ctx))
	return store, nil
}

// BeginRun durably creates a run and its units.
func (s *Store) BeginRun(ctx context.Context, run Run) error {
	if run.ID == "" || run.Command == "" {
		return fmt.Errorf("run ID and command are required")
	}
	if run.Status == "" {
		run.Status = "running"
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	return s.submit(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO runs(id, command, status, started_at) VALUES(?,?,?,?)`, run.ID, run.Command, run.Status, timestamp(run.StartedAt)); err != nil {
			return fmt.Errorf("insert run: %w", err)
		}
		for _, unit := range run.Units {
			if unit.Key == "" || unit.Kind == "" {
				return fmt.Errorf("unit key and kind are required")
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO units(run_id, unit_key, kind) VALUES(?,?,?)`, run.ID, unit.Key, unit.Kind); err != nil {
				return fmt.Errorf("insert unit %s: %w", unit.Key, err)
			}
		}
		return nil
	})
}

// UpsertResource inserts a planned resource or updates its non-state metadata.
func (s *Store) UpsertResource(ctx context.Context, resource Resource) error {
	if resource.RunID == "" || resource.Key == "" {
		return fmt.Errorf("resource run ID and key are required")
	}
	if resource.State == "" {
		resource.State = StatePlanned
	}
	if resource.State != StatePlanned {
		return fmt.Errorf("new resource state must be planned")
	}
	if len(resource.PreviousValue) > 0 && !jsonValid(resource.PreviousValue) {
		return fmt.Errorf("previous value must be valid JSON")
	}
	if resource.UpdatedAt.IsZero() {
		resource.UpdatedAt = time.Now().UTC()
	}
	return s.submit(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
INSERT INTO resources(run_id, unit_key, resource_key, state, previous_value, target_id, etag, updated_at)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(run_id, resource_key) DO UPDATE SET
  unit_key=COALESCE(excluded.unit_key, resources.unit_key),
  previous_value=COALESCE(resources.previous_value, excluded.previous_value),
  target_id=COALESCE(NULLIF(excluded.target_id,''), resources.target_id),
  etag=COALESCE(NULLIF(excluded.etag,''), resources.etag),
  updated_at=excluded.updated_at`,
			resource.RunID, nullString(resource.UnitKey), resource.Key, StatePlanned,
			nullBytes(resource.PreviousValue), nullString(resource.TargetID), nullString(resource.ETag), timestamp(resource.UpdatedAt))
		if err != nil {
			return fmt.Errorf("upsert resource: %w", err)
		}
		return nil
	})
}

// Transition performs one legal state transition and appends an operation log row.
func (s *Store) Transition(ctx context.Context, runID, resourceKey string, to State, failure *Failure) error {
	if runID == "" || resourceKey == "" {
		return fmt.Errorf("run ID and resource key are required")
	}
	return s.submit(ctx, func(tx *sql.Tx) error {
		var from State
		var wasRetryable bool
		if err := tx.QueryRowContext(ctx, `SELECT state, retryable FROM resources WHERE run_id=? AND resource_key=?`, runID, resourceKey).Scan(&from, &wasRetryable); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("resource %s: %w", resourceKey, sql.ErrNoRows)
			}
			return fmt.Errorf("read resource state: %w", err)
		}
		if !legalTransition(from, to, wasRetryable) {
			return fmt.Errorf("illegal resource transition %s -> %s", from, to)
		}
		retryable, reason, err := transitionDetails(to, failure)
		if err != nil {
			return err
		}
		now := timestamp(time.Now().UTC())
		if _, err := tx.ExecContext(ctx, `UPDATE resources SET state=?, retryable=?, failure_reason=?, updated_at=? WHERE run_id=? AND resource_key=?`, to, retryable, nullString(reason), now, runID, resourceKey); err != nil {
			return fmt.Errorf("update resource state: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO operations(run_id, resource_key, previous_state, new_state, retryable, reason, occurred_at) VALUES(?,?,?,?,?,?,?)`, runID, resourceKey, from, to, retryable, nullString(reason), now); err != nil {
			return fmt.Errorf("append operation log: %w", err)
		}
		return nil
	})
}

// ListByState returns resources in stable resource-key order.
func (s *Store) ListByState(ctx context.Context, runID string, state State) ([]Resource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id, COALESCE(unit_key,''), resource_key, state, retryable, COALESCE(failure_reason,''), previous_value, COALESCE(target_id,''), COALESCE(etag,''), updated_at FROM resources WHERE run_id=? AND state=? ORDER BY resource_key`, runID, state)
	if err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}
	var resources []Resource
	for rows.Next() {
		var resource Resource
		var previous []byte
		var updated string
		if err := rows.Scan(&resource.RunID, &resource.UnitKey, &resource.Key, &resource.State, &resource.Retryable, &resource.FailureReason, &previous, &resource.TargetID, &resource.ETag, &updated); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan resource: %w", err)
		}
		resource.PreviousValue = append(resource.PreviousValue, previous...)
		resource.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		resources = append(resources, resource)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("list resources: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close resource rows: %w", err)
	}
	return resources, nil
}

// PreviousValue returns a copy of the non-secret JSON stored for rollback.
func (s *Store) PreviousValue(ctx context.Context, runID, resourceKey string) ([]byte, error) {
	var value []byte
	if err := s.db.QueryRowContext(ctx, `SELECT previous_value FROM resources WHERE run_id=? AND resource_key=?`, runID, resourceKey).Scan(&value); err != nil {
		return nil, fmt.Errorf("read previous value: %w", err)
	}
	return append([]byte(nil), value...), nil
}

// MarkRolledBack transitions an applied resource to rolledback.
func (s *Store) MarkRolledBack(ctx context.Context, runID, resourceKey string) error {
	return s.Transition(ctx, runID, resourceKey, StateRolledBack, nil)
}

// Close flushes all queued writes and closes the database.
func (s *Store) Close() error {
	s.gate.Lock()
	if s.closed {
		s.gate.Unlock()
		<-s.writerDone
		return nil
	}
	s.closed = true
	done := make(chan error, 1)
	s.writes <- writeRequest{ctx: context.Background(), done: done, close: true}
	s.gate.Unlock()
	writerErr := <-done
	<-s.writerDone
	dbErr := s.db.Close()
	return errors.Join(writerErr, dbErr)
}

func (s *Store) submit(ctx context.Context, apply func(*sql.Tx) error) error {
	s.gate.RLock()
	if s.closed {
		s.gate.RUnlock()
		return fmt.Errorf("ledger closed: %w", sql.ErrConnDone)
	}
	done := make(chan error, 1)
	request := writeRequest{ctx: ctx, apply: apply, done: done}
	select {
	case s.writes <- request:
		s.gate.RUnlock()
	case <-ctx.Done():
		s.gate.RUnlock()
		return ctx.Err()
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) writer(ctx context.Context) {
	defer close(s.writerDone)
	for {
		first := <-s.writes
		if first.close {
			first.done <- nil
			return
		}
		batch := []writeRequest{first}
		var closeRequest *writeRequest
		timer := time.NewTimer(batchDelay)
	collect:
		for len(batch) < batchSize {
			select {
			case request := <-s.writes:
				if request.close {
					closeRequest = &request
					break collect
				}
				batch = append(batch, request)
			case <-timer.C:
				break collect
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		errorsByRequest := s.executeBatch(ctx, batch)
		for index, request := range batch {
			request.done <- errorsByRequest[index]
		}
		if closeRequest != nil {
			closeRequest.done <- nil
			return
		}
	}
}

func (s *Store) executeBatch(ctx context.Context, batch []writeRequest) []error {
	result := make([]error, len(batch))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		for i := range result {
			result[i] = fmt.Errorf("begin ledger batch: %w", err)
		}
		return result
	}
	for index, request := range batch {
		savepoint := fmt.Sprintf("request_%d", index)
		if _, err := tx.ExecContext(ctx, "SAVEPOINT "+savepoint); err != nil {
			result[index] = err
			continue
		}
		result[index] = request.apply(tx)
		if result[index] != nil {
			_, _ = tx.ExecContext(ctx, "ROLLBACK TO "+savepoint)
		}
		if _, err := tx.ExecContext(ctx, "RELEASE "+savepoint); err != nil && result[index] == nil {
			result[index] = err
		}
	}
	if s.beforeCommit != nil {
		s.beforeCommit(tx)
	}
	if err := tx.Commit(); err != nil {
		for i := range result {
			if result[i] == nil {
				result[i] = fmt.Errorf("commit ledger batch: %w", err)
			}
		}
	}
	return result
}

func legalTransition(from, to State, retryable bool) bool {
	switch from {
	case StatePlanned:
		return to == StateApplying || to == StateSkipped
	case StateApplying:
		return to == StateApplied || to == StateFailed
	case StateFailed:
		return retryable && to == StateApplying
	case StateApplied:
		return to == StateRolledBack
	default:
		return false
	}
}

func transitionDetails(to State, failure *Failure) (bool, string, error) {
	switch to {
	case StateFailed:
		if failure == nil || strings.TrimSpace(failure.Reason) == "" {
			return false, "", fmt.Errorf("failed transition requires a reason")
		}
		return failure.Retryable, failure.Reason, nil
	case StateSkipped:
		if failure == nil || strings.TrimSpace(failure.Reason) == "" {
			return false, "", fmt.Errorf("skipped transition requires a manual reason")
		}
		return false, failure.Reason, nil
	default:
		if failure != nil {
			return false, "", fmt.Errorf("transition to %s does not accept failure details", to)
		}
		return false, "", nil
	}
}

func applyMigrations(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var current int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current > len(entries) {
		return fmt.Errorf("ledger schema version %d is newer than supported %d", current, len(entries))
	}
	for index, entry := range entries {
		version := index + 1
		if version <= current {
			continue
		}
		script, err := fs.ReadFile(migrationFS, "migrations/"+entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %d: %w", version, err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, string(script)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?,?)`, version, timestamp(time.Now().UTC())); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}
	return nil
}

func timestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
