package mapping

import (
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/githubactions/emit/model"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/google/go-cmp/cmp"
)

// runnerCase is one row of the emit-gha.md 5.5 mapping tests. An empty
// wantLabel means the job must become a placeholder without a fragment.
type runnerCase struct {
	name         string
	runner       ir.RunnerReq
	visibility   string
	wantLabel    string
	wantKind     string
	wantFidelity diag.Fidelity
	wantCodes    []string
}

func hostedRunnerCases() []runnerCase {
	return []runnerCase{
		{"ubuntu alias pinned", ir.RunnerReq{Image: "ubuntu-latest"}, "", "ubuntu-24.04", "emitted", diag.FidelityApproximated, []string{aliasPinnedCode}},
		{"ubuntu 26.04 exact", ir.RunnerReq{Image: "ubuntu-26.04"}, "", "ubuntu-26.04", "emitted", diag.FidelityExact, nil},
		{"ubuntu 24.04 exact", ir.RunnerReq{Image: "ubuntu-24.04"}, "", "ubuntu-24.04", "emitted", diag.FidelityExact, nil},
		{"ubuntu 22.04 exact", ir.RunnerReq{Image: "ubuntu-22.04"}, "", "ubuntu-22.04", "emitted", diag.FidelityExact, nil},
		{"windows alias pinned", ir.RunnerReq{Image: "windows-latest"}, "", "windows-2025", "emitted", diag.FidelityApproximated, []string{aliasPinnedCode}},
		{"windows 2025 exact", ir.RunnerReq{Image: "windows-2025"}, "", "windows-2025", "emitted", diag.FidelityExact, nil},
		{"windows 2022 exact", ir.RunnerReq{Image: "windows-2022"}, "", "windows-2022", "emitted", diag.FidelityExact, nil},
		{"vs2026 public", ir.RunnerReq{Image: "windows-2025-vs2026"}, visibilityPublic, "windows-2025-vs2026", "emitted", diag.FidelityApproximated, []string{imageSubstitutedCode}},
		{"vs2026 private substitutes", ir.RunnerReq{Image: "windows-2025-vs2026"}, visibilityPrivate, "windows-2025", "emitted", diag.FidelityApproximated, []string{imageSubstitutedCode}},
		{"vs2026 unknown placeholder", ir.RunnerReq{Image: "windows-2025-vs2026"}, "", "", "placeholder", diag.FidelityNeedsManual, []string{manualConversionCode}},
		{"macos alias public intel", ir.RunnerReq{Image: "macOS-latest"}, visibilityPublic, "macos-15-intel", "emitted", diag.FidelityApproximated, []string{aliasPinnedCode}},
		{"macos alias private substitutes", ir.RunnerReq{Image: "macOS-latest"}, visibilityPrivate, "macos-15", "emitted", diag.FidelityApproximated, []string{aliasPinnedCode, imageSubstitutedCode}},
		{"macos alias unknown placeholder", ir.RunnerReq{Image: "macOS-latest"}, "unknown", "", "placeholder", diag.FidelityNeedsManual, []string{manualConversionCode}},
		{"macos 26 public exact", ir.RunnerReq{Image: "macOS-26"}, visibilityPublic, "macos-26-intel", "emitted", diag.FidelityExact, nil},
		{"macos 26 private substitutes", ir.RunnerReq{Image: "macOS-26"}, visibilityPrivate, "macos-26", "emitted", diag.FidelityApproximated, []string{imageSubstitutedCode}},
		{"macos 26 unknown placeholder", ir.RunnerReq{Image: "macOS-26"}, "", "", "placeholder", diag.FidelityNeedsManual, []string{manualConversionCode}},
		{"macos 15 public exact", ir.RunnerReq{Image: "macOS-15"}, visibilityPublic, "macos-15-intel", "emitted", diag.FidelityExact, nil},
		{"macos 15 private substitutes", ir.RunnerReq{Image: "macOS-15"}, visibilityPrivate, "macos-15", "emitted", diag.FidelityApproximated, []string{imageSubstitutedCode}},
		{"macos 15 unknown placeholder", ir.RunnerReq{Image: "macOS-15"}, "", "", "placeholder", diag.FidelityNeedsManual, []string{manualConversionCode}},
		{"macos 14 silicon substitution", ir.RunnerReq{Image: "macOS-14"}, "", "macos-14", "emitted", diag.FidelityApproximated, []string{imageSubstitutedCode}},
		{"image case-insensitive", ir.RunnerReq{Image: "UBUNTU-LATEST"}, "", "ubuntu-24.04", "emitted", diag.FidelityApproximated, []string{aliasPinnedCode}},
		{"hosted pool name", ir.RunnerReq{Pool: "Azure Pipelines", Image: "ubuntu-22.04"}, "", "ubuntu-22.04", "emitted", diag.FidelityExact, nil},
		{"hosted pool case-insensitive", ir.RunnerReq{Pool: "azure pipelines", Image: "windows-2022"}, "", "windows-2022", "emitted", diag.FidelityExact, nil},
		{"x64 arch allowed", ir.RunnerReq{Image: "ubuntu-22.04", Arch: "x64"}, "", "ubuntu-22.04", "emitted", diag.FidelityExact, nil},
		{"matching os allowed", ir.RunnerReq{Image: "ubuntu-22.04", OS: "linux"}, "", "ubuntu-22.04", "emitted", diag.FidelityExact, nil},
		{"self-hosted pool placeholder", ir.RunnerReq{Pool: "my-pool", Image: "ubuntu-22.04"}, "", "", "placeholder", diag.FidelityNeedsManual, []string{manualConversionCode}},
		{"labels placeholder", ir.RunnerReq{Image: "ubuntu-22.04", Labels: []string{"self-hosted"}}, "", "", "placeholder", diag.FidelityNeedsManual, []string{manualConversionCode}},
		{"demands placeholder", ir.RunnerReq{Image: "ubuntu-22.04", Demands: []string{"Agent.OS -eq Linux"}}, "", "", "placeholder", diag.FidelityNeedsManual, []string{manualConversionCode}},
		{"non-x64 arch placeholder", ir.RunnerReq{Image: "ubuntu-22.04", Arch: "arm64"}, "", "", "placeholder", diag.FidelityNeedsManual, []string{manualConversionCode}},
		{"empty image placeholder", ir.RunnerReq{}, "", "", "placeholder", diag.FidelityNeedsManual, []string{manualConversionCode}},
		{"retired image placeholder", ir.RunnerReq{Image: "vs2017-win2016"}, "", "", "placeholder", diag.FidelityNeedsManual, []string{manualConversionCode}},
		{"disagreeing os placeholder", ir.RunnerReq{Image: "ubuntu-22.04", OS: "windows"}, "", "", "placeholder", diag.FidelityNeedsManual, []string{manualConversionCode}},
	}
}

func TestRunnersMap(t *testing.T) {
	for _, tc := range hostedRunnerCases() {
		t.Run(tc.name, func(t *testing.T) {
			pipeline := newPipeline("ci").withJob("build").build()
			pipeline.Jobs[0].Runner = tc.runner
			out, err := Run(pipeline, runnersMapper{visibility: tc.visibility})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(out.Outcomes) != 1 {
				t.Fatalf("outcomes = %d, want 1", len(out.Outcomes))
			}
			want := Disposition{ID: "job:build", Kind: tc.wantKind, Fidelity: tc.wantFidelity}
			if diff := cmp.Diff(want, out.Outcomes[0].Disposition); diff != "" {
				t.Errorf("disposition mismatch (-want +got):\n%s", diff)
			}
			var codes []string
			for _, finding := range out.Outcomes[0].Diagnostics {
				codes = append(codes, finding.Code)
			}
			if diff := cmp.Diff(tc.wantCodes, codes); diff != "" {
				t.Errorf("diagnostic codes mismatch (-want +got):\n%s", diff)
			}
			literals := append([]string{tc.runner.Image, tc.runner.Pool, tc.runner.OS, tc.runner.Arch}, tc.runner.Labels...)
			literals = append(literals, tc.runner.Demands...)
			for _, finding := range out.Outcomes[0].Diagnostics {
				for key, value := range finding.Params {
					text, ok := value.(string)
					if !ok {
						continue
					}
					for _, literal := range literals {
						// The emitted target label may coincide with the source
						// image name; it is catalog output, not source text.
						if literal != "" && text == literal && text != tc.wantLabel {
							t.Errorf("param %q leaks source value %q (INV-05)", key, literal)
						}
					}
				}
			}
			if tc.wantLabel == "" {
				if len(out.Fragments) != 1 || len(out.Fragments[0].Jobs) != 0 {
					t.Errorf("placeholder job must contribute no fragment: %+v", out.Fragments)
				}
				return
			}
			wantFragment := JobFragment{Node: "job:build", JobID: "build", Job: model.Job{RunsOn: tc.wantLabel}}
			if len(out.Fragments) != 1 || len(out.Fragments[0].Jobs) != 1 {
				t.Fatalf("fragments = %+v, want one job fragment", out.Fragments)
			}
			if diff := cmp.Diff(wantFragment, out.Fragments[0].Jobs[0]); diff != "" {
				t.Errorf("fragment mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRunnersDiagnosticParamsAndAttribution(t *testing.T) {
	pipeline := newPipeline("ci").withJob("build").build()
	pipeline.Jobs[0].Runner = ir.RunnerReq{Image: "ubuntu-latest"}
	pipeline.Jobs[0].Origin = &ir.Origin{Source: diag.Source{Platform: "ado", File: "azure-pipelines.yml", Line: 12}}
	out, err := Run(pipeline, runnersMapper{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	finding := out.Outcomes[0].Diagnostics[0]
	wantParams := map[string]any{"label": "ubuntu-24.04", "date": catalogDate}
	if diff := cmp.Diff(wantParams, finding.Params); diff != "" {
		t.Errorf("params mismatch (-want +got):\n%s", diff)
	}
	if finding.Source == nil || finding.Source.Line != 12 || finding.Source.File != "azure-pipelines.yml" {
		t.Errorf("source = %+v, want the job origin", finding.Source)
	}
	if finding.Resource == nil || finding.Resource.Kind != "job" || finding.Resource.Key != "job:build" {
		t.Errorf("resource = %+v, want the job node", finding.Resource)
	}
	if finding.Severity != diag.SeverityInfo {
		t.Errorf("severity = %s, want the registry info severity", finding.Severity)
	}
}

// TestRunnersKeepsUnrepresentableNameFinding checks section 5.13: the job
// claim replaces the emitter's outcome, so the mapper must keep the
// expression-delimiter finding of an unrepresentable job name itself.
func TestRunnersKeepsUnrepresentableNameFinding(t *testing.T) {
	pipeline := newPipeline("ci").withJob("build").build()
	pipeline.Jobs[0].Name = "job ${{ secrets.X }}"
	pipeline.Jobs[0].Runner = ir.RunnerReq{Image: "ubuntu-latest"}
	out, err := Run(pipeline, runnersMapper{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	codes := make([]string, 0, len(out.Outcomes[0].Diagnostics))
	for _, finding := range out.Outcomes[0].Diagnostics {
		codes = append(codes, finding.Code)
	}
	if diff := cmp.Diff([]string{aliasPinnedCode, expressionDelimiterCode}, codes); diff != "" {
		t.Errorf("diagnostic codes mismatch (-want +got):\n%s", diff)
	}
}

func TestRunnersSkipsClaimedJobs(t *testing.T) {
	pipeline := newPipeline("ci").withJob("build").build()
	pipeline.Jobs[0].Runner = ir.RunnerReq{Image: "ubuntu-22.04"}
	claimed := func(string) bool { return true }
	result := runnersMapper{}.Map(pipeline, claimed)
	if len(result.Outcomes) != 0 || len(result.Fragment.Jobs) != 0 {
		t.Errorf("claimed job was mapped again: %+v", result)
	}
}

func TestRunnersKeepJobOrder(t *testing.T) {
	pipeline := newPipeline("ci").withJob("first").withJob("second").build()
	pipeline.Jobs[0].Runner = ir.RunnerReq{Image: "ubuntu-22.04"}
	pipeline.Jobs[1].Runner = ir.RunnerReq{Pool: "my-pool"}
	out, err := Run(pipeline, runnersMapper{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	ids := make([]string, 0, len(out.Outcomes))
	for _, outcome := range out.Outcomes {
		ids = append(ids, outcome.Disposition.ID)
	}
	if diff := cmp.Diff([]string{"job:first", "job:second"}, ids); diff != "" {
		t.Errorf("outcome order mismatch (-want +got):\n%s", diff)
	}
	if len(out.Fragments[0].Jobs) != 1 || out.Fragments[0].Jobs[0].JobID != "first" {
		t.Errorf("fragments = %+v, want only the mapped job", out.Fragments)
	}
}

// TestDispatchDefaultsToUnknownVisibility checks the registered mapper: with
// no visibility supplied, a public-only image stays a placeholder (INV-08,
// emit-gha.md 5.5 rule 4).
func TestDispatchDefaultsToUnknownVisibility(t *testing.T) {
	pipeline := newPipeline("ci").withJob("build").build()
	pipeline.Jobs[0].Runner = ir.RunnerReq{Image: "windows-2025-vs2026"}
	out, err := Dispatch(pipeline)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	want := Disposition{ID: "job:build", Kind: "placeholder", Fidelity: diag.FidelityNeedsManual}
	if len(out.Outcomes) != 1 {
		t.Fatalf("outcomes = %d, want 1", len(out.Outcomes))
	}
	if diff := cmp.Diff(want, out.Outcomes[0].Disposition); diff != "" {
		t.Errorf("disposition mismatch (-want +got):\n%s", diff)
	}
}
