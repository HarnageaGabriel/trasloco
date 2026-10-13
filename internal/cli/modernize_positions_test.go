package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestModernizeWriteTextOutput pins the exact text report of --write: rewritten
// steps in the finding format, positions of the file on disk after the run, and
// one entry per step.
func TestModernizeWriteTextOutput(t *testing.T) {
	const more = "    More: https://harnageagabriel.github.io/trasloco/tasks/"
	const manualAction = "    Migrate this step manually to CmdLine@2, reviewing the documented behavior and input differences.\n"
	const guardAction = "    Replace ServiceFabricUpdateAppVersions@1 with ServiceFabricUpdateManifests@2 and review the replacement inputs before updating the pipeline.\n"
	for _, tc := range []struct {
		name, input, want string
	}{
		{
			name:  "class A",
			input: "steps:\n- task: CopyFiles@1\n",
			want:  "pipeline.yml:2:9  CopyFiles@1 -> CopyFiles@2  [rewritten]\nNo retired Azure Pipelines tasks found (1 YAML files scanned).\nRewrote 1 step(s) in 1 file(s); 0 step(s) need manual migration\n",
		},
		{
			name:  "class B lists changes",
			input: "steps:\n- task: ServiceFabricUpdateAppVersions@1\n  inputs:\n    applicationPackagePath: pkg\n",
			want: "pipeline.yml:2:9  ServiceFabricUpdateAppVersions@1 -> ServiceFabricUpdateManifests@2  [rewritten]\n" +
				"    pinned updateType: Manifest versions\n" +
				"No retired Azure Pipelines tasks found (1 YAML files scanned).\nRewrote 1 step(s) in 1 file(s); 0 step(s) need manual migration\n",
		},
		{
			name:  "positions follow the rewritten file and steps are listed once",
			input: "steps:\n- task: CopyFiles@1\n- task: ServiceFabricUpdateAppVersions@1\n  inputs:\n    applicationPackagePath: pkg\n- task: CmdLine@1\n- task: ServiceFabricUpdateAppVersions@1\n  inputs:\n    applicationPackagePath: pkg\n    updateOnlyChanged: true\n",
			want: "pipeline.yml:2:9  CopyFiles@1 -> CopyFiles@2  [rewritten]\n" +
				"pipeline.yml:3:9  ServiceFabricUpdateAppVersions@1 -> ServiceFabricUpdateManifests@2  [rewritten]\n" +
				"    pinned updateType: Manifest versions\n" +
				"pipeline.yml:7:9  CmdLine@1 -> CmdLine@2  [manual migration]\n" + manualAction + more + "cmdline-1/\n" +
				"pipeline.yml:8:9  ServiceFabricUpdateAppVersions@1 -> ServiceFabricUpdateManifests@2  [replacement with input changes]\n" + guardAction +
				"    Not rewritten: input updateOnlyChanged: guard-condition\n" + more + "servicefabricupdateappversions-1/\n" +
				"\nFound 2 retired task reference(s) in 1 file(s): 0 drop-in, 1 with input changes, 1 manual, 0 no replacement. Retired 2026-10-15, removed 2027-03-15 (pipelines fail after removal).\n" +
				"Rewrote 2 step(s) in 1 file(s); 2 step(s) need manual migration\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeTree(t, map[string]string{"pipeline.yml": tc.input})
			t.Chdir(root)
			_, output, stderr := executeModernize(t, "modernize", "--write", "--check")
			if output != tc.want {
				t.Fatalf("text output:\n%s\nwant:\n%s\nstderr: %s", output, tc.want, stderr)
			}
		})
	}
}

// TestModernizeWritePositionsJSON checks that JSON findings and diagnostics use
// the same coordinate system and no step appears twice.
func TestModernizeWritePositionsJSON(t *testing.T) {
	input := "steps:\n- task: ServiceFabricUpdateAppVersions@1\n  inputs:\n    applicationPackagePath: pkg\n- task: CmdLine@1\n"
	root := writeTree(t, map[string]string{"pipeline.yml": input})
	t.Chdir(root)
	_, output, stderr := executeModernize(t, "modernize", "--write", "--check", "--format", "json")
	var report modernizeJSONReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("%v: %s %s", err, output, stderr)
	}
	written, err := os.ReadFile(filepath.Join(root, "pipeline.yml"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(written), "\n")
	if len(report.Diagnostics) != 1 || report.Diagnostics[0].Source.Line != 2 || len(report.Findings) != 1 || report.Findings[0].Line != 6 {
		t.Fatalf("unexpected positions: %s", output)
	}
	if !strings.HasPrefix(lines[report.Findings[0].Line-1], "- task: CmdLine@1") {
		t.Fatalf("finding does not point into the written file: %q", lines)
	}
}
