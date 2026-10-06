// Command trasloco migrates CI/CD pipelines and DevOps resources between platforms.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/HarnageaGabriel/trasloco/internal/cli"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The first signal cancels ctx and lets the command wind down; a second
	// one terminates immediately.
	go func() {
		<-ctx.Done()
		second := make(chan os.Signal, 1)
		signal.Notify(second, os.Interrupt, syscall.SIGTERM)
		<-second
		os.Exit(cli.ExitInterrupted)
	}()

	return cli.Execute(ctx, os.Args[1:], cli.IO{Out: os.Stdout, Err: os.Stderr, Getenv: os.LookupEnv})
}
