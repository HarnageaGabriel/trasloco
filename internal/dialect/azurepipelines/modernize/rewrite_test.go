package modernize

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	catalogdata "github.com/HarnageaGabriel/trasloco/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
)

type vectorDiagnostic struct {
	Code   string         `json:"code"`
	At     string         `json:"at"`
	Params map[string]any `json:"params"`
}
type rewriteVector struct {
	id, input, output string
	diagnostics       []vectorDiagnostic
}

// Read the independent oracle directly. YAML cells have only three escapes;
// diagnostic JSON must retain its own escapes until json.Unmarshal.
func rewriteVectors(t testing.TB) []rewriteVector {
	t.Helper()
	data, err := os.ReadFile("../../../../docs/spec/modernize-ado.md")
	if err != nil {
		t.Fatal(err)
	}
	section := strings.Split(strings.Split(string(data), "## 10. Test vectors\n")[1], "## 11.")[0]
	row := regexp.MustCompile("(?m)^\\| (MODV-[0-9]+) \\| `([^`]+)` \\| (.*?)<br>diags: (.*?) \\| ")
	decode := strings.NewReplacer(`\n`, "\n", `\r`, "\r", `\|`, "|")
	var vectors []rewriteVector
	for _, m := range row.FindAllStringSubmatch(section, -1) {
		v := rewriteVector{id: m[1], input: decode.Replace(m[2])}
		v.output = v.input
		if m[3] != "unchanged" {
			if !strings.HasPrefix(m[3], "`") || !strings.HasSuffix(m[3], "`") {
				t.Fatalf("%s invalid output cell", v.id)
			}
			v.output = decode.Replace(strings.Trim(m[3], "`"))
		}
		if err := json.Unmarshal([]byte(strings.ReplaceAll(m[4], "`", "")), &v.diagnostics); err != nil {
			t.Fatalf("%s: %v", v.id, err)
		}
		vectors = append(vectors, v)
	}
	if len(vectors) != 80 {
		t.Fatalf("loaded %d / 80 vectors", len(vectors))
	}
	return vectors
}

func projectDiagnostics(ds []diag.Diagnostic) []vectorDiagnostic {
	result := []vectorDiagnostic{}
	for _, d := range ds {
		at := ""
		if d.Source != nil {
			at = fmt.Sprintf("%d:%d", d.Source.Line, d.Source.Col)
		}
		result = append(result, vectorDiagnostic{d.Code, at, d.Params})
	}
	return result
}

func TestRewriteMODV(t *testing.T) {
	cat := testCatalog(t)
	passed := 0
	for _, v := range rewriteVectors(t) {
		t.Run(v.id, func(t *testing.T) {
			input := []byte(v.input)
			original := bytes.Clone(input)
			out, ds, changed, err := Rewrite(context.Background(), input, "vector.yml", cat)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(input, original) {
				t.Fatal("mutated input")
			}
			if string(out) != v.output {
				t.Errorf("output\n got: %q\nwant: %q", out, v.output)
			}
			if changed != (v.input != v.output) {
				t.Errorf("changed=%v", changed)
			}
			got := projectDiagnostics(ds)
			if !reflect.DeepEqual(got, v.diagnostics) {
				g, _ := json.Marshal(got)
				w, _ := json.Marshal(v.diagnostics)
				t.Errorf("diagnostics\n got: %s\nwant: %s", g, w)
			}
			again, againDS, againChanged, againErr := Rewrite(context.Background(), input, "vector.yml", cat)
			if againErr != nil || againChanged != changed || !bytes.Equal(out, again) || !reflect.DeepEqual(ds, againDS) {
				t.Fatal("nondeterministic rewrite")
			}
			if !t.Failed() {
				passed++
			}
		})
	}
	t.Logf("MODV: %d / 80 passing", passed)
}

func TestRewriteRejectsInvalidSourceRanges(t *testing.T) {
	src := []byte("steps:\n- tAsk: GrAdle@1\n  \xeb:")
	out, ds, changed, err := Rewrite(context.Background(), src, "invalid.yml", testCatalog(t))
	if err != nil || changed || !bytes.Equal(out, src) || len(ds) != 1 || ds[0].Code != "TRL-INT-0002" {
		t.Fatalf("invalid source ranges: changed=%v err=%v diagnostics=%v", changed, err, ds)
	}
}

func TestRewriteSelfCheckCollectionBoundary(t *testing.T) {
	src := []byte("steps:\n- task: CopyFiles@1\n  inputs:\n    Contents: src")
	doc, _ := parseRewrite("test.yml", src)
	for _, tc := range []struct {
		name, insertion string
		valid           bool
	}{
		{"new entry", "\n    TargetFolder: out", true},
		{"changes last scalar", "suffix", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edits := []yamlx.Patch{{Start: len(src), End: len(src), Replacement: []byte(tc.insertion)}}
			out, err := ApplyEdits(src, edits)
			if err != nil {
				t.Fatal(err)
			}
			if err := selfCheck("test.yml", src, out, doc, edits, 1); (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}

func TestRewriteIdempotence(t *testing.T) {
	cat := testCatalog(t)
	for _, v := range rewriteVectors(t) {
		t.Run(v.id, func(t *testing.T) {
			out, ds, changed, err := Rewrite(context.Background(), []byte(v.output), "vector.yml", cat)
			if err != nil || changed || string(out) != v.output {
				t.Fatalf("second run: changed=%v err=%v output=%q", changed, err, out)
			}
			for _, d := range ds {
				if d.Code == "TRL-MOD-0001" || d.Code == "TRL-MOD-0002" || d.Code == "TRL-MOD-0008" || d.Code == "TRL-MOD-0011" {
					t.Errorf("second run rewrite diagnostic: %s", d.Code)
				}
			}
		})
	}
}

func FuzzRewrite(f *testing.F) {
	cat, err := catalog.Load(context.Background(), catalogdata.FS, diag.NewCollector())
	if err != nil {
		f.Fatal(err)
	}
	for _, v := range rewriteVectors(f) {
		f.Add([]byte(v.input))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 65536 {
			t.Skip()
		}
		original := bytes.Clone(input)
		out, _, edits, err := rewrite(context.Background(), input, "fuzz.yml", cat)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(input, original) {
			t.Fatal("mutated source")
		}
		twice, ds, changed, err := Rewrite(context.Background(), out, "fuzz.yml", cat)
		if err != nil || changed || !bytes.Equal(out, twice) {
			t.Fatalf("not idempotent: %v", err)
		}
		for _, d := range ds {
			if d.Code == "TRL-MOD-0001" || d.Code == "TRL-MOD-0002" {
				t.Fatal("repeat rewrite diagnostic")
			}
		}
		if len(edits) > 0 {
			want, err := ApplyEdits(input, edits)
			if err != nil || !bytes.Equal(out, want) {
				t.Fatal("change outside edit plan")
			}
			doc, _ := parseRewrite("fuzz.yml", input)
			steps := 0
			idx := newIndex(nil)
			idx.onStep = func(_, _ *yamlx.Node) { steps++ }
			for _, root := range doc.Documents {
				walk(root, walkContext{}, func(Finding) {}, idx)
			}
			if err := selfCheck("fuzz.yml", input, out, doc, edits, steps); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestRewriteSelfCheckRejectsUnplannedChanges(t *testing.T) {
	src := []byte("steps:\n- task: CopyFiles@1\n- script: echo unchanged\n")
	doc, _ := parseRewrite("test.yml", src)
	start := bytes.Index(src, []byte("CopyFiles@1"))
	edits := []yamlx.Patch{{Start: start, End: start + len("CopyFiles@1"), Replacement: []byte("CopyFiles@2")}}
	out, _ := ApplyEdits(src, edits)
	out = bytes.ReplaceAll(out, []byte("echo unchanged"), []byte("echo changed"))
	if selfCheck("test.yml", src, out, doc, edits, 1) == nil {
		t.Fatal("accepted an unplanned change")
	}
}
