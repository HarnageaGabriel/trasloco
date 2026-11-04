package emit

import (
	"fmt"
	"sort"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

// checkConservation enforces INV-12 over one emission result: every required
// node and feature ID appears in exactly one disposition, every output
// element is recorded, every emitted disposition produces at least one output
// element, and a no-file result (zero files) carries no emitted disposition
// (docs/spec/emit-gha.md 2.3). A violation is an internal error; it is a
// defect of the emitter, never input.
func checkConservation(requirements []requirement, dispositions []Disposition, elements []OutputElement, fileCount int) error {
	required := make(map[string]int, len(requirements))
	for _, item := range requirements {
		required[item.ID]++
	}
	counted := make(map[string]int, len(requirements))
	for _, disposition := range dispositions {
		counted[disposition.ID]++
	}
	for _, id := range sortedCountKeys(required) {
		if required[id] > 1 {
			return conservationViolation("required node or feature ID %q occurs %d times", id, required[id])
		}
		if counted[id] != 1 {
			return conservationViolation("required node or feature ID %q has %d dispositions, want exactly 1", id, counted[id])
		}
	}
	for _, id := range sortedCountKeys(counted) {
		if required[id] == 0 {
			return conservationViolation("disposition %q does not belong to any required node or feature", id)
		}
	}
	elementCount := make(map[string]int, len(dispositions))
	seenPaths := make(map[string]bool, len(elements))
	for _, element := range elements {
		if seenPaths[element.Path] {
			return conservationViolation("output element path %q is recorded more than once", element.Path)
		}
		seenPaths[element.Path] = true
		if counted[element.NodeID] == 0 {
			return conservationViolation("output element for %q has no disposition", element.NodeID)
		}
		elementCount[element.NodeID]++
	}
	for _, disposition := range dispositions {
		if disposition.Kind == DispositionEmitted && elementCount[disposition.ID] == 0 {
			return conservationViolation("emitted disposition %q produced no output element", disposition.ID)
		}
	}
	if fileCount == 0 {
		for _, disposition := range dispositions {
			if disposition.Kind == DispositionEmitted {
				return conservationViolation("a no-file result keeps the emitted disposition %q", disposition.ID)
			}
		}
		if len(elements) > 0 {
			return conservationViolation("a no-file result keeps %d output elements", len(elements))
		}
	}
	return nil
}

// sortedCountKeys returns the keys of one count map in byte order, so that a
// conservation violation is reported deterministically.
func sortedCountKeys(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for key := range counts { //trasloco:unordered-ok the keys are sorted before use
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// conservationViolation builds the internal error of one INV-12 violation.
// Messages contain only stable identifiers, never source text (INV-05).
func conservationViolation(format string, args ...any) error {
	finding := diag.MustNew(internalEmitterCode,
		diag.WithMessage("emitter conservation violated: "+fmt.Sprintf(format, args...)),
		diag.WithResource(diag.Resource{Kind: "emit", Key: "conservation"}),
	)
	return &diag.Error{Finding: finding}
}
