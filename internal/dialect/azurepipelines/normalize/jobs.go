package normalize

import (
	"strconv"
	"strings"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/hosted"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

// maxParallelLegs caps `strategy.parallel` expansion; a larger count becomes an
// opaque matrix instead of allocating one leg per replica.
const maxParallelLegs = 256

func (c *conv) job(source *ast.JobDef, inherited *ast.Pool) ir.Job {
	id := scalarOr(source.Job, "job")
	a := c.newAcc(source.Source).in(source.Chain)
	job := ir.NewJob(id, scalarOr(source.DisplayName, id))
	job.Needs = dependencyNames(source.DependsOn)
	job.If = c.condition(source.Condition, a)
	job.AllowFailure = c.flag(a, source.ContinueOnError, "job", "continueOnError")
	job.Variables = c.variables(source.Variables, ir.ScopeJob, id)
	pool := firstPool(source.Pool, inherited)
	job.Runner = c.runner(pool, pool != nil, a)
	job.Timeout = c.jobTimeout(a, source.TimeoutInMinutes, "job", pool, job.Runner)
	c.pushMacroScope(source.Variables)
	defer c.popMacroScope()
	job.Container = c.container(source.Container, a)
	job.Services = c.services(source.Services, source.ServiceSpecs, a)
	job.Matrix = c.matrix(source.Strategy, a)
	defer c.useRunner(job.Runner)()
	job.Steps = c.steps(source.Steps, false, "")
	dedupeStepIDs(job.Steps, &c.work)
	c.jobLosses(a, source.CancelTimeoutInMinutes, source.Workspace, source.Uses, source.TemplateContext, source.Extra)
	job.Metadata = a.metadata()
	rollUpJob(&job)
	return job
}

func (c *conv) jobFromRoot(root *ast.Pipeline) ir.Job {
	a := c.newAcc(root.Source).in(root.Chain)
	job := ir.NewJob(implicitID, "Default")
	job.Runner = c.runner(root.Pool, root.Pool != nil, a)
	job.Timeout = c.jobTimeout(a, nil, "job", root.Pool, job.Runner)
	job.Container = c.container(root.Container, a)
	job.Services = c.services(root.Services, root.ServiceSpecs, a)
	job.Matrix = c.matrix(root.Strategy, a)
	job.AllowFailure = c.flag(a, root.ContinueOnError, "job", "continueOnError")
	defer c.useRunner(job.Runner)()
	job.Steps = c.steps(root.Steps, false, "")
	dedupeStepIDs(job.Steps, &c.work)
	if root.Workspace != nil {
		a.keep("workspace.clean", scalarEnum(root.Workspace.Clean))
		a.lose(codeExt, "job", "workspace", root.Workspace.Source, "workspace cleanup has no IR field")
	}
	job.Metadata = a.metadata()
	rollUpJob(&job)
	return job
}

func (c *conv) deployment(source *ast.Deployment, inherited *ast.Pool) ir.Job {
	id := scalarOr(source.Deployment, "deployment")
	a := c.newAcc(source.Source).in(source.Chain)
	job := ir.NewJob(id, scalarOr(source.DisplayName, id))
	job.Needs = dependencyNames(source.DependsOn)
	job.If = c.condition(source.Condition, a)
	job.AllowFailure = c.flag(a, source.ContinueOnError, "job", "continueOnError")
	job.Variables = c.variables(source.Variables, ir.ScopeJob, id)
	pool := firstPool(source.Pool, inherited)
	job.Runner = c.runner(pool, pool != nil || deploymentHookPoolDeclared(source.Strategy), a)
	job.Timeout = c.jobTimeout(a, source.TimeoutInMinutes, "deployment", pool, job.Runner)
	c.pushMacroScope(source.Variables)
	defer c.popMacroScope()
	defer c.useRunner(job.Runner)()
	job.Container = c.container(source.Container, a)
	job.Services = c.services(source.Services, source.ServiceSpecs, a)
	environment := "environment"
	if source.Environment != nil {
		environment = c.environment(source.Environment, a)
	}
	strategy, lifecycle := c.deploymentLifecycle(source.Strategy, a)
	job.Deployment = &ir.DeploymentIntent{Environment: environment, Strategy: strategy}
	if lifecycle != nil {
		job.Steps = c.lifecycleSteps(lifecycle, a)
	}
	if len(job.Steps) == 0 {
		job.Steps = []ir.Step{}
	}
	dedupeStepIDs(job.Steps, &c.work)
	c.jobLosses(a, source.CancelTimeoutInMinutes, source.Workspace, source.Uses, source.TemplateContext, source.Extra)
	job.Metadata = a.metadata()
	rollUpJob(&job)
	return job
}

// environment resolves the deployment environment. The short form
// `environment: name.resource` names an environment and one of its resources
// (https://learn.microsoft.com/en-us/azure/devops/pipelines/process/environments).
func (c *conv) environment(env *ast.Environment, a *acc) string {
	name := scalarOr(env.Name, "environment")
	resourceName := env.ResourceName
	if env.Short && !deferredText(name) {
		if base, resource, ok := strings.Cut(name, "."); ok && base != "" && resource != "" {
			name = base
			a.keep("environment.resource", orderedScalars("name", resource))
			a.lose(codeExt, "deployment", "environment.resource", env.Source, "short-form environment resource has no IR deployment field")
			c.unknown(a, "environment", env.Extra)
			return name
		}
	}
	if resourceName != nil || env.ResourceID != nil || env.ResourceType != nil || env.Tags != nil {
		a.keep("environment.resource", orderedScalars("name", resourceName, "id", env.ResourceID, "type", env.ResourceType, "tags", env.Tags))
		a.lose(codeExt, "deployment", "environment.resource", env.Source, "environment resource details have no IR deployment field")
	}
	c.unknown(a, "environment", env.Extra)
	return name
}

func rollUpJob(job *ir.Job) {
	f := job.Fidelity
	for i := range job.Variables {
		f = maxFidelity(f, job.Variables[i].Fidelity)
	}
	for i := range job.Steps {
		f = maxFidelity(f, job.Steps[i].Fidelity)
	}
	job.Fidelity = f
}

func (c *conv) deploymentLifecycle(strategy *ast.DeploymentStrategy, a *acc) (string, *ast.Lifecycle) {
	if strategy == nil {
		return "", nil
	}
	c.unknown(a, "deploymentStrategy", strategy.Extra)
	var name string
	var chosen *ast.Lifecycle
	count := 0
	for _, candidate := range []struct {
		name string
		l    *ast.Lifecycle
	}{{"runOnce", strategy.RunOnce}, {"rolling", strategy.Rolling}, {"canary", strategy.Canary}} {
		if candidate.l == nil {
			continue
		}
		count++
		if chosen == nil {
			name, chosen = candidate.name, candidate.l
		}
	}
	if count > 1 {
		a.lose(codeExt, "deployment", "strategy", strategy.Source, "more than one deployment strategy is declared; only the first is normalized")
	}
	return name, chosen
}

// lifecycleSteps flattens deployment hooks into ordered steps. Hooks are
// described at https://learn.microsoft.com/en-us/azure/devops/pipelines/process/deployment-jobs :
// `on.failure` runs only when the deployment failed and `on.success` only when
// it succeeded, so their steps carry failure()/success() conditions. The deploy
// hook downloads artifacts automatically unless it contains `download: none`.
func (c *conv) lifecycleSteps(lifecycle *ast.Lifecycle, a *acc) []ir.Step {
	var out []ir.Step
	type hookSpec struct {
		name string
		hook *ast.Hook
		gate ir.Function
	}
	hooks := []hookSpec{{"preDeploy", lifecycle.PreDeploy, ""}, {"deploy", lifecycle.Deploy, ""}, {"routeTraffic", lifecycle.RouteTraffic, ""}, {"postRouteTraffic", lifecycle.PostRouteTraffic, ""}}
	if lifecycle.On != nil {
		hooks = append(hooks, hookSpec{"on.failure", lifecycle.On.Failure, ir.FunctionFailure}, hookSpec{"on.success", lifecycle.On.Success, ir.FunctionSuccess})
		c.unknown(a, "deployment-on", lifecycle.On.Extra)
	}
	present := 0
	for _, pair := range hooks {
		if pair.hook == nil {
			continue
		}
		present++
		if pair.name == "deploy" && !hasDownloadNone(pair.hook) {
			out = append(out, ir.Step{
				Metadata: c.syntheticMetadata("implicit artifact download of the deploy hook"), ID: "implicit-download", Name: "Download artifacts", Kind: ir.StepArtifactRestore,
				If: ir.SuccessCondition(ir.PhaseRuntime), ArtifactRestore: &ir.ArtifactRestoreStep{Name: "", Path: stringExpr("$(Pipeline.Workspace)")},
			})
		}
		restoreRunner := func() {}
		if pair.hook.Pool != nil {
			restoreRunner = c.useRunner(hookRunner(pair.hook.Pool))
		}
		steps := c.steps(pair.hook.Steps, true, pair.gate)
		restoreRunner()
		out = append(out, steps...)
		a.keep("lifecycle."+pair.name, len(steps))
		if pair.hook.Pool != nil {
			a.keep("lifecycle."+pair.name+".pool", orderedScalars("name", pair.hook.Pool.Name, "vmImage", pair.hook.Pool.VMImage))
			a.lose(codeExt, "deployment", "hook.pool", pair.hook.Pool.Source, "per-hook pool has no IR field")
		}
		c.unknown(a, "hook", pair.hook.Extra)
	}
	if lifecycle.MaxParallel != nil {
		a.keep("strategy.maxParallel", lifecycle.MaxParallel.Value)
		a.lose(codeExt, "deployment", "strategy.maxParallel", lifecycle.MaxParallel.Source, "rolling maxParallel has no IR field")
	}
	if lifecycle.Increments != nil {
		var increments []string
		for _, item := range lifecycle.Increments.Items {
			if item.Value != nil {
				increments = append(increments, item.Value.Value)
			}
		}
		a.keep("strategy.increments", increments)
		a.lose(codeExt, "deployment", "strategy.increments", lifecycle.Increments.Source, "canary increments have no IR field")
	}
	if present > 1 {
		a.lose(codeExt, "deployment", "lifecycleHooks", lifecycle.Source, "IR flattens deployment lifecycle hooks into ordered steps; the hook identity is kept in the step conditions and extension only")
	}
	c.unknown(a, "lifecycle", lifecycle.Extra)
	return out
}

func hasDownloadNone(hook *ast.Hook) bool {
	if hook.Steps == nil {
		return false
	}
	for _, item := range hook.Steps.Items {
		if download, ok := item.Value.(*ast.DownloadStep); ok && download.Download != nil && strings.EqualFold(download.Download.Value, "none") {
			return true
		}
	}
	return false
}

// gateCondition applies a deployment-hook gate (failure() for on.failure,
// success() for on.success) to a step condition. A step without its own
// condition takes the gate; an explicit condition is conjoined with it so that
// e.g. always() cannot escape the hook. An opaque condition cannot be conjoined:
// the gate is then reported as needing manual work on the step.
func (c *conv) gateCondition(cond *ir.Cond, explicit, disabled bool, gate ir.Function, a *acc, source *ast.Expression) *ir.Cond {
	if gate == "" {
		return cond
	}
	hookCond := ir.Expr{Kind: ir.ExprCall, Call: &ir.CallExpr{Function: gate}}
	switch {
	case disabled:
		return cond // enabled: false stays disabled
	case !explicit:
		if gate == ir.FunctionSuccess {
			return cond
		}
		return &ir.Cond{Phase: ir.PhaseRuntime, Expr: &hookCond}
	case cond == nil || cond.Opaque != nil || cond.Expr == nil:
		var src diag.Source
		if source != nil {
			src = source.Source
		}
		a.lose(codeExt, "deployment", "hook.condition", src, "opaque step condition cannot be conjoined with the "+string(gate)+"() hook gate; reapply the gate manually")
		a.needsManual()
		return cond
	default:
		combined := ir.Expr{Kind: ir.ExprCall, Call: &ir.CallExpr{Function: ir.FunctionAnd, Args: []ir.Expr{hookCond, *cond.Expr}}}
		return &ir.Cond{Phase: ir.PhaseRuntime, Expr: &combined}
	}
}

func (c *conv) jobLosses(a *acc, cancel *ast.Scalar, workspace *ast.Workspace, uses *ast.Uses, templateContext *ast.Any, extras []*ast.Extra) {
	if cancel != nil {
		a.keep("cancelTimeoutInMinutes", cancel.Value)
		a.lose(codeExt, "job", "cancelTimeoutInMinutes", cancel.Source, "cancel timeout has no IR field")
	}
	if workspace != nil {
		a.keep("workspace.clean", scalarEnum(workspace.Clean))
		a.lose(codeExt, "job", "workspace", workspace.Source, "workspace cleanup has no IR field")
		c.unknown(a, "workspace", workspace.Extra)
	}
	if uses != nil {
		a.keep("uses", orderedLists("repositories", uses.Repositories, "pools", uses.Pools))
		a.lose(codeExt, "job", "uses", uses.Source, "Azure job resource authorization has no IR field")
	}
	if templateContext != nil {
		a.keep("templateContext", anyValue(templateContext))
		a.lose(codeExt, "job", "templateContext", templateContext.Source, "template context has no IR field")
	}
	c.unknown(a, "job", extras)
}

func orderedLists(values ...any) ir.OrderedMap[any] {
	out := ir.OrderedMap[any]{}
	for i := 0; i+1 < len(values); i += 2 {
		list, ok := values[i+1].(*ast.List[*ast.Scalar])
		if !ok || list == nil {
			continue
		}
		var items []any
		for _, item := range list.Items {
			if item.Value != nil {
				items = append(items, item.Value.Value)
			}
		}
		out.Set(values[i].(string), items)
	}
	return out
}

// runner materializes the runner requirement for a node. declared is only
// meaningful when pool is nil: a deployment lifecycle hook declares a pool that
// cannot be collapsed onto the single IR job runner, so it suppresses the
// hosted default without inventing a runner.
func (c *conv) runner(pool *ast.Pool, declared bool, a *acc) ir.RunnerReq {
	if pool == nil {
		if declared {
			return ir.RunnerReq{}
		}
		const detail = "omitted Azure pool uses the Microsoft-hosted ubuntu-latest runner"
		a.keep("synthetic.runner", detail)
		a.note(codeSynth, "runner", "pool", a.source, detail)
		return ir.RunnerReq{Image: "ubuntu-latest", OS: "linux"}
	}
	c.unknown(a, "pool", pool.Extra)
	if agentlessPool(pool) {
		a.keep("pool", "server")
		a.lose(codeExt, "pool", "server", pool.Source, "agentless server pool has no runner requirement")
		return ir.RunnerReq{}
	}
	out := ir.RunnerReq{Pool: scalarOr(pool.Name, ""), Image: scalarOr(pool.VMImage, "")}
	out.OS, _ = hosted.ImageOS(out.Image)
	if pool.Demands != nil {
		for _, item := range pool.Demands.Items {
			if item.Value != nil {
				out.Demands = append(out.Demands, item.Value.Value)
			}
		}
	}
	return out
}

func (c *conv) jobTimeout(a *acc, value *ast.Scalar, construct string, pool *ast.Pool, runner ir.RunnerReq) *ir.Duration {
	if value != nil {
		return c.timeout(a, value, construct)
	}
	if agentlessPool(pool) {
		return nil
	}
	// An unnamed pool and the Microsoft-hosted "Azure Pipelines" pool are both
	// hosted: an omitted job timeout defaults to one hour. Any other pool name is
	// self-hosted and never receives the default.
	_, knownHostedImage := hosted.ImageOS(runner.Image)
	if knownHostedImage && (runner.Pool == "" || hosted.IsHostedPoolName(runner.Pool)) {
		const detail = "omitted timeout on a Microsoft-hosted job is materialized as 60 minutes (VERIFY-12)"
		a.keep("synthetic.timeout", detail)
		a.note(codeSynth, construct, "timeoutInMinutes", a.source, detail)
		a.withFidelity(diag.FidelityApproximated)
		return &ir.Duration{Duration: time.Hour}
	}
	// A nil pool with an unresolved runner means the only declared pool sits on a
	// deployment lifecycle hook that cannot be flattened here; say so instead of
	// blaming a self-hosted pool.
	detail := "omitted job timeout on a self-hosted or unknown runner cannot be materialized"
	if pool == nil {
		detail = "omitted job timeout on a deployment-hook runner cannot be materialized"
	}
	a.lose(codeExt, construct, "timeoutInMinutes", a.source, detail)
	return nil
}

func agentlessPool(pool *ast.Pool) bool {
	return pool != nil && pool.Name != nil && strings.EqualFold(strings.TrimSpace(pool.Name.Value), "server") && pool.VMImage == nil
}

// deploymentHookPoolDeclared reports whether any lifecycle hook declares a
// pool. Hook pools cannot be collapsed into the single flattened IR job runner,
// but their presence must prevent inventing the global hosted default.
func deploymentHookPoolDeclared(strategy *ast.DeploymentStrategy) bool {
	if strategy == nil {
		return false
	}
	for _, lifecycle := range []*ast.Lifecycle{strategy.RunOnce, strategy.Rolling, strategy.Canary} {
		if lifecycle == nil {
			continue
		}
		hooks := []*ast.Hook{lifecycle.PreDeploy, lifecycle.Deploy, lifecycle.RouteTraffic, lifecycle.PostRouteTraffic}
		if lifecycle.On != nil {
			hooks = append(hooks, lifecycle.On.Failure, lifecycle.On.Success)
		}
		for _, hook := range hooks {
			if hook != nil && hook.Pool != nil {
				return true
			}
		}
	}
	return false
}

func (c *conv) matrix(strategy *ast.Strategy, a *acc) *ir.Matrix {
	if strategy == nil {
		return nil
	}
	out := &ir.Matrix{FailFast: false}
	maxParallelSecret := false
	if strategy.MaxParallel != nil {
		var err error
		var n int
		n, maxParallelSecret, err = c.checkedAtoi(a, strategy.MaxParallel.Value, strategy.MaxParallel.Source, "strategy.maxParallel", "strategy", "maxParallel")
		if !maxParallelSecret {
			if err != nil || n < 0 {
				a.keep("strategy.maxParallel", safeText(strategy.MaxParallel.Value))
				a.lose(codeExt, "strategy", "maxParallel", strategy.MaxParallel.Source, "maxParallel is not a literal non-negative integer")
			} else {
				out.MaxParallel = n
			}
		}
	}
	if strategy.Matrix != nil {
		for _, entry := range strategy.Matrix.Entries {
			if entry.Key == nil || entry.Value == nil {
				continue
			}
			leg := ir.MatrixLeg{ID: entry.Key.Value}
			var legValues ir.OrderedMapBuilder[ir.Literal]
			for _, value := range entry.Value.Entries {
				if value.Key == nil || value.Value == nil {
					continue
				}
				text := value.Value.Value
				if literalSecret(value.Key.Value, text) {
					a.lose(codeExt, "strategy", "matrix."+c.part(a, "matrix", value.Key.Value, value.Key.Source), value.Value.Source, "secret-looking matrix value was replaced by the redaction placeholder; supply it from a secret store")
					a.needsManual()
					text = redact.Replacement()
				} else {
					text = a.scrub("strategy", "matrix."+c.part(a, "matrix", value.Key.Value, value.Key.Source), value.Value.Source, text)
				}
				legValues.Set(value.Key.Value, ir.Literal{Type: ir.LiteralString, String: text})
				c.work++
			}
			if legValues.Len() > 0 {
				leg.Values = legValues.Build()
			}
			out.Legs = append(out.Legs, leg)
		}
	}
	if strategy.Parallel != nil {
		count, parallelSecret, err := c.checkedAtoi(a, strategy.Parallel.Value, strategy.Parallel.Source, "strategy.parallel", "strategy", "parallel")
		if parallelSecret {
			out.Opaque = &ir.Raw{Provider: Platform, Kind: "strategy", Format: "json", JSON: rawJSON(map[string]any{"parallel": redact.Replacement()}), Origin: c.origin(strategy.Source, strategy.Chain)}
		} else {
			switch {
			case err != nil || count < 1:
				a.keep("strategy.parallel", safeText(strategy.Parallel.Value))
				a.lose(codeExt, "strategy", "parallel", strategy.Parallel.Source, "parallel is not a literal positive integer")
			case count > maxParallelLegs:
				a.keep("strategy.parallel", count)
				a.lose(codeExt, "strategy", "parallel", strategy.Parallel.Source, "parallel count exceeds the expansion cap and is kept opaque")
				out.Opaque = &ir.Raw{Provider: Platform, Kind: "strategy", Format: "json", JSON: rawJSON(map[string]any{"parallel": count}), Origin: c.origin(strategy.Source, strategy.Chain)}
			default:
				// Replica position variables: System.JobPositionInPhase and
				// System.TotalJobsInPhase (https://learn.microsoft.com/en-us/azure/devops/pipelines/process/phases).
				for i := 1; i <= count; i++ {
					leg := ir.MatrixLeg{ID: strconv.Itoa(i)}
					leg.Values.Set("System.JobPositionInPhase", ir.Literal{Type: ir.LiteralNumber, Number: strconv.Itoa(i)})
					leg.Values.Set("System.TotalJobsInPhase", ir.Literal{Type: ir.LiteralNumber, Number: strconv.Itoa(count)})
					out.Legs = append(out.Legs, leg)
				}
			}
		}
	}
	if len(out.Legs) == 0 && out.Opaque == nil && strategy.Matrix != nil && runtimeDirective(strategy.Matrix.Insertion) {
		// A runtime `$[ ]` matrix is evaluated by the agent: keep the expression
		// text, never expand legs at compile time.
		text := c.sanitize(strategy.Matrix.Insertion.Raw)
		a.keep("strategy.matrix", text)
		a.lose(codeExt, "strategy", "matrix", strategy.Matrix.Insertion.Source, "runtime matrix expression is evaluated by the agent; no legs can be expanded")
		out.Opaque = &ir.Raw{Provider: Platform, Kind: "strategy", Format: "json", JSON: rawJSON(map[string]any{"dynamic": true, "runtimeExpression": text}), Origin: c.origin(strategy.Source, strategy.Chain)}
	}
	if len(out.Legs) == 0 && out.Opaque == nil && strategy.Matrix == nil && strategy.Parallel == nil {
		if strategy.MaxParallel != nil && !maxParallelSecret {
			a.keep("strategy.maxParallel", safeText(strategy.MaxParallel.Value))
			a.lose(codeExt, "strategy", "maxParallel", strategy.MaxParallel.Source, "maxParallel without a matrix or parallel count has no IR field")
		}
		c.unknown(a, "strategy", strategy.Extra)
		return nil
	}
	if len(out.Legs) == 0 && out.Opaque == nil {
		a.lose(codeExt, "strategy", "matrix", strategy.Source, "dynamic or empty matrix: the expression text is not available in the AST and no legs can be expanded")
		out.Opaque = &ir.Raw{Provider: Platform, Kind: "strategy", Format: "json", JSON: rawJSON(map[string]any{"dynamic": true}), Origin: c.origin(strategy.Source, strategy.Chain)}
	}
	c.unknown(a, "strategy", strategy.Extra)
	return out
}

// runtimeDirective reports a whole-value runtime `$[ ]` expression.
func runtimeDirective(d *ast.Directive) bool {
	return d != nil && d.Kind == ast.DirectiveRuntime
}

func (c *conv) container(value *ast.Container, a *acc) *ir.Container {
	if value == nil {
		return nil
	}
	if runtimeDirective(value.Insertion) {
		// The agent evaluates the container expression at run time; the IR
		// container has no expression field, so the text is kept in the
		// extension and recorded as a loss.
		a.keep("container.runtimeExpression", c.sanitize(value.Insertion.Raw))
		a.lose(codeExt, "container", "runtimeExpression", value.Insertion.Source, "runtime container expression is evaluated by the agent; the IR container is left empty")
		return nil
	}
	if value.Alias != nil && strings.TrimSpace(value.Alias.Value) == "" && value.Spec == nil {
		// `container: ''` (or an empty template parameter): no container.
		return nil
	}
	if value.Alias != nil && value.Alias.Null {
		// A written null (for example a runtime `$[ ]` expression the resolver
		// evaluated to null) is an unresolved alias, never the image "null".
		a.keep("container.alias", value.Alias.Value)
		a.lose(codeExt, "container", "alias", value.Alias.Source, "unresolved container alias has no image")
		return nil
	}
	if value.Alias != nil {
		c.work++
		if candidate := c.containerByAlias[value.Alias.Value]; candidate != nil {
			return c.containerResource(candidate, a)
		}
		a.keep("container.alias", value.Alias.Value)
		a.lose(codeExt, "container", "alias", value.Alias.Source, "unresolved container alias has no image")
		return &ir.Container{Image: value.Alias.Value}
	}
	return c.containerSpec(value.Spec, a)
}

func (c *conv) containerResource(resource *ast.ContainerResource, a *acc) *ir.Container {
	if resource.Image == nil {
		acr := orderedScalars("type", resource.Type, "azureSubscription", resource.AzureSubscription, "resourceGroup", resource.ResourceGroup, "registry", resource.Registry, "repository", resource.Repository)
		a.keep("container.registry", acr)
		a.lose(codeExt, "container", "image", resource.Source, "container resource has no image (for example an Azure Container Registry resource); IR records \"unknown\"")
	}
	if resource.LocalImage != nil {
		a.keep("container.localImage", resource.LocalImage.Value)
		a.lose(codeExt, "container", "localImage", resource.Source, "localImage has no IR container field")
	}
	return c.containerSpec(&resource.ContainerSpec, a)
}

func (c *conv) containerSpec(spec *ast.ContainerSpec, a *acc) *ir.Container {
	if spec == nil {
		return nil
	}
	out := &ir.Container{Image: scalarOr(spec.Image, "unknown")}
	if spec.Env != nil {
		var env ir.OrderedMapBuilder[ir.Expr]
		for _, entry := range spec.Env.Entries {
			if entry.Key != nil && entry.Value != nil {
				env.Set(entry.Key.Value, c.keyedExpr(a, "container-env", entry.Key.Value, entry.Value))
				c.work++
			}
		}
		if env.Len() > 0 {
			out.Env = env.Build()
		}
	}
	if spec.Ports != nil {
		for _, item := range spec.Ports.Items {
			if item.Value != nil {
				out.Ports = append(out.Ports, item.Value.Value)
			}
		}
	}
	if spec.Options != nil {
		// Scrub the command line before splitting: quotes that delimit a
		// value are gone once the options are tokenized.
		text := spec.Options.Value
		if clean := scrubFlags(scrubPairs(safeText(text))); clean != text {
			a.lose(codeExt, "container", "options", spec.Options.Source, "secret material in container options was replaced by the redaction placeholder")
			a.needsManual()
			text = clean
		}
		out.Options = splitOptions(text)
	}
	if spec.Endpoint != nil || spec.Volumes != nil || spec.MapDockerSocket != nil || spec.MountReadOnly != nil {
		opts := orderedScalars("endpoint", spec.Endpoint)
		if spec.MapDockerSocket != nil {
			opts.Set("mapDockerSocket", spec.MapDockerSocket.Value)
		}
		if spec.Volumes != nil {
			var volumes []any
			for _, item := range spec.Volumes.Items {
				if item.Value != nil {
					volumes = append(volumes, safeText(item.Value.Value))
				}
			}
			opts.Set("volumes", volumes)
		}
		if m := spec.MountReadOnly; m != nil {
			mounts := ir.OrderedMap[any]{}
			for _, pair := range []struct {
				key string
				v   *ast.Bool
			}{{"work", m.Work}, {"externals", m.Externals}, {"tools", m.Tools}, {"tasks", m.Tasks}} {
				if pair.v != nil {
					mounts.Set(pair.key, pair.v.Value)
				}
			}
			opts.Set("mountReadOnly", mounts)
		}
		a.keep("container.providerOptions", opts)
		a.lose(codeExt, "container", "providerOptions", a.source, "container endpoint, mounts, or volumes are not fully represented in IR")
	}
	return out
}

// splitOptions splits container options on whitespace, keeping quoted segments
// together.
func splitOptions(text string) []string {
	var out []string
	var current strings.Builder
	var quote rune
	started := false
	for _, r := range text {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, started = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if started {
				out = append(out, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteRune(r)
			started = true
		}
	}
	if started {
		out = append(out, current.String())
	}
	return out
}

func (c *conv) services(values *ast.Map[*ast.Scalar], specs *ast.Map[*ast.Any], a *acc) []ir.Service {
	var out []ir.Service
	if values != nil {
		for _, entry := range values.Entries {
			if entry.Key == nil || entry.Value == nil {
				continue
			}
			container := &ast.Container{Alias: entry.Value}
			mapped := c.container(container, a)
			if mapped != nil {
				out = append(out, ir.Service{ID: entry.Key.Value, Container: *mapped})
			}
		}
	}
	if specs != nil {
		for _, entry := range specs.Entries {
			if entry.Key == nil || entry.Value == nil {
				continue
			}
			image := "unknown"
			if entry.Value.Kind == ast.AnyMap {
				for _, field := range entry.Value.Entries {
					if field.Key != nil && field.Key.Value == "image" && field.Value != nil && field.Value.Scalar != nil {
						image = field.Value.Scalar.Value
					}
				}
			}
			a.keep("service."+c.part(a, "service", entry.Key.Value, entry.Key.Source), anyValue(entry.Value))
			a.lose(codeExt, "service", entry.Key.Value, entry.Key.Source, "object service specification is preserved but not fully modeled")
			out = append(out, ir.Service{ID: entry.Key.Value, Container: ir.Container{Image: image}})
		}
	}
	return out
}

func listValues(list *ast.List[*ast.Scalar]) []any {
	var out []any
	if list == nil {
		return out
	}
	for _, item := range list.Items {
		if item.Value != nil {
			out = append(out, item.Value.Value)
		}
	}
	return out
}
