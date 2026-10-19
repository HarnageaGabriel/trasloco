package emit

import (
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/HarnageaGabriel/trasloco/internal/naming"
	"github.com/google/go-cmp/cmp"
)

// stubNaming is a caller-supplied naming service: the emitter consumes the
// interface, never the package that implements it.
type stubNaming struct{}

func (stubNaming) WorkflowFile(name string) (string, error) { return name + ".yml", nil }

func (stubNaming) JobID(_, jobID string) (string, error) { return jobID, nil }

func (stubNaming) StepID(_, stepID string) (string, error) { return stepID, nil }

func (stubNaming) SecretName(secret ir.SecretRef) (string, error) { return secret.Name, nil }

// The two assertions are the consumer-interface test: a caller stub and the real
// naming policy both satisfy emit.Naming, and the interface stays where its
// consumer is (CONVENTIONS.md).
var (
	_ Naming = stubNaming{}
	_ Naming = (*naming.Namer)(nil)
)

// samplePipeline is a pipeline with one node of every kind the conservation
// contract names, plus an origin on one of them.
func samplePipeline() *ir.Pipeline {
	return &ir.Pipeline{
		ID:            "ci",
		Name:          "Build and Test",
		Inputs:        []ir.Input{{ID: "in1", Name: "target", Type: ir.InputString}},
		Triggers:      []ir.Trigger{{ID: "tr1", Kind: ir.TriggerPush}},
		Variables:     []ir.Variable{{Name: "FOO", Scope: ir.ScopePipeline}},
		Stages:        []ir.Stage{{ID: "st1", Name: "Stage one", Jobs: []string{"Job A"}}},
		SchemaVersion: ir.SchemaVersion,
		Jobs: []ir.Job{{
			ID:        "Job A",
			Name:      "Job A",
			Variables: []ir.Variable{{Name: "BAR", Scope: ir.ScopeJob, ScopeID: "Job A"}},
			Steps:     []ir.Step{{ID: "step-1", Name: "step-1", Kind: ir.StepRun}},
			Metadata: ir.Metadata{Origin: &ir.Origin{
				Source: diag.Source{Platform: "ado", File: "azure-pipelines.yml", Line: 12},
			}},
		}},
	}
}

// TestEmitReportsOneDispositionPerNode checks the T7a contract: nothing is
// emitted, the result is blocked, and every IR node of emit-gha.md 2.3 carries
// exactly one needs-manual placeholder disposition, sorted by ID.
func TestEmitReportsOneDispositionPerNode(t *testing.T) {
	result := Emit(samplePipeline(), catalog.ActionsLock{}, stubNaming{}, Options{}, nil)
	if !result.Blocked {
		t.Error("T7a emission must be blocked: no mapping rule exists yet")
	}
	if len(result.Files) != 0 || len(result.OutputElements) != 0 || len(result.RequiredSecrets) != 0 {
		t.Errorf("T7a emitted %d files, %d elements and %d secrets; it must emit none",
			len(result.Files), len(result.OutputElements), len(result.RequiredSecrets))
	}
	want := []string{
		"input:in1",
		"job:Job A",
		"pipeline:ci",
		"stage:st1",
		"step:Job A/step-1",
		"trigger:tr1",
		"variable:job:Job A/0/BAR",
		"variable:pipeline:ci/0/FOO",
	}
	got := make([]string, 0, len(result.Dispositions))
	for _, disposition := range result.Dispositions {
		got = append(got, disposition.ID)
		if disposition.Kind != DispositionPlaceholder {
			t.Errorf("%s: kind = %q, want %q", disposition.ID, disposition.Kind, DispositionPlaceholder)
		}
		if disposition.Fidelity != diag.FidelityNeedsManual {
			t.Errorf("%s: fidelity = %q, want %q", disposition.ID, disposition.Fidelity, diag.FidelityNeedsManual)
		}
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("disposition IDs (-want +got):\n%s", diff)
	}
	if len(result.Diagnostics) != len(result.Dispositions) {
		t.Errorf("%d findings for %d nodes; every node states why it is not converted",
			len(result.Diagnostics), len(result.Dispositions))
	}
}

// TestEmitAttributesFindingsToTheNodeOrigin checks ADR 0003: a finding about an
// IR node uses that node's origin, and one for a node without origin carries no
// source.
func TestEmitAttributesFindingsToTheNodeOrigin(t *testing.T) {
	result := Emit(samplePipeline(), catalog.ActionsLock{}, stubNaming{}, Options{}, nil)
	for _, finding := range result.Diagnostics {
		if finding.Code != manualConversionCode {
			t.Errorf("finding code = %s, want %s", finding.Code, manualConversionCode)
		}
		if finding.Resource == nil {
			t.Fatalf("%s: finding without a resource", finding.Code)
		}
		if finding.Resource.Key == "job:Job A" {
			if finding.Source == nil || finding.Source.File != "azure-pipelines.yml" || finding.Source.Line != 12 {
				t.Errorf("job finding source = %+v, want the job origin", finding.Source)
			}
			continue
		}
		if finding.Source != nil {
			t.Errorf("%s: source = %+v, want none", finding.Resource.Key, finding.Source)
		}
	}
}

// TestEmitAddsFindingsToTheCallerCollector checks that the emitter reports
// through the collector it was given, once per node.
func TestEmitAddsFindingsToTheCallerCollector(t *testing.T) {
	collector := diag.NewCollector()
	result := Emit(samplePipeline(), catalog.ActionsLock{}, stubNaming{}, Options{}, collector)
	list := collector.List()
	if len(list) != len(result.Dispositions) {
		t.Fatalf("collector holds %d findings, want %d", len(list), len(result.Dispositions))
	}
	held := make(map[string]int, len(list))
	for _, finding := range list {
		held[finding.Resource.Key]++
	}
	for _, disposition := range result.Dispositions {
		if held[disposition.ID] != 1 {
			t.Errorf("%s: collector holds %d findings, want 1", disposition.ID, held[disposition.ID])
		}
	}
}

// TestEmitIsDeterministic checks INV-01 at the API boundary.
func TestEmitIsDeterministic(t *testing.T) {
	first := Emit(samplePipeline(), catalog.ActionsLock{}, stubNaming{}, Options{}, nil)
	second := Emit(samplePipeline(), catalog.ActionsLock{}, stubNaming{}, Options{}, nil)
	if diff := cmp.Diff(first, second); diff != "" {
		t.Errorf("two emissions of the same pipeline differ (-first +second):\n%s", diff)
	}
}

// TestEmitRejectsMissingPipeline keeps the API total: a nil pipeline is blocked
// and empty instead of panicking.
func TestEmitRejectsMissingPipeline(t *testing.T) {
	result := Emit(nil, catalog.ActionsLock{}, stubNaming{}, Options{}, nil)
	if !result.Blocked {
		t.Error("a nil pipeline must block")
	}
	if len(result.Files)+len(result.Dispositions)+len(result.Diagnostics) != 0 {
		t.Errorf("a nil pipeline produced %+v", result)
	}
}

// TestVisibilityVocabulary checks the constant values of the options contract.
func TestVisibilityVocabulary(t *testing.T) {
	for _, testCase := range []struct {
		got  TargetVisibility
		want string
	}{
		{VisibilityUnknown, "unknown"},
		{VisibilityPublic, "public"},
		{VisibilityPrivate, "private"},
	} {
		if string(testCase.got) != testCase.want {
			t.Errorf("visibility %q, want %q (emit-gha.md 2.1)", testCase.got, testCase.want)
		}
	}
}

// TestOptionsZeroValueIsTheDocumentedDefault checks that the zero value is
// lenient and never claims a public target.
func TestOptionsZeroValueIsTheDocumentedDefault(t *testing.T) {
	var options Options
	if options.AllowPermissionWidening {
		t.Error("permission widening must default to false (emit-gha.md 2.1)")
	}
	if options.Strict {
		t.Error("Strict must default to false (emit-gha.md 2.1)")
	}
	if options.TargetVisibility == VisibilityPublic {
		t.Error("the zero value must not claim a public target")
	}
}

// TestDispositionIDsAreUnambiguousAndUnique checks Ruling R9: the variable
// declaration index and component escaping keep IDs unique for duplicate names
// and slash-containing IDs.
func TestDispositionIDsAreUnambiguousAndUnique(t *testing.T) {
	pipeline := &ir.Pipeline{
		ID:        "ci",
		Variables: []ir.Variable{{Name: "FOO"}, {Name: "FOO"}},
		Stages: []ir.Stage{{
			ID:        "st/1",
			Variables: []ir.Variable{{Name: "FOO"}},
		}},
		Jobs: []ir.Job{{
			ID:        "j/1",
			Variables: []ir.Variable{{Name: "FOO"}},
			Steps:     []ir.Step{{ID: "s/1"}},
		}},
	}
	result := Emit(pipeline, catalog.ActionsLock{}, stubNaming{}, Options{}, nil)
	seen := make(map[string]bool, len(result.Dispositions))
	for _, disposition := range result.Dispositions {
		if seen[disposition.ID] {
			t.Errorf("duplicate disposition ID %q", disposition.ID)
		}
		seen[disposition.ID] = true
	}
	for _, want := range []string{
		"variable:pipeline:ci/0/FOO",
		"variable:pipeline:ci/1/FOO",
		"variable:stage:st%2F1/0/FOO",
		"variable:job:j%2F1/0/FOO",
		"step:j%2F1/s%2F1",
	} {
		if !seen[want] {
			t.Errorf("missing disposition ID %q in %v", want, seen)
		}
	}
}
