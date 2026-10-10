package resolve

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/corpus"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/source/localfs"
	"github.com/google/go-cmp/cmp"
)

func TestLaterStageCorpus(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "testdata", "azurepipelines", "corpus")
	index, err := corpus.Load(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	source, err := localfs.New(map[string]string{"": root})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range index.Files {
		if entry.Stage != "resolve" && entry.Stage != "validate" {
			continue
		}
		count++
		entry := entry
		t.Run(entry.Path, func(t *testing.T) {
			data, err := fs.ReadFile(os.DirFS(root), entry.Path)
			if err != nil {
				t.Fatal(err)
			}
			collector := diag.NewCollector()
			pipeline := parse.Source(entry.Path, data, collector)
			_, err = Resolve(context.Background(), pipeline, source, Options{Collector: collector})
			if err != nil {
				t.Fatal(err)
			}
			got := codes(collector)
			if diff := cmp.Diff(entry.Expect.Diagnostics, got); diff != "" {
				t.Fatalf("diagnostics (-want +got):\n%s", diff)
			}
		})
	}
	if count != 17 {
		t.Fatalf("later-stage fixtures = %d, want 17", count)
	}
}
