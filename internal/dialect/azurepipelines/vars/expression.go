package vars

import (
	"context"
	"fmt"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
)

// Result is the typed outcome of evaluating one expression at a consumer.
type Result struct {
	Value expr.Value
	// Text is the scalar rendering; empty for Null.
	Text string
	// Null is true when the expression evaluated successfully to null (a
	// dictionary miss such as an invisible output). It is never set for errors.
	Null bool
	// OrderDependent names runtime-expression variables whose value would depend
	// on an unspecified evaluation order; the result is then Null and
	// Diagnostics holds TRL-VAR-0004 (never a guess).
	OrderDependent []string
	Diagnostics    []diag.Diagnostic
}

// EvalRuntime evaluates a runtime expression ($[ ... ] or its inner text) at a
// consumer scope. Variables are the scope's runtime bindings (secrets and
// unknown values are not exposed and evaluate to null); dependencies and
// stageDependencies expose only the outputs visible along the dependsOn edges
// of the graph. Parse and evaluation failures are returned as errors.
func (g *Graph) EvalRuntime(scope Scope, expression string) (Result, error) {
	table, ok := g.Table(scope)
	if !ok {
		return Result{}, fmt.Errorf("evaluate runtime expression: no table for scope %+v", scope)
	}
	return g.newRuntime(table, scope).eval(unwrapExpression(expression, "$[", "]"))
}

// EvalCompile evaluates a compile-time template expression (${{ ... }} or its
// inner text) at a consumer scope. Only compile-visible bindings exist: YAML
// declarations and the predefined variables marked compile-visible. Anything
// else, including runtime-only values, evaluates to null, which a template
// renders as an empty string.
func (g *Graph) EvalCompile(scope Scope, expression string) (Result, error) {
	table, ok := g.Table(scope)
	if !ok {
		return Result{}, fmt.Errorf("evaluate compile expression: no table for scope %+v", scope)
	}
	evaluator := &CompileEvaluator{vars: &expr.Object{}}
	for i := range table.Bindings {
		if candidate, found := effective(&table.Bindings[i], PhaseCompile); found && !candidate.Secret && !candidate.Unknown {
			evaluator.Declare(candidate.Name, expr.String(candidate.Value))
		}
	}
	value, err := evaluator.Eval(expression)
	if err != nil {
		return Result{}, err
	}
	return render(value)
}

// EvalCondition evaluates a job or stage condition at a consumer scope. The
// dependency snapshot reports every visible dependency as Succeeded.
func (g *Graph) EvalCondition(scope Scope, condition string) (bool, error) {
	table, ok := g.Table(scope)
	if !ok {
		return false, fmt.Errorf("evaluate condition: no table for scope %+v", scope)
	}
	rt := g.newRuntime(table, scope)
	node, err := expr.Parse(condition)
	if err != nil {
		return false, err
	}
	options := expr.EvalOptions{Phase: expr.PhaseRuntime, Status: expr.StatusSnapshot{Scope: "job", Dependencies: rt.deps}}
	value, err := expr.Eval(context.Background(), node, rt, options)
	if err != nil {
		return false, err
	}
	flag, isBool := value.(expr.Bool)
	if !isBool {
		return false, fmt.Errorf("condition evaluated to %T, not a boolean", value)
	}
	return bool(flag), nil
}

func unwrapExpression(text, open, closing string) string {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, open) && strings.HasSuffix(trimmed, closing) {
		return strings.TrimSpace(trimmed[len(open) : len(trimmed)-len(closing)])
	}
	return trimmed
}

// runtimeEval is the expression context of one consumer.
type runtimeEval struct {
	graph *Graph
	table *Table
	scope Scope
	// vars holds the plain (non-expression) variables.
	vars *expr.Object
	// expressions maps a folded name to the inner text of a variable whose
	// value is a whole $[ ] expression.
	expressions map[string]string //trasloco:unordered-ok lookups only
	// definitions keeps the candidate behind each expression variable.
	definitions map[string]Candidate //trasloco:unordered-ok lookups only
	cache       map[string]Result    //trasloco:unordered-ok lookups only
	active      map[string]bool      //trasloco:unordered-ok lookups only
	deps        *expr.Object
	stageDeps   *expr.Object
}

func (g *Graph) newRuntime(table *Table, scope Scope) *runtimeEval {
	rt := &runtimeEval{graph: g, table: table, scope: scope, vars: &expr.Object{}, deps: &expr.Object{}, stageDeps: &expr.Object{}, expressions: map[string]string{}, definitions: map[string]Candidate{}, cache: map[string]Result{}, active: map[string]bool{}} //trasloco:unordered-ok lookups only
	for i := range table.Bindings {
		candidate, found := effective(&table.Bindings[i], PhaseRuntime)
		if !found || candidate.Secret || candidate.Unknown {
			continue
		}
		if isRuntimeExpression(candidate.Value) {
			rt.expressions[fold(candidate.Name)] = unwrapExpression(candidate.Value, "$[", "]")
			rt.definitions[fold(candidate.Name)] = candidate
			continue
		}
		setProperty(rt.vars, candidate.Name, expr.String(candidate.Value))
	}
	rt.buildDependencies()
	return rt
}

func (rt *runtimeEval) buildDependencies() {
	g, scope := rt.graph, rt.scope
	if scope.Level == "pipeline" {
		return
	}
	stage := g.node("stage", scope.Stage, "")
	if scope.Level == "stage" {
		if stage != nil {
			for _, name := range stage.DependsOn {
				succeeded(rt.deps, name)
			}
		}
		for _, o := range g.Outputs {
			if value, ok := outputValue(o); ok && g.visibleOutput(scope, o) {
				setProperty(ensureObject(ensureObject(rt.deps, o.Stage), "outputs"), o.Job+"."+o.Key(), value)
			}
		}
		return
	}
	if job := g.node("job", scope.Stage, scope.Job); job != nil {
		for _, name := range job.DependsOn {
			succeeded(rt.deps, name)
		}
	}
	if stage != nil {
		for _, name := range stage.DependsOn {
			ensureObject(rt.stageDeps, name)
		}
	}
	for _, o := range g.Outputs {
		value, ok := outputValue(o)
		if !ok || !g.visibleOutput(scope, o) {
			continue
		}
		if sameName(o.Stage, scope.Stage) {
			setProperty(ensureObject(ensureObject(rt.deps, o.Job), "outputs"), o.Key(), value)
		} else {
			setProperty(ensureObject(ensureObject(ensureObject(rt.stageDeps, o.Stage), o.Job), "outputs"), o.Key(), value)
		}
	}
}

// outputValue exposes only non-secret, known output values to expressions.
func outputValue(o Output) (expr.Value, bool) {
	if o.Candidate.Secret || o.Candidate.Unknown {
		return nil, false
	}
	return expr.String(o.Candidate.Value), true
}

func succeeded(parent *expr.Object, name string) {
	dependency := ensureObject(parent, name)
	ensureObject(dependency, "outputs")
	setProperty(dependency, "result", expr.String("Succeeded"))
}

// Lookup implements expr.Context for the three named roots.
func (rt *runtimeEval) Lookup(name string) (expr.Value, bool) {
	switch fold(name) {
	case "VARIABLES":
		return rt.vars, true
	case "DEPENDENCIES":
		return rt.deps, true
	case "STAGEDEPENDENCIES":
		return rt.stageDeps, true
	}
	return expr.Null{}, false
}

// evalVariable evaluates the runtime expression a variable is defined as. A
// variable may read another expression variable only when the platform
// guarantees the order: both are YAML declarations and the read one comes
// first (expr#variables). Any other read is order dependent and unprovable.
func (rt *runtimeEval) evalVariable(name string) (Result, error) {
	folded := fold(name)
	text, ok := rt.expressions[folded]
	if !ok {
		return Result{}, fmt.Errorf("variable %s is not a runtime expression", name)
	}
	if cached, done := rt.cache[folded]; done {
		return cached, nil
	}
	if rt.active[folded] {
		return orderDependentResult([]string{name}), nil
	}
	rt.active[folded] = true
	defer delete(rt.active, folded)
	result, err := rt.evalPhase(text, expr.PhaseRuntime, name)
	if err == nil {
		rt.cache[folded] = result
	}
	return result, err
}

func (rt *runtimeEval) eval(text string) (Result, error) {
	return rt.evalPhase(text, expr.PhaseRuntime, "")
}

// scopedContext exposes the runtime roots with a per-evaluation variables object.
type scopedContext struct {
	rt   *runtimeEval
	vars *expr.Object
}

func (c scopedContext) Lookup(name string) (expr.Value, bool) {
	if fold(name) == "VARIABLES" {
		return c.vars, true
	}
	return c.rt.Lookup(name)
}

func orderDependentResult(names []string) Result {
	result := Result{Value: expr.Null{}, Null: true, OrderDependent: names}
	for _, name := range names {
		result.Diagnostics = append(result.Diagnostics, diag.MustNew("TRL-VAR-0004", diag.WithArgs(map[string]any{"name": displayName(name)})))
	}
	return result
}

// evalPhase evaluates one parsed expression. self names the variable whose
// definition is being evaluated (empty for a plain consumer expression).
func (rt *runtimeEval) evalPhase(text string, phase expr.Phase, self string) (Result, error) {
	node, err := expr.Parse(text)
	if err != nil {
		return Result{}, err
	}
	vars := rt.vars
	if phase == expr.PhaseRuntime {
		var dependent []string
		vars = &expr.Object{Properties: append([]expr.Property(nil), rt.vars.Properties...)}
		for _, ref := range rt.expressionReads(node) {
			if self != "" && !rt.declaredBefore(ref, self) {
				dependent = append(dependent, ref)
				continue
			}
			value, evalErr := rt.evalVariable(ref)
			switch {
			case evalErr != nil:
				// an unavailable read evaluates to null, as a missing key does
			case len(value.OrderDependent) > 0:
				dependent = append(dependent, ref)
			case !value.Null:
				setProperty(vars, ref, value.Value)
			}
		}
		if len(dependent) > 0 {
			return orderDependentResult(dependent), nil
		}
	}
	value, err := expr.Eval(context.Background(), node, scopedContext{rt: rt, vars: vars}, expr.EvalOptions{Phase: phase, VariableDefinition: self != ""})
	if err != nil {
		return Result{}, err
	}
	return render(value)
}

// declaredBefore reports whether the platform evaluates ref before self.
func (rt *runtimeEval) declaredBefore(ref, self string) bool {
	read, readOK := rt.definitions[fold(ref)]
	own, ownOK := rt.definitions[fold(self)]
	if !readOK || !ownOK || read.Source != SourceYAML || own.Source != SourceYAML {
		return false
	}
	if read.Scope != own.Scope {
		return scopeDepth(read.Scope) < scopeDepth(own.Scope)
	}
	return read.DeclarationIndex < own.DeclarationIndex
}

func scopeDepth(scope Scope) int {
	switch scope.Level {
	case "pipeline":
		return 0
	case "stage":
		return 1
	}
	return 2
}

// expressionReads lists the expression variables the expression reads.
func (rt *runtimeEval) expressionReads(node expr.Expr) []string {
	var found []string
	expr.Walk(expr.VisitorFunc(func(n expr.Expr) {
		var name string
		switch v := n.(type) {
		case *expr.Member:
			if isVariablesRoot(v.Receiver) {
				name = v.Name
			}
		case *expr.Index:
			if literal, ok := v.Key.(*expr.Literal); ok && isVariablesRoot(v.Receiver) {
				if s, isString := literal.Value.(expr.String); isString {
					name = string(s)
				}
			}
		}
		if _, isExpression := rt.expressions[fold(name)]; name != "" && isExpression && !containsFold(found, name) {
			found = append(found, name)
		}
	}), node)
	return found
}

func isVariablesRoot(n expr.Expr) bool {
	id, ok := n.(*expr.Identifier)
	return ok && fold(id.Name) == "VARIABLES"
}

func render(value expr.Value) (Result, error) {
	result := Result{Value: value}
	switch v := value.(type) {
	case expr.Null:
		result.Null = true
	case expr.String:
		result.Text = string(v)
	case expr.Bool:
		if v {
			result.Text = "True"
		} else {
			result.Text = "False"
		}
	case expr.Number:
		result.Text = v.String()
	default:
		return result, fmt.Errorf("expression evaluated to %T, not a scalar", value)
	}
	return result, nil
}

func ensureObject(parent *expr.Object, name string) *expr.Object {
	for _, property := range parent.Properties {
		if sameName(property.Name, name) {
			if object, ok := property.Value.(*expr.Object); ok {
				return object
			}
		}
	}
	child := &expr.Object{}
	parent.Properties = append(parent.Properties, expr.Property{Name: name, Value: child})
	return child
}

func setProperty(parent *expr.Object, name string, value expr.Value) {
	for i := range parent.Properties {
		if sameName(parent.Properties[i].Name, name) {
			parent.Properties[i].Value = value
			return
		}
	}
	parent.Properties = append(parent.Properties, expr.Property{Name: name, Value: value})
}
