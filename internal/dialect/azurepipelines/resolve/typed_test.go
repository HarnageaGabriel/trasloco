package resolve

import (
	"context"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/printer"
)

// resolveText parses and resolves input and returns the result, the collector
// and the printed YAML ("" when the AST is nil).
func resolveText(t *testing.T, input string, source Source, options Options) (*ast.Pipeline, *diag.Collector, string) {
	t.Helper()
	collector := diag.NewCollector()
	options.Collector = collector
	root := parse.Source("root.yml", []byte(input), collector)
	if root == nil {
		t.Fatalf("root did not parse: %v", codes(collector))
	}
	resolved, err := Resolve(context.Background(), root, source, options)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == nil {
		return nil, collector, ""
	}
	if leftovers := findLeftovers(resolved); len(leftovers) > 0 {
		t.Fatalf("post-condition oracle found %+v", leftovers)
	}
	printed, err := printer.Print(resolved)
	if err != nil {
		t.Fatal(err)
	}
	return resolved, collector, string(printed)
}

const typedParams = `parameters:
- name: env
  type: string
  default: prod
- name: on
  type: boolean
  default: true
- name: deps
  type: object
  default: [A, B]
- name: vars
  type: object
  default:
    X: 1
    Y: two
- name: steps
  type: stepList
  default:
  - script: echo from-param
- name: pool
  type: object
  default:
    vmImage: ubuntu
`

func TestTypedPathsAreFullyExpanded(t *testing.T) {
	tests := []struct {
		name, body string
		source     memorySource
		want       []string
		not        []string
	}{
		{
			name: "bool-and-condition", body: "steps:\n- script: a\n  enabled: ${{ parameters.on }}\n  continueOnError: ${{ parameters.on }}\n  condition: and(succeeded(), eq('${{ parameters.env }}', 'prod'))\n",
			want: []string{"enabled: true", "continueOnError: true", "eq('prod', 'prod')"},
		},
		{name: "enum", body: "jobs:\n- job: J\n  workspace:\n    clean: ${{ 'outputs' }}\n  steps:\n  - script: a\n", want: []string{"clean: outputs"}},
		{name: "depends-cond-items", body: "jobs:\n- job: A\n- job: B\n- job: C\n  dependsOn:\n  - A\n  - ${{ if eq(parameters.env, 'prod') }}:\n    - B\n  - ${{ if eq(parameters.env, 'dev') }}:\n    - Z\n", want: []string{"- A", "- B"}, not: []string{"Z"}},
		{name: "scalar-list-insertion", body: "jobs:\n- job: A\n- job: B\n- job: C\n  dependsOn: ${{ parameters.deps }}\n", want: []string{"- A", "- B"}},
		{name: "scalar-list-insert-item", body: "jobs:\n- job: A\n- job: B\n- job: C\n  dependsOn:\n  - ${{ parameters.deps }}\n", want: []string{"- A", "- B"}},
		{name: "scalar-map-chain", body: "steps:\n- script: a\n  env:\n    ${{ if eq(parameters.env, 'dev') }}:\n      WHO: dev\n    ${{ elseif eq(parameters.env, 'prod') }}:\n      WHO: prod\n    ${{ else }}:\n      WHO: other\n    ${{ each pair in parameters.vars }}:\n      ${{ pair.key }}: ${{ pair.value }}\n", want: []string{"WHO: prod", "X: 1", "Y: two"}, not: []string{"dev", "other"}},
		{name: "scalar-map-insertion", body: "steps:\n- task: A@1\n  env: ${{ parameters.vars }}\n  inputs: ${{ parameters.vars }}\n", want: []string{"X: 1", "Y: two"}},
		{name: "scalar-map-insert-key", body: "steps:\n- script: a\n  env:\n    ${{ insert }}: ${{ parameters.vars }}\n    Z: 3\n", want: []string{"X: 1", "Z: 3"}},
		{name: "list-insertion", body: "steps: ${{ parameters.steps }}\n", want: []string{"echo from-param"}},
		{name: "list-insert-item", body: "steps:\n- script: first\n- ${{ parameters.steps }}\n", want: []string{"first", "echo from-param"}},
		{name: "matrix", body: "jobs:\n- job: J\n  strategy:\n    matrix:\n      ${{ each v in parameters.deps }}:\n        ${{ v }}:\n          cfg: ${{ v }}\n      ${{ if parameters.on }}:\n        extra:\n          cfg: x\n  steps:\n  - script: a\n", want: []string{"cfg: A", "cfg: B", "cfg: x"}},
		{name: "stage-variables", body: "stages:\n- stage: S\n  variables:\n  - template: v.yml\n  - ${{ if parameters.on }}:\n    - name: cond\n      value: yes\n  jobs:\n  - job: J\n    variables:\n      ${{ if parameters.on }}:\n        m: 1\n    steps:\n    - script: a\n", source: memorySource{"@v.yml": "variables:\n- name: fromTemplate\n  value: t\n"}, want: []string{"fromTemplate", "name: cond", "m: 1"}},
		{name: "variables-insertion", body: "variables: ${{ parameters.vars }}\nsteps:\n- script: a\n", want: []string{"X: 1"}},
		{name: "variables-cond-key", body: "variables:\n  ${{ if parameters.on }}:\n    A: 1\n  ${{ else }}:\n    A: 2\nsteps:\n- script: a\n", want: []string{"A: 1"}, not: []string{"A: 2"}},
		{name: "pool-insertion", body: "pool: ${{ parameters.pool }}\nsteps:\n- script: a\n", want: []string{"vmImage: ubuntu"}},
		{name: "struct-insert-chain", body: "pool:\n  ${{ if parameters.on }}:\n    vmImage: a\n  ${{ else }}:\n    vmImage: b\nsteps:\n- script: a\n", want: []string{"vmImage: a"}, not: []string{"vmImage: b"}},
		{name: "deployment-hook-steps", body: "jobs:\n- deployment: D\n  environment: e\n  strategy:\n    runOnce:\n      deploy:\n        steps:\n        - ${{ each p in parameters.deps }}:\n          - script: echo ${{ p }}\n", want: []string{"echo A", "echo B"}},
		{name: "interpolated-key", body: "steps:\n- script: a\n  env:\n    ${{ parameters.env }}-KEY: v\n", want: []string{"prod-KEY: v"}},
		{name: "each-body-object", body: "steps:\n- ${{ each step in parameters.steps }}:\n  - ${{ each pair in step }}:\n      ${{ if ne(pair.key, 'never') }}:\n        ${{ pair.key }}: ${{ pair.value }}\n", want: []string{"echo from-param"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var source Source
			if test.source != nil {
				source = test.source
			}
			resolved, collector, printed := resolveText(t, typedParams+test.body, source, Options{})
			assertNoErrors(t, collector)
			if resolved == nil {
				t.Fatalf("nil AST: %v", codes(collector))
			}
			printed = strings.ReplaceAll(printed, `"`, "")
			for _, want := range test.want {
				if !strings.Contains(printed, want) {
					t.Errorf("missing %q in:\n%s", want, printed)
				}
			}
			for _, not := range test.not {
				if strings.Contains(printed, not) {
					t.Errorf("unexpected %q in:\n%s", not, printed)
				}
			}
			if strings.Contains(printed, "${{") {
				t.Errorf("directive text left in:\n%s", printed)
			}
		})
	}
}

func TestTaskReferenceIsRederivedAfterExpansion(t *testing.T) {
	resolved, collector, _ := resolveText(t, "parameters:\n- name: task\n  default: 01234567-89ab-cdef-0123-456789abcdef@2.3.4\nsteps:\n- task: ${{ parameters.task }}\n", nil, Options{})
	assertNoErrors(t, collector)
	task := resolved.Steps.Items[0].Value.(*ast.TaskStep)
	if !task.Ref.Valid || task.Ref.Dynamic || !task.Ref.IsGUID || task.Ref.Name != "01234567-89ab-cdef-0123-456789abcdef" || task.Ref.Version != "2.3.4" || task.Ref.Major != 2 {
		t.Fatalf("task ref = %+v", task.Ref)
	}
	resolved, collector, _ = resolveText(t, "parameters:\n- name: task\n  default: ${{ '$(dynamic)' }}\nsteps:\n- task: ${{ parameters.task }}\n", nil, Options{})
	assertNoErrors(t, collector)
	if ref := resolved.Steps.Items[0].Value.(*ast.TaskStep).Ref; !ref.Valid || !ref.Dynamic || ref.Name != "$(dynamic)" || ref.Major != -1 {
		t.Fatalf("dynamic task ref = %+v", ref)
	}
	failure(t, "parameters:\n- name: task\n  default: invalid\nsteps:\n- task: ${{ parameters.task }}\n", nil, Options{}, "TRL-PARSE-0015")
	failure(t, "parameters:\n- name: task\n  default: Name@bad\nsteps:\n- task: ${{ parameters.task }}\n", nil, Options{}, "TRL-PARSE-0015")
}

func TestPostconditionDetectsCompileTimeShapes(t *testing.T) {
	inputs := []string{
		"steps:\n- template: t.yml\n",
		"steps:\n- ${{ if true }}:\n  - script: x\n",
		"steps:\n- ${{ parameters.steps }}\n",
		"steps: ${{ parameters.steps }}\n",
		"parameters:\n- name: p\n  default:\n    ${{ if true }}: x\nsteps: []\n",
		"pool:\n  ${{ if true }}:\n    vmImage: ubuntu\nsteps: []\n",
		"steps:\n- script: x-${{ parameters.x }}\n",
	}
	for _, input := range inputs {
		collector := diag.NewCollector()
		pipeline := parse.Source("leftover.yml", []byte(input), collector)
		if pipeline == nil || len(findLeftovers(pipeline)) == 0 {
			t.Fatalf("leftover not found in:\n%s", input)
		}
	}
	collector := diag.NewCollector()
	pipeline := parse.Source("leftover.yml", []byte(inputs[0]), collector)
	r := &resolver{opts: Options{Collector: collector}}
	r.postcondition(pipeline)
	if !r.failed || !hasCode(collector, "TRL-INT-0002") {
		t.Fatalf("postcondition: failed=%v diagnostics=%v", r.failed, codes(collector))
	}
}
