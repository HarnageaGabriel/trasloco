package cli

import (
	"fmt"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/modernize"
)

// mergeRewriteDiagnostics removes the rewrite diagnostics that describe a step
// the scan reports again as a finding, so every step is reported once. The
// reason a rewrite was refused is kept on the finding as a note.
func mergeRewriteDiagnostics(result *modernizeResult) {
	kept := result.diagnostics[:0:0]
	for _, d := range result.diagnostics {
		if !mergeRewriteDiagnostic(result.findings, d) {
			kept = append(kept, d)
		}
	}
	result.diagnostics = kept
}

func mergeRewriteDiagnostic(findings []modernize.Finding, d diag.Diagnostic) bool {
	if d.Source == nil {
		return false
	}
	switch d.Code {
	case "TRL-MOD-0005", "TRL-MOD-0010":
		for _, f := range findings {
			if f.Code == d.Code && f.Path == d.Source.File && f.Line == d.Source.Line && f.Column == d.Source.Col {
				return true
			}
		}
		return false
	case "TRL-MOD-0003", "TRL-MOD-0004", "TRL-MOD-0006", "TRL-MOD-0009":
	default:
		return false
	}
	index := matchingFinding(findings, d)
	if index < 0 {
		return false
	}
	if note := rewriteNote(d); note != "" {
		notes := findings[index].RewriteNotes
		for _, existing := range notes {
			if existing == note {
				return true
			}
		}
		findings[index].RewriteNotes = append(findings[index].RewriteNotes, note)
	}
	return true
}

// matchingFinding returns the finding of the step the diagnostic belongs to:
// the one at the position of the step's task reference, in the same file.
func matchingFinding(findings []modernize.Finding, d diag.Diagnostic) int {
	if d.Step == nil {
		return -1
	}
	for i, f := range findings {
		if f.Path == d.Step.File && f.Line == d.Step.Line && f.Column == d.Step.Col {
			return i
		}
	}
	return -1
}

func rewriteNote(d diag.Diagnostic) string {
	switch d.Code {
	case "TRL-MOD-0006":
		return fmt.Sprintf("Not rewritten: input %v: %v", d.Params["input"], d.Params["reason"])
	case "TRL-MOD-0009":
		return fmt.Sprintf("Not rewritten: %v", d.Params["reason"])
	}
	return ""
}

// writeTextRewrittenStep prints a rewritten step in the format of a finding.
// It reports false for diagnostics that are not rewritten steps.
func writeTextRewrittenStep(builder *strings.Builder, d diag.Diagnostic) bool {
	if d.Code != "TRL-MOD-0001" && d.Code != "TRL-MOD-0002" {
		return false
	}
	path, line, column := modernizeDiagnosticLocation(d)
	fmt.Fprintf(builder, "%s:%d:%d  %v -> %v  [rewritten]\n", path, line, column, d.Params["from"], d.Params["to"])
	for _, change := range rewriteChangeLines(d) {
		fmt.Fprintf(builder, "    %s\n", change)
	}
	return true
}

func rewriteChangeLines(d diag.Diagnostic) []string {
	var lines []string
	for _, change := range paramMaps(d.Params["changes"]) {
		input := change["input"]
		switch change["op"] {
		case "drop":
			lines = append(lines, fmt.Sprintf("dropped %v", input))
		case "rename":
			lines = append(lines, fmt.Sprintf("renamed %v -> %v", input, change["to"]))
		case "map":
			lines = append(lines, fmt.Sprintf("mapped %v: %v -> %v", input, change["from"], change["to"]))
		case "invert":
			lines = append(lines, fmt.Sprintf("renamed %v -> %v and inverted it to %v", input, change["to"], change["value"]))
		case "add":
			lines = append(lines, fmt.Sprintf("added %v: %v", input, change["value"]))
		default:
			lines = append(lines, fmt.Sprintf("pinned %v: %v", input, change["value"]))
		}
	}
	for _, review := range paramMaps(d.Params["review"]) {
		if review["kind"] == "default" {
			lines = append(lines, fmt.Sprintf("review %v: new default is %v", review["input"], review["value"]))
		} else {
			lines = append(lines, fmt.Sprintf("review %v: %v -> %v", review["input"], review["from"], review["to"]))
		}
	}
	return lines
}

func paramMaps(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		var out []map[string]any
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}
