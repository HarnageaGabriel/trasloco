// Package event implements the version 1 engine event stream.
package event

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

// Version is the current event wire schema version.
const Version = 1

// Type identifies an event payload contract.
type Type string

const (
	// RunStarted opens a run and must be the first event.
	RunStarted Type = "RunStarted"
	// PhaseStarted opens an engine phase.
	PhaseStarted Type = "PhaseStarted"
	// PhaseFinished closes an engine phase.
	PhaseFinished Type = "PhaseFinished"
	// UnitStarted opens work for one unit.
	UnitStarted Type = "UnitStarted"
	// UnitFinished closes work for one unit.
	UnitFinished Type = "UnitFinished"
	// RequestThrottled reports a scheduled rate-limit delay.
	RequestThrottled Type = "RequestThrottled"
	// CacheHit reports content-addressed reuse.
	CacheHit Type = "CacheHit"
	// DiagnosticEmitted reports one diagnostic.
	DiagnosticEmitted Type = "DiagnosticEmitted"
	// ChangePlanned reports one canonical planned change.
	ChangePlanned Type = "ChangePlanned"
	// ChangeApplied reports a verified mutation.
	ChangeApplied Type = "ChangeApplied"
	// ChangeFailed reports a terminal failed attempt.
	ChangeFailed Type = "ChangeFailed"
	// ChangeSkipped reports a change that was not attempted.
	ChangeSkipped Type = "ChangeSkipped"
	// ProgressTick reports a coalescible progress snapshot.
	ProgressTick Type = "ProgressTick"
	// RollbackStarted opens a compensating rollback.
	RollbackStarted Type = "RollbackStarted"
	// RollbackApplied reports one completed compensation.
	RollbackApplied Type = "RollbackApplied"
	// RunFinished closes an orderly run and must be last.
	RunFinished Type = "RunFinished"
)

var knownTypes = map[Type]bool{
	RunStarted: true, PhaseStarted: true, PhaseFinished: true,
	UnitStarted: true, UnitFinished: true, RequestThrottled: true,
	CacheHit: true, DiagnosticEmitted: true, ChangePlanned: true,
	ChangeApplied: true, ChangeFailed: true, ChangeSkipped: true,
	ProgressTick: true, RollbackStarted: true, RollbackApplied: true,
	RunFinished: true,
}

// Event is the canonical JSONL envelope.
type Event struct {
	Version     int            `json:"version"`
	Sequence    uint64         `json:"sequence"`
	Time        time.Time      `json:"-"`
	RunID       string         `json:"runId"`
	Type        Type           `json:"type"`
	Phase       *string        `json:"phase"`
	UnitKey     *string        `json:"unitKey"`
	OperationID *string        `json:"operationId"`
	Payload     map[string]any `json:"payload"`
}

type wireEvent struct {
	Version     int            `json:"version"`
	Sequence    uint64         `json:"sequence"`
	Time        string         `json:"time"`
	RunID       string         `json:"runId"`
	Type        Type           `json:"type"`
	Phase       *string        `json:"phase"`
	UnitKey     *string        `json:"unitKey"`
	OperationID *string        `json:"operationId"`
	Payload     map[string]any `json:"payload"`
}

// MarshalJSON emits UTC RFC 3339 with at least millisecond precision.
func (e Event) MarshalJSON() ([]byte, error) {
	wire := wireEvent{
		Version: e.Version, Sequence: e.Sequence,
		Time:  e.Time.UTC().Format("2006-01-02T15:04:05.000000000Z"),
		RunID: e.RunID, Type: e.Type, Phase: e.Phase, UnitKey: e.UnitKey,
		OperationID: e.OperationID, Payload: e.Payload,
	}
	return json.Marshal(wire)
}

// UnmarshalJSON parses the canonical envelope. Strict unknown-field checking
// is performed by ReadJSONL.
func (e *Event) UnmarshalJSON(data []byte) error {
	var wire wireEvent
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339Nano, wire.Time)
	if err != nil {
		return fmt.Errorf("invalid event time: %w", err)
	}
	e.Version, e.Sequence, e.Time = wire.Version, wire.Sequence, parsed
	e.RunID, e.Type, e.Phase = wire.RunID, wire.Type, wire.Phase
	e.UnitKey, e.OperationID, e.Payload = wire.UnitKey, wire.OperationID, wire.Payload
	return nil
}

// New constructs an unsequenced event from any JSON-object payload.
func New(eventType Type, payload any) (Event, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("marshal %s payload: %w", eventType, err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		return Event{}, fmt.Errorf("%s payload must be an object: %w", eventType, err)
	}
	if object == nil {
		object = make(map[string]any)
	}
	return Event{Version: Version, Type: eventType, Payload: object}, nil
}

// RunStartedPayload is the RunStarted payload contract.
type RunStartedPayload struct {
	Command      string `json:"command"`
	ToolVersion  string `json:"toolVersion"`
	ConfigDigest string `json:"configDigest"`
}

// PhaseStartedPayload is the PhaseStarted payload contract.
type PhaseStartedPayload struct {
	PhaseID string `json:"phaseId"`
	Name    string `json:"name"`
	Ordinal int    `json:"ordinal"`
}

// PhaseFinishedPayload is the PhaseFinished payload contract.
type PhaseFinishedPayload struct {
	PhaseID string         `json:"phaseId"`
	Outcome string         `json:"outcome"`
	Counts  map[string]int `json:"counts"`
}

// UnitStartedPayload is the UnitStarted payload contract.
type UnitStartedPayload struct {
	Kind        string `json:"kind"`
	ResourceRef string `json:"resourceRef"`
}

// UnitFinishedPayload is the UnitFinished payload contract.
type UnitFinishedPayload struct {
	Outcome  string         `json:"outcome"`
	Counts   map[string]int `json:"counts"`
	Fidelity string         `json:"fidelity"`
}

// RequestThrottledPayload is the RequestThrottled payload contract.
type RequestThrottledPayload struct {
	Platform      string `json:"platform"`
	EndpointClass string `json:"endpointClass"`
	DelayMS       int64  `json:"delayMs"`
	Reason        string `json:"reason"`
}

// CacheHitPayload is the CacheHit payload contract.
type CacheHitPayload struct {
	CacheKind string `json:"cacheKind"`
	KeyDigest string `json:"keyDigest"`
}

// DiagnosticEmittedPayload is the DiagnosticEmitted payload contract.
type DiagnosticEmittedPayload struct {
	Diagnostic diag.Diagnostic `json:"diagnostic"`
}

// ChangePlannedPayload is the ChangePlanned payload contract.
type ChangePlannedPayload struct {
	ChangeID    string `json:"changeId"`
	Action      string `json:"action"`
	ResourceRef string `json:"resourceRef"`
	Digest      string `json:"digest"`
}

// ChangeAppliedPayload is the ChangeApplied payload contract.
type ChangeAppliedPayload struct {
	ChangeID    string `json:"changeId"`
	Action      string `json:"action"`
	ResourceRef string `json:"resourceRef"`
	ResultRef   string `json:"resultRef"`
}

// ChangeFailedPayload is the ChangeFailed payload contract.
type ChangeFailedPayload struct {
	ChangeID       string `json:"changeId"`
	ResourceRef    string `json:"resourceRef"`
	Retryable      bool   `json:"retryable"`
	DiagnosticCode string `json:"diagnosticCode"`
}

// ChangeSkippedPayload is the ChangeSkipped payload contract.
type ChangeSkippedPayload struct {
	ChangeID       string `json:"changeId"`
	Reason         string `json:"reason"`
	PrerequisiteID string `json:"prerequisiteId"`
}

// ProgressTickPayload is the ProgressTick payload contract.
type ProgressTickPayload struct {
	Completed int64   `json:"completed"`
	Total     *int64  `json:"total"`
	Unit      string  `json:"unit"`
	Rate      float64 `json:"rate"`
}

// RollbackStartedPayload is the RollbackStarted payload contract.
type RollbackStartedPayload struct {
	SourceRunID string `json:"sourceRunId"`
}

// RollbackAppliedPayload is the RollbackApplied payload contract.
type RollbackAppliedPayload struct {
	ChangeID string `json:"changeId"`
	Result   string `json:"result"`
}

// RunFinishedPayload is the RunFinished payload contract.
type RunFinishedPayload struct {
	Outcome   string         `json:"outcome"`
	ExitCode  int            `json:"exitCode"`
	Counts    map[string]int `json:"counts"`
	ReportRef string         `json:"reportRef"`
}
