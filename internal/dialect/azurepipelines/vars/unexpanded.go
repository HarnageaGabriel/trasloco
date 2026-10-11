package vars

import (
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
)

// unexpandedCheck enforces the resolver contract: the evaluator receives an AST
// in which every template reference and insertion has been expanded. Anything
// left over is diagnosed instead of being silently dropped. The walk covers
// every node family that can carry an insertion: the pipeline root, stages,
// jobs, deployments, environments, strategies, lifecycles, hooks, variables,
// matrix legs, targets and every step kind.
type unexpandedCheck struct {
	findings *diag.Collector
	first    *diag.Diagnostic
}

func (c *unexpandedCheck) report(source diag.Source, what string) {
	d := diag.MustNew("TRL-VAR-0014", diag.WithSource(source), diag.WithArgs(map[string]any{"node": what}))
	c.findings.Add(d)
	if c.first == nil {
		c.first = &d
	}
}

func rejectUnexpanded(pipeline *ast.Pipeline, findings *diag.Collector) error {
	c := &unexpandedCheck{findings: findings}
	tail(c, pipeline.Position(), &pipeline.Tail)
	if pipeline.Extends != nil {
		c.report(pipeline.Extends.Position(), "extends template")
	}
	c.variables(pipeline.Variables)
	c.stages(pipeline.Stages)
	c.jobs(pipeline.Jobs)
	c.steps(pipeline.Steps)
	c.strategy(pipeline.Strategy)
	if c.first != nil {
		return &diag.Error{Finding: *c.first}
	}
	return nil
}

// tail reports the key insertions and the dynamic or directive-bearing extra
// keys a typed mapping node still holds.
func tail[T any](c *unexpandedCheck, source diag.Source, t *ast.Tail[T]) {
	if len(t.Inserts) > 0 {
		c.report(source, "conditional key insertion")
	}
	for _, extra := range t.Extra {
		if extra == nil {
			continue
		}
		if extra.Key != nil && strings.Contains(extra.Key.Value, "${{") {
			c.report(source, "dynamic key")
		}
		if holdsDirective(extra.Value) {
			c.report(source, "directive in key value")
		}
	}
}

func holdsDirective(value *ast.Any) bool {
	if value == nil {
		return false
	}
	for _, item := range value.Items {
		if holdsDirective(item) {
			return true
		}
	}
	for _, entry := range value.Entries {
		if entry != nil && (entry.Directive != nil || holdsDirective(entry.Value)) {
			return true
		}
	}
	return false
}

// text reports a scalar that still contains a template expression.
func (c *unexpandedCheck) text(s *ast.Scalar, what string) {
	if s != nil && strings.Contains(s.Value, "${{") {
		c.report(s.Position(), what)
	}
}

func (c *unexpandedCheck) variables(v *ast.Variables) {
	if v == nil {
		return
	}
	if v.Insertion != nil || v.Form == ast.VariablesInsertion {
		c.report(v.Position(), "variables insertion")
	}
	if v.Map != nil {
		c.mapNode(v.Map.Insertion, v.Map.Position(), "variables map insertion")
		for _, entry := range v.Map.Entries {
			if entry.Cond != nil {
				c.report(v.Map.Position(), "conditional variable")
				continue
			}
			c.text(entry.Key, "template expression in variable name")
			c.text(entry.Value, "template expression in variable value")
		}
	}
	if v.List != nil {
		c.list(v.List.Insertion, v.List.Position(), "variables list insertion")
		for _, item := range v.List.Items {
			c.item(item.Cond != nil || item.Insert != nil, v.List.Position(), "conditional variable")
			switch variable := item.Value.(type) {
			case *ast.VariableTemplate:
				c.report(variable.Position(), "variable template reference")
			case *ast.VariableDef:
				tail(c, variable.Position(), &variable.Tail)
				c.text(variable.Name, "template expression in variable name")
				c.text(variable.Value, "template expression in variable value")
			case *ast.VariableGroup:
				tail(c, variable.Position(), &variable.Tail)
				c.text(variable.Group, "template expression in variable group name")
			}
		}
	}
}

func (c *unexpandedCheck) mapNode(insertion *ast.Directive, source diag.Source, what string) {
	if insertion != nil {
		c.report(source, what)
	}
}

func (c *unexpandedCheck) list(insertion *ast.Directive, source diag.Source, what string) {
	if insertion != nil {
		c.report(source, what)
	}
}

func (c *unexpandedCheck) item(unexpanded bool, source diag.Source, what string) {
	if unexpanded {
		c.report(source, what)
	}
}

func (c *unexpandedCheck) stages(list *ast.List[ast.Stage]) {
	if list == nil {
		return
	}
	c.list(list.Insertion, list.Position(), "stages insertion")
	for _, item := range list.Items {
		c.item(item.Cond != nil || item.Insert != nil, list.Position(), "conditional stage")
		switch stage := item.Value.(type) {
		case *ast.StageTemplate:
			c.report(stage.Position(), "stage template reference")
		case *ast.StageDef:
			tail(c, stage.Position(), &stage.Tail)
			c.text(stage.Stage, "template expression in stage name")
			c.dependsOn(stage.DependsOn)
			c.variables(stage.Variables)
			c.jobs(stage.Jobs)
		}
	}
}

func (c *unexpandedCheck) dependsOn(d *ast.DependsOn) {
	if d == nil {
		return
	}
	if d.Insertion != nil {
		c.report(d.Position(), "dependsOn insertion")
	}
	if d.Names != nil {
		c.list(d.Names.Insertion, d.Names.Position(), "dependsOn list insertion")
		for _, item := range d.Names.Items {
			c.item(item.Cond != nil || item.Insert != nil, d.Position(), "conditional dependsOn entry")
			c.text(item.Value, "template expression in dependsOn")
		}
	}
}

func (c *unexpandedCheck) jobs(list *ast.List[ast.Job]) {
	if list == nil {
		return
	}
	c.list(list.Insertion, list.Position(), "jobs insertion")
	for _, item := range list.Items {
		c.item(item.Cond != nil || item.Insert != nil, list.Position(), "conditional job")
		switch job := item.Value.(type) {
		case *ast.JobTemplate:
			c.report(job.Position(), "job template reference")
		case *ast.JobDef:
			tail(c, job.Position(), &job.Tail)
			c.text(job.Job, "template expression in job name")
			c.dependsOn(job.DependsOn)
			c.variables(job.Variables)
			c.strategy(job.Strategy)
			c.steps(job.Steps)
		case *ast.Deployment:
			tail(c, job.Position(), &job.Tail)
			c.text(job.Deployment, "template expression in deployment name")
			c.dependsOn(job.DependsOn)
			c.variables(job.Variables)
			c.environment(job.Environment)
			c.deploymentStrategy(job.Strategy)
		}
	}
}

func (c *unexpandedCheck) environment(e *ast.Environment) {
	if e == nil {
		return
	}
	if e.Insertion != nil {
		c.report(e.Position(), "environment insertion")
	}
	tail(c, e.Position(), &e.Tail)
	c.text(e.Name, "template expression in environment name")
	c.text(e.ResourceName, "template expression in environment resource")
}

func (c *unexpandedCheck) strategy(s *ast.Strategy) {
	if s == nil {
		return
	}
	if s.Insertion != nil {
		c.report(s.Position(), "strategy insertion")
	}
	tail(c, s.Position(), &s.Tail)
	if s.Matrix != nil {
		c.mapNode(s.Matrix.Insertion, s.Matrix.Position(), "matrix insertion")
		for _, entry := range s.Matrix.Entries {
			if entry.Cond != nil {
				c.report(s.Matrix.Position(), "conditional matrix leg")
			}
			c.text(entry.Key, "template expression in matrix leg")
			if entry.Value != nil {
				c.mapNode(entry.Value.Insertion, entry.Value.Position(), "matrix leg insertion")
				for _, inner := range entry.Value.Entries {
					if inner.Cond != nil {
						c.report(entry.Value.Position(), "conditional matrix variable")
					}
					c.text(inner.Key, "template expression in matrix variable")
					c.text(inner.Value, "template expression in matrix variable")
				}
			}
		}
	}
}

func (c *unexpandedCheck) deploymentStrategy(s *ast.DeploymentStrategy) {
	if s == nil {
		return
	}
	if s.Insertion != nil {
		c.report(s.Position(), "deployment strategy insertion")
	}
	tail(c, s.Position(), &s.Tail)
	for _, lifecycle := range []*ast.Lifecycle{s.RunOnce, s.Rolling, s.Canary} {
		if lifecycle == nil {
			continue
		}
		if lifecycle.Insertion != nil {
			c.report(lifecycle.Position(), "lifecycle insertion")
		}
		tail(c, lifecycle.Position(), &lifecycle.Tail)
		if lifecycle.Increments != nil {
			c.list(lifecycle.Increments.Insertion, lifecycle.Increments.Position(), "increments insertion")
			for _, item := range lifecycle.Increments.Items {
				c.item(item.Cond != nil || item.Insert != nil, lifecycle.Position(), "conditional increment")
			}
		}
		hooks := []*ast.Hook{lifecycle.PreDeploy, lifecycle.Deploy, lifecycle.RouteTraffic, lifecycle.PostRouteTraffic}
		if lifecycle.On != nil {
			if lifecycle.On.Insertion != nil {
				c.report(lifecycle.On.Position(), "on-hooks insertion")
			}
			tail(c, lifecycle.On.Position(), &lifecycle.On.Tail)
			hooks = append(hooks, lifecycle.On.Failure, lifecycle.On.Success)
		}
		for _, hook := range hooks {
			if hook == nil {
				continue
			}
			if hook.Insertion != nil {
				c.report(hook.Position(), "hook insertion")
			}
			tail(c, hook.Position(), &hook.Tail)
			c.steps(hook.Steps)
		}
	}
}

func (c *unexpandedCheck) steps(list *ast.List[ast.Step]) {
	if list == nil {
		return
	}
	c.list(list.Insertion, list.Position(), "steps insertion")
	for _, item := range list.Items {
		c.item(item.Cond != nil || item.Insert != nil, list.Position(), "conditional step")
		if item.Value == nil {
			continue
		}
		if template, ok := item.Value.(*ast.StepTemplate); ok {
			c.report(template.Position(), "step template reference")
			continue
		}
		props := stepProps(item.Value)
		c.stepProps(item.Value.Position(), props)
		switch step := item.Value.(type) {
		case *ast.ScriptStep:
			tail(c, step.Position(), &step.Tail)
			c.text(step.Script, "template expression in script")
		case *ast.TaskStep:
			tail(c, step.Position(), &step.Tail)
			if step.Inputs != nil {
				c.mapNode(step.Inputs.Insertion, step.Position(), "task inputs insertion")
				for _, entry := range step.Inputs.Entries {
					if entry.Cond != nil {
						c.report(step.Position(), "conditional task input")
						continue
					}
					c.text(entry.Value, "template expression in task input")
				}
			}
		case *ast.CheckoutStep:
			tail(c, step.Position(), &step.Tail)
		case *ast.DownloadStep:
			tail(c, step.Position(), &step.Tail)
		case *ast.DownloadBuildStep:
			tail(c, step.Position(), &step.Tail)
		case *ast.GetPackageStep:
			tail(c, step.Position(), &step.Tail)
		case *ast.PublishStep:
			tail(c, step.Position(), &step.Tail)
		case *ast.ReviewAppStep:
			tail(c, step.Position(), &step.Tail)
		}
	}
}

// stepProps checks the properties every step kind shares: env and target.
func (c *unexpandedCheck) stepProps(source diag.Source, props ast.StepProps) {
	c.text(props.Name, "template expression in step name")
	if props.Env != nil {
		c.mapNode(props.Env.Insertion, source, "env insertion")
		for _, entry := range props.Env.Entries {
			if entry.Cond != nil {
				c.report(source, "conditional env entry")
				continue
			}
			c.text(entry.Key, "template expression in env name")
			c.text(entry.Value, "template expression in env value")
		}
	}
	target := props.Target
	if target == nil {
		return
	}
	if target.Insertion != nil {
		c.report(source, "target insertion")
	}
	tail(c, source, &target.Tail)
	if settable := target.SettableVariables; settable != nil {
		if settable.Insertion != nil {
			c.report(source, "settableVariables insertion")
		}
		if settable.List != nil {
			c.list(settable.List.Insertion, source, "settableVariables list insertion")
			for _, item := range settable.List.Items {
				c.item(item.Cond != nil || item.Insert != nil, source, "conditional settableVariables entry")
			}
		}
	}
}
