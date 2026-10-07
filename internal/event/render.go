package event

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

// JSONLSink writes one compact JSON event and LF per Handle call.
type JSONLSink struct {
	mu sync.Mutex
	w  io.Writer
}

// NewJSONLSink creates a canonical JSONL renderer.
func NewJSONLSink(w io.Writer) *JSONLSink { return &JSONLSink{w: w} }

// Handle writes event in the canonical JSONL wire format.
func (s *JSONLSink) Handle(_ context.Context, event Event) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.w.Write(encoded); err != nil {
		return err
	}
	_, err = s.w.Write([]byte{'\n'})
	return err
}

// HumanSink writes concise, single-line event summaries.
type HumanSink struct {
	mu sync.Mutex
	w  io.Writer
}

// NewHumanSink creates a human text renderer.
func NewHumanSink(w io.Writer) *HumanSink { return &HumanSink{w: w} }

// Handle writes one concise human-readable line.
func (s *HumanSink) Handle(_ context.Context, event Event) error {
	detail := ""
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(event.Payload, &fields)
	for _, key := range []string{"outcome", "name", "action", "reason", "diagnosticCode"} {
		if raw, ok := fields[key]; ok {
			var value any
			if json.Unmarshal(raw, &value) == nil {
				detail = " " + strings.Join(strings.Fields(fmt.Sprint(value)), " ")
			}
			break
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := fmt.Fprintf(s.w, "%d %s%s\n", event.Sequence, event.Type, detail)
	return err
}
