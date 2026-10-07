package asttest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/asttest"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
)

// TestDumpCoversEveryNodeShape dumps real fixtures, which exercise lists,
// mappings, free-form values, directives and embedded expressions.
func TestDumpCoversEveryNodeShape(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "..", "testdata", "azurepipelines", "structure", "*.yml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("fixtures: %d files, err %v", len(files), err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		pipeline := parse.Source(filepath.Base(file), data, diag.NewCollector())
		plain := asttest.Dump(pipeline, false)
		positioned := asttest.Dump(pipeline, true)
		if len(plain) == 0 || len(positioned) <= len(plain) {
			t.Errorf("%s: %d plain lines, %d positioned lines", file, len(plain), len(positioned))
		}
		for _, line := range positioned {
			if strings.HasSuffix(line, " = ") {
				t.Errorf("%s: empty value rendered as %q", file, line)
			}
		}
	}
}
