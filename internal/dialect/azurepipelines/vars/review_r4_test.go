package vars

import (
	"strconv"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
)

func TestReviewR4CmdQuotedMetacharactersAreNotProven(t *testing.T) {
	for _, metachar := range []string{"&", "|", "<", ">", "^"} {
		t.Run(metachar, func(t *testing.T) {
			script := "steps:\n- script: |\n    echo \"##vso[task.setvariable variable=x]a" + metachar + "b\"\n- bash: echo consume\n"
			graph, collector := reviewGraphWith(t, script, Context{AgentOS: AgentOSWindows})
			candidate, ok := graph.Resolve(stepScope(1), "x", PhaseMacro)
			if !ok || !candidate.Unknown || candidate.Value != "" {
				t.Fatalf("x = %#v, %t; want unknown", candidate, ok)
			}
			if !hasCode(collector.List(), "TRL-VAR-0016") {
				t.Fatalf("diagnostics = %v, want TRL-VAR-0016", codes(collector.List()))
			}
		})
	}
}

func TestReviewR4CmdUnquotedMetacharacterIsUnknown(t *testing.T) {
	const source = "steps:\n- script: |\n    echo ##vso[task.setvariable variable=x]a&b\n- bash: echo consume\n"
	graph, collector := reviewGraphWith(t, source, Context{AgentOS: AgentOSWindows})
	candidate, ok := graph.Resolve(stepScope(1), "x", PhaseMacro)
	if !ok || !candidate.Unknown {
		t.Fatalf("x = %#v, %t; want unknown", candidate, ok)
	}
	if !hasCode(collector.List(), "TRL-VAR-0016") {
		t.Fatalf("diagnostics = %v, want TRL-VAR-0016", codes(collector.List()))
	}
}

func TestReviewR4TerminatingWordsAsArgumentsDoNotEndProvability(t *testing.T) {
	for _, line := range []string{"echo 'exit'", "echo 'return'", "echo 'exec'", "echo 'then exit'"} {
		t.Run(line, func(t *testing.T) {
			graph, collector, scope := bashLogging(t, line+"\necho \"##vso[task.setvariable variable=x]ok\"")
			assertValue(t, graph, scope, "x", PhaseMacro, "ok")
			if hasCode(collector.List(), "TRL-VAR-0016") {
				t.Fatalf("argument was treated as a command: %v", codes(collector.List()))
			}
		})
	}
}

func TestReviewR4RealExitStillEndsProvability(t *testing.T) {
	graph, collector, scope := bashLogging(t, "exit 0\necho \"##vso[task.setvariable variable=x]late\"")
	candidate, ok := graph.Resolve(scope, "x", PhaseMacro)
	if !ok || !candidate.Unknown {
		t.Fatalf("x = %#v, %t; want unknown", candidate, ok)
	}
	if !hasCode(collector.List(), "TRL-VAR-0016") {
		t.Fatalf("diagnostics = %v, want TRL-VAR-0016", codes(collector.List()))
	}
}

func TestReviewR4HyphenWarningCoversEveryCandidateSource(t *testing.T) {
	tests := []struct {
		name, source string
		context      Context
	}{
		{"UI plain", "steps:\n- bash: echo ok\n", Context{UIPlain: []PlainVariable{{Name: "ui-name", Value: "v"}}}},
		{"UI secret", "steps:\n- bash: echo ok\n", Context{UISecrets: []SecretVariable{{Name: "ui-secret", Provider: "ado", ID: "1"}}}},
		{"predefined", "steps:\n- bash: echo ok\n", Context{Predefined: []PlainVariable{{Name: "pre-name", Value: "v"}}}},
		{"queue", "steps:\n- bash: echo ok\n", Context{Queue: []QueueAssignment{{Name: "queue-name", Value: "v"}}}},
		{"group plain", "variables:\n- group: G\nsteps:\n- bash: echo ok\n", Context{Groups: []VariableGroup{{Name: "G", Authorized: true, OrderProven: true, Plain: []PlainVariable{{Name: "group-name", Value: "v"}}}}}},
		{"group secret", "variables:\n- group: G\nsteps:\n- bash: echo ok\n", Context{Groups: []VariableGroup{{Name: "G", Authorized: true, OrderProven: true, Secrets: []SecretVariable{{Name: "group-secret", Provider: "ado", ID: "1"}}}}}},
		{"logging", "steps:\n- bash: echo \"##vso[task.setvariable variable=logged-name]v\"\n- bash: echo ok\n", Context{}},
		{"matrix", "strategy:\n  matrix:\n    leg:\n      matrix-name: v\nsteps:\n- bash: echo ok\n", Context{}},
		{"YAML", "variables:\n  yaml-name: v\nsteps:\n- bash: echo ok\n", Context{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, collector := reviewGraphWith(t, tc.source, tc.context)
			if !hasCode(collector.List(), "TRL-VAR-0018") {
				t.Fatalf("diagnostics = %v, want TRL-VAR-0018", codes(collector.List()))
			}
		})
	}
}

func TestReviewR4SingleRootMacroExhaustsWorkBudget(t *testing.T) {
	const levels = 46
	var pairs []string
	for i := 0; i < levels-1; i++ {
		pairs = append(pairs, "l"+strconv.Itoa(i), "$(l"+strconv.Itoa(i+1)+")")
	}
	pairs = append(pairs, "l"+strconv.Itoa(levels-1), strings.Repeat("a", 200000))
	graph, scope := macroGraph(pairs...)
	expanded := graph.Expand(scope, "$(l0)")
	if len(expanded.Text) > expr.MaxEvaluationMemory {
		t.Fatalf("output over the bound: %d", len(expanded.Text))
	}
	if !hasOverflow(expanded) {
		t.Fatalf("budget exhaustion left no diagnostic: %v", codes(expanded.Diagnostics))
	}
}
