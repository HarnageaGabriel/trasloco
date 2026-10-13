package modernize

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
)

func (s *stepPlan) ending() string {
	crlf := bytes.Count(s.src, []byte("\r\n"))
	lf := bytes.Count(s.src, []byte("\n")) - crlf
	if crlf > lf {
		return "\r\n"
	}
	return "\n"
}
func quoteYAML(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
func (s *stepPlan) rangeOf(n *yamlx.Node) yamlx.ByteRange {
	r, e := n.ByteRange()
	if e != nil {
		s.err = e
	}
	return r
}
func (s *stepPlan) patch(r yamlx.ByteRange, text string) {
	s.edits = append(s.edits, yamlx.Patch{Start: r.Start, End: r.End, Replacement: []byte(text)})
}

func (s *stepPlan) scalarEdit(n *yamlx.Node, value string, plain bool) {
	r := s.rangeOf(n)
	if s.err != nil {
		return
	}
	token := s.src[r.Start:r.End]
	replacement := value
	if !plain && len(token) > 0 {
		switch token[0] {
		case '\'':
			replacement = quoteYAML(value)
		case '"':
			encoded, _ := json.Marshal(value)
			replacement = string(encoded)
		case '|', '>':
			replacement = quoteYAML(value)
		default:
			// Check representability without invoking a YAML printer.
			doc, ds := yamlx.Parse("scalar", []byte(value))
			if hasError(ds) || doc == nil || doc.Root() == nil || doc.Root().Kind() != yamlx.ScalarKind || doc.Root().Value() != value {
				replacement = quoteYAML(value)
			}
		}
	}
	if len(token) > 0 && (token[0] == '|' || token[0] == '>') && bytes.HasSuffix(token, []byte("\n")) {
		replacement += s.ending()
	}
	s.patch(r, replacement)
}

// contentEnd includes a line's comment and terminator, but does not swallow
// following comment-only lines. Block scalar trailing blank lines stay intact.
func (s *stepPlan) contentEnd(n *yamlx.Node) int {
	r := s.rangeOf(n)
	end := r.End
	for end > r.Start && (s.src[end-1] == '\n' || s.src[end-1] == '\r' || s.src[end-1] == ' ' || s.src[end-1] == '\t') {
		end--
	}
	for end < len(s.src) && s.src[end] != '\n' {
		end++
	}
	if end < len(s.src) {
		end++
	}
	return end
}
func (s *stepPlan) lineStart(offset int) int { return bytes.LastIndexByte(s.src[:offset], '\n') + 1 }
func (s *stepPlan) deletion(p yamlx.Pair) yamlx.ByteRange {
	r := s.rangeOf(p.Key)
	// Remove indentation along with the entry so no whitespace-only line leaks
	// into the next entry; coalescing writes that indentation back exactly once.
	r.Start = s.lineStart(r.Start)
	r.End = s.contentEnd(p.Value)
	return r
}
func (s *stepPlan) unit() int {
	pairs, _ := s.step.Map()
	if len(pairs) == 0 {
		return 2
	}
	first := s.rangeOf(pairs[0].Key).Start
	prefix := string(s.src[s.lineStart(first):first])
	if i := strings.IndexByte(prefix, '-'); i >= 0 && strings.TrimSpace(prefix[:i]) == "" {
		if n := s.taskKeyColumn() - i; n > 0 {
			return n
		}
	}
	return 2
}
func (s *stepPlan) taskKeyColumn() int {
	pairs, _ := s.step.Map()
	for _, p := range pairs {
		if strings.EqualFold(p.Key.Value(), "task") {
			return p.Key.Column() - 1
		}
	}
	return 2
}

func (s *stepPlan) mappingEdits() {
	unit := s.unit()
	indent := s.taskKeyColumn() + unit
	if len(s.values) > 0 {
		indent = s.values[0].pair.Key.Column() - 1
	}
	ending := s.ending()
	var lines []string
	if s.inputs == nil && len(s.appendOps) > 0 {
		lines = append(lines, strings.Repeat(" ", s.taskKeyColumn())+"inputs:")
	}
	for _, op := range s.appendOps {
		text := op.Value.Text
		switch op.Value.Kind {
		case "string":
			text = quoteYAML(text)
		case "K1":
			text = "|-"
		}
		lines = append(lines, strings.Repeat(" ", indent)+op.Input+": "+text)
		if op.Value.Kind == "K1" {
			for _, line := range strings.Split(headerValue, "\n") {
				lines = append(lines, strings.Repeat(" ", indent+unit)+line)
			}
		}
	}
	remaining := len(s.appendOps)
	for _, v := range s.values {
		if !v.dropped {
			remaining++
		}
	}
	if s.inputs != nil && remaining == 0 && len(s.values) > 0 {
		r := s.rangeOf(s.inputKey)
		r.Start = s.lineStart(r.Start)
		r.End = s.contentEnd(s.inputKey)
		s.patch(r, "")
	}
	insertion := 0
	if len(lines) > 0 {
		if s.inputs != nil {
			insertion = s.contentEnd(s.inputs)
		} else {
			insertion = s.contentEnd(s.step)
		}
	}
	coalesced := false
	for _, v := range s.values {
		if !v.dropped {
			continue
		}
		r := s.deletion(v.pair)
		if len(lines) > 0 && r.End == insertion {
			text := strings.Join(lines, ending)
			if r.End < len(s.src) || bytes.HasSuffix(s.src[:r.End], []byte("\n")) {
				text += ending
			}
			s.patch(r, text)
			coalesced = true
		} else {
			s.patch(r, "")
		}
	}
	if len(lines) > 0 && !coalesced {
		text := strings.Join(lines, ending)
		if insertion > 0 && s.src[insertion-1] != '\n' {
			text = ending + text
		}
		if insertion < len(s.src) || bytes.HasSuffix(s.src, []byte("\n")) {
			text += ending
		}
		s.patch(yamlx.ByteRange{Start: insertion, End: insertion}, text)
	}
}
