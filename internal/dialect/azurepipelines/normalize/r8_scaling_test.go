package normalize

import (
	"fmt"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

func TestOpaqueConditionScanBounded(t *testing.T) {
	text := strings.Repeat("$(env.a", (1<<20)/7+1)
	if _, _, work := redactOpaqueComparisons(text); work > 2*len(text) {
		t.Fatalf("opaque scan work = %d for %d bytes", work, len(text))
	}

	got, work := pipelineWorkOf(t, "steps:\n- script: echo ok\n  name: condition-step\n  condition: "+text+"\n")
	if work > 8*len(text) {
		t.Fatalf("pipeline work = %d for %d condition bytes", work, len(text))
	}
	step := stepByID(t, got, implicitID, "condition-step")
	if step.If == nil || step.If.Opaque == nil || step.If.Opaque.Text != redact.Replacement() {
		t.Fatalf("oversize condition was not replaced: %#v", step.If)
	}
	if step.Fidelity != diag.FidelityNeedsManual {
		t.Fatalf("condition step fidelity = %s, want needs-manual", step.Fidelity)
	}
}

func TestDedupeStepIDsWorkLinear(t *testing.T) {
	const count = 8_000
	steps := make([]ir.Step, count)
	for i := range steps {
		steps[i].ID = "a"
	}
	work := 0
	dedupeStepIDs(steps, &work)
	if work > 4*count {
		t.Fatalf("dedupe work = %d for %d duplicate IDs", work, count)
	}
	if steps[0].ID != "a" || steps[1].ID != "a-2" || steps[count-1].ID != fmt.Sprintf("a-%d", count) {
		t.Fatalf("deduped IDs = %q, %q, ..., %q", steps[0].ID, steps[1].ID, steps[count-1].ID)
	}

	collisions := []ir.Step{{ID: "a"}, {ID: "a-2"}, {ID: "a"}, {ID: "a-2"}}
	dedupeStepIDs(collisions)
	if got := []string{collisions[0].ID, collisions[1].ID, collisions[2].ID, collisions[3].ID}; fmt.Sprint(got) != "[a a-2 a-3 a-2-2]" {
		t.Fatalf("collision handling = %v", got)
	}
}

func TestPipelineScalingWork(t *testing.T) {
	const size = 4 << 10
	shapes := []struct {
		name  string
		build func(int) string
	}{
		{"many-steps", manyStepsYAML(false)},
		{"duplicate-step-names", manyStepsYAML(true)},
		{"env-keys", mappingShape("steps:\n- script: echo ok\n  name: s\n  env:\n", "    ENV_%06d: value\n")},
		{"task-inputs", mappingShape("steps:\n- task: Example@1\n  name: s\n  inputs:\n", "    input_%06d: value\n")},
		{"matrix-legs", mappingShape("jobs:\n- job: matrix\n  strategy:\n    matrix:\n", "      leg_%06d:\n        value: x\n")},
		{"unknown-keys", mappingShape("steps:\n- script: echo ok\n  name: s\n", "  unknown_%06d: value\n")},
		{"opaque-condition", func(n int) string {
			return "steps:\n- script: echo ok\n  name: s\n  condition: azureOnly('" + strings.Repeat("x", n) + "')\n"
		}},
		{"portable-condition", func(n int) string {
			return "steps:\n- script: echo ok\n  name: s\n  condition: eq(variables.Build, '" + strings.Repeat("x", n) + "')\n"
		}},
		{"resource-lookups", resourceLookupShape},
		{"many-stages", stagesShape},
		{"many-jobs", mappingShape("jobs:\n", "- job: job_%06d\n  steps:\n  - script: echo ok\n")},
		{"logissue-line", func(n int) string {
			return "steps:\n- script: |\n    " + strings.Repeat("x##vso[task.logissue]a", n/22) + "\n  name: s\n"
		}},
		{"secret-keyed-value", func(n int) string {
			return "steps:\n- script: echo ok\n  name: s\n  env:\n    DB_PASSWORD: " + strings.Repeat("x", n) + "\n"
		}},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			_, small := pipelineWorkOf(t, shape.build(size))
			_, large := pipelineWorkOf(t, shape.build(4*size))
			if small == 0 {
				t.Fatal("zero work counter")
			}
			if large > 6*small {
				t.Fatalf("work ratio exceeds 6: size %d work, 4x size %d work", small, large)
			}
			t.Logf("work ratio %.2f (%d/%d)", float64(large)/float64(small), large, small)
		})
	}
}

func manyStepsYAML(duplicate bool) func(int) string {
	return func(target int) string {
		var out strings.Builder
		out.WriteString("steps:\n")
		for i := 0; out.Len() < target; i++ {
			name := fmt.Sprintf("step_%06d", i)
			if duplicate {
				name = "a"
			}
			fmt.Fprintf(&out, "- script: echo ok\n  name: %s\n", name)
		}
		return out.String()
	}
}

func mappingShape(prefix, entry string) func(int) string {
	return func(target int) string {
		var out strings.Builder
		out.WriteString(prefix)
		for i := 0; out.Len() < target; i++ {
			fmt.Fprintf(&out, entry, i)
		}
		return out.String()
	}
}

// stagesShape chains stages by dependsOn so dependency validation is exercised.
func stagesShape(target int) string {
	var out strings.Builder
	out.WriteString("stages:\n")
	for i := 0; out.Len() < target; i++ {
		fmt.Fprintf(&out, "- stage: s%06d\n", i)
		if i > 0 {
			fmt.Fprintf(&out, "  dependsOn: s%06d\n", i-1)
		}
		out.WriteString("  jobs:\n  - job: j\n    steps:\n    - script: echo ok\n")
	}
	return out.String()
}

func resourceLookupShape(target int) string {
	// Equal resource and job counts make the old per-job linear alias search
	// quadratic while keeping the source size proportional to the count.
	count := target / 180
	if count < 1 {
		count = 1
	}
	var out strings.Builder
	out.WriteString("resources:\n  containers:\n")
	for i := 0; i < count; i++ {
		fmt.Fprintf(&out, "  - container: c%d\n    image: example/image:%d\n", i, i)
	}
	out.WriteString("  repositories:\n")
	for i := 0; i < count; i++ {
		fmt.Fprintf(&out, "  - repository: r%d\n    type: git\n    name: project/repo%d\n    ref: refs/heads/main\n", i, i)
	}
	out.WriteString("jobs:\n")
	for i := 0; i < count; i++ {
		fmt.Fprintf(&out, "- job: j%d\n  container: c%d\n  steps:\n  - checkout: r%d\n", i, count-1, count-1)
	}
	return out.String()
}
