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
	// ExitBlocked means manual work, a secret, or an identity decision blocks completion.
	ExitBlocked = 3
	// ExitStale means apply was refused before mutation because plan preconditions changed.
	ExitStale = 4
	// ExitPolicy means a --fail-on policy threshold was exceeded.
	ExitPolicy = 5
	// ExitDrift is retained as a compatibility alias for ExitStale.
	ExitDrift = ExitStale
	// ExitUsage means the command line was malformed (BSD EX_USAGE).
	ExitUsage = 64
	// ExitInterrupted means the user cancelled the run.
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
		if validExitCode(ee.Code) {
			return ee.Code
		}
		return ExitError
	}
	return ExitError
}

func validExitCode(code int) bool {
	switch code {
	case ExitOK, ExitError, ExitPartial, ExitBlocked, ExitStale, ExitPolicy, ExitUsage, ExitInterrupted:
		return true
	default:
		return false
	}
}

// OutcomeSummary contains the terminal facts needed by the normative exit-code
// precedence. Counts are per requested unit or durable change.
type OutcomeSummary struct {
	InvalidInput        bool
	StaleBeforeMutation bool
	MutationOccurred    bool
	Succeeded           int
	Failed              int
	FailureSkipped      int
	ManualBlocked       int
	Cancelled           bool
	RecoveredPanic      bool
	DriftAfterMutation  bool
	PolicyThreshold     bool
	Command             string
}

// ResolveExitCode applies docs/spec/exit-codes.md precedence.
func ResolveExitCode(summary OutcomeSummary) int {
	if summary.InvalidInput {
		return ExitUsage
	}
	if summary.Cancelled {
		return ExitInterrupted
	}
	if summary.StaleBeforeMutation && !summary.MutationOccurred {
		return ExitStale
	}
	hasError := summary.Failed > 0 || summary.FailureSkipped > 0 ||
		summary.RecoveredPanic || summary.DriftAfterMutation
	if summary.StaleBeforeMutation && summary.MutationOccurred {
		hasError = true
	}
	if hasError {
		if summary.Succeeded > 0 {
			return ExitPartial
		}
		return ExitError
	}
	if summary.PolicyThreshold {
		return ExitPolicy
	}
	if summary.ManualBlocked > 0 && (summary.Command == "plan" || summary.Command == "apply") {
		return ExitBlocked
	}
	return ExitOK
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
