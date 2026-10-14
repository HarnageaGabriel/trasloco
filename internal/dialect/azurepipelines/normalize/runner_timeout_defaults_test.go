package normalize

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

func assertValidIR(t *testing.T, pipeline *ir.Pipeline) {
	t.Helper()
	if findings := ir.Validate(pipeline); len(findings) != 0 {
		t.Fatalf("IR validation findings: %v", findings)
	}
}

// hasSynthetic reports a TRL-CONV-0004 note carrying the exact construct and
// property params. A bare codeSynth check is satisfied by the unrelated
// synthesized PR-trigger note, so the job/runner syntheses must be matched on
// their params.
func hasSynthetic(p *ir.Pipeline, construct, property string) bool {
	for _, finding := range p.Diagnostics {
		if finding.Code != codeSynth {
			continue
		}
		if finding.Params["construct"] == construct && finding.Params["property"] == property {
			return true
		}
	}
	return false
}

// lossDetail returns the recorded detail of the loss on the given property.
func lossDetail(t *testing.T, m ir.Metadata, property string) string {
	t.Helper()
	raw, ok := m.Extensions[ExtensionKey]
	if !ok {
		return ""
	}
	var bag struct {
		Losses []struct {
			Property string `json:"property"`
			Detail   string `json:"detail"`
		} `json:"losses"`
	}
	if err := json.Unmarshal(raw, &bag); err != nil {
		t.Fatal(err)
	}
	for _, l := range bag.Losses {
		if l.Property == property {
			return l.Detail
		}
	}
	return ""
}

func TestRunnerPoolInheritanceAndDefault(t *testing.T) {
	cases := []struct {
		name      string
		yaml      string
		jobID     string
		wantPool  string
		wantImage string
		wantOS    string
	}{
		{"no pool anywhere", "jobs:\n- job: j\n  steps: []\n", "j", "", "ubuntu-latest", "linux"},
		{"pipeline pool", "pool:\n  name: root-agents\njobs:\n- job: j\n  steps: []\n", "j", "root-agents", "", ""},
		{"stage pool", "stages:\n- stage: s\n  pool:\n    name: stage-agents\n  jobs:\n  - job: j\n    steps: []\n", "s.j", "stage-agents", "", ""},
		{"pool expression", "jobs:\n- job: j\n  pool: $[ variables.pool ]\n  steps: []\n", "j", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeText(t, tc.yaml)
			assertValidIR(t, got)
			job := jobByID(t, got, tc.jobID)
			if job.Runner.Pool != tc.wantPool || job.Runner.Image != tc.wantImage || job.Runner.OS != tc.wantOS {
				t.Fatalf("runner = %+v", job.Runner)
			}
			if tc.name == "no pool anywhere" {
				if !hasSynthetic(got, "runner", "pool") {
					t.Fatalf("missing runner/pool synthesis note: %v", got.Diagnostics)
				}
				if job.Timeout == nil || job.Timeout.Duration != time.Hour {
					t.Fatalf("default timeout = %v", job.Timeout)
				}
			}
			if tc.name == "pool expression" && (job.Timeout != nil || !hasLoss(t, job.Metadata, "timeoutInMinutes")) {
				t.Fatalf("expression pool timeout = %v, losses %v", job.Timeout, lossProperties(t, job.Metadata))
			}
		})
	}
}

func TestRunnerHostedImageOS(t *testing.T) {
	cases := []struct{ image, os string }{
		{"windows-latest", "windows"},
		{"macos-latest", "macos"},
		{"ubuntu-22.04", "linux"},
		{"private-image", ""},
	}
	for _, tc := range cases {
		t.Run(tc.image, func(t *testing.T) {
			got := normalizeText(t, "jobs:\n- job: j\n  pool:\n    vmImage: "+tc.image+"\n  steps: []\n")
			assertValidIR(t, got)
			job := jobByID(t, got, "j")
			if job.Runner.Image != tc.image || job.Runner.OS != tc.os || job.Runner.Arch != "" {
				t.Fatalf("runner = %+v", job.Runner)
			}
			if tc.os == "" && (job.Timeout != nil || !hasLoss(t, job.Metadata, "timeoutInMinutes")) {
				t.Fatalf("unknown hosted timeout = %v, losses %v", job.Timeout, lossProperties(t, job.Metadata))
			}
		})
	}
}

// TestDeclaredPoolNeverDefaultsRunner pins that any declared pool other than the
// documented Microsoft-hosted one suppresses the ubuntu-latest runner default
// and the 60-minute timeout default, whatever form the pool takes.
func TestDeclaredPoolNeverDefaultsRunner(t *testing.T) {
	cases := []struct {
		name   string
		yaml   string
		jobID  string
		pool   string
		image  string
		demand string
	}{
		{
			name:  "pipeline shorthand pool",
			yaml:  "pool: MyPool\njobs:\n- job: j\n  steps: []\n",
			jobID: "j", pool: "MyPool",
		},
		{
			name:  "stage shorthand pool",
			yaml:  "stages:\n- stage: s\n  pool: MyPool\n  jobs:\n  - job: j\n    steps: []\n",
			jobID: "s.j", pool: "MyPool",
		},
		{
			name:  "job shorthand pool",
			yaml:  "jobs:\n- job: j\n  pool: MyPool\n  steps: []\n",
			jobID: "j", pool: "MyPool",
		},
		{
			name:  "demands-only pool",
			yaml:  "jobs:\n- job: j\n  pool:\n    demands:\n    - Custom.Agent\n  steps: []\n",
			jobID: "j", demand: "Custom.Agent",
		},
		{
			name:  "macro pool",
			yaml:  "jobs:\n- job: j\n  pool: $(poolName)\n  steps: []\n",
			jobID: "j", pool: "$(poolName)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeText(t, tc.yaml)
			assertValidIR(t, got)
			job := jobByID(t, got, tc.jobID)
			if job.Runner.Pool != tc.pool || job.Runner.Image != tc.image {
				t.Fatalf("runner = %+v, want pool %q image %q", job.Runner, tc.pool, tc.image)
			}
			if tc.demand != "" && (len(job.Runner.Demands) != 1 || job.Runner.Demands[0] != tc.demand) {
				t.Fatalf("demands = %v", job.Runner.Demands)
			}
			if job.Timeout != nil || !hasLoss(t, job.Metadata, "timeoutInMinutes") {
				t.Fatalf("timeout = %v, losses %v", job.Timeout, lossProperties(t, job.Metadata))
			}
			if hasSynthetic(got, "runner", "pool") {
				t.Fatal("hosted runner default synthesized for a declared pool")
			}
		})
	}
}

// TestTemplateExpressionPoolDeclared resolves a pool whose value is a
// compile-time template expression and pins that the resolved (non-hosted) pool
// still suppresses the hosted runner and timeout defaults.
func TestTemplateExpressionPoolDeclared(t *testing.T) {
	got, _ := resolvedPipeline(t, `parameters:
- name: p
  type: string
  default: MyPool
jobs:
- job: j
  pool: ${{ parameters.p }}
  steps: []
`)
	assertValidIR(t, got)
	job := jobByID(t, got, "j")
	if job.Runner.Pool != "MyPool" || job.Runner.Image != "" {
		t.Fatalf("runner = %+v", job.Runner)
	}
	if job.Timeout != nil || !hasLoss(t, job.Metadata, "timeoutInMinutes") {
		t.Fatalf("timeout = %v, losses %v", job.Timeout, lossProperties(t, job.Metadata))
	}
	if hasSynthetic(got, "runner", "pool") {
		t.Fatal("hosted runner default synthesized for a template-expression pool")
	}
}

// TestHostedPoolNameTimeoutDefault pins that the Microsoft-hosted "Azure
// Pipelines" pool name is treated like an unnamed hosted pool: the 1h default
// applies with a documented image, and an unknown image still loses.
func TestHostedPoolNameTimeoutDefault(t *testing.T) {
	got := normalizeText(t, `jobs:
- job: hosted
  pool:
    name: Azure Pipelines
    vmImage: windows-latest
  steps: []
- job: no-image
  pool:
    name: Azure Pipelines
  steps: []
- job: private
  pool:
    name: MyPool
    vmImage: windows-latest
  steps: []
`)
	assertValidIR(t, got)

	hosted := jobByID(t, got, "hosted")
	if hosted.Runner.Pool != "Azure Pipelines" || hosted.Runner.Image != "windows-latest" || hosted.Runner.OS != "windows" {
		t.Fatalf("hosted runner = %+v", hosted.Runner)
	}
	if hosted.Timeout == nil || hosted.Timeout.Duration != time.Hour || hosted.Fidelity != diag.FidelityApproximated {
		t.Fatalf("hosted timeout = %v, fidelity = %s", hosted.Timeout, hosted.Fidelity)
	}
	if !hasSynthetic(got, "job", "timeoutInMinutes") {
		t.Fatalf("missing job/timeoutInMinutes synthesis note: %v", got.Diagnostics)
	}

	noImage := jobByID(t, got, "no-image")
	if noImage.Runner.Pool != "Azure Pipelines" || noImage.Runner.Image != "" {
		t.Fatalf("named hosted pool without vmImage runner = %+v", noImage.Runner)
	}
	if noImage.Timeout != nil || !hasLoss(t, noImage.Metadata, "timeoutInMinutes") {
		t.Fatalf("named hosted pool without vmImage timeout = %v", noImage.Timeout)
	}

	private := jobByID(t, got, "private")
	if private.Runner.Image != "windows-latest" {
		t.Fatalf("private runner = %+v", private.Runner)
	}
	if private.Timeout != nil || !hasLoss(t, private.Metadata, "timeoutInMinutes") {
		t.Fatalf("private pool with hosted image timeout = %v", private.Timeout)
	}
}

func TestDeploymentHostedTimeoutDefault(t *testing.T) {
	got := normalizeText(t, `jobs:
- deployment: d
  environment: test
  pool:
    vmImage: ubuntu-latest
  strategy:
    runOnce:
      deploy:
        steps: []
`)
	assertValidIR(t, got)
	job := jobByID(t, got, "d")
	if job.Timeout == nil || job.Timeout.Duration != time.Hour {
		t.Fatalf("deployment timeout = %v", job.Timeout)
	}
	if !hasSynthetic(got, "deployment", "timeoutInMinutes") {
		t.Fatalf("missing deployment/timeoutInMinutes synthesis note: %v", got.Diagnostics)
	}
}

func TestJobTimeoutDefaults(t *testing.T) {
	got := normalizeText(t, `jobs:
- job: hosted
  pool: {vmImage: windows-latest}
  steps: []
- job: zero
  pool: {vmImage: ubuntu-latest}
  timeoutInMinutes: 0
  steps: []
- job: thirty
  pool: {vmImage: ubuntu-latest}
  timeoutInMinutes: 30
  steps: []
- job: expression
  pool: {vmImage: ubuntu-latest}
  timeoutInMinutes: $(timeout)
  steps: []
- job: self
  pool: private-agents
  steps: []
- job: agentless
  pool: server
  steps: []
`)
	assertValidIR(t, got)
	hosted := jobByID(t, got, "hosted")
	if hosted.Timeout == nil || hosted.Timeout.Duration != time.Hour || hosted.Fidelity != diag.FidelityApproximated {
		t.Fatalf("hosted timeout = %v, fidelity = %s", hosted.Timeout, hosted.Fidelity)
	}
	if !hasSynthetic(got, "job", "timeoutInMinutes") {
		t.Fatalf("missing job/timeoutInMinutes synthesis note: %v", got.Diagnostics)
	}
	zero := jobByID(t, got, "zero")
	if zero.Timeout != nil || !hasLoss(t, zero.Metadata, "timeoutInMinutes") {
		t.Fatalf("zero timeout = %v", zero.Timeout)
	}
	if thirty := jobByID(t, got, "thirty"); thirty.Timeout == nil || thirty.Timeout.Duration != 30*time.Minute {
		t.Fatalf("explicit timeout = %v", thirty.Timeout)
	}
	expression := jobByID(t, got, "expression")
	if expression.Timeout != nil || !hasLoss(t, expression.Metadata, "timeoutInMinutes") {
		t.Fatalf("expression timeout = %v", expression.Timeout)
	}
	self := jobByID(t, got, "self")
	if self.Timeout != nil || !hasLoss(t, self.Metadata, "timeoutInMinutes") {
		t.Fatalf("self-hosted timeout = %v", self.Timeout)
	}
	agentless := jobByID(t, got, "agentless")
	if agentless.Timeout != nil || hasLoss(t, agentless.Metadata, "timeoutInMinutes") {
		t.Fatalf("agentless timeout/loss = %v, %v", agentless.Timeout, lossProperties(t, agentless.Metadata))
	}
}

// TestDeploymentHookPoolBlocksRunnerDefault pins that a pool declared only on a
// deployment lifecycle hook (runOnce deploy, rolling preDeploy, canary on
// failure) suppresses the hosted runner and timeout defaults, and that the
// timeout loss says so rather than blaming a self-hosted pool.
func TestDeploymentHookPoolBlocksRunnerDefault(t *testing.T) {
	got := normalizeText(t, `jobs:
- deployment: runonce
  environment: test
  strategy:
    runOnce:
      deploy:
        pool: hook-agents
        steps: []
- deployment: rolling
  environment: test
  strategy:
    rolling:
      preDeploy:
        pool: hook-agents
        steps: []
      deploy:
        steps: []
- deployment: canary
  environment: test
  strategy:
    canary:
      increments: [10, 50]
      deploy:
        steps: []
      on:
        failure:
          pool: hook-agents
          steps: []
`)
	assertValidIR(t, got)
	const wantDetail = "omitted job timeout on a deployment-hook runner cannot be materialized"
	for _, id := range []string{"runonce", "rolling", "canary"} {
		job := jobByID(t, got, id)
		if job.Runner.Image != "" || job.Runner.Pool != "" {
			t.Fatalf("%s: hook-pooled deployment runner = %+v", id, job.Runner)
		}
		if job.Timeout != nil || !hasLoss(t, job.Metadata, "timeoutInMinutes") {
			t.Fatalf("%s: hook-pooled deployment timeout = %v, losses %v", id, job.Timeout, lossProperties(t, job.Metadata))
		}
		if detail := lossDetail(t, job.Metadata, "timeoutInMinutes"); detail != wantDetail {
			t.Fatalf("%s: hook timeout loss detail = %q, want %q", id, detail, wantDetail)
		}
	}
}
