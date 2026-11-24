package mapping

import (
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/githubactions/emit/model"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

func init() { Register(runnersMapper{}) }

// Runner mapping codes (emit-gha.md sections 5.5, 5.13, 6).
const (
	aliasPinnedCode         = "TRL-CAT-0006"
	imageSubstitutedCode    = "TRL-CONV-0007"
	manualConversionCode    = "TRL-CONV-0003"
	expressionDelimiterCode = "TRL-EMIT-0006"
)

// catalogDate is the dated snapshot of the hosted image table (emit-gha.md 5.5).
const catalogDate = "2026-10-11"

// Target visibility values, spelled as emit.TargetVisibility. The mapping
// package sits below emit and cannot import it; any other value (including the
// empty string) means unknown and never unlocks a public-only label.
const (
	visibilityPublic  = "public"
	visibilityPrivate = "private"
)

// runnersMapper maps Job.runner to runs-on (emit-gha.md section 5.5). The
// registered zero value targets unknown visibility, so Dispatch never emits a
// public-only label; callers that know the visibility inject a configured
// mapper into Run.
type runnersMapper struct {
	visibility string
}

func (runnersMapper) Concern() Concern { return Runners }

func (m runnersMapper) Map(pipeline *ir.Pipeline, claimed func(string) bool) Result {
	result := Result{Outcomes: make([]Outcome, 0, len(pipeline.Jobs))}
	for _, job := range pipeline.Jobs {
		id := QualifyID("job", job.ID)
		if claimed(id) {
			continue
		}
		label, ok := m.mapRunner(job.Runner)
		var outcome Outcome
		if !ok {
			// The runner itself is the problem: no fragment, so the emitter
			// writes the job placeholder on ubuntu-24.04 (section 2.5).
			outcome = Outcome{
				Disposition: Disposition{ID: id, Kind: "placeholder", Fidelity: diag.FidelityNeedsManual},
				Diagnostics: []diag.Diagnostic{runnerFinding(job, manualConversionCode, nil)},
			}
		} else {
			outcome = Outcome{Disposition: Disposition{ID: id, Kind: "emitted", Fidelity: label.fidelity}}
			for _, code := range label.codes {
				params := map[string]any{"label": label.label}
				if code == aliasPinnedCode {
					params["date"] = catalogDate
				}
				outcome.Diagnostics = append(outcome.Diagnostics, runnerFinding(job, code, params))
			}
			result.Fragment.Jobs = append(result.Fragment.Jobs, JobFragment{
				Node: id, JobID: job.ID, Job: model.Job{RunsOn: label.label},
			})
		}
		if strings.Contains(job.Name, "${{") {
			// The claim replaces the emitter's job outcome, so it must carry
			// the section 5.13 name finding itself; the emitter omits the name.
			outcome.Diagnostics = append(outcome.Diagnostics, runnerFinding(job, expressionDelimiterCode, map[string]any{"field": "name"}))
		}
		result.Outcomes = append(result.Outcomes, outcome)
	}
	return result
}

// mapRunner applies the eligibility rules of emit-gha.md 5.5 and selects the
// visibility-specific label. False means the job becomes a placeholder.
func (m runnersMapper) mapRunner(req ir.RunnerReq) (runnerLabel, bool) {
	if req.Pool != "" && !strings.EqualFold(req.Pool, "Azure Pipelines") {
		return runnerLabel{}, false
	}
	if len(req.Labels) > 0 || len(req.Demands) > 0 || (req.Arch != "" && req.Arch != "x64") {
		return runnerLabel{}, false
	}
	rule, ok := runnerTable[strings.ToLower(req.Image)]
	if !ok {
		return runnerLabel{}, false
	}
	if req.OS != "" && !strings.EqualFold(req.OS, rule.os) {
		return runnerLabel{}, false
	}
	switch m.visibility {
	case visibilityPublic:
		return rule.public, true
	case visibilityPrivate:
		return rule.private, true
	default:
		if rule.gated {
			return runnerLabel{}, false
		}
		return rule.public, true
	}
}

// runnerLabel is one visibility-specific outcome of an image table row.
type runnerLabel struct {
	label    string
	fidelity diag.Fidelity
	codes    []string
}

// runnerRule is one row of the hosted image table (emit-gha.md 5.5).
type runnerRule struct {
	os      string
	public  runnerLabel
	private runnerLabel
	// gated marks a public-only image: unknown visibility placeholders the job.
	gated bool
}

// anyVisibility builds a row whose label does not depend on visibility.
func anyVisibility(os, label string, fidelity diag.Fidelity, codes ...string) runnerRule {
	outcome := runnerLabel{label: label, fidelity: fidelity, codes: codes}
	return runnerRule{os: os, public: outcome, private: outcome}
}

// runnerTable is the dated catalog snapshot of emit-gha.md 5.5 (AZ-HOST,
// GH-RUN), keyed by lower-case Azure vmImage. Read-only after declaration;
// lookups only, never iterated (INV-01).
var runnerTable = map[string]runnerRule{
	"ubuntu-latest":  anyVisibility("linux", "ubuntu-24.04", diag.FidelityApproximated, aliasPinnedCode),
	"ubuntu-26.04":   anyVisibility("linux", "ubuntu-26.04", diag.FidelityExact),
	"ubuntu-24.04":   anyVisibility("linux", "ubuntu-24.04", diag.FidelityExact),
	"ubuntu-22.04":   anyVisibility("linux", "ubuntu-22.04", diag.FidelityExact),
	"windows-latest": anyVisibility("windows", "windows-2025", diag.FidelityApproximated, aliasPinnedCode),
	"windows-2025":   anyVisibility("windows", "windows-2025", diag.FidelityExact),
	"windows-2022":   anyVisibility("windows", "windows-2022", diag.FidelityExact),
	"windows-2025-vs2026": {
		os:      "windows",
		gated:   true,
		public:  runnerLabel{label: "windows-2025-vs2026", fidelity: diag.FidelityApproximated, codes: []string{imageSubstitutedCode}},
		private: runnerLabel{label: "windows-2025", fidelity: diag.FidelityApproximated, codes: []string{imageSubstitutedCode}},
	},
	"macos-latest": {
		os:      "macos",
		gated:   true,
		public:  runnerLabel{label: "macos-15-intel", fidelity: diag.FidelityApproximated, codes: []string{aliasPinnedCode}},
		private: runnerLabel{label: "macos-15", fidelity: diag.FidelityApproximated, codes: []string{aliasPinnedCode, imageSubstitutedCode}},
	},
	"macos-26": {
		os:      "macos",
		gated:   true,
		public:  runnerLabel{label: "macos-26-intel", fidelity: diag.FidelityExact},
		private: runnerLabel{label: "macos-26", fidelity: diag.FidelityApproximated, codes: []string{imageSubstitutedCode}},
	},
	"macos-15": {
		os:      "macos",
		gated:   true,
		public:  runnerLabel{label: "macos-15-intel", fidelity: diag.FidelityExact},
		private: runnerLabel{label: "macos-15", fidelity: diag.FidelityApproximated, codes: []string{imageSubstitutedCode}},
	},
	"macos-14": anyVisibility("macos", "macos-14", diag.FidelityApproximated, imageSubstitutedCode),
}

// runnerFinding builds a registered finding attributed to the job node and its
// origin (ADR 0003), carrying identifiers only (INV-04, INV-05).
func runnerFinding(job ir.Job, code string, params map[string]any) diag.Diagnostic {
	opts := []diag.Option{diag.WithResource(diag.Resource{Kind: "job", Key: QualifyID("job", job.ID)})}
	if job.Origin != nil {
		opts = append(opts, diag.WithSource(job.Origin.Source))
	}
	if params != nil {
		opts = append(opts, diag.WithParams(params))
	}
	return diag.MustNew(code, opts...)
}
