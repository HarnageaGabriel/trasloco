package normalize

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

func TestExplicitShorthandShellOptions(t *testing.T) {
	got := normalizeText(t, `steps:
- bash: echo bash
- pwsh: Write-Host pwsh
- powershell: Write-Host powershell
`)
	assertValidIR(t, got)
	var runs []*ir.RunStep
	for _, step := range jobByID(t, got, implicitID).Steps {
		if step.Run != nil {
			runs = append(runs, step.Run)
		}
	}
	if len(runs) != 3 {
		t.Fatalf("run steps = %d, want 3", len(runs))
	}
	if runs[0].Shell != "bash" || runs[0].ShellOptions == nil || runs[0].ShellOptions.Errexit || runs[0].ShellOptions.Pipefail {
		t.Fatalf("bash step = %+v", runs[0])
	}
	if runs[1].Shell != "pwsh" || runs[1].ShellOptions != nil {
		t.Fatalf("pwsh step = %+v", runs[1])
	}
	if runs[2].Shell != "powershell" || runs[2].ShellOptions != nil {
		t.Fatalf("powershell step = %+v", runs[2])
	}
}

func TestGenericScriptShellFromResolvedRunner(t *testing.T) {
	cases := []struct {
		name        string
		pool        string
		wantShell   ir.Shell
		wantOptions bool
		wantLoss    bool
	}{
		{"windows hosted", "pool: {vmImage: windows-latest}", "cmd", false, false},
		{"linux hosted", "pool: {vmImage: ubuntu-latest}", "bash", true, false},
		{"macos hosted", "pool: {vmImage: macos-latest}", "bash", true, false},
		{"named self-hosted", "pool: {name: private-agents, vmImage: windows-latest}", "azure-default", false, true},
		{"unknown image", "pool: {vmImage: unknown-image}", "azure-default", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeText(t, "jobs:\n- job: j\n  "+tc.pool+"\n  steps:\n  - script: echo ok\n")
			assertValidIR(t, got)
			job := jobByID(t, got, "j")
			var run *ir.RunStep
			for _, step := range job.Steps {
				if step.Run != nil {
					run = step.Run
				}
			}
			if run == nil || run.Shell != tc.wantShell {
				t.Fatalf("shell = %v, want %s", run, tc.wantShell)
			}
			if tc.wantOptions && (run.ShellOptions == nil || run.ShellOptions.Errexit || run.ShellOptions.Pipefail) {
				t.Fatalf("shell options = %+v", run.ShellOptions)
			}
			if !tc.wantOptions && run.ShellOptions != nil {
				t.Fatalf("unexpected shell options = %+v", run.ShellOptions)
			}
			jobDefaultShellLoss := false
			for _, step := range job.Steps {
				if step.Run != nil && hasLoss(t, step.Metadata, "defaultShell") {
					jobDefaultShellLoss = true
				}
			}
			if jobDefaultShellLoss != tc.wantLoss {
				t.Fatalf("defaultShell loss = %v, want %v", jobDefaultShellLoss, tc.wantLoss)
			}
		})
	}
}

func TestActiveRunnerRestoresAcrossJobShapes(t *testing.T) {
	runNamed := func(t *testing.T, pipeline *ir.Pipeline, jobID, name string) *ir.RunStep {
		t.Helper()
		for _, step := range jobByID(t, pipeline, jobID).Steps {
			if step.Name == name && step.Run != nil {
				return step.Run
			}
		}
		t.Fatalf("named run step %q not found", name)
		return nil
	}

	t.Run("root job", func(t *testing.T) {
		got := normalizeText(t, `pool:
  vmImage: windows-latest
steps:
- script: echo ok
`)
		assertValidIR(t, got)
		if run := runStepByID(t, got, implicitID, "step-1"); run.Shell != "cmd" {
			t.Fatalf("root shell = %s, want cmd", run.Shell)
		}
	})

	t.Run("sibling jobs", func(t *testing.T) {
		got := normalizeText(t, `jobs:
- job: windows
  pool: {vmImage: windows-latest}
  steps:
  - script: echo windows
- job: unix
  pool: {vmImage: ubuntu-latest}
  steps:
  - script: echo unix
`)
		assertValidIR(t, got)
		if run := runStepByID(t, got, "windows", "step-1"); run.Shell != "cmd" {
			t.Fatalf("windows sibling shell = %s, want cmd", run.Shell)
		}
		if run := runStepByID(t, got, "unix", "step-1"); run.Shell != "bash" {
			t.Fatalf("unix sibling shell = %s, want bash", run.Shell)
		}
	})

	t.Run("deployment hook pool and restoration", func(t *testing.T) {
		got := normalizeText(t, `jobs:
- deployment: d
  environment: e
  pool: {vmImage: ubuntu-latest}
  strategy:
    runOnce:
      deploy:
        pool: {vmImage: windows-latest}
        steps:
        - script: echo hook
          name: hook
      routeTraffic:
        steps:
        - script: echo sibling
          name: sibling
`)
		assertValidIR(t, got)
		if run := runNamed(t, got, "d", "hook"); run.Shell != "cmd" {
			t.Fatalf("hook shell = %s, want cmd", run.Shell)
		}
		if run := runNamed(t, got, "d", "sibling"); run.Shell != "bash" {
			t.Fatalf("restored hook shell = %s, want bash", run.Shell)
		}
	})

	t.Run("deployment runner", func(t *testing.T) {
		got := normalizeText(t, `jobs:
- deployment: d
  environment: e
  pool: {vmImage: windows-latest}
  strategy:
    runOnce:
      deploy:
        steps:
        - script: echo deployment
          name: deployment
`)
		assertValidIR(t, got)
		if run := runNamed(t, got, "d", "deployment"); run.Shell != "cmd" {
			t.Fatalf("deployment shell = %s, want cmd", run.Shell)
		}
	})

	t.Run("stage pool inherited by job", func(t *testing.T) {
		got := normalizeText(t, `stages:
- stage: s
  pool: {vmImage: windows-latest}
  jobs:
  - job: j
    steps:
    - script: echo ok
      name: staged
`)
		assertValidIR(t, got)
		if run := runNamed(t, got, "s.j", "staged"); run.Shell != "cmd" || run.ShellOptions != nil {
			t.Fatalf("stage-inherited shell = %+v", run)
		}
	})

	t.Run("named hook pool stays azure-default with the shell loss", func(t *testing.T) {
		got := normalizeText(t, `jobs:
- deployment: d
  environment: e
  pool: {vmImage: ubuntu-latest}
  strategy:
    runOnce:
      deploy:
        pool: {name: private-agents}
        steps:
        - script: echo hook
          name: hook
`)
		assertValidIR(t, got)
		step := stepByID(t, got, "d", "hook")
		if step.Run == nil || step.Run.Shell != "azure-default" || step.Run.ShellOptions != nil {
			t.Fatalf("named hook shell = %+v", step.Run)
		}
		if !hasLoss(t, step.Metadata, "defaultShell") {
			t.Fatal("named hook kept an unresolved shell without the defaultShell loss")
		}
	})

	t.Run("agentless hook adds no runner loss", func(t *testing.T) {
		got := normalizeText(t, `jobs:
- deployment: d
  environment: e
  strategy:
    runOnce:
      deploy:
        pool: server
        steps:
        - script: echo invalid-on-server
`)
		assertValidIR(t, got)
		job := jobByID(t, got, "d")
		if hasLoss(t, job.Metadata, "server") {
			t.Fatalf("agentless hook added a job-level pool loss: %+v", job.Metadata)
		}
		if !hasLoss(t, job.Metadata, "hook.pool") {
			t.Fatalf("agentless hook lost the existing hook.pool loss: %+v", job.Metadata)
		}
		for _, finding := range got.Diagnostics {
			if finding.Params["construct"] == "pool" && finding.Params["property"] == "server" {
				t.Fatalf("agentless hook added a pool diagnostic: %+v", finding)
			}
		}
	})
}

func TestRunMacroRefsClassifyInTextualOrder(t *testing.T) {
	got := normalizeText(t, `variables:
- name: PLAIN
  value: visible
- name: TOKEN
  value: hunter2-Azure-secret-value
steps:
- script: echo $(PLAIN) $(TOKEN) $(PLAIN) $(DB_PASSWORD) $(Build.SourceVersion)
  workingDirectory: $(TOKEN)/$(PLAIN)
  env:
    WHOLE: $(PLAIN)
    WHOLE_SECRET: $(TOKEN)
    PARTIAL: prefix-$(PLAIN)
`)
	assertValidIR(t, got)
	run := runStepByID(t, got, implicitID, "step-1")
	want := []ir.MacroRef{
		{Name: "PLAIN", Declared: true},
		{Name: "TOKEN", Declared: true, Secret: true},
		{Name: "PLAIN", Declared: true},
		{Name: "DB_PASSWORD"},
		{Name: "Build.SourceVersion"},
		{Name: "TOKEN", Declared: true, Secret: true},
		{Name: "PLAIN", Declared: true},
	}
	if len(run.MacroRefs) != len(want) {
		t.Fatalf("macroRefs = %+v, want %+v", run.MacroRefs, want)
	}
	for index := range want {
		if run.MacroRefs[index] != want[index] {
			t.Fatalf("macroRefs[%d] = %+v, want %+v", index, run.MacroRefs[index], want[index])
		}
	}
	if whole, ok := runStepEnv(t, got, implicitID, "step-1", "WHOLE"); !ok || whole.Kind != ir.ExprMacro || whole.Macro.Name != "PLAIN" || !whole.Macro.Declared || whole.Macro.Secret {
		t.Fatalf("whole env = %+v, %v", whole, ok)
	}
	if secret, ok := runStepEnv(t, got, implicitID, "step-1", "WHOLE_SECRET"); !ok || secret.Kind != ir.ExprMacro || secret.Macro.Name != "TOKEN" || !secret.Macro.Declared || !secret.Macro.Secret {
		t.Fatalf("secret env = %+v, %v", secret, ok)
	}
	if partial, ok := runStepEnv(t, got, implicitID, "step-1", "PARTIAL"); !ok || partial.Kind != ir.ExprLiteral || partial.Literal.String != "prefix-$(PLAIN)" {
		t.Fatalf("partial env = %+v, %v", partial, ok)
	}
	assertUnresolvedMacroDiagnostic(t, got, "DB_PASSWORD")
	assertUnresolvedMacroDiagnostic(t, got, "Build.SourceVersion")
}

func TestWholeMacroClassificationInVariablesAndTaskInputs(t *testing.T) {
	got := normalizeText(t, `variables:
- name: PLAIN
  value: visible
- name: WHOLE
  value: $(PLAIN)
- name: PARTIAL
  value: prefix-$(PLAIN)
steps:
- task: Example@1
  inputs:
    whole: $(PLAIN)
    partial: prefix-$(PLAIN)
`)
	assertValidIR(t, got)
	if expr := variableExpr(t, got, "WHOLE"); expr == nil || expr.Kind != ir.ExprMacro || expr.Macro.Name != "PLAIN" || !expr.Macro.Declared || expr.Macro.Secret {
		t.Fatalf("whole variable = %+v", expr)
	}
	if expr := variableExpr(t, got, "PARTIAL"); expr == nil || expr.Kind != ir.ExprLiteral || expr.Literal.String != "prefix-$(PLAIN)" {
		t.Fatalf("partial variable = %+v", expr)
	}
	var task *ir.ActionStep
	for _, step := range jobByID(t, got, implicitID).Steps {
		if step.Action != nil {
			task = step.Action
		}
	}
	if task == nil {
		t.Fatal("task step missing")
	}
	if whole, ok := task.With.Get("whole"); !ok || whole.Kind != ir.ExprMacro || whole.Macro.Name != "PLAIN" || !whole.Macro.Declared {
		t.Fatalf("whole input = %+v, %v", whole, ok)
	}
	if partial, ok := task.With.Get("partial"); !ok || partial.Kind != ir.ExprLiteral || partial.Literal.String != "prefix-$(PLAIN)" {
		t.Fatalf("partial input = %+v, %v", partial, ok)
	}
}

func TestMacroClassificationRespectsStageScope(t *testing.T) {
	got := normalizeText(t, `stages:
- stage: one
  variables:
    TOKEN: hunter2-Azure-secret-value
  jobs:
  - job: first
    steps:
    - script: echo $(TOKEN)
- stage: two
  jobs:
  - job: second
    steps:
    - script: echo $(TOKEN)
`)
	assertValidIR(t, got)
	first := runStepByID(t, got, "one.first", "step-1")
	second := runStepByID(t, got, "two.second", "step-1")
	if first.MacroRefs[0].Name != "TOKEN" || !first.MacroRefs[0].Declared || !first.MacroRefs[0].Secret {
		t.Fatalf("declared stage macro = %+v", first.MacroRefs[0])
	}
	if second.MacroRefs[0].Name != "TOKEN" || second.MacroRefs[0].Declared || second.MacroRefs[0].Secret {
		t.Fatalf("out-of-scope macro = %+v", second.MacroRefs[0])
	}
	assertUnresolvedMacroDiagnostic(t, got, "TOKEN")
}

func TestMacroScopeShadowingAndSiblingIsolation(t *testing.T) {
	got := normalizeText(t, `variables:
  PLAIN: visible
stages:
- stage: one
  variables:
    TOKEN: hunter2-Azure-secret-value
  jobs:
  - job: shadow
    variables:
      token: $(PLAIN)
    steps:
    - script: echo $(TOKEN)
  - job: inherited
    steps:
    - script: echo $(TOKEN)
- stage: two
  jobs:
  - job: isolated
    steps:
    - script: echo $(TOKEN)
`)
	assertValidIR(t, got)
	shadow := runStepByID(t, got, "one.shadow", "step-1").MacroRefs[0]
	inherited := runStepByID(t, got, "one.inherited", "step-1").MacroRefs[0]
	isolated := runStepByID(t, got, "two.isolated", "step-1").MacroRefs[0]
	if shadow.Name != "TOKEN" || !shadow.Declared || shadow.Secret {
		t.Fatalf("shadowed macro = %+v", shadow)
	}
	if inherited.Name != "TOKEN" || !inherited.Declared || !inherited.Secret {
		t.Fatalf("inherited secret macro = %+v", inherited)
	}
	if isolated.Name != "TOKEN" || isolated.Declared || isolated.Secret {
		t.Fatalf("sibling-isolated macro = %+v", isolated)
	}
	if strings.Contains(canonical(t, got), "hunter2-Azure-secret-value") {
		t.Fatal("stage secret leaked into canonical IR")
	}
	assertUnresolvedMacroDiagnostic(t, got, "TOKEN")
}

func TestMacroClassificationExcludesGroupsAndParametersAndKeepsSecretValuesOut(t *testing.T) {
	const secret = "hunter2-Azure-task-input-secret"
	const scriptBody = "DIAGNOSTIC-BODY-CANARY"
	got := normalizeText(t, `parameters:
- name: TEMPLATE_INPUT
  type: string
  default: visible
variables:
- group: imported-secrets
- name: DB_PASSWORD
  value: `+secret+`
steps:
- task: Example@1
  inputs:
    declaredSecret: $(DB_PASSWORD)
    groupMember: $(GROUP_MEMBER)
    parameterName: $(TEMPLATE_INPUT)
- script: echo `+scriptBody+` $(Agent.Name) $(System.TeamProject)
`)
	assertValidIR(t, got)
	var task *ir.ActionStep
	for _, step := range jobByID(t, got, implicitID).Steps {
		if step.Action != nil {
			task = step.Action
		}
	}
	if task == nil {
		t.Fatal("task step missing")
	}
	for _, want := range []struct {
		name  string
		macro ir.MacroExpr
	}{
		{"declaredSecret", ir.MacroExpr{Name: "DB_PASSWORD", Declared: true, Secret: true}},
		{"groupMember", ir.MacroExpr{Name: "GROUP_MEMBER"}},
		{"parameterName", ir.MacroExpr{Name: "TEMPLATE_INPUT"}},
	} {
		expr, ok := task.With.Get(want.name)
		if !ok || expr.Kind != ir.ExprMacro || expr.Macro == nil || *expr.Macro != want.macro {
			t.Fatalf("task input %s = %+v, %v; want %+v", want.name, expr, ok, want.macro)
		}
	}
	run := runStepByID(t, got, implicitID, "step-2")
	if len(run.MacroRefs) != 2 || run.MacroRefs[0] != (ir.MacroRef{Name: "Agent.Name"}) || run.MacroRefs[1] != (ir.MacroRef{Name: "System.TeamProject"}) {
		t.Fatalf("system macro refs = %+v", run.MacroRefs)
	}
	encoded := canonical(t, got)
	if strings.Contains(encoded, secret) {
		t.Fatal("declared secret value leaked into IR")
	}
	diagnostics, err := json.Marshal(got.Diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(diagnostics), secret) || strings.Contains(string(diagnostics), scriptBody) {
		t.Fatalf("diagnostics leaked source text: %s", diagnostics)
	}
}

func TestNonSelfCheckoutMacroAndLiteralScrub(t *testing.T) {
	got := normalizeText(t, `variables:
  BRANCH: main
resources:
  repositories:
  - repository: tools
    type: git
    name: proj/tools
    ref: $(BRANCH)
  - repository: scrubbed
    type: git
    name: proj/scrubbed
    ref: refs/heads/ghp_123456789012345678901234567890123456
steps:
- checkout: tools
- checkout: scrubbed
`)
	assertValidIR(t, got)
	steps := jobByID(t, got, implicitID).Steps
	macroRef := steps[0].Checkout.Ref
	if macroRef.Kind != ir.ExprMacro || macroRef.Macro.Name != "BRANCH" || !macroRef.Macro.Declared || macroRef.Macro.Secret || steps[0].Checkout.RefSource != ir.CheckoutRefLiteral {
		t.Fatalf("macro checkout = %+v, %v", steps[0].Checkout, macroRef)
	}
	if hasLoss(t, steps[0].Metadata, "ref") {
		t.Fatal("declared whole checkout macro unexpectedly recorded a ref loss")
	}
	literalRef := steps[1].Checkout.Ref
	if literalRef.Kind != ir.ExprLiteral || strings.Contains(literalRef.Literal.String, "ghp_123456789012345678901234567890123456") || !hasLoss(t, steps[1].Metadata, "ref") {
		t.Fatalf("scrubbed checkout = %+v, literal=%q, losses=%v", steps[1].Checkout, literalRef.Literal.String, lossProperties(t, steps[1].Metadata))
	}
}

func runStepByID(t *testing.T, pipeline *ir.Pipeline, jobID, stepID string) *ir.RunStep {
	t.Helper()
	for _, step := range jobByID(t, pipeline, jobID).Steps {
		if step.ID == stepID && step.Run != nil {
			return step.Run
		}
	}
	for _, step := range jobByID(t, pipeline, jobID).Steps {
		if step.Run != nil {
			return step.Run
		}
	}
	t.Fatalf("run step %q not found", stepID)
	return nil
}

func runStepEnv(t *testing.T, pipeline *ir.Pipeline, jobID, stepID, key string) (ir.Expr, bool) {
	t.Helper()
	_ = stepID
	for _, step := range jobByID(t, pipeline, jobID).Steps {
		if step.Run != nil {
			return step.Env.Get(key)
		}
	}
	t.Fatal("run step not found")
	return ir.Expr{}, false
}

func variableExpr(t *testing.T, pipeline *ir.Pipeline, name string) *ir.Expr {
	t.Helper()
	for _, variable := range pipeline.Variables {
		if variable.Name == name {
			return variable.Value
		}
	}
	t.Fatalf("variable %q not found", name)
	return nil
}

func assertUnresolvedMacroDiagnostic(t *testing.T, pipeline *ir.Pipeline, name string) {
	t.Helper()
	found := false
	for _, finding := range pipeline.Diagnostics {
		if finding.Code != codeDeferred || finding.Params["property"] != "value" && finding.Params["property"] != "script" {
			continue
		}
		if finding.Message != "" && strings.Contains(finding.Message, name) {
			t.Fatalf("diagnostic leaked macro name %q: %q", name, finding.Message)
		}
		if strings.Contains(finding.Message, "VERIFY-22") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no unresolved macro diagnostic for %q", name)
	}
}

func TestJobContainerEnvSeesJobVariables(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		job  string
	}{
		{
			"job",
			`jobs:
- job: j
  variables:
    V: visible
  container:
    image: example/image:1
    env:
      X: $(V)
  steps:
  - script: echo ok
`,
			"j",
		},
		{
			"deployment",
			`jobs:
- deployment: d
  environment: e
  variables:
    V: visible
  container:
    image: example/image:1
    env:
      X: $(V)
  strategy:
    runOnce:
      deploy:
        steps:
        - script: echo ok
`,
			"d",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeText(t, tc.yaml)
			assertValidIR(t, got)
			job := jobByID(t, got, tc.job)
			if job.Container == nil {
				t.Fatal("container missing")
			}
			expr, ok := job.Container.Env.Get("X")
			if !ok || expr.Kind != ir.ExprMacro || expr.Macro == nil || expr.Macro.Name != "V" || !expr.Macro.Declared || expr.Macro.Secret {
				t.Fatalf("container env = %+v, %v", expr, ok)
			}
		})
	}

	t.Run("jobFromRoot", func(t *testing.T) {
		got := normalizeText(t, `variables:
  V: visible
container:
  image: example/image:1
  env:
    X: $(V)
steps:
- script: echo ok
`)
		assertValidIR(t, got)
		job := jobByID(t, got, implicitID)
		if job.Container == nil {
			t.Fatal("container missing")
		}
		expr, ok := job.Container.Env.Get("X")
		if !ok || expr.Kind != ir.ExprMacro || expr.Macro == nil || expr.Macro.Name != "V" || !expr.Macro.Declared {
			t.Fatalf("root container env = %+v, %v", expr, ok)
		}
	})

	t.Run("lifecycle hook step", func(t *testing.T) {
		got := normalizeText(t, `jobs:
- deployment: d
  environment: e
  variables:
    V: visible
  strategy:
    runOnce:
      deploy:
        steps:
        - script: echo ok
          name: hook
          env:
            X: $(V)
`)
		assertValidIR(t, got)
		expr, ok := stepByID(t, got, "d", "hook").Env.Get("X")
		if !ok || expr.Kind != ir.ExprMacro || expr.Macro == nil || expr.Macro.Name != "V" || !expr.Macro.Declared {
			t.Fatalf("hook env = %+v, %v", expr, ok)
		}
	})
}

func TestMacroScopeMixedDeclarationForms(t *testing.T) {
	got := normalizeText(t, `variables:
  ROOT_MAP: root
jobs:
- job: j
  variables:
  - name: JOB_LIST
    value: list
  steps:
  - script: echo $(ROOT_MAP) $(JOB_LIST) $(root_map)
`)
	assertValidIR(t, got)
	refs := runStepByID(t, got, "j", "step-1").MacroRefs
	want := []ir.MacroRef{
		{Name: "ROOT_MAP", Declared: true},
		{Name: "JOB_LIST", Declared: true},
		{Name: "root_map", Declared: true},
	}
	if len(refs) != len(want) {
		t.Fatalf("macroRefs = %+v, want %+v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("macroRefs[%d] = %+v, want %+v", i, refs[i], want[i])
		}
	}
}

func TestMacroNamesAreCaseInsensitive(t *testing.T) {
	got := normalizeText(t, `variables:
  myVar: visible
steps:
- script: echo $(MYVAR)
`)
	assertValidIR(t, got)
	ref := runStepByID(t, got, implicitID, "step-2").MacroRefs[0]
	if ref.Name != "MYVAR" || !ref.Declared || ref.Secret {
		t.Fatalf("case-insensitive macro = %+v", ref)
	}
}

func TestBashCommandSubstitutionIsNotAnUndefinedMacro(t *testing.T) {
	got := normalizeText(t, `steps:
- bash: |
    echo $(date) "$(( 1+2 ))"
`)
	assertValidIR(t, got)
	refs := runStepByID(t, got, implicitID, "step-2").MacroRefs
	if len(refs) != 1 || refs[0] != (ir.MacroRef{Name: "date"}) {
		t.Fatalf("bash macroRefs = %+v, want only the undeclared date macro", refs)
	}
}

func TestWhitespacePaddedWholeMacroStaysLiteral(t *testing.T) {
	got := normalizeText(t, `variables:
  PLAIN: visible
steps:
- script: echo ok
  env:
    PADDED: " $(PLAIN)"
`)
	assertValidIR(t, got)
	expr, ok := stepByID(t, got, implicitID, "step-2").Env.Get("PADDED")
	if !ok || expr.Kind != ir.ExprLiteral || expr.Literal == nil || expr.Literal.String != " $(PLAIN)" {
		t.Fatalf("padded env = %+v, %v", expr, ok)
	}
}

func TestUndeclaredSecretNamedMacrosAreNotSecret(t *testing.T) {
	got := normalizeText(t, `resources:
  repositories:
  - repository: tools
    type: git
    name: proj/tools
    ref: $(apiKey)
steps:
- checkout: tools
- script: echo ok
  name: env
  env:
    API: $(apiKey)
    TOKEN: $(token)
- task: Example@1
  name: task
  inputs:
    api: $(apiKey)
    token: $(token)
`)
	assertValidIR(t, got)
	checkout := stepByID(t, got, implicitID, "step-1").Checkout.Ref
	if checkout.Kind != ir.ExprMacro || checkout.Macro == nil || checkout.Macro.Name != "apiKey" || checkout.Macro.Declared || checkout.Macro.Secret {
		t.Fatalf("checkout ref = %+v", checkout)
	}
	env := stepByID(t, got, implicitID, "env").Env
	for _, key := range []string{"API", "TOKEN"} {
		expr, ok := env.Get(key)
		if !ok || expr.Kind != ir.ExprMacro || expr.Macro == nil || expr.Macro.Declared || expr.Macro.Secret {
			t.Fatalf("env %s = %+v, %v", key, expr, ok)
		}
	}
	var task *ir.ActionStep
	for _, step := range jobByID(t, got, implicitID).Steps {
		if step.ID == "task" {
			task = step.Action
		}
	}
	if task == nil {
		t.Fatal("task missing")
	}
	for _, key := range []string{"api", "token"} {
		expr, ok := task.With.Get(key)
		if !ok || expr.Kind != ir.ExprMacro || expr.Macro == nil || expr.Macro.Declared || expr.Macro.Secret {
			t.Fatalf("task input %s = %+v, %v", key, expr, ok)
		}
	}
}

func TestVariableExpansionNeverInfersSecret(t *testing.T) {
	got := normalizeText(t, `variables:
  WRAPPER: $(OTHER_SECRET)
steps:
- script: echo ok
`)
	assertValidIR(t, got)
	var wrapper *ir.Variable
	for i := range got.Variables {
		if got.Variables[i].Name == "WRAPPER" {
			wrapper = &got.Variables[i]
		}
	}
	if wrapper == nil {
		t.Fatal("WRAPPER variable missing")
	}
	// Intentional: a value that references another macro is not inferred to
	// be secret; only declared secret classification propagates.
	if wrapper.Secret || wrapper.SecretRef != nil {
		t.Fatalf("WRAPPER secret = %v / %+v", wrapper.Secret, wrapper.SecretRef)
	}
	if wrapper.Value == nil || wrapper.Value.Kind != ir.ExprMacro || wrapper.Value.Macro == nil || wrapper.Value.Macro.Name != "OTHER_SECRET" || wrapper.Value.Macro.Declared || wrapper.Value.Macro.Secret {
		t.Fatalf("WRAPPER value = %+v", wrapper.Value)
	}
}

func TestCredentialShapedMacroNameFallsBackToLiteral(t *testing.T) {
	name := "ghp_" + strings.Repeat("a", 36)
	got := normalizeText(t, `steps:
- script: echo $(`+name+`)
  name: scripted
  env:
    VALUE: $(`+name+`)
`)
	assertValidIR(t, got)
	if strings.Contains(canonical(t, got), name) {
		t.Fatal("credential-shaped macro name leaked into IR")
	}
	step := stepByID(t, got, implicitID, "scripted")
	if step.Run == nil || len(step.Run.MacroRefs) != 0 {
		t.Fatalf("script macroRefs = %+v", step.Run)
	}
	if !hasLoss(t, step.Metadata, "script") {
		t.Fatalf("script losses = %v, want the usual scrub loss", lossProperties(t, step.Metadata))
	}
	expr, ok := step.Env.Get("VALUE")
	if !ok || expr.Kind != ir.ExprLiteral || expr.Literal == nil || strings.Contains(expr.Literal.String, name) {
		t.Fatalf("credential-shaped env = %+v, %v", expr, ok)
	}
	if !hasLoss(t, step.Metadata, "VALUE") {
		t.Fatalf("env losses = %v, want the usual scrub loss", lossProperties(t, step.Metadata))
	}
}

func TestUndeclaredMacroWorkAndLossesBounded(t *testing.T) {
	const count = 20_000
	shape := func(macros bool) string {
		var out strings.Builder
		out.WriteString("steps:\n- script: |\n")
		for i := 0; i < count; i++ {
			if macros {
				out.WriteString("    echo $(UNDECLARED)\n")
			} else {
				out.WriteString("    echo ok\n")
			}
		}
		return out.String()
	}
	_, plainWork := pipelineWorkOf(t, shape(false))
	got, macroWork := pipelineWorkOf(t, shape(true))
	if plainWork == 0 || macroWork > 6*plainWork {
		t.Fatalf("work ratio too high: %d macros vs %d plain bytes", macroWork, plainWork)
	}
	refs := runStepByID(t, got, implicitID, "step-2").MacroRefs
	if len(refs) != count {
		t.Fatalf("macroRefs = %d, want every occurrence (%d)", len(refs), count)
	}
	losses := 0
	for _, finding := range got.Diagnostics {
		if finding.Code == codeDeferred && finding.Params["property"] == "script" {
			losses++
		}
	}
	if losses != 1 {
		t.Fatalf("deferred script losses = %d, want 1 for one distinct macro name", losses)
	}
}

func TestRepositoryResourceRefUsesPipelineRootScope(t *testing.T) {
	got := normalizeText(t, `variables:
  ROOT: main
resources:
  repositories:
  - repository: rooted
    type: git
    name: proj/rooted
    ref: $(ROOT)
  - repository: staged
    type: git
    name: proj/staged
    ref: $(STAGE_ONLY)
stages:
- stage: s
  variables:
    STAGE_ONLY: main
  jobs:
  - job: j
    steps:
    - checkout: rooted
      name: rooted
    - checkout: staged
      name: staged
`)
	assertValidIR(t, got)
	rooted := stepByID(t, got, "s.j", "rooted").Checkout.Ref
	if rooted.Kind != ir.ExprMacro || rooted.Macro == nil || rooted.Macro.Name != "ROOT" || !rooted.Macro.Declared {
		t.Fatalf("root ref = %+v", rooted)
	}
	staged := stepByID(t, got, "s.j", "staged").Checkout.Ref
	if staged.Kind != ir.ExprMacro || staged.Macro == nil || staged.Macro.Name != "STAGE_ONLY" || staged.Macro.Declared || staged.Macro.Secret {
		t.Fatalf("stage-only ref = %+v", staged)
	}
}

func TestGenericScriptResolvedShellWithSecretNamedShell(t *testing.T) {
	tests := []struct{ name, page, pool, want string }{
		{"bash", "bash", "ubuntu-latest", "bash"},
		{"cmd", "cmd", "windows-latest", "cmd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeText(t, `parameters:
- name: dbPassword
  type: string
  default: `+tt.page+`
jobs:
- job: j
  pool:
    vmImage: `+tt.pool+`
  steps:
  - script: echo safe
`)
			assertValidIR(t, got)
			run := runStepByID(t, got, "j", "step-1")
			if run.Shell != ir.Shell(tt.want) {
				t.Fatalf("shell = %s, want %s", run.Shell, tt.want)
			}
		})
	}
}
