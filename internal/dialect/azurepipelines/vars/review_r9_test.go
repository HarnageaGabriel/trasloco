package vars

import (
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
)

func TestReviewR9CmdLineLengthLimit(t *testing.T) {
	const prefix = "echo "
	atLimit := prefix + strings.Repeat("a", maxCmdLine-len(prefix))
	for _, tc := range []struct {
		name   string
		line   string
		proven bool
	}{
		{"at the limit", atLimit, true},
		{"over the limit", atLimit + "a", false},
	} {
		candidate, ok, findings := emissionRun(t, ast.ScriptCmd, AgentOSWindows, tc.line+"\necho "+markerM+"v")
		if tc.proven && (!ok || candidate.Unknown || candidate.Value != "v") {
			t.Fatalf("%s: m = %#v, %t; want proven", tc.name, candidate, ok)
		}
		if !tc.proven && (!ok || !candidate.Unknown || !hasCode(findings, "TRL-VAR-0016")) {
			t.Fatalf("%s: m = %#v, %t, %v; want Unknown with TRL-VAR-0016", tc.name, candidate, ok, codes(findings))
		}
	}
	// bash has no such limit.
	candidate, ok, _ := emissionRun(t, ast.ScriptBash, AgentOSLinux, atLimit+"aa\necho '"+markerM+"v'")
	if !ok || candidate.Unknown {
		t.Fatalf("bash long line: %#v, %t", candidate, ok)
	}
}

func TestReviewR9SplitMarkerWarnsTheStep(t *testing.T) {
	candidate, ok, findings := knownMacroRun(t, "e", "", "echo '##vso[task.set$(e)variable variable=m]v'")
	if ok && !candidate.Unknown {
		t.Fatalf("m = %#v; a split marker is not a marker", candidate)
	}
	if !hasCode(findings, "TRL-VAR-0016") {
		t.Fatalf("diagnostics = %v, want the step warning", codes(findings))
	}
}

func TestReviewR9HeaderPropertyNameMacroIsSecret(t *testing.T) {
	candidate, ok, findings := knownMacroRun(t, "p", "issecret", "echo '##vso[task.setvariable variable=m;$(p)=true]v'")
	if !ok || !candidate.Unknown || !candidate.Secret || !hasCode(findings, "TRL-VAR-0016") {
		t.Fatalf("m = %#v, %t, %v; want Unknown and Secret", candidate, ok, codes(findings))
	}
}

func TestReviewR9MacroInVariableNameWarnsTheStep(t *testing.T) {
	_, ok, findings := knownMacroRun(t, "n", "m", "echo '##vso[task.setvariable variable=$(n)]v'")
	if ok {
		t.Fatal("a macro-named variable must not be recorded under a guessed name")
	}
	if !hasCode(findings, "TRL-VAR-0016") {
		t.Fatalf("diagnostics = %v, want the step warning", codes(findings))
	}
}

func TestReviewR9OnlyRiskyMacrosWarn(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		warn        bool
	}{
		{"benign known value", "10", false},
		{"value with a marker", "##vso[task.setvariable variable=x]1", true},
	} {
		_, _, findings := knownMacroRun(t, "k", tc.value, "echo $(k)")
		if got := hasCode(findings, "TRL-VAR-0016"); got != tc.warn {
			t.Fatalf("%s: warning = %t, want %t", tc.name, got, tc.warn)
		}
	}
	if _, _, findings := knownMacroRun(t, "k", "10", "echo $(missing)"); !hasCode(findings, "TRL-VAR-0016") {
		t.Fatal("an unknown macro must warn")
	}
}

func TestReviewR9CmdEchoOffSplitsOnASCIIOnly(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		proven     bool
	}{
		{"space", "@echo off", true},
		{"tab", "@echo\toff", true},
		{"no-break space", "@echo off", false},
	} {
		candidate, ok, _ := emissionRun(t, ast.ScriptCmd, AgentOSWindows, tc.line+"\necho "+markerM+"v")
		if proven := ok && !candidate.Unknown && candidate.Value == "v"; proven != tc.proven {
			t.Fatalf("%s: proven = %t, want %t (%#v)", tc.name, proven, tc.proven, candidate)
		}
	}
}

func TestReviewR9SameLineMarkerScanScalesLinearly(t *testing.T) {
	work := func(count int) *evaluationWork {
		script := "# " + strings.Repeat("##vso[task.setvariable variable=x]v", count)
		step := &ast.ScriptStep{Kind: ast.ScriptBash, Script: scalarNode(script)}
		counter := &evaluationWork{}
		events := mutationsWithExpanderWork(step, newMacroExpander(&Table{}), "", "", "", 0, AgentOSLinux, counter)
		if len(events) != count {
			t.Fatalf("markers = %d, want %d", len(events), count)
		}
		if counter.scanBytes == 0 || counter.lineBytes == 0 {
			t.Fatalf("counters not wired: %+v", *counter)
		}
		return counter
	}
	small, large := work(500), work(1000)
	assertLinearGrowth(t, small.total(), large.total())
	assertWorkBudget(t, 1000, large)
}

func TestReviewR9DistinctMarkerBindingsScaleLinearly(t *testing.T) {
	work := func(count int) *evaluationWork {
		var script strings.Builder
		for i := 0; i < count; i++ {
			script.WriteString("# ##vso[task.setvariable variable=x")
			script.WriteString(strconv.Itoa(i))
			script.WriteString("]v\n")
		}
		step := &ast.ScriptStep{Kind: ast.ScriptBash, Script: scalarNode(script.String())}
		counter := &evaluationWork{}
		events := mutationsWithExpanderWork(step, newMacroExpander(&Table{}), "", "", "", 0, AgentOSLinux, counter)
		table := &Table{}
		for _, event := range events {
			applyMutationWork(table, event, ast.StepProps{}, diag.NewCollector(), counter)
		}
		if len(table.Bindings) != count {
			t.Fatalf("bindings = %d, want %d", len(table.Bindings), count)
		}
		return counter
	}
	small, large := work(500), work(1000)
	assertLinearGrowth(t, small.total(), large.total())
	assertWorkBudget(t, 1000, large)
}

func assertLinearGrowth(t *testing.T, small, large int) {
	t.Helper()
	ratio := float64(large) / float64(small)
	t.Logf("work ratio %.2fx (%d to %d)", ratio, small, large)
	if ratio > 2.5 {
		t.Fatalf("work grew by %.2fx (%d to %d); want <= 2.5x", ratio, small, large)
	}
}

// assertWorkBudget pins the counted work to the marker count, so the tests fix
// the constant and not only the slope. The growth checks above compare two
// sizes and pass for any counted work that is linear; a bound at one size fails
// as soon as the per-marker constant jumps, as it would if the cached candidate
// lookup were replaced by a scan over candidates or bindings (exactly what
// candidateSteps and bindingScans count). Neither check can see a loop the
// counters never reach, so a new loop must keep counting its iterations.
func assertWorkBudget(t *testing.T, markers int, work *evaluationWork) {
	t.Helper()
	t.Logf("work for %d markers: lines=%d scan=%d bindings=%d candidates=%d total=%d", markers, work.lineBytes, work.scanBytes, work.bindingScans, work.candidateSteps, work.total())
	if work.candidateSteps > 3*markers {
		t.Fatalf("candidate steps for %d markers = %d, want at most %d", markers, work.candidateSteps, 3*markers)
	}
	if work.bindingScans > 3*markers {
		t.Fatalf("binding scans for %d markers = %d, want at most %d", markers, work.bindingScans, 3*markers)
	}
	if work.total() > 200*markers {
		t.Fatalf("total work for %d markers = %d, want at most %d", markers, work.total(), 200*markers)
	}
}

func TestReviewR9LargeRepeatedMarkerInput(t *testing.T) {
	// This is the review's 50,000-marker shape at a test-sized count; the
	// counter tests above prove the remaining scale-up is linear.
	const count = 2_000
	script := "# " + strings.Repeat("##vso[task.setvariable variable=x]v", count)
	pipeline := &ast.Pipeline{Shape: ast.ShapeSteps, Steps: list[ast.Step](
		&ast.ScriptStep{Kind: ast.ScriptBash, Script: scalarNode(script)},
		&ast.ScriptStep{Kind: ast.ScriptBash, Script: scalarNode("echo consume")},
	)}
	collector := diag.NewCollector()
	graph, err := Evaluate(pipeline, Context{AgentOS: AgentOSLinux}, collector)
	if err != nil {
		t.Fatal(err)
	}
	candidate, ok := graph.Resolve(stepScope(1), "x", PhaseMacro)
	if !ok || !candidate.Unknown || candidate.Shadowed {
		t.Fatalf("effective x = %#v, %t; want unshadowed Unknown", candidate, ok)
	}
	if got := countCode(collector.List(), "TRL-VAR-0016"); got != 1 {
		t.Fatalf("TRL-VAR-0016 count = %d, want one deduplicated outcome", got)
	}
	table, _ := graph.Table(stepScope(1))
	i, ok := bindingIndex(table, key("x"), nil)
	if !ok || len(table.Bindings[i].Candidates) != count {
		t.Fatalf("x binding found = %t, want %d candidates", ok, count)
	}
	binding := &table.Bindings[i]
	for i, candidate := range binding.Candidates {
		if candidate.Shadowed != (i < count-1) {
			t.Fatalf("candidate %d Shadowed = %t", i, candidate.Shadowed)
		}
	}
}

func TestReviewR9ShadowingMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	names := []string{"x", "X", "y", "Y", "output.z", "OUTPUT.Z"}
	for trial := 0; trial < 500; trial++ {
		var got, want Table
		for i := 0; i < 1+rng.Intn(40); i++ {
			name := names[rng.Intn(len(names))]
			candidate := Candidate{
				Name:           name,
				ComparisonKey:  key(name),
				Value:          strconv.Itoa(rng.Intn(10)),
				Shadowed:       rng.Intn(2) == 0,
				Contested:      rng.Intn(2) == 0,
				Available:      rng.Intn(3) != 0,
				CompileVisible: rng.Intn(2) == 0,
			}
			add(&got, candidate)
			addReference(&want, candidate)
		}
		if !reflect.DeepEqual(got.Bindings, want.Bindings) {
			t.Fatalf("trial %d shadowing differs:\n got: %#v\nwant: %#v", trial, got.Bindings, want.Bindings)
		}
	}
}

func addReference(table *Table, candidate Candidate) {
	for i := range table.Bindings {
		if table.Bindings[i].Candidates[0].ComparisonKey == candidate.ComparisonKey {
			for j := range table.Bindings[i].Candidates {
				table.Bindings[i].Candidates[j].Shadowed = true
			}
			candidate.Shadowed = false
			table.Bindings[i].Candidates = append(table.Bindings[i].Candidates, candidate)
			table.Bindings[i].note(len(table.Bindings[i].Candidates) - 1)
			table.Bindings[i].Effective = len(table.Bindings[i].Candidates) - 1
			table.Bindings[i].Ambiguous = candidate.Contested
			return
		}
	}
	table.Bindings = append(table.Bindings, Binding{Name: candidate.Name, Candidates: []Candidate{candidate}, Effective: 0, Ambiguous: candidate.Contested})
	table.Bindings[len(table.Bindings)-1].note(0)
}
