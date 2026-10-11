package normalize

import (
	"context"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

// predicateShapes are the r7 inputs that made the value predicates (lower-casing,
// "$(" search) run over the whole remaining token for every secret key.
func predicateShapes(n int) []predicateShape {
	return []predicateShape{
		{"password-macro", strings.Repeat("PASSWORD=$(", n)},
		{"token-expression", strings.Repeat("TOKEN=$[", n) + "]"},
		{"password-tail", strings.Repeat("password=", n) + "$("},
		{"quoted-expr", `PASSWORD="$[` + strings.Repeat("PASSWORD=$[", n) + `]"`},
	}
}

type predicateShape struct{ name, text string }

func TestScrubPairsPredicateWorkLinear(t *testing.T) {
	for _, n := range []int{50, 5000, 50000} {
		for _, shape := range predicateShapes(n) {
			name, text := shape.name, shape.text
			got, steps := scrubPairsCount(text)
			if limit := 16*len(text) + 256; steps > limit {
				t.Errorf("%s n=%d: %d steps for %d bytes, want <= %d (non-linear predicate work)", name, n, steps, len(text), limit)
			}
			if len(text) > maxPredicateBytes && !strings.Contains(got, redact.Replacement()) {
				t.Errorf("%s n=%d: oversize secret value kept verbatim", name, n)
			}
		}
	}
}

func TestLoggingSpansWorkLinear(t *testing.T) {
	for _, n := range []int{50, 5000, 50000} {
		line := strings.Repeat("x##vso[task.logissue]a", n)
		spans, work := loggingSpansWork(line)
		if len(spans) != n {
			t.Fatalf("n=%d: %d spans", n, len(spans))
		}
		if limit := 8 * len(line); work > limit {
			t.Errorf("n=%d: %d work for %d bytes, want <= %d (quoteContext rescans the prefix)", n, work, len(line), limit)
		}
	}
}

// referenceQuoteContext is the original quadratic prefix scan, kept as the
// oracle for the incremental quoteScanner.
func referenceQuoteContext(prefix string) (quote byte, commented bool) {
	for i := 0; i < len(prefix); i++ {
		ch := prefix[i]
		switch {
		case quote == '"' && ch == backslash:
			i++
		case quote != 0:
			if ch == quote {
				quote = 0
			}
		case ch == '"' || ch == '\'':
			quote = ch
		case ch == '#' && (i == 0 || prefix[i-1] == ' ' || prefix[i-1] == '\t'):
			return 0, true
		}
	}
	return quote, false
}

func TestQuoteScannerMatchesReference(t *testing.T) {
	alphabet := []string{`"`, `'`, `\`, `#`, ` `, "\t", "a", "##vso[x]"}
	var build func(prefix string, depth int)
	count := 0
	build = func(prefix string, depth int) {
		var s quoteScanner
		// Scan in uneven steps: every cut point must agree with the oracle.
		for to := 0; to <= len(prefix); to++ {
			s.advance(prefix, to)
			wantQuote, wantCommented := referenceQuoteContext(prefix[:to])
			if s.quote != wantQuote || s.commented != wantCommented {
				t.Fatalf("%q cut %d: got (%q,%v), want (%q,%v)", prefix, to, s.quote, s.commented, wantQuote, wantCommented)
			}
		}
		count++
		if depth == 0 {
			return
		}
		for _, piece := range alphabet {
			build(prefix+piece, depth-1)
		}
	}
	build("", 5)
	if count < 1000 {
		t.Fatalf("only %d prefixes checked", count)
	}
}

func TestLoggingSpansQuoteStateAcrossCommands(t *testing.T) {
	// A quote opened before the first command encloses the second; a comment
	// marker between commands hides the second.
	spans, _ := loggingSpansWork(`echo "a ##vso[task.logissue]x" ##vso[task.logissue]y`)
	if len(spans) != 2 || spans[0].text != "x" || spans[1].text != "y" {
		t.Fatalf("spans = %+v", spans)
	}
	spans, _ = loggingSpansWork(`##vso[task.logissue]x # ##vso[task.logissue]y`)
	if len(spans) != 1 {
		t.Fatalf("commented command kept: %+v", spans)
	}
}

// pipelineWorkOf normalizes a pipeline and returns its work counter.
func pipelineWorkOf(t *testing.T, yaml string) (*ir.Pipeline, int) {
	t.Helper()
	data := []byte(yaml)
	parsed := parse.Source("pipeline.yml", data, diag.NewCollector())
	got, work, err := pipelineWork(context.Background(), parsed, Options{SourceText: func(string) ([]byte, bool) { return data, true }})
	if err != nil {
		t.Fatal(err)
	}
	return got, work
}

// TestPipelineWorkBoundedOnHostileLeaves normalizes 1 MiB of each r7 shape
// through the full pipeline and asserts a deterministic work bound.
func TestPipelineWorkBoundedOnHostileLeaves(t *testing.T) {
	const size = 1 << 20
	displayName := strings.Repeat("PASSWORD=$(", size/11)
	got, work := pipelineWorkOf(t, "steps:\n- script: echo\n  name: s\n  displayName: '"+displayName+"'\n")
	if limit := 40 * len(displayName); work > limit {
		t.Errorf("displayName: work %d for %d bytes, want <= %d", work, len(displayName), limit)
	}
	step := stepByID(t, got, implicitID, "s")
	if strings.Contains(step.Name, "$(") || !strings.Contains(step.Name, redact.Replacement()) {
		t.Errorf("secret-shaped displayName kept: %.60q", step.Name)
	}

	line := strings.Repeat("x##vso[task.logissue]a", size/22)
	got, work = pipelineWorkOf(t, "steps:\n- script: |\n    "+line+"\n  name: s\n")
	if limit := 40 * len(line); work > limit {
		t.Errorf("script line: work %d for %d bytes, want <= %d", work, len(line), limit)
	}
	if ops := stepByID(t, got, implicitID, "s").Run.Operations; len(ops) != size/22 {
		t.Errorf("%d annotate operations, want %d", len(ops), size/22)
	}
}

func TestPredicateBoundTreatsLongValueAsSecret(t *testing.T) {
	long := strings.Repeat("x", maxPredicateBytes+1)
	if !literalSecret("password", long) || literalSecret("password", strings.Repeat(" ", maxPredicateBytes+1)) {
		t.Fatal("long value must be a literal unless blank")
	}
	// Within the bound the usual exemptions still apply.
	for _, value := range []string{"true", "$(pw)", "$[a.b]", "${{ x }}", "12", "a$(b)"} {
		if literalSecret("password", value) {
			t.Errorf("%q treated as a literal secret", value)
		}
	}
	if !literalSecret("password", "hunter2") {
		t.Fatal("plain literal not secret")
	}
}

// BenchmarkR7HostileLeaves times the r7 shapes at about 1 MiB (informational).
func BenchmarkR7HostileLeaves(b *testing.B) {
	for _, shape := range predicateShapes(1 << 17) {
		b.Run(shape.name, func(b *testing.B) {
			for b.Loop() {
				scrubPairsCount(shape.text)
			}
		})
	}
	line := strings.Repeat("x##vso[task.logissue]a", 1<<20/22)
	b.Run("logissue", func(b *testing.B) {
		for b.Loop() {
			loggingSpansWork(line)
		}
	})
}
