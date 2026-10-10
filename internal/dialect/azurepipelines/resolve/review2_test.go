package resolve

import (
	"context"
	"errors"
	"math"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
)

func testResolver() (*resolver, *diag.Collector) {
	collector := diag.NewCollector()
	return &resolver{opts: Options{Collector: collector}, maxBytes: math.MaxInt}, collector
}

func TestRuntimeDirectiveOnAnyEntryIsNotALeftover(t *testing.T) {
	p := &ast.Pipeline{Parameters: &ast.Parameters{List: &ast.List[*ast.Parameter]{Items: []ast.ListItem[*ast.Parameter]{{
		Value: &ast.Parameter{Default: &ast.Any{Kind: ast.AnyMap, Entries: []*ast.AnyEntry{{
			Directive: &ast.Directive{Kind: ast.DirectiveRuntime, Raw: "$[ variables.x ]"},
		}}}},
	}}}}}
	if got := findLeftovers(p); len(got) != 0 {
		t.Fatalf("runtime AnyEntry is a leftover: %+v", got)
	}
	p.Parameters.List.Items[0].Value.Default.Entries[0].Directive.Kind = ast.DirectiveIf
	if got := findLeftovers(p); len(got) == 0 {
		t.Fatal("compile-time AnyEntry directive was not found")
	}
}

func TestInterpolationChargesOnlyItsDelta(t *testing.T) {
	input := "steps:\n- script: " + strings.Repeat("x${{ 'a' }}", 3000) + "\n"
	resolved, collector, _ := resolveText(t, input, nil, Options{})
	assertNoErrors(t, collector)
	if resolved == nil {
		t.Fatal("linear-size interpolation exceeded the expansion budget")
	}
}

func TestInsertKeyInTypedMappings(t *testing.T) {
	input := `parameters:
- name: pool
  type: object
  default:
    vmImage: ubuntu
- name: props
  type: object
  default:
    displayName: inserted
    continueOnError: true
pool:
  ${{ insert }}: ${{ parameters.pool }}
steps:
- script: echo hi
  ${{ insert }}: ${{ parameters.props }}
`
	resolved, collector, printed := resolveText(t, input, nil, Options{})
	assertNoErrors(t, collector)
	if resolved == nil || !strings.Contains(printed, "vmImage: ubuntu") || !strings.Contains(printed, "displayName: inserted") || !strings.Contains(printed, `continueOnError: "true"`) {
		t.Fatalf("typed insert not materialized:\n%s", printed)
	}
}

func TestInsertKeyAcrossTypedNodeKinds(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"pipeline", "parameters:\n- name: p\n  type: object\n  default: {name: inserted}\n${{ insert }}: ${{ parameters.p }}\nsteps: []\n", "name: inserted"},
		{"stage", "parameters:\n- name: p\n  type: object\n  default: {displayName: inserted}\nstages:\n- stage: S\n  ${{ insert }}: ${{ parameters.p }}\n  jobs: []\n", "displayName: inserted"},
		{"job", "parameters:\n- name: p\n  type: object\n  default: {displayName: inserted}\njobs:\n- job: J\n  ${{ insert }}: ${{ parameters.p }}\n  steps: []\n", "displayName: inserted"},
		{"deployment", "parameters:\n- name: p\n  type: object\n  default: {displayName: inserted}\njobs:\n- deployment: D\n  environment: prod\n  ${{ insert }}: ${{ parameters.p }}\n", "displayName: inserted"},
		{"task", "parameters:\n- name: p\n  type: object\n  default: {displayName: inserted}\nsteps:\n- task: Echo@1\n  ${{ insert }}: ${{ parameters.p }}\n", "displayName: inserted"},
		{"checkout", "parameters:\n- name: p\n  type: object\n  default: {displayName: inserted}\nsteps:\n- checkout: self\n  ${{ insert }}: ${{ parameters.p }}\n", "displayName: inserted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved, collector, printed := resolveText(t, test.input, nil, Options{})
			assertNoErrors(t, collector)
			if resolved == nil || !strings.Contains(printed, test.want) {
				t.Fatalf("typed insert not materialized:\n%s", printed)
			}
		})
	}
}

func TestExpandedEnumsUseTheirOwnValueSets(t *testing.T) {
	good := "parameters:\n- name: v\n  default: sequential\nlockBehavior: ${{ parameters.v }}\nsteps: []\n"
	resolved, collector, _ := resolveText(t, good, nil, Options{})
	assertNoErrors(t, collector)
	if resolved == nil || resolved.LockBehavior.Canon != "sequential" {
		t.Fatalf("lockBehavior = %#v", resolved)
	}
	bad := "parameters:\n- name: v\n  default: sequential\njobs:\n- job: J\n  workspace:\n    clean: ${{ parameters.v }}\n  steps: []\n"
	failure(t, bad, nil, Options{}, "TRL-PARSE-0012")
}

func TestMaterializedDataIsNotExpandedAgain(t *testing.T) {
	input := "steps:\n- script: hi\n  env: ${{ variables.payload }}\n"
	variables := &expr.Object{Properties: []expr.Property{{Name: "payload", Value: &expr.Object{Properties: []expr.Property{{Name: "literal", Value: expr.String("${{ 1 }}")}}}}}}
	resolved, collector, printed := resolveText(t, input, nil, Options{Variables: variables})
	assertNoErrors(t, collector)
	if resolved == nil || !strings.Contains(printed, "${{ 1 }}") {
		t.Fatalf("materialized literal was re-evaluated:\n%s", printed)
	}
}

func TestAnyMappingInsertAndDuplicate(t *testing.T) {
	input := `parameters:
- name: extra
  type: object
  default:
    db:
      image: postgres:16
      ports: ['5432:5432']
jobs:
- job: J
  services:
    db:
      image: postgres:15
    ${{ insert }}: ${{ parameters.extra }}
  steps: []
`
	resolved, collector, printed := resolveText(t, input, nil, Options{})
	if resolved == nil || !hasCode(collector, "TRL-TPL-0010") || strings.Contains(printed, "postgres:15") || !strings.Contains(printed, "postgres:16") || !strings.Contains(printed, "5432:5432") {
		t.Fatalf("free-form insertion/last-wins failed: codes=%v\n%s", codes(collector), printed)
	}
	failure(t, "steps:\n- script: hi\n  ${{ insert }}: ${{ 1 }}\n", nil, Options{}, "TRL-EXPR-0005")
	failure(t, "parameters:\n- name: p\n  type: object\n  default:\n    displayName: {bad: shape}\nsteps:\n- script: hi\n  ${{ insert }}: ${{ parameters.p }}\n", nil, Options{}, "TRL-EXPR-0005")
}

func TestAliasesComeFromExpandedResources(t *testing.T) {
	source := memorySource{"lib@step.yml": "steps:\n- script: library\n"}
	fromParameter := `parameters:
- name: repos
  type: object
  default:
  - repository: lib
    type: git
    name: org/lib
resources:
  repositories: ${{ parameters.repos }}
steps:
- template: step.yml@lib
`
	resolved, collector, _ := resolveText(t, fromParameter, source, Options{})
	assertNoErrors(t, collector)
	if resolved == nil {
		t.Fatal("parameter-produced repository alias was not collected")
	}
	fromEach := `parameters:
- name: aliases
  type: object
  default: [lib]
resources:
  repositories:
  - ${{ each alias in parameters.aliases }}:
    - repository: ${{ alias }}
      type: git
      name: org/lib
steps:
- template: step.yml@lib
`
	resolved, collector, _ = resolveText(t, fromEach, source, Options{})
	assertNoErrors(t, collector)
	if resolved == nil {
		t.Fatal("each-produced repository alias was not collected")
	}
	underFalse := "resources:\n  repositories:\n  - ${{ if false }}:\n    - repository: ghost\n      type: git\n      name: org/ghost\nsteps:\n- template: x.yml@ghost\n"
	failure(t, underFalse, memorySource{"ghost@x.yml": "steps: []\n"}, Options{}, "TRL-TPL-0007")
}

func TestDynamicTemplatePath(t *testing.T) {
	input := "parameters:\n- name: path\n  default: t.yml\nsteps:\n- template: ${{ parameters.path }}\n"
	resolved, collector, printed := resolveText(t, input, memorySource{"@t.yml": "steps:\n- script: dynamic\n"}, Options{})
	assertNoErrors(t, collector)
	if resolved == nil || !strings.Contains(printed, "dynamic") {
		t.Fatalf("dynamic template did not resolve:\n%s", printed)
	}
	failure(t, "parameters:\n- name: path\n  type: object\n  default: {}\nsteps:\n- template: ${{ parameters.path }}\n", nil, Options{}, "TRL-EXPR-0005")
	embedded := "parameters:\n- name: dir\n  default: sub\nsteps:\n- template: ${{ parameters.dir }}/t.yml\n"
	resolved, collector, printed = resolveText(t, embedded, memorySource{"@sub/t.yml": "steps:\n- script: embedded\n"}, Options{})
	assertNoErrors(t, collector)
	if resolved == nil || !strings.Contains(printed, "embedded") {
		t.Fatalf("embedded dynamic template did not resolve:\n%s", printed)
	}
}

func TestExpandedBoolAndMatrixValidationPaths(t *testing.T) {
	failure(t, "parameters:\n- name: b\n  default: maybe\nsteps:\n- script: hi\n  continueOnError: ${{ parameters.b }}\n", nil, Options{}, "TRL-PARSE-0012")
	input := "parameters:\n- name: m\n  type: object\n  default:\n    one:\n      cfg: a\njobs:\n- job: J\n  strategy:\n    matrix: ${{ parameters.m }}\n  steps: []\n"
	resolved, collector, printed := resolveText(t, input, nil, Options{})
	assertNoErrors(t, collector)
	if resolved == nil || !strings.Contains(printed, "cfg: a") {
		t.Fatalf("matrix insertion failed:\n%s", printed)
	}
	failure(t, "parameters:\n- name: m\n  default: nope\njobs:\n- job: J\n  strategy:\n    matrix: ${{ parameters.m }}\n  steps: []\n", nil, Options{}, "TRL-EXPR-0005")
}

func TestWholeValueMaterializationAcrossOwners(t *testing.T) {
	const parameters = "parameters:\n- name: p\n  type: object\n  default: {vmImage: ubuntu}\n"
	tests := []struct{ name, body string }{
		{"deployment", "jobs:\n- deployment: D\n  environment: prod\n  pool: ${{ parameters.p }}\n"},
		{"stage", "stages:\n- stage: S\n  pool: ${{ parameters.p }}\n  jobs: []\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved, collector, printed := resolveText(t, parameters+test.body, nil, Options{})
			assertNoErrors(t, collector)
			if resolved == nil || !strings.Contains(printed, "vmImage: ubuntu") {
				t.Fatalf("whole-value materialization failed:\n%s", printed)
			}
		})
	}
}

func TestVariableAndParameterMappingInsertions(t *testing.T) {
	variables := "parameters:\n- name: v\n  type: object\n  default:\n  - name: A\n    value: one\nvariables: ${{ parameters.v }}\nsteps:\n- script: $(A)\n"
	resolved, collector, printed := resolveText(t, variables, nil, Options{})
	assertNoErrors(t, collector)
	if resolved == nil || !strings.Contains(printed, "name: A") {
		t.Fatalf("variable-list insertion failed:\n%s", printed)
	}
	failure(t, "parameters:\n- name: v\n  default: nope\nvariables: ${{ parameters.v }}\nsteps: []\n", nil, Options{}, "TRL-EXPR-0005")

	root := "parameters:\n- name: args\n  type: object\n  default: {x: selected}\nsteps:\n- template: t.yml\n  parameters: ${{ parameters.args }}\n"
	source := memorySource{"@t.yml": "parameters:\n- name: x\nsteps:\n- script: echo ${{ parameters.x }}\n"}
	resolved, collector, printed = resolveText(t, root, source, Options{})
	assertNoErrors(t, collector)
	if resolved == nil || !strings.Contains(printed, "echo selected") {
		t.Fatalf("parameter-map insertion failed:\n%s", printed)
	}
	conditional := "steps:\n- template: t.yml\n  parameters:\n    ${{ if true }}:\n      x: conditional\n"
	resolved, collector, printed = resolveText(t, conditional, source, Options{})
	assertNoErrors(t, collector)
	if resolved == nil || !strings.Contains(printed, "echo conditional") {
		t.Fatalf("conditional parameter mapping failed:\n%s", printed)
	}
}

func TestParameterCoercionBranches(t *testing.T) {
	number, err := expr.ParseNumber("2")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		kind string
		in   expr.Value
		ok   bool
	}{
		{"string", expr.Null{}, true},
		{"string", number, true},
		{"string", expr.Bool(false), true},
		{"string", &expr.Object{}, false},
		{"number", expr.String("3.5"), true},
		{"number", expr.String("bad"), false},
		{"boolean", expr.String("true"), true},
		{"boolean", expr.String("false"), true},
		{"boolean", expr.String("bad"), false},
		{"object", &expr.Array{}, true},
		{"step", &expr.Object{}, true},
		{"step", expr.String("bad"), false},
		{"unknown", expr.String("x"), false},
	}
	for _, test := range tests {
		if _, ok := coerce(test.kind, test.in); ok != test.ok {
			t.Errorf("coerce(%q, %T) ok = %v, want %v", test.kind, test.in, ok, test.ok)
		}
	}
	if truthy(expr.Null{}) || truthy(number) == false || truthy(&expr.Array{}) == false {
		t.Fatal("truthiness conversion mismatch")
	}
}

func TestWalkerDispatchesTypedContainers(t *testing.T) {
	r, _ := testResolver()
	env := &expr.Object{}
	nodes := []any{
		&ast.Extends{}, &ast.Variables{}, &ast.Map[*ast.Any]{}, &ast.Map[*ast.Map[*ast.Scalar]]{},
		&ast.List[*ast.Parameter]{}, &ast.List[*ast.Schedule]{}, &ast.List[*ast.BuildResource]{},
		&ast.List[*ast.ContainerResource]{}, &ast.List[*ast.PackageResource]{}, &ast.List[*ast.PipelineResource]{},
		&ast.List[*ast.WebhookFilter]{}, &ast.List[*ast.WebhookResource]{},
	}
	for _, node := range nodes {
		handled, err := r.walkKnown(context.Background(), node, env)
		if err != nil || !handled {
			t.Fatalf("walkKnown(%T) = %v, %v", node, handled, err)
		}
	}
}

func TestWrittenScopeRestoresResolverScope(t *testing.T) {
	r, _ := testResolver()
	node := &ast.StepTemplate{}
	original := scope{file: "callee.yml", depth: 2}
	written := scope{file: "caller.yml", depth: 1}
	wantActive := original
	wantActive.file = written.file
	r.scope = original
	r.written = map[any]scope{node: written}
	restore := r.inWrittenScope(node)
	if !reflect.DeepEqual(r.scope, wantActive) {
		t.Fatalf("scope = %+v, want %+v", r.scope, wantActive)
	}
	restore()
	if !reflect.DeepEqual(r.scope, original) {
		t.Fatalf("restored scope = %+v, want %+v", r.scope, original)
	}
}

func TestBudgetAndTraversalGuards(t *testing.T) {
	r, collector := testResolver()
	r.maxBytes = 64
	_, err := r.valueToAny(context.Background(), &expr.Object{Properties: []expr.Property{{Name: "a", Value: expr.String("a")}, {Name: "b", Value: expr.String("b")}}}, ast.Pos{})
	if !errors.Is(err, errBudget) || !hasCode(collector, "TRL-TPL-0003") {
		t.Fatalf("valueToAny budget = %v, %v", err, codes(collector))
	}
	r, collector = testResolver()
	if err := r.charge(maxExpandedNodes+1, 0); !errors.Is(err, errBudget) || !hasCode(collector, "TRL-TPL-0003") {
		t.Fatalf("node guard = %v, %v", err, codes(collector))
	}
	r, collector = testResolver()
	r.iterations = maxEachIterations
	if err := r.iterate(0); !errors.Is(err, errBudget) || !hasCode(collector, "TRL-TPL-0003") {
		t.Fatalf("iteration guard = %v, %v", err, codes(collector))
	}
	r, collector = testResolver()
	if err := r.walk(context.Background(), reflect.ValueOf(&ast.Scalar{}), &expr.Object{}, maxWalkDepth+1); err != nil || !hasCode(collector, "TRL-EXPR-0005") {
		t.Fatalf("walk-depth guard = %v, %v", err, codes(collector))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.chain(ctx, &expr.Object{}, chainOps{n: 1, at: func(int) (*ast.Directive, *ast.Directive) { return nil, nil }, plain: func(int) error { t.Fatal("plain called"); return nil }}); !errors.Is(err, context.Canceled) {
		t.Fatalf("chain cancellation = %v", err)
	}
}

type evalCanceledContext struct {
	context.Context
	errCalls int
}

func (*evalCanceledContext) Done() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

func (c *evalCanceledContext) Err() error {
	c.errCalls++
	if c.errCalls == 1 {
		return nil // resolver pre-check
	}
	return context.Canceled // expression evaluator check
}

func TestExpressionEvaluatorObservesCancellation(t *testing.T) {
	r, _ := testResolver()
	parsed, err := expr.Parse("eq('a', 'a')")
	if err != nil {
		t.Fatal(err)
	}
	ctx := &evalCanceledContext{Context: context.Background()}
	_, err = r.eval(ctx, &ast.Directive{Kind: ast.DirectiveInsert, Expr: parsed}, &expr.Object{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("eval error = %v, want context.Canceled", err)
	}
}

func TestValidationAndReparseMutationGuards(t *testing.T) {
	failure(t, "resources:\n  repositories: []\nsteps:\n- template: x.yml@missing\n", nil, Options{}, "TRL-TPL-0007")
	failure(t, "parameters:\n- name: x\n  default: 1\n- name: X\n  default: 2\nsteps: []\n", nil, Options{}, "TRL-VAL-0008")
	resolved, collector, _ := resolveText(t, "parameters:\n- name: x\n  default: succeeded()\nsteps:\n- script: hi\n  condition: ${{ parameters.x }}\n", nil, Options{})
	assertNoErrors(t, collector)
	condition := resolved.Steps.Items[0].Value.(*ast.ScriptStep).Condition
	if condition.Expr == nil || len(condition.ExprSources) == 0 {
		t.Fatalf("expanded expression was not reparsed: %#v", condition)
	}
	legacy := "parameters:\n  ${{ if true }}:\n    who: root\n  ${{ else }}:\n    who: wrong\nsteps:\n- script: echo ${{ parameters.who }}\n"
	resolved, collector, printed := resolveText(t, legacy, nil, Options{})
	assertNoErrors(t, collector)
	if resolved == nil || !strings.Contains(printed, "echo root") || strings.Contains(printed, "wrong") {
		t.Fatalf("legacy conditional parameters:\n%s", printed)
	}
	if truthy(expr.String("")) {
		t.Fatal("empty string is truthy")
	}
}

func TestMaterializeFieldKeepsProvenance(t *testing.T) {
	source := memorySource{"@jobs.yml": "parameters:\n- name: p\n  type: object\n  default:\n    vmImage: ubuntu\njobs:\n- job: J\n  pool: ${{ parameters.p }}\n  steps: []\n"}
	resolved, collector, _ := resolveText(t, "jobs:\n- template: jobs.yml\n", source, Options{})
	assertNoErrors(t, collector)
	job := resolved.Jobs.Items[0].Value.(*ast.JobDef)
	if job.Pool == nil || job.Pool.VMImage == nil || job.Pool.VMImage.Position().File != "jobs.yml" || len(job.Pool.VMImage.Chain) != 1 {
		t.Fatalf("materialized pool provenance = %+v", job.Pool)
	}
}

func TestDedicatedWrongTemplateShapeDiagnostic(t *testing.T) {
	failure(t, "steps:\n- template: jobs.yml\n", memorySource{"@jobs.yml": "jobs:\n- job: J\n  steps: []\n"}, Options{}, "TRL-TPL-0009")
}

func TestBooleanRenderingAndNativeTypedBool(t *testing.T) {
	input := "parameters:\n- name: b\n  type: boolean\n  default: true\nsteps:\n- script: echo ${{ parameters.b }}\n  continueOnError: ${{ parameters.b }}\n"
	resolved, collector, printed := resolveText(t, input, nil, Options{})
	assertNoErrors(t, collector)
	step := resolved.Steps.Items[0].Value.(*ast.ScriptStep)
	if !strings.Contains(printed, "echo True") || step.ContinueOnError == nil || !step.ContinueOnError.Known || !step.ContinueOnError.Val {
		t.Fatalf("boolean conversion/typing:\n%s", printed)
	}
}

func TestWindowsCycleKeysIgnorePathCase(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path semantics")
	}
	if includeKey("", "Dir/A.yml") != includeKey("", "dir/a.YML") {
		t.Fatal("cycle keys are case-sensitive on Windows")
	}
}
