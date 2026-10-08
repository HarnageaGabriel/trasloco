package ledger

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/clock"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
	// Register the pure-Go SQLite database/sql driver.
	_ "modernc.org/sqlite"
)

const (
	databaseName = "ledger.db"
	batchSize    = 64
	pageSize     = 512
	batchDelay   = 5 * time.Millisecond
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Option configures Open.
type Option func(*openOptions) error

type openOptions struct {
	clock    clock.Clock
	redactor *redact.Redactor
}

// WithClock supplies the ledger's timestamp and batching clock.
func WithClock(c clock.Clock) Option {
	return func(o *openOptions) error {
		if c == nil {
			return errors.New("nil ledger clock")
		}
		o.clock = c
		return nil
	}
}

// WithRedactor supplies the run redactor used to reject secret rollback data.
func WithRedactor(r *redact.Redactor) Option {
	return func(o *openOptions) error {
		if r == nil {
			return errors.New("nil ledger redactor")
		}
		o.redactor = r
		return nil
	}
}

type writeRequest struct {
	ctx   context.Context
	apply func(context.Context, *sql.Tx) error
	done  chan error
	close bool
}

// Store is a crash-safe SQLite ledger with one batched writer and concurrent readers.
type Store struct {
	clock        clock.Clock
	redactor     *redact.Redactor
	db           *sql.DB
	lock         *processLock
	writes       chan writeRequest
	gate         sync.RWMutex
	closed       bool
	writerDone   chan struct{}
	beforeCommit func(*sql.Tx)
}

// Open locks outputDir, opens ledger.db, applies migrations, and starts the writer.
func Open(ctx context.Context, outputDir string, options ...Option) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	opts := openOptions{clock: clock.Real{}, redactor: redact.New()}
	for _, option := range options {
		if option == nil {
			return nil, ledgerError(errors.New("nil ledger option"))
		}
		if err := option(&opts); err != nil {
			return nil, ledgerError(err)
		}
	}
	if strings.TrimSpace(outputDir) == "" {
		return nil, ledgerError(errors.New("output directory is required"))
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil { // #nosec G703 -- outputDir is the caller-selected ledger directory.
		return nil, ledgerError(fmt.Errorf("create output directory: %w", err))
	}
	if err := os.Chmod(outputDir, 0o700); err != nil && !errors.Is(err, os.ErrPermission) { // #nosec G302,G703 -- caller-selected directory must be owner-only.
		return nil, ledgerError(fmt.Errorf("set output directory permissions: %w", err))
	}
	lock, err := acquireLock(outputDir)
	if err != nil {
		var diagnostic *diag.Error
		if errors.As(err, &diagnostic) {
			return nil, err
		}
		return nil, ledgerError(err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = lock.release()
		}
	}()
	path := filepath.Join(outputDir, databaseName)
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		if err := ensurePrivateFile(name); err != nil {
			return nil, ledgerError(err)
		}
	}
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=" + url.QueryEscape("busy_timeout(5000)") + "&_pragma=" + url.QueryEscape("foreign_keys(1)")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, ledgerError(fmt.Errorf("open ledger: %w", err))
	}
	defer func() {
		if !ok {
			_ = db.Close()
		}
	}()
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		return nil, ledgerError(fmt.Errorf("enable WAL: %w", err))
	}
	if err := chmodSidecars(path); err != nil {
		return nil, ledgerError(err)
	}
	if err := applyMigrations(ctx, db, opts.clock); err != nil {
		return nil, err
	}
	store := &Store{clock: opts.clock, redactor: opts.redactor, db: db, lock: lock, writes: make(chan writeRequest, batchSize), writerDone: make(chan struct{})}
	go store.writer(context.WithoutCancel(ctx))
	ok = true
	return store, nil
}

func ensurePrivateFile(path string) error {
	//nolint:gosec // Path is a fixed filename inside the caller-selected ledger directory.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("create ledger file %s: %w", filepath.Base(path), err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close ledger file %s: %w", filepath.Base(path), err)
	}
	// #nosec G703 -- path is a fixed filename inside the caller-selected ledger directory.
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("set ledger file permissions: %w", err)
	}
	return nil
}

func chmodSidecars(path string) error {
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		// #nosec G703 -- names are fixed ledger sidecars in the caller-selected directory.
		if err := os.Chmod(name, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("set %s permissions: %w", filepath.Base(name), err)
		}
	}
	return nil
}

// BeginRun durably creates one run attempt and its planned units.
func (s *Store) BeginRun(ctx context.Context, run Run) error {
	if run.ID == "" || run.MigrationID == "" || run.Command == "" {
		return ledgerError(errors.New("run ID, migration ID, and command are required"))
	}
	if run.Status == "" {
		run.Status = RunRunning
	}
	if run.Status != RunRunning {
		return ledgerError(errors.New("new run status must be running"))
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = s.clock.Now().UTC()
	}
	return s.submit(ctx, func(writeCtx context.Context, tx *sql.Tx) error {
		now := unixNano(run.StartedAt)
		if _, err := tx.ExecContext(writeCtx, `INSERT INTO migrations(id,created_at) VALUES(?,?) ON CONFLICT(id) DO NOTHING`, run.MigrationID, now); err != nil {
			return fmt.Errorf("ensure migration: %w", err)
		}
		if _, err := tx.ExecContext(writeCtx, `INSERT INTO runs(id,migration_id,command,status,started_at) VALUES(?,?,?,?,?)`, run.ID, run.MigrationID, run.Command, run.Status, now); err != nil {
			return fmt.Errorf("insert run: %w", err)
		}
		for _, unit := range run.Units {
			if unit.Key == "" || unit.Kind == "" {
				return errors.New("unit key and kind are required")
			}
			state := unit.State
			if state == "" {
				state = UnitPlanned
			}
			if state != UnitPlanned {
				return errors.New("new unit state must be planned")
			}
			updatedAt := unit.UpdatedAt
			if updatedAt.IsZero() {
				updatedAt = run.StartedAt
			}
			if _, err := tx.ExecContext(writeCtx, `INSERT INTO units(migration_id,unit_key,kind,state,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(migration_id,unit_key) DO UPDATE SET kind=excluded.kind`, run.MigrationID, unit.Key, unit.Kind, state, unixNano(updatedAt)); err != nil {
				return fmt.Errorf("upsert unit %s: %w", unit.Key, err)
			}
		}
		return nil
	})
}

// FinishRun makes a running attempt terminal exactly once.
func (s *Store) FinishRun(ctx context.Context, runID string, status RunStatus) error {
	if runID == "" || status == "" || status == RunRunning {
		return ledgerError(errors.New("run ID and terminal status are required"))
	}
	switch status {
	case RunSucceeded, RunFailed, RunPartial, RunInterrupted:
	default:
		return ledgerError(fmt.Errorf("invalid run status %q", status))
	}
	return s.submit(ctx, func(writeCtx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(writeCtx, `UPDATE runs SET status=?,finished_at=? WHERE id=? AND status=?`, status, unixNano(s.clock.Now()), runID, RunRunning)
		if err != nil {
			return fmt.Errorf("finish run: %w", err)
		}
		return requireChanged(result, "active run", runID)
	})
}

// TransitionUnit performs one legal unit state transition.
func (s *Store) TransitionUnit(ctx context.Context, runID, unitKey string, to UnitState) error {
	if runID == "" || unitKey == "" {
		return ledgerError(errors.New("run ID and unit key are required"))
	}
	return s.submit(ctx, func(writeCtx context.Context, tx *sql.Tx) error {
		migrationID, err := migrationForRun(writeCtx, tx, runID)
		if err != nil {
			return err
		}
		var from UnitState
		if err := tx.QueryRowContext(writeCtx, `SELECT state FROM units WHERE migration_id=? AND unit_key=?`, migrationID, unitKey).Scan(&from); err != nil {
			return fmt.Errorf("read unit state: %w", err)
		}
		if !legalUnitTransition(from, to) {
			return fmt.Errorf("illegal unit transition %s -> %s", from, to)
		}
		_, err = tx.ExecContext(writeCtx, `UPDATE units SET state=?,updated_at=? WHERE migration_id=? AND unit_key=?`, to, unixNano(s.clock.Now()), migrationID, unitKey)
		return err
	})
}

// UpsertResource inserts one planned resource or fills its non-state metadata.
func (s *Store) UpsertResource(ctx context.Context, runID string, resource Resource) error {
	return s.UpsertResources(ctx, runID, []Resource{resource})
}

// UpsertResources atomically inserts a batch of planned resources.
func (s *Store) UpsertResources(ctx context.Context, runID string, resources []Resource) error {
	if runID == "" || len(resources) == 0 {
		return ledgerError(errors.New("run ID and resources are required"))
	}
	for i := range resources {
		if err := s.validateResource(resources[i]); err != nil {
			return err
		}
	}
	return s.submit(ctx, func(writeCtx context.Context, tx *sql.Tx) error {
		migrationID, err := migrationForRun(writeCtx, tx, runID)
		if err != nil {
			return err
		}
		stmt, err := tx.PrepareContext(writeCtx, `INSERT INTO resources(migration_id,unit_key,resource_key,state,previous_value,target_id,etag,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(migration_id,resource_key) DO UPDATE SET unit_key=COALESCE(excluded.unit_key,resources.unit_key),previous_value=COALESCE(resources.previous_value,excluded.previous_value),target_id=COALESCE(NULLIF(excluded.target_id,''),resources.target_id),etag=COALESCE(NULLIF(excluded.etag,''),resources.etag),updated_at=excluded.updated_at`)
		if err != nil {
			return fmt.Errorf("prepare resource upsert: %w", err)
		}
		for _, resource := range resources {
			when := resource.UpdatedAt
			if when.IsZero() {
				when = s.clock.Now().UTC()
			}
			if _, err := stmt.ExecContext(writeCtx, migrationID, nullString(resource.UnitKey), resource.Key, StatePlanned, nullBytes(resource.PreviousValue), nullString(resource.TargetID), nullString(resource.ETag), unixNano(when)); err != nil {
				return errors.Join(fmt.Errorf("upsert resource %s: %w", resource.Key, err), stmt.Close())
			}
		}
		return stmt.Close()
	})
}

func (s *Store) validateResource(resource Resource) error {
	if resource.Key == "" {
		return ledgerError(errors.New("resource key is required"))
	}
	if resource.State != "" && resource.State != StatePlanned {
		return ledgerError(errors.New("new resource state must be planned"))
	}
	if len(resource.PreviousValue) > 0 && !jsonValid(resource.PreviousValue) {
		return ledgerError(errors.New("previous value must be valid JSON"))
	}
	if resource.PreviousValueSecret || (len(resource.PreviousValue) > 0 && s.redactor.Contains(string(resource.PreviousValue))) {
		return diagnosticError("TRL-APPLY-0009", errors.New("previous value contains secret material"), nil)
	}
	return nil
}

// Transition performs one legal resource state transition.
func (s *Store) Transition(ctx context.Context, runID, resourceKey string, to State, failure *Failure) error {
	return s.TransitionResources(ctx, runID, []ResourceTransition{{ResourceKey: resourceKey, To: to, Failure: failure}})
}

// TransitionResources atomically performs a batch of legal resource transitions.
func (s *Store) TransitionResources(ctx context.Context, runID string, transitions []ResourceTransition) error {
	if runID == "" || len(transitions) == 0 {
		return ledgerError(errors.New("run ID and transitions are required"))
	}
	return s.submit(ctx, func(writeCtx context.Context, tx *sql.Tx) error {
		migrationID, err := migrationForRun(writeCtx, tx, runID)
		if err != nil {
			return err
		}
		for _, transition := range transitions {
			if transition.ResourceKey == "" {
				return errors.New("resource key is required")
			}
			if err := s.transitionResource(writeCtx, tx, migrationID, runID, transition); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) transitionResource(ctx context.Context, tx *sql.Tx, migrationID, runID string, transition ResourceTransition) error {
	var from State
	var wasRetryable bool
	if err := tx.QueryRowContext(ctx, `SELECT state,retryable FROM resources WHERE migration_id=? AND resource_key=?`, migrationID, transition.ResourceKey).Scan(&from, &wasRetryable); err != nil {
		return fmt.Errorf("read resource state: %w", err)
	}
	if !legalTransition(from, transition.To, wasRetryable) {
		return fmt.Errorf("illegal resource transition %s -> %s", from, transition.To)
	}
	if transition.To == StateApplying {
		pending, err := resourceHasPendingIntent(ctx, tx, migrationID, transition.ResourceKey)
		if err != nil {
			return err
		}
		if pending {
			return diagnosticError("TRL-APPLY-0008", errors.New("resource has an unreconciled mutation intent"), nil)
		}
	}
	retryable, reason, err := transitionDetails(transition.To, transition.Failure)
	if err != nil {
		return err
	}
	now := unixNano(s.clock.Now())
	if _, err := tx.ExecContext(ctx, `UPDATE resources SET state=?,retryable=?,failure_reason=?,updated_at=? WHERE migration_id=? AND resource_key=?`, transition.To, retryable, nullString(reason), now, migrationID, transition.ResourceKey); err != nil {
		return fmt.Errorf("update resource state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO operations(migration_id,resource_key,run_id,previous_state,new_state,retryable,reason,occurred_at) VALUES(?,?,?,?,?,?,?,?)`, migrationID, transition.ResourceKey, runID, from, transition.To, retryable, nullString(reason), now); err != nil {
		return fmt.Errorf("append operation: %w", err)
	}
	return nil
}

// ListByState streams migration resources in resource-key order.
func (s *Store) ListByState(ctx context.Context, runID string, state State) iter.Seq2[Resource, error] {
	return func(yield func(Resource, error) bool) {
		migrationID, err := s.migrationForRun(ctx, runID)
		if err != nil {
			yield(Resource{}, err)
			return
		}
		last := ""
		for {
			rows, err := s.db.QueryContext(ctx, `SELECT migration_id,COALESCE(unit_key,''),resource_key,state,retryable,COALESCE(failure_reason,''),previous_value,COALESCE(target_id,''),COALESCE(etag,''),updated_at FROM resources WHERE migration_id=? AND state=? AND resource_key>? ORDER BY resource_key LIMIT ?`, migrationID, state, last, pageSize)
			if err != nil {
				yield(Resource{}, ledgerError(fmt.Errorf("list resources: %w", err)))
				return
			}
			count := 0
			for rows.Next() {
				var resource Resource
				var previous []byte
				var updated int64
				if err := rows.Scan(&resource.MigrationID, &resource.UnitKey, &resource.Key, &resource.State, &resource.Retryable, &resource.FailureReason, &previous, &resource.TargetID, &resource.ETag, &updated); err != nil {
					_ = rows.Close()
					yield(Resource{}, ledgerError(fmt.Errorf("scan resource: %w", err)))
					return
				}
				resource.PreviousValue = append(resource.PreviousValue, previous...)
				resource.UpdatedAt = fromUnixNano(updated)
				last = resource.Key
				count++
				if !yield(resource, nil) {
					_ = rows.Close()
					return
				}
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				yield(Resource{}, ledgerError(fmt.Errorf("iterate resources: %w", err)))
				return
			}
			if err := rows.Close(); err != nil {
				yield(Resource{}, ledgerError(fmt.Errorf("close resource rows: %w", err)))
				return
			}
			if count < pageSize {
				return
			}
		}
	}
}

// PreviousValue returns a copy of secret-free rollback JSON.
func (s *Store) PreviousValue(ctx context.Context, runID, resourceKey string) ([]byte, error) {
	migrationID, err := s.migrationForRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	var value []byte
	if err := s.db.QueryRowContext(ctx, `SELECT previous_value FROM resources WHERE migration_id=? AND resource_key=?`, migrationID, resourceKey).Scan(&value); err != nil {
		return nil, ledgerError(fmt.Errorf("read previous value: %w", err))
	}
	return append([]byte(nil), value...), nil
}

// RecordIntent durably records a target mutation before the request is sent.
func (s *Store) RecordIntent(ctx context.Context, intent Intent) (int64, error) {
	if intent.RunID == "" || intent.ResourceKey == "" || intent.Operation == "" || intent.Fingerprint == "" {
		return 0, ledgerError(errors.New("intent run, resource, operation, and fingerprint are required"))
	}
	var id int64
	err := s.submit(ctx, func(writeCtx context.Context, tx *sql.Tx) error {
		migrationID, err := migrationForRun(writeCtx, tx, intent.RunID)
		if err != nil {
			return err
		}
		pending, err := resourceHasPendingIntent(writeCtx, tx, migrationID, intent.ResourceKey)
		if err != nil {
			return err
		}
		if pending {
			return diagnosticError("TRL-APPLY-0008", errors.New("resource already has an unreconciled mutation intent"), nil)
		}
		when := intent.CreatedAt
		if when.IsZero() {
			when = s.clock.Now().UTC()
		}
		result, err := tx.ExecContext(writeCtx, `INSERT INTO intents(migration_id,resource_key,run_id,operation,fingerprint,created_at) VALUES(?,?,?,?,?,?)`, migrationID, intent.ResourceKey, intent.RunID, intent.Operation, intent.Fingerprint, unixNano(when))
		if err != nil {
			return fmt.Errorf("record intent: %w", err)
		}
		id, err = result.LastInsertId()
		return err
	})
	return id, err
}

// RecordOutcome closes an intent after a response or reconciliation read.
func (s *Store) RecordOutcome(ctx context.Context, intentID int64, outcome Outcome) error {
	if intentID <= 0 {
		return ledgerError(errors.New("positive intent ID is required"))
	}
	switch outcome.Status {
	case OutcomeSucceeded, OutcomeFailed, OutcomeAbsent:
	default:
		return ledgerError(fmt.Errorf("invalid outcome status %q", outcome.Status))
	}
	return s.submit(ctx, func(writeCtx context.Context, tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(writeCtx, `SELECT EXISTS(SELECT 1 FROM outcomes WHERE intent_id=?)`, intentID).Scan(&exists); err != nil {
			return fmt.Errorf("check existing outcome: %w", err)
		}
		if exists {
			return fmt.Errorf("outcome already recorded for intent %d", intentID)
		}
		_, err := tx.ExecContext(writeCtx, `INSERT INTO outcomes(intent_id,status,target_id,detail,recorded_at) VALUES(?,?,?,?,?)`, intentID, outcome.Status, nullString(outcome.TargetID), nullString(outcome.Detail), unixNano(s.clock.Now()))
		return err
	})
}

func resourceHasPendingIntent(ctx context.Context, tx *sql.Tx, migrationID, resourceKey string) (bool, error) {
	var pending bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM intents i
		LEFT JOIN outcomes o ON o.intent_id=i.id
		WHERE i.migration_id=? AND i.resource_key=? AND o.intent_id IS NULL
	)`, migrationID, resourceKey).Scan(&pending)
	if err != nil {
		return false, fmt.Errorf("check pending resource intent: %w", err)
	}
	return pending, nil
}

// ListPendingIntents streams mutation intents without outcomes in ID order.
func (s *Store) ListPendingIntents(ctx context.Context, runID string) iter.Seq2[Intent, error] {
	return func(yield func(Intent, error) bool) {
		migrationID, err := s.migrationForRun(ctx, runID)
		if err != nil {
			yield(Intent{}, err)
			return
		}
		var last int64
		for {
			rows, err := s.db.QueryContext(ctx, `SELECT i.id,i.migration_id,i.resource_key,i.run_id,i.operation,i.fingerprint,i.created_at FROM intents i LEFT JOIN outcomes o ON o.intent_id=i.id WHERE i.migration_id=? AND o.intent_id IS NULL AND i.id>? ORDER BY i.id LIMIT ?`, migrationID, last, pageSize)
			if err != nil {
				yield(Intent{}, ledgerError(err))
				return
			}
			count := 0
			for rows.Next() {
				var value Intent
				var created int64
				if err := rows.Scan(&value.ID, &value.MigrationID, &value.ResourceKey, &value.RunID, &value.Operation, &value.Fingerprint, &created); err != nil {
					_ = rows.Close()
					yield(Intent{}, ledgerError(err))
					return
				}
				value.CreatedAt = fromUnixNano(created)
				last = value.ID
				count++
				if !yield(value, nil) {
					_ = rows.Close()
					return
				}
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				yield(Intent{}, ledgerError(err))
				return
			}
			_ = rows.Close()
			if count < pageSize {
				return
			}
		}
	}
}

// RequireNoPendingIntents fails when target reconciliation is required.
func (s *Store) RequireNoPendingIntents(ctx context.Context, runID string) error {
	for _, err := range s.ListPendingIntents(ctx, runID) {
		if err != nil {
			return err
		}
		return diagnosticError("TRL-APPLY-0008", errors.New("unreconciled mutation intent"), nil)
	}
	return nil
}

// PutIDMapping records or replaces a source-to-target identifier mapping.
func (s *Store) PutIDMapping(ctx context.Context, runID string, mapping IDMapping) error {
	if runID == "" || mapping.Kind == "" || mapping.OldID == "" || mapping.NewID == "" {
		return ledgerError(errors.New("run, mapping kind, old ID, and new ID are required"))
	}
	return s.submit(ctx, func(writeCtx context.Context, tx *sql.Tx) error {
		migrationID, err := migrationForRun(writeCtx, tx, runID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(writeCtx, `INSERT INTO id_map(migration_id,kind,old_id,new_id,recorded_at) VALUES(?,?,?,?,?) ON CONFLICT(migration_id,kind,old_id) DO UPDATE SET new_id=excluded.new_id,recorded_at=excluded.recorded_at`, migrationID, mapping.Kind, mapping.OldID, mapping.NewID, unixNano(s.clock.Now()))
		return err
	})
}

// LookupIDMapping returns one source-to-target identifier mapping.
func (s *Store) LookupIDMapping(ctx context.Context, runID, kind, oldID string) (IDMapping, error) {
	migrationID, err := s.migrationForRun(ctx, runID)
	if err != nil {
		return IDMapping{}, err
	}
	value := IDMapping{MigrationID: migrationID, Kind: kind, OldID: oldID}
	if err := s.db.QueryRowContext(ctx, `SELECT new_id FROM id_map WHERE migration_id=? AND kind=? AND old_id=?`, migrationID, kind, oldID).Scan(&value.NewID); err != nil {
		return IDMapping{}, ledgerError(fmt.Errorf("lookup ID mapping: %w", err))
	}
	return value, nil
}

// ListIDMappings streams mappings in kind and old-ID order.
func (s *Store) ListIDMappings(ctx context.Context, runID string) iter.Seq2[IDMapping, error] {
	return func(yield func(IDMapping, error) bool) {
		migrationID, err := s.migrationForRun(ctx, runID)
		if err != nil {
			yield(IDMapping{}, err)
			return
		}
		lastKind, lastOld := "", ""
		for {
			rows, err := s.db.QueryContext(ctx, `SELECT migration_id,kind,old_id,new_id FROM id_map WHERE migration_id=? AND (kind>? OR (kind=? AND old_id>?)) ORDER BY kind,old_id LIMIT ?`, migrationID, lastKind, lastKind, lastOld, pageSize)
			if err != nil {
				yield(IDMapping{}, ledgerError(err))
				return
			}
			count := 0
			for rows.Next() {
				var value IDMapping
				if err := rows.Scan(&value.MigrationID, &value.Kind, &value.OldID, &value.NewID); err != nil {
					_ = rows.Close()
					yield(IDMapping{}, ledgerError(err))
					return
				}
				lastKind, lastOld = value.Kind, value.OldID
				count++
				if !yield(value, nil) {
					_ = rows.Close()
					return
				}
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				yield(IDMapping{}, ledgerError(err))
				return
			}
			_ = rows.Close()
			if count < pageSize {
				return
			}
		}
	}
}

func (s *Store) migrationForRun(ctx context.Context, runID string) (string, error) {
	var id string
	if err := s.db.QueryRowContext(ctx, `SELECT migration_id FROM runs WHERE id=?`, runID).Scan(&id); err != nil {
		return "", ledgerError(fmt.Errorf("resolve run migration: %w", err))
	}
	return id, nil
}

func migrationForRun(ctx context.Context, tx *sql.Tx, runID string) (string, error) {
	var id string
	if err := tx.QueryRowContext(ctx, `SELECT migration_id FROM runs WHERE id=?`, runID).Scan(&id); err != nil {
		return "", fmt.Errorf("resolve run migration: %w", err)
	}
	return id, nil
}

// Close flushes queued writes, closes SQLite, and releases the process lock.
func (s *Store) Close() error {
	s.gate.Lock()
	if s.closed {
		s.gate.Unlock()
		<-s.writerDone
		return nil
	}
	s.closed = true
	done := make(chan error, 1)
	s.writes <- writeRequest{done: done, close: true}
	s.gate.Unlock()
	writerErr := <-done
	<-s.writerDone
	dbErr := s.db.Close()
	lockErr := s.lock.release()
	return errors.Join(writerErr, dbErr, lockErr)
}

func (s *Store) submit(ctx context.Context, apply func(context.Context, *sql.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.gate.RLock()
	if s.closed {
		s.gate.RUnlock()
		return ledgerError(sql.ErrConnDone)
	}
	done := make(chan error, 1)
	request := writeRequest{ctx: context.WithoutCancel(ctx), apply: apply, done: done}
	select {
	case s.writes <- request:
		s.gate.RUnlock()
	case <-ctx.Done():
		s.gate.RUnlock()
		return ctx.Err()
	}
	return <-done
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
		timer := s.clock.NewTimer(batchDelay)
	collect:
		for len(batch) < batchSize {
			select {
			case request := <-s.writes:
				if request.close {
					closeRequest = &request
					break collect
				}
				batch = append(batch, request)
			case <-timer.C():
				break collect
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C():
			default:
			}
		}
		results := s.executeBatch(ctx, batch)
		for i, request := range batch {
			request.done <- results[i]
		}
		if closeRequest != nil {
			closeRequest.done <- nil
			return
		}
	}
}

//nolint:contextcheck // Queued request contexts preserve caller values after cancellation is detached.
func (s *Store) executeBatch(ctx context.Context, batch []writeRequest) []error {
	result := make([]error, len(batch))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		for i := range result {
			result[i] = ledgerError(fmt.Errorf("begin ledger batch: %w", err))
		}
		return result
	}
	for i, request := range batch {
		savepoint := "request_" + strconv.Itoa(i)
		if _, err := tx.ExecContext(request.ctx, "SAVEPOINT "+savepoint); err != nil {
			result[i] = err
			continue
		}
		result[i] = request.apply(request.ctx, tx)
		if result[i] != nil {
			_, _ = tx.ExecContext(request.ctx, "ROLLBACK TO "+savepoint)
		}
		if _, err := tx.ExecContext(request.ctx, "RELEASE "+savepoint); err != nil && result[i] == nil {
			result[i] = err
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
	for i := range result {
		if result[i] != nil {
			var diagnostic *diag.Error
			if !errors.As(result[i], &diagnostic) {
				result[i] = ledgerError(result[i])
			}
		}
	}
	return result
}

func legalUnitTransition(from, to UnitState) bool {
	switch from {
	case UnitPlanned:
		return to == UnitRunning || to == UnitSkipped
	case UnitRunning:
		return to == UnitDone || to == UnitFailed
	case UnitFailed:
		return to == UnitRunning
	default:
		return false
	}
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
			return false, "", errors.New("failed transition requires a reason")
		}
		return failure.Retryable, failure.Reason, nil
	case StateSkipped:
		if failure == nil || strings.TrimSpace(failure.Reason) == "" {
			return false, "", errors.New("skipped transition requires a manual reason")
		}
		return false, failure.Reason, nil
	default:
		if failure != nil {
			return false, "", fmt.Errorf("transition to %s does not accept failure details", to)
		}
		return false, "", nil
	}
}

var migrationName = regexp.MustCompile(`^(\d{3})_[a-z0-9][a-z0-9_-]*\.sql$`)

func applyMigrations(ctx context.Context, db *sql.DB, c clock.Clock) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return ledgerError(fmt.Errorf("create migration table: %w", err))
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return schemaError(err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		names = append(names, entry.Name())
	}
	versions, err := migrationVersions(names)
	if err != nil {
		return schemaError(err)
	}
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return schemaError(err)
	}
	var applied []int
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			_ = rows.Close()
			return schemaError(err)
		}
		applied = append(applied, version)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return schemaError(err)
	}
	if err := rows.Close(); err != nil {
		return schemaError(err)
	}
	for i, version := range applied {
		if version != i+1 {
			return schemaError(fmt.Errorf("applied migration gap: expected %03d, found %03d", i+1, version))
		}
	}
	if len(applied) > len(versions) {
		return schemaError(fmt.Errorf("ledger schema version %d is newer than supported %d", applied[len(applied)-1], len(versions)))
	}
	for i := len(applied); i < len(names); i++ {
		version := versions[i]
		script, err := fs.ReadFile(migrationFS, "migrations/"+names[i])
		if err != nil {
			return schemaError(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return schemaError(err)
		}
		if _, err := tx.ExecContext(ctx, string(script)); err != nil {
			_ = tx.Rollback()
			return schemaError(fmt.Errorf("apply migration %03d: %w", version, err))
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`, version, unixNano(c.Now())); err != nil {
			_ = tx.Rollback()
			return schemaError(err)
		}
		if err := tx.Commit(); err != nil {
			return schemaError(err)
		}
	}
	var schemaVersion int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schemaVersion); err != nil {
		return schemaError(err)
	}
	if schemaVersion != 2 {
		return schemaError(fmt.Errorf("ledger schema identity %d, want 2", schemaVersion))
	}
	return nil
}

func migrationVersions(names []string) ([]int, error) {
	versions := make([]int, 0, len(names))
	for _, name := range names {
		matches := migrationName.FindStringSubmatch(name)
		if matches == nil {
			return nil, fmt.Errorf("invalid migration filename %q", name)
		}
		version, _ := strconv.Atoi(matches[1])
		versions = append(versions, version)
	}
	for i, version := range versions {
		if version != i+1 {
			return nil, fmt.Errorf("migration gap: expected %03d, found %03d", i+1, version)
		}
	}
	return versions, nil
}

func ledgerError(cause error) error { return diagnosticError("TRL-APPLY-0004", cause, nil) }

func schemaError(cause error) error { return diagnosticError("TRL-APPLY-0010", cause, nil) }

func diagnosticError(code string, cause error, args map[string]any) error {
	opts := []diag.Option{diag.WithMessage(cause.Error())}
	if args != nil {
		opts = append(opts, diag.WithArgs(args))
	}
	finding := diag.MustNew(code, opts...)
	return fmt.Errorf("%w: %w", cause, &diag.Error{Finding: finding})
}

func requireChanged(result sql.Result, kind, id string) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("%s %q not found", kind, id)
	}
	return nil
}

func unixNano(value time.Time) int64 { return value.UTC().UnixNano() }

func fromUnixNano(value int64) time.Time { return time.Unix(0, value).UTC() }

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
