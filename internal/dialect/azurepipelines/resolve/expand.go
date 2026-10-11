package resolve

import (
	"context"
	"reflect"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
)

// templated expands one template reference of a list item: the template is
// opened with the item's parameters and the matching list of its result is
// returned. Results are already fully resolved in the callee scope.
func templated[T any](ctx context.Context, r *resolver, env *expr.Object, ref *ast.TemplateRef, pos ast.Pos, noun string, pick func(*ast.Pipeline) *ast.List[T]) ([]T, error) {
	pipeline, err := r.open(ctx, *ref, env)
	if err != nil || pipeline == nil {
		return nil, err
	}
	list := pick(pipeline)
	if list == nil {
		r.addAt("TRL-TPL-0009", pos, noun+" template does not define "+noun+"s", map[string]any{"kind": noun})
		return nil, nil
	}
	return listValues(list), nil
}

// templateTail resolves the key directives (`${{ if }}`, `${{ insert }}`) of a
// template reference item before it is opened, so conditional `parameters:`
// reach the callee instead of being dropped.
func (r *resolver) templateTail(ctx context.Context, item any, env *expr.Object) error {
	value := reflect.ValueOf(item)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return nil
	}
	value = value.Elem()
	self := r.takeFrame(value)
	if err := r.structInsertKeys(ctx, value, env, self); err != nil {
		return err
	}
	return r.structInserts(ctx, value, env, self)
}

func (r *resolver) expandStep(ctx context.Context, item ast.Step, env *expr.Object) ([]ast.Step, error) {
	if item == nil {
		return nil, nil
	}
	if template, ok := item.(*ast.StepTemplate); ok {
		defer r.inWrittenScope(item)()
		if err := r.templateTail(ctx, template, env); err != nil {
			return nil, err
		}
		return templated(ctx, r, env, &template.TemplateRef, template.Pos, "step", func(p *ast.Pipeline) *ast.List[ast.Step] { return p.Steps })
	}
	return []ast.Step{item}, r.walk(ctx, reflect.ValueOf(item), env, 0)
}

func (r *resolver) expandJob(ctx context.Context, item ast.Job, env *expr.Object) ([]ast.Job, error) {
	if item == nil {
		return nil, nil
	}
	if template, ok := item.(*ast.JobTemplate); ok {
		defer r.inWrittenScope(item)()
		if err := r.templateTail(ctx, template, env); err != nil {
			return nil, err
		}
		return templated(ctx, r, env, &template.TemplateRef, template.Pos, "job", func(p *ast.Pipeline) *ast.List[ast.Job] { return p.Jobs })
	}
	return []ast.Job{item}, r.walk(ctx, reflect.ValueOf(item), env, 0)
}

func (r *resolver) expandStage(ctx context.Context, item ast.Stage, env *expr.Object) ([]ast.Stage, error) {
	if item == nil {
		return nil, nil
	}
	if template, ok := item.(*ast.StageTemplate); ok {
		defer r.inWrittenScope(item)()
		if err := r.templateTail(ctx, template, env); err != nil {
			return nil, err
		}
		return templated(ctx, r, env, &template.TemplateRef, template.Pos, "stage", func(p *ast.Pipeline) *ast.List[ast.Stage] { return p.Stages })
	}
	return []ast.Stage{item}, r.walk(ctx, reflect.ValueOf(item), env, 0)
}

func (r *resolver) expandVariable(ctx context.Context, item ast.Variable, env *expr.Object) ([]ast.Variable, error) {
	if item == nil {
		return nil, nil
	}
	template, ok := item.(*ast.VariableTemplate)
	if !ok {
		return []ast.Variable{item}, r.walk(ctx, reflect.ValueOf(item), env, 0)
	}
	defer r.inWrittenScope(item)()
	if err := r.templateTail(ctx, template, env); err != nil {
		return nil, err
	}
	pipeline, err := r.open(ctx, template.TemplateRef, env)
	if err != nil || pipeline == nil || pipeline.Variables == nil {
		return nil, err
	}
	switch {
	case pipeline.Variables.List != nil:
		return listValues(pipeline.Variables.List), nil
	case pipeline.Variables.Map != nil:
		var out []ast.Variable
		for _, entry := range pipeline.Variables.Map.Entries {
			if entry.Key == nil || entry.Cond != nil {
				continue
			}
			out = append(out, &ast.VariableDef{Pos: ast.Pos{Source: entry.Key.Position(), Chain: entry.Key.Chain}, Name: entry.Key, Value: entry.Value})
		}
		return out, r.charge(len(out), 0)
	}
	return nil, nil
}

// expandVariables resolves a variables block: conditional and inserted
// entries, template references and whole-value insertion.
func (r *resolver) expandVariables(ctx context.Context, vars *ast.Variables, env *expr.Object) error {
	if vars == nil {
		return nil
	}
	if r.rejectRuntime(vars.Insertion) {
		return nil
	}
	if vars.Insertion != nil {
		insertion := vars.Insertion
		vars.Insertion = nil
		value, ok, err := r.evalOK(ctx, insertion, env)
		if err != nil || !ok {
			return err
		}
		switch value := value.(type) {
		case *expr.Object:
			mapped, mapErr := r.scalarMapFromObject(ctx, value, r.originOf(value, insertion.Pos))
			if mapErr != nil {
				return mapErr
			}
			vars.Form, vars.List = ast.VariablesMap, nil
			vars.Map = mapped
		case *expr.Array:
			ops := opsFor[ast.Variable](r)
			items, err := ops.fromValue(ctx, value, insertion.Pos)
			if err != nil {
				return err
			}
			vars.Form, vars.Map = ast.VariablesList, nil
			vars.List = &ast.List[ast.Variable]{Pos: insertion.Pos, Items: valuesToItems(items)}
		default:
			r.addAt("TRL-EXPR-0005", insertion.Pos, "variables must be a mapping or a list", nil)
		}
		return nil
	}
	if vars.Map != nil {
		if err := mapWalk(ctx, r, vars.Map, env, scalarMapOps(r), false); err != nil {
			return err
		}
	}
	if vars.List != nil {
		return listWalk(ctx, r, vars.List, env)
	}
	return nil
}
