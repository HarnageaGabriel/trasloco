package normalize

import (
	"context"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

// The resolver substitutes secret-classified root parameter and variable
// literals into every use site. These inputs are the post-resolution shape:
// the declaration keeps the literal and the use sites carry it verbatim.

func TestSecretParameterLiteralSubstitutedEverywhere(t *testing.T) {
	const secret = "s3cretPassw0rd"
	got := normalizeText(t, `parameters:
- name: dbPassword
  type: string
  default: `+secret+`
steps:
- script: psql -W `+secret+`
  displayName: login `+secret+`
  name: s
  env:
    DB: `+secret+`
- task: Foo@1
  name: t
  inputs:
    conn: host=`+secret+`
`)
	out := canonical(t, got)
	if strings.Contains(out, secret) {
		t.Fatalf("secret leaked:\n%s", out)
	}
	for _, id := range []string{"s", "t"} {
		step := stepByID(t, got, implicitID, id)
		if !hasLoss(t, step.Metadata, "secretMaterial") || step.Fidelity != diag.FidelityNeedsManual {
			t.Fatalf("step %s: loss/fidelity = %v %v", id, lossProperties(t, step.Metadata), step.Fidelity)
		}
	}
	if script := stepByID(t, got, implicitID, "s").Run.Script; script != "psql -W [REDACTED]" {
		t.Fatalf("script = %q", script)
	}
}

func TestSecretVariableLiteralSubstitutedEverywhere(t *testing.T) {
	const secret = "v4rSecretValue"
	for _, tc := range []struct{ name, vars string }{
		{"map", "variables:\n  dbPassword: " + secret + "\n"},
		{"list", "variables:\n- name: apiToken\n  value: " + secret + "\n"},
	} {
		name, vars := tc.name, tc.vars
		t.Run(name, func(t *testing.T) {
			got := normalizeText(t, vars+"steps:\n- script: echo "+secret+"\n  displayName: x "+secret+"\n  name: s\n")
			if out := canonical(t, got); strings.Contains(out, secret) {
				t.Fatalf("secret leaked:\n%s", out)
			}
			step := stepByID(t, got, implicitID, "s")
			if !hasLoss(t, step.Metadata, "secretMaterial") {
				t.Fatalf("no loss: %v", lossProperties(t, step.Metadata))
			}
		})
	}
}

func TestSecretMapFormParameterSubstituted(t *testing.T) {
	const secret = "mapFormSecret1"
	got := normalizeText(t, "parameters:\n  dbPassword: "+secret+"\nsteps:\n- script: echo "+secret+"\n  name: s\n")
	if out := canonical(t, got); strings.Contains(out, secret) {
		t.Fatalf("secret leaked:\n%s", out)
	}
}

func TestSecretObjectDefaultLeavesSubstituted(t *testing.T) {
	got := normalizeText(t, `parameters:
- name: creds
  type: object
  default:
    password: nestedS3cret
    user: admin1
    list:
    - token: listedS3cret
steps:
- script: login admin1 nestedS3cret listedS3cret
  displayName: nestedS3cret
  name: s
`)
	out := canonical(t, got)
	for _, leak := range []string{"nestedS3cret", "listedS3cret"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q leaked:\n%s", leak, out)
		}
	}
	if script := stepByID(t, got, implicitID, "s").Run.Script; script != "login admin1 [REDACTED] [REDACTED]" {
		t.Fatalf("script = %q (non-secret sibling must survive)", script)
	}
}

func TestSecretCollectionSkipsBooleansNumbersAndShortValues(t *testing.T) {
	got := normalizeText(t, `parameters:
- name: secretFlag
  type: boolean
  default: true
- name: secretCount
  type: number
  default: 12345
- name: shortPassword
  type: string
  default: abc
steps:
- script: echo true 12345 abc
  name: s
`)
	step := stepByID(t, got, implicitID, "s")
	if step.Run.Script != "echo true 12345 abc" || hasLoss(t, step.Metadata, "secretMaterial") {
		t.Fatalf("script = %q losses %v", step.Run.Script, lossProperties(t, step.Metadata))
	}
}

func TestEmptyOrNullConditionIsAbsent(t *testing.T) {
	for _, tc := range []struct{ name, cond string }{{"empty", "''"}, {"null", "null"}, {"tilde", "~"}, {"blank", ""}} {
		name, cond := tc.name, tc.cond
		t.Run(name, func(t *testing.T) {
			got := normalizeText(t, "steps:\n- script: echo a\n  name: s\n  condition: "+cond+"\n")
			step := stepByID(t, got, implicitID, "s")
			if step.If == nil || step.If.Opaque != nil || condFunction(step.If) != ir.FunctionSuccess {
				t.Fatalf("condition = %#v", step.If)
			}
			if hasLoss(t, step.Metadata, "expression") || step.Fidelity == diag.FidelityNeedsManual {
				t.Fatalf("losses %v fidelity %v", lossProperties(t, step.Metadata), step.Fidelity)
			}
		})
	}
}

func TestEmptyConditionDoesNotDisableHookGate(t *testing.T) {
	got := normalizeText(t, `jobs:
- deployment: d
  environment: prod
  strategy:
    runOnce:
      deploy:
        steps:
        - script: echo deploy
      on:
        failure:
          steps:
          - script: echo rollback
            name: rb
            condition: ''
`)
	step := stepByID(t, got, "d", "rb")
	if condFunction(step.If) != ir.FunctionFailure {
		t.Fatalf("hook step condition = %#v, want failure()", step.If)
	}
}

func TestEmptyOrNullContainerIsNone(t *testing.T) {
	for _, tc := range []struct{ name, container string }{{"empty", "''"}, {"blank", ""}} {
		name, container := tc.name, tc.container
		t.Run(name, func(t *testing.T) {
			got := normalizeText(t, "jobs:\n- job: j\n  container: "+container+"\n  steps:\n  - script: echo a\n")
			job := jobByID(t, got, "j")
			if job.Container != nil || hasLoss(t, job.Metadata, "alias") {
				t.Fatalf("container = %#v losses %v", job.Container, lossProperties(t, job.Metadata))
			}
		})
	}
}

func TestNullContainerAliasIsUnresolvedNotImageNull(t *testing.T) {
	got := normalizeText(t, "jobs:\n- job: j\n  container: null\n  steps:\n  - script: echo a\n")
	job := jobByID(t, got, "j")
	if job.Container != nil || !hasLoss(t, job.Metadata, "alias") {
		t.Fatalf("container = %#v losses %v", job.Container, lossProperties(t, job.Metadata))
	}
	if strings.Contains(canonical(t, got), `"image":"null"`) {
		t.Fatal("image null produced")
	}
}

// TestMatrixOpaqueDynamicBranch builds the AST directly: a strategy whose
// matrix mapping has no entries and no parallel count cannot be expanded.
func TestMatrixOpaqueDynamicBranch(t *testing.T) {
	root := parse.Source("p.yml", []byte("strategy:\n  matrix:\n    a:\n      k: v\nsteps:\n- script: echo a\n"), diag.NewCollector())
	if root.Strategy == nil || root.Strategy.Matrix == nil {
		t.Fatal("fixture has no strategy matrix")
	}
	root.Strategy.Matrix.Entries = nil
	got, err := Pipeline(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	job := jobByID(t, got, implicitID)
	if job.Matrix == nil || job.Matrix.Opaque == nil || len(job.Matrix.Legs) != 0 {
		t.Fatalf("matrix = %#v", job.Matrix)
	}
	if text := string(job.Matrix.Opaque.JSON); !strings.Contains(text, `"dynamic":true`) {
		t.Fatalf("opaque = %s", text)
	}
	if !hasLoss(t, job.Metadata, "matrix") {
		t.Fatalf("losses = %v", lossProperties(t, job.Metadata))
	}
}

func TestOriginHashUnavailableWithoutSource(t *testing.T) {
	parsed := parse.Source("p.yml", []byte("steps:\n- script: echo a\n  name: s\n"), diag.NewCollector())
	got, err := Pipeline(context.Background(), parsed, Options{})
	if err != nil {
		t.Fatal(err)
	}
	step := stepByID(t, got, implicitID, "s")
	if step.Origin == nil || step.Origin.OriginalTextHash != "unavailable" {
		t.Fatalf("origin = %#v", step.Origin)
	}
	withText := normalizeText(t, "steps:\n- script: echo a\n  name: s\n")
	if hash := stepByID(t, withText, implicitID, "s").Origin.OriginalTextHash; !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("hash = %q", hash)
	}
}
