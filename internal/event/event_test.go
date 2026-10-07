package event

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/clock"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

var eventTime = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func TestReadJSONLVectors(t *testing.T) {
	unit := "u1"
	cases := []struct {
		name      string
		planApply bool
		events    []Event
		wantErr   string
		complete  bool
	}{
		{name: "prefix", events: []Event{started(1)}},
		{name: "complete", events: []Event{started(1), finished(2)}, complete: true},
		{name: "sequence gap", events: []Event{started(1), progress(3, nil)}, wantErr: "sequence 3, want 2"},
		{name: "duplicate start", events: []Event{started(1), started(2)}, wantErr: "duplicate RunStarted"},
		{name: "finish before start", events: []Event{started(1), unitFinished(2, unit)}, wantErr: "inactive unit"},
		{name: "event after finish", events: []Event{started(1), finished(2), progress(3, nil)}, wantErr: "event after RunFinished"},
		{name: "valid units", events: []Event{started(1), unitStarted(2, unit), progress(3, &unit), unitFinished(4, unit), finished(5)}, complete: true},
		{name: "unplanned apply", planApply: true, events: []Event{started(1), baseEvent(2, ChangeApplied, ChangeAppliedPayload{ChangeID: "c", Action: "create", ResourceRef: "r", ResultRef: "x"})}, wantErr: "no prior plan"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader, _, err := readEvents(t, tc.planApply, tc.events...)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || reader.Complete() != tc.complete {
				t.Fatalf("complete = %v, err = %v", reader.Complete(), err)
			}
		})
	}
}

func TestReadJSONLStrictAndPartial(t *testing.T) {
	start := marshalLine(t, started(1))
	tests := []struct {
		name    string
		input   string
		wantN   int
		wantErr string
	}{
		{name: "partial final line", input: start + `{"version":`, wantN: 1},
		{name: "empty line", input: "\n", wantErr: "empty event"},
		{name: "unknown field", input: strings.Replace(start, `"payload":`, `"future":1,"payload":`, 1), wantErr: "unknown field"},
		{name: "multiple values", input: strings.TrimSuffix(start, "\n") + " {}\n", wantErr: "multiple JSON values"},
		{name: "not UTC", input: strings.Replace(start, "Z\"", "+00:00\"", 1), wantErr: "must be UTC"},
		{name: "low precision", input: strings.Replace(start, ".000000000Z", "Z", 1), wantErr: "millisecond precision"},
		{name: "oversize", input: strings.Repeat("x", MaxJSONLLine+1) + "\n", wantErr: "exceeds"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := ReadJSONL(strings.NewReader(tc.input), false)
			count := 0
			var gotErr error
			for _, err := range reader.Seq() {
				if err != nil {
					gotErr = err
					break
				}
				count++
			}
			if count != tc.wantN {
				t.Fatalf("count = %d, want %d", count, tc.wantN)
			}
			if tc.wantErr != "" && (gotErr == nil || !strings.Contains(gotErr.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want %q", gotErr, tc.wantErr)
			}
		})
	}
	reader := ReadJSONL(strings.NewReader(start), false)
	for range reader.All {
	}
	for _, err := range reader.All {
		if err == nil || !strings.Contains(err.Error(), "already consumed") {
			t.Fatalf("second iteration error = %v", err)
		}
	}
}

func TestValidatorPayloadAndLifecycle(t *testing.T) {
	phase := string(PhaseDiscover)
	phaseStart := baseEvent(2, PhaseStarted, PhaseStartedPayload{PhaseID: PhaseDiscover, Name: "Discover", Ordinal: 1})
	phaseStart.Phase = &phase
	phaseFinish := baseEvent(3, PhaseFinished, PhaseFinishedPayload{PhaseID: PhaseDiscover, Outcome: "succeeded", Counts: map[string]int{}})
	phaseFinish.Phase = &phase
	valid := []Event{started(1), phaseStart, phaseFinish, finished(4)}
	if reader, _, err := readEvents(t, false, valid...); err != nil || !reader.Complete() {
		t.Fatalf("valid lifecycle: complete=%v err=%v", reader.Complete(), err)
	}

	mutations := []struct {
		name string
		edit func(*Event)
		want string
	}{
		{name: "version", edit: func(e *Event) { e.Version = 2 }, want: "unsupported event version"},
		{name: "run id", edit: func(e *Event) { e.RunID = "" }, want: "runId is required"},
		{name: "attempt", edit: func(e *Event) { e.Attempt = 0 }, want: "attempt must be at least 1"},
		{name: "unknown type", edit: func(e *Event) { e.Type = "Future" }, want: "unknown event type"},
		{name: "non object", edit: func(e *Event) { e.Payload = json.RawMessage(`[]`) }, want: "payload must be an object"},
		{name: "missing field", edit: func(e *Event) { e.Payload = json.RawMessage(`{}`) }, want: "payload missing"},
		{name: "unknown payload field", edit: func(e *Event) {
			e.Payload = json.RawMessage(`{"command":"plan","toolVersion":"v","configDigest":"d","extra":1}`)
		}, want: "unknown field extra"},
		{name: "bad extensions", edit: func(e *Event) {
			e.Payload = json.RawMessage(`{"command":"plan","toolVersion":"v","configDigest":"d","extensions":[]}`)
		}, want: "extensions must be an object"},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			event := started(1)
			tc.edit(&event)
			_, _, err := readEvents(t, false, event)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidatorBounds(t *testing.T) {
	v := newValidator(false)
	for i := 0; i < MaxTrackedChangeIDs; i++ {
		v.trackedChangeIDs[fmt.Sprint(i)] = true
	}
	if v.trackChange("overflow") || !v.changeTrackingCapped {
		t.Fatal("change tracking cap not enforced")
	}
	v = newValidator(false)
	for i := 0; i < MaxOpenUnits; i++ {
		v.units[fmt.Sprint(i)] = true
	}
	event := baseEvent(1, UnitStarted, UnitStartedPayload{Kind: "k", ResourceRef: "r"})
	unit := "overflow"
	event.UnitKey = &unit
	if err := v.lifecycle(event, payloadFields(t, event)); err == nil || !strings.Contains(err.Error(), "open units exceed") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidatorChangeAndPhaseFailures(t *testing.T) {
	phase := string(PhaseDiscover)
	phaseStart := baseEvent(0, PhaseStarted, PhaseStartedPayload{PhaseID: PhaseDiscover, Name: "Discover", Ordinal: 1})
	phaseStart.Phase = &phase
	phaseFinish := baseEvent(0, PhaseFinished, PhaseFinishedPayload{PhaseID: PhaseDiscover, Outcome: "succeeded", Counts: map[string]int{}})
	phaseFinish.Phase = &phase
	v := newValidator(true)
	if err := v.lifecycle(phaseFinish, payloadFields(t, phaseFinish)); err == nil {
		t.Fatal("phase finish before start accepted")
	}
	if err := v.lifecycle(phaseStart, payloadFields(t, phaseStart)); err != nil {
		t.Fatal(err)
	}
	if err := v.lifecycle(phaseStart, payloadFields(t, phaseStart)); err == nil {
		t.Fatal("duplicate phase accepted")
	}
	planned := baseEvent(0, ChangePlanned, ChangePlannedPayload{ChangeID: "c", Action: "create", ResourceRef: "r", Digest: "d"})
	applied := baseEvent(0, ChangeApplied, ChangeAppliedPayload{ChangeID: "c", Action: "create", ResourceRef: "r", ResultRef: "x"})
	if err := v.lifecycle(planned, payloadFields(t, planned)); err != nil {
		t.Fatal(err)
	}
	if err := v.lifecycle(planned, payloadFields(t, planned)); err == nil {
		t.Fatal("duplicate plan accepted")
	}
	if err := v.lifecycle(applied, payloadFields(t, applied)); err != nil {
		t.Fatal(err)
	}
	if err := v.lifecycle(applied, payloadFields(t, applied)); err == nil {
		t.Fatal("duplicate terminal event accepted")
	}
}

func TestEventConstructionAndWire(t *testing.T) {
	event, err := New(CacheHit, CacheHitPayload{CacheKind: "http", KeyDigest: "sha256:x"})
	if err != nil {
		t.Fatal(err)
	}
	var payload CacheHitPayload
	if err := event.DecodePayload(&payload); err != nil || payload.CacheKind != "http" {
		t.Fatalf("payload=%+v err=%v", payload, err)
	}
	if _, err := New(CacheHit, []string{"not", "object"}); err == nil {
		t.Fatal("array payload accepted")
	}
	if err := (Event{Type: CacheHit}).DecodePayload(&payload); err == nil {
		t.Fatal("empty payload accepted")
	}
	event = started(1)
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Event
	if err := json.Unmarshal(encoded, &decoded); err != nil || !decoded.Time.Equal(event.Time) {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	bad := bytes.Replace(encoded, []byte(".000000000Z"), []byte("-bad"), 1)
	if err := json.Unmarshal(bad, &decoded); err == nil {
		t.Fatal("bad timestamp accepted")
	}
}

func TestSanitizeEvent(t *testing.T) {
	r := redact.New()
	r.Add("canary-secret")
	event := baseEvent(0, ChangeApplied, map[string]any{
		"changeId": "c", "action": "create",
		"resourceRef": "https://user:pass@example.test/r?q=canary-secret#fragment",
		"resultRef":   "relative/path?preserved", "extensions": map[string]any{
			"authorization": "Bearer value", "note": "canary-secret", "assetUrl": "ftp://example.test/a?q=preserved",
		},
	})
	clean, err := sanitizeEvent(event, r)
	if err != nil {
		t.Fatal(err)
	}
	text := string(clean.Payload)
	for _, forbidden := range []string{"user", "pass", "fragment", "canary-secret", "Bearer value", "example.test/r?q="} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("%q leaked in %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "relative/path?preserved") || !strings.Contains(text, "ftp://example.test/a?q=preserved") {
		t.Fatalf("non-http reference altered: %s", text)
	}
	event = baseEvent(0, ProgressTick, map[string]any{"completed": 1, "total": nil, "unit": "items", "extensions": map[string]any{"values": []any{"canary-secret", 2, true, nil}}})
	phase, unit, operation := "canary-secret", "unit", "operation"
	event.Phase, event.UnitKey, event.OperationID = &phase, &unit, &operation
	clean, err = sanitizeEvent(event, r)
	if err != nil || strings.Contains(string(clean.Payload), "canary-secret") || *clean.Phase != redact.Replacement() {
		t.Fatalf("nested sanitization = %+v, err=%v", clean, err)
	}
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`[]`), json.RawMessage(`{"x":`)} {
		event.Payload = raw
		if _, err := sanitizeEvent(event, r); !errors.Is(err, ErrRedaction) {
			t.Fatalf("payload %q error = %v", raw, err)
		}
	}
}

type fixedClock struct {
	clock.Real
	now time.Time
}

func (c fixedClock) Now() time.Time { return c.now }

type recordingSink struct {
	mu     sync.Mutex
	events []Event
	err    error
	closed bool
}

type blockingSink struct {
	*recordingSink
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingSink() *blockingSink {
	return &blockingSink{recordingSink: &recordingSink{}, started: make(chan struct{}), release: make(chan struct{})}
}

func (s *blockingSink) Handle(ctx context.Context, event Event) error {
	s.once.Do(func() {
		close(s.started)
		<-s.release
	})
	return s.recordingSink.Handle(ctx, event)
}

type closeErrorSink struct{ error }

func (closeErrorSink) Handle(context.Context, Event) error { return nil }
func (s closeErrorSink) Close() error                      { return s.error }

func (s *recordingSink) Handle(_ context.Context, event Event) error {
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	return nil
}

func (s *recordingSink) Close() error { s.closed = true; return s.err }

func (s *recordingSink) snapshot() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...)
}

func TestBusSinkIsolation(t *testing.T) {
	collector := diag.NewCollector()
	failing := &recordingSink{err: errors.New("disk canary-secret")}
	working := &recordingSink{}
	r := redact.New()
	r.Add("canary-secret")
	bus, err := NewBusWithOptions(Options{
		RunID: "run", Redactor: r, Buffer: 4, Clock: fixedClock{now: eventTime}, Diagnostics: collector,
	}, failing, working)
	if err != nil {
		t.Fatal(err)
	}
	publishRun(t, bus)
	if err := bus.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	events := working.snapshot()
	if len(events) != 3 || events[1].Type != DiagnosticEmitted || events[2].Sequence != 3 {
		t.Fatalf("events = %+v", events)
	}
	diagnostics := collector.List()
	if len(diagnostics) != 1 || diagnostics[0].Code != "TRL-INT-0004" || strings.Contains(diagnostics[0].Message, "canary-secret") {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

func TestBusFailClosed(t *testing.T) {
	failing := &recordingSink{err: errors.New("ledger unavailable")}
	bus, err := NewBus("run", redact.New(), 2, FailClosedSink(failing))
	if err != nil {
		t.Fatal(err)
	}
	start, _ := New(RunStarted, RunStartedPayload{Command: "plan", ToolVersion: "v", ConfigDigest: "d"})
	if err := bus.Publish(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	if err := bus.Close(t.Context()); err == nil || !strings.Contains(err.Error(), "ledger unavailable") {
		t.Fatalf("close error = %v", err)
	}
	if err := bus.Close(t.Context()); err == nil {
		t.Fatal("second close lost fail-closed error")
	}
}

func TestBusCoalescesUnderBackpressure(t *testing.T) {
	sink := newBlockingSink()
	bus, err := NewBus("run", redact.New(), 1, sink)
	if err != nil {
		t.Fatal(err)
	}
	start, _ := New(RunStarted, RunStartedPayload{Command: "plan", ToolVersion: "v", ConfigDigest: "d"})
	if err := bus.Publish(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	<-sink.started
	for completed := int64(1); completed <= 3; completed++ {
		tick, _ := New(ProgressTick, ProgressTickPayload{Completed: completed, Unit: "items"})
		if err := bus.Publish(t.Context(), tick); err != nil {
			t.Fatal(err)
		}
	}
	finish, _ := New(RunFinished, RunFinishedPayload{Outcome: "succeeded", Counts: map[string]int{}, ReportRef: "r"})
	published := make(chan error, 1)
	go func() { published <- bus.Publish(t.Context(), finish) }()
	close(sink.release)
	if err := <-published; err != nil {
		t.Fatal(err)
	}
	if err := bus.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	events := sink.snapshot()
	if len(events) != 4 {
		t.Fatalf("events = %+v", events)
	}
	var last ProgressTickPayload
	if err := events[2].DecodePayload(&last); err != nil || last.Completed != 3 {
		t.Fatalf("last progress = %+v, err=%v", last, err)
	}
}

func TestCoalescingKeys(t *testing.T) {
	unit := "u"
	tests := []struct {
		event Event
		want  string
		ok    bool
	}{
		{event: progress(0, nil), want: "ProgressTick", ok: true},
		{event: progress(0, &unit), want: "ProgressTicku", ok: true},
		{event: baseEvent(0, CacheHit, CacheHitPayload{CacheKind: "http", KeyDigest: "digest"}), want: "CacheHithttp:digest", ok: true},
		{event: baseEvent(0, RequestThrottled, RequestThrottledPayload{Platform: "github", EndpointClass: "rest"}), want: "RequestThrottledgithub:rest", ok: true},
		{event: finished(0)},
	}
	for _, tc := range tests {
		got, ok := coalescingKey(tc.event)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("key=(%q,%v), want (%q,%v)", got, ok, tc.want, tc.ok)
		}
	}
}

func TestBusCloseSinkFailures(t *testing.T) {
	collector := diag.NewCollector()
	bus, err := NewBusWithOptions(Options{RunID: "run", Buffer: 2, Diagnostics: collector}, closeErrorSink{errors.New("stdout closed")})
	if err != nil {
		t.Fatal(err)
	}
	publishRun(t, bus)
	if err := bus.Close(t.Context()); err != nil || len(collector.List()) != 1 {
		t.Fatalf("optional close err=%v diagnostics=%+v", err, collector.List())
	}
	bus, err = NewBus("run", nil, 2, FailClosedSink(closeErrorSink{errors.New("ledger close")}))
	if err != nil {
		t.Fatal(err)
	}
	publishRun(t, bus)
	if err := bus.Close(t.Context()); err == nil || !strings.Contains(err.Error(), "ledger close") {
		t.Fatalf("fail-closed close error=%v", err)
	}
}

func TestBusConstructionAndClose(t *testing.T) {
	for _, options := range []Options{{Buffer: 1}, {RunID: "run"}, {RunID: "run", Buffer: 1}} {
		sinks := []Sink(nil)
		if options.RunID != "" && options.Buffer > 0 {
			sinks = []Sink{nil}
		}
		if _, err := NewBusWithOptions(options, sinks...); err == nil {
			t.Fatalf("options %+v accepted", options)
		}
	}
	if FailClosedSink(nil) != nil {
		t.Fatal("nil fail-closed sink not preserved")
	}
	sink := &recordingSink{}
	bus, err := NewBusWithClock("run", nil, 2, nil, sink)
	if err != nil {
		t.Fatal(err)
	}
	publishRun(t, bus)
	if err := bus.Close(t.Context()); err != nil || !sink.closed {
		t.Fatalf("close=%v closed=%v", err, sink.closed)
	}
	event, _ := New(ProgressTick, ProgressTickPayload{Completed: 1, Unit: "item"})
	if err := bus.Publish(t.Context(), event); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("publish after close error = %v", err)
	}
}

func publishRun(t *testing.T, bus *Bus) {
	t.Helper()
	start, _ := New(RunStarted, RunStartedPayload{Command: "plan", ToolVersion: "v", ConfigDigest: "d"})
	finish, _ := New(RunFinished, RunFinishedPayload{Outcome: "succeeded", Counts: map[string]int{}, ReportRef: "report.json"})
	if err := bus.Publish(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(t.Context(), finish); err != nil {
		t.Fatal(err)
	}
}

func readEvents(t *testing.T, planApply bool, events ...Event) (*Reader, []Event, error) {
	t.Helper()
	var input bytes.Buffer
	for _, event := range events {
		input.WriteString(marshalLine(t, event))
	}
	reader := ReadJSONL(&input, planApply)
	var got []Event
	for event, err := range reader.All {
		if err != nil {
			return reader, got, err
		}
		got = append(got, event)
	}
	return reader, got, nil
}

func marshalLine(t *testing.T, event Event) string {
	t.Helper()
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded) + "\n"
}

func baseEvent(sequence uint64, eventType Type, payload any) Event {
	encoded, _ := json.Marshal(payload)
	return Event{Version: Version, Sequence: sequence, Time: eventTime, RunID: "run", Type: eventType, Attempt: 1, Payload: encoded}
}

func started(sequence uint64) Event {
	return baseEvent(sequence, RunStarted, RunStartedPayload{Command: "plan", ToolVersion: "v", ConfigDigest: "d"})
}

func finished(sequence uint64) Event {
	return baseEvent(sequence, RunFinished, RunFinishedPayload{Outcome: "succeeded", Counts: map[string]int{}, ReportRef: "report.json"})
}

func progress(sequence uint64, unit *string) Event {
	event := baseEvent(sequence, ProgressTick, ProgressTickPayload{Completed: 1, Unit: "items"})
	event.UnitKey = unit
	return event
}

func unitStarted(sequence uint64, unit string) Event {
	event := baseEvent(sequence, UnitStarted, UnitStartedPayload{Kind: "pipeline", ResourceRef: unit})
	event.UnitKey = &unit
	return event
}

func unitFinished(sequence uint64, unit string) Event {
	event := baseEvent(sequence, UnitFinished, UnitFinishedPayload{Outcome: "succeeded", Counts: map[string]int{}, Fidelity: "exact", Provenance: "deterministic"})
	event.UnitKey = &unit
	return event
}

func payloadFields(t *testing.T, event Event) map[string]json.RawMessage {
	t.Helper()
	fields, err := decodePayloadFields(event.Payload)
	if err != nil {
		t.Fatal(err)
	}
	return fields
}

func BenchmarkSanitizeEventRedaction(b *testing.B) {
	r := redact.New()
	r.Add("benchmark-secret")
	event := baseEvent(0, ChangeApplied, map[string]any{
		"changeId": "change", "action": "create",
		"resourceRef": "https://example.test/resource?token=benchmark-secret",
		"resultRef":   "https://example.test/result?signature=benchmark-secret",
	})
	b.ReportAllocs()
	for b.Loop() {
		if _, err := sanitizeEvent(event, r); err != nil {
			b.Fatal(err)
		}
	}
}
