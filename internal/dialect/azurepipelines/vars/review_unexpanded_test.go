package vars

import (
	"errors"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
)

// withInserts is a typed-mapping tail holding one unexpanded key directive.
func withInserts[T any]() ast.Tail[T] {
	return ast.Tail[T]{Inserts: []*ast.Branch[*T]{{}}}
}

func insertion() ast.Insertable { return ast.Insertable{Insertion: &ast.Directive{}} }

func mustReject(t *testing.T, pipeline *ast.Pipeline) {
	t.Helper()
	graph, err := Evaluate(pipeline, Context{}, diag.NewCollector())
	var rejection *diag.Error
	if graph != nil || !errors.As(err, &rejection) || rejection.Finding.Code != "TRL-VAR-0014" {
		t.Fatalf("unexpanded node accepted: graph=%v err=%v", graph, err)
	}
}

func stepsPipeline(steps ...ast.Step) *ast.Pipeline {
	return &ast.Pipeline{Shape: ast.ShapeSteps, Steps: list(steps...)}
}

func deploymentPipeline(deployment *ast.Deployment) *ast.Pipeline {
	return &ast.Pipeline{Shape: ast.ShapeJobs, Jobs: list[ast.Job](deployment)}
}

func TestReviewRootKeyInsertionIsRejected(t *testing.T) {
	collector := diag.NewCollector()
	pipeline := parse.Source("root.yml", []byte("${{ if true }}:\n  variables:\n    x: one\nsteps:\n- script: echo $(x)\n"), collector)
	if len(pipeline.Inserts) == 0 {
		t.Fatal("the parser kept no root insertion; the regression input is stale")
	}
	graph, err := Evaluate(pipeline, Context{}, collector)
	var rejection *diag.Error
	if graph != nil || !errors.As(err, &rejection) || rejection.Finding.Code != "TRL-VAR-0014" {
		t.Fatalf("root insertion silently omitted: graph=%v err=%v", graph, err)
	}
}

func TestReviewRootTailInsertionIsRejected(t *testing.T) {
	pipeline := stepsPipeline(&ast.ScriptStep{})
	pipeline.Tail = withInserts[ast.Pipeline]()
	mustReject(t, pipeline)
}

func TestReviewTemplateReferencesAreRejected(t *testing.T) {
	cases := map[string]*ast.Pipeline{ //trasloco:unordered-ok subtests are independent
		"job":      {Shape: ast.ShapeJobs, Jobs: list[ast.Job](&ast.JobTemplate{})},
		"stage":    {Shape: ast.ShapeStages, Stages: list[ast.Stage](&ast.StageTemplate{})},
		"step":     stepsPipeline(&ast.StepTemplate{}),
		"variable": {Shape: ast.ShapeSteps, Variables: variables(&ast.VariableTemplate{})},
	}
	for name, pipeline := range cases { //trasloco:unordered-ok subtests are independent
		t.Run(name, func(t *testing.T) { mustReject(t, pipeline) })
	}
}

func TestReviewStepKindsWithUnexpandedKeysAreRejected(t *testing.T) {
	steps := map[string]ast.Step{ //trasloco:unordered-ok subtests are independent
		"script":        &ast.ScriptStep{Tail: withInserts[ast.ScriptStep]()},
		"task":          &ast.TaskStep{Tail: withInserts[ast.TaskStep]()},
		"checkout":      &ast.CheckoutStep{Tail: withInserts[ast.CheckoutStep]()},
		"download":      &ast.DownloadStep{Tail: withInserts[ast.DownloadStep]()},
		"downloadBuild": &ast.DownloadBuildStep{Tail: withInserts[ast.DownloadBuildStep]()},
		"getPackage":    &ast.GetPackageStep{Tail: withInserts[ast.GetPackageStep]()},
		"publish":       &ast.PublishStep{Tail: withInserts[ast.PublishStep]()},
		"reviewApp":     &ast.ReviewAppStep{Tail: withInserts[ast.ReviewAppStep]()},
	}
	for name, step := range steps { //trasloco:unordered-ok subtests are independent
		t.Run(name, func(t *testing.T) { mustReject(t, stepsPipeline(step)) })
	}
}

func TestReviewStepEnvAndTargetInsertionsAreRejected(t *testing.T) {
	t.Run("env insertion", func(t *testing.T) {
		mustReject(t, stepsPipeline(&ast.CheckoutStep{StepProps: ast.StepProps{Env: &ast.Map[*ast.Scalar]{Insertable: insertion()}}}))
	})
	t.Run("conditional env entry", func(t *testing.T) {
		env := &ast.Map[*ast.Scalar]{Entries: []ast.MapEntry[*ast.Scalar]{{Cond: &ast.MapCond[*ast.Scalar]{}}}}
		mustReject(t, stepsPipeline(&ast.PublishStep{StepProps: ast.StepProps{Env: env}}))
	})
	t.Run("target insertion", func(t *testing.T) {
		mustReject(t, stepsPipeline(&ast.DownloadStep{StepProps: ast.StepProps{Target: &ast.Target{Insertable: insertion()}}}))
	})
	t.Run("target key insertion", func(t *testing.T) {
		mustReject(t, stepsPipeline(&ast.ScriptStep{StepProps: ast.StepProps{Target: &ast.Target{Tail: withInserts[ast.Target]()}}}))
	})
	t.Run("settableVariables insertion", func(t *testing.T) {
		target := &ast.Target{SettableVariables: &ast.SettableVariables{Insertable: insertion()}}
		mustReject(t, stepsPipeline(&ast.TaskStep{StepProps: ast.StepProps{Target: target}}))
	})
}

func TestReviewConditionalListItemsAreRejected(t *testing.T) {
	t.Run("step", func(t *testing.T) {
		mustReject(t, &ast.Pipeline{Shape: ast.ShapeSteps, Steps: &ast.List[ast.Step]{Items: []ast.ListItem[ast.Step]{{Cond: &ast.ListCond[ast.Step]{}}}}})
	})
	t.Run("whole-value step insertion", func(t *testing.T) {
		mustReject(t, &ast.Pipeline{Shape: ast.ShapeSteps, Steps: &ast.List[ast.Step]{Items: []ast.ListItem[ast.Step]{{Insert: &ast.Directive{}}}}})
	})
	t.Run("job", func(t *testing.T) {
		mustReject(t, &ast.Pipeline{Shape: ast.ShapeJobs, Jobs: &ast.List[ast.Job]{Items: []ast.ListItem[ast.Job]{{Cond: &ast.ListCond[ast.Job]{}}}}})
	})
	t.Run("stage", func(t *testing.T) {
		mustReject(t, &ast.Pipeline{Shape: ast.ShapeStages, Stages: &ast.List[ast.Stage]{Items: []ast.ListItem[ast.Stage]{{Cond: &ast.ListCond[ast.Stage]{}}}}})
	})
}

func TestReviewStageAndJobKeyInsertionsAreRejected(t *testing.T) {
	t.Run("stage", func(t *testing.T) {
		mustReject(t, &ast.Pipeline{Shape: ast.ShapeStages, Stages: list[ast.Stage](&ast.StageDef{Stage: scalarNode("S"), Tail: withInserts[ast.StageDef]()})})
	})
	t.Run("job", func(t *testing.T) {
		mustReject(t, &ast.Pipeline{Shape: ast.ShapeJobs, Jobs: list[ast.Job](&ast.JobDef{Job: scalarNode("J"), Tail: withInserts[ast.JobDef]()})})
	})
	t.Run("deployment", func(t *testing.T) {
		mustReject(t, deploymentPipeline(&ast.Deployment{Deployment: scalarNode("D"), Tail: withInserts[ast.Deployment]()}))
	})
}

func TestReviewEnvironmentInsertionsAreRejected(t *testing.T) {
	t.Run("whole-value insertion", func(t *testing.T) {
		mustReject(t, deploymentPipeline(&ast.Deployment{Deployment: scalarNode("D"), Environment: &ast.Environment{Insertable: insertion()}}))
	})
	t.Run("key insertion", func(t *testing.T) {
		mustReject(t, deploymentPipeline(&ast.Deployment{Deployment: scalarNode("D"), Environment: &ast.Environment{Name: scalarNode("e"), Tail: withInserts[ast.Environment]()}}))
	})
	t.Run("template expression in the resource", func(t *testing.T) {
		mustReject(t, deploymentPipeline(&ast.Deployment{Deployment: scalarNode("D"), Environment: &ast.Environment{Name: scalarNode("e"), ResourceName: scalarNode("${{ parameters.vm }}")}}))
	})
}

func TestReviewStrategyInsertionsAreRejected(t *testing.T) {
	t.Run("job strategy key insertion", func(t *testing.T) {
		mustReject(t, &ast.Pipeline{Shape: ast.ShapeSteps, Strategy: &ast.Strategy{Tail: withInserts[ast.Strategy]()}})
	})
	t.Run("job strategy insertion", func(t *testing.T) {
		mustReject(t, &ast.Pipeline{Shape: ast.ShapeSteps, Strategy: &ast.Strategy{Insertable: insertion()}})
	})
	t.Run("deployment strategy key insertion", func(t *testing.T) {
		mustReject(t, deploymentPipeline(&ast.Deployment{Deployment: scalarNode("D"), Strategy: &ast.DeploymentStrategy{Tail: withInserts[ast.DeploymentStrategy]()}}))
	})
	t.Run("lifecycle key insertion", func(t *testing.T) {
		mustReject(t, deploymentPipeline(&ast.Deployment{Deployment: scalarNode("D"), Strategy: &ast.DeploymentStrategy{RunOnce: &ast.Lifecycle{Tail: withInserts[ast.Lifecycle]()}}}))
	})
	t.Run("hook key insertion", func(t *testing.T) {
		mustReject(t, deploymentPipeline(&ast.Deployment{Deployment: scalarNode("D"), Strategy: &ast.DeploymentStrategy{RunOnce: &ast.Lifecycle{Deploy: &ast.Hook{Tail: withInserts[ast.Hook]()}}}}))
	})
	t.Run("on-hooks key insertion", func(t *testing.T) {
		mustReject(t, deploymentPipeline(&ast.Deployment{Deployment: scalarNode("D"), Strategy: &ast.DeploymentStrategy{RunOnce: &ast.Lifecycle{On: &ast.OnHooks{Tail: withInserts[ast.OnHooks]()}}}}))
	})
	t.Run("step inside a hook", func(t *testing.T) {
		hook := &ast.Hook{Steps: list[ast.Step](&ast.PublishStep{Tail: withInserts[ast.PublishStep]()})}
		mustReject(t, deploymentPipeline(&ast.Deployment{Deployment: scalarNode("D"), Strategy: &ast.DeploymentStrategy{Rolling: &ast.Lifecycle{Deploy: hook}}}))
	})
}

func TestReviewVariableInsertionsAreRejected(t *testing.T) {
	t.Run("definition key insertion", func(t *testing.T) {
		mustReject(t, &ast.Pipeline{Shape: ast.ShapeSteps, Variables: variables(&ast.VariableDef{Name: scalarNode("x"), Tail: withInserts[ast.VariableDef]()})})
	})
	t.Run("group key insertion", func(t *testing.T) {
		mustReject(t, &ast.Pipeline{Shape: ast.ShapeSteps, Variables: variables(&ast.VariableGroup{Group: scalarNode("G"), Tail: withInserts[ast.VariableGroup]()})})
	})
	t.Run("whole-value insertion", func(t *testing.T) {
		mustReject(t, &ast.Pipeline{Shape: ast.ShapeSteps, Variables: &ast.Variables{Form: ast.VariablesInsertion}})
	})
	t.Run("conditional map entry", func(t *testing.T) {
		vars := &ast.Variables{Form: ast.VariablesMap, Map: &ast.Map[*ast.Scalar]{Entries: []ast.MapEntry[*ast.Scalar]{{Cond: &ast.MapCond[*ast.Scalar]{}}}}}
		mustReject(t, &ast.Pipeline{Shape: ast.ShapeSteps, Variables: vars})
	})
}

func TestReviewResidualTemplateExpressionsAreRejected(t *testing.T) {
	t.Run("variable value", func(t *testing.T) {
		mustReject(t, &ast.Pipeline{Shape: ast.ShapeSteps, Variables: variables(def("x", "${{ variables.y }}", false))})
	})
	t.Run("variable name", func(t *testing.T) {
		mustReject(t, &ast.Pipeline{Shape: ast.ShapeSteps, Variables: variables(def("${{ variables.k }}", "v", false))})
	})
	t.Run("script text", func(t *testing.T) {
		mustReject(t, stepsPipeline(&ast.ScriptStep{Script: scalarNode("echo ${{ parameters.x }}")}))
	})
	t.Run("dynamic extra key", func(t *testing.T) {
		step := &ast.ScriptStep{Tail: ast.Tail[ast.ScriptStep]{Extra: []*ast.Extra{{Key: scalarNode("${{ parameters.key }}"), Value: &ast.Any{Kind: ast.AnyScalar}}}}}
		mustReject(t, stepsPipeline(step))
	})
	t.Run("directive inside an extra value", func(t *testing.T) {
		value := &ast.Any{Kind: ast.AnyMap, Entries: []*ast.AnyEntry{{Directive: &ast.Directive{}}}}
		step := &ast.ScriptStep{Tail: ast.Tail[ast.ScriptStep]{Extra: []*ast.Extra{{Key: scalarNode("custom"), Value: value}}}}
		mustReject(t, stepsPipeline(step))
	})
}

func TestReviewExpandedPipelineIsAccepted(t *testing.T) {
	graph, err := Evaluate(stepsPipeline(&ast.ScriptStep{Script: scalarNode("echo $(x)")}), Context{}, diag.NewCollector())
	if err != nil || graph == nil {
		t.Fatalf("fully expanded pipeline rejected: %v", err)
	}
}
