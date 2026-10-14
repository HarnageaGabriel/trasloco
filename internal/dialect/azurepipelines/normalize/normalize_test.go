package normalize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/HarnageaGabriel/trasloco/internal/corpus"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/resolve"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/HarnageaGabriel/trasloco/internal/source/localfs"
)

var update = flag.Bool("update", false, "update canonical IR golden files")

const normalizedCount = 229

// resolveFailures pins the positive corpus fixtures whose template resolution
// reports error diagnostics. They are not normalized; the reason is the
// documented cause. Any change to this table is a deliberate review.
var resolveFailures = map[string]struct {
	codes  []string
	reason string
}{
	"templates/cross-repo/parts/local-wrapper.yml": {
		codes:  []string{"TRL-TPL-0007"},
		reason: "template fragment is resolved as a root but declares no resources.repositories alias 'common'; the alias comes from its caller",
	},
}

// tokenDiff lists the comma-separated tokens that differ between two canonical
// IR documents (the documents are single-line JSON).
func tokenDiff(want, got string) string {
	w, g := strings.Split(want, ","), strings.Split(got, ",")
	var out strings.Builder
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			fmt.Fprintf(&out, "token %d\n  want %s\n  got  %s\n", i, a, b)
		}
	}
	return out.String()
}

func TestCorpusGoldens(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "testdata", "azurepipelines", "corpus")
	corpusFS := os.DirFS(root)
	index, err := corpus.Load(corpusFS)
	if err != nil {
		t.Fatal(err)
	}
	source, err := localfs.New(map[string]string{
		"":       root,
		"common": filepath.Join(root, "_repos", "templates-common"),
		"scans":  filepath.Join(root, "_repos", "security-scans"),
	})
	if err != nil {
		t.Fatal(err)
	}
	readSource := func(file string) ([]byte, bool) {
		// `alias@path` names a file of a template repository.
		if i := strings.LastIndex(file, "@"); i >= 0 {
			dirs := map[string]string{"common": "templates-common", "scans": "security-scans"}
			dir, ok := dirs[file[i+1:]]
			if !ok {
				dir, ok = dirs[file[:i]]
				file = file[i+1:]
			} else {
				file = file[:i]
			}
			if !ok {
				return nil, false
			}
			file = "_repos/" + dir + "/" + file
		}
		data, err := fs.ReadFile(corpusFS, file)
		return data, err == nil
	}

	normalized, failed := 0, 0
	seenFailures := map[string]bool{}
	wantGoldens := map[string]bool{}
	for _, entry := range index.Files {
		if !entry.Expect.OK {
			continue
		}
		entry := entry
		t.Run(entry.Path, func(t *testing.T) {
			data, err := fs.ReadFile(corpusFS, entry.Path)
			if err != nil {
				t.Fatal(err)
			}
			opts := Options{ID: strings.TrimSuffix(filepath.ToSlash(entry.Path), filepath.Ext(entry.Path)), SourceText: readSource}
			resolved, errCodes := parseAndResolve(t, entry.Path, data, source)
			if len(errCodes) != 0 {
				want, pinned := resolveFailures[entry.Path]
				if !pinned {
					t.Fatalf("unpinned resolve failure %v", errCodes)
				}
				if diff := cmp.Diff(want.codes, errCodes); diff != "" {
					t.Fatalf("pinned resolve codes (-want +got):\n%s", diff)
				}
				seenFailures[entry.Path] = true
				failed++
				return
			}
			if _, pinned := resolveFailures[entry.Path]; pinned {
				t.Fatalf("pinned failure %s now resolves; remove it from resolveFailures", entry.Path)
			}
			first, err := Pipeline(context.Background(), resolved, opts)
			if err != nil {
				t.Fatal(err)
			}
			normalized++
			if findings := ir.Validate(first); len(findings) != 0 {
				t.Fatalf("IR validation: %v", findings)
			}
			firstJSON, err := ir.Canonical(first)
			if err != nil {
				t.Fatal(err)
			}
			reresolved, _ := parseAndResolve(t, entry.Path, data, source)
			second, err := Pipeline(context.Background(), reresolved, opts)
			if err != nil {
				t.Fatal(err)
			}
			secondJSON, err := ir.Canonical(second)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(string(firstJSON), string(secondJSON)); diff != "" {
				t.Fatalf("non-deterministic (-first +second):\n%s", diff)
			}

			golden := filepath.Join("testdata", filepath.FromSlash(entry.Path)+".ir.json")
			wantGoldens[filepath.ToSlash(golden)] = true
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, append(firstJSON, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden (run with -update): %v", err)
			}
			if !bytes.Equal(want, append(firstJSON, '\n')) {
				t.Errorf("golden mismatch; run with -update\n%s", tokenDiff(string(want), string(firstJSON)+"\n"))
			}
		})
	}
	t.Logf("normalized=%d resolve-failures=%d", normalized, failed)
	if failed != len(resolveFailures) || len(seenFailures) != len(resolveFailures) {
		t.Fatalf("resolve failures = %d (seen %d), pinned %d", failed, len(seenFailures), len(resolveFailures))
	}
	if normalized != normalizedCount {
		t.Fatalf("normalized count = %d, want %d", normalized, normalizedCount)
	}
	if !*update {
		_ = filepath.WalkDir("testdata", func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".ir.json") && !wantGoldens[filepath.ToSlash(p)] {
				t.Errorf("orphan golden %s", p)
			}
			return nil
		})
	}
}

// parseAndResolve parses a fixture and expands its templates. It returns the
// resolved AST and the sorted error-severity codes (nil AST when non-empty).
func parseAndResolve(t *testing.T, path string, data []byte, source resolve.Source) (*ast.Pipeline, []string) {
	t.Helper()
	collector := diag.NewCollector()
	parsed := parse.Source(path, data, collector)
	for _, finding := range collector.List() {
		if finding.Severity == diag.SeverityError {
			t.Fatalf("parse: %s", finding.Code)
		}
	}
	resolved, err := resolve.Resolve(context.Background(), parsed, source, resolve.Options{Collector: collector})
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, finding := range collector.List() {
		if finding.Severity == diag.SeverityError {
			codes = append(codes, finding.Code)
			t.Logf("resolve %s: %s (%s:%d)", finding.Code, finding.Message, finding.Source.File, finding.Source.Line)
		}
	}
	slices.Sort(codes)
	if len(codes) != 0 {
		return nil, codes
	}
	return resolved, nil
}

func TestNilAndCancellation(t *testing.T) {
	if _, err := Pipeline(context.Background(), nil, Options{}); err == nil {
		t.Fatal("nil pipeline accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Pipeline(ctx, nil, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestResolverBoundaryAndOriginChain(t *testing.T) {
	t.Run("compile-time directive reports resolver diagnostic", func(t *testing.T) {
		data := []byte("steps:\n- ${{ if true }}:\n  - script: echo ok\n")
		parsed := parse.Source("pipeline.yml", data, diag.NewCollector())
		collector := diag.NewCollector()
		_, err := Pipeline(context.Background(), parsed, Options{Collector: collector})
		if !errors.Is(err, ErrNeedsResolver) {
			t.Fatalf("got %v", err)
		}
		findings := collector.List()
		if len(findings) != 1 || findings[0].Code != "TRL-CONV-0010" {
			t.Fatalf("findings = %v", findings)
		}
	})

	t.Run("origin includes resolver chain", func(t *testing.T) {
		data := []byte("steps:\n- script: echo ok\n")
		parsed := parse.Source("pipeline.yml", data, diag.NewCollector())
		callSite := diag.Source{File: "caller.yml", Line: 3, Col: 5}
		got, err := Pipeline(context.Background(), parsed, Options{
			SourceText:  func(file string) ([]byte, bool) { return data, file == "pipeline.yml" },
			SourceChain: func(diag.Source) []diag.Source { return []diag.Source{callSite} },
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.Origin == nil || len(got.Origin.Chain) != 1 || got.Origin.Chain[0].Platform != Platform {
			t.Fatalf("origin = %#v", got.Origin)
		}
	})
}

func TestDeferredExpressionsAreNotLosses(t *testing.T) {
	data := []byte("variables:\n  later: $[ variables.source ]\njobs:\n- job: deferred\n  pool: {vmImage: ubuntu-latest}\n  timeoutInMinutes: 30\n  steps:\n  - task: Bash@3\n    inputs:\n      script: $(script)\n    env:\n      MODE: $(mode)\n")
	parsed := parse.Source("pipeline.yml", data, diag.NewCollector())
	// GitHub keeps the synthesized default PR trigger exact, so the pipeline stays
	// exact and only the deferred diagnostics are asserted here.
	got, err := Pipeline(context.Background(), parsed, Options{RepositoryType: RepositoryGitHub})
	if err != nil {
		t.Fatal(err)
	}
	if got.Fidelity != diag.FidelityExact || got.Variables[0].Fidelity != diag.FidelityExact {
		t.Fatalf("deferred expressions changed fidelity: pipeline=%s variable=%s", got.Fidelity, got.Variables[0].Fidelity)
	}
	var deferred []diag.Diagnostic
	for _, finding := range got.Diagnostics {
		if finding.Code == codeDeferred {
			deferred = append(deferred, finding)
		}
	}
	if len(deferred) != 3 {
		t.Fatalf("deferred diagnostics = %d, want 3 (%v)", len(deferred), got.Diagnostics)
	}
	for _, finding := range deferred {
		if finding.Severity != diag.SeverityInfo {
			t.Fatalf("deferred diagnostic = %s/%s", finding.Code, finding.Severity)
		}
	}

	step := got.Jobs[0].Steps[1] // implicit checkout precedes the source task.
	var bag struct {
		Deferred []map[string]any `json:"deferred"`
		Losses   []map[string]any `json:"losses"`
	}
	if err := json.Unmarshal(step.Extensions[ExtensionKey], &bag); err != nil {
		t.Fatal(err)
	}
	if len(bag.Deferred) != 2 || len(bag.Losses) != 0 {
		t.Fatalf("extension records: deferred=%d losses=%d", len(bag.Deferred), len(bag.Losses))
	}
	for _, record := range bag.Deferred {
		if _, exists := record["category"]; exists {
			t.Fatalf("duplicate category field in %#v", record)
		}
		if _, exists := record["value"]; exists {
			t.Fatalf("empty value field in %#v", record)
		}
	}
}
