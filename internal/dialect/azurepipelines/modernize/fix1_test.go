package modernize

import (
	"strings"
	"testing"
)

func codesOf(findings []Finding) []string {
	codes := make([]string, 0, len(findings))
	for _, finding := range findings {
		codes = append(codes, finding.Code)
	}
	return codes
}

func TestScanStepWithDirectiveKeysIsStillAStep(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"if directive inside step", "steps:\n- task: CopyFiles@1\n  ${{ if eq(variables.x, 1) }}:\n    condition: always()\n", []string{"TRL-MOD-0001"}},
		{"if directive with inputs", "steps:\n- task: CopyFiles@1\n  displayName: x\n  ${{ if eq(variables.x, 1) }}:\n    inputs:\n      Contents: '**'\n", []string{"TRL-MOD-0001"}},
		{"insert directive", "steps:\n- task: CopyFiles@1\n  ${{ insert }}: ${{ parameters.props }}\n", []string{"TRL-MOD-0001"}},
		{"each directive", "steps:\n- task: CopyFiles@1\n  ${{ each p in parameters.list }}:\n    ${{ p.key }}: ${{ p.value }}\n", []string{"TRL-MOD-0001"}},
		{"unknown sibling key", "steps:\n- task: CopyFiles@1\n  futureKey: 1\n", []string{"TRL-MOD-0001"}},
		{"task key produced by directive", "steps:\n- displayName: x\n  ${{ parameters.key }}: CopyFiles@1\n", []string{"TRL-MOD-0005"}},
		{"task under directive body", "steps:\n- displayName: x\n  ${{ if true }}:\n    task: CopyFiles@1\n", []string{"TRL-MOD-0001"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			findings, _ := Scan("p.yml", []byte(test.input), testCatalog(t))
			got := codesOf(findings)
			if strings.Join(got, ",") != strings.Join(test.want, ",") {
				t.Fatalf("codes = %v, want %v", got, test.want)
			}
		})
	}
}

func TestScanGUIDResolvesGUIDAndMajor(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"live CopyFiles@2 by GUID", "steps:\n- task: 5bfb729a-a7c8-4a78-a7c3-8d717bb7c13c@2\n", nil},
		{"live NuGetCommand@2 by GUID", "steps:\n- task: 333b11bd-d341-40d9-afcf-b32d5ce6f23b@2\n", nil},
		{"unknown major of known GUID", "steps:\n- task: 333b11bd-d341-40d9-afcf-b32d5ce6f23b@9\n", nil},
		{"retired NuGetRestore@1 by GUID", "steps:\n- task: 333b11bd-d341-40d9-afcf-b32d5ce6f23b@1\n", []string{"TRL-MOD-0002"}},
		{"retired CopyFiles@1 by GUID", "steps:\n- task: 5bfb729a-a7c8-4a78-a7c3-8d717bb7c13c@1\n", []string{"TRL-MOD-0001"}},
		{"unknown GUID", "steps:\n- task: 00000000-0000-0000-0000-000000000000@1\n", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			findings, _ := Scan("p.yml", []byte(test.input), testCatalog(t))
			got := codesOf(findings)
			if strings.Join(got, ",") != strings.Join(test.want, ",") {
				t.Fatalf("codes = %v, want %v", got, test.want)
			}
		})
	}
}

func TestScanNeverReportsTheRecommendedReplacement(t *testing.T) {
	cat := testCatalog(t)
	for _, entry := range cat.Modernizations {
		for _, replacement := range entry.Replacement {
			name, major, _, ok := splitReference(replacement)
			if !ok {
				continue
			}
			if _, retired := cat.Modernization(name, major); retired {
				continue
			}
			findings, _ := Scan("p.yml", []byte("steps:\n- task: "+replacement+"\n"), cat)
			if len(findings) != 0 {
				t.Errorf("replacement %s of %s reported: %v", replacement, entry.Task, codesOf(findings))
			}
		}
	}
}

func TestScanParameterStepContentsAreNotStrings(t *testing.T) {
	input := "parameters:\n- name: s\n  type: step\n  default:\n    task: Foo@1\n    displayName: CopyFiles@1\n    inputs:\n      note: CopyFiles@1\n"
	findings, _ := Scan("p.yml", []byte(input), testCatalog(t))
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
}

func TestSplitReferenceRejectsSignedMajors(t *testing.T) {
	for _, raw := range []string{"CopyFiles@+1", "CopyFiles@-1", "CopyFiles@1.+2", "CopyFiles@ 1"} {
		if _, _, _, ok := splitReference(raw); ok {
			t.Errorf("%q accepted", raw)
		}
	}
	findings, _ := Scan("p.yml", []byte("steps:\n- task: CopyFiles@+1\n"), testCatalog(t))
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
}
