package parse

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/HarnageaGabriel/trasloco/internal/corpus"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

func TestCorpusParse(t *testing.T) {
	corpusRoot := filepath.Join("..", "..", "..", "..", "testdata", "azurepipelines", "corpus")
	corpusFS := os.DirFS(corpusRoot)
	index, err := corpus.Load(corpusFS)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range index.Files {
		entry := entry
		t.Run(entry.Path, func(t *testing.T) {
			if entry.Expect.TemplatePlaceholder {
				t.Skip("template-placeholder: upstream wizard template requires rendering")
			}
			if entry.Stage == "resolve" || entry.Stage == "validate" {
				t.Skipf("stage %s: expected %v", entry.Stage, entry.Expect.Diagnostics)
			}
			data, err := fs.ReadFile(corpusFS, entry.Path)
			if err != nil {
				t.Fatal(err)
			}
			collector := diag.NewCollector()
			Source(entry.Path, data, collector)
			if entry.Expect.OK {
				for _, diagnostic := range collector.List() {
					if diagnostic.Severity == diag.SeverityError {
						t.Errorf("unexpected %s at %d:%d", diagnostic.Code, diagnostic.Source.Line, diagnostic.Source.Col)
					}
				}
				return
			}
			var got []string
			for _, diagnostic := range collector.List() {
				got = append(got, diagnostic.Code)
			}
			if diff := cmp.Diff(entry.Expect.Diagnostics, got); diff != "" {
				t.Errorf("diagnostics mismatch (-want +got):\n%s\n%s", diff,
					fmt.Sprintf("fixture stage: %s", entry.Stage))
			}
		})
	}
}
