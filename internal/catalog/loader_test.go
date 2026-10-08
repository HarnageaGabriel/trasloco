package catalog

import (
	"context"
	"errors"
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
		"unknown field":  {"azure-pipelines/tasks.generated.yaml": {Data: []byte("schemaVersion: 1\nkind: tasks-generated\nextra: true\nsource: {repo: https://example.com, commit: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa, license: MIT}\ntasks: []\n")}},
		"bad runtime":    {"github-actions/actions.lock.yaml": {Data: []byte("schemaVersion: 1\nkind: actions-lock\nactions:\n- {name: actions/old, version: v1.0.0, sha: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa, runsUsing: node20}\n")}},
		"duplicate task": {"azure-pipelines/tasks.generated.yaml": {Data: []byte(tasksFixture + strings.Replace(tasksFixture, "schemaVersion: 1\nkind: tasks-generated\nsource:\n  repo: https://github.com/microsoft/azure-pipelines-tasks\n  commit: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n  license: MIT\ntasks:\n", "", 1))}},
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
