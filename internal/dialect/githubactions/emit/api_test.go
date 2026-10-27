package emit

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
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

// TestEmitReportsOneDispositionPerNode checks the T7b contract: the
// scaffolding is written, the result is blocked, and every IR node of
// emit-gha.md 2.3 carries exactly one disposition under a stable ID, sorted by
// ID.
func TestEmitReportsOneDispositionPerNode(t *testing.T) {
	result := Emit(samplePipeline(), catalog.ActionsLock{}, stubNaming{}, Options{}, nil)
	if !result.Blocked {
		t.Error("T7b emission must be blocked: no mapping rule exists yet")
	}
	if len(result.Files) != 1 {
		t.Fatalf("files = %d, want the placeholder workflow", len(result.Files))
	}
	if len(result.OutputElements) == 0 {
		t.Error("the placeholder workflow produced no output elements")
	}
	if len(result.RequiredSecrets) != 0 {
		t.Errorf("requiredSecrets = %v, want none", result.RequiredSecrets)
	}
	want := map[string]struct {
		kind     DispositionKind
		fidelity diag.Fidelity
	}{
		"input:in1":                  {DispositionDropped, diag.FidelityApproximated},
		"job:Job A":                  {DispositionPlaceholder, diag.FidelityNeedsManual},
		"pipeline:ci":                {DispositionEmitted, diag.FidelityApproximated},
		"stage:st1":                  {DispositionDropped, diag.FidelityNeedsManual},
		"step:Job A/step-1":          {DispositionPlaceholder, diag.FidelityNeedsManual},
		"trigger:tr1":                {DispositionDropped, diag.FidelityNeedsManual},
		"variable:job:Job A/0/BAR":   {DispositionDropped, diag.FidelityNeedsManual},
		"variable:pipeline:ci/0/FOO": {DispositionDropped, diag.FidelityNeedsManual},
	}
	if len(result.Dispositions) != len(want) {
		t.Fatalf("%d dispositions, want %d", len(result.Dispositions), len(want))
	}
	previous := ""
	for _, disposition := range result.Dispositions {
		expected, ok := want[disposition.ID]
		if !ok {
			t.Errorf("unexpected disposition ID %q", disposition.ID)
			continue
		}
		if disposition.Kind != expected.kind || disposition.Fidelity != expected.fidelity {
			t.Errorf("%s: %s/%s, want %s/%s", disposition.ID, disposition.Kind,
				disposition.Fidelity, expected.kind, expected.fidelity)
		}
		if previous > disposition.ID {
			t.Errorf("dispositions not sorted: %q before %q", previous, disposition.ID)
		}
		previous = disposition.ID
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
		if finding.Resource == nil {
			t.Fatalf("%s: finding without a resource", finding.Code)
		}
		if finding.Resource.Key == "job:Job A" {
			if finding.Code != manualConversionCode {
				t.Errorf("job finding code = %s, want %s", finding.Code, manualConversionCode)
			}
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

// failingNaming is a naming inventory that has no decision for a requested
// name: the emitter must treat that as an internal defect, never invent one.
type failingNaming struct{ stubNaming }

func (failingNaming) JobID(_, _ string) (string, error) {
	return "", errors.New("no naming decision")
}

// TestEmitWritesReviewablePlaceholder checks the section 2.5 placeholder and
// the section 4.1 scaffolding of a T7b document.
func TestEmitWritesReviewablePlaceholder(t *testing.T) {
	result := Emit(samplePipeline(), catalog.ActionsLock{}, stubNaming{}, Options{}, nil)
	if len(result.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(result.Files))
	}
	file := result.Files[0]
	if file.Path != ".github/workflows/Build and Test.yml" {
		t.Errorf("path = %q", file.Path)
	}
	data := string(file.Data)
	for _, want := range []string{
		"# Generated by trasloco. Review before enabling.\n",
		"\non:\n",
		"\npermissions: {}\n",
		"\njobs:\n",
		"\n  Job A:\n",
		"\n    permissions: {}\n",
		"\n    runs-on: ubuntu-24.04\n",
		"\n    steps:\n",
		"Manual conversion required",
		"TRL-CONV-0003",
	} {
		if !strings.Contains(data, want) {
			t.Errorf("workflow does not contain %q:\n%s", want, data)
		}
	}
	if bytes.Contains(file.Data, []byte("\r")) {
		t.Error("workflow contains a carriage return")
	}
	if bytes.HasPrefix(file.Data, []byte{0xEF, 0xBB, 0xBF}) {
		t.Error("workflow starts with a BOM")
	}
	if !bytes.HasSuffix(file.Data, []byte("\n")) || bytes.HasSuffix(file.Data, []byte("\n\n")) {
		t.Errorf("workflow must end with exactly one LF, got %q", data[len(data)-2:])
	}
}

// TestEmitStrictSuppressesFileOnlyOnErrorDiagnostic checks F8: Options.Strict
// keeps the reviewable file when no error-severity diagnostic was collected
// (the T7b placeholder findings are warnings), and hasErrorDiagnostic is the
// trigger that would suppress it.
func TestEmitStrictSuppressesFileOnlyOnErrorDiagnostic(t *testing.T) {
	result := Emit(samplePipeline(), catalog.ActionsLock{}, stubNaming{}, Options{Strict: true}, nil)
	if len(result.Files) != 1 {
		t.Errorf("strict result without error diagnostics has %d files, want 1", len(result.Files))
	}
	if hasErrorDiagnostic(result.Diagnostics) {
		t.Error("T7b placeholder diagnostics must not have error severity")
	}
	warning := diag.MustNew(manualConversionCode)
	failure := diag.MustNew(internalEmitterCode)
	if hasErrorDiagnostic([]diag.Diagnostic{warning}) || !hasErrorDiagnostic([]diag.Diagnostic{warning, failure}) {
		t.Error("hasErrorDiagnostic must report exactly the error severity")
	}
}

// TestEmitWithoutJobsReturnsNoFile checks section 2.2: a pipeline without jobs
// yields no file and the emitter adjustment finding.
func TestEmitWithoutJobsReturnsNoFile(t *testing.T) {
	pipeline := samplePipeline()
	pipeline.Jobs = nil
	pipeline.Stages = nil
	result := Emit(pipeline, catalog.ActionsLock{}, stubNaming{}, Options{}, nil)
	if len(result.Files) != 0 || len(result.OutputElements) != 0 {
		t.Errorf("a pipeline without jobs produced %d files and %d elements",
			len(result.Files), len(result.OutputElements))
	}
	found := false
	for _, finding := range result.Diagnostics {
		if finding.Code == emitterAdjustmentCode {
			found = true
		}
	}
	if !found {
		t.Errorf("no %s finding for a pipeline without jobs", emitterAdjustmentCode)
	}
}

// TestEmitFeatureIDsAndCarriedExtensions checks section 2.3: every present
// out-of-scope feature carries its own `<node>#<feature>` disposition, and a
// reader-recorded extension is carried without a new diagnostic.
func TestEmitFeatureIDsAndCarriedExtensions(t *testing.T) {
	pipeline := &ir.Pipeline{
		ID:          "ci",
		Concurrency: &ir.Concurrency{Group: ir.Expr{}},
		Metadata: ir.Metadata{Extensions: ir.Extensions{
			"x-ado/b": json.RawMessage(`2`),
			"x-ado/a": json.RawMessage(`1`),
		}},
		Jobs: []ir.Job{{
			ID:          "j1",
			Container:   &ir.Container{Image: "alpine"},
			Matrix:      &ir.Matrix{},
			Uses:        &ir.ReusableCall{Ref: "org/repo/.github/workflows/ci.yml@main"},
			Permissions: ir.OrderedMap[string]{{Key: "contents", Value: "read"}},
			Steps: []ir.Step{
				{ID: "s1", Kind: ir.StepRun, Run: &ir.RunStep{Operations: []ir.ProtocolOperation{{Kind: ir.StepSetEnv}}}},
				{ID: "s2", Kind: ir.StepCheckout, Checkout: &ir.CheckoutStep{Repository: "self", Path: "sub"}},
			},
		}},
	}
	result := Emit(pipeline, catalog.ActionsLock{}, stubNaming{}, Options{}, nil)
	kinds := make(map[string]Disposition, len(result.Dispositions))
	for _, disposition := range result.Dispositions {
		kinds[disposition.ID] = disposition
	}
	for _, want := range []struct {
		id   string
		kind DispositionKind
	}{
		{"pipeline:ci#concurrency", DispositionDropped},
		{"pipeline:ci#carried/x-ado/a", DispositionCarried},
		{"pipeline:ci#carried/x-ado/b", DispositionCarried},
		{"job:j1#container", DispositionPlaceholder},
		{"job:j1#matrix", DispositionPlaceholder},
		{"job:j1#permissions", DispositionDropped},
		{"job:j1#uses", DispositionPlaceholder},
		{"step:j1/s1#run-operations", DispositionPlaceholder},
		{"step:j1/s2#checkout-path", DispositionPlaceholder},
	} {
		disposition, ok := kinds[want.id]
		if !ok {
			t.Errorf("missing disposition %q", want.id)
			continue
		}
		if disposition.Kind != want.kind {
			t.Errorf("%s: kind = %s, want %s", want.id, disposition.Kind, want.kind)
		}
	}
	for _, finding := range result.Diagnostics {
		if finding.Resource != nil && strings.Contains(finding.Resource.Key, "#carried/") {
			t.Errorf("carried feature %q produced diagnostic %s", finding.Resource.Key, finding.Code)
		}
	}
}

// TestEmitNamingMissIsInternal checks that a naming inventory miss is reported
// as an internal defect and never yields a file.
func TestEmitNamingMissIsInternal(t *testing.T) {
	result := Emit(samplePipeline(), catalog.ActionsLock{}, failingNaming{}, Options{}, nil)
	if len(result.Files) != 0 || len(result.OutputElements) != 0 {
		t.Errorf("a naming miss produced %d files and %d elements",
			len(result.Files), len(result.OutputElements))
	}
	found := false
	for _, finding := range result.Diagnostics {
		if finding.Code == internalEmitterCode {
			found = true
		}
	}
	if !found {
		t.Errorf("no %s finding for a naming miss", internalEmitterCode)
	}
}

// TestEmitOmitsUnrepresentableName checks section 5.13: a name containing the
// GitHub expression delimiter is omitted and reported.
func TestEmitOmitsUnrepresentableName(t *testing.T) {
	pipeline := samplePipeline()
	pipeline.Name = "build ${{ secrets.X }}"
	pipeline.Jobs[0].Name = "job ${{ secrets.Y }}"
	result := Emit(pipeline, catalog.ActionsLock{}, stubNaming{}, Options{}, nil)
	if len(result.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(result.Files))
	}
	if bytes.Contains(result.Files[0].Data, []byte("${{")) {
		t.Errorf("workflow contains the expression delimiter:\n%s", result.Files[0].Data)
	}
	seen := make(map[string]bool, 2)
	for _, finding := range result.Diagnostics {
		if finding.Code == expressionDelimiterCode && finding.Resource != nil {
			seen[finding.Resource.Key] = true
		}
	}
	for _, key := range []string{"pipeline:ci", "job:Job A"} {
		if !seen[key] {
			t.Errorf("no %s finding for %s", expressionDelimiterCode, key)
		}
	}
}
