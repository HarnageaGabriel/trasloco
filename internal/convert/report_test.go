package convert_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/convert"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/githubactions/emit"
	"github.com/google/go-cmp/cmp"
)

var update = flag.Bool("update", false, "update the report golden files")

// elem returns the writer record of a node in the output file.
func elem(nodeID string) emit.OutputElement {
	return emit.OutputElement{Path: "w.yml#/" + nodeID, NodeID: nodeID}
}

// resource returns the diagnostic option naming a node.
func resource(kind, key string) diag.Option {
	return diag.WithResource(diag.Resource{Kind: kind, Key: key, Name: key})
}

// buildInputs returns one representative multi-file scenario: a converted
// pipeline with approximations and placeholders, a blocked pipeline without
// output and a pipeline suppressed under --strict. The disposition lists are
// deliberately unsorted to prove the builder sorts them by ID.
func buildInputs() []convert.Input {
	return []convert.Input{
		{
			Path:   "src/deploy.yml",
			Result: emit.Result{Blocked: true},
		},
		{
			Path:   "src/gated.yml",
			Strict: true,
			Result: emit.Result{
				Diagnostics: []diag.Diagnostic{diag.MustNew("TRL-CONV-0003")},
				Dispositions: []emit.Disposition{
					{ID: "step:Job B/step-1", Kind: emit.DispositionPlaceholder, Fidelity: diag.FidelityNeedsManual},
				},
			},
		},
		{
			Path:        "src/build.yml",
			Output:      ".github/workflows/build.yml",
			Diagnostics: []diag.Diagnostic{diag.MustNew("TRL-CONV-0004")},
			Result: emit.Result{
				Files: []emit.File{{Path: ".github/workflows/build.yml", Data: []byte("name: build\n")}},
				Diagnostics: []diag.Diagnostic{
					diag.MustNew("TRL-CONV-0001"),
					diag.MustNew("TRL-CONV-0003"),
					diag.MustNew("TRL-CONV-0003"),
					diag.MustNew("TRL-CONV-0002", resource("workflow", "concurrency")),
				},
				Dispositions: []emit.Disposition{
					{ID: "trigger:tr1", Kind: emit.DispositionEmitted, Fidelity: diag.FidelityApproximated},
					{ID: "step:Job A/step-1", Kind: emit.DispositionEmitted, Fidelity: diag.FidelityExact},
					{ID: "job:Job A", Kind: emit.DispositionPlaceholder, Fidelity: diag.FidelityNeedsManual},
					{ID: "workflow:concurrency", Kind: emit.DispositionDropped, Fidelity: diag.FidelityUnsupported},
					{ID: "variable:pipeline:ci/0/group", Kind: emit.DispositionCarried, Fidelity: diag.FidelityApproximated},
				},
				OutputElements:  []emit.OutputElement{elem("trigger:tr1"), elem("step:Job A/step-1")},
				RequiredSecrets: []string{"API_TOKEN", "DB_PASSWORD"},
			},
		},
	}
}

// wantReport builds the golden report from buildInputs.
func wantReport() convert.Report {
	report, err := convert.New(buildInputs())
	if err != nil {
		panic(err)
	}
	return report
}

// TestNewReportGolden compares the encoded report with testdata/report.golden.
// Regenerate the golden file with:
//
//	go test ./internal/convert -run TestNewReportGolden -update
func TestNewReportGolden(t *testing.T) {
	report := wantReport()
	trace, err := convert.Encode(report)
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "report.golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, trace, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), string(trace)); diff != "" {
		t.Errorf("golden report (-want +got):\n%s", diff)
	}
}

// TestNewReportFromBlockedPipelineInputsOnly checks the T7a reality: every
// emit.Result is blocked, no file is emitted, and every node is a manual
// placeholder; the default policy (EXIT-021) still reports 0.
func TestNewReportFromBlockedPipelineInputsOnly(t *testing.T) {
	inputs := []convert.Input{
		{Path: "b.yml", Result: emit.Result{Blocked: true}},
		{Path: "a.yml", Result: emit.Result{Blocked: true}},
	}
	report, err := convert.New(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Files) != 2 {
		t.Fatalf("got %d files, want 2", len(report.Files))
	}
	for _, file := range report.Files {
		if !file.NoFile || !file.Blocked {
			t.Errorf("%s: blocked input must record noFile and blocked", file.Input)
		}
	}
	if got := convert.ExitCode(report, convert.Policy{}); got != convert.ExitOK {
		t.Errorf("ExitCode() = %d, want %d", got, convert.ExitOK)
	}
}

// TestNewReportFileFields checks the output path, hash and blocked state.
func TestNewReportFileFields(t *testing.T) {
	data := []byte("name: x\n")
	sum := sha256.Sum256(data)
	inputs := []convert.Input{{
		Path:   "a.yml",
		Result: emit.Result{Files: []emit.File{{Path: ".github/workflows/x.yml", Data: data}}},
	}}
	report, err := convert.New(inputs)
	if err != nil {
		t.Fatal(err)
	}
	file := report.Files[0]
	if file.Output != ".github/workflows/x.yml" || file.SHA256 != hex.EncodeToString(sum[:]) || file.Blocked || file.NoFile {
		t.Errorf("file fields = %+v", file)
	}
}

// TestNewReportRejectsOutputMismatch covers an Input.Output that disagrees
// with the emitted file.
func TestNewReportRejectsOutputMismatch(t *testing.T) {
	for name, input := range map[string]convert.Input{
		"different-path": {Path: "a.yml", Output: "other.yml", Result: emit.Result{Files: []emit.File{{Path: "x.yml"}}}},
		"no-file":        {Path: "a.yml", Output: "x.yml", Result: emit.Result{Blocked: true}},
	} {
		requireError(t, name, []convert.Input{input}, "output")
	}
}

func TestNewReportRejectsDuplicateInputPath(t *testing.T) {
	requireError(t, "dup-path", []convert.Input{{Path: "a.yml"}, {Path: "a.yml"}}, "duplicate input path")
}

// TestNewReportSortsEverything checks the deterministic orders: input paths,
// per-file disposition IDs, secret names, diagnostic codes and kinds.
func TestNewReportSortsEverything(t *testing.T) {
	report := wantReport()
	if len(report.Files) != 3 {
		t.Fatalf("got %d files, want 3", len(report.Files))
	}
	gotPaths := make([]string, 0, len(report.Files))
	for _, file := range report.Files {
		gotPaths = append(gotPaths, file.Input)
	}
	if diff := cmp.Diff([]string{"src/build.yml", "src/deploy.yml", "src/gated.yml"}, gotPaths); diff != "" {
		t.Errorf("file order (-want +got):\n%s", diff)
	}
	build := report.Files[0]
	buildIDs := make([]string, 0, len(build.Dispositions))
	for _, disposition := range build.Dispositions {
		buildIDs = append(buildIDs, disposition.ID)
	}
	wantIDs := []string{
		"job:Job A",
		"step:Job A/step-1",
		"trigger:tr1",
		"variable:pipeline:ci/0/group",
		"workflow:concurrency",
	}
	if diff := cmp.Diff(wantIDs, buildIDs); diff != "" {
		t.Errorf("build disposition order (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"API_TOKEN", "DB_PASSWORD"}, report.RequiredSecrets); diff != "" {
		t.Errorf("secrets (-want +got):\n%s", diff)
	}
	codes := make([]string, 0, len(report.Diagnostics))
	for _, summary := range report.Diagnostics {
		codes = append(codes, summary.Code)
	}
	if diff := cmp.Diff([]string{"TRL-CONV-0001", "TRL-CONV-0002", "TRL-CONV-0003", "TRL-CONV-0004"}, codes); diff != "" {
		t.Errorf("diagnostic summary order (-want +got):\n%s", diff)
	}
	kinds := make([]string, 0, len(report.Dispositions))
	for _, summary := range report.Dispositions {
		kinds = append(kinds, summary.Kind)
	}
	if diff := cmp.Diff([]string{"carried", "dropped", "emitted", "placeholder"}, kinds); diff != "" {
		t.Errorf("kind summary order (-want +got):\n%s", diff)
	}
	wantTotals := convert.Totals{Converted: 1, Approximated: 2, Manual: 3, Placeholders: 2, Dropped: 1, Carried: 1}
	if diff := cmp.Diff(wantTotals, report.Totals); diff != "" {
		t.Errorf("totals (-want +got):\n%s", diff)
	}
	// Pre-emit diagnostics precede the emitter's in the file entry.
	if len(build.Diagnostics) != 5 || build.Diagnostics[0].Code != "TRL-CONV-0004" {
		t.Errorf("build diagnostics = %d entries, first %q; want 5 with the pre-emit one first", len(build.Diagnostics), build.Diagnostics[0].Code)
	}
}

// TestNewReportDeterministicPermutation reorders the inputs and re-encodes:
// the report bytes must not change (emit-gha.md EMITV-082 property).
func TestNewReportDeterministicPermutation(t *testing.T) {
	inputs := buildInputs()
	first, err := convert.New(inputs)
	if err != nil {
		t.Fatal(err)
	}
	firstTrace, err := convert.Encode(first)
	if err != nil {
		t.Fatal(err)
	}
	for index := range inputs {
		permuted := append(append(make([]convert.Input, 0, len(inputs)), inputs[index:]...), inputs[:index]...)
		report, err := convert.New(permuted)
		if err != nil {
			t.Fatal(err)
		}
		trace, err := convert.Encode(report)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(string(firstTrace), string(trace)); diff != "" {
			t.Errorf("permutation %d changed the report (-first +got):\n%s", index, diff)
		}
	}
}

func TestNewReportDeduplicatesSecretNames(t *testing.T) {
	inputs := []convert.Input{
		{
			Path: "a.yml",
			Result: emit.Result{
				RequiredSecrets: []string{"DB_PASSWORD", "API_TOKEN", "DB_PASSWORD"},
			},
		},
		{
			Path: "b.yml",
			Result: emit.Result{
				RequiredSecrets: []string{"api_token", "API_TOKEN"},
			},
		},
	}
	report, err := convert.New(inputs)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"API_TOKEN", "DB_PASSWORD", "api_token"}
	if diff := cmp.Diff(want, report.RequiredSecrets); diff != "" {
		t.Errorf("secrets (-want +got):\n%s", diff)
	}
}

// TestNewReportSecretNamedVariable feeds a secret-named variable: its name
// may appear (diagnostic text is scrubbed upstream and names are not values),
// but no value the report was never given can appear, in any escaping.
func TestNewReportSecretNamedVariable(t *testing.T) {
	canary := "s3cr3t-canary-0001"
	finding := diag.MustNew("TRL-CONV-0003",
		resource("variable", "DB_PASSWORD"),
		diag.WithMessage("variable DB_PASSWORD needs a manual secret"),
		diag.WithSource(diag.Source{Platform: "azure-pipelines", File: "a.yml", Line: 3, Col: 5}))
	inputs := []convert.Input{{
		Path: "a.yml",
		Result: emit.Result{
			Diagnostics: []diag.Diagnostic{finding},
			Dispositions: []emit.Disposition{
				{ID: "variable:DB_PASSWORD", Kind: emit.DispositionPlaceholder, Fidelity: diag.FidelityNeedsManual},
			},
			RequiredSecrets: []string{"DB_PASSWORD"},
		},
	}}
	report, err := convert.New(inputs)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := convert.Encode(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(trace, []byte("variable DB_PASSWORD needs a manual secret")) {
		t.Error("the report must carry the full diagnostic message")
	}
	for _, form := range []string{canary, strings.ToUpper(canary), fmt.Sprintf("%q", canary)} {
		if bytes.Contains(trace, []byte(form)) {
			t.Errorf("report carries the canary form %q", form)
		}
	}
	file := report.Files[0]
	if len(file.Diagnostics) != 1 || file.Diagnostics[0].Resource == nil || file.Diagnostics[0].Source == nil {
		t.Errorf("file diagnostics lost the resource or source: %+v", file.Diagnostics)
	}
}

// requireError asserts New fails with an error mentioning want and returns no
// report (a silent loss never reaches the JSON output).
func requireError(t *testing.T, name string, inputs []convert.Input, want string) {
	t.Helper()
	report, err := convert.New(inputs)
	if err == nil {
		t.Errorf("%s: New() succeeded, want an error mentioning %q", name, want)
		return
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("%s: error = %v, want it to mention %q", name, err, want)
	}
	if report.SchemaVersion != 0 || len(report.Files) != 0 {
		t.Errorf("%s: New() returned a report together with an error", name)
	}
}

// TestNewReportConservation has one case per silent-loss condition of
// emit-gha.md section 2.3.
func TestNewReportConservation(t *testing.T) {
	file := []emit.File{{Path: "w.yml", Data: []byte("x\n")}}
	tests := map[string]struct {
		result emit.Result
		want   string
	}{
		"duplicate-disposition-id": {
			result: emit.Result{Dispositions: []emit.Disposition{
				{ID: "job:x", Kind: emit.DispositionDefault, Fidelity: diag.FidelityExact},
				{ID: "job:x", Kind: emit.DispositionDefault, Fidelity: diag.FidelityExact},
			}},
			want: "duplicate disposition ID",
		},
		"empty-id": {
			result: emit.Result{Dispositions: []emit.Disposition{{Kind: emit.DispositionPlaceholder, Fidelity: diag.FidelityNeedsManual}}},
			want:   "without an ID",
		},
		"dropped-without-diagnostic": {
			result: emit.Result{Dispositions: []emit.Disposition{{ID: "job:x#container", Kind: emit.DispositionDropped, Fidelity: diag.FidelityUnsupported}}},
			want:   "no diagnostic naming it",
		},
		"dropped-with-diagnostic-of-another-node": {
			result: emit.Result{
				Diagnostics:  []diag.Diagnostic{diag.MustNew("TRL-CONV-0002", resource("job", "y"))},
				Dispositions: []emit.Disposition{{ID: "job:x#container", Kind: emit.DispositionDropped, Fidelity: diag.FidelityUnsupported}},
			},
			want: "no diagnostic naming it",
		},
		"emitted-without-output-element": {
			result: emit.Result{Files: file, Dispositions: []emit.Disposition{{ID: "job:x", Kind: emit.DispositionEmitted, Fidelity: diag.FidelityExact}}},
			want:   "has no output element",
		},
		"emitted-without-file": {
			result: emit.Result{
				Dispositions:   []emit.Disposition{{ID: "job:x", Kind: emit.DispositionEmitted, Fidelity: diag.FidelityExact}},
				OutputElements: []emit.OutputElement{elem("job:x")},
			},
			want: "without a file",
		},
		"output-element-without-disposition": {
			result: emit.Result{Files: file, OutputElements: []emit.OutputElement{elem("job:ghost")}},
			want:   "without a disposition",
		},
		"placeholder-required": {
			result: emit.Result{
				Files:       file,
				Diagnostics: []diag.Diagnostic{diag.MustNew("TRL-CAT-0003", resource("step", "j/step-1"))},
				Dispositions: []emit.Disposition{
					{ID: "step:j/step-1", Kind: emit.DispositionEmitted, Fidelity: diag.FidelityExact},
				},
				OutputElements: []emit.OutputElement{elem("step:j/step-1")},
			},
			want: "requires a placeholder",
		},
	}
	for name, test := range tests {
		requireError(t, name, []convert.Input{{Path: "a.yml", Result: test.result}}, test.want)
	}
}

// TestNewReportPlaceholderRequiredAccepted is the passing counterpart: the
// node of a missing lock entry is a placeholder.
func TestNewReportPlaceholderRequiredAccepted(t *testing.T) {
	inputs := []convert.Input{{Path: "a.yml", Result: emit.Result{
		Diagnostics:  []diag.Diagnostic{diag.MustNew("TRL-CAT-0003", resource("step", "j/step-1"))},
		Dispositions: []emit.Disposition{{ID: "step:j/step-1", Kind: emit.DispositionPlaceholder, Fidelity: diag.FidelityNeedsManual}},
	}}}
	if _, err := convert.New(inputs); err != nil {
		t.Fatal(err)
	}
}

// TestNewReportDroppedFeatureMatchesOwner accepts a feature ID whose
// diagnostic names the owning node.
func TestNewReportDroppedFeatureMatchesOwner(t *testing.T) {
	inputs := []convert.Input{{Path: "a.yml", Result: emit.Result{
		Diagnostics:  []diag.Diagnostic{diag.MustNew("TRL-CONV-0002", resource("job", "Build"))},
		Dispositions: []emit.Disposition{{ID: "job:Build#container", Kind: emit.DispositionDropped, Fidelity: diag.FidelityUnsupported}},
	}}}
	if _, err := convert.New(inputs); err != nil {
		t.Fatal(err)
	}
}

// qualified names a node the way the emitter does: Key is the qualified ID.
func qualified(kind, id string) diag.Option {
	return diag.WithResource(diag.Resource{Kind: kind, Key: id, Name: id})
}

// TestNewReportEmitterResourceForm accepts the emitter's Resource form
// (Kind "job", Key "job:Build") for dropped nodes of a no-file result and for
// the placeholder-required check.
func TestNewReportEmitterResourceForm(t *testing.T) {
	inputs := []convert.Input{{Path: "a.yml", Result: emit.Result{
		Blocked: true,
		Diagnostics: []diag.Diagnostic{
			diag.MustNew("TRL-CONV-0002", qualified("job", "job:Build")),
			diag.MustNew("TRL-CONV-0002", qualified("step", "step:Build/step-1")),
			diag.MustNew("TRL-CAT-0003", qualified("step", "step:Build/step-2")),
		},
		Dispositions: []emit.Disposition{
			{ID: "job:Build#container", Kind: emit.DispositionDropped, Fidelity: diag.FidelityUnsupported},
			{ID: "step:Build/step-1", Kind: emit.DispositionDropped, Fidelity: diag.FidelityUnsupported},
			{ID: "step:Build/step-2", Kind: emit.DispositionPlaceholder, Fidelity: diag.FidelityNeedsManual},
		},
	}}}
	if _, err := convert.New(inputs); err != nil {
		t.Fatal(err)
	}
	inputs[0].Result.Dispositions[2].Kind = emit.DispositionDropped
	inputs[0].Result.Dispositions[2].Fidelity = diag.FidelityUnsupported
	requireError(t, "placeholder-required-qualified", inputs, "requires a placeholder")
	requireError(t, "dropped-qualified-other-node", []convert.Input{{Path: "a.yml", Result: emit.Result{
		Diagnostics:  []diag.Diagnostic{diag.MustNew("TRL-CONV-0002", qualified("job", "job:Other"))},
		Dispositions: []emit.Disposition{{ID: "job:Build#container", Kind: emit.DispositionDropped, Fidelity: diag.FidelityUnsupported}},
	}}}, "no diagnostic naming it")
}

// TestNewReportRejectsStrictWithFileOrEmitted covers emit-gha.md 2.2/2.3.
func TestNewReportRejectsStrictWithFileOrEmitted(t *testing.T) {
	requireError(t, "strict-file", []convert.Input{{Path: "a.yml", Strict: true, Result: emit.Result{Files: okFile()}}}, "strict result must not carry a file")
	requireError(t, "strict-emitted", []convert.Input{{Path: "a.yml", Strict: true, Result: emit.Result{
		Dispositions: []emit.Disposition{{ID: "job:a", Kind: emit.DispositionEmitted, Fidelity: diag.FidelityExact}},
	}}}, "strict result has emitted")
}

// TestNewReportKindFidelityPairs validates the table of section 2.3.
func TestNewReportKindFidelityPairs(t *testing.T) {
	bad := []emit.Disposition{
		{ID: "job:x", Kind: "bogus", Fidelity: diag.FidelityExact},
		{ID: "job:x", Kind: emit.DispositionEmitted},
		{ID: "job:x", Kind: emit.DispositionDefault, Fidelity: diag.FidelityApproximated},
		{ID: "job:x", Kind: emit.DispositionPlaceholder, Fidelity: diag.FidelityExact},
		{ID: "job:x", Kind: emit.DispositionPlaceholder, Fidelity: diag.FidelityUnsupported},
		{ID: "job:x", Kind: emit.DispositionDropped, Fidelity: diag.FidelityExact},
		{ID: "job:x", Kind: emit.DispositionDropped, Fidelity: diag.FidelityNeedsManual},
		{ID: "job:x", Kind: emit.DispositionCarried, Fidelity: "weird"},
	}
	for _, disposition := range bad {
		name := fmt.Sprintf("%s/%s", disposition.Kind, disposition.Fidelity)
		inputs := []convert.Input{{Path: "a.yml", Result: emit.Result{Dispositions: []emit.Disposition{disposition}}}}
		requireError(t, name, inputs, "invalid kind/fidelity pair")
	}
}

// TestNewReportInconsistentSeverity rejects one code reported with two
// severities.
func TestNewReportInconsistentSeverity(t *testing.T) {
	first := diag.MustNew("TRL-CONV-0003")
	second := first
	second.Severity = diag.SeverityError
	inputs := []convert.Input{{Path: "a.yml", Result: emit.Result{Diagnostics: []diag.Diagnostic{first, second}}}}
	requireError(t, "severity", inputs, "severities")
}

// TestNewReportTotalsCountEveryNodeOnce is the conservation property over
// the totals: whatever the kind, fidelity decides the class counts.
func TestNewReportTotalsCountEveryNodeOnce(t *testing.T) {
	inputs := []convert.Input{{
		Path: "a.yml",
		Result: emit.Result{
			Files: []emit.File{{Path: "w.yml"}},
			Diagnostics: []diag.Diagnostic{
				diag.MustNew("TRL-CONV-0002", resource("a", "7")),
				diag.MustNew("TRL-CONV-0002", resource("a", "9")),
			},
			Dispositions: []emit.Disposition{
				{ID: "a:1", Kind: emit.DispositionEmitted, Fidelity: diag.FidelityExact},
				{ID: "a:2", Kind: emit.DispositionEmitted, Fidelity: diag.FidelityApproximated},
				{ID: "a:3", Kind: emit.DispositionEmitted, Fidelity: diag.FidelityNeedsManual},
				{ID: "a:4", Kind: emit.DispositionEmitted, Fidelity: diag.FidelityUnsupported},
				{ID: "a:5", Kind: emit.DispositionDefault, Fidelity: diag.FidelityExact},
				{ID: "a:6", Kind: emit.DispositionPlaceholder, Fidelity: diag.FidelityNeedsManual},
				{ID: "a:7", Kind: emit.DispositionDropped, Fidelity: diag.FidelityApproximated},
				{ID: "a:8", Kind: emit.DispositionCarried, Fidelity: diag.FidelityUnsupported},
				{ID: "a:9", Kind: emit.DispositionDropped, Fidelity: diag.FidelityUnsupported},
			},
			OutputElements: []emit.OutputElement{elem("a:1"), elem("a:2"), elem("a:3"), elem("a:4")},
		},
	}}
	report, err := convert.New(inputs)
	if err != nil {
		t.Fatal(err)
	}
	want := convert.Totals{Converted: 2, Approximated: 2, Manual: 5, Placeholders: 1, Dropped: 2, Carried: 1}
	if diff := cmp.Diff(want, report.Totals); diff != "" {
		t.Errorf("totals (-want +got):\n%s", diff)
	}
	if total := want.Converted + want.Approximated + want.Manual; total != 9 {
		t.Errorf("classes cover %d of 9 nodes", total)
	}
}

// TestExitCodeVectors follows docs/spec/exit-codes.md for convert. A single
// input never yields exit 2.
func TestExitCodeVectors(t *testing.T) {
	tests := []struct {
		id     string
		inputs func() []convert.Input
		policy convert.Policy
		want   int
	}{
		{"EXIT-001", converted, convert.Policy{}, convert.ExitOK},
		{"EXIT-002", approximated, convert.Policy{}, convert.ExitOK},
		{"EXIT-003", errored, convert.Policy{}, convert.ExitError},
		{"EXIT-015", manual, convert.Policy{FailOn: convert.FailOnNone}, convert.ExitOK},
		{"EXIT-016", manual, convert.Policy{FailOn: convert.FailOnManual}, convert.ExitPolicy},
		{"EXIT-017", approximated, convert.Policy{FailOn: convert.FailOnManual}, convert.ExitOK},
		{"EXIT-021", blocked, convert.Policy{}, convert.ExitOK},
		{"EXIT-022", manual, convert.Policy{FailOn: convert.FailOnWarning}, convert.ExitPolicy},
		// Fidelity, not kind, drives the thresholds (F1).
		{"manual-via-unsupported-emitted", unsupportedEmitted, convert.Policy{FailOn: convert.FailOnManual}, convert.ExitPolicy},
		{"manual-via-dropped", dropped, convert.Policy{FailOn: convert.FailOnManual}, convert.ExitPolicy},
		{"warning-via-approximated", approximated, convert.Policy{FailOn: convert.FailOnWarning}, convert.ExitPolicy},
		{"warning-via-warning-severity", warningOnly, convert.Policy{FailOn: convert.FailOnWarning}, convert.ExitPolicy},
		{"warning-clean", converted, convert.Policy{FailOn: convert.FailOnWarning}, convert.ExitOK},
		// Error diagnostics of a pre-emit stage fail the unit.
		{"pre-emit-error", preEmitError, convert.Policy{}, convert.ExitError},
		// --strict counts a suppressed output as a failed unit (docs/install.md).
		{"strict-suppressed", suppressed, convert.Policy{Strict: true}, convert.ExitError},
		{"strict-blocked", blocked, convert.Policy{Strict: true}, convert.ExitError},
		// An operational error dominates a --fail-on threshold.
		{"error-beats-failon", errored, convert.Policy{FailOn: convert.FailOnManual}, convert.ExitError},
	}
	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			report, err := convert.New(test.inputs())
			if err != nil {
				t.Fatal(err)
			}
			if got := convert.ExitCode(report, test.policy); got != test.want {
				t.Errorf("ExitCode() = %d, want %d", got, test.want)
			}
		})
	}
}

// TestExitCodeNeverPartial documents that exit 2 is not returned for any
// single-input combination.
func TestExitCodeNeverPartial(t *testing.T) {
	fixtures := []func() []convert.Input{converted, approximated, errored, manual, blocked, suppressed, unsupportedEmitted, dropped, warningOnly, preEmitError}
	policies := []convert.Policy{{}, {Strict: true}, {FailOn: convert.FailOnManual}, {FailOn: convert.FailOnWarning}, {Strict: true, FailOn: convert.FailOnWarning}}
	for index, fixture := range fixtures {
		report, err := convert.New(fixture())
		if err != nil {
			t.Fatal(err)
		}
		for _, policy := range policies {
			if got := convert.ExitCode(report, policy); got == 2 {
				t.Errorf("fixture %d policy %+v: ExitCode() = 2, want it never", index, policy)
			}
		}
	}
}

// TestExitCodeWithoutFiles yields 0 for an empty report under any policy.
func TestExitCodeWithoutFiles(t *testing.T) {
	report := convert.Report{SchemaVersion: convert.SchemaVersion}
	for _, policy := range []convert.Policy{
		{},
		{Strict: true},
		{FailOn: convert.FailOnManual},
		{Strict: true, FailOn: convert.FailOnWarning},
	} {
		if got := convert.ExitCode(report, policy); got != convert.ExitOK {
			t.Errorf("ExitCode(%+v) = %d, want %d", policy, got, convert.ExitOK)
		}
	}
}

func TestParseFailOn(t *testing.T) {
	for value, want := range map[string]convert.FailOn{
		"":        convert.FailOnNone,
		"manual":  convert.FailOnManual,
		"warning": convert.FailOnWarning,
	} {
		got, err := convert.ParseFailOn(value)
		if err != nil || got != want {
			t.Errorf("ParseFailOn(%q) = %q, %v; want %q", value, got, err, want)
		}
	}
	// The plan lists never and error; the spec does not, and the spec wins.
	for _, value := range []string{"never", "error", "Manual", "bogus"} {
		if _, err := convert.ParseFailOn(value); err == nil {
			t.Errorf("ParseFailOn(%q) succeeded, want an error", value)
		}
	}
}

func TestEncodeShape(t *testing.T) {
	report, err := convert.New(buildInputs())
	if err != nil {
		t.Fatal(err)
	}
	trace, err := convert.Encode(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(trace, []byte("}\n")) || bytes.HasSuffix(trace, []byte("}\n\n")) {
		t.Errorf("the report must end with exactly one LF: %.20q", trace[len(trace)-10:])
	}
	if !bytes.Contains(trace, []byte("\n  \"schemaVersion\": 1,\n")) {
		t.Error("the report must use two-space indentation with a stable head field")
	}
	if bytes.Contains(trace, []byte("\\u003c")) {
		t.Error("the report must not HTML-escape")
	}
}

// TestEncodeNormalisesNilSlices proves a hand-built report encodes arrays,
// never null.
func TestEncodeNormalisesNilSlices(t *testing.T) {
	report := convert.Report{SchemaVersion: convert.SchemaVersion, Files: []convert.FileReport{{Input: "a.yml"}}}
	trace, err := convert.Encode(report)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(trace, []byte("null")) {
		t.Errorf("the report must not contain null:\n%s", trace)
	}
	for _, key := range []string{`"diagnostics": []`, `"requiredSecrets": []`, `"dispositions": []`} {
		if !bytes.Contains(trace, []byte(key)) {
			t.Errorf("the report must contain %s:\n%s", key, trace)
		}
	}
}

func okFile() []emit.File { return []emit.File{{Path: "out.yml", Data: []byte("x\n")}} }

// converted is one emitted file with exact fidelity.
func converted() []convert.Input {
	return []convert.Input{{Path: "a.yml", Output: "out.yml", Result: emit.Result{
		Files:        okFile(),
		Diagnostics:  []diag.Diagnostic{diag.MustNew("TRL-CONV-0004")},
		Dispositions: []emit.Disposition{{ID: "job:a", Kind: emit.DispositionDefault, Fidelity: diag.FidelityExact}},
	}}}
}

func approximated() []convert.Input {
	return []convert.Input{{Path: "a.yml", Output: "out.yml", Result: emit.Result{
		Files:          okFile(),
		Diagnostics:    []diag.Diagnostic{diag.MustNew("TRL-CONV-0001")},
		Dispositions:   []emit.Disposition{{ID: "job:a", Kind: emit.DispositionEmitted, Fidelity: diag.FidelityApproximated}},
		OutputElements: []emit.OutputElement{elem("job:a")},
	}}}
}

// unsupportedEmitted is an emitted node whose fidelity is unsupported.
func unsupportedEmitted() []convert.Input {
	return []convert.Input{{Path: "a.yml", Output: "out.yml", Result: emit.Result{
		Files:          okFile(),
		Dispositions:   []emit.Disposition{{ID: "job:a", Kind: emit.DispositionEmitted, Fidelity: diag.FidelityUnsupported}},
		OutputElements: []emit.OutputElement{elem("job:a")},
	}}}
}

// dropped is a dropped node with its diagnostic.
func dropped() []convert.Input {
	return []convert.Input{{Path: "a.yml", Output: "out.yml", Result: emit.Result{
		Files:        okFile(),
		Diagnostics:  []diag.Diagnostic{diag.MustNew("TRL-CONV-0002", resource("job", "a"))},
		Dispositions: []emit.Disposition{{ID: "job:a#container", Kind: emit.DispositionDropped, Fidelity: diag.FidelityUnsupported}},
	}}}
}

// warningOnly has a warning diagnostic and only exact nodes.
func warningOnly() []convert.Input {
	return []convert.Input{{Path: "a.yml", Output: "out.yml", Result: emit.Result{
		Files:        okFile(),
		Diagnostics:  []diag.Diagnostic{diag.MustNew("TRL-CONV-0003")},
		Dispositions: []emit.Disposition{{ID: "job:a", Kind: emit.DispositionDefault, Fidelity: diag.FidelityExact}},
	}}}
}

// preEmitError is an error diagnostic of a stage before emission.
func preEmitError() []convert.Input {
	return []convert.Input{{Path: "a.yml", Diagnostics: []diag.Diagnostic{diag.MustNew("TRL-EMIT-0002")}, Result: emit.Result{Blocked: true}}}
}

// errored is one unit with an error-severity diagnostic and no emitted file.
func errored() []convert.Input {
	return []convert.Input{{Path: "a.yml", Result: emit.Result{
		Diagnostics:     []diag.Diagnostic{diag.MustNew("TRL-EMIT-0002")},
		Dispositions:    []emit.Disposition{{ID: "job:a", Kind: emit.DispositionPlaceholder, Fidelity: diag.FidelityNeedsManual}},
		RequiredSecrets: []string{"token"},
	}}}
}

// manual is one emitted file with placeholder nodes, no errors.
func manual() []convert.Input {
	return []convert.Input{{Path: "a.yml", Output: "out.yml", Result: emit.Result{
		Files:        okFile(),
		Diagnostics:  []diag.Diagnostic{diag.MustNew("TRL-CONV-0003")},
		Dispositions: []emit.Disposition{{ID: "job:a", Kind: emit.DispositionPlaceholder, Fidelity: diag.FidelityNeedsManual}},
	}}}
}

// blocked is a blocked emission (EXIT-021): no file, placeholder only.
func blocked() []convert.Input {
	return []convert.Input{{Path: "a.yml", Result: emit.Result{
		Blocked:      true,
		Diagnostics:  []diag.Diagnostic{diag.MustNew("TRL-CONV-0003")},
		Dispositions: []emit.Disposition{{ID: "job:a", Kind: emit.DispositionPlaceholder, Fidelity: diag.FidelityNeedsManual}},
	}}}
}

// suppressed is a file emitted then suppressed by --strict.
func suppressed() []convert.Input {
	return []convert.Input{{Path: "a.yml", Strict: true, Result: emit.Result{
		Diagnostics:  []diag.Diagnostic{diag.MustNew("TRL-CONV-0003")},
		Dispositions: []emit.Disposition{{ID: "job:a", Kind: emit.DispositionPlaceholder, Fidelity: diag.FidelityNeedsManual}},
	}}}
}
