package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func executeModernize(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Execute(context.Background(), args, IO{Out: &out, Err: &errOut, Getenv: func(string) (string, bool) { return "", false }})
	return code, out.String(), errOut.String()
}

func TestModernizeExitCodesAndDeterminism(t *testing.T) {
	path := "testdata/modernize/pipeline.yml"
	code, first, stderr := executeModernize(t, "modernize", path)
	if code != ExitOK {
		t.Fatalf("default exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(first, "COPYFILES@1") || !strings.Contains(first, "replacement=CopyFiles@2") {
		t.Fatalf("text output = %s", first)
	}
	code, second, stderr := executeModernize(t, "modernize", "--check", path)
	if code != ExitPolicy {
		t.Fatalf("--check exit = %d, stderr = %s", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("--check stderr = %q", stderr)
	}
	if first != second {
		t.Fatal("report changed between identical runs")
	}
}

func TestModernizeJSONAndSARIFShape(t *testing.T) {
	path := "testdata/modernize/pipeline.yml"
	code, output, stderr := executeModernize(t, "modernize", "--format", "json", path)
	if code != ExitOK {
		t.Fatalf("json exit = %d, stderr = %s", code, stderr)
	}
	var report struct {
		Findings []map[string]any `json:"findings"`
	}
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 5 {
		t.Fatalf("findings = %d", len(report.Findings))
	}

	code, output, stderr = executeModernize(t, "modernize", "--format", "sarif", path)
	if code != ExitOK {
		t.Fatalf("sarif exit = %d, stderr = %s", code, stderr)
	}
	var sarif struct {
		Version string `json:"version"`
		Runs    []struct {
			Tool struct {
				Driver struct {
					Name  string `json:"name"`
					Rules []struct {
						ID string `json:"id"`
					} `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				RuleID    string `json:"ruleId"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
						Region struct {
							StartLine   int `json:"startLine"`
							StartColumn int `json:"startColumn"`
						} `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(output), &sarif); err != nil {
		t.Fatal(err)
	}
	if sarif.Version != "2.1.0" || len(sarif.Runs) != 1 || len(sarif.Runs[0].Results) != 5 || len(sarif.Runs[0].Tool.Driver.Rules) != 11 || sarif.Runs[0].Tool.Driver.Name == "" {
		t.Fatalf("invalid SARIF shape: %#v", sarif)
	}
	for _, result := range sarif.Runs[0].Results {
		if result.RuleID == "" || len(result.Locations) != 1 || result.Locations[0].PhysicalLocation.ArtifactLocation.URI == "" || result.Locations[0].PhysicalLocation.Region.StartLine == 0 || result.Locations[0].PhysicalLocation.Region.StartColumn == 0 {
			t.Fatalf("invalid SARIF result: %#v", result)
		}
	}
}

func TestModernizeContinuesAfterNonPipelineYAML(t *testing.T) {
	code, output, stderr := executeModernize(t, "modernize", "testdata/modernize")
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(output, "AppCenterDistribute@2") || !strings.Contains(stderr, "TRL-PARSE-") {
		t.Fatalf("stdout=%s\nstderr=%s", output, stderr)
	}
}
