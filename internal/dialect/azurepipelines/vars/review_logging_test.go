package vars

import (
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
)

// bashLogging evaluates a bash step with the given script followed by a
// consumer step, and returns the graph, its findings and the consumer scope.
func bashLogging(t *testing.T, script string) (*Graph, *diag.Collector, Scope) {
	t.Helper()
	var b strings.Builder
	b.WriteString("steps:\n- bash: |\n")
	for _, line := range strings.Split(script, "\n") {
		b.WriteString("    " + line + "\n")
	}
	b.WriteString("- bash: echo consume\n")
	graph, collector := reviewGraphWith(t, b.String(), Context{})
	return graph, collector, stepScope(1)
}

func TestReviewDisabledStepDoesNotMutate(t *testing.T) {
	pipeline := &ast.Pipeline{Shape: ast.ShapeSteps, Steps: list[ast.Step](
		&ast.ScriptStep{Script: scalarNode("echo '##vso[task.setvariable variable=disabled]bad'"), StepProps: ast.StepProps{Enabled: &ast.Bool{Val: false, Known: true}}},
		&ast.ScriptStep{Script: scalarNode("echo consume")},
	)}
	graph, err := Evaluate(pipeline, Context{}, diag.NewCollector())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := graph.Resolve(stepScope(1), "disabled", PhaseMacro); ok {
		t.Fatal("enabled:false step mutated state")
	}
}

func TestReviewDynamicShellValueIsUnknown(t *testing.T) {
	graph, _, scope := bashLogging(t, `echo "##vso[task.setvariable variable=dynamic]$VALUE"`)
	dynamic, ok := graph.Resolve(scope, "dynamic", PhaseMacro)
	if !ok || !dynamic.Unknown || dynamic.Value != "" {
		t.Fatalf("dynamic shell value = %#v, %t", dynamic, ok)
	}
}

func TestReviewAgentEscapesAreDecodedInOrder(t *testing.T) {
	graph, _, scope := bashLogging(t, `echo '##vso[task.setvariable variable=encoded]v%3B%0D%0A%5D%AZP2525'`)
	assertValue(t, graph, scope, "encoded", PhaseMacro, "v;\r\n]%25")
}

func TestReviewSettableVariablesAllowTheListedOutput(t *testing.T) {
	allowed := &ast.Target{SettableVariables: &ast.SettableVariables{List: list(scalarNode("sauce"))}}
	pipeline := &ast.Pipeline{Shape: ast.ShapeSteps, Steps: list[ast.Step](
		&ast.ScriptStep{Script: scalarNode("echo '##vso[task.setvariable variable=sauce;isOutput=true]ok'"), StepProps: ast.StepProps{Name: scalarNode("emit"), Target: allowed}},
		&ast.ScriptStep{Script: scalarNode("echo consume")},
	)}
	graph, err := Evaluate(pipeline, Context{AgentOS: AgentOSLinux}, diag.NewCollector())
	if err != nil {
		t.Fatal(err)
	}
	output, ok := graph.Resolve(stepScope(1), "emit.sauce", PhaseMacro)
	if !ok || output.Value != "ok" || !output.Readonly {
		t.Fatalf("settable output = %#v, %t", output, ok)
	}
}

func TestReviewUnnamedOutputCreatesNoVariable(t *testing.T) {
	graph, _, scope := bashLogging(t, `echo '##vso[task.setvariable variable=orphan;isOutput=true]bad'`)
	if _, ok := graph.Resolve(scope, "orphan", PhaseMacro); ok {
		t.Fatal("unnamed isOutput created a job variable")
	}
}

func TestReviewTaskInlineScriptsAreScanned(t *testing.T) {
	for _, task := range []struct{ name, step string }{
		{"Bash", "- task: Bash@3\n  inputs:\n    targetType: inline\n    script: echo \"##vso[task.setvariable variable=fromTask]yes\"\n"},
		{"CmdLine", "- task: CmdLine@2\n  inputs:\n    script: 'echo ##vso[task.setvariable variable=fromTask]yes'\n"},
		{"PowerShell", "- task: PowerShell@2\n  inputs:\n    targetType: inline\n    script: Write-Host \"##vso[task.setvariable variable=fromTask]yes\"\n"},
	} {
		t.Run(task.name, func(t *testing.T) {
			graph, _ := reviewGraphWith(t, "steps:\n"+task.step+"- bash: echo consume\n", Context{AgentOS: AgentOSWindows})
			assertValue(t, graph, stepScope(1), "fromTask", PhaseMacro, "yes")
		})
	}
}

func TestReviewLoggingMacroInValueIsUnknown(t *testing.T) {
	graph, _ := reviewGraphWith(t, `variables:
  x: one
steps:
- bash: echo "##vso[task.setvariable variable=copy]$(x)"
- bash: echo "##vso[task.setvariable variable=x]two"
- bash: echo consume
`, Context{})
	scope := stepScope(2)
	assertValue(t, graph, scope, "x", PhaseMacro, "two")
	if copied, ok := graph.Resolve(scope, "copy", PhaseMacro); !ok || !copied.Unknown || copied.Value != "" {
		t.Fatalf("copy = %#v, %t; want Unknown (macros in a marker are not expanded)", copied, ok)
	}
}

func TestReviewQuotedLoggingCommandTailIsNotPartOfTheValue(t *testing.T) {
	for _, tc := range []struct{ name, script string }{
		{"single quotes, next command", `echo '##vso[task.setvariable variable=x]val'; echo after`},
		{"double quotes, fallback", `echo "##vso[task.setvariable variable=x]val" || true`},
		{"single quotes, comment", `echo '##vso[task.setvariable variable=x]val' # note`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A tail on the emitting line leaves the allowlist: the value is
			// never taken from it, and the omission is diagnosed.
			graph, collector, scope := bashLogging(t, tc.script)
			candidate, ok := graph.Resolve(scope, "x", PhaseMacro)
			if !ok || !candidate.Unknown || candidate.Value != "" {
				t.Fatalf("x = %#v, %t; want unknown without the tail in the value", candidate, ok)
			}
			if !hasCode(collector.List(), "TRL-VAR-0016") {
				t.Fatalf("diagnostics = %v, want TRL-VAR-0016", codes(collector.List()))
			}
		})
	}
}

func TestReviewTopLevelEchoIsApplied(t *testing.T) {
	graph, collector, scope := bashLogging(t, "echo 'start'\necho \"##vso[task.setvariable variable=x]yes\"")
	assertValue(t, graph, scope, "x", PhaseMacro, "yes")
	if hasCode(collector.List(), "TRL-VAR-0016") {
		t.Fatalf("a provably emitted command was diagnosed: %v", codes(collector.List()))
	}
}

func TestReviewCommentedLoggingCommandIsNotApplied(t *testing.T) {
	graph, collector := reviewGraphWith(t, `steps:
- bash: |
    # ##vso[task.setvariable variable=x]wrong
    echo safe
- bash: echo $(x)
`, Context{})
	scope := stepScope(1)
	candidate, ok := graph.Resolve(scope, "x", PhaseMacro)
	if candidate.Value == "wrong" {
		t.Fatal("a commented logging command mutated the graph")
	}
	if !ok || !candidate.Unknown {
		t.Fatalf("x = %#v, %t; want an unknown mutation", candidate, ok)
	}
	if expanded := graph.Expand(scope, "$(x)"); expanded.Text != "$(x)" {
		t.Fatalf("an unknown value must stay literal: %q", expanded.Text)
	}
	if !hasCode(collector.List(), "TRL-VAR-0016") {
		t.Fatalf("diagnostics = %v, want TRL-VAR-0016", codes(collector.List()))
	}
}

func TestReviewConditionalLoggingCommandIsNotApplied(t *testing.T) {
	graph, collector, scope := bashLogging(t, "if [ \"$FLAG\" = 1 ]; then\n  echo \"##vso[task.setvariable variable=x]maybe\"\nfi")
	candidate, ok := graph.Resolve(scope, "x", PhaseMacro)
	if !ok || !candidate.Unknown || candidate.Value == "maybe" {
		t.Fatalf("x = %#v, %t; want an unknown mutation", candidate, ok)
	}
	if !hasCode(collector.List(), "TRL-VAR-0016") {
		t.Fatalf("diagnostics = %v, want TRL-VAR-0016", codes(collector.List()))
	}
}

func TestReviewOtherUnprovableLoggingCommandsAreUnknown(t *testing.T) {
	for _, tc := range []struct{ name, script string }{
		{"loop", "for i in 1 2; do\n  echo \"##vso[task.setvariable variable=x]loop\"\ndone"},
		{"function", "emit() {\n  echo \"##vso[task.setvariable variable=x]fn\"\n}\nemit"},
		{"heredoc", "cat <<EOF\n##vso[task.setvariable variable=x]doc\nEOF"},
		{"assignment", "MSG=\"##vso[task.setvariable variable=x]assigned\"\necho \"$MSG\""},
		{"after exit", "exit 0\necho \"##vso[task.setvariable variable=x]dead\""},
		{"second argument", "echo note \"##vso[task.setvariable variable=x]late\""},
		{"redirected", "echo \"##vso[task.setvariable variable=x]file\" > out.txt"},
		{"unquoted in bash is a comment", "echo ##vso[task.setvariable variable=x]bare"},
		{"short-circuited", "false && echo \"##vso[task.setvariable variable=x]never\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph, collector, scope := bashLogging(t, tc.script)
			candidate, ok := graph.Resolve(scope, "x", PhaseMacro)
			if !ok || !candidate.Unknown || candidate.Value != "" {
				t.Fatalf("x = %#v, %t; want an unknown mutation", candidate, ok)
			}
			if !hasCode(collector.List(), "TRL-VAR-0016") {
				t.Fatalf("diagnostics = %v, want TRL-VAR-0016", codes(collector.List()))
			}
		})
	}
}

func TestReviewConditionedStepLoggingCommandIsNotApplied(t *testing.T) {
	graph, _ := reviewGraphWith(t, `steps:
- bash: echo "##vso[task.setvariable variable=x]maybe"
  condition: eq(variables['Build.Reason'], 'Manual')
- bash: echo consume
`, Context{})
	candidate, ok := graph.Resolve(stepScope(1), "x", PhaseMacro)
	if !ok || !candidate.Unknown {
		t.Fatalf("x = %#v, %t; want an unknown mutation", candidate, ok)
	}
}
