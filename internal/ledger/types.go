// Package ledger stores durable apply, resume, and rollback state in SQLite.
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

// Run is a ledger run and its initially planned units.
type Run struct {
	ID        string
	Command   string
	Status    string
	StartedAt time.Time
	Units     []Unit
}

// Unit is one independently processed migration unit.
type Unit struct {
	Key  string
	Kind string
}

// Resource is durable state needed by apply, resume, and rollback.
type Resource struct {
	RunID         string
	UnitKey       string
	Key           string
	State         State
	Retryable     bool
	FailureReason string
	PreviousValue json.RawMessage
	TargetID      string
	ETag          string
	UpdatedAt     time.Time
}

// Failure carries terminal failure or manual-skip details.
type Failure struct {
	Retryable bool
	Reason    string
}
