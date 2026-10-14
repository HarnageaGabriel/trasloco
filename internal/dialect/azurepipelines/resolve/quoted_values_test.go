package resolve

import (
	"context"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
)

// Issue #15 item 4 / ADR 0009: a quoted scalar is a string, so an object value
// keeps '60' and "true" as strings while plain 3 is still inferred as a number.
// The decoded text alone cannot tell 60 from '60'.
func TestQuotedObjectValuesStayStrings(t *testing.T) {
	const template = `parameters:
- name: opts
  type: object
  default:
    version: '60'
    retries: 3
    flag: "true"
    disabled: 'false'
    nothing: 'null'
steps:
- script: ${{ convertToJson(parameters.opts) }}
`
	const want = `{"version":"60","retries":3,"flag":"true","disabled":"false","nothing":"null"}`
	source := memorySource{"@t.yml": template}

	t.Run("default", func(t *testing.T) {
		collector := diag.NewCollector()
		root := parse.Source("root.yml", []byte("steps:\n- template: t.yml\n"), collector)
		resolved, err := Resolve(context.Background(), root, source, Options{Collector: collector})
		if err != nil {
			t.Fatal(err)
		}
		assertNoErrors(t, collector)
		assertJSONEqual(t, echoedScriptJSON(t, resolved), want)
	})

	t.Run("call-site", func(t *testing.T) {
		const caller = `steps:
- template: t.yml
  parameters:
    opts:
      version: '60'
      retries: 3
      flag: "true"
      disabled: 'false'
      nothing: 'null'
`
		collector := diag.NewCollector()
		root := parse.Source("root.yml", []byte(caller), collector)
		resolved, err := Resolve(context.Background(), root, source, Options{Collector: collector})
		if err != nil {
			t.Fatal(err)
		}
		assertNoErrors(t, collector)
		assertJSONEqual(t, echoedScriptJSON(t, resolved), want)
	})
}

func echoedScriptJSON(t *testing.T, resolved *ast.Pipeline) expr.Value {
	t.Helper()
	if resolved == nil || resolved.Steps == nil || len(resolved.Steps.Items) != 1 {
		t.Fatalf("resolved steps unavailable: %v", resolved)
	}
	step, ok := resolved.Steps.Items[0].Value.(*ast.ScriptStep)
	if !ok || step.Script == nil {
		t.Fatalf("echo step = %T", resolved.Steps.Items[0].Value)
	}
	value, err := expr.DecodeJSON([]byte(step.Script.Value))
	if err != nil {
		t.Fatalf("decode echoed JSON %q: %v", step.Script.Value, err)
	}
	return value
}

func assertJSONEqual(t *testing.T, got expr.Value, want string) {
	t.Helper()
	expected, err := expr.DecodeJSON([]byte(want))
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := expr.EncodeJSON(got, false)
	wantJSON, _ := expr.EncodeJSON(expected, false)
	if gotJSON != wantJSON {
		t.Fatalf("echoed JSON = %s, want %s", gotJSON, wantJSON)
	}
}
