package emit

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/google/go-cmp/cmp"
)

// walkerPipeline is a representative IR graph with one node of every kind the
// walker must enumerate, plus out-of-scope features and extension keys.
func walkerPipeline() *ir.Pipeline {
	return &ir.Pipeline{
		ID:          "P/1",
		Name:        "pipeline",
		Concurrency: &ir.Concurrency{},
		Metadata: ir.Metadata{Extensions: ir.Extensions{
			"x-ado/z": json.RawMessage(`3`),
			"x-ado/a": json.RawMessage(`1`),
		}},
		Inputs:   []ir.Input{{ID: "in/1"}},
		Triggers: []ir.Trigger{{ID: "tr1", Kind: ir.TriggerPush}},
		Variables: []ir.Variable{
			{Name: "FOO"}, {Name: "FOO"},
		},
		Stages: []ir.Stage{{
			ID:        "st/1",
			If:        &ir.Cond{},
			Variables: []ir.Variable{{Name: "S"}},
			Jobs:      []string{"j/1"},
		}},
		Jobs: []ir.Job{{
			ID:          "j/1",
			Container:   &ir.Container{},
			Services:    []ir.Service{{}},
			Matrix:      &ir.Matrix{},
			Uses:        &ir.ReusableCall{},
			Outputs:     []ir.OutputDef{{}},
			Permissions: ir.OrderedMap[string]{{Key: "contents", Value: "read"}},
			Variables:   []ir.Variable{{Name: "J"}},
			Steps: []ir.Step{
				{ID: "s/1", Kind: ir.StepRun, Run: &ir.RunStep{Operations: []ir.ProtocolOperation{{Kind: ir.StepSetEnv}}}},
				{ID: "s/2", Kind: ir.StepCheckout, Checkout: &ir.CheckoutStep{Repository: "self", Path: "sub"}},
			},
		}},
	}
}

// TestWalkPipelineEnumeratesEveryNodeAndFeature checks emit-gha.md 2.3: one
// requirement per node, one per present out-of-scope feature under
// `<nodeID>#<feature>`, and one carried requirement per extension key.
func TestWalkPipelineEnumeratesEveryNodeAndFeature(t *testing.T) {
	requirements := walkPipeline(walkerPipeline())
	ids := make([]string, len(requirements))
	for index, requirement := range requirements {
		ids[index] = requirement.ID
	}
	if !sort.StringsAreSorted(ids) {
		t.Errorf("requirement IDs are not sorted: %v", ids)
	}
	want := []string{
		"input:in%2F1",
		"job:j%2F1",
		"job:j%2F1#container",
		"job:j%2F1#matrix",
		"job:j%2F1#outputs",
		"job:j%2F1#permissions",
		"job:j%2F1#services",
		"job:j%2F1#uses",
		"pipeline:P%2F1",
		"pipeline:P%2F1#carried/x-ado/a",
		"pipeline:P%2F1#carried/x-ado/z",
		"pipeline:P%2F1#concurrency",
		"stage:st%2F1",
		"stage:st%2F1#if",
		"step:j%2F1/s%2F1",
		"step:j%2F1/s%2F1#run-operations",
		"step:j%2F1/s%2F2",
		"step:j%2F1/s%2F2#checkout-path",
		"trigger:tr1",
		"variable:job:j%2F1/0/J",
		"variable:pipeline:P%2F1/0/FOO",
		"variable:pipeline:P%2F1/1/FOO",
		"variable:stage:st%2F1/0/S",
	}
	sort.Strings(want)
	if diff := cmp.Diff(want, ids); diff != "" {
		t.Errorf("requirements (-want +got):\n%s", diff)
	}
	for _, requirement := range requirements {
		if requirement.Feature != "" && requirement.Feature != "carried/x-ado/a" &&
			requirement.Feature != "carried/x-ado/z" && requirement.Carried {
			t.Errorf("%s: non-extension feature marked carried", requirement.ID)
		}
		if requirement.Feature == "carried/x-ado/a" && !requirement.Carried {
			t.Errorf("%s: extension feature not carried", requirement.ID)
		}
	}
}

// TestWalkPipelineIsIndependentOfExtensionMapOrder checks that the extension
// keys are sorted, never read in Go map order.
func TestWalkPipelineIsIndependentOfExtensionMapOrder(t *testing.T) {
	first := walkerPipeline()
	second := walkerPipeline()
	second.Extensions = ir.Extensions{
		"x-ado/a": json.RawMessage(`1`),
		"x-ado/z": json.RawMessage(`3`),
	}
	firstRequirements := walkPipeline(first)
	firstIDs := make([]string, 0, len(firstRequirements))
	for _, requirement := range firstRequirements {
		firstIDs = append(firstIDs, requirement.ID)
	}
	for run := 0; run < 20; run++ {
		secondRequirements := walkPipeline(second)
		secondIDs := make([]string, 0, len(secondRequirements))
		for _, requirement := range secondRequirements {
			secondIDs = append(secondIDs, requirement.ID)
		}
		if diff := cmp.Diff(firstIDs, secondIDs); diff != "" {
			t.Fatalf("run %d differs (-first +second):\n%s", run, diff)
		}
	}
}

// TestWalkPipelineKeepsOriginsAndKinds checks the diagnostic attribution data
// the walker hands to the API.
func TestWalkPipelineKeepsOriginsAndKinds(t *testing.T) {
	pipeline := walkerPipeline()
	pipeline.Triggers[0].Origin = &ir.Origin{Source: diag.Source{Line: 7}}
	requirements := walkPipeline(pipeline)
	found := false
	for _, requirement := range requirements {
		if requirement.ID != "trigger:tr1" {
			continue
		}
		found = true
		if requirement.Source == nil || requirement.Source.Line != 7 {
			t.Errorf("trigger source = %+v, want line 7", requirement.Source)
		}
		if requirement.TriggerKind != ir.TriggerPush {
			t.Errorf("trigger kind = %q", requirement.TriggerKind)
		}
	}
	if !found {
		t.Error("trigger requirement missing")
	}
}

// TestQualifyIDEscapesComponents checks R9 escaping.
func TestQualifyIDEscapesComponents(t *testing.T) {
	for _, testCase := range []struct{ got, want string }{
		{qualifyID("job", "Build"), "job:Build"},
		{qualifyID("step", "j/1", "s/1"), "step:j%2F1/s%2F1"},
		{qualifyID("variable", "pipeline:ci", "0", "FOO"), "variable:pipeline:ci/0/FOO"},
		{qualifyID("job", "100%"), "job:100%25"},
		{qualifyID("job", "a%2Fb"), "job:a%252Fb"},
	} {
		if testCase.got != testCase.want {
			t.Errorf("qualifyID = %q, want %q", testCase.got, testCase.want)
		}
	}
}

// TestWalkPipelineNil checks the API total: a nil pipeline has no nodes.
func TestWalkPipelineNil(t *testing.T) {
	if requirements := walkPipeline(nil); len(requirements) != 0 {
		t.Errorf("nil pipeline produced %v", requirements)
	}
}
