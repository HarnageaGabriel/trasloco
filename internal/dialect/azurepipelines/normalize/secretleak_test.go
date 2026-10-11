package normalize

import (
	"context"
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/corpus"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/resolve"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
	"github.com/HarnageaGabriel/trasloco/internal/source/localfs"
)

// resolvedPipeline runs parse, resolve and normalize on text and returns the IR
// plus the collector that received every finding.
func resolvedPipeline(t *testing.T, text string) (*ir.Pipeline, *diag.Collector) {
	t.Helper()
	source, err := localfs.New(map[string]string{"": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	collector := diag.NewCollector()
	parsed := parse.Source("main.yml", []byte(text), collector)
	resolved, err := resolve.Resolve(context.Background(), parsed, source, resolve.Options{Collector: collector})
	if err != nil || resolved == nil {
		t.Fatalf("resolve: %v %v", resolved, err)
	}
	got, err := Pipeline(context.Background(), resolved, Options{Collector: collector})
	if err != nil {
		t.Fatal(err)
	}
	return got, collector
}

func parsedPipeline(t *testing.T, text string) (*ir.Pipeline, *diag.Collector) {
	t.Helper()
	parseCollector := diag.NewCollector()
	parsed := parse.Source("main.yml", []byte(text), parseCollector)
	collector := diag.NewCollector()
	got, err := Pipeline(context.Background(), parsed, Options{Collector: collector})
	if err != nil {
		t.Fatal(err)
	}
	return got, collector
}

func requireAbsent(t *testing.T, got *ir.Pipeline, collector *diag.Collector, leaks ...string) string {
	t.Helper()
	out := canonical(t, got)
	diags, err := json.Marshal([][]diag.Diagnostic{got.Diagnostics, collector.List()})
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range leaks {
		if strings.Contains(out, leak) {
			t.Fatalf("%q leaked in canonical IR:\n%s", leak, out)
		}
		if strings.Contains(string(diags), leak) {
			t.Fatalf("%q leaked in diagnostics:\n%s", leak, diags)
		}
	}
	return out
}

func diagnosticsJSON(t *testing.T, got *ir.Pipeline, collector *diag.Collector) string {
	t.Helper()
	diagnostics, err := json.Marshal([][]diag.Diagnostic{got.Diagnostics, collector.List()})
	if err != nil {
		t.Fatal(err)
	}
	return string(diagnostics)
}

func TestNumericSecretNeverReachesIR(t *testing.T) {
	for _, kind := range []string{"number", "string"} {
		t.Run(kind, func(t *testing.T) {
			got, collector := resolvedPipeline(t, "parameters:\n- name: dbPassword\n  type: "+kind+"\n  default: 123456\n"+
				"steps:\n- script: echo hi\n  condition: eq(variables['x'], ${{ parameters.dbPassword }})\n")
			out := requireAbsent(t, got, collector, "123456")
			if findings := ir.Validate(got); len(findings) != 0 {
				t.Fatalf("IR validation: %v", findings)
			}
			if !strings.Contains(out, "[REDACTED]") {
				t.Fatalf("no placeholder in IR:\n%s", out)
			}
		})
	}
}

func TestNumericLiteralNeverPartiallyEdited(t *testing.T) {
	got, collector := resolvedPipeline(t, "parameters:\n- name: dbPassword\n  type: string\n  default: LongSecretValue99\n"+
		"steps:\n- script: echo hi\n  condition: eq(variables['x'], 12345)\n")
	out := requireAbsent(t, got, collector, "LongSecretValue99")
	if !strings.Contains(out, `"number":"12345"`) {
		t.Fatalf("unrelated number literal changed:\n%s", out)
	}
}

func TestSelfCheckoutKeptWhenSelfIsSecret(t *testing.T) {
	got, _ := resolvedPipeline(t, "parameters:\n- name: dbPassword\n  type: string\n  default: self\nsteps:\n- checkout: self\n")
	for _, step := range got.Jobs[0].Steps {
		if step.Checkout != nil && step.Checkout.Repository != "self" {
			t.Fatalf("repository = %q", step.Checkout.Repository)
		}
	}
	if !strings.Contains(canonical(t, got), `"repository":"self"`) {
		t.Fatal("self checkout missing")
	}
}

func TestCheckoutAliasStillScrubbed(t *testing.T) {
	got, collector := resolvedPipeline(t, "parameters:\n- name: dbPassword\n  type: string\n  default: secretAlias77\n"+
		"resources:\n  repositories:\n  - repository: secretAlias77\n    type: git\n    name: proj/repo\n"+
		"steps:\n- checkout: secretAlias77\n")
	requireAbsent(t, got, collector, "secretAlias77")
}

func TestCollapsedOrderedKeysAreDeduplicated(t *testing.T) {
	input := "parameters:\n- name: passwordOne\n  type: string\n  default: KeyOneSecret9z\n- name: passwordTwo\n  type: string\n  default: KeyTwoSecret9z\n" +
		"steps:\n- script: echo hi\n  env:\n    KeyOneSecret9z: a\n    KeyTwoSecret9z: b\n"
	got, collector := resolvedPipeline(t, input)
	out := requireAbsent(t, got, collector, "KeyOneSecret9z", "KeyTwoSecret9z")
	if !strings.Contains(out, "[REDACTED]-2") {
		t.Fatalf("collapsed keys not de-duplicated:\n%s", out)
	}
	if findings := ir.Validate(got); len(findings) != 0 {
		t.Fatalf("IR validation: %v", findings)
	}
}

func TestCollapsedOrderedKeysInOverflowMode(t *testing.T) {
	secret := strings.Repeat("S", maxTaintSourceBytes+1)
	got, collector := resolvedPipeline(t, "parameters:\n- name: dbPassword\n  type: string\n  default: "+secret+"\n"+
		"steps:\n- script: echo hi\n  env:\n    ONE: a\n    TWO: b\n")
	out := requireAbsent(t, got, collector, secret)
	if !strings.Contains(out, "[REDACTED]-2") {
		t.Fatalf("overflow keys not de-duplicated:\n%s", out)
	}
}

func TestDeclaredNumericSecretsDoNotReachNumericCarriers(t *testing.T) {
	input := `parameters:
- name: smallPassword
  type: number
  default: 2
- name: dbPassword
  type: number
  default: 123456
- name: retryPassword
  type: number
  default: 7
unknownNumber: 123456
jobs:
- job: typed
  timeoutInMinutes: 123456
  cancelTimeoutInMinutes: 123456
  strategy:
    matrix:
      only:
        generated: 123456
    maxParallel: 123456
  steps:
  - checkout: self
    fetchDepth: 123456
  - script: echo ok
    name: run
    retryCountOnTaskFailure: 7
    condition: eq(variables.answer, 123456)
- job: small
  strategy:
    parallel: 2
  steps:
  - script: echo ok
- job: opaque
  strategy:
    parallel: 123456
  steps:
  - script: echo ok
`
	got, collector := parsedPipeline(t, input)
	out := requireAbsent(t, got, collector, "123456")
	typed := jobByID(t, got, "typed")
	if typed.Timeout != nil || typed.Matrix == nil || typed.Matrix.MaxParallel != 0 {
		t.Fatalf("typed numeric carriers remain: timeout=%v maxParallel=%d", typed.Timeout, typed.Matrix.MaxParallel)
	}
	if typed.Matrix.Legs[0].Values.Len() != 1 || typed.Steps[0].Checkout.FetchDepth != 0 || typed.Steps[1].Retry != 0 {
		t.Fatalf("typed values were not safely omitted: matrix=%v fetchDepth=%d retry=%d", typed.Matrix.Legs, typed.Steps[0].Checkout.FetchDepth, typed.Steps[1].Retry)
	}
	for _, id := range []string{"typed", "small", "opaque"} {
		job := jobByID(t, got, id)
		if job.Fidelity != diag.FidelityNeedsManual {
			t.Fatalf("%s fidelity = %v", id, job.Fidelity)
		}
	}
	if small := jobByID(t, got, "small"); small.Matrix == nil || len(small.Matrix.Legs) != 0 {
		t.Fatalf("tainted small parallel expanded to %d legs", len(small.Matrix.Legs))
	}
	if opaque := jobByID(t, got, "opaque"); opaque.Matrix == nil || len(opaque.Matrix.Legs) != 0 {
		t.Fatalf("tainted large parallel expanded to %d legs", len(opaque.Matrix.Legs))
	}
	if strings.Count(out, "[REDACTED]") < 5 {
		t.Fatalf("too few atomic replacements: %d", strings.Count(out, "[REDACTED]"))
	}
}

func TestCollapsedOrderedKeysReserveLiteralSuffixes(t *testing.T) {
	input := `parameters:
- name: passwordOne
  type: string
  default: KeyOneSecret9z
- name: passwordTwo
  type: string
  default: KeyTwoSecret9z
- name: passwordThree
  type: string
  default: KeyThreeSecret9z
steps:
- script: echo safe
  env:
    KeyOneSecret9z: a
    '[REDACTED]-2': b
    KeyTwoSecret9z: c
    '[REDACTED]': d
    KeyThreeSecret9z: e
    '[REDACTED]-3': f
`
	got, collector := resolvedPipeline(t, input)
	out := requireAbsent(t, got, collector, "KeyOneSecret9z", "KeyTwoSecret9z", "KeyThreeSecret9z")
	steps := jobByID(t, got, implicitID).Steps
	step := steps[len(steps)-1]
	keys := make([]string, 0, step.Env.Len())
	for key := range step.Env.All() {
		keys = append(keys, key)
	}
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if seen[key] {
			t.Fatalf("duplicate key %q in %v", key, keys)
		}
		seen[key] = true
	}
	if !seen["[REDACTED]-4"] || !seen["[REDACTED]-5"] || !seen["[REDACTED]-6"] {
		t.Fatalf("reserved suffix allocation = %v", keys)
	}
	gotAgain, _ := resolvedPipeline(t, input)
	if out != canonical(t, gotAgain) {
		t.Fatal("key repair is not deterministic")
	}
}

func TestRawJSONRedactedKeysRemainCanonical(t *testing.T) {
	input := `parameters:
- name: passwordOne
  type: string
  default: KeyOneSecret9z
- name: passwordTwo
  type: string
  default: KeyTwoSecret9z
KeyOneSecret9z: a
KeyTwoSecret9z: b
'[REDACTED]-2': c
steps:
- script: echo safe
`
	got, collector := parsedPipeline(t, input)
	out := requireAbsent(t, got, collector, "KeyOneSecret9z", "KeyTwoSecret9z")
	if strings.Count(out, "unknown.[REDACTED]") != 3 || !strings.Contains(out, "unknown.[REDACTED]-2") || !strings.Contains(out, "unknown.[REDACTED]-3") {
		t.Fatalf("raw JSON key allocation was not collision-free:\n%s", out)
	}
}

func TestOverflowModePreservesStructuralRawJSONKeys(t *testing.T) {
	secret := strings.Repeat("S", maxTaintSourceBytes+1)
	got, collector := resolvedPipeline(t, "parameters:\n- name: dbPassword\n  type: string\n  default: "+secret+"\nsteps:\n- script: echo safe\n")
	out := requireAbsent(t, got, collector, secret)
	for _, key := range []string{"construct", "property", "detail"} {
		if !strings.Contains(out, `"`+key+`"`) {
			t.Fatalf("structural loss key %q was redacted:\n%s", key, out)
		}
	}
}

func TestCorpusDeclaredScalarSecretsAndOverflowStayCanonical(t *testing.T) {
	const sampleLimit = 1
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
	processed, normalRuns, overflowRuns, sampledRuns := 0, 0, 0, 0
	// The corpus files do not reference the injected parameter; the use-site
	// coverage for these shapes is TestDeclaredSecretShapesAtUseSites.
	shapeSecrets := []string{"-12.5", "1e3", "Qz9", " 42.5 ", "007421", "3.14000", "0042"}
	for _, entry := range index.Files {
		if !entry.Expect.OK {
			continue
		}
		data, err := fs.ReadFile(corpusFS, entry.Path)
		if err != nil {
			t.Fatal(err)
		}
		resolved, errCodes := parseAndResolve(t, entry.Path, data, source)
		if len(errCodes) != 0 {
			if _, pinned := resolveFailures[entry.Path]; !pinned {
				t.Fatalf("%s: unpinned resolve failure %v", entry.Path, errCodes)
			}
			continue
		}
		processed++
		values := sampledScalarLeaves(resolved, sampleLimit)
		var normalSecret string
		if processed <= len(shapeSecrets) {
			normalSecret = shapeSecrets[processed-1]
		} else if len(values) != 0 {
			normalSecret = values[0]
			sampledRuns++
		} else {
			normalSecret = "CorpusSecretValue99"
		}
		modes := []struct{ mode, secret string }{
			{mode: "normal", secret: normalSecret},
			{mode: "overflow", secret: strings.Repeat("S", maxTaintSourceBytes+1)},
		}
		for _, run := range modes {
			mode, secret := run.mode, run.secret
			injected := injectSecretParameter(data, secret)
			resolvedWithSecret, errCodes := parseAndResolve(t, entry.Path, injected, source)
			if len(errCodes) != 0 {
				t.Fatalf("%s %s secret resolve: %v", entry.Path, mode, errCodes)
			}
			collector := diag.NewCollector()
			got, err := Pipeline(context.Background(), resolvedWithSecret, Options{
				ID:        strings.TrimSuffix(filepath.ToSlash(entry.Path), filepath.Ext(entry.Path)),
				Collector: collector,
			})
			if err != nil {
				t.Fatalf("%s secret %q: %v", entry.Path, shortSecret(secret), err)
			}
			canonicalBytes, err := ir.Canonical(got)
			if err != nil {
				t.Fatalf("%s secret %q canonical: %v", entry.Path, shortSecret(secret), err)
			}
			diagnostics := diagnosticsJSON(t, got, collector)
			forms := []string{
				secret,
				strings.ToLower(secret),
				strings.ToUpper(secret),
				jsonEscaped(secret, true),
				jsonEscaped(secret, false),
			}
			leaks := 0
			for _, form := range forms {
				inIR := form != "" && strings.Contains(string(canonicalBytes), form)
				inDiagnostics := form != "" && strings.Contains(diagnostics, form)
				if inIR || inDiagnostics {
					leaks++
				}
			}
			if leaks != 0 {
				t.Fatalf("%s %s secret %q leaked in %d forms", entry.Path, mode, shortSecret(secret), leaks)
			}
			if mode == "normal" {
				normalRuns++
			} else {
				overflowRuns++
			}
		}
	}
	if processed != normalizedCount || normalRuns != normalizedCount || overflowRuns != normalizedCount || sampledRuns == 0 {
		t.Fatalf("processed=%d normal=%d overflow=%d sampled=%d want=%d", processed, normalRuns, overflowRuns, sampledRuns, normalizedCount)
	}
}

func sampledScalarLeaves(root *ast.Pipeline, limit int) []string {
	values := map[string]bool{}
	scalarType := reflect.TypeFor[ast.Scalar]()
	var walk func(reflect.Value)
	walk = func(v reflect.Value) {
		if !v.IsValid() {
			return
		}
		if v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
			if !v.IsNil() {
				walk(v.Elem())
			}
			return
		}
		if v.Type() == scalarType {
			value := v.FieldByName("Value").String()
			trimmed := strings.TrimSpace(value)
			longEnough := len(value) >= 12 || decimalText.MatchString(trimmed) && len(trimmed) >= 4
			if value != redact.Replacement() && longEnough && len(value) <= 128 {
				values[value] = true
			}
			return
		}
		switch v.Kind() {
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(v.Field(i))
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i))
			}
		case reflect.Map:
			iter := v.MapRange()
			for iter.Next() {
				walk(iter.Key())
				walk(iter.Value())
			}
		}
	}
	walk(reflect.ValueOf(root))
	out := slices.Sorted(maps.Keys(values))
	slices.SortFunc(out, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func injectSecretParameter(data []byte, value string) []byte {
	encoded, _ := json.Marshal(value)
	text := string(data)
	marker := "parameters:"
	start := strings.Index(text, marker)
	for start >= 0 && start > 0 && text[start-1] != '\n' {
		next := strings.Index(text[start+len(marker):], marker)
		if next < 0 {
			start = -1
			break
		}
		start += len(marker) + next
	}
	indent := ""
	insert := 0
	if start >= 0 {
		lineEnd := strings.IndexByte(text[start:], '\n')
		if lineEnd >= 0 {
			insert = start + lineEnd + 1
			nextLine := text[insert:]
			if nextEnd := strings.IndexByte(nextLine, '\n'); nextEnd >= 0 {
				nextLine = nextLine[:nextEnd]
			}
			indent = nextLine[:len(nextLine)-len(strings.TrimLeft(nextLine, " \t"))]
		}
	}
	declaration := indent + "- name: __corpusPassword\n" + indent + "  type: string\n" + indent + "  default: " + string(encoded) + "\n"
	if insert == 0 {
		return append([]byte(marker+"\n"+declaration), data...)
	}
	return []byte(text[:insert] + declaration + text[insert:])
}

func shortSecret(secret string) string {
	if len(secret) <= 32 {
		return secret
	}
	return secret[:32] + "..."
}
