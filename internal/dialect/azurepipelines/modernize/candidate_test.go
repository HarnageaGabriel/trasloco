package modernize

import "testing"

func TestIsCandidate(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"steps", "steps:\n- script: x\n", true},
		{"stages", "stages:\n- stage: a\n", true},
		{"trigger", "trigger:\n- main\n", true},
		{"template call", "template: x.yml\n", true},
		{"parameters only template", "parameters:\n- name: x\n  type: string\n", true},
		{"directive wrapped variables", "${{ if eq(1, 1) }}:\n  variables:\n    x: y\n", true},
		{"task nested", "a:\n  b:\n  - task: Foo@1\n", true},
		{"github workflow", "name: ci\non: [push]\njobs:\n  b:\n    runs-on: x\n    steps:\n    - uses: a/b@v1\n", false},
		{"github matrix task", "on: push\njobs:\n  build:\n    strategy:\n      matrix:\n        include:\n        - task: test\n", false},
		{"kubernetes multi document", "apiVersion: v1\nkind: Service\n---\napiVersion: apps/v1\nkind: Deployment\nspec:\n  template: {}\n", false},
		{"unrelated mapping", "unrelated:\n  document: true\n", false},
		{"scalar document", "just text\n", false},
		{"helm template", "name: {{ .Release.Name }}\n", false},
		{"invalid yaml without task", "a: [1, 2\nb: : :\n", false},
		{"invalid yaml with task line", "steps:\n- task: CopyFiles@1\n  inputs: [unclosed\n", true},
		{"invalid flow parameters with task", "extends: {template: t.yml, parameters: {list: [{task: CopyFiles@1, ${{ if eq(1,1) }}: {inputs: {}}}]}}\n", true},
		{"empty", "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsCandidate("f.yml", []byte(test.input)); got != test.want {
				t.Fatalf("IsCandidate = %v, want %v", got, test.want)
			}
		})
	}
}

func TestClassDSeverityIsError(t *testing.T) {
	findings, _ := Scan("p.yml", []byte("steps:\n- task: AppCenterDistribute@2\n"), testCatalog(t))
	if len(findings) != 1 || findings[0].Code != "TRL-MOD-0004" || findings[0].Severity != SeverityError || !findings[0].Actionable() {
		t.Fatalf("findings = %#v", findings)
	}
}

func TestFindingSeverityAndHelp(t *testing.T) {
	input := "steps:\n- task: acme.CopyFiles@1\n- task: CopyFiles@1.1.4\n- task: ${{ x }}\n"
	findings, _ := Scan("p.yml", []byte(input), testCatalog(t))
	if len(findings) != 3 {
		t.Fatalf("findings = %#v", findings)
	}
	if findings[0].Code != "TRL-MOD-0010" || findings[0].Actionable() || findings[0].Severity != SeverityInfo {
		t.Errorf("extension task finding = %#v", findings[0])
	}
	if !findings[1].Actionable() || findings[1].Help != HelpBaseURL+"copyfiles-1/" {
		t.Errorf("copyfiles finding = %#v", findings[1])
	}
	if !findings[2].Actionable() || findings[2].Help != "" {
		t.Errorf("dynamic finding = %#v", findings[2])
	}
	if HelpURL("nonsense") != "" || HelpURL("CopyFiles@1.1.4") != HelpBaseURL+"copyfiles-1/" {
		t.Error("HelpURL mismatch")
	}
}

func TestScanWithoutCatalogAndMultipleDocuments(t *testing.T) {
	findings, _ := Scan("p.yml", []byte("steps:\n- task: CopyFiles@1\n"), nil)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	findings, diagnostics := Scan("p.yml", []byte("steps:\n- task: CopyFiles@1\n---\nsteps:\n- task: Maven@2\n"), testCatalog(t))
	if len(findings) != 2 || len(diagnostics) == 0 {
		t.Fatalf("findings = %#v, diagnostics = %#v", findings, diagnostics)
	}
	if findings, _ := Scan("p.yml", []byte("a: [1, 2\n"), testCatalog(t)); len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	if findings, _ := Scan("p.yml", nil, testCatalog(t)); len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
}

func TestScanStringsAndDirectiveShapes(t *testing.T) {
	input := "variables:\n  t: CopyFiles@1\n  u: 5bfb729a-a7c8-4a78-a7c3-8d717bb7c13c@2\n" +
		"steps:\n- ${{ each s in parameters.list }}:\n  - ${{ s }}\n" +
		"- ${{ else }}:\n  - task: CopyFiles@1\n" +
		"- ${{ if true }}:\n  - script: x\n    env:\n      task: CopyFiles@1\n"
	findings, _ := Scan("p.yml", []byte(input), testCatalog(t))
	got := codesOf(findings)
	want := []string{"TRL-MOD-0005", "TRL-MOD-0001"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("codes = %v, want %v", got, want)
	}
}

func TestScanInvalidTopLevelSequenceFallsBackToTaskStrings(t *testing.T) {
	findings, diagnostics := Scan("p.yml", []byte("- task: CopyFiles@1\n"), testCatalog(t))
	if got := codesOf(findings); len(got) != 1 || got[0] != "TRL-MOD-0005" {
		t.Fatalf("codes = %v", got)
	}
	if !hasError(diagnostics) {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}
