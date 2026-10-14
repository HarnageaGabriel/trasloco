package parse

import (
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
)

// Issue #15 item 1: a `${{ if }}` key inside a repository resource trigger was
// dropped; its branches were diagnosed as unknown keys and lost.
func TestRepositoryTriggerConditionalKey(t *testing.T) {
	const input = `resources:
  repositories:
  - repository: lib
    type: git
    name: org/lib
    trigger:
      ${{ if eq(1, 1) }}:
        branches:
          include:
          - main
`
	pipeline, got := parseText("repo-trigger.yml", input)
	if len(got) != 0 {
		t.Fatalf("diagnostics = %v, want none", got)
	}
	repositories := pipeline.Resources.Repositories.Items
	if len(repositories) != 1 {
		t.Fatalf("repositories = %d, want 1", len(repositories))
	}
	trigger := repositories[0].Value.Trigger
	if trigger == nil {
		t.Fatal("repository trigger is missing")
	}
	if len(trigger.Inserts) != 1 {
		t.Fatalf("trigger.Inserts = %d, want 1", len(trigger.Inserts))
	}
	body := trigger.Inserts[0].Body
	if body == nil || body.Branches == nil || body.Branches.Include == nil {
		t.Fatalf("conditional trigger branches were dropped: %+v", body)
	}
	include := body.Branches.Include.Items
	if len(include) != 1 || include[0].Value == nil || include[0].Value.Value != "main" {
		t.Fatalf("include = %+v, want [main]", include)
	}
}

// Issue #15 item 2: a conditional key at the root shape level did not select
// the pipeline shape, so `${{ if }}: {steps: [...]}` parsed as shape-less.
func TestRootConditionalShapeKey(t *testing.T) {
	const input = `${{ if eq(1, 1) }}:
  steps:
  - script: echo hi
`
	pipeline, got := parseText("shape.yml", input)
	for _, finding := range got {
		if code := strings.SplitN(finding, "@", 2)[0]; code != "TRL-PARSE-0002" {
			t.Fatalf("unexpected diagnostic %s", finding)
		}
	}
	if pipeline.Shape != ast.ShapeSteps {
		t.Fatalf("shape = %v, want steps", pipeline.Shape)
	}
}

// ADR 0009 / issue #15 item 2: ADO evaluates `${{ }}` before schema binding, so
// mutually exclusive directive bodies may select different shapes. Only
// explicit root keys conflict; a conditional shape switch is never diagnosed.
func TestConditionalShapeSwitchIsNotAConflict(t *testing.T) {
	const input = `${{ if eq(parameters.mode, 'a') }}:
  steps:
  - script: a
${{ else }}:
  jobs:
  - job: j
    steps:
    - script: b
`
	pipeline, got := parseText("switch.yml", input)
	for _, finding := range got {
		if strings.HasPrefix(finding, "TRL-PARSE-0014@") {
			t.Fatalf("conditional shape switch diagnosed: %v", got)
		}
	}
	if pipeline.Shape != ast.ShapeSteps {
		t.Fatalf("shape = %v, want steps (first directive body)", pipeline.Shape)
	}
}

// Two explicit shape keys still select different shapes and conflict.
func TestExplicitShapeKeysStillConflict(t *testing.T) {
	const input = `steps:
- script: echo hi
jobs:
- job: a
  steps:
  - script: echo a
`
	_, got := parseText("conflict.yml", input)
	found := false
	for _, finding := range got {
		if strings.HasPrefix(finding, "TRL-PARSE-0014@") {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %v, want TRL-PARSE-0014", got)
	}
}

// Issue #15 item 4 (ADR 0009): scalars record whether they were quoted, so a
// quoted "60" in a parameter default is not read as the number 60.
func TestScalarQuotedFlag(t *testing.T) {
	const input = `parameters:
- name: version
  type: string
  default: '60'
- name: count
  type: number
  default: 60
- name: opts
  type: object
  default:
    version: '60'
    retries: 3
steps:
- script: echo hi
`
	pipeline, got := parseText("quoted.yml", input)
	if len(got) != 0 {
		t.Fatalf("diagnostics = %v, want none", got)
	}
	items := pipeline.Parameters.List.Items
	if len(items) != 3 {
		t.Fatalf("parameters = %d, want 3", len(items))
	}
	if s := items[0].Value.Default.Scalar; s == nil || !s.Quoted || s.Value != "60" {
		t.Fatalf("string default = %+v, want quoted 60", s)
	}
	if s := items[1].Value.Default.Scalar; s == nil || s.Quoted || s.Value != "60" {
		t.Fatalf("number default = %+v, want unquoted 60", s)
	}
	entries := items[2].Value.Default.Entries
	if len(entries) != 2 {
		t.Fatalf("object default entries = %d, want 2", len(entries))
	}
	if s := entries[0].Value.Scalar; s == nil || !s.Quoted || s.Value != "60" {
		t.Fatalf("object default version = %+v, want quoted 60", s)
	}
}
