package resolve

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
)

// Blocking item 1 and 5: runtime $[ ] directives are not compile-time leftovers.
func TestRuntimeDirectivesAreNotLeftovers(t *testing.T) {
	tests := []struct{ name, input string }{
		{"bool", "steps:\n- script: a\n  continueOnError: $[ variables.x ]\n"},
		{"enum", "jobs:\n- job: J\n  workspace:\n    clean: $[ variables.c ]\n  steps:\n  - script: a\n"},
		{"expression", "steps:\n- script: a\n  condition: $[ variables.c ]\n"},
		{"any-entry", "steps:\n- task: A@1\n  inputs:\n    k: v\n    r: $[ variables.y ]\n"},
		{"scalar-map", "steps:\n- script: a\n  env:\n    R: $[ variables.y ]\n"},
		{"matrix", "jobs:\n- job: J\n  strategy:\n    matrix:\n      one:\n        cfg: $[ variables.v ]\n  steps:\n  - script: a\n"},
		{"free-form-param", "parameters:\n- name: p\n  type: object\n  default:\n    r: $[ variables.y ]\nsteps:\n- script: a\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			collector := diag.NewCollector()
			parsed := parse.Source("root.yml", []byte(test.input), collector)
			if parsed == nil {
				t.Fatalf("parse failed: %v", codes(collector))
			}
			if leftovers := findLeftovers(parsed); len(leftovers) > 0 {
				t.Fatalf("runtime directive reported as leftover: %+v", leftovers)
			}
			resolved, collector, printed := resolveText(t, test.input, nil, Options{})
			assertNoErrors(t, collector)
			if resolved == nil || !strings.Contains(printed, "$[") {
				t.Fatalf("runtime expression lost: AST=%v diagnostics=%v\n%s", resolved != nil, codes(collector), printed)
			}
		})
	}
}

// Blocking item 5: Resolve itself must refuse an AST with a leftover.
func TestResolveReturnsNilAndInternalErrorOnLeftover(t *testing.T) {
	root := &ast.Pipeline{Extends: &ast.Extends{}}
	collector := diag.NewCollector()
	resolved, err := Resolve(context.Background(), root, nil, Options{Collector: collector})
	if err != nil {
		t.Fatal(err)
	}
	if resolved != nil {
		t.Fatal("Resolve returned an AST that still holds a compile-time construct")
	}
	if !hasCode(collector, "TRL-INT-0002") {
		t.Fatalf("diagnostics = %v, want TRL-INT-0002", codes(collector))
	}
}

// Blocking item 2: the size limit is applied incrementally; a 5 MB plain file resolves.
func TestLargePlainStepsFileResolves(t *testing.T) {
	var b strings.Builder
	b.WriteString("steps:\n")
	for i := 0; i < 300000; i++ {
		b.WriteString("- script: echo hi\n")
	}
	body := b.String()
	if len(body) < 5<<20 {
		t.Fatalf("fixture is only %d bytes", len(body))
	}
	t.Run("root", func(t *testing.T) {
		collector := diag.NewCollector()
		root := parse.Source("root.yml", []byte(body), collector)
		resolved, err := Resolve(context.Background(), root, nil, Options{Collector: collector})
		if err != nil {
			t.Fatal(err)
		}
		assertNoErrors(t, collector)
		if resolved == nil || len(resolved.Steps.Items) != 300000 {
			t.Fatalf("resolved = %v, diagnostics = %v", resolved != nil, codes(collector))
		}
	})
	t.Run("template", func(t *testing.T) {
		collector := diag.NewCollector()
		root := parse.Source("root.yml", []byte("steps:\n- template: big.yml\n"), collector)
		resolved, err := Resolve(context.Background(), root, memorySource{"@big.yml": body}, Options{Collector: collector})
		if err != nil {
			t.Fatal(err)
		}
		assertNoErrors(t, collector)
		if resolved == nil || len(resolved.Steps.Items) != 300000 {
			t.Fatalf("resolved = %v, diagnostics = %v", resolved != nil, codes(collector))
		}
	})
}

// deepChain builds levels nested templates whose leaf holds leafSteps steps.
func deepChain(levels, leafSteps int) memorySource {
	source := memorySource{}
	for level := 0; level < levels; level++ {
		source[fmt.Sprintf("@t%d.yml", level)] = fmt.Sprintf("steps:\n- template: t%d.yml\n", level+1)
	}
	source[fmt.Sprintf("@t%d.yml", levels)] = "steps:\n" + strings.Repeat("- script: leaf\n", leafSteps)
	return source
}

// Blocking item 3: one immutable chain per template instance, charged per node.
func TestDeepChainSharesAndChargesCallSites(t *testing.T) {
	const leafSteps = 200
	options := Options{MaxDepth: 120, MaxFiles: 120}
	collector, shallow, err := resolveBudget(t, "steps:\n- template: t0.yml\n", deepChain(1, leafSteps), options)
	if err != nil {
		t.Fatal(err)
	}
	assertNoErrors(t, collector)
	collector, deep, err := resolveBudget(t, "steps:\n- template: t0.yml\n", deepChain(99, leafSteps), options)
	if err != nil {
		t.Fatal(err)
	}
	assertNoErrors(t, collector)
	// 200 nodes at 98 additional call sites must cost at least that many units.
	if extra := deep.bytes - shallow.bytes; extra < leafSteps*98 {
		t.Fatalf("deep chain charged only %d extra units over depth 1", extra)
	}

	collector = diag.NewCollector()
	options.Collector = collector
	root := parse.Source("root.yml", []byte("steps:\n- template: t0.yml\n"), collector)
	resolved, err := Resolve(context.Background(), root, deepChain(99, leafSteps), options)
	if err != nil || resolved == nil {
		t.Fatalf("resolve: %v %v", err, codes(collector))
	}
	items := resolved.Steps.Items
	if len(items) != leafSteps {
		t.Fatalf("steps = %d", len(items))
	}
	first := items[0].Value.(*ast.ScriptStep)
	if len(first.Chain) != 100 {
		t.Fatalf("chain length = %d, want 100", len(first.Chain))
	}
	for i, item := range items {
		step := item.Value.(*ast.ScriptStep)
		if &step.Chain[0] != &first.Chain[0] || &step.Script.Chain[0] != &first.Chain[0] {
			t.Fatalf("step %d holds its own copy of the call-site chain", i)
		}
	}
}
