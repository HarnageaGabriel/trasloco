package ast

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
)

func TestEnumNames(t *testing.T) {
	tests := []struct {
		got  string
		want string
	}{
		{DirectiveIf.String(), "if"},
		{DirectiveElseIf.String(), "elseif"},
		{DirectiveElse.String(), "else"},
		{DirectiveEach.String(), "each"},
		{DirectiveInsert.String(), "insert"},
		{DirectiveRuntime.String(), "runtime"},
		{DirectiveKind(0).String(), "unknown"},
		{ShapeNone.String(), "none"},
		{ShapeStages.String(), "stages"},
		{ShapeJobs.String(), "jobs"},
		{ShapeSteps.String(), "steps"},
		{ShapeExtends.String(), "extends"},
		{TriggerFull.String(), "full"},
		{TriggerNone.String(), "none"},
		{TriggerList.String(), "list"},
		{TriggerInsertion.String(), "insertion"},
		{ResourceTriggerFull.String(), "full"},
		{ResourceTriggerNone.String(), "none"},
		{ResourceTriggerTrue.String(), "true"},
		{ResourceTriggerInsertion.String(), "insertion"},
		{ParametersList.String(), "list"},
		{ParametersMap.String(), "map"},
		{ParametersInsertion.String(), "insertion"},
		{VariablesMap.String(), "map"},
		{VariablesList.String(), "list"},
		{VariablesInsertion.String(), "insertion"},
		{AnyScalar.String(), "scalar"},
		{AnySeq.String(), "seq"},
		{AnyMap.String(), "map"},
		{ScriptCmd.String(), "script"},
		{ScriptBash.String(), "bash"},
		{ScriptPwsh.String(), "pwsh"},
		{ScriptPowerShell.String(), "powershell"},
	}
	for _, test := range tests {
		if test.got != test.want {
			t.Errorf("got %q, want %q", test.got, test.want)
		}
	}
	if !DirectiveIf.IsKey() || !DirectiveEach.IsKey() || DirectiveInsert.IsKey() || DirectiveRuntime.IsKey() {
		t.Error("IsKey classification is wrong")
	}
}

func TestScalarWhole(t *testing.T) {
	whole := &Directive{Kind: DirectiveInsert, Raw: "${{ a }}"}
	partial := &Directive{Kind: DirectiveInsert, Raw: "${{ b }}"}
	tests := []struct {
		name   string
		scalar *Scalar
		want   *Directive
	}{
		{"nil", nil, nil},
		{"none", &Scalar{Value: "plain"}, nil},
		{"whole", &Scalar{Value: "  ${{ a }}  ", Directives: []*Directive{whole}}, whole},
		{"embedded", &Scalar{Value: "x ${{ b }}", Directives: []*Directive{partial}}, nil},
	}
	for _, test := range tests {
		if got := test.scalar.Whole(); got != test.want {
			t.Errorf("%s: Whole() = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestSourceOfAndPositions(t *testing.T) {
	span := expr.Span{Start: 1, End: 3}
	source := diag.Source{Line: 4, Col: 9}
	directive := &Directive{ExprSources: []ExprSource{{Span: span, Source: source}}}
	expression := &Expression{ExprSources: []ExprSource{{Span: span, Source: source}}}
	for name, got := range map[string]func(expr.Span) (diag.Source, bool){
		"directive": directive.SourceOf, "expression": expression.SourceOf,
	} {
		found, ok := got(span)
		if !ok || found != source {
			t.Errorf("%s: SourceOf = %v, %v", name, found, ok)
		}
		if _, ok := got(expr.Span{Start: 9, End: 9}); ok {
			t.Errorf("%s: unknown span was found", name)
		}
	}
	var pos Pos
	pos.SetPos(Pos{Source: source})
	if diff := cmp.Diff(source, pos.Position()); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	var insertable Insertable
	insertable.SetInsertion(directive)
	if insertable.Insertion != directive {
		t.Error("SetInsertion did not store the directive")
	}
}
