// Package convert aggregates GitHub Actions emitter results into the
// conversion report (trasloco-convert-report.json).
//
// # Purity
//
// The package is pure (CONVENTIONS.md): it performs no I/O, reads no clock,
// takes no context.Context and holds no package-level mutable state. The
// caller writes the encoded report to disk. Diagnostic and disposition data
// are consumed from the emitter API of
// github.com/HarnageaGabriel/trasloco/internal/dialect/githubactions/emit;
// the report never carries secret values, only sorted secret names.
//
// # Determinism
//
// Every observable order is a byte-order sort: input files by input path,
// dispositions by node ID, secret names, diagnostic codes and disposition
// kinds are all sorted. Per-file diagnostics keep the (deterministic) order
// of the stages that produced them. The JSON encoding has a stable field
// order (struct order), two-space indentation and exactly one trailing LF.
package convert

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/githubactions/emit"
)

// SchemaVersion is the schema version of the convert report document
// (docs/spec/schema-versioning.md: every persisted report carries one).
const SchemaVersion = 1

// Exit classes the convert aggregation can produce (docs/spec/exit-codes.md).
// Exit 2 (partial) is never returned: the convert command takes exactly one
// input pipeline, so a unit is either completed or failed. It stays reserved
// for a future multi-unit convert mode.
const (
	// ExitOK reports a completed conversion with manual placeholders allowed.
	ExitOK = 0
	// ExitError reports an operational error or a failed unit.
	ExitError = 1
	// ExitPolicy reports a --fail-on threshold exceeded by the run.
	ExitPolicy = 5
)

// Report is the aggregated conversion outcome of one run. Every array is
// present (never nil) and every field is serialized so the document byte
// layout follows the struct field order.
type Report struct {
	// SchemaVersion is the report schema version (must be SchemaVersion).
	SchemaVersion int `json:"schemaVersion"`
	// Files are the per-input entries sorted by input path in byte order.
	Files []FileReport `json:"files"`
	// Diagnostics summarizes all diagnostics of the run by code, sorted by
	// code in byte order. Values are never included.
	Diagnostics []DiagnosticSummary `json:"diagnostics"`
	// RequiredSecrets is the sorted, de-duplicated list of secret NAMES the
	// emitted outputs reference. It never contains a secret value.
	RequiredSecrets []string `json:"requiredSecrets"`
	// Dispositions counts the node dispositions across all inputs by kind,
	// sorted by kind in byte order.
	Dispositions []DispositionSummary `json:"dispositions"`
	// Totals are the terminal class counts of the run.
	Totals Totals `json:"totals"`
}

// FileReport is the outcome of converting one input pipeline.
type FileReport struct {
	// Input is the repository-relative source pipeline path.
	Input string `json:"input"`
	// Output is the emitted workflow path (Files[0].Path of the emitter
	// result), or absent when no file was produced.
	Output string `json:"output,omitempty"`
	// SHA256 is the lower-case hex SHA-256 of the emitted workflow bytes, or
	// absent when no file was produced.
	SHA256 string `json:"sha256,omitempty"`
	// Strict reports that the output was suppressed under --strict.
	Strict bool `json:"strict"`
	// Blocked reports the emitter result was blocked (emit.Result.Blocked).
	Blocked bool `json:"blocked"`
	// NoFile reports that emission produced no workflow file.
	NoFile bool `json:"noFile"`
	// Dispositions are the per-node outcomes sorted by ID in byte order.
	Dispositions []DispositionEntry `json:"dispositions"`
	// Diagnostics are the full findings of this input: the pre-emit stages
	// first, then the emitter's, each in emission order. Their text fields
	// are scrubbed upstream and carry no secret values.
	Diagnostics []diag.Diagnostic `json:"diagnostics"`
}

// DispositionEntry is one IR node outcome of one file.
type DispositionEntry struct {
	// ID is the qualified IR node ID.
	ID string `json:"id"`
	// Kind is the disposition the emitter applied.
	Kind string `json:"kind"`
	// Fidelity is the grade the node contributes to the report.
	Fidelity diag.Fidelity `json:"fidelity"`
}

// DispositionSummary counts the dispositions of one kind.
type DispositionSummary struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// DiagnosticSummary counts the diagnostics of one code.
type DiagnosticSummary struct {
	// Code is the registered diagnostic code.
	Code string `json:"code"`
	// Severity is the registry severity of the code.
	Severity diag.Severity `json:"severity"`
	// Count is the number of instances the run reported.
	Count int `json:"count"`
}

// Totals counts every disposition exactly once by fidelity
// (docs/spec/emit-gha.md section 2.3): Converted + Approximated + Manual is
// the number of dispositions of the run, whatever their kind. Placeholders,
// Dropped and Carried are an additional breakdown by kind, not extra nodes.
type Totals struct {
	// Converted counts nodes of exact fidelity.
	Converted int `json:"converted"`
	// Approximated counts nodes of approximated fidelity.
	Approximated int `json:"approximated"`
	// Manual counts nodes of needs-manual or unsupported fidelity.
	Manual int `json:"manual"`
	// Placeholders counts placeholder dispositions (all manual).
	Placeholders int `json:"placeholders"`
	// Dropped counts dropped dispositions (approximated or unsupported).
	Dropped int `json:"dropped"`
	// Carried counts dispositions whose loss the reader already recorded.
	Carried int `json:"carried"`
}

// Input is one emitted pipeline.
type Input struct {
	// Path is the input pipeline path; it must be unique within a run. Byte
	// order defines the file order in the report.
	Path string
	// Output is the output workflow path. It is optional; when set it must
	// equal the emitted file path (Result.Files[0].Path), and it must be empty
	// when no file was produced.
	Output string
	// Strict reports that --strict policy suppressed this output.
	Strict bool
	// Diagnostics are the findings of the stages before emission (parse,
	// resolve, normalize, validate).
	Diagnostics []diag.Diagnostic
	// Result is the emitter outcome of this pipeline.
	Result emit.Result
}

// placeholderRequiredCode is the diagnostic whose node the mapping requires
// to become a placeholder (emit-gha.md INV-03: an absent lock entry).
const placeholderRequiredCode = "TRL-CAT-0003"

// New builds the report for the given emitted pipelines. The input order
// does not matter: entries are sorted by input path in byte order, and so
// are the disposition, secret and diagnostic orders. It fails with an error
// (returning zero values) on any conservation violation of emit-gha.md
// section 2.3 or an inconsistent input, so a silent loss can never reach the
// JSON output.
func New(inputs []Input) (Report, error) {
	entries := make([]Input, len(inputs))
	copy(entries, inputs)
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})
	report := Report{
		SchemaVersion:   SchemaVersion,
		Files:           make([]FileReport, 0, len(entries)),
		Diagnostics:     []DiagnosticSummary{},
		RequiredSecrets: []string{},
		Dispositions:    []DispositionSummary{},
	}
	var problems []error
	paths := map[string]struct{}{}                //trasloco:unordered-ok lookup only
	secretNames := map[string]struct{}{}          //trasloco:unordered-ok lookup only, emitted sorted
	diagnostics := map[string]DiagnosticSummary{} //trasloco:unordered-ok lookup only, emitted sorted
	kinds := map[string]int{}                     //trasloco:unordered-ok lookup only, emitted sorted
	for _, input := range entries {
		if _, dup := paths[input.Path]; dup {
			problems = append(problems, fmt.Errorf("input %q: duplicate input path", input.Path))
		}
		paths[input.Path] = struct{}{}
		for _, problem := range validateInput(input) {
			problems = append(problems, fmt.Errorf("input %q: %w", input.Path, problem))
		}
		report.Files = append(report.Files, buildFile(input))
		for _, name := range input.Result.RequiredSecrets {
			secretNames[name] = struct{}{}
		}
		for _, diagnostic := range allDiagnostics(input) {
			summary, seen := diagnostics[diagnostic.Code]
			if seen && summary.Severity != diagnostic.Severity {
				problems = append(problems, fmt.Errorf("input %q: diagnostic %s reported with severities %s and %s", input.Path, diagnostic.Code, summary.Severity, diagnostic.Severity))
			}
			summary.Code = diagnostic.Code
			summary.Severity = diagnostic.Severity
			summary.Count++
			diagnostics[diagnostic.Code] = summary
		}
		for _, disposition := range input.Result.Dispositions {
			kinds[string(disposition.Kind)]++
			countDisposition(disposition, &report.Totals)
		}
	}
	if len(problems) > 0 {
		return Report{}, fmt.Errorf("new convert report: %w", errors.Join(problems...))
	}
	for name := range secretNames {
		report.RequiredSecrets = append(report.RequiredSecrets, name)
	}
	sort.Strings(report.RequiredSecrets)
	for _, summary := range diagnostics {
		report.Diagnostics = append(report.Diagnostics, summary)
	}
	sort.Slice(report.Diagnostics, func(i, j int) bool { return report.Diagnostics[i].Code < report.Diagnostics[j].Code })
	for kind, count := range kinds {
		report.Dispositions = append(report.Dispositions, DispositionSummary{Kind: kind, Count: count})
	}
	sort.Slice(report.Dispositions, func(i, j int) bool { return report.Dispositions[i].Kind < report.Dispositions[j].Kind })
	return report, nil
}

// allDiagnostics returns the pre-emit then the emit diagnostics of an input.
func allDiagnostics(input Input) []diag.Diagnostic {
	all := make([]diag.Diagnostic, 0, len(input.Diagnostics)+len(input.Result.Diagnostics))
	all = append(all, input.Diagnostics...)
	return append(all, input.Result.Diagnostics...)
}

// validateInput checks one input against the conservation predicate of
// emit-gha.md section 2.3 and the consistency of its file fields.
func validateInput(input Input) []error {
	result := input.Result
	var problems []error
	if len(result.Files) > 1 {
		problems = append(problems, fmt.Errorf("%d files emitted, want at most 1", len(result.Files)))
	}
	switch {
	case len(result.Files) == 0 && input.Output != "":
		problems = append(problems, fmt.Errorf("output %q recorded but no file was emitted", input.Output))
	case len(result.Files) > 0 && input.Output != "" && input.Output != result.Files[0].Path:
		problems = append(problems, fmt.Errorf("output %q does not match emitted file %q", input.Output, result.Files[0].Path))
	}
	if input.Strict {
		if len(result.Files) > 0 {
			problems = append(problems, errors.New("strict result must not carry a file (emit-gha.md 2.2)"))
		}
		for _, disposition := range result.Dispositions {
			if disposition.Kind == emit.DispositionEmitted {
				problems = append(problems, fmt.Errorf("strict result has emitted disposition %q (emit-gha.md 2.3)", disposition.ID))
			}
		}
	}
	byID := make(map[string]emit.Disposition, len(result.Dispositions)) //trasloco:unordered-ok lookup only
	for _, disposition := range result.Dispositions {
		if disposition.ID == "" {
			problems = append(problems, errors.New("disposition without an ID"))
			continue
		}
		if _, dup := byID[disposition.ID]; dup {
			problems = append(problems, fmt.Errorf("duplicate disposition ID %q", disposition.ID))
		}
		byID[disposition.ID] = disposition
		if !validPair(disposition.Kind, disposition.Fidelity) {
			problems = append(problems, fmt.Errorf("disposition %q has the invalid kind/fidelity pair %q/%q", disposition.ID, disposition.Kind, disposition.Fidelity))
		}
	}
	elements := map[string]struct{}{} //trasloco:unordered-ok lookup only
	for _, element := range result.OutputElements {
		if _, ok := byID[element.NodeID]; !ok {
			problems = append(problems, fmt.Errorf("output element %q references node %q without a disposition", element.Path, element.NodeID))
		}
		elements[element.NodeID] = struct{}{}
	}
	diagnostics := allDiagnostics(input)
	for _, disposition := range result.Dispositions {
		switch disposition.Kind {
		case emit.DispositionEmitted:
			if len(result.Files) == 0 {
				problems = append(problems, fmt.Errorf("emitted disposition %q in a result without a file", disposition.ID))
			} else if _, ok := elements[disposition.ID]; !ok {
				problems = append(problems, fmt.Errorf("emitted disposition %q has no output element", disposition.ID))
			}
		case emit.DispositionDropped:
			if !hasDiagnosticFor(diagnostics, disposition.ID) {
				problems = append(problems, fmt.Errorf("dropped node %q has no diagnostic naming it", disposition.ID))
			}
		}
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Code != placeholderRequiredCode || diagnostic.Resource == nil {
			continue
		}
		for _, disposition := range result.Dispositions {
			if refersTo(*diagnostic.Resource, disposition.ID) && disposition.Kind != emit.DispositionPlaceholder {
				problems = append(problems, fmt.Errorf("node %q requires a placeholder (%s) but is %q", disposition.ID, placeholderRequiredCode, disposition.Kind))
			}
		}
	}
	return problems
}

// validPair reports whether the kind/fidelity pair is one of the table of
// emit-gha.md section 2.3.
func validPair(kind emit.DispositionKind, fidelity diag.Fidelity) bool {
	switch kind {
	case emit.DispositionEmitted, emit.DispositionCarried:
		return knownFidelity(fidelity)
	case emit.DispositionDefault:
		return fidelity == diag.FidelityExact
	case emit.DispositionPlaceholder:
		return fidelity == diag.FidelityNeedsManual
	case emit.DispositionDropped:
		return fidelity == diag.FidelityApproximated || fidelity == diag.FidelityUnsupported
	default:
		return false
	}
}

func knownFidelity(fidelity diag.Fidelity) bool {
	switch fidelity {
	case diag.FidelityExact, diag.FidelityApproximated, diag.FidelityNeedsManual, diag.FidelityUnsupported:
		return true
	default:
		return false
	}
}

// hasDiagnosticFor reports whether some diagnostic resource names the node.
func hasDiagnosticFor(diagnostics []diag.Diagnostic, id string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Resource != nil && refersTo(*diagnostic.Resource, id) {
			return true
		}
	}
	return false
}

// refersTo reports whether the resource is the node with the given ID, or
// the node owning the section 1.2 feature ID "<nodeKind>:<nodeID>#<feature>".
// The emitter sets Key to the qualified node ID (Kind "job", Key "job:Build");
// the Kind+":"+Key form is also accepted for unqualified keys.
func refersTo(resource diag.Resource, id string) bool {
	for _, ref := range []string{resource.Key, resource.Kind + ":" + resource.Key} {
		if ref != "" && (ref == id || strings.HasPrefix(id, ref+"#")) {
			return true
		}
	}
	return false
}

func buildFile(input Input) FileReport {
	dispositions := make([]emit.Disposition, len(input.Result.Dispositions))
	copy(dispositions, input.Result.Dispositions)
	sort.SliceStable(dispositions, func(i, j int) bool {
		return dispositions[i].ID < dispositions[j].ID
	})
	entries := make([]DispositionEntry, 0, len(dispositions))
	for _, disposition := range dispositions {
		entries = append(entries, DispositionEntry{
			ID:       disposition.ID,
			Kind:     string(disposition.Kind),
			Fidelity: disposition.Fidelity,
		})
	}
	file := FileReport{
		Input:        input.Path,
		Strict:       input.Strict,
		Blocked:      input.Result.Blocked,
		NoFile:       len(input.Result.Files) == 0,
		Dispositions: entries,
		Diagnostics:  allDiagnostics(input),
	}
	if len(input.Result.Files) > 0 {
		sum := sha256.Sum256(input.Result.Files[0].Data)
		file.Output = input.Result.Files[0].Path
		file.SHA256 = hex.EncodeToString(sum[:])
	}
	return file
}

// countDisposition adds one disposition to the totals: by fidelity for the
// class counts and by kind for the breakdown counters.
func countDisposition(disposition emit.Disposition, totals *Totals) {
	switch disposition.Fidelity {
	case diag.FidelityExact:
		totals.Converted++
	case diag.FidelityApproximated:
		totals.Approximated++
	case diag.FidelityNeedsManual, diag.FidelityUnsupported:
		totals.Manual++
	}
	switch disposition.Kind {
	case emit.DispositionPlaceholder:
		totals.Placeholders++
	case emit.DispositionDropped:
		totals.Dropped++
	case emit.DispositionCarried:
		totals.Carried++
	}
}

// Encode returns the report as JSON bytes: stable field order, two-space
// indentation, no HTML escaping and exactly one trailing LF. Nil slices are
// normalised to empty arrays so a hand-built report encodes like a built one.
func Encode(report Report) ([]byte, error) {
	normalised := report
	normalised.Files = make([]FileReport, len(report.Files))
	for index, file := range report.Files {
		if file.Dispositions == nil {
			file.Dispositions = []DispositionEntry{}
		}
		if file.Diagnostics == nil {
			file.Diagnostics = []diag.Diagnostic{}
		}
		normalised.Files[index] = file
	}
	if normalised.Diagnostics == nil {
		normalised.Diagnostics = []DiagnosticSummary{}
	}
	if normalised.RequiredSecrets == nil {
		normalised.RequiredSecrets = []string{}
	}
	if normalised.Dispositions == nil {
		normalised.Dispositions = []DispositionSummary{}
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(normalised); err != nil {
		return nil, fmt.Errorf("encode convert report: %w", err)
	}
	return buffer.Bytes(), nil
}
