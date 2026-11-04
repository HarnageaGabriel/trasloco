package emit

import (
	"errors"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

// conservationFixture is a valid INV-12 result: one emitted pipeline node with
// an output element and one placeholder job without one.
func conservationFixture() ([]requirement, []Disposition, []OutputElement) {
	requirements := []requirement{
		{ID: "job:j", Kind: "job"},
		{ID: "pipeline:ci", Kind: "pipeline"},
	}
	dispositions := []Disposition{
		{ID: "job:j", Kind: DispositionPlaceholder},
		{ID: "pipeline:ci", Kind: DispositionEmitted},
	}
	elements := []OutputElement{{Path: "ci.yml#/jobs", NodeID: "pipeline:ci"}}
	return requirements, dispositions, elements
}

// TestCheckConservationAcceptsValidResults checks the positive cases,
// including a no-file result that carries no emitted disposition.
func TestCheckConservationAcceptsValidResults(t *testing.T) {
	requirements, dispositions, elements := conservationFixture()
	if err := checkConservation(requirements, dispositions, elements, 1); err != nil {
		t.Errorf("valid result rejected: %v", err)
	}
	noFile := []Disposition{
		{ID: "job:j", Kind: DispositionPlaceholder},
		{ID: "pipeline:ci", Kind: DispositionDropped},
	}
	if err := checkConservation(requirements, noFile, nil, 0); err != nil {
		t.Errorf("valid no-file result rejected: %v", err)
	}
}

// TestCheckConservationDetectsViolations injects each INV-12 violation and
// asserts it is reported as an internal error, never dropped or panicked.
func TestCheckConservationDetectsViolations(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		requirements  []requirement
		dispositions  []Disposition
		elements      []OutputElement
		fileCount     int
		wantSubstring string
	}{
		{
			name:          "duplicate requirement",
			requirements:  []requirement{{ID: "pipeline:ci"}, {ID: "pipeline:ci"}},
			dispositions:  []Disposition{{ID: "pipeline:ci", Kind: DispositionPlaceholder}},
			wantSubstring: "occurs 2 times",
		},
		{
			name:          "missing disposition",
			requirements:  []requirement{{ID: "pipeline:ci"}, {ID: "job:j"}},
			dispositions:  []Disposition{{ID: "pipeline:ci", Kind: DispositionDropped}},
			wantSubstring: "has 0 dispositions",
		},
		{
			name:          "duplicate disposition",
			requirements:  []requirement{{ID: "pipeline:ci"}},
			dispositions:  []Disposition{{ID: "pipeline:ci", Kind: DispositionDropped}, {ID: "pipeline:ci", Kind: DispositionDropped}},
			wantSubstring: "has 2 dispositions",
		},
		{
			name:          "unknown disposition",
			requirements:  []requirement{{ID: "pipeline:ci"}},
			dispositions:  []Disposition{{ID: "pipeline:ci", Kind: DispositionDropped}, {ID: "job:ghost", Kind: DispositionDropped}},
			wantSubstring: "does not belong",
		},
		{
			name:          "element without disposition",
			requirements:  []requirement{{ID: "pipeline:ci"}},
			dispositions:  []Disposition{{ID: "pipeline:ci", Kind: DispositionDropped}},
			elements:      []OutputElement{{Path: "ci.yml#/jobs", NodeID: "job:ghost"}},
			wantSubstring: "has no disposition",
		},
		{
			name:          "emitted without element",
			requirements:  []requirement{{ID: "pipeline:ci"}},
			dispositions:  []Disposition{{ID: "pipeline:ci", Kind: DispositionEmitted}},
			wantSubstring: "produced no output element",
		},
		{
			name:          "no-file result keeps emitted",
			requirements:  []requirement{{ID: "pipeline:ci"}},
			dispositions:  []Disposition{{ID: "pipeline:ci", Kind: DispositionEmitted}},
			elements:      []OutputElement{{Path: "ci.yml#/jobs", NodeID: "pipeline:ci"}},
			wantSubstring: "keeps the emitted disposition",
		},
		{
			name:          "no-file result keeps elements",
			requirements:  []requirement{{ID: "pipeline:ci"}},
			dispositions:  []Disposition{{ID: "pipeline:ci", Kind: DispositionDropped}},
			elements:      []OutputElement{{Path: "ci.yml#/jobs", NodeID: "pipeline:ci"}},
			wantSubstring: "keeps 1 output elements",
		},
		{
			name:          "duplicate element path",
			requirements:  []requirement{{ID: "pipeline:ci"}},
			dispositions:  []Disposition{{ID: "pipeline:ci", Kind: DispositionEmitted}},
			elements:      []OutputElement{{Path: "ci.yml#/jobs", NodeID: "pipeline:ci"}, {Path: "ci.yml#/jobs", NodeID: "pipeline:ci"}},
			fileCount:     1,
			wantSubstring: "recorded more than once",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := checkConservation(testCase.requirements, testCase.dispositions, testCase.elements, testCase.fileCount)
			if err == nil {
				t.Fatal("violation was not detected")
			}
			var finding *diag.Error
			if !errors.As(err, &finding) {
				t.Fatalf("error is not a *diag.Error: %v", err)
			}
			if finding.Finding.Code != internalEmitterCode {
				t.Errorf("code = %s, want %s", finding.Finding.Code, internalEmitterCode)
			}
			if finding.Finding.Severity != diag.SeverityError {
				t.Errorf("severity = %s, want error", finding.Finding.Severity)
			}
			if !strings.Contains(finding.Finding.Message, testCase.wantSubstring) {
				t.Errorf("message %q does not contain %q", finding.Finding.Message, testCase.wantSubstring)
			}
		})
	}
}
