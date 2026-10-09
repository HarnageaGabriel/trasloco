package event

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	// MaxJSONLLine bounds memory used for one wire event.
	MaxJSONLLine = 1 << 20
	// MaxTrackedChangeIDs bounds change-lifecycle validation state.
	MaxTrackedChangeIDs = 1 << 16
	// MaxOpenUnits bounds concurrent unit-lifecycle validation state.
	MaxOpenUnits = 1 << 16
)

// Reader streams and validates a JSONL event stream.
type Reader struct {
	reader               io.Reader
	planApply            bool
	complete             bool
	changeTrackingCapped bool
	started              bool
}

// ReadJSONL creates a single-pass strict event reader.
func ReadJSONL(reader io.Reader, planApply bool) *Reader {
	return &Reader{reader: reader, planApply: planApply}
}

// Complete reports whether iteration consumed a valid RunFinished event.
func (r *Reader) Complete() bool { return r.complete }

// ChangeTrackingCapped reports that new change IDs were no longer tracked.
func (r *Reader) ChangeTrackingCapped() bool { return r.changeTrackingCapped }

// All returns the event/error sequence. It may be consumed once.
func (r *Reader) All(yield func(Event, error) bool) {
	if r.started {
		yield(Event{}, fmt.Errorf("event reader already consumed"))
		return
	}
	r.started = true
	buffered := bufio.NewReaderSize(r.reader, 64*1024)
	validator := newValidator(r.planApply)
	lineNumber := 0
	var line []byte
	tooLong := false
	for {
		fragment, err := buffered.ReadSlice('\n')
		hasLF := len(fragment) > 0 && fragment[len(fragment)-1] == '\n'
		if !tooLong {
			if len(line)+len(fragment) > MaxJSONLLine+1 {
				tooLong = true
				line = nil
			} else {
				line = append(line, fragment...)
			}
		}
		if hasLF {
			lineNumber++
			if tooLong {
				if !yield(Event{}, fmt.Errorf("line %d: event exceeds %d bytes", lineNumber, MaxJSONLLine)) {
					return
				}
				return
			}
			line = line[:len(line)-1]
			if len(line) == 0 {
				yield(Event{}, fmt.Errorf("line %d: empty event", lineNumber))
				return
			}
			event, decodeErr := decodeStrict(line)
			if decodeErr != nil {
				yield(Event{}, fmt.Errorf("line %d: %w", lineNumber, decodeErr))
				return
			}
			if validateErr := validator.add(event); validateErr != nil {
				yield(Event{}, fmt.Errorf("line %d: %w", lineNumber, validateErr))
				return
			}
			r.changeTrackingCapped = validator.changeTrackingCapped
			if !yield(event, nil) {
				return
			}
			line = line[:0]
			tooLong = false
		}
		if errors.Is(err, io.EOF) {
			break // Ignore a non-LF final fragment, including an overlong one.
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			yield(Event{}, err)
			return
		}
	}
	if validator.finished {
		if err := validator.terminal(); err != nil {
			yield(Event{}, err)
			return
		}
		r.complete = true
	}
}

// Seq exposes All as iter.Seq2 for APIs that store a sequence value.
func (r *Reader) Seq() iter.Seq2[Event, error] { return r.All }

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
	parsed, err := time.Parse(time.RFC3339Nano, wire.Time)
	if err != nil {
		return Event{}, fmt.Errorf("invalid event time: %w", err)
	}
	return Event{
		Version: wire.Version, Sequence: wire.Sequence, Time: parsed, RunID: wire.RunID,
		Type: wire.Type, Phase: wire.Phase, UnitKey: wire.UnitKey,
		OperationID: wire.OperationID, Attempt: wire.Attempt, Payload: wire.Payload,
	}, nil
}

type validator struct {
	planApply            bool
	next                 uint64
	runID                string
	started              bool
	finished             bool
	phases               map[string]bool
	units                map[string]bool
	planned              map[string]bool
	terminalChanges      map[string]bool
	trackedChangeIDs     map[string]bool
	changeTrackingCapped bool
}

func newValidator(planApply bool) *validator {
	return &validator{
		planApply: planApply, next: 1, phases: make(map[string]bool), units: make(map[string]bool),
		planned: make(map[string]bool), terminalChanges: make(map[string]bool), trackedChangeIDs: make(map[string]bool),
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
	fields, err := decodePayloadFields(event.Payload)
	if err != nil {
		return err
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
	if err := validateRequiredPayload(event.Type, fields); err != nil {
		return err
	}
	if event.Phase != nil && event.Type != PhaseStarted && !v.phases[*event.Phase] {
		return fmt.Errorf("event references inactive phase %q", *event.Phase)
	}
	if event.UnitKey != nil && event.Type != UnitStarted && !v.units[*event.UnitKey] {
		return fmt.Errorf("event references inactive unit %q", *event.UnitKey)
	}
	return v.lifecycle(event, fields)
}

func decodePayloadFields(payload json.RawMessage) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("payload must be an object")
	}
	return fields, nil
}

func (v *validator) lifecycle(event Event, fields map[string]json.RawMessage) error {
	switch event.Type {
	case PhaseStarted:
		id := stringField(fields, "phaseId")
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
		id := stringField(fields, "phaseId")
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
		if len(v.units) >= MaxOpenUnits {
			return fmt.Errorf("open units exceed %d", MaxOpenUnits)
		}
		v.units[*event.UnitKey] = true
	case UnitFinished:
		if event.UnitKey == nil || !v.units[*event.UnitKey] {
			return fmt.Errorf("UnitFinished before UnitStarted")
		}
		delete(v.units, *event.UnitKey)
	case ChangePlanned:
		id := stringField(fields, "changeId")
		if !v.trackChange(id) {
			return nil
		}
		if v.planned[id] {
			return fmt.Errorf("change %q planned twice", id)
		}
		v.planned[id] = true
	case ChangeApplied, ChangeFailed, ChangeSkipped:
		id := stringField(fields, "changeId")
		if !v.trackChange(id) {
			return nil
		}
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

func (v *validator) trackChange(id string) bool {
	if v.trackedChangeIDs[id] {
		return true
	}
	if len(v.trackedChangeIDs) >= MaxTrackedChangeIDs {
		v.changeTrackingCapped = true
		return false
	}
	v.trackedChangeIDs[id] = true
	return true
}

func (v *validator) terminal() error {
	if len(v.phases) != 0 || len(v.units) != 0 {
		return fmt.Errorf("terminal stream has open lifecycle")
	}
	return nil
}

var requiredPayload = map[Type][]string{
	RunStarted: {"command", "toolVersion", "configDigest"}, PhaseStarted: {"phaseId", "name", "ordinal"},
	PhaseFinished: {"phaseId", "outcome", "counts"}, UnitStarted: {"kind", "resourceRef"},
	UnitFinished:     {"outcome", "counts", "fidelity", "provenance"},
	RequestThrottled: {"platform", "endpointClass", "delayMs", "reason"}, CacheHit: {"cacheKind", "keyDigest"},
	DiagnosticEmitted: {"diagnostic"}, ChangePlanned: {"changeId", "action", "resourceRef", "digest"},
	ChangeApplied: {"changeId", "action", "resourceRef", "resultRef"},
	ChangeFailed:  {"changeId", "resourceRef", "retryable", "diagnosticCode"},
	ChangeSkipped: {"changeId", "reason", "prerequisiteId"}, ProgressTick: {"completed", "total", "unit"},
	RollbackStarted: {"sourceRunId"}, RollbackApplied: {"changeId", "result"},
	RunFinished: {"outcome", "exitCode", "counts", "reportRef"},
}

func validateRequiredPayload(eventType Type, fields map[string]json.RawMessage) error {
	allowed := make(map[string]bool, len(requiredPayload[eventType])+1)
	for _, key := range requiredPayload[eventType] {
		allowed[key] = true
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("%s payload missing %s", eventType, key)
		}
	}
	allowed["extensions"] = true
	keys := make([]string, 0, len(fields))
	//trasloco:unordered-ok keys are sorted before validation produces an error
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !allowed[key] {
			return fmt.Errorf("%s payload has unknown field %s", eventType, key)
		}
	}
	if extensions, ok := fields["extensions"]; ok {
		var object map[string]json.RawMessage
		if json.Unmarshal(extensions, &object) != nil || object == nil {
			return fmt.Errorf("payload.extensions must be an object")
		}
	}
	if rawCounts, ok := fields["counts"]; ok {
		var counts map[string]json.Number
		decoder := json.NewDecoder(bytes.NewReader(rawCounts))
		decoder.UseNumber()
		if err := decoder.Decode(&counts); err != nil || counts == nil {
			return fmt.Errorf("counts must be an object")
		}
		names := make([]string, 0, len(counts))
		//trasloco:unordered-ok names are sorted before validation produces an error
		for name := range counts {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			number, err := counts[name].Float64()
			if err != nil || number < 0 || math.Trunc(number) != number {
				return fmt.Errorf("count %s must be a non-negative integer", name)
			}
		}
	}
	if rawOutcome, ok := fields["outcome"]; ok {
		var outcome string
		if json.Unmarshal(rawOutcome, &outcome) == nil {
			valid := map[string]bool{"succeeded": true, "partial": true, "blocked": true, "failed": true, "stale": true, "cancelled": true}
			if !valid[outcome] {
				return fmt.Errorf("invalid outcome %q", outcome)
			}
		}
	}
	return nil
}

func stringField(payload map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(payload[key], &value)
	return value
}
