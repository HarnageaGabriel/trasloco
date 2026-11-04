package parse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/asttest"
)

// The seed corpus in testdata/fuzz/FuzzParse holds original, hand-written
// Azure Pipelines YAML in the style of the public Microsoft documentation.
// It is not copied from private, employer-owned or customer-owned material
// (clean-room rule in CONTRIBUTING.md). The seeds cover the shapes the
// documentation uses: stages, jobs, deployment jobs, strategy matrices,
// template and runtime parameters, resources, triggers and schedules,
// conditions, long ${{ }} template expressions, flow sequences, literal and
// folded block scalars, odd indentation, tab indentation, CRLF line endings
// and a UTF-8 BOM.
//
// Documentation used to derive the seeds (public Microsoft Learn pages):
//
//   - https://learn.microsoft.com/en-us/azure/devops/pipelines/yaml-schema/pipeline
//   - https://learn.microsoft.com/en-us/azure/devops/pipelines/yaml-schema/jobs
//   - https://learn.microsoft.com/en-us/azure/devops/pipelines/yaml-schema/jobs-job
//   - https://learn.microsoft.com/en-us/azure/devops/pipelines/yaml-schema/jobs-deployment
//   - https://learn.microsoft.com/en-us/azure/devops/pipelines/yaml-schema/jobs-deployment-strategy
//   - https://learn.microsoft.com/en-us/azure/devops/pipelines/yaml-schema/jobs-job-strategy
//   - https://learn.microsoft.com/en-us/azure/devops/pipelines/yaml-schema/parameters-parameter
//   - https://learn.microsoft.com/en-us/azure/devops/pipelines/yaml-schema/resources
//   - https://learn.microsoft.com/en-us/azure/devops/pipelines/yaml-schema/triggers
//   - https://learn.microsoft.com/en-us/azure/devops/pipelines/yaml-schema/schedules
//   - https://learn.microsoft.com/en-us/azure/devops/pipelines/process/templates
//   - https://learn.microsoft.com/en-us/azure/devops/pipelines/process/runtime-parameters
//   - https://learn.microsoft.com/en-us/azure/devops/pipelines/process/deployment-jobs
//   - https://learn.microsoft.com/en-us/azure/devops/pipelines/process/conditions
//   - https://learn.microsoft.com/en-us/azure/devops/pipelines/process/expressions
func FuzzParse(f *testing.F) {
	for _, vector := range asttest.LoadVectors(f) {
		f.Add([]byte(vector.Input))
	}
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "..", "testdata", "azurepipelines", "structure", "*.yml"))
	if err != nil {
		f.Fatal(err)
	}
	if len(files) == 0 {
		f.Fatal("no structure fixtures")
	}
	for _, file := range files {
		input, err := os.ReadFile(file)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(input)
	}
	for _, input := range [][]byte{
		{},
		{0xff},
		[]byte("a: &x [*x]"),
		[]byte("- ${{ if x }}:"),
		[]byte("${{"),
		[]byte(strings.Repeat("[", 2000) + strings.Repeat("]", 2000)),
	} {
		f.Add(input)
	}

	f.Fuzz(func(t *testing.T, input []byte) {
		collector := diag.NewCollector()
		Source("fuzz.yml", input, collector)
		for _, diagnostic := range collector.List() {
			if diagnostic.Source == nil {
				t.Errorf("diagnostic %q has nil source", diagnostic.Code)
				continue
			}
			if diagnostic.Source.File == "" {
				t.Errorf("diagnostic %q has empty source file", diagnostic.Code)
			}
			if !diag.ValidCode(diagnostic.Code) {
				t.Errorf("diagnostic has invalid code %q", diagnostic.Code)
			}
		}
	})
}
