package normalize

import (
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

func TestFreeFormSecretCanaries(t *testing.T) {
	tests := []struct {
		name, yaml, canary string
		node               func(*ir.Pipeline) ir.Metadata
	}{
		{
			name:   "display name pair",
			yaml:   "steps:\n- script: echo ok\n  name: s\n  displayName: DB_PASSWORD=display-canary\n",
			canary: "display-canary",
			node:   func(p *ir.Pipeline) ir.Metadata { return stepByID(t, p, implicitID, "s").Metadata },
		},
		{
			name:   "task whitespace flag",
			yaml:   "steps:\n- task: AzureCLI@2\n  name: s\n  inputs:\n    arguments: --client-secret argument-canary\n",
			canary: "argument-canary",
			node:   func(p *ir.Pipeline) ir.Metadata { return stepByID(t, p, implicitID, "s").Metadata },
		},
		{
			name:   "pool demand",
			yaml:   "pool:\n  demands:\n  - DB_PASSWORD -equals demand-canary\nsteps:\n- script: ok\n",
			canary: "demand-canary",
			node:   func(p *ir.Pipeline) ir.Metadata { return jobByID(t, p, implicitID).Metadata },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := leaks(t, test.yaml, test.canary)
			if metadata := test.node(got); metadata.Fidelity != diag.FidelityNeedsManual {
				t.Fatalf("fidelity = %v, want needs-manual", metadata.Fidelity)
			}
		})
	}
}

func TestConditionSecretComparisonCanaries(t *testing.T) {
	tests := []struct {
		name, condition, canary string
		opaque                  bool
	}{
		{"portable", "eq(variables.DB_PASSWORD, 'portable-condition-canary')", "portable-condition-canary", false},
		{"opaque", "custom(variables['api-token'], 'opaque-condition-canary')", "opaque-condition-canary", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := leaks(t, "steps:\n- script: ok\n  name: s\n  condition: "+test.condition+"\n", test.canary)
			step := stepByID(t, got, implicitID, "s")
			if step.Fidelity != diag.FidelityNeedsManual {
				t.Fatalf("fidelity = %v, want needs-manual", step.Fidelity)
			}
			if test.opaque {
				if step.If.Opaque == nil || !strings.Contains(step.If.Opaque.Text, redact.Replacement()) {
					t.Fatalf("opaque condition = %#v, want redaction", step.If.Opaque)
				}
			} else if step.If.Expr == nil || step.If.Expr.Call == nil || step.If.Expr.Call.Args[1].Literal == nil || step.If.Expr.Call.Args[1].Literal.String != redact.Replacement() {
				t.Fatalf("portable condition = %#v, want redacted literal", step.If)
			}
		})
	}
}

func TestRedactSecretMacroComparison(t *testing.T) {
	expr := ir.Expr{Kind: ir.ExprCall, Call: &ir.CallExpr{
		Function: ir.FunctionEqual,
		Args: []ir.Expr{
			{Kind: ir.ExprMacro, Macro: &ir.MacroExpr{Name: "TOKEN", Declared: true, Secret: true}},
			ir.StringLiteral("macro-comparison-canary"),
		},
	}}

	if !redactSecretComparisons(&expr) {
		t.Fatal("secret macro comparison was not redacted")
	}
	if got := expr.Call.Args[1].Literal.String; got != redact.Replacement() {
		t.Fatalf("comparison literal = %q, want redaction placeholder", got)
	}
}
