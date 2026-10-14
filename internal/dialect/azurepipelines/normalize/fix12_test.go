package normalize

import (
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

// stepNamed returns the step with the given ID in any job.
func stepNamed(t *testing.T, p *ir.Pipeline, id string) ir.Step {
	t.Helper()
	for i := range p.Jobs {
		for j := range p.Jobs[i].Steps {
			if p.Jobs[i].Steps[j].ID == id {
				return p.Jobs[i].Steps[j]
			}
		}
	}
	t.Fatalf("step %q not found", id)
	return ir.Step{}
}

// A declared secret collapses two IDs onto one scrubbed spelling, so the later
// declaration is renamed (issue #12). A reference inside free text cannot be
// re-pointed to the declaration it named: every such string carries a
// needs-manual loss, and the original spelling never survives.
func TestRenamedIDFreeTextReferenceNeedsManual(t *testing.T) {
	const text = `parameters:
- name: dbPassword
  type: string
  default: MixedS3cretPw
stages:
- stage: MixedS3cretPw
  jobs:
  - job: first
    steps:
    - script: echo first
- stage: MIXEDS3CRETPW
  jobs:
  - job: second
    steps:
    - script: echo "$[ dependencies.MIXEDS3CRETPW.outputs['a.b'] ]"
      name: script-ref
    - script: echo ok
      name: opaque-ref
      condition: and(succeeded(), eq(dependencies.MIXEDS3CRETPW.result, 'Succeeded'))
    - script: echo safe
      name: clean
`
	got, collector := parsedPipeline(t, text)
	if len(got.Stages) != 2 || got.Stages[0].ID != "[REDACTED]" || got.Stages[1].ID != "[REDACTED]-2" {
		t.Fatalf("stage IDs = %q, %q, want the later collision renamed", got.Stages[0].ID, got.Stages[1].ID)
	}
	for _, id := range []string{"script-ref", "opaque-ref"} {
		step := stepNamed(t, got, id)
		if !hasLoss(t, step.Metadata, propertyRenamedReference) {
			t.Fatalf("step %s losses = %v, want %s", id, lossProperties(t, step.Metadata), propertyRenamedReference)
		}
		if step.Fidelity != diag.FidelityNeedsManual {
			t.Fatalf("step %s fidelity = %s, want needs-manual", id, step.Fidelity)
		}
	}
	if clean := stepNamed(t, got, "clean"); hasLoss(t, clean.Metadata, propertyRenamedReference) {
		t.Fatalf("clean step has %s: %v", propertyRenamedReference, lossProperties(t, clean.Metadata))
	}
	out := requireAbsent(t, got, collector, "MIXEDS3CRETPW", "MixedS3cretPw")
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("no redaction placeholder in IR:\n%s", out)
	}
	if findings := ir.Validate(got); len(findings) != 0 {
		t.Fatalf("IR validation: %v", findings)
	}
}
