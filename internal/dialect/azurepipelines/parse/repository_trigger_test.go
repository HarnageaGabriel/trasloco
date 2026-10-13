package parse

import (
	"reflect"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
)

func filterValues(f *ast.Filter) []string {
	if f == nil || f.Include == nil {
		return nil
	}
	out := make([]string, 0, len(f.Include.Items))
	for _, item := range f.Include.Items {
		if item.Value != nil {
			out = append(out, item.Value.Value)
		}
	}
	return out
}

// A key directive such as `${{ if }}` inside a repository resource trigger
// wraps a partial trigger mapping. Its body must be parsed like the
// surrounding mapping, so branches, paths and tags are kept instead of being
// dropped. Regression test for #15.
func TestRepositoryTriggerKeepsDirectiveFields(t *testing.T) {
	text := `
resources:
  repositories:
    - repository: common
      type: github
      name: org/common
      trigger:
        branches:
          include:
            - main
        ${{ if eq(variables['build.sourceBranch'], 'refs/heads/release') }}:
          branches:
            include:
              - release
          tags:
            include:
              - v*
`
	pipeline, findings := parseText("repository-trigger.yml", text)
	if len(findings) != 0 {
		t.Fatalf("unexpected findings: %v", findings)
	}
	if pipeline.Resources == nil || pipeline.Resources.Repositories == nil {
		t.Fatal("repository resource was not parsed")
	}
	items := pipeline.Resources.Repositories.Items
	if len(items) != 1 || items[0].Value == nil {
		t.Fatalf("want one repository resource, got %d", len(items))
	}
	trigger := items[0].Value.Trigger
	if trigger == nil {
		t.Fatal("trigger was not parsed")
	}
	if got := filterValues(trigger.Branches); !reflect.DeepEqual(got, []string{"main"}) {
		t.Errorf("top-level branches = %v, want [main]", got)
	}
	if len(trigger.Inserts) != 1 {
		t.Fatalf("want one key directive, got %d", len(trigger.Inserts))
	}
	if kind := trigger.Inserts[0].Directive.Kind; kind != ast.DirectiveIf {
		t.Errorf("directive kind = %v, want if", kind)
	}
	body := trigger.Inserts[0].Body
	if body == nil {
		t.Fatal("directive body is nil")
	}
	if got := filterValues(body.Branches); !reflect.DeepEqual(got, []string{"release"}) {
		t.Errorf("conditional branches = %v, want [release]", got)
	}
	if got := filterValues(body.Tags); !reflect.DeepEqual(got, []string{"v*"}) {
		t.Errorf("conditional tags = %v, want [v*]", got)
	}
}
