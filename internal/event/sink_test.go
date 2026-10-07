package event

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

type errorWriter struct{ calls int }

func (w *errorWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls > 1 {
		return 0, errors.New("write failed")
	}
	return len(p), nil
}

func TestRenderSinks(t *testing.T) {
	event := finished(1)
	var jsonl bytes.Buffer
	if err := NewJSONLSink(&jsonl).Handle(context.Background(), event); err != nil || !strings.HasSuffix(jsonl.String(), "\n") {
		t.Fatalf("jsonl=%q err=%v", jsonl.String(), err)
	}
	var human bytes.Buffer
	if err := NewHumanSink(&human).Handle(context.Background(), event); err != nil || !strings.Contains(human.String(), "succeeded") {
		t.Fatalf("human=%q err=%v", human.String(), err)
	}
	if err := NewHumanSink(&human).Handle(context.Background(), baseEvent(2, CacheHit, CacheHitPayload{})); err != nil {
		t.Fatal(err)
	}
	writer := &errorWriter{}
	if err := NewJSONLSink(writer).Handle(context.Background(), event); err == nil {
		t.Fatal("newline write error lost")
	}
}
