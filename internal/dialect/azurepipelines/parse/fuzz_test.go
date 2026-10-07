package parse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/asttest"
)

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
