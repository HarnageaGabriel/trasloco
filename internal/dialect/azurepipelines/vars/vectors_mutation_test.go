package vars

import (
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
)

// compileVectors are the vectors whose outcome is decided by compile-time
// variable lookups, conditions, substitutions and lexical snapshots.
var compileVectors = []string{"VARV-026", "VARV-030", "VARV-031", "VARV-032", "VARV-034", "VARV-035", "VARV-036", "VARV-037", "VARV-038", "VARV-039", "VARV-040", "VARV-041", "VARV-042", "VARV-043", "VARV-044", "VARV-045", "VARV-046", "VARV-047", "VARV-048", "VARV-049"}

// breakCompileEval replaces the production compile evaluator with one that
// inverts booleans and turns every other value into a sentinel, until the
// test ends.
func breakCompileEval(t *testing.T) {
	t.Helper()
	original := compileEval
	compileEval = func(node expr.Expr, values expr.Context) (expr.Value, error) {
		value, err := original(node, values)
		if err != nil {
			return value, err
		}
		if flag, ok := value.(expr.Bool); ok {
			return !flag, nil
		}
		return expr.String("BROKEN"), nil
	}
	t.Cleanup(func() { compileEval = original })
}

// TestVARVCompileVectorsDependOnTheProductionEvaluator is the mutation check:
// the test expander is structural only, so breaking the production compile
// evaluator must make every compile-time vector fail.
func TestVARVCompileVectorsDependOnTheProductionEvaluator(t *testing.T) {
	byID := make(map[string]vectorCase) //trasloco:unordered-ok lookups only
	for _, tc := range loadVectorCases(t) {
		byID[tc.ID] = tc
	}
	for _, id := range compileVectors {
		tc, ok := byID[id]
		if !ok {
			t.Fatalf("%s is not a structured case", id)
		}
		t.Run(id+" passes with the production evaluator", func(t *testing.T) {
			if _, _, failures := vectorOutcome(t, tc); len(failures) != 0 {
				t.Fatalf("healthy evaluator mismatches: %v", failures)
			}
		})
		t.Run(id+" fails with a broken evaluator", func(t *testing.T) {
			breakCompileEval(t)
			if _, _, failures := vectorOutcome(t, tc); len(failures) == 0 {
				t.Fatalf("%s still passes with a broken compile evaluator: the vector does not depend on production code", id)
			}
		})
	}
}

func TestCompileEvaluatorLexicalSnapshots(t *testing.T) {
	evaluator := NewCompileEvaluator([]PlainVariable{{Name: "Build.SourceBranch", Value: "refs/heads/main", CompileVisible: true}, {Name: "Build.BuildId", Value: "7"}})
	evaluator.Declare("a", expr.String("outer"))
	evaluator.Push()
	evaluator.Declare("a", expr.String("inner"))
	if got := evaluator.Substitute("${{ variables.a }}"); got != "inner" {
		t.Fatalf("inner scope = %q", got)
	}
	evaluator.Pop()
	if got := evaluator.Substitute("${{ variables.a }}"); got != "outer" {
		t.Fatalf("restored scope = %q", got)
	}
	if got := evaluator.Substitute("[${{ variables['Build.BuildId'] }}]"); got != "[]" {
		t.Fatalf("runtime-only predefined variable leaked into templates: %q", got)
	}
	if ok, err := evaluator.Condition("eq(variables['Build.SourceBranch'], 'refs/heads/main')"); err != nil || !ok {
		t.Fatalf("condition = %t, %v", ok, err)
	}
}
