package parse

import (
	"fmt"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
)

// Closed value sets; matching is case-insensitive and the canonical spelling below is stored.
var (
	lockBehaviors  = []string{"sequential", "runLatest"}
	stageTriggers  = []string{"manual", "automatic"}
	cleanModes     = []string{"outputs", "resources", "all"}
	commandModes   = []string{"any", "restricted"}
	repositoryType = []string{"git", "github", "githubenterprise", "bitbucket"}
	parameterTypes = []string{
		"string", "number", "boolean", "object", "step", "stepList", "job", "jobList",
		"deployment", "deploymentList", "stage", "stageList", "stringList",
	}
)

// insertionTarget is implemented by (pointers to) the nodes that may be a
// whole-value template expression.
type insertionTarget[T any] interface {
	*T
	SetPos(ast.Pos)
	SetInsertion(*ast.Directive)
}

// buildOrInsert parses a mapping node, or a node that is one whole `${{ }}` value.
func buildOrInsert[T any, PT insertionTarget[T]](p *parser, n *yamlx.Node, ctx string, fill func(*T, *mapping)) *T {
	if n != nil && n.Kind() == yamlx.ScalarKind {
		s := p.scalar(n, ctx)
		if s == nil {
			return nil
		}
		d := s.Whole()
		if d == nil {
			if !s.Null {
				p.typeError(n, "mapping", ctx)
				return nil
			}
			out := new(T)
			PT(out).SetPos(pos(n))
			return out
		}
		out := new(T)
		PT(out).SetPos(pos(n))
		PT(out).SetInsertion(d)
		return out
	}
	return build(p, n, ctx, fill)
}

func (p *parser) pipeline(n *yamlx.Node) *ast.Pipeline {
	m := p.open(n, "pipeline")
	if m == nil {
		return &ast.Pipeline{Pos: pos(n)}
	}
	defer m.close()
	out := new(ast.Pipeline)
	p.fillPipeline(out, m)
	return out
}

func (p *parser) shape(m *mapping) ast.Shape {
	shape := ast.ShapeNone
	first := ""
	for _, e := range m.entries {
		if e.dir != 0 {
			continue
		}
		var s ast.Shape
		switch strings.ToLower(e.name) {
		case "stages":
			s = ast.ShapeStages
		case "jobs":
			s = ast.ShapeJobs
		case "steps":
			s = ast.ShapeSteps
		case "extends":
			s = ast.ShapeExtends
		default:
			continue
		}
		if shape == ast.ShapeNone {
			shape, first = s, e.name
			continue
		}
		p.add("TRL-PARSE-0014", e.key.Position(),
			fmt.Sprintf("pipeline: %s and %s select different pipeline shapes; only one is allowed", first, e.name),
			map[string]any{"keys": first + ", " + e.name})
	}
	return shape
}

func (p *parser) fillPipeline(out *ast.Pipeline, m *mapping) {
	out.Pos = pos(m.node)
	out.Shape = p.shape(m)
	out.Name = m.str("name")
	out.AppendCommitMessageToRunName = m.boolean("appendCommitMessageToRunName")
	if e := m.take("trigger"); e != nil {
		out.Trigger = p.trigger(e.value, e.name, false)
	}
	if e := m.take("pr"); e != nil {
		out.PR = p.trigger(e.value, e.name, true)
	}
	if e := m.take("schedules"); e != nil {
		out.Schedules = parseList(p, e.value, e.name, item(p, "schedule", p.fillSchedule, "cron"))
	}
	out.LockBehavior = m.enum("lockBehavior", lockBehaviors...)
	if e := m.take("resources"); e != nil {
		out.Resources = buildOrInsert[ast.Resources](p, e.value, e.name, p.fillResources)
	}
	if e := m.take("parameters"); e != nil {
		out.Parameters = p.parameters(e.value, e.name)
	}
	if e := m.take("variables"); e != nil {
		out.Variables = p.variables(e.value, e.name)
	}
	if e := m.take("pool"); e != nil {
		out.Pool = p.pool(e.value, e.name)
	}
	if e := m.take("extends"); e != nil {
		out.Extends = buildOrInsert[ast.Extends](p, e.value, e.name, p.fillExtends)
	}
	if e := m.take("stages"); e != nil {
		out.Stages = parseList(p, e.value, e.name, p.stage)
	}
	if e := m.take("jobs"); e != nil {
		out.Jobs = parseList(p, e.value, e.name, p.job)
	}
	if e := m.take("steps"); e != nil {
		out.Steps = parseList(p, e.value, e.name, p.step)
	}
	if e := m.take("strategy"); e != nil {
		out.Strategy = buildOrInsert[ast.Strategy](p, e.value, e.name, p.fillStrategy)
	}
	out.ContinueOnError = m.boolean("continueOnError")
	if e := m.take("container"); e != nil {
		out.Container = p.container(e.value, e.name)
	}
	out.Services = m.strMap("services")
	if e := m.take("workspace"); e != nil {
		out.Workspace = buildOrInsert[ast.Workspace](p, e.value, e.name, p.fillWorkspace)
	}
	out.Inserts, out.Extra = finish(m, p.fillPipeline)
}

// ---------------------------------------------------------------------------
// Triggers.

func (p *parser) trigger(n *yamlx.Node, ctx string, pullRequest bool) *ast.Trigger {
	if n == nil {
		return nil
	}
	switch n.Kind() {
	case yamlx.SequenceKind:
		list := p.scalarList(n, ctx)
		if list == nil {
			return nil
		}
		return &ast.Trigger{
			Pos: pos(n), Form: ast.TriggerList,
			Branches: &ast.Filter{Pos: pos(n), Bare: true, Include: list},
		}
	case yamlx.ScalarKind:
		s := p.scalar(n, ctx)
		if s == nil {
			return nil
		}
		out := &ast.Trigger{Pos: pos(n)}
		switch {
		case s.Whole() != nil:
			out.Form, out.Insertion = ast.TriggerInsertion, s.Whole()
		case strings.EqualFold(strings.TrimSpace(s.Value), "none"):
			out.Form, out.Keyword = ast.TriggerNone, s
		case s.Null:
			out.Form = ast.TriggerFull
		default:
			p.add("TRL-PARSE-0012", s.Source, fmt.Sprintf("%s: %q is not none, a list of branches or a mapping", ctx, s.Value),
				map[string]any{"key": ctx, "value": s.Value, "allowed": "none"})
			return nil
		}
		return out
	default:
		return build(p, n, ctx, func(t *ast.Trigger, m *mapping) { p.fillTrigger(t, m, pullRequest) })
	}
}

func (p *parser) fillTrigger(out *ast.Trigger, m *mapping, pullRequest bool) {
	out.Pos = pos(m.node)
	out.Form = ast.TriggerFull
	if pullRequest {
		out.AutoCancel = m.boolean("autoCancel")
		out.Drafts = m.boolean("drafts")
	} else {
		out.Batch = m.boolean("batch")
	}
	if e := m.take("branches"); e != nil {
		out.Branches = p.filter(e.value, e.name)
	}
	if e := m.take("paths"); e != nil {
		out.Paths = p.filter(e.value, e.name)
	}
	if !pullRequest {
		if e := m.take("tags"); e != nil {
			out.Tags = p.filter(e.value, e.name)
		}
	}
	out.Inserts, out.Extra = finish(m, func(t *ast.Trigger, bm *mapping) { p.fillTrigger(t, bm, pullRequest) })
}

func (p *parser) filter(n *yamlx.Node, ctx string) *ast.Filter {
	if n == nil {
		return nil
	}
	if n.Kind() == yamlx.SequenceKind {
		list := p.scalarList(n, ctx)
		if list == nil {
			return nil
		}
		return &ast.Filter{Pos: pos(n), Bare: true, Include: list}
	}
	return buildOrInsert[ast.Filter](p, n, ctx, p.fillFilter)
}

func (p *parser) fillFilter(out *ast.Filter, m *mapping) {
	out.Pos = pos(m.node)
	out.Include = m.strList("include")
	out.Exclude = m.strList("exclude")
	out.Inserts, out.Extra = finish(m, p.fillFilter)
}

func (p *parser) fillSchedule(out *ast.Schedule, m *mapping) {
	out.Pos = pos(m.node)
	out.Cron = m.str("cron")
	out.DisplayName = m.str("displayName")
	if e := m.take("branches"); e != nil {
		out.Branches = p.filter(e.value, e.name)
	}
	out.Batch = m.boolean("batch")
	out.Always = m.boolean("always")
	out.Inserts, out.Extra = finish(m, p.fillSchedule)
}

// ---------------------------------------------------------------------------
// Resources.

func (p *parser) fillResources(out *ast.Resources, m *mapping) {
	out.Pos = pos(m.node)
	if e := m.take("builds"); e != nil {
		out.Builds = parseList(p, e.value, e.name, item(p, "build", p.fillBuild, "build", "type", "connection", "source"))
	}
	if e := m.take("containers"); e != nil {
		out.Containers = parseList(p, e.value, e.name, item(p, "container", p.fillContainerResource, "container", "image"))
	}
	if e := m.take("pipelines"); e != nil {
		out.Pipelines = parseList(p, e.value, e.name, item(p, "pipeline", p.fillPipelineResource, "pipeline"))
	}
	if e := m.take("repositories"); e != nil {
		out.Repositories = parseList(p, e.value, e.name, item(p, "repository", p.fillRepository, "repository"))
	}
	if e := m.take("webhooks"); e != nil {
		out.Webhooks = parseList(p, e.value, e.name, item(p, "webhook", p.fillWebhook, "webhook", "connection"))
	}
	if e := m.take("packages"); e != nil {
		out.Packages = parseList(p, e.value, e.name, item(p, "package", p.fillPackage, "package", "type", "connection", "name"))
	}
	out.Inserts, out.Extra = finish(m, p.fillResources)
}

func (p *parser) resourceTrigger(n *yamlx.Node, ctx string) *ast.ResourceTrigger {
	if n == nil {
		return nil
	}
	if n.Kind() == yamlx.ScalarKind {
		s := p.scalar(n, ctx)
		if s == nil {
			return nil
		}
		out := &ast.ResourceTrigger{Pos: pos(n)}
		switch value := strings.ToLower(strings.TrimSpace(s.Value)); {
		case s.Whole() != nil:
			out.Form, out.Insertion = ast.ResourceTriggerInsertion, s.Whole()
		case value == "none":
			out.Form, out.Keyword = ast.ResourceTriggerNone, s
		case value == "true":
			out.Form, out.Keyword = ast.ResourceTriggerTrue, s
		case s.Null:
			out.Form = ast.ResourceTriggerFull
		default:
			p.add("TRL-PARSE-0012", s.Source, fmt.Sprintf("%s: %q is not none, true or a mapping", ctx, s.Value),
				map[string]any{"key": ctx, "value": s.Value, "allowed": "none, true"})
			return nil
		}
		return out
	}
	return build(p, n, ctx, p.fillResourceTrigger)
}

func (p *parser) fillResourceTrigger(out *ast.ResourceTrigger, m *mapping) {
	out.Pos = pos(m.node)
	out.Form = ast.ResourceTriggerFull
	out.Enabled = m.boolean("enabled")
	if e := m.take("branches"); e != nil {
		out.Branches = p.filter(e.value, e.name)
	}
	out.Stages = m.strList("stages")
	if e := m.take("tags"); e != nil {
		out.Tags = p.filter(e.value, e.name)
	}
	out.Inserts, out.Extra = finish(m, p.fillResourceTrigger)
}

func (p *parser) fillRepository(out *ast.RepositoryResource, m *mapping) {
	out.Pos = pos(m.node)
	out.Repository = m.str("repository")
	out.Endpoint = m.str("endpoint")
	if e := m.take("trigger"); e != nil {
		out.Trigger = p.trigger(e.value, e.name, false)
	}
	out.Name = m.str("name")
	out.Ref = m.str("ref")
	out.Type = m.enum("type", repositoryType...)
	out.Inserts, out.Extra = finish(m, p.fillRepository)
}

func (p *parser) fillPipelineResource(out *ast.PipelineResource, m *mapping) {
	out.Pos = pos(m.node)
	out.Pipeline = m.str("pipeline")
	out.Project = m.str("project")
	out.Source = m.str("source")
	out.Version = m.str("version")
	out.Branch = m.str("branch")
	out.Tags = m.strList("tags")
	if e := m.take("trigger"); e != nil {
		out.Trigger = p.resourceTrigger(e.value, e.name)
	}
	out.Inserts, out.Extra = finish(m, p.fillPipelineResource)
}

func (p *parser) fillContainerResource(out *ast.ContainerResource, m *mapping) {
	out.Pos = pos(m.node)
	out.Container = m.str("container")
	p.fillContainerSpec(&out.ContainerSpec, m)
	out.Type = m.str("type")
	if e := m.take("trigger"); e != nil {
		out.Trigger = p.resourceTrigger(e.value, e.name)
	}
	out.AzureSubscription = m.str("azureSubscription")
	out.ResourceGroup = m.str("resourceGroup")
	out.Registry = m.str("registry")
	out.Repository = m.str("repository")
	out.LocalImage = m.boolean("localImage")
	out.Inserts, out.Extra = finish(m, p.fillContainerResource)
}

func (p *parser) fillContainerSpec(out *ast.ContainerSpec, m *mapping) {
	out.Image = m.str("image")
	out.Endpoint = m.str("endpoint")
	out.Env = m.strMap("env")
	out.MapDockerSocket = m.boolean("mapDockerSocket")
	out.Options = m.str("options")
	out.Ports = m.strList("ports")
	out.Volumes = m.strList("volumes")
	if e := m.take("mountReadOnly"); e != nil {
		out.MountReadOnly = buildOrInsert[ast.MountReadOnly](p, e.value, e.name, p.fillMountReadOnly)
	}
}

func (p *parser) fillMountReadOnly(out *ast.MountReadOnly, m *mapping) {
	out.Pos = pos(m.node)
	out.Work = m.boolean("work")
	out.Externals = m.boolean("externals")
	out.Tools = m.boolean("tools")
	out.Tasks = m.boolean("tasks")
	out.Inserts, out.Extra = finish(m, p.fillMountReadOnly)
}

func (p *parser) fillBuild(out *ast.BuildResource, m *mapping) {
	out.Pos = pos(m.node)
	out.Build = m.str("build")
	out.Type = m.str("type")
	out.Connection = m.str("connection")
	out.Source = m.str("source")
	out.Version = m.str("version")
	out.Branch = m.str("branch")
	if e := m.take("trigger"); e != nil {
		out.Trigger = p.resourceTrigger(e.value, e.name)
	}
	out.Inserts, out.Extra = finish(m, p.fillBuild)
}

func (p *parser) fillPackage(out *ast.PackageResource, m *mapping) {
	out.Pos = pos(m.node)
	out.Package = m.str("package")
	out.Type = m.str("type")
	out.Connection = m.str("connection")
	out.Name = m.str("name")
	out.Version = m.str("version")
	out.Tag = m.str("tag")
	if e := m.take("trigger"); e != nil {
		out.Trigger = p.resourceTrigger(e.value, e.name)
	}
	out.Inserts, out.Extra = finish(m, p.fillPackage)
}

func (p *parser) fillWebhook(out *ast.WebhookResource, m *mapping) {
	out.Pos = pos(m.node)
	out.Webhook = m.str("webhook")
	out.Connection = m.str("connection")
	out.Type = m.str("type")
	if e := m.take("filters"); e != nil {
		out.Filters = parseList(p, e.value, e.name, item(p, "filter", p.fillWebhookFilter))
	}
	out.Inserts, out.Extra = finish(m, p.fillWebhook)
}

func (p *parser) fillWebhookFilter(out *ast.WebhookFilter, m *mapping) {
	out.Pos = pos(m.node)
	out.Path = m.str("path")
	out.Value = m.str("value")
	out.Inserts, out.Extra = finish(m, p.fillWebhookFilter)
}

// ---------------------------------------------------------------------------
// Parameters, variables and template references.

func (p *parser) parameters(n *yamlx.Node, ctx string) *ast.Parameters {
	if n == nil {
		return nil
	}
	switch n.Kind() {
	case yamlx.SequenceKind:
		return &ast.Parameters{
			Pos: pos(n), Form: ast.ParametersList,
			List: parseList(p, n, ctx, item(p, "parameter", p.fillParameter, "name")),
		}
	case yamlx.MappingKind:
		return &ast.Parameters{Pos: pos(n), Form: ast.ParametersMap, Map: p.anyMap(n, ctx)}
	default:
		s := p.scalar(n, ctx)
		if s == nil {
			return nil
		}
		out := &ast.Parameters{Pos: pos(n), Form: ast.ParametersList}
		switch {
		case s.Whole() != nil:
			out.Form, out.Insertion = ast.ParametersInsertion, s.Whole()
		case !s.Null:
			p.typeError(n, "sequence or mapping", ctx)
			return nil
		}
		return out
	}
}

func (p *parser) fillParameter(out *ast.Parameter, m *mapping) {
	out.Pos = pos(m.node)
	out.Name = m.str("name")
	out.DisplayName = m.str("displayName")
	out.Type = m.enum("type", parameterTypes...)
	out.Default = m.free("default")
	out.Values = m.strList("values")
	out.Inserts, out.Extra = finish(m, p.fillParameter)
}

func (p *parser) variables(n *yamlx.Node, ctx string) *ast.Variables {
	if n == nil {
		return nil
	}
	switch n.Kind() {
	case yamlx.MappingKind:
		return &ast.Variables{Pos: pos(n), Form: ast.VariablesMap, Map: p.stringMap(n, ctx)}
	case yamlx.SequenceKind:
		return &ast.Variables{Pos: pos(n), Form: ast.VariablesList, List: parseList(p, n, ctx, p.variable)}
	default:
		s := p.scalar(n, ctx)
		if s == nil {
			return nil
		}
		out := &ast.Variables{Pos: pos(n), Form: ast.VariablesMap}
		switch {
		case s.Whole() != nil:
			out.Form, out.Insertion = ast.VariablesInsertion, s.Whole()
		case !s.Null:
			p.typeError(n, "sequence or mapping", ctx)
			return nil
		}
		return out
	}
}

func (p *parser) variable(n *yamlx.Node) (ast.Variable, bool) {
	m := p.open(n, "variables")
	if m == nil {
		return nil, false
	}
	defer m.close()
	key, ok := m.selectKey("name", "group", "template")
	if !ok {
		return nil, false
	}
	switch key {
	case "name":
		out := new(ast.VariableDef)
		p.fillVariableDef(out, m)
		return out, true
	case "group":
		out := new(ast.VariableGroup)
		p.fillVariableGroup(out, m)
		return out, true
	default:
		out := new(ast.VariableTemplate)
		p.fillVariableTemplate(out, m)
		return out, true
	}
}

func (p *parser) fillVariableDef(out *ast.VariableDef, m *mapping) {
	out.Pos = pos(m.node)
	out.Name = m.str("name")
	out.Value = m.str("value")
	out.Readonly = m.boolean("readonly")
	out.Inserts, out.Extra = finish(m, p.fillVariableDef)
}

func (p *parser) fillVariableGroup(out *ast.VariableGroup, m *mapping) {
	out.Pos = pos(m.node)
	out.Group = m.str("group")
	out.Inserts, out.Extra = finish(m, p.fillVariableGroup)
}

func (p *parser) fillVariableTemplate(out *ast.VariableTemplate, m *mapping) {
	out.Pos = pos(m.node)
	p.fillTemplateRef(&out.TemplateRef, m)
	out.Inserts, out.Extra = finish(m, p.fillVariableTemplate)
}

// fillTemplateRef reads the `template` and `parameters` keys of a template
// reference and splits the path from its `@alias`.
func (p *parser) fillTemplateRef(out *ast.TemplateRef, m *mapping) {
	out.Template = m.str("template")
	if out.Template != nil {
		value := strings.TrimSpace(out.Template.Value)
		out.File = value
		if len(out.Template.Directives) > 0 {
			out.Dynamic = true
		} else if at := strings.LastIndex(value, "@"); at > 0 && at < len(value)-1 {
			out.File, out.Alias = value[:at], value[at+1:]
		}
	}
	if e := m.take("parameters"); e != nil {
		out.Parameters = p.anyMap(e.value, e.name)
	}
}

func (p *parser) fillExtends(out *ast.Extends, m *mapping) {
	out.Pos = pos(m.node)
	p.fillTemplateRef(&out.TemplateRef, m)
	out.Inserts, out.Extra = finish(m, p.fillExtends)
}
