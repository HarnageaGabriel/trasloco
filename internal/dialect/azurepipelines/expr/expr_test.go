package expr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

type vector struct {
	id       string
	mode     string
	phase    string
	input    string
	context  string
	expected string
}

func TestSpecVectors(t *testing.T) {
	vectors := loadVectors(t)
	if len(vectors) < 80 {
		t.Fatalf("loaded %d vectors, want at least 80", len(vectors))
	}
	seen := make(map[string]bool, len(vectors))
	for _, test := range vectors {
		if seen[test.id] {
			t.Fatalf("duplicate vector %s", test.id)
		}
		seen[test.id] = true
		t.Run(test.id, func(t *testing.T) { runVector(t, test) })
	}
}

func runVector(t *testing.T, test vector) {
	t.Helper()
	var values MapContext
	decoder := json.NewDecoder(strings.NewReader(test.context))
	decoder.UseNumber()
	if err := decoder.Decode(&values); err != nil {
		t.Fatalf("decode context: %v", err)
	}

	var got any
	var err error
	switch test.mode {
	case "eval":
		var node Expr
		node, err = Parse(test.input)
		if err == nil {
			got, err = Eval(context.Background(), node, values, EvalOptions{Phase: parsePhase(test.phase)})
		}
	case "parse":
		var node Expr
		node, err = Parse(test.input)
		if err == nil {
			got = Print(node)
		}
	case "macro":
		got, err = ExpandMacros(test.input, values)
	case "directive":
		var directive *Directive
		directive, err = ParseDirective(test.input)
		if err == nil {
			got = directive.Kind.String()
		}
	default:
		t.Fatalf("unknown vector mode %q", test.mode)
	}

	if wantError, ok := strings.CutPrefix(test.expected, "error:"); ok {
		if err == nil || !strings.Contains(err.Error(), wantError) {
			t.Fatalf("error = %v, want substring %q", err, wantError)
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gotJSON, err := json.Marshal(jsonValue(got))
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	var wantCompact bytes.Buffer
	if err := json.Compact(&wantCompact, []byte(test.expected)); err != nil {
		t.Fatalf("invalid expected JSON %q: %v", test.expected, err)
	}
	if diff := cmp.Diff(wantCompact.String(), string(gotJSON)); diff != "" {
		t.Fatalf("result mismatch (-want +got):\n%s", diff)
	}
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "docs", "spec", "ado-expressions.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var vectors []vector
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "| EXPR-") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) != 8 {
			t.Fatalf("malformed vector row: %s", line)
		}
		vectors = append(vectors, vector{
			id:       cleanCell(cells[1]),
			mode:     cleanCell(cells[2]),
			phase:    cleanCell(cells[3]),
			input:    cleanCell(cells[4]),
			context:  cleanCell(cells[5]),
			expected: cleanCell(cells[6]),
		})
	}
	return vectors
}

func cleanCell(cell string) string {
	cell = strings.TrimSpace(cell)
	if len(cell) >= 2 && cell[0] == '`' && cell[len(cell)-1] == '`' {
		return cell[1 : len(cell)-1]
	}
	return cell
}

func parsePhase(value string) Phase {
	if value == "runtime" {
		return PhaseRuntime
	}
	if value == "macro" {
		return PhaseMacro
	}
	return PhaseCompile
}

func jsonValue(value any) any {
	switch v := value.(type) {
	case Version:
		return v.String()
	case projected:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = jsonValue(item)
		}
		return out
	default:
		return value
	}
}

func TestParsePrintRoundTrip(t *testing.T) {
	for _, test := range loadVectors(t) {
		if test.mode != "eval" && test.mode != "parse" || strings.HasPrefix(test.expected, "error:") {
			continue
		}
		node, err := Parse(test.input)
		if err != nil {
			t.Fatalf("%s first parse: %v", test.id, err)
		}
		roundTrip, err := Parse(Print(node))
		if err != nil {
			t.Fatalf("%s second parse: %v", test.id, err)
		}
		options := []cmp.Option{
			cmpopts.IgnoreFields(Literal{}, "Span"),
			cmpopts.IgnoreFields(Identifier{}, "Span"),
			cmpopts.IgnoreFields(Call{}, "Span"),
			cmpopts.IgnoreFields(Member{}, "Span"),
			cmpopts.IgnoreFields(Index{}, "Span"),
			cmp.Comparer(func(a, b Version) bool { return cmp.Equal(a.Parts, b.Parts) }),
		}
		if diff := cmp.Diff(node, roundTrip, options...); diff != "" {
			t.Fatalf("%s round trip mismatch (-want +got):\n%s", test.id, diff)
		}
	}
}

func TestByteOffsetsAndWalk(t *testing.T) {
	node, err := Parse("${{ eq(variables.x, 'é') }}")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(Span{Start: 4, End: 25}, node.Range()); diff != "" {
		t.Fatalf("root span mismatch (-want +got):\n%s", diff)
	}
	var types []string
	Walk(VisitorFunc(func(node Expr) { types = append(types, fmt.Sprintf("%T", node)) }), node)
	want := []string{"*expr.Call", "*expr.Member", "*expr.Identifier", "*expr.Literal"}
	if diff := cmp.Diff(want, types); diff != "" {
		t.Fatalf("walk mismatch (-want +got):\n%s", diff)
	}
}

type testCounter struct{ value int64 }

func (c *testCounter) Next(_ context.Context, _ string, seed int64) (int64, error) {
	if c.value == 0 {
		c.value = seed
	} else {
		c.value++
	}
	return c.value, nil
}

type testStatus map[string]bool

func (s testStatus) Status(_ context.Context, name string, _ []string) (bool, error) {
	value, ok := s[name]
	if !ok {
		return false, errors.New("unknown status")
	}
	return value, nil
}

func TestProviders(t *testing.T) {
	counterNode, err := Parse("counter('x', 10)")
	if err != nil {
		t.Fatal(err)
	}
	counter := &testCounter{}
	got, err := Eval(context.Background(), counterNode, MapContext{}, EvalOptions{
		Phase: PhaseRuntime, Counter: counter, VariableDefinition: true,
	})
	if err != nil || got != float64(10) {
		t.Fatalf("counter = %v, %v", got, err)
	}
	statusNode, err := Parse("succeeded('build')")
	if err != nil {
		t.Fatal(err)
	}
	got, err = Eval(context.Background(), statusNode, MapContext{}, EvalOptions{
		Phase: PhaseRuntime, Status: testStatus{"succeeded": true},
	})
	if err != nil || got != true {
		t.Fatalf("status = %v, %v", got, err)
	}
}

func FuzzParse(f *testing.F) {
	seeds := []string{
		"eq(variables.x, 'x')", "foo.*.id", "1.2.3", "'it''s'", "and(true, false)", "", "${{ x }}", "$[x]",
	}
	path := filepath.Join("..", "..", "..", "..", "testdata", "azurepipelines", "expr", "fuzz-seeds.txt")
	if data, err := os.ReadFile(path); err == nil {
		seeds = append(seeds, strings.Split(strings.TrimSpace(string(data)), "\n")...)
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		node, err := Parse(input)
		if err != nil {
			return
		}
		printed := Print(node)
		if _, err := Parse(printed); err != nil {
			t.Fatalf("canonical output %q does not parse: %v", printed, err)
		}
	})
}
