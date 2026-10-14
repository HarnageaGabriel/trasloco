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
func reservedMarkerWork(t *testing.T, name string, count int, perLine bool) *evaluationWork {
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
	return counter
}

func TestReviewR10ReservedNameMarkersScaleLinearly(t *testing.T) {
	for _, perLine := range []bool{false, true} {
		for _, name := range []string{"path.x", "x"} {
			small := reservedMarkerWork(t, name, 500, perLine)
			large := reservedMarkerWork(t, name, 1000, perLine)
			t.Logf("%s perLine=%t: %d -> %d", name, perLine, small.total(), large.total())
			assertLinearGrowth(t, small.total(), large.total())
			assertWorkBudget(t, 1000, large)
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

// TestReviewR10StepSnapshotLookupsStayLinear pins the read cost of the tables
// the evaluator stores for every step: a snapshot cloned from the job state
// must answer each macro reference with an index hit, not with a scan over its
// bindings. Without that, N variables and N references in each of M steps cost
// O(N*N*M) counted binding scans instead of O(N*M); the counters of the logging
// budget tests never reach this path.
func TestReviewR10StepSnapshotLookupsStayLinear(t *testing.T) {
	const (
		variables  = 200
		references = 200
		steps      = 8
	)
	work := &evaluationWork{}
	var state Table
	for i := 0; i < variables; i++ {
		name := "v" + strconv.Itoa(i)
		add(&state, Candidate{Name: name, ComparisonKey: key(name), Value: name, Available: true, CompileVisible: true})
	}
	for step := 0; step < steps; step++ {
		snapshot := cloneTable(state)
		for r := 0; r < references; r++ {
			name := "v" + strconv.Itoa(r)
			candidate, ok := lookupWork(&snapshot, name, PhaseMacro, work)
			if !ok || candidate.Value != name {
				t.Fatalf("step %d: %s = %#v, %t", step, name, candidate, ok)
			}
		}
		// the step mutates the state the next snapshot is cloned from
		name := "s" + strconv.Itoa(step)
		add(&state, Candidate{Name: name, ComparisonKey: key(name), Value: name, Available: true, CompileVisible: true})
	}
	t.Logf("lookups=%d scans=%d", steps*references, work.bindingScans)
	if want := 3 * (variables + references); work.bindingScans > want {
		t.Fatalf("binding scans for %d variables and %d references per step = %d, want at most %d", variables, references, work.bindingScans, want)
	}
}

// TestReviewR10SnapshotIndexIsSharedButNeverStale proves a clone may share the
// parent's index: bindings are append-only, so a position the clone knows can
// never start naming a different binding, and a position the parent added after
// the clone was taken is rejected by the lookup's own check.
func TestReviewR10SnapshotIndexIsSharedButNeverStale(t *testing.T) {
	var parent Table
	for i := 0; i < 8; i++ {
		name := "v" + strconv.Itoa(i)
		add(&parent, Candidate{Name: name, ComparisonKey: key(name), Value: "old" + strconv.Itoa(i), Available: true, CompileVisible: true})
	}
	snapshot := cloneTable(parent)
	// the parent grows both ways: new candidates for existing bindings and a
	// binding the snapshot never had
	for i := 0; i < 8; i++ {
		name := "v" + strconv.Itoa(i)
		add(&parent, Candidate{Name: name, ComparisonKey: key(name), Value: "new" + strconv.Itoa(i), Available: true, CompileVisible: true})
	}
	add(&parent, Candidate{Name: "late", ComparisonKey: key("late"), Value: "late", Available: true, CompileVisible: true})
	for i := 0; i < 8; i++ {
		name := "v" + strconv.Itoa(i)
		if candidate, ok := lookup(&snapshot, name, PhaseMacro); !ok || candidate.Value != "old"+strconv.Itoa(i) {
			t.Fatalf("snapshot %s = %#v, %t; want old%d", name, candidate, ok, i)
		}
	}
	if _, ok := lookup(&snapshot, "late", PhaseMacro); ok {
		t.Fatal("the snapshot resolved a binding the parent added after the clone")
	}
	if candidate, ok := lookup(&parent, "v0", PhaseMacro); !ok || candidate.Value != "new0" {
		t.Fatalf("parent v0 = %#v, %t; want new0", candidate, ok)
	}
}

// TestReviewR10BindingIndexRejectsStaleSharedPosition covers the key check
// bindingIndex runs on a shared index. A clone holds the parent's map by
// reference, so a binding the parent appends after the clone lands in that map
// at a position the clone does not have -- and the position the clone does have
// may carry a different key. Without the check the lookup trusts the stale
// position and answers with the wrong binding, which nothing else observes.
func TestReviewR10BindingIndexRejectsStaleSharedPosition(t *testing.T) {
	var p Table
	add(&p, Candidate{Name: "a", ComparisonKey: key("a"), Value: "va", Available: true, CompileVisible: true})
	add(&p, Candidate{Name: "b", ComparisonKey: key("b"), Value: "vb", Available: true, CompileVisible: true})
	c := cloneTable(p)
	if !c.sharedIndex {
		t.Fatal("the clone does not share the parent's index")
	}
	add(&p, Candidate{Name: "x", ComparisonKey: key("x"), Value: "vx", Available: true, CompileVisible: true})
	add(&c, Candidate{Name: "y", ComparisonKey: key("y"), Value: "vy", Available: true, CompileVisible: true})

	// the shared map now names x at index 2, a position c does not have; the key
	// at that position is y, so the lookup must fall back to the scan and miss
	if i, ok := bindingIndex(&c, key("x"), nil); ok || i != 0 {
		t.Fatalf("bindingIndex(c, x) = %d, %t; want 0, false (x lives in p only)", i, ok)
	}
	if i, ok := bindingIndex(&p, key("x"), nil); !ok || i != 2 {
		t.Fatalf("bindingIndex(p, x) = %d, %t; want 2, true", i, ok)
	}
	for _, tc := range []struct{ name, want string }{{"y", "vy"}, {"a", "va"}, {"b", "vb"}} {
		candidate, ok := lookup(&c, tc.name, PhaseMacro)
		if !ok || candidate.Value != tc.want {
			t.Fatalf("c %s = %#v, %t; want %q", tc.name, candidate, ok, tc.want)
		}
	}
	if _, ok := lookup(&c, "x", PhaseMacro); ok {
		t.Fatal("c resolved x, which only p has")
	}
	for _, tc := range []struct{ name, want string }{{"x", "vx"}, {"a", "va"}, {"b", "vb"}} {
		candidate, ok := lookup(&p, tc.name, PhaseMacro)
		if !ok || candidate.Value != tc.want {
			t.Fatalf("p %s = %#v, %t; want %q", tc.name, candidate, ok, tc.want)
		}
	}
}

// TestReviewR10ManyStepsIndexWorkStaysLinear is the deterministic guard for the
// per-step index rebuild #11 removed: rebuilding inside cloneTable wrote one
// index entry per binding per clone, so N variables and M steps paid N*M of
// them, while the shared index is built once and copied only on a clone's first
// write. Two assertions pin that: every clone must hand back the parent's own
// map (a rebuild hands back a private one), and the clones must add nothing to
// indexBuilds, whose total stays within c*(N+M) -- a rebuild that counted its
// entries would blow the bound.
func TestReviewR10ManyStepsIndexWorkStaysLinear(t *testing.T) {
	const (
		variables = 120
		steps     = 40
	)
	work := &evaluationWork{}
	var state Table
	for i := 0; i < variables; i++ {
		name := "v" + strconv.Itoa(i)
		addWithWork(&state, Candidate{Name: name, ComparisonKey: key(name), Value: name, Available: true, CompileVisible: true}, work)
	}
	if work.indexBuilds != variables {
		t.Fatalf("index builds after %d variables = %d, want %d", variables, work.indexBuilds, variables)
	}
	for step := 0; step < steps; step++ {
		snapshot := cloneTable(state)
		if snapshot.bindingIndex == nil || !snapshot.sharedIndex {
			t.Fatalf("step %d: the snapshot does not share the parent's index", step)
		}
		shared := snapshot.bindingIndex
		if len(shared) != len(state.Bindings) {
			t.Fatalf("step %d: snapshot index has %d keys for %d bindings", step, len(shared), len(state.Bindings))
		}
		// the step adds nothing to its snapshot, exactly like the evaluator: the
		// job state grows and the next snapshot inherits it. A rebuild would show
		// up here, because the map the snapshot handed out is not the one the
		// parent writes into unless the clone shares it.
		name := "s" + strconv.Itoa(step)
		addWithWork(&state, Candidate{Name: name, ComparisonKey: key(name), Value: name, Available: true, CompileVisible: true}, work)
		if len(shared) != len(state.Bindings) {
			t.Fatalf("step %d: the snapshot's index is not the parent's (%d keys for %d bindings)", step, len(shared), len(state.Bindings))
		}
	}
	t.Logf("index builds for %d variables and %d steps = %d", variables, steps, work.indexBuilds)
	if want := variables + steps; work.indexBuilds != want {
		t.Fatalf("index builds = %d, want %d (one per binding, none per clone)", work.indexBuilds, want)
	}
	if limit := 4 * (variables + steps); work.indexBuilds > limit {
		t.Fatalf("index builds = %d, want at most %d", work.indexBuilds, limit)
	}
}

// TestReviewR10IndexBuildsCountBothPaths pins the accounting the scaling guard
// reads: a table that has bindings but no map counts every binding it indexes,
// and the first write to a clone counts the shared map it copies. Neither path
// changes a value, so the counters cannot alter any output.
func TestReviewR10IndexBuildsCountBothPaths(t *testing.T) {
	work := &evaluationWork{}
	var base Table
	for i := 0; i < 5; i++ {
		name := "v" + strconv.Itoa(i)
		addWithWork(&base, Candidate{Name: name, ComparisonKey: key(name), Value: name, Available: true, CompileVisible: true}, work)
	}
	if want := 5; work.indexBuilds != want {
		t.Fatalf("index builds for five appended bindings = %d, want %d", work.indexBuilds, want)
	}
	// an unindexed table with bindings builds its index from every binding
	rebuild := Table{Bindings: make([]Binding, len(base.Bindings))}
	copy(rebuild.Bindings, base.Bindings)
	addWithWork(&rebuild, Candidate{Name: "w", ComparisonKey: key("w"), Value: "w", Available: true, CompileVisible: true}, work)
	built := 5 + 5 + 1
	if work.indexBuilds != built {
		t.Fatalf("index builds after a fresh build over five bindings = %d, want %d", work.indexBuilds, built)
	}
	// a clone copies the shared map before its first write
	clone := cloneTable(base)
	addWithWork(&clone, Candidate{Name: "c", ComparisonKey: key("c"), Value: "c", Available: true, CompileVisible: true}, work)
	if want := built + len(base.bindingIndex) + 1; work.indexBuilds != want {
		t.Fatalf("index builds after a copy-on-write = %d, want %d", work.indexBuilds, want)
	}
	if len(base.bindingIndex) != len(base.Bindings) || clone.sharedIndex {
		t.Fatal("the copy-on-write changed the parent's index or left the clone sharing")
	}
	if _, ok := lookup(&base, "c", PhaseMacro); ok {
		t.Fatal("the clone's write leaked into the parent")
	}
	if candidate, ok := lookup(&clone, "c", PhaseMacro); !ok || candidate.Value != "c" {
		t.Fatalf("clone c = %#v, %t; want c", candidate, ok)
	}
}

// TestReviewR10FindingCacheKeepsDistinctFindings guards the pre-construction
// dedup of TRL-VAR-0016: only an identical code, origin and name may collapse.
func TestReviewR10FindingCacheKeepsDistinctFindings(t *testing.T) {
	collector := diag.NewCollector()
	var cache findingCache
	origin := diag.Source{File: "p.yml", Line: 3, Col: 5}
	cache.addName(collector, "TRL-VAR-0016", origin, "x")
	cache.addName(collector, "TRL-VAR-0016", origin, "x")
	cache.addName(collector, "TRL-VAR-0016", origin, "y")
	cache.addName(collector, "TRL-VAR-0016", diag.Source{File: "p.yml", Line: 4, Col: 1}, "x")
	cache.addName(collector, "TRL-VAR-0015", origin, "x")
	if got := len(collector.List()); got != 4 {
		t.Fatalf("stored diagnostics = %d, want 4: %v", got, codes(collector.List()))
	}
	if got := countCode(collector.List(), "TRL-VAR-0016"); got != 3 {
		t.Fatalf("TRL-VAR-0016 = %d, want 3", got)
	}
	if got := len(collector.Suppressed()); got != 0 {
		t.Fatalf("suppressed = %v, want none", collector.Suppressed())
	}
}

// TestReviewR10FindingCacheKeepsSuppressedCounts keeps the per-code cap
// semantics the collector had before the cache: a finding the cap suppressed is
// not remembered as stored, so its repeats still count as suppressed.
func TestReviewR10FindingCacheKeepsSuppressedCounts(t *testing.T) {
	collector := diag.NewCollector(diag.WithPerCodeCap(1))
	var cache findingCache
	origin := diag.Source{File: "p.yml", Line: 3, Col: 5}
	cache.addName(collector, "TRL-VAR-0016", origin, "a")
	cache.addName(collector, "TRL-VAR-0016", origin, "b")
	cache.addName(collector, "TRL-VAR-0016", origin, "b")
	if got := countCode(collector.List(), "TRL-VAR-0016"); got != 1 {
		t.Fatalf("stored TRL-VAR-0016 = %d, want 1", got)
	}
	if got := collector.Suppressed()["TRL-VAR-0016"]; got != 2 {
		t.Fatalf("suppressed TRL-VAR-0016 = %d, want 2 (the cache must not change it)", got)
	}
}

// BenchmarkEvaluateManyStepsSharedState measures the per-step snapshot the
// evaluator takes of the job table: every step deep-copies the bindings the
// previous steps added. It exercises the clone path that must not also rebuild
// a binding index per step.
func BenchmarkEvaluateManyStepsSharedState(b *testing.B) {
	for _, steps := range []int{200, 400, 800} {
		b.Run(strconv.Itoa(steps), func(b *testing.B) {
			items := make([]ast.Step, steps)
			for i := range items {
				script := "# ##vso[task.setvariable variable=v" + strconv.Itoa(i) + "]1"
				items[i] = &ast.ScriptStep{Kind: ast.ScriptBash, Script: scalarNode(script)}
			}
			pipeline := &ast.Pipeline{Shape: ast.ShapeSteps, Steps: list[ast.Step](items...)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := Evaluate(pipeline, Context{AgentOS: AgentOSLinux}, diag.NewCollector()); err != nil {
					b.Fatal(err)
				}
			}
		})
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
