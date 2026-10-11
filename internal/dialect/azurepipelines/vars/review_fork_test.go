package vars

import (
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
)

// forkPolicyGraph builds a graph with one secret from every source: UI, an
// imported variable group, a queue-time secret slot and a provider-backed output.
func forkPolicyGraph(t *testing.T, fork, secretsAvailable bool) (*Graph, Scope) {
	t.Helper()
	pipeline := &ast.Pipeline{Shape: ast.ShapeJobs, Variables: variables(group("G")), Jobs: list[ast.Job](
		&ast.JobDef{Job: scalarNode("A"), Steps: list[ast.Step](&ast.ScriptStep{Script: scalarNode("echo a")})},
		&ast.JobDef{Job: scalarNode("B"), DependsOn: &ast.DependsOn{Names: list(scalarNode("A"))}, Steps: list[ast.Step](&ast.ScriptStep{Script: scalarNode("echo b")})},
	)}
	graph, err := Evaluate(pipeline, Context{
		ForkPullRequest:         fork,
		SecretsAvailableToForks: secretsAvailable,
		UIPlain:                 []PlainVariable{{Name: "plain", Value: "visible"}},
		UISecrets:               []SecretVariable{{Name: "uiSecret", Provider: "ado", ID: "ui/1"}},
		Groups:                  []VariableGroup{{Name: "G", Authorized: true, OrderProven: true, Secrets: []SecretVariable{{Name: "groupSecret", Provider: "ado", ID: "group/1"}}}},
		Queue:                   []QueueAssignment{{Name: "queued", Value: "ignored", Secret: true}},
	}, diag.NewCollector())
	if err != nil {
		t.Fatal(err)
	}
	graph.Outputs = append(graph.Outputs, Output{Job: "A", StepName: "emit", Name: "token", Candidate: Candidate{Secret: true, SecretRef: &SecretRef{Provider: "ado", ID: "output/1"}, Available: true}})
	return graph, Scope{Level: "step", Job: "B", Step: 0}
}

func TestReviewForkBuildWithoutSecretPolicyWithholdsEverySecretSource(t *testing.T) {
	graph, scope := forkPolicyGraph(t, true, false)
	for _, name := range []string{"uiSecret", "groupSecret", "queued"} {
		t.Run(name, func(t *testing.T) {
			if candidate, ok := graph.Resolve(scope, name, PhaseRuntime); ok {
				t.Fatalf("fork build kept %s: %#v", name, candidate)
			}
		})
	}
	t.Run("provider output", func(t *testing.T) {
		if output, ok := graph.ResolveOutput(Scope{Level: "job", Job: "B", Step: -1}, "", "A", "emit.token"); ok {
			t.Fatalf("fork build kept the provider output: %#v", output)
		}
	})
	t.Run("expansion stays literal and unmasked", func(t *testing.T) {
		expanded := graph.Expand(scope, "$(uiSecret)$(groupSecret)")
		if expanded.Masked || expanded.Text != "$(uiSecret)$(groupSecret)" {
			t.Fatalf("Expand = %#v", expanded)
		}
	})
	t.Run("plain variables are unaffected", func(t *testing.T) {
		assertValue(t, graph, scope, "plain", PhaseRuntime, "visible")
	})
}

func TestReviewForkBuildWithSecretPolicyKeepsSecretReferences(t *testing.T) {
	graph, scope := forkPolicyGraph(t, true, true)
	for _, name := range []string{"uiSecret", "groupSecret", "queued"} {
		t.Run(name, func(t *testing.T) {
			candidate, ok := graph.Resolve(scope, name, PhaseRuntime)
			if !ok || !candidate.Secret || candidate.SecretRef == nil || candidate.Value != "" {
				t.Fatalf("Resolve(%s) = %#v, %t", name, candidate, ok)
			}
		})
	}
	t.Run("provider output", func(t *testing.T) {
		output, ok := graph.ResolveOutput(Scope{Level: "job", Job: "B", Step: -1}, "", "A", "emit.token")
		if !ok || !output.Secret || output.Value != "" {
			t.Fatalf("ResolveOutput = %#v, %t", output, ok)
		}
	})
}

func TestReviewNonForkBuildIgnoresTheForkSecretPolicy(t *testing.T) {
	graph, scope := forkPolicyGraph(t, false, false)
	if candidate, ok := graph.Resolve(scope, "uiSecret", PhaseRuntime); !ok || !candidate.Secret {
		t.Fatalf("non-fork build lost its UI secret: %#v, %t", candidate, ok)
	}
}

func TestReviewForkWithholdingKeepsLoggingCommandSecrets(t *testing.T) {
	pipeline := &ast.Pipeline{Shape: ast.ShapeJobs, Jobs: list[ast.Job](
		&ast.JobDef{Job: scalarNode("A"), Steps: list[ast.Step](&ast.ScriptStep{Script: scalarNode("echo '##vso[task.setvariable variable=o;isOutput=true;issecret=true]s'"), StepProps: ast.StepProps{Name: scalarNode("emit")}})},
		&ast.JobDef{Job: scalarNode("B"), DependsOn: &ast.DependsOn{Names: list(scalarNode("A"))}, Steps: list[ast.Step](&ast.ScriptStep{})},
	)}
	graph, err := Evaluate(pipeline, Context{ForkPullRequest: true, AgentOS: AgentOSLinux}, diag.NewCollector())
	if err != nil {
		t.Fatal(err)
	}
	if output, ok := graph.ResolveOutput(Scope{Level: "job", Job: "B", Step: -1}, "", "A", "emit.o"); !ok || !output.Secret || output.Value != "" {
		t.Fatalf("step-declared secret output = %#v, %t", output, ok)
	}
}
