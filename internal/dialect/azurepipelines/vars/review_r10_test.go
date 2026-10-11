package vars

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
)

// reservedMarkerWork runs the logging markers of one script through the same
// scan, readonly lookup and ingestion the evaluator uses and returns all
// counted work.
func reservedMarkerWork(t *testing.T, name string, count int, perLine bool) int {
	t.Helper()
	var script string
	if perLine {
		script = strings.Repeat("echo ##vso[task.setvariable variable="+name+"]v\n", count)
	} else {
		script = "# " + strings.Repeat("##vso[task.setvariable variable="+name+"]v", count)
	}
	step := &ast.ScriptStep{Kind: ast.ScriptBash, Script: scalarNode(script)}
	counter := &evaluationWork{}
	events := mutationsWithExpanderWork(step, newMacroExpander(&Table{}), "", "", "", 0, AgentOSLinux, counter)
	if len(events) != count {
		t.Fatalf("markers = %d, want %d", len(events), count)
	}
	table := &Table{}
	collector := diag.NewCollector()
	for _, event := range events {
		applyMutationWork(table, event, ast.StepProps{}, collector, counter)
	}
	if len(table.Bindings) != 1 || len(table.Bindings[0].Candidates) != count {
		t.Fatalf("bindings = %d, want one with %d candidates", len(table.Bindings), count)
	}
	return counter.total()
}

func TestReviewR10ReservedNameMarkersScaleLinearly(t *testing.T) {
	for _, perLine := range []bool{false, true} {
		for _, name := range []string{"path.x", "x"} {
			small := reservedMarkerWork(t, name, 500, perLine)
			large := reservedMarkerWork(t, name, 1000, perLine)
			t.Logf("%s perLine=%t: %d -> %d", name, perLine, small, large)
			assertLinearGrowth(t, small, large)
		}
	}
}

func TestReviewR10ReservedNameKeepsLookupSemantics(t *testing.T) {
	table := &Table{}
	collector := diag.NewCollector()
	for i := 0; i < 3; i++ {
		event := mutation{candidate: loggingCandidate("path.x", "j", "s", "st", "", 0, false, diag.Source{}), bareName: "path.x"}
		applyMutationWork(table, event, ast.StepProps{}, collector, nil)
	}
	if _, ok := lookup(table, "path.x", PhaseMacro); ok {
		t.Fatal("a reserved name must stay unavailable")
	}
	add(table, Candidate{Name: "y", ComparisonKey: key("y"), Available: true, CompileVisible: true, Value: "1"})
	add(table, Candidate{Name: "y", ComparisonKey: key("y"), Available: false, Value: "2"})
	add(table, Candidate{Name: "y", ComparisonKey: key("y"), Available: true, Value: "3"})
	add(table, Candidate{Name: "y", ComparisonKey: key("y"), Available: false, Value: "4"})
	if c, ok := lookup(table, "y", PhaseMacro); !ok || c.Value != "3" {
		t.Fatalf("macro y = %#v, %t; want 3", c, ok)
	}
	if c, ok := lookup(table, "y", PhaseCompile); !ok || c.Value != "1" {
		t.Fatalf("compile y = %#v, %t; want 1", c, ok)
	}
}

func TestReviewR10CloneTableIndexesMatchLinearScan(t *testing.T) {
	var base Table
	for i := 0; i < 20; i++ {
		name := "v" + strconv.Itoa(i%7)
		add(&base, Candidate{Name: name, ComparisonKey: key(name), Available: i%3 != 0, CompileVisible: i%2 == 0, Value: strconv.Itoa(i)})
	}
	left, right := cloneTable(base), cloneTable(base)
	for i := 0; i < 15; i++ {
		leftName, rightName := "v"+strconv.Itoa(i%9), "w"+strconv.Itoa(i%4)
		add(&left, Candidate{Name: leftName, ComparisonKey: key(leftName), Available: i%2 == 0, CompileVisible: true, Value: "l"})
		add(&right, Candidate{Name: rightName, ComparisonKey: key(rightName), Available: i%5 != 0, Value: "r"})
	}
	for name, table := range map[string]*Table{"base": &base, "left": &left, "right": &right} { //trasloco:unordered-ok independent subtests
		if len(table.bindingIndex) != len(table.Bindings) {
			t.Fatalf("%s: index has %d keys for %d bindings", name, len(table.bindingIndex), len(table.Bindings))
		}
		for i := range table.Bindings {
			binding := &table.Bindings[i]
			cmp := binding.Candidates[0].ComparisonKey
			if got, ok := bindingIndex(table, cmp, nil); !ok || got != i {
				t.Fatalf("%s: index(%q) = %d, %t; want %d", name, cmp, got, ok, i)
			}
			wantTop, wantCompile := 0, 0
			for j, c := range binding.Candidates {
				if c.Available {
					wantTop = j + 1
					if c.CompileVisible {
						wantCompile = j + 1
					}
				}
			}
			if binding.top != wantTop || binding.topCompile != wantCompile {
				t.Fatalf("%s: %s cache = %d/%d, want %d/%d", name, binding.Name, binding.top, binding.topCompile, wantTop, wantCompile)
			}
		}
	}
	// the copies must not share state
	if reflect.DeepEqual(left.Bindings, right.Bindings) || len(base.Bindings) != 7 {
		t.Fatal("cloned tables share state or the base changed")
	}
	// a table without an index is read without being mutated
	bare := Table{Bindings: base.Bindings}
	if i, ok := bindingIndex(&bare, key("v3"), nil); !ok || bare.bindingIndex != nil || base.Bindings[i].Name != "v3" {
		t.Fatalf("read-only fallback failed: %d, %t, index stored = %t", i, ok, bare.bindingIndex != nil)
	}
}

func BenchmarkEvaluateReservedSameLine(b *testing.B) {
	for _, count := range []int{10_000, 20_000, 40_000} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			script := "# " + strings.Repeat("##vso[task.setvariable variable=path.x]v", count)
			pipeline := &ast.Pipeline{Shape: ast.ShapeSteps, Steps: list[ast.Step](&ast.ScriptStep{Kind: ast.ScriptBash, Script: scalarNode(script)})}
			for i := 0; i < b.N; i++ {
				if _, err := Evaluate(pipeline, Context{AgentOS: AgentOSLinux}, diag.NewCollector()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
