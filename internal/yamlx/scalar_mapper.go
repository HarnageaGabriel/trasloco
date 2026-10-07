package yamlx

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"go.yaml.in/yaml/v3"
)

// ScalarMapper maps byte offsets in a decoded YAML scalar back to the file.
type ScalarMapper struct {
	ctx     *nodeContext
	value   string
	offsets []int
}

// NewScalarMapper constructs a mapper for a scalar node.
func NewScalarMapper(node *Node) (*ScalarMapper, error) {
	if node == nil || node.raw == nil || node.ctx == nil {
		return nil, errors.New("yamlx: scalar mapper requires a parsed node")
	}
	resolved, diagnostic := node.resolved()
	if diagnostic != nil {
		return nil, fmt.Errorf("yamlx: scalar mapper cannot resolve alias: %s", diagnostic.Message)
	}
	if resolved == nil || resolved.Kind != yaml.ScalarNode {
		return nil, errors.New("yamlx: scalar mapper requires a scalar node")
	}
	span, err := rawNodeRange(node.ctx, resolved)
	if err != nil {
		return nil, err
	}
	start, end := span.Start-node.ctx.baseOffset, span.End-node.ctx.baseOffset
	candidate, positions := scalarCandidates(node.ctx.data, start, end, resolved.Style)
	for index := range positions {
		positions[index] += node.ctx.baseOffset
	}
	globalEnd := end + node.ctx.baseOffset
	offsets := alignScalar([]byte(resolved.Value), candidate, positions, globalEnd)
	return &ScalarMapper{ctx: node.ctx, value: resolved.Value, offsets: offsets}, nil
}

// ScalarMapper constructs a mapper for n.
func (n *Node) ScalarMapper() (*ScalarMapper, error) {
	return NewScalarMapper(n)
}

// Map returns the file position for a decoded-value byte offset.
func (m *ScalarMapper) Map(offset int) (diag.Source, error) {
	if m == nil || m.ctx == nil {
		return diag.Source{}, errors.New("yamlx: nil scalar mapper")
	}
	if offset < 0 || offset > len(m.value) {
		return diag.Source{}, fmt.Errorf("yamlx: scalar offset %d outside [0,%d]", offset, len(m.value))
	}
	fileOffset := m.offsets[offset]
	line, col := m.ctx.offsetPosition(fileOffset - m.ctx.baseOffset)
	return diag.Source{
		File: m.ctx.name, Line: line, Col: col,
		EndLine: line, EndCol: col, Offset: fileOffset, EndOffset: fileOffset,
	}, nil
}

// MapRange maps a half-open decoded-value byte range to a file source span.
func (m *ScalarMapper) MapRange(start, end int) (diag.Source, error) {
	if start < 0 || end < start || m == nil || end > len(m.value) {
		return diag.Source{}, fmt.Errorf("yamlx: scalar range [%d,%d) is invalid", start, end)
	}
	from, err := m.Map(start)
	if err != nil {
		return diag.Source{}, err
	}
	to, err := m.Map(end)
	if err != nil {
		return diag.Source{}, err
	}
	from.EndLine, from.EndCol, from.EndOffset = to.Line, to.Col, to.Offset
	return from, nil
}

// MapScalarOffset maps one decoded-value byte offset without retaining a mapper.
func MapScalarOffset(node *Node, offset int) (diag.Source, error) {
	mapper, err := NewScalarMapper(node)
	if err != nil {
		return diag.Source{}, err
	}
	return mapper.Map(offset)
}

func scalarCandidates(data []byte, start, end int, style yaml.Style) ([]byte, []int) {
	switch {
	case style&yaml.SingleQuotedStyle != 0:
		contentStart, contentEnd := start+1, max(start+1, end-1)
		return quotedCandidates(data, contentStart, contentEnd, '\'', start)
	case style&yaml.DoubleQuotedStyle != 0:
		contentStart, contentEnd := start+1, max(start+1, end-1)
		return quotedCandidates(data, contentStart, contentEnd, '"', start)
	case style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
		return blockCandidates(data, start, end, style)
	default:
		return plainCandidates(data, start, end)
	}
}

func quotedCandidates(data []byte, start, end int, quote byte, fallback int) ([]byte, []int) {
	var output []byte
	var positions []int
	for index := start; index < end; {
		if quote == '\'' && data[index] == '\'' && index+1 < end && data[index+1] == '\'' {
			output, positions = appendMapped(output, positions, []byte{'\''}, index)
			index += 2
			continue
		}
		if quote == '"' && data[index] == '\\' && index+1 < end {
			if data[index+1] == '\r' || data[index+1] == '\n' {
				index = consumeLineEnding(data, lineEnd(data, index+1))
				for index < end && (data[index] == ' ' || data[index] == '\t') {
					index++
				}
				continue
			}
			decoded, consumed := decodeDoubleEscape(data[index:end])
			output, positions = appendMapped(output, positions, decoded, index)
			index += consumed
			continue
		}
		if data[index] == '\r' || data[index] == '\n' {
			breakStart := index
			index = consumeLineEnding(data, lineEnd(data, index))
			for index < end && (data[index] == ' ' || data[index] == '\t') {
				index++
			}
			output, positions = appendMapped(output, positions, []byte{' '}, breakStart)
			continue
		}
		_, size := utf8.DecodeRune(data[index:end])
		if size <= 0 {
			size = 1
		}
		for byteIndex := 0; byteIndex < size; byteIndex++ {
			output = append(output, data[index+byteIndex])
			positions = append(positions, index+byteIndex)
		}
		index += size
	}
	if len(positions) == 0 && len(output) > 0 {
		positions = make([]int, len(output))
		for index := range positions {
			positions[index] = fallback
		}
	}
	return output, positions
}

func decodeDoubleEscape(data []byte) ([]byte, int) {
	if len(data) < 2 {
		return data[:1], 1
	}
	escapes := map[byte]string{
		'0': "\x00", 'a': "\x07", 'b': "\x08", 't': "\t", 'n': "\n", 'v': "\x0b",
		'f': "\x0c", 'r': "\r", 'e': "\x1b", ' ': " ", '"': "\"", '/': "/", '\\': "\\",
		'N': "\u0085", '_': "\u00a0", 'L': "\u2028", 'P': "\u2029",
	}
	if decoded, ok := escapes[data[1]]; ok {
		return []byte(decoded), 2
	}
	digits := 0
	switch data[1] {
	case 'x':
		digits = 2
	case 'u':
		digits = 4
	case 'U':
		digits = 8
	}
	if digits > 0 && len(data) >= digits+2 {
		value, err := strconv.ParseUint(string(data[2:digits+2]), 16, 32)
		if err == nil && value <= utf8.MaxRune {
			return []byte(string(rune(value))), digits + 2 //nolint:gosec // bounded by utf8.MaxRune above
		}
	}
	return data[1:2], 2
}

func plainCandidates(data []byte, start, end int) ([]byte, []int) {
	return foldedLineCandidates(data, start, end, 0, false)
}

func blockCandidates(data []byte, start, end int, style yaml.Style) ([]byte, []int) {
	headerEnd := lineEnd(data, start)
	contentStart := consumeLineEnding(data, headerEnd)
	indent := blockIndent(data, contentStart, end, lineIndent(data, start))
	return foldedLineCandidates(data, contentStart, end, indent, style&yaml.FoldedStyle != 0)
}

func foldedLineCandidates(data []byte, start, end, indent int, folded bool) ([]byte, []int) {
	var output []byte
	var positions []int
	cursor := start
	first := true
	previousBlank := false
	for cursor < end {
		currentEnd := min(lineEnd(data, cursor), end)
		content := cursor
		for skipped := 0; skipped < indent && content < currentEnd && data[content] == ' '; skipped++ {
			content++
		}
		trimmedEnd := currentEnd
		if indent == 0 {
			for content < trimmedEnd && (data[content] == ' ' || data[content] == '\t') {
				content++
			}
			for trimmedEnd > content && (data[trimmedEnd-1] == ' ' || data[trimmedEnd-1] == '\t') {
				trimmedEnd--
			}
			if comment := bytes.Index(data[content:trimmedEnd], []byte(" #")); comment >= 0 {
				trimmedEnd = content + comment
			}
		}
		blank := content == trimmedEnd
		if !first {
			separator := byte('\n')
			if folded && !blank && !previousBlank {
				separator = ' '
			} else if !folded && cursor >= end {
				separator = 0
			}
			if separator != 0 {
				output = append(output, separator)
				positions = append(positions, max(start, cursor-1))
			}
		}
		for index := content; index < trimmedEnd; index++ {
			output = append(output, data[index])
			positions = append(positions, index)
		}
		first = false
		previousBlank = blank
		cursor = consumeLineEnding(data, currentEnd)
	}
	return output, positions
}

func blockIndent(data []byte, start, end, parent int) int {
	indent := 0
	for cursor := start; cursor < end; cursor = consumeLineEnding(data, lineEnd(data, cursor)) {
		currentEnd := min(lineEnd(data, cursor), end)
		if len(bytes.TrimSpace(data[cursor:currentEnd])) == 0 {
			continue
		}
		line := lineIndent(data, cursor)
		if line > parent && (indent == 0 || line < indent) {
			indent = line
		}
	}
	return indent
}

func appendMapped(output []byte, positions []int, decoded []byte, source int) ([]byte, []int) {
	output = append(output, decoded...)
	for range decoded {
		positions = append(positions, source)
	}
	return output, positions
}

func alignScalar(value, candidate []byte, positions []int, end int) []int {
	result := make([]int, len(value)+1)
	if bytes.Equal(value, candidate) && len(positions) == len(candidate) {
		copy(result, positions)
		result[len(value)] = end
		return result
	}
	cursor := 0
	last := end
	for index, want := range value {
		found := -1
		for candidateIndex := cursor; candidateIndex < len(candidate); candidateIndex++ {
			if candidate[candidateIndex] == want {
				found = candidateIndex
				break
			}
		}
		if found >= 0 && found < len(positions) {
			last = positions[found]
			cursor = found + 1
		}
		result[index] = last
	}
	result[len(value)] = end
	return result
}
