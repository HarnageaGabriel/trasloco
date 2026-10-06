package event

import (
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

// ErrRedaction means an event could not be proven safe for output.
var ErrRedaction = &diag.Error{Finding: diag.MustNew("TRL-SECRET-0004")}

// Sink consumes serialized, sequenced, redacted events.
type Sink interface {
	Handle(context.Context, Event) error
}

type queueItem struct {
	ctx   context.Context
	event Event
	close chan error
}

// Bus fans events out at one serialization boundary.
type Bus struct {
	clock    clock.Clock
	runID    string
	redactor *redact.Redactor
	sinks    []Sink
	queue    chan queueItem

	gate   sync.RWMutex
	closed bool
	errMu  sync.Mutex
	err    error

	enqueueMu sync.Mutex
	pendingMu sync.Mutex
	pending   map[string]queueItem
	done      chan struct{}
}

// NewBus starts a bus. Buffer must be positive.
func NewBus(runID string, redactor *redact.Redactor, buffer int, sinks ...Sink) (*Bus, error) {
	return NewBusWithClock(runID, redactor, buffer, clock.Real{}, sinks...)
}

// NewBusWithClock starts a bus using the invocation clock.
func NewBusWithClock(runID string, redactor *redact.Redactor, buffer int, c clock.Clock, sinks ...Sink) (*Bus, error) {
	if c == nil {
		c = clock.Real{}
	}
	if runID == "" {
		return nil, fmt.Errorf("run ID is required")
	}
	if buffer <= 0 {
		return nil, fmt.Errorf("event buffer must be positive")
	}
	if redactor == nil {
		redactor = redact.New()
	}
	b := &Bus{
		clock: c, runID: runID, redactor: redactor, sinks: append([]Sink(nil), sinks...),
		queue: make(chan queueItem, buffer), pending: make(map[string]queueItem), done: make(chan struct{}),
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
		b.pendingMu.Lock()
		if len(b.pending) > 0 {
			b.pending[key] = item
			b.pendingMu.Unlock()
			return nil
		}
		b.pendingMu.Unlock()
		select {
		case b.queue <- item:
			return nil
		default:
			b.pendingMu.Lock()
			b.pending[key] = item
			b.pendingMu.Unlock()
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
			for _, sink := range b.sinks {
				if closer, ok := sink.(io.Closer); ok {
					if err := closer.Close(); err != nil {
						b.setError(err)
					}
				}
			}
			item.close <- b.currentError()
			return
		}
		sequence++
		item.event.Sequence = sequence
		if item.event.Type == RunStarted && item.event.Payload["command"] == "apply" {
			validator.planApply = true
		}
		if err := validator.add(item.event); err != nil {
			b.setError(fmt.Errorf("invalid event stream: %w", err))
			continue
		}
		b.deliver(item.ctx, item.event)
	}
}

func (b *Bus) flushPendingToQueue(ctx context.Context) error {
	type pendingItem struct {
		key  string
		item queueItem
	}
	b.pendingMu.Lock()
	items := make([]pendingItem, 0, len(b.pending))
	for key, item := range b.pending {
		items = append(items, pendingItem{key: key, item: item})
	}
	b.pendingMu.Unlock()
	for _, pending := range items {
		select {
		case b.queue <- pending.item:
			b.pendingMu.Lock()
			delete(b.pending, pending.key)
			b.pendingMu.Unlock()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (b *Bus) deliver(ctx context.Context, event Event) {
	if b.currentError() != nil {
		return
	}
	for _, sink := range b.sinks {
		if err := sink.Handle(ctx, event); err != nil {
			b.setError(fmt.Errorf("event sink: %w", err))
			return
		}
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
		return string(event.Type) + fmt.Sprint(event.Payload["cacheKind"], ":", event.Payload["keyDigest"]), true
	case RequestThrottled:
		return string(event.Type) + fmt.Sprint(event.Payload["platform"], ":", event.Payload["endpointClass"]), true
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
	encodedPayload, err := json.Marshal(event.Payload)
	if err != nil {
		return Event{}, fmt.Errorf("marshal event payload for redaction: %w", err)
	}
	var normalized map[string]any
	if err := json.Unmarshal(encodedPayload, &normalized); err != nil {
		return Event{}, fmt.Errorf("normalize event payload for redaction: %w", err)
	}
	clean, ok := sanitizeValue(normalized, "", r).(map[string]any)
	if !ok {
		return Event{}, ErrRedaction
	}
	event.Payload = clean
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

func sanitizePointer(value *string, r *redact.Redactor) *string {
	if value == nil {
		return nil
	}
	clean := r.Redact(*value)
	return &clean
}

func sanitizeValue(value any, key string, r *redact.Redactor) any {
	switch typed := value.(type) {
	case string:
		lower := strings.ToLower(key)
		if strings.Contains(lower, "authorization") || strings.Contains(lower, "cookie") ||
			strings.Contains(lower, "secret") || strings.Contains(lower, "password") ||
			lower == "token" || lower == "email" || lower == "displayname" || lower == "body" {
			return redact.Replacement()
		}
		clean := r.Redact(typed)
		if parsed, err := url.Parse(clean); err == nil && (parsed.IsAbs() || strings.HasSuffix(lower, "ref") || strings.Contains(lower, "url")) {
			parsed.User = nil
			parsed.RawQuery = ""
			parsed.ForceQuery = false
			clean = parsed.String()
		}
		return clean
	case map[string]any:
		result := make(map[string]any, len(typed))
		for childKey, child := range typed {
			result[childKey] = sanitizeValue(child, childKey, r)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, child := range typed {
			result[i] = sanitizeValue(child, key, r)
		}
		return result
	default:
		return typed
	}
}
