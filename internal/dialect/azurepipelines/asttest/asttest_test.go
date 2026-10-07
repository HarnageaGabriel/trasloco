package asttest

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
)

func sample() *ast.Pipeline {
	parsed, err := expr.Parse("eq(a, 'b')")
	if err != nil {
		panic(err)
	}
	scalar := func(value string) *ast.Scalar { return &ast.Scalar{Value: value} }
	return &ast.Pipeline{
		Shape: ast.ShapeSteps,
		Name:  scalar("n"),
		Steps: &ast.List[ast.Step]{Items: []ast.ListItem[ast.Step]{
			{Value: &ast.ScriptStep{
				Script:    scalar("echo"),
				StepProps: ast.StepProps{Condition: &ast.Expression{Scalar: *scalar("eq(a, 'b')"), Expr: parsed}},
			}},
			{Insert: &ast.Directive{Kind: ast.DirectiveInsert, Raw: "${{ x }}"}},
		}},
		Tail: ast.Tail[ast.Pipeline]{Extra: []*ast.Extra{{
			Key:   scalar("k"),
			Value: &ast.Any{Kind: ast.AnyScalar, Scalar: scalar("")},
		}}},
	}
}

func TestDumpAndDiff(t *testing.T) {
	want := []string{
		"shape = steps",
		"name = n",
		"steps[0].type = ScriptStep",
		"steps[0].kind = script",
		"steps[0].script = echo",
		"steps[0].condition = eq(a, 'b')",
		"steps[0].condition.expr = eq(a, 'b')",
		"steps[1] = insert: ${{ x }}",
		"extra[0].key = k",
		"extra[0].value = <empty>",
	}
	if diff := cmp.Diff(want, Dump(sample(), false)); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	if got := Dump(sample(), true); len(got) <= len(want) {
		t.Errorf("positions were not dumped: %d lines", len(got))
	}
	changed := sample()
	changed.Name.Value = "other"
	if Diff(sample(), sample()) != "" || Diff(sample(), changed) == "" {
		t.Error("Diff does not distinguish trees correctly")
	}
}

func TestLoadVectors(t *testing.T) {
	vectors := LoadVectors(t)
	if len(vectors) == 0 {
		t.Fatal("no vectors")
	}
	first := vectors[0]
	if first.ID != "STRUCT-001" || !strings.Contains(first.Input, "\n") || len(first.Expected) == 0 || first.Source == "" {
		t.Errorf("first vector = %+v", first)
	}
	if got := tableCells("| a | `b\\|c` | d |"); len(got) != 5 || got[2] != "b|c" {
		t.Errorf("tableCells = %q", got)
	}
}
