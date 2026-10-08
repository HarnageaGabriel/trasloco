// Package ledger stores durable apply, resume, and rollback state in SQLite.
//
// See docs/spec/ledger.md for the schema, lifecycle and concurrency contract.
package ledger

import (
	"encoding/json"
	"time"
)

// State is a durable resource lifecycle state.
type State string

const (
	// StatePlanned is the initial state before mutation starts.
	StatePlanned State = "planned"
	// StateApplying means a mutation attempt is active.
	StateApplying State = "applying"
	// StateApplied means the target mutation was verified.
	StateApplied State = "applied"
	// StateFailed records a terminal attempt failure.
	StateFailed State = "failed"
	// StateSkipped records a manual prerequisite that prevented an attempt.
	StateSkipped State = "skipped"
	// StateRolledBack means compensation completed.
	StateRolledBack State = "rolledback"
)

// RunStatus is the lifecycle status of one run (attempt).
type RunStatus string

const (
	// RunRunning is the status of an active attempt.
	RunRunning RunStatus = "running"
	// RunSucceeded means every unit completed.
	RunSucceeded RunStatus = "succeeded"
	// RunFailed means the attempt ended with an error.
	RunFailed RunStatus = "failed"
	// RunPartial means some units failed or were skipped.
	RunPartial RunStatus = "partial"
	// RunInterrupted means the attempt was cancelled or crashed.
	RunInterrupted RunStatus = "interrupted"
)

// UnitState is the lifecycle state of a migration unit.
type UnitState string

const (
	// UnitPlanned is the initial unit state.
	UnitPlanned UnitState = "planned"
	// UnitRunning means a run is processing the unit.
	UnitRunning UnitState = "running"
	// UnitDone means the unit completed.
	UnitDone UnitState = "done"
	// UnitFailed means the unit failed; it may be retried.
	UnitFailed UnitState = "failed"
	// UnitSkipped means the unit was skipped.
	UnitSkipped UnitState = "skipped"
)

// Run is one attempt of a migration and its planned units.
type Run struct {
	ID          string
	MigrationID string
	Command     string
	Status      RunStatus
	StartedAt   time.Time
	FinishedAt  time.Time
	Units       []Unit
}

// Unit is one independently processed migration unit.
type Unit struct {
	Key       string
	Kind      string
	State     UnitState
	UpdatedAt time.Time
}

// Resource is durable state needed by apply, resume, and rollback. It is keyed
// by (MigrationID, Key) so later runs see the resources of earlier attempts.
type Resource struct {
	MigrationID   string
	UnitKey       string
	Key           string
	State         State
	Retryable     bool
	FailureReason string
	// PreviousValue is rollback data. It must be valid JSON and secret-free.
	PreviousValue json.RawMessage
	// PreviousValueSecret flags PreviousValue as secret-bearing; such a value
	// is rejected and never stored.
	PreviousValueSecret bool
	TargetID            string
	ETag                string
	UpdatedAt           time.Time
}

// Failure carries terminal failure or manual-skip details.
type Failure struct {
	Retryable bool
	Reason    string
}

// ResourceTransition is one element of an atomic transition batch.
type ResourceTransition struct {
	ResourceKey string
	To          State
	Failure     *Failure
}

// Intent records a mutation that is about to be sent to the target.
type Intent struct {
	ID          int64
	MigrationID string
	ResourceKey string
	RunID       string
	Operation   string
	// Fingerprint identifies the request (for example a hash of its canonical body).
	Fingerprint string
	CreatedAt   time.Time
}

// OutcomeStatus is the recorded result of an intent.
type OutcomeStatus string

const (
	// OutcomeSucceeded means the target applied the mutation.
	OutcomeSucceeded OutcomeStatus = "succeeded"
	// OutcomeFailed means the target rejected the mutation.
	OutcomeFailed OutcomeStatus = "failed"
	// OutcomeAbsent means reconciliation found no effect on the target.
	OutcomeAbsent OutcomeStatus = "absent"
)

// Outcome closes an intent.
type Outcome struct {
	Status   OutcomeStatus
	TargetID string
	Detail   string
}

// IDMapping maps an identifier on the source platform to the target one.
type IDMapping struct {
	MigrationID string
	Kind        string
	OldID       string
	NewID       string
}
