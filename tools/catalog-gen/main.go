package main

import (
	"context"
	"errors"
	"fmt"
	"os"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "catalog-gen:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: catalog-gen tasks|retirement|actions [flags]")
	}
	switch args[0] {
	case "tasks":
		return runTasks(ctx, args[1:])
	case "retirement":
		return runRetirement(ctx, args[1:])
	case "actions":
		return runActions(ctx, args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}
