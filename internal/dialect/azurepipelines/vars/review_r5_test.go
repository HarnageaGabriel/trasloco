package vars

import (
	"errors"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
)

func TestReviewR5CmdLiteralsUseCmdExpansionRules(t *testing.T) {
	const source = `steps:
- script: |
    echo ##vso[task.setvariable variable=dir]C:\tmp
    echo ##vso[task.setvariable variable=price]cost$5
    echo "##vso[task.setvariable variable=quoted]cost"
- bash: echo consume
`
	graph, collector := reviewGraphWith(t, source, Context{AgentOS: AgentOSWindows})
	assertValue(t, graph, stepScope(1), "dir", PhaseMacro, `C:\tmp`)
	assertValue(t, graph, stepScope(1), "price", PhaseMacro, `cost$5`)
	// The quoted form is outside the minimal cmd grammar.
	quoted, ok := graph.Resolve(stepScope(1), "quoted", PhaseMacro)
	if !ok || !quoted.Unknown {
		t.Fatalf("quoted = %#v, %t; want unknown", quoted, ok)
	}
	if !hasCode(collector.List(), "TRL-VAR-0016") {
		t.Fatalf("diagnostics = %v, want TRL-VAR-0016", codes(collector.List()))
	}
}

func TestReviewR5CmdExpansionsAreUnknownAndDiagnosed(t *testing.T) {
	for _, value := range []string{"%NAME%", "!NAME!"} {
		t.Run(value, func(t *testing.T) {
			source := "steps:\n- script: |\n    echo ##vso[task.setvariable variable=x]" + value + "\n- bash: echo consume\n"
			graph, collector := reviewGraphWith(t, source, Context{AgentOS: AgentOSWindows})
			candidate, ok := graph.Resolve(stepScope(1), "x", PhaseMacro)
			if !ok || !candidate.Unknown {
				t.Fatalf("x = %#v, %t; want unknown", candidate, ok)
			}
			if !hasCode(collector.List(), "TRL-VAR-0016") {
				t.Fatalf("diagnostics = %v, want TRL-VAR-0016", codes(collector.List()))
			}
		})
	}
}

func TestReviewR5OpaqueLinesEndProvability(t *testing.T) {
	const emission = `echo "##vso[task.setvariable variable=x]late"`
	for _, line := range []string{"command exit 7", "builtin exit", "eval 'exit 7'", `"exit" 7`} {
		t.Run(line, func(t *testing.T) {
			graph, collector, scope := bashLogging(t, line+"\n"+emission)
			candidate, ok := graph.Resolve(scope, "x", PhaseMacro)
			if !ok || !candidate.Unknown {
				t.Fatalf("x = %#v, %t; want unknown", candidate, ok)
			}
			if !hasCode(collector.List(), "TRL-VAR-0016") {
				t.Fatalf("diagnostics = %v, want TRL-VAR-0016", codes(collector.List()))
			}
		})
	}
}

func TestReviewR5AllowlistedPrefixesStayProven(t *testing.T) {
	tests := []struct {
		name, source string
		context      Context
	}{
		{"bash", "steps:\n- bash: |\n\n    # note\n    echo 'prior'\n    echo \"##vso[task.setvariable variable=x]bash\"\n- bash: echo consume\n", Context{}},
		{"pwsh", "steps:\n- pwsh: |\n\n    # note\n    Write-Host 'prior'\n    Write-Output \"##vso[task.setvariable variable=x]pwsh\"\n- bash: echo consume\n", Context{}},
		{"cmd", "steps:\n- script: |\n\n    REM note\n    @echo off\n    echo prior\n    echo ##vso[task.setvariable variable=x]cmd\n- bash: echo consume\n", Context{AgentOS: AgentOSWindows}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			graph, collector := reviewGraphWith(t, tc.source, tc.context)
			assertValue(t, graph, stepScope(1), "x", PhaseMacro, tc.name)
			if hasCode(collector.List(), "TRL-VAR-0016") {
				t.Fatalf("allowlisted prefix was diagnosed: %v", codes(collector.List()))
			}
		})
	}
}

func TestReviewR5InvalidNamesAreRejectedAtEveryIngestionPath(t *testing.T) {
	tests := []struct {
		name, source string
		context      Context
	}{
		{"UI", "steps:\n- bash: echo ok\n", Context{UIPlain: []PlainVariable{{Name: "bad name", Value: "v"}}}},
		{"group", "variables:\n- group: G\nsteps:\n- bash: echo ok\n", Context{Groups: []VariableGroup{{Name: "G", Authorized: true, OrderProven: true, Plain: []PlainVariable{{Name: "bad name", Value: "v"}}}}}},
		{"queue", "steps:\n- bash: echo ok\n", Context{Queue: []QueueAssignment{{Name: "bad name", Value: "v"}}}},
		{"predefined", "steps:\n- bash: echo ok\n", Context{Predefined: []PlainVariable{{Name: "bad name", Value: "v"}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			collector := diag.NewCollector()
			pipeline := parse.Source(t.Name()+".yml", []byte(tc.source), collector)
			graph, err := Evaluate(pipeline, tc.context, collector)
			var rejection *diag.Error
			if graph != nil || !errors.As(err, &rejection) || rejection.Finding.Code != "TRL-VAR-0009" {
				t.Fatalf("invalid name accepted: graph=%v err=%v diagnostics=%v", graph, err, codes(collector.List()))
			}
		})
	}
}

func TestReviewR5InvalidLoggingOutputIsNotRecorded(t *testing.T) {
	collector := diag.NewCollector()
	state := Table{Scope: Scope{Level: "job", Job: "j", Step: -1}}
	event := mutation{
		candidate:  Candidate{Name: "emit.bad name", ComparisonKey: key("emit.bad name"), Source: SourceLogging},
		output:     true,
		outputName: "bad name",
		stepName:   "emit",
		bareName:   "bad name",
	}
	if applyMutation(&state, event, ast.StepProps{}, collector) {
		t.Fatal("invalid logging output was accepted")
	}
	if len(state.Bindings) != 0 || !hasCode(collector.List(), "TRL-VAR-0009") {
		t.Fatalf("bindings=%v diagnostics=%v", state.Bindings, codes(collector.List()))
	}
}
