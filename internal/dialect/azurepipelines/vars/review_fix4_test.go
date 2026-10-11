package vars

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
)

func TestFix4ScriptStepEmissionDependsOnTheAgentOS(t *testing.T) {
	const unquoted = "steps:\n- script: |\n    echo ##vso[task.setvariable variable=x]v\n- bash: echo consume\n"
	const quoted = "steps:\n- script: |\n    echo \"##vso[task.setvariable variable=x]v\"\n- bash: echo consume\n"
	for _, tc := range []struct {
		name, source, os string
		wantValue        string
		wantUnknown      bool
	}{
		{"cmd unquoted", unquoted, AgentOSWindows, "v", false},
		{"cmd quoted is not proven", quoted, AgentOSWindows, "", true},
		{"linux unquoted is a bash comment", unquoted, AgentOSLinux, "", true},
		{"macos unquoted is a bash comment", unquoted, AgentOSMacOS, "", true},
		{"unknown OS unquoted", unquoted, "", "", true},
		{"unknown OS quoted", quoted, "", "", true},
		{"linux quoted is bash", quoted, AgentOSLinux, "v", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph, collector := reviewGraphWith(t, tc.source, Context{AgentOS: tc.os})
			candidate, ok := graph.Resolve(stepScope(1), "x", PhaseMacro)
			if !ok || candidate.Unknown != tc.wantUnknown || candidate.Value != tc.wantValue {
				t.Fatalf("x = %#v, %t; want unknown=%t value=%q", candidate, ok, tc.wantUnknown, tc.wantValue)
			}
			if got := hasCode(collector.List(), "TRL-VAR-0016"); got != tc.wantUnknown {
				t.Fatalf("TRL-VAR-0016 = %t, want %t: %v", got, tc.wantUnknown, codes(collector.List()))
			}
		})
	}
}

func TestFix4CmdLineTaskEmissionDependsOnTheAgentOS(t *testing.T) {
	const source = "steps:\n- task: CmdLine@2\n  inputs:\n    script: 'echo ##vso[task.setvariable variable=x]v'\n- bash: echo consume\n"
	for _, tc := range []struct {
		os      string
		unknown bool
	}{{AgentOSWindows, false}, {AgentOSLinux, true}, {"", true}} {
		graph, _ := reviewGraphWith(t, source, Context{AgentOS: tc.os})
		candidate, ok := graph.Resolve(stepScope(1), "x", PhaseMacro)
		if !ok || candidate.Unknown != tc.unknown {
			t.Fatalf("os %q: x = %#v, %t; want unknown=%t", tc.os, candidate, ok, tc.unknown)
		}
	}
}

func TestFix4ExtendsIsRejected(t *testing.T) {
	pipeline := stepsPipeline(&ast.ScriptStep{})
	pipeline.Extends = &ast.Extends{}
	mustReject(t, pipeline)
}

func overflowGraph() (*Graph, Scope) {
	return macroGraph("x", strings.Repeat("a", expr.MaxEvaluationMemory), "y", "$(x)TAIL")
}

func TestFix4OverflowMakesEnvironmentEntriesUnknown(t *testing.T) {
	graph, scope := overflowGraph()
	var found bool
	for _, entry := range graph.EnvironmentEntries(scope) {
		if entry.Name == "Y" {
			found = true
			if !entry.Unknown || entry.Value != "" {
				t.Fatalf("truncated entry used as valid: unknown=%t len=%d", entry.Unknown, len(entry.Value))
			}
		}
	}
	if !found {
		t.Fatal("entry Y missing")
	}
}

func TestFix4OverflowMakesEnvironmentFail(t *testing.T) {
	graph, scope := overflowGraph()
	if _, err := graph.Environment(scope); err == nil {
		t.Fatal("Environment returned a truncated value as valid")
	}
}

func TestFix4OverflowMakesExplicitEnvEntryUnknown(t *testing.T) {
	graph, scope := overflowGraph()
	graph.Tables[0].Env = []EnvMapping{{Name: "Z", Value: "$(y)"}}
	for _, entry := range graph.EnvironmentEntries(scope) {
		if entry.Name == "Z" && (!entry.Unknown || entry.Value != "") {
			t.Fatalf("truncated env: entry = unknown=%t len=%d", entry.Unknown, len(entry.Value))
		}
	}
}

func TestFix4OverflowMakesLoggedVariableUnknown(t *testing.T) {
	graph, _ := overflowGraph()
	step := &ast.ScriptStep{Script: scalarNode(`echo "##vso[task.setvariable variable=z]$(y)"`), Kind: ast.ScriptBash}
	events := mutations(step, &graph.Tables[0], "", "", "", 0, AgentOSLinux)
	if len(events) != 2 || !events[1].stepWarning {
		t.Fatalf("events = %d, want the variable and the step warning", len(events))
	}
	candidate := events[0].candidate
	if !candidate.Unknown || candidate.Value != "" {
		t.Fatalf("truncated logged value used as valid: unknown=%t len=%d", candidate.Unknown, len(candidate.Value))
	}
}

func TestFix4OverflowOfALoggedVariableIsDiagnosed(t *testing.T) {
	collector := diag.NewCollector()
	pipeline := stepsPipeline(&ast.ScriptStep{Script: scalarNode(`echo "##vso[task.setvariable variable=z]$(y)"`), Kind: ast.ScriptBash}, &ast.ScriptStep{Script: scalarNode("echo consume")})
	pipeline.Variables = &ast.Variables{Form: ast.VariablesMap, Map: &ast.Map[*ast.Scalar]{Entries: []ast.MapEntry[*ast.Scalar]{
		{Key: scalarNode("x"), Value: scalarNode(strings.Repeat("a", expr.MaxEvaluationMemory))},
		{Key: scalarNode("y"), Value: scalarNode("$(x)TAIL")},
	}}}
	if _, err := Evaluate(pipeline, Context{AgentOS: AgentOSLinux}, collector); err != nil {
		t.Fatal(err)
	}
	if !hasCode(collector.List(), "TRL-VAR-0016") {
		t.Fatalf("diagnostics = %v, want TRL-VAR-0016", codes(collector.List()))
	}
}

func TestFix4EarlyExitsEndProvability(t *testing.T) {
	const echo = `echo "##vso[task.setvariable variable=x]late"`
	for _, tc := range []struct{ name, script string }{
		{"short-circuit exit", "[ -n \"$SKIP\" ] && exit 0\n" + echo},
		{"if exit fi", "if [ -n \"$SKIP\" ]; then exit 0; fi\n" + echo},
		{"multi-line if exit", "if [ -n \"$SKIP\" ]; then\n  exit 0\nfi\n" + echo},
		{"powershell if exit", "if ($env:SKIP) { exit 0 }\n" + echo},
		{"exec", "exec true\n" + echo},
		{"set -e then false", "set -e\nfalse\n" + echo},
		{"return", "[ -n \"$SKIP\" ] && return 0\n" + echo},
		{"throw", "if ($env:SKIP) { throw 'stop' }\n" + echo},
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

func TestFix4EchoBeforeAnExitIsStillProven(t *testing.T) {
	graph, _, scope := bashLogging(t, "# note\necho \"##vso[task.setvariable variable=x]ok\"\nexit 0")
	assertValue(t, graph, scope, "x", PhaseMacro, "ok")
}

func TestFix4HyphenatedNamesAreAcceptedWithAWarning(t *testing.T) {
	graph, collector := reviewGraphWith(t, "variables:\n  my-var: v\nsteps:\n- bash: echo $(my-var)\n", Context{})
	assertValue(t, graph, stepScope(0), "my-var", PhaseMacro, "v")
	if expanded := graph.Expand(stepScope(0), "$(my-var)"); expanded.Text != "v" {
		t.Fatalf("hyphenated macro = %q", expanded.Text)
	}
	if !hasCode(collector.List(), "TRL-VAR-0018") {
		t.Fatalf("diagnostics = %v, want TRL-VAR-0018", codes(collector.List()))
	}
}

func TestFix4NamesWithOtherInvalidCharactersAreStillRejected(t *testing.T) {
	collector := diag.NewCollector()
	pipeline := stepsPipeline(&ast.ScriptStep{Script: scalarNode("echo")})
	pipeline.Variables = &ast.Variables{Form: ast.VariablesMap, Map: &ast.Map[*ast.Scalar]{Entries: []ast.MapEntry[*ast.Scalar]{{Key: scalarNode("bad name"), Value: scalarNode("v")}}}}
	graph, err := Evaluate(pipeline, Context{}, collector)
	var rejection *diag.Error
	if graph != nil || !errors.As(err, &rejection) || rejection.Finding.Code != "TRL-VAR-0009" {
		t.Fatalf("invalid name accepted: graph=%v err=%v", graph, err)
	}
}

func TestFix4UnknownEnabledMakesTheMutationUnknown(t *testing.T) {
	graph, collector := reviewGraphWith(t, "steps:\n- bash: echo \"##vso[task.setvariable variable=x]maybe\"\n  enabled: $(flag)\n- bash: echo consume\n", Context{})
	candidate, ok := graph.Resolve(stepScope(1), "x", PhaseMacro)
	if !ok || !candidate.Unknown || candidate.Value != "" {
		t.Fatalf("x = %#v, %t; want an unknown mutation", candidate, ok)
	}
	if !hasCode(collector.List(), "TRL-VAR-0016") {
		t.Fatalf("diagnostics = %v, want TRL-VAR-0016", codes(collector.List()))
	}
}

func TestFix4SubstituteIgnoresClosersInsideQuotedStrings(t *testing.T) {
	evaluator := NewCompileEvaluator(nil)
	if got := evaluator.Substitute("a ${{ replace('x', 'x', '}}') }} b"); got != "a }} b" {
		t.Fatalf("Substitute = %q", got)
	}
	if got := evaluator.Substitute("${{ 'it''s }}' }}!"); got != "it's }}!" {
		t.Fatalf("escaped quote: %q", got)
	}
}

func TestFix4WorkBudgetExhaustionIsSignalled(t *testing.T) {
	const levels = 46
	var pairs []string
	for i := 0; i < levels-1; i++ {
		pairs = append(pairs, "l"+strconv.Itoa(i), "$(l"+strconv.Itoa(i+1)+")")
	}
	pairs = append(pairs, "l"+strconv.Itoa(levels-1), strings.Repeat("a", 200000), "extra", "ok")
	graph, scope := macroGraph(pairs...)
	expanded := graph.Expand(scope, "$(l0)$(extra)")
	if len(expanded.Text) > expr.MaxEvaluationMemory {
		t.Fatalf("output over the bound: %d", len(expanded.Text))
	}
	if !strings.HasSuffix(expanded.Text, "$(extra)") {
		t.Fatalf("the budget was not exhausted; the test input is stale")
	}
	if !hasOverflow(expanded) {
		t.Fatalf("budget exhaustion left no diagnostic: %v", codes(expanded.Diagnostics))
	}
}
