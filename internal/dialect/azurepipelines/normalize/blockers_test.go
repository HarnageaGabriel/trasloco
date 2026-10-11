package normalize

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

func stepByID(t *testing.T, p *ir.Pipeline, job, id string) ir.Step {
	t.Helper()
	for _, s := range jobByID(t, p, job).Steps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("step %q not found", id)
	return ir.Step{}
}

func canonical(t *testing.T, p *ir.Pipeline) string {
	t.Helper()
	b, err := ir.Canonical(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestScriptSecretLiteralsRewritten(t *testing.T) {
	script := strings.Join([]string{
		`echo "##vso[task.setvariable variable=a;issecret=true]s3cr3t"`,
		`echo "##vso[task.setsecret]hunter2"`,
		`echo "##vso[task.setvariable issecret=true]nameless-canary"`,
		`echo "##vso[task.setsecret]$(tok)"`,
		`echo "##vso[task.setvariable variable=b]x" ; echo "##vso[task.setvariable variable=c;issecret=true]second"`,
	}, "\n    ")
	got := normalizeText(t, "steps:\n- script: |\n    "+script+"\n  name: s\n")
	step := stepByID(t, got, implicitID, "s")
	out := canonical(t, got)
	for _, leak := range []string{"s3cr3t", "hunter2", "nameless-canary", "second"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q leaked", leak)
		}
	}
	if step.Run.Script != "[REDACTED]" {
		t.Fatalf("script = %q", step.Run.Script)
	}
	if !hasLoss(t, step.Metadata, "secretLiteral") || step.Fidelity != diag.FidelityNeedsManual {
		t.Fatalf("loss/fidelity = %v %v", lossProperties(t, step.Metadata), step.Fidelity)
	}
	if len(step.Run.Operations) != 5 {
		t.Fatalf("operations = %d", len(step.Run.Operations))
	}
}

func TestSecretLoggingCommandReplacesWholeScript(t *testing.T) {
	got := normalizeText(t, "steps:\n- script: |\n    echo ##vso[task.setsecret]unbounded-canary \\\n    echo keep\n  name: s\n")
	step := stepByID(t, got, implicitID, "s")
	if step.Run.Script != "[REDACTED]" || strings.Contains(canonical(t, got), "unbounded-canary") {
		t.Fatalf("script = %q", step.Run.Script)
	}
	if step.Fidelity != diag.FidelityNeedsManual || !hasLoss(t, step.Metadata, "secretLiteral") {
		t.Fatalf("loss/fidelity = %v %v", lossProperties(t, step.Metadata), step.Fidelity)
	}
}

func TestReferenceOnlySecretCommandsKeepScript(t *testing.T) {
	script := "echo \"##vso[task.setsecret]$(tok)\"\n    echo ##vso[task.setsecret]$TOKEN\n    echo ##vso[task.setsecret]${TOKEN}\n    echo ##VSO[task.setsecret]$env:TOKEN\n    echo ##vso[task.setsecret]%TOKEN%\n    echo keep"
	got := normalizeText(t, "steps:\n- script: |\n    "+script+"\n  name: s\n")
	step := stepByID(t, got, implicitID, "s")
	if !strings.Contains(step.Run.Script, "echo keep") || step.Fidelity == diag.FidelityNeedsManual || len(step.Run.Operations) != 5 {
		t.Fatalf("reference-only script = %q fidelity %v ops %d", step.Run.Script, step.Fidelity, len(step.Run.Operations))
	}
}

func TestURLPasswordScrubbedWithLoss(t *testing.T) {
	got := normalizeText(t, "steps:\n- script: psql postgres://postgres:ci-only-password@localhost:5432/app\n  name: s\n  env:\n    DB: postgres://u:ci-only-password@h/db\n")
	if strings.Contains(canonical(t, got), "ci-only-password") {
		t.Fatal("URL password leaked")
	}
	if !hasLoss(t, stepByID(t, got, implicitID, "s").Metadata, "script") {
		t.Fatal("no scrub loss")
	}
}

func TestResourceExtensionSecretsScrubbedWithLoss(t *testing.T) {
	got := normalizeText(t, `resources:
  containers:
  - container: c
    image: x
    options: "--env DSN=postgres://u:resource-canary@h/db"
    env:
      API_TOKEN: resource-env-canary
steps:
- script: echo ok
`)
	out := canonical(t, got)
	for _, leak := range []string{"resource-canary", "resource-env-canary"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q leaked", leak)
		}
	}
	if got.Fidelity != diag.FidelityNeedsManual || !hasLoss(t, got.Metadata, "secretMaterial") {
		t.Fatalf("loss/fidelity = %v %v", lossProperties(t, got.Metadata), got.Fidelity)
	}
}

func TestSecretParameterDefaultDropped(t *testing.T) {
	got := normalizeText(t, "parameters:\n- name: dbPassword\n  type: string\n  default: hunter2\n- name: cfg\n  type: object\n  default:\n    apiToken: tok-999\nsteps:\n- script: x\n")
	out := canonical(t, got)
	if strings.Contains(out, "hunter2") || strings.Contains(out, "tok-999") {
		t.Fatal("parameter secret leaked")
	}
	found := false
	for _, in := range got.Inputs {
		switch in.ID {
		case "dbPassword":
			found = true
			if in.Default != nil || !in.Required || !hasLoss(t, in.Metadata, "default") {
				t.Fatalf("input = %+v", in)
			}
		case "cfg":
			if in.Fidelity != diag.FidelityNeedsManual {
				t.Fatalf("structured secret default fidelity = %v", in.Fidelity)
			}
		}
	}
	if !found {
		t.Fatal("dbPassword input missing")
	}
}

func TestSecretNameClassification(t *testing.T) {
	for _, n := range []string{"accountKey", "AZURE_STORAGE_KEY", "pat", "sas", "sig", "dbPassword", "apiToken", "connectionString", "sasToken", "dbCredentials", "externalFeedCredentials", "pwd"} {
		if !secretName(n) {
			t.Errorf("%s should be secret", n)
		}
	}
	for _, n := range []string{"SecretsFilter", "tokenEndpoint", "PWD", "secretName", "cacheKey", "path"} {
		if secretName(n) {
			t.Errorf("%s should not be secret", n)
		}
	}
	if literalSecret("persistCredentials", "true") || literalSecret("dbPassword", "12345") || literalSecret("x", "y") {
		t.Fatal("never-secret value misclassified")
	}
	got := normalizeText(t, "variables:\n  dbCredentials: user:credential-canary\nsteps:\n- task: Foo@1\n  name: t\n  inputs:\n    SecretsFilter: '*'\n    imagePullSecret: contosoacr-secret\n    externalFeedCredentials: feed1\n    publishFeedCredentials: feed2\n    tokenEndpoint: https://x/y\n    accountKey: abcdef\n  env:\n    PWD: /home\n    pwd: lowercase-canary\n    AZURE_STORAGE_KEY: zzzz1\n")
	s := stepByID(t, got, implicitID, "t")
	for _, k := range []string{"SecretsFilter", "imagePullSecret", "externalFeedCredentials", "publishFeedCredentials", "tokenEndpoint"} {
		if v := getExpr(s.Action.With, k); v.Kind != ir.ExprLiteral {
			t.Errorf("%s became %v", k, v.Kind)
		}
	}
	if v := getExpr(s.Action.With, "accountKey"); v.Kind != ir.ExprRef || s.Fidelity != diag.FidelityNeedsManual {
		t.Errorf("accountKey = %+v fidelity %v", v, s.Fidelity)
	}
	if v := getExpr(s.Env, "PWD"); v.Kind != ir.ExprLiteral {
		t.Error("PWD must stay literal")
	}
	if v := getExpr(s.Env, "pwd"); v.Kind != ir.ExprRef {
		t.Error("lowercase pwd must be a secret ref")
	}
	if v := getExpr(s.Env, "AZURE_STORAGE_KEY"); v.Kind != ir.ExprRef {
		t.Error("AZURE_STORAGE_KEY must be a secret ref")
	}
	if len(got.Variables) != 1 || !got.Variables[0].Secret || got.Variables[0].SecretRef == nil {
		t.Fatalf("dbCredentials = %+v", got.Variables)
	}
}

func TestLoggingEscapesAndOtherCommands(t *testing.T) {
	script := `echo "##vso[task.setvariable variable=v]a%3Bb%0Ac%5Dd\"e" tail` + "\n    " +
		`echo "##vso[build.updatebuildnumber]1.2"` + "\n    " + `echo "##vso[task.complete result=Failed]"`
	got := normalizeText(t, "steps:\n- script: |\n    "+script+"\n  name: s\n")
	s := stepByID(t, got, implicitID, "s")
	if v := s.Run.Operations[0].Value.Literal.String; v != "a;b\nc]d\\\"e" {
		t.Fatalf("decoded = %q", v)
	}
	if !hasLoss(t, s.Metadata, "build.updatebuildnumber") || !hasLoss(t, s.Metadata, "task.complete") {
		t.Fatalf("losses = %v", lossProperties(t, s.Metadata))
	}
}

func TestHookGateConjoined(t *testing.T) {
	got := normalizeText(t, `jobs:
- deployment: d
  environment: $(Build.Env)
  strategy:
    runOnce:
      deploy:
        steps:
        - script: a
      on:
        success:
          steps:
          - script: ok
            name: ok
            condition: always()
          - script: plain
            name: plain
        failure:
          steps:
          - script: explicit
            name: explicit
            condition: succeeded()
          - script: literal
            name: literal
            condition: true
`)
	j := jobByID(t, got, "d")
	if j.Deployment.Environment != "$(Build.Env)" {
		t.Fatalf("env = %q", j.Deployment.Environment)
	}
	byID := map[string]ir.Step{}
	for _, s := range j.Steps {
		byID[s.ID] = s
	}
	if condFunction(byID["ok"].If) != ir.FunctionAnd {
		t.Fatalf("on.success always() not gated: %+v", byID["ok"].If)
	}
	if condFunction(byID["plain"].If) != ir.FunctionSuccess {
		t.Fatalf("plain = %+v", byID["plain"].If)
	}
	if condFunction(byID["explicit"].If) != ir.FunctionAnd {
		t.Fatalf("explicit succeeded() overwritten: %+v", byID["explicit"].If)
	}
	if condFunction(byID["literal"].If) != ir.FunctionAnd {
		t.Fatalf("literal true escaped failure gate: %+v", byID["literal"].If)
	}
}

func TestOpaqueConditionKeepsGateAsNeedsManual(t *testing.T) {
	got := normalizeText(t, "jobs:\n- deployment: d\n  environment: e\n  strategy:\n    runOnce:\n      on:\n        failure:\n          steps:\n          - script: o\n            name: o\n            condition: eq(variables.x, dependencies.a.result)\n")
	s := stepByID(t, got, "d", "o")
	if s.If.Opaque == nil {
		t.Skip("condition portable")
	}
	if !hasLoss(t, s.Metadata, "hook.condition") || s.Fidelity != diag.FidelityNeedsManual {
		t.Fatalf("step losses %v fidelity %v", lossProperties(t, s.Metadata), s.Fidelity)
	}
}

func TestMaxParallelWithoutMatrix(t *testing.T) {
	got := normalizeText(t, "jobs:\n- job: j\n  strategy:\n    maxParallel: 2\n  steps:\n  - script: x\n")
	j := jobByID(t, got, "j")
	if j.Matrix != nil || !hasLoss(t, j.Metadata, "maxParallel") {
		t.Fatalf("matrix = %+v", j.Matrix)
	}
}

func TestNoFixtureSecretInGoldens(t *testing.T) {
	literals := []string{"s3cr3t", "ci-only-password", "hunter2"}
	root := filepath.Join("..", "..", "..", "..", "testdata", "azurepipelines", "corpus")
	assign := regexp.MustCompile(`^[[:space:]]*(?:- )?["']?([A-Za-z_][A-Za-z0-9_.-]*)["']?[[:space:]]*[:=][[:space:]]*["']?([^"'[:space:]]+)["']?[[:space:]]*$`)
	// This deliberately independent fixture heuristic must not call the
	// production secret classifier that it is intended to guard.
	literalSecret := func(name, value string) bool {
		independentKeys := map[string]bool{"dbPassword": true, "POSTGRES_PASSWORD": true, "apiToken": true, "credentials": true}
		return independentKeys[name] && value != ""
	}
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".yml") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, line := range strings.FieldsFunc(string(data), func(r rune) bool { return r == 10 }) {
			if m := assign.FindStringSubmatch(strings.TrimRight(line, "")); m != nil && literalSecret(m[1], m[2]) && len(m[2]) >= 6 {
				literals = append(literals, `"`+m[2]+`"`)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Canary fixtures live beside the normalizer goldens, not in the shared
	// corpus. They are normalized here, so the check fails on a regression even
	// when a stale golden would still look clean.
	canaryFiles, err := filepath.Glob(filepath.Join("testdata", "canary", "*.yml"))
	if err != nil || len(canaryFiles) == 0 {
		t.Fatalf("canary fixtures missing: %v", err)
	}
	canaryLiteral := regexp.MustCompile(`CANARY-[A-Za-z0-9-]+`)
	for _, p := range canaryFiles {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		canaryLiterals := canaryLiteral.FindAllString(string(data), -1)
		if len(canaryLiterals) == 0 {
			t.Fatalf("%s has no CANARY- literal", p)
		}
		got := canonical(t, normalizeText(t, string(data)))
		golden := p + ".golden"
		if *update {
			if err := os.WriteFile(golden, []byte(got+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		stored, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("read canary golden (run with -update): %v", err)
		}
		for _, lit := range canaryLiterals {
			if strings.Contains(got, lit) || strings.Contains(string(stored), lit) {
				t.Errorf("%s: canary %q reached the IR", filepath.Base(p), lit)
			}
		}
		if string(stored) != got+"\n" {
			t.Errorf("%s: canary golden mismatch; run with -update", filepath.Base(p))
		}
	}
	checked := 0
	if err := filepath.WalkDir("testdata", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".ir.json") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		checked++
		for _, lit := range literals {
			if strings.Contains(string(data), lit) {
				t.Errorf("%s: %q", filepath.Base(p), lit)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no goldens checked")
	}
}

func TestIndependentSecretCanariesNeverReachIR(t *testing.T) {
	stepNode := func(t *testing.T, p *ir.Pipeline) ir.Metadata { return stepByID(t, p, implicitID, "s").Metadata }
	script := func(kind string, lines ...string) string {
		return "steps:\n- " + kind + ": |\n    " + strings.Join(lines, "\n    ") + "\n  name: s\n"
	}
	taskScript := func(task, input string, lines ...string) string {
		return "steps:\n- task: " + task + "\n  name: s\n  inputs:\n    " + input + ": |\n      " + strings.Join(lines, "\n      ") + "\n"
	}
	triggerNode := func(t *testing.T, p *ir.Pipeline) ir.Metadata {
		if len(p.Triggers) == 0 {
			t.Fatal("no trigger")
		}
		return p.Triggers[0].Metadata
	}
	jobNode := func(t *testing.T, p *ir.Pipeline) ir.Metadata { return jobByID(t, p, "j").Metadata }
	type canaryCase struct {
		name    string
		yaml    string
		canary  []string
		node    func(*testing.T, *ir.Pipeline) ir.Metadata // nil: no node assertion
		manual  bool
		lossFor string
	}
	scriptCase := func(name, yaml string, canary ...string) canaryCase {
		return canaryCase{name: name, yaml: yaml, canary: canary, node: stepNode, manual: true, lossFor: "secretLiteral"}
	}
	manualCase := func(name, yaml, canary string, node func(*testing.T, *ir.Pipeline) ir.Metadata) canaryCase {
		return canaryCase{name: name, yaml: yaml, canary: []string{canary}, node: node, manual: true}
	}
	tests := []canaryCase{
		{name: "resource extension", canary: []string{"extension-canary"}, yaml: "resources:\n  containers:\n  - container: c\n    image: x\n    options: --env DSN=postgres://u:extension-canary@h/db\nsteps:\n- script: ok\n"},
		{name: "malformed logging command", canary: []string{"logging-canary"}, yaml: "steps:\n- script: 'echo \"##vso[task.setvariable issecret=true]logging-canary\"'\n"},
		{name: "diagnostics", canary: []string{"diagnostic-canary"}, yaml: "steps:\n- script: echo postgres://u:diagnostic-canary@h/db\n"},
		{name: "raw node", canary: []string{"raw-canary"}, yaml: "steps:\n- script: ok\n  condition: eq('postgres://u:raw-canary@h/db', dependencies.a.result)\n"},
		{name: "task input", canary: []string{"input-canary"}, yaml: "steps:\n- task: Foo@1\n  inputs:\n    apiToken: input-canary\n"},
		{name: "step environment", canary: []string{"environment-canary"}, yaml: "steps:\n- script: ok\n  env:\n    password: environment-canary\n"},
		{name: "variable", canary: []string{"variable-canary"}, yaml: "variables:\n  credentials: variable-canary\nsteps:\n- script: ok\n"},

		scriptCase("two commands with semicolon", script("script", "echo ##vso[task.setsecret]semi-one-canary;echo ##vso[task.setsecret]semi-two-canary"), "semi-one-canary", "semi-two-canary"),
		scriptCase("two commands on one line", script("script", "echo ##vso[task.setsecret]space-one-canary ##vso[task.setsecret]space-two-canary"), "space-one-canary", "space-two-canary"),
		scriptCase("command after and", script("script", `echo start && echo "##vso[task.setsecret]and-canary"`), "and-canary"),
		scriptCase("escaped ampersand", script("script", `echo ##vso[task.setsecret]pa\&ss-amp-canary`), "ss-amp-canary"),
		scriptCase("escaped semicolon", script("script", `echo ##vso[task.setsecret]pa\;ss-semi-canary`), "ss-semi-canary"),
		scriptCase("cmd caret ampersand", script("script", "echo ##vso[task.setsecret]pa^&cmd-caret-canary"), "cmd-caret-canary"),
		scriptCase("powershell backtick quote", script("pwsh", "Write-Host \"##vso[task.setsecret]pa`\"tick-canary\""), "tick-canary"),
		scriptCase("powershell doubled quote", script("pwsh", `Write-Host "##vso[task.setsecret]pa""double-canary"`), "double-canary"),
		scriptCase("redirect to file", script("script", "echo ##vso[task.setsecret]redirect-canary > out.txt"), "redirect-canary"),
		scriptCase("stderr redirect", script("script", "echo ##vso[task.setsecret]fd-canary 2>&1"), "fd-canary"),
		scriptCase("subshell", script("script", "(echo ##vso[task.setsecret]subshell-canary)"), "subshell-canary"),
		scriptCase("Bash@3 task input", taskScript("Bash@3", "script", "echo ##vso[task.setsecret]bash-input-canary"), "bash-input-canary"),
		scriptCase("PowerShell@2 task input", taskScript("PowerShell@2", "script", `Write-Host "##vso[task.setvariable variable=x;issecret=true]ps-input-canary"`), "ps-input-canary"),
		scriptCase("CmdLine@2 task input", taskScript("CmdLine@2", "script", "echo ##vso[task.setsecret]cmdline-input-canary"), "cmdline-input-canary"),
		scriptCase("heredoc", script("script", "cat <<EOF", "##vso[task.setsecret]heredoc-canary", "EOF"), "heredoc-canary"),
		scriptCase("upper case command", script("script", "echo ##VSO[task.setsecret]upper-canary", `echo "##VSO[TASK.SETVARIABLE variable=v;IsSecret=True]upper-two-canary"`), "upper-canary", "upper-two-canary"),
		scriptCase("comment line", script("script", "# ##vso[task.setsecret]comment-canary"), "comment-canary"),
		manualCase("trigger glob URL credentials", "trigger:\n  branches:\n    include:\n    - https://user:trigger-canary@host/*\nsteps:\n- script: ok\n", "trigger-canary", triggerNode),
		manualCase("container options env", "jobs:\n- job: j\n  container:\n    image: x\n    options: --env DB_PASSWORD=container-canary\n  steps:\n  - script: ok\n", "container-canary", jobNode),
		manualCase("condition URL credentials", "steps:\n- script: ok\n  name: s\n  condition: contains(variables.url, 'https://u:cond-canary@h/x')\n", "cond-canary", stepNode),
		manualCase("display name URL credentials", "steps:\n- script: ok\n  name: s\n  displayName: deploy https://u:name-canary@host/x\n", "name-canary", stepNode),
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := normalizeText(t, test.yaml)
			diagnostics, err := json.Marshal(got.Diagnostics)
			if err != nil {
				t.Fatal(err)
			}
			for _, canary := range test.canary {
				if strings.Contains(canonical(t, got), canary) || strings.Contains(string(diagnostics), canary) {
					t.Fatalf("independent canary %q leaked", canary)
				}
			}
			if test.node == nil {
				return
			}
			node := test.node(t, got)
			if test.manual && node.Fidelity != diag.FidelityNeedsManual {
				t.Fatalf("node fidelity = %v, want needs-manual (losses %v)", node.Fidelity, lossProperties(t, node))
			}
			if test.lossFor != "" && !hasLoss(t, node, test.lossFor) {
				t.Fatalf("losses = %v, want %s", lossProperties(t, node), test.lossFor)
			}
			if test.lossFor == "secretLiteral" {
				step := stepByID(t, got, implicitID, "s")
				text := ""
				if step.Run != nil {
					text = step.Run.Script
				} else if v := getExpr(step.Action.With, "script"); v.Literal != nil {
					text = v.Literal.String
				}
				if text != "[REDACTED]" {
					t.Fatalf("script text = %q, want the whole text replaced", text)
				}
			}
			if test.manual && got.Fidelity != diag.FidelityNeedsManual {
				t.Fatalf("pipeline fidelity = %v, want needs-manual roll-up", got.Fidelity)
			}
		})
	}
}

func getExpr(m ir.OrderedMap[ir.Expr], key string) ir.Expr {
	v, _ := m.Get(key)
	return v
}
