package clock

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReal(t *testing.T) {
	c := Real{}
	if c.Now().IsZero() {
		t.Fatal("zero clock")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(c.Sleep(ctx, time.Hour), context.Canceled) {
		t.Fatal("cancellation lost")
	}
	if err := c.Sleep(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Sleep(context.Background(), time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	timer := c.NewTimer(time.Hour)
	if !timer.Stop() {
		t.Fatal("fresh timer not stopped")
	}
	ctx, cancel = context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() { close(started); done <- c.Sleep(ctx, time.Hour) }()
	<-started
	cancel()
	if !errors.Is(<-done, context.Canceled) {
		t.Fatal("waiting cancellation lost")
	}
}
