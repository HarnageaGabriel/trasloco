package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

const mergeMore = "    More: https://harnageagabriel.github.io/trasloco/tasks/"

const mergeTail = "\nFound 2 retired task reference(s) in 1 file(s): 0 drop-in, 2 with input changes, 0 manual, 0 no replacement. Retired 2026-10-15, removed 2027-03-15 (pipelines fail after removal).\n" +
	"Rewrote 0 step(s) in 0 file(s); 2 step(s) need manual migration\n"

const mergeDotNet = "    Replace DotNetCoreCLI@1 with DotNetCoreCLI@2 and review the replacement inputs before updating the pipeline.\n"

var mergeCases = []struct {
	name, input, want string
	notes             [][]string
}{
	{
		name:  "inputs before task and previous same-task step also unrewritten",
		input: "steps:\n- task: DotNetCoreCLI@1\n  inputs:\n    command: $(a)\n- inputs:\n    command: build\n    publishTestResults: true\n  task: DotNetCoreCLI@1\n",
		want: "pipeline.yml:2:9  DotNetCoreCLI@1 -> DotNetCoreCLI@2  [replacement with input changes]\n" + mergeDotNet +
			"    Not rewritten: input command: non-literal-input\n" + mergeMore + "dotnetcorecli-1/\n" +
			"pipeline.yml:8:9  DotNetCoreCLI@1 -> DotNetCoreCLI@2  [replacement with input changes]\n" + mergeDotNet +
			"    Not rewritten: input publishTestResults: new-input-collision\n" + mergeMore + "dotnetcorecli-1/\n" + mergeTail,
		notes: [][]string{{"Not rewritten: input command: non-literal-input"}, {"Not rewritten: input publishTestResults: new-input-collision"}},
	},
	{
		name:  "GUID reference with collision",
		input: "steps:\n- task: DotNetCoreCLI@1\n  inputs:\n    command: $(a)\n- task: 5541a522-603c-47ad-91fc-a4b1d163081b@1\n  inputs:\n    command: build\n    publishTestResults: true\n",
		want: "pipeline.yml:2:9  DotNetCoreCLI@1 -> DotNetCoreCLI@2  [replacement with input changes]\n" + mergeDotNet +
			"    Not rewritten: input command: non-literal-input\n" + mergeMore + "dotnetcorecli-1/\n" +
			"pipeline.yml:5:9  5541a522-603c-47ad-91fc-a4b1d163081b@1 -> DotNetCoreCLI@2  [replacement with input changes]\n" + mergeDotNet +
			"    Not rewritten: input publishTestResults: new-input-collision\n" + mergeMore + "dotnetcorecli-1/\n" + mergeTail,
		notes: [][]string{{"Not rewritten: input command: non-literal-input"}, {"Not rewritten: input publishTestResults: new-input-collision"}},
	},
	{
		name:  "GUID layout refusal on the second of two steps",
		input: "steps:\n- task: Gradle@1\n  inputs: {}\n- task: 8d8eebd8-2b94-4c97-85af-839254cc6da4@1\n  inputs: {}\n",
		want: "pipeline.yml:2:9  Gradle@1 -> Gradle@4  [replacement with input changes]\n" +
			"    Replace Gradle@1 with Gradle@4 and review the replacement inputs before updating the pipeline.\n" +
			"    Not rewritten: flow-inputs\n" + mergeMore + "gradle-1/\n" +
			"pipeline.yml:4:9  8d8eebd8-2b94-4c97-85af-839254cc6da4@1 -> Gradle@4  [replacement with input changes]\n" +
			"    Replace Gradle@1 with Gradle@4 and review the replacement inputs before updating the pipeline.\n" +
			"    Not rewritten: flow-inputs\n" + mergeMore + "gradle-1/\n" + mergeTail,
		notes: [][]string{{"Not rewritten: flow-inputs"}, {"Not rewritten: flow-inputs"}},
	},
	{
		name:  "GUID class C or D step is listed once",
		input: "steps:\n- task: df857559-8715-46eb-a74e-ac98b9178aa0@1\n",
		want: "pipeline.yml:2:9  df857559-8715-46eb-a74e-ac98b9178aa0@1 -> Gradle@4  [manual migration]\n" +
			"    Migrate this step manually to Gradle@4, reviewing the documented behavior and input differences.\n" +
			mergeMore + "androidbuild-1/\n" +
			"\nFound 1 retired task reference(s) in 1 file(s): 0 drop-in, 0 with input changes, 1 manual, 0 no replacement. Retired 2026-10-15, removed 2027-03-15 (pipelines fail after removal).\n" +
			"Rewrote 0 step(s) in 0 file(s); 1 step(s) need manual migration\n",
		notes: [][]string{nil},
	},
}

func TestModernizeWriteMergeMatchesEnclosingStep(t *testing.T) {
	for _, tc := range mergeCases {
		t.Run(tc.name, func(t *testing.T) {
			root := writeTree(t, map[string]string{"pipeline.yml": tc.input})
			t.Chdir(root)
			_, output, stderr := executeModernize(t, "modernize", "--write", "--check")
			if output != tc.want {
				t.Fatalf("text output:\n%s\nwant:\n%s\nstderr: %s", output, tc.want, stderr)
			}
			_, output, stderr = executeModernize(t, "modernize", "--write", "--check", "--format", "json")
			var report modernizeJSONReport
			if err := json.Unmarshal([]byte(output), &report); err != nil {
				t.Fatalf("%v: %s %s", err, output, stderr)
			}
			if len(report.Diagnostics) != 0 || len(report.Findings) != len(tc.notes) {
				t.Fatalf("diagnostics must merge into findings: %s", output)
			}
			for i, want := range tc.notes {
				if got := strings.Join(report.Findings[i].RewriteNotes, "|"); got != strings.Join(want, "|") {
					t.Fatalf("finding %d notes %q, want %q", i, got, want)
				}
			}
		})
	}
}

func TestModernizeWriteSarifRewriteNotes(t *testing.T) {
	root := writeTree(t, map[string]string{"pipeline.yml": mergeCases[0].input})
	t.Chdir(root)
	_, output, stderr := executeModernize(t, "modernize", "--write", "--check", "--format", "sarif")
	var sarif struct {
		Runs []struct {
			Results []struct {
				Message    struct{ Text string } `json:"message"`
				Properties map[string]any        `json:"properties"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(output), &sarif); err != nil {
		t.Fatalf("%v: %s %s", err, output, stderr)
	}
	results := sarif.Runs[0].Results
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %s", output)
	}
	for i, note := range []string{"Not rewritten: input command: non-literal-input", "Not rewritten: input publishTestResults: new-input-collision"} {
		notes, _ := results[i].Properties["rewriteNotes"].([]any)
		if len(notes) != 1 || notes[0] != note || !strings.HasSuffix(results[i].Message.Text, " "+note) {
			t.Fatalf("result %d: notes %v message %q", i, notes, results[i].Message.Text)
		}
	}
}
