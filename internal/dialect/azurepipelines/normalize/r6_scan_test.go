package normalize

import (
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

func TestScrubPairsNestedAssignmentsLinear(t *testing.T) {
	for _, depth := range []int{100, 1000, 10000} {
		text := nestedAssignments(depth)
		got, steps := scrubPairsCount(text)
		if strings.Contains(got, "panic-canary") {
			t.Fatalf("depth %d: canary leaked", depth)
		}
		if !strings.HasSuffix(got, "DB_PASSWORD="+redact.Replacement()) {
			t.Fatalf("depth %d: tail = %q", depth, got[len(got)-40:])
		}
		if limit := 8 * len(text); steps > limit {
			t.Fatalf("depth %d: %d steps for %d bytes, want <= %d (non-linear work)", depth, steps, len(text), limit)
		}
	}
	// Quoted and whitespace-heavy variants stay linear as well.
	for _, text := range []string{
		strings.Repeat(`a="a=`, 5000) + "DB_PASSWORD=x",
		strings.Repeat("PASSWORD=$(x)", 5000),
		strings.Repeat("a='a=\"", 4000) + "TOKEN=x",
		strings.Repeat("A=b ", 20000) + "TOKEN=x",
	} {
		_, steps := scrubPairsCount(text)
		if limit := 8 * len(text); steps > limit {
			t.Fatalf("%.20q...: %d steps for %d bytes, want <= %d", text, steps, len(text), limit)
		}
	}
	// The r7 predicate shapes (value tests per key) stay linear too; the
	// counter includes the predicate work.
	for _, shape := range predicateShapes(5000) {
		if _, steps := scrubPairsCount(shape.text); steps > 8*len(shape.text) {
			t.Fatalf("%s: %d steps for %d bytes, want <= %d", shape.name, steps, len(shape.text), 8*len(shape.text))
		}
	}
}

func TestDeepNestedAssignmentPipeline(t *testing.T) {
	got := leaks(t, "steps:\n- task: Foo@1\n  name: s\n  inputs:\n    arguments: "+nestedAssignments(10000)+"\n", "panic-canary")
	if stepByID(t, got, implicitID, "s").Fidelity != diag.FidelityNeedsManual {
		t.Fatal("nested secret did not raise needs-manual")
	}
}

func TestOversizeLeafReplaced(t *testing.T) {
	b := &backstop{hitSet: map[*ir.Metadata]bool{}, raise: map[*ir.Metadata]bool{}}
	big := strings.Repeat("x", maxScanBytes+1)
	if got := b.text(big, true); got != redact.Replacement() {
		t.Fatalf("oversize leaf = %d bytes, want placeholder", len(got))
	}
	if got := b.text(strings.Repeat("x", 1000), true); len(got) != 1000 {
		t.Fatal("bounded leaf changed")
	}
}

func FuzzScrubPairs(f *testing.F) {
	for _, seed := range []string{
		"", "a=b", "DB_PASSWORD=x", `--env="DB_PASSWORD=a b" c=d`, `'A=b' "TOKEN=x y`, nestedAssignments(20),
		"a=a=a=PASSWORD='q", `k="`, "=", "==", `"="`, "x=y\nTOKEN=z",
	} {
		f.Add(seed)
	}
	for _, shape := range predicateShapes(20) {
		f.Add(shape.text)
	}
	f.Fuzz(func(t *testing.T, text string) {
		got, steps, overflow := scrubPairsWork(text)
		// The budget is workFactor*len+workSlack; one loop iteration adds at
		// most a few len(text) beyond it before the overflow check.
		if limit := (workFactor+4)*len(text) + workSlack + 8; steps > limit || (!overflow && steps > workFactor*len(text)+workSlack) {
			t.Fatalf("%d steps for %d bytes (overflow=%v)", steps, len(text), overflow)
		}
		if scrubPairs(text) != got {
			t.Fatal("scrubPairs differs from scrubPairsCount")
		}
		_ = scrubFlags(text)
		_ = scrubDemand(text)
	})
}

func TestFlagAndDemandScrubbing(t *testing.T) {
	for _, text := range []string{
		"--client-secret a1", "--password a1", "--token a1", "--secret a1", "--api-key a1", "-Password a1",
		`--password "a1 b2"`, "build --verbose --token a1 --out dir",
	} {
		if got := scrubFlags(text); strings.Contains(got, "a1") {
			t.Errorf("scrubFlags(%q) = %q", text, got)
		}
	}
	for _, text := range []string{
		"--secret-name my-secret", "--token-file t.txt", "--password $(pw)", "--token --other", "--name value",
		"--password", "-p x", "--verbose --out dir",
	} {
		if got := scrubFlags(text); got != text {
			t.Errorf("scrubFlags(%q) = %q, want unchanged", text, got)
		}
	}
	if got := scrubDemand("DB_PASSWORD -equals d1"); strings.Contains(got, "d1") {
		t.Errorf("scrubDemand leaked: %q", got)
	}
	for _, text := range []string{"Agent.Version -equals 3.2", "DB_PASSWORD -equals $(pw)", "npm", "Cmd"} {
		if got := scrubDemand(text); got != text {
			t.Errorf("scrubDemand(%q) = %q, want unchanged", text, got)
		}
	}
}

func TestConditionComparisonVariants(t *testing.T) {
	for _, condition := range []string{
		"and(succeeded(), eq(variables['db-password'], 'cond-two-canary'))",
		"ne('cond-two-canary', variables.API_TOKEN)",
		"contains(variables.SECRET_KEY, 'cond-two-canary')",
	} {
		leaks(t, "steps:\n- script: ok\n  condition: "+condition+"\n", "cond-two-canary")
	}
	// A non-secret variable keeps its literal.
	got := leaks(t, "steps:\n- script: ok\n  name: s\n  condition: eq(variables.Build, 'keep-me')\n")
	if step := stepByID(t, got, implicitID, "s"); step.If.Expr == nil || step.If.Expr.Call.Args[1].Literal.String != "keep-me" {
		t.Fatalf("benign comparison changed: %#v", step.If)
	}
}

func TestContinuationFollowsResolvedShell(t *testing.T) {
	const caret = "echo '##vso[task.setse^\ncret]caret-canary'"
	const backslash = "echo '##vso[task.setse\\\ncret]slash-canary'"
	const backtick = "echo '##vso[task.setse`\ncret]tick-canary'"
	for _, test := range []struct {
		shell, script string
		replaced      bool
	}{
		{"bash", caret, false},
		{"bash", backtick, false},
		{"bash", backslash, true},
		{"pwsh", backslash, false},
		{"pwsh", caret, false},
		{"pwsh", backtick, true},
		{"powershell", backtick, true},
		{"powershell", caret, false},
		{"", caret, true},
		{"azure-default", backslash, true},
	} {
		if got := secretLoggingLiteralFor(test.script, test.shell); got != test.replaced {
			t.Errorf("shell %q script %q: replaced = %v, want %v", test.shell, test.script, got, test.replaced)
		}
	}
	got := normalizeText(t, "steps:\n- bash: |\n    "+strings.ReplaceAll(caret, "\n", "\n    ")+"\n  name: s\n")
	if run := stepByID(t, got, implicitID, "s").Run; run == nil || !strings.Contains(run.Script, "caret-canary") {
		t.Fatalf("bash script with a literal caret was replaced: %#v", run)
	}
}
