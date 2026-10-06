package ir

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/quick"

	"github.com/google/go-cmp/cmp"
)

func TestIRVectors(t *testing.T) {
	vectors := loadIRVectors(t)
	if len(vectors) != 15 {
		t.Fatalf("loaded %d IR vectors, want 15", len(vectors))
	}
	for _, vector := range vectors {
		vector := vector
		t.Run(vector.id, func(t *testing.T) {
			switch vector.id {
			case "IR-014":
				testReorderedRoundTrip(t)
			case "IR-015":
				got, err := CanonicalJSON([]byte(vector.input))
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != vector.expected {
					t.Fatalf("CanonicalJSON() = %s, want %s", got, vector.expected)
				}
			default:
				pipeline := vectorPipeline(t, vector.id)
				diagnostics := Validate(pipeline)
				if vector.diagnostic == "none" {
					if len(diagnostics) != 0 {
						t.Fatalf("Validate() diagnostics = %#v", diagnostics)
					}
					return
				}
				for _, diagnostic := range diagnostics {
					if diagnostic.Code == vector.diagnostic {
						return
					}
				}
				t.Fatalf("Validate() missing %s; got %#v", vector.diagnostic, diagnostics)
			}
		})
	}
}

func TestJSONRoundTripProperty(t *testing.T) {
	property := func(name, script string, failFast bool, number float64) bool {
		pipeline := validPipeline()
		pipeline.Name = name
		pipeline.Jobs[0].Steps[0].Run.Script = script
		pipeline.Jobs[0].Matrix = &Matrix{
			Legs: []MatrixLeg{{
				ID: "leg",
				Values: map[string]Literal{
					"value": {Type: LiteralNumber, Number: strconv.FormatFloat(number, 'g', -1, 64)},
				},
			}},
			FailFast: failFast,
		}
		encoded, err := json.Marshal(pipeline)
		if err != nil {
			return false
		}
		var decoded Pipeline
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			return false
		}
		return Equal(pipeline, &decoded)
	}
	config := &quick.Config{MaxCount: 200}
	if err := quick.Check(property, config); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalGoldens(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "numbers", input: `[333333333.33333329,1E30,4.50,2e-3,0.000000000000000000000000001,-0]`},
		{name: "utf16-keys", input: `{"€":"Euro Sign","\r":"Carriage Return","דּ":"Hebrew Letter Dalet With Dagesh","1":"One","😀":"Emoji: Grinning Face","\u0080":"Control","ö":"Latin Small Letter O With Diaeresis"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("..", "..", "testdata", "ir", test.name+".golden"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := CanonicalJSON([]byte(test.input))
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(strings.TrimSpace(string(want)), string(got)); diff != "" {
				t.Fatalf("canonical mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestJSONVersionAndUnknownField(t *testing.T) {
	for _, input := range []string{
		`{"id":"p"}`,
		`{"schemaVersion":2,"id":"p"}`,
		`{"schemaVersion":1,"id":"p","name":"p","fidelity":"exact","provenance":"deterministic","jobs":[],"stages":[],"unknown":true}`,
	} {
		var pipeline Pipeline
		if err := json.Unmarshal([]byte(input), &pipeline); err == nil {
			t.Fatalf("json.Unmarshal(%s) succeeded", input)
		}
	}
}

func TestCanonicalRejectsDuplicateObjectNames(t *testing.T) {
	if _, err := CanonicalJSON([]byte(`{"a":1,"a":2}`)); err == nil {
		t.Fatal("duplicate object names accepted")
	}
}

type irVector struct {
	id         string
	input      string
	expected   string
	diagnostic string
}

func loadIRVectors(t *testing.T) []irVector {
	t.Helper()
	file, err := os.Open(filepath.Join("..", "..", "docs", "spec", "ir.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close IR spec: %v", err)
		}
	}()
	var vectors []irVector
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "| IR-") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 4 {
			t.Fatalf("invalid IR vector row: %s", line)
		}
		for index := range cells {
			cells[index] = markdownCell(cells[index])
		}
		vectors = append(vectors, irVector{id: cells[0], input: cells[1], expected: cells[2], diagnostic: cells[3]})
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func markdownCell(value string) string {
	value = strings.TrimSpace(value)
	first := strings.IndexByte(value, '`')
	last := strings.LastIndexByte(value, '`')
	if first >= 0 && last > first {
		return value[first+1 : last]
	}
	return value
}

func vectorPipeline(t *testing.T, id string) *Pipeline {
	t.Helper()
	pipeline := validPipeline()
	switch id {
	case "IR-001":
	case "IR-002":
		duplicate := NewJob("build", "duplicate")
		pipeline.Jobs = append(pipeline.Jobs, duplicate)
		pipeline.Stages[0].Jobs = append(pipeline.Stages[0].Jobs, duplicate.ID)
	case "IR-003":
		second := NewJob("test", "test")
		pipeline.Jobs[0].Needs = []string{"test"}
		second.Needs = []string{"build"}
		pipeline.Jobs = append(pipeline.Jobs, second)
		pipeline.Stages[0].Jobs = append(pipeline.Stages[0].Jobs, second.ID)
	case "IR-004":
		pipeline.Jobs[0].Needs = []string{"missing"}
	case "IR-005":
		pipeline.Jobs[0].Outputs = []OutputDef{{
			Name: "value",
			From: OutputRef{Stage: "main", Job: "build", Step: "missing", Name: "value"},
		}}
	case "IR-006":
		pipeline.Stages = append(pipeline.Stages, Stage{
			Metadata: DefaultMetadata(), ID: "two", Name: "two", Jobs: []string{"build"},
		})
	case "IR-007":
		pipeline.Stages[0].Jobs = nil
	case "IR-008":
		pipeline.SchemaVersion = 2
	case "IR-009":
		step := &pipeline.Jobs[0].Steps[0]
		step.Run = nil
		step.Action = &ActionStep{Provider: "github-actions", Name: "owner/action", Version: "v1"}
	case "IR-010":
		pipeline.Jobs[0].If.Opaque = &Raw{Provider: "github-actions", Kind: "condition", Format: "text", Text: "always()"}
	case "IR-011":
		pipeline.Extensions = Extensions{"github": json.RawMessage(`{"key":true}`)}
	case "IR-012":
		pipeline.Jobs[0].Steps[0].Run.Shell = ""
	case "IR-013":
		producer := &pipeline.Jobs[0]
		producer.Steps = []Step{{
			Metadata: DefaultMetadata(), ID: "publish", Name: "publish", Kind: StepSetOutput,
			If:        SuccessCondition(PhaseRuntime),
			Operation: &ProtocolOperation{Kind: StepSetOutput, Name: "value", Value: exprPtr(StringLiteral("ok"))},
		}}
		consumer := NewJob("consume", "consume")
		consumer.Needs = []string{"build"}
		consumer.If = &Cond{Phase: PhaseRuntime, Expr: &Expr{
			Kind: ExprRef,
			Ref: &RefExpr{Context: RefDependencyOutputs, Output: &OutputRef{
				Stage: "main", Job: "build", Step: "publish", Name: "value",
			}},
		}}
		pipeline.Jobs = append(pipeline.Jobs, consumer)
		pipeline.Stages[0].Jobs = append(pipeline.Stages[0].Jobs, consumer.ID)
	default:
		t.Fatalf("no vector implementation for %s", id)
	}
	return pipeline
}

func validPipeline() *Pipeline {
	pipeline := NewPipeline("pipeline", "pipeline")
	pipeline.Stages = []Stage{{Metadata: DefaultMetadata(), ID: "main", Name: "main", Jobs: []string{"build"}}}
	job := NewJob("build", "build")
	job.Steps = []Step{NewRunStep("compile", "compile", "bash", "go build ./...")}
	pipeline.Jobs = []Job{job}
	return pipeline
}

func testReorderedRoundTrip(t *testing.T) {
	t.Helper()
	pipeline := validPipeline()
	encoded, err := json.Marshal(pipeline)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	reordered := []byte(`{"stages":` + string(object["stages"]) + `,"jobs":` + string(object["jobs"]) + `,"name":"pipeline","id":"pipeline","provenance":"deterministic","fidelity":"exact","schemaVersion":1}`)
	var decoded Pipeline
	if err := json.Unmarshal(reordered, &decoded); err != nil {
		t.Fatal(err)
	}
	if !Equal(pipeline, &decoded) {
		t.Fatalf("round trip mismatch (-want +got):\n%s", cmp.Diff(pipeline, &decoded))
	}
}

func exprPtr(expression Expr) *Expr { return &expression }
