package vars

import (
	"context"
	"fmt"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
)

// compileEval is the single point where a compile-time expression tree is
// evaluated. Graph.EvalCompile and every CompileEvaluator go through it, so a
// test can replace it to prove its callers depend on the production evaluator.
var compileEval = evalCompileNode

func evalCompileNode(node expr.Expr, values expr.Context) (expr.Value, error) {
	return expr.Eval(context.Background(), node, values, expr.EvalOptions{Phase: expr.PhaseCompile})
}

// CompileEvaluator evaluates ${{ }} template expressions against the
// compile-visible variables declared so far (YAML declarations and the
// predefined variables marked compile-visible) and the template parameters.
// Declarations are lexically scoped: Push starts a stage/job scope that sees
// its parents' declarations and Pop restores the snapshot. A template resolver
// uses it so every variables.* lookup, condition and substitution is decided
// by the same code as Graph.EvalCompile.
type CompileEvaluator struct {
	vars   *expr.Object
	params *expr.Object
	stack  []*expr.Object
}

// NewCompileEvaluator seeds the evaluator with the compile-visible predefined
// variables. Everything else a run supplies is runtime-only and is not visible.
func NewCompileEvaluator(predefined []PlainVariable) *CompileEvaluator {
	c := &CompileEvaluator{vars: &expr.Object{}, params: &expr.Object{}}
	for _, v := range predefined {
		if v.CompileVisible {
			setProperty(c.vars, v.Name, expr.String(v.Value))
		}
	}
	return c
}

// Child returns an evaluator for an included template: it shares this
// evaluator's variable declarations but has its own, empty parameter set.
func (c *CompileEvaluator) Child() *CompileEvaluator {
	return &CompileEvaluator{vars: c.vars, params: &expr.Object{}}
}

// SetParameter declares or replaces one template parameter.
func (c *CompileEvaluator) SetParameter(name string, value expr.Value) {
	setProperty(c.params, name, value)
}

// Declare records a compile-visible variable declaration.
func (c *CompileEvaluator) Declare(name string, value expr.Value) {
	setProperty(c.vars, name, value)
}

// Push starts a lexical scope that sees every declaration made so far.
func (c *CompileEvaluator) Push() {
	c.stack = append(c.stack, c.vars)
	c.vars = &expr.Object{Properties: append([]expr.Property(nil), c.vars.Properties...)}
}

// Pop ends the scope started by the matching Push.
func (c *CompileEvaluator) Pop() {
	if len(c.stack) == 0 {
		return
	}
	c.vars = c.stack[len(c.stack)-1]
	c.stack = c.stack[:len(c.stack)-1]
}

// Lookup implements expr.Context for the variables and parameters roots.
func (c *CompileEvaluator) Lookup(name string) (expr.Value, bool) {
	switch fold(name) {
	case "VARIABLES":
		return c.vars, true
	case "PARAMETERS":
		if c.params != nil {
			return c.params, true
		}
	}
	return expr.Null{}, false
}

// Eval evaluates one template expression, with or without its ${{ }} wrapper.
func (c *CompileEvaluator) Eval(text string) (expr.Value, error) {
	node, err := expr.Parse(unwrapExpression(text, "${{", "}}"))
	if err != nil {
		return nil, err
	}
	return compileEval(node, c)
}

// Condition evaluates an if/elseif expression to its truth value.
func (c *CompileEvaluator) Condition(text string) (bool, error) {
	inner := unwrapExpression(text, "${{", "}}")
	value, err := c.Eval("not(not(" + inner + "))")
	if err != nil {
		return false, err
	}
	flag, ok := value.(expr.Bool)
	if !ok {
		return false, fmt.Errorf("condition evaluated to %T, not a boolean", value)
	}
	return bool(flag), nil
}

// Substitute replaces every ${{ }} occurrence in text with its rendered value.
// An occurrence that cannot be evaluated or rendered stays as written.
func (c *CompileEvaluator) Substitute(text string) string {
	var b strings.Builder
	for {
		start := strings.Index(text, "${{")
		if start < 0 {
			break
		}
		end := closingBraces(text[start+3:])
		if end >= 0 {
			end += 3
		}
		if end < 0 {
			break
		}
		end += start + 2
		literal := text[start:end]
		b.WriteString(text[:start])
		b.WriteString(c.render(literal))
		text = text[end:]
	}
	b.WriteString(text)
	return b.String()
}

// closingBraces returns the offset of the first "}}" outside a single-quoted
// string literal (a doubled quote is an escaped quote), or -1.
func closingBraces(text string) int {
	inString := false
	for i := 0; i < len(text); i++ {
		switch {
		case text[i] == '\'':
			inString = !inString
		case !inString && strings.HasPrefix(text[i:], "}}"):
			return i
		}
	}
	return -1
}

func (c *CompileEvaluator) render(literal string) string {
	value, err := c.Eval(literal)
	if err != nil {
		return literal
	}
	result, err := render(value)
	if err != nil {
		return literal
	}
	return result.Text
}
