package printer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/asttest"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
)

func structureFixtures(t testing.TB) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "..", "testdata", "azurepipelines", "structure", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no structure fixtures")
	}
	return files
}

func hasError(collector *diag.Collector) bool {
	for _, diagnostic := range collector.List() {
		if diagnostic.Severity == diag.SeverityError {
			return true
		}
	}
	return false
}

func TestRoundTripVectors(t *testing.T) {
	for _, vector := range asttest.LoadVectors(t) {
		t.Run(vector.ID, func(t *testing.T) {
			firstDiagnostics := diag.NewCollector()
			first := parse.Source(vector.ID+".yml", []byte(vector.Input), firstDiagnostics)
			if hasError(firstDiagnostics) {
				t.Skip("input has error diagnostics")
			}

			printed, err := Print(first)
			if err != nil {
				t.Fatalf("Print: %v", err)
			}
			secondDiagnostics := diag.NewCollector()
			second := parse.Source("printed.yml", printed, secondDiagnostics)
			if hasError(secondDiagnostics) {
				t.Fatalf("printed output has error diagnostics: %v", secondDiagnostics.List())
			}
			if diff := asttest.Diff(first, second); diff != "" {
				t.Fatalf("round trip mismatch (-first +second):\n%s\nprinted:\n%s", diff, printed)
			}
		})
	}
}

func TestRoundTripFixtures(t *testing.T) {
	for _, file := range structureFixtures(t) {
		file := file
		t.Run(filepath.Base(file), func(t *testing.T) {
			input, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			firstDiagnostics := diag.NewCollector()
			first := parse.Source(filepath.Base(file), input, firstDiagnostics)
			if hasError(firstDiagnostics) {
				t.Skip("input has error diagnostics")
			}

			firstPrint, err := Print(first)
			if err != nil {
				t.Fatalf("first Print: %v", err)
			}
			secondDiagnostics := diag.NewCollector()
			second := parse.Source("printed.yml", firstPrint, secondDiagnostics)
			if hasError(secondDiagnostics) {
				t.Fatalf("printed output has error diagnostics: %v", secondDiagnostics.List())
			}
			if diff := asttest.Diff(first, second); diff != "" {
				t.Fatalf("round trip mismatch (-first +second):\n%s\nprinted:\n%s", diff, firstPrint)
			}
			secondPrint, err := Print(second)
			if err != nil {
				t.Fatalf("second Print: %v", err)
			}
			if diff := cmp.Diff(firstPrint, secondPrint); diff != "" {
				t.Fatalf("Print is not idempotent (-first +second):\n%s", diff)
			}
		})
	}
}

func TestNormalizedOutput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "canonical key case and order",
			input: "Steps:\n- script: echo hi\nTrigger: None\n",
			want:  "trigger: None\nsteps:\n  - script: echo hi\n",
		},
		{
			name:  "short pool and dependsOn",
			input: "jobs:\n- job: Build\n  pool: linux\n  dependsOn: Setup\n  steps:\n  - script: echo hi\n",
			want:  "jobs:\n  - job: Build\n    dependsOn: Setup\n    pool: linux\n    steps:\n      - script: echo hi\n",
		},
		{
			name:  "numeric-looking string",
			input: "variables:\n  buildNumber: '123'\n",
			want:  "variables:\n  buildNumber: 123\n",
		},
		{
			name:  "block scalar script",
			input: "steps:\n- script: |\n    echo one\n    echo two\n",
			want:  "steps:\n  - script: |\n      echo one\n      echo two\n",
		},
		{
			name:  "whole steps insertion",
			input: "steps: ${{ parameters.steps }}\n",
			want:  "steps: ${{ parameters.steps }}\n",
		},
		{
			name:  "null parameters",
			input: "pArAmeters:\n",
			want:  "parameters:\n",
		},
		{
			name:  "null variables",
			input: "vAriABles:\n",
			want:  "variables:\n",
		},
		{
			name:  "conditional list items with else",
			input: "steps:\n- ${{ if eq(parameters.run, true) }}:\n  - script: echo yes\n- ${{ else }}:\n  - script: echo no\n",
			want:  "steps:\n  - ${{ if eq(parameters.run, true) }}:\n      - script: echo yes\n  - ${{ else }}:\n      - script: echo no\n",
		},
		{
			name:  "unknown key follows typed fields",
			input: "mystery: kept\ntrigger: none\n",
			want:  "trigger: none\nmystery: kept\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			collector := diag.NewCollector()
			pipeline := parse.Source("input.yml", []byte(test.input), collector)
			if hasError(collector) {
				t.Fatalf("input has error diagnostics: %v", collector.List())
			}
			got, err := Print(pipeline)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(test.want, string(got)); diff != "" {
				t.Errorf("normalized output mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPrintNil(t *testing.T) {
	if _, err := Print(nil); err == nil {
		t.Fatal("Print(nil) returned nil error")
	}
}

func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte("parameters:"))
	f.Add([]byte("variables:"))
	for _, vector := range asttest.LoadVectors(f) {
		f.Add([]byte(vector.Input))
	}
	for _, file := range structureFixtures(f) {
		input, err := os.ReadFile(file)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(input)
	}

	f.Fuzz(func(t *testing.T, input []byte) {
		firstDiagnostics := diag.NewCollector()
		first := parse.Source("fuzz.yml", input, firstDiagnostics)
		if hasError(firstDiagnostics) {
			t.Skip()
		}
		printed, err := Print(first)
		if err != nil {
			t.Fatalf("Print: %v", err)
		}
		secondDiagnostics := diag.NewCollector()
		second := parse.Source("printed.yml", printed, secondDiagnostics)
		if hasError(secondDiagnostics) {
			t.Fatalf("printed output has error diagnostics: %v\nprinted:\n%s", secondDiagnostics.List(), printed)
		}
		if diff := asttest.Diff(first, second); diff != "" {
			t.Fatalf("round trip mismatch (-first +second):\n%s\nprinted:\n%s", diff, printed)
		}
	})
}
