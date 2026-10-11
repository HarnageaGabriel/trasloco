package normalize

import "testing"

// A declared secret equal to a structural selector value (deployment
// strategy) must not corrupt the plain-string IR field.
func TestSecretEqualToDeploymentStrategyKeepsStructure(t *testing.T) {
	got := normalizeText(t, `parameters:
- name: dbPassword
  type: string
  default: runOnce
jobs:
- deployment: dep
  environment: prod
  strategy:
    runOnce:
      deploy:
        steps:
        - script: echo safe
`)
	for _, job := range got.Jobs {
		if job.Deployment != nil {
			if job.Deployment.Strategy != "runOnce" {
				t.Fatalf("strategy = %q", job.Deployment.Strategy)
			}
			return
		}
	}
	t.Fatal("no deployment job")
}

func TestStructuralString(t *testing.T) {
	for _, c := range [][2]string{{"RunnerReq", "OS"}, {"RunnerReq", "Arch"}, {"DeploymentIntent", "Hook"}, {"ProtocolOperation", "Level"}, {"ActionStep", "Provider"}, {"Raw", "Kind"}, {"Literal", "Number"}} {
		if !structuralString(c[0], c[1]) {
			t.Fatalf("%v not structural", c)
		}
	}
	if structuralString("RunnerReq", "Pool") || structuralString("Stage", "ID") {
		t.Fatal("free text marked structural")
	}
}
