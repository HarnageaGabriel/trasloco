package normalize

import (
	"fmt"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

// overflowHeader declares a secret above the taint bound, so every component
// collapses to the placeholder.
func overflowHeader() string {
	return "parameters:\n- name: dbPassword\n  type: string\n  default: " + strings.Repeat("S", maxTaintSourceBytes+1) + "\n"
}

func namedStepsYAML(n int) string {
	var out strings.Builder
	out.WriteString(overflowHeader())
	out.WriteString("steps:\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&out, "- script: echo hi\n  name: step%d\n", i)
	}
	return out.String()
}

func namedStagesYAML(n int) string {
	var out strings.Builder
	out.WriteString(overflowHeader())
	out.WriteString("stages:\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&out, "- stage: st%d\n  dependsOn: []\n  jobs:\n", i)
		fmt.Fprintf(&out, "  - job: j%d\n    steps:\n    - script: echo hi\n", i)
	}
	return out.String()
}

func namedJobsYAML(n int) string {
	var out strings.Builder
	out.WriteString(overflowHeader())
	out.WriteString("stages:\n- stage: s\n  jobs:\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&out, "  - job: j%d\n    steps:\n    - script: echo hi\n", i)
	}
	return out.String()
}

// The scrubbed-component spelling allocator is linear in the number of
// distinct components, also when all of them collapse to one spelling.
func TestComponentSpellingWorkScalesLinearly(t *testing.T) {
	shapes := map[string]func(int) string{
		"named steps":           namedStepsYAML,
		"named jobs":            func(n int) string { return namedJobsYAML(3 * n) },
		"named stages and jobs": func(n int) string { return namedStagesYAML(3 * n) },
	}
	for name, build := range shapes { //trasloco:unordered-ok independent subtests
		t.Run(name, func(t *testing.T) {
			_, small := pipelineWorkOf(t, build(200))
			_, large := pipelineWorkOf(t, build(400))
			if small == 0 || float64(large) > 2.5*float64(small) {
				t.Fatalf("work doubled by more than 2.5x: %d -> %d", small, large)
			}
		})
	}
}

// A renamed job composite is listed by its stage, so the stage carries the
// loss as well, once.
func TestRenamedJobCompositeMarksStage(t *testing.T) {
	const ref = "${{ parameters.dbPassword }}"
	text := "parameters:\n- name: dbPassword\n  type: string\n  default: \"7421\"\n" +
		"stages:\n- stage: s\n  jobs:\n  - job: " + ref + "\n    steps:\n    - script: echo hi\n" +
		"  - job: a\n    dependsOn: " + ref + "\n    steps:\n    - script: echo hi\n"
	got, _ := resolvedPipeline(t, text)
	stage := got.Stages[len(got.Stages)-1]
	count := 0
	for _, property := range lossProperties(t, stage.Metadata) {
		if property == "secretMaterial" {
			count++
		}
	}
	if count != 1 || stage.Fidelity != diag.FidelityNeedsManual {
		t.Fatalf("stage secretMaterial losses = %d (want 1), fidelity %v", count, stage.Fidelity)
	}
}
