package model

import (
	"bytes"
	"reflect"
	"testing"

	goyaml "go.yaml.in/yaml/v3"
)

// mappingKeys returns the key values of a mapping node in order.
func mappingKeys(node *goyaml.Node) []string {
	keys := make([]string, 0, len(node.Content)/2)
	for index := 0; index+1 < len(node.Content); index += 2 {
		keys = append(keys, node.Content[index].Value)
	}
	return keys
}

// TestMapPreservesOrder checks that dynamic mappings never reorder entries.
func TestMapPreservesOrder(t *testing.T) {
	node := Map(
		Pair{Key: "b", Value: String("2")},
		Pair{Key: "a", Value: String("1")},
		Pair{Key: "c", Value: Int(3)},
	)
	if node.Kind != goyaml.MappingNode || node.Tag != "!!map" {
		t.Fatalf("kind/tag = %v/%s, want mapping/!!map", node.Kind, node.Tag)
	}
	if got, want := mappingKeys(node), []string{"b", "a", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
}

// TestSeqPreservesOrder checks that sequence items keep their order.
func TestSeqPreservesOrder(t *testing.T) {
	node := Seq(String("first"), String("second"))
	if node.Kind != goyaml.SequenceNode || node.Tag != "!!seq" {
		t.Fatalf("kind/tag = %v/%s, want sequence/!!seq", node.Kind, node.Tag)
	}
	if len(node.Content) != 2 || node.Content[0].Value != "first" || node.Content[1].Value != "second" {
		t.Errorf("items = %+v", node.Content)
	}
}

// TestScalarConstructors checks the YAML tags of the scalar helpers.
func TestScalarConstructors(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		node  *goyaml.Node
		tag   string
		value string
	}{
		{"string", String("text"), "!!str", "text"},
		{"int", Int(42), "!!int", "42"},
		{"bool true", Bool(true), "!!bool", "true"},
		{"bool false", Bool(false), "!!bool", "false"},
		{"null", Null(), "!!null", ""},
	} {
		if testCase.node.Kind != goyaml.ScalarNode {
			t.Errorf("%s: kind = %v, want scalar", testCase.name, testCase.node.Kind)
		}
		if testCase.node.Tag != testCase.tag {
			t.Errorf("%s: tag = %s, want %s", testCase.name, testCase.node.Tag, testCase.tag)
		}
		if testCase.node.Value != testCase.value {
			t.Errorf("%s: value = %q, want %q", testCase.name, testCase.node.Value, testCase.value)
		}
	}
}

// TestEmptyMap checks the flow-style empty mapping of INV-02.
func TestEmptyMap(t *testing.T) {
	node := EmptyMap()
	if node.Kind != goyaml.MappingNode || len(node.Content) != 0 {
		t.Fatalf("empty map = %+v", node)
	}
	var output bytes.Buffer
	encoder := goyaml.NewEncoder(&output)
	if err := encoder.Encode(node); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := output.String(); got != "{}\n" {
		t.Errorf("empty map = %q, want %q", got, "{}\n")
	}
}

// encodeKeys returns the top-level mapping keys of one encoded value.
func encodeKeys(t *testing.T, value interface{}) []string {
	t.Helper()
	var node goyaml.Node
	if err := node.Encode(value); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return mappingKeys(&node)
}

// TestWorkflowFieldOrder checks the document key order of emit-gha.md 4.1.
func TestWorkflowFieldOrder(t *testing.T) {
	workflow := Workflow{
		Name:        "ci",
		On:          Map(),
		Permissions: EmptyMap(),
		Env:         Map(),
		Jobs:        Map(),
	}
	want := []string{"name", "on", "permissions", "env", "jobs"}
	if got := encodeKeys(t, workflow); !reflect.DeepEqual(got, want) {
		t.Errorf("workflow keys = %v, want %v", got, want)
	}
}

// TestJobFieldOrder checks the job key order of emit-gha.md 4.1.
func TestJobFieldOrder(t *testing.T) {
	job := Job{
		Name:            "job",
		Permissions:     EmptyMap(),
		Needs:           []string{"a"},
		If:              "success()",
		RunsOn:          "ubuntu-24.04",
		TimeoutMinutes:  5,
		ContinueOnError: true,
		Env:             Map(),
		Steps:           Seq(),
	}
	want := []string{
		"name", "permissions", "needs", "if", "runs-on",
		"timeout-minutes", "continue-on-error", "env", "steps",
	}
	if got := encodeKeys(t, job); !reflect.DeepEqual(got, want) {
		t.Errorf("job keys = %v, want %v", got, want)
	}
}

// TestStepFieldOrder checks the step key order of emit-gha.md 4.1.
func TestStepFieldOrder(t *testing.T) {
	step := Step{
		Name:             "step",
		If:               "success()",
		Uses:             "actions/checkout@0000000000000000000000000000000000000000",
		With:             Map(),
		Shell:            "bash",
		WorkingDirectory: "sub",
		Env:              Map(),
		TimeoutMinutes:   2,
		ContinueOnError:  true,
		Run:              "echo hi",
	}
	want := []string{
		"name", "if", "uses", "with", "shell", "working-directory",
		"env", "timeout-minutes", "continue-on-error", "run",
	}
	if got := encodeKeys(t, step); !reflect.DeepEqual(got, want) {
		t.Errorf("step keys = %v, want %v", got, want)
	}
}
