package expr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

type vector struct{ id, phase, input, context, expected, basis, source string }

func TestSpecVectors(t *testing.T) {
	vectors := loadVectors(t)
	if len(vectors) != 446 {
		t.Fatalf("loaded %d vectors, want 446", len(vectors))
	}
	seen := map[string]bool{}
	for _, v := range vectors {
		if seen[v.id] {
			t.Fatalf("duplicate %s", v.id)
		}
		seen[v.id] = true
		t.Run(v.id, func(t *testing.T) { runVector(t, v) })
	}
}

func tableCells(line string) []string {
	var cells []string
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' && i+1 < len(line) && line[i+1] == '|' {
			b.WriteByte('|')
			i++
			continue
		}
		if line[i] == '|' {
			cells = append(cells, cleanCell(b.String()))
			b.Reset()
		} else {
			b.WriteByte(line[i])
		}
	}
	cells = append(cells, cleanCell(b.String()))
	return cells
}

func cleanCell(cell string) string {
	cell = strings.TrimSpace(cell)
	if strings.HasPrefix(cell, "`") {
		if end := strings.LastIndex(cell, "`"); end > 0 {
			return cell[1:end]
		}
	}
	return cell
}

func loadVectors(t testing.TB) []vector {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "docs", "spec", "ado-expressions.md"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors []vector
	for _, line := range strings.Split(strings.Split(string(data), "### Conversion-matrix coverage index")[0], "\n") {
		if !strings.HasPrefix(line, "| EXPRV-") {
			continue
		}
		cells := tableCells(line)
		if len(cells) != 9 {
			t.Fatalf("malformed vector: %s", line)
		}
		v := vector{cells[1], cells[2], cells[3], cells[4], cells[5], cells[6], cells[7]}
		if v.source == "" || v.basis != "docs" && v.basis != "reference-impl" && v.basis != "inferred" {
			t.Fatalf("unsourced vector %s", v.id)
		}
		vectors = append(vectors, v)
	}
	return vectors
}

func vectorNumber(v vector) int { n, _ := strconv.Atoi(strings.TrimPrefix(v.id, "EXPRV-")); return n }

func resolverVector(v vector) bool {
	n := vectorNumber(v)
	return n >= 355 && n <= 397 || n == 415 || n == 445 || n == 446 || n == 350
}

func materialize(v vector) vector {
	switch vectorNumber(v) {
	case 306:
		v.input = "counter('a', 1)"
	case 417:
		v.input = "$[variables.a]"
	case 418:
		var b strings.Builder
		b.WriteString(`{"variables":{`)
		for i := 1; i <= 60; i++ {
			if i > 1 {
				b.WriteByte(',')
			}
			value := "end"
			if i < 60 {
				value = fmt.Sprintf("$(v%d)", i+1)
			}
			fmt.Fprintf(&b, `"v%d":%q`, i, value)
		}
		b.WriteString("}}")
		v.input = "$(v1)"
		v.context = b.String()
	case 439:
		v.input = ""
	case 440:
		v.input = strings.Repeat("not(", 49) + "true" + strings.Repeat(")", 49)
	case 441:
		v.input = strings.Repeat("not(", 50) + "true" + strings.Repeat(")", 50)
	case 442:
		v.input = "'" + strings.Repeat("a", 20998) + "'"
		v.expected = `"` + strings.Repeat("a", 20998) + `"`
	case 443:
		v.input = "'" + strings.Repeat("a", 20999) + "'"
	case 444:
		v.context = `{"parameters":{"big":["` + strings.Repeat("a", 300000) + `","` + strings.Repeat("b", 300000) + `"]}}`
	}
	return v
}

func runVector(t *testing.T, v vector) {
	t.Helper()
	if resolverVector(v) {
		t.Skip("template resolver integration contract; retained verbatim outside expression engine")
	}
	v = materialize(v)
	value, err := DecodeJSON([]byte(v.context))
	if err != nil {
		t.Fatal(err)
	}
	values := value.(*Object)
	if pipeline, ok := values.Lookup("pipeline"); ok {
		if o, ok := pipeline.(*Object); ok {
			for i, p := range o.Properties {
				if p.Name == "startTime" {
					text, _ := stringify(p.Value)
					date, err := time.Parse(time.RFC3339Nano, text)
					if err != nil {
						t.Fatal(err)
					}
					o.Properties[i].Value = DateTime{date}
				}
			}
		}
	}
	options := EvalOptions{}
	if v.phase == "runtime" {
		options.Phase = PhaseRuntime
		options.VariableDefinition = strings.HasPrefix(v.input, "$[")
	}
	scope, _ := values.Lookup("scope")
	options.Scope, _ = stringify(scope)
	canceled, _ := values.Lookup("canceled")
	variables, _ := values.Lookup("variables")
	status, _ := lookupProperty(variables, "Agent.JobStatus")
	jobStatus, _ := stringify(status)
	dependencies, _ := values.Lookup("dependencies")
	deps, _ := dependencies.(*Object)
	options.Status = StatusSnapshot{Scope: options.Scope, Canceled: truthy(canceled), JobStatus: jobStatus, Dependencies: deps}
	counters, _ := values.Lookup("counters")
	counterValues, _ := counters.(*Object)
	options.Counter = &testCounter{values: counterValues}
	var got Value
	if v.phase == "macro" {
		var text string
		text, err = ExpandMacros(v.input, values)
		got = String(text)
	} else {
		var node Expr
		node, err = Parse(v.input)
		if err == nil {
			got, err = Eval(context.Background(), node, values, options)
		}
		if err == nil && options.VariableDefinition {
			text, ok := stringify(got)
			if !ok {
				err = errors.New("not renderable")
			} else {
				got = String(text)
			}
		}
	}
	if strings.HasPrefix(v.expected, "error:") {
		if err == nil {
			t.Fatalf("wanted %s, got %v", v.expected, got)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(v.expected, "version(") {
		version, ok := got.(Version)
		if !ok || "version("+version.String()+")" != v.expected {
			t.Fatalf("version = %v", got)
		}
		return
	}
	want, err := DecodeJSON([]byte(v.expected))
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := EncodeJSON(want, false)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := got.(*projection); ok {
		got = &p.Array
	}
	gotJSON, err := EncodeJSON(got, false)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(wantJSON, gotJSON); diff != "" {
		t.Fatalf("result (-want +got):\n%s", diff)
	}
	// Pure functions also run at runtime, moving typed parameters into variables.
	if v.phase == "compile" && isPureVector(v.input) {
		runtimeInput := strings.ReplaceAll(v.input, "parameters", "variables")
		for i, p := range values.Properties {
			if p.Name == "parameters" {
				values.Properties[i].Name = "variables"
			}
		}
		node, err := Parse(runtimeInput)
		if err != nil {
			t.Fatal(err)
		}
		runtimeValue, err := Eval(context.Background(), node, values, EvalOptions{Phase: PhaseRuntime})
		if err != nil {
			t.Fatal(err)
		}
		if p, ok := runtimeValue.(*projection); ok {
			runtimeValue = &p.Array
		}
		runtimeJSON, err := EncodeJSON(runtimeValue, false)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(gotJSON, runtimeJSON); diff != "" {
			t.Fatalf("runtime differs: %s", diff)
		}
	}
}

func isPureVector(input string) bool {
	return !strings.Contains(strings.ToLower(input), "variables") && !strings.Contains(input, "dependencies")
}

type testCounter struct{ values *Object }

func (c *testCounter) Next(_ context.Context, prefix string, seed int64) (int64, error) {
	if value, ok := c.values.Lookup(prefix); ok {
		number, ok := toNumber(value)
		if !ok {
			return 0, errors.New("bad counter")
		}
		return newIntegerQuotient(number.rat()).Int64() + 1, nil
	}
	return seed, nil
}

func TestParsePrintRoundTrip(t *testing.T) {
	for _, v := range loadVectors(t) {
		if resolverVector(v) || v.phase == "macro" {
			continue
		}
		v = materialize(v)
		node, err := Parse(v.input)
		if err != nil {
			continue
		}
		printed := Print(node)
		again, err := Parse(printed)
		if err != nil {
			t.Fatalf("%s: %v", v.id, err)
		}
		if diff := cmp.Diff(printed, Print(again)); diff != "" {
			t.Fatal(diff)
		}
	}
}

func TestByteOffsetsAndWalk(t *testing.T) {
	node, err := Parse("${{ eq(variables.x, '?') }}")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(Span{Start: 4, End: 24}, node.Range()); diff != "" {
		t.Fatal(diff)
	}
	var types []string
	Walk(VisitorFunc(func(node Expr) { types = append(types, fmt.Sprintf("%T", node)) }), node)
	if diff := cmp.Diff([]string{"*expr.Call", "*expr.Member", "*expr.Identifier", "*expr.Literal"}, types); diff != "" {
		t.Fatal(diff)
	}
}

func TestEscapedTablePipes(t *testing.T) {
	if diff := cmp.Diff([]string{"", "EXPRV-x", "join('|', parameters.a)", ""}, tableCells("| EXPRV-x | `join('\\|', parameters.a)` |")); diff != "" {
		t.Fatal(diff)
	}
}

func FuzzParse(f *testing.F) {
	for _, v := range loadVectors(f) {
		if !resolverVector(v) {
			f.Add(materialize(v).input)
		}
	}
	f.Fuzz(func(t *testing.T, input string) {
		node, err := Parse(input)
		if err == nil {
			printed := Print(node)
			again, err := Parse(printed)
			if err != nil {
				t.Fatalf("round trip: %v", err)
			}
			if Print(again) != printed {
				t.Fatal("unstable print")
			}
			_, _ = Eval(context.Background(), node, &Object{}, EvalOptions{})
		}
		if _, err := ScanMacros(input); err != nil {
			t.Fatal(err)
		}
		if _, err := ExpandMacros(input, &Object{}); err != nil {
			t.Fatal(err)
		}
		_, _ = ParseDirective(input)
	})
}
