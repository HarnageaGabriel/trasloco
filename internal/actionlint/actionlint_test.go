// This file tests the actionlint wrapper: the findings it reports for known
// workflow defects, its determinism, its cancellation behavior and the
// hermetic configuration it promises.
package actionlint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	upstream "github.com/rhysd/actionlint"
)

// validWorkflow is a workflow actionlint accepts without a single finding.
const validWorkflow = `name: build
on:
  push:
    branches:
      - main
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: echo hello
`

// TestValidate checks the findings of workflows with known defects. The
// expected kind, line and column come from the actionlint rules "events",
// "job-needs" and "syntax-check".
func TestValidate(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		content string
		want    []Finding
	}{
		{
			name:    "valid workflow has no finding",
			content: validWorkflow,
			want:    []Finding{},
		},
		{
			name: "unknown event name",
			content: `name: build
on:
  pushd:
    branches:
      - main
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo hello
`,
			want: []Finding{{
				Line:   3,
				Column: 3,
				Kind:   "events",
				Message: "unknown Webhook event \"pushd\". see " +
					"https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#webhook-events " +
					"for list of all Webhook event names",
			}},
		},
		{
			name: "job needs an undefined job",
			content: `name: build
on: push
jobs:
  build:
    needs: missing
    runs-on: ubuntu-latest
    steps:
      - run: echo hello
`,
			want: []Finding{{
				Line:    4,
				Column:  3,
				Kind:    "job-needs",
				Message: "job \"build\" needs job \"missing\" which does not exist in this workflow",
			}},
		},
		{
			name: "broken workflow is reported per defect",
			content: `name: build
on: push
jobs:
  build:
   - broken
`,
			want: []Finding{
				{
					Line:    4,
					Column:  3,
					Kind:    "syntax-check",
					Message: "\"runs-on\" section is missing in job \"build\"",
				},
				{
					Line:    4,
					Column:  3,
					Kind:    "syntax-check",
					Message: "\"steps\" section is missing in job \"build\"",
				},
				{
					Line:    5,
					Column:  4,
					Kind:    "syntax-check",
					Message: "\"build\" job is sequence node but mapping node is expected",
				},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			findings, err := Validate(context.Background(), "build.yml", []byte(testCase.content))
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if diff := cmp.Diff(testCase.want, findings); diff != "" {
				t.Errorf("findings differ (-want +got):\n%s", diff)
			}
		})
	}
}

// TestValidateReportsParseFailuresAsFindings checks that malformed workflow
// content remains a validation result rather than becoming an API error.
func TestValidateReportsParseFailuresAsFindings(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		content []byte
	}{
		{name: "empty content", content: []byte{}},
		{name: "unparseable YAML", content: []byte("a: [")},
		{name: "invalid bytes", content: []byte{0x00, 0xff}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			findings, err := Validate(context.Background(), "build.yml", testCase.content)
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if len(findings) == 0 {
				t.Fatal("Validate returned no finding")
			}
			for _, finding := range findings {
				if finding.Kind == "syntax-check" {
					return
				}
			}
			t.Errorf("findings = %v, want at least one syntax-check finding", findings)
		})
	}
}

// TestValidateIsDeterministic checks that repeated validations of the same
// content report the same findings in the same order: the emitter compares
// them against golden files, so the sequence must not depend on the run.
func TestValidateIsDeterministic(t *testing.T) {
	content := []byte(`name: build
on:
  pushd:
    branches:
      - main
jobs:
  build:
    needs: missing
    runs-on: ubuntu-latest
    steps:
      - run: echo hello
`)
	first, err := Validate(context.Background(), "build.yml", content)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for attempt := 0; attempt < 5; attempt++ {
		again, err := Validate(context.Background(), "build.yml", content)
		if err != nil {
			t.Fatalf("Validate (attempt %d): %v", attempt, err)
		}
		if diff := cmp.Diff(first, again); diff != "" {
			t.Fatalf("attempt %d differs from the first validation (-first +again):\n%s", attempt, diff)
		}
	}
	if len(first) == 0 {
		t.Fatal("the workflow under test reported no finding")
	}
}

// TestValidateCanceledContext checks that a canceled context stops the
// validation and that the context error is returned unchanged.
func TestValidateCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	findings, err := Validate(ctx, "build.yml", []byte(validWorkflow))
	if err == nil {
		t.Fatal("a canceled context was accepted")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if errors.Unwrap(err) != nil {
		t.Errorf("error = %v, want the context error itself and not a wrapped one", err)
	}
	if findings != nil {
		t.Errorf("findings = %v, want none", findings)
	}
}

// TestValidateDoesNotRunShellcheck proves that the optional shellcheck
// integration is disabled: a shell script with an obvious error produces no
// finding, because no shellcheck process ran.
func TestValidateDoesNotRunShellcheck(t *testing.T) {
	content := []byte(`name: build
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: |
          echo "unterminated
          if [ -z $UNQUOTED ]; then echo yes; fi
`)
	findings, err := Validate(context.Background(), "build.yml", content)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, finding := range findings {
		if finding.Kind == "shellcheck" || finding.Kind == "pyflakes" {
			t.Errorf("finding %q reports the external tool %q", finding.Message, finding.Kind)
		}
	}
	if len(findings) != 0 {
		t.Errorf("findings = %v, want none", findings)
	}
}

// TestValidateIgnoresTheFileAtPath proves that the content is linted from
// memory: the file that carries the path is never read.
func TestValidateIgnoresTheFileAtPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "build.yml")
	if err := os.WriteFile(path, []byte(validWorkflow), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	findings, err := Validate(context.Background(), path, []byte(`name: build
on:
  pushd:
    branches:
      - main
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo hello
`))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	want := []Finding{{
		Line:   3,
		Column: 3,
		Kind:   "events",
		Message: "unknown Webhook event \"pushd\". see " +
			"https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#webhook-events " +
			"for list of all Webhook event names",
	}}
	if diff := cmp.Diff(want, findings); diff != "" {
		t.Errorf("findings differ (-want +got):\n%s", diff)
	}
}

// TestLinterOptionsAreHermetic checks the configuration the wrapper builds.
// The external tool integrations must stay disabled and the content must be
// linted under the pseudo-path that makes actionlint skip its project
// discovery. The assertions hold on every host, with or without shellcheck.
func TestLinterOptionsAreHermetic(t *testing.T) {
	options := linterOptions()
	for _, testCase := range []struct {
		name string
		got  string
		want string
	}{
		{name: "shellcheck executable", got: options.Shellcheck, want: ""},
		{name: "pyflakes executable", got: options.Pyflakes, want: ""},
		{name: "pseudo path", got: stdinPath, want: "<stdin>"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.got != testCase.want {
				t.Errorf("%s = %q, want %q", testCase.name, testCase.got, testCase.want)
			}
		})
	}
}

// TestLinterOptionsDoNotWriteGlobalState checks that the Color option stays
// at its zero value. Upstream's NewLinter writes the process-global
// fatih/color.NoColor for ColorOptionKindNever and for ColorOptionKindAlways,
// and the wrapper must not touch that global.
func TestLinterOptionsDoNotWriteGlobalState(t *testing.T) {
	if got := linterOptions().Color; got != upstream.ColorOptionKindAuto {
		t.Errorf("Color = %v, want %v", got, upstream.ColorOptionKindAuto)
	}
}

// TestSortFindings checks that the findings are ordered by line, column, kind
// and message, including the ties: findings that share a line, a column and a
// kind are ordered by their message.
func TestSortFindings(t *testing.T) {
	findings := []Finding{
		{Line: 3, Column: 5, Kind: "b-rule", Message: "zulu"},
		{Line: 1, Column: 2, Kind: "a-rule", Message: "bravo"},
		{Line: 3, Column: 4, Kind: "z-rule", Message: "alpha"},
		{Line: 3, Column: 5, Kind: "a-rule", Message: "charlie"},
		{Line: 3, Column: 5, Kind: "b-rule", Message: "alpha"},
		{Line: 1, Column: 2, Kind: "a-rule", Message: "alpha"},
	}
	sortFindings(findings)
	want := []Finding{
		{Line: 1, Column: 2, Kind: "a-rule", Message: "alpha"},
		{Line: 1, Column: 2, Kind: "a-rule", Message: "bravo"},
		{Line: 3, Column: 4, Kind: "z-rule", Message: "alpha"},
		{Line: 3, Column: 5, Kind: "a-rule", Message: "charlie"},
		{Line: 3, Column: 5, Kind: "b-rule", Message: "alpha"},
		{Line: 3, Column: 5, Kind: "b-rule", Message: "zulu"},
	}
	if diff := cmp.Diff(want, findings); diff != "" {
		t.Errorf("findings differ (-want +got):\n%s", diff)
	}
}
