package emit

import (
	"bytes"
	"strings"
	"testing"

	goyaml "go.yaml.in/yaml/v3"

	"github.com/HarnageaGabriel/trasloco/internal/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/githubactions/emit/model"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/google/go-cmp/cmp"
)

// fixedNaming is the writer-test naming service: stable, simple names so the
// byte-level expectations do not depend on the naming policy.
type fixedNaming struct{}

func (fixedNaming) WorkflowFile(string) (string, error)            { return "ci.yml", nil }
func (fixedNaming) JobID(_, jobID string) (string, error)          { return jobID, nil }
func (fixedNaming) StepID(_, stepID string) (string, error)        { return stepID, nil }
func (fixedNaming) SecretName(secret ir.SecretRef) (string, error) { return secret.Name, nil }

// TestEmitOutputElementPaths pins the JSON Pointer path of every element of a
// representative document, in file traversal order (emit-gha.md 2.2, 2.3).
func TestEmitOutputElementPaths(t *testing.T) {
	pipeline := &ir.Pipeline{
		ID:   "ci",
		Name: "ci",
		Jobs: []ir.Job{{
			ID:    "j",
			Name:  "j",
			Steps: []ir.Step{{ID: "s", Name: "s", Kind: ir.StepRun}},
		}},
	}
	result := Emit(pipeline, catalog.ActionsLock{}, fixedNaming{}, Options{}, nil)
	if len(result.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(result.Files))
	}
	want := []OutputElement{
		{".github/workflows/ci.yml#/name", "pipeline:ci"},
		{".github/workflows/ci.yml#/on", "pipeline:ci"},
		{".github/workflows/ci.yml#/on/workflow_dispatch", "pipeline:ci"},
		{".github/workflows/ci.yml#/permissions", "pipeline:ci"},
		{".github/workflows/ci.yml#/jobs", "pipeline:ci"},
		{".github/workflows/ci.yml#/jobs/j", "job:j"},
		{".github/workflows/ci.yml#/jobs/j/permissions", "job:j"},
		{".github/workflows/ci.yml#/jobs/j/runs-on", "job:j"},
		{".github/workflows/ci.yml#/jobs/j/steps", "job:j"},
		{".github/workflows/ci.yml#/jobs/j/steps/0", "job:j"},
		{".github/workflows/ci.yml#/jobs/j/steps/0/name", "job:j"},
		{".github/workflows/ci.yml#/jobs/j/steps/0/shell", "job:j"},
		{".github/workflows/ci.yml#/jobs/j/steps/0/run", "job:j"},
	}
	if diff := cmp.Diff(want, result.OutputElements); diff != "" {
		t.Errorf("output elements (-want +got):\n%s", diff)
	}
}

// TestWriterFinalizeIsDeterministic checks two finalizations of the same
// writer produce identical bytes and elements, with the output invariants of
// INV-01.
func TestWriterFinalizeIsDeterministic(t *testing.T) {
	writer := newFileWriter(".github/workflows/ci.yml")
	writer.writeName("pipeline:ci", "ci")
	writer.writeWorkflowDispatch("pipeline:ci")
	writer.writeWorkflowPermissions("pipeline:ci")
	writer.startJobs("pipeline:ci")
	writer.writePlaceholderJob("job:j", "j", model.Job{
		Permissions: model.EmptyMap(),
		RunsOn:      placeholderRunsOn,
	}, manualConversionCode, 0)
	first, firstElements, err := writer.finalize()
	if err != nil {
		t.Fatalf("first finalize: %v", err)
	}
	second, secondElements, err := writer.finalize()
	if err != nil {
		t.Fatalf("second finalize: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("two finalizations differ:\n%s\n---\n%s", first, second)
	}
	if diff := cmp.Diff(firstElements, secondElements); diff != "" {
		t.Errorf("two element lists differ (-first +second):\n%s", diff)
	}
	if bytes.HasPrefix(first, []byte{0xEF, 0xBB, 0xBF}) {
		t.Error("output starts with a BOM")
	}
	if bytes.Contains(first, []byte("\r")) {
		t.Error("output contains a carriage return")
	}
	if !bytes.HasSuffix(first, []byte("\n")) || bytes.HasSuffix(first, []byte("\n\n")) {
		t.Errorf("output must end with exactly one LF, got %q", first[len(first)-2:])
	}
	if !bytes.HasPrefix(first, []byte(workflowHeaderComment+"\n")) {
		t.Errorf("output does not start with the header comment:\n%s", first)
	}
	if !bytes.Contains(first, []byte("\non:\n")) {
		t.Errorf("workflow event key is not plain on:\n%s", first)
	}
}

// TestPointerEscape checks RFC 6901 reference-token escaping.
func TestPointerEscape(t *testing.T) {
	for _, testCase := range []struct{ in, want string }{
		{"", ""},
		{"job", "job"},
		{"a/b", "a~1b"},
		{"a~b", "a~0b"},
		{"a~/b", "a~0~1b"},
	} {
		if got := pointerEscape(testCase.in); got != testCase.want {
			t.Errorf("pointerEscape(%q) = %q, want %q", testCase.in, got, testCase.want)
		}
	}
}

// TestPlaceholderJobEscapesPointer checks that a job key with "~" and "/"
// yields an escaped JSON Pointer.
func TestPlaceholderJobEscapesPointer(t *testing.T) {
	writer := newFileWriter(".github/workflows/ci.yml")
	writer.startJobs("pipeline:ci")
	writer.writePlaceholderJob("job:j", "j~x/y", model.Job{
		Permissions: model.EmptyMap(),
		RunsOn:      placeholderRunsOn,
	}, manualConversionCode, 0)
	if _, _, err := writer.finalize(); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	want := ".github/workflows/ci.yml#/jobs/j~0x~1y"
	foundJob := false
	for _, element := range writer.elements {
		if element.NodeID != "job:j" {
			continue
		}
		foundJob = true
		if element.Path != want && !strings.HasPrefix(element.Path, want+"/") {
			t.Errorf("element path %q is not below %q", element.Path, want)
		}
	}
	if !foundJob {
		t.Errorf("no output element for the placeholder job in %v", writer.elements)
	}
}

// renderTestValue styles and encodes one mapping with a single value.
func renderTestValue(t *testing.T, value string) string {
	t.Helper()
	root := model.Map(model.Pair{Key: "k", Value: model.String(value)})
	styleDocument(root)
	data, err := encodeDocument(root)
	if err != nil {
		t.Fatalf("encode %q: %v", value, err)
	}
	return strings.TrimSuffix(string(data), "\n")
}

// TestScalarStyles checks INV-07 against the rules of emit-gha.md 4.2.
func TestScalarStyles(t *testing.T) {
	for _, testCase := range []struct{ value, want string }{
		{"plain", "k: plain"},
		{"yes", "k: 'yes'"},
		{"on", "k: 'on'"},
		{"1e3", "k: '1e3'"},
		{"*-releases", "k: '*-releases'"},
		{"${{ always() }}", "k: '${{ always() }}'"},
		{"it's", `k: "it's"`},
		{`say "hi"`, `k: 'say "hi"'`},
		{"a: b", "k: 'a: b'"},
		{"a # b", "k: 'a # b'"},
		{"", "k: ''"},
		{"line\nbreak", `k: "line\nbreak"`},
		{"tab\there", `k: "tab\there"`},
		{"back\\slash", `k: 'back\slash'`},
	} {
		if got := renderTestValue(t, testCase.value); got != testCase.want {
			t.Errorf("value %q rendered %q, want %q", testCase.value, got, testCase.want)
		}
	}
}

// TestRunBlockStyles checks the run block-scalar indicators of 4.2.
func TestRunBlockStyles(t *testing.T) {
	for _, testCase := range []struct{ value, want string }{
		{"echo hi\nexit 0\n", "run: |\n  echo hi\n  exit 0"},
		{"echo hi", "run: echo hi"},
		{" echo hi\n", "run: |2\n   echo hi"},
		{"\necho\n", "run: |2\n\n  echo"},
		{"echo\r\n", `run: "echo\r\n"`},
	} {
		root := model.Map(model.Pair{Key: "run", Value: model.String(testCase.value)})
		styleDocument(root)
		data, err := encodeDocument(root)
		if err != nil {
			t.Fatalf("encode run %q: %v", testCase.value, err)
		}
		got := strings.TrimSuffix(string(data), "\n")
		if got != testCase.want {
			t.Errorf("run %q rendered %q, want %q", testCase.value, got, testCase.want)
		}
	}
}

// TestEncodeStructReportsDefect checks that an unencodable value is returned
// as an error, never written to a document.
func TestEncodeStructReportsDefect(t *testing.T) {
	if _, err := encodeStruct(make(chan int)); err == nil {
		t.Error("encoding a channel succeeded; it must fail")
	}
}

// TestStyleDocumentKeyOnOnlyAtRoot checks that only the workflow key "on" is
// an exception; a nested key with the same spelling is quoted.
func TestStyleDocumentKeyOnOnlyAtRoot(t *testing.T) {
	root := model.Map(model.Pair{Key: "on", Value: model.Map(model.Pair{
		Key: "on", Value: model.String("value"),
	})})
	styleDocument(root)
	data, err := encodeDocument(root)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "on:\n") {
		t.Errorf("root on key is not plain:\n%s", text)
	}
	if !strings.Contains(text, "'on': value") {
		t.Errorf("nested on key is not quoted:\n%s", text)
	}
}

// TestEncodeStructKeepsFieldOrder checks that the fixed model field order is
// the document order.
func TestEncodeStructKeepsFieldOrder(t *testing.T) {
	node, err := encodeStruct(model.Step{Name: "n", Uses: "u", Shell: "s", Run: "r"})
	if err != nil {
		t.Fatalf("encode step: %v", err)
	}
	var keys []string
	for index := 0; index+1 < len(node.Content); index += 2 {
		keys = append(keys, node.Content[index].Value)
	}
	want := []string{"name", "uses", "shell", "run"}
	if diff := cmp.Diff(want, keys); diff != "" {
		t.Errorf("step keys (-want +got):\n%s", diff)
	}
	if node.Kind != goyaml.MappingNode {
		t.Errorf("kind = %v, want mapping", node.Kind)
	}
}

// TestRunBlockStyleSelection checks the style decision before the encoder
// derives the concrete indicator.
func TestRunBlockStyleSelection(t *testing.T) {
	for _, testCase := range []struct {
		value string
		want  goyaml.Style
	}{
		{"echo hi\nexit 0\n", goyaml.LiteralStyle},
		{"echo hi\n\n", goyaml.LiteralStyle},
		{" echo hi\n", goyaml.LiteralStyle},
		{"echo\r\n", goyaml.DoubleQuotedStyle},
		{"echo\u0085\n", goyaml.DoubleQuotedStyle},
	} {
		if got := runBlockStyle(testCase.value); got != testCase.want {
			t.Errorf("runBlockStyle(%q) = %v, want %v", testCase.value, got, testCase.want)
		}
	}
}
