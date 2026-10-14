package yamlx

import (
	"fmt"
	"strings"
	"testing"
)

func flowSequenceText(items int) string {
	var b strings.Builder
	b.WriteString("steps: [")
	for i := 0; i < items; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "s%d", i)
	}
	b.WriteString("]\n")
	return b.String()
}

// Issue #15 item 3: resolving the position of every node in a flow sequence
// walked from the start of the line, which is quadratic in the number of items
// on that line. scanWork counts the source bytes decoded while locating nodes;
// the fix keeps it proportional to the input length, so the linear bounds below
// fail on the pre-fix code.
func TestFlowSequenceScanWorkIsLinear(t *testing.T) {
	var previousWork, previousLen int
	for _, items := range []int{500, 4000, 8000} {
		text := flowSequenceText(items)
		document, diagnostics := Parse("flow.yml", []byte(text))
		if len(diagnostics) != 0 {
			t.Fatalf("items=%d: %v", items, diagnostics)
		}
		work := document.ctx.scanWork
		if limit := 4 * len(text); work > limit {
			t.Fatalf("items=%d len=%d: scan work %d exceeds linear budget %d", items, len(text), work, limit)
		}
		if previousLen != 0 {
			workRatio := float64(work) / float64(previousWork)
			lenRatio := float64(len(text)) / float64(previousLen)
			if workRatio > 2*lenRatio {
				t.Fatalf("scan work grew %.1fx for %.1fx input: not linear", workRatio, lenRatio)
			}
		}
		previousWork, previousLen = work, len(text)
	}
}

// Issue #15 item 4 (ADR 0009): a scalar records whether it was written quoted.
func TestScalarQuoted(t *testing.T) {
	document, diagnostics := Parse("quoted.yml", []byte("a: '60'\nb: 60\nc: \"x\"\nd: |\n  text\n"))
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	for _, test := range []struct {
		key    string
		quoted bool
	}{
		{"a", true},
		{"b", false},
		{"c", true},
		{"d", false},
	} {
		pair := findPair(t, document.Root(), test.key)
		if got := pair.Value.Quoted(); got != test.quoted {
			t.Errorf("%s: Quoted() = %v, want %v", test.key, got, test.quoted)
		}
	}
}
