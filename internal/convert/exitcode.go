package convert

import (
	"fmt"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

// Exit-code aggregation of the convert report. The function computes the
// terminal class of the run as pure logic following docs/spec/exit-codes.md;
// the CLI maps the returned code to the process exit status.

// FailOn is the --fail-on threshold of the convert command
// (docs/spec/exit-codes.md: only manual and warning thresholds exist).
type FailOn string

const (
	// FailOnNone is the default: placeholder warnings never fail the run.
	FailOnNone FailOn = ""
	// FailOnWarning fails the run when a warning, an approximated node or a
	// manual node was reported.
	FailOnWarning FailOn = "warning"
	// FailOnManual fails the run when a manual (needs-manual or unsupported)
	// node was reported.
	FailOnManual FailOn = "manual"
)

// ParseFailOn parses the --fail-on flag value. The empty string is the
// default (no threshold). docs/spec/exit-codes.md lists only manual and
// warning; the plan's never and error values are rejected as the spec wins.
func ParseFailOn(value string) (FailOn, error) {
	switch FailOn(value) {
	case FailOnNone, FailOnWarning, FailOnManual:
		return FailOn(value), nil
	default:
		return FailOnNone, fmt.Errorf("invalid --fail-on value %q: want manual or warning", value)
	}
}

// Policy is the caller-supplied exit aggregation policy. The zero value is
// the documented default: lenient (no strict suppression) and no fail-on
// threshold.
type Policy struct {
	// Strict counts suppressed and file-less outputs as failed units
	// (docs/install.md: a failed conversion exits 1 under --strict).
	Strict bool
	// FailOn is the threshold to enforce.
	FailOn FailOn
}

// ExitCode returns the convert exit class of the report under the policy
// (docs/spec/exit-codes.md "Precedence"). Convert takes a single input, so
// exit 2 is never returned: an error-severity diagnostic (from any stage) or
// a unit failure under --strict yields 1; an exceeded --fail-on threshold
// yields 5 without changing stored severities; every other run, including
// one with manual placeholders, yields 0. With no fail-on, a blocked no-file
// result without --strict also exits 0 (EXIT-021).
func ExitCode(report Report, policy Policy) int {
	if severityCount(report, diag.SeverityError)+unitFailures(report, policy) > 0 {
		return ExitError
	}
	switch policy.FailOn {
	case FailOnManual:
		if report.Totals.Manual > 0 {
			return ExitPolicy
		}
	case FailOnWarning:
		if severityCount(report, diag.SeverityWarning) > 0 || report.Totals.Approximated > 0 || report.Totals.Manual > 0 {
			return ExitPolicy
		}
	}
	return ExitOK
}

// unitFailures counts the units the policy fails: a suppressed output and a
// file-less result are failed units only under --strict; under the default
// they remain manual findings (EXIT-021).
func unitFailures(report Report, policy Policy) int {
	failures := 0
	for _, file := range report.Files {
		if policy.Strict && (file.NoFile || file.Strict) {
			failures++
		}
	}
	return failures
}

func severityCount(report Report, severity diag.Severity) int {
	count := 0
	for _, summary := range report.Diagnostics {
		if summary.Severity == severity {
			count += summary.Count
		}
	}
	return count
}
