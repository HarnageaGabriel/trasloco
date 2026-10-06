package expr

import (
	"context"
	"fmt"
	"strings"
)

// Context resolves typed expression roots without unordered Go maps.
type Context interface{ Lookup(string) (Value, bool) }

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
	// Scope is step or job; names are invalid in step conditions.
	Scope string
}

// EvalError carries an expression failure and relative byte offsets.
// ADR 0002 migration awaits diag.Error in the shared diagnostics package.
type EvalError struct {
	Code string
	Span Span
	Msg  string
}

func (e *EvalError) Error() string {
	return fmt.Sprintf("%s at bytes %d:%d: %s", e.Code, e.Span.Start, e.Span.End, e.Msg)
}

// MaxDepth and MaxExpressionLength are DTExpressions v1 parser limits.
const (
	MaxDepth            = 50
	MaxExpressionLength = 21000
)

// MaxEvaluationMemory is the reference evaluator's one MiB memory budget.
const MaxEvaluationMemory = 1048576

// Eval evaluates typed values without mutating the AST or context.
func Eval(ctx context.Context, node Expr, values Context, options EvalOptions) (Value, error) {
	e := evaluator{ctx: ctx, values: values, options: options}
	if node == nil {
		return nil, syntaxError(0, "nil expression")
	}
	if options.Phase == PhaseMacro {
		return nil, e.failure(node, "macro phase uses ExpandMacros")
	}
	return e.eval(node)
}

type evaluator struct {
	ctx     context.Context
	values  Context
	options EvalOptions
	depth   int
	memory  int
}

func (e *evaluator) eval(node Expr) (Value, error) {
	if err := e.ctx.Err(); err != nil {
		return nil, err
	}
	if e.depth >= MaxDepth {
		return nil, e.failure(node, "expression exceeds depth limit")
	}
	e.depth++
	defer func() { e.depth-- }()
	value, err := e.evalNode(node)
	if err != nil {
		return nil, err
	}
	cost := valueMemory(value, MaxDepth)
	if cost > MaxEvaluationMemory-e.memory {
		return nil, e.failure(node, "expression exceeds evaluation memory")
	}
	e.memory += cost
	return value, nil
}

func (e *evaluator) evalNode(node Expr) (Value, error) {
	switch n := node.(type) {
	case *Literal:
		return n.Value, nil
	case *Identifier:
		name := strings.ToLower(n.Name)
		if e.options.Phase == PhaseRuntime && name == "parameters" {
			return nil, e.failure(n, "parameters unavailable at runtime")
		}
		if e.options.Phase == PhaseCompile && (name == "dependencies" || name == "stagedependencies") {
			return nil, e.failure(n, "dependencies unavailable at compile time")
		}
		if e.values != nil {
			if v, ok := e.values.Lookup(n.Name); ok {
				return v, nil
			}
		}
		switch name {
		case "variables", "parameters", "dependencies", "stagedependencies", "pipeline":
			return &Object{}, nil
		}
		return nil, e.failure(n, "unknown named value")
	case *Member:
		v, err := e.eval(n.Receiver)
		if err != nil {
			return nil, err
		}
		if n.Wildcard {
			items, _ := valuesOf(v)
			return &projection{Array: Array{Items: items}}, nil
		}
		return selectIndex(v, String(n.Name)), nil
	case *Index:
		v, err := e.eval(n.Receiver)
		if err != nil {
			return nil, err
		}
		key, err := e.eval(n.Key)
		if err != nil {
			return nil, err
		}
		return selectIndex(v, key), nil
	case *Call:
		return e.evalCall(n)
	default:
		return nil, e.failure(node, "unsupported expression node")
	}
}

type projection struct{ Array }

func selectIndex(v, key Value) Value {
	if p, ok := v.(*projection); ok {
		out := &projection{}
		for _, item := range p.Items {
			selected := selectIndex(item, key)
			if _, missing := selected.(Null); !missing {
				out.Items = append(out.Items, selected)
			}
		}
		return out
	}
	switch v := v.(type) {
	case *Object:
		if s, ok := stringify(key); ok {
			result, _ := v.Lookup(s)
			return result
		}
	case *Array:
		if n, ok := toNumber(key); ok {
			r := n.rat()
			index := newIntegerQuotient(r)
			if index.IsInt64() {
				i := index.Int64()
				if i >= 0 && i < int64(len(v.Items)) {
					return v.Items[i]
				}
			}
		}
	}
	return Null{}
}

func lookupProperty(v Value, name string) (Value, bool) {
	if o, ok := v.(*Object); ok {
		return o.Lookup(name)
	}
	return Null{}, false
}

func valuesOf(v Value) ([]Value, bool) {
	switch v := v.(type) {
	case *Array:
		return v.Items, true
	case *projection:
		return v.Items, true
	case *Object:
		out := make([]Value, len(v.Properties))
		for i, p := range v.Properties {
			out[i] = p.Value
		}
		return out, true
	default:
		return nil, false
	}
}

func (e *evaluator) failure(node Expr, msg string) error {
	return &EvalError{Code: "TRL-EXPR-0001", Span: node.Range(), Msg: msg}
}

func (e *evaluator) typeFailure(node Expr, msg string) error {
	return &EvalError{Code: "TRL-EXPR-0005", Span: node.Range(), Msg: msg}
}

func utf16Length(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}

func valueMemory(v Value, depth int) int {
	if depth == 0 {
		return MaxEvaluationMemory + 1
	}
	if s, ok := v.(String); ok {
		return 26 + 2*utf16Length(string(s))
	}
	total := 24
	if values, ok := valuesOf(v); ok {
		for _, item := range values {
			total += valueMemory(item, depth-1)
			if total > MaxEvaluationMemory {
				return total
			}
		}
	}
	return total
}
