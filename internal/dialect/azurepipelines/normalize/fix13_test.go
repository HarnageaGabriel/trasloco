package normalize

import (
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

// A declared secret whose value is short, small-numeric or boolean-like is not
// replaced inside longer text (issue #13). Every string that references such a
// variable by name (`$(name)`, `variables.name`, `variables['name']`) carries a
// needs-manual loss; the reference itself is left intact (no broadened
// replacement).
func TestShortSecretByNameReferenceNeedsManual(t *testing.T) {
	const text = `parameters:
- name: dbPassword
  type: string
  default: Zq7
- name: apiSecret
  type: string
  default: 'true'
- name: secretPin
  type: string
  default: '4242'
steps:
- script: echo $(dbPassword)
  name: script-macro
- task: Foo@1
  name: task-input
  inputs:
    arguments: --pw $(secretPin)
- script: echo ok
  name: opaque-cond
  condition: and(succeeded(), eq(dependencies.X.outputs['a.b'], variables.dbPassword))
- script: echo ok
  name: typed-cond
  condition: eq(variables.apiSecret, 'on')
- script: echo safe
  name: clean
`
	got, collector := parsedPipeline(t, text)
	for _, id := range []string{"script-macro", "task-input", "opaque-cond", "typed-cond"} {
		step := stepNamed(t, got, id)
		if !hasLoss(t, step.Metadata, propertyShortSecretRef) {
			t.Fatalf("step %s losses = %v, want %s", id, lossProperties(t, step.Metadata), propertyShortSecretRef)
		}
		if step.Fidelity != diag.FidelityNeedsManual {
			t.Fatalf("step %s fidelity = %s, want needs-manual", id, step.Fidelity)
		}
	}
	if clean := stepNamed(t, got, "clean"); hasLoss(t, clean.Metadata, propertyShortSecretRef) {
		t.Fatalf("clean step has %s: %v", propertyShortSecretRef, lossProperties(t, clean.Metadata))
	}
	// The reference is not replaced: the tool must not broaden replacement.
	if script := stepNamed(t, got, "script-macro").Run.Script; !strings.Contains(script, "$(dbPassword)") {
		t.Fatalf("macro reference changed: %q", script)
	}
	requireAbsent(t, got, collector, "Zq7", "4242")
	if findings := ir.Validate(got); len(findings) != 0 {
		t.Fatalf("IR validation: %v", findings)
	}
}
