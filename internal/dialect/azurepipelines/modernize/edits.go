package modernize

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
)

// ApplyEdits applies a modernization edit plan in original byte coordinates.
// E8 permits multiple insertions at one offset, in program order. Coalesce
// those before using yamlx's stricter patch applicator. The caller must have
// already coalesced E4 deletion/append boundaries and must perform S8 before
// writing the result. This function does not authorize edits or perform I/O.
// Neither the original bytes nor the plan (including replacements) is mutated.
func ApplyEdits(original []byte, edits []yamlx.Patch) ([]byte, error) {
	ordered := append([]yamlx.Patch(nil), edits...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Start != ordered[j].Start {
			return ordered[i].Start < ordered[j].Start
		}
		return ordered[i].End < ordered[j].End
	})
	coalesced := make([]yamlx.Patch, 0, len(ordered))
	for _, edit := range ordered {
		if len(coalesced) > 0 && edit.Start == edit.End {
			previous := &coalesced[len(coalesced)-1]
			if previous.Start == edit.Start && previous.End == edit.End {
				previous.Replacement = append(previous.Replacement, edit.Replacement...)
				continue
			}
		}
		edit.Replacement = bytes.Clone(edit.Replacement)
		coalesced = append(coalesced, edit)
	}
	result, err := yamlx.ApplyPatches(original, coalesced)
	if err != nil {
		return nil, fmt.Errorf("apply modernization edits: %w", err)
	}
	return result, nil
}
