package emit

import (
	"strconv"
	"strings"
	"testing"

	goyaml "go.yaml.in/yaml/v3"

	"github.com/HarnageaGabriel/trasloco/internal/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

// resolvePointer resolves one RFC 6901 JSON Pointer against a yaml node.
func resolvePointer(root *goyaml.Node, pointer string) (*goyaml.Node, bool) {
	node := root
	if node.Kind == goyaml.DocumentNode {
		node = node.Content[0]
	}
	if pointer == "" {
		return node, true
	}
	for _, token := range strings.Split(pointer[1:], "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch node.Kind {
		case goyaml.MappingNode:
			var next *goyaml.Node
			for index := 0; index+1 < len(node.Content); index += 2 {
				if node.Content[index].Value == token {
					next = node.Content[index+1]
					break
				}
			}
			if next == nil {
				return nil, false
			}
			node = next
		case goyaml.SequenceNode:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(node.Content) {
				return nil, false
			}
			node = node.Content[index]
		default:
			return nil, false
		}
	}
	return node, true
}

// TestEmitOutputElementPathsResolve parses the emitted bytes and resolves
// every output element path to an existing node, over a multi-job graph with
// "~" and "/" in job keys, names and needs.
func TestEmitOutputElementPathsResolve(t *testing.T) {
	pipeline := &ir.Pipeline{
		ID:   "ci",
		Name: "ci ~ / pipeline",
		Jobs: []ir.Job{
			{ID: "a~b", Name: "a~b"},
			{ID: "c/d", Name: "name ~ with / both", Needs: []string{"a~b"}},
			{ID: "e~1f/~0", Name: "e~1f/~0", Needs: []string{"a~b", "c/d"},
				Steps: []ir.Step{{ID: "s", Name: "s", Kind: ir.StepRun}}},
		},
	}
	result := Emit(pipeline, catalog.ActionsLock{}, fixedNaming{}, Options{}, nil)
	if len(result.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(result.Files))
	}
	var root goyaml.Node
	if err := goyaml.Unmarshal(result.Files[0].Data, &root); err != nil {
		t.Fatalf("parse emitted file: %v", err)
	}
	if len(result.OutputElements) == 0 {
		t.Fatal("no output elements")
	}
	for _, element := range result.OutputElements {
		file, pointer, found := strings.Cut(element.Path, "#")
		if !found || file != result.Files[0].Path {
			t.Errorf("path %q is not <file>#<pointer>", element.Path)
			continue
		}
		if _, ok := resolvePointer(&root, pointer); !ok {
			t.Errorf("pointer %q (%s) does not resolve in the emitted file", pointer, element.NodeID)
		}
	}
}

// TestPlaceholderStepHeadCommentSurvives checks the source-line comment of
// the placeholder step is present in the emitted bytes.
func TestPlaceholderStepHeadCommentSurvives(t *testing.T) {
	pipeline := &ir.Pipeline{
		ID:   "ci",
		Name: "ci",
		Jobs: []ir.Job{{
			ID: "j", Name: "j",
			Metadata: ir.Metadata{Origin: &ir.Origin{Source: diag.Source{Line: 7}}},
		}},
	}
	result := Emit(pipeline, catalog.ActionsLock{}, fixedNaming{}, Options{}, nil)
	if len(result.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(result.Files))
	}
	want := "# trasloco: " + manualConversionCode + " (source line 7)"
	if !strings.Contains(string(result.Files[0].Data), want) {
		t.Errorf("emitted file lacks %q:\n%s", want, result.Files[0].Data)
	}
}
