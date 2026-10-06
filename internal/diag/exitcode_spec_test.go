package diag_test

import (
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/cli"
)

func TestExitCodeVectors(t *testing.T) {
	tests := []struct {
		id      string
		summary cli.OutcomeSummary
		want    int
	}{
		{"EXIT-001", cli.OutcomeSummary{Succeeded: 2}, cli.ExitOK},
		{"EXIT-002", cli.OutcomeSummary{Succeeded: 1}, cli.ExitOK},
		{"EXIT-003", cli.OutcomeSummary{Failed: 1}, cli.ExitError},
		{"EXIT-004", cli.OutcomeSummary{RecoveredPanic: true}, cli.ExitError},
		{"EXIT-005", cli.OutcomeSummary{Succeeded: 1, Failed: 1}, cli.ExitPartial},
		{"EXIT-006", cli.OutcomeSummary{Succeeded: 1, FailureSkipped: 1}, cli.ExitPartial},
		{"EXIT-007", cli.OutcomeSummary{Command: "plan", Succeeded: 1, ManualBlocked: 1}, cli.ExitBlocked},
		{"EXIT-008", cli.OutcomeSummary{Command: "apply", ManualBlocked: 1}, cli.ExitBlocked},
		{"EXIT-009", cli.OutcomeSummary{StaleBeforeMutation: true}, cli.ExitStale},
		{"EXIT-010", cli.OutcomeSummary{StaleBeforeMutation: true}, cli.ExitStale},
		{"EXIT-011", cli.OutcomeSummary{InvalidInput: true}, cli.ExitUsage},
		{"EXIT-012", cli.OutcomeSummary{InvalidInput: true}, cli.ExitUsage},
		{"EXIT-013", cli.OutcomeSummary{Failed: 1}, cli.ExitError},
		{"EXIT-014", cli.OutcomeSummary{Succeeded: 1, Failed: 1}, cli.ExitPartial},
		{"EXIT-015", cli.OutcomeSummary{Command: "audit", ManualBlocked: 1}, cli.ExitOK},
		{"EXIT-016", cli.OutcomeSummary{Command: "audit", PolicyThreshold: true}, cli.ExitPolicy},
		{"EXIT-017", cli.OutcomeSummary{Succeeded: 1}, cli.ExitOK},
		{"EXIT-018", cli.OutcomeSummary{Succeeded: 1, Cancelled: true}, cli.ExitInterrupted},
		{"EXIT-019", cli.OutcomeSummary{Cancelled: true}, cli.ExitInterrupted},
		{"EXIT-020", cli.OutcomeSummary{MutationOccurred: true, DriftAfterMutation: true}, cli.ExitError},
		{"EXIT-021", cli.OutcomeSummary{Command: "convert", ManualBlocked: 1}, cli.ExitOK},
		{"EXIT-022", cli.OutcomeSummary{Command: "modernize", ManualBlocked: 1, PolicyThreshold: true}, cli.ExitPolicy},
	}
	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			if got := cli.ResolveExitCode(test.summary); got != test.want {
				t.Fatalf("ResolveExitCode() = %d, want %d", got, test.want)
			}
		})
	}
}
