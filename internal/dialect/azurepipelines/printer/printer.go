// Package printer renders an Azure Pipelines syntax tree back to normalized YAML:
// canonical key spellings and order, two-space indentation, LF line endings.
// Parsing the output yields an equal tree (positions aside), so
// parse -> print -> parse is the identity on the structure.
//
// Template directives are printed from their raw text and are never evaluated.
// Comments, original key order between typed fields and template directives,
// and quoting style are not preserved; in-place edits use yamlx byte patches.
package printer

import (
	"bytes"
	"fmt"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
	"go.yaml.in/yaml/v3"
)

// Print renders the pipeline as normalized YAML.
func Print(p *ast.Pipeline) ([]byte, error) {
	if p == nil {
		return nil, fmt.Errorf("printer: nil pipeline")
	}
	root := pipeline(p)
	if len(root.Content) == 0 {
		return []byte("{}\n"), nil
	}
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(root); err != nil {
		return nil, fmt.Errorf("printer: encode pipeline: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("printer: close encoder: %w", err)
	}
	// The text goes through the YAML layer so that it is normalized the same way
	// as every other emitted document and proven to be readable by it.
	document, diagnostics := yamlx.Parse("printed.yml", buffer.Bytes())
	if document == nil || len(diagnostics) > 0 {
		return nil, fmt.Errorf("printer: printed YAML is not readable: %d diagnostics", len(diagnostics))
	}
	out, err := yamlx.Emit(document, yamlx.WithNormalize())
	if err != nil {
		return nil, fmt.Errorf("printer: normalize: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// YAML node construction.

type mapper struct{ node *yaml.Node }

func newMapping() *mapper { return &mapper{node: &yaml.Node{Kind: yaml.MappingNode}} }

func (m *mapper) put(key string, value *yaml.Node) {
	if value != nil {
		m.node.Content = append(m.node.Content, plain(key), value)
	}
}

func (m *mapper) putNode(key, value *yaml.Node) {
	if key != nil && value != nil {
		m.node.Content = append(m.node.Content, key, value)
	}
}

func plain(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: value}
}

func sequence(items ...*yaml.Node) *yaml.Node {
	node := &yaml.Node{Kind: yaml.SequenceNode}
	for _, item := range items {
		if item != nil {
			node.Content = append(node.Content, item)
		}
	}
	return node
}

// scalar prints decoded text. The tag is left implicit so that numbers and
// booleans stay unquoted and text that would read back as another type is
// quoted by the encoder.
func scalar(s *ast.Scalar) *yaml.Node {
	if s == nil {
		return nil
	}
	node := &yaml.Node{Kind: yaml.ScalarNode, Value: s.Value}
	switch {
	case s.Null:
		node.Tag = "!!null"
	case s.Value == "":
		node.Style = yaml.SingleQuotedStyle
	}
	return node
}

func directive(d *ast.Directive) *yaml.Node {
	if d == nil {
		return nil
	}
	return plain(d.Raw)
}

func boolean(b *ast.Bool) *yaml.Node {
	if b == nil {
		return nil
	}
	return scalar(&b.Scalar)
}

func enum(e *ast.Enum) *yaml.Node {
	if e == nil {
		return nil
	}
	return scalar(&e.Scalar)
}

func expression(e *ast.Expression) *yaml.Node {
	if e == nil {
		return nil
	}
	return scalar(&e.Scalar)
}

// ---------------------------------------------------------------------------
// Generic containers.

func list[T any](l *ast.List[T], item func(T) *yaml.Node) *yaml.Node {
	if l == nil {
		return nil
	}
	if l.Insertion != nil {
		return directive(l.Insertion)
	}
	seq := sequence()
	for _, entry := range l.Items {
		switch {
		case entry.Insert != nil:
			seq.Content = append(seq.Content, directive(entry.Insert))
		case entry.Cond != nil:
			body := newMapping()
			if entry.Cond.Items != nil {
				body.putNode(directive(entry.Cond.Directive), list(entry.Cond.Items, item))
			} else {
				body.putNode(directive(entry.Cond.Directive), free(entry.Cond.Other))
			}
			seq.Content = append(seq.Content, body.node)
		default:
			if node := item(entry.Value); node != nil {
				seq.Content = append(seq.Content, node)
			}
		}
	}
	return seq
}

func mapOf[V any](mp *ast.Map[V], value func(V) *yaml.Node) *yaml.Node {
	if mp == nil {
		return nil
	}
	if mp.Insertion != nil {
		return directive(mp.Insertion)
	}
	out := newMapping()
	for _, entry := range mp.Entries {
		if entry.Cond != nil {
			out.putNode(directive(entry.Cond.Directive), mapOf(entry.Cond.Body, value))
			continue
		}
		out.putNode(scalar(entry.Key), value(entry.Value))
	}
	return out.node
}

func stringMap(mp *ast.Map[*ast.Scalar]) *yaml.Node { return mapOf(mp, scalar) }

func stringList(l *ast.List[*ast.Scalar]) *yaml.Node { return list(l, scalar) }

func free(a *ast.Any) *yaml.Node {
	if a == nil {
		return nil
	}
	switch a.Kind {
	case ast.AnySeq:
		seq := sequence()
		for _, item := range a.Items {
			if node := free(item); node != nil {
				seq.Content = append(seq.Content, node)
			}
		}
		return seq
	case ast.AnyMap:
		out := newMapping()
		for _, entry := range a.Entries {
			if entry.Directive != nil {
				out.putNode(directive(entry.Directive), free(entry.Value))
			} else {
				out.putNode(scalar(entry.Key), free(entry.Value))
			}
		}
		return out.node
	default:
		return scalar(a.Scalar)
	}
}

// tail prints key directives and preserved unknown keys after the typed fields.
func tail[T any](m *mapper, inserts []*ast.Branch[*T], extras []*ast.Extra, body func(*T) *yaml.Node) {
	for _, extra := range extras {
		m.putNode(scalar(extra.Key), free(extra.Value))
	}
	for _, branch := range inserts {
		m.putNode(directive(branch.Directive), body(branch.Body))
	}
}

// ---------------------------------------------------------------------------
// Pipeline.

func pipeline(p *ast.Pipeline) *yaml.Node {
	m := newMapping()
	fillPipeline(m, p)
	return m.node
}

func fillPipeline(m *mapper, p *ast.Pipeline) {
	m.put("name", scalar(p.Name))
	m.put("appendCommitMessageToRunName", boolean(p.AppendCommitMessageToRunName))
	m.put("trigger", trigger(p.Trigger, false))
	m.put("pr", trigger(p.PR, true))
	m.put("schedules", list(p.Schedules, schedule))
	m.put("lockBehavior", enum(p.LockBehavior))
	m.put("resources", resources(p.Resources))
	m.put("parameters", parameters(p.Parameters))
	m.put("variables", variables(p.Variables))
	m.put("pool", pool(p.Pool))
	m.put("extends", extends(p.Extends))
	m.put("stages", list(p.Stages, stage))
	m.put("jobs", list(p.Jobs, job))
	m.put("steps", list(p.Steps, step))
	m.put("strategy", strategy(p.Strategy))
	m.put("continueOnError", boolean(p.ContinueOnError))
	m.put("container", container(p.Container))
	m.put("services", stringMap(p.Services))
	m.put("workspace", workspace(p.Workspace))
	tail(m, p.Inserts, p.Extra, pipeline)
}

func trigger(t *ast.Trigger, pullRequest bool) *yaml.Node {
	if t == nil {
		return nil
	}
	switch t.Form {
	case ast.TriggerInsertion:
		return directive(t.Insertion)
	case ast.TriggerNone:
		return scalar(t.Keyword)
	case ast.TriggerList:
		if t.Branches != nil {
			return stringList(t.Branches.Include)
		}
		return sequence()
	}
	m := newMapping()
	if pullRequest {
		m.put("autoCancel", boolean(t.AutoCancel))
	} else {
		m.put("batch", boolean(t.Batch))
	}
	m.put("branches", filter(t.Branches))
	m.put("paths", filter(t.Paths))
	if !pullRequest {
		m.put("tags", filter(t.Tags))
	}
	m.put("drafts", boolean(t.Drafts))
	tail(m, t.Inserts, t.Extra, func(b *ast.Trigger) *yaml.Node { return trigger(b, pullRequest) })
	return m.node
}

func filter(f *ast.Filter) *yaml.Node {
	if f == nil {
		return nil
	}
	if f.Insertion != nil {
		return directive(f.Insertion)
	}
	if f.Bare {
		return stringList(f.Include)
	}
	m := newMapping()
	m.put("include", stringList(f.Include))
	m.put("exclude", stringList(f.Exclude))
	tail(m, f.Inserts, f.Extra, filter)
	return m.node
}

func schedule(s *ast.Schedule) *yaml.Node {
	m := newMapping()
	m.put("cron", scalar(s.Cron))
	m.put("displayName", scalar(s.DisplayName))
	m.put("branches", filter(s.Branches))
	m.put("batch", boolean(s.Batch))
	m.put("always", boolean(s.Always))
	tail(m, s.Inserts, s.Extra, schedule)
	return m.node
}

// ---------------------------------------------------------------------------
// Resources.

func resources(r *ast.Resources) *yaml.Node {
	if r == nil {
		return nil
	}
	if r.Insertion != nil {
		return directive(r.Insertion)
	}
	m := newMapping()
	m.put("builds", list(r.Builds, buildResource))
	m.put("containers", list(r.Containers, containerResource))
	m.put("pipelines", list(r.Pipelines, pipelineResource))
	m.put("repositories", list(r.Repositories, repository))
	m.put("webhooks", list(r.Webhooks, webhook))
	m.put("packages", list(r.Packages, packageResource))
	tail(m, r.Inserts, r.Extra, resources)
	return m.node
}

func resourceTrigger(t *ast.ResourceTrigger) *yaml.Node {
	if t == nil {
		return nil
	}
	switch t.Form {
	case ast.ResourceTriggerInsertion:
		return directive(t.Insertion)
	case ast.ResourceTriggerNone, ast.ResourceTriggerTrue:
		return scalar(t.Keyword)
	}
	m := newMapping()
	m.put("enabled", boolean(t.Enabled))
	m.put("branches", filter(t.Branches))
	m.put("stages", stringList(t.Stages))
	m.put("tags", filter(t.Tags))
	tail(m, t.Inserts, t.Extra, resourceTrigger)
	return m.node
}

func repository(r *ast.RepositoryResource) *yaml.Node {
	m := newMapping()
	m.put("repository", scalar(r.Repository))
	m.put("endpoint", scalar(r.Endpoint))
	m.put("trigger", trigger(r.Trigger, false))
	m.put("name", scalar(r.Name))
	m.put("ref", scalar(r.Ref))
	m.put("type", enum(r.Type))
	tail(m, r.Inserts, r.Extra, repository)
	return m.node
}

func pipelineResource(r *ast.PipelineResource) *yaml.Node {
	m := newMapping()
	m.put("pipeline", scalar(r.Pipeline))
	m.put("project", scalar(r.Project))
	m.put("source", scalar(r.Source))
	m.put("version", scalar(r.Version))
	m.put("branch", scalar(r.Branch))
	m.put("tags", stringList(r.Tags))
	m.put("trigger", resourceTrigger(r.Trigger))
	tail(m, r.Inserts, r.Extra, pipelineResource)
	return m.node
}

func containerSpec(m *mapper, c *ast.ContainerSpec) {
	m.put("image", scalar(c.Image))
	m.put("endpoint", scalar(c.Endpoint))
	m.put("env", stringMap(c.Env))
	m.put("mapDockerSocket", boolean(c.MapDockerSocket))
	m.put("options", scalar(c.Options))
	m.put("ports", stringList(c.Ports))
	m.put("volumes", stringList(c.Volumes))
	m.put("mountReadOnly", mountReadOnly(c.MountReadOnly))
}

func mountReadOnly(r *ast.MountReadOnly) *yaml.Node {
	if r == nil {
		return nil
	}
	if r.Insertion != nil {
		return directive(r.Insertion)
	}
	m := newMapping()
	m.put("work", boolean(r.Work))
	m.put("externals", boolean(r.Externals))
	m.put("tools", boolean(r.Tools))
	m.put("tasks", boolean(r.Tasks))
	tail(m, r.Inserts, r.Extra, mountReadOnly)
	return m.node
}

func containerResource(r *ast.ContainerResource) *yaml.Node {
	m := newMapping()
	m.put("container", scalar(r.Container))
	m.put("type", scalar(r.Type))
	m.put("trigger", resourceTrigger(r.Trigger))
	m.put("azureSubscription", scalar(r.AzureSubscription))
	m.put("resourceGroup", scalar(r.ResourceGroup))
	m.put("registry", scalar(r.Registry))
	m.put("repository", scalar(r.Repository))
	m.put("localImage", boolean(r.LocalImage))
	containerSpec(m, &r.ContainerSpec)
	tail(m, r.Inserts, r.Extra, containerResource)
	return m.node
}

func buildResource(r *ast.BuildResource) *yaml.Node {
	m := newMapping()
	m.put("build", scalar(r.Build))
	m.put("type", scalar(r.Type))
	m.put("connection", scalar(r.Connection))
	m.put("source", scalar(r.Source))
	m.put("version", scalar(r.Version))
	m.put("branch", scalar(r.Branch))
	m.put("trigger", resourceTrigger(r.Trigger))
	tail(m, r.Inserts, r.Extra, buildResource)
	return m.node
}

func packageResource(r *ast.PackageResource) *yaml.Node {
	m := newMapping()
	m.put("package", scalar(r.Package))
	m.put("type", scalar(r.Type))
	m.put("connection", scalar(r.Connection))
	m.put("name", scalar(r.Name))
	m.put("version", scalar(r.Version))
	m.put("tag", scalar(r.Tag))
	m.put("trigger", resourceTrigger(r.Trigger))
	tail(m, r.Inserts, r.Extra, packageResource)
	return m.node
}

func webhook(r *ast.WebhookResource) *yaml.Node {
	m := newMapping()
	m.put("webhook", scalar(r.Webhook))
	m.put("connection", scalar(r.Connection))
	m.put("type", scalar(r.Type))
	m.put("filters", list(r.Filters, webhookFilter))
	tail(m, r.Inserts, r.Extra, webhook)
	return m.node
}

func webhookFilter(f *ast.WebhookFilter) *yaml.Node {
	m := newMapping()
	m.put("path", scalar(f.Path))
	m.put("value", scalar(f.Value))
	tail(m, f.Inserts, f.Extra, webhookFilter)
	return m.node
}

// ---------------------------------------------------------------------------
// Parameters, variables, templates.

func parameters(p *ast.Parameters) *yaml.Node {
	if p == nil {
		return nil
	}
	switch p.Form {
	case ast.ParametersInsertion:
		return directive(p.Insertion)
	case ast.ParametersMap:
		return mapOf(p.Map, free)
	default:
		if p.List == nil {
			return scalar(&ast.Scalar{Null: true})
		}
		return list(p.List, parameter)
	}
}

func parameter(p *ast.Parameter) *yaml.Node {
	m := newMapping()
	m.put("name", scalar(p.Name))
	m.put("displayName", scalar(p.DisplayName))
	m.put("type", enum(p.Type))
	m.put("default", free(p.Default))
	m.put("values", stringList(p.Values))
	tail(m, p.Inserts, p.Extra, parameter)
	return m.node
}

func variables(v *ast.Variables) *yaml.Node {
	if v == nil {
		return nil
	}
	switch v.Form {
	case ast.VariablesInsertion:
		return directive(v.Insertion)
	case ast.VariablesList:
		return list(v.List, variable)
	default:
		if v.Map == nil {
			return scalar(&ast.Scalar{Null: true})
		}
		return stringMap(v.Map)
	}
}

func variable(v ast.Variable) *yaml.Node {
	m := newMapping()
	switch v := v.(type) {
	case *ast.VariableDef:
		m.put("name", scalar(v.Name))
		m.put("value", scalar(v.Value))
		m.put("readonly", boolean(v.Readonly))
		tail(m, v.Inserts, v.Extra, func(b *ast.VariableDef) *yaml.Node { return variable(b) })
	case *ast.VariableGroup:
		m.put("group", scalar(v.Group))
		tail(m, v.Inserts, v.Extra, func(b *ast.VariableGroup) *yaml.Node { return variable(b) })
	case *ast.VariableTemplate:
		templateRef(m, &v.TemplateRef)
		tail(m, v.Inserts, v.Extra, func(b *ast.VariableTemplate) *yaml.Node { return variable(b) })
	}
	return m.node
}

func templateRef(m *mapper, t *ast.TemplateRef) {
	m.put("template", scalar(t.Template))
	m.put("parameters", mapOf(t.Parameters, free))
}

func extends(e *ast.Extends) *yaml.Node {
	if e == nil {
		return nil
	}
	if e.Insertion != nil {
		return directive(e.Insertion)
	}
	m := newMapping()
	templateRef(m, &e.TemplateRef)
	tail(m, e.Inserts, e.Extra, extends)
	return m.node
}

// ---------------------------------------------------------------------------
// Pool, container, workspace, strategy.

func pool(p *ast.Pool) *yaml.Node {
	if p == nil {
		return nil
	}
	switch {
	case p.Insertion != nil:
		return directive(p.Insertion)
	case p.Short:
		return scalar(p.Name)
	}
	m := newMapping()
	m.put("name", scalar(p.Name))
	if p.DemandsScalar && p.Demands != nil && len(p.Demands.Items) == 1 {
		m.put("demands", scalar(p.Demands.Items[0].Value))
	} else {
		m.put("demands", stringList(p.Demands))
	}
	m.put("vmImage", scalar(p.VMImage))
	tail(m, p.Inserts, p.Extra, pool)
	return m.node
}

func container(c *ast.Container) *yaml.Node {
	if c == nil {
		return nil
	}
	switch {
	case c.Insertion != nil:
		return directive(c.Insertion)
	case c.Alias != nil:
		return scalar(c.Alias)
	}
	m := newMapping()
	if c.Spec != nil {
		containerSpec(m, c.Spec)
	}
	tail(m, c.Inserts, c.Extra, container)
	return m.node
}

func workspace(w *ast.Workspace) *yaml.Node {
	if w == nil {
		return nil
	}
	if w.Insertion != nil {
		return directive(w.Insertion)
	}
	m := newMapping()
	m.put("clean", enum(w.Clean))
	tail(m, w.Inserts, w.Extra, workspace)
	return m.node
}

func uses(u *ast.Uses) *yaml.Node {
	if u == nil {
		return nil
	}
	if u.Insertion != nil {
		return directive(u.Insertion)
	}
	m := newMapping()
	m.put("repositories", stringList(u.Repositories))
	m.put("pools", stringList(u.Pools))
	tail(m, u.Inserts, u.Extra, uses)
	return m.node
}

func dependsOn(d *ast.DependsOn) *yaml.Node {
	if d == nil {
		return nil
	}
	switch {
	case d.Insertion != nil:
		return directive(d.Insertion)
	case d.Scalar && d.Names != nil && len(d.Names.Items) == 1:
		return scalar(d.Names.Items[0].Value)
	case d.Names == nil:
		return sequence()
	}
	return stringList(d.Names)
}

func strategy(s *ast.Strategy) *yaml.Node {
	if s == nil {
		return nil
	}
	if s.Insertion != nil {
		return directive(s.Insertion)
	}
	m := newMapping()
	m.put("matrix", mapOf(s.Matrix, stringMap))
	m.put("maxParallel", scalar(s.MaxParallel))
	m.put("parallel", scalar(s.Parallel))
	tail(m, s.Inserts, s.Extra, strategy)
	return m.node
}

// ---------------------------------------------------------------------------
// Stages, jobs, deployments.

func stage(s ast.Stage) *yaml.Node {
	m := newMapping()
	switch s := s.(type) {
	case *ast.StageDef:
		m.put("stage", scalar(s.Stage))
		m.put("group", scalar(s.Group))
		m.put("displayName", scalar(s.DisplayName))
		m.put("pool", pool(s.Pool))
		m.put("dependsOn", dependsOn(s.DependsOn))
		m.put("condition", expression(s.Condition))
		m.put("variables", variables(s.Variables))
		m.put("jobs", list(s.Jobs, job))
		m.put("lockBehavior", enum(s.LockBehavior))
		m.put("trigger", enum(s.Trigger))
		m.put("isSkippable", boolean(s.IsSkippable))
		m.put("templateContext", free(s.TemplateContext))
		tail(m, s.Inserts, s.Extra, func(b *ast.StageDef) *yaml.Node { return stage(b) })
	case *ast.StageTemplate:
		templateRef(m, &s.TemplateRef)
		tail(m, s.Inserts, s.Extra, func(b *ast.StageTemplate) *yaml.Node { return stage(b) })
	}
	return m.node
}

func job(j ast.Job) *yaml.Node {
	m := newMapping()
	switch j := j.(type) {
	case *ast.JobDef:
		m.put("job", scalar(j.Job))
		m.put("displayName", scalar(j.DisplayName))
		m.put("dependsOn", dependsOn(j.DependsOn))
		m.put("condition", expression(j.Condition))
		m.put("continueOnError", boolean(j.ContinueOnError))
		m.put("timeoutInMinutes", scalar(j.TimeoutInMinutes))
		m.put("cancelTimeoutInMinutes", scalar(j.CancelTimeoutInMinutes))
		m.put("variables", variables(j.Variables))
		m.put("strategy", strategy(j.Strategy))
		m.put("pool", pool(j.Pool))
		m.put("container", container(j.Container))
		m.put("services", stringMap(j.Services))
		m.put("workspace", workspace(j.Workspace))
		m.put("uses", uses(j.Uses))
		m.put("steps", list(j.Steps, step))
		m.put("templateContext", free(j.TemplateContext))
		tail(m, j.Inserts, j.Extra, func(b *ast.JobDef) *yaml.Node { return job(b) })
	case *ast.Deployment:
		m.put("deployment", scalar(j.Deployment))
		m.put("displayName", scalar(j.DisplayName))
		m.put("dependsOn", dependsOn(j.DependsOn))
		m.put("condition", expression(j.Condition))
		m.put("continueOnError", boolean(j.ContinueOnError))
		m.put("timeoutInMinutes", scalar(j.TimeoutInMinutes))
		m.put("cancelTimeoutInMinutes", scalar(j.CancelTimeoutInMinutes))
		m.put("variables", variables(j.Variables))
		m.put("pool", pool(j.Pool))
		m.put("environment", environment(j.Environment))
		m.put("strategy", deploymentStrategy(j.Strategy))
		m.put("workspace", workspace(j.Workspace))
		m.put("uses", uses(j.Uses))
		m.put("container", container(j.Container))
		m.put("services", stringMap(j.Services))
		m.put("templateContext", free(j.TemplateContext))
		tail(m, j.Inserts, j.Extra, func(b *ast.Deployment) *yaml.Node { return job(b) })
	case *ast.JobTemplate:
		templateRef(m, &j.TemplateRef)
		tail(m, j.Inserts, j.Extra, func(b *ast.JobTemplate) *yaml.Node { return job(b) })
	}
	return m.node
}

func environment(e *ast.Environment) *yaml.Node {
	if e == nil {
		return nil
	}
	switch {
	case e.Insertion != nil:
		return directive(e.Insertion)
	case e.Short:
		return scalar(e.Name)
	}
	m := newMapping()
	m.put("name", scalar(e.Name))
	m.put("resourceName", scalar(e.ResourceName))
	m.put("resourceId", scalar(e.ResourceID))
	m.put("resourceType", scalar(e.ResourceType))
	m.put("tags", scalar(e.Tags))
	tail(m, e.Inserts, e.Extra, environment)
	return m.node
}

func deploymentStrategy(s *ast.DeploymentStrategy) *yaml.Node {
	if s == nil {
		return nil
	}
	if s.Insertion != nil {
		return directive(s.Insertion)
	}
	m := newMapping()
	m.put("runOnce", lifecycle(s.RunOnce))
	m.put("rolling", lifecycle(s.Rolling))
	m.put("canary", lifecycle(s.Canary))
	tail(m, s.Inserts, s.Extra, deploymentStrategy)
	return m.node
}

func lifecycle(l *ast.Lifecycle) *yaml.Node {
	if l == nil {
		return nil
	}
	if l.Insertion != nil {
		return directive(l.Insertion)
	}
	m := newMapping()
	m.put("maxParallel", scalar(l.MaxParallel))
	m.put("increments", stringList(l.Increments))
	m.put("preDeploy", hook(l.PreDeploy))
	m.put("deploy", hook(l.Deploy))
	m.put("routeTraffic", hook(l.RouteTraffic))
	m.put("postRouteTraffic", hook(l.PostRouteTraffic))
	m.put("on", onHooks(l.On))
	tail(m, l.Inserts, l.Extra, lifecycle)
	return m.node
}

func onHooks(o *ast.OnHooks) *yaml.Node {
	if o == nil {
		return nil
	}
	if o.Insertion != nil {
		return directive(o.Insertion)
	}
	m := newMapping()
	m.put("failure", hook(o.Failure))
	m.put("success", hook(o.Success))
	tail(m, o.Inserts, o.Extra, onHooks)
	return m.node
}

func hook(h *ast.Hook) *yaml.Node {
	if h == nil {
		return nil
	}
	if h.Insertion != nil {
		return directive(h.Insertion)
	}
	m := newMapping()
	m.put("steps", list(h.Steps, step))
	m.put("pool", pool(h.Pool))
	tail(m, h.Inserts, h.Extra, hook)
	return m.node
}

// ---------------------------------------------------------------------------
// Steps.

func stepProps(m *mapper, p *ast.StepProps) {
	m.put("condition", expression(p.Condition))
	m.put("continueOnError", boolean(p.ContinueOnError))
	m.put("displayName", scalar(p.DisplayName))
	m.put("target", target(p.Target))
	m.put("enabled", boolean(p.Enabled))
	m.put("env", stringMap(p.Env))
	m.put("name", scalar(p.Name))
	m.put("timeoutInMinutes", scalar(p.TimeoutInMinutes))
	m.put("retryCountOnTaskFailure", scalar(p.RetryCountOnTaskFailure))
}

func target(t *ast.Target) *yaml.Node {
	if t == nil {
		return nil
	}
	switch {
	case t.Insertion != nil:
		return directive(t.Insertion)
	case t.Short:
		return scalar(t.Container)
	}
	m := newMapping()
	m.put("container", scalar(t.Container))
	m.put("commands", enum(t.Commands))
	m.put("settableVariables", settableVariables(t.SettableVariables))
	tail(m, t.Inserts, t.Extra, target)
	return m.node
}

func settableVariables(s *ast.SettableVariables) *yaml.Node {
	switch {
	case s == nil:
		return nil
	case s.Insertion != nil:
		return directive(s.Insertion)
	case s.None:
		return plain("none")
	case s.List == nil:
		return scalar(&ast.Scalar{Null: true})
	}
	return stringList(s.List)
}

func step(s ast.Step) *yaml.Node {
	m := newMapping()
	switch s := s.(type) {
	case *ast.ScriptStep:
		scriptStep(m, s)
	case *ast.TaskStep:
		m.put("task", scalar(s.Task))
		m.put("inputs", stringMap(s.Inputs))
		stepProps(m, &s.StepProps)
		tail(m, s.Inserts, s.Extra, func(b *ast.TaskStep) *yaml.Node { return step(b) })
	case *ast.CheckoutStep:
		m.put("checkout", scalar(s.Checkout))
		m.put("clean", boolean(s.Clean))
		m.put("fetchDepth", scalar(s.FetchDepth))
		m.put("fetchFilter", scalar(s.FetchFilter))
		m.put("fetchTags", scalar(s.FetchTags))
		m.put("lfs", scalar(s.Lfs))
		m.put("persistCredentials", scalar(s.PersistCredentials))
		m.put("submodules", scalar(s.Submodules))
		m.put("path", scalar(s.Path))
		m.put("sparseCheckoutDirectories", scalar(s.SparseCheckoutDirectories))
		m.put("sparseCheckoutPatterns", scalar(s.SparseCheckoutPatterns))
		m.put("workspaceRepo", boolean(s.WorkspaceRepo))
		stepProps(m, &s.StepProps)
		tail(m, s.Inserts, s.Extra, func(b *ast.CheckoutStep) *yaml.Node { return step(b) })
	case *ast.DownloadStep:
		m.put("download", scalar(s.Download))
		m.put("artifact", scalar(s.Artifact))
		m.put("patterns", scalar(s.Patterns))
		stepProps(m, &s.StepProps)
		tail(m, s.Inserts, s.Extra, func(b *ast.DownloadStep) *yaml.Node { return step(b) })
	case *ast.DownloadBuildStep:
		m.put("downloadBuild", scalar(s.DownloadBuild))
		m.put("artifact", scalar(s.Artifact))
		m.put("path", scalar(s.Path))
		m.put("patterns", scalar(s.Patterns))
		stepProps(m, &s.StepProps)
		tail(m, s.Inserts, s.Extra, func(b *ast.DownloadBuildStep) *yaml.Node { return step(b) })
	case *ast.GetPackageStep:
		m.put("getPackage", scalar(s.GetPackage))
		m.put("path", scalar(s.Path))
		stepProps(m, &s.StepProps)
		tail(m, s.Inserts, s.Extra, func(b *ast.GetPackageStep) *yaml.Node { return step(b) })
	case *ast.PublishStep:
		m.put("publish", scalar(s.Publish))
		m.put("artifact", scalar(s.Artifact))
		stepProps(m, &s.StepProps)
		tail(m, s.Inserts, s.Extra, func(b *ast.PublishStep) *yaml.Node { return step(b) })
	case *ast.ReviewAppStep:
		m.put("reviewApp", scalar(s.ReviewApp))
		stepProps(m, &s.StepProps)
		tail(m, s.Inserts, s.Extra, func(b *ast.ReviewAppStep) *yaml.Node { return step(b) })
	case *ast.StepTemplate:
		templateRef(m, &s.TemplateRef)
		tail(m, s.Inserts, s.Extra, func(b *ast.StepTemplate) *yaml.Node { return step(b) })
	}
	return m.node
}

func scriptStep(m *mapper, s *ast.ScriptStep) {
	m.put(s.Kind.Keyword(), scalar(s.Script))
	m.put("errorActionPreference", scalar(s.ErrorActionPreference))
	m.put("failOnStderr", scalar(s.FailOnStderr))
	m.put("ignoreLASTEXITCODE", scalar(s.IgnoreLASTEXITCODE))
	m.put("workingDirectory", scalar(s.WorkingDirectory))
	stepProps(m, &s.StepProps)
	tail(m, s.Inserts, s.Extra, func(b *ast.ScriptStep) *yaml.Node {
		inner := newMapping()
		scriptStep(inner, b)
		return inner.node
	})
}
