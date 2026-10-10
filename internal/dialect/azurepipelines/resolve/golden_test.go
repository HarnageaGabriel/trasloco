package resolve

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/printer"
	"github.com/HarnageaGabriel/trasloco/internal/source/localfs"
	"github.com/google/go-cmp/cmp"
)

var update = flag.Bool("update", false, "update resolver goldens")

func TestTemplateGoldens(t *testing.T) {
	corpusRoot := filepath.Join("..", "..", "..", "..", "testdata", "azurepipelines", "corpus")
	source, err := localfs.New(map[string]string{"": corpusRoot, "common": filepath.Join(corpusRoot, "_repos", "templates-common"), "scans": filepath.Join(corpusRoot, "_repos", "security-scans")})
	if err != nil {
		t.Fatal(err)
	}
	mainFiles, err := filepath.Glob(filepath.Join(corpusRoot, "templates", "*", "main.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, filename := range mainFiles {
		filename := filename
		name := filepath.Base(filepath.Dir(filename))
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			relative, _ := filepath.Rel(corpusRoot, filename)
			relative = filepath.ToSlash(relative)
			resolveAndPrint := func() []byte {
				collector := diag.NewCollector()
				pipeline := parse.Source(relative, data, collector)
				resolved, resolveErr := Resolve(context.Background(), pipeline, source, Options{Collector: collector})
				if resolveErr != nil {
					t.Fatal(resolveErr)
				}
				assertNoErrors(t, collector)
				printed, printErr := printer.Print(resolved)
				if printErr != nil {
					t.Fatal(printErr)
				}
				return printed
			}
			first := resolveAndPrint()
			second := resolveAndPrint()
			if diff := cmp.Diff(string(first), string(second)); diff != "" {
				t.Fatalf("unstable resolution from re-parsed input (-first +second):\n%s", diff)
			}
			golden := filepath.Join(corpusRoot, "..", "goldens", "resolve", name+".yml")
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, first, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(want), "${{") {
				t.Fatalf("golden contains an unresolved compile-time directive:\n%s", want)
			}
			if diff := cmp.Diff(strings.ReplaceAll(string(want), "\r\n", "\n"), strings.ReplaceAll(string(first), "\r\n", "\n")); diff != "" {
				t.Fatalf("golden (-want +got):\n%s", diff)
			}
		})
	}
}
