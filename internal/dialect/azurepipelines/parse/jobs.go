package parse

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
)

var (
	pipelineIdentifier        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	resourceIdentifierPattern = regexp.MustCompile(`^[-_A-Za-z0-9]+$`)
)

func (p *parser) identifier(value *ast.Scalar, kind string) {
	if value == nil || value.Null || len(value.Directives) != 0 || pipelineIdentifier.MatchString(value.Value) {
		return
	}
	p.add("TRL-PARSE-0016", value.Source,
		fmt.Sprintf("%s identifier %q must contain only letters, digits, and underscores and must not start with a digit", kind, value.Value),
		map[string]any{"kind": kind, "identifier": value.Value})
}

func (p *parser) resourceIdentifier(value *ast.Scalar, kind string) {
	if value == nil || value.Null || len(value.Directives) != 0 || resourceIdentifierPattern.MatchString(value.Value) {
		return
	}
	p.add("TRL-PARSE-0016", value.Source,
		fmt.Sprintf("%s identifier %q contains unsupported characters", kind, value.Value),
		map[string]any{"kind": kind, "identifier": value.Value})
}

// ---------------------------------------------------------------------------
// Stages.

func (p *parser) stage(n *yamlx.Node) (ast.Stage, bool) {
	m := p.open(n, "stage")
	if m == nil {
		return nil, false
	}
	defer m.close()
	key, ok := m.selectKey("stage", "template")
	if !ok {
		return nil, false
	}
	if key == "template" {
		out := new(ast.StageTemplate)
		p.fillStageTemplate(out, m)
		return out, true
	}
	out := new(ast.StageDef)
	p.fillStage(out, m)
	return out, true
}

func (p *parser) fillStageTemplate(out *ast.StageTemplate, m *mapping) {
	out.Pos = pos(m.node)
	p.fillTemplateRef(&out.TemplateRef, m)
	out.Inserts, out.Extra = finish(m, p.fillStageTemplate)
}

func (p *parser) fillStage(out *ast.StageDef, m *mapping) {
	out.Pos = pos(m.node)
	out.Stage = m.str("stage")
	p.identifier(out.Stage, "stage")
	out.Group = m.str("group")
	out.DisplayName = m.str("displayName")
	if e := m.take("pool"); e != nil {
		out.Pool = p.pool(e.value, e.name)
	}
	if e := m.take("dependsOn"); e != nil {
		out.DependsOn = p.dependsOn(e.value, e.name)
	}
	out.Condition = m.expression("condition")
	if e := m.take("variables"); e != nil {
		out.Variables = p.variables(e.value, e.name)
	}
	if e := m.take("jobs"); e != nil {
		out.Jobs = parseList(p, e.value, e.name, p.job)
	}
	out.LockBehavior = m.enum("lockBehavior", lockBehaviors...)
	out.Trigger = m.enum("trigger", stageTriggers...)
	out.IsSkippable = m.boolean("isSkippable")
	out.TemplateContext = m.free("templateContext")
	out.Inserts, out.Extra = finish(m, p.fillStage)
}

// ---------------------------------------------------------------------------
// Jobs and deployments.

func (p *parser) job(n *yamlx.Node) (ast.Job, bool) {
	m := p.open(n, "job")
	if m == nil {
		return nil, false
	}
	defer m.close()
	key, ok := m.selectKey("job", "deployment", "template")
	if !ok {
		return nil, false
	}
	switch key {
	case "template":
		out := new(ast.JobTemplate)
		p.fillJobTemplate(out, m)
		return out, true
	case "deployment":
		out := new(ast.Deployment)
		p.fillDeployment(out, m)
		return out, true
	default:
		out := new(ast.JobDef)
		p.fillJob(out, m)
		return out, true
	}
}

func (p *parser) fillJobTemplate(out *ast.JobTemplate, m *mapping) {
	out.Pos = pos(m.node)
	p.fillTemplateRef(&out.TemplateRef, m)
	out.Inserts, out.Extra = finish(m, p.fillJobTemplate)
}

func (p *parser) fillJob(out *ast.JobDef, m *mapping) {
	out.Pos = pos(m.node)
	out.Job = m.str("job")
	p.identifier(out.Job, "job")
	out.DisplayName = m.str("displayName")
	if e := m.take("dependsOn"); e != nil {
		out.DependsOn = p.dependsOn(e.value, e.name)
	}
	out.Condition = m.expression("condition")
	out.ContinueOnError = m.boolean("continueOnError")
	out.TimeoutInMinutes = m.str("timeoutInMinutes")
	out.CancelTimeoutInMinutes = m.str("cancelTimeoutInMinutes")
	if e := m.take("variables"); e != nil {
		out.Variables = p.variables(e.value, e.name)
	}
	if e := m.take("strategy"); e != nil {
		out.Strategy = buildOrInsert[ast.Strategy](p, e.value, e.name, p.fillStrategy)
	}
	if e := m.take("pool"); e != nil {
		out.Pool = p.pool(e.value, e.name)
	}
	if e := m.take("container"); e != nil {
		out.Container = p.container(e.value, e.name)
	}
	if e := m.take("services"); e != nil {
		out.Services, out.ServiceSpecs = p.services(e.value, e.name)
	}
	if e := m.take("workspace"); e != nil {
		out.Workspace = buildOrInsert[ast.Workspace](p, e.value, e.name, p.fillWorkspace)
	}
	if e := m.take("uses"); e != nil {
		out.Uses = buildOrInsert[ast.Uses](p, e.value, e.name, p.fillUses)
	}
	if e := m.take("steps"); e != nil {
		out.Steps = parseList(p, e.value, e.name, p.step)
	}
	out.TemplateContext = m.free("templateContext")
	out.Inserts, out.Extra = finish(m, p.fillJob)
}

func (p *parser) fillDeployment(out *ast.Deployment, m *mapping) {
	out.Pos = pos(m.node)
	out.Deployment = m.str("deployment")
	p.identifier(out.Deployment, "deployment")
	if out.Deployment != nil && out.Deployment.Value == "deploy" {
		p.add("TRL-PARSE-0016", out.Deployment.Source, "deployment identifier deploy is reserved",
			map[string]any{"kind": "deployment", "identifier": out.Deployment.Value})
	}
	out.DisplayName = m.str("displayName")
	if e := m.take("dependsOn"); e != nil {
		out.DependsOn = p.dependsOn(e.value, e.name)
	}
	out.Condition = m.expression("condition")
	out.ContinueOnError = m.boolean("continueOnError")
	out.TimeoutInMinutes = m.str("timeoutInMinutes")
	out.CancelTimeoutInMinutes = m.str("cancelTimeoutInMinutes")
	if e := m.take("variables"); e != nil {
		out.Variables = p.variables(e.value, e.name)
	}
	if e := m.take("pool"); e != nil {
		out.Pool = p.pool(e.value, e.name)
	}
	if e := m.take("environment"); e != nil {
		out.Environment = p.environment(e.value, e.name)
	}
	if e := m.take("strategy"); e != nil {
		out.Strategy = buildOrInsert[ast.DeploymentStrategy](p, e.value, e.name, p.fillDeploymentStrategy)
	}
	if e := m.take("workspace"); e != nil {
		out.Workspace = buildOrInsert[ast.Workspace](p, e.value, e.name, p.fillWorkspace)
	}
	if e := m.take("uses"); e != nil {
		out.Uses = buildOrInsert[ast.Uses](p, e.value, e.name, p.fillUses)
	}
	if e := m.take("container"); e != nil {
		out.Container = p.container(e.value, e.name)
	}
	if e := m.take("services"); e != nil {
		out.Services, out.ServiceSpecs = p.services(e.value, e.name)
	}
	out.TemplateContext = m.free("templateContext")
	out.Inserts, out.Extra = finish(m, p.fillDeployment)
}

func (p *parser) services(n *yamlx.Node, ctx string) (*ast.Map[*ast.Scalar], *ast.Map[*ast.Any]) {
	if n != nil && n.Kind() == yamlx.MappingKind {
		pairs, problems := n.Map()
		if len(problems) == 0 {
			allScalar := true
			for _, pair := range pairs {
				allScalar = allScalar && pair.Value != nil && pair.Value.Kind() == yamlx.ScalarKind
			}
			if allScalar {
				return p.stringMap(n, ctx), nil
			}
		}
	}
	return nil, p.anyMap(n, ctx)
}

func (p *parser) environment(n *yamlx.Node, ctx string) *ast.Environment {
	if n == nil {
		return nil
	}
	if n.Kind() == yamlx.ScalarKind {
		s := p.scalar(n, ctx)
		if s == nil {
			return nil
		}
		out := &ast.Environment{Pos: pos(n)}
		if d := s.Whole(); d != nil {
			out.Insertion = d
			return out
		}
		out.Short, out.Name = true, s
		return out
	}
	return build(p, n, ctx, p.fillEnvironment)
}

func (p *parser) fillEnvironment(out *ast.Environment, m *mapping) {
	out.Pos = pos(m.node)
	out.Name = m.str("name")
	out.ResourceName = m.str("resourceName")
	out.ResourceID = m.str("resourceId")
	out.ResourceType = m.str("resourceType")
	out.Tags = m.str("tags")
	out.Inserts, out.Extra = finish(m, p.fillEnvironment)
}

func (p *parser) fillStrategy(out *ast.Strategy, m *mapping) {
	out.Pos = pos(m.node)
	if e := m.take("matrix"); e != nil {
		out.Matrix = mapOf(p, e.value, e.name, func(v *yamlx.Node, key string) (*ast.Map[*ast.Scalar], bool) {
			leg := p.stringMap(v, key)
			return leg, leg != nil
		})
		if out.Matrix != nil {
			for _, entry := range out.Matrix.Entries {
				if entry.Key != nil {
					p.identifier(entry.Key, "matrix leg")
				}
			}
		}
	}
	out.MaxParallel = m.str("maxParallel")
	out.Parallel = m.str("parallel")
	if out.Matrix != nil && out.Parallel != nil {
		p.add("TRL-PARSE-0014", out.Parallel.Source, "strategy: parallel and matrix are mutually exclusive",
			map[string]any{"keys": "parallel, matrix"})
	}
	if out.MaxParallel != nil && out.Matrix == nil {
		p.add("TRL-PARSE-0014", out.MaxParallel.Source, "strategy: maxParallel requires matrix",
			map[string]any{"keys": "maxParallel without matrix"})
	}
	out.Inserts, out.Extra = finish(m, p.fillStrategy)
}

func (p *parser) fillDeploymentStrategy(out *ast.DeploymentStrategy, m *mapping) {
	out.Pos = pos(m.node)
	if e := m.take("runOnce"); e != nil {
		out.RunOnce = buildOrInsert[ast.Lifecycle](p, e.value, e.name, p.fillLifecycle)
	}
	if e := m.take("rolling"); e != nil {
		out.Rolling = buildOrInsert[ast.Lifecycle](p, e.value, e.name, p.fillLifecycle)
	}
	if e := m.take("canary"); e != nil {
		out.Canary = buildOrInsert[ast.Lifecycle](p, e.value, e.name, p.fillLifecycle)
	}
	out.Inserts, out.Extra = finish(m, p.fillDeploymentStrategy)
}

func (p *parser) fillLifecycle(out *ast.Lifecycle, m *mapping) {
	out.Pos = pos(m.node)
	out.MaxParallel = m.str("maxParallel")
	out.Increments = m.strList("increments")
	if e := m.take("preDeploy"); e != nil {
		out.PreDeploy = buildOrInsert[ast.Hook](p, e.value, e.name, p.fillHook)
	}
	if e := m.take("deploy"); e != nil {
		out.Deploy = buildOrInsert[ast.Hook](p, e.value, e.name, p.fillHook)
	}
	if e := m.take("routeTraffic"); e != nil {
		out.RouteTraffic = buildOrInsert[ast.Hook](p, e.value, e.name, p.fillHook)
	}
	if e := m.take("postRouteTraffic"); e != nil {
		out.PostRouteTraffic = buildOrInsert[ast.Hook](p, e.value, e.name, p.fillHook)
	}
	if e := m.take("on"); e != nil {
		out.On = buildOrInsert[ast.OnHooks](p, e.value, e.name, p.fillOn)
	}
	out.Inserts, out.Extra = finish(m, p.fillLifecycle)
}

func (p *parser) fillOn(out *ast.OnHooks, m *mapping) {
	out.Pos = pos(m.node)
	if e := m.take("failure"); e != nil {
		out.Failure = buildOrInsert[ast.Hook](p, e.value, e.name, p.fillHook)
	}
	if e := m.take("success"); e != nil {
		out.Success = buildOrInsert[ast.Hook](p, e.value, e.name, p.fillHook)
	}
	out.Inserts, out.Extra = finish(m, p.fillOn)
}

func (p *parser) fillHook(out *ast.Hook, m *mapping) {
	out.Pos = pos(m.node)
	if e := m.take("steps"); e != nil {
		out.Steps = parseList(p, e.value, e.name, p.step)
	}
	if e := m.take("pool"); e != nil {
		out.Pool = p.pool(e.value, e.name)
	}
	out.Inserts, out.Extra = finish(m, p.fillHook)
}

func (p *parser) fillWorkspace(out *ast.Workspace, m *mapping) {
	out.Pos = pos(m.node)
	out.Clean = m.enum("clean", cleanModes...)
	out.Inserts, out.Extra = finish(m, p.fillWorkspace)
}

func (p *parser) fillUses(out *ast.Uses, m *mapping) {
	out.Pos = pos(m.node)
	out.Repositories = m.strList("repositories")
	out.Pools = m.strList("pools")
	out.Inserts, out.Extra = finish(m, p.fillUses)
}

// ---------------------------------------------------------------------------
// Pool, container and dependencies.

func (p *parser) pool(n *yamlx.Node, ctx string) *ast.Pool {
	if n == nil {
		return nil
	}
	if n.Kind() == yamlx.ScalarKind {
		s := p.scalar(n, ctx)
		if s == nil {
			return nil
		}
		out := &ast.Pool{Pos: pos(n)}
		if d := s.Whole(); d != nil {
			out.Insertion = d
			return out
		}
		out.Short, out.Name = true, s
		return out
	}
	return build(p, n, ctx, p.fillPool)
}

func (p *parser) fillPool(out *ast.Pool, m *mapping) {
	out.Pos = pos(m.node)
	out.Name = m.str("name")
	out.VMImage = m.str("vmImage")
	if e := m.take("demands"); e != nil {
		if e.value != nil && e.value.Kind() == yamlx.ScalarKind {
			if s := p.scalar(e.value, e.name); s != nil {
				demands := &ast.List[*ast.Scalar]{Pos: pos(e.value)}
				switch {
				case s.Whole() != nil:
					demands.Insertion = s.Whole()
				case !s.Null:
					out.DemandsScalar = true
					demands.Items = []ast.ListItem[*ast.Scalar]{{Value: s}}
				}
				out.Demands = demands
			}
		} else {
			out.Demands = p.scalarList(e.value, e.name)
		}
	}
	out.Inserts, out.Extra = finish(m, p.fillPool)
}

func (p *parser) container(n *yamlx.Node, ctx string) *ast.Container {
	if n == nil {
		return nil
	}
	if n.Kind() == yamlx.ScalarKind {
		s := p.scalar(n, ctx)
		if s == nil {
			return nil
		}
		out := &ast.Container{Pos: pos(n)}
		if d := s.Whole(); d != nil {
			out.Insertion = d
			return out
		}
		out.Alias = s
		return out
	}
	return build(p, n, ctx, p.fillContainer, "image")
}

func (p *parser) fillContainer(out *ast.Container, m *mapping) {
	out.Pos = pos(m.node)
	out.Spec = new(ast.ContainerSpec)
	p.fillContainerSpec(out.Spec, m)
	out.Inserts, out.Extra = finish(m, p.fillContainer)
}

func (p *parser) dependsOn(n *yamlx.Node, ctx string) *ast.DependsOn {
	if n == nil {
		return nil
	}
	if n.Kind() == yamlx.SequenceKind {
		list := p.scalarList(n, ctx)
		if list == nil {
			return nil
		}
		return &ast.DependsOn{Pos: pos(n), Names: list}
	}
	s := p.scalar(n, ctx)
	if s == nil {
		return nil
	}
	out := &ast.DependsOn{Pos: pos(n)}
	switch {
	case s.Whole() != nil:
		out.Insertion = s.Whole()
	case s.Null:
		out.Names = &ast.List[*ast.Scalar]{Pos: pos(n)}
	default:
		out.Scalar = true
		out.Names = &ast.List[*ast.Scalar]{Pos: pos(n), Items: []ast.ListItem[*ast.Scalar]{{Value: s}}}
	}
	return out
}

// target reads the execution context of a step.
func (p *parser) target(n *yamlx.Node, ctx string) *ast.Target {
	if n == nil {
		return nil
	}
	if n.Kind() == yamlx.ScalarKind {
		s := p.scalar(n, ctx)
		if s == nil {
			return nil
		}
		out := &ast.Target{Pos: pos(n)}
		if d := s.Whole(); d != nil {
			out.Insertion = d
			return out
		}
		out.Short, out.Container = true, s
		return out
	}
	return build(p, n, ctx, p.fillTarget)
}

func (p *parser) fillTarget(out *ast.Target, m *mapping) {
	out.Pos = pos(m.node)
	out.Container = m.str("container")
	out.Commands = m.enum("commands", commandModes...)
	if e := m.take("settableVariables"); e != nil {
		out.SettableVariables = p.settableVariables(e.value, e.name)
	}
	out.Inserts, out.Extra = finish(m, p.fillTarget)
}

func (p *parser) settableVariables(n *yamlx.Node, ctx string) *ast.SettableVariables {
	if n == nil {
		return nil
	}
	if n.Kind() == yamlx.SequenceKind {
		list := p.scalarList(n, ctx)
		if list == nil {
			return nil
		}
		return &ast.SettableVariables{Pos: pos(n), List: list}
	}
	s := p.scalar(n, ctx)
	if s == nil {
		return nil
	}
	out := &ast.SettableVariables{Pos: pos(n)}
	switch {
	case s.Whole() != nil:
		out.Insertion = s.Whole()
	case strings.EqualFold(strings.TrimSpace(s.Value), "none"):
		out.None = true
	case s.Null:
	default:
		p.add("TRL-PARSE-0012", s.Source, fmt.Sprintf("%s: %q is not none or a list of variable names", ctx, s.Value),
			map[string]any{"key": ctx, "value": s.Value, "allowed": "none"})
		return nil
	}
	return out
}
