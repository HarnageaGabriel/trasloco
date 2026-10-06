package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

func TestCancellationAndRedaction(t *testing.T) {
	var dst bytes.Buffer
	r := redact.New()
	r.Add("cli-secret")
	streams := IO{Out: &dst, Err: &dst, Redactor: r}
	if got := Execute(context.Background(), []string{"--output", "cli-secret", "init"}, streams); got != ExitUsage {
		t.Fatal(got)
	}
	if strings.Contains(dst.String(), "cli-secret") {
		t.Fatal("stderr leaked secret")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Execute(ctx, []string{"init"}, streams); got != ExitInterrupted {
		t.Fatal(got)
	}
	if CodeFor(WithExitCode(ExitUsage, fmt.Errorf("cancel: %w", context.Canceled))) != ExitInterrupted {
		t.Fatal("wrapped cancellation lost")
	}
	if CodeFor(errors.New("failure")) != ExitError {
		t.Fatal("operational failure mapping")
	}
}
