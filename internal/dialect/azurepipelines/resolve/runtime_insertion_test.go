package resolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A whole-value runtime $[ ] expression is accepted only where Azure documents
// runtime expressions for the whole value: strategy.matrix and the job or
// deployment container. The resolver leaves those untouched.
func TestRuntimeWholeValueInsertionAllowed(t *testing.T) {
	cases := map[string]struct{ input, want string }{
		"matrix": {`jobs:
- job: a
  strategy:
    matrix: $[ dependencies.generate.outputs['mtrx.legs'] ]
  steps:
  - script: echo
`, "$[ dependencies.generate.outputs['mtrx.legs'] ]"},
		"job container": {`jobs:
- job: a
  container: $[ variables['containerResource'] ]
  steps:
  - script: echo
`, "$[ variables['containerResource'] ]"},
		"deployment container": {`jobs:
- deployment: d
  container: $[ variables['containerResource'] ]
  environment: e
  strategy:
    runOnce:
      deploy:
        steps:
        - script: echo
`, "$[ variables['containerResource'] ]"},
	}
	for name, tc := range cases { //trasloco:unordered-ok independent subtests
		t.Run(name, func(t *testing.T) {
			resolved, collector, text := resolveText(t, tc.input, nil, Options{})
			if resolved == nil {
				t.Fatalf("no result: %v", codes(collector))
			}
			assertNoErrors(t, collector)
			if !strings.Contains(text, tc.want) {
				t.Fatalf("runtime directive lost:\n%s", text)
			}
		})
	}
}

// Everywhere else a whole-value runtime expression is an error and there is no AST.
func TestRuntimeWholeValueInsertionRejected(t *testing.T) {
	source := memorySource{
		"@steps.yml":   "steps:\n- script: x\n",
		"@jobs.yml":    "jobs:\n- job: j\n  steps:\n  - script: x\n",
		"@stages.yml":  "stages:\n- stage: s\n",
		"@vars.yml":    "variables:\n  a: b\n",
		"@calleeS.yml": "steps: $[ variables['s'] ]\n",
		"@calleeJ.yml": "jobs: $[ variables['j'] ]\n",
		"@calleeG.yml": "stages: $[ variables['g'] ]\n",
		"@calleeV.yml": "variables: $[ variables['v'] ]\n",
		"@base.yml":    "steps:\n- script: x\n",
	}
	cases := map[string]string{
		"root steps":         "steps: $[ variables['steps'] ]\n",
		"root jobs":          "jobs: $[ variables['jobs'] ]\n",
		"root stages":        "stages: $[ variables['stages'] ]\n",
		"root variables":     "variables: $[ variables['extra'] ]\nsteps:\n- script: echo\n",
		"pool":               "jobs:\n- job: a\n  pool: $[ variables['poolName'] ]\n  steps:\n  - script: echo\n",
		"strategy":           "jobs:\n- job: a\n  strategy: $[ variables['s'] ]\n  steps:\n  - script: echo\n",
		"job steps":          "jobs:\n- job: a\n  steps: $[ variables['s'] ]\n",
		"job variables":      "jobs:\n- job: a\n  variables: $[ variables['v'] ]\n  steps:\n  - script: echo\n",
		"resources":          "resources: $[ variables['r'] ]\nsteps:\n- script: echo\n",
		"trigger":            "trigger: $[ variables['t'] ]\nsteps:\n- script: echo\n",
		"extends":            "extends: $[ variables['e'] ]\n",
		"call parameters":    "steps:\n- template: steps.yml\n  parameters: $[ variables['p'] ]\n",
		"callee steps":       "steps:\n- template: calleeS.yml\n",
		"callee jobs":        "jobs:\n- template: calleeJ.yml\n",
		"callee stages":      "stages:\n- template: calleeG.yml\n",
		"callee variables":   "variables:\n- template: calleeV.yml\nsteps:\n- script: echo\n",
		"each body":          "parameters:\n- name: l\n  type: object\n  default: [a]\nsteps:\n- ${{ each x in parameters.l }}:\n  - script: echo\n    env: $[ variables['e'] ]\n",
		"if body":            "parameters:\n- name: b\n  type: boolean\n  default: true\njobs:\n- ${{ if parameters.b }}:\n  - job: a\n    pool: $[ variables['p'] ]\n    steps:\n    - script: echo\n",
		"each matrix parent": "parameters:\n- name: l\n  type: object\n  default: [a]\njobs:\n- ${{ each x in parameters.l }}:\n  - job: j${{ x }}\n    steps: $[ variables['s'] ]\n",
	}
	for name, input := range cases { //trasloco:unordered-ok independent subtests
		t.Run(name, func(t *testing.T) {
			findings := failure(t, input, source, Options{}, "TRL-EXPR-0005")
			file := "root.yml"
			positionInput := input
			if strings.HasPrefix(name, "callee ") {
				keys := map[string]string{"callee steps": "@calleeS.yml", "callee jobs": "@calleeJ.yml", "callee stages": "@calleeG.yml", "callee variables": "@calleeV.yml"}
				key := keys[name]
				file = strings.TrimPrefix(key, "@")
				positionInput = source[key]
			}
			at(t, findings[0], file, strings.Count(positionInput[:strings.Index(positionInput, "$[")], "\n")+1)
		})
	}
}

func TestRuntimeListItemInsertionsRejected(t *testing.T) {
	template := `parameters:
- name: xs
  type: stepList
  default:
  - script: echo injected
steps:
- $[ parameters.xs ]
`
	cases := map[string]struct {
		input  string
		source Source
		file   string
		line   int
	}{
		"steps": {`parameters:
- name: xs
  type: stepList
  default:
  - script: echo injected
steps:
- $[ parameters.xs ]
`, nil, "root.yml", 7},
		"jobs": {`parameters:
- name: xs
  type: jobList
  default:
  - job: injected
jobs:
- $[ parameters.xs ]
`, nil, "root.yml", 7},
		"stages": {`parameters:
- name: xs
  type: stageList
  default:
  - stage: injected
stages:
- $[ parameters.xs ]
`, nil, "root.yml", 7},
		"variables": {`parameters:
- name: xs
  type: object
  default:
  - name: injected
    value: yes
variables:
- $[ parameters.xs ]
steps:
- script: echo
`, nil, "root.yml", 8},
		"template body": {"steps:\n- template: runtime-list.yml\n", memorySource{"@runtime-list.yml": template}, "runtime-list.yml", 7},
		"if body": {`parameters:
- name: xs
  type: stepList
  default: []
steps:
- ${{ if true }}:
  - $[ parameters.xs ]
`, nil, "root.yml", 7},
		"each body": {`parameters:
- name: xs
  type: stepList
  default: []
- name: items
  type: object
  default: [one]
steps:
- ${{ each item in parameters.items }}:
  - $[ parameters.xs ]
`, nil, "root.yml", 10},
	}
	for name, tc := range cases { //trasloco:unordered-ok independent subtests
		t.Run(name, func(t *testing.T) {
			findings := failure(t, tc.input, tc.source, Options{}, "TRL-EXPR-0005")
			at(t, findings[0], tc.file, tc.line)
		})
	}
}

func TestRuntimeInsertionNestedStillAllowed(t *testing.T) {
	input := `parameters:
- name: l
  type: object
  default: [a, b]
jobs:
- ${{ each x in parameters.l }}:
  - job: j${{ x }}
    strategy:
      matrix: $[ variables['m'] ]
    container: $[ variables['c'] ]
    steps:
    - script: echo
`
	resolved, collector, text := resolveText(t, input, nil, Options{})
	if resolved == nil {
		t.Fatalf("no result: %v", codes(collector))
	}
	assertNoErrors(t, collector)
	if strings.Count(text, "$[ variables['m'] ]") != 2 || strings.Count(text, "$[ variables['c'] ]") != 2 {
		t.Fatalf("runtime directives lost:\n%s", text)
	}
}

func TestRuntimeInsertionCorpusFixturesResolve(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "testdata", "azurepipelines", "corpus")
	for _, name := range []string{"strategies/matrix-dynamic.yml", "strategies/matrix-container-images.yml"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
			if err != nil {
				t.Fatal(err)
			}
			resolved, collector, text := resolveText(t, string(data), nil, Options{})
			if resolved == nil {
				t.Fatalf("no result: %v", codes(collector))
			}
			assertNoErrors(t, collector)
			if !strings.Contains(text, "$[") {
				t.Fatalf("runtime expression lost:\n%s", text)
			}
		})
	}
}

// A runtime $[ ] as the body of a key directive inside matrix is not the
// matrix value itself and stays an error.
func TestRuntimeInsertionMatrixDirectiveBodiesRejected(t *testing.T) {
	head := "parameters:\n- name: l\n  type: object\n  default: [a]\njobs:\n- job: a\n  strategy:\n    matrix:\n"
	tail := "  steps:\n  - script: echo\n"
	cases := map[string]string{
		"if":   "      ${{ if true }}: $[ variables['m'] ]\n",
		"else": "      ${{ if false }}:\n        x:\n          k: v\n      ${{ else }}: $[ variables['m'] ]\n",
		"each": "      ${{ each x in parameters.l }}: $[ variables['m'] ]\n",
	}
	for name, body := range cases { //trasloco:unordered-ok independent subtests
		t.Run(name, func(t *testing.T) {
			input := head + body + tail
			findings := failure(t, input, nil, Options{}, "TRL-EXPR-0005")
			at(t, findings[0], "root.yml", strings.Count(input[:strings.Index(input, "$[")], "\n")+1)
		})
	}
}

func TestRuntimeInsertionMatrixInsertKeyRejected(t *testing.T) {
	cases := map[string]string{
		"direct": "jobs:\n- job: a\n  strategy:\n    matrix:\n      ${{ insert }}: $[ variables['m'] ]\n  steps:\n  - script: echo\n",
		"each":   "parameters:\n- name: items\n  type: object\n  default: [one]\njobs:\n- job: a\n  strategy:\n    matrix:\n      ${{ each item in parameters.items }}:\n        ${{ insert }}: $[ variables['m'] ]\n  steps:\n  - script: echo\n",
	}
	for name, input := range cases { //trasloco:unordered-ok independent subtests
		t.Run(name, func(t *testing.T) {
			findings := failure(t, input, nil, Options{}, "TRL-EXPR-0005")
			at(t, findings[0], "root.yml", strings.Count(input[:strings.Index(input, "$[")], "\n")+1)
		})
	}
}

// matrix: $[ ] under a strategy-level or root ${{ if }} is still the matrix value.
func TestRuntimeInsertionMatrixUnderConditionalAccepted(t *testing.T) {
	cases := map[string]string{
		"strategy if": "jobs:\n- job: a\n  strategy:\n    ${{ if true }}:\n      matrix: $[ variables['m'] ]\n  steps:\n  - script: echo\n",
		"root if":     "jobs:\n- ${{ if true }}:\n  - job: a\n    strategy:\n      matrix: $[ variables['m'] ]\n    steps:\n    - script: echo\n",
	}
	for name, input := range cases { //trasloco:unordered-ok independent subtests
		t.Run(name, func(t *testing.T) {
			resolved, collector, text := resolveText(t, input, nil, Options{})
			if resolved == nil {
				t.Fatalf("no result: %v", codes(collector))
			}
			assertNoErrors(t, collector)
			if !strings.Contains(text, "$[ variables['m'] ]") {
				t.Fatalf("runtime directive lost:\n%s", text)
			}
		})
	}
}

func TestCompileTimeWholeValueInsertionStillExpands(t *testing.T) {
	input := `parameters:
- name: pool
  type: object
  default:
    name: mypool
jobs:
- job: a
  pool: ${{ parameters.pool }}
  steps:
  - script: echo
`
	_, collector, text := resolveText(t, input, nil, Options{})
	assertNoErrors(t, collector)
	if !strings.Contains(text, "mypool") || strings.Contains(text, "${{") {
		t.Fatalf("compile-time insertion not expanded:\n%s", text)
	}
}
