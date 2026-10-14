package resolve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
)

func nestedEachPipeline(items, depth int) string {
	var b strings.Builder
	b.WriteString("parameters:\n- name: items\n  type: object\n  default:\n")
	for i := 0; i < items; i++ {
		fmt.Fprintf(&b, "  - v%d\n", i)
	}
	b.WriteString("steps:\n")
	indent := ""
	for d := 0; d < depth; d++ {
		fmt.Fprintf(&b, "%s- ${{ each i%d in parameters.items }}:\n", indent, d)
		indent += "  "
	}
	fmt.Fprintf(&b, "%s- script: echo ${{ i0 }}\n", indent)
	return b.String()
}

func resolveBudget(t *testing.T, input string, source memorySource, options Options) (*diag.Collector, *resolver, error) {
	t.Helper()
	collector := diag.NewCollector()
	options.Collector = collector
	root := parse.Source("root.yml", []byte(input), collector)
	resolved, state, err := resolveWith(context.Background(), root, source, options)
	if err != nil && !errors.Is(err, ErrExpansionLimit) {
		t.Fatal(err)
	}
	if errors.Is(err, ErrExpansionLimit) && resolved != nil {
		t.Fatal("aborted resolution returned a partial AST")
	}
	return collector, state, err
}

func TestNestedEachFailsFast(t *testing.T) {
	// 100^4 = 10^8 leaf steps would expand far beyond 20 MB.
	collector, state, err := resolveBudget(t, nestedEachPipeline(100, 4), nil, Options{})
	if !errors.Is(err, ErrExpansionLimit) {
		t.Fatalf("error = %v, want ErrExpansionLimit", err)
	}
	if !hasCode(collector, "TRL-TPL-0003") {
		t.Fatalf("diagnostics = %v, want TRL-TPL-0003", codes(collector))
	}
	if state.nodes != 31462 || state.bytes != 20971665 {
		t.Fatalf("nested each budget = nodes %d, bytes %d", state.nodes, state.bytes)
	}
}

func TestRepeatedTemplateInclusionFailsFast(t *testing.T) {
	// Each level includes the next one ten times: 10^9 steps in total.
	// minOpenCost=1024 bounds even tiny cached template instantiations to about
	// 20,000 opens under 20 MiB; these counters pin that deterministic cutoff.
	source := memorySource{}
	for level := 0; level < 9; level++ {
		source[fmt.Sprintf("@t%d.yml", level)] = "steps:\n" + strings.Repeat(fmt.Sprintf("- template: t%d.yml\n", level+1), 10)
	}
	source["@t9.yml"] = "steps:\n- script: leaf\n"
	collector, state, err := resolveBudget(t, "steps:\n- template: t0.yml\n", source, Options{MaxDepth: 50, MaxFiles: 50})
	if !errors.Is(err, ErrExpansionLimit) {
		t.Fatalf("error = %v, want ErrExpansionLimit", err)
	}
	if !hasCode(collector, "TRL-TPL-0003") {
		t.Fatalf("diagnostics = %v, want TRL-TPL-0003", codes(collector))
	}
	if state.nodes != 19268 || state.bytes != 20972347 {
		t.Fatalf("repeated inclusion budget = nodes %d, bytes %d", state.nodes, state.bytes)
	}
}

func TestSmallBudgetAndIterationLimits(t *testing.T) {
	collector, state, err := resolveBudget(t, nestedEachPipeline(10, 3), nil, Options{MaxExpandedBytes: 4096})
	if !errors.Is(err, ErrExpansionLimit) || state.bytes != 4695 || state.nodes != 15 {
		t.Fatalf("small budget = nodes %d, bytes %d, err %v", state.nodes, state.bytes, err)
	}
	if !hasCode(collector, "TRL-TPL-0003") {
		t.Fatalf("diagnostics = %v", codes(collector))
	}
	collector, _, err = resolveBudget(t, nestedEachPipeline(10, 2), nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	assertNoErrors(t, collector)
}

func TestCrossRepositoryReferences(t *testing.T) {
	root := "resources:\n  repositories:\n  - repository: lib\n    type: git\n    name: org/lib\nsteps:\n- template: dir/a.yml@lib\n"
	source := memorySource{
		// dir/a.yml in lib includes b.yml relative to itself, then /top.yml from lib's root.
		"lib@dir/a.yml": "steps:\n- template: b.yml\n- template: /top.yml\n",
		"lib@dir/b.yml": "steps:\n- script: lib-relative\n",
		"lib@top.yml":   "steps:\n- script: lib-root\n",
		"@dir/b.yml":    "steps:\n- script: WRONG-self-relative\n",
		"@top.yml":      "steps:\n- script: WRONG-self-root\n",
		"@dir/a.yml":    "steps:\n- script: WRONG-self-a\n",
	}
	collector := diag.NewCollector()
	got, err := Resolve(context.Background(), parse.Source("root.yml", []byte(root), collector), source, Options{Collector: collector})
	if err != nil {
		t.Fatal(err)
	}
	assertNoErrors(t, collector)
	files := make([]string, 0, len(got.Steps.Items))
	for _, item := range got.Steps.Items {
		files = append(files, item.Value.Position().File)
	}
	if strings.Join(files, ",") != "dir/b.yml,top.yml" || len(got.Steps.Items) != 2 {
		t.Fatalf("files = %v", files)
	}
}

func TestSelfRootPathUsesIncludingRepository(t *testing.T) {
	// "/x.yml" in a root-repository file is the root of the root repository,
	// regardless of the including file's directory; @self returns to it.
	root := "resources:\n  repositories:\n  - repository: lib\n    type: git\n    name: org/lib\nsteps:\n- template: sub/a.yml\n- template: /sub/c.yml@self\n"
	source := memorySource{
		"@sub/a.yml": "steps:\n- template: /top.yml\n- template: x.yml@lib\n",
		"@top.yml":   "steps:\n- script: self-root\n",
		"@sub/c.yml": "steps:\n- script: self-c\n",
		"lib@x.yml":  "steps:\n- template: /y.yml\n",
		"lib@y.yml":  "steps:\n- script: lib-y\n",
		"@y.yml":     "steps:\n- script: WRONG\n",
	}
	collector := diag.NewCollector()
	got, err := Resolve(context.Background(), parse.Source("root.yml", []byte(root), collector), source, Options{Collector: collector})
	if err != nil {
		t.Fatal(err)
	}
	assertNoErrors(t, collector)
	files := make([]string, 0, len(got.Steps.Items))
	for _, item := range got.Steps.Items {
		files = append(files, item.Value.Position().File)
	}
	if strings.Join(files, ",") != "top.yml,y.yml,sub/c.yml" {
		t.Fatalf("files = %v", files)
	}
}
