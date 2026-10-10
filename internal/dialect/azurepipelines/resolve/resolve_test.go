package resolve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/printer"
)

type memorySource map[string]string

func (m memorySource) Open(_ context.Context, alias, name string) ([]byte, diag.Source, error) {
	value, ok := m[alias+"@"+name]
	if !ok {
		return nil, diag.Source{}, errors.New("not found")
	}
	return []byte(value), diag.Source{Platform: "azure-pipelines", File: name}, nil
}

func TestResolveDirectives(t *testing.T) {
	input := `parameters:
- name: use
  type: boolean
  default: true
- name: names
  type: object
  default: [one, two]
steps:
- ${{ if parameters.use }}:
  - ${{ each name in parameters.names }}:
    - script: echo ${{ name }}
- ${{ else }}:
  - script: never
`
	collector := diag.NewCollector()
	root := parse.Source("root.yml", []byte(input), collector)
	got, err := Resolve(context.Background(), root, nil, Options{Collector: collector})
	if err != nil {
		t.Fatal(err)
	}
	printed, err := printer.Print(got)
	if err != nil {
		t.Fatal(err)
	}
	text := string(printed)
	if !strings.Contains(text, "echo one") || !strings.Contains(text, "echo two") || strings.Contains(text, "never") || strings.Contains(text, "${{") {
		t.Fatalf("unexpected expansion:\n%s", text)
	}
	assertNoErrors(t, collector)
}

func TestResolveTemplateAndProvenance(t *testing.T) {
	collector := diag.NewCollector()
	root := parse.Source("root.yml", []byte("steps:\n- template: parts/step.yml\n  parameters:\n    word: hi\n"), collector)
	source := memorySource{"@parts/step.yml": "parameters:\n- name: word\n  type: string\nsteps:\n- script: echo ${{ parameters.word }}\n"}
	got, err := Resolve(context.Background(), root, source, Options{Collector: collector})
	if err != nil {
		t.Fatal(err)
	}
	step := got.Steps.Items[0].Value
	if step.Position().File != "parts/step.yml" {
		t.Fatalf("source = %#v", step.Position())
	}
	if chain := step.(*ast.ScriptStep).Chain; len(chain) != 1 || chain[0].File != "root.yml" {
		t.Fatalf("chain = %#v", chain)
	}
	printed, _ := printer.Print(got)
	if !strings.Contains(string(printed), "echo hi") {
		t.Fatalf("%s", printed)
	}
	assertNoErrors(t, collector)
}

func TestResolveFailuresAndValidation(t *testing.T) {
	tests := []struct {
		name, input, code string
		source            memorySource
	}{
		{"cycle", "steps:\n- template: a.yml\n", "TRL-TPL-0002", memorySource{"@a.yml": "steps:\n- template: a.yml\n"}},
		{"missing", "steps:\n- template: absent.yml\n", "TRL-TPL-0001", memorySource{}},
		{"duplicate", "jobs:\n- job: A\n- job: A\n", "TRL-VAL-0008", nil},
		{"unknown", "jobs:\n- job: A\n  dependsOn: B\n", "TRL-VAL-0010", nil},
		{"dependency-cycle", "jobs:\n- job: A\n  dependsOn: B\n- job: B\n  dependsOn: A\n", "TRL-VAL-0009", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			collector := diag.NewCollector()
			root := parse.Source("root.yml", []byte(test.input), collector)
			_, err := Resolve(context.Background(), root, test.source, Options{Collector: collector})
			if err != nil {
				t.Fatal(err)
			}
			if !hasCode(collector, test.code) {
				t.Fatalf("diagnostics = %v, want %s", codes(collector), test.code)
			}
		})
	}
}

func TestResolveContextAndLimits(t *testing.T) {
	collector := diag.NewCollector()
	root := parse.Source("root.yml", []byte("steps:\n- template: a.yml\n"), collector)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Resolve(ctx, root, memorySource{}, Options{Collector: collector}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	collector = diag.NewCollector()
	root = parse.Source("root.yml", []byte("steps:\n- template: a.yml\n"), collector)
	_, _ = Resolve(context.Background(), root, memorySource{"@a.yml": "steps:\n- script: x\n"}, Options{Collector: collector, MaxFiles: -1})
	if !hasCode(collector, "TRL-TPL-0003") {
		t.Fatalf("diagnostics = %v", codes(collector))
	}
}

func assertNoErrors(t *testing.T, collector *diag.Collector) {
	t.Helper()
	for _, finding := range collector.List() {
		if finding.Severity == diag.SeverityError {
			t.Fatalf("%s: %s", finding.Code, finding.Message)
		}
	}
}

func hasCode(collector *diag.Collector, code string) bool {
	for _, finding := range collector.List() {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func codes(collector *diag.Collector) []string {
	findings := collector.List()
	out := make([]string, 0, len(findings))
	for _, finding := range findings {
		out = append(out, finding.Code)
	}
	return out
}

func TestResolverErrorHelpers(t *testing.T) {
	if got := sourceOf(nil); got != (diag.Source{}) {
		t.Fatalf("nil scalar source = %+v", got)
	}
	want := diag.Source{File: "task.yml", Line: 7}
	if got := sourceOf(&ast.Scalar{Pos: ast.Pos{Source: want}}); got != want {
		t.Fatalf("scalar source = %+v, want %+v", got, want)
	}
	r := new(resolver)
	if !errors.Is(r.fatal(errBudget), ErrExpansionLimit) {
		t.Fatalf("budget error = %v", r.fatal(errBudget))
	}
	sentinel := errors.New("sentinel")
	if !errors.Is(r.fatal(sentinel), sentinel) {
		t.Fatalf("ordinary error = %v", r.fatal(sentinel))
	}
}

func TestDirectiveExpressionVectors(t *testing.T) {
	vectors := loadDirectiveVectors(t)
	if len(vectors) != 45 {
		t.Fatalf("executable directive vectors = %d, want 45", len(vectors))
	}
	for _, vector := range vectors {
		vector := vector
		t.Run(vector.id, func(t *testing.T) {
			contextValue, err := expr.DecodeJSON([]byte(vector.context))
			if err != nil {
				t.Fatal(err)
			}
			env := contextValue.(*expr.Object)
			input := vector.input
			if vector.id == "EXPRV-350" {
				input = strconv.Quote(input)
			}
			collector := diag.NewCollector()
			pipeline := parse.Source("vector.yml", []byte("parameters:\n- name: payload\n  type: object\n  default: "+input+"\n"), collector)
			var got expr.Value
			if pipeline != nil && pipeline.Parameters != nil && pipeline.Parameters.List != nil {
				resolver := &resolver{opts: Options{Collector: collector}, origins: map[expr.Value]ast.Pos{}, maxBytes: defaultMaxBytes}
				got, _ = resolver.resolveAny(context.Background(), pipeline.Parameters.List.Items[0].Value.Default, env)
			}
			if strings.HasPrefix(vector.expected, "error:") {
				if len(collector.List()) == 0 {
					t.Fatalf("wanted error, got %v", got)
				}
				want, ok := expectedErrorCode(vector.expected)
				if !ok {
					t.Fatalf("%s: error kind %q has no code mapping", vector.id, vector.expected)
				}
				if got := errorCodes(collector); !slices.Contains(got, want) {
					t.Fatalf("error diagnostics = %v, want %s for %q", got, want, vector.expected)
				}
				return
			}
			if vector.id == "EXPRV-397" {
				text, _ := scalarText(got)
				expanded, err := expr.ExpandMacros(text, env)
				if err != nil {
					t.Fatal(err)
				}
				got = expr.String(expanded)
			}
			want, err := expr.DecodeJSON([]byte(vector.expected))
			if err != nil {
				t.Fatal(err)
			}
			gotJSON, _ := expr.EncodeJSON(got, false)
			wantJSON, _ := expr.EncodeJSON(want, false)
			if gotJSON != wantJSON {
				t.Fatalf("got %s, want %s", gotJSON, wantJSON)
			}
		})
	}
	t.Run("EXPRV-445", func(t *testing.T) {
		// Template nesting counts template files, not the root pipeline: with a
		// limit of 2 the chain a -> b passes and a -> b -> c fails.
		source := memorySource{"@a.yml": "steps:\n- template: b.yml\n", "@b.yml": "steps:\n- template: c.yml\n", "@c.yml": "steps:\n- script: x\n"}
		failure(t, "steps:\n- template: a.yml\n", source, Options{MaxDepth: 2}, "TRL-TPL-0003")
	})
	t.Run("EXPRV-446", func(t *testing.T) {
		collector := diag.NewCollector()
		root := parse.Source("root.yml", []byte("steps:\n- template: a.yml\n- template: b.yml\n"), collector)
		source := memorySource{"@a.yml": "steps:\n- script: a\n", "@b.yml": "steps:\n- script: b\n"}
		_, err := Resolve(context.Background(), root, source, Options{Collector: collector, MaxFiles: 1})
		if err != nil {
			t.Fatal(err)
		}
		if !hasCode(collector, "TRL-TPL-0003") {
			t.Fatalf("diagnostics = %v", codes(collector))
		}
	})
}

func TestDirectiveExpressionVectorsThroughResolve(t *testing.T) {
	vectors := loadDirectiveVectors(t)
	if len(vectors) != 45 {
		t.Fatalf("executable directive vectors = %d, want 45", len(vectors))
	}
	const echoTemplate = `parameters:
- name: payload
  type: object
steps:
- script: ${{ convertToJson(parameters.payload) }}
`
	for _, vector := range vectors {
		vector := vector
		t.Run(vector.id, func(t *testing.T) {
			input, env := directiveVectorRoot(t, vector)
			collector := diag.NewCollector()
			root := parse.Source("root.yml", []byte(input), collector)
			if root == nil {
				t.Fatalf("root did not parse: %v", codes(collector))
			}
			resolved, err := Resolve(context.Background(), root, memorySource{"@vector.yml": echoTemplate}, Options{Collector: collector})
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(vector.expected, "error:") {
				want, ok := expectedErrorCode(vector.expected)
				if !ok {
					t.Fatalf("%s: error kind %q has no code mapping", vector.id, vector.expected)
				}
				if resolved != nil {
					t.Fatal("error vector returned an AST")
				}
				if got := errorCodes(collector); !slices.Contains(got, want) {
					t.Fatalf("error diagnostics = %v, want %s for %q", got, want, vector.expected)
				}
				return
			}
			assertNoErrors(t, collector)
			if resolved == nil || resolved.Steps == nil || len(resolved.Steps.Items) != 1 {
				t.Fatalf("resolved steps unavailable: AST=%v diagnostics=%v", resolved != nil, codes(collector))
			}
			step, ok := resolved.Steps.Items[0].Value.(*ast.ScriptStep)
			if !ok || step.Script == nil {
				t.Fatalf("echo step = %T", resolved.Steps.Items[0].Value)
			}
			got, err := expr.DecodeJSON([]byte(step.Script.Value))
			if err != nil {
				t.Fatalf("decode echoed JSON %q: %v", step.Script.Value, err)
			}
			if vector.id == "EXPRV-397" {
				text, _ := scalarText(got)
				expanded, expandErr := expr.ExpandMacros(text, env)
				if expandErr != nil {
					t.Fatal(expandErr)
				}
				got = expr.String(expanded)
			}
			want, err := expr.DecodeJSON([]byte(vector.expected))
			if err != nil {
				t.Fatal(err)
			}
			gotJSON, _ := expr.EncodeJSON(got, false)
			wantJSON, _ := expr.EncodeJSON(want, false)
			if gotJSON != wantJSON {
				t.Fatalf("got %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

// directiveVectorRoot promotes the vector context into root parameters and
// variables, then passes the vector document through a real template call.
func directiveVectorRoot(t *testing.T, vector directiveVector) (string, *expr.Object) {
	t.Helper()
	contextValue, err := expr.DecodeJSON([]byte(vector.context))
	if err != nil {
		t.Fatal(err)
	}
	env := contextValue.(*expr.Object)
	var parameters []expr.Property
	var variables *expr.Object
	for _, property := range env.Properties {
		switch property.Name {
		case "parameters":
			parameters = append(parameters, property.Value.(*expr.Object).Properties...)
		case "variables":
			variables = property.Value.(*expr.Object)
		case "job":
			// EXPRV-371 is specified inside an each-job body. Recreate that
			// caller scope using a private root parameter.
			parameters = append(parameters, expr.Property{Name: "__jobs", Value: &expr.Array{Items: []expr.Value{property.Value}}})
		default:
			t.Fatalf("unsupported vector context %q", property.Name)
		}
	}
	input := vector.input
	if vector.id == "EXPRV-350" {
		input = strconv.Quote(input)
	}
	inputValue, err := expr.DecodeJSON([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	input = encodeVectorDocument(t, inputValue)
	if vector.id == "EXPRV-371" {
		input = `[{"${{ each job in parameters.__jobs }}":` + input + `}]`
	}
	var root strings.Builder
	if len(parameters) > 0 {
		root.WriteString("parameters:\n")
		for _, parameter := range parameters {
			encoded := encodeVectorDocument(t, parameter.Value)
			fmt.Fprintf(&root, "- name: %s\n  default: %s\n", strconv.Quote(parameter.Name), encoded)
		}
	}
	if variables != nil {
		encoded, encodeErr := expr.EncodeJSON(variables, false)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		fmt.Fprintf(&root, "variables: %s\n", encoded)
	}
	root.WriteString("steps:\n- template: vector.yml\n  parameters:\n    payload: ")
	root.WriteString(input)
	root.WriteByte('\n')
	return root.String(), env
}

// encodeVectorDocument keeps JSON string values string-typed after they pass
// through an untyped/object YAML parameter. Plain numeric and boolean-looking
// strings otherwise cannot be distinguished from YAML scalars by ast.Scalar.
func encodeVectorDocument(t *testing.T, value expr.Value) string {
	t.Helper()
	switch value := value.(type) {
	case expr.String:
		text := string(value)
		if !strings.Contains(text, "${{") && !strings.Contains(text, "$(") && !strings.Contains(text, "$[") {
			text = "${{ '" + strings.ReplaceAll(text, "'", "''") + "' }}"
		}
		encoded, err := expr.EncodeJSON(expr.String(text), false)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	case *expr.Array:
		parts := make([]string, len(value.Items))
		for i, item := range value.Items {
			parts[i] = encodeVectorDocument(t, item)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case *expr.Object:
		parts := make([]string, len(value.Properties))
		for i, property := range value.Properties {
			key, err := expr.EncodeJSON(expr.String(property.Name), false)
			if err != nil {
				t.Fatal(err)
			}
			parts[i] = key + ":" + encodeVectorDocument(t, property.Value)
		}
		return "{" + strings.Join(parts, ",") + "}"
	default:
		encoded, err := expr.EncodeJSON(value, false)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
}

type directiveVector struct{ id, input, context, expected string }

func loadDirectiveVectors(t *testing.T) []directiveVector {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "docs", "spec", "ado-expressions.md"))
	if err != nil {
		t.Fatal(err)
	}
	var out []directiveVector
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "| EXPRV-") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 10 {
			continue
		}
		id := strings.TrimSpace(cells[1])
		number, _ := strconv.Atoi(strings.TrimPrefix(id, "EXPRV-"))
		if number != 350 && (number < 355 || number > 397) && number != 415 {
			continue
		}
		clean := func(cell string) string {
			cell = strings.TrimSpace(cell)
			if strings.HasPrefix(cell, "`") {
				if end := strings.Index(cell[1:], "`"); end >= 0 {
					return cell[1 : end+1]
				}
			}
			return cell
		}
		out = append(out, directiveVector{id, clean(cells[3]), clean(cells[4]), clean(cells[5])})
	}
	return out
}

func FuzzResolve(f *testing.F) {
	for _, seed := range [][2]string{
		{"steps:\n- script: hi\n", "steps:\n- script: body\n"},
		{"steps:\n- template: body.yml\n", "parameters:\n- name: xs\n  type: object\n  default: [[a, b]]\nsteps:\n- ${{ each row in parameters.xs }}:\n  - ${{ each x in row }}:\n    - script: echo ${{ x }}\n"},
		{"steps:\n- template: body.yml\n", "parameters:\n- name: p\n  type: object\n  default: {displayName: inserted}\nsteps:\n- script: hi\n  ${{ insert }}: ${{ parameters.p }}\n"},
		{"jobs:\n- job: J\n  strategy:\n    matrix:\n      one: {cfg: a}\n  steps: []\n", "steps: []\n"},
		{"variables:\n  ${{ if true }}:\n    A: one\nsteps:\n- script: $(A)\n", "steps: []\n"},
		{"steps:\n- script: hi\n  continueOnError: $[ variables.x ]\n", "steps: []\n"},
		{"steps:\n- template: d0.yml\n", "steps:\n- script: deep\n"},
	} {
		f.Add(seed[0], seed[1])
	}
	addCorpusSeeds(f, 24)
	f.Fuzz(func(t *testing.T, input, templateBody string) {
		source := memorySource{"@body.yml": templateBody}
		for i := 0; i < 7; i++ {
			source[fmt.Sprintf("@d%d.yml", i)] = fmt.Sprintf("steps:\n- template: d%d.yml\n", i+1)
		}
		source["@d7.yml"] = "steps:\n- template: body.yml\n"
		type result struct {
			printed string
			codes   []string
			err     error
		}
		run := func() result {
			// Work is bounded by the budgets (1 MiB, 16 files), not by the
			// clock; the deadline is only a generous safety net so slow CI
			// machines do not make two runs diverge.
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			collector := diag.NewCollector()
			root := parse.Source("fuzz.yml", []byte(input), collector)
			if root == nil {
				return result{codes: codes(collector)}
			}
			resolved, err := Resolve(ctx, root, source, Options{Collector: collector, MaxDepth: 16, MaxFiles: 16, MaxExpandedBytes: 1 << 20})
			if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ErrExpansionLimit) {
				t.Fatalf("unexpected error: %v", err)
			}
			if err != nil && resolved != nil {
				t.Fatal("error returned a partial AST")
			}
			for _, finding := range collector.List() {
				if strings.HasPrefix(finding.Code, "TRL-INT-") {
					t.Fatalf("internal diagnostic escaped fuzz input: %s", finding.Code)
				}
			}
			if resolved == nil && err == nil && len(errorCodes(collector)) == 0 {
				t.Fatal("nil AST without an error-severity diagnostic")
			}
			if len(errorCodes(collector)) > 0 && resolved != nil {
				t.Fatalf("error diagnostics %v returned an AST", errorCodes(collector))
			}
			out := result{codes: codes(collector), err: err}
			if resolved == nil {
				return out
			}
			if leftovers := findLeftovers(resolved); len(leftovers) > 0 {
				t.Fatalf("post-condition failed: %+v", leftovers)
			}
			printed, printErr := printer.Print(resolved)
			if printErr != nil {
				t.Fatalf("printer rejected resolved AST: %v", printErr)
			}
			out.printed = string(printed)
			roundCollector := diag.NewCollector()
			round := parse.Source("roundtrip.yml", printed, roundCollector)
			if round == nil || len(errorCodes(roundCollector)) > 0 {
				t.Fatalf("printed AST did not parse: %v\n%s", codes(roundCollector), printed)
			}
			reprinted, printErr := printer.Print(round)
			if printErr != nil || string(reprinted) != out.printed {
				t.Fatalf("printer round trip changed output: %v\nfirst:\n%s\nsecond:\n%s", printErr, printed, reprinted)
			}
			return out
		}
		first, second := run(), run()
		if !slices.Equal(first.codes, second.codes) || first.printed != second.printed || !errors.Is(first.err, second.err) || !errors.Is(second.err, first.err) {
			t.Fatalf("non-deterministic resolution:\nfirst:  codes=%v err=%v output=%q\nsecond: codes=%v err=%v output=%q", first.codes, first.err, first.printed, second.codes, second.err, second.printed)
		}
	})
}

func errorCodes(c *diag.Collector) []string {
	var out []string
	for _, f := range c.List() {
		if f.Severity == diag.SeverityError {
			out = append(out, f.Code)
		}
	}
	return out
}

// errorKinds maps the error kind named in an "error:" vector to the diagnostic
// code it must produce. A kind missing here fails the test instead of being
// silently accepted.
var errorKinds = []struct{ kind, code string }{
	// A conditional chain that is malformed: elseif/else with no open if.
	{"elseif without preceding if", "TRL-EXPR-0001"},
	{"else not immediately after if/elseif", "TRL-EXPR-0001"},
	// A directive in a position where it cannot appear, or text that is not a
	// closed expression, is an expression syntax error.
	{"directive not allowed inline", "TRL-EXPR-0001"},
	{"expression not closed", "TRL-EXPR-0001"},
	// A compound value interpolated into a string is an expression type error.
	{"Array not convertible to String in embedded expression", "TRL-EXPR-0005"},
}

func expectedErrorCode(expected string) (string, bool) {
	kind := strings.TrimSpace(strings.TrimPrefix(expected, "error:"))
	for _, entry := range errorKinds {
		if entry.kind == kind {
			return entry.code, true
		}
	}
	return "", false
}
