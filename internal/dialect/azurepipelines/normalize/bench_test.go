package normalize

import (
	"fmt"
	"strings"
	"testing"
)

func largePipeline(steps int) string {
	var b strings.Builder
	b.WriteString("jobs:\n- job: j\n  steps:\n")
	for i := 0; i < steps; i++ {
		fmt.Fprintf(&b, "  - script: echo build step %d with some ordinary text https://example.com/a/b\n    name: s%d\n    displayName: Step number %d\n    env:\n      LOG_LEVEL: debug\n", i, i, i)
	}
	return b.String()
}

// nestedAssignments is the adversarial command line from review 6:
// a=a=a=...=DB_PASSWORD=canary.
func nestedAssignments(depth int) string {
	return strings.Repeat("a=", depth) + "DB_PASSWORD=panic-canary"
}

// BenchmarkScrubPairsNested measures the pair scrubber on 10k nested
// assignments (the work was quadratic and recursive before fix6).
func BenchmarkScrubPairsNested(b *testing.B) {
	text := nestedAssignments(10000)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if strings.Contains(scrubPairs(text), "panic-canary") {
			b.Fatal("canary leaked")
		}
	}
}

// BenchmarkFinalScrubNested runs the whole backstop on 20k steps that each
// carry a nested-assignment argument.
func BenchmarkFinalScrubNested(b *testing.B) {
	var y strings.Builder
	y.WriteString("jobs:\n- job: j\n  steps:\n")
	arg := strings.Repeat("a=", 50) + "DB_PASSWORD=x"
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&y, "  - task: Foo@1\n    name: s%d\n    inputs:\n      arguments: %s\n", i, arg)
	}
	p := normalizeText(b, y.String())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := &conv{}
		c.finalScrub(p)
	}
}

// BenchmarkFinalScrub measures the backstop alone on a ~20k-step pipeline.
func BenchmarkFinalScrub(b *testing.B) {
	p := normalizeText(b, largePipeline(20000))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := &conv{}
		c.finalScrub(p)
	}
}
