package vars

import (
	"strings"
	"testing"
)

// deploymentSource wraps one deployment and a consumer job. environment and
// strategy are YAML fragments indented by the caller.
func deploymentSource(environment, strategy string) string {
	return "jobs:\n- deployment: A\n" + environment + "  strategy:\n" + strategy + `- job: B
  dependsOn: A
  steps:
  - bash: echo consume
`
}

const emitStep = `        - bash: echo '##vso[task.setvariable variable=o;isOutput=true]v'
          name: emit
`

func runOnceStrategy(hook string) string {
	return "    runOnce:\n      " + hook + ":\n        steps:\n" + emitStep
}

func rollingStrategy(hook string) string {
	return "    rolling:\n      maxParallel: 2\n      " + hook + ":\n        steps:\n" + emitStep
}

func consumerB() Scope { return Scope{Level: "job", Job: "B", Step: -1} }

func hasOutput(graph *Graph, key string) bool {
	_, ok := graph.ResolveOutput(consumerB(), "", "A", key)
	return ok
}

func TestReviewRunOnceWithoutResourceUsesTheJobPrefix(t *testing.T) {
	graph := reviewGraph(t, deploymentSource("  environment: staging\n", runOnceStrategy("deploy")))
	if !hasOutput(graph, "A.emit.o") {
		t.Fatalf("outputs = %#v", graph.Outputs)
	}
}

func TestReviewRunOnceOnAResourceUsesDeployPrefix(t *testing.T) {
	object := "  environment:\n    name: env1\n    resourceName: VM1\n    resourceType: virtualMachine\n"
	for _, form := range []struct{ name, environment string }{{"object form", object}, {"short form", "  environment: env1.VM1\n"}} {
		t.Run(form.name, func(t *testing.T) {
			graph := reviewGraph(t, deploymentSource(form.environment, runOnceStrategy("deploy")))
			if !hasOutput(graph, "Deploy_VM1.emit.o") {
				t.Fatalf("outputs = %#v", graph.Outputs)
			}
			if hasOutput(graph, "A.emit.o") {
				t.Fatal("the unqualified job key must not exist for a resource deployment")
			}
		})
	}
}

func TestReviewRunOnceOnADynamicResourceInventsNoKey(t *testing.T) {
	environment := "  environment:\n    name: env1\n    resourceName: $(resource)\n    resourceType: virtualMachine\n"
	graph, collector := reviewGraphWith(t, deploymentSource(environment, runOnceStrategy("deploy")), Context{})
	if len(graph.Outputs) != 0 {
		t.Fatalf("an output key was invented: %#v", graph.Outputs)
	}
	if !hasCode(collector.List(), "TRL-VAR-0015") {
		t.Fatalf("diagnostics = %v, want TRL-VAR-0015", codes(collector.List()))
	}
}

func TestReviewRollingUsesHookAndResource(t *testing.T) {
	graph := reviewGraph(t, deploymentSource("  environment: env1.vm1\n", rollingStrategy("deploy")))
	if !hasOutput(graph, "deploy_vm1.emit.o") {
		t.Fatalf("outputs = %#v", graph.Outputs)
	}
}

func TestReviewRollingPreDeployUsesTheHookName(t *testing.T) {
	graph := reviewGraph(t, deploymentSource("  environment: env1.vm1\n", rollingStrategy("preDeploy")))
	if !hasOutput(graph, "preDeploy_vm1.emit.o") {
		t.Fatalf("outputs = %#v", graph.Outputs)
	}
}

func TestReviewRollingWithoutAResourceInventsNoKey(t *testing.T) {
	graph, collector := reviewGraphWith(t, deploymentSource("  environment: staging\n", rollingStrategy("deploy")), Context{})
	if len(graph.Outputs) != 0 {
		t.Fatalf("an output key was invented: %#v", graph.Outputs)
	}
	if !hasCode(collector.List(), "TRL-VAR-0015") {
		t.Fatalf("diagnostics = %v, want TRL-VAR-0015", codes(collector.List()))
	}
}

func TestReviewDeploymentCreatesEveryRunOnceLifecycleUnit(t *testing.T) {
	var strategy strings.Builder
	strategy.WriteString("    runOnce:\n")
	for _, hook := range []string{"preDeploy", "deploy", "routeTraffic", "postRouteTraffic"} {
		strategy.WriteString("      " + hook + ":\n        steps:\n" + emitStep)
	}
	onStep := "          - bash: echo '##vso[task.setvariable variable=o;isOutput=true]v'\n            name: emit\n"
	strategy.WriteString("      on:\n        failure:\n          steps:\n" + onStep)
	strategy.WriteString("        success:\n          steps:\n" + onStep)
	graph := reviewGraph(t, deploymentSource("  environment: staging\n", strategy.String()))
	for _, key := range []string{"preDeploy.emit.o", "A.emit.o", "routeTraffic.emit.o", "postRouteTraffic.emit.o", "on.failure.emit.o", "on.success.emit.o"} {
		if !hasOutput(graph, key) {
			t.Errorf("missing runOnce output %s", key)
		}
	}
}

func TestReviewCanaryHooksRunOncePerIncrement(t *testing.T) {
	strategy := "    canary:\n      increments: [10, 20]\n      deploy:\n        steps:\n" + emitStep
	graph := reviewGraph(t, deploymentSource("  environment: staging\n", strategy))
	for _, key := range []string{"deploy_10.emit.o", "deploy_20.emit.o"} {
		if !hasOutput(graph, key) {
			t.Errorf("missing canary output %s", key)
		}
	}
}

func TestReviewRootStrategyMatrixCreatesOneTablePerLeg(t *testing.T) {
	graph := reviewGraph(t, `strategy:
  matrix:
    first:
      X: one
    second:
      X: two
steps:
- bash: echo $(X)
`)
	assertValue(t, graph, Scope{Level: "step", Step: 0, Leg: "first"}, "X", PhaseMacro, "one")
	assertValue(t, graph, Scope{Level: "step", Step: 0, Leg: "second"}, "X", PhaseMacro, "two")
}
