package parse

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/asttest"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
)

var update = flag.Bool("update", false, "rewrite the golden dump files under testdata/azurepipelines/structure")

func structureDir() string {
	return filepath.Join("..", "..", "..", "..", "testdata", "azurepipelines", "structure")
}

// parseText parses with the production entry point and returns the sorted findings as CODE@line:col.
func parseText(name, text string) (*ast.Pipeline, []string) {
	collector := diag.NewCollector()
	pipeline := Source(name, []byte(text), collector)
	return pipeline, findings(collector)
}

func findings(collector *diag.Collector) []string {
	diagnostics := collector.List()
	if len(diagnostics) == 0 {
		return nil
	}
	out := make([]string, 0, len(diagnostics))
	for _, d := range diagnostics {
		out = append(out, fmt.Sprintf("%s@%d:%d", d.Code, d.Source.Line, d.Source.Col))
	}
	return out
}

// containsInOrder reports whether want is a subsequence of got.
func containsInOrder(got, want []string) (string, bool) {
	next := 0
	for _, line := range got {
		if next < len(want) && line == want[next] {
			next++
		}
	}
	if next == len(want) {
		return "", true
	}
	return want[next], false
}

func TestStructVectors(t *testing.T) {
	vectors := asttest.LoadVectors(t)
	if len(vectors) != 142 {
		t.Fatalf("loaded %d STRUCT vectors, want 142", len(vectors))
	}
	seen := map[string]bool{}
	for _, v := range vectors {
		if seen[v.ID] {
			t.Fatalf("duplicate vector %s", v.ID)
		}
		seen[v.ID] = true
		t.Run(v.ID, func(t *testing.T) {
			pipeline, got := parseText(v.ID+".yml", v.Input)
			if diff := cmp.Diff(v.Diagnostics, got); diff != "" {
				t.Errorf("diagnostics mismatch (-want +got):\n%s", diff)
			}
			if missing, ok := containsInOrder(asttest.Dump(pipeline, false), v.Expected); !ok {
				t.Errorf("expected dump line not found (or out of order): %q\ndump:\n%s",
					missing, strings.Join(asttest.Dump(pipeline, false), "\n"))
			}
		})
	}
}

// expectation reads the `# expect:` header of a structure fixture.
func expectation(t *testing.T, text string) []string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(line, "# expect:"); ok {
			rest = strings.TrimSpace(rest)
			if rest == "none" {
				return nil
			}
			return strings.Fields(rest)
		}
	}
	t.Fatal("fixture has no # expect: header")
	return nil
}

func TestStructureFixtures(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(structureDir(), "*.yml"))
	if err != nil || len(files) < 10 {
		t.Fatalf("fixtures: %d files, err %v", len(files), err)
	}
	for _, file := range files {
		name := filepath.Base(file)
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			pipeline, got := parseText(name, string(data))
			if diff := cmp.Diff(expectation(t, string(data)), got); diff != "" {
				t.Errorf("diagnostics mismatch (-want +got):\n%s", diff)
			}
			dump := strings.Join(asttest.Dump(pipeline, true), "\n") + "\n"
			golden := strings.TrimSuffix(file, ".yml") + ".dump"
			if *update {
				if err := os.WriteFile(golden, []byte(dump), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden (run with -update): %v", err)
			}
			if diff := cmp.Diff(strings.ReplaceAll(string(want), "\r\n", "\n"), dump); diff != "" {
				t.Errorf("dump mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// pos is shorthand for the line and column of a node.
func lc(source diag.Source) [2]int { return [2]int{source.Line, source.Col} }

const positionsFixture = `# comment
name: demo
stages:
- stage: Build
  condition: >-
    and(succeeded(),
    eq(1, 1))
  jobs:
  - job: A
    steps:
    - script: echo
    - ${{ if eq(parameters.x, 'y') }}:
      - task: Foo@1
        inputs:
          a: "q"
          ${{ if eq(1, 1) }}:
            b: c
        displayName: Deploy ${{ parameters.env }}
`

func TestPositionsAreExact(t *testing.T) {
	pipeline, found := parseText("p.yml", positionsFixture)
	if len(found) != 0 {
		t.Fatalf("unexpected findings: %v", found)
	}
	stage := pipeline.Stages.Items[0].Value.(*ast.StageDef)
	job := stage.Jobs.Items[0].Value.(*ast.JobDef)
	script := job.Steps.Items[0].Value.(*ast.ScriptStep)
	cond := job.Steps.Items[1].Cond
	task := cond.Items.Items[0].Value.(*ast.TaskStep)
	inputs := task.Inputs
	checks := []struct {
		name string
		got  diag.Source
		want [2]int
	}{
		{"pipeline root", pipeline.Source, [2]int{2, 1}},
		{"name", pipeline.Name.Source, [2]int{2, 7}},
		{"stages list", pipeline.Stages.Source, [2]int{4, 1}},
		{"stage mapping", stage.Source, [2]int{4, 3}},
		{"stage id", stage.Stage.Source, [2]int{4, 10}},
		{"condition (folded scalar)", stage.Condition.Source, [2]int{5, 14}},
		{"jobs list", stage.Jobs.Source, [2]int{9, 3}},
		{"job mapping", job.Source, [2]int{9, 5}},
		{"job id", job.Job.Source, [2]int{9, 10}},
		{"steps list", job.Steps.Source, [2]int{11, 5}},
		{"script step", script.Source, [2]int{11, 7}},
		{"script text", script.Script.Source, [2]int{11, 15}},
		{"if directive (key)", cond.Directive.Source, [2]int{12, 7}},
		{"task step", task.Source, [2]int{13, 9}},
		{"task name", task.Task.Source, [2]int{13, 15}},
		{"inputs mapping", inputs.Source, [2]int{15, 11}},
		{"input key a", inputs.Entries[0].Key.Source, [2]int{15, 11}},
		{"input value (double quoted)", inputs.Entries[0].Value.Source, [2]int{15, 14}},
		{"nested if directive (key)", inputs.Entries[1].Cond.Directive.Source, [2]int{16, 11}},
		{"nested body mapping", inputs.Entries[1].Cond.Body.Source, [2]int{17, 13}},
		{"nested body value", inputs.Entries[1].Cond.Body.Entries[0].Value.Source, [2]int{17, 16}},
		{"displayName", task.DisplayName.Source, [2]int{18, 22}},
		{"embedded insert directive", task.DisplayName.Directives[0].Source, [2]int{18, 29}},
	}
	for _, check := range checks {
		if got := lc(check.got); got != check.want {
			t.Errorf("%s: position %v, want %v", check.name, got, check.want)
		}
	}
	if end := stage.Stage.Source; end.EndLine != 4 || end.EndCol != 15 {
		t.Errorf("stage id end = %d:%d, want 4:15", end.EndLine, end.EndCol)
	}
}

func TestExpressionSpansAreMappedThroughTheScalarStyle(t *testing.T) {
	pipeline, _ := parseText("p.yml", positionsFixture)
	stage := pipeline.Stages.Items[0].Value.(*ast.StageDef)
	condition := stage.Condition
	if condition.Value != "and(succeeded(), eq(1, 1))" || condition.Expr == nil {
		t.Fatalf("condition = %q, expr nil=%v", condition.Value, condition.Expr == nil)
	}
	call := condition.Expr.(*expr.Call)
	eq := call.Args[1].(*expr.Call)
	// "and(succeeded(), " is 17 bytes; the folded scalar continues on the next line.
	sources := []struct {
		name string
		span expr.Span
		want [2]int
	}{
		{"and call starts", call.Range(), [2]int{6, 5}},
		{"succeeded() call", call.Args[0].Range(), [2]int{6, 9}},
		{"eq call starts on the second folded line", eq.Range(), [2]int{7, 5}},
		{"literal 1", eq.Args[0].Range(), [2]int{7, 8}},
	}
	for _, check := range sources {
		got, ok := condition.SourceOf(check.span)
		if !ok {
			t.Fatalf("%s: no mapped source for span %v", check.name, check.span)
		}
		if lc(got) != check.want {
			t.Errorf("%s: %v, want %v", check.name, lc(got), check.want)
		}
	}
	end, _ := condition.SourceOf(call.Range())
	if end.EndLine != 7 || end.EndCol != 14 {
		t.Errorf("expression end = %d:%d, want 7:14", end.EndLine, end.EndCol)
	}

	job := stage.Jobs.Items[0].Value.(*ast.JobDef)
	directive := job.Steps.Items[1].Cond.Directive
	if directive.Kind != ast.DirectiveIf || directive.Expr == nil {
		t.Fatalf("directive = %+v", directive)
	}
	member := directive.Expr.(*expr.Call).Args[0].(*expr.Member)
	got, ok := directive.SourceOf(member.Range())
	if !ok || lc(got) != [2]int{12, 17} {
		t.Errorf("parameters.x inside the directive key at %v (ok=%v), want 12:17", lc(got), ok)
	}
}

func TestSyntaxErrorColumnsInQuotedAndFoldedScalars(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"plain", "steps:\n- script: x\n  condition: and(succeeded(), eq(1,))\n", []string{"TRL-EXPR-0001@3:36"}},
		{"double quoted", "steps:\n- script: x\n  condition: \"and(succeeded(), eq(1,))\"\n", []string{"TRL-EXPR-0001@3:37"}},
		{"double quoted with escape", "steps:\n- script: x\n  condition: \"eq(variables.a, 'x\\ty', )\"\n", []string{"TRL-EXPR-0001@3:39"}},
		{"single quoted with doubled quote", "steps:\n- script: x\n  condition: 'eq(variables.a, ''x'', )'\n", []string{"TRL-EXPR-0001@3:38"}},
		{"folded", "steps:\n- script: x\n  condition: >-\n    and(succeeded(),\n    eq(1,))\n", []string{"TRL-EXPR-0001@5:10"}},
		{"literal block", "steps:\n- script: x\n  condition: |\n    and(succeeded(),\n    eq(1,))\n", []string{"TRL-EXPR-0001@5:10"}},
		{"directive key", "steps:\n- ${{ if eq(parameters.x }}:\n  - script: a\n", []string{"TRL-EXPR-0001@2:25"}},
		{"embedded insertion", "steps:\n- script: a\n  displayName: x ${{ eq(1,) }} y\n", []string{"TRL-EXPR-0001@3:27"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, got := parseText("p.yml", test.text)
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestUnknownKeysArePreservedWithPosition(t *testing.T) {
	text := "steps:\n- script: a\n  shell: zsh\n  nested:\n    k: [1, 2]\n"
	pipeline, got := parseText("p.yml", text)
	if diff := cmp.Diff([]string{"TRL-PARSE-0002@3:3", "TRL-PARSE-0002@4:3"}, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	step := pipeline.Steps.Items[0].Value.(*ast.ScriptStep)
	if len(step.Extra) != 2 || step.Extra[0].Key.Value != "shell" || step.Extra[1].Key.Value != "nested" {
		t.Fatalf("extra = %+v", step.Extra)
	}
	if lc(step.Extra[0].Key.Source) != [2]int{3, 3} || lc(step.Extra[0].Value.Source) != [2]int{3, 10} {
		t.Errorf("extra[0] positions key %v value %v", lc(step.Extra[0].Key.Source), lc(step.Extra[0].Value.Source))
	}
	nested := step.Extra[1].Value
	if nested.Kind != ast.AnyMap || nested.Entries[0].Value.Kind != ast.AnySeq || nested.Entries[0].Value.Items[1].Scalar.Value != "2" {
		t.Errorf("nested value not preserved: %+v", nested)
	}
}

func TestInputsAndMatrixKeepSourceOrder(t *testing.T) {
	var text strings.Builder
	text.WriteString("steps:\n- task: A@1\n  inputs:\n")
	keys := []string{"zeta", "alpha", "mid", "Alpha2", "beta", "omega", "b", "a"}
	for _, key := range keys {
		fmt.Fprintf(&text, "    %s: v\n", key)
	}
	pipeline, got := parseText("p.yml", text.String())
	if len(got) != 0 {
		t.Fatal(got)
	}
	task := pipeline.Steps.Items[0].Value.(*ast.TaskStep)
	order := make([]string, 0, len(task.Inputs.Entries))
	for _, entry := range task.Inputs.Entries {
		order = append(order, entry.Key.Value)
	}
	if diff := cmp.Diff(keys, order); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

func TestTaskReferences(t *testing.T) {
	tests := []struct {
		raw  string
		want ast.TaskRef
		bad  bool
	}{
		{"CmdLine@2", ast.TaskRef{Name: "CmdLine", Version: "2", Major: 2, Valid: true}, false},
		{"GoTool@0.3.1", ast.TaskRef{Name: "GoTool", Version: "0.3.1", Major: 0, Valid: true}, false},
		{"5e1e3830-fbfb-11e5-aab1-090c92bc4988@1", ast.TaskRef{
			Name: "5e1e3830-fbfb-11e5-aab1-090c92bc4988", IsGUID: true, Version: "1", Major: 1, Valid: true,
		}, false},
		{"My.Task-X_1@12.4", ast.TaskRef{Name: "My.Task-X_1", Version: "12.4", Major: 12, Valid: true}, false},
		{"${{ parameters.t }}", ast.TaskRef{Name: "${{ parameters.t }}", Major: -1, Dynamic: true, Valid: true}, false},
		{"$(t)@1", ast.TaskRef{Name: "$(t)@1", Major: -1, Dynamic: true, Valid: true}, false},
		{"CmdLine", ast.TaskRef{Major: -1}, true},
		{"@1", ast.TaskRef{Major: -1}, true},
		{"Foo@", ast.TaskRef{Major: -1}, true},
		{"Foo@latest", ast.TaskRef{Name: "Foo", Major: -1}, true},
		{"Foo@1.2.3.4", ast.TaskRef{Name: "Foo", Major: -1}, true},
		{"Foo Bar@1", ast.TaskRef{Major: -1}, true},
		{"Foo@99999999999999999999", ast.TaskRef{Name: "Foo", Major: -1}, true},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			collector := diag.NewCollector()
			p := &parser{c: collector, limit: 10}
			scalar := &ast.Scalar{Value: test.raw}
			if strings.Contains(test.raw, "${{") {
				scalar.Directives = []*ast.Directive{{Raw: test.raw}}
			}
			got := p.taskRef(scalar)
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
			if bad := len(collector.List()) > 0; bad != test.bad {
				t.Errorf("diagnostic reported = %v, want %v", bad, test.bad)
			}
		})
	}
}

func TestClassifyKeyAndCloseTemplate(t *testing.T) {
	keys := []struct {
		key  string
		want ast.DirectiveKind
	}{
		{"${{ if eq(a, b) }}", ast.DirectiveIf},
		{"  ${{ elseif x }}  ", ast.DirectiveElseIf},
		{"${{ else }}", ast.DirectiveElse},
		{"${{ each x in y }}", ast.DirectiveEach},
		{"${{ else if x }}", ast.DirectiveElse},
		{"${{ parameters.key }}", 0},
		{"${{ }}", 0},
		{"${{}}", 0},
		{"if x", 0},
		{"${{ if x", 0},
	}
	for _, test := range keys {
		got, _ := classifyKey(test.key)
		if got != test.want {
			t.Errorf("classifyKey(%q) = %v, want %v", test.key, got, test.want)
		}
	}
	closes := []struct {
		value string
		from  int
		want  int
	}{
		{"${{ a }}", 3, 8},
		{"${{ format('}}', x) }} tail", 3, 22},
		{"${{ a }} ${{ b }}", 3, 8},
		{"${{ 'it''s' }}", 3, 14},
		{"${{ a", 3, -1},
		{"${{ a } }", 3, -1},
	}
	for _, test := range closes {
		if got := closeTemplate(test.value, test.from); got != test.want {
			t.Errorf("closeTemplate(%q) = %d, want %d", test.value, got, test.want)
		}
	}
}

func TestSourceReportsYAMLProblems(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"syntax error", "steps: [\n", []string{"TRL-PARSE-0001@1:1"}},
		{"duplicate key", "name: a\nname: b\n", []string{"TRL-PARSE-0003@2:1"}},
		{"invalid utf8", "name: \xff\n", []string{"TRL-PARSE-0004@1:7"}},
		{"empty", "", nil},
		{"only comment", "# nothing\n", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pipeline, got := parseText("p.yml", test.text)
			if pipeline == nil {
				t.Fatal("nil pipeline")
			}
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
	big := make([]byte, yamlx.DefaultSizeLimit+1)
	collector := diag.NewCollector()
	if pipeline := Source("big.yml", big, collector); pipeline == nil || findings(collector)[0] != "TRL-PARSE-0005@1:1" {
		t.Errorf("oversize input: %v", findings(collector))
	}
}

func TestDocumentEntryPoint(t *testing.T) {
	// A document read with anchors allowed: parse.Document still reports the
	// alias it reaches; the anchor itself is the YAML layer's finding.
	document, problems := yamlx.Parse("d.yml", []byte("variables:\n  shared: &a value\nsteps:\n- script: x\n  displayName: *a\n"))
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	collector := diag.NewCollector()
	pipeline := Document(document, collector)
	if diff := cmp.Diff([]string{"TRL-PARSE-0008@5:16"}, findings(collector)); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	if step := pipeline.Steps.Items[0].Value.(*ast.ScriptStep); step.DisplayName.Value != "value" {
		t.Errorf("alias value = %q", step.DisplayName.Value)
	}
	empty, _ := yamlx.Parse("e.yml", nil)
	if got := Document(empty, diag.NewCollector()); got == nil || got.Source.File != "e.yml" {
		t.Errorf("empty document: %+v", got)
	}
}

func TestNestingIsBounded(t *testing.T) {
	deep := "extra: " + strings.Repeat("[", 1500) + strings.Repeat("]", 1500) + "\n"
	document, _ := yamlx.ParseWithOptions("deep.yml", []byte(deep), yamlx.WithDepthLimit(0))
	collector := diag.NewCollector()
	Document(document, collector)
	codes := strings.Join(findings(collector), " ")
	if !strings.Contains(codes, "TRL-PARSE-0010") {
		t.Errorf("no depth diagnostic: %s", codes)
	}
	// An alias cycle must not recurse forever either.
	cycle, _ := yamlx.ParseWithOptions("cycle.yml", []byte("a: &x [*x]\n"), yamlx.WithAliasExpansionLimit(0), yamlx.WithDepthLimit(0))
	Document(cycle, diag.NewCollector())
}

func TestCollectorOrderIsDeterministic(t *testing.T) {
	text := "frobnicate: 1\nsteps:\n- script: a\n  shell: zsh\n- task: Foo\n- displayName: nothing\n"
	want, _ := parseText("p.yml", text)
	_ = want
	var first []string
	for range 20 {
		_, got := parseText("p.yml", text)
		if first == nil {
			first = got
			continue
		}
		if diff := cmp.Diff(first, got); diff != "" {
			t.Fatalf("diagnostics order changed between runs:\n%s", diff)
		}
	}
	if len(first) != 4 {
		t.Errorf("findings = %v", first)
	}
}

func TestDirectiveShapesInTheTree(t *testing.T) {
	text := `steps:
- ${{ if eq(1, 1) }}:
  - script: a
- ${{ else }}:
  - script: b
- ${{ each p in parameters.list }}:
  - ${{ p }}
- ${{ parameters.more }}
- script: c
  ${{ if eq(1, 1) }}:
    displayName: d
  ${{ pair.key }}: v
`
	pipeline, got := parseText("p.yml", text)
	if len(got) != 0 {
		t.Fatal(got)
	}
	items := pipeline.Steps.Items
	if len(items) != 5 {
		t.Fatalf("items = %d", len(items))
	}
	if items[0].Cond.Directive.Kind != ast.DirectiveIf || items[1].Cond.Directive.Kind != ast.DirectiveElse {
		t.Errorf("if/else kinds: %v %v", items[0].Cond.Directive.Kind, items[1].Cond.Directive.Kind)
	}
	each := items[2].Cond
	if each.Directive.Kind != ast.DirectiveEach || each.Directive.Variable != "p" || each.Items.Items[0].Insert == nil {
		t.Errorf("each item = %+v", each.Directive)
	}
	if items[3].Insert == nil || items[3].Insert.Kind != ast.DirectiveInsert {
		t.Errorf("whole-value insertion item = %+v", items[3])
	}
	step := items[4].Value.(*ast.ScriptStep)
	if len(step.Inserts) != 1 || step.Inserts[0].Body.DisplayName.Value != "d" {
		t.Errorf("step directive branch = %+v", step.Inserts)
	}
	if len(step.Extra) != 1 || step.Extra[0].Key.Directives == nil {
		t.Errorf("dynamic key = %+v", step.Extra)
	}
}
