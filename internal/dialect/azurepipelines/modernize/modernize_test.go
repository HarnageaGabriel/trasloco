package modernize

import (
	"context"
	"reflect"
	"testing"

	catalogdata "github.com/HarnageaGabriel/trasloco/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

func testCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	loaded, err := catalog.Load(context.Background(), catalogdata.FS, diag.NewCollector())
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestScanRecognitionFormsAndContexts(t *testing.T) {
	input := []byte(`parameters:
- name: taskName
  type: string
  default: CopyFiles@1
- name: extra
  type: step
  default:
    task: COPYFILES@1
steps:
- ${{ if true }}:
  - task: 5BFB729A-A7C8-4A78-A7C3-8D717BB7C13C@1
- task: acme.tools.CopyFiles@1
- task: ${{ parameters.taskName }}
`)
	findings, diagnostics := Scan("pipeline.yml", input, testCatalog(t))
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	if len(findings) != 5 {
		t.Fatalf("got %d findings: %#v", len(findings), findings)
	}
	wantCodes := []string{"TRL-MOD-0005", "TRL-MOD-0001", "TRL-MOD-0001", "TRL-MOD-0010", "TRL-MOD-0005"}
	for index, code := range wantCodes {
		if findings[index].Code != code {
			t.Errorf("finding[%d].Code = %s, want %s", index, findings[index].Code, code)
		}
	}
}

func TestScanClassesAndIgnoresLiveTasks(t *testing.T) {
	input := []byte(`steps:
- task: CopyFiles@1
- task: ArchiveFiles@1
- task: CmdLine@1
- task: AppCenterDistribute@2
- task: Maven@3
`)
	findings, _ := Scan("pipeline.yml", input, testCatalog(t))
	if len(findings) != 4 {
		t.Fatalf("got %d findings: %#v", len(findings), findings)
	}
	for index, code := range []string{"TRL-MOD-0001", "TRL-MOD-0002", "TRL-MOD-0003", "TRL-MOD-0004"} {
		if findings[index].Code != code {
			t.Errorf("finding[%d].Code = %s, want %s", index, findings[index].Code, code)
		}
	}
}

func TestScanDeterministic(t *testing.T) {
	input := []byte("steps:\n- task: copyfiles@1\n- task: CopyFiles@1.1.4\n- task: 5BFB729A-A7C8-4A78-A7C3-8D717BB7C13C@99\n")
	first, _ := Scan("pipeline.yml", input, testCatalog(t))
	second, _ := Scan("pipeline.yml", input, testCatalog(t))
	if len(first) != len(second) || len(first) != 3 {
		t.Fatalf("lengths %d and %d", len(first), len(second))
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("findings differ:\n%#v\n%#v", first, second)
	}
	if len(first[1].Diagnostics) != 1 || first[1].Diagnostics[0] != "TRL-MOD-0011" || first[2].Code != "TRL-MOD-0005" {
		t.Fatalf("supplemental diagnostics = %#v", first)
	}
}
