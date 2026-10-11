package vars

import (
	"strings"
	"unicode/utf8"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
)

// maskText is what the log shows in place of a secret value.
const maskText = "***"

const maxExpandDepth = 50

// workBudget bounds the total bytes produced while expanding one text, across
// every nested variable, so a fan-out chain cannot multiply work.
const workBudget = 8 * expr.MaxEvaluationMemory

// Expansion is the log-visible result of macro expansion at one consumer.
// Secret references render as *** and set Masked; no secret material exists
// here. SecretRefs lists the references the text was derived from.
type Expansion struct {
	Text        string
	Masked      bool
	SecretRefs  []SecretRef
	Diagnostics []diag.Diagnostic
}

// accumulator carries the secret references and diagnostics of one expansion.
type accumulator struct {
	refs       []SecretRef
	diags      []diag.Diagnostic
	overflowed bool
}

// addOverflow records the output-bound overflow once per expansion.
func (a *accumulator) addOverflow() {
	if a.overflowed {
		return
	}
	a.overflowed = true
	a.diags = append(a.diags, diag.MustNew("TRL-VAR-0017", diag.WithArgs(map[string]any{"limit": expr.MaxEvaluationMemory})))
}

// overflowed reports a TRL-VAR-0017 among the diagnostics: the text it
// belongs to was cut at the bound and must not be used as a valid value.
func overflowed(diagnostics []diag.Diagnostic) bool {
	for _, d := range diagnostics {
		if d.Code == "TRL-VAR-0017" {
			return true
		}
	}
	return false
}

func (a *accumulator) addRef(ref *SecretRef) {
	if ref == nil {
		ref = &SecretRef{Provider: "unknown"}
	}
	for _, existing := range a.refs {
		if existing == *ref {
			return
		}
	}
	a.refs = append(a.refs, *ref)
}

func (a *accumulator) merge(other *accumulator) {
	for i := range other.refs {
		a.addRef(&other.refs[i])
	}
	for _, d := range other.diags {
		if d.Code == "TRL-VAR-0017" {
			a.addOverflow()
			continue
		}
		a.diags = append(a.diags, d)
	}
}

type memoEntry struct {
	text string
	acc  accumulator
}

// macroExpander expands $(name) macros over one table. Each variable's
// expansion is memoized so repeated references cost one expansion, and the
// output is bounded by expr.MaxEvaluationMemory per value.
type macroExpander struct {
	table *Table
	index map[string]int //trasloco:unordered-ok lookups only, never iterated
	memo  map[string]memoEntry
	work  int
	// totalWork is monotonic across top-level calls and proves memo reuse in
	// tests; work itself is reset because its budget applies to one text.
	totalWork int
	cycles    int
	// graph and scope enable runtime-expression variable values; both are zero
	// when binding values at mutation time.
	graph   *Graph
	scope   Scope
	runtime *runtimeEval
}

func newMacroExpander(table *Table) *macroExpander {
	e := &macroExpander{table: table, index: make(map[string]int, len(table.Bindings)), memo: map[string]memoEntry{}} //trasloco:unordered-ok lookups only
	for i, binding := range table.Bindings {
		e.index[fold(binding.Name)] = i
	}
	return e
}

// useTable moves a job-scoped expander to the next step snapshot. The binding
// set is unchanged between steps without a logging mutation, so its index and
// memo remain valid and large nested values are not rebuilt per step.
func (e *macroExpander) useTable(table *Table) { e.table = table }

func (g *Graph) newExpander(table *Table, scope Scope) *macroExpander {
	e := newMacroExpander(table)
	e.graph, e.scope = g, scope
	return e
}

// Expand substitutes $(name) macros recursively using the consumer's table.
// Unknown names, unavailable names, unknown values and cycles stay literal; a
// variable whose value is a whole runtime expression expands to its evaluated
// value. Expansion output is bounded and memoized.
func (g *Graph) Expand(scope Scope, text string) Expansion {
	table, ok := g.Table(scope)
	if !ok {
		return Expansion{Text: text}
	}
	return g.newExpander(table, scope).expandText(text)
}

func (e *macroExpander) expandText(text string) Expansion {
	e.work = 0
	var acc accumulator
	out := e.expand(text, nil, &acc)
	return Expansion{Text: out, Masked: len(acc.refs) > 0, SecretRefs: acc.refs, Diagnostics: acc.diags}
}

// boundedBuilder caps every write at expr.MaxEvaluationMemory. A write that
// does not fit is cut at a rune boundary, so the result never exceeds the bound
// and the overflow is reported deterministically instead of growing silently.
type boundedBuilder struct {
	b        strings.Builder
	overflow bool
}

func (w *boundedBuilder) fits(n int) bool { return w.b.Len()+n <= expr.MaxEvaluationMemory }

func (w *boundedBuilder) write(s string) {
	room := expr.MaxEvaluationMemory - w.b.Len()
	if len(s) > room {
		cut := room
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s, w.overflow = s[:cut], true
	}
	w.b.WriteString(s)
}

func (e *macroExpander) expand(text string, stack []string, acc *accumulator) string {
	macros, _ := expr.ScanMacros(text)
	if len(macros) == 0 && len(text) <= expr.MaxEvaluationMemory {
		return text
	}
	var w boundedBuilder
	last := 0
	for _, m := range macros {
		w.write(text[last:m.Span.Start])
		last = m.Span.End
		literal := text[m.Span.Start:m.Span.End]
		replacement, ok := e.value(m.Name, stack, acc)
		if ok && !w.fits(len(replacement)) {
			w.overflow = true
		}
		if !ok || !w.fits(len(replacement)) {
			replacement = literal
		}
		w.write(replacement)
	}
	w.write(text[last:])
	if w.overflow {
		acc.addOverflow()
	}
	return w.b.String()
}

// value resolves one macro name. The bool is false when the reference must stay
// literal (unknown, unavailable, unknown value, cycle, depth or work overflow).
func (e *macroExpander) value(name string, stack []string, acc *accumulator) (string, bool) {
	i, found := e.index[fold(name)]
	if !found {
		return "", false
	}
	candidate, ok := effective(&e.table.Bindings[i], PhaseMacro)
	if !ok || candidate.Unknown {
		return "", false
	}
	if candidate.Secret {
		acc.addRef(candidate.SecretRef)
		return maskText, true
	}
	if containsFold(stack, name) || len(stack) >= maxExpandDepth {
		e.cycles++
		return "", false
	}
	if entry, memoized := e.memo[fold(name)]; memoized {
		acc.merge(&entry.acc)
		return entry.text, true
	}
	if e.work > workBudget {
		acc.addOverflow()
		e.cycles++
		return "", false
	}
	before := e.cycles
	var local accumulator
	var text string
	if e.graph != nil && isRuntimeExpression(candidate.Value) {
		var evaluated bool
		text, evaluated = e.runtimeValue(candidate.Name, &local)
		if !evaluated {
			acc.merge(&local)
			return "", false
		}
	} else {
		text = e.expand(candidate.Value, append(append([]string(nil), stack...), name), &local)
	}
	e.work += len(text)
	e.totalWork += len(text)
	if e.work > workBudget {
		local.addOverflow()
		e.cycles++
		acc.merge(&local)
		return "", false
	}
	if e.cycles == before {
		e.memo[fold(name)] = memoEntry{text: text, acc: local}
	}
	acc.merge(&local)
	return text, true
}

func (e *macroExpander) runtimeValue(name string, acc *accumulator) (string, bool) {
	if e.runtime == nil {
		e.runtime = e.graph.newRuntime(e.table, e.scope)
	}
	result, err := e.runtime.evalVariable(name)
	acc.diags = append(acc.diags, result.Diagnostics...)
	if len(result.OrderDependent) > 0 {
		return "", false
	}
	if err != nil {
		return "", true
	}
	return result.Text, true
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if sameName(value, want) {
			return true
		}
	}
	return false
}

func isRuntimeExpression(value string) bool {
	trimmed := strings.TrimSpace(value)
	return strings.HasPrefix(trimmed, "$[") && strings.HasSuffix(trimmed, "]")
}
