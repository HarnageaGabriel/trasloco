package normalize

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	adoexpr "github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

func stringExpr(value string) ir.Expr { return ir.StringLiteral(value) }

// hasCondition reports whether a condition was written. An empty or null
// condition (also a template parameter that resolved to empty) is absent.
func hasCondition(value *ast.Expression) bool {
	return value != nil && !value.Null && strings.TrimSpace(value.Value) != ""
}

func (c *conv) condition(value *ast.Expression, a *acc) *ir.Cond {
	if !hasCondition(value) {
		return ir.SuccessCondition(ir.PhaseRuntime)
	}
	const detail = "a literal compared with a secret-classified variable was replaced by the redaction placeholder"
	if converted, ok := c.expression(value.Expr); ok {
		if redactSecretComparisons(&converted) {
			a.lose(codeExt, "condition", "secretMaterial", value.Source, detail)
			a.needsManual()
		}
		return &ir.Cond{Phase: ir.PhaseRuntime, Expr: &converted}
	}
	retained := a.scrubRetained("condition", value.Source, value.Value).(string)
	if len(retained) > maxScanBytes {
		c.work += maxScanBytes
		retained = redact.Replacement()
		a.lose(codeExt, "condition", "secretMaterial", value.Source, "condition exceeded the safe scan bound and was replaced by the redaction placeholder")
		a.needsManual()
	} else if clean, hit, work := redactOpaqueComparisons(retained); hit {
		c.work += work
		retained = clean
		a.lose(codeExt, "condition", "secretMaterial", value.Source, detail)
		a.needsManual()
	} else {
		c.work += work
	}
	a.keep("condition", retained)
	a.lose(codeExt, "condition", "expression", value.Source, "condition is outside the portable expression AST and is retained as opaque Azure text")
	raw := &ir.Raw{Provider: Platform, Kind: "condition", Format: "text", Text: retained, Origin: c.origin(value.Source, value.Chain)}
	return &ir.Cond{Phase: ir.PhaseRuntime, Opaque: raw}
}

func (c *conv) expression(node adoexpr.Expr) (ir.Expr, bool) {
	switch value := node.(type) {
	case *adoexpr.Literal:
		literal := ir.Literal{}
		switch typed := value.Value.(type) {
		case adoexpr.Null:
			literal.Type = ir.LiteralNull
		case adoexpr.Bool:
			literal.Type, literal.Bool = ir.LiteralBool, bool(typed)
		case adoexpr.Number:
			literal.Type, literal.Number = ir.LiteralNumber, typed.ExactString()
		case adoexpr.String:
			literal.Type, literal.String = ir.LiteralString, string(typed)
		case adoexpr.Version:
			literal.Type, literal.String = ir.LiteralString, typed.String()
		default:
			return ir.Expr{}, false
		}
		return ir.Expr{Kind: ir.ExprLiteral, Literal: &literal}, true
	case *adoexpr.Identifier:
		return c.reference(value.Name, nil)
	case *adoexpr.Member:
		root, parts, ok := exprPath(value)
		if !ok {
			return ir.Expr{}, false
		}
		return c.reference(root, parts)
	case *adoexpr.Index:
		root, parts, ok := exprPath(value)
		if !ok {
			return ir.Expr{}, false
		}
		return c.reference(root, parts)
	case *adoexpr.Call:
		fn, ok := portableFunction(value.Name)
		if !ok {
			return ir.Expr{}, false
		}
		call := ir.CallExpr{Function: fn}
		for _, arg := range value.Args {
			converted, ok := c.expression(arg)
			if !ok {
				return ir.Expr{}, false
			}
			call.Args = append(call.Args, converted)
		}
		return ir.Expr{Kind: ir.ExprCall, Call: &call}, true
	default:
		return ir.Expr{}, false
	}
}

// secretComparand reports a reference whose last path element is
// secret-classified.
func secretComparand(e ir.Expr) bool {
	return e.Kind == ir.ExprRef && e.Ref != nil && len(e.Ref.Path) > 0 && secretName(e.Ref.Path[len(e.Ref.Path)-1])
}

// redactSecretComparisons replaces, in place, every non-reference string
// literal that is an argument of a call with a secret-classified reference
// argument (eq(variables.DB_PASSWORD, 'x')). It reports whether it changed
// anything. Iterative: the expression is walked with an explicit stack.
func redactSecretComparisons(root *ir.Expr) bool {
	changed := false
	stack := []*ir.Expr{root}
	for len(stack) > 0 {
		e := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if e.Kind != ir.ExprCall || e.Call == nil {
			continue
		}
		secret := false
		for _, arg := range e.Call.Args {
			secret = secret || secretComparand(arg)
		}
		for i := range e.Call.Args {
			arg := &e.Call.Args[i]
			if secret && arg.Kind == ir.ExprLiteral && arg.Literal != nil && arg.Literal.Type == ir.LiteralString && literalSecret("secret", arg.Literal.String) {
				arg.Literal.String = redact.Replacement()
				changed = true
			}
			stack = append(stack, arg)
		}
	}
	return changed
}

var (
	opaqueNamedSecretRef = regexp.MustCompile(`(?i)\b(?:variables|parameters|env)\s*(?:\.\s*([A-Za-z_][\w.\-]*)|\[\s*'([^']+)'\s*\])`)
	opaqueMacroSecretRef = regexp.MustCompile(`\$\(([^)]+)\)`)
	quotedLiteral        = regexp.MustCompile(`'(?:[^']|'')*'`)
)

// redactOpaqueComparisons is the conservative scan for conditions outside the
// portable AST: when the text names a secret-classified variable, every quoted
// literal that is not a reference is replaced by the placeholder.
func redactOpaqueComparisons(text string) (string, bool, int) {
	secret := false
	for _, m := range opaqueNamedSecretRef.FindAllStringSubmatch(text, -1) {
		for _, name := range m[1:] {
			secret = secret || (name != "" && secretName(name))
		}
	}
	for _, m := range opaqueMacroSecretRef.FindAllStringSubmatch(text, -1) {
		secret = secret || secretName(m[1])
	}
	work := 2 * len(text)
	if !secret {
		return text, false, work
	}
	changed := false
	out := quotedLiteral.ReplaceAllStringFunc(text, func(lit string) string {
		if literalSecret("secret", lit[1:len(lit)-1]) {
			changed = true
			return "'" + redact.Replacement() + "'"
		}
		return lit
	})
	return out, changed, work + len(text)
}

func exprPath(node adoexpr.Expr) (string, []string, bool) {
	switch value := node.(type) {
	case *adoexpr.Identifier:
		return value.Name, nil, true
	case *adoexpr.Member:
		if value.Wildcard {
			return "", nil, false
		}
		root, parts, ok := exprPath(value.Receiver)
		return root, append(parts, value.Name), ok
	case *adoexpr.Index:
		root, parts, ok := exprPath(value.Receiver)
		if !ok {
			return "", nil, false
		}
		literal, ok := value.Key.(*adoexpr.Literal)
		if !ok {
			return "", nil, false
		}
		key, ok := literal.Value.(adoexpr.String)
		if !ok {
			return "", nil, false
		}
		return root, append(parts, string(key)), true
	default:
		return "", nil, false
	}
}

func (c *conv) reference(root string, parts []string) (ir.Expr, bool) {
	var context ir.RefContext
	switch strings.ToLower(root) {
	case "variables":
		context = ir.RefVariables
	case "parameters":
		context = ir.RefParameters
	case "env":
		context = ir.RefEnv
	case "matrix":
		context = ir.RefMatrix
	case "agent":
		context, parts = ir.RefRunner, append([]string{root}, parts...)
	case "pipeline":
		context, parts = ir.RefRun, append([]string{root}, parts...)
	default:
		return ir.Expr{}, false
	}
	if len(parts) == 0 {
		return ir.Expr{}, false
	}
	return ir.Expr{Kind: ir.ExprRef, Ref: &ir.RefExpr{Context: context, Path: parts}}, true
}

func portableFunction(name string) (ir.Function, bool) {
	functions := []struct {
		name string
		fn   ir.Function
	}{
		{"and", ir.FunctionAnd},
		{"or", ir.FunctionOr},
		{"not", ir.FunctionNot},
		{"eq", ir.FunctionEqual},
		{"ne", ir.FunctionNotEqual},
		{"lt", ir.FunctionLess},
		{"le", ir.FunctionLessOrEqual},
		{"gt", ir.FunctionGreater},
		{"ge", ir.FunctionGreaterOrEqual},
		{"contains", ir.FunctionContains},
		{"startswith", ir.FunctionStartsWith},
		{"endswith", ir.FunctionEndsWith},
		{"format", ir.FunctionFormat},
		{"join", ir.FunctionJoin},
		{"succeeded", ir.FunctionSuccess},
		{"failed", ir.FunctionFailure},
		{"always", ir.FunctionAlways},
		{"canceled", ir.FunctionCancelled},
		{"cancelled", ir.FunctionCancelled},
		{"succeededwithissues", ir.FunctionSucceededWithIssues},
	}
	for _, candidate := range functions {
		if strings.EqualFold(name, candidate.name) {
			return candidate.fn, true
		}
	}
	return "", false
}

func rawJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}
