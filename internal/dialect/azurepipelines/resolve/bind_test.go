package resolve

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
)

// failure resolves input and requires the documented failure contract: a nil
// AST and exactly the wanted error diagnostics, in report order.
func failure(t *testing.T, input string, source Source, options Options, wantCodes ...string) []diag.Diagnostic {
	t.Helper()
	collector := diag.NewCollector()
	options.Collector = collector
	root := parse.Source("root.yml", []byte(input), collector)
	resolved, err := Resolve(context.Background(), root, source, options)
	if err != nil && !errors.Is(err, ErrExpansionLimit) {
		t.Fatal(err)
	}
	if resolved != nil {
		t.Fatal("failed resolution returned an AST")
	}
	var got []string
	var findings []diag.Diagnostic
	for _, finding := range collector.List() {
		if finding.Severity == diag.SeverityError {
			got = append(got, finding.Code)
			findings = append(findings, finding)
		}
	}
	if !slices.Equal(got, wantCodes) {
		t.Fatalf("error codes = %v, want %v", got, wantCodes)
	}
	return findings
}

func at(t *testing.T, finding diag.Diagnostic, file string, line int) {
	t.Helper()
	if finding.Source == nil || finding.Source.File != file || finding.Source.Line != line {
		t.Fatalf("%s at %+v, want %s:%d", finding.Code, finding.Source, file, line)
	}
}

func TestParameterErrorsAreDedicatedAndPositioned(t *testing.T) {
	template := "parameters:\n- name: req\n  type: string\n- name: n\n  type: number\n  default: 1\nsteps:\n- script: echo ${{ parameters.req }} ${{ parameters.n }}\n"
	source := memorySource{"@t.yml": template}
	t.Run("unknown", func(t *testing.T) {
		findings := failure(t, "steps:\n- template: t.yml\n  parameters:\n    req: a\n    nope: 1\n", source, Options{}, "TRL-TPL-0008")
		at(t, findings[0], "root.yml", 5)
	})
	t.Run("missing", func(t *testing.T) {
		findings := failure(t, "steps:\n- template: t.yml\n", source, Options{}, "TRL-TPL-0008")
		at(t, findings[0], "t.yml", 2)
	})
	t.Run("mistyped-and-template-not-expanded", func(t *testing.T) {
		findings := failure(t, "steps:\n- template: t.yml\n  parameters:\n    req: a\n    n: many\n", source, Options{}, "TRL-TPL-0005")
		at(t, findings[0], "root.yml", 5)
	})
	t.Run("not-in-values", func(t *testing.T) {
		values := memorySource{"@t.yml": "parameters:\n- name: e\n  type: string\n  values: [a, b]\nsteps:\n- script: x\n"}
		findings := failure(t, "steps:\n- template: t.yml\n  parameters:\n    e: c\n", values, Options{}, "TRL-TPL-0005")
		at(t, findings[0], "root.yml", 4)
	})
	t.Run("error-in-template-carries-call-sites", func(t *testing.T) {
		nested := memorySource{"@t.yml": "steps:\n- ${{ if eq(1) }}:\n  - script: x\n"}
		findings := failure(t, "steps:\n- template: t.yml\n", nested, Options{}, "TRL-EXPR-0005")
		at(t, findings[0], "t.yml", 2)
		sites, _ := findings[0].Params["callSites"].([]any)
		if len(sites) != 1 || sites[0] != "root.yml:2" {
			t.Fatalf("callSites = %#v", findings[0].Params)
		}
	})
}

func TestCycleDiagnosticCarriesChain(t *testing.T) {
	source := memorySource{"@a.yml": "steps:\n- template: b.yml\n", "@b.yml": "steps:\n- template: a.yml\n"}
	findings := failure(t, "steps:\n- template: a.yml\n", source, Options{}, "TRL-TPL-0002")
	at(t, findings[0], "b.yml", 2)
	chain, _ := findings[0].Params["chain"].([]any)
	if len(chain) != 4 || chain[0] != "@root.yml" || chain[1] != "@a.yml" || chain[3] != "@a.yml" {
		t.Fatalf("chain = %#v", findings[0].Params)
	}
}

func TestParametersBindByDeclaredType(t *testing.T) {
	source := memorySource{"@t.yml": "parameters:\n- name: v\n  type: string\n  default: '3.10'\n- name: w\n  type: string\n  default: '007'\n- name: b\n  type: string\n  default: 'true'\n- name: n\n  type: number\n  default: '5'\n- name: f\n  type: boolean\n  default: 'false'\n- name: o\n  type: object\n  default:\n    k: 1\nsteps:\n- script: echo ${{ parameters.v }}/${{ parameters.w }}/${{ parameters.b }}/${{ parameters.n }}/${{ parameters.f }}/${{ parameters.o.k }}\n"}
	_, collector, printed := resolveText(t, "steps:\n- template: t.yml\n", source, Options{})
	assertNoErrors(t, collector)
	if !strings.Contains(printed, "echo 3.10/007/true/5/False/1") {
		t.Fatalf("printed:\n%s", printed)
	}
	// A supplied number keeps its text for a string parameter.
	_, collector, printed = resolveText(t, "steps:\n- template: t.yml\n  parameters:\n    v: '1.0'\n    w: 5\n", source, Options{})
	assertNoErrors(t, collector)
	if !strings.Contains(printed, "echo 1.0/5/true") {
		t.Fatalf("printed:\n%s", printed)
	}
}

func TestRootParametersWithoutValues(t *testing.T) {
	resolved, collector, printed := resolveText(t, "parameters:\n- name: x\n  type: string\n- name: y\n  type: string\n  default: ok\nsteps:\n- script: ${{ parameters.y }}\n", nil, Options{})
	assertNoErrors(t, collector)
	if resolved == nil || !strings.Contains(printed, "script: ok") {
		t.Fatalf("printed:\n%s", printed)
	}
	for _, finding := range collector.List() {
		if finding.Code == "TRL-TPL-0006" || finding.Code == "TRL-TPL-0008" {
			t.Fatalf("root parameter reported %s", finding.Code)
		}
	}
	// One mistyped root parameter is a single error and does not null the rest.
	failure(t, "parameters:\n- name: n\n  type: number\n  default: many\n- name: y\n  type: string\n  default: ok\nsteps:\n- script: ${{ parameters.y }}\n", nil, Options{}, "TRL-TPL-0005")
}

func TestCallSiteDirectivesAndInterpolation(t *testing.T) {
	template := "parameters:\n- name: a\n  type: string\n  default: ''\n- name: b\n  type: string\n  default: ''\n- name: c\n  type: string\n  default: ''\n- name: d\n  type: string\n  default: ''\n- name: l\n  type: object\n  default: []\nsteps:\n- script: echo a=${{ parameters.a }} b=${{ parameters.b }} c=${{ parameters.c }} d=${{ parameters.d }} l=${{ join(',', parameters.l) }}\n"
	input := "parameters:\n- name: env\n  type: string\n  default: prod\n- name: extra\n  type: object\n  default:\n    d: from-insert\nsteps:\n- template: t.yml\n  parameters:\n    a: app-${{ parameters.env }}\n    ${{ if eq(parameters.env, 'prod') }}:\n      b: yes-prod\n    ${{ else }}:\n      b: not-prod\n    ${{ each p in parameters.extra }}:\n      ${{ p.key }}: ${{ p.value }}\n    c: ${{ parameters.env }}\n    l:\n    - one\n    - ${{ if parameters.env }}:\n      - two\n    - app-${{ parameters.env }}\n"
	_, collector, printed := resolveText(t, input, memorySource{"@t.yml": template}, Options{})
	assertNoErrors(t, collector)
	want := "a=app-prod b=yes-prod c=prod d=from-insert l=one,two,app-prod"
	if !strings.Contains(printed, want) {
		t.Fatalf("want %q in:\n%s", want, printed)
	}
}

func TestExtendsKeepsRootIdentity(t *testing.T) {
	source := memorySource{"@base.yml": "parameters:\n- name: fromBase\n  type: string\n  default: b\nstages:\n- stage: S\n  jobs:\n  - job: J\n    steps:\n    - script: ${{ parameters.fromBase }}\n"}
	resolved, collector, printed := resolveText(t, "parameters:\n- name: rootOnly\n  type: string\n  default: r\nextends:\n  template: base.yml\n  parameters:\n    fromBase: given\n", source, Options{RootPath: "root.yml"})
	assertNoErrors(t, collector)
	if resolved.Position().File != "root.yml" || resolved.Position().Line != 1 {
		t.Fatalf("root position = %+v", resolved.Position())
	}
	if resolved.Parameters == nil || len(resolved.Parameters.List.Items) != 1 || resolved.Parameters.List.Items[0].Value.Name.Value != "rootOnly" {
		t.Fatalf("root parameters replaced:\n%s", printed)
	}
	if !strings.Contains(printed, "script: given") || strings.Contains(printed, "fromBase") {
		t.Fatalf("printed:\n%s", printed)
	}
}

func TestPathEscapesAndRootPath(t *testing.T) {
	failure(t, "steps:\n- template: ../x.yml\n", memorySource{"@x.yml": "steps: []\n"}, Options{}, "TRL-TPL-0001")
	failure(t, "steps:\n- template: /../x.yml\n", memorySource{"@x.yml": "steps: []\n"}, Options{}, "TRL-TPL-0001")
	failure(t, "steps:\n- template: a/../../x.yml\n", memorySource{"@x.yml": "steps: []\n"}, Options{}, "TRL-TPL-0001")
	for _, root := range []string{"/abs/root.yml", `C:\repo\root.yml`, "../root.yml", `\\server\share\root.yml`} {
		failure(t, "steps: []\n", nil, Options{RootPath: root}, "TRL-TPL-0001")
	}
	// A repository-relative root path is the base of relative references.
	source := memorySource{"@dir/t.yml": "steps:\n- template: u.yml\n", "@dir/u.yml": "steps:\n- script: ok\n"}
	_, collector, printed := resolveText(t, "steps:\n- template: t.yml\n", source, Options{RootPath: "dir/root.yml"})
	assertNoErrors(t, collector)
	if !strings.Contains(printed, "script: ok") {
		t.Fatalf("printed:\n%s", printed)
	}
}

func TestInsertedListsKeepProvenance(t *testing.T) {
	source := memorySource{
		"@mid.yml":  "parameters:\n- name: who\n  type: string\n  default: mid\n- name: s\n  type: stepList\n  default: []\nsteps:\n- template: leaf.yml\n  parameters:\n    s: ${{ parameters.s }}\n",
		"@leaf.yml": "parameters:\n- name: who\n  type: string\n  default: leaf\n- name: s\n  type: stepList\n  default: []\nsteps: ${{ parameters.s }}\n",
	}
	input := "parameters:\n- name: who\n  type: string\n  default: root\nsteps:\n- template: mid.yml\n  parameters:\n    s:\n    - script: echo ${{ parameters.who }}\n    - ${{ if true }}:\n      - script: second\n"
	resolved, collector, printed := resolveText(t, input, source, Options{})
	assertNoErrors(t, collector)
	if !strings.Contains(printed, "echo root") || strings.Contains(printed, "echo leaf") || strings.Contains(printed, "echo mid") {
		t.Fatalf("caller scope not used:\n%s", printed)
	}
	if len(resolved.Steps.Items) != 2 {
		t.Fatalf("steps = %d:\n%s", len(resolved.Steps.Items), printed)
	}
	first := resolved.Steps.Items[0].Value
	if first.Position().File != "root.yml" {
		t.Fatalf("first step position = %+v", first.Position())
	}
	if len(first.(*ast.ScriptStep).Chain) != 0 {
		t.Fatalf("authored in the root file, chain = %+v", first.(*ast.ScriptStep).Chain)
	}
}

func TestDepthCountsTemplateFilesOnly(t *testing.T) {
	chain := func(levels int) memorySource {
		source := memorySource{}
		for i := 1; i <= levels; i++ {
			next := fmt.Sprintf("steps:\n- template: t%d.yml\n", i+1)
			if i == levels {
				next = "steps:\n- script: leaf\n"
			}
			source[fmt.Sprintf("@t%d.yml", i)] = next
		}
		return source
	}
	// EXPRV-445: 100 levels of template nesting pass, the 101st fails. The root
	// pipeline is not a template level.
	_, collector, _ := resolveText(t, "steps:\n- template: t1.yml\n", chain(100), Options{MaxFiles: 200})
	assertNoErrors(t, collector)
	failure(t, "steps:\n- template: t1.yml\n", chain(101), Options{MaxFiles: 200}, "TRL-TPL-0003")
	_, collector, _ = resolveText(t, "steps:\n- template: t1.yml\n", chain(2), Options{MaxDepth: 2})
	assertNoErrors(t, collector)
	failure(t, "steps:\n- template: t1.yml\n", chain(3), Options{MaxDepth: 2}, "TRL-TPL-0003")
}

func TestNegativeLimitsAllowNothing(t *testing.T) {
	source := memorySource{"@a.yml": "steps:\n- script: x\n"}
	failure(t, "steps:\n- template: a.yml\n", source, Options{MaxFiles: -1}, "TRL-TPL-0003")
	failure(t, "steps:\n- template: a.yml\n", source, Options{MaxDepth: -5}, "TRL-TPL-0003")
}

func TestWarningsKeepTheAST(t *testing.T) {
	// A key produced twice by expansion is a warning; the last value wins.
	resolved, collector, printed := resolveText(t, "parameters:\n- name: extra\n  type: object\n  default:\n    A: second\nsteps:\n- script: kept\n  env:\n    A: first\n    ${{ insert }}: ${{ parameters.extra }}\n", nil, Options{})
	if resolved == nil || !hasCode(collector, "TRL-TPL-0010") {
		t.Fatalf("duplicate key: resolved=%v codes=%v", resolved != nil, codes(collector))
	}
	if strings.Contains(printed, "first") || !strings.Contains(printed, "A: second") {
		t.Fatalf("last value must win:\n%s", printed)
	}
}

// cancelSource cancels the context while a template is being opened.
type cancelSource struct{ cancel context.CancelFunc }

func (c cancelSource) Open(ctx context.Context, _, _ string) ([]byte, diag.Source, error) {
	c.cancel()
	return nil, diag.Source{}, ctx.Err()
}

func TestContextErrorsPropagate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	collector := diag.NewCollector()
	root := parse.Source("root.yml", []byte("steps:\n- template: a.yml\n"), collector)
	resolved, err := Resolve(ctx, root, cancelSource{cancel}, Options{Collector: collector})
	if !errors.Is(err, context.Canceled) || resolved != nil {
		t.Fatalf("Resolve = %v, %v", resolved != nil, err)
	}
	if len(collector.List()) != 0 {
		t.Fatalf("cancellation became diagnostics: %v", codes(collector))
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	collector = diag.NewCollector()
	root = parse.Source("root.yml", []byte("steps:\n- ${{ if true }}:\n  - script: a\n"), collector)
	if _, err := Resolve(ctx, root, nil, Options{Collector: collector}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestEmptyAndNilInputs(t *testing.T) {
	if _, err := Resolve(context.Background(), nil, nil, Options{}); err == nil {
		t.Fatal("nil root accepted")
	}
	collector := diag.NewCollector()
	root := parse.Source("root.yml", []byte("steps:\n- template: a.yml\n"), collector)
	if _, err := Resolve(context.Background(), root, nil, Options{Collector: collector}); err != nil || !hasCode(collector, "TRL-TPL-0001") {
		t.Fatalf("nil source: %v %v", err, codes(collector))
	}
}
