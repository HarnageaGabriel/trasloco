package modernize

import (
	"sort"
	"unicode/utf8"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
)

// remapDiagnostics rewrites the sources of ds, which refer to src, so that they
// refer to out, the result of applying edits to src. Every position a rewrite
// reports therefore describes the file as it is after the rewrite.
func remapDiagnostics(ds []diag.Diagnostic, src, out []byte, edits []yamlx.Patch) {
	if len(edits) == 0 {
		return
	}
	ordered := append([]yamlx.Patch(nil), edits...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Start != ordered[j].Start {
			return ordered[i].Start < ordered[j].Start
		}
		return ordered[i].End < ordered[j].End
	})
	starts := lineStarts(src)
	newStarts := lineStarts(out)
	for i := range ds {
		if ds[i].Source == nil || ds[i].Source.Line < 1 {
			continue
		}
		source := *ds[i].Source
		start := mapOffset(ordered, offsetOf(src, starts, source.Line, source.Col))
		source.Line, source.Col = positionOf(out, newStarts, start)
		if source.Offset > 0 || source.EndOffset > 0 {
			source.Offset = start
		}
		if source.EndOffset > 0 {
			end := mapOffset(ordered, source.EndOffset)
			source.EndOffset = end
			source.EndLine, source.EndCol = positionOf(out, newStarts, end)
		}
		ds[i].Source = &source
	}
}

// mapOffset moves an offset of the original through the sorted edits. An
// offset inside a replaced range maps to the start of the replacement, and a
// pure insertion at the offset itself does not move it.
func mapOffset(edits []yamlx.Patch, offset int) int {
	shift := 0
	for _, e := range edits {
		switch {
		case e.End <= offset && e.Start < e.End:
			shift += len(e.Replacement) - (e.End - e.Start)
		case e.Start < offset && offset < e.End:
			return e.Start + shift
		case e.Start == e.End && e.Start < offset:
			shift += len(e.Replacement)
		}
	}
	return offset + shift
}

func lineStarts(data []byte) []int {
	starts := []int{0}
	for i, b := range data {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// offsetOf converts a one-based line and rune column to a byte offset.
func offsetOf(data []byte, starts []int, line, column int) int {
	if line > len(starts) {
		return len(data)
	}
	offset := starts[line-1]
	for current := 1; current < column && offset < len(data) && data[offset] != '\n'; current++ {
		_, size := utf8.DecodeRune(data[offset:])
		offset += size
	}
	return offset
}

// positionOf converts a byte offset to a one-based line and rune column.
func positionOf(data []byte, starts []int, offset int) (line, column int) {
	offset = min(max(offset, 0), len(data))
	line = sort.Search(len(starts), func(i int) bool { return starts[i] > offset })
	return line, utf8.RuneCount(data[starts[line-1]:offset]) + 1
}
