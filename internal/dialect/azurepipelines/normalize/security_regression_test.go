package normalize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

func TestSourceNumberConversionsUseCheckedHelper(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	patterns := []string{"strconv.Atoi(", "strconv.ParseInt(", "strconv.ParseUint(", "strconv.ParseFloat(", "time.ParseDuration(", "json.Number("}
	violations, helperCalls := 0, 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, pattern := range patterns {
			count := strings.Count(string(data), pattern)
			if entry.Name() == "number.go" && pattern == "strconv.Atoi(" {
				helperCalls += count
				continue
			}
			violations += count
		}
	}
	if violations != 0 || helperCalls != 1 {
		t.Fatalf("unchecked conversions=%d helper conversions=%d", violations, helperCalls)
	}
}

func TestDeclaredSecretShapesAreOmittedBeforeTypedConversion(t *testing.T) {
	tests := []struct {
		name, kind, value, leak string
	}{
		{name: "negative decimal", kind: "number", value: "-12.5", leak: "-12.5"},
		{name: "exponent", kind: "string", value: `"1e3"`, leak: "1e3"},
		{name: "whitespace wrapped", kind: "string", value: `" 42 "`, leak: " 42 "},
		{name: "boolean like", kind: "boolean", value: "TrUe", leak: "TrUe"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, collector := resolvedPipeline(t, "parameters:\n- name: dbPassword\n  type: "+test.kind+"\n  default: "+test.value+"\nsteps:\n- script: echo safe\n")
			canonicalBytes, err := ir.Canonical(got)
			if err != nil {
				t.Fatal(err)
			}
			diagnostics := diagnosticsJSON(t, got, collector)
			leaks := strings.Count(string(canonicalBytes), test.leak) + strings.Count(diagnostics, test.leak)
			defaults := 0
			for _, input := range got.Inputs {
				if input.ID == "dbPassword" && input.Default != nil {
					defaults++
				}
			}
			if leaks != 0 || defaults != 0 {
				t.Fatalf("leaks=%d retained defaults=%d", leaks, defaults)
			}
		})
	}
}

func TestLoggingCoordinatesUseCheckedIntegerConversion(t *testing.T) {
	got, collector := resolvedPipeline(t, `parameters:
- name: dbPassword
  type: number
  default: 123456
steps:
- bash: |
    echo "##vso[task.logissue type=error;sourcepath=a.go;linenumber=123456;columnnumber=123456]bad"
    echo "##vso[task.setsecret]mask-me-now"
`)
	out := canonical(t, got)
	step := got.Jobs[0].Steps[len(got.Jobs[0].Steps)-1]
	coordinates, manualLosses := 0, 0
	for _, operation := range step.Run.Operations {
		if operation.Location != nil {
			if operation.Location.Line != 0 {
				coordinates++
			}
			if operation.Location.Col != 0 {
				coordinates++
			}
		}
	}
	for _, property := range lossProperties(t, step.Metadata) {
		if property == "linenumber" || property == "columnnumber" {
			manualLosses++
		}
	}
	leaks := strings.Count(out, "123456") + strings.Count(diagnosticsJSON(t, got, collector), "123456")
	if leaks != 0 || coordinates != 0 || manualLosses != 2 || step.Fidelity != diag.FidelityNeedsManual {
		t.Fatalf("leaks=%d coordinates=%d numeric losses=%d fidelity=%s", leaks, coordinates, manualLosses, step.Fidelity)
	}
}

func TestPartialRedactionOrderedKeyCollisionIsAllocated(t *testing.T) {
	input := `parameters:
- name: dbPassword
  type: string
  default: KeySecretValue99
steps:
- script: echo safe
  env:
    prefixKeySecretValue99: first
    'prefix[REDACTED]': second
    'prefix[REDACTED]-2': reserved
`
	got, collector := resolvedPipeline(t, input)
	out := requireAbsent(t, got, collector, "KeySecretValue99")
	step := got.Jobs[0].Steps[len(got.Jobs[0].Steps)-1]
	counts := map[string]int{}
	for key := range step.Env.All() {
		counts[key]++
	}
	want := []string{"prefix[REDACTED]", "prefix[REDACTED]-2", "prefix[REDACTED]-3"}
	bad := 0
	for _, key := range want {
		if counts[key] != 1 {
			bad++
		}
	}
	again, _ := resolvedPipeline(t, input)
	if bad != 0 || len(counts) != len(want) || out != canonical(t, again) || step.Fidelity != diag.FidelityNeedsManual {
		t.Fatalf("bad keys=%d unique=%d deterministic=%t fidelity=%s", bad, len(counts), out == canonical(t, again), step.Fidelity)
	}
}
