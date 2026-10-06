package yamlx

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

func TestParseStreamsAndPositions(t *testing.T) {
	data := readFixture(t, "positions.yaml")
	document, diagnostics := Parse("positions.yaml", append([]byte{0xef, 0xbb, 0xbf}, bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))...))
	if len(diagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %#v", diagnostics)
	}
	if len(document.Documents) != 2 {
		t.Fatalf("documents = %d, want 2", len(document.Documents))
	}
	name, problems := document.Root().Lookup("NAME", true)
	if len(problems) != 0 || name == nil {
		t.Fatalf("Lookup() = %#v, %#v", name, problems)
	}
	value, problems := name.String()
	if len(problems) != 0 || value != "on" {
		t.Fatalf("String() = %q, %#v", value, problems)
	}
	if got := name.Position(); got.File != "positions.yaml" || got.Line != 2 || got.Col != 7 {
		t.Fatalf("Position() = %#v", got)
	}
	if name.LineComment() != "# ambiguous string" {
		t.Fatalf("LineComment() = %q", name.LineComment())
	}
	if document.Root().HeadComment() != "# first document" {
		t.Fatalf("HeadComment() = %q", document.Root().HeadComment())
	}
}

func TestTypedAccessors(t *testing.T) {
	document, diagnostics := Parse("types.yaml", []byte("s: text\nb: true\ni: 42\nq: [one, two]\nm: {x: y}\n"))
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	tests := []struct {
		name  string
		check func(*Node) []string
	}{
		{"string", func(root *Node) []string { value, d := lookup(t, root, "s").String(); return result(value, d) }},
		{"bool", func(root *Node) []string { value, d := lookup(t, root, "b").Bool(); return result(value, d) }},
		{"int", func(root *Node) []string { value, d := lookup(t, root, "i").Int(); return result(value, d) }},
		{"seq", func(root *Node) []string { value, d := lookup(t, root, "q").Seq(); return result(len(value), d) }},
		{"map", func(root *Node) []string { value, d := lookup(t, root, "m").Map(); return result(len(value), d) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.check(document.Root()); got[1] != "0" {
				t.Fatalf("accessor result = %v", got)
			}
		})
	}
	_, problems := lookup(t, document.Root(), "s").Bool()
	if len(problems) != 1 || problems[0].Code != "TRL-PARSE-0006" || problems[0].Source.Line != 1 {
		t.Fatalf("Bool(string) diagnostics = %#v", problems)
	}
}

func TestDuplicateKeysReportBothPositions(t *testing.T) {
	document, diagnostics := Parse("duplicate.yaml", []byte("name: first\nname: second\n"))
	if document == nil || len(diagnostics) != 1 {
		t.Fatalf("Parse() = %#v, %#v", document, diagnostics)
	}
	got := diagnostics[0]
	if got.Code != "TRL-PARSE-0003" || got.Source.Line != 2 || got.Source.Col != 1 {
		t.Fatalf("duplicate diagnostic = %#v", got)
	}
	if got.Params["firstLine"] != float64(1) || got.Params["duplicateLine"] != float64(2) {
		t.Fatalf("duplicate positions = %#v", got.Params)
	}
}

func TestAliasesMergesAndProvenance(t *testing.T) {
	data := []byte("defaults: &defaults\n  pool: ubuntu\n  retries: 2\njob:\n  <<: *defaults\n  retries: 3\n")
	document, diagnostics := Parse("merge.yaml", data)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	job := lookup(t, document.Root(), "job")
	pairs, problems := job.Map()
	if len(problems) != 0 || len(pairs) != 2 {
		t.Fatalf("job.Map() = %#v, %#v", pairs, problems)
	}
	if pairs[0].Key.Value() != "retries" || pairs[1].Key.Value() != "pool" {
		t.Fatalf("merged order = %q, %q", pairs[0].Key.Value(), pairs[1].Key.Value())
	}
	pool := lookup(t, job, "pool")
	if got := pool.Provenance(); got.Line != 2 || got.Col != 9 {
		t.Fatalf("pool provenance = %#v", got)
	}
}

func TestLimitsAndInvalidUTF8(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		options []ParseOption
		code    string
	}{
		{"size", []byte("long: value\n"), []ParseOption{WithSizeLimit(3)}, "TRL-PARSE-0005"},
		{
			"aliases",
			[]byte("a: &a [one, two, three]\nb: &b [*a, *a, *a]\nc: &c [*b, *b, *b]\nd: [*c, *c, *c]\n"),
			[]ParseOption{WithAliasExpansionLimit(20)},
			"TRL-PARSE-0007",
		},
		{"utf8", []byte{'x', ':', ' ', 0xff, '\n'}, nil, "TRL-PARSE-0004"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, diagnostics := ParseWithOptions(test.name+".yaml", test.data, test.options...)
			if len(diagnostics) == 0 || diagnostics[0].Code != test.code {
				t.Fatalf("diagnostics = %#v, want %s", diagnostics, test.code)
			}
		})
	}
}

func TestAzureDirectivesRemainStrings(t *testing.T) {
	data := []byte("${{ if eq(parameters.x, true) }}:\n  value: ${{ parameters.x }}\neach: $[ variables.x ]\nmacro: $(Name)\n")
	document, diagnostics := Parse("azure.yaml", data)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	pairs, problems := document.Root().Map()
	if len(problems) != 0 || pairs[0].Key.Value() != "${{ if eq(parameters.x, true) }}" {
		t.Fatalf("directive key = %#v, diagnostics = %#v", pairs, problems)
	}
	for _, key := range []string{"each", "macro"} {
		value, problems := lookup(t, document.Root(), key).String()
		if len(problems) != 0 || !strings.ContainsAny(value, "$[") {
			t.Fatalf("directive %s = %q, %#v", key, value, problems)
		}
	}
}

func TestEmitGolden(t *testing.T) {
	document, diagnostics := Parse("emit.yaml", readFixture(t, "emit-input.yaml"))
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	got := Emit(document, WithNormalize())
	want := readFixture(t, "emit-normalized.golden")
	if !bytes.Equal(got, want) {
		t.Fatalf("Emit() mismatch\n--- got\n%s--- want\n%s", got, want)
	}
	reparsed, problems := Parse("emitted.yaml", got)
	if len(problems) != 0 {
		t.Fatalf("emitted YAML invalid: %#v", problems)
	}
	for _, key := range []string{"on", "yes", "no", "float", "tilde", "null"} {
		if _, problems := lookup(t, reparsed.Root(), key).String(); len(problems) != 0 {
			t.Fatalf("%s changed type: %#v", key, problems)
		}
	}
}

func TestEmitPreservesBOMAndCRLF(t *testing.T) {
	input := append([]byte{0xef, 0xbb, 0xbf}, []byte("# head\r\nkey:\r\n    nested: value # line\r\n")...)
	document, diagnostics := Parse("format.yaml", input)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	got := Emit(document)
	if !bytes.HasPrefix(got, []byte{0xef, 0xbb, 0xbf}) || bytes.Contains(bytes.TrimPrefix(got, []byte{0xef, 0xbb, 0xbf}), []byte("\n")) && !bytes.Contains(got, []byte("\r\n")) {
		t.Fatalf("format not preserved: %q", got)
	}
	if !bytes.Contains(got, []byte("\r\n    nested:")) {
		t.Fatalf("indentation not preserved: %q", got)
	}
}

func FuzzParse(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		[]byte("key: value\n"),
		[]byte("---\na: &a [1, 2]\n---\nb: *a\n"),
		[]byte("${{ each s in parameters.stages }}:\n- ${{ s }}\n"),
		{0xff, 0xfe, 0xfd},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(_ *testing.T, data []byte) {
		document, _ := ParseWithOptions("fuzz.yaml", data, WithSizeLimit(1<<20), WithAliasExpansionLimit(1_000))
		if document != nil {
			_ = Emit(document, WithNormalize())
			for _, root := range document.Documents {
				_, _ = root.Map()
				_, _ = root.Seq()
				_, _ = root.String()
				_, _ = root.Bool()
				_, _ = root.Int()
			}
		}
	})
}

func lookup(t *testing.T, node *Node, key string) *Node {
	t.Helper()
	value, diagnostics := node.Lookup(key)
	if len(diagnostics) != 0 || value == nil {
		t.Fatalf("Lookup(%q) = %#v, %#v", key, value, diagnostics)
	}
	return value
}

func result(value any, diagnostics []diag.Diagnostic) []string {
	return []string{fmt.Sprint(value), strconv.Itoa(len(diagnostics))}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "yamlx", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
