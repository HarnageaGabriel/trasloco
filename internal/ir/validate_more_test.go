package ir

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

func codes(diagnostics []diag.Diagnostic) map[string]bool {
	out := map[string]bool{}
	for _, d := range diagnostics {
		out[d.Code] = true
	}
	return out
}

func TestValidateRejectsMalformedNodes(t *testing.T) {
	rawBad := &Raw{}
	cases := map[string]struct {
		mutate func(*Pipeline)
		code   string
	}{
		"nil stage dependency": {func(p *Pipeline) { p.Stages[0].DependsOn = []string{"ghost"} }, "TRL-VAL-0010"},
		"stage cycle": {func(p *Pipeline) {
			p.Stages[0].DependsOn = []string{"main"}
		}, "TRL-VAL-0009"},
		"stage if bad":  {func(p *Pipeline) { p.Stages[0].If = &Cond{Phase: "x"} }, "TRL-VAL-0012"},
		"stage var bad": {func(p *Pipeline) { p.Stages[0].Variables = []Variable{{}} }, "TRL-VAL-0007"},
		"input bad": {func(p *Pipeline) {
			p.Inputs = []Input{{Metadata: DefaultMetadata(), Name: "", Type: "weird"}}
		}, "TRL-VAL-0012"},
		"input choice": {func(p *Pipeline) {
			p.Inputs = []Input{{
				Metadata: DefaultMetadata(), Name: "c", Type: InputChoice,
				Default: &Literal{Type: LiteralString, String: "z"},
			}}
		}, "TRL-VAL-0007"},
		"input defaults": {func(p *Pipeline) {
			for _, typ := range []InputType{InputString, InputBool, InputNumber, InputChoice} {
				p.Inputs = append(p.Inputs, Input{
					Metadata: DefaultMetadata(), Name: "n", Type: typ,
					Choices: []string{"a"}, Default: &Literal{Type: LiteralNull},
				})
			}
		}, "TRL-VAL-0012"},
		"trigger bad": {func(p *Pipeline) {
			p.Triggers = []Trigger{
				{Metadata: DefaultMetadata(), Kind: "nope"},
				{Metadata: DefaultMetadata(), Kind: TriggerSchedule, Schedule: &Schedule{Cron: "* *"}},
				{Metadata: DefaultMetadata(), Kind: TriggerOther, Other: rawBad},
			}
		}, "TRL-VAL-0012"},
		"matrix both": {func(p *Pipeline) {
			p.Jobs[0].Matrix = &Matrix{
				Opaque: &Raw{Provider: "p", Kind: "k", Format: "text", Text: "x"},
				Legs:   []MatrixLeg{{ID: "a"}}, MaxParallel: -1,
			}
		}, "TRL-VAL-0012"},
		"matrix empty": {func(p *Pipeline) { p.Jobs[0].Matrix = &Matrix{} }, "TRL-VAL-0007"},
		"matrix dup": {func(p *Pipeline) {
			var values OrderedMap[Literal]
			values.Set("n", Literal{Type: LiteralNumber, Number: "1.x"})
			p.Jobs[0].Matrix = &Matrix{Legs: []MatrixLeg{
				{ID: "a", Values: values}, {ID: "a"}, {},
			}}
		}, "TRL-VAL-0008"},
		"need missing": {func(p *Pipeline) { p.Jobs[0].Needs = []string{"ghost"} }, "TRL-VAL-0010"},
		"job cycle":    {func(p *Pipeline) { p.Jobs[0].Needs = []string{"build"} }, "TRL-VAL-0009"},
		"job fields": {func(p *Pipeline) {
			j := &p.Jobs[0]
			j.Name, j.If, j.Result = "", nil, "x"
			j.Variables = []Variable{{Scope: "bad", Secret: true}}
			j.Concurrency = &Concurrency{Mode: "bad"}
			j.Uses = &ReusableCall{}
			j.Steps[0].Name = ""
			j.Steps[0].Kind = "bad"
			j.Steps[0].Run = nil
		}, "TRL-VAL-0012"},
		"raw bad": {func(p *Pipeline) {
			p.Triggers = []Trigger{{
				Metadata: DefaultMetadata(), Kind: TriggerOther,
				Other: &Raw{Provider: "p", Kind: "k", Format: "json", JSON: json.RawMessage("{")},
			}}
		}, "TRL-VAL-0012"},
		"metadata bad": {func(p *Pipeline) {
			p.Metadata = Metadata{
				Extensions: Extensions{"bad": json.RawMessage("{"), "x-": nil},
				Origin:     &Origin{},
			}
		}, "TRL-VAL-0012"},
		"nil operation": {func(p *Pipeline) {
			s := &p.Jobs[0].Steps[0]
			s.Kind = StepSetOutput
			s.Run = nil
			s.Operation = &ProtocolOperation{Kind: StepSetOutput}
			p.Jobs[0].Steps = append(p.Jobs[0].Steps,
				Step{Metadata: DefaultMetadata(), ID: "a", Name: "a", Kind: StepAnnotate, If: SuccessCondition(PhaseRuntime), Operation: &ProtocolOperation{Kind: StepAnnotate}},
				Step{Metadata: DefaultMetadata(), ID: "b", Name: "b", Kind: StepMask, If: SuccessCondition(PhaseRuntime), Operation: &ProtocolOperation{Kind: StepMask}},
				Step{Metadata: DefaultMetadata(), ID: "c", Name: "c", Kind: StepRaw, If: SuccessCondition(PhaseRuntime)},
				Step{Metadata: DefaultMetadata(), ID: "d", Name: "d", Kind: StepAddPath, If: SuccessCondition(PhaseRuntime), Operation: &ProtocolOperation{Kind: "weird"}},
			)
		}, "TRL-VAL-0007"},
		"no stages": {func(p *Pipeline) { p.Stages = nil }, "TRL-VAL-0011"},
		"orphan job": {func(p *Pipeline) {
			p.Jobs = append(p.Jobs, NewJob("other", "other"))
		}, "TRL-VAL-0011"},
		"nil pipeline": {nil, "TRL-VAL-0007"},
	}
	//trasloco:unordered-ok independent validation subtests have no shared ordering
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var p *Pipeline
			if tc.mutate != nil {
				p = validPipeline()
				tc.mutate(p)
			}
			if !codes(Validate(p))[tc.code] {
				t.Fatalf("missing %s in %v", tc.code, Validate(p))
			}
		})
	}
}

func TestValidateVariableAndExprShapes(t *testing.T) {
	p := validPipeline()
	secret := &SecretRef{Name: ""}
	p.Variables = []Variable{
		{Metadata: DefaultMetadata(), Name: "s", Scope: ScopePipeline, Secret: true, SecretRef: secret},
		{Metadata: DefaultMetadata(), Name: "v", Scope: ScopePipeline, Value: &Expr{Kind: "weird"}},
		{Metadata: DefaultMetadata(), Name: "n", Scope: ScopePipeline, Value: &Expr{Kind: ExprLiteral, Literal: &Literal{Type: "bad"}}},
		{Metadata: DefaultMetadata(), Name: "m", Scope: ScopePipeline, Value: &Expr{Kind: ExprCall, Call: &CallExpr{Function: "nope"}}},
	}
	p.Jobs[0].If = &Cond{Phase: PhaseRuntime, Opaque: &Raw{}, Expr: exprPtr(StringLiteral("x"))}
	p.Jobs[0].Steps[0].Run.Operations = []ProtocolOperation{
		{Kind: StepSetOutput, Name: "o", Value: exprPtr(StringLiteral("1"))},
	}
	p.Jobs[0].Steps = append(p.Jobs[0].Steps, NewRunStep("use", "use", "bash", "x"))
	p.Jobs[0].Steps[1].Env.Set("A", Expr{Kind: ExprRef, Ref: &RefExpr{Context: RefDependencyOutputs, Output: &OutputRef{Stage: "main", Job: "build", Step: "compile", Name: "o"}}})
	p.Jobs[0].Steps[1].Env.Set("B", Expr{Kind: ExprRef, Ref: &RefExpr{Context: RefDependencyOutputs, Output: &OutputRef{Stage: "main", Job: "build", Step: "compile", Name: "missing"}}})
	p.Jobs[0].Steps[1].Env.Set("C", Expr{Kind: ExprRef, Ref: &RefExpr{Context: RefDependencyOutputs, Output: &OutputRef{}}})
	p.Jobs[0].Steps[1].Env.Set("D", Expr{Kind: ExprRef, Ref: &RefExpr{Context: RefDependencyOutputs, Output: &OutputRef{Stage: "zz", Job: "build", Step: "s", Name: "n"}}})
	got := codes(Validate(p))
	if !got["TRL-VAL-0012"] || !got["TRL-VAL-0010"] {
		t.Fatalf("got %v", got)
	}
}

func TestValidateAdditiveIRFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Pipeline)
		code   string
	}{
		{
			name: "synthetic trigger cannot be in YAML",
			mutate: func(p *Pipeline) {
				p.Triggers = []Trigger{{
					Metadata: DefaultMetadata(),
					Kind:     TriggerPush,
					Binding:  TriggerBinding{InYAML: true, Synthetic: true},
				}}
			},
			code: "TRL-VAL-0012",
		},
		{
			name: "synthetic trigger outside YAML",
			mutate: func(p *Pipeline) {
				p.Triggers = []Trigger{{
					Metadata: DefaultMetadata(),
					ID:       "push",
					Kind:     TriggerPush,
					Binding:  TriggerBinding{Synthetic: true},
				}}
			},
		},
		{
			name: "source YAML trigger is not synthetic",
			mutate: func(p *Pipeline) {
				p.Triggers = []Trigger{{
					Metadata: DefaultMetadata(),
					ID:       "push",
					Kind:     TriggerPush,
					Binding:  TriggerBinding{InYAML: true},
				}}
			},
		},
		{
			name: "literal checkout ref",
			mutate: func(p *Pipeline) {
				p.Jobs[0].Steps[0] = checkoutStep(CheckoutRefLiteral, 0, false)
			},
		},
		{
			name: "source version may retain ignored literal ref",
			mutate: func(p *Pipeline) {
				p.Jobs[0].Steps[0] = checkoutStep(CheckoutRefSourceVersion, 0, false)
			},
		},
		{
			name: "unknown checkout ref source",
			mutate: func(p *Pipeline) {
				p.Jobs[0].Steps[0] = checkoutStep("branchTip", 0, false)
			},
			code: "TRL-VAL-0012",
		},
		{
			name: "fetch depth cannot be negative",
			mutate: func(p *Pipeline) {
				p.Jobs[0].Steps[0] = checkoutStep(CheckoutRefLiteral, -1, true)
			},
			code: "TRL-VAL-0012",
		},
		{
			name: "non-zero fetch depth requires fetchDepthSet",
			mutate: func(p *Pipeline) {
				p.Jobs[0].Steps[0] = checkoutStep(CheckoutRefLiteral, 2, false)
			},
			code: "TRL-VAL-0012",
		},
		{
			name: "explicit full-history fetch depth",
			mutate: func(p *Pipeline) {
				p.Jobs[0].Steps[0] = checkoutStep(CheckoutRefSourceVersion, 0, true)
			},
		},
		{
			name: "explicit positive fetch depth",
			mutate: func(p *Pipeline) {
				p.Jobs[0].Steps[0] = checkoutStep(CheckoutRefLiteral, 1, true)
			},
		},
		{
			name: "classified macros",
			mutate: func(p *Pipeline) {
				p.Jobs[0].Steps[0].Env.Set("MACRO", Expr{Kind: ExprMacro, Macro: &MacroExpr{Name: "TOKEN", Declared: true, Secret: true}})
				p.Jobs[0].Steps[0].Run.MacroRefs = []MacroRef{
					{Name: "TOKEN", Declared: true, Secret: true},
					{Name: "UNRESOLVED"},
				}
			},
		},
		{
			name: "macro expression name required",
			mutate: func(p *Pipeline) {
				p.Jobs[0].Steps[0].Env.Set("MACRO", Expr{Kind: ExprMacro, Macro: &MacroExpr{Declared: true}})
			},
			code: "TRL-VAL-0007",
		},
		{
			name: "macro expression secret must be declared",
			mutate: func(p *Pipeline) {
				p.Jobs[0].Steps[0].Env.Set("MACRO", Expr{Kind: ExprMacro, Macro: &MacroExpr{Name: "TOKEN", Secret: true}})
			},
			code: "TRL-VAL-0012",
		},
		{
			name: "embedded macro name required",
			mutate: func(p *Pipeline) {
				p.Jobs[0].Steps[0].Run.MacroRefs = []MacroRef{{Declared: true}}
			},
			code: "TRL-VAL-0007",
		},
		{
			name: "embedded secret macro must be declared",
			mutate: func(p *Pipeline) {
				p.Jobs[0].Steps[0].Run.MacroRefs = []MacroRef{{Name: "TOKEN", Secret: true}}
			},
			code: "TRL-VAL-0012",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validPipeline()
			tc.mutate(p)
			got := Validate(p)
			if tc.code == "" && len(got) != 0 {
				t.Fatalf("unexpected diagnostics: %v", got)
			}
			if tc.code != "" && !codes(got)[tc.code] {
				t.Fatalf("missing %s in %v", tc.code, got)
			}
		})
	}
}

func TestAdditiveIRFieldsOmitZeroValues(t *testing.T) {
	encoded, err := json.Marshal(validPipeline())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"synthetic", "fetchDepthSet", "refSource", "shellOptions", "macroRefs", "macro"} {
		if strings.Contains(string(encoded), `"`+field+`"`) {
			t.Fatalf("zero-value field %q was not omitted: %s", field, encoded)
		}
	}
}

func checkoutStep(refSource CheckoutRefSource, fetchDepth int, fetchDepthSet bool) Step {
	return Step{
		Metadata: DefaultMetadata(),
		ID:       "compile",
		Name:     "compile",
		Kind:     StepCheckout,
		If:       SuccessCondition(PhaseRuntime),
		Checkout: &CheckoutStep{
			Repository:    "self",
			Ref:           StringLiteral("$(Build.SourceVersion)"),
			RefSource:     refSource,
			FetchDepth:    fetchDepth,
			FetchDepthSet: fetchDepthSet,
		},
	}
}

func TestNeedsMayNotTargetLaterBarrierStage(t *testing.T) {
	p := validPipeline()
	p.Stages = append(p.Stages, Stage{Metadata: DefaultMetadata(), ID: "late", Name: "late", Jobs: []string{"deploy"}, Barrier: true})
	deploy := NewJob("deploy", "deploy")
	deploy.Steps = []Step{NewRunStep("s", "s", "bash", "x")}
	p.Jobs = append(p.Jobs, deploy)
	p.Jobs[0].Needs = []string{"deploy"}
	if len(Validate(p)) == 0 {
		t.Fatal("expected barrier ordering diagnostic")
	}
}

func TestBuildersDebugDigestDuration(t *testing.T) {
	p := NewPipeline("p", "p")
	p.Stages = []Stage{{Metadata: DefaultMetadata(), ID: "s", Name: "s"}}
	if !p.AddJob("s", NewJob("j", "j")) || p.AddJob("zz", NewJob("k", "k")) {
		t.Fatal("AddJob stage handling")
	}
	if s, err := DebugString(p); err != nil || s == "" {
		t.Fatal(err)
	}
	if d, err := Digest(p); err != nil || len(d) < 10 {
		t.Fatal(err)
	}
	b, err := json.Marshal(Duration{Duration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var d Duration
	if err := json.Unmarshal(b, &d); err != nil || d.Duration != time.Second {
		t.Fatal(err)
	}
	if json.Unmarshal([]byte(`"bad"`), &d) == nil || json.Unmarshal([]byte(`1`), &d) == nil {
		t.Fatal("expected duration errors")
	}
}

func TestLargeIntegerLiteralPreserved(t *testing.T) {
	in := Literal{Type: LiteralNumber, Number: "9007199254740993123"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Literal
	if err := json.Unmarshal(b, &out); err != nil || out.Number != in.Number {
		t.Fatalf("%s %v", b, err)
	}
}
