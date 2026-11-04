// Package actionlint validates GitHub Actions workflow content with a pinned
// copy of actionlint (github.com/rhysd/actionlint v1.7.12).
//
// The package owns the only import of the upstream module. actionlint has no
// semver guarantee, so CONVENTIONS.md requires a pinned wrapper around it;
// callers of this package see only Finding values and never an upstream type.
//
// Validation is hermetic. The workflow bytes are linted from memory, no file is
// read, no external command runs and no configuration file is looked up.
// actionlint's optional shellcheck and pyflakes integrations stay disabled, so
// the result never depends on the tools installed on the host, and the
// linter's own report goes to io.Discard. Local actions and reusable workflows
// referenced with uses: ./... are not resolved or validated. Runner labels are
// checked only against actionlint's built-in hosted-runner list; the wrapper
// does not load a configuration file with custom labels.
//
// The wrapper writes no process-global state. Upstream's NewLinter mutates the
// global fatih/color.NoColor when its Color option is set, so the wrapper
// leaves that option unset and keeps the discarded report unobservable.
//
// The package is deliberately not one of the pure packages of CONVENTIONS.md.
// actionlint itself imports os and os/exec for its optional external-tool
// integrations and for its project discovery. The wrapper performs no I/O of
// its own, but the import graph of the dependency does, so the package stays
// out of the depguard pure-package rule.
package actionlint

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	upstream "github.com/rhysd/actionlint"
)

// stdinPath is the path actionlint uses for content that does not come from a
// file. Lint skips its project discovery - which stats the path and then
// searches the surrounding repository for a project and its actionlint.yaml -
// for exactly this value, so the wrapper never touches the filesystem.
const stdinPath = "<stdin>"

// Finding is one actionlint finding about a workflow.
//
// Line and Column are the 1-based position of the finding in the validated
// content, Kind is the name of the actionlint rule that reported it (for
// example "events", "job-needs" or "syntax-check") and Message is the rule
// message without the position prefix.
type Finding struct {
	Line    int
	Column  int
	Kind    string
	Message string
}

// Validate lints the workflow content and returns its findings sorted by
// line, column, kind and message.
//
// Callers must treat any returned finding as a validation failure. Workflow
// parse failures, including empty or malformed content, are returned as
// syntax-check findings rather than errors.
//
// path names the workflow for error messages only. The content is linted from
// memory, so actionlint resolves no project from it and the result is the
// same on every operating system.
//
// Validate checks ctx only on entry and returns its error unchanged when it is
// already done. Once linting begins it is not interruptible. Failures to create
// or run the linter are wrapped with %w. A workflow without findings yields an
// empty slice and no error.
func Validate(ctx context.Context, path string, content []byte) ([]Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	linter, err := upstream.NewLinter(io.Discard, linterOptions())
	if err != nil {
		return nil, fmt.Errorf("actionlint: cannot create the linter for %s: %w", path, err)
	}
	lintErrors, err := linter.Lint(stdinPath, content, nil)
	if err != nil {
		return nil, fmt.Errorf("actionlint: cannot lint %s: %w", path, err)
	}
	findings := make([]Finding, 0, len(lintErrors))
	for _, lintError := range lintErrors {
		findings = append(findings, Finding{
			Line:    lintError.Line,
			Column:  lintError.Column,
			Kind:    lintError.Kind,
			Message: lintError.Message,
		})
	}
	sortFindings(findings)
	return findings, nil
}

// linterOptions returns the options the linter is created with.
//
// The options keep the validation hermetic and side-effect free:
//
//   - Shellcheck and Pyflakes are empty, which disables the rules that run
//     those external commands. The findings would otherwise depend on the
//     tools installed on the host.
//   - ConfigFile is empty, so no configuration file is read and the result
//     does not depend on the repository the caller happens to run in.
//   - WorkingDir is fixed, so the linter does not ask the process for its
//     current directory.
//   - Color is left at its zero value on purpose. Upstream's NewLinter writes
//     the process-global fatih/color.NoColor for ColorOptionKindNever and for
//     ColorOptionKindAlways, which races with every other goroutine and every
//     other package that colors its output. The linter's report goes to
//     io.Discard, so its color is unobservable and the wrapper writes no
//     global state at all.
func linterOptions() *upstream.LinterOptions {
	return &upstream.LinterOptions{
		Shellcheck: "",
		Pyflakes:   "",
		ConfigFile: "",
		WorkingDir: ".",
	}
}

// sortFindings sorts the findings by line, column, kind and message, so that
// two validations of the same content report the same sequence.
func sortFindings(findings []Finding) {
	slices.SortFunc(findings, compareFindings)
}

// compareFindings orders findings by line, column, kind and message, so that
// two validations of the same content report the same sequence.
func compareFindings(left, right Finding) int {
	if left.Line != right.Line {
		return left.Line - right.Line
	}
	if left.Column != right.Column {
		return left.Column - right.Column
	}
	if order := strings.Compare(left.Kind, right.Kind); order != 0 {
		return order
	}
	return strings.Compare(left.Message, right.Message)
}
