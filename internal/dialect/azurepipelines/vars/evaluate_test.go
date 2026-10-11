package vars

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
)

func TestScopePrecedenceQueueAndCompileVisibility(t *testing.T) {
	pipeline := &ast.Pipeline{
		Shape:     ast.ShapeStages,
		Variables: variables(def("A", "root", false)),
		Stages: list[ast.Stage](&ast.StageDef{
			Stage:     scalarNode("S"),
			Variables: variables(def("a", "stage", false)),
			Jobs: list[ast.Job](&ast.JobDef{
				Job:       scalarNode("J"),
				Variables: variables(def("A", "job", false)),
				Steps:     list[ast.Step](&ast.ScriptStep{Script: scalarNode("echo $(A)")}),
			}),
		}),
	}
	graph, err := Evaluate(pipeline, Context{
		UIPlain: []PlainVariable{{Name: "a", Value: "ui", Settable: true}},
		Queue:   []QueueAssignment{{Name: "A", Value: "queued"}},
	}, diag.NewCollector())
	if err != nil {
		t.Fatal(err)
	}
	step := Scope{Level: "step", Stage: "S", Job: "J", Step: 0}
	assertValue(t, graph, step, "a", PhaseMacro, "job")
	assertValue(t, graph, Scope{Level: "pipeline", Step: -1}, "A", PhaseCompile, "root")
	binding := graph.Tables[len(graph.Tables)-1].Bindings[0]
	if len(binding.Candidates) != 5 || !binding.Candidates[3].Shadowed || binding.Candidates[4].Shadowed {
		t.Fatalf("candidate provenance/shadowing = %#v", binding.Candidates)
	}
}

func TestQueueLimitRejectsWithoutPartialGraph(t *testing.T) {
	collector := diag.NewCollector()
	graph, err := Evaluate(&ast.Pipeline{Shape: ast.ShapeSteps}, Context{
		LimitQueueTimeVariables: true,
		Queue:                   []QueueAssignment{{Name: "newVar", Value: "v"}},
	}, collector)
	if graph != nil || err == nil {
		t.Fatalf("graph=%v err=%v", graph, err)
	}
	var diagnosticError *diag.Error
	if !errors.As(err, &diagnosticError) || diagnosticError.Finding.Code != "TRL-VAR-0010" {
		t.Fatalf("error = %v", err)
	}
}

func TestSecretsStayReferencesAndAreNotEnvironmentMapped(t *testing.T) {
	graph, err := Evaluate(&ast.Pipeline{Shape: ast.ShapeSteps, Steps: list[ast.Step](&ast.ScriptStep{Script: scalarNode("echo hi")})}, Context{
		UISecrets: []SecretVariable{{Name: "token", Provider: "ado", ID: "variable/7"}},
	}, diag.NewCollector())
	if err != nil {
		t.Fatal(err)
	}
	candidate, ok := graph.Resolve(Scope{Level: "step", Step: 0}, "TOKEN", PhaseMacro)
	if !ok || !candidate.Secret || candidate.SecretRef == nil || candidate.SecretRef.ID != "variable/7" || candidate.Value != "" {
		t.Fatalf("secret candidate = %#v, %v", candidate, ok)
	}
	if env, err := graph.Environment(Scope{Level: "step", Step: 0}); err != nil || len(env) != 0 {
		t.Fatalf("secret automatically projected: %#v, %v", env, err)
	}
}

func TestLoggingMutationStartsAtNextStepAndHonorsReadonly(t *testing.T) {
	pipeline := &ast.Pipeline{Shape: ast.ShapeSteps, Variables: variables(def("x", "old", true)), Steps: list[ast.Step](
		&ast.ScriptStep{Script: scalarNode("echo $(x)\necho '##vso[task.setvariable variable=x]new'\necho $(x)")},
		&ast.ScriptStep{Script: scalarNode("echo $(x)")},
	)}
	collector := diag.NewCollector()
	graph, err := Evaluate(pipeline, Context{}, collector)
	if err != nil {
		t.Fatal(err)
	}
	assertValue(t, graph, Scope{Level: "step", Step: 0}, "x", PhaseMacro, "old")
	assertValue(t, graph, Scope{Level: "step", Step: 1}, "x", PhaseMacro, "old")
	if got := collector.List(); len(got) != 1 || got[0].Code != "TRL-VAR-0013" {
		t.Fatalf("diagnostics = %#v", got)
	}
}

func TestOutputDependencyAndImplicitStageEdge(t *testing.T) {
	pipeline := &ast.Pipeline{Shape: ast.ShapeStages, Stages: list[ast.Stage](
		&ast.StageDef{Stage: scalarNode("A"), Jobs: list[ast.Job](&ast.JobDef{Job: scalarNode("A1"), Steps: list[ast.Step](&ast.ScriptStep{Script: scalarNode("echo '##vso[task.setvariable variable=o;isOutput=true]v'"), StepProps: ast.StepProps{Name: scalarNode("s")}})})},
		&ast.StageDef{Stage: scalarNode("B"), Jobs: list[ast.Job](&ast.JobDef{Job: scalarNode("B1"), Steps: list[ast.Step](&ast.ScriptStep{Script: scalarNode("echo hi")})})},
	)}
	graph, err := Evaluate(pipeline, Context{AgentOS: AgentOSLinux}, diag.NewCollector())
	if err != nil {
		t.Fatal(err)
	}
	consumer := Scope{Level: "job", Stage: "B", Job: "B1", Step: -1}
	got, ok := graph.ResolveOutput(consumer, "A", "A1", "s.o")
	if !ok || got.Value != "v" {
		t.Fatalf("implicit stage output = %#v, %v", got, ok)
	}
	if _, ok := graph.ResolveOutput(consumer, "X", "A1", "s.o"); ok {
		t.Fatal("unrelated output became visible")
	}
}

func TestUnknownGroupOrderDoesNotGuess(t *testing.T) {
	pipeline := &ast.Pipeline{Shape: ast.ShapeSteps, Variables: variables(group("G1"), group("G2")), Steps: list[ast.Step](&ast.ScriptStep{Script: scalarNode("echo $(x)")})}
	collector := diag.NewCollector()
	graph, err := Evaluate(pipeline, Context{Groups: []VariableGroup{
		{Name: "G1", Authorized: true, Plain: []PlainVariable{{Name: "x", Value: "one"}}},
		{Name: "G2", Authorized: true, Plain: []PlainVariable{{Name: "x", Value: "two"}}},
	}}, collector)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := graph.Resolve(Scope{Level: "step", Step: 0}, "x", PhaseMacro); ok {
		t.Fatal("ambiguous group collision was guessed")
	}
	// The unresolved macro in the script also yields the step-level warning.
	if got := collector.List(); len(got) != 2 || !hasCode(got, "TRL-VAR-0004") || !hasCode(got, "TRL-VAR-0016") {
		t.Fatalf("diagnostics = %#v", got)
	}
}

func TestNamesAndEnvironmentProjection(t *testing.T) {
	if !ValidName("any.variable_2") || ValidName("my var") || !ValidName("my-var") || ValidName("my$var") || !ValidGroupName("my-variable-group") || ValidGroupName("") || !ReservedName("PATHROOT") || ReservedName("dispatch") {
		t.Fatal("naming rules")
	}
	if got := EnvironmentName("any.variable"); got != "ANY_VARIABLE" {
		t.Fatalf("environment name = %q", got)
	}
}

func TestValidationMappingAndHelperBranches(t *testing.T) {
	allowedTarget := &ast.Target{SettableVariables: &ast.SettableVariables{List: list(scalarNode("Sauce"))}}
	if !mutationAllowed("sauce", nil) || !mutationAllowed("sauce", allowedTarget) || mutationAllowed("other", allowedTarget) || mutationAllowed("sauce", &ast.Target{SettableVariables: &ast.SettableVariables{None: true}}) {
		t.Fatal("settable variable rules")
	}
	if got := deploymentUnits(&ast.Deployment{Strategy: &ast.DeploymentStrategy{RunOnce: &ast.Lifecycle{Deploy: &ast.Hook{Steps: list[ast.Step](&ast.ScriptStep{})}}}}); len(got) != 1 || got[0].steps == nil {
		t.Fatal("deployment steps missing")
	}
	if got := deploymentUnits(&ast.Deployment{}); len(got) != 1 || got[0].steps != nil {
		t.Fatal("empty deployment has steps")
	}

	badVars := &ast.Variables{Form: ast.VariablesMap, Map: &ast.Map[*ast.Scalar]{Entries: []ast.MapEntry[*ast.Scalar]{
		{Key: scalarNode("good.name"), Value: scalarNode("v")},
		{Key: scalarNode("bad name"), Value: scalarNode("bad")},
	}}}
	collector := diag.NewCollector()
	rejected, err := Evaluate(&ast.Pipeline{Shape: ast.ShapeSteps, Variables: badVars, Steps: list[ast.Step](&ast.ScriptStep{Script: scalarNode("echo hi")})}, Context{}, collector)
	var nameError *diag.Error
	if rejected != nil || !errors.As(err, &nameError) || nameError.Finding.Code != "TRL-VAR-0009" {
		t.Fatalf("invalid name must reject without a graph: %v", err)
	}
	if got := collector.List(); len(got) != 1 || got[0].Code != "TRL-VAR-0009" {
		t.Fatalf("diagnostics = %#v", got)
	}
	mapVars := &ast.Variables{Form: ast.VariablesMap, Map: &ast.Map[*ast.Scalar]{Entries: []ast.MapEntry[*ast.Scalar]{
		{Key: scalarNode("good.name"), Value: scalarNode("v")},
	}}}
	graph, err := Evaluate(&ast.Pipeline{Shape: ast.ShapeSteps, Variables: mapVars, Steps: list[ast.Step](&ast.ScriptStep{Script: scalarNode("echo hi")})}, Context{}, diag.NewCollector())
	if err != nil {
		t.Fatal(err)
	}
	assertValue(t, graph, Scope{Level: "step", Step: 0}, "GOOD.NAME", PhaseMacro, "v")
	if got, err := graph.Environment(Scope{Level: "step", Step: 0}); err != nil || len(got) != 1 || got[0] != [2]string{"GOOD_NAME", "v"} {
		t.Fatalf("environment = %#v, %v", got, err)
	}

	_, err = Evaluate(&ast.Pipeline{Shape: ast.ShapeSteps, Variables: variables(group("G"))}, Context{Groups: []VariableGroup{{Name: "G", Authorized: false}}}, diag.NewCollector())
	var diagnosticError *diag.Error
	if !errors.As(err, &diagnosticError) || diagnosticError.Finding.Code != "TRL-VAR-0011" {
		t.Fatalf("unauthorized group error = %v", err)
	}

	graph, err = Evaluate(&ast.Pipeline{Shape: ast.ShapeSteps}, Context{
		UISecrets:               []SecretVariable{{Name: "s", Settable: true, ID: "s"}},
		Queue:                   []QueueAssignment{{Name: "S", Value: "replacement"}},
		LimitQueueTimeVariables: true,
	}, diag.NewCollector())
	if err != nil || graph == nil {
		t.Fatalf("settable secret queue assignment: graph=%v err=%v", graph, err)
	}

	var nilGraph *Graph
	if _, ok := nilGraph.Table(Scope{}); ok {
		t.Fatal("nil graph table found")
	}
	if env, err := graph.Environment(Scope{Level: "missing"}); env != nil || err != nil {
		t.Fatalf("missing environment = %#v, %v", env, err)
	}
}

func TestIndependentVectorTable(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "testdata", "azurepipelines", "vars", "ado-variable-vectors.md")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	counts := map[string]int{"docs": 0, "inferred": 0, "uncertain": 0} //trasloco:unordered-ok fixed-key counters only
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "| VARV-") {
			continue
		}
		fields := strings.Split(strings.Trim(line, "|"), " | ")
		if len(fields) != 7 {
			continue
		}
		id, confidence, source := strings.TrimSpace(fields[0]), strings.TrimSpace(fields[5]), strings.TrimSpace(fields[6])
		if seen[id] || source == "" {
			t.Fatalf("duplicate or unsourced vector %s", id)
		}
		seen[id] = true
		if _, ok := counts[confidence]; !ok {
			t.Fatalf("unknown confidence %q", confidence)
		}
		counts[confidence]++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 146 {
		t.Fatalf("loaded %d vectors, want 146", len(seen))
	}
	t.Logf("vectors loaded: docs=%d inferred=%d uncertain=%d", counts["docs"], counts["inferred"], counts["uncertain"])
}

func assertValue(t *testing.T, graph *Graph, scope Scope, name string, phase Phase, want string) {
	t.Helper()
	got, ok := graph.Resolve(scope, name, phase)
	if !ok || got.Value != want {
		t.Fatalf("Resolve(%#v, %q) = %#v, %v; want %q", scope, name, got, ok, want)
	}
}

func scalarNode(value string) *ast.Scalar { return &ast.Scalar{Value: value} }

func def(name, value string, readonly bool) ast.Variable {
	return &ast.VariableDef{Name: scalarNode(name), Value: scalarNode(value), Readonly: &ast.Bool{Scalar: *scalarNode(""), Val: readonly, Known: true}}
}

func group(name string) ast.Variable { return &ast.VariableGroup{Group: scalarNode(name)} }

func variables(items ...ast.Variable) *ast.Variables {
	listValue := list(items...)
	return &ast.Variables{Form: ast.VariablesList, List: listValue}
}

func list[T any](items ...T) *ast.List[T] {
	result := &ast.List[T]{}
	for _, item := range items {
		result.Items = append(result.Items, ast.ListItem[T]{Value: item})
	}
	return result
}
