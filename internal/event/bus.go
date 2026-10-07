package event

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"

	"github.com/HarnageaGabriel/trasloco/internal/clock"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

const maxPendingKeys = 1024

// ErrRedaction means an event could not be proven safe for output.
var ErrRedaction = &diag.Error{Finding: diag.MustNew("TRL-SECRET-0004")}

// Sink consumes serialized, sequenced, redacted events.
type Sink interface {
	Handle(context.Context, Event) error
}

// FailClosed marks a sink whose failure must abort the run.
type FailClosed interface {
	Sink
	FailClosed()
}

type failClosedSink struct{ Sink }

func (failClosedSink) FailClosed() {}

func (s failClosedSink) Close() error {
	if closer, ok := s.Sink.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// FailClosedSink marks sink as required and forwards Close when supported.
func FailClosedSink(sink Sink) Sink {
	if sink == nil {
		return nil
	}
	return failClosedSink{Sink: sink}
}

// Options configures a Bus.
type Options struct {
	RunID       string
	Redactor    *redact.Redactor
	Buffer      int
	Clock       clock.Clock
	Diagnostics *diag.Collector
}

type queueItem struct {
	ctx   context.Context
	event Event
	close chan error
}

// Bus fans events out at one serialization boundary.
type Bus struct {
	clock       clock.Clock
	runID       string
	redactor    *redact.Redactor
	diagnostics *diag.Collector
	sinks       []Sink
	queue       chan queueItem

	gate   sync.RWMutex
	closed bool
	errMu  sync.Mutex
	err    error

	enqueueMu   sync.Mutex
	pending     map[string]queueItem
	pendingKeys []string
	done        chan struct{}
}

// NewBus starts a bus. Buffer must be positive.
func NewBus(runID string, redactor *redact.Redactor, buffer int, sinks ...Sink) (*Bus, error) {
	return NewBusWithOptions(Options{RunID: runID, Redactor: redactor, Buffer: buffer}, sinks...)
}

// NewBusWithClock starts a bus using the invocation clock.
func NewBusWithClock(runID string, redactor *redact.Redactor, buffer int, c clock.Clock, sinks ...Sink) (*Bus, error) {
	return NewBusWithOptions(Options{RunID: runID, Redactor: redactor, Buffer: buffer, Clock: c}, sinks...)
}

// NewBusWithOptions starts a bus with explicit options.
func NewBusWithOptions(options Options, sinks ...Sink) (*Bus, error) {
	if options.RunID == "" {
		return nil, fmt.Errorf("run ID is required")
	}
	if options.Buffer <= 0 {
		return nil, fmt.Errorf("event buffer must be positive")
	}
	for index, sink := range sinks {
		if sink == nil {
			return nil, fmt.Errorf("event sink %d is nil", index)
		}
	}
	if options.Clock == nil {
		options.Clock = clock.Real{}
	}
	if options.Redactor == nil {
		options.Redactor = redact.New()
	}
	b := &Bus{
		clock: options.Clock, runID: options.RunID, redactor: options.Redactor,
		diagnostics: options.Diagnostics, sinks: append([]Sink(nil), sinks...),
		queue: make(chan queueItem, options.Buffer), pending: make(map[string]queueItem), done: make(chan struct{}),
	}
	go b.run()
	return b, nil
}

// Publish redacts and queues an event. Semantic events wait for buffer space
// until ctx is cancelled; coalescible events replace an equivalent pending one.
func (b *Bus) Publish(ctx context.Context, event Event) error {
	b.gate.RLock()
	defer b.gate.RUnlock()
	if b.closed {
		return io.ErrClosedPipe
	}
	if err := b.currentError(); err != nil {
		return err
	}
	event.Version = Version
	event.Sequence = 0
	event.RunID = b.runID
	if event.Time.IsZero() {
		event.Time = b.clock.Now().UTC()
	} else {
		event.Time = event.Time.UTC()
	}
	clean, err := sanitizeEvent(event, b.redactor)
	if err != nil {
		return err
	}
	item := queueItem{ctx: ctx, event: clean}
	b.enqueueMu.Lock()
	defer b.enqueueMu.Unlock()
	if key, ok := coalescingKey(clean); ok {
		if len(b.pending) > 0 {
			if _, exists := b.pending[key]; !exists {
				b.pendingKeys = append(b.pendingKeys, key)
			}
			b.pending[key] = item
			if len(b.pending) >= maxPendingKeys {
				return b.flushPendingToQueue(ctx)
			}
			return nil
		}
		select {
		case b.queue <- item:
			return nil
		default:
			b.pending[key] = item
			b.pendingKeys = append(b.pendingKeys, key)
			return nil
		}
	}
	if err := b.flushPendingToQueue(ctx); err != nil {
		return err
	}
	select {
	case b.queue <- item:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close flushes queued and coalesced events and closes closeable sinks.
func (b *Bus) Close(ctx context.Context) error {
	b.gate.Lock()
	if b.closed {
		b.gate.Unlock()
		select {
		case <-b.done:
			return b.currentError()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	b.closed = true
	done := make(chan error, 1)
	b.enqueueMu.Lock()
	if err := b.flushPendingToQueue(ctx); err != nil {
		b.closed = false
		b.enqueueMu.Unlock()
		b.gate.Unlock()
		return err
	}
	select {
	case b.queue <- queueItem{ctx: ctx, close: done}:
		b.enqueueMu.Unlock()
		b.gate.Unlock()
	case <-ctx.Done():
		b.closed = false
		b.enqueueMu.Unlock()
		b.gate.Unlock()
		return ctx.Err()
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *Bus) run() {
	defer close(b.done)
	var sequence uint64
	validator := newValidator(false)
	for item := range b.queue {
		if item.close != nil {
			b.closeSinks()
			item.close <- b.currentError()
			return
		}
		sequence++
		item.event.Sequence = sequence
		if item.event.Type == RunStarted {
			var payload RunStartedPayload
			if item.event.DecodePayload(&payload) == nil && payload.Command == "apply" {
				validator.planApply = true
			}
		}
		if err := validator.add(item.event); err != nil {
			b.setError(fmt.Errorf("invalid event stream: %w", err))
			continue
		}
		sequence = b.deliver(item.ctx, item.event, sequence, validator)
	}
}

func (b *Bus) flushPendingToQueue(ctx context.Context) error {
	for len(b.pendingKeys) > 0 {
		key := b.pendingKeys[0]
		item := b.pending[key]
		select {
		case b.queue <- item:
			delete(b.pending, key)
			b.pendingKeys = b.pendingKeys[1:]
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (b *Bus) deliver(ctx context.Context, event Event, sequence uint64, validator *validator) uint64 {
	var diagnostics []diag.Diagnostic
	for index := 0; index < len(b.sinks); {
		sink := b.sinks[index]
		if err := sink.Handle(ctx, event); err != nil {
			if _, required := sink.(FailClosed); required {
				b.setError(fmt.Errorf("event sink: %w", err))
				return sequence
			}
			b.sinks = append(b.sinks[:index], b.sinks[index+1:]...)
			diagnostic := b.sinkDiagnostic(sink, err)
			b.reportDiagnostic(diagnostic)
			diagnostics = append(diagnostics, diagnostic)
			continue
		}
		index++
	}
	if event.Type == RunFinished {
		return sequence
	}
	for _, diagnostic := range diagnostics {
		sequence++
		diagnosticEvent, err := New(DiagnosticEmitted, DiagnosticEmittedPayload{Diagnostic: diagnostic})
		if err != nil {
			b.setError(err)
			return sequence
		}
		diagnosticEvent.RunID = b.runID
		diagnosticEvent.Time = b.clock.Now().UTC()
		diagnosticEvent.Sequence = sequence
		clean, err := sanitizeEvent(diagnosticEvent, b.redactor)
		if err != nil {
			b.setError(err)
			return sequence
		}
		clean.Sequence = sequence
		if err := validator.add(clean); err != nil {
			b.setError(fmt.Errorf("invalid event stream: %w", err))
			return sequence
		}
		sequence = b.deliver(ctx, clean, sequence, validator)
	}
	return sequence
}

func (b *Bus) closeSinks() {
	for _, sink := range b.sinks {
		closer, ok := sink.(io.Closer)
		if !ok {
			continue
		}
		if err := closer.Close(); err != nil {
			if _, required := sink.(FailClosed); required {
				b.setError(fmt.Errorf("event sink: %w", err))
			} else {
				b.reportDiagnostic(b.sinkDiagnostic(sink, err))
			}
		}
	}
}

func (b *Bus) sinkDiagnostic(sink Sink, err error) diag.Diagnostic {
	message := b.redactor.Redact(err.Error())
	return diag.MustNew("TRL-INT-0004",
		diag.WithMessage("Event sink failed and was detached: "+message),
		diag.WithParams(map[string]any{"sink": fmt.Sprintf("%T", sink), "error": message}),
	)
}

func (b *Bus) reportDiagnostic(diagnostic diag.Diagnostic) {
	if b.diagnostics != nil {
		b.diagnostics.Add(diagnostic)
	}
}

func (b *Bus) setError(err error) {
	b.errMu.Lock()
	if b.err == nil {
		b.err = err
	}
	b.errMu.Unlock()
}

func (b *Bus) currentError() error {
	b.errMu.Lock()
	defer b.errMu.Unlock()
	return b.err
}

func coalescingKey(event Event) (string, bool) {
	switch event.Type {
	case ProgressTick:
		return string(event.Type) + nullable(event.UnitKey), true
	case CacheHit:
		var payload CacheHitPayload
		if event.DecodePayload(&payload) != nil {
			return "", false
		}
		return string(event.Type) + payload.CacheKind + ":" + payload.KeyDigest, true
	case RequestThrottled:
		var payload RequestThrottledPayload
		if event.DecodePayload(&payload) != nil {
			return "", false
		}
		return string(event.Type) + payload.Platform + ":" + payload.EndpointClass, true
	default:
		return "", false
	}
}

func nullable(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sanitizeEvent(event Event, r *redact.Redactor) (Event, error) {
	if r == nil {
		r = redact.New()
	}
	decoder := json.NewDecoder(bytes.NewReader(event.Payload))
	decoder.UseNumber()
	var payload bytes.Buffer
	if err := sanitizeJSONValue(decoder, &payload, "", r, true); err != nil {
		return Event{}, ErrRedaction
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		return Event{}, ErrRedaction
	}
	event.Payload = append(json.RawMessage(nil), payload.Bytes()...)
	event.RunID = r.Redact(event.RunID)
	event.Phase = sanitizePointer(event.Phase, r)
	event.UnitKey = sanitizePointer(event.UnitKey, r)
	event.OperationID = sanitizePointer(event.OperationID, r)
	encoded, err := json.Marshal(event)
	if err != nil {
		return Event{}, fmt.Errorf("marshal event for redaction: %w", err)
	}
	if r.Contains(string(encoded)) {
		return Event{}, ErrRedaction
	}
	return event, nil
}

func sanitizeJSONValue(decoder *json.Decoder, output *bytes.Buffer, key string, r *redact.Redactor, root bool) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); ok {
		if root && delimiter != '{' {
			return fmt.Errorf("payload must be an object")
		}
		switch delimiter {
		case '{':
			output.WriteByte('{')
			first := true
			for decoder.More() {
				keyToken, tokenErr := decoder.Token()
				if tokenErr != nil {
					return tokenErr
				}
				childKey, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if !first {
					output.WriteByte(',')
				}
				first = false
				writeJSONScalar(output, childKey)
				output.WriteByte(':')
				if err := sanitizeJSONValue(decoder, output, childKey, r, false); err != nil {
					return err
				}
			}
			end, endErr := decoder.Token()
			if endErr != nil || end != json.Delim('}') {
				return fmt.Errorf("unterminated object")
			}
			output.WriteByte('}')
		case '[':
			output.WriteByte('[')
			first := true
			for decoder.More() {
				if !first {
					output.WriteByte(',')
				}
				first = false
				if err := sanitizeJSONValue(decoder, output, key, r, false); err != nil {
					return err
				}
			}
			end, endErr := decoder.Token()
			if endErr != nil || end != json.Delim(']') {
				return fmt.Errorf("unterminated array")
			}
			output.WriteByte(']')
		default:
			return fmt.Errorf("unexpected delimiter")
		}
		return nil
	}
	if root {
		return fmt.Errorf("payload must be an object")
	}
	if value, ok := token.(string); ok {
		writeJSONScalar(output, sanitizeString(value, key, r))
		return nil
	}
	writeJSONScalar(output, token)
	return nil
}

func writeJSONScalar(output *bytes.Buffer, value any) {
	encoded, _ := json.Marshal(value)
	output.Write(encoded)
}

func sanitizeString(value, key string, r *redact.Redactor) string {
	lower := strings.ToLower(key)
	if strings.Contains(lower, "authorization") || strings.Contains(lower, "cookie") ||
		strings.Contains(lower, "secret") || strings.Contains(lower, "password") ||
		lower == "token" || lower == "email" || lower == "displayname" || lower == "body" {
		return redact.Replacement()
	}
	clean := r.Redact(value)
	if strings.HasSuffix(lower, "url") || strings.HasSuffix(lower, "ref") {
		if parsed, err := url.Parse(clean); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
			parsed.User = nil
			parsed.RawQuery = ""
			parsed.ForceQuery = false
			parsed.Fragment = ""
			clean = parsed.String()
		}
	}
	return clean
}

func sanitizePointer(value *string, r *redact.Redactor) *string {
	if value == nil {
		return nil
	}
	clean := r.Redact(*value)
	return &clean
}
