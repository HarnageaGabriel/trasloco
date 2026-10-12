package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const retiredCopy = "steps:\n- task: CopyFiles@1\n  inputs:\n    Contents: '**'\n"

func TestModernizeInfoOnlyDoesNotFailCheck(t *testing.T) {
	root := writeTree(t, map[string]string{"azure-pipelines.yml": "steps:\n- task: acme.CopyFiles@1\n"})
	t.Chdir(root)
	code, out, stderr := executeModernize(t, "modernize", "--check")
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(out, "Notes") || !strings.Contains(out, "acme.CopyFiles@1") {
		t.Fatalf("notes missing:\n%s", out)
	}
	if !strings.Contains(out, "No retired Azure Pipelines tasks found (1 YAML files scanned).") {
		t.Fatalf("summary missing:\n%s", out)
	}
}

func TestModernizeSkipsNonPipelineYAMLAndNeverChangesExit(t *testing.T) {
	root := writeTree(t, map[string]string{
		"azure-pipelines.yml":        retiredCopy,
		".github/workflows/ci.yml":   "name: ci\non: [push]\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n    - uses: actions/checkout@v4\n",
		"k8s/all.yaml":               "apiVersion: v1\nkind: Service\n---\napiVersion: apps/v1\nkind: Deployment\nspec:\n  template:\n    spec: {}\n",
		"helm/templates/dep.yaml":    "metadata:\n  name: {{ .Release.Name }}\n",
		"broken.yml":                 "a: [1, 2\nb: : :\n",
		"not-pipeline/settings.yaml": "unrelated:\n  document: true\n",
	})
	t.Chdir(root)
	code, out, stderr := executeModernize(t, "modernize", "--check")
	if code != ExitPolicy {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if strings.Contains(stderr, "TRL-PARSE") || stderr != "" {
		t.Fatalf("non-pipeline files must be skipped silently: %q", stderr)
	}
	if !strings.Contains(out, "azure-pipelines.yml:2:9") || !strings.Contains(out, "Found 1 retired task reference(s) in 1 file(s)") {
		t.Fatalf("stdout:\n%s", out)
	}
}

func TestModernizeCandidateParseProblemIsAWarning(t *testing.T) {
	root := writeTree(t, map[string]string{
		"azure-pipelines.yml": retiredCopy,
		"bad.yml":             "steps:\n- task: CopyFiles@1\n  inputs: [unclosed\n",
	})
	t.Chdir(root)
	code, out, stderr := executeModernize(t, "modernize", "--check")
	if code != ExitPolicy {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stderr, "bad.yml") || !strings.Contains(out, "azure-pipelines.yml") {
		t.Fatalf("stdout:\n%s\nstderr:\n%s", out, stderr)
	}
	code, _, _ = executeModernize(t, "modernize", "--strict")
	if code != ExitError {
		t.Fatalf("--strict exit = %d", code)
	}
}

func TestModernizeSARIFURIsAreEncodedRelativeReferences(t *testing.T) {
	root := writeTree(t, map[string]string{"my dir/a b#c%d.yml": retiredCopy})
	t.Chdir(root)
	code, out, stderr := executeModernize(t, "modernize", "--format", "sarif")
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	var sarif struct {
		Runs []struct {
			Results []struct {
				Level     string `json:"level"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct {
							URI       string `json:"uri"`
							URIBaseID string `json:"uriBaseId"`
						} `json:"artifactLocation"`
					} `json:"physicalLocation"`
				} `json:"locations"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(out), &sarif); err != nil {
		t.Fatal(err)
	}
	location := sarif.Runs[0].Results[0].Locations[0].PhysicalLocation.ArtifactLocation
	if location.URI != "my%20dir/a%20b%23c%25d.yml" || location.URIBaseID != "%SRCROOT%" {
		t.Fatalf("location = %#v", location)
	}
	if sarif.Runs[0].Results[0].Level != "warning" {
		t.Fatalf("level = %s", sarif.Runs[0].Results[0].Level)
	}
}

func TestModernizeFileArgumentKeepsItsPathAndJSONEmptyIsArray(t *testing.T) {
	root := writeTree(t, map[string]string{"a/azure-pipelines.yml": retiredCopy, "b/azure-pipelines.yml": "steps:\n- script: ok\n"})
	t.Chdir(root)
	code, out, _ := executeModernize(t, "modernize", "a/azure-pipelines.yml", "b/azure-pipelines.yml")
	if code != ExitOK || !strings.Contains(out, "a/azure-pipelines.yml:2:9") {
		t.Fatalf("exit = %d\n%s", code, out)
	}
	code, out, _ = executeModernize(t, "modernize", "--format", "json", "b")
	if code != ExitOK || !strings.Contains(out, `"findings": []`) {
		t.Fatalf("exit = %d\n%s", code, out)
	}
}

func TestModernizeWarnsOnEmptyScanAndSkippedArguments(t *testing.T) {
	root := writeTree(t, map[string]string{"readme.txt": "x"})
	t.Chdir(root)
	code, _, stderr := executeModernize(t, "modernize", "readme.txt")
	if code != ExitOK || !strings.Contains(stderr, "readme.txt") || !strings.Contains(stderr, "0 YAML files scanned") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
}

func TestModernizeTextLayout(t *testing.T) {
	root := writeTree(t, map[string]string{"p.yml": "steps:\n- task: CopyFiles@1\n- task: ArchiveFiles@1\n- task: CmdLine@1\n- task: AppCenterDistribute@2\n"})
	t.Chdir(root)
	_, out, _ := executeModernize(t, "modernize")
	for _, want := range []string{
		"p.yml:2:9  CopyFiles@1 -> CopyFiles@2  [drop-in replacement]\n",
		"p.yml:3:9  ArchiveFiles@1 -> ArchiveFiles@2  [replacement with input changes]\n",
		"[manual migration]\n",
		"AppCenterDistribute@2 -> no replacement  [no replacement]\n",
		"    More: https://harnageagabriel.github.io/trasloco/tasks/copyfiles-1/\n",
		"Found 4 retired task reference(s) in 1 file(s): 1 drop-in, 1 with input changes, 1 manual, 1 no replacement. Retired 2026-10-15, removed 2027-03-15 (pipelines fail after removal).\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
