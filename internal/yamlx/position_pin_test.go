// This file records the exact node positions main's yamlx produced for a corpus
// of offset edge cases; see TestPositionOffsetsMatchMain.
package yamlx

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// positionPin is one corpus document plus the exact projection of its node tree
// recorded from main (the branch's yamlx before the linear-offset rewrite),
// with one line per node in depth-first order:
//
//	kind|line|col|endLine|endCol|offset|endOffset|byteStart|byteEnd|text|err
//
// The corpus pins every offset class the rewrite must preserve: BOM+CRLF and a
// missing final newline, 3- and 4-byte runes, a combining mark, tab
// separation, all three quote styles, folded and literal block scalars with
// indentation indicators, and nested flow. The values were produced by running
// this projection against main's yamlx (extracted with "git show" and loaded
// through "go test -overlay"), so any diff is a position regression.
type positionPin struct {
	name  string
	input string
	want  string
}

var positionPins = []positionPin{
	{"bom_crlf_no_final_newline", "\ufeffa: first\r\nkey: 'two'\r\nblock: |-\r\n  line1\r\n  line2\r\nlast: 3", "4|1|1|6|8|3|61|3|61|\"a: first\\r\\nkey: 'two'\\r\\nblock: |-\\r\\n  line1\\r\\n  line2\\r\\nlast: 3\"|\n8|1|1|1|2|3|4|3|4|\"a\"|\n8|1|4|1|9|6|11|6|11|\"first\"|\n8|2|1|2|4|13|16|13|16|\"key\"|\n8|2|6|2|11|18|23|18|23|\"'two'\"|\n8|3|1|3|6|25|30|25|30|\"block\"|\n8|3|8|6|1|32|54|32|54|\"|-\\r\\n  line1\\r\\n  line2\\r\\n\"|\n8|6|1|6|5|54|58|54|58|\"last\"|\n8|6|7|6|8|60|61|60|61|\"3\"|"},
	{"unicode_runes", "key: é€😀x\nother: plain\n", "4|1|1|2|13|0|28|0|28|\"key: é€😀x\\nother: plain\"|\n8|1|1|1|4|0|3|0|3|\"key\"|\n8|1|6|1|10|5|15|5|15|\"é€😀x\"|\n8|2|1|2|6|16|21|16|21|\"other\"|\n8|2|8|2|13|23|28|23|28|\"plain\"|"},
	{"combining_mark", "key: éx\n", "4|1|1|1|9|0|9|0|9|\"key: éx\"|\n8|1|1|1|4|0|3|0|3|\"key\"|\n8|1|6|1|9|5|9|5|9|\"éx\"|"},
	{"tab_separation", "key:\tvalue\nquoted: \"a\tb\"\n", "4|1|1|2|14|0|24|0|24|\"key:\\tvalue\\nquoted: \\\"a\\tb\\\"\"|\n8|1|1|1|4|0|3|0|3|\"key\"|\n8|1|6|1|11|5|10|5|10|\"value\"|\n8|2|1|2|7|11|17|11|17|\"quoted\"|\n8|2|9|2|14|19|24|19|24|\"\\\"a\\tb\\\"\"|"},
	{"quote_styles", "plain: 60\nsingle: '60'\ndouble: \"60\"\n", "4|1|1|3|13|0|35|0|35|\"plain: 60\\nsingle: '60'\\ndouble: \\\"60\\\"\"|\n8|1|1|1|6|0|5|0|5|\"plain\"|\n8|1|8|1|10|7|9|7|9|\"60\"|\n8|2|1|2|7|10|16|10|16|\"single\"|\n8|2|9|2|13|18|22|18|22|\"'60'\"|\n8|3|1|3|7|23|29|23|29|\"double\"|\n8|3|9|3|13|31|35|31|35|\"\\\"60\\\"\"|"},
	{"block_indicators", "lit: |2\n    indented\nfold: >2-\n    folded\n    text\nkeep: |+\n  trailing\n\n\nnext: 1\n", "4|1|1|10|8|0|80|0|80|\"lit: |2\\n    indented\\nfold: >2-\\n    folded\\n    text\\nkeep: |+\\n  trailing\\n\\n\\nnext: 1\"|\n8|1|1|1|4|0|3|0|3|\"lit\"|\n8|1|6|3|1|5|21|5|21|\"|2\\n    indented\\n\"|\n8|3|1|3|5|21|25|21|25|\"fold\"|\n8|3|7|6|1|27|51|27|51|\">2-\\n    folded\\n    text\\n\"|\n8|6|1|6|5|51|55|51|55|\"keep\"|\n8|6|7|10|1|57|73|57|73|\"|+\\n  trailing\\n\\n\\n\"|\n8|10|1|10|5|73|77|73|77|\"next\"|\n8|10|7|10|8|79|80|79|80|\"1\"|"},
	{"nested_flow", "flow: [[1, 2], {a: [3, {b: 4}]}]\n", "4|1|1|1|33|0|32|0|32|\"flow: [[1, 2], {a: [3, {b: 4}]}]\"|\n8|1|1|1|5|0|4|0|4|\"flow\"|\n2|1|7|1|33|6|32|6|32|\"[[1, 2], {a: [3, {b: 4}]}]\"|\n2|1|8|1|14|7|13|7|13|\"[1, 2]\"|\n8|1|9|1|10|8|9|8|9|\"1\"|\n8|1|12|1|13|11|12|11|12|\"2\"|\n4|1|16|1|32|15|31|15|31|\"{a: [3, {b: 4}]}\"|\n8|1|17|1|18|16|17|16|17|\"a\"|\n2|1|20|1|31|19|30|19|30|\"[3, {b: 4}]\"|\n8|1|21|1|22|20|21|20|21|\"3\"|\n4|1|24|1|30|23|29|23|29|\"{b: 4}\"|\n8|1|25|1|26|24|25|24|25|\"b\"|\n8|1|28|1|29|27|28|27|28|\"4\"|"},
}

// TestPositionOffsetsMatchMain pins Position.Offset/EndCol and ByteRange
// against the values main produced for the same corpus.
func TestPositionOffsetsMatchMain(t *testing.T) {
	for _, pin := range positionPins {
		t.Run(pin.name, func(t *testing.T) {
			document, diagnostics := Parse("pin.yaml", []byte(pin.input))
			if len(diagnostics) != 0 {
				t.Fatal(diagnostics)
			}
			var lines []string
			var visit func(*Node)
			visit = func(n *Node) {
				position := n.Position()
				span, err := n.ByteRange()
				text, errText := "", ""
				if err != nil {
					errText = err.Error()
				} else {
					text = pin.input[span.Start:span.End]
				}
				lines = append(lines, fmt.Sprintf("%d|%d|%d|%d|%d|%d|%d|%d|%d|%s|%s",
					n.raw.Kind, position.Line, position.Col, position.EndLine, position.EndCol,
					position.Offset, position.EndOffset, span.Start, span.End, strconv.Quote(text), errText))
				for _, child := range n.raw.Content {
					visit(wrap(child, n.ctx))
				}
			}
			visit(document.Root())
			if got := strings.Join(lines, "\n"); got != pin.want {
				t.Fatalf("positions differ from main:\n got %q\nwant %q", got, pin.want)
			}
		})
	}
}

// TestLineIndentAtAgreesWithLineIndent keeps the precomputed per-line
// indentation in step with the reference lineIndent over the same corpus.
func TestLineIndentAtAgreesWithLineIndent(t *testing.T) {
	for _, pin := range positionPins {
		t.Run(pin.name, func(t *testing.T) {
			document, diagnostics := Parse("pin.yaml", []byte(pin.input))
			if len(diagnostics) != 0 {
				t.Fatal(diagnostics)
			}
			ctx := document.ctx
			for offset := 0; offset <= len(ctx.data); offset++ {
				if got, want := ctx.lineIndentAt(offset), lineIndent(ctx.data, offset); got != want {
					t.Fatalf("lineIndentAt(%d) = %d, lineIndent = %d", offset, got, want)
				}
			}
		})
	}
}
