package catalog

import (
	"bufio"
	"context"
	"errors"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/google/go-cmp/cmp"
)

const tasksFixture = `schemaVersion: 1
kind: tasks-generated
source:
  repo: https://github.com/microsoft/azure-pipelines-tasks
  commit: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  license: MIT
tasks:
  - name: Build
    id: 01234567-89ab-cdef-0123-456789abcdef
    versions:
      - major: 1
        minor: 2
        patch: 3
        dir: BuildV1
        inputs:
          - name: mode
            aliases: [kind]
            type: pickList
      - major: 2
        minor: 0
        patch: 0
        dir: BuildV2
        deprecated: true
      - major: 3
        minor: 0
        patch: 0
        dir: BuildV3
      - major: 4
        minor: 0
        patch: 0
        dir: BuildV4
        preview: true
`

const retirementFixture = `schemaVersion: 1
kind: task-retirement
source:
  url: https://learn.microsoft.com/example
retirementDate: 2026-10-15
removalDate: 2027-03-15
tasks:
  - task: Build@3
    name: Build
    major: 3
    replacementKind: task
    replacements: [Build@4]
`

const modernizationFixture = `schemaVersion: 1
kind: task-modernization
source:
  spec: docs/spec/modernize-ado.md
  retirementUrl: https://learn.microsoft.com/example
retirementDate: 2026-10-15
removalDate: 2027-03-15
tasks:
  - task: Build@3
    guid: 01234567-89ab-cdef-0123-456789abcdef
    replacement: [Build@4]
    action: Replace Build@3 with Build@4 and review its inputs.
    class: B
    sources: [https://learn.microsoft.com/example]
    verify: [V1]
`

const mappingFixture = `schemaVersion: 1
kind: task-mapping
task: Build@1
source: https://learn.microsoft.com/example
fidelity: exact
target:
  kind: uses
  uses: actions/example@v1
inputs:
  - from: kind
    to: mode
tests:
  - name: basic
    inputs: {kind: safe}
    expect: {mode: safe}
`

const actionsFixture = `schemaVersion: 1
kind: actions-lock
actions:
  - name: actions/checkout
    version: v4.1.2
    sha: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    runsUsing: node24
`

func fullFS() fstest.MapFS {
	return fstest.MapFS{
		"azure-pipelines/tasks.generated.yaml":  {Data: []byte(tasksFixture)},
		"azure-pipelines/tasks.retirement.yaml": {Data: []byte(retirementFixture)},
		"azure-pipelines/modernize.yaml":        {Data: []byte(modernizationFixture)},
		"azure-pipelines/mappings/build.yaml":   {Data: []byte(mappingFixture)},
		"github-actions/actions.lock.yaml":      {Data: []byte(actionsFixture)},
	}
}

func TestLoadAndQueries(t *testing.T) {
	collector := diag.NewCollector()
	got, err := Load(context.Background(), fullFS(), collector)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if diff := cmp.Diff([]int{1, 2, 3, 4}, majors(got.Tasks[0].Versions)); diff != "" {
		t.Fatalf("versions mismatch (-want +got):\n%s", diff)
	}
	for _, ref := range []string{"build", "BUILD@1", "01234567-89AB-CDEF-0123-456789ABCDEF"} {
		if _, ok := got.ResolveTask(ref); !ok {
			t.Errorf("ResolveTask(%q) not found", ref)
		}
	}
	version, ok := got.Tasks[0].Version(1)
	if !ok {
		t.Fatal("Version(1) not found")
	}
	if input, found := version.ResolveInput("KIND"); !found || input.Name != "mode" {
		t.Fatalf("ResolveInput(alias) = %#v, %v", input, found)
	}
	if major, found := got.LatestMajor("Build"); !found || major != 1 {
		t.Fatalf("LatestMajor() = %d, %v; want 1, true", major, found)
	}
	if entry, found := got.Retirement("BUILD", 3); !found || entry.Task != "Build@3" {
		t.Fatalf("Retirement() = %#v, %v", entry, found)
	}
	if entry, found := got.Modernization("bUiLd", 3); !found || entry.Class != "B" {
		t.Fatalf("Modernization() = %#v, %v", entry, found)
	}
	if entry, found := got.Mapping("build", 1); !found || entry.Fidelity != "exact" {
		t.Fatalf("Mapping() = %#v, %v", entry, found)
	}
	if entry, found := got.Action("ACTIONS/CHECKOUT"); !found || entry.Version != "v4.1.2" {
		t.Fatalf("Action() = %#v, %v", entry, found)
	}
	if len(collector.List()) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", collector.List())
	}
}

func TestLoadInvalidDocuments(t *testing.T) {
	tests := map[string]fstest.MapFS{
		"unknown field":            {"azure-pipelines/tasks.generated.yaml": {Data: []byte("schemaVersion: 1\nkind: tasks-generated\nextra: true\nsource: {repo: https://example.com, commit: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa, license: MIT}\ntasks: []\n")}},
		"bad runtime":              {"github-actions/actions.lock.yaml": {Data: []byte("schemaVersion: 1\nkind: actions-lock\nactions:\n- {name: actions/old, version: v1.0.0, sha: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa, runsUsing: node20}\n")}},
		"duplicate task":           {"azure-pipelines/tasks.generated.yaml": {Data: []byte(tasksFixture + strings.Replace(tasksFixture, "schemaVersion: 1\nkind: tasks-generated\nsource:\n  repo: https://github.com/microsoft/azure-pipelines-tasks\n  commit: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n  license: MIT\ntasks:\n", "", 1))}},
		"bad modernization class":  {"azure-pipelines/modernize.yaml": {Data: []byte(strings.Replace(modernizationFixture, "class: B", "class: E", 1))}},
		"modernization operations": {"azure-pipelines/modernize.yaml": {Data: []byte(strings.Replace(modernizationFixture, "    verify: [V1]", "    operations: []", 1))}},
	}
	for name, fsys := range tests {
		t.Run(name, func(t *testing.T) {
			collector := diag.NewCollector()
			_, err := Load(context.Background(), fsys, collector)
			if err == nil {
				t.Fatal("Load() error = nil")
			}
			var diagnosticError *diag.Error
			if !errors.As(err, &diagnosticError) {
				t.Fatalf("Load() error type = %T", err)
			}
			if len(collector.List()) == 0 {
				t.Fatal("no diagnostic collected")
			}
		})
	}
}

func TestModernizationConsistencyFailures(t *testing.T) {
	tests := map[string]fstest.MapFS{
		"missing row": {
			"azure-pipelines/tasks.generated.yaml":  {Data: []byte(tasksFixture)},
			"azure-pipelines/tasks.retirement.yaml": {Data: []byte(retirementFixture)},
		},
		"replacement mismatch": withModernization(strings.Replace(modernizationFixture, "replacement: [Build@4]", "replacement: [Build@1]", 1)),
		"missing replacement":  withModernization(strings.Replace(modernizationFixture, "replacement: [Build@4]", "replacement: [Missing@1]", 1)),
		"retirement task mismatch": func() fstest.MapFS {
			fsys := fullFS()
			fsys["azure-pipelines/tasks.retirement.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(retirementFixture, "task: Build@3", "task: Build@2", 1))}
			return fsys
		}(),
	}
	for name, fsys := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(context.Background(), fsys, nil); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}
}

func TestAzureLoadTestReplacementExemption(t *testing.T) {
	quickRetirement := strings.NewReplacer(
		"task: Build@3", "task: QuickPerfTest@1",
		"name: Build", "name: QuickPerfTest",
		"major: 3", "major: 1",
		"Build@4", "AzureLoadTest@1",
	).Replace(retirementFixture)
	quickModernization := strings.NewReplacer(
		"task: Build@3", "task: QuickPerfTest@1",
		"Build@4", "AzureLoadTest@1",
	).Replace(modernizationFixture)
	fsys := fullFS()
	fsys["azure-pipelines/tasks.retirement.yaml"] = &fstest.MapFile{Data: []byte(quickRetirement)}
	fsys["azure-pipelines/modernize.yaml"] = &fstest.MapFile{Data: []byte(quickModernization)}
	if _, err := Load(context.Background(), fsys, nil); err != nil {
		t.Fatalf("Load(QuickPerfTest@1 -> AzureLoadTest@1) error = %v", err)
	}

	otherRetirement := strings.Replace(retirementFixture, "Build@4", "AzureLoadTest@1", 1)
	otherModernization := strings.Replace(modernizationFixture, "Build@4", "AzureLoadTest@1", 1)
	fsys = fullFS()
	fsys["azure-pipelines/tasks.retirement.yaml"] = &fstest.MapFile{Data: []byte(otherRetirement)}
	fsys["azure-pipelines/modernize.yaml"] = &fstest.MapFile{Data: []byte(otherModernization)}
	if _, err := Load(context.Background(), fsys, nil); err == nil {
		t.Fatal("Load(Build@3 -> AzureLoadTest@1) error = nil")
	}
}

func withModernization(document string) fstest.MapFS {
	fsys := fullFS()
	fsys["azure-pipelines/modernize.yaml"] = &fstest.MapFile{Data: []byte(document)}
	if strings.Contains(document, "Missing@1") {
		retirement := strings.Replace(retirementFixture, "replacements: [Build@4]", "replacements: [Missing@1]", 1)
		fsys["azure-pipelines/tasks.retirement.yaml"] = &fstest.MapFile{Data: []byte(retirement)}
	}
	return fsys
}

func TestModernizationMatchesSpecificationTable(t *testing.T) {
	data, err := os.ReadFile("../../catalog/azure-pipelines/modernize.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document ModernizationFile
	if err := validateAndDecode(data, &document); err != nil {
		t.Fatal(err)
	}

	specification, err := os.ReadFile("../../docs/spec/modernize-ado.md")
	if err != nil {
		t.Fatal(err)
	}
	rowPattern := regexp.MustCompile(`^\| ([0-9]+) \| (.*?) \| (.*?) \| ([ABCD]) \| (.*?) \| (.*?) \|$`)
	refPattern := regexp.MustCompile("`([A-Za-z0-9_.-]+@[0-9]+)`")
	guidPattern := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	linkPattern := regexp.MustCompile(`(?m)^\[([^]]+)\]: (https?://\S+)$`)
	linkUsePattern := regexp.MustCompile(`\[([^]]+)\]`)
	verifyGroupPattern := regexp.MustCompile(`VERIFY: (V[0-9]+(?:, V[0-9]+)*)`)
	verifyPattern := regexp.MustCompile(`V[0-9]+`)
	type tableEntry struct {
		Task        string
		GUID        string
		Replacement []string
		Class       string
		Sources     []string
		Verify      []string
	}
	links := make(map[string]string)
	for _, match := range linkPattern.FindAllStringSubmatch(string(specification), -1) {
		links[match[1]] = match[2]
	}
	want := make([]tableEntry, 0, 73)
	seen := make(map[string]struct{}, 73)
	inSection := false
	scanner := bufio.NewScanner(strings.NewReader(string(specification)))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "## 8. The 73 retired task versions" {
			inSection = true
			continue
		}
		if inSection && strings.HasPrefix(line, "## ") {
			break
		}
		if !inSection {
			continue
		}
		match := rowPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		row, err := strconv.Atoi(match[1])
		if err != nil || row != len(want)+1 {
			t.Fatalf("specification row number = %q; want %d", match[1], len(want)+1)
		}
		taskMatch := refPattern.FindStringSubmatch(match[2])
		if taskMatch == nil {
			t.Fatalf("specification row %d has no task reference", row)
		}
		task := taskMatch[1]
		if _, duplicate := seen[strings.ToLower(task)]; duplicate {
			t.Fatalf("specification row %d duplicates %s", row, task)
		}
		seen[strings.ToLower(task)] = struct{}{}
		refs := refPattern.FindAllStringSubmatch(match[3], -1)
		replacements := make([]string, 0, len(refs))
		for _, ref := range refs {
			replacements = append(replacements, ref[1])
		}
		sort.Strings(replacements)
		linkUses := linkUsePattern.FindAllStringSubmatch(match[6], -1)
		sources := make([]string, 0, len(linkUses))
		for _, linkUse := range linkUses {
			url, ok := links[linkUse[1]]
			if !ok {
				t.Fatalf("specification row %d has unresolved link %s", row, linkUse[0])
			}
			sources = append(sources, url)
		}
		verifyGroups := verifyGroupPattern.FindAllStringSubmatch(line, -1)
		verify := make([]string, 0, len(verifyGroups))
		for _, group := range verifyGroups {
			verify = append(verify, verifyPattern.FindAllString(group[1], -1)...)
		}
		want = append(want, tableEntry{
			Task: task, GUID: guidPattern.FindString(match[2]), Replacement: replacements,
			Class: match[4], Sources: sources, Verify: verify,
		})
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(want) != 73 {
		t.Fatalf("specification row count = %d; want 73", len(want))
	}
	got := make([]tableEntry, 0, len(document.Tasks))
	counts := map[string]int{}
	for _, entry := range document.Tasks {
		replacements := append(make([]string, 0, len(entry.Replacement)), entry.Replacement...)
		sort.Strings(replacements)
		verify := append(make([]string, 0, len(entry.Verify)), entry.Verify...)
		got = append(got, tableEntry{
			Task: entry.Task, GUID: entry.GUID, Replacement: replacements,
			Class: entry.Class, Sources: entry.Sources, Verify: verify,
		})
		counts[entry.Class]++
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("specification table mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]int{"A": 2, "B": 24, "C": 27, "D": 20}, counts); diff != "" {
		t.Fatalf("class counts mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadMappingFailuresAndCancellation(t *testing.T) {
	badMapping := strings.Replace(mappingFixture, "from: kind", "from: missing", 1)
	fsys := fullFS()
	fsys["azure-pipelines/mappings/build.yaml"] = &fstest.MapFile{Data: []byte(badMapping)}
	_, err := Load(context.Background(), fsys, nil)
	if err == nil {
		t.Fatal("Load() error = nil for dangling input")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Load(ctx, fstest.MapFS{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Load(canceled) error = %v", err)
	}
}

func TestHelpersAndEmptyCatalog(t *testing.T) {
	if _, _, ok := splitRef("Build"); ok {
		t.Fatal("splitRef accepted missing major")
	}
	if _, _, ok := splitRef("Build@-1"); ok {
		t.Fatal("splitRef accepted negative major")
	}
	if RuntimeAllowed("node20") || !RuntimeAllowed("docker") {
		t.Fatal("RuntimeAllowed returned an unexpected result")
	}
	empty, err := Load(context.Background(), fstest.MapFS{}, nil)
	if err != nil {
		t.Fatalf("Load(empty) error = %v", err)
	}
	if _, ok := empty.LatestMajor("missing"); ok {
		t.Fatal("LatestMajor(missing) found a task")
	}
	if _, ok := (&Task{}).Version(9); ok {
		t.Fatal("Version(missing) found a version")
	}
}

func majors(versions []TaskVersion) []int {
	result := make([]int, len(versions))
	for i, version := range versions {
		result[i] = version.Major
	}
	return result
}
