package vars

import (
	"errors"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
)

// Each test below pins one claimed review fix with its own assertions, so
// removing the fix fails exactly one test. The deployment, logging, macro,
// secret and unexpanded-node regressions live in the review_*_test.go files.

func reviewGraphWith(t *testing.T, source string, ctx Context) (*Graph, *diag.Collector) {
	t.Helper()
	collector := diag.NewCollector()
	pipeline := parse.Source(t.Name()+".yml", []byte(source), collector)
	graph, err := Evaluate(pipeline, ctx, collector)
	if err != nil {
		t.Fatalf("Evaluate: %v; diagnostics=%v", err, codes(collector.List()))
	}
	return graph, collector
}

func reviewGraph(t *testing.T, source string) *Graph {
	t.Helper()
	graph, _ := reviewGraphWith(t, source, Context{})
	return graph
}

func hasCode(findings []diag.Diagnostic, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func stepScope(index int) Scope { return Scope{Level: "step", Step: index} }

func secretFixture(t *testing.T) (*Graph, Scope) {
	t.Helper()
	env := &ast.Map[*ast.Scalar]{Entries: []ast.MapEntry[*ast.Scalar]{
		{Key: scalarNode("FROM_UI"), Value: scalarNode("$(uiSecret)")},
		{Key: scalarNode("FROM_GROUP"), Value: scalarNode("$(groupSecret)")},
	}}
	pipeline := &ast.Pipeline{Shape: ast.ShapeSteps, Steps: list[ast.Step](
		&ast.ScriptStep{Script: scalarNode("echo safe"), StepProps: ast.StepProps{Env: env}},
	)}
	graph, err := Evaluate(pipeline, Context{
		UISecrets: []SecretVariable{{Name: "uiSecret", Provider: "ado", ID: "ui/1", Settable: true}},
		Groups:    []VariableGroup{{Name: "unused", Secrets: []SecretVariable{{Name: "groupSecret", Provider: "ado", ID: "group/1"}}}},
		Queue:     []QueueAssignment{{Name: "UISECRET", Value: "plaintext-one"}, {Name: "groupSecret", Value: "plaintext-two"}},
	}, diag.NewCollector())
	if err != nil {
		t.Fatal(err)
	}
	return graph, stepScope(0)
}

func TestReviewSecretsRemainReferences(t *testing.T) {
	graph, scope := secretFixture(t)
	t.Run("resolve returns a reference without a value", func(t *testing.T) {
		for _, name := range []string{"uiSecret", "groupSecret"} {
			candidate, ok := graph.Resolve(scope, name, PhaseRuntime)
			if !ok || !candidate.Secret || candidate.SecretRef == nil || candidate.Value != "" {
				t.Fatalf("Resolve(%s) = %#v, %t", name, candidate, ok)
			}
		}
	})
	t.Run("queue-time plaintext never replaces a secret", func(t *testing.T) {
		candidate, _ := graph.Resolve(scope, "groupSecret", PhaseRuntime)
		if !candidate.Secret || candidate.Value != "" {
			t.Fatalf("queue plaintext reached the graph: %#v", candidate)
		}
	})
	t.Run("runtime expressions see null", func(t *testing.T) {
		result, err := graph.EvalRuntime(scope, "$[ variables['uiSecret'] ]")
		if err != nil || !result.Null {
			t.Fatalf("EvalRuntime(secret) = %#v, %v; want successful null", result, err)
		}
	})
	t.Run("compile expressions see null", func(t *testing.T) {
		result, err := graph.EvalCompile(scope, "${{ variables.uiSecret }}")
		if err != nil || !result.Null {
			t.Fatalf("EvalCompile(secret) = %#v, %v; want successful null", result, err)
		}
	})
	t.Run("conditions see the secret as absent", func(t *testing.T) {
		runs, err := graph.EvalCondition(scope, "not(variables.uiSecret)")
		if err != nil || !runs {
			t.Fatalf("EvalCondition(secret absent) = %t, %v", runs, err)
		}
	})
	t.Run("macro expansion masks and keeps references", func(t *testing.T) {
		expanded := graph.Expand(scope, "$(uiSecret)/$(groupSecret)")
		if !expanded.Masked || expanded.Text != "***/***" || len(expanded.SecretRefs) != 2 {
			t.Fatalf("Expand(secret) = %#v", expanded)
		}
	})
	t.Run("explicit env entries carry references only", func(t *testing.T) {
		entries := graph.EnvironmentEntries(scope)
		for _, name := range []string{"FROM_UI", "FROM_GROUP"} {
			entry, ok := environmentEntry(entries, name)
			if !ok || !entry.Masked || entry.Value != "" || len(entry.SecretRefs) != 1 {
				t.Fatalf("EnvironmentEntries(%s) = %#v, %t", name, entry, ok)
			}
		}
	})
	t.Run("environment refuses a secret-derived literal", func(t *testing.T) {
		if _, err := graph.Environment(scope); err == nil {
			t.Fatal("Environment accepted a secret-derived literal")
		}
	})
	t.Run("secret outputs resolve as references", func(t *testing.T) {
		graph.Nodes = append(graph.Nodes, Node{Level: "job", Stage: "S", Job: "B", DependsOn: []string{"A"}, Explicit: true})
		graph.Outputs = append(graph.Outputs, Output{Stage: "S", Job: "A", StepName: "emit", Name: "token", Candidate: Candidate{Secret: true, SecretRef: &SecretRef{Provider: "ado", ID: "output/1"}, Available: true}})
		output, ok := graph.ResolveOutput(Scope{Level: "job", Stage: "S", Job: "B", Step: -1}, "S", "A", "emit.token")
		if !ok || !output.Secret || output.SecretRef == nil || output.Value != "" {
			t.Fatalf("ResolveOutput(secret) = %#v, %t", output, ok)
		}
	})
}

func TestReviewProductionExpressionAPIsDecideVisibility(t *testing.T) {
	pipeline := &ast.Pipeline{Shape: ast.ShapeSteps, Variables: variables(def("yamlOnly", "yaml", false)), Steps: list[ast.Step](&ast.ScriptStep{Script: scalarNode("echo")})}
	graph, err := Evaluate(pipeline, Context{UIPlain: []PlainVariable{{Name: "runtimeOnly", Value: "runtime"}}}, diag.NewCollector())
	if err != nil {
		t.Fatal(err)
	}
	scope := stepScope(0)
	t.Run("compile sees YAML declarations", func(t *testing.T) {
		result, err := graph.EvalCompile(scope, "${{ variables.yamlOnly }}")
		if err != nil || result.Text != "yaml" || result.Null {
			t.Fatalf("EvalCompile(yamlOnly) = %#v, %v", result, err)
		}
	})
	t.Run("compile does not see runtime-only values", func(t *testing.T) {
		result, err := graph.EvalCompile(scope, "${{ variables.runtimeOnly }}")
		if err != nil || !result.Null {
			t.Fatalf("EvalCompile(runtimeOnly) = %#v, %v; want null", result, err)
		}
	})
	t.Run("runtime sees runtime-only values", func(t *testing.T) {
		result, err := graph.EvalRuntime(scope, "$[ variables.runtimeOnly ]")
		if err != nil || result.Text != "runtime" || result.Null {
			t.Fatalf("EvalRuntime(runtimeOnly) = %#v, %v", result, err)
		}
	})
	t.Run("a missing runtime variable is a successful null", func(t *testing.T) {
		result, err := graph.EvalRuntime(scope, "$[ variables.missing ]")
		if err != nil || !result.Null {
			t.Fatalf("negative runtime row = %#v, %v; want successful null", result, err)
		}
	})
}

func orderedGroupsFixture(t *testing.T, firstProven, secondProven bool) (*Graph, *diag.Collector) {
	t.Helper()
	collector := diag.NewCollector()
	graph, err := Evaluate(&ast.Pipeline{Shape: ast.ShapeSteps, Variables: variables(group("G1"), group("G2"), def("x", "local", false)), Steps: list[ast.Step](&ast.ScriptStep{})}, Context{Groups: []VariableGroup{
		{Name: "G1", Authorized: true, OrderProven: firstProven, Plain: []PlainVariable{{Name: "x", Value: "one"}}},
		{Name: "G2", Authorized: true, OrderProven: secondProven, Plain: []PlainVariable{{Name: "x", Value: "two"}}},
	}}, collector)
	if err != nil {
		t.Fatal(err)
	}
	return graph, collector
}

func TestReviewAmbiguityIsSymmetricInGroupOrder(t *testing.T) {
	for _, order := range []struct {
		name   string
		proven [2]bool
	}{{"first unproven", [2]bool{false, true}}, {"second unproven", [2]bool{true, false}}} {
		t.Run(order.name, func(t *testing.T) {
			_, collector := orderedGroupsFixture(t, order.proven[0], order.proven[1])
			if got := collector.List(); len(got) != 1 || got[0].Code != "TRL-VAR-0004" {
				t.Fatalf("diagnostics = %#v", got)
			}
		})
	}
}

func TestReviewAmbiguityIsClearedByALaterUnambiguousDeclaration(t *testing.T) {
	graph, _ := orderedGroupsFixture(t, false, true)
	assertValue(t, graph, stepScope(0), "x", PhaseMacro, "local")
}

func TestReviewAmbiguityDoesNotCrossScopes(t *testing.T) {
	pipeline := &ast.Pipeline{Shape: ast.ShapeStages, Variables: variables(group("G1")), Stages: list[ast.Stage](
		&ast.StageDef{Stage: scalarNode("S"), Variables: variables(group("G2")), Jobs: list[ast.Job](&ast.JobDef{Job: scalarNode("J"), Steps: list[ast.Step](&ast.ScriptStep{})})},
	)}
	collector := diag.NewCollector()
	graph, err := Evaluate(pipeline, Context{Groups: []VariableGroup{
		{Name: "G1", Authorized: true, Plain: []PlainVariable{{Name: "x", Value: "root"}}},
		{Name: "G2", Authorized: true, Plain: []PlainVariable{{Name: "x", Value: "stage"}}},
	}}, collector)
	if err != nil {
		t.Fatal(err)
	}
	assertValue(t, graph, Scope{Level: "step", Stage: "S", Job: "J", Step: 0}, "x", PhaseMacro, "stage")
	if len(collector.List()) != 0 {
		t.Fatalf("cross-scope groups were marked ambiguous: %#v", collector.List())
	}
}

const stageEdgeSource = `stages:
- stage: A
  jobs:
  - job: A1
    steps:
    - bash: echo '##vso[task.setvariable variable=o;isOutput=true]a'
      name: emit
- stage: B
  jobs:
  - job: B1
    steps:
    - bash: echo '##vso[task.setvariable variable=o;isOutput=true]b'
      name: emit
- stage: C
  dependsOn: [A]
  jobs:
  - job: C1
    steps: []
- stage: D
  dependsOn: []
  jobs:
  - job: D1
    steps: []
`

func TestReviewExplicitStageDependsOnReplacesTheImplicitEdge(t *testing.T) {
	graph := reviewGraph(t, stageEdgeSource)
	c := Scope{Level: "job", Stage: "C", Job: "C1", Step: -1}
	if got, ok := graph.ResolveOutput(c, "A", "A1", "emit.o"); !ok || got.Value != "a" {
		t.Fatalf("explicit A edge = %#v, %t", got, ok)
	}
	if _, ok := graph.ResolveOutput(c, "B", "B1", "emit.o"); ok {
		t.Fatal("explicit dependsOn [A] retained the implicit B edge")
	}
}

func TestReviewEmptyStageDependsOnRemovesTheImplicitEdge(t *testing.T) {
	graph := reviewGraph(t, stageEdgeSource)
	d := Scope{Level: "job", Stage: "D", Job: "D1", Step: -1}
	if _, ok := graph.ResolveOutput(d, "C", "C1", "emit.o"); ok {
		t.Fatal("explicit dependsOn [] retained an implicit stage edge")
	}
}

func TestReviewKelvinSignUsesOneCaseRule(t *testing.T) {
	pipeline := &ast.Pipeline{Shape: ast.ShapeSteps, Steps: list[ast.Step](&ast.ScriptStep{})}
	_, err := Evaluate(pipeline, Context{UIPlain: []PlainVariable{{Name: "Key", Value: "value"}}}, diag.NewCollector())
	// The Kelvin sign is outside the name grammar: ingest rejects it (TRL-VAR-0009),
	// so no second case rule can arise from it.
	var rejection *diag.Error
	if !errors.As(err, &rejection) || rejection.Finding.Code != "TRL-VAR-0009" {
		t.Fatalf("Kelvin-sign name accepted: err=%v", err)
	}
	graph, err := Evaluate(pipeline, Context{UIPlain: []PlainVariable{{Name: "Key", Value: "value"}}}, diag.NewCollector())
	if err != nil {
		t.Fatal(err)
	}
	scope := stepScope(0)
	_, resolved := graph.Resolve(scope, "Key", PhaseRuntime)
	result, err := graph.EvalRuntime(scope, "$[ variables.Key ]")
	if err != nil {
		t.Fatal(err)
	}
	if resolved == result.Null {
		t.Fatalf("Resolve found=%t but EvalRuntime null=%t", resolved, result.Null)
	}
}

// VAR-007 and VAR-008 of docs/spec/variable-precedence.md.

func TestReviewVAR007AllowedQueueValueStaysBelowJobYAML(t *testing.T) {
	pipeline := &ast.Pipeline{Shape: ast.ShapeJobs, Jobs: list[ast.Job](
		&ast.JobDef{Job: scalarNode("J"), Variables: variables(def("X", "job", false)), Steps: list[ast.Step](&ast.ScriptStep{Script: scalarNode("echo $(X)")})},
	)}
	graph, err := Evaluate(pipeline, Context{
		UIPlain: []PlainVariable{{Name: "X", Value: "ui", Settable: true}},
		Queue:   []QueueAssignment{{Name: "X", Value: "queued"}},
	}, diag.NewCollector())
	if err != nil {
		t.Fatal(err)
	}
	assertValue(t, graph, Scope{Level: "step", Job: "J", Step: 0}, "X", PhaseMacro, "job")
}

func TestReviewVAR008QueueValueRejectedWhenTheOrganizationLimitsIt(t *testing.T) {
	graph, err := Evaluate(&ast.Pipeline{Shape: ast.ShapeSteps}, Context{
		LimitQueueTimeVariables: true,
		UIPlain:                 []PlainVariable{{Name: "X", Value: "ui"}},
		Queue:                   []QueueAssignment{{Name: "X", Value: "queued"}},
	}, diag.NewCollector())
	var rejection *diag.Error
	if graph != nil || !errors.As(err, &rejection) || rejection.Finding.Code != "TRL-VAR-0010" {
		t.Fatalf("graph=%v err=%v", graph, err)
	}
}

func TestReviewHyphenatedVariableNamesAreAcceptedWithAWarning(t *testing.T) {
	graph, collector := reviewGraphWith(t, "variables:\n  good-name: value\nsteps:\n- bash: echo hi\n", Context{})
	assertValue(t, graph, stepScope(0), "good-name", PhaseMacro, "value")
	if !hasCode(collector.List(), "TRL-VAR-0018") {
		t.Fatalf("diagnostics = %v, want TRL-VAR-0018", codes(collector.List()))
	}
}

func TestReviewGroupIdentifiersKeepHyphens(t *testing.T) {
	graph, _ := reviewGraphWith(t, "variables:\n- group: my-variable-group\nsteps:\n- bash: echo hi\n", Context{Groups: []VariableGroup{
		{Name: "my-variable-group", Authorized: true, OrderProven: true, Plain: []PlainVariable{{Name: "myhello", Value: "hello"}}},
	}})
	assertValue(t, graph, stepScope(0), "myhello", PhaseMacro, "hello")
}

func TestReviewDynamicGroupIdentifierIsRejected(t *testing.T) {
	collector := diag.NewCollector()
	pipeline := parse.Source("group.yml", []byte("variables:\n- group: $(which)\nsteps:\n- bash: echo hi\n"), collector)
	graph, err := Evaluate(pipeline, Context{}, collector)
	var rejection *diag.Error
	if graph != nil || !errors.As(err, &rejection) || rejection.Finding.Code != "TRL-VAR-0009" {
		t.Fatalf("dynamic group name: graph=%v err=%v", graph, err)
	}
}
