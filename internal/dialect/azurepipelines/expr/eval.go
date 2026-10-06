package expr

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Context resolves named expression roots such as variables, parameters,
// dependencies, stageDependencies, and pipeline.
type Context interface {
	Lookup(name string) (any, bool)
}

// MapContext is a convenient case-insensitive Context implementation.
type MapContext map[string]any

// Lookup implements Context.
func (m MapContext) Lookup(name string) (any, bool) { return lookupStringMap(m, name) }

// CounterProvider supplies persistent counter values. Implementations own
// concurrency and persistence; the evaluator keeps no mutable global state.
type CounterProvider interface {
	Next(context.Context, string, int64) (int64, error)
}

// StatusProvider evaluates agent/job status predicates.
type StatusProvider interface {
	Status(context.Context, string, []string) (bool, error)
}

// EvalOptions controls phase-sensitive evaluation.
type EvalOptions struct {
	Phase              Phase
	Counter            CounterProvider
	Status             StatusProvider
	VariableDefinition bool
}

// EvalError is an expression type, lookup, arity, or phase error.
type EvalError struct {
	Code string
	Span Span
	Msg  string
}

func (e *EvalError) Error() string {
	return fmt.Sprintf("%s at bytes %d:%d: %s", e.Code, e.Span.Start, e.Span.End, e.Msg)
}

// Eval evaluates an AST without mutating it or its Context.
func Eval(ctx context.Context, node Expr, values Context, options EvalOptions) (any, error) {
	if options.Phase == PhaseMacro {
		return nil, &EvalError{Code: "TRL-EXPR-0001", Span: node.Range(), Msg: "macro phase uses ExpandMacros"}
	}
	e := evaluator{ctx: ctx, values: values, options: options}
	return e.eval(node)
}

type evaluator struct {
	ctx     context.Context
	values  Context
	options EvalOptions
}

func (e evaluator) eval(node Expr) (any, error) {
	switch n := node.(type) {
	case *Literal:
		return n.Value, nil
	case *Identifier:
		if e.options.Phase == PhaseRuntime && strings.EqualFold(n.Name, "parameters") {
			return nil, e.failure(n, "parameters are unavailable at runtime")
		}
		if e.options.Phase == PhaseCompile && (strings.EqualFold(n.Name, "dependencies") || strings.EqualFold(n.Name, "stageDependencies")) {
			return nil, e.failure(n, n.Name+" is unavailable at compile time")
		}
		if e.values == nil {
			return nil, e.failure(n, "unknown name "+n.Name)
		}
		value, ok := e.values.Lookup(n.Name)
		if !ok {
			return nil, e.failure(n, "unknown name "+n.Name)
		}
		return value, nil
	case *Member:
		value, err := e.eval(n.Receiver)
		if err != nil {
			return nil, err
		}
		if n.Wildcard {
			items, ok := valuesOf(value)
			if !ok {
				return nil, e.typeFailure(n, "wildcard requires an array or object")
			}
			return projected(items), nil
		}
		value, ok := selectMember(value, n.Name)
		if !ok {
			return nil, e.failure(n, "property not found: "+n.Name)
		}
		return value, nil
	case *Index:
		receiver, err := e.eval(n.Receiver)
		if err != nil {
			return nil, err
		}
		key, err := e.eval(n.Key)
		if err != nil {
			return nil, err
		}
		value, ok := selectIndex(receiver, key)
		if !ok {
			return nil, e.failure(n, "index not found: "+stringify(key))
		}
		return value, nil
	case *Call:
		return e.evalCall(n)
	default:
		return nil, e.failure(node, fmt.Sprintf("unsupported node %T", node))
	}
}

type projected []any

func selectMember(value any, name string) (any, bool) {
	if list, ok := value.(projected); ok {
		out := make(projected, 0, len(list))
		for _, item := range list {
			if selected, found := lookupProperty(item, name); found {
				out = append(out, selected)
			}
		}
		return out, true
	}
	return lookupProperty(value, name)
}

func selectIndex(value, key any) (any, bool) {
	if list, ok := value.(projected); ok {
		out := make(projected, 0, len(list))
		for _, item := range list {
			if selected, found := selectIndex(item, key); found {
				out = append(out, selected)
			}
		}
		return out, true
	}
	if text, ok := key.(string); ok {
		return lookupProperty(value, text)
	}
	number, ok := toFloat(key)
	if !ok || number < 0 || number != float64(int(number)) {
		return nil, false
	}
	rv := indirect(reflect.ValueOf(value))
	if !rv.IsValid() || (rv.Kind() != reflect.Array && rv.Kind() != reflect.Slice) || int(number) >= rv.Len() {
		return nil, false
	}
	return rv.Index(int(number)).Interface(), true
}

func lookupProperty(value any, name string) (any, bool) {
	if value == nil {
		return nil, false
	}
	if m, ok := value.(map[string]any); ok {
		return lookupStringMap(m, name)
	}
	if m, ok := value.(MapContext); ok {
		return lookupStringMap(m, name)
	}
	rv := indirect(reflect.ValueOf(value))
	if !rv.IsValid() {
		return nil, false
	}
	switch rv.Kind() {
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return nil, false
		}
		iter := rv.MapRange()
		for iter.Next() {
			if strings.EqualFold(iter.Key().String(), name) {
				return iter.Value().Interface(), true
			}
		}
	case reflect.Struct:
		typ := rv.Type()
		for i := range rv.NumField() {
			field := typ.Field(i)
			if !field.IsExported() || !rv.Field(i).CanInterface() {
				continue
			}
			jsonName := strings.Split(field.Tag.Get("json"), ",")[0]
			if strings.EqualFold(field.Name, name) || jsonName != "" && strings.EqualFold(jsonName, name) {
				return rv.Field(i).Interface(), true
			}
		}
	}
	return nil, false
}

func lookupStringMap[V any](m map[string]V, name string) (V, bool) {
	if value, ok := m[name]; ok {
		return value, true
	}
	for key, value := range m {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	var zero V
	return zero, false
}

func valuesOf(value any) ([]any, bool) {
	rv := indirect(reflect.ValueOf(value))
	if !rv.IsValid() {
		return nil, false
	}
	var out []any
	switch rv.Kind() {
	case reflect.Array, reflect.Slice:
		out = make([]any, rv.Len())
		for i := range rv.Len() {
			out[i] = rv.Index(i).Interface()
		}
	case reflect.Map:
		out = make([]any, 0, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out = append(out, iter.Value().Interface())
		}
	default:
		return nil, false
	}
	return out, true
}

func indirect(value reflect.Value) reflect.Value {
	for value.IsValid() && (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) {
		if value.IsNil() {
			return reflect.Value{}
		}
		value = value.Elem()
	}
	return value
}

func (e evaluator) failure(node Expr, msg string) error {
	return &EvalError{Code: "TRL-EXPR-0001", Span: node.Range(), Msg: msg}
}

func (e evaluator) typeFailure(node Expr, msg string) error {
	return &EvalError{Code: "TRL-EXPR-0005", Span: node.Range(), Msg: msg}
}

func stringify(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "True"
		}
		return "False"
	case Version:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'g', -1, 32)
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(value)
	}
}
