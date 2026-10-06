package event

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

type failingSink struct{}

func (failingSink) Handle(context.Context, Event) error { return errors.New("boom") }

func TestHumanSink(t *testing.T) {
	var out bytes.Buffer
	sink := NewHumanSink(&out)
	e := baseEvent(1, RunFinished, map[string]any{"outcome": "ok"})
	if err := sink.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := sink.Handle(context.Background(), baseEvent(2, RunStarted, nil)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ok") {
		t.Fatalf("output %q", out.String())
	}
}

func TestBusSinkFailureSurfacesOnClose(t *testing.T) {
	bus, err := NewBus("run-1", redact.New(), 2, failingSink{})
	if err != nil {
		t.Fatal(err)
	}
	ev, _ := New(RunStarted, RunStartedPayload{Command: "plan", ToolVersion: "0.1.0", ConfigDigest: "sha256:x"})
	_ = bus.Publish(context.Background(), ev)
	fin, _ := New(RunFinished, RunFinishedPayload{Outcome: "succeeded", Counts: map[string]int{}, ReportRef: "r"})
	_ = bus.Publish(context.Background(), fin)
	if err := bus.Close(context.Background()); err == nil {
		t.Fatal("sink error lost")
	}
	if err := bus.Close(context.Background()); err == nil {
		t.Fatal("second close must return the same error")
	}
}
