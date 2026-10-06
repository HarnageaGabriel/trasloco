package event

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

// Stream is a validated event prefix. Complete means RunFinished was present.
type Stream struct {
	Events   []Event
	Complete bool
}

// ReadJSONL strictly reads and validates a version 1 stream. A non-LF final
// fragment is ignored as an abrupt partial write.
func ReadJSONL(reader io.Reader, planApply bool) (Stream, error) {
	buffered := bufio.NewReader(reader)
	var result Stream
	validator := newValidator(planApply)
	lineNumber := 0
	for {
		line, err := buffered.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			lineNumber++
			line = bytes.TrimSuffix(line, []byte{'\n'})
			if len(line) == 0 {
				return Stream{}, fmt.Errorf("line %d: empty event", lineNumber)
			}
			event, decodeErr := decodeStrict(line)
			if decodeErr != nil {
				return Stream{}, fmt.Errorf("line %d: %w", lineNumber, decodeErr)
			}
			if validateErr := validator.add(event); validateErr != nil {
				return Stream{}, fmt.Errorf("line %d: %w", lineNumber, validateErr)
			}
			result.Events = append(result.Events, event)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Stream{}, err
		}
	}
	result.Complete = validator.finished
	if result.Complete {
		if err := validator.terminal(); err != nil {
			return Stream{}, err
		}
	}
	return result, nil
}

func decodeStrict(line []byte) (Event, error) {
	var wire wireEvent
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return Event{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Event{}, fmt.Errorf("multiple JSON values")
	}
	if !strings.HasSuffix(wire.Time, "Z") {
		return Event{}, fmt.Errorf("event time must be UTC")
	}
	dot := strings.LastIndexByte(wire.Time, '.')
	if dot < 0 || len(wire.Time)-dot-2 < 3 {
		return Event{}, fmt.Errorf("event time must have millisecond precision")
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return Event{}, err
	}
	var event Event
	if err := json.Unmarshal(encoded, &event); err != nil {
		return Event{}, err
	}
	return event, nil
}

type validator struct {
	planApply       bool
	next            uint64
	runID           string
	started         bool
	finished        bool
	phases          map[string]bool
	units           map[string]bool
	planned         map[string]bool
	terminalChanges map[string]bool
}

func newValidator(planApply bool) *validator {
	return &validator{
		planApply: planApply, next: 1, phases: make(map[string]bool),
		units: make(map[string]bool), planned: make(map[string]bool),
		terminalChanges: make(map[string]bool),
	}
}

func (v *validator) add(event Event) error {
	if event.Version != Version {
		return fmt.Errorf("unsupported event version %d", event.Version)
	}
	if event.Sequence != v.next {
		return fmt.Errorf("sequence %d, want %d", event.Sequence, v.next)
	}
	v.next++
	if !knownTypes[event.Type] {
		return fmt.Errorf("unknown event type %q", event.Type)
	}
	if event.RunID == "" {
		return fmt.Errorf("runId is required")
	}
	if event.Attempt == 0 {
		return fmt.Errorf("attempt must be at least 1")
	}
	if v.runID == "" {
		v.runID = event.RunID
	} else if event.RunID != v.runID {
		return fmt.Errorf("runId changed")
	}
	if event.Payload == nil {
		return fmt.Errorf("payload must be an object")
	}
	if v.finished {
		return fmt.Errorf("event after RunFinished")
	}
	if !v.started {
		if event.Type != RunStarted {
			return fmt.Errorf("first event must be RunStarted")
		}
		v.started = true
	} else if event.Type == RunStarted {
		return fmt.Errorf("duplicate RunStarted")
	}
	if err := validateRequiredPayload(event); err != nil {
		return err
	}
	if event.Phase != nil && event.Type != PhaseStarted {
		if !v.phases[*event.Phase] {
			return fmt.Errorf("event references inactive phase %q", *event.Phase)
		}
	}
	if event.UnitKey != nil && event.Type != UnitStarted {
		if !v.units[*event.UnitKey] {
			return fmt.Errorf("event references inactive unit %q", *event.UnitKey)
		}
	}
	return v.lifecycle(event)
}

func (v *validator) lifecycle(event Event) error {
	switch event.Type {
	case PhaseStarted:
		id := stringField(event.Payload, "phaseId")
		if !knownPhases[id] {
			return fmt.Errorf("unknown phase %q for event version %d", id, Version)
		}
		if event.Phase == nil || *event.Phase != id {
			return fmt.Errorf("PhaseStarted phase must equal phaseId %q", id)
		}
		if v.phases[id] {
			return fmt.Errorf("phase %q already started", id)
		}
		v.phases[id] = true
	case PhaseFinished:
		id := stringField(event.Payload, "phaseId")
		if !knownPhases[id] {
			return fmt.Errorf("unknown phase %q for event version %d", id, Version)
		}
		if event.Phase == nil || *event.Phase != id {
			return fmt.Errorf("PhaseFinished phase must equal phaseId %q", id)
		}
		if !v.phases[id] {
			return fmt.Errorf("phase %q finished before start", id)
		}
		delete(v.phases, id)
	case UnitStarted:
		if event.UnitKey == nil || *event.UnitKey == "" {
			return fmt.Errorf("UnitStarted requires unitKey")
		}
		if v.units[*event.UnitKey] {
			return fmt.Errorf("unit %q already started", *event.UnitKey)
		}
		v.units[*event.UnitKey] = true
	case UnitFinished:
		if event.UnitKey == nil || !v.units[*event.UnitKey] {
			return fmt.Errorf("UnitFinished before UnitStarted")
		}
		delete(v.units, *event.UnitKey)
	case ChangePlanned:
		id := stringField(event.Payload, "changeId")
		if v.planned[id] {
			return fmt.Errorf("change %q planned twice", id)
		}
		v.planned[id] = true
	case ChangeApplied, ChangeFailed, ChangeSkipped:
		id := stringField(event.Payload, "changeId")
		if v.planApply && !v.planned[id] {
			return fmt.Errorf("terminal change %q has no prior plan", id)
		}
		if v.terminalChanges[id] {
			return fmt.Errorf("change %q has multiple terminal events", id)
		}
		v.terminalChanges[id] = true
	case RunFinished:
		if len(v.phases) != 0 || len(v.units) != 0 {
			return fmt.Errorf("RunFinished with open phase or unit")
		}
		v.finished = true
	}
	return nil
}

func (v *validator) terminal() error {
	if len(v.phases) != 0 || len(v.units) != 0 {
		return fmt.Errorf("terminal stream has open lifecycle")
	}
	return nil
}

var requiredPayload = map[Type][]string{
	RunStarted:        {"command", "toolVersion", "configDigest"},
	PhaseStarted:      {"phaseId", "name", "ordinal"},
	PhaseFinished:     {"phaseId", "outcome", "counts"},
	UnitStarted:       {"kind", "resourceRef"},
	UnitFinished:      {"outcome", "counts", "fidelity", "provenance"},
	RequestThrottled:  {"platform", "endpointClass", "delayMs", "reason"},
	CacheHit:          {"cacheKind", "keyDigest"},
	DiagnosticEmitted: {"diagnostic"},
	ChangePlanned:     {"changeId", "action", "resourceRef", "digest"},
	ChangeApplied:     {"changeId", "action", "resourceRef", "resultRef"},
	ChangeFailed:      {"changeId", "resourceRef", "retryable", "diagnosticCode"},
	ChangeSkipped:     {"changeId", "reason", "prerequisiteId"},
	ProgressTick:      {"completed", "total", "unit"},
	RollbackStarted:   {"sourceRunId"},
	RollbackApplied:   {"changeId", "result"},
	RunFinished:       {"outcome", "exitCode", "counts", "reportRef"},
}

func validateRequiredPayload(event Event) error {
	allowed := make(map[string]bool, len(requiredPayload[event.Type])+1)
	for _, key := range requiredPayload[event.Type] {
		allowed[key] = true
		if _, ok := event.Payload[key]; !ok {
			return fmt.Errorf("%s payload missing %s", event.Type, key)
		}
	}
	allowed["extensions"] = true
	for key := range event.Payload {
		if !allowed[key] {
			return fmt.Errorf("%s payload has unknown field %s", event.Type, key)
		}
	}
	if extensions, ok := event.Payload["extensions"]; ok {
		if _, ok := extensions.(map[string]any); !ok {
			return fmt.Errorf("payload.extensions must be an object")
		}
	}
	for key, value := range event.Payload {
		if strings.EqualFold(key, "counts") {
			counts, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("counts must be an object")
			}
			for name, count := range counts {
				number, ok := count.(float64)
				if !ok || number < 0 || math.Trunc(number) != number {
					return fmt.Errorf("count %s must be a non-negative integer", name)
				}
			}
		}
	}
	if outcome, ok := event.Payload["outcome"].(string); ok {
		validOutcome := map[string]bool{"succeeded": true, "partial": true, "blocked": true, "failed": true, "stale": true, "cancelled": true}
		if !validOutcome[outcome] {
			return fmt.Errorf("invalid outcome %q", outcome)
		}
	}
	return nil
}

func stringField(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}
