package cli

import (
	"errors"
	"fmt"
	"strings"
)

// Process exit codes.
const (
	// ExitOK means the command succeeded.
	ExitOK = 0
	// ExitError is a generic failure.
	ExitError = 1
	// ExitPartial means the command completed only in part.
	ExitPartial = 2
	// ExitBlocked means the command was blocked by a precondition or policy.
	ExitBlocked = 3
	// ExitDrift means drift was detected between expected and actual state.
	ExitDrift = 4
	// ExitUsage means the command line was malformed (BSD EX_USAGE).
	ExitUsage = 64
	// ExitInterrupted is used when a second interrupt forces termination.
	ExitInterrupted = 130
)

// CodedError carries a process exit code alongside the underlying error.
type CodedError struct {
	// Code is the process exit code.
	Code int
	// Err is the cause; it may be nil for codes that need no message.
	Err error
}

// Error implements the error interface.
func (e *CodedError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CodedError) Unwrap() error { return e.Err }

// WithExitCode wraps err so that it maps to the given exit code.
func WithExitCode(code int, err error) error {
	return &CodedError{Code: code, Err: err}
}

// NotImplementedError is returned by commands that are registered but not
// built yet. It maps to exit code 1.
type NotImplementedError struct {
	// Command is the full command path, e.g. "trasloco audit".
	Command string
}

// Error implements the error interface.
func (e *NotImplementedError) Error() string {
	return e.Command + ": not implemented yet (trasloco is pre-alpha)"
}

// CodeFor maps an error returned by command execution to an exit code.
func CodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	var ee *CodedError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return ExitError
}

// usageError wraps err as a usage error (exit code 64).
func usageError(err error) error {
	return WithExitCode(ExitUsage, err)
}

// isCobraUsage reports whether err is one of cobra's untyped usage errors.
func isCobraUsage(err error) bool {
	msg := err.Error()
	for _, p := range []string{"unknown command", "unknown flag", "unknown shorthand flag", "invalid argument", "flag needs an argument", "required flag"} {
		if strings.HasPrefix(msg, p) {
			return true
		}
	}
	return false
}
