package yamlx

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"go.yaml.in/yaml/v3"
)

// ByteRange is a zero-based, half-open byte range in the original file.
type ByteRange struct {
	Start int
	End   int
}

// Patch replaces [Start, End) in the original bytes.
type Patch struct {
	Start       int
	End         int
	Replacement []byte
}

// ApplyPatches applies non-overlapping patches to original. Inputs are not mutated.
func ApplyPatches(original []byte, patches []Patch) ([]byte, error) {
	ordered := append([]Patch(nil), patches...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Start != ordered[j].Start {
			return ordered[i].Start < ordered[j].Start
		}
		return ordered[i].End < ordered[j].End
	})
	previousEnd := 0
	for index, patch := range ordered {
		if patch.Start < 0 || patch.End < patch.Start || patch.End > len(original) {
			return nil, fmt.Errorf("yamlx: patch %d range [%d,%d) outside [0,%d)", index, patch.Start, patch.End, len(original))
		}
		if index > 0 && patch.Start < previousEnd {
			return nil, fmt.Errorf("yamlx: patch %d range [%d,%d) overlaps previous patch ending at %d", index, patch.Start, patch.End, previousEnd)
		}
		if index > 0 && patch.Start == previousEnd && patch.Start == patch.End && ordered[index-1].Start == ordered[index-1].End {
			return nil, fmt.Errorf("yamlx: patch %d conflicts with another insertion at %d", index, patch.Start)
		}
		previousEnd = patch.End
	}
	capacity := len(original)
	for _, patch := range ordered {
		capacity += len(patch.Replacement) - (patch.End - patch.Start)
	}
	result := make([]byte, 0, capacity)
	cursor := 0
	for _, patch := range ordered {
		result = append(result, original[cursor:patch.Start]...)
		result = append(result, patch.Replacement...)
		cursor = patch.End
	}
	result = append(result, original[cursor:]...)
	return result, nil
}

// ApplyPatches applies patches to the exact bytes supplied to Parse.
func (d *Document) ApplyPatches(patches []Patch) ([]byte, error) {
	if d == nil || d.ctx == nil {
		return nil, errors.New("yamlx: document has no original bytes")
	}
	return ApplyPatches(d.ctx.original, patches)
}

// ByteRange returns the exact lexical range of a node in the original file.
// Block scalars include their final newline and trailing blank lines. A
// replacement must supply any line endings required before the following node.
func (n *Node) ByteRange() (ByteRange, error) {
	if n == nil || n.raw == nil || n.ctx == nil {
		return ByteRange{}, errors.New("yamlx: node has no source range")
	}
	return rawNodeRange(n.ctx, n.raw)
}

// KeyRange returns the mapping key's lexical range.
func (p Pair) KeyRange() (ByteRange, error) {
	return p.Key.ByteRange()
}

// ValueRange returns the mapping value's lexical range.
func (p Pair) ValueRange() (ByteRange, error) {
	return p.Value.ByteRange()
}

// EntryRange returns the mapping entry from the key start through the value end.
func (p Pair) EntryRange() (ByteRange, error) {
	keyRange, err := p.KeyRange()
	if err != nil {
		return ByteRange{}, err
	}
	valueRange, err := p.ValueRange()
	if err != nil {
		return ByteRange{}, err
	}
	if keyRange.Start > valueRange.End {
		return ByteRange{}, errors.New("yamlx: invalid mapping entry range")
	}
	return ByteRange{Start: keyRange.Start, End: valueRange.End}, nil
}

func rawNodeRange(ctx *nodeContext, node *yaml.Node) (ByteRange, error) {
	if ctx == nil || node == nil {
		return ByteRange{}, errors.New("yamlx: node has no source range")
	}
	start := ctx.positionOffset(node.Line, node.Column)
	end := start
	switch node.Kind {
	case yaml.ScalarNode:
		start, end = scalarRange(ctx, node, start)
	case yaml.AliasNode:
		end = scanTokenEnd(ctx.data, start)
	case yaml.SequenceNode, yaml.MappingNode:
		if node.Style&yaml.FlowStyle != 0 {
			end = scanFlowEnd(ctx.data, start)
		} else {
			for _, child := range node.Content {
				childRange, err := rawNodeRange(ctx, child)
				if err == nil && childRange.End-ctx.baseOffset > end {
					end = childRange.End - ctx.baseOffset
				}
			}
		}
	case yaml.DocumentNode:
		for _, child := range node.Content {
			childRange, err := rawNodeRange(ctx, child)
			if err == nil && childRange.End-ctx.baseOffset > end {
				end = childRange.End - ctx.baseOffset
			}
		}
	default:
		return ByteRange{}, fmt.Errorf("yamlx: unsupported node kind %d", node.Kind)
	}
	return ByteRange{Start: start + ctx.baseOffset, End: end + ctx.baseOffset}, nil
}

func scalarRange(ctx *nodeContext, node *yaml.Node, start int) (int, int) {
	data, style := ctx.data, node.Style
	limit := ctx.nextOffset(start)
	start = skipNodeProperties(data, start)
	if start >= len(data) {
		return start, start
	}
	switch {
	case style&yaml.SingleQuotedStyle != 0:
		return start, scanQuotedEnd(data, start, '\'')
	case style&yaml.DoubleQuotedStyle != 0:
		return start, scanQuotedEnd(data, start, '"')
	case style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
		return start, scanBlockScalarEnd(data, start)
	default:
		return start, scanPlainEnd(data, start, limit, ctx.nodeContexts[node])
	}
}

func skipNodeProperties(data []byte, start int) int {
	for start < len(data) {
		for start < len(data) && (data[start] == ' ' || data[start] == '\t') {
			start++
		}
		if start >= len(data) || (data[start] != '&' && data[start] != '!') {
			return start
		}
		start = scanTokenEnd(data, start)
	}
	return start
}

func scanTokenEnd(data []byte, start int) int {
	end := start
	for end < len(data) && !bytes.ContainsRune([]byte(" \t\r\n,[]{}"), rune(data[end])) {
		end++
	}
	return end
}

func scanQuotedEnd(data []byte, start int, quote byte) int {
	for index := start + 1; index < len(data); index++ {
		if quote == '"' && data[index] == '\\' {
			index++
			continue
		}
		if data[index] == quote {
			if quote == '\'' && index+1 < len(data) && data[index+1] == '\'' {
				index++
				continue
			}
			return index + 1
		}
	}
	return len(data)
}

func scanPlainEnd(data []byte, start, limit int, context lexicalContext) int {
	end := start
	parentIndent := lineIndent(data, start)
	for cursor := start; cursor < limit; cursor++ {
		if data[cursor] == '\r' || data[cursor] == '\n' {
			next := consumeLineEnding(data, cursor)
			for next < limit && (data[next] == ' ' || data[next] == '\t' || data[next] == '\r' || data[next] == '\n') {
				next++
			}
			if next >= limit || data[next] == '#' || (!context.flow && !context.root && lineIndent(data, next) <= parentIndent) {
				break
			}
			if !context.flow && lineIndent(data, next) == 0 && isDocumentBoundary(data[next:]) {
				break
			}
			cursor = next
		}
		if data[cursor] == '#' && (cursor == start || data[cursor-1] == ' ' || data[cursor-1] == '\t') {
			break
		}
		if data[cursor] == ':' && (cursor+1 >= len(data) || bytes.ContainsRune([]byte(" \t\r\n"), rune(data[cursor+1]))) {
			break
		}
		if context.flow && bytes.ContainsRune([]byte(",[]{}"), rune(data[cursor])) {
			break
		}
		if data[cursor] != ' ' && data[cursor] != '\t' {
			end = cursor + 1
		}
	}
	return end
}

func isDocumentBoundary(data []byte) bool {
	return (bytes.HasPrefix(data, []byte("---")) || bytes.HasPrefix(data, []byte("..."))) &&
		(len(data) == 3 || bytes.ContainsRune([]byte(" \t\r\n"), rune(data[3])))
}

func scanBlockScalarEnd(data []byte, start int) int {
	headerEnd := lineEnd(data, start)
	parentIndent := lineIndent(data, start)
	end := consumeLineEnding(data, headerEnd)
	cursor := end
	for cursor < len(data) {
		contentEnd := lineEnd(data, cursor)
		indent := lineIndent(data, cursor)
		blank := len(bytes.TrimSpace(data[cursor:contentEnd])) == 0
		if !blank && indent <= parentIndent {
			break
		}
		end = consumeLineEnding(data, contentEnd)
		cursor = end
	}
	return end
}

func scanFlowEnd(data []byte, start int) int {
	if start >= len(data) || (data[start] != '[' && data[start] != '{') {
		return start
	}
	stack := []byte{data[start]}
	for index := start + 1; index < len(data); index++ {
		switch data[index] {
		case '#':
			if index > start && bytes.ContainsRune([]byte(" \t\r\n"), rune(data[index-1])) {
				index = lineEnd(data, index) - 1
			}
		case '\'', '"':
			index = scanQuotedEnd(data, index, data[index]) - 1
		case '[', '{':
			stack = append(stack, data[index])
		case ']', '}':
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return index + 1
			}
		}
	}
	return len(data)
}

func lineEnd(data []byte, offset int) int {
	if newline := bytes.IndexByte(data[offset:], '\n'); newline >= 0 {
		end := offset + newline
		if end > offset && data[end-1] == '\r' {
			end--
		}
		return end
	}
	return len(data)
}

func consumeLineEnding(data []byte, end int) int {
	if end < len(data) && data[end] == '\r' {
		end++
	}
	if end < len(data) && data[end] == '\n' {
		end++
	}
	return end
}

func lineIndent(data []byte, offset int) int {
	for offset > 0 && data[offset-1] != '\n' {
		offset--
	}
	indent := 0
	for offset+indent < len(data) && data[offset+indent] == ' ' {
		indent++
	}
	return indent
}
