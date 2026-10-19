package normalize

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

// taintShape builds count secret parameters and count steps, each step leaf
// holding one of the secrets.
func taintShape(count int) string { return taintShapeWith(count, true) }

// taintShapeWith optionally keeps the leaves free of secrets so the run
// measures the scan, not finding construction.
func taintShapeWith(count int, leak bool) string {
	var out strings.Builder
	out.WriteString("parameters:\n")
	for i := 0; i < count; i++ {
		fmt.Fprintf(&out, "- name: db_password_%d\n  type: string\n  default: Sx%07dZq\n", i, i)
	}
	out.WriteString("steps:\n")
	for i := 0; i < count; i++ {
		word := "Sx%07dZq"
		if !leak {
			word = "clean%07d"
		}
		fmt.Fprintf(&out, "- script: echo "+word+"\n  name: s%d\n", i, i)
	}
	return out.String()
}

// TestTaintTimesLeavesWork asserts the T x L shape: doubling both the number of
// secret literals and the number of leaves keeps the work counter linear.
func TestTaintTimesLeavesWork(t *testing.T) {
	_, small := pipelineWorkOf(t, taintShape(500))
	got, large := pipelineWorkOf(t, taintShape(1000))
	if small == 0 || large >= 3*small {
		t.Fatalf("work ratio is not below 3: %d -> %d", small, large)
	}
	if strings.Contains(canonical(t, got), "Sx0000007Zq") {
		t.Fatal("secret leaked")
	}
	t.Logf("work ratio %.2f (%d/%d)", float64(large)/float64(small), large, small)
}

func TestTaintReplacerScansOncePerLeaf(t *testing.T) {
	set := newTaintSet([]string{"longS3cret"}, nil)
	for _, leaf := range []string{"clean", "longS3cret", "a [REDACTED] b"} {
		set.apply(leaf)
	}
	if set.replacerScans != 3 || set.appliedBytes != len("clean")+len("longS3cret")+len("a [REDACTED] b") {
		t.Fatalf("replacer scans/bytes = %d/%d", set.replacerScans, set.appliedBytes)
	}
}

// B2: secret text used as a key name never reaches findings or the IR.
func TestSecretAsKeyNameNotLeaked(t *testing.T) {
	const secret = "keyS3cretName"
	data := []byte(`parameters:
- name: dbPassword
  type: string
  default: ` + secret + `
` + secret + `: rootValue
jobs:
- job: ` + secret + `
  ` + secret + `: jobValue
  steps:
  - script: echo a
    name: ` + secret + `
    ` + secret + `: stepValue
  - task: Foo@1
    name: t
    inputs:
      ` + secret + `: x
`)
	collector := diag.NewCollector()
	parsed := parse.Source("pipeline.yml", data, diag.NewCollector())
	got, err := Pipeline(context.Background(), parsed, Options{Collector: collector})
	if err != nil {
		t.Fatal(err)
	}
	if out := canonical(t, got); strings.Contains(out, secret) {
		t.Fatalf("secret leaked in IR:\n%s", out)
	}
	for _, set := range []struct {
		name     string
		findings []diag.Diagnostic
	}{{"collector", collector.List()}, {"ir", got.Diagnostics}} {
		name := set.name
		encoded, err := json.Marshal(set.findings)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("secret leaked in %s findings:\n%s", name, encoded)
		}
	}
}

// B3: derived forms of a secret literal are replaced too.
func TestSecretDerivedFormsReplaced(t *testing.T) {
	got := normalizeText(t, `parameters:
- name: dbPassword
  type: string
  default: MixedS3cretPw
- name: creds
  type: object
  default:
    password: 'pa"ss\word1'
steps:
- script: echo mixeds3cretpw
  name: lower
- script: echo MIXEDS3CRETPW
  name: upper
- script: echo '{"password":"pa\"ss\\word1"}'
  name: json
`)
	out := canonical(t, got)
	for _, leak := range []string{"mixeds3cretpw", "MIXEDS3CRETPW", `pa\\\"ss\\\\word1`, `pa\"ss\\word1`, `ss\\word1`} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q leaked:\n%s", leak, out)
		}
	}
	for _, id := range []string{"lower", "upper", "json"} {
		if step := stepByID(t, got, implicitID, id); !strings.Contains(step.Run.Script, "[REDACTED]") {
			t.Fatalf("step %s script = %q", id, step.Run.Script)
		}
	}
}

func TestTaintIDDedupPreservesStageReferences(t *testing.T) {
	got := normalizeText(t, `parameters:
- name: dbPassword
  type: string
  default: MixedS3cretPw
stages:
- stage: MixedS3cretPw
  jobs:
  - job: first
    steps:
    - script: echo first
- stage: MIXEDS3CRETPW
  jobs:
  - job: second
    steps:
    - script: echo second
- stage: Consumer
  dependsOn: MIXEDS3CRETPW
  jobs:
  - job: consume
    steps:
    - script: echo consume
`)
	if len(got.Stages) != 3 {
		t.Fatalf("stages = %#v", got.Stages)
	}
	if got.Stages[0].ID != "[REDACTED]" || got.Stages[1].ID != "[REDACTED]-2" || got.Stages[2].ID != "Consumer" {
		t.Fatalf("stage IDs = %q, %q, %q", got.Stages[0].ID, got.Stages[1].ID, got.Stages[2].ID)
	}
	if fmt.Sprint(got.Stages[1].DependsOn) != "[[REDACTED]]" || fmt.Sprint(got.Stages[2].DependsOn) != "[[REDACTED]-2]" {
		t.Fatalf("stage dependencies = %#v", got.Stages)
	}
}

func TestTaintIDDedupPreservesJobReferences(t *testing.T) {
	got := normalizeText(t, `parameters:
- name: dbPassword
  type: string
  default: MixedS3cretPw
jobs:
- job: MixedS3cretPw
  steps:
  - script: echo first
- job: MIXEDS3CRETPW
  steps:
  - script: echo second
- job: Consumer
  dependsOn: MIXEDS3CRETPW
  steps:
  - script: echo consume
`)
	if len(got.Jobs) != 3 {
		t.Fatalf("jobs = %#v", got.Jobs)
	}
	if got.Jobs[0].ID != "[REDACTED]" || got.Jobs[1].ID != "[REDACTED]-2" || got.Jobs[2].ID != "Consumer" {
		t.Fatalf("job IDs = %q, %q, %q", got.Jobs[0].ID, got.Jobs[1].ID, got.Jobs[2].ID)
	}
	if fmt.Sprint(got.Jobs[2].Needs) != "[[REDACTED]-2]" {
		t.Fatalf("consumer needs = %v", got.Jobs[2].Needs)
	}
	if fmt.Sprint(got.Stages[0].Jobs) != "[[REDACTED] [REDACTED]-2 Consumer]" {
		t.Fatalf("stage jobs = %v", got.Stages[0].Jobs)
	}
}

func TestShortSecretDerivedFormsWholeLeafOnly(t *testing.T) {
	const input = `parameters:
- name: dbPassword
  type: string
  default: Ab1
- name: apiToken
  type: string
  default: 'A"'
steps:
- script: echo safe
  env:
    LOWER: ab1
    UPPER: AB1
    EMBEDDED: xab1x
    JSON: 'A\"'
    JSON_EMBEDDED: 'xa\"x'
`
	data := []byte(input)
	collector := diag.NewCollector()
	parsed := parse.Source("pipeline.yml", data, diag.NewCollector())
	got, err := Pipeline(context.Background(), parsed, Options{Collector: collector})
	if err != nil {
		t.Fatal(err)
	}
	out := canonical(t, got)
	if strings.Contains(out, `"string":"ab1"`) || strings.Contains(out, `"string":"AB1"`) {
		t.Fatalf("derived short secret leaked in IR:\n%s", out)
	}
	if !strings.Contains(out, `"string":"xab1x"`) {
		t.Fatalf("embedded short value changed:\n%s", out)
	}
	env := map[string]string{}
	step := got.Jobs[0].Steps[len(got.Jobs[0].Steps)-1]
	for _, entry := range step.Env {
		if entry.Value.Literal != nil {
			env[entry.Key] = entry.Value.Literal.String
		}
	}
	if env["JSON"] != "[REDACTED]" || env["JSON_EMBEDDED"] != `xa\"x` {
		t.Fatalf("JSON variants = %v", env)
	}
	for _, findings := range [][]diag.Diagnostic{got.Diagnostics, collector.List()} {
		encoded, err := json.Marshal(findings)
		if err != nil {
			t.Fatal(err)
		}
		for _, leak := range []string{"Ab1", "ab1", "AB1"} {
			if strings.Contains(string(encoded), leak) {
				t.Fatalf("%q leaked in diagnostics: %s", leak, encoded)
			}
		}
	}
}

func TestTaintSourceByteBoundIsConservative(t *testing.T) {
	secret := strings.Repeat("S", maxTaintSourceBytes+1)
	got := normalizeText(t, "parameters:\n- name: dbPassword\n  type: string\n  default: "+secret+"\nsteps:\n- script: echo safe\n  env:\n    VALUE: "+secret+"\n")
	out := canonical(t, got)
	if strings.Contains(out, secret) {
		t.Fatal("oversize declared secret leaked")
	}
	if len(got.Diagnostics) == 0 {
		t.Fatal("oversize taint set produced no diagnostic")
	}
}

func TestSecretEqualToShellKeepsStructure(t *testing.T) {
	tests := []struct {
		name, pool, step, want string
	}{
		{"bash", "", "bash: echo safe", "bash"},
		{"pwsh", "", "pwsh: echo safe", "pwsh"},
		{"powershell", "", "powershell: echo safe", "powershell"},
		{"azure-default", "unknown-image", "script: echo safe", "azure-default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yaml := ""
			if tt.pool != "" {
				yaml = "pool:\n  vmImage: " + tt.pool + "\n"
			}
			yaml += "parameters:\n- name: dbPassword\n  type: string\n  default: " + tt.name + "\nsteps:\n- " + tt.step + "\n"
			got := normalizeText(t, yaml)
			step := got.Jobs[0].Steps[len(got.Jobs[0].Steps)-1]
			if step.Run == nil || string(step.Run.Shell) != tt.want {
				t.Fatalf("shell = %#v, want %q", step.Run, tt.want)
			}
		})
	}
}

// F1: a secret equal to an enum value must not corrupt structural fields.
func TestSecretEqualToEnumValueKeepsStructure(t *testing.T) {
	for _, value := range []string{"runtime", "compile", "postgres", "admin"} {
		t.Run(value, func(t *testing.T) {
			got := normalizeText(t, `parameters:
- name: dbPassword
  type: string
  default: `+value+`
jobs:
- job: j
  container: postgres:15
  steps:
  - script: echo a
    name: s
    condition: succeeded()
`)
			job := jobByID(t, got, "j")
			for _, step := range job.Steps {
				if step.If != nil && step.If.Phase != ir.PhaseRuntime && step.If.Phase != ir.PhaseCompile && step.If.Phase != ir.PhaseCreation {
					t.Fatalf("phase corrupted: %q", step.If.Phase)
				}
			}
			if value == "postgres" {
				if out := canonical(t, got); strings.Contains(out, "postgres") {
					t.Fatalf("image text leaked:\n%s", out)
				}
				if job.Fidelity != diag.FidelityNeedsManual {
					t.Fatalf("job fidelity = %s", job.Fidelity)
				}
			}
		})
	}
}

// F2: replaced text is never scanned again (no nested placeholders).
func TestTaintPlaceholderNotNested(t *testing.T) {
	got := normalizeText(t, `parameters:
- name: dbPassword
  type: string
  default: firstS3cretValue
- name: apiToken
  type: string
  default: REDACTED
steps:
- script: echo firstS3cretValue and [REDACTED] and REDACTED
  name: s
`)
	if script := stepByID(t, got, implicitID, "s").Run.Script; script != "echo [REDACTED] and [REDACTED] and [REDACTED]" {
		t.Fatalf("script = %q", script)
	}
}

// F3: short and all-digit secrets are replaced on whole-leaf equality only.
func TestShortAndNumericSecretsWholeLeafOnly(t *testing.T) {
	got := normalizeText(t, `parameters:
- name: dbPassword
  type: string
  default: ab1
- name: apiToken
  type: string
  default: '246813'
steps:
- script: echo ab1 246813
  name: s
  env:
    A: ab1
    B: '246813'
    C: xab1x
`)
	step := stepByID(t, got, implicitID, "s")
	// A numeric secret of six or more characters is also replaced inside text.
	if step.Run.Script != "echo ab1 [REDACTED]" {
		t.Fatalf("script = %q", step.Run.Script)
	}
	env := map[string]string{}
	for _, entry := range step.Env {
		if lit := entry.Value.Literal; lit != nil {
			env[entry.Key] = lit.String + lit.Number
		}
	}
	if env["A"] != "[REDACTED]" || env["B"] != "[REDACTED]" || env["C"] != "xab1x" {
		t.Fatalf("env = %v", env)
	}
	if !hasLoss(t, step.Metadata, "secretMaterial") {
		t.Fatalf("losses = %v", lossProperties(t, step.Metadata))
	}
}
