// Command trasloco migrates CI/CD pipelines and DevOps resources between platforms.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/HarnageaGabriel/trasloco/internal/cli"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Restore default signal handling after cancellation so a second signal exits.
	go func() { <-ctx.Done(); stop() }()

	r := redact.New()
	return guarded(os.Stderr, r, func() int {
		return cli.Execute(ctx, os.Args[1:], cli.IO{Out: os.Stdout, Err: os.Stderr, Getenv: os.LookupEnv, Redactor: r})
	})
}

// guarded keeps panic material behind the same run-scoped redaction boundary.
func guarded(stderr io.Writer, r *redact.Redactor, execute func() int) (code int) {
	defer func() {
		if value := recover(); value != nil {
			_, _ = fmt.Fprintln(stderr, "trasloco: panic:", r.Redact(fmt.Sprint(value)))
			code = cli.ExitError
		}
	}()
	return execute()
}
