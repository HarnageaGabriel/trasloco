package vars

import (
	"strconv"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
)

// macroGraph is a one-table graph whose variables are the given name/value pairs.
func macroGraph(pairs ...string) (*Graph, Scope) {
	scope := Scope{Level: "step", Step: 0}
	table := Table{Scope: scope}
	for i := 0; i < len(pairs); i += 2 {
		add(&table, Candidate{Name: pairs[i], ComparisonKey: key(pairs[i]), Value: pairs[i+1], Available: true})
	}
	return &Graph{Tables: []Table{table}}, scope
}

func hasOverflow(e Expansion) bool { return hasCode(e.Diagnostics, "TRL-VAR-0017") }

func TestReviewMacroFanoutIsMemoizedAndDeterministic(t *testing.T) {
	var pairs []string
	for i := 0; i < 39; i++ {
		next := "v" + strconv.Itoa(i+1)
		pairs = append(pairs, "v"+strconv.Itoa(i), "$("+next+")$("+next+")")
	}
	graph, scope := macroGraph(append(pairs, "v39", "x")...)
	first, second := graph.Expand(scope, "$(v0)"), graph.Expand(scope, "$(v0)")
	if first.Text != second.Text {
		t.Fatal("fan-out expansion is not deterministic")
	}
}

func TestReviewMacroFanoutStaysWithinTheBoundAndSignals(t *testing.T) {
	var pairs []string
	for i := 0; i < 39; i++ {
		next := "v" + strconv.Itoa(i+1)
		pairs = append(pairs, "v"+strconv.Itoa(i), "$("+next+")$("+next+")")
	}
	graph, scope := macroGraph(append(pairs, "v39", "x")...)
	expanded := graph.Expand(scope, "$(v0)")
	if len(expanded.Text) > expr.MaxEvaluationMemory {
		t.Fatalf("fan-out output is %d bytes, over the %d bound", len(expanded.Text), expr.MaxEvaluationMemory)
	}
	if !hasOverflow(expanded) {
		t.Fatalf("overflow was not signalled: %v", codes(expanded.Diagnostics))
	}
}

func TestReviewMacroExactBoundIsNotAnOverflow(t *testing.T) {
	graph, scope := macroGraph("x", strings.Repeat("a", expr.MaxEvaluationMemory))
	expanded := graph.Expand(scope, "$(x)")
	if len(expanded.Text) != expr.MaxEvaluationMemory || hasOverflow(expanded) {
		t.Fatalf("exact bound: len=%d overflow=%t", len(expanded.Text), hasOverflow(expanded))
	}
}

func TestReviewMacroSuffixCannotExceedTheBound(t *testing.T) {
	graph, scope := macroGraph("x", strings.Repeat("a", expr.MaxEvaluationMemory))
	expanded := graph.Expand(scope, "$(x)tail")
	if len(expanded.Text) != expr.MaxEvaluationMemory {
		t.Fatalf("suffix grew the output to %d bytes", len(expanded.Text))
	}
	if !hasOverflow(expanded) {
		t.Fatalf("suffix overflow was not signalled: %v", codes(expanded.Diagnostics))
	}
}

func TestReviewMacroLiteralFallbacksCannotExceedTheBound(t *testing.T) {
	graph, scope := macroGraph("x", strings.Repeat("a", expr.MaxEvaluationMemory/2+1))
	expanded := graph.Expand(scope, strings.Repeat("$(x)", 64))
	if len(expanded.Text) > expr.MaxEvaluationMemory {
		t.Fatalf("repeated macros grew the output to %d bytes", len(expanded.Text))
	}
	if !strings.HasSuffix(expanded.Text, "$(x)") {
		t.Fatal("a macro that does not fit must stay literal")
	}
	if !hasOverflow(expanded) {
		t.Fatalf("overflow was not signalled: %v", codes(expanded.Diagnostics))
	}
}

func TestReviewMacroOverflowIsReportedOnce(t *testing.T) {
	graph, scope := macroGraph("x", strings.Repeat("a", expr.MaxEvaluationMemory/2+1))
	count := 0
	for _, d := range graph.Expand(scope, strings.Repeat("$(x)", 64)).Diagnostics {
		if d.Code == "TRL-VAR-0017" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("overflow diagnostics = %d, want 1", count)
	}
}

func TestReviewMacroLiteralTextOverTheBoundIsCut(t *testing.T) {
	graph, scope := macroGraph("x", "v")
	expanded := graph.Expand(scope, strings.Repeat("b", expr.MaxEvaluationMemory+10))
	if len(expanded.Text) != expr.MaxEvaluationMemory || !hasOverflow(expanded) {
		t.Fatalf("literal span: len=%d overflow=%t", len(expanded.Text), hasOverflow(expanded))
	}
}
