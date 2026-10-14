package normalize

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

func normalizeText(t testing.TB, text string) *ir.Pipeline {
	t.Helper()
	data := []byte(text)
	parsed := parse.Source("pipeline.yml", data, diag.NewCollector())
	got, err := Pipeline(context.Background(), parsed, Options{SourceText: func(string) ([]byte, bool) { return data, true }})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func jobByID(t *testing.T, p *ir.Pipeline, id string) ir.Job {
	t.Helper()
	for _, job := range p.Jobs {
		if job.ID == id {
			return job
		}
	}
	t.Fatalf("job %q not found in %v", id, p.Jobs)
	return ir.Job{}
}

func condFunction(cond *ir.Cond) ir.Function {
	if cond == nil || cond.Expr == nil || cond.Expr.Call == nil {
		return ""
	}
	return cond.Expr.Call.Function
}

func lossProperties(t *testing.T, m ir.Metadata) []string {
	t.Helper()
	var bag struct {
		Losses []struct {
			Property string `json:"property"`
		} `json:"losses"`
	}
	raw, ok := m.Extensions[ExtensionKey]
	if !ok {
		return nil
	}
	if err := json.Unmarshal(raw, &bag); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range bag.Losses {
		out = append(out, l.Property)
	}
	return out
}

func hasLoss(t *testing.T, m ir.Metadata, property string) bool {
	for _, p := range lossProperties(t, m) {
		if p == property {
			return true
		}
	}
	return false
}

func extensionValue(t *testing.T, m ir.Metadata, key string) (string, bool) {
	t.Helper()
	raw, ok := m.Extensions[ExtensionKey]
	if !ok {
		return "", false
	}
	var bag struct {
		Values map[string]json.RawMessage `json:"values"`
	}
	if err := json.Unmarshal(raw, &bag); err != nil {
		t.Fatal(err)
	}
	value, ok := bag.Values[key]
	if !ok {
		return "", false
	}
	var out string
	if err := json.Unmarshal(value, &out); err != nil {
		t.Fatal(err)
	}
	return out, true
}

func TestDeploymentHookConditions(t *testing.T) {
	got := normalizeText(t, `jobs:
- deployment: d
  environment: prod
  strategy:
    runOnce:
      deploy:
        steps:
        - script: echo deploy
          name: dep
      on:
        failure:
          steps:
          - script: echo rollback
            name: rb
        success:
          steps:
          - script: echo ok
            name: okstep
`)
	job := jobByID(t, got, "d")
	conds := map[string]ir.Function{}
	for _, s := range job.Steps {
		conds[s.ID] = condFunction(s.If)
	}
	if conds["rb"] != ir.FunctionFailure {
		t.Fatalf("on.failure step condition = %q", conds["rb"])
	}
	if conds["okstep"] != ir.FunctionSuccess || conds["dep"] != ir.FunctionSuccess {
		t.Fatalf("conds = %v", conds)
	}
	if conds["implicit-download"] != ir.FunctionSuccess {
		t.Fatalf("implicit download missing: %v", conds)
	}
}

func TestDeploymentSingleHookNoLossAndDownloadNone(t *testing.T) {
	got := normalizeText(t, `jobs:
- deployment: d
  environment: prod
  strategy:
    runOnce:
      deploy:
        steps:
        - download: none
        - script: echo hi
`)
	job := jobByID(t, got, "d")
	for _, s := range job.Steps {
		if s.ID == "implicit-download" {
			t.Fatal("implicit download emitted despite download: none")
		}
	}
	if hasLoss(t, job.Metadata, "lifecycleHooks") {
		t.Fatal("spurious lifecycleHooks loss for a single hook")
	}
}

func TestEnvironmentShortForm(t *testing.T) {
	got := normalizeText(t, `jobs:
- deployment: d
  environment: smarthotel-dev.bookings
  strategy:
    runOnce:
      deploy:
        steps:
        - script: echo hi
`)
	job := jobByID(t, got, "d")
	if job.Deployment.Environment != "smarthotel-dev" {
		t.Fatalf("environment = %q", job.Deployment.Environment)
	}
	if !hasLoss(t, job.Metadata, "environment.resource") {
		t.Fatal("environment resource loss missing")
	}
}

func TestStageDependsOnDefaults(t *testing.T) {
	got := normalizeText(t, `stages:
- stage: a
  jobs:
  - job: j
    steps:
    - script: echo a
- stage: b
  jobs:
  - job: j
    steps:
    - script: echo b
- stage: c
  dependsOn: []
  jobs:
  - job: j
    steps:
    - script: echo c
- stage: d
  dependsOn: [a, c]
  jobs:
  - job: j
    steps:
    - script: echo d
`)
	want := map[string][]string{"a": nil, "b": {"a"}, "c": nil, "d": {"a", "c"}}
	for _, stage := range got.Stages {
		if stage.Barrier {
			t.Fatalf("stage %s has a barrier", stage.ID)
		}
		if strings.Join(stage.DependsOn, ",") != strings.Join(want[stage.ID], ",") {
			t.Fatalf("stage %s dependsOn = %v, want %v", stage.ID, stage.DependsOn, want[stage.ID])
		}
	}
}

func TestCrossStageDuplicateJobNames(t *testing.T) {
	got := normalizeText(t, `stages:
- stage: one
  jobs:
  - job: build
    steps:
    - script: echo 1
  - job: test
    dependsOn: build
    steps:
    - script: echo t
- stage: two
  jobs:
  - job: build
    steps:
    - script: echo 2
`)
	ids := make([]string, 0, len(got.Jobs))
	for _, job := range got.Jobs {
		ids = append(ids, job.ID)
	}
	if strings.Join(ids, ",") != "one.build,one.test,two.build" {
		t.Fatalf("ids = %v", ids)
	}
	if n := jobByID(t, got, "one.test").Needs; len(n) != 1 || n[0] != "one.build" {
		t.Fatalf("needs = %v", n)
	}
}

func TestParameterDefaultTyping(t *testing.T) {
	got := normalizeText(t, `parameters:
- name: s
  type: string
  default: '1'
- name: t
  type: string
  default: 'true'
- name: b
  type: boolean
  default: true
- name: n
  type: number
  default: 3
- name: bad
  type: boolean
  default: maybe
- name: obj
  type: object
  default:
    a: 1
steps:
- script: echo
`)
	byID := map[string]ir.Input{}
	for _, in := range got.Inputs {
		byID[in.ID] = in
	}
	if d := byID["s"].Default; d == nil || d.Type != ir.LiteralString || d.String != "1" {
		t.Fatalf("s default = %+v", d)
	}
	if d := byID["t"].Default; d == nil || d.Type != ir.LiteralString {
		t.Fatalf("t default = %+v", d)
	}
	if d := byID["b"].Default; d == nil || d.Type != ir.LiteralBool || !d.Bool {
		t.Fatalf("b default = %+v", d)
	}
	if d := byID["n"].Default; d == nil || d.Type != ir.LiteralNumber || d.Number != "3" {
		t.Fatalf("n default = %+v", d)
	}
	if byID["bad"].Default != nil || !hasLoss(t, byID["bad"].Metadata, "default") {
		t.Fatalf("bad default not dropped with loss: %+v", byID["bad"])
	}
	if byID["obj"].Default != nil || !hasLoss(t, byID["obj"].Metadata, "default") {
		t.Fatal("object default not recorded as loss")
	}
}

func TestLoggingCommandParsing(t *testing.T) {
	script := strings.Join([]string{
		`echo "##vso[task.setvariable variable=Quoted]value"`,
		`echo '##vso[task.setvariable variable=Single;isOutput=true]v1'`,
		`echo "##vso[task.setvariable VARIABLE=Upper;ISSECRET=true]s3cr3t-value"`,
		`echo "##vso[task.setvariable variable=ReadOnly;isreadonly=true]x"`,
		`# echo "##vso[task.setvariable variable=Comment]x"`,
		`##vso[task.setvariable variable=BareComment]x`,
		`echo "##vso[task.setvariable variable=]novalue"`,
		`echo "##vso[task.setsecret]hunter2-literal"`,
		`echo "##vso[task.setsecret]$(token)"`,
		`echo "##vso[task.logissue type=error;sourcepath=a.go;linenumber=3]bad"`,
	}, "\n")
	got := normalizeText(t, "steps:\n- script: |\n    "+strings.ReplaceAll(script, "\n", "\n    ")+"\n  name: s\n")
	var step ir.Step
	for _, s := range jobByID(t, got, implicitID).Steps {
		if s.ID == "s" {
			step = s
		}
	}
	ops := step.Run.Operations
	if len(ops) != 7 {
		t.Fatalf("operations = %d: %+v", len(ops), ops)
	}
	if ops[0].Name != "Quoted" || ops[0].Value.Literal.String != "value" {
		t.Fatalf("quoted value = %+v", ops[0].Value.Literal)
	}
	if ops[1].Kind != ir.StepSetOutput || ops[1].Value.Literal.String != "v1" {
		t.Fatalf("single-quoted output = %+v", ops[1])
	}
	if ops[2].Name != "Upper" || !ops[2].Secret || ops[2].Value.Ref == nil {
		t.Fatalf("secret op = %+v", ops[2])
	}
	if !hasLoss(t, step.Metadata, "isReadOnly") {
		t.Fatal("isReadOnly loss missing")
	}
	if ops[4].Kind != ir.StepMask || ops[4].Value.Ref == nil {
		t.Fatalf("setsecret literal leaked: %+v", ops[4])
	}
	if ops[5].Value.Literal.String != "$(token)" {
		t.Fatalf("setsecret macro = %+v", ops[5])
	}
	if ops[6].Level != "error" || ops[6].Location == nil || ops[6].Location.File != "a.go" {
		t.Fatalf("logissue = %+v", ops[6])
	}
	encoded, err := json.Marshal(ops)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "s3cr3t") || strings.Contains(string(encoded), "hunter2") {
		t.Fatal("secret value present in canonical IR")
	}
}

func TestCheckoutRefs(t *testing.T) {
	got := normalizeText(t, `resources:
  repositories:
  - repository: tools
    type: git
    name: proj/tools
  - repository: pinned
    type: git
    name: proj/pinned
    ref: refs/heads/rel
steps:
- checkout: self
- checkout: tools
- checkout: pinned
  submodules: recursive
`)
	steps := jobByID(t, got, implicitID).Steps
	ref := func(i int) string { return steps[i].Checkout.Ref.Literal.String }
	if steps[0].Checkout.RefSource != ir.CheckoutRefSourceVersion || ref(0) != "" {
		t.Fatalf("self checkout = %+v", steps[0].Checkout)
	}
	if macro, ok := extensionValue(t, steps[0].Metadata, "ref"); !ok || macro != "$(Build.SourceVersion)" {
		t.Fatalf("self checkout extension ref = %q, %v", macro, ok)
	}
	if ref(1) != "HEAD" || steps[1].Checkout.RefSource != ir.CheckoutRefLiteral || !hasLoss(t, steps[1].Metadata, "ref") {
		t.Fatalf("non-self checkout = %+v", steps[1].Checkout)
	}
	if ref(2) != "refs/heads/rel" || steps[2].Checkout.RefSource != ir.CheckoutRefLiteral || !steps[2].Checkout.Submodules || !hasLoss(t, steps[2].Metadata, "submodules") {
		t.Fatalf("pinned checkout = %+v", steps[2].Checkout)
	}
}

func TestCheckoutNone(t *testing.T) {
	got := normalizeText(t, "steps:\n- checkout: none\n- script: echo ok\n")
	for _, step := range jobByID(t, got, implicitID).Steps {
		if step.Checkout != nil {
			t.Fatalf("checkout: none left a checkout step: %+v", step.Checkout)
		}
	}
}

func TestCheckoutFetchDepth(t *testing.T) {
	got := normalizeText(t, `steps:
- checkout: self
- checkout: self
  fetchDepth: 0
- checkout: self
  fetchDepth: 1
- checkout: self
  fetchDepth: $(DEPTH)
`)
	var checkouts []ir.Step
	for _, step := range jobByID(t, got, implicitID).Steps {
		if step.Checkout != nil {
			checkouts = append(checkouts, step)
		}
	}
	if len(checkouts) != 4 {
		t.Fatalf("checkouts = %d, want 4", len(checkouts))
	}
	if d, set := checkouts[0].Checkout.FetchDepth, checkouts[0].Checkout.FetchDepthSet; d != 0 || set {
		t.Fatalf("absent depth = %d, %v; want 0, false", d, set)
	}
	if d, set := checkouts[1].Checkout.FetchDepth, checkouts[1].Checkout.FetchDepthSet; d != 0 || !set {
		t.Fatalf("fetchDepth 0 = %d, %v; want 0, true (full history)", d, set)
	}
	if d, set := checkouts[2].Checkout.FetchDepth, checkouts[2].Checkout.FetchDepthSet; d != 1 || !set {
		t.Fatalf("fetchDepth 1 = %d, %v; want 1, true", d, set)
	}
	if d, set := checkouts[3].Checkout.FetchDepth, checkouts[3].Checkout.FetchDepthSet; d != 0 || set {
		t.Fatalf("non-literal depth = %d, %v; want 0, false", d, set)
	}
	if !hasLoss(t, checkouts[3].Metadata, "fetchDepth") {
		t.Fatal("non-literal depth did not record a loss")
	}
}

func TestDownloadVariants(t *testing.T) {
	got := normalizeText(t, `steps:
- download: none
- download: current
- download: other
  artifact: drop
`)
	steps := jobByID(t, got, implicitID).Steps
	var downloads []ir.Step
	for _, s := range steps {
		if s.Kind == ir.StepArtifactRestore {
			downloads = append(downloads, s)
		}
	}
	if len(downloads) != 2 {
		t.Fatalf("downloads = %d", len(downloads))
	}
	if downloads[0].ArtifactRestore.Name != "" || !hasLoss(t, downloads[0].Metadata, "artifact") {
		t.Fatalf("current download = %+v", downloads[0].ArtifactRestore)
	}
	if downloads[1].ArtifactRestore.Name != "drop" || !hasLoss(t, downloads[1].Metadata, "source") {
		t.Fatalf("alias download = %+v", downloads[1].ArtifactRestore)
	}
}

func TestParallelCapAndLegs(t *testing.T) {
	got := normalizeText(t, "jobs:\n- job: big\n  strategy:\n    parallel: 100000000\n  steps:\n  - script: echo\n- job: small\n  strategy:\n    parallel: 2\n  steps:\n  - script: echo\n")
	big := jobByID(t, got, "big")
	if big.Matrix == nil || big.Matrix.Opaque == nil || len(big.Matrix.Legs) != 0 || !hasLoss(t, big.Metadata, "parallel") {
		t.Fatalf("big matrix = %+v", big.Matrix)
	}
	small := jobByID(t, got, "small")
	if len(small.Matrix.Legs) != 2 {
		t.Fatalf("legs = %d", len(small.Matrix.Legs))
	}
	if _, ok := small.Matrix.Legs[1].Values.Get("System.JobPositionInPhase"); !ok {
		t.Fatal("missing System.JobPositionInPhase")
	}
}

func TestTimeouts(t *testing.T) {
	got := normalizeText(t, "jobs:\n- job: zero\n  timeoutInMinutes: 0\n  steps:\n  - script: echo\n- job: five\n  timeoutInMinutes: 5\n  steps:\n  - script: echo\n- job: macro\n  timeoutInMinutes: $(t)\n  steps:\n  - script: echo\n")
	zero := jobByID(t, got, "zero")
	if zero.Timeout != nil || !hasLoss(t, zero.Metadata, "timeoutInMinutes") {
		t.Fatalf("zero timeout = %v", zero.Timeout)
	}
	if five := jobByID(t, got, "five"); five.Timeout == nil || five.Timeout.Minutes() != 5 {
		t.Fatalf("five timeout = %v", five.Timeout)
	}
	if m := jobByID(t, got, "macro"); m.Timeout != nil || !hasLoss(t, m.Metadata, "timeoutInMinutes") {
		t.Fatal("macro timeout not a loss")
	}
}

func TestStepIDDedupe(t *testing.T) {
	steps := []ir.Step{{ID: "a"}, {ID: "a"}, {ID: "a-2"}}
	dedupeStepIDs(steps)
	if steps[0].ID != "a" || steps[1].ID != "a-3" || steps[2].ID != "a-2" {
		t.Fatalf("ids = %s %s %s", steps[0].ID, steps[1].ID, steps[2].ID)
	}
}

func TestSecretsNeverReachIR(t *testing.T) {
	got := normalizeText(t, `variables:
  dbPassword: hunter2
  plain: value
resources:
  containers:
  - container: pg
    image: postgres
    env:
      POSTGRES_PASSWORD: example-pw
jobs:
- job: j
  container: pg
  steps:
  - task: Foo@1
    inputs:
      apiToken: tok-123
`)
	encoded, err := ir.Canonical(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"hunter2", "example-pw", "tok-123"} {
		if strings.Contains(string(encoded), leaked) {
			t.Fatalf("%q leaked into the IR", leaked)
		}
	}
	if !got.Variables[0].Secret || got.Variables[0].SecretRef == nil || got.Variables[1].Secret {
		t.Fatalf("variables = %+v", got.Variables)
	}
}

// TestFilterGlobsScrubsOriginal pins the trigger namespace of IR rule 7 at the
// construction site: filterGlobs scrubs Glob.Original itself, not only the
// whole-pipeline backstop pass. This covers include and exclude entries.
func TestFilterGlobsScrubsOriginal(t *testing.T) {
	const secret = "release/internalsecret"
	parsed := parse.Source("pipeline.yml",
		[]byte("trigger:\n  branches:\n    include: ["+secret+"]\n    exclude: ["+secret+"]\n"), diag.NewCollector())
	if parsed.Trigger == nil || parsed.Trigger.Branches == nil {
		t.Fatal("trigger filter not parsed")
	}
	c := &conv{}
	c.own = diag.NewCollector()
	c.ts = newTaintSet([]string{secret}, []string{secret})
	globs := c.filterGlobs(c.newAcc(diag.Source{File: "pipeline.yml"}), parsed.Trigger.Branches)
	if len(globs) != 2 {
		t.Fatalf("globs = %+v", globs)
	}
	for _, glob := range globs {
		if strings.Contains(glob.Original, secret) {
			t.Fatalf("declared secret left in Glob.Original: %+v", glob)
		}
		if strings.Contains(glob.Pattern, secret) {
			t.Fatalf("declared secret left in Glob.Pattern: %+v", glob)
		}
	}
}

// TestTriggerGlobSecretsNeverReachIR pins IR rule 7 for the trigger namespace:
// a declared-secret value used as a branch, path or tag pattern is scrubbed
// from both Glob.Pattern and Glob.Original, in include and exclude lists alike,
// for CI, PR and schedule triggers and for repository resource triggers.
func TestTriggerGlobSecretsNeverReachIR(t *testing.T) {
	const secret = "release/internalsecret"
	got := normalizeText(t, `variables:
- name: apiToken
  value: `+secret+`
trigger:
  branches:
    include: [`+secret+`]
    exclude: [`+secret+`]
  paths:
    include: [`+secret+`]
  tags:
    include: [`+secret+`]
pr:
  branches:
    include: [`+secret+`]
    exclude: [`+secret+`]
  paths:
    include: [`+secret+`]
schedules:
- cron: "0 0 * * *"
  branches:
    include: [`+secret+`]
    exclude: [`+secret+`]
resources:
  repositories:
  - repository: other
    type: git
    name: proj/repo
    trigger:
      branches:
        include: [`+secret+`]
        exclude: [`+secret+`]
      tags:
        include: [`+secret+`]
steps:
- script: echo hi
`)
	if out := canonical(t, got); strings.Contains(out, secret) {
		t.Fatalf("declared secret leaked into trigger globs:\n%s", out)
	}
	if len(got.Triggers) == 0 {
		t.Fatal("no triggers normalized")
	}
	seen := 0
	for _, trigger := range got.Triggers {
		globs := append(append(append([]ir.Glob{}, trigger.Filters.Branches...), trigger.Filters.Paths...), trigger.Filters.Tags...)
		for _, glob := range globs {
			seen++
			if strings.Contains(glob.Original, secret) || strings.Contains(glob.Pattern, secret) {
				t.Fatalf("declared secret in trigger glob: %+v", glob)
			}
		}
	}
	if seen != 12 {
		t.Fatalf("glob coverage = %d, want 12 branches/paths/tags across ci, pr, schedule and repository triggers", seen)
	}
}

func TestSplitOptionsAndTriggerSynthesis(t *testing.T) {
	if got := splitOptions(`--name "a b" --cpus 2`); strings.Join(got, "|") != "--name|a b|--cpus|2" {
		t.Fatalf("options = %q", got)
	}
	got := normalizeText(t, "steps:\n- script: echo\n")
	// trigger and pr are both omitted: the default CI and PR triggers are
	// synthesized and bound to provisioning rather than the YAML.
	if len(got.Triggers) != 2 {
		t.Fatalf("synthesized triggers = %+v", got.Triggers)
	}
	for _, trigger := range got.Triggers {
		if trigger.Binding.InYAML || !trigger.Binding.Synthetic {
			t.Fatalf("synthesized trigger binding = %+v", trigger.Binding)
		}
	}
}
