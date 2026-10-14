package normalize

import (
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
			if tc.name == "no pool anywhere" && (!hasCode(got, codeSynth) || job.Timeout == nil || job.Timeout.Duration != time.Hour) {
				t.Fatalf("default diagnostics/timeout = %v, %v", got.Diagnostics, job.Timeout)
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
	if hosted := jobByID(t, got, "hosted"); hosted.Timeout == nil || hosted.Timeout.Duration != time.Hour || hosted.Fidelity != diag.FidelityApproximated {
		t.Fatalf("hosted timeout = %v, fidelity = %s", hosted.Timeout, hosted.Fidelity)
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

func TestDeploymentHookPoolBlocksRunnerDefault(t *testing.T) {
	got := normalizeText(t, `jobs:
- deployment: deploy
  environment: test
  strategy:
    runOnce:
      deploy:
        pool: hook-agents
        steps: []
`)
	assertValidIR(t, got)
	job := jobByID(t, got, "deploy")
	if job.Runner.Image != "" || job.Timeout != nil || !hasLoss(t, job.Metadata, "timeoutInMinutes") {
		t.Fatalf("hook-pooled deployment = runner %+v, timeout %v", job.Runner, job.Timeout)
	}
}
