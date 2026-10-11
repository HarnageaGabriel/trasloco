package normalize

import (
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

func triggerWithPrefix(t *testing.T, p *ir.Pipeline, prefix string) ir.Trigger {
	t.Helper()
	for _, trigger := range p.Triggers {
		if strings.HasPrefix(trigger.ID, prefix) {
			return trigger
		}
	}
	t.Fatalf("no trigger with prefix %q in %v", prefix, p.Triggers)
	return ir.Trigger{}
}

// useSite is one place where the normalizer composes a string from a user
// value. forbidden lists the composites that must never appear; owner returns
// the node that must carry the secretMaterial loss.
type useSite struct {
	name      string
	body      string
	forbidden func(secret string) []string
	owner     func(t *testing.T, p *ir.Pipeline) ir.Metadata
}

func useSites() []useSite {
	const ref = "${{ parameters.dbPassword }}"
	job := func(_ *testing.T, p *ir.Pipeline) ir.Metadata { return p.Jobs[0].Metadata }
	return []useSite{
		{
			name: "step id", body: "steps:\n- script: echo hi\n  name: " + ref + "\n",
			forbidden: func(s string) []string { return []string{`"id":"` + s + `"`, `"name":"` + s + `"`} },
			owner: func(_ *testing.T, p *ir.Pipeline) ir.Metadata {
				return p.Jobs[0].Steps[len(p.Jobs[0].Steps)-1].Metadata
			},
		},
		{
			name: "exclude glob", body: "trigger:\n  branches:\n    exclude:\n    - " + ref + "\nsteps:\n- script: echo hi\n",
			forbidden: func(s string) []string { return []string{"!" + s} },
			owner:     func(t *testing.T, p *ir.Pipeline) ir.Metadata { return triggerWithPrefix(t, p, "ci").Metadata },
		},
		{
			name: "schedule glob", body: "schedules:\n- cron: '0 1 * * *'\n  branches:\n    exclude:\n    - " + ref + "\nsteps:\n- script: echo hi\n",
			forbidden: func(s string) []string { return []string{"!" + s} },
			owner:     func(t *testing.T, p *ir.Pipeline) ir.Metadata { return triggerWithPrefix(t, p, "schedule-").Metadata },
		},
		{
			name: "qualified job id", body: "stages:\n- stage: s\n  jobs:\n  - job: " + ref + "\n    steps:\n    - script: echo hi\n",
			forbidden: func(s string) []string { return []string{`"s.` + s + `"`} },
			owner:     job,
		},
		{
			name: "qualified stage prefix", body: "stages:\n- stage: " + ref + "\n  jobs:\n  - job: j\n    steps:\n    - script: echo hi\n",
			forbidden: func(s string) []string { return []string{`"` + s + `.j"`} },
			owner:     job,
		},
		{
			name: "qualified needs", body: "stages:\n- stage: s\n  jobs:\n  - job: " + ref + "\n    steps:\n    - script: echo hi\n  - job: a\n    dependsOn: " + ref + "\n    steps:\n    - script: echo hi\n",
			forbidden: func(s string) []string { return []string{`"s.` + s + `"`} },
			owner: func(t *testing.T, p *ir.Pipeline) ir.Metadata {
				for _, j := range p.Jobs {
					if len(j.Needs) != 0 {
						return j.Metadata
					}
				}
				t.Fatal("no job with needs")
				return ir.Metadata{}
			},
		},
		{
			name: "unknown key", body: "jobs:\n- job: a\n  " + ref + ": x\n  steps:\n  - script: echo hi\n",
			forbidden: func(s string) []string { return []string{"unknown." + s} },
			owner:     job,
		},
		{
			name: "service key", body: "jobs:\n- job: a\n  services:\n    " + ref + ":\n      image: postgres\n  steps:\n  - script: echo hi\n",
			forbidden: func(s string) []string { return []string{"service." + s} },
			owner:     job,
		},
		{
			name: "matrix key", body: "jobs:\n- job: a\n  strategy:\n    matrix:\n      leg:\n        " + ref + ": value\n  steps:\n  - script: echo hi\n",
			forbidden: func(s string) []string { return []string{"matrix." + s} },
			owner:     job,
		},
		{
			name:      "upstream trigger",
			body:      "resources:\n  pipelines:\n  - pipeline: " + ref + "\n    source: up\n    trigger: true\nsteps:\n- script: echo hi\n",
			forbidden: func(s string) []string { return []string{"upstream-" + s} },
			owner:     func(t *testing.T, p *ir.Pipeline) ir.Metadata { return triggerWithPrefix(t, p, "upstream-").Metadata },
		},
		{
			name:      "repository trigger",
			body:      "resources:\n  repositories:\n  - repository: " + ref + "\n    type: git\n    name: org/repo\n    trigger:\n    - main\nsteps:\n- script: echo hi\n",
			forbidden: func(s string) []string { return []string{"repository-" + s} },
			owner:     func(t *testing.T, p *ir.Pipeline) ir.Metadata { return triggerWithPrefix(t, p, "repository-").Metadata },
		},
		{
			name:      "webhook trigger",
			body:      "resources:\n  webhooks:\n  - webhook: " + ref + "\n    connection: c\nsteps:\n- script: echo hi\n",
			forbidden: func(s string) []string { return []string{"webhook-" + s} },
			owner:     func(t *testing.T, p *ir.Pipeline) ir.Metadata { return triggerWithPrefix(t, p, "webhook-").Metadata },
		},
		{
			name:      "container trigger",
			body:      "resources:\n  containers:\n  - container: " + ref + "\n    image: ubuntu\n    trigger: true\nsteps:\n- script: echo hi\n",
			forbidden: func(s string) []string { return []string{"container-" + s} },
			owner:     func(t *testing.T, p *ir.Pipeline) ir.Metadata { return triggerWithPrefix(t, p, "container-").Metadata },
		},
	}
}

// Every pre-composition replacement records a secretMaterial needs-manual loss
// on the node that owns the composite, and no composite keeps the secret.
func TestPreCompositionReplacementRecordsLoss(t *testing.T) {
	secrets := []string{"123456", "Qz9Long-Secret_Val", "7421", "x", "-12.5", "true"}
	for _, site := range useSites() {
		for _, secret := range secrets {
			t.Run(site.name+"/"+secret, func(t *testing.T) {
				got, collector := resolvedPipeline(t, "parameters:\n- name: dbPassword\n  type: string\n  default: \""+secret+"\"\n"+site.body)
				out := canonical(t, got)
				diagnostics := diagnosticsJSON(t, got, collector)
				for _, form := range site.forbidden(secret) {
					if strings.Contains(out, form) || strings.Contains(diagnostics, form) {
						t.Fatalf("composite %q leaked:\n%s", form, out)
					}
				}
				owner := site.owner(t, got)
				if !hasLoss(t, owner, "secretMaterial") || owner.Fidelity != diag.FidelityNeedsManual {
					t.Fatalf("owner has losses %v fidelity %v:\n%s", lossProperties(t, owner), owner.Fidelity, out)
				}
				if findings := ir.Validate(got); len(findings) != 0 {
					t.Fatalf("IR validation: %v", findings)
				}
			})
		}
	}
}

// Job prefixes follow the stage IDs the backstop de-duplicates in order.
func TestQualifiedPrefixesMatchStageOwners(t *testing.T) {
	const a, b = "${{ parameters.dbPassword }}", "${{ parameters.apiToken }}"
	text := "parameters:\n- name: dbPassword\n  type: string\n  default: \"123456\"\n- name: apiToken\n  type: string\n  default: \"7777777\"\n" +
		"stages:\n- stage: ok\n  jobs:\n  - job: j\n    steps:\n    - script: echo hi\n" +
		"- stage: " + a + "\n  jobs:\n  - job: " + b + "\n    steps:\n    - script: echo hi\n" +
		"- stage: " + b + "\n  jobs:\n  - job: " + a + "\n    steps:\n    - script: echo hi\n  - job: " + b + "\n    steps:\n    - script: echo hi\n"
	got, collector := resolvedPipeline(t, text)
	requireAbsent(t, got, collector, "123456", "7777777")
	for _, stage := range got.Stages {
		for _, id := range stage.Jobs {
			if !strings.HasPrefix(id, stage.ID+".") {
				t.Errorf("job %q does not start with its stage ID %q", id, stage.ID)
			}
		}
	}
	if findings := ir.Validate(got); len(findings) != 0 {
		t.Fatalf("IR validation: %v", findings)
	}
}

// F2: a clash in the declaration map must not repoint an edge.
func TestAmbiguousDeclarationIsNotRepointed(t *testing.T) {
	p := &ir.Pipeline{
		Stages: []ir.Stage{{ID: "s", Jobs: []string{"j", "j", "k"}}},
		Jobs:   []ir.Job{{ID: "j"}, {ID: "j"}, {ID: "k", Needs: []string{"j"}}},
	}
	plan := captureIDPlan(p)
	p.Jobs[1].ID = "j-2"
	plan.rewrite(p)
	if got := p.Jobs[2].Needs[0]; got != "j" {
		t.Fatalf("need repointed to %q on an ambiguous declaration", got)
	}
}

// Exponent decimals of six or more characters are replaced inside text too.
func TestExponentDecimalSecretReplacedInsideText(t *testing.T) {
	got, collector := resolvedPipeline(t, "parameters:\n- name: dbPassword\n  type: string\n  default: \"1.0E+10\"\nsteps:\n- script: echo 1.0E+10 done\n")
	requireAbsent(t, got, collector, "1.0E+10", "1.0e+10")
}
