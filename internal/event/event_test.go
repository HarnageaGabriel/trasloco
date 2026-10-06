package event

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

func TestEventVectors(t *testing.T) {
	t.Run("EVENT-001", func(t *testing.T) {
		stream, err := readEvents(t, false, started(1))
		if err != nil || stream.Complete {
			t.Fatalf("stream = %#v, err = %v", stream, err)
		}
	})
	t.Run("EVENT-002", func(t *testing.T) {
		_, err := readEvents(t, false, started(1), progress(2, nil), progress(4, nil))
		if err == nil {
			t.Fatal("sequence gap accepted")
		}
	})
	t.Run("EVENT-003", func(t *testing.T) {
		_, err := readEvents(t, false, started(1), progress(1, nil))
		if err == nil {
			t.Fatal("duplicate sequence accepted")
		}
	})
	t.Run("EVENT-004", func(t *testing.T) {
		first := started(1)
		first.Time = time.Date(2026, 10, 6, 12, 0, 1, 0, time.UTC)
		second := progress(2, nil)
		second.Time = first.Time.Add(-time.Second)
		if _, err := readEvents(t, false, first, second); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("EVENT-005", func(t *testing.T) {
		unit := "u1"
		finished := baseEvent(2, UnitFinished, map[string]any{"outcome": "failed", "counts": map[string]int{}, "fidelity": "exact", "provenance": "deterministic"})
		finished.UnitKey = &unit
		if _, err := readEvents(t, false, started(1), finished); err == nil {
			t.Fatal("UnitFinished before UnitStarted accepted")
		}
	})
	t.Run("EVENT-006", func(t *testing.T) {
		if _, err := readEvents(t, false, started(1), finished(2), finished(3)); err == nil {
			t.Fatal("duplicate RunFinished accepted")
		}
	})
	t.Run("EVENT-007", func(t *testing.T) {
		if _, err := readEvents(t, false, started(1), finished(2), progress(3, nil)); err == nil {
			t.Fatal("event after RunFinished accepted")
		}
	})
	t.Run("EVENT-008", func(t *testing.T) {
		u1, u2 := "u1", "u2"
		events := []Event{started(1), unitStarted(2, u1), unitStarted(3, u2), progress(4, &u1), unitFinished(5, u1), unitFinished(6, u2), finished(7)}
		if _, err := readEvents(t, false, events...); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("EVENT-009", func(t *testing.T) {
		applied := baseEvent(2, ChangeApplied, map[string]any{"changeId": "c1", "action": "create", "resourceRef": "repo", "resultRef": "result"})
		if _, err := readEvents(t, true, started(1), applied); err == nil {
			t.Fatal("unplanned change accepted")
		}
	})
	t.Run("EVENT-010", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "event", "partial.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		data = bytes.Replace(data, []byte(`"payload"`), []byte(`"attempt":1,"payload"`), 1)
		data = bytes.TrimSuffix(data, []byte{'\n'})
		stream, err := ReadJSONL(bytes.NewReader(data), false)
		if err != nil || len(stream.Events) != 1 || stream.Complete {
			t.Fatalf("stream = %#v, err = %v", stream, err)
		}
	})
	t.Run("EVENT-011", func(t *testing.T) {
		stream, err := readEvents(t, false, started(1), progress(2, nil))
		if err != nil || stream.Complete {
			t.Fatalf("stream = %#v, err = %v", stream, err)
		}
	})
	t.Run("EVENT-012", func(t *testing.T) {
		r := redact.New()
		event := progress(0, nil)
		event.Payload["authorization"] = "Bearer token-value"
		clean, err := sanitizeEvent(event, r)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(clean.Payload["authorization"].(string)), "token") {
			t.Fatalf("authorization leaked: %#v", clean.Payload)
		}
	})
	t.Run("EVENT-013", func(t *testing.T) {
		sink := newBlockingSink()
		bus, err := NewBus("run-1", redact.New(), 1, sink)
		if err != nil {
			t.Fatal(err)
		}
		start, _ := New(RunStarted, RunStartedPayload{Command: "plan", ToolVersion: "0.1.0", ConfigDigest: "sha256:x"})
		if err := bus.Publish(context.Background(), start); err != nil {
			t.Fatal(err)
		}
		<-sink.started
		for completed := 1; completed <= 3; completed++ {
			tick, _ := New(ProgressTick, map[string]any{"completed": completed, "total": nil, "unit": "items"})
			if err := bus.Publish(context.Background(), tick); err != nil {
				t.Fatal(err)
			}
		}
		finish, _ := New(RunFinished, RunFinishedPayload{Outcome: "succeeded", Counts: map[string]int{}, ReportRef: "report.json"})
		published := make(chan error, 1)
		go func() { published <- bus.Publish(context.Background(), finish) }()
		close(sink.release)
		if err := <-published; err != nil {
			t.Fatal(err)
		}
		if err := bus.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		events := sink.snapshot()
		progressCount := 0
		for _, event := range events {
			if event.Type == ProgressTick {
				progressCount++
			}
		}
		if progressCount != 2 {
			t.Fatalf("progress event count = %d, want 2 after coalescing; events = %#v", progressCount, events)
		}
	})
	t.Run("EVENT-014", func(t *testing.T) {
		sink := newBlockingSink()
		bus, err := NewBus("run-1", redact.New(), 1, sink)
		if err != nil {
			t.Fatal(err)
		}
		start, _ := New(RunStarted, RunStartedPayload{Command: "plan", ToolVersion: "0.1.0", ConfigDigest: "sha256:x"})
		if err := bus.Publish(context.Background(), start); err != nil {
			t.Fatal(err)
		}
		<-sink.started
		first, _ := New(DiagnosticEmitted, map[string]any{"diagnostic": map[string]any{"code": "TRL-INT-0001"}})
		second, _ := New(DiagnosticEmitted, map[string]any{"diagnostic": map[string]any{"code": "TRL-INT-0002"}})
		if err := bus.Publish(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		published := make(chan error, 1)
		go func() { published <- bus.Publish(context.Background(), second) }()
		close(sink.release)
		if err := <-published; err != nil {
			t.Fatal(err)
		}
		if err := bus.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, event := range sink.snapshot() {
			if event.Type == DiagnosticEmitted {
				count++
			}
		}
		if count != 2 {
			t.Fatalf("diagnostic event count = %d, want 2", count)
		}
	})
	t.Run("EVENT-015", func(t *testing.T) {
		event := started(1)
		event.Version = 2
		if _, err := readEvents(t, false, event); err == nil {
			t.Fatal("version 2 accepted")
		}
	})
	t.Run("EVENT-016", func(t *testing.T) {
		event := baseEvent(0, ChangeApplied, map[string]any{"changeId": "c", "action": "create", "resourceRef": "https://example.test/r?q=token", "resultRef": "https://example.test/result?sig=secret"})
		clean, err := sanitizeEvent(event, redact.New())
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(clean.Payload)
		if strings.Contains(string(encoded), "?") {
			t.Fatalf("query survived: %s", encoded)
		}
	})
	t.Run("EVENT-017", func(t *testing.T) {
		event := started(1)
		event.Attempt = 0
		if _, err := readEvents(t, false, event); err == nil {
			t.Fatal("missing attempt accepted")
		}
	})
	t.Run("EVENT-018", func(t *testing.T) {
		phase := "future-phase"
		event := baseEvent(2, PhaseStarted, map[string]any{"phaseId": phase, "name": "Future", "ordinal": 1})
		event.Phase = &phase
		if _, err := readEvents(t, false, started(1), event); err == nil {
			t.Fatal("unknown phase accepted")
		}
	})
}

type blockingSink struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	events  []Event
}

func newBlockingSink() *blockingSink {
	return &blockingSink{started: make(chan struct{}), release: make(chan struct{})}
}

func (s *blockingSink) Handle(_ context.Context, event Event) error {
	s.once.Do(func() {
		close(s.started)
		<-s.release
	})
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	return nil
}

func (s *blockingSink) snapshot() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...)
}

func TestBusRedactsBeforeJSONL(t *testing.T) {
	var output bytes.Buffer
	r := redact.New()
	r.Add("canary-secret")
	bus, err := NewBus("run-1", r, 2, NewJSONLSink(&output))
	if err != nil {
		t.Fatal(err)
	}
	event, _ := New(RunStarted, RunStartedPayload{Command: "plan canary-secret", ToolVersion: "0.1.0", ConfigDigest: "sha256:safe"})
	if err := bus.Publish(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	finish, _ := New(RunFinished, RunFinishedPayload{Outcome: "succeeded", Counts: map[string]int{}, ReportRef: "report.json"})
	if err := bus.Publish(context.Background(), finish); err != nil {
		t.Fatal(err)
	}
	if err := bus.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "canary-secret") {
		t.Fatalf("secret leaked: %s", output.String())
	}
	stream, err := ReadJSONL(strings.NewReader(output.String()), false)
	if err != nil || !stream.Complete {
		t.Fatalf("stream = %#v, err = %v", stream, err)
	}
}

func readEvents(t *testing.T, planApply bool, events ...Event) (Stream, error) {
	t.Helper()
	var data bytes.Buffer
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		data.Write(encoded)
		data.WriteByte('\n')
	}
	return ReadJSONL(&data, planApply)
}

func baseEvent(sequence uint64, eventType Type, payload map[string]any) Event {
	return Event{Version: Version, Sequence: sequence, Time: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC), RunID: "run-1", Type: eventType, Attempt: 1, Payload: payload}
}

func started(sequence uint64) Event {
	return baseEvent(sequence, RunStarted, map[string]any{"command": "plan", "toolVersion": "0.1.0", "configDigest": "sha256:abc"})
}

func finished(sequence uint64) Event {
	return baseEvent(sequence, RunFinished, map[string]any{"outcome": "succeeded", "exitCode": 0, "counts": map[string]int{}, "reportRef": "report.json"})
}

func progress(sequence uint64, unit *string) Event {
	event := baseEvent(sequence, ProgressTick, map[string]any{"completed": 1, "total": nil, "unit": "items"})
	event.UnitKey = unit
	return event
}

func unitStarted(sequence uint64, unit string) Event {
	event := baseEvent(sequence, UnitStarted, map[string]any{"kind": "pipeline", "resourceRef": unit})
	event.UnitKey = &unit
	return event
}

func unitFinished(sequence uint64, unit string) Event {
	event := baseEvent(sequence, UnitFinished, map[string]any{"outcome": "succeeded", "counts": map[string]int{}, "fidelity": "exact", "provenance": "deterministic"})
	event.UnitKey = &unit
	return event
}
