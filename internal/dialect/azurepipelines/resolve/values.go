package resolve

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
)

func environment(parameters, variables *expr.Object) *expr.Object {
	return &expr.Object{Properties: []expr.Property{{Name: "parameters", Value: parameters}, {Name: "variables", Value: variables}}}
}

func lookupObject(env *expr.Object, name string) *expr.Object {
	if env != nil {
		if value, ok := env.Lookup(name); ok {
			if object, ok := value.(*expr.Object); ok {
				return object
			}
		}
	}
	return &expr.Object{}
}

func with(env *expr.Object, name string, value expr.Value) *expr.Object {
	properties := append([]expr.Property(nil), env.Properties...)
	for i := range properties {
		if strings.EqualFold(properties[i].Name, name) {
			properties[i].Value = value
			return &expr.Object{Properties: properties}
		}
	}
	properties = append(properties, expr.Property{Name: name, Value: value})
	return &expr.Object{Properties: properties}
}

// supplied is one call-site argument. It is evaluated lazily, once the declared
// type of the parameter decides whether literals are inferred or kept as text.
type supplied struct {
	name  string
	node  *ast.Any
	value expr.Value
	env   *expr.Object
	pos   ast.Pos
	// scope is set for a default value: it was written in the template that
	// declares the parameter, whatever file is being resolved when it is bound.
	scope *scope
}

// mapParams flattens a call-site (or legacy) parameter mapping, evaluating
// directives and dynamic keys in the caller scope.
func (r *resolver) mapParams(ctx context.Context, m *ast.Map[*ast.Any], env *expr.Object) ([]supplied, error) {
	if m == nil {
		return nil, nil
	}
	var out []supplied
	if r.rejectRuntime(m.Insertion) {
		return nil, nil
	}
	if compileTime(m.Insertion) {
		value, ok, err := r.evalOK(ctx, m.Insertion, env)
		if err != nil {
			return nil, err
		}
		if object, isObject := value.(*expr.Object); ok && isObject {
			for _, property := range object.Properties {
				out = append(out, supplied{name: property.Name, value: property.Value, pos: m.Pos})
			}
		} else if ok {
			r.addAt("TRL-EXPR-0005", m.Insertion.Pos, "parameters must be a mapping", nil)
		}
	}
	err := r.chain(ctx, env, chainOps{
		n: len(m.Entries),
		at: func(i int) (*ast.Directive, *ast.Directive) {
			if m.Entries[i].Cond != nil {
				return m.Entries[i].Cond.Directive, nil
			}
			return nil, nil
		},
		size: func(i int) int { return approximateSize(reflect.ValueOf(m.Entries[i].Cond), 0) },
		plain: func(i int) error {
			entry := m.Entries[i]
			if entry.Key == nil {
				return nil
			}
			if isInsertKey(entry.Key) {
				value, err := r.resolveAny(ctx, entry.Value, env)
				if err != nil {
					return err
				}
				if object, ok := value.(*expr.Object); ok {
					for _, property := range object.Properties {
						out = append(out, supplied{name: property.Name, value: property.Value, pos: entry.Key.Pos})
					}
				} else {
					r.addAt("TRL-EXPR-0005", entry.Key.Pos, "insert requires a mapping", nil)
				}
				return nil
			}
			name, err := r.keyText(ctx, entry.Key, env)
			if err != nil {
				return err
			}
			pos := entry.Key.Pos
			if entry.Value != nil {
				pos = entry.Value.Pos
			}
			out = append(out, supplied{name: name, node: entry.Value, env: env, pos: pos})
			return nil
		},
		body: func(i int, bodyEnv *expr.Object, repeated bool) error {
			body := m.Entries[i].Cond.Body
			if repeated {
				body = clone(body)
			}
			nested, err := r.mapParams(ctx, body, bodyEnv)
			out = append(out, nested...)
			return err
		},
	})
	return out, err
}

func isInsertKey(key *ast.Scalar) bool {
	return key != nil && strings.TrimSpace(key.Value) == "${{ insert }}"
}

// keyText returns the evaluated text of a mapping key.
func (r *resolver) keyText(ctx context.Context, key *ast.Scalar, env *expr.Object) (string, error) {
	if err := r.expandScalar(ctx, key, env); err != nil {
		return "", err
	}
	return key.Value, nil
}

// bind binds the parameters of one file. The root file never fails on missing
// values; a template call is valid only when every parameter is declared,
// supplied or defaulted, and of its declared type.
func (r *resolver) bind(ctx context.Context, defs *ast.Parameters, args *ast.Map[*ast.Any], caller *expr.Object, root bool, callee scope) (*expr.Object, bool, error) {
	result := &expr.Object{}
	if root {
		caller = environment(&expr.Object{}, r.opts.Variables)
	}
	provided, err := r.mapParams(ctx, args, caller)
	if err != nil {
		return nil, false, err
	}
	consumed := make([]bool, len(provided))
	take := func(name string) (supplied, bool) {
		for i := range provided {
			if !consumed[i] && strings.EqualFold(provided[i].name, name) {
				consumed[i] = true
				return provided[i], true
			}
		}
		return supplied{}, false
	}
	valid := true
	switch {
	case defs != nil && defs.Form == ast.ParametersMap && defs.Map != nil:
		defaults, err := r.mapParams(ctx, defs.Map, caller)
		if err != nil {
			return nil, false, err
		}
		for _, def := range defaults {
			if arg, ok := take(def.name); ok {
				def = arg
			}
			value, err := r.suppliedValue(ctx, def, "object", caller)
			if err != nil {
				return nil, false, err
			}
			result.Properties = append(result.Properties, expr.Property{Name: def.name, Value: value})
		}
	case defs != nil && defs.List != nil:
		seen := map[string]bool{}
		for _, item := range defs.List.Items {
			parameter := item.Value
			if parameter == nil || parameter.Name == nil {
				continue
			}
			name := parameter.Name.Value
			lower := strings.ToLower(name)
			if seen[lower] {
				if !root {
					r.addAt("TRL-VAL-0008", parameter.Name.Pos, "duplicate parameter "+name, map[string]any{"name": name})
					valid = false
				}
				continue
			}
			seen[lower] = true
			kind := "object"
			if parameter.Type != nil && parameter.Type.Canon != "" {
				kind = strings.ToLower(parameter.Type.Canon)
			}
			arg, has := take(name)
			switch {
			case has:
			case parameter.Default != nil:
				arg = supplied{name: name, node: parameter.Default, env: caller, pos: parameter.Default.Pos, scope: &callee}
			case root:
				result.Properties = append(result.Properties, expr.Property{Name: name, Value: expr.Null{}})
				continue
			default:
				r.addAt("TRL-TPL-0008", parameter.Pos, "required template parameter is missing: "+name, map[string]any{"parameter": name})
				valid = false
				result.Properties = append(result.Properties, expr.Property{Name: name, Value: expr.Null{}})
				continue
			}
			value, err := r.suppliedValue(ctx, arg, kind, caller)
			if err != nil {
				return nil, false, err
			}
			typed, ok := coerce(kind, value)
			if !ok {
				r.addAt("TRL-TPL-0005", arg.pos, "parameter "+name+" does not match type "+kind, map[string]any{"parameter": name, "type": kind})
				valid = false
				typed = value
			} else if parameter.Values != nil && !allowedValue(typed, parameter.Values) {
				r.addAt("TRL-TPL-0005", arg.pos, "parameter "+name+" is not one of its allowed values", map[string]any{"parameter": name})
				valid = false
			}
			result.Properties = append(result.Properties, expr.Property{Name: name, Value: typed})
		}
	}
	for i, arg := range provided {
		if !consumed[i] {
			r.addAt("TRL-TPL-0008", arg.pos, "unknown template parameter "+arg.name, map[string]any{"parameter": arg.name})
			valid = false
		}
	}
	return result, valid || root, nil
}

// suppliedValue evaluates an argument or default. Only object-like and
// untyped parameters infer number and boolean literals; every other declared
// type keeps the literal text and coerces by its own rules.
func (r *resolver) suppliedValue(ctx context.Context, arg supplied, kind string, fallback *expr.Object) (expr.Value, error) {
	if arg.node == nil {
		if arg.value == nil {
			return expr.Null{}, nil
		}
		return arg.value, nil
	}
	env := arg.env
	if env == nil {
		env = fallback
	}
	if arg.scope != nil {
		saved := r.scope
		r.scope = *arg.scope
		defer func() { r.scope = saved }()
	}
	switch kind {
	case "string", "number", "boolean":
		return r.resolveAnyMode(ctx, arg.node, env, false)
	default:
		return r.resolveAnyMode(ctx, arg.node, env, true)
	}
}

// coerce converts a bound value to the declared parameter type.
func coerce(kind string, value expr.Value) (expr.Value, bool) {
	switch kind {
	case "string":
		switch value := value.(type) {
		case expr.String:
			return value, true
		case expr.Null:
			return expr.String(""), true
		case expr.Number, expr.Bool:
			text, _ := scalarText(value)
			return expr.String(text), true
		}
		return value, false
	case "number":
		switch value := value.(type) {
		case expr.Number:
			return value, true
		case expr.String:
			if number, err := expr.ParseNumber(strings.TrimSpace(string(value))); err == nil {
				return number, true
			}
		}
		return value, false
	case "boolean":
		switch value := value.(type) {
		case expr.Bool:
			return value, true
		case expr.String:
			switch strings.ToLower(strings.TrimSpace(string(value))) {
			case "true":
				return expr.Bool(true), true
			case "false":
				return expr.Bool(false), true
			}
		}
		return value, false
	case "object":
		return value, true
	case "step", "job", "deployment", "stage":
		_, ok := value.(*expr.Object)
		return value, ok
	case "steplist", "joblist", "deploymentlist", "stagelist":
		_, ok := value.(*expr.Array)
		return value, ok
	default:
		return value, false
	}
}

func allowedValue(value expr.Value, allowed *ast.List[*ast.Scalar]) bool {
	want, ok := scalarText(value)
	if !ok {
		return false
	}
	for _, item := range allowed.Items {
		if item.Value != nil && strings.EqualFold(item.Value.Value, want) {
			return true
		}
	}
	return false
}

// variables builds the compile-time variable object from resolved variables.
func (r *resolver) variables(vars *ast.Variables, inherited *expr.Object) *expr.Object {
	result := &expr.Object{}
	index := map[string]int{}
	set := func(name string, value expr.Value) {
		key := strings.ToUpper(name)
		if i, ok := index[key]; ok {
			result.Properties[i].Value = value
			return
		}
		index[key] = len(result.Properties)
		result.Properties = append(result.Properties, expr.Property{Name: name, Value: value})
	}
	if inherited != nil {
		for _, property := range inherited.Properties {
			set(property.Name, property.Value)
		}
	}
	if vars == nil {
		return result
	}
	if vars.Map != nil {
		for _, entry := range vars.Map.Entries {
			if entry.Cond == nil && entry.Key != nil && entry.Value != nil {
				set(entry.Key.Value, variableLiteral(entry.Value))
			}
		}
	}
	if vars.List != nil {
		for _, item := range vars.List.Items {
			if def, ok := item.Value.(*ast.VariableDef); ok && item.Cond == nil && def.Name != nil && def.Value != nil {
				set(def.Name.Value, variableLiteral(def.Value))
			}
		}
	}
	return result
}

func variableLiteral(s *ast.Scalar) expr.Value {
	if s == nil || s.Null {
		return expr.Null{}
	}
	return expr.String(s.Value)
}

func scalarLiteral(s *ast.Scalar) expr.Value {
	if s == nil || s.Null {
		return expr.Null{}
	}
	// A quoted scalar is a string regardless of its text: the decoded value
	// cannot tell 60 from '60', so quoting wins before bool/number inference
	// (ADR 0009, issue #15 item 4).
	if s.Quoted {
		return expr.String(s.Value)
	}
	if strings.EqualFold(s.Value, "true") {
		return expr.Bool(true)
	}
	if strings.EqualFold(s.Value, "false") {
		return expr.Bool(false)
	}
	if number, err := expr.ParseNumber(s.Value); err == nil {
		return number
	}
	return expr.String(s.Value)
}

// isFatal reports errors that must abort resolution instead of becoming
// diagnostics: cancellation, deadline and the expansion budget.
func isFatal(ctx context.Context, err error) bool {
	return err != nil && (ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errBudget) || errors.Is(err, errTooMany))
}

// evalOK evaluates a directive. A failed evaluation is reported as a
// diagnostic (ok false, nil error); only fatal errors are returned.
func (r *resolver) evalOK(ctx context.Context, directive *ast.Directive, env *expr.Object) (expr.Value, bool, error) {
	value, err := r.eval(ctx, directive, env)
	if err != nil {
		if isFatal(ctx, err) {
			return nil, false, err
		}
		return nil, false, nil
	}
	return value, true, nil
}

func (r *resolver) eval(ctx context.Context, directive *ast.Directive, env *expr.Object) (expr.Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if directive == nil || directive.Expr == nil {
		if directive != nil {
			r.addAt("TRL-EXPR-0001", directive.Pos, "directive has no valid expression", nil)
		}
		return expr.Null{}, fmt.Errorf("missing expression")
	}
	value, err := expr.Eval(ctx, directive.Expr, env, expr.EvalOptions{Phase: expr.PhaseCompile})
	if err != nil {
		if isFatal(ctx, err) {
			return nil, err
		}
		var diagnostic *diag.Error
		if errors.As(err, &diagnostic) {
			finding := diagnostic.Finding
			// The expression package currently exposes function lookup and arity
			// failures only as TRL-EXPR-0001 diagnostics, without a typed error
			// kind. Keep the compatibility remap isolated here until it does.
			if finding.Code == "TRL-EXPR-0001" && (strings.Contains(finding.Message, "unknown function") || strings.Contains(finding.Message, "function arity")) {
				finding = diag.MustNew("TRL-EXPR-0005", diag.WithMessage(finding.Message))
			}
			position := directive.Position()
			finding.Source = &position
			if len(directive.Chain) > 0 {
				if finding.Params == nil {
					finding.Params = map[string]any{}
				}
				finding.Params["callSites"] = callSites(directive.Chain)
			}
			r.report(finding)
		} else {
			r.addAt("TRL-EXPR-0005", directive.Pos, err.Error(), nil)
		}
		return nil, err
	}
	return value, nil
}

// truthy mirrors the expression engine's conversion to boolean.
func truthy(value expr.Value) bool {
	switch value := value.(type) {
	case nil, expr.Null:
		return false
	case expr.Bool:
		return bool(value)
	case expr.String:
		return value != ""
	case expr.Number:
		return value.ExactString() != "0"
	default:
		return true
	}
}

func scalarText(value expr.Value) (string, bool) {
	switch value := value.(type) {
	case expr.Null:
		return "", true
	case expr.String:
		return string(value), true
	case expr.Bool:
		if value {
			return "True", true
		}
		return "False", true
	case expr.Number:
		return value.String(), true
	case expr.Version:
		return value.String(), true
	case expr.DateTime:
		return value.Time.Format("2006-01-02 15:04:05-07:00"), true
	default:
		return "", false
	}
}
